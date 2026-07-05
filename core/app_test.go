package core

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlainRoundTripThroughQRImages(t *testing.T) {
	input := make([]byte, 4096)
	for i := range input {
		input[i] = byte(i)
	}

	frames, meta, err := buildTransferFrames(input, "payload.bin", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ChunkCount != 41 {
		t.Fatalf("chunk count = %d, want 41", meta.ChunkCount)
	}

	dir := t.TempDir()
	renderOpt := qrRenderOptions{
		qrSize:      defaultQRSize,
		qrVersion:   defaultQRVersion,
		videoWidth:  defaultVideoWidth,
		videoHeight: defaultVideoHeight,
		gridSize:    defaultGridSize,
	}
	if err := writeQRFrames(frames, dir, renderOpt); err != nil {
		t.Fatal(err)
	}
	paths, err := sortedFramePaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("rendered images = %d, want 2", len(paths))
	}

	collected, total, stats, err := collectFramesFromImages(paths, renderOpt.gridSize)
	if err != nil {
		t.Fatal(err)
	}
	if stats.decoded != len(frames) {
		t.Fatalf("decoded frames = %d, want %d", stats.decoded, len(frames))
	}

	restoredMeta, output, err := restoreFromFrames(collected, total, "")
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

func TestCollectFramesSkipsNoisyImages(t *testing.T) {
	input := []byte("small payload")
	frames, _, err := buildTransferFrames(input, "noise.txt", "", 8)
	if err != nil {
		t.Fatal(err)
	}

	renderOpt := qrRenderOptions{
		qrSize:      defaultQRSize,
		qrVersion:   defaultQRVersion,
		videoWidth:  defaultVideoWidth,
		videoHeight: defaultVideoHeight,
		gridSize:    defaultGridSize,
	}
	dir := t.TempDir()
	if err := writeBlankPNG(filepath.Join(dir, "frame_000000.png")); err != nil {
		t.Fatal(err)
	}
	if err := writeQRFrames(frames, dir, renderOpt); err != nil {
		t.Fatal(err)
	}

	paths, err := sortedFramePaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	collected, total, stats, err := collectFramesFromImages(paths, renderOpt.gridSize)
	if err != nil {
		t.Fatal(err)
	}
	if stats.decodeFailures == 0 {
		t.Fatal("decode failures = 0, want noisy image to be skipped")
	}
	_, output, err := restoreFromFrames(collected, total, "")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output, input) {
		t.Fatal("restored bytes do not match input")
	}
}

func TestEncryptedRoundTripAndWrongPassword(t *testing.T) {
	input := []byte("secret file contents that should be authenticated before restore")

	frames, _, err := buildTransferFrames(input, "secret.txt", "correct horse", 16)
	if err != nil {
		t.Fatal(err)
	}
	frameMap := framesToMap(frames)

	if _, _, err := restoreFromFrames(frameMap, uint32(len(frames)), "wrong horse"); err == nil || !strings.Contains(err.Error(), "password check failed") {
		t.Fatalf("wrong password error = %v, want password check failed", err)
	}

	meta, output, err := restoreFromFrames(frameMap, uint32(len(frames)), "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if meta.FileName != "secret.txt" {
		t.Fatalf("file name = %q, want secret.txt", meta.FileName)
	}
	if !bytes.Equal(output, input) {
		t.Fatal("restored encrypted bytes do not match input")
	}
}

func TestMissingFrameFails(t *testing.T) {
	frames, _, err := buildTransferFrames([]byte("0123456789abcdef"), "split.txt", "", 4)
	if err != nil {
		t.Fatal(err)
	}
	frameMap := framesToMap(frames)
	delete(frameMap, 2)

	_, _, err = restoreFromFrames(frameMap, uint32(len(frames)), "")
	if err == nil || !strings.Contains(err.Error(), "missing frame") {
		t.Fatalf("missing frame error = %v, want missing frame", err)
	}
}

func TestPrepareFramesDirRejectsStaleFrames(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "frame_000001.png"), []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}

	_, _, err := prepareFramesDir(dir, "unused-*", false)
	if err == nil || !strings.Contains(err.Error(), "already contains frame_*.png") {
		t.Fatalf("prepareFramesDir error = %v, want stale frame rejection", err)
	}
}

func TestResolveFFmpegPath(t *testing.T) {
	t.Setenv("FFMPEG_PATH", "/env/ffmpeg")

	if got := resolveFFmpegPath("/flag/ffmpeg"); got != "/flag/ffmpeg" {
		t.Fatalf("explicit ffmpeg path = %q, want /flag/ffmpeg", got)
	}
	if got := resolveFFmpegPath(""); got != "/env/ffmpeg" {
		t.Fatalf("env ffmpeg path = %q, want /env/ffmpeg", got)
	}

	t.Setenv("FFMPEG_PATH", "")
	if got := resolveFFmpegPath(""); got != "ffmpeg" {
		t.Fatalf("default ffmpeg path = %q, want ffmpeg", got)
	}
}

func framesToMap(frames []transferFrame) map[uint32]transferFrame {
	out := make(map[uint32]transferFrame, len(frames))
	for _, frame := range frames {
		out[frame.Seq] = frame
	}
	return out
}

func writeBlankPNG(path string) error {
	img := image.NewRGBA(image.Rect(0, 0, defaultVideoWidth, defaultVideoHeight))
	for y := 0; y < defaultVideoHeight; y++ {
		for x := 0; x < defaultVideoWidth; x++ {
			img.Set(x, y, color.RGBA{R: 20, G: 20, B: 20, A: 255})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
	}()
	return png.Encode(file, img)
}
