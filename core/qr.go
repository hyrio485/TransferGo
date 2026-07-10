package core

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
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
	characterSet           = "ISO-8859-1"
	errorCorrectionLevel   = qrdecoder.ErrorCorrectionLevel_L
	defaultDecodeFrameSize = 2048
	maxImageDimension      = 16_384
	maxImagePixels         = 64 * 1024 * 1024
)

// qrGridDimensionFits 判断单个方向是否能容纳全部二维码及其最小间距。
func qrGridDimensionFits(imageSize int, count int, qrSize int) bool {
	if imageSize <= 0 || count <= 0 || qrSize <= 0 {
		return false
	}
	// 最小需要的距离：每个二维码的前后各留大小的五分之一的gap，最后留有10像素的余量
	gap := qrSize / 5
	if qrSize%5 != 0 {
		gap++
	}
	requiredSize := count*qrSize + (count+1)*gap + 10
	return requiredSize <= imageSize
}

// region Encode

// EncodeMultiByteArraysToSinglePng 把每个字节数组编码成一个二维码，并按网格排列到同一张 PNG 中。
// 函数会在分配图片内存前校验尺寸和像素总量，避免异常参数造成过量内存占用。
func EncodeMultiByteArraysToSinglePng(bytes [][]byte, path string, qrSize int, rows int, cols int, imageWidth int, imageHeight int) error {
	if qrSize <= 0 || rows <= 0 || cols <= 0 || imageWidth <= 0 || imageHeight <= 0 {
		return errors.New("二维码尺寸、网格行列数和输出图片尺寸都必须大于 0")
	}
	if imageWidth > maxImageDimension || imageHeight > maxImageDimension || imageWidth > maxImagePixels/imageHeight {
		return errors.New("输出图片尺寸过大")
	}
	if !qrGridDimensionFits(imageHeight, rows, qrSize) || !qrGridDimensionFits(imageWidth, cols, qrSize) {
		return errors.New("二维码网格超出输出图片范围，请减小二维码尺寸或网格行列数")
	}
	count := len(bytes)
	if count == 0 {
		return errors.New("没有可用于生成二维码的数据")
	}
	if count > rows*cols {
		return errors.New("待编码数据块数量超过二维码网格容量")
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
			return E("绘制二维码失败", err)
		}
	}

	fileHandle, err := os.Create(path)
	if err != nil {
		return E("创建 PNG 输出文件失败", err)
	}
	if err := png.Encode(fileHandle, img); err != nil {
		CloseFile(fileHandle)
		return E("写入 PNG 图片失败", err)
	}
	if err := fileHandle.Close(); err != nil {
		return E("关闭 PNG 输出文件失败", err)
	}
	return nil
}

func qrEncodeHints() map[gozxing.EncodeHintType]any {
	return map[gozxing.EncodeHintType]any{
		gozxing.EncodeHintType_CHARACTER_SET:    characterSet,
		gozxing.EncodeHintType_ERROR_CORRECTION: errorCorrectionLevel,
		gozxing.EncodeHintType_MARGIN:           0,
	}
}

func newWhiteGrayImage(width int, height int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, width, height))
	for i := range img.Pix {
		img.Pix[i] = 0xEE
	}
	return img
}

//// drawQRCode 生成无白边的原生二维码，并使用最近邻缩放绘制到网格中的指定位置。
//func drawQRCode(img *image.Gray, content string, index int, qrSize int, cols int, gapX int, gapY int, hints map[gozxing.EncodeHintType]any) error {
//	matrix, err := gozxingqr.NewQRCodeWriter().Encode(content, gozxing.BarcodeFormat_QR_CODE, 0, 0, hints)
//	if err != nil {
//		return E("encode string to QR code", err)
//	}
//	matrixWidth := matrix.GetWidth()
//	matrixHeight := matrix.GetHeight()
//	if matrixWidth > qrSize || matrixHeight > qrSize {
//		return fmt.Errorf("QR size %d is too small; encoded matrix requires %dx%d pixels", qrSize, matrix.GetWidth(), matrix.GetHeight())
//	}
//
//	qrImage := newWhiteGrayImage(matrixWidth, matrixHeight)
//	for y := 0; y < matrixHeight; y++ {
//		for x := 0; x < matrixWidth; x++ {
//			if matrix.Get(x, y) {
//				qrImage.SetGray(x, y, color.Gray{Y: 0})
//			}
//		}
//	}
//
//	idxX := index % cols
//	idxY := index / cols
//	offsetX := gapX + idxX*(qrSize+gapX)
//	offsetY := gapY + idxY*(qrSize+gapY)
//	destination := image.Rect(offsetX, offsetY, offsetX+qrSize, offsetY+qrSize)
//	draw.NearestNeighbor.Scale(img, destination, qrImage, qrImage.Bounds(), draw.Src, nil)
//
//	return nil
//}

//// drawQRCode 把一个二维码矩阵绘制到网格中的指定位置，并拒绝尺寸不足导致的矩阵裁剪。
//func drawQRCode(img *image.Gray, content string, index int, qrSize int, cols int, gapX int, gapY int, hints map[gozxing.EncodeHintType]any) error {
//	matrix, err := gozxingqr.NewQRCodeWriter().Encode(content, gozxing.BarcodeFormat_QR_CODE, 0, 0, hints)
//	if err != nil {
//		return E("encode string to QR code", err)
//	}
//	matrixSize := matrix.GetWidth()
//	if matrixSize > qrSize || matrix.GetHeight() > qrSize {
//		return fmt.Errorf("QR size %d is too small; encoded matrix requires %dx%d pixels", qrSize, matrix.GetWidth(), matrix.GetHeight())
//	}
//	scale := qrSize / matrixSize
//
//	idxX := index % cols
//	idxY := index / cols
//	offsetX := gapX + idxX*(qrSize+gapX)
//	offsetY := gapY + idxY*(qrSize+gapY)
//
//	for y := 0; y < matrixSize; y++ {
//		for x := 0; x < matrixSize; x++ {
//			if matrix.Get(x, y) {
//				for dy := 0; dy < scale; dy++ {
//					for dx := 0; dx < scale; dx++ {
//						img.SetGray(offsetX+x*scale+dx, offsetY+y*scale+dy, color.Gray{Y: 0})
//					}
//				}
//			}
//		}
//	}
//
//	return nil
//}

// drawQRCode 把一个二维码矩阵绘制到网格中的指定位置，并拒绝尺寸不足导致的矩阵裁剪。
func drawQRCode(img *image.Gray, content string, index int, qrSize int, cols int, gapX int, gapY int, hints map[gozxing.EncodeHintType]any) error {
	matrix, err := gozxingqr.NewQRCodeWriter().Encode(content, gozxing.BarcodeFormat_QR_CODE, qrSize, qrSize, hints)
	if err != nil {
		return E("生成二维码矩阵失败", err)
	}
	if matrix.GetWidth() != qrSize || matrix.GetHeight() != qrSize {
		return fmt.Errorf("二维码尺寸 %d 像素过小，当前数据至少需要 %d×%d 像素", qrSize, matrix.GetWidth(), matrix.GetHeight())
	}

	idxX := index % cols
	idxY := index / cols
	offsetX := gapX + idxX*(qrSize+gapX)
	offsetY := gapY + idxY*(qrSize+gapY)

	for y := 0; y < qrSize; y++ {
		for x := 0; x < qrSize; x++ {
			if matrix.Get(x, y) {
				img.SetGray(offsetX+x, offsetY+y, color.Gray{Y: 0})
			} else {
				img.SetGray(offsetX+x, offsetY+y, color.Gray{Y: 0xFF})
			}
		}
	}

	return nil
}

// endregion

// region Decode

// DecodeSinglePngToMultiByteArraysWithMaxFrameSize 在指定最长边限制下解码图片中的所有二维码。
// 找不到二维码属于正常情况，此时返回空切片而不是错误。
func DecodeSinglePngToMultiByteArraysWithMaxFrameSize(path string, maxFrameSize int) ([][]byte, error) {
	if maxFrameSize <= 0 || maxFrameSize > maxImageDimension {
		return nil, fmt.Errorf("图片最长边限制必须在 1 至 %d 之间", maxImageDimension)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, E("打开图片失败", err)
	}
	defer CloseFile(file)

	// 先读取轻量级图片头，避免在完整解码后才发现图片尺寸超出安全范围。
	config, _, err := image.DecodeConfig(file)
	if err != nil {
		return nil, E("读取图片尺寸信息失败", err)
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxImageDimension || config.Height > maxImageDimension || config.Width > maxImagePixels/config.Height {
		return nil, errors.New("图片尺寸过大，无法安全解码")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, E("重新读取图片失败", err)
	}
	img, _, err := image.Decode(file)
	if err != nil {
		return nil, E("解析图片内容失败", err)
	}

	img = resizeImageWithinLimit(img, maxFrameSize)

	bitmap, err := newQRCodeBinaryBitmap(img)
	if err != nil {
		return nil, E("创建二维码识别位图失败", err)
	}

	reader := gozxingmultiqr.NewQRCodeMultiReader()
	gozxingResult, err := reader.DecodeMultiple(bitmap, qrDecodeHints())
	if err != nil {
		if _, ok := errors.AsType[gozxing.NotFoundException](err); ok {
			return [][]byte{}, nil
		}
		return nil, E("识别图片中的二维码失败", err)
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
				return nil, E("读取二维码载荷失败", errors.New("二维码载荷包含 Latin-1 编码范围之外的字符"))
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

// resizeImageWithinLimit 在保持宽高比的前提下，把图片最长边缩小到解码限制以内。
func resizeImageWithinLimit(img image.Image, maxFrameSize int) image.Image {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= maxFrameSize && height <= maxFrameSize {
		return img
	}

	newWidth := width
	newHeight := height
	if width > maxFrameSize {
		newWidth = maxFrameSize
		newHeight = height * maxFrameSize / width
	}
	if newHeight > maxFrameSize {
		newWidth = width * maxFrameSize / height
		newHeight = maxFrameSize
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
