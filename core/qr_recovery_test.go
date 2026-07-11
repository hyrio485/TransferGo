package core

import (
	"errors"
	"image"
	"image/color"
	_ "image/png"
	"os"
	"sort"
	"strconv"
	"testing"

	gozxingqr "github.com/makiuchi-d/gozxing/qrcode"
	"golang.org/x/image/draw"
)

const qrRecoveryImagePath = "/Users/hyrio/Workspace/Projects/hyrio/services/TransferGo/transfergo-decode-2751990497/frame_000013.png"

// TestRecoverAllQRCodesFromPhotographedFrame 尝试从拍摄的屏幕图片中恢复全部二维码。
// 前置条件：测试图片存在，并包含五列三行共十五个二维码。
// 执行方式：先整图识别，再逐格尝试原图、放大图和多种二值化结果。
// 期望结果：识别并去重得到十五个二维码载荷，同时打印对应协议帧序号。
func TestRecoverAllQRCodesFromPhotographedFrame(t *testing.T) {
	file, err := os.Open(qrRecoveryImagePath)
	if errors.Is(err, os.ErrNotExist) {
		t.Skipf("测试图片不存在：%s", qrRecoveryImagePath)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer CloseFile(file)

	img, _, err := image.Decode(file)
	if err != nil {
		t.Fatal(err)
	}

	found := make(map[string][]byte)
	baseline, err := DecodeSinglePngToMultiByteArraysWithMaxFrameSize(qrRecoveryImagePath, defaultDecodeFrameSize)
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range baseline {
		found[string(payload)] = payload
	}
	t.Logf("整图识别数量：%d", len(found))

	columns := []int{120, 475, 820, 1160, 1495, 1835}
	rows := []int{10, 355, 690, 1035}
	for row := 0; row < 3; row++ {
		for column := 0; column < 5; column++ {
			rect := image.Rect(columns[column], rows[row], columns[column+1], rows[row+1]).Intersect(img.Bounds())
			cell := cropQRCodeRecoveryImage(img, rect)
			payload, method, ok := recoverQRCodePayload(cell)
			if !ok {
				t.Logf("网格第 %d 行第 %d 列识别失败", row+1, column+1)
				continue
			}
			found[string(payload)] = payload
			t.Logf("网格第 %d 行第 %d 列识别成功：方法=%s，帧序号=%d", row+1, column+1, method, recoveryFrameIndex(payload))
		}
	}

	indexes := make([]uint32, 0, len(found))
	for _, payload := range found {
		indexes = append(indexes, recoveryFrameIndex(payload))
	}
	sort.Slice(indexes, func(i int, j int) bool { return indexes[i] < indexes[j] })
	t.Logf("最终识别数量：%d，帧序号：%v", len(found), indexes)
	if len(found) != 15 {
		t.Fatalf("最终识别到 %d 个二维码，期望 15 个", len(found))
	}
}

// TestDecodePhotographedFrameWithManifestGrid 验证正式解码入口能够利用清单网格恢复全部二维码。
func TestDecodePhotographedFrameWithManifestGrid(t *testing.T) {
	if _, err := os.Stat(qrRecoveryImagePath); errors.Is(err, os.ErrNotExist) {
		t.Skipf("测试图片不存在：%s", qrRecoveryImagePath)
	} else if err != nil {
		t.Fatal(err)
	}

	payloads, err := DecodeSinglePngToMultiByteArraysWithGrid(qrRecoveryImagePath, defaultDecodeFrameSize, 3, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 15 {
		t.Fatalf("正式解码入口识别到 %d 个二维码，期望 15 个", len(payloads))
	}
}

func cropQRCodeRecoveryImage(img image.Image, rect image.Rectangle) image.Image {
	result := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Copy(result, image.Point{}, img, rect, draw.Src, nil)
	return result
}

func recoverQRCodePayload(img image.Image) ([]byte, string, bool) {
	for _, scale := range []int{1, 2, 3, 4} {
		scaled := scaleQRCodeRecoveryImage(img, scale)
		if payload, ok := decodeSingleQRCodeRecoveryImage(scaled); ok {
			return payload, recoveryScaleName(scale), true
		}
		for _, threshold := range []uint8{80, 100, 120, 140, 160, 180, 200} {
			binary := thresholdQRCodeRecoveryImage(scaled, threshold)
			if payload, ok := decodeSingleQRCodeRecoveryImage(binary); ok {
				return payload, recoveryScaleName(scale) + "、阈值 " + strconv.Itoa(int(threshold)), true
			}
		}
	}
	return nil, "", false
}

func scaleQRCodeRecoveryImage(img image.Image, scale int) image.Image {
	if scale == 1 {
		return img
	}
	bounds := img.Bounds()
	result := image.NewRGBA(image.Rect(0, 0, bounds.Dx()*scale, bounds.Dy()*scale))
	draw.NearestNeighbor.Scale(result, result.Bounds(), img, bounds, draw.Src, nil)
	return result
}

func thresholdQRCodeRecoveryImage(img image.Image, threshold uint8) image.Image {
	bounds := img.Bounds()
	result := image.NewGray(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := 0; y < bounds.Dy(); y++ {
		for x := 0; x < bounds.Dx(); x++ {
			gray := color.GrayModel.Convert(img.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.Gray)
			if gray.Y >= threshold {
				gray.Y = 255
			} else {
				gray.Y = 0
			}
			result.SetGray(x, y, gray)
		}
	}
	return result
}

func decodeSingleQRCodeRecoveryImage(img image.Image) ([]byte, bool) {
	bitmap, err := newQRCodeBinaryBitmap(img)
	if err != nil {
		return nil, false
	}
	result, err := gozxingqr.NewQRCodeReader().Decode(bitmap, qrDecodeHints())
	if err != nil {
		return nil, false
	}
	payload := make([]byte, 0, len(result.GetText()))
	for _, char := range result.GetText() {
		if char > 255 {
			return nil, false
		}
		payload = append(payload, byte(char))
	}
	return payload, true
}

func recoveryFrameIndex(payload []byte) uint32 {
	frame, err := parseFrame(payload)
	if err != nil {
		return ^uint32(0)
	}
	return frame.index
}

func recoveryScaleName(scale int) string {
	return map[int]string{1: "原始裁剪", 2: "放大两倍", 3: "放大三倍", 4: "放大四倍"}[scale]
}
