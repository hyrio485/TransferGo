package core

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// encodeOptions mirrors the encode command flags after parsing. Keeping all
// user-controlled values in one struct makes the validation and pipeline order
// easy to audit.
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

// decodeOptions mirrors the decode command flags after parsing. The decode
// path accepts partial and noisy frame extraction results, so these options are
// kept separate from encode even when the names overlap.
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

// runEncode reads one input file, turns it into protocol frames, renders each
// frame as a QR PNG, and asks ffmpeg to assemble those PNGs into a video.
func runEncode(args []string) error {
	fs := flag.NewFlagSet("encode", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var opt encodeOptions
	// The short flags are the preferred CLI. The longer names are kept as
	// compatibility aliases so older command lines keep working.
	fs.StringVar(&opt.input, "i", "", "input file")
	fs.StringVar(&opt.input, "in", "", "input file (alias for -i)")
	fs.StringVar(&opt.output, "o", "", "output video path")
	fs.StringVar(&opt.output, "out", "", "output video path (alias for -o)")
	fs.StringVar(&opt.password, "p", "", "optional password for AES-GCM encryption")
	fs.StringVar(&opt.password, "password", "", "optional password for AES-GCM encryption (alias for -p)")
	fs.StringVar(&opt.ffmpeg, "ffmpeg", "", "ffmpeg executable path; falls back to FFMPEG_PATH, then PATH")
	fs.StringVar(&opt.framesDir, "frames-dir", "", "directory for generated PNG frames")
	fs.Float64Var(&opt.fps, "fps", defaultFPS, "video frames per second")
	fs.IntVar(&opt.qrSize, "qr-size", defaultQRSize, "QR image size inside each grid cell")
	fs.IntVar(&opt.qrVersion, "qr-version", defaultQRVersion, "QR version, 1-40; lower versions make each QR easier to film")
	fs.IntVar(&opt.videoWidth, "width", defaultVideoWidth, "output video width in pixels")
	fs.IntVar(&opt.videoWidth, "video-width", defaultVideoWidth, "output video width in pixels (alias for -width)")
	fs.IntVar(&opt.videoHeight, "height", defaultVideoHeight, "output video height in pixels")
	fs.IntVar(&opt.videoHeight, "video-height", defaultVideoHeight, "output video height in pixels (alias for -height)")
	fs.IntVar(&opt.gridSize, "grid-size", defaultGridSize, "QR grid rows and columns per video frame")
	fs.IntVar(&opt.chunkSize, "chunk-size", 0, "plaintext bytes per data QR; 0 auto-detects the maximum")
	fs.IntVar(&opt.crf, "crf", defaultCRF, "x264 CRF; 0 is lossless")
	fs.BoolVar(&opt.keep, "keep-frames", false, "keep generated PNG frames")

	if err := fs.Parse(args); err != nil {
		return err
	}
	// Validate before doing file or ffmpeg work so users get fast, local errors
	// for invalid command lines.
	if opt.input == "" {
		return errors.New("encode requires -i")
	}
	if opt.output == "" {
		return errors.New("encode requires -o")
	}
	if opt.fps <= 0 {
		return errors.New("-fps must be greater than 0")
	}
	if opt.qrSize <= 0 {
		return errors.New("-qr-size must be greater than 0")
	}
	if opt.qrVersion < 1 || opt.qrVersion > 40 {
		return errors.New("-qr-version must be between 1 and 40")
	}
	renderOpt := qrRenderOptions{
		qrSize:      opt.qrSize,
		qrVersion:   opt.qrVersion,
		videoWidth:  opt.videoWidth,
		videoHeight: opt.videoHeight,
		gridSize:    opt.gridSize,
	}
	if err := renderOpt.validate(); err != nil {
		return err
	}
	if opt.chunkSize < 0 {
		return errors.New("-chunk-size cannot be negative")
	}
	if opt.crf < 0 || opt.crf > 51 {
		return errors.New("-crf must be between 0 and 51")
	}

	input, err := os.ReadFile(opt.input)
	if err != nil {
		return err
	}

	// A zero chunk size means "fit as much plaintext as the selected QR version
	// can actually encode". The check uses the real QR encoder because capacity
	// depends on version, error correction, and encryption overhead.
	chunkSize := opt.chunkSize
	if chunkSize == 0 {
		chunkSize, err = autoChunkSize(opt.password != "", renderOpt.effectiveQRSize(), opt.qrVersion)
		if err != nil {
			return err
		}
	}

	frames, meta, err := buildTransferFrames(input, filepath.Base(opt.input), opt.password, chunkSize)
	if err != nil {
		return err
	}

	// Probe every frame before creating files. This catches a too-small QR
	// version or manually oversized chunk size with a precise frame number.
	for _, frame := range frames {
		if _, err := encodeQRPNG(marshalFrame(frame), renderOpt.effectiveQRSize(), opt.qrVersion); err != nil {
			return fmt.Errorf("frame %d does not fit QR version %d: %w", frame.Seq, opt.qrVersion, err)
		}
	}

	// Generated PNGs are either written to a user-specified empty directory or
	// to a temporary directory that is removed unless -keep-frames is set.
	framesDir, cleanup, err := prepareFramesDir(opt.framesDir, "transfergo-encode-*", opt.keep)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := writeQRFrames(frames, framesDir, renderOpt); err != nil {
		return err
	}
	if err := encodeVideoWithFFmpeg(opt.ffmpeg, framesDir, opt.output, opt.fps, opt.crf); err != nil {
		return err
	}

	fmt.Printf("encoded %s -> %s\n", opt.input, opt.output)
	fmt.Printf("protocol frames: %d, video frames: %d, data chunks: %d, chunk size: %d bytes, grid: %dx%d, fps: %s, encrypted: %t\n",
		len(frames), renderedFrameCount(len(frames), renderOpt), meta.ChunkCount, chunkSize, opt.gridSize, opt.gridSize, formatFPS(opt.fps), opt.password != "")
	if opt.keep {
		fmt.Printf("frames kept in %s\n", framesDir)
	}
	return nil
}

// runDecode extracts PNG frames from a video, decodes any TransferGo QR payloads
// it can find, then verifies and reassembles the original file.
func runDecode(args []string) error {
	fs := flag.NewFlagSet("decode", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var opt decodeOptions
	// The short flags are the preferred CLI. The longer names are kept as
	// compatibility aliases so existing decode commands do not break.
	fs.StringVar(&opt.input, "i", "", "input video path")
	fs.StringVar(&opt.input, "in", "", "input video path (alias for -i)")
	fs.StringVar(&opt.output, "o", "", "output file path; defaults to the original file name from the manifest")
	fs.StringVar(&opt.output, "out", "", "output file path (alias for -o)")
	fs.StringVar(&opt.password, "p", "", "password for encrypted videos")
	fs.StringVar(&opt.password, "password", "", "password for encrypted videos (alias for -p)")
	fs.StringVar(&opt.ffmpeg, "ffmpeg", "", "ffmpeg executable path; falls back to FFMPEG_PATH, then PATH")
	fs.StringVar(&opt.framesDir, "frames-dir", "", "directory for extracted PNG frames")
	fs.Float64Var(&opt.sampleFPS, "sample-fps", defaultSampleFPS, "QR sampling rate while decoding")
	fs.IntVar(&opt.gridSize, "grid-size", defaultGridSize, "QR grid rows and columns per video frame")
	fs.BoolVar(&opt.force, "force", false, "overwrite the output file if it exists")
	fs.BoolVar(&opt.keep, "keep-frames", false, "keep extracted PNG frames")

	if err := fs.Parse(args); err != nil {
		return err
	}
	// Decode has fewer required flags: when -o is omitted, the manifest's
	// original file name becomes the output path.
	if opt.input == "" {
		return errors.New("decode requires -i")
	}
	if opt.sampleFPS <= 0 {
		return errors.New("-sample-fps must be greater than 0")
	}
	if opt.gridSize <= 0 {
		return errors.New("-grid-size must be greater than 0")
	}

	framesDir, cleanup, err := prepareFramesDir(opt.framesDir, "transfergo-decode-*", opt.keep)
	if err != nil {
		return err
	}
	defer cleanup()

	// ffmpeg may extract duplicate frames or frames with motion blur. The later
	// collection step treats those as noisy input and keeps only valid payloads.
	if err := extractFramesWithFFmpeg(opt.ffmpeg, opt.input, framesDir, opt.sampleFPS); err != nil {
		return err
	}

	paths, err := sortedFramePaths(framesDir)
	if err != nil {
		return err
	}
	frames, total, stats, err := collectFramesFromImages(paths, opt.gridSize)
	if err != nil {
		return err
	}
	// restoreFromFrames performs the protocol-level checks: manifest parsing,
	// optional password verification, missing frame detection, decryption, and
	// final file hash validation.
	meta, output, err := restoreFromFrames(frames, total, opt.password)
	if err != nil {
		return err
	}

	// Prefer an explicit output path, then the manifest file name, then a stable
	// fallback for videos whose manifest did not carry a name.
	outputPath := opt.output
	if outputPath == "" {
		outputPath = meta.FileName
		if outputPath == "" {
			outputPath = "decoded.bin"
		}
	}
	// Refuse to overwrite by default; decode output is often the only recovered
	// copy of a file, so accidental replacement should require -force.
	if !opt.force {
		if _, err := os.Stat(outputPath); err == nil {
			return fmt.Errorf("output file %q already exists; pass -force to overwrite", outputPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.WriteFile(outputPath, output, 0644); err != nil {
		return err
	}

	fmt.Printf("decoded %s -> %s\n", opt.input, outputPath)
	fmt.Printf("frames: %d/%d, extracted images: %d, duplicates: %d, ignored QR payloads: %d, unreadable images: %d\n",
		len(frames), total, stats.images, stats.duplicates, stats.ignored, stats.decodeFailures)
	if opt.keep {
		fmt.Printf("frames kept in %s\n", framesDir)
	}
	return nil
}
