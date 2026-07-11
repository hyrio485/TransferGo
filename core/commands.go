package core

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"
)

const (
	defaultFPS               = 3.0
	defaultSampleFPS         = 9.0
	defaultQRSize            = 175
	defaultQRErrorCorrection = "L"
	defaultRows              = 3
	defaultCols              = 3
	defaultChunkSize         = 240
	defaultCRF               = 24
)

const usageText = `用法：
  transfergo encode [参数] <文件>
  transfergo decode [参数] <视频>

标记为【必填】的参数必须提供，其余参数均为可选。

encode 参数：
  <文件>                   【必填】输入文件
  -i、-in <文件>           输入文件，可代替位置参数
  -o、-out <视频>          输出视频路径，默认在输入文件名后追加 .mp4
  -p、-password <密码>     AES-GCM 加密密码，默认不加密
  -ffmpeg <路径>           ffmpeg 可执行文件路径，默认依次使用 FFMPEG_PATH 和 PATH
  -frames-dir <目录>       生成的 PNG 帧目录，默认创建随机临时目录
  -fps <帧率>              输出视频帧率，默认 3
  -qr-size <像素>          单个二维码尺寸，默认 175
  -qr-error-correction <等级> 二维码纠错等级，可选 L、M、Q、H，默认 L
  -width <像素>            输出视频宽度，默认根据列数和二维码尺寸自动计算
  -height <像素>           输出视频高度，默认根据行数和二维码尺寸自动计算
  -rows <数量>             每个视频帧的二维码行数，默认 3
  -cols <数量>             每个视频帧的二维码列数，默认 3
  -chunk-size <字节>       每个数据二维码的明文字节数，默认 240
  -crf <数值>              x264 CRF，取值范围为 0 至 51，默认 24
  -parallel <布尔值>       是否并行生成 PNG，默认 true
  -replace                 允许替换已有输出视频，默认关闭
  -keep-frames             保留生成的 PNG 帧，默认关闭

decode 参数：
  <视频>                   【必填】输入视频路径
  -i、-in <视频>           输入视频路径，可代替位置参数
  -o、-out <文件>          输出文件路径，默认使用清单中的原文件名
  -p、-password <密码>     加密视频的解码密码，默认空
  -ffmpeg <路径>           ffmpeg 可执行文件路径，默认依次使用 FFMPEG_PATH 和 PATH
  -frames-dir <目录>       抽取的 PNG 帧目录，默认创建随机临时目录
  -sample-fps <帧率>       解码抽帧率，默认 9
  -max-frame-size <像素>   解码帧最长边，范围为 1 至 16384，默认 2048
  -rows <数量>             手动指定二维码行数，必须与 -cols 同时使用
  -cols <数量>             手动指定二维码列数，必须与 -rows 同时使用
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
	Input             string
	Output            string
	Password          string
	Ffmpeg            string
	FramesDir         string
	FPS               float64
	QRSize            int
	QRErrorCorrection string
	Rows              int
	Cols              int
	ImageWidth        int
	ImageHeight       int
	ChunkSize         int
	CRF               int
	Parallel          bool
	Replace           bool
	Keep              bool
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
	Rows         int
	Cols         int
	Parallel     bool
	Replace      bool
	Keep         bool
}

// ParseEncodeOptions 解析并校验 encode 命令参数。
func (ctx CommandContext) ParseEncodeOptions(args []string) (EncodeOptions, error) {
	fs := flag.NewFlagSet("encode", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	opt := EncodeOptions{
		FPS:               defaultFPS,
		QRSize:            defaultQRSize,
		QRErrorCorrection: defaultQRErrorCorrection,
		Rows:              defaultRows,
		Cols:              defaultCols,
		ChunkSize:         defaultChunkSize,
		CRF:               defaultCRF,
		Parallel:          true,
	}
	fs.StringVar(&opt.Input, "i", opt.Input, "输入文件")
	fs.StringVar(&opt.Input, "in", opt.Input, "输入文件，等同于 -i")
	fs.StringVar(&opt.Output, "o", opt.Output, "输出视频路径")
	fs.StringVar(&opt.Output, "out", opt.Output, "输出视频路径，等同于 -o")
	fs.StringVar(&opt.Password, "p", opt.Password, "AES-GCM 加密密码")
	fs.StringVar(&opt.Password, "password", opt.Password, "AES-GCM 加密密码，等同于 -p")
	fs.StringVar(&opt.Ffmpeg, "ffmpeg", opt.Ffmpeg, "ffmpeg 可执行文件路径")
	fs.StringVar(&opt.FramesDir, "frames-dir", opt.FramesDir, "生成的 PNG 帧目录")
	fs.Float64Var(&opt.FPS, "fps", opt.FPS, "视频帧率")
	fs.IntVar(&opt.QRSize, "qr-size", opt.QRSize, "单个二维码尺寸")
	fs.StringVar(&opt.QRErrorCorrection, "qr-error-correction", opt.QRErrorCorrection, "二维码纠错等级，可选 L、M、Q、H")
	fs.IntVar(&opt.ImageWidth, "width", opt.ImageWidth, "输出画面宽度")
	fs.IntVar(&opt.ImageHeight, "height", opt.ImageHeight, "输出画面高度")
	fs.IntVar(&opt.Rows, "rows", opt.Rows, "每帧的二维码行数")
	fs.IntVar(&opt.Cols, "cols", opt.Cols, "每帧的二维码列数")
	fs.IntVar(&opt.ChunkSize, "chunk-size", opt.ChunkSize, "每个数据二维码的明文字节数")
	fs.IntVar(&opt.CRF, "crf", opt.CRF, "x264 CRF，0 表示无损")
	fs.BoolVar(&opt.Parallel, "parallel", opt.Parallel, "是否并行生成 PNG 图片")
	fs.BoolVar(&opt.Replace, "replace", opt.Replace, "是否覆盖已有输出视频")
	fs.BoolVar(&opt.Keep, "keep-frames", opt.Keep, "是否保留生成的 PNG 图片")

	if err := fs.Parse(args); err != nil {
		return EncodeOptions{}, E("编码参数格式不正确", err)
	}
	widthSpecified := false
	heightSpecified := false
	fs.Visit(func(f *flag.Flag) {
		widthSpecified = widthSpecified || f.Name == "width"
		heightSpecified = heightSpecified || f.Name == "height"
	})
	if fs.NArg() > 1 {
		return EncodeOptions{}, errors.New("encode 命令只能指定一个输入文件")
	}
	if fs.NArg() == 1 {
		if opt.Input != "" {
			return EncodeOptions{}, errors.New("输入文件不能同时使用位置参数和 -i 参数指定")
		}
		opt.Input = fs.Arg(0)
	}
	if opt.QRSize <= 0 {
		return EncodeOptions{}, errors.New("-qr-size 必须大于 0")
	}
	opt.QRErrorCorrection = strings.ToUpper(opt.QRErrorCorrection)
	if !isValidQRErrorCorrection(opt.QRErrorCorrection) {
		return EncodeOptions{}, errors.New("-qr-error-correction 必须是 L、M、Q、H 之一")
	}
	if opt.Rows <= 0 {
		return EncodeOptions{}, errors.New("-rows 必须大于 0")
	}
	if opt.Cols <= 0 {
		return EncodeOptions{}, errors.New("-cols 必须大于 0")
	}
	if !widthSpecified {
		var ok bool
		opt.ImageWidth, ok = qrGridDimension(opt.Cols, opt.QRSize)
		if !ok {
			return EncodeOptions{}, errors.New("-cols 与 -qr-size 的组合过大，无法计算视频宽度")
		}
		if opt.ImageWidth%2 != 0 {
			opt.ImageWidth++
		}
	}
	if !heightSpecified {
		var ok bool
		opt.ImageHeight, ok = qrGridDimension(opt.Rows, opt.QRSize)
		if !ok {
			return EncodeOptions{}, errors.New("-rows 与 -qr-size 的组合过大，无法计算视频高度")
		}
		if opt.ImageHeight%2 != 0 {
			opt.ImageHeight++
		}
	}
	if opt.ImageWidth <= 0 {
		return EncodeOptions{}, errors.New("-width 必须大于 0")
	}
	if opt.ImageHeight <= 0 {
		return EncodeOptions{}, errors.New("-height 必须大于 0")
	}
	if opt.ImageWidth%2 != 0 {
		return EncodeOptions{}, errors.New("使用 yuv420p 视频格式时，-width 必须是偶数")
	}
	if opt.ImageHeight%2 != 0 {
		return EncodeOptions{}, errors.New("使用 yuv420p 视频格式时，-height 必须是偶数")
	}
	if !qrGridDimensionFits(opt.ImageHeight, opt.Rows, opt.QRSize) {
		return EncodeOptions{}, errors.New("-rows 与 -qr-size 的组合超出 -height 指定的画面高度")
	}
	if !qrGridDimensionFits(opt.ImageWidth, opt.Cols, opt.QRSize) {
		return EncodeOptions{}, errors.New("-cols 与 -qr-size 的组合超出 -width 指定的画面宽度")
	}
	maxInt := int(^uint(0) >> 1)
	if opt.Rows > maxInt/opt.Cols {
		return EncodeOptions{}, errors.New("-rows 与 -cols 的乘积过大，无法创建二维码网格")
	}
	if opt.Input == "" {
		return EncodeOptions{}, errors.New("encode 命令缺少输入文件")
	}
	if opt.Output == "" {
		opt.Output = opt.Input + ".mp4"
	}
	if opt.FPS <= 0 || math.IsNaN(opt.FPS) || math.IsInf(opt.FPS, 0) {
		return EncodeOptions{}, errors.New("-fps 必须是大于 0 的有效数字")
	}
	if opt.ChunkSize <= 0 {
		return EncodeOptions{}, errors.New("-chunk-size 必须大于 0")
	}
	if opt.CRF < 0 || opt.CRF > 51 {
		return EncodeOptions{}, errors.New("-crf 必须在 0 至 51 之间")
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
	fs.StringVar(&opt.Input, "i", opt.Input, "输入视频路径")
	fs.StringVar(&opt.Input, "in", opt.Input, "输入视频路径，等同于 -i")
	fs.StringVar(&opt.Output, "o", opt.Output, "输出文件路径")
	fs.StringVar(&opt.Output, "out", opt.Output, "输出文件路径，等同于 -o")
	fs.StringVar(&opt.Password, "p", opt.Password, "加密视频的解码密码")
	fs.StringVar(&opt.Password, "password", opt.Password, "加密视频的解码密码，等同于 -p")
	fs.StringVar(&opt.Ffmpeg, "ffmpeg", opt.Ffmpeg, "ffmpeg 可执行文件路径")
	fs.StringVar(&opt.FramesDir, "frames-dir", opt.FramesDir, "抽取的 PNG 帧目录")
	fs.Float64Var(&opt.SampleFPS, "sample-fps", opt.SampleFPS, "解码时每秒抽取的图片数")
	fs.IntVar(&opt.MaxFrameSize, "max-frame-size", opt.MaxFrameSize, "解码图片最长边限制")
	fs.IntVar(&opt.Rows, "rows", opt.Rows, "手动指定每张图片中的二维码行数")
	fs.IntVar(&opt.Cols, "cols", opt.Cols, "手动指定每张图片中的二维码列数")
	fs.BoolVar(&opt.Parallel, "parallel", opt.Parallel, "是否并行识别 PNG 图片")
	fs.BoolVar(&opt.Replace, "replace", opt.Replace, "是否覆盖已有输出文件")
	fs.BoolVar(&opt.Keep, "keep-frames", opt.Keep, "是否保留抽取的 PNG 图片")

	if err := fs.Parse(args); err != nil {
		return DecodeOptions{}, E("解码参数格式不正确", err)
	}
	if fs.NArg() > 1 {
		return DecodeOptions{}, errors.New("decode 命令只能指定一个输入视频")
	}
	if fs.NArg() == 1 {
		if opt.Input != "" {
			return DecodeOptions{}, errors.New("输入视频不能同时使用位置参数和 -i 参数指定")
		}
		opt.Input = fs.Arg(0)
	}
	if opt.Input == "" {
		return DecodeOptions{}, errors.New("decode 命令缺少输入视频")
	}
	if opt.SampleFPS <= 0 || math.IsNaN(opt.SampleFPS) || math.IsInf(opt.SampleFPS, 0) {
		return DecodeOptions{}, errors.New("-sample-fps 必须是大于 0 的有效数字")
	}
	if opt.MaxFrameSize <= 0 || opt.MaxFrameSize > maxImageDimension {
		return DecodeOptions{}, fmt.Errorf("-max-frame-size 必须在 1 至 %d 之间", maxImageDimension)
	}
	if (opt.Rows == 0) != (opt.Cols == 0) {
		return DecodeOptions{}, errors.New("-rows 与 -cols 必须同时指定")
	}
	if opt.Rows < 0 || opt.Rows > maxImageDimension {
		return DecodeOptions{}, fmt.Errorf("-rows 必须在 1 至 %d 之间，或不指定", maxImageDimension)
	}
	if opt.Cols < 0 || opt.Cols > maxImageDimension {
		return DecodeOptions{}, fmt.Errorf("-cols 必须在 1 至 %d 之间，或不指定", maxImageDimension)
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
		LogI("%s：%d／%d\n", label, done, total)
	}
}
