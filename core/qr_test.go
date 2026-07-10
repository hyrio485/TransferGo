package core

import (
	"bytes"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件测试二维码图片的编码、解码和尺寸保护。
// 运行方式：在仓库根目录执行 go test ./...，或执行 go test ./core -run QR。
// 文件影响：所有 PNG 和无效样例都写入 t.TempDir，测试结束后由 Go 自动清理。

// TestQRCodeBinaryRoundTrip 验证全部单字节取值都能经过二维码无损往返。
// 前置条件：构造包含零至二百五十五全部字节值的内存载荷。
// 执行方式：编码为临时 PNG，再从同一 PNG 解码。
// 期望结果：只识别出一个载荷，并与原始二进制内容逐字节相同。
func TestQRCodeBinaryRoundTrip(t *testing.T) {
	input := make([]byte, 256)
	for i := range input {
		input[i] = byte(i)
	}
	path := filepath.Join(t.TempDir(), "frame.png")
	if err := EncodeMultiByteArraysToSinglePng([][]byte{input}, path, 300, 1, 1, 430, 430); err != nil {
		t.Fatal(err)
	}
	payloads, err := DecodeSinglePngToMultiByteArraysWithMaxFrameSize(path, defaultDecodeFrameSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 1 || !bytes.Equal(payloads[0], input) {
		t.Fatalf("decoded payloads do not match input: count = %d", len(payloads))
	}
}

// TestQRCodeMultiplePayloadsRoundTrip 验证一张图片中的多二维码识别。
// 前置条件：准备四个不同的短载荷，并使用二乘二网格。
// 执行方式：把四个载荷编码到同一临时 PNG，再一次性解码全部二维码。
// 期望结果：四个载荷都能被识别；返回顺序不作为协议保证。
func TestQRCodeMultiplePayloadsRoundTrip(t *testing.T) {
	inputs := [][]byte{[]byte("one"), []byte("two"), []byte("three"), []byte("four")}
	path := filepath.Join(t.TempDir(), "frame.png")
	if err := EncodeMultiByteArraysToSinglePng(inputs, path, 200, 2, 2, 530, 530); err != nil {
		t.Fatal(err)
	}
	payloads, err := DecodeSinglePngToMultiByteArraysWithMaxFrameSize(path, defaultDecodeFrameSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != len(inputs) {
		t.Fatalf("decoded payload count = %d, want %d", len(payloads), len(inputs))
	}
	for _, input := range inputs {
		found := false
		for _, payload := range payloads {
			if bytes.Equal(payload, input) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing decoded payload %q", input)
		}
	}
}

// TestEncodeRejectsInvalidQRCodeOptions 验证二维码编码入口的尺寸和容量约束。
// 前置条件：所有输出路径都位于 t.TempDir，且每个子测试只使用一种无效配置。
// 执行方式：覆盖空载荷、网格容量不足、零尺寸、超大图片和网格越界。
// 期望结果：所有配置都返回错误，并且不会生成有效 PNG。
func TestEncodeRejectsInvalidQRCodeOptions(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name        string
		payloads    [][]byte
		qrSize      int
		rows        int
		cols        int
		imageWidth  int
		imageHeight int
	}{
		{name: "empty payloads", payloads: nil, qrSize: 200, rows: 1, cols: 1, imageWidth: 240, imageHeight: 240},
		{name: "too many payloads", payloads: [][]byte{[]byte("one"), []byte("two")}, qrSize: 200, rows: 1, cols: 1, imageWidth: 240, imageHeight: 240},
		{name: "zero QR size", payloads: [][]byte{[]byte("one")}, qrSize: 0, rows: 1, cols: 1, imageWidth: 240, imageHeight: 240},
		{name: "grid does not fit", payloads: [][]byte{[]byte("one")}, qrSize: 200, rows: 2, cols: 2, imageWidth: 300, imageHeight: 300},
		{name: "gap is too small", payloads: [][]byte{[]byte("one")}, qrSize: 200, rows: 2, cols: 2, imageWidth: 519, imageHeight: 520},
		{name: "image too large", payloads: [][]byte{[]byte("one")}, qrSize: 200, rows: 1, cols: 1, imageWidth: maxImageDimension + 1, imageHeight: 240},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(dir, test.name+".png")
			err := EncodeMultiByteArraysToSinglePng(test.payloads, path, test.qrSize, test.rows, test.cols, test.imageWidth, test.imageHeight)
			if err == nil {
				t.Fatalf("EncodeMultiByteArraysToSinglePng(%q) succeeded", test.name)
			}
		})
	}
}

// TestEncodeRejectsQRCodeCropping 验证过小二维码不会被静默裁剪。
// 前置条件：使用小于二维码最小矩阵的 qrSize，并把输出放入 t.TempDir。
// 执行方式：尝试编码一个短载荷。
// 期望结果：返回包含 too small 的错误，而不是生成不可解码图片。
func TestEncodeRejectsQRCodeCropping(t *testing.T) {
	err := EncodeMultiByteArraysToSinglePng(
		[][]byte{[]byte("payload")},
		filepath.Join(t.TempDir(), "frame.png"),
		1,
		1,
		1,
		13,
		13,
	)
	if err == nil || !strings.Contains(err.Error(), "尺寸 1 像素过小") {
		t.Fatalf("EncodeMultiByteArraysToSinglePng() error = %v", err)
	}
}

// TestDecodeRejectsInvalidImage 验证图片读取和格式错误处理。
// 前置条件：在 t.TempDir 创建一个不包含合法图片数据的文件。
// 执行方式：分别解码不存在的路径和无效图片文件。
// 期望结果：两次调用都返回错误，不产生其他文件。
func TestDecodeRejectsInvalidImage(t *testing.T) {
	if _, err := DecodeSinglePngToMultiByteArraysWithMaxFrameSize("missing.png", 0); err == nil {
		t.Fatal("DecodeSinglePngToMultiByteArraysWithMaxFrameSize accepted zero max frame size")
	}
	dir := t.TempDir()
	if _, err := DecodeSinglePngToMultiByteArraysWithMaxFrameSize(filepath.Join(dir, "missing.png"), defaultDecodeFrameSize); err == nil {
		t.Fatal("DecodeSinglePngToMultiByteArraysWithMaxFrameSize accepted missing file")
	}
	invalidPath := filepath.Join(dir, "invalid.png")
	if err := os.WriteFile(invalidPath, []byte("not an image"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSinglePngToMultiByteArraysWithMaxFrameSize(invalidPath, defaultDecodeFrameSize); err == nil {
		t.Fatal("DecodeSinglePngToMultiByteArraysWithMaxFrameSize accepted invalid image")
	}
}

// TestResizeImageWithinLimit 验证二维码识别前的等比例缩放。
// 前置条件：在内存中创建横向大图、纵向大图和限制内小图。
// 执行方式：分别调用 resizeImageWithinLimit。
// 期望结果：大图最长边缩小到限制值并保持比例，小图直接复用原对象。
func TestResizeImageWithinLimit(t *testing.T) {
	landscape := image.NewRGBA(image.Rect(0, 0, 4096, 2048))
	if got := resizeImageWithinLimit(landscape, 2048).Bounds(); got.Dx() != 2048 || got.Dy() != 1024 {
		t.Fatalf("landscape bounds = %v", got)
	}
	portrait := image.NewRGBA(image.Rect(0, 0, 2048, 4096))
	if got := resizeImageWithinLimit(portrait, 2048).Bounds(); got.Dx() != 1024 || got.Dy() != 2048 {
		t.Fatalf("portrait bounds = %v", got)
	}
	small := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	if got := resizeImageWithinLimit(small, 2048); got != small {
		t.Fatal("small image was unnecessarily replaced")
	}
}
