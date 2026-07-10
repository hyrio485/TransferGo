package core

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type VideoContext struct {
	ffmpegPath string
	getenv     func(string) string
	lookPath   func(string) (string, error)
	runCommand func(string, ...string) error
	now        func() time.Time
}

func NewVideoContext() VideoContext {
	return VideoContext{
		getenv:     os.Getenv,
		lookPath:   exec.LookPath,
		runCommand: runCommand,
		now:        time.Now,
	}
}

func (ctx VideoContext) PrepareFramesDir(specified string, tempPrefix string, keepTemp bool) (string, func(), error) {
	// 指定了 specified 将直接使用指定的目录；未指定将以 tempPrefix+时间戳 作为自动目录，清理阶段按 keepTemp 决定是否保留。
	if specified != "" {
		// 显式目录由调用方拥有：这里只确保目录存在且没有旧帧，不会因为 keep=false 而删除它。
		if err := os.MkdirAll(specified, 0755); err != nil {
			return "", nil, E("create frames directory", err)
		}
		existing, err := filepath.Glob(filepath.Join(specified, "frame_*.png"))
		if err != nil {
			return "", nil, E("check existing frame files", err)
		}
		if len(existing) > 0 {
			return "", nil, fmt.Errorf("%s already contains frame_*.png files; choose an empty frames directory", specified)
		}
		return specified, func() {}, nil
	}

	// 自动目录由本次运行拥有：用 pattern 中的 "*" 替换为时间戳，清理阶段再按 keep 决定是否保留。
	tmp := tempPrefix + ctx.now().Format("20060102150405")
	if err := os.Mkdir(tmp, 0755); err != nil {
		return "", nil, E("create temporary frames directory", err)
	}
	cleanup := func() {
		// 清理是尽力而为，因为主操作已经完成；暴露临时目录删除失败只会增加噪声，帮助不大。
		if !keepTemp {
			_ = os.RemoveAll(tmp)
		}
	}
	return tmp, cleanup, nil
}

func (ctx VideoContext) EncodeVideo(ffmpegPath string, framesDir string, output string, fps float64, crf int) error {
	if err := ctx.runWithFfmpeg(
		ffmpegPath,
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
	); err != nil {
		return E("run ffmpeg encode command", err)
	}
	return nil
}

func (ctx VideoContext) ExtractFrames(ffmpegPath string, input string, framesDir string, sampleFPS float64) error {
	if err := ctx.runWithFfmpeg(
		ffmpegPath,
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-i", input,
		"-vf", "fps="+formatFPS(sampleFPS),
		filepath.Join(framesDir, "frame_%06d.png"),
	); err != nil {
		return E("run ffmpeg extract command", err)
	}
	return nil
}

// region tools

func (ctx VideoContext) runWithFfmpeg(ffmpegPath string, args ...string) error {
	ffmpeg := "ffmpeg"
	if ffmpegPath != "" {
		ffmpeg = ffmpegPath
	} else if env := ctx.getenv("FFMPEG_PATH"); env != "" {
		ffmpeg = env
	}
	if _, err := ctx.lookPath(ffmpeg); err != nil && !strings.Contains(ffmpeg, string(os.PathSeparator)) {
		return fmt.Errorf("ffmpeg not found; pass -ffmpeg <path>, set FFMPEG_PATH, or make ffmpeg available in PATH")
	}
	if err := ctx.runCommand(ffmpeg, args...); err != nil {
		return E("run ffmpeg command", err)
	}
	return nil
}

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

// endregion
