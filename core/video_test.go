package core

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// 本文件通过依赖注入测试帧目录和 ffmpeg 命令构造，不启动真实 ffmpeg。
// 运行方式：在仓库根目录执行 go test ./...，或执行 go test ./core -run Video。
// 文件影响：涉及当前目录的测试会先使用 t.Chdir 切换到 t.TempDir，测试结束后自动恢复并清理。

// TestPrepareFramesDirCreatesUniqueDirectories 验证自动帧目录的唯一性和清理行为。
// 前置条件：测试当前目录已经切换到 t.TempDir。
// 执行方式：连续创建两个自动目录，再分别调用返回的清理函数。
// 期望结果：两个目录路径不同，清理后都不存在，仓库目录不产生文件。
func TestPrepareFramesDirCreatesUniqueDirectories(t *testing.T) {
	t.Chdir(t.TempDir())
	ctx := NewVideoContext()
	first, cleanupFirst, err := ctx.PrepareFramesDir("", "transfergo-test-", false)
	if err != nil {
		t.Fatal(err)
	}
	second, cleanupSecond, err := ctx.PrepareFramesDir("", "transfergo-test-", false)
	if err != nil {
		cleanupFirst()
		t.Fatal(err)
	}
	if first == second {
		cleanupFirst()
		cleanupSecond()
		t.Fatalf("temporary directories are equal: %q", first)
	}
	cleanupFirst()
	cleanupSecond()
	for _, path := range []string{first, second} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("temporary directory %q still exists: %v", path, err)
		}
	}
}

// TestPrepareFramesDirKeepAndExplicitOwnership 验证自动保留和显式目录归属规则。
// 前置条件：自动目录和显式目录都位于 t.TempDir。
// 执行方式：创建 keepTemp 自动目录和显式目录，并调用各自清理函数。
// 期望结果：两个目录都继续存在；显式目录出现旧帧后，再次准备会被拒绝。
func TestPrepareFramesDirKeepAndExplicitOwnership(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	ctx := NewVideoContext()

	automatic, cleanupAutomatic, err := ctx.PrepareFramesDir("", "transfergo-keep-", true)
	if err != nil {
		t.Fatal(err)
	}
	cleanupAutomatic()
	if info, err := os.Stat(automatic); err != nil || !info.IsDir() {
		t.Fatalf("kept automatic directory is unavailable: %v", err)
	}

	explicit := filepath.Join(root, "explicit")
	got, cleanupExplicit, err := ctx.PrepareFramesDir(explicit, "unused-", false)
	if err != nil {
		t.Fatal(err)
	}
	cleanupExplicit()
	if got != explicit {
		t.Fatalf("explicit directory = %q, want %q", got, explicit)
	}
	framePath := filepath.Join(explicit, "frame_000001.png")
	if err := os.WriteFile(framePath, []byte("frame"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ctx.PrepareFramesDir(explicit, "unused-", false); err == nil {
		t.Fatal("PrepareFramesDir accepted explicit directory with existing frames")
	}
}

// TestEncodeVideoBuildsExpectedCommand 验证视频编码参数和覆盖策略。
// 前置条件：使用内存中的 mock 替换可执行文件查找和命令运行函数。
// 执行方式：分别以 replace=false 和 replace=true 调用 EncodeVideo。
// 期望结果：命令包含正确输入模式、帧率、CRF，以及对应的 -n 或 -y 参数。
func TestEncodeVideoBuildsExpectedCommand(t *testing.T) {
	var command string
	var args []string
	ctx := newMockVideoContext(func(name string, commandArgs ...string) error {
		command = name
		args = append([]string{}, commandArgs...)
		return nil
	})
	framesDir := t.TempDir()

	if err := ctx.EncodeVideo("/custom/ffmpeg", framesDir, "output.mp4", 3.5, 20, false); err != nil {
		t.Fatal(err)
	}
	if command != "/custom/ffmpeg" || !slices.Contains(args, "-n") || slices.Contains(args, "-y") {
		t.Fatalf("unexpected no-replace command: %q %v", command, args)
	}
	assertArgumentPair(t, args, "-framerate", "3.5")
	assertArgumentPair(t, args, "-crf", "20")
	assertArgumentPair(t, args, "-i", filepath.Join(framesDir, "frame_%06d.png"))

	if err := ctx.EncodeVideo("/custom/ffmpeg", framesDir, "output.mp4", 3, 24, true); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(args, "-y") || slices.Contains(args, "-n") {
		t.Fatalf("unexpected replace command: %v", args)
	}
}

// TestExtractFramesBuildsExpectedCommand 验证视频抽帧参数。
// 前置条件：使用 mock 命令执行器，不要求输入视频真实存在。
// 执行方式：调用 ExtractFrames，并捕获传给 ffmpeg 的参数。
// 期望结果：命令包含输入视频、fps 滤镜和临时目录中的输出模式。
func TestExtractFramesBuildsExpectedCommand(t *testing.T) {
	var args []string
	ctx := newMockVideoContext(func(_ string, commandArgs ...string) error {
		args = append([]string{}, commandArgs...)
		return nil
	})
	framesDir := t.TempDir()
	if err := ctx.ExtractFrames("/custom/ffmpeg", "input.mp4", framesDir, 9.5); err != nil {
		t.Fatal(err)
	}
	assertArgumentPair(t, args, "-i", "input.mp4")
	assertArgumentPair(t, args, "-vf", "fps=9.5")
	if got := args[len(args)-1]; got != filepath.Join(framesDir, "frame_%06d.png") {
		t.Fatalf("output pattern = %q", got)
	}
}

// TestRunWithFfmpegResolutionOrder 验证 ffmpeg 的查找优先级。
// 前置条件：命令执行和 PATH 查找均由内存 mock 提供。
// 执行方式：分别提供显式路径、环境变量路径和默认 PATH 名称。
// 期望结果：实际执行名称依次采用显式参数、FFMPEG_PATH、ffmpeg。
func TestRunWithFfmpegResolutionOrder(t *testing.T) {
	tests := []struct {
		name     string
		explicit string
		env      string
		want     string
	}{
		{name: "explicit", explicit: "/explicit/ffmpeg", env: "/env/ffmpeg", want: "/explicit/ffmpeg"},
		{name: "environment", env: "/env/ffmpeg", want: "/env/ffmpeg"},
		{name: "path", want: "ffmpeg"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var command string
			ctx := VideoContext{
				getenv: func(name string) string {
					if name == "FFMPEG_PATH" {
						return test.env
					}
					return ""
				},
				lookPath: func(path string) (string, error) {
					return path, nil
				},
				runCommand: func(name string, _ ...string) error {
					command = name
					return nil
				},
			}
			if err := ctx.runWithFfmpeg(test.explicit, "-version"); err != nil {
				t.Fatal(err)
			}
			if command != test.want {
				t.Fatalf("ffmpeg command = %q, want %q", command, test.want)
			}
		})
	}
}

// TestRunWithFfmpegRejectsMissingExecutable 验证默认 ffmpeg 不存在时的错误。
// 前置条件：mock PATH 查找始终返回错误。
// 执行方式：不提供显式路径或环境变量，并调用 runWithFfmpeg。
// 期望结果：返回 ffmpeg not found，且不会调用命令执行器。
func TestRunWithFfmpegRejectsMissingExecutable(t *testing.T) {
	runCalled := false
	ctx := VideoContext{
		getenv: func(string) string { return "" },
		lookPath: func(string) (string, error) {
			return "", errors.New("not found")
		},
		runCommand: func(string, ...string) error {
			runCalled = true
			return nil
		},
	}
	err := ctx.runWithFfmpeg("", "-version")
	if err == nil || !strings.Contains(err.Error(), "ffmpeg not found") {
		t.Fatalf("runWithFfmpeg() error = %v", err)
	}
	if runCalled {
		t.Fatal("runWithFfmpeg called command runner after lookup failure")
	}
}

// newMockVideoContext 创建不会启动外部进程的视频上下文。
func newMockVideoContext(run func(string, ...string) error) VideoContext {
	return VideoContext{
		getenv: func(string) string { return "" },
		lookPath: func(path string) (string, error) {
			return path, nil
		},
		runCommand: run,
	}
}

// assertArgumentPair 检查命令参数中是否存在连续的名称和值。
func assertArgumentPair(t *testing.T, args []string, name string, value string) {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name && args[i+1] == value {
			return
		}
	}
	t.Fatalf("argument pair %q %q not found in %v", name, value, args)
}
