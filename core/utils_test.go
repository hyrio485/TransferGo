package core

import (
	"bytes"
	"errors"
	"testing"
)

// 本文件测试不涉及标准流的通用辅助函数。
// 运行方式：在仓库根目录执行 go test ./...，或执行 go test ./core -run 'Concat|Error|FPS'。
// 期望影响：测试只操作内存，不创建文件、不输出日志、不启动外部进程。

// TestLogAddsPrefixAndColor 验证底层日志格式包含前缀和颜色。
// 前置条件：使用 bytes.Buffer 代替标准输出，避免污染测试终端。
// 执行方式：向 log 传入颜色、前缀、格式字符串和格式参数。
// 期望结果：缓冲区内容依次包含颜色、前缀、颜色重置符和格式化正文。
func TestLogAddsPrefixAndColor(t *testing.T) {
	var output bytes.Buffer
	log(&output, colorGreen, prefixInfo, "message %d", 1)
	want := colorGreen + prefixInfo + colorReset + "message 1"
	if output.String() != want {
		t.Fatalf("log output = %q, want %q", output.String(), want)
	}
}

// TestConcatByteArrays 验证字节切片拼接顺序和结果独立性。
// 前置条件：准备多个普通、空和 nil 字节切片。
// 执行方式：调用 ConcatByteArrays，并在调用后修改原始切片。
// 期望结果：返回值顺序正确，且不会随原始切片变化。
func TestConcatByteArrays(t *testing.T) {
	first := []byte{1, 2}
	second := []byte{3, 4}
	got := ConcatByteArrays(first, nil, []byte{}, second)
	first[0] = 9
	want := []byte{1, 2, 3, 4}
	if len(got) != len(want) {
		t.Fatalf("ConcatByteArrays() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ConcatByteArrays() = %v, want %v", got, want)
		}
	}
}

// TestErrorWrapperPreservesCause 验证 E 补充上下文后仍保留错误链。
// 前置条件：准备一个可通过 errors.Is 识别的哨兵错误。
// 执行方式：使用 E 包装哨兵错误。
// 期望结果：错误文本包含处理步骤，并且 errors.Is 仍能匹配原错误。
func TestErrorWrapperPreservesCause(t *testing.T) {
	cause := errors.New("cause")
	err := E("step", cause)
	if err.Error() != "step：cause" || !errors.Is(err, cause) {
		t.Fatalf("E() = %v", err)
	}
}

// TestFormatFPS 验证传给 ffmpeg 的帧率字符串格式。
// 前置条件：不需要外部命令。
// 执行方式：格式化整数、小数和高精度帧率。
// 期望结果：输出不带多余零，并保留必要精度。
func TestFormatFPS(t *testing.T) {
	tests := []struct {
		fps  float64
		want string
	}{
		{fps: 3, want: "3"},
		{fps: 3.5, want: "3.5"},
		{fps: 29.97, want: "29.97"},
	}
	for _, test := range tests {
		if got := formatFPS(test.fps); got != test.want {
			t.Fatalf("formatFPS(%v) = %q, want %q", test.fps, got, test.want)
		}
	}
}
