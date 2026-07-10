package core

import (
	"errors"
	"flag"
	"io"
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

type CommandContext struct {
	stdout io.Writer
	stderr io.Writer
	now    func() time.Time
}

func (ctx CommandContext) Stdout() io.Writer {
	return ctx.stdout
}

func (ctx CommandContext) Stderr() io.Writer {
	return ctx.stderr
}

func NewCommandContext(stdout io.Writer, stderr io.Writer) CommandContext {
	return CommandContext{
		stdout: stdout,
		stderr: stderr,
		now:    time.Now,
	}
}

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
	Keep        bool
}

type DecodeOptions struct {
	Input     string
	Output    string
	Password  string
	Ffmpeg    string
	FramesDir string
	SampleFPS float64
	Replace   bool
	Keep      bool
}

func (ctx CommandContext) ParseEncodeOptions(args []string) (EncodeOptions, error) {
	fs := flag.NewFlagSet("encode", flag.ContinueOnError)
	fs.SetOutput(ctx.stderr)

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
	fs.BoolVar(&opt.Keep, "keep-frames", opt.Keep, "keep generated PNG frames")

	if err := fs.Parse(args); err != nil {
		return EncodeOptions{}, E("parse encode flags failed", err)
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
	if opt.Rows*opt.QRSize > opt.ImageHeight {
		return EncodeOptions{}, errors.New("-rows and -qr-size do not fit inside -height")
	}
	if opt.Cols*opt.QRSize > opt.ImageWidth {
		return EncodeOptions{}, errors.New("-cols and -qr-size do not fit inside -width")
	}
	if opt.Input == "" {
		return EncodeOptions{}, errors.New("encode requires -i")
	}
	if opt.Output == "" {
		return EncodeOptions{}, errors.New("encode requires -o")
	}
	if opt.FPS <= 0 {
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

func (ctx CommandContext) ParseDecodeOptions(args []string) (DecodeOptions, error) {
	fs := flag.NewFlagSet("decode", flag.ContinueOnError)
	fs.SetOutput(ctx.stderr)

	opt := DecodeOptions{
		SampleFPS: defaultSampleFPS,
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
	fs.BoolVar(&opt.Replace, "replace", opt.Replace, "replace the output file if it exists")
	fs.BoolVar(&opt.Keep, "keep-frames", opt.Keep, "keep extracted PNG frames")

	if err := fs.Parse(args); err != nil {
		return DecodeOptions{}, E("parse decode flags", err)
	}
	if opt.Input == "" {
		return DecodeOptions{}, errors.New("decode requires -i")
	}
	if opt.SampleFPS <= 0 {
		return DecodeOptions{}, errors.New("-sample-fps must be greater than 0")
	}
	return opt, nil
}

func (ctx CommandContext) PrintUsage(w io.Writer) {
	Fprint(w, `usage:
  transfergo encode -i <file> -o <video.mp4> [-p <password>]
  transfergo decode -i <video.mp4> [-o <file>] [-p <password>]

commands:
  encode  split a file into QR frames and render them into a video with ffmpeg
  decode  extract QR frames from a recorded video and rebuild the original file
`)
}

func (ctx CommandContext) NewProgressPrinter(label string) func(done int, total int) {
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
