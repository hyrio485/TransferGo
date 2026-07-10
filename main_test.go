package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"hyrio.xyz/transfergo/core"
)

// 本文件测试命令主流程中的纯函数和文件辅助逻辑。
// 运行方式：在仓库根目录执行 go test ./...，或执行 go test .。
// 文件影响：所有测试文件都位于 t.TempDir 返回的系统临时目录，测试结束后由 Go 自动清理。

// TestRunClassifiesUsageErrors 验证参数错误与运行错误的分类。
// 前置条件：只初始化命令上下文，不需要输入文件、二维码或 ffmpeg。
// 执行方式：分别传入缺少命令、未知命令、encode 缺参、decode 缺参和普通运行错误。
// 期望结果：四种参数错误都要求打印用法，普通运行错误不要求打印用法，底层错误链保持可识别。
func TestRunClassifiesUsageErrors(t *testing.T) {
	app := appContext{commands: core.NewCommandContext()}
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing command", args: nil},
		{name: "unknown command", args: []string{"unknown"}},
		{name: "encode missing options", args: []string{"encode"}},
		{name: "decode missing options", args: []string{"decode"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := app.Run(test.args)
			if err == nil || !shouldPrintUsage(err) {
				t.Fatalf("Run(%q) error = %v, want usage error", test.args, err)
			}
		})
	}

	runtimeErr := errors.New("runtime failure")
	if shouldPrintUsage(runtimeErr) {
		t.Fatal("plain runtime error was classified as usage error")
	}
	wrapped := withUsage(runtimeErr)
	if !errors.Is(wrapped, runtimeErr) {
		t.Fatal("usage error does not preserve its cause")
	}
}

// TestDecodedOutputPath 验证默认输出文件名的路径安全策略。
// 前置条件：不需要真实文件，输入仅为调用方路径和外部清单文件名。
// 执行方式：覆盖显式路径、安全文件名、空文件名和多种路径穿越形式。
// 期望结果：显式路径原样返回，安全清单名称可用，危险清单名称全部被拒绝。
func TestDecodedOutputPath(t *testing.T) {
	tests := []struct {
		name         string
		requested    string
		manifestName string
		want         string
		wantErr      bool
	}{
		{name: "requested", requested: "chosen.bin", manifestName: "../ignored.bin", want: "chosen.bin"},
		{name: "manifest", manifestName: "original.bin", want: "original.bin"},
		{name: "fallback", want: "decoded.bin"},
		{name: "current directory", manifestName: ".", wantErr: true},
		{name: "parent directory", manifestName: "..", wantErr: true},
		{name: "nested path", manifestName: "nested/escape.bin", wantErr: true},
		{name: "parent path", manifestName: "../escape.bin", wantErr: true},
		{name: "absolute path", manifestName: filepath.Join(string(filepath.Separator), "tmp", "escape.bin"), wantErr: true},
		{name: "windows separator", manifestName: `..\escape.bin`, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodedOutputPath(test.requested, test.manifestName)
			if (err != nil) != test.wantErr {
				t.Fatalf("decodedOutputPath() error = %v, wantErr %t", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("decodedOutputPath() = %q, want %q", got, test.want)
			}
		})
	}
}

// TestWriteOutputFileReplacePolicy 验证输出文件的排他创建和原子替换策略。
// 前置条件：目标路径位于 t.TempDir，测试开始时不存在。
// 执行方式：依次执行首次写入、禁止替换写入和允许替换写入。
// 期望结果：禁止替换时保留原内容，允许替换时写入新内容，且不遗留临时文件。
func TestWriteOutputFileReplacePolicy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.bin")
	if err := writeOutputFile(path, []byte("first"), false); err != nil {
		t.Fatal(err)
	}
	if err := writeOutputFile(path, []byte("second"), false); err == nil {
		t.Fatal("expected existing output error")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first" {
		t.Fatalf("existing output changed to %q", data)
	}
	if err := writeOutputFile(path, []byte("second"), true); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second" {
		t.Fatalf("replaced output = %q", data)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "output.bin" {
		t.Fatalf("unexpected files after replacement: %v", entries)
	}
}

// TestRenderedFrameCount 验证二维码网格对应的视频帧数量计算。
// 前置条件：不需要文件系统或外部命令。
// 执行方式：覆盖空载荷、整除、存在余数、无效网格和乘法溢出输入。
// 期望结果：正常输入向上取整，无效或溢出输入返回零。
func TestRenderedFrameCount(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	tests := []struct {
		name         string
		payloadCount int
		rows         int
		cols         int
		want         int
	}{
		{name: "empty", payloadCount: 0, rows: 3, cols: 3, want: 0},
		{name: "single", payloadCount: 1, rows: 3, cols: 3, want: 1},
		{name: "exact", payloadCount: 18, rows: 3, cols: 3, want: 2},
		{name: "remainder", payloadCount: 19, rows: 3, cols: 3, want: 3},
		{name: "invalid rows", payloadCount: 1, rows: 0, cols: 3, want: 0},
		{name: "overflow", payloadCount: 1, rows: maxInt, cols: 2, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := renderedFrameCount(test.payloadCount, test.rows, test.cols); got != test.want {
				t.Fatalf("renderedFrameCount() = %d, want %d", got, test.want)
			}
		})
	}
}

// TestCollectPayloadsFromImages 验证二维码图片收集时对不可读图片的容错。
// 前置条件：在 t.TempDir 中生成一张有效二维码图片和一张无效图片，不依赖 ffmpeg。
// 执行方式：按顺序收集两张图片，并记录进度回调参数。
// 期望结果：返回有效载荷，不可读数量为一，最终进度为二分之二。
func TestCollectPayloadsFromImages(t *testing.T) {
	dir := t.TempDir()
	validPath := filepath.Join(dir, "frame_000001.png")
	invalidPath := filepath.Join(dir, "frame_000002.png")
	wantPayload := []byte("payload")
	if err := core.EncodeMultiByteArraysToSinglePng([][]byte{wantPayload}, validPath, 240, 1, 1, 300, 300); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(invalidPath, []byte("not an image"), 0644); err != nil {
		t.Fatal(err)
	}

	progressDone := 0
	progressTotal := 0
	payloads, unreadable, err := collectPayloadsFromImages(
		[]string{validPath, invalidPath},
		func(done int, total int) {
			progressDone = done
			progressTotal = total
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 1 || !bytes.Equal(payloads[0], wantPayload) {
		t.Fatalf("payloads = %q", payloads)
	}
	if unreadable != 1 {
		t.Fatalf("unreadable = %d, want 1", unreadable)
	}
	if progressDone != 2 || progressTotal != 2 {
		t.Fatalf("progress = %d/%d, want 2/2", progressDone, progressTotal)
	}
}

// TestSortedFramePaths 验证帧文件筛选和排序行为。
// 前置条件：所有样例文件都创建在 t.TempDir 中。
// 执行方式：混合创建乱序帧文件和无关文件，然后读取帧路径。
// 期望结果：只返回 frame_*.png，并按文件名升序排列。
func TestSortedFramePaths(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"frame_000002.png", "ignored.png", "frame_000001.png"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := sortedFramePaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(dir, "frame_000001.png"),
		filepath.Join(dir, "frame_000002.png"),
	}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths = %v, want %v", paths, want)
		}
	}
}
