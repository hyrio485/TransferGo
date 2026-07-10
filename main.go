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
		return withUsage(fmt.Errorf("unknown command %q", args[0]))
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
		return withUsage(core.E("parse encode options", err))
	}
	if !opt.Replace {
		if _, err := os.Lstat(opt.Output); err == nil {
			return fmt.Errorf("output file %q already exists; pass -replace to replace it", opt.Output)
		} else if !errors.Is(err, os.ErrNotExist) {
			return core.E("check output file", err)
		}
	}

	core.LogI("encode configuration: input=%q, output=%q, fps=%g, image=%dx%d, grid=%dx%d, QR size=%d, chunk size=%d bytes, CRF=%d, encrypted=%t, replace=%t\n",
		opt.Input, opt.Output, opt.FPS, opt.ImageWidth, opt.ImageHeight, opt.Rows, opt.Cols, opt.QRSize, opt.ChunkSize, opt.CRF, opt.Password != "", opt.Replace)
	core.LogI("reading input file: %s\n", opt.Input)
	input, err := os.ReadFile(opt.Input)
	if err != nil {
		return core.E("read input file", err)
	}
	core.LogI("input file loaded: %d bytes\n", len(input))

	core.LogI("building protocol frames...\n")
	payloads, err := app.protocol.EncodeFile(input, filepath.Base(opt.Input), opt.Password, opt.ChunkSize)
	if err != nil {
		return core.E("encode file payloads", err)
	}
	core.LogI("protocol prepared: %d QR codes, %d video frames\n", len(payloads), renderedFrameCount(len(payloads), opt.Rows, opt.Cols))

	framesDir, cleanup, err := app.video.PrepareFramesDir(opt.FramesDir, "transfergo-encode-", opt.Keep)
	if err != nil {
		return core.E("prepare encode frames directory", err)
	}
	defer cleanup()

	core.LogI("rendering QR images...\n")
	if err := app.writePayloadImages(payloads, framesDir, opt); err != nil {
		return core.E("write QR images", err)
	}
	core.LogI("QR images rendered: %d PNG files in %s\n", renderedFrameCount(len(payloads), opt.Rows, opt.Cols), framesDir)

	core.LogI("encoding video with ffmpeg...\n")
	if err := app.video.EncodeVideo(opt.Ffmpeg, framesDir, opt.Output, opt.FPS, opt.CRF, opt.Replace); err != nil {
		return core.E("encode video with ffmpeg", err)
	}
	if info, statErr := os.Stat(opt.Output); statErr == nil {
		core.LogI("video encoded: %d bytes\n", info.Size())
	}

	core.LogI("encoded %s -> %s\n", opt.Input, opt.Output)
	core.LogI("protocol frames: %d, video frames: %d, chunk size: %d bytes, grid: %dx%d, fps: %s, encrypted: %t\n",
		len(payloads), renderedFrameCount(len(payloads), opt.Rows, opt.Cols), opt.ChunkSize, opt.Rows, opt.Cols, fmt.Sprintf("%g", opt.FPS), opt.Password != "")
	if opt.Keep {
		core.LogI("frames kept in %s\n", framesDir)
	}
	return nil
}

// writePayloadImages 按网格容量对协议载荷分组，并为每组生成一张二维码图片。
func (app appContext) writePayloadImages(payloads [][]byte, framesDir string, opt core.EncodeOptions) error {
	slots := opt.Rows * opt.Cols
	total := renderedFrameCount(len(payloads), opt.Rows, opt.Cols)
	printProgress := app.commands.NewProgressPrinter("rendered QR images")

	for start, imageIndex := 0, 1; start < len(payloads); imageIndex++ {
		// 使用剩余长度判断是否还能装满一帧，避免 start 与 slots 相加时发生整数溢出。
		end := len(payloads)
		if slots < len(payloads)-start {
			end = start + slots
		}

		path := filepath.Join(framesDir, fmt.Sprintf("frame_%06d.png", imageIndex))
		if err := core.EncodeMultiByteArraysToSinglePng(payloads[start:end], path, opt.QRSize, opt.Rows, opt.Cols, opt.ImageWidth, opt.ImageHeight); err != nil {
			return fmt.Errorf("encode QR image %d: %w", imageIndex, err)
		}
		printProgress(imageIndex, total)
		start = end
	}
	return nil
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

// endregion

// region Decode

// runDecode 解析解码参数，并依次完成视频抽帧、二维码识别、协议还原和文件写入。
func (app appContext) runDecode(args []string) error {
	opt, err := app.commands.ParseDecodeOptions(args)
	if err != nil {
		return withUsage(core.E("parse decode options", err))
	}
	requestedOutput := opt.Output
	if requestedOutput == "" {
		requestedOutput = "<manifest file name>"
	}
	core.LogI("decode configuration: input=%q, output=%q, sample fps=%g, max frame size=%d, parallel=%t, password supplied=%t, replace=%t\n",
		opt.Input, requestedOutput, opt.SampleFPS, opt.MaxFrameSize, opt.Parallel, opt.Password != "", opt.Replace)

	framesDir, cleanup, err := app.video.PrepareFramesDir(opt.FramesDir, "transfergo-decode-", opt.Keep)
	if err != nil {
		return core.E("prepare decode frames directory", err)
	}
	defer cleanup()

	core.LogI("extracting video frames with ffmpeg...\n")
	if err := app.video.ExtractFrames(opt.Ffmpeg, opt.Input, framesDir, opt.SampleFPS); err != nil {
		return core.E("extract video frames with ffmpeg", err)
	}

	paths, err := sortedFramePaths(framesDir)
	if err != nil {
		return core.E("list extracted frame paths", err)
	}
	if len(paths) == 0 {
		return errors.New("no extracted image(s) found")
	}
	core.LogI("video frames extracted: %d PNG files in %s\n", len(paths), framesDir)

	workers := decodeWorkerCount(opt.Parallel, len(paths))
	core.LogI("decoding QR images: parallel=%t, workers=%d, max frame size=%d\n", opt.Parallel, workers, opt.MaxFrameSize)
	payloads, stats, err := collectPayloadsFromImages(paths, opt.MaxFrameSize, opt.Parallel, app.commands.NewProgressPrinter("decoded QR images"))
	core.LogI("QR decode summary: images=%d, with QR=%d, without QR=%d, unreadable=%d, QR codes=%d, unique=%d, duplicates=%d\n",
		stats.TotalImages, stats.ImagesWithPayloads, stats.EmptyImages, stats.UnreadableImages, stats.PayloadCount, stats.UniquePayloadCount, stats.DuplicatePayloadCount)
	if err != nil {
		return core.E("collect QR payloads from images", err)
	}

	core.LogI("restoring file bytes...\n")
	manifest, output, err := core.RestoreFile(payloads, opt.Password)
	if err != nil {
		return core.E("restore file bytes", err)
	}
	digest := sha256.Sum256(output)
	core.LogI("file restored and verified: manifest name=%q, size=%d bytes, SHA-256=%x\n", manifest.FileName(), len(output), digest)

	outputPath, err := decodedOutputPath(opt.Output, manifest.FileName())
	if err != nil {
		return core.E("choose output file", err)
	}
	if err := writeOutputFile(outputPath, output, opt.Replace); err != nil {
		return core.E("write output file", err)
	}

	core.LogI("decoded %s -> %s\n", opt.Input, outputPath)
	core.LogI("decode completed: extracted images=%d, QR codes=%d, unique QR codes=%d, restored bytes=%d\n",
		stats.TotalImages, stats.PayloadCount, stats.UniquePayloadCount, len(output))
	if opt.Keep {
		core.LogI("frames kept in %s\n", framesDir)
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
		return "", fmt.Errorf("unsafe file name %q in manifest; pass -o to choose an output path", manifestName)
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
				return fmt.Errorf("output file %q already exists; pass -replace to replace it", path)
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
	if !parallel || len(paths) < 2 {
		return collectPayloadsSequentially(paths, maxFrameSize, progress)
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

	workerCount := decodeWorkerCount(true, len(paths))
	jobs := make(chan decodeJob, len(paths))
	results := make(chan decodeResult, workerCount)
	for range workerCount {
		go func() {
			for job := range jobs {
				decoded, err := core.DecodeSinglePngToMultiByteArraysWithMaxFrameSize(job.path, maxFrameSize)
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
		return nil, stats, errors.New("no TransferGo QR payloads decoded")
	}
	return payloads, stats, nil
}

// collectPayloadsSequentially 按图片顺序串行解码，供关闭并行模式时使用。
func collectPayloadsSequentially(paths []string, maxFrameSize int, progress func(done int, total int)) ([][]byte, payloadCollectionStats, error) {
	var payloads [][]byte
	stats := payloadCollectionStats{TotalImages: len(paths)}

	for i, path := range paths {
		decodedPayloads, err := core.DecodeSinglePngToMultiByteArraysWithMaxFrameSize(path, maxFrameSize)
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
		return nil, stats, errors.New("no TransferGo QR payloads decoded")
	}
	return payloads, stats, nil
}

// decodeWorkerCount 根据并行开关、逻辑处理器数量和任务数量确定工作协程数。
func decodeWorkerCount(parallel bool, taskCount int) int {
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
