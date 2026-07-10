package core

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// VideoContext 封装 ffmpeg 查找、命令执行和临时帧目录管理所需的依赖。
type VideoContext struct {
	ffmpegPath string
	getenv     func(string) string
	lookPath   func(string) (string, error)
	runCommand func(string, ...string) error
}

// NewVideoContext 创建使用当前环境变量和系统命令执行器的视频上下文。
func NewVideoContext() VideoContext {
	return VideoContext{
		getenv:     os.Getenv,
		lookPath:   exec.LookPath,
		runCommand: runCommand,
	}
}

// PrepareFramesDir 准备帧目录并返回清理函数。
// 显式目录归调用方所有，不会被自动删除；自动目录则由 keepTemp 决定是否保留。
func (ctx VideoContext) PrepareFramesDir(specified string, tempPrefix string, keepTemp bool) (string, func(), error) {
	// 指定了 specified 将直接使用指定的目录；未指定将创建唯一的自动目录，清理阶段按 keepTemp 决定是否保留。
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

	// 自动目录由本次运行拥有，随机后缀可以避免并发任务发生目录冲突。
	tmp, err := os.MkdirTemp(".", tempPrefix)
	if err != nil {
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

// EncodeVideo 调用 ffmpeg，把连续编号的 PNG 帧编码为 H.264 视频。
func (ctx VideoContext) EncodeVideo(ffmpegPath string, framesDir string, output string, fps float64, crf int, replace bool) error {
	// 默认使用 -n 保护已有文件，只有调用方明确允许替换时才传递 -y。
	overwriteFlag := "-n"
	if replace {
		overwriteFlag = "-y"
	}
	if err := ctx.runWithFfmpeg(
		ffmpegPath,
		"-hide_banner",
		"-loglevel", "error",
		overwriteFlag,
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

// ExtractFrames 按指定采样帧率从输入视频中抽取连续编号的 PNG 图片。
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

// runWithFfmpeg 按命令参数、环境变量和 PATH 的优先级解析 ffmpeg，再执行命令。
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

// runCommand 合并收集子进程输出，仅在命令失败时把输出附加到错误信息中。
func runCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(output))
		if msg == "" {
			return fmt.Errorf("run command %s: %w", name, err)
		}
		return fmt.Errorf("run command %s: %w: %s", name, err, msg)
	}
	return nil
}

// endregion
