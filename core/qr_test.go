package core

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestQRPayloadRoundTripThroughImage(t *testing.T) {
	payload := []byte{0, 1, 2, 3, 200, 255}

	pngBytes, err := encodeQRPNG(payload, defaultQRSize, defaultQRVersion)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeQRCodeBytesFromImage(img)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatalf("decoded payload = %v, want %v", decoded, payload)
	}
}

func TestQRWritePayloadFramesAndDecodePayloads(t *testing.T) {
	payloads := [][]byte{
		[]byte("first payload"),
		[]byte("second payload"),
		[]byte("third payload"),
	}
	dir := t.TempDir()

	if err := writeQRPayloadFrames(payloads, dir, testRenderOptions(), nil); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(dir, "frame_*.png"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 {
		t.Fatalf("rendered images = %d, want 1", len(paths))
	}

	decoded, err := decodeQRCodePayloads(paths[0], defaultGridSize)
	if err != nil {
		t.Fatal(err)
	}
	want := make(map[string]struct{}, len(payloads))
	for _, payload := range payloads {
		want[string(payload)] = struct{}{}
	}
	for _, payload := range decoded {
		delete(want, string(payload))
	}
	if len(want) != 0 {
		t.Fatalf("missing decoded payloads: %v", want)
	}
}

func TestQRDecodeBlankImageFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blank.png")
	if err := writeBlankPNG(path); err != nil {
		t.Fatal(err)
	}

	_, err := decodeQRCodePayloads(path, defaultGridSize)
	if err == nil {
		t.Fatal("decodeQRCodePayloads error = nil, want blank image failure")
	}
}

func TestQRRenderOptionsValidateSmallQRSize(t *testing.T) {
	opt := testRenderOptions()
	opt.qrSize = 10

	err := opt.validate()
	if err == nil {
		t.Fatal("validate error = nil, want too-small QR size")
	}
}

func writeBlankPNG(path string) error {
	img := image.NewRGBA(image.Rect(0, 0, defaultVideoWidth, defaultVideoHeight))
	for y := 0; y < defaultVideoHeight; y++ {
		for x := 0; x < defaultVideoWidth; x++ {
			img.Set(x, y, color.RGBA{R: 20, G: 20, B: 20, A: 255})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create blank PNG file: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()
	return png.Encode(file, img)
}
