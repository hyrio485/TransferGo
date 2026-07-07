package core

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppPlainRoundTripThroughQRImages(t *testing.T) {
	app := newAppContext()
	input := make([]byte, 4096)
	for i := range input {
		input[i] = byte(i)
	}

	frames, meta, err := app.protocol.BuildTransferFrames(input, "payload.bin", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ChunkCount != 41 {
		t.Fatalf("chunk count = %d, want 41", meta.ChunkCount)
	}

	dir := t.TempDir()
	renderOpt := testRenderOptions()
	if err := app.writeTransferFrames(frames, dir, renderOpt, nil); err != nil {
		t.Fatal(err)
	}
	paths, err := app.video.SortedFramePaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 5 {
		t.Fatalf("rendered images = %d, want 5", len(paths))
	}

	collected, total, stats, err := app.collectFramesFromImages(paths, renderOpt.gridSize, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.decoded != len(frames) {
		t.Fatalf("decoded frames = %d, want %d", stats.decoded, len(frames))
	}

	restoredMeta, output, err := app.protocol.RestoreFromFrames(collected, total, "")
	if err != nil {
		t.Fatal(err)
	}
	if restoredMeta.FileName != "payload.bin" {
		t.Fatalf("file name = %q, want payload.bin", restoredMeta.FileName)
	}
	if !bytes.Equal(output, input) {
		t.Fatal("restored bytes do not match input")
	}
}

func TestAppCollectFramesSkipsNoisyImages(t *testing.T) {
	app := newAppContext()
	input := []byte("small payload")
	frames, _, err := app.protocol.BuildTransferFrames(input, "noise.txt", "", 8)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := writeBlankPNG(filepath.Join(dir, "frame_000000.png")); err != nil {
		t.Fatal(err)
	}
	if err := app.writeTransferFrames(frames, dir, testRenderOptions(), nil); err != nil {
		t.Fatal(err)
	}

	paths, err := app.video.SortedFramePaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	collected, total, stats, err := app.collectFramesFromImages(paths, defaultGridSize, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.decodeFailures == 0 {
		t.Fatal("decode failures = 0, want noisy image to be skipped")
	}
	_, output, err := app.protocol.RestoreFromFrames(collected, total, "")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output, input) {
		t.Fatal("restored bytes do not match input")
	}
}

func TestAppAutoChunkSizeUsesCameraFriendlyDefault(t *testing.T) {
	app := newAppContext()
	chunkSize, err := app.autoChunkSize(false, defaultQRSize, defaultQRVersion)
	if err != nil {
		t.Fatal(err)
	}
	if chunkSize != defaultChunkSize {
		t.Fatalf("chunk size = %d, want %d", chunkSize, defaultChunkSize)
	}
}

func TestAppRunRejectsUnknownCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := newAppContext()
	app.commands.stdout = &stdout
	app.commands.stderr = &stderr

	err := app.Run([]string{"unknown"})
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("Run error = %v, want unknown command", err)
	}
	if !strings.Contains(stderr.String(), "usage:") {
		t.Fatalf("stderr = %q, want usage", stderr.String())
	}
}

func testRenderOptions() QRRenderOptions {
	return QRRenderOptions{
		qrSize:      defaultQRSize,
		qrVersion:   defaultQRVersion,
		videoWidth:  defaultVideoWidth,
		videoHeight: defaultVideoHeight,
		gridSize:    defaultGridSize,
	}
}
