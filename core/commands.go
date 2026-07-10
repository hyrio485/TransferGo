package core

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"time"
)

const (
	defaultFPS         = 3.0
	defaultSampleFPS   = 9.0
	defaultQRSize      = 240
	defaultImageWidth  = 800
	defaultImageHeight = 800
	defaultRows        = 3
	defaultCols        = 3
	defaultChunkSize   = 240
	defaultCRF         = 24
)

const usageText = `用法：
  transfergo encode [参数]
  transfergo decode [参数]

标记为【必填】的参数必须提供，其余参数均为可选。

encode 参数：
  -i、-in <文件>           【必填】输入文件
  -o、-out <视频>          【必填】输出视频路径
  -p、-password <密码>     AES-GCM 加密密码，默认不加密
  -ffmpeg <路径>           ffmpeg 可执行文件路径，默认依次使用 FFMPEG_PATH 和 PATH
  -frames-dir <目录>       生成的 PNG 帧目录，默认创建随机临时目录
  -fps <帧率>              输出视频帧率，默认 3
  -qr-size <像素>          单个二维码尺寸，默认 240
  -width <像素>            输出视频宽度，默认 800
  -height <像素>           输出视频高度，默认 800
  -rows <数量>             每个视频帧的二维码行数，默认 3
  -cols <数量>             每个视频帧的二维码列数，默认 3
  -chunk-size <字节>       每个数据二维码的明文字节数，默认 240
  -crf <数值>              x264 CRF，取值范围为 0 至 51，默认 24
  -replace                 允许替换已有输出视频，默认关闭
  -keep-frames             保留生成的 PNG 帧，默认关闭

decode 参数：
  -i、-in <视频>           【必填】输入视频路径
  -o、-out <文件>          输出文件路径，默认使用清单中的原文件名
  -p、-password <密码>     加密视频的解码密码，默认空
  -ffmpeg <路径>           ffmpeg 可执行文件路径，默认依次使用 FFMPEG_PATH 和 PATH
  -frames-dir <目录>       抽取的 PNG 帧目录，默认创建随机临时目录
  -sample-fps <帧率>       解码抽帧率，默认 9
  -max-frame-size <像素>   解码帧最长边，范围为 1 至 16384，默认 2048
  -parallel <布尔值>       是否并行解码 PNG，默认 true
  -replace                 允许替换已有输出文件，默认关闭
  -keep-frames             保留抽取的 PNG 帧，默认关闭
`

// CommandContext 保存命令行处理过程中可替换的时间来源。
type CommandContext struct {
	now func() time.Time
}

// NewCommandContext 创建使用系统时间的命令上下文。
func NewCommandContext() CommandContext {
	return CommandContext{
		now: time.Now,
	}
}

// EncodeOptions 描述 encode 命令支持的全部参数。
type EncodeOptions struct {
	Input       string
	Output      string
	Password    string
	Ffmpeg      string
	FramesDir   string
	FPS         float64
	QRSize      int
	Rows        int
	Cols        int
	ImageWidth  int
	ImageHeight int
	ChunkSize   int
	CRF         int
	Replace     bool
	Keep        bool
}

// DecodeOptions 描述 decode 命令支持的全部参数。
type DecodeOptions struct {
	Input        string
	Output       string
	Password     string
	Ffmpeg       string
	FramesDir    string
	SampleFPS    float64
	MaxFrameSize int
	Parallel     bool
	Replace      bool
	Keep         bool
}

// ParseEncodeOptions 解析并校验 encode 命令参数。
func (ctx CommandContext) ParseEncodeOptions(args []string) (EncodeOptions, error) {
	fs := flag.NewFlagSet("encode", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	opt := EncodeOptions{
		FPS:         defaultFPS,
		QRSize:      defaultQRSize,
		Rows:        defaultRows,
		Cols:        defaultCols,
		ImageWidth:  defaultImageWidth,
		ImageHeight: defaultImageHeight,
		ChunkSize:   defaultChunkSize,
		CRF:         defaultCRF,
	}
	fs.StringVar(&opt.Input, "i", opt.Input, "input file")
	fs.StringVar(&opt.Input, "in", opt.Input, "input file (alias for -i)")
	fs.StringVar(&opt.Output, "o", opt.Output, "output video path")
	fs.StringVar(&opt.Output, "out", opt.Output, "output video path (alias for -o)")
	fs.StringVar(&opt.Password, "p", opt.Password, "optional password for AES-GCM encryption")
	fs.StringVar(&opt.Password, "password", opt.Password, "optional password for AES-GCM encryption (alias for -p)")
	fs.StringVar(&opt.Ffmpeg, "ffmpeg", opt.Ffmpeg, "ffmpeg executable path; falls back to FFMPEG_PATH, then PATH")
	fs.StringVar(&opt.FramesDir, "frames-dir", opt.FramesDir, "directory for generated PNG frames")
	fs.Float64Var(&opt.FPS, "fps", opt.FPS, "video frames per second")
	fs.IntVar(&opt.QRSize, "qr-size", opt.QRSize, "QR image size inside each grid cell")
	fs.IntVar(&opt.ImageWidth, "width", opt.ImageWidth, "output image width in pixels")
	fs.IntVar(&opt.ImageHeight, "height", opt.ImageHeight, "output image height in pixels")
	fs.IntVar(&opt.Rows, "rows", opt.Rows, "QR grid rows per video frame")
	fs.IntVar(&opt.Cols, "cols", opt.Cols, "QR grid columns per video frame")
	fs.IntVar(&opt.ChunkSize, "chunk-size", opt.ChunkSize, "plaintext bytes per data QR")
	fs.IntVar(&opt.CRF, "crf", opt.CRF, "x264 CRF; 0 is lossless")
	fs.BoolVar(&opt.Replace, "replace", opt.Replace, "replace the output video if it exists")
	fs.BoolVar(&opt.Keep, "keep-frames", opt.Keep, "keep generated PNG frames")

	if err := fs.Parse(args); err != nil {
		return EncodeOptions{}, E("parse encode flags failed", err)
	}
	if fs.NArg() != 0 {
		return EncodeOptions{}, errors.New("encode does not accept positional arguments")
	}
	if opt.QRSize <= 0 {
		return EncodeOptions{}, errors.New("-qr-size must be greater than 0")
	}
	if opt.Rows <= 0 {
		return EncodeOptions{}, errors.New("-rows must be greater than 0")
	}
	if opt.Cols <= 0 {
		return EncodeOptions{}, errors.New("-cols must be greater than 0")
	}
	if opt.ImageWidth <= 0 {
		return EncodeOptions{}, errors.New("-width must be greater than 0")
	}
	if opt.ImageHeight <= 0 {
		return EncodeOptions{}, errors.New("-height must be greater than 0")
	}
	if opt.ImageWidth%2 != 0 {
		return EncodeOptions{}, errors.New("-width must be an even number for yuv420p video")
	}
	if opt.ImageHeight%2 != 0 {
		return EncodeOptions{}, errors.New("-height must be an even number for yuv420p video")
	}
	// 使用除法判断网格是否能放入图片，避免直接相乘造成整数溢出。
	if opt.Rows > opt.ImageHeight/opt.QRSize {
		return EncodeOptions{}, errors.New("-rows and -qr-size do not fit inside -height")
	}
	if opt.Cols > opt.ImageWidth/opt.QRSize {
		return EncodeOptions{}, errors.New("-cols and -qr-size do not fit inside -width")
	}
	maxInt := int(^uint(0) >> 1)
	if opt.Rows > maxInt/opt.Cols {
		return EncodeOptions{}, errors.New("-rows and -cols produce too many grid slots")
	}
	if opt.Input == "" {
		return EncodeOptions{}, errors.New("encode requires -i")
	}
	if opt.Output == "" {
		return EncodeOptions{}, errors.New("encode requires -o")
	}
	if opt.FPS <= 0 || math.IsNaN(opt.FPS) || math.IsInf(opt.FPS, 0) {
		return EncodeOptions{}, errors.New("-fps must be greater than 0")
	}
	if opt.ChunkSize <= 0 {
		return EncodeOptions{}, errors.New("-chunk-size must be greater than 0")
	}
	if opt.CRF < 0 || opt.CRF > 51 {
		return EncodeOptions{}, errors.New("-crf must be between 0 and 51")
	}
	return opt, nil
}

// ParseDecodeOptions 解析并校验 decode 命令参数。
func (ctx CommandContext) ParseDecodeOptions(args []string) (DecodeOptions, error) {
	fs := flag.NewFlagSet("decode", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	opt := DecodeOptions{
		SampleFPS:    defaultSampleFPS,
		MaxFrameSize: defaultDecodeFrameSize,
		Parallel:     true,
	}
	fs.StringVar(&opt.Input, "i", opt.Input, "input video path")
	fs.StringVar(&opt.Input, "in", opt.Input, "input video path (alias for -i)")
	fs.StringVar(&opt.Output, "o", opt.Output, "output file path; defaults to the original file name from the manifest")
	fs.StringVar(&opt.Output, "out", opt.Output, "output file path (alias for -o)")
	fs.StringVar(&opt.Password, "p", opt.Password, "password for encrypted videos")
	fs.StringVar(&opt.Password, "password", opt.Password, "password for encrypted videos (alias for -p)")
	fs.StringVar(&opt.Ffmpeg, "ffmpeg", opt.Ffmpeg, "ffmpeg executable path; falls back to FFMPEG_PATH, then PATH")
	fs.StringVar(&opt.FramesDir, "frames-dir", opt.FramesDir, "directory for extracted PNG frames")
	fs.Float64Var(&opt.SampleFPS, "sample-fps", opt.SampleFPS, "QR sampling rate while decoding")
	fs.IntVar(&opt.MaxFrameSize, "max-frame-size", opt.MaxFrameSize, "maximum decoded frame edge in pixels")
	fs.BoolVar(&opt.Parallel, "parallel", opt.Parallel, "decode PNG frames in parallel")
	fs.BoolVar(&opt.Replace, "replace", opt.Replace, "replace the output file if it exists")
	fs.BoolVar(&opt.Keep, "keep-frames", opt.Keep, "keep extracted PNG frames")

	if err := fs.Parse(args); err != nil {
		return DecodeOptions{}, E("parse decode flags", err)
	}
	if fs.NArg() != 0 {
		return DecodeOptions{}, errors.New("decode does not accept positional arguments")
	}
	if opt.Input == "" {
		return DecodeOptions{}, errors.New("decode requires -i")
	}
	if opt.SampleFPS <= 0 || math.IsNaN(opt.SampleFPS) || math.IsInf(opt.SampleFPS, 0) {
		return DecodeOptions{}, errors.New("-sample-fps must be greater than 0")
	}
	if opt.MaxFrameSize <= 0 || opt.MaxFrameSize > maxImageDimension {
		return DecodeOptions{}, fmt.Errorf("-max-frame-size must be between 1 and %d", maxImageDimension)
	}
	return opt, nil
}

// PrintUsage 不经过日志格式化，直接向标准输出打印 encode 和 decode 的完整参数说明。
func (ctx CommandContext) PrintUsage() {
	_, _ = fmt.Fprint(os.Stdout, usageText)
}

// NewProgressPrinter 创建带限流的进度输出函数，并保证最后一次进度一定会输出。
func (ctx CommandContext) NewProgressPrinter(label string) func(done int, total int) {
	var last time.Time
	return func(done int, total int) {
		if total <= 0 {
			return
		}
		now := ctx.now()
		// 中间进度最多每 500 毫秒输出一次，完成进度不受限流影响。
		if done != total && !last.IsZero() && now.Sub(last) < 500*time.Millisecond {
			return
		}
		last = now
		LogI("%s: %d/%d\n", label, done, total)
	}
}
