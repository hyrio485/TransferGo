package next

import (
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"

	// 注册解码器
	_ "image/jpeg"
	_ "image/png"

	"golang.org/x/image/draw"

	"github.com/makiuchi-d/gozxing"
	gozxingmultiqr "github.com/makiuchi-d/gozxing/multi/qrcode"
	gozxingqr "github.com/makiuchi-d/gozxing/qrcode"
	qrdecoder "github.com/makiuchi-d/gozxing/qrcode/decoder"
)

const (
	characterSet         = "ISO-8859-1"
	errorCorrectionLevel = qrdecoder.ErrorCorrectionLevel_L
	maxDecodeFrameSize   = 1000
)

// region Encode

// EncodeMultiByteArraysToSinglePng 把每个字节数组编码成一个二维码，并按网格排列到同一张 PNG 中。
func EncodeMultiByteArraysToSinglePng(bytes [][]byte, path string, qrSize int, rows int, cols int, imageWidth int, imageHeight int) error {
	count := len(bytes)
	if count == 0 {
		return errors.New("no byte arrays to encode")
	}
	if count > rows*cols {
		return errors.New("too many byte arrays to encode")
	}

	hints := qrEncodeHints()
	gapX := (imageWidth - cols*qrSize) / (cols + 1)
	gapY := (imageHeight - rows*qrSize) / (rows + 1)
	img := newWhiteGrayImage(imageWidth, imageHeight)

	for i, data := range bytes {
		// gozxing 的写入接口只接受 string。这里使用 Latin-1 的一一映射，
		// 将每个字节转换为相同数值的 rune，避免按 UTF-8 文本解释时改变原始字节。
		runes := make([]rune, len(data))
		for j, b := range data {
			runes[j] = rune(b)
		}
		if err := drawQRCode(img, string(runes), i, qrSize, cols, gapX, gapY, hints); err != nil {
			return E("draw QR code", err)
		}
	}

	fileHandle, err := os.Create(path)
	if err != nil {
		return E("create output file", err)
	}
	defer CloseFile(fileHandle)

	err = png.Encode(fileHandle, img)
	if err != nil {
		return E("encode PNG", err)
	}
	return nil
}

func qrEncodeHints() map[gozxing.EncodeHintType]any {
	return map[gozxing.EncodeHintType]any{
		gozxing.EncodeHintType_CHARACTER_SET:    characterSet,
		gozxing.EncodeHintType_ERROR_CORRECTION: errorCorrectionLevel,
	}
}

func newWhiteGrayImage(width int, height int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, width, height))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	return img
}

func drawQRCode(img *image.Gray, content string, index int, qrSize int, cols int, gapX int, gapY int, hints map[gozxing.EncodeHintType]any) error {
	matrix, err := gozxingqr.NewQRCodeWriter().Encode(content, gozxing.BarcodeFormat_QR_CODE, qrSize, qrSize, hints)
	if err != nil {
		return E("encode string to QR code", err)
	}

	idxX := index % cols
	idxY := index / cols
	offsetX := gapX + idxX*(qrSize+gapX)
	offsetY := gapY + idxY*(qrSize+gapY)

	for y := 0; y < qrSize; y++ {
		for x := 0; x < qrSize; x++ {
			if matrix.Get(x, y) {
				img.SetGray(offsetX+x, offsetY+y, color.Gray{Y: 0})
			}
		}
	}

	return nil
}

// endregion

// region Decode

// DecodeSinglePngToMultiByteArrays 解码一张 PNG 中的所有二维码，并恢复各自的原始字节数组。
func DecodeSinglePngToMultiByteArrays(path string) ([][]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, E("open image", err)
	}
	defer CloseFile(file)

	img, _, err := image.Decode(file)
	if err != nil {
		return nil, E("decode image", err)
	}

	img = resizeImageWithinLimit(img)

	bitmap, err := newQRCodeBinaryBitmap(img)
	if err != nil {
		return nil, E("new binary bitmap", err)
	}

	reader := gozxingmultiqr.NewQRCodeMultiReader()
	gozxingResult, err := reader.DecodeMultiple(bitmap, qrDecodeHints())
	if err != nil {
		if _, ok := errors.AsType[gozxing.NotFoundException](err); ok {
			return [][]byte{}, nil
		}
		return nil, E("decode multiple", err)
	}

	if len(gozxingResult) == 0 {
		return [][]byte{}, nil
	}

	result := make([][]byte, 0, len(gozxingResult))
	for _, r := range gozxingResult {
		// 反向执行编码时的 Latin-1 映射。超出单字节范围的字符无法无损还原，
		// 因此将其视为无效的二进制二维码内容。
		text := r.GetText()
		data := make([]byte, 0, len(text))
		for _, char := range text {
			if char > 255 {
				return nil, E("decode QR code bytes", errors.New("QR payload contains a non Latin-1 character"))
			}
			data = append(data, byte(char))
		}
		result = append(result, data)
	}
	return result, nil
}

func newQRCodeBinaryBitmap(img image.Image) (*gozxing.BinaryBitmap, error) {
	source := gozxing.NewLuminanceSourceFromImage(img)
	return gozxing.NewBinaryBitmap(gozxing.NewHybridBinarizer(source))
}

func qrDecodeHints() map[gozxing.DecodeHintType]any {
	return map[gozxing.DecodeHintType]any{
		gozxing.DecodeHintType_TRY_HARDER:    true,
		gozxing.DecodeHintType_CHARACTER_SET: characterSet,
		gozxing.DecodeHintType_POSSIBLE_FORMATS: []gozxing.BarcodeFormat{
			gozxing.BarcodeFormat_QR_CODE,
		},
	}
}

func resizeImageWithinLimit(img image.Image) image.Image {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= maxDecodeFrameSize && height <= maxDecodeFrameSize {
		return img
	}

	newWidth := width
	newHeight := height
	if width > maxDecodeFrameSize {
		newWidth = maxDecodeFrameSize
		newHeight = height * maxDecodeFrameSize / width
	}
	if newHeight > maxDecodeFrameSize {
		newWidth = width * maxDecodeFrameSize / height
		newHeight = maxDecodeFrameSize
	}
	if newWidth == width && newHeight == height {
		return img
	}
	if newWidth < 1 {
		newWidth = 1
	}
	if newHeight < 1 {
		newHeight = 1
	}

	resized := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
	draw.CatmullRom.Scale(resized, resized.Bounds(), img, bounds, draw.Over, nil)

	return resized
}

// endregion
