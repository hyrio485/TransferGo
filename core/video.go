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

// prepareFramesDir returns a directory for intermediate PNG frames plus a
// cleanup callback. User-provided directories must not already contain frame
// files, otherwise stale frames could be mixed into a new encode/decode run.
// Default directories are created under the current working directory and end
// with a timestamp.
func prepareFramesDir(dir string, pattern string, keep bool) (string, func(), error) {
	if dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", nil, err
		}
		existing, err := filepath.Glob(filepath.Join(dir, "frame_*.png"))
		if err != nil {
			return "", nil, err
		}
		if len(existing) > 0 {
			return "", nil, fmt.Errorf("%s already contains frame_*.png files; choose an empty frames directory", dir)
		}
		return dir, func() {}, nil
	}

	tmp := strings.Replace(pattern, "*", time.Now().Format("20060102150405_000000000"), 1)
	if err := os.Mkdir(tmp, 0755); err != nil {
		return "", nil, err
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
func sortedFramePaths(dir string) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "frame_*.png"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

// encodeVideoWithFFmpeg turns a PNG sequence into an H.264 MP4. The output uses
// yuv420p because it previews correctly in common players and phones.
func encodeVideoWithFFmpeg(ffmpegPath string, framesDir string, output string, fps float64, crf int) error {
	ffmpegPath = resolveFFmpegPath(ffmpegPath)
	if _, err := exec.LookPath(ffmpegPath); err != nil && !strings.Contains(ffmpegPath, string(os.PathSeparator)) {
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
	return runCommand(ffmpegPath, args...)
}

// extractFramesWithFFmpeg samples the input video into PNGs. Sampling more often
// than the source frame rate can create duplicates, which the QR collection step
// intentionally tolerates.
func extractFramesWithFFmpeg(ffmpegPath string, input string, framesDir string, sampleFPS float64) error {
	ffmpegPath = resolveFFmpegPath(ffmpegPath)
	if _, err := exec.LookPath(ffmpegPath); err != nil && !strings.Contains(ffmpegPath, string(os.PathSeparator)) {
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
	return runCommand(ffmpegPath, args...)
}

// resolveFFmpegPath applies the documented lookup order: explicit -ffmpeg,
// FFMPEG_PATH, then the plain "ffmpeg" command resolved through PATH.
func resolveFFmpegPath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if env := os.Getenv("FFMPEG_PATH"); env != "" {
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
			return err
		}
		return fmt.Errorf("%w: %s", err, msg)
	}
	return nil
}

// formatFPS avoids trailing zeros in ffmpeg arguments while preserving precise
// decimal values such as 0.5 or 29.97.
func formatFPS(fps float64) string {
	return strconv.FormatFloat(fps, 'f', -1, 64)
}
