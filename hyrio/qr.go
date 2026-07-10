package hyrio

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"

	"github.com/makiuchi-d/gozxing"
	gozxingqr "github.com/makiuchi-d/gozxing/qrcode"
	qrdecoder "github.com/makiuchi-d/gozxing/qrcode/decoder"
)

const (
	qrEncodeMargin = 2
)

func EncodeQRGray(payload []byte, size int, version int) (*image.Gray, error) {
	content := bytesToLatin1String(payload)
	hints := map[gozxing.EncodeHintType]interface{}{
		gozxing.EncodeHintType_CHARACTER_SET:    "ISO-8859-1",
		gozxing.EncodeHintType_ERROR_CORRECTION: qrdecoder.ErrorCorrectionLevel_L,
		gozxing.EncodeHintType_MARGIN:           qrEncodeMargin,
	}
	if version > 0 {
		hints[gozxing.EncodeHintType_QR_VERSION] = version
	}

	matrix, err := gozxingqr.NewQRCodeWriter().Encode(content, gozxing.BarcodeFormat_QR_CODE, size, size, hints)
	if err != nil {
		return nil, E("write QR matrix", err)
	}

	return BitMatrixToGray(matrix), nil
}

func bytesToLatin1String(data []byte) string {
	runes := make([]rune, len(data))
	for i, b := range data {
		runes[i] = rune(b)
	}
	return string(runes)
}

func BitMatrixToGray(matrix *gozxing.BitMatrix) *image.Gray {
	bounds := image.Rect(0, 0, matrix.GetWidth(), matrix.GetHeight())
	img := image.NewGray(bounds)
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	for y := 0; y < matrix.GetHeight(); y++ {
		for x := 0; x < matrix.GetWidth(); x++ {
			if matrix.Get(x, y) {
				img.SetGray(x, y, color.Gray{Y: 0})
			}
		}
	}
	return img
}

func SaveGrayAsPng(img *image.Gray, path string) error {
	file, err := os.Create(path)
	if err != nil {
		return ("write png: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()
	err = png.Encode(file, img)
	if err != nil {
		return E("encode png", err)
	}
	return nil
}
