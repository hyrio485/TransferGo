package core

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// videoContext owns filesystem, environment, and process hooks used by video
// helpers. Tests can replace these hooks without involving command or QR code.
type videoContext struct {
	getenv     func(string) string
	lookPath   func(string) (string, error)
	runCommand func(string, ...string) error
	now        func() time.Time
}

// newVideoContext wires production filesystem, environment, command lookup,
// and process execution hooks.
func newVideoContext() videoContext {
	return videoContext{
		getenv:     os.Getenv,
		lookPath:   exec.LookPath,
		runCommand: runCommand,
		now:        time.Now,
	}
}

// prepareFramesDir returns a directory for intermediate PNG frames plus a
// cleanup callback. User-provided directories must not already contain frame
// files, otherwise stale frames could be mixed into a new encode/decode run.
// Default directories are created under the current working directory and end
// with a timestamp.
func (ctx videoContext) prepareFramesDir(dir string, pattern string, keep bool) (string, func(), error) {
	if dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", nil, fmt.Errorf("create frames directory: %w", err)
		}
		existing, err := filepath.Glob(filepath.Join(dir, "frame_*.png"))
		if err != nil {
			return "", nil, fmt.Errorf("check existing frame files: %w", err)
		}
		if len(existing) > 0 {
			return "", nil, fmt.Errorf("%s already contains frame_*.png files; choose an empty frames directory", dir)
		}
		return dir, func() {}, nil
	}

	tmp := strings.Replace(pattern, "*", ctx.now().Format("20060102150405_000000000"), 1)
	if err := os.Mkdir(tmp, 0755); err != nil {
		return "", nil, fmt.Errorf("create temporary frames directory: %w", err)
	}
	cleanup := func() {
		// Cleanup is best-effort because the main operation has already finished;
		// surfacing a temp removal failure would be noisier than helpful.
		if !keep {
			_ = os.RemoveAll(tmp)
		}
	}
	return tmp, cleanup, nil
}

// sortedFramePaths returns extracted frames in sequence order. The frame names
// are zero-padded, so lexical sorting is the same as numeric sorting.
func (ctx videoContext) sortedFramePaths(dir string) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "frame_*.png"))
	if err != nil {
		return nil, fmt.Errorf("glob frame files: %w", err)
	}
	sort.Strings(paths)
	return paths, nil
}

// encodeVideoWithFFmpeg turns a PNG sequence into an H.264 MP4. The output uses
// yuv420p because it previews correctly in common players and phones.
func (ctx videoContext) encodeVideoWithFFmpeg(ffmpegPath string, framesDir string, output string, fps float64, crf int) error {
	ffmpegPath = ctx.resolveFFmpegPath(ffmpegPath)
	if _, err := ctx.lookPath(ffmpegPath); err != nil && !strings.Contains(ffmpegPath, string(os.PathSeparator)) {
		return fmt.Errorf("ffmpeg not found; pass -ffmpeg <path>, set FFMPEG_PATH, or make ffmpeg available in PATH")
	}

	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-framerate", formatFPS(fps),
		"-i", filepath.Join(framesDir, "frame_%06d.png"),
		"-c:v", "libx264",
		"-preset", "slow",
		"-crf", strconv.Itoa(crf),
		"-pix_fmt", "yuv420p",
		output,
	}
	if err := ctx.runCommand(ffmpegPath, args...); err != nil {
		return fmt.Errorf("run ffmpeg encode command: %w", err)
	}
	return nil
}

// extractFramesWithFFmpeg samples the input video into PNGs. Sampling more often
// than the source frame rate can create duplicates, which the QR collection step
// intentionally tolerates.
func (ctx videoContext) extractFramesWithFFmpeg(ffmpegPath string, input string, framesDir string, sampleFPS float64) error {
	ffmpegPath = ctx.resolveFFmpegPath(ffmpegPath)
	if _, err := ctx.lookPath(ffmpegPath); err != nil && !strings.Contains(ffmpegPath, string(os.PathSeparator)) {
		return fmt.Errorf("ffmpeg not found; pass -ffmpeg <path>, set FFMPEG_PATH, or make ffmpeg available in PATH")
	}

	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-i", input,
		"-vf", "fps=" + formatFPS(sampleFPS),
		filepath.Join(framesDir, "frame_%06d.png"),
	}
	if err := ctx.runCommand(ffmpegPath, args...); err != nil {
		return fmt.Errorf("run ffmpeg extract command: %w", err)
	}
	return nil
}

// resolveFFmpegPath applies the documented lookup order: explicit -ffmpeg,
// FFMPEG_PATH, then the plain "ffmpeg" command resolved through PATH.
func (ctx videoContext) resolveFFmpegPath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if env := ctx.getenv("FFMPEG_PATH"); env != "" {
		return env
	}
	return "ffmpeg"
}

// runCommand captures ffmpeg output and attaches it to the returned error. This
// keeps successful runs quiet while preserving useful diagnostics on failure.
func runCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	var stderr bytes.Buffer
	cmd.Stdout = &stderr
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return fmt.Errorf("run command %s: %w", name, err)
		}
		return fmt.Errorf("run command %s: %w: %s", name, err, msg)
	}
	return nil
}
