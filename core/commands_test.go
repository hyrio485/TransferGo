package core

import (
	"strconv"
	"strings"
	"testing"
)

// 本文件测试命令参数解析和帮助文本，不访问文件系统，也不启动外部进程。
// 运行方式：在仓库根目录执行 go test ./...，或执行 go test ./core -run Command。
// 期望影响：只在内存中构造参数，不创建或修改任何本地文件。

// TestParseEncodeOptionsDefaults 验证 encode 命令的必填参数和默认值。
// 前置条件：仅提供输入和输出两个必填参数。
// 执行方式：调用 ParseEncodeOptions 解析最小有效参数集合。
// 期望结果：解析成功，其余字段与代码定义的默认值完全一致。
func TestParseEncodeOptionsDefaults(t *testing.T) {
	ctx := NewCommandContext()
	got, err := ctx.ParseEncodeOptions([]string{"-i", "input.bin", "-o", "output.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	want := EncodeOptions{
		Input:       "input.bin",
		Output:      "output.mp4",
		FPS:         defaultFPS,
		QRSize:      defaultQRSize,
		Rows:        defaultRows,
		Cols:        defaultCols,
		ImageWidth:  defaultImageWidth,
		ImageHeight: defaultImageHeight,
		ChunkSize:   defaultChunkSize,
		CRF:         defaultCRF,
	}
	if got != want {
		t.Fatalf("ParseEncodeOptions() = %+v, want %+v", got, want)
	}
}

// TestParseEncodeOptionsAliases 验证 encode 参数别名和自定义值。
// 前置条件：使用长别名，并为所有可配置字段提供有效值。
// 执行方式：调用 ParseEncodeOptions 解析完整参数集合。
// 期望结果：每个字段都保存调用方传入的值，布尔开关为 true。
func TestParseEncodeOptionsAliases(t *testing.T) {
	ctx := NewCommandContext()
	got, err := ctx.ParseEncodeOptions([]string{
		"-in", "input.bin",
		"-out", "output.mp4",
		"-password", "secret",
		"-ffmpeg", "/path/to/ffmpeg",
		"-frames-dir", "frames",
		"-fps", "4.5",
		"-qr-size", "200",
		"-width", "1000",
		"-height", "800",
		"-rows", "2",
		"-cols", "4",
		"-chunk-size", "128",
		"-crf", "20",
		"-replace",
		"-keep-frames",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Input != "input.bin" || got.Output != "output.mp4" || got.Password != "secret" ||
		got.Ffmpeg != "/path/to/ffmpeg" || got.FramesDir != "frames" || got.FPS != 4.5 ||
		got.QRSize != 200 || got.ImageWidth != 1000 || got.ImageHeight != 800 ||
		got.Rows != 2 || got.Cols != 4 || got.ChunkSize != 128 || got.CRF != 20 ||
		!got.Replace || !got.Keep {
		t.Fatalf("unexpected encode options: %+v", got)
	}
}

// TestParseEncodeOptionsRejectsInvalidValues 验证 encode 的全部关键参数边界。
// 前置条件：每个子测试只引入一种无效参数，其他必填参数保持有效。
// 执行方式：逐项解析缺失参数、非法尺寸、非有限帧率和越界 CRF。
// 期望结果：所有无效参数组合都返回错误，不发生文件读写。
func TestParseEncodeOptionsRejectsInvalidValues(t *testing.T) {
	ctx := NewCommandContext()
	maxInt := int(^uint(0) >> 1)
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing input", args: []string{"-o", "output"}},
		{name: "missing output", args: []string{"-i", "input"}},
		{name: "positional argument", args: []string{"-i", "input", "-o", "output", "extra"}},
		{name: "zero QR size", args: []string{"-i", "input", "-o", "output", "-qr-size", "0"}},
		{name: "zero rows", args: []string{"-i", "input", "-o", "output", "-rows", "0"}},
		{name: "zero cols", args: []string{"-i", "input", "-o", "output", "-cols", "0"}},
		{name: "odd width", args: []string{"-i", "input", "-o", "output", "-width", "799"}},
		{name: "odd height", args: []string{"-i", "input", "-o", "output", "-height", "799"}},
		{name: "rows do not fit", args: []string{"-i", "input", "-o", "output", "-rows", "4"}},
		{name: "cols do not fit", args: []string{"-i", "input", "-o", "output", "-cols", "4"}},
		{name: "zero FPS", args: []string{"-i", "input", "-o", "output", "-fps", "0"}},
		{name: "NaN FPS", args: []string{"-i", "input", "-o", "output", "-fps", "NaN"}},
		{name: "infinite FPS", args: []string{"-i", "input", "-o", "output", "-fps", "+Inf"}},
		{name: "zero chunk", args: []string{"-i", "input", "-o", "output", "-chunk-size", "0"}},
		{name: "negative CRF", args: []string{"-i", "input", "-o", "output", "-crf", "-1"}},
		{name: "large CRF", args: []string{"-i", "input", "-o", "output", "-crf", "52"}},
		{name: "grid overflow", args: []string{"-i", "input", "-o", "output", "-rows", strconv.Itoa(maxInt), "-qr-size", "2"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ctx.ParseEncodeOptions(test.args); err == nil {
				t.Fatalf("ParseEncodeOptions(%q) succeeded", test.args)
			}
		})
	}
}

// TestParseDecodeOptions 验证 decode 的默认值、别名和参数边界。
// 前置条件：不需要视频文件存在，参数解析阶段不会访问输入路径。
// 执行方式：先解析完整有效参数，再逐项解析无效参数组合。
// 期望结果：有效参数完整保留，无效参数全部返回错误。
func TestParseDecodeOptions(t *testing.T) {
	ctx := NewCommandContext()
	defaults, err := ctx.ParseDecodeOptions([]string{"-i", "input.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	if !defaults.Parallel {
		t.Fatal("parallel decoding is not enabled by default")
	}

	got, err := ctx.ParseDecodeOptions([]string{
		"-in", "input.mp4",
		"-out", "output.bin",
		"-password", "secret",
		"-ffmpeg", "/path/to/ffmpeg",
		"-frames-dir", "frames",
		"-sample-fps", "12.5",
		"-max-frame-size", "3072",
		"-parallel=false",
		"-replace",
		"-keep-frames",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Input != "input.mp4" || got.Output != "output.bin" || got.Password != "secret" ||
		got.Ffmpeg != "/path/to/ffmpeg" || got.FramesDir != "frames" || got.SampleFPS != 12.5 || got.MaxFrameSize != 3072 ||
		got.Parallel || !got.Replace || !got.Keep {
		t.Fatalf("unexpected decode options: %+v", got)
	}

	tests := []struct {
		name string
		args []string
	}{
		{name: "missing input", args: nil},
		{name: "positional argument", args: []string{"-i", "input", "extra"}},
		{name: "zero sample FPS", args: []string{"-i", "input", "-sample-fps", "0"}},
		{name: "NaN sample FPS", args: []string{"-i", "input", "-sample-fps", "NaN"}},
		{name: "infinite sample FPS", args: []string{"-i", "input", "-sample-fps", "+Inf"}},
		{name: "zero max frame size", args: []string{"-i", "input", "-max-frame-size", "0"}},
		{name: "large max frame size", args: []string{"-i", "input", "-max-frame-size", "16385"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ctx.ParseDecodeOptions(test.args); err == nil {
				t.Fatalf("ParseDecodeOptions(%q) succeeded", test.args)
			}
		})
	}
}

// TestUsageListsEveryCommandOption 验证帮助文本与命令参数定义保持同步。
// 前置条件：usageText 已分为 encode 和 decode 两个参数区域。
// 执行方式：逐项搜索全部参数名称，并统计必填标记。
// 期望结果：每个参数都出现在正确区域，且包含一条图例和三个必填标记。
func TestUsageListsEveryCommandOption(t *testing.T) {
	parts := strings.Split(usageText, "decode 参数：")
	if len(parts) != 2 {
		t.Fatal("usage text does not contain decode section")
	}
	encodeSection := parts[0]
	decodeSection := parts[1]

	encodeOptions := []string{
		"-i、-in", "-o、-out", "-p、-password", "-ffmpeg", "-frames-dir",
		"-fps", "-qr-size", "-width", "-height", "-rows", "-cols",
		"-chunk-size", "-crf", "-replace", "-keep-frames",
	}
	decodeOptions := []string{
		"-i、-in", "-o、-out", "-p、-password", "-ffmpeg", "-frames-dir",
		"-sample-fps", "-max-frame-size", "-parallel", "-replace", "-keep-frames",
	}

	for _, option := range encodeOptions {
		if !strings.Contains(encodeSection, option) {
			t.Errorf("encode usage is missing %q", option)
		}
	}
	for _, option := range decodeOptions {
		if !strings.Contains(decodeSection, option) {
			t.Errorf("decode usage is missing %q", option)
		}
	}
	if strings.Count(usageText, "【必填】") != 4 {
		t.Fatalf("usage text must contain one legend and three required option markers")
	}
}
