package core

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"time"
)

// commandContext 持有面向 CLI 的流和时间。它解析参数，不依赖协议、二维码或视频实现细节。
type commandContext struct {
	stdout io.Writer
	stderr io.Writer
	now    func() time.Time
}

// encodeOptions 对应 encode 命令解析后的参数。把所有用户可控值放在一个结构中，便于审查校验和流水线顺序。
type encodeOptions struct {
	input       string
	output      string
	password    string
	ffmpeg      string
	framesDir   string
	fps         float64
	qrSize      int
	qrVersion   int
	videoWidth  int
	videoHeight int
	gridSize    int
	chunkSize   int
	crf         int
	keep        bool
}

// decodeOptions 对应 decode 命令解析后的参数。decode 路径会接受不完整且有噪声的抽帧结果，所以即使名称重叠，也和 encode 参数分开保存。
type decodeOptions struct {
	input     string
	output    string
	password  string
	ffmpeg    string
	framesDir string
	sampleFPS float64
	gridSize  int
	force     bool
	keep      bool
}

// newCommandContext 连接命令输出流和用于进度节流的时钟。保持这些依赖可注入，让 CLI 行为测试无需触碰全局 stdout、stderr 或时间。
func newCommandContext(stdout io.Writer, stderr io.Writer) commandContext {
	return commandContext{
		stdout: stdout,
		stderr: stderr,
		now:    time.Now,
	}
}

// parseEncodeOptions 只解析 encode 参数，并校验那些若留到编码流水线后面才失败会更难理解的值。
func (ctx commandContext) parseEncodeOptions(args []string, defaults encodeOptions) (encodeOptions, error) {
	fs := flag.NewFlagSet("encode", flag.ContinueOnError)
	fs.SetOutput(ctx.stderr)

	opt := defaults
	fs.StringVar(&opt.input, "i", opt.input, "input file")
	fs.StringVar(&opt.input, "in", opt.input, "input file (alias for -i)")
	fs.StringVar(&opt.output, "o", opt.output, "output video path")
	fs.StringVar(&opt.output, "out", opt.output, "output video path (alias for -o)")
	fs.StringVar(&opt.password, "p", opt.password, "optional password for AES-GCM encryption")
	fs.StringVar(&opt.password, "password", opt.password, "optional password for AES-GCM encryption (alias for -p)")
	fs.StringVar(&opt.ffmpeg, "ffmpeg", opt.ffmpeg, "ffmpeg executable path; falls back to FFMPEG_PATH, then PATH")
	fs.StringVar(&opt.framesDir, "frames-dir", opt.framesDir, "directory for generated PNG frames")
	fs.Float64Var(&opt.fps, "fps", opt.fps, "video frames per second")
	fs.IntVar(&opt.qrSize, "qr-size", opt.qrSize, "QR image size inside each grid cell")
	fs.IntVar(&opt.qrVersion, "qr-version", opt.qrVersion, "QR version, 1-40; lower versions make each QR easier to film")
	fs.IntVar(&opt.videoWidth, "width", opt.videoWidth, "output video width in pixels")
	fs.IntVar(&opt.videoWidth, "video-width", opt.videoWidth, "output video width in pixels (alias for -width)")
	fs.IntVar(&opt.videoHeight, "height", opt.videoHeight, "output video height in pixels")
	fs.IntVar(&opt.videoHeight, "video-height", opt.videoHeight, "output video height in pixels (alias for -height)")
	fs.IntVar(&opt.gridSize, "grid-size", opt.gridSize, "QR grid rows and columns per video frame")
	fs.IntVar(&opt.chunkSize, "chunk-size", opt.chunkSize, "plaintext bytes per data QR; 0 selects a camera-friendly default")
	fs.IntVar(&opt.crf, "crf", opt.crf, "x264 CRF; 0 is lossless")
	fs.BoolVar(&opt.keep, "keep-frames", opt.keep, "keep generated PNG frames")

	if err := fs.Parse(args); err != nil {
		return encodeOptions{}, fmt.Errorf("parse encode flags failed: %w", err)
	}
	if opt.input == "" {
		return encodeOptions{}, errors.New("encode requires -i")
	}
	if opt.output == "" {
		return encodeOptions{}, errors.New("encode requires -o")
	}
	if opt.fps <= 0 {
		return encodeOptions{}, errors.New("-fps must be greater than 0")
	}
	if opt.qrSize <= 0 {
		return encodeOptions{}, errors.New("-qr-size must be greater than 0")
	}
	if opt.qrVersion < 1 || opt.qrVersion > 40 {
		return encodeOptions{}, errors.New("-qr-version must be between 1 and 40")
	}
	if opt.videoWidth <= 0 {
		return encodeOptions{}, errors.New("-width must be greater than 0")
	}
	if opt.videoHeight <= 0 {
		return encodeOptions{}, errors.New("-height must be greater than 0")
	}
	if opt.gridSize <= 0 {
		return encodeOptions{}, errors.New("-grid-size must be greater than 0")
	}
	if opt.chunkSize < 0 {
		return encodeOptions{}, errors.New("-chunk-size cannot be negative")
	}
	if opt.crf < 0 || opt.crf > 51 {
		return encodeOptions{}, errors.New("-crf must be between 0 and 51")
	}
	return opt, nil
}

// parseDecodeOptions 只解析 decode 参数，并校验 ffmpeg 抽帧或帧恢复开始前所需的选项。
func (ctx commandContext) parseDecodeOptions(args []string, defaults decodeOptions) (decodeOptions, error) {
	fs := flag.NewFlagSet("decode", flag.ContinueOnError)
	fs.SetOutput(ctx.stderr)

	opt := defaults
	fs.StringVar(&opt.input, "i", opt.input, "input video path")
	fs.StringVar(&opt.input, "in", opt.input, "input video path (alias for -i)")
	fs.StringVar(&opt.output, "o", opt.output, "output file path; defaults to the original file name from the manifest")
	fs.StringVar(&opt.output, "out", opt.output, "output file path (alias for -o)")
	fs.StringVar(&opt.password, "p", opt.password, "password for encrypted videos")
	fs.StringVar(&opt.password, "password", opt.password, "password for encrypted videos (alias for -p)")
	fs.StringVar(&opt.ffmpeg, "ffmpeg", opt.ffmpeg, "ffmpeg executable path; falls back to FFMPEG_PATH, then PATH")
	fs.StringVar(&opt.framesDir, "frames-dir", opt.framesDir, "directory for extracted PNG frames")
	fs.Float64Var(&opt.sampleFPS, "sample-fps", opt.sampleFPS, "QR sampling rate while decoding")
	fs.IntVar(&opt.gridSize, "grid-size", opt.gridSize, "QR grid rows and columns per video frame")
	fs.BoolVar(&opt.force, "force", opt.force, "overwrite the output file if it exists")
	fs.BoolVar(&opt.keep, "keep-frames", opt.keep, "keep extracted PNG frames")

	if err := fs.Parse(args); err != nil {
		return decodeOptions{}, fmt.Errorf("parse decode flags: %w", err)
	}
	if opt.input == "" {
		return decodeOptions{}, errors.New("decode requires -i")
	}
	if opt.sampleFPS <= 0 {
		return decodeOptions{}, errors.New("-sample-fps must be greater than 0")
	}
	if opt.gridSize <= 0 {
		return decodeOptions{}, errors.New("-grid-size must be greater than 0")
	}
	return opt, nil
}

// printUsage 有意保持简短：详细命令参数放在各命令自己的 FlagSet 帮助中，这段文本只帮助选择子命令。
func (ctx commandContext) printUsage(w io.Writer) {
	Fprint(w, `usage:
  transfergo encode -i <file> -o <video.mp4> [-p <password>]
  transfergo decode -i <video.mp4> [-o <file>] [-p <password>]

commands:
  encode  split a file into QR frames and render them into a video with ffmpeg
  decode  extract QR frames from a recorded video and rebuild the original file
`)
}

// newProgressPrinter 对进度输出做节流，让长扫描仍然可见，同时避免在快机器上每帧打印一行。
func (ctx commandContext) newProgressPrinter(label string) func(done int, total int) {
	var last time.Time
	return func(done int, total int) {
		if total <= 0 {
			return
		}
		now := ctx.now()
		if done != total && !last.IsZero() && now.Sub(last) < 500*time.Millisecond {
			return
		}
		last = now
		Fprintf(ctx.stdout, "%s: %d/%d\n", label, done, total)
	}
}
