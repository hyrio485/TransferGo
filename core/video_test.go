package core

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestVideoPrepareFramesDirRejectsStaleFrames(t *testing.T) {
	ctx := newVideoContext()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "frame_000001.png"), []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}

	_, _, err := ctx.prepareFramesDir(dir, "unused-*", false)
	if err == nil || !strings.Contains(err.Error(), "already contains frame_*.png") {
		t.Fatalf("prepareFramesDir error = %v, want stale frame rejection", err)
	}
}

func TestVideoPrepareFramesDirDefaultsToCurrentDirectory(t *testing.T) {
	ctx := newVideoContext()
	ctx.now = func() time.Time {
		return time.Date(2026, 7, 5, 12, 34, 56, 789, time.UTC)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatal(err)
		}
	})

	framesDir, cleanup, err := ctx.prepareFramesDir("", "transfergo-encode-*", false)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if filepath.Dir(framesDir) != "." {
		t.Fatalf("frames dir parent = %q, want current directory", filepath.Dir(framesDir))
	}
	if filepath.Base(framesDir) != "transfergo-encode-20260705123456_000000000" {
		t.Fatalf("frames dir = %q, want fixed timestamp name", framesDir)
	}
	if _, err := os.Stat(filepath.Join(dir, framesDir)); err != nil {
		t.Fatalf("frames dir was not created under current directory: %v", err)
	}
}

func TestVideoSortedFramePaths(t *testing.T) {
	ctx := newVideoContext()
	dir := t.TempDir()
	for _, name := range []string{"frame_000003.png", "frame_000001.png", "frame_000002.png"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	paths, err := ctx.sortedFramePaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{filepath.Base(paths[0]), filepath.Base(paths[1]), filepath.Base(paths[2])}
	want := []string{"frame_000001.png", "frame_000002.png", "frame_000003.png"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sorted frame paths = %v, want %v", got, want)
	}
}

func TestVideoResolveFfmpegPath(t *testing.T) {
	ctx := newVideoContext()
	ctx.getenv = func(key string) string {
		if key == "FFMPEG_PATH" {
			return "/env/ffmpeg"
		}
		return ""
	}

	if got := ctx.resolveFfmpegPath("/flag/ffmpeg"); got != "/flag/ffmpeg" {
		t.Fatalf("explicit ffmpeg path = %q, want /flag/ffmpeg", got)
	}
	if got := ctx.resolveFfmpegPath(""); got != "/env/ffmpeg" {
		t.Fatalf("env ffmpeg path = %q, want /env/ffmpeg", got)
	}

	ctx.getenv = func(string) string { return "" }
	if got := ctx.resolveFfmpegPath(""); got != "ffmpeg" {
		t.Fatalf("default ffmpeg path = %q, want ffmpeg", got)
	}
}

func TestVideoEncodeFfmpegCommand(t *testing.T) {
	ctx := newVideoContext()
	ctx.lookPath = func(name string) (string, error) {
		if name != "ffmpeg" {
			t.Fatalf("lookPath name = %q, want ffmpeg", name)
		}
		return "/bin/ffmpeg", nil
	}
	var gotName string
	var gotArgs []string
	ctx.runCommand = func(name string, args ...string) error {
		gotName = name
		gotArgs = append([]string{}, args...)
		return nil
	}

	err := ctx.encodeVideoWithFfmpeg("", "frames", "out.mp4", 29.97, 23)
	if err != nil {
		t.Fatal(err)
	}
	if gotName != "ffmpeg" {
		t.Fatalf("command name = %q, want ffmpeg", gotName)
	}
	wantArgs := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-framerate", "29.97",
		"-i", filepath.Join("frames", "frame_%06d.png"),
		"-c:v", "libx264",
		"-preset", "slow",
		"-crf", "23",
		"-pix_fmt", "yuv420p",
		"out.mp4",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("ffmpeg args = %v, want %v", gotArgs, wantArgs)
	}
}

func TestVideoEncodeFfmpegReportsMissingCommand(t *testing.T) {
	ctx := newVideoContext()
	ctx.lookPath = func(string) (string, error) {
		return "", errors.New("not found")
	}

	err := ctx.encodeVideoWithFfmpeg("", "frames", "out.mp4", 1, 24)
	if err == nil || !strings.Contains(err.Error(), "ffmpeg not found") {
		t.Fatalf("encode error = %v, want ffmpeg not found", err)
	}
}
