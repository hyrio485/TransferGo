package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"hyrio.xyz/transfergo/core"
)

type appContext struct {
	commands core.CommandContext
	protocol core.ProtocolContext
	video    core.VideoContext
}

func newAppContext() appContext {
	return appContext{
		commands: core.NewCommandContext(os.Stdout, os.Stderr),
		protocol: core.NewProtocolContext(),
		video:    core.NewVideoContext(),
	}
}

// region Encode

func (app appContext) runEncode(args []string) error {
	opt, err := app.commands.ParseEncodeOptions(args)
	if err != nil {
		return core.E("parse encode options", err)
	}

	core.Fprintf(app.commands.Stdout(), "reading input file: %s\n", opt.Input)
	input, err := os.ReadFile(opt.Input)
	if err != nil {
		return core.E("read input file", err)
	}

	core.Fprintln(app.commands.Stdout(), "building protocol frames...")
	payloads, err := app.protocol.EncodeFile(input, filepath.Base(opt.Input), opt.Password, opt.ChunkSize)
	if err != nil {
		return core.E("encode file payloads", err)
	}

	framesDir, cleanup, err := app.video.PrepareFramesDir(opt.FramesDir, "transfergo-encode-", opt.Keep)
	if err != nil {
		return core.E("prepare encode frames directory", err)
	}
	defer cleanup()

	core.Fprintln(app.commands.Stdout(), "rendering QR images...")
	if err := app.writePayloadImages(payloads, framesDir, opt); err != nil {
		return core.E("write QR images", err)
	}

	core.Fprintln(app.commands.Stdout(), "encoding video with ffmpeg...")
	if err := app.video.EncodeVideo(opt.Ffmpeg, framesDir, opt.Output, opt.FPS, opt.CRF); err != nil {
		return core.E("encode video with ffmpeg", err)
	}

	core.Fprintf(app.commands.Stdout(), "encoded %s -> %s\n", opt.Input, opt.Output)
	core.Fprintf(app.commands.Stdout(), "protocol frames: %d, video frames: %d, chunk size: %d bytes, grid: %dx%d, fps: %s, encrypted: %t\n",
		len(payloads), renderedFrameCount(len(payloads), opt.Rows, opt.Cols), opt.ChunkSize, opt.Rows, opt.Cols, fmt.Sprintf("%g", opt.FPS), opt.Password != "")
	if opt.Keep {
		core.Fprintf(app.commands.Stdout(), "frames kept in %s\n", framesDir)
	}
	return nil
}

func (app appContext) writePayloadImages(payloads [][]byte, framesDir string, opt core.EncodeOptions) error {
	slots := opt.Rows * opt.Cols
	total := renderedFrameCount(len(payloads), opt.Rows, opt.Cols)
	printProgress := app.commands.NewProgressPrinter("rendered QR images")

	for start, imageIndex := 0, 1; start < len(payloads); start, imageIndex = start+slots, imageIndex+1 {
		end := start + slots
		if end > len(payloads) {
			end = len(payloads)
		}

		path := filepath.Join(framesDir, fmt.Sprintf("frame_%06d.png", imageIndex))
		if err := core.EncodeMultiByteArraysToSinglePng(payloads[start:end], path, opt.QRSize, opt.Rows, opt.Cols, opt.ImageWidth, opt.ImageHeight); err != nil {
			return fmt.Errorf("encode QR image %d: %w", imageIndex, err)
		}
		printProgress(imageIndex, total)
	}
	return nil
}

func renderedFrameCount(payloadCount int, rows int, cols int) int {
	if payloadCount <= 0 {
		return 0
	}
	slots := rows * cols
	return (payloadCount + slots - 1) / slots
}

// endregion

// region Decode

func (app appContext) runDecode(args []string) error {
	opt, err := app.commands.ParseDecodeOptions(args)
	if err != nil {
		return core.E("parse decode options", err)
	}

	framesDir, cleanup, err := app.video.PrepareFramesDir(opt.FramesDir, "transfergo-decode-", opt.Keep)
	if err != nil {
		return core.E("prepare decode frames directory", err)
	}
	defer cleanup()

	core.Fprintln(app.commands.Stdout(), "extracting video frames with ffmpeg...")
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

	core.Fprintln(app.commands.Stdout(), "decoding QR images...")
	payloads, unreadable, err := collectPayloadsFromImages(paths, app.commands.NewProgressPrinter("decoded QR images"))
	if err != nil {
		return core.E("collect QR payloads from images", err)
	}

	core.Fprintln(app.commands.Stdout(), "restoring file bytes...")
	manifest, output, err := core.RestoreFile(payloads, opt.Password)
	if err != nil {
		return core.E("restore file bytes", err)
	}

	outputPath := opt.Output
	if outputPath == "" {
		outputPath = manifest.FileName()
		if outputPath == "" {
			outputPath = "decoded.bin"
		}
	}
	if !opt.Replace {
		if _, err := os.Stat(outputPath); err == nil {
			return fmt.Errorf("output file %q already exists; pass -replace to replace it", outputPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return core.E("check output file", err)
		}
	}
	if err := os.WriteFile(outputPath, output, 0644); err != nil {
		return core.E("write output file", err)
	}

	core.Fprintf(app.commands.Stdout(), "decoded %s -> %s\n", opt.Input, outputPath)
	core.Fprintf(app.commands.Stdout(), "payloads: %d, extracted images: %d, unreadable images: %d\n", len(payloads), len(paths), unreadable)
	if opt.Keep {
		core.Fprintf(app.commands.Stdout(), "frames kept in %s\n", framesDir)
	}
	return nil
}

func collectPayloadsFromImages(paths []string, progress func(done int, total int)) ([][]byte, int, error) {
	var payloads [][]byte
	unreadable := 0

	for i, path := range paths {
		decodedPayloads, err := core.DecodeSinglePngToMultiByteArrays(path)
		if err != nil {
			unreadable++
		} else {
			payloads = append(payloads, decodedPayloads...)
		}
		if progress != nil {
			progress(i+1, len(paths))
		}
	}
	if len(payloads) == 0 {
		return nil, unreadable, errors.New("no TransferGo QR payloads decoded")
	}
	return payloads, unreadable, nil
}

func sortedFramePaths(dir string) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "frame_*.png"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

// endregion

func (app appContext) Run(args []string) error {
	if len(args) == 0 {
		app.commands.PrintUsage(app.commands.Stderr())
		return errors.New("missing command")
	}

	switch args[0] {
	case "encode":
		return app.runEncode(args[1:])
	case "decode":
		return app.runDecode(args[1:])
	case "help", "-h", "--help":
		app.commands.PrintUsage(app.commands.Stdout())
		return nil
	default:
		app.commands.PrintUsage(app.commands.Stderr())
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func main() {
	if err := newAppContext().Run(os.Args[1:]); err != nil {
		core.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
