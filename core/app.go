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
	// Defaults bias toward filmed-screen decoding: each QR is monochrome, uses a
	// higher version for capacity, and leaves enough pixels per module for phone
	// recordings to survive focus, scaling, and compression.
	//
	// defaultFPS keeps encoded videos slow enough for camera capture.
	defaultFPS = 3.0
	// defaultSampleFPS oversamples decode videos so dropped or blurred frames
	// can be recovered from neighboring samples.
	defaultSampleFPS = 9.0
	// defaultQRSize leaves enough pixels per module for version 12 QR codes.
	defaultQRSize = 240
	// defaultQRVersion balances payload capacity against scan reliability.
	defaultQRVersion = 12
	// defaultVideoWidth and defaultVideoHeight produce a square canvas that is
	// easy to center on a screen or in a camera viewfinder.
	defaultVideoWidth  = 800
	defaultVideoHeight = 800
	// defaultGridSize packs multiple QR codes per frame without making each
	// tile too small for filmed-screen decoding.
	defaultGridSize = 3
	// defaultCRF is visually lossless enough for QR edges while keeping output
	// videos smaller than fully lossless x264.
	defaultCRF = 24
	// defaultChunkSize is the upper bound for automatically selected plaintext
	// chunks; smaller values may be chosen for constrained QR versions.
	defaultChunkSize = 240
	// maxQRBytePayload is the practical binary payload ceiling used while
	// searching for a chunk size that fits the requested QR version.
	maxQRBytePayload = 2953
)

// appContext wires the stateful source-level contexts together. Stateless QR
// helpers stay as package functions in qr.go.
type appContext struct {
	commands commandContext
	protocol protocolContext
	video    videoContext
}

// collectStats records how noisy the extracted image set was. Decode reports
// these numbers so users can tell the difference between missing payload frames
// and images that simply were not readable QR codes.
type collectStats struct {
	images         int
	decoded        int
	ignored        int
	duplicates     int
	decodeFailures int
}

// imageDecodeResult carries one worker result back to the merge loop. Workers
// never mutate the final frame map, which keeps duplicate detection serialized.
type imageDecodeResult struct {
	payloads [][]byte
	err      error
}

// newAppContext builds the production wiring for the CLI, using real OS
// streams, randomness, QR handling, and ffmpeg execution hooks.
func newAppContext() appContext {
	return appContext{
		commands: newCommandContext(os.Stdout, os.Stderr),
		protocol: newProtocolContext(),
		video:    newVideoContext(),
	}
}

// Run is the top-level CLI dispatcher. It accepts argv without the program
// name, which keeps it easy to test and leaves process concerns in main.
func Run(args []string) error {
	return newAppContext().Run(args)
}

// Run dispatches one parsed subcommand to the encode or decode pipeline. It is
// a method so tests can inject fake command, protocol, QR, or video contexts.
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

// runEncode reads one input file, turns it into protocol frames, renders each
// frame as a QR PNG, and asks ffmpeg to assemble those PNGs into a video.
func (app appContext) runEncode(args []string) error {
	opt, err := app.commands.parseEncodeOptions(args, defaultEncodeOptions())
	if err != nil {
		return err
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
	if err := app.video.encodeVideoWithFFmpeg(opt.ffmpeg, framesDir, opt.output, opt.fps, opt.crf); err != nil {
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

// runDecode extracts PNG frames from a video, decodes any TransferGo QR
// payloads it can find, then verifies and reassembles the original file.
func (app appContext) runDecode(args []string) error {
	opt, err := app.commands.parseDecodeOptions(args, defaultDecodeOptions())
	if err != nil {
		return err
	}

	framesDir, cleanup, err := app.video.prepareFramesDir(opt.framesDir, "transfergo-decode-*", opt.keep)
	if err != nil {
		return fmt.Errorf("prepare decode frames directory: %w", err)
	}
	defer cleanup()

	Fprintln(app.commands.stdout, "extracting video frames with ffmpeg...")
	if err := app.video.extractFramesWithFFmpeg(opt.ffmpeg, opt.input, framesDir, opt.sampleFPS); err != nil {
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

// defaultEncodeOptions returns the camera-friendly encode defaults used before
// command-line flags override individual fields.
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

// defaultDecodeOptions returns decode defaults that favor robust recovery from
// a recorded screen over raw processing speed.
func defaultDecodeOptions() decodeOptions {
	return decodeOptions{
		sampleFPS: defaultSampleFPS,
		gridSize:  defaultGridSize,
	}
}

// writeTransferFrames marshals protocol frames before handing them to QR
// rendering. Keeping the conversion here lets qr.go stay unaware of TransferGo
// frame headers and encryption details.
func (app appContext) writeTransferFrames(frames []transferFrame, dir string, opt qrRenderOptions, progress func(done int, total int)) error {
	payloads := make([][]byte, 0, len(frames))
	for _, frame := range frames {
		payloads = append(payloads, app.protocol.marshalFrame(frame))
	}
	return writeQRPayloadFrames(payloads, dir, opt, progress)
}

// collectFramesFromImages decodes every extracted PNG and keeps only valid
// TransferGo frames. Image decoding is parallelized because each extracted frame
// is independent, while protocol merging stays on the caller goroutine so
// sequence de-duplication and conflict checks remain simple.
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

// autoChunkSize chooses a camera-friendly plaintext chunk size that still fits
// the requested QR version. This belongs in app because it uses protocol bytes
// and QR capacity together.
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

// canEncodeChunkSize builds a representative data frame and asks the QR encoder
// whether it fits. Encrypted frames reserve space for the GCM nonce and tag.
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
