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

// videoContext 持有视频辅助函数使用的文件系统、环境和进程钩子。测试可以替换这些钩子，而不牵涉命令或二维码代码。
type videoContext struct {
	getenv     func(string) string
	lookPath   func(string) (string, error)
	runCommand func(string, ...string) error
	now        func() time.Time
}

// newVideoContext 连接生产环境的文件系统、环境变量、命令查找和进程执行钩子。
func newVideoContext() videoContext {
	return videoContext{
		getenv:     os.Getenv,
		lookPath:   exec.LookPath,
		runCommand: runCommand,
		now:        time.Now,
	}
}

// prepareFramesDir 返回用于中间 PNG 帧的目录和清理回调。用户提供的目录不能已包含帧文件，否则旧帧可能混入新的 encode/decode 运行。
// dir 非空时直接作为帧目录使用；pattern 只在 dir 为空时用于生成自动目录，例如 "transfergo-encode-*" 会把 "*" 替换为时间戳。
// keep 只控制自动创建目录的清理行为；对已存在或用户显式传入的 dir 不生效，因为该分支始终返回空清理函数。
func (ctx videoContext) prepareFramesDir(dir string, pattern string, keep bool) (string, func(), error) {
	if dir != "" {
		// 显式目录由调用方拥有：这里只确保目录存在且没有旧帧，不会因为 keep=false 而删除它。
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

	// 自动目录由本次运行拥有：用 pattern 中的 "*" 替换为时间戳，清理阶段再按 keep 决定是否保留。
	tmp := strings.Replace(pattern, "*", ctx.now().Format("20060102150405"), 1)
	if err := os.Mkdir(tmp, 0755); err != nil {
		return "", nil, fmt.Errorf("create temporary frames directory: %w", err)
	}
	cleanup := func() {
		// 清理是尽力而为，因为主操作已经完成；暴露临时目录删除失败只会增加噪声，帮助不大。
		if !keep {
			_ = os.RemoveAll(tmp)
		}
	}
	return tmp, cleanup, nil
}

// sortedFramePaths 按顺序返回抽取出的帧。帧名带零填充，所以字典序排序等同于数字排序。
func (ctx videoContext) sortedFramePaths(dir string) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "frame_*.png"))
	if err != nil {
		return nil, fmt.Errorf("glob frame files: %w", err)
	}
	sort.Strings(paths)
	return paths, nil
}

// encodeVideoWithFfmpeg 把 PNG 序列转成 H.264 MP4。输出使用 yuv420p，因为它能在常见播放器和手机上正确预览。
func (ctx videoContext) encodeVideoWithFfmpeg(ffmpegPath string, framesDir string, output string, fps float64, crf int) error {
	ffmpegPath = ctx.resolveFfmpegPath(ffmpegPath)
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

// extractFramesWithFfmpeg 把输入视频采样为 PNG。高于源帧率的采样可能产生重复帧，二维码收集步骤会有意容忍这种情况。
func (ctx videoContext) extractFramesWithFfmpeg(ffmpegPath string, input string, framesDir string, sampleFPS float64) error {
	ffmpegPath = ctx.resolveFfmpegPath(ffmpegPath)
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

// resolveFfmpegPath 应用文档中的查找顺序：显式 -ffmpeg、FFMPEG_PATH，然后是通过 PATH 解析的普通 "ffmpeg" 命令。
func (ctx videoContext) resolveFfmpegPath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if env := ctx.getenv("FFMPEG_PATH"); env != "" {
		return env
	}
	return "ffmpeg"
}

// runCommand 捕获 ffmpeg 输出并附加到返回的错误中。这样成功运行时保持安静，失败时仍保留有用诊断信息。
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
