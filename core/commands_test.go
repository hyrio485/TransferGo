package core

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestCommandParseEncodeOptions(t *testing.T) {
	ctx := NewCommandContext(&bytes.Buffer{}, &bytes.Buffer{})

	opt, err := ctx.ParseEncodeOptions([]string{
		"-i", "input.bin",
		"-o", "out.mp4",
		"-p", "secret",
		"-fps", "6",
		"-grid-size", "2",
		"-chunk-size", "128",
		"-keep-frames",
	}, defaultEncodeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if opt.input != "input.bin" || opt.output != "out.mp4" || opt.password != "secret" {
		t.Fatalf("parsed paths/password = %#v", opt)
	}
	if opt.fps != 6 || opt.gridSize != 2 || opt.chunkSize != 128 || !opt.keep {
		t.Fatalf("parsed numeric flags = %#v", opt)
	}
}

func TestCommandParseDecodeOptionsRejectsInvalidSampleFPS(t *testing.T) {
	ctx := NewCommandContext(&bytes.Buffer{}, &bytes.Buffer{})

	_, err := ctx.ParseDecodeOptions([]string{"-i", "video.mp4", "-sample-fps", "0"}, defaultDecodeOptions())
	if err == nil || !strings.Contains(err.Error(), "-sample-fps must be greater than 0") {
		t.Fatalf("parse decode error = %v, want sample fps validation", err)
	}
}

func TestCommandPrintUsage(t *testing.T) {
	ctx := NewCommandContext(&bytes.Buffer{}, &bytes.Buffer{})
	var out bytes.Buffer

	ctx.PrintUsage(&out)

	if !strings.Contains(out.String(), "transfergo encode") || !strings.Contains(out.String(), "transfergo decode") {
		t.Fatalf("usage = %q, want encode and decode commands", out.String())
	}
}

func TestCommandProgressPrinterThrottlesIntermediateOutput(t *testing.T) {
	var out bytes.Buffer
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	ctx := NewCommandContext(&out, &bytes.Buffer{})
	ctx.now = func() time.Time { return now }

	progress := ctx.NewProgressPrinter("work")
	progress(1, 3)
	progress(2, 3)
	progress(3, 3)

	lines := strings.Count(out.String(), "\n")
	if lines != 2 {
		t.Fatalf("progress lines = %d, want 2; output = %q", lines, out.String())
	}
}
