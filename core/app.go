package core

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const (
	// 默认值偏向屏幕拍摄后的解码：每个二维码都是单色，使用较高版本换取容量，并为每个模块留下足够像素，让手机录像能经受对焦、缩放和压缩。
	//
	// defaultFPS 让编码后的视频足够慢，便于相机捕获。
	defaultFPS = 3.0
	// defaultSampleFPS 对解码视频进行过采样，让丢失或模糊的帧可以从相邻采样中恢复。
	defaultSampleFPS = 9.0
	// defaultQRSize 为版本 12 的二维码给每个模块留下足够像素。
	defaultQRSize = 240
	// defaultQRVersion 在载荷容量和扫码可靠性之间取得平衡。
	defaultQRVersion = 12
	// defaultVideoWidth 和 defaultVideoHeight 生成正方形画布，方便在屏幕或相机取景框中居中。
	defaultVideoWidth  = 800
	defaultVideoHeight = 800
	// defaultGridSize 在每帧中打包多个二维码，同时避免每个格子小到不利于屏幕拍摄解码。
	defaultGridSize = 3
	// defaultCRF 对二维码边缘来说视觉上足够无损，同时让输出视频小于完全无损的 x264。
	defaultCRF = 24
	// defaultChunkSize 是自动选择明文分块大小的上限；受限的二维码版本可能选择更小的值。
	defaultChunkSize = 240
	// maxQRBytePayload 是搜索适配目标二维码版本的分块大小时使用的实际二进制载荷上限。
	maxQRBytePayload = 2953
)

// appContext 把有状态的源级上下文连接在一起。无状态的二维码辅助函数保留为 qr.go 中的包函数。
type appContext struct {
	commands commandContext
	protocol protocolContext
	video    videoContext
}

// collectStats 记录抽取出的图片集合有多嘈杂。decode 会报告这些数字，方便区分缺失载荷帧和图片本身无法读取二维码两种情况。
type collectStats struct {
	images         int
	decoded        int
	ignored        int
	duplicates     int
	decodeFailures int
}

// imageDecodeResult 把一个 worker 的结果带回合并循环。worker 从不修改最终帧映射，从而让重复检测保持串行。
type imageDecodeResult struct {
	payloads [][]byte
	err      error
}

// newAppContext 为 CLI 构建生产环境连接，使用真实的操作系统流、随机源、二维码处理和 ffmpeg 执行钩子。
func newAppContext() appContext {
	return appContext{
		commands: newCommandContext(os.Stdout, os.Stderr),
		protocol: newProtocolContext(),
		video:    newVideoContext(),
	}
}

// Run 是顶层 CLI 分发器。它接收不含程序名的 argv，便于测试，并把进程相关职责留在 main 中。
func Run(args []string) error {
	return newAppContext().Run(args)
}

// Run 把解析出的子命令分发到 encode 或 decode 流水线。它是方法，因此测试可以注入假的命令、协议、二维码或视频上下文。
func (app appContext) Run(args []string) error {
	if len(args) == 0 {
		app.commands.printUsage(app.commands.stderr)
		return errors.New("missing command")
	}

	switch args[0] {
	case "encode":
		return app.runEncode(args[1:])
	case "decode":
		return app.runDecode(args[1:])
	case "help", "-h", "--help":
		app.commands.printUsage(app.commands.stdout)
		return nil
	default:
		app.commands.printUsage(app.commands.stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// runEncode 读取一个输入文件，将其转为协议帧，把每帧渲染成二维码 PNG，并让 ffmpeg 将这些 PNG 组装成视频。
func (app appContext) runEncode(args []string) error {
	opt, err := app.commands.parseEncodeOptions(args, defaultEncodeOptions())
	if err != nil {
		return fmt.Errorf("parse encode options: %w", err)
	}

	renderOpt := qrRenderOptions{
		qrSize:      opt.qrSize,
		qrVersion:   opt.qrVersion,
		videoWidth:  opt.videoWidth,
		videoHeight: opt.videoHeight,
		gridSize:    opt.gridSize,
	}
	if err := renderOpt.validate(); err != nil {
		return fmt.Errorf("validate encode options: %w", err)
	}

	Fprintf(app.commands.stdout, "reading input file: %s\n", opt.input)
	input, err := os.ReadFile(opt.input)
	if err != nil {
		return fmt.Errorf("read input file: %w", err)
	}

	chunkSize := opt.chunkSize
	if chunkSize == 0 {
		chunkSize, err = app.autoChunkSize(opt.password != "", renderOpt.effectiveQRSize(), opt.qrVersion)
		if err != nil {
			return fmt.Errorf("choose automatic chunk size: %w", err)
		}
	}

	Fprintln(app.commands.stdout, "building protocol frames...")
	frames, meta, err := app.protocol.buildTransferFrames(input, filepath.Base(opt.input), opt.password, chunkSize)
	if err != nil {
		return fmt.Errorf("build protocol frames: %w", err)
	}

	for _, frame := range frames {
		if _, err := encodeQRPNG(app.protocol.marshalFrame(frame), renderOpt.effectiveQRSize(), opt.qrVersion); err != nil {
			return fmt.Errorf("frame %d does not fit QR version %d: %w", frame.Seq, opt.qrVersion, err)
		}
	}

	framesDir, cleanup, err := app.video.prepareFramesDir(opt.framesDir, "transfergo-encode-*", opt.keep)
	if err != nil {
		return fmt.Errorf("prepare encode frames directory: %w", err)
	}
	defer cleanup()

	Fprintln(app.commands.stdout, "rendering QR images...")
	if err := app.writeTransferFrames(frames, framesDir, renderOpt, app.commands.newProgressPrinter("rendered QR images")); err != nil {
		return fmt.Errorf("write QR frames: %w", err)
	}
	Fprintln(app.commands.stdout, "encoding video with ffmpeg...")
	if err := app.video.encodeVideoWithFfmpeg(opt.ffmpeg, framesDir, opt.output, opt.fps, opt.crf); err != nil {
		return fmt.Errorf("encode video with ffmpeg: %w", err)
	}

	Fprintf(app.commands.stdout, "encoded %s -> %s\n", opt.input, opt.output)
	Fprintf(app.commands.stdout, "protocol frames: %d, video frames: %d, data chunks: %d, chunk size: %d bytes, grid: %dx%d, fps: %s, encrypted: %t\n",
		len(frames), renderedFrameCount(len(frames), renderOpt), meta.ChunkCount, chunkSize, opt.gridSize, opt.gridSize, formatFPS(opt.fps), opt.password != "")
	if opt.keep {
		Fprintf(app.commands.stdout, "frames kept in %s\n", framesDir)
	}
	return nil
}

// runDecode 从视频中抽取 PNG 帧，解码能找到的 TransferGo 二维码载荷，然后校验并重新组装原始文件。
func (app appContext) runDecode(args []string) error {
	opt, err := app.commands.parseDecodeOptions(args, defaultDecodeOptions())
	if err != nil {
		return fmt.Errorf("parse decode options: %w", err)
	}

	framesDir, cleanup, err := app.video.prepareFramesDir(opt.framesDir, "transfergo-decode-*", opt.keep)
	if err != nil {
		return fmt.Errorf("prepare decode frames directory: %w", err)
	}
	defer cleanup()

	Fprintln(app.commands.stdout, "extracting video frames with ffmpeg...")
	if err := app.video.extractFramesWithFfmpeg(opt.ffmpeg, opt.input, framesDir, opt.sampleFPS); err != nil {
		return fmt.Errorf("extract video frames with ffmpeg: %w", err)
	}

	paths, err := app.video.sortedFramePaths(framesDir)
	if err != nil {
		return fmt.Errorf("list extracted frame paths: %w", err)
	}
	Fprintln(app.commands.stdout, "decoding QR images...")
	frames, total, stats, err := app.collectFramesFromImages(paths, opt.gridSize, app.commands.newProgressPrinter("decoded QR images"))
	if err != nil {
		return fmt.Errorf("collect transfer frames from images: %w", err)
	}

	Fprintln(app.commands.stdout, "restoring file bytes...")
	meta, output, err := app.protocol.restoreFromFrames(frames, total, opt.password)
	if err != nil {
		return fmt.Errorf("restore file bytes: %w", err)
	}

	outputPath := opt.output
	if outputPath == "" {
		outputPath = meta.FileName
		if outputPath == "" {
			outputPath = "decoded.bin"
		}
	}
	if !opt.force {
		if _, err := os.Stat(outputPath); err == nil {
			return fmt.Errorf("output file %q already exists; pass -force to overwrite", outputPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("check output file: %w", err)
		}
	}
	if err := os.WriteFile(outputPath, output, 0644); err != nil {
		return fmt.Errorf("write output file: %w", err)
	}

	Fprintf(app.commands.stdout, "decoded %s -> %s\n", opt.input, outputPath)
	Fprintf(app.commands.stdout, "frames: %d/%d, extracted images: %d, duplicates: %d, ignored QR payloads: %d, unreadable images: %d\n",
		len(frames), total, stats.images, stats.duplicates, stats.ignored, stats.decodeFailures)
	if opt.keep {
		Fprintf(app.commands.stdout, "frames kept in %s\n", framesDir)
	}
	return nil
}

// defaultEncodeOptions 返回命令行参数覆盖各字段前使用的、适合相机拍摄的 encode 默认值。
func defaultEncodeOptions() encodeOptions {
	return encodeOptions{
		fps:         defaultFPS,
		qrSize:      defaultQRSize,
		qrVersion:   defaultQRVersion,
		videoWidth:  defaultVideoWidth,
		videoHeight: defaultVideoHeight,
		gridSize:    defaultGridSize,
		crf:         defaultCRF,
	}
}

// defaultDecodeOptions 返回 decode 默认值，相比原始处理速度更偏向从屏幕录像中稳健恢复。
func defaultDecodeOptions() decodeOptions {
	return decodeOptions{
		sampleFPS: defaultSampleFPS,
		gridSize:  defaultGridSize,
	}
}

// writeTransferFrames 在交给二维码渲染前序列化协议帧。把转换保留在这里，可以让 qr.go 不感知 TransferGo 帧头和加密细节。
func (app appContext) writeTransferFrames(frames []transferFrame, dir string, opt qrRenderOptions, progress func(done int, total int)) error {
	payloads := make([][]byte, 0, len(frames))
	for _, frame := range frames {
		payloads = append(payloads, app.protocol.marshalFrame(frame))
	}
	return writeQRPayloadFrames(payloads, dir, opt, progress)
}

// collectFramesFromImages 解码每张抽取出的 PNG，并只保留有效的 TransferGo 帧。图片解码会并行执行，因为每张抽取帧彼此独立；协议合并留在调用方 goroutine 中，让序列去重和冲突检查保持简单。
func (app appContext) collectFramesFromImages(paths []string, gridSize int, progress func(done int, total int)) (map[uint32]transferFrame, uint32, collectStats, error) {
	frames := make(map[uint32]transferFrame)
	var total uint32
	stats := collectStats{images: len(paths)}
	if len(paths) == 0 {
		return nil, 0, stats, fmt.Errorf("no extracted image(s) found")
	}

	workerCount := minInt(runtime.NumCPU(), len(paths))
	jobs := make(chan string)
	results := make(chan imageDecodeResult)

	for worker := 0; worker < workerCount; worker++ {
		go func() {
			for path := range jobs {
				payloads, err := decodeQRCodePayloads(path, gridSize)
				results <- imageDecodeResult{payloads: payloads, err: err}
			}
		}()
	}
	go func() {
		for _, path := range paths {
			jobs <- path
		}
		close(jobs)
	}()

	for done := 1; done <= len(paths); done++ {
		result := <-results
		if result.err != nil {
			stats.decodeFailures++
		} else {
			for _, payload := range result.payloads {
				frame, err := app.protocol.parseFrame(payload)
				if err != nil {
					stats.ignored++
					continue
				}
				stats.decoded++
				if total == 0 {
					total = frame.Total
				} else if total != frame.Total {
					return nil, 0, stats, fmt.Errorf("frame %d reports total %d, expected %d", frame.Seq, frame.Total, total)
				}
				if existing, ok := frames[frame.Seq]; ok {
					if !sameFrame(existing, frame) {
						return nil, 0, stats, fmt.Errorf("conflicting duplicate frame %d", frame.Seq)
					}
					stats.duplicates++
					continue
				}
				frames[frame.Seq] = frame
			}
		}
		if progress != nil {
			progress(done, len(paths))
		}
	}

	if total == 0 {
		return nil, 0, stats, fmt.Errorf("no TransferGo QR frames decoded from %d extracted image(s)", len(paths))
	}
	return frames, total, stats, nil
}

// autoChunkSize 选择仍能放入目标二维码版本、且适合相机拍摄的明文分块大小。它属于 app，因为这里同时使用协议字节和二维码容量。
func (app appContext) autoChunkSize(encrypted bool, qrSize int, qrVersion int) (int, error) {
	low, high := 1, maxQRBytePayload
	best := 0
	for low <= high {
		mid := low + (high-low)/2
		if app.canEncodeChunkSize(mid, encrypted, qrSize, qrVersion) {
			best = mid
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	if best == 0 {
		return 0, fmt.Errorf("no data chunk size fits QR version %d", qrVersion)
	}
	if best > defaultChunkSize {
		return defaultChunkSize, nil
	}
	return best, nil
}

// canEncodeChunkSize 构造一个有代表性的数据帧，并询问二维码编码器它是否能放下。加密帧会为 GCM nonce 和 tag 预留空间。
func (app appContext) canEncodeChunkSize(chunkSize int, encrypted bool, qrSize int, qrVersion int) bool {
	bodyLen := chunkSize
	if encrypted {
		bodyLen += nonceSize + 16
	}
	frame := transferFrame{
		Flags: 0,
		Kind:  frameKindData,
		Seq:   1,
		Total: 2,
		Body:  bytes.Repeat([]byte{0x80}, bodyLen),
	}
	if encrypted {
		frame.Flags = frameFlagEncrypted
	}
	_, err := encodeQRPNG(app.protocol.marshalFrame(frame), qrSize, qrVersion)
	return err == nil
}
