package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"hyrio.xyz/transfergo/core"
)

func main() {
	app := newAppContext()
	if err := app.Run(os.Args[1:]); err != nil {
		core.LogE("%v\n", err)
		if shouldPrintUsage(err) {
			app.commands.PrintUsage()
		}
		os.Exit(1)
	}
}

// Run 根据首个命令行参数分发 encode、decode 或帮助命令。
func (app appContext) Run(args []string) error {
	if len(args) == 0 {
		app.commands.PrintUsage()
		return nil
	}

	switch args[0] {
	case "encode":
		return app.runEncode(args[1:])
	case "decode":
		return app.runDecode(args[1:])
	case "help", "-h", "--help":
		app.commands.PrintUsage()
		return nil
	default:
		return withUsage(fmt.Errorf("无法识别命令 %q，请使用 encode、decode 或 help", args[0]))
	}
}

type appContext struct {
	commands core.CommandContext
	protocol core.ProtocolContext
	video    core.VideoContext
}

// usageError 标记需要在错误信息之后补充命令用法的参数错误。
type usageError struct {
	cause error
}

// Error 返回底层参数错误文本。
func (err usageError) Error() string {
	return err.cause.Error()
}

// Unwrap 保留底层错误链，便于 errors.Is 和 errors.As 继续识别原始错误。
func (err usageError) Unwrap() error {
	return err.cause
}

// withUsage 把参数错误标记为需要显示命令用法。
func withUsage(err error) error {
	return usageError{cause: err}
}

// shouldPrintUsage 判断错误处理阶段是否需要补充命令用法。
func shouldPrintUsage(err error) bool {
	var target usageError
	return errors.As(err, &target)
}

// newAppContext 创建命令行应用所需的命令、协议和视频处理上下文。
func newAppContext() appContext {
	return appContext{
		commands: core.NewCommandContext(),
		protocol: core.NewProtocolContext(),
		video:    core.NewVideoContext(),
	}
}

// region Encode

// runEncode 解析编码参数，并依次完成文件读取、协议分帧、二维码渲染和视频生成。
func (app appContext) runEncode(args []string) error {
	opt, err := app.commands.ParseEncodeOptions(args)
	if err != nil {
		return withUsage(core.E("解析编码参数失败", err))
	}
	if !opt.Replace {
		if _, err := os.Lstat(opt.Output); err == nil {
			return fmt.Errorf("输出文件 %q 已存在，如需覆盖请添加 -replace 参数", opt.Output)
		} else if !errors.Is(err, os.ErrNotExist) {
			return core.E("检查输出文件失败", err)
		}
	}

	core.LogI("编码配置：输入文件=%q，输出视频=%q，帧率=%g，画面尺寸=%d×%d，二维码网格=%d×%d，单个二维码尺寸=%d 像素，二维码纠错等级=%s，单块数据=%d 字节，CRF=%d，并行处理=%s，加密=%s，覆盖已有文件=%s\n",
		opt.Input, opt.Output, opt.FPS, opt.ImageWidth, opt.ImageHeight, opt.Rows, opt.Cols, opt.QRSize, opt.QRErrorCorrection, opt.ChunkSize, opt.CRF, boolText(opt.Parallel), boolText(opt.Password != ""), boolText(opt.Replace))
	core.LogI("正在读取输入文件：%s\n", opt.Input)
	input, err := os.ReadFile(opt.Input)
	if err != nil {
		return core.E("读取输入文件失败", err)
	}
	core.LogI("输入文件读取完成：共 %d 字节\n", len(input))

	core.LogI("正在把文件拆分为传输数据帧……\n")
	payloads, err := app.protocol.EncodeFile(input, filepath.Base(opt.Input), opt.Password, opt.ChunkSize, opt.Rows, opt.Cols)
	if err != nil {
		return core.E("生成传输数据帧失败", err)
	}
	core.LogI("传输数据帧生成完成：需要 %d 个二维码，预计生成 %d 帧视频画面\n", len(payloads), renderedFrameCount(len(payloads), opt.Rows, opt.Cols))

	framesDir, cleanup, err := app.video.PrepareFramesDir(opt.FramesDir, "transfergo-encode-", opt.Keep)
	if err != nil {
		return core.E("准备编码帧目录失败", err)
	}
	defer cleanup()

	imageCount := renderedFrameCount(len(payloads), opt.Rows, opt.Cols)
	core.LogI("正在生成二维码图片：并行处理=%s，工作协程数=%d\n", boolText(opt.Parallel), parallelWorkerCount(opt.Parallel, imageCount))
	if err := app.writePayloadImages(payloads, framesDir, opt); err != nil {
		return core.E("生成二维码图片失败", err)
	}
	core.LogI("二维码图片生成完成：共 %d 张 PNG 图片，目录为 %s\n", imageCount, framesDir)

	core.LogI("正在使用 ffmpeg 生成视频……\n")
	if err := app.video.EncodeVideo(opt.Ffmpeg, framesDir, opt.Output, opt.FPS, opt.CRF, opt.Replace); err != nil {
		return core.E("使用 ffmpeg 生成视频失败", err)
	}
	if info, statErr := os.Stat(opt.Output); statErr == nil {
		core.LogI("视频生成完成：文件大小为 %d 字节\n", info.Size())
	}

	core.LogI("编码成功：%s → %s\n", opt.Input, opt.Output)
	core.LogI("编码汇总：二维码总数=%d，视频画面数=%d，单块数据=%d 字节，二维码网格=%d×%d，帧率=%s，加密=%s，等效传输速率=%.2f KiB/s\n",
		len(payloads), imageCount, opt.ChunkSize, opt.Rows, opt.Cols, fmt.Sprintf("%g", opt.FPS), boolText(opt.Password != ""), effectiveTransferRate(len(input), imageCount, opt.FPS))
	if opt.Keep {
		core.LogI("已保留编码过程中生成的 PNG 图片：%s\n", framesDir)
	}
	return nil
}

// writePayloadImages 按网格容量对协议载荷分组，并为每组生成一张二维码图片。
func (app appContext) writePayloadImages(payloads [][]byte, framesDir string, opt core.EncodeOptions) error {
	slots := opt.Rows * opt.Cols
	total := renderedFrameCount(len(payloads), opt.Rows, opt.Cols)
	printProgress := app.commands.NewProgressPrinter("二维码图片生成进度")
	jobs := make([]payloadImageJob, 0, total)

	for start, imageIndex := 0, 1; start < len(payloads); imageIndex++ {
		// 使用剩余长度判断是否还能装满一帧，避免 start 与 slots 相加时发生整数溢出。
		end := len(payloads)
		if slots < len(payloads)-start {
			end = start + slots
		}

		jobs = append(jobs, payloadImageJob{index: imageIndex, payloads: payloads[start:end]})
		start = end
	}

	render := func(job payloadImageJob) error {
		path := filepath.Join(framesDir, fmt.Sprintf("frame_%06d.png", job.index))
		if err := core.EncodeMultiByteArraysToSinglePng(job.payloads, path, opt.QRSize, opt.Rows, opt.Cols, opt.ImageWidth, opt.ImageHeight, opt.QRErrorCorrection); err != nil {
			return fmt.Errorf("生成第 %d 张二维码图片失败：%w", job.index, err)
		}
		return nil
	}

	workerCount := parallelWorkerCount(opt.Parallel, len(jobs))
	if workerCount <= 1 {
		for done, job := range jobs {
			if err := render(job); err != nil {
				return err
			}
			printProgress(done+1, total)
		}
		return nil
	}

	jobChannel := make(chan payloadImageJob, len(jobs))
	results := make(chan error, workerCount)
	for range workerCount {
		go func() {
			for job := range jobChannel {
				results <- render(job)
			}
		}()
	}
	for _, job := range jobs {
		jobChannel <- job
	}
	close(jobChannel)

	var firstErr error
	for done := 1; done <= len(jobs); done++ {
		if err := <-results; err != nil && firstErr == nil {
			firstErr = err
		}
		printProgress(done, total)
	}
	return firstErr
}

// payloadImageJob 保存一张输出图片对应的帧序号和协议载荷。
type payloadImageJob struct {
	index    int
	payloads [][]byte
}

// renderedFrameCount 根据载荷数量和网格容量计算需要生成的视频帧数。
func renderedFrameCount(payloadCount int, rows int, cols int) int {
	if payloadCount <= 0 || rows <= 0 || cols <= 0 {
		return 0
	}
	maxInt := int(^uint(0) >> 1)
	if rows > maxInt/cols {
		return 0
	}
	slots := rows * cols
	// 先减一再做整数除法，避免 payloadCount 加上 slots 时发生溢出。
	return 1 + (payloadCount-1)/slots
}

// effectiveTransferRate 根据原始文件大小和视频播放时长计算等效传输速率。
func effectiveTransferRate(fileSize int, frameCount int, fps float64) float64 {
	if fileSize <= 0 || frameCount <= 0 || fps <= 0 {
		return 0
	}
	return float64(fileSize) * fps / float64(frameCount) / 1024
}

// endregion

// region Decode

// runDecode 解析解码参数，并依次完成视频抽帧、二维码识别、协议还原和文件写入。
func (app appContext) runDecode(args []string) error {
	opt, err := app.commands.ParseDecodeOptions(args)
	if err != nil {
		return withUsage(core.E("解析解码参数失败", err))
	}
	requestedOutput := opt.Output
	if requestedOutput == "" {
		requestedOutput = "<使用视频清单中的原文件名>"
	}
	core.LogI("解码配置：输入视频=%q，输出文件=%q，抽帧率=%g，图片最长边限制=%d 像素，手动二维码网格=%d×%d，并行处理=%s，已提供密码=%s，覆盖已有文件=%s\n",
		opt.Input, requestedOutput, opt.SampleFPS, opt.MaxFrameSize, opt.Rows, opt.Cols, boolText(opt.Parallel), boolText(opt.Password != ""), boolText(opt.Replace))

	framesDir, cleanup, err := app.video.PrepareFramesDir(opt.FramesDir, "transfergo-decode-", opt.Keep)
	if err != nil {
		return core.E("准备解码帧目录失败", err)
	}
	defer cleanup()

	core.LogI("正在使用 ffmpeg 从视频中抽取图片……\n")
	if err := app.video.ExtractFrames(opt.Ffmpeg, opt.Input, framesDir, opt.SampleFPS); err != nil {
		return core.E("使用 ffmpeg 抽取视频图片失败", err)
	}

	paths, err := sortedFramePaths(framesDir)
	if err != nil {
		return core.E("读取抽帧图片列表失败", err)
	}
	if len(paths) == 0 {
		return errors.New("没有找到 ffmpeg 抽取的 PNG 图片")
	}
	core.LogI("视频抽帧完成：共生成 %d 张 PNG 图片，目录为 %s\n", len(paths), framesDir)

	workers := parallelWorkerCount(opt.Parallel, len(paths))
	core.LogI("正在识别图片中的二维码：并行处理=%s，工作协程数=%d，图片最长边限制=%d 像素\n", boolText(opt.Parallel), workers, opt.MaxFrameSize)
	var payloads [][]byte
	var stats payloadCollectionStats
	if opt.Rows > 0 {
		payloads, stats, err = collectPayloadsFromImagesWithGrid(paths, opt.MaxFrameSize, opt.Rows, opt.Cols, opt.Parallel, app.commands.NewProgressPrinter("二维码图片识别进度"))
		if err == nil {
			manifest, manifestErr := core.DecodeManifest(payloads, opt.Password)
			if manifestErr == nil && manifest.Rows() > 0 && manifest.Cols() > 0 && (manifest.Rows() != opt.Rows || manifest.Cols() != opt.Cols) {
				core.LogW("手动二维码网格 %d×%d 与文件清单中的 %d×%d 不一致，将继续使用手动参数\n", opt.Rows, opt.Cols, manifest.Rows(), manifest.Cols())
			}
		}
	} else {
		payloads, stats, err = collectPayloadsFromImages(paths, opt.MaxFrameSize, opt.Parallel, app.commands.NewProgressPrinter("二维码图片识别进度"))
	}
	if err == nil && opt.Rows == 0 {
		manifest, manifestErr := core.DecodeManifest(payloads, opt.Password)
		if manifestErr == nil && manifest.Rows() > 0 && manifest.Cols() > 0 {
			payloads, stats, err = collectPayloadsFromImagesWithGrid(paths, opt.MaxFrameSize, manifest.Rows(), manifest.Cols(), opt.Parallel, nil)
		}
	}
	core.LogI("二维码识别汇总：抽帧图片=%d 张，含二维码图片=%d 张，未发现二维码图片=%d 张，无法读取图片=%d 张，识别到的二维码载荷=%d 个，去重后载荷=%d 个，重复载荷=%d 个\n",
		stats.TotalImages, stats.ImagesWithPayloads, stats.EmptyImages, stats.UnreadableImages, stats.PayloadCount, stats.UniquePayloadCount, stats.DuplicatePayloadCount)
	if err != nil {
		return core.E("汇总二维码载荷失败", err)
	}

	core.LogI("正在根据二维码载荷还原文件内容……\n")
	manifest, output, err := core.RestoreFile(payloads, opt.Password)
	if err != nil {
		return core.E("还原文件内容失败", err)
	}
	digest := sha256.Sum256(output)
	core.LogI("文件内容还原并校验通过：原文件名=%q，文件大小=%d 字节，SHA-256=%x\n", manifest.FileName(), len(output), digest)

	outputPath, err := decodedOutputPath(opt.Output, manifest.FileName())
	if err != nil {
		return core.E("确定输出文件路径失败", err)
	}
	if err := writeOutputFile(outputPath, output, opt.Replace); err != nil {
		return core.E("写入输出文件失败", err)
	}

	core.LogI("解码成功：%s → %s\n", opt.Input, outputPath)
	core.LogI("解码汇总：抽帧图片=%d 张，识别到的二维码载荷=%d 个，去重后载荷=%d 个，还原文件大小=%d 字节\n",
		stats.TotalImages, stats.PayloadCount, stats.UniquePayloadCount, len(output))
	if opt.Keep {
		core.LogI("已保留解码过程中抽取的 PNG 图片：%s\n", framesDir)
	}
	return nil
}

// decodedOutputPath 选择解码输出路径，并拒绝清单文件名携带目录信息。
// 显式传入的输出路径由调用方负责；只有来自外部视频清单的默认文件名需要限制。
func decodedOutputPath(requested string, manifestName string) (string, error) {
	if requested != "" {
		return requested, nil
	}
	if manifestName == "" {
		return "decoded.bin", nil
	}
	if manifestName == "." || manifestName == ".." || filepath.IsAbs(manifestName) || strings.ContainsAny(manifestName, `/\`) || filepath.Base(manifestName) != manifestName {
		return "", fmt.Errorf("视频清单中的原文件名 %q 不安全，请使用 -o 参数指定输出路径", manifestName)
	}
	return manifestName, nil
}

// writeOutputFile 根据 replace 决定排他创建新文件，或通过临时文件原子替换已有文件。
func writeOutputFile(path string, data []byte, replace bool) error {
	if !replace {
		// O_EXCL 把“检查是否存在”和“创建文件”合并为一个原子操作，避免竞争条件。
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf("输出文件 %q 已存在，如需覆盖请添加 -replace 参数", path)
			}
			return err
		}
		written := false
		defer func() {
			if !written {
				_ = os.Remove(path)
			}
		}()
		if _, err := file.Write(data); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		written = true
		return nil
	}

	// 临时文件与目标文件放在同一目录，确保重命名不会跨文件系统，并尽可能保持原子性。
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() {
		_ = os.Remove(tempPath)
	}()
	if err := temp.Chmod(0644); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

// payloadCollectionStats 保存图片和二维码识别阶段的汇总统计。
type payloadCollectionStats struct {
	TotalImages           int
	ImagesWithPayloads    int
	EmptyImages           int
	UnreadableImages      int
	PayloadCount          int
	UniquePayloadCount    int
	DuplicatePayloadCount int
}

// collectPayloadsFromImages 汇总所有图片中的二维码载荷，并统计识别结果。
// 单张图片失败不会立即终止，因为其他抽帧图片可能包含同一批协议载荷。
func collectPayloadsFromImages(paths []string, maxFrameSize int, parallel bool, progress func(done int, total int)) ([][]byte, payloadCollectionStats, error) {
	return collectPayloadsFromImagesUsing(paths, parallel, progress, func(path string) ([][]byte, error) {
		return core.DecodeSinglePngToMultiByteArraysWithMaxFrameSize(path, maxFrameSize)
	})
}

func collectPayloadsFromImagesWithGrid(paths []string, maxFrameSize int, rows int, cols int, parallel bool, progress func(done int, total int)) ([][]byte, payloadCollectionStats, error) {
	return collectPayloadsFromImagesUsing(paths, parallel, progress, func(path string) ([][]byte, error) {
		return core.DecodeSinglePngToMultiByteArraysWithGrid(path, maxFrameSize, rows, cols)
	})
}

func collectPayloadsFromImagesUsing(paths []string, parallel bool, progress func(done int, total int), decode func(path string) ([][]byte, error)) ([][]byte, payloadCollectionStats, error) {
	if !parallel || len(paths) < 2 {
		return collectPayloadsSequentially(paths, progress, decode)
	}

	type decodeJob struct {
		index int
		path  string
	}
	type decodeResult struct {
		index    int
		payloads [][]byte
		err      error
	}

	workerCount := parallelWorkerCount(true, len(paths))
	jobs := make(chan decodeJob, len(paths))
	results := make(chan decodeResult, workerCount)
	for range workerCount {
		go func() {
			for job := range jobs {
				decoded, err := decode(job.path)
				results <- decodeResult{index: job.index, payloads: decoded, err: err}
			}
		}()
	}
	for index, path := range paths {
		jobs <- decodeJob{index: index, path: path}
	}
	close(jobs)

	decodedByIndex := make([][][]byte, len(paths))
	stats := payloadCollectionStats{TotalImages: len(paths)}
	for done := 1; done <= len(paths); done++ {
		result := <-results
		if result.err != nil {
			stats.UnreadableImages++
		} else {
			decodedByIndex[result.index] = result.payloads
			if len(result.payloads) == 0 {
				stats.EmptyImages++
			} else {
				stats.ImagesWithPayloads++
			}
		}
		if progress != nil {
			progress(done, len(paths))
		}
	}

	var payloads [][]byte
	for _, decoded := range decodedByIndex {
		payloads = append(payloads, decoded...)
	}
	completePayloadStats(&stats, payloads)
	if len(payloads) == 0 {
		return nil, stats, errors.New("未能从任何图片中识别出 TransferGo 二维码载荷")
	}
	return payloads, stats, nil
}

// collectPayloadsSequentially 按图片顺序串行解码，供关闭并行模式时使用。
func collectPayloadsSequentially(paths []string, progress func(done int, total int), decode func(path string) ([][]byte, error)) ([][]byte, payloadCollectionStats, error) {
	var payloads [][]byte
	stats := payloadCollectionStats{TotalImages: len(paths)}

	for i, path := range paths {
		decodedPayloads, err := decode(path)
		if err != nil {
			stats.UnreadableImages++
		} else {
			payloads = append(payloads, decodedPayloads...)
			if len(decodedPayloads) == 0 {
				stats.EmptyImages++
			} else {
				stats.ImagesWithPayloads++
			}
		}
		if progress != nil {
			progress(i+1, len(paths))
		}
	}
	completePayloadStats(&stats, payloads)
	if len(payloads) == 0 {
		return nil, stats, errors.New("未能从任何图片中识别出 TransferGo 二维码载荷")
	}
	return payloads, stats, nil
}

// parallelWorkerCount 根据并行开关、逻辑处理器数量和任务数量确定工作协程数。
func parallelWorkerCount(parallel bool, taskCount int) int {
	if taskCount <= 0 {
		return 0
	}
	if !parallel {
		return 1
	}
	return min(runtime.GOMAXPROCS(0), taskCount)
}

// completePayloadStats 统计二维码总数、唯一数和重复数。
func completePayloadStats(stats *payloadCollectionStats, payloads [][]byte) {
	unique := make(map[string]struct{}, len(payloads))
	for _, payload := range payloads {
		unique[string(payload)] = struct{}{}
	}
	stats.PayloadCount = len(payloads)
	stats.UniquePayloadCount = len(unique)
	stats.DuplicatePayloadCount = stats.PayloadCount - stats.UniquePayloadCount
}

// boolText 把布尔配置转换为便于阅读的中文。
func boolText(value bool) string {
	if value {
		return "是"
	}
	return "否"
}

// sortedFramePaths 返回目录内按文件名排序的视频帧路径。
func sortedFramePaths(dir string) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "frame_*.png"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

// endregion
