package core

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"

	"github.com/makiuchi-d/gozxing"
	gozxingmultiqr "github.com/makiuchi-d/gozxing/multi/qrcode"
	gozxingqr "github.com/makiuchi-d/gozxing/qrcode"
	qrdecoder "github.com/makiuchi-d/gozxing/qrcode/decoder"
)

// pointF 存储浮点图片坐标，用于在把拍摄到的屏幕四边形扭正为正方形前进行近似。
type pointF struct {
	x float64
	y float64
}

const (
	// 较小的二维码边距可以提高载荷密度，同时不完全移除静区。两个模块是屏幕拍摄场景中的实用折中。
	qrEncodeMargin     = 2
	qrQuietZoneModules = qrEncodeMargin * 2
	// qrCanvasWhite 和 qrGuideBlack 是固定灰度锚点，用于生成的视频画布和格子边框。
	qrCanvasWhite = 255
	qrGuideBlack  = 16
	// qrGuideWidth 让每个格子对人眼和简单对比度检测都可见，同时不挤占二维码静区。
	qrGuideWidth = 6
	// 高对比度采样只用于在更大的相机画面中寻找可能的二维码区域；实际二维码解码器仍会校验载荷。
	qrContrastDistance      = 96
	qrContrastSampleMinimum = 24
	// qrWarpPadding 会略微扩展估计出的屏幕四边形，让被裁切的二维码边框更可能在透视扭正后保留下来。
	qrWarpPadding = 1.14
)

// QRRenderOptions 收集几何参数，供多个二维码载荷打包进一个视频帧时的编码和校验共同使用。
type QRRenderOptions struct {
	qrSize      int
	qrVersion   int
	videoWidth  int
	videoHeight int
	gridSize    int
}

// Validate 拒绝那些会让二维码小到无法编码，或大到无法放入指定网格的渲染选项。
func (opt QRRenderOptions) Validate() error {
	if opt.qrSize <= 0 {
		return fmt.Errorf("-qr-size must be greater than 0")
	}
	if opt.qrVersion < 1 || opt.qrVersion > 40 {
		return fmt.Errorf("-qr-version must be between 1 and 40")
	}
	if opt.videoWidth <= 0 {
		return fmt.Errorf("-width must be greater than 0")
	}
	if opt.videoHeight <= 0 {
		return fmt.Errorf("-height must be greater than 0")
	}
	if opt.gridSize <= 0 {
		return fmt.Errorf("-grid-size must be greater than 0")
	}
	minQRSize := minRenderedQRSize(opt.qrVersion)
	if opt.qrSize < minQRSize {
		return fmt.Errorf("-qr-size %d is too small for QR version %d; need at least %d", opt.qrSize, opt.qrVersion, minQRSize)
	}
	if opt.minTileSize() < minQRSize {
		return fmt.Errorf("grid cells are too small for QR version %d; need at least %d pixels per cell", opt.qrVersion, minQRSize)
	}
	return nil
}

// slotsPerImage 返回在当前网格大小下，一张渲染后视频图片能容纳多少协议帧。
func (opt QRRenderOptions) slotsPerImage() int {
	return opt.gridSize * opt.gridSize
}

// minTileSize 返回限制性的格子边长，单位为像素，并考虑输出画布无法均匀整除的情况。
func (opt QRRenderOptions) minTileSize() int {
	return minInt(opt.videoWidth/opt.gridSize, opt.videoHeight/opt.gridSize)
}

// EffectiveQRSize 把请求的二维码尺寸限制到可用格子尺寸内，让用户可以请求偏大的尺寸而不溢出网格。
func (opt QRRenderOptions) EffectiveQRSize() int {
	return minInt(opt.qrSize, opt.minTileSize())
}

// tileRect 返回某个从 0 开始的网格槽位在画布上的精确矩形。
func (opt QRRenderOptions) tileRect(tile int) image.Rectangle {
	row := tile / opt.gridSize
	col := tile % opt.gridSize
	return image.Rect(
		col*opt.videoWidth/opt.gridSize,
		row*opt.videoHeight/opt.gridSize,
		(col+1)*opt.videoWidth/opt.gridSize,
		(row+1)*opt.videoHeight/opt.gridSize,
	)
}

// RenderedFrameCount 计算应用网格打包系数后，容纳所有协议帧需要多少张 PNG。
func RenderedFrameCount(protocolFrames int, opt QRRenderOptions) int {
	if protocolFrames <= 0 {
		return 0
	}
	slots := opt.slotsPerImage()
	return (protocolFrames + slots - 1) / slots
}

// WriteQRPayloadFrames 把每个载荷序列化为带零填充的 PNG 序列。文件命名约定用于匹配 ffmpeg 的 frame_%06d.png 输入模式，并让字典序排序等同于帧顺序。
func WriteQRPayloadFrames(payloads [][]byte, dir string, opt QRRenderOptions, progress func(done int, total int)) error {
	if err := opt.Validate(); err != nil {
		return fmt.Errorf("validate QR render options: %w", err)
	}
	qrSize := opt.EffectiveQRSize()
	slots := opt.slotsPerImage()

	for start, imageIndex := 0, 1; start < len(payloads); start, imageIndex = start+slots, imageIndex+1 {
		img := newMosaicCanvas(opt.videoWidth, opt.videoHeight)
		drawMosaicGuides(img, opt)
		end := minInt(start+slots, len(payloads))
		for payloadIndex := start; payloadIndex < end; payloadIndex++ {
			slot := payloadIndex - start
			qrImg, err := encodeQRGray(payloads[payloadIndex], qrSize, opt.qrVersion)
			if err != nil {
				return fmt.Errorf("encode QR payload %d: %w", payloadIndex, err)
			}
			if err := drawQRIntoTile(img, qrImg, opt.tileRect(slot)); err != nil {
				return fmt.Errorf("render QR payload %d: %w", payloadIndex, err)
			}
		}

		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return fmt.Errorf("encode QR mosaic PNG %d: %w", imageIndex, err)
		}
		path := filepath.Join(dir, fmt.Sprintf("frame_%06d.png", imageIndex))
		if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
			return fmt.Errorf("write QR mosaic PNG %d: %w", imageIndex, err)
		}
		if progress != nil {
			progress(imageIndex, RenderedFrameCount(len(payloads), opt))
		}
	}
	return nil
}

// EncodeQRPNG 把任意字节存入二维码。gozxing 的写入器接收文本，因此字节会通过 ISO-8859-1 映射，其中每个字节值都映射到相同编号的码点。使用纠错等级 L 以最大化容量。
func EncodeQRPNG(payload []byte, size int, version int) ([]byte, error) {
	img, err := encodeQRGray(payload, size, version)
	if err != nil {
		return nil, fmt.Errorf("encode QR image: %w", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode QR PNG: %w", err)
	}
	return buf.Bytes(), nil
}

// encodeQRGray 创建用于校验和 PNG 渲染的原始灰度二维码位图。它让载荷编码和文件 I/O 保持分离。
func encodeQRGray(payload []byte, size int, version int) (*image.Gray, error) {
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
		return nil, fmt.Errorf("write QR matrix: %w", err)
	}

	return bitMatrixToGray(matrix), nil
}

// bitMatrixToGray 把二维码位矩阵转换成普通黑白图片。灰度图片紧凑、确定，并且足够用于 PNG 输出。
func bitMatrixToGray(matrix *gozxing.BitMatrix) *image.Gray {
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

// newMosaicCanvas 创建一张白色 RGBA 画布，大小足以容纳完整二维码网格。这里使用 RGBA，是因为后续绘制代码会直接写入像素通道，以获得速度和确定性输出。
func newMosaicCanvas(width int, height int) *image.RGBA {
	bounds := image.Rect(0, 0, width, height)
	img := image.NewRGBA(bounds)
	bg := image.NewUniform(color.RGBA{R: qrCanvasWhite, G: qrCanvasWhite, B: qrCanvasWhite, A: 255})
	draw.Draw(img, bounds, bg, image.Point{}, draw.Src)
	return img
}

// drawMosaicGuides 为每个格子描边，让拍摄后的帧具有强矩形对比度，便于解码时重新发现。
func drawMosaicGuides(dst *image.RGBA, opt QRRenderOptions) {
	for tile := 0; tile < opt.gridSize*opt.gridSize; tile++ {
		drawTileGuide(dst, opt.tileRect(tile))
	}
}

// drawTileGuide 在一个格子周围绘制简单深色边框。非常小的格子会跳过引导边框，因为边框会占用过多二维码空间。
func drawTileGuide(dst *image.RGBA, rect image.Rectangle) {
	guide := qrGuideWidth
	if rect.Dx() < guide*6 || rect.Dy() < guide*6 {
		return
	}
	black := color.RGBA{R: qrGuideBlack, G: qrGuideBlack, B: qrGuideBlack, A: 255}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			if x < rect.Min.X+guide || x >= rect.Max.X-guide || y < rect.Min.Y+guide || y >= rect.Max.Y-guide {
				dst.SetRGBA(x, y, black)
			}
		}
	}
}

// drawQRIntoTile 把一个普通黑白二维码居中绘制到格子中。单色输出比旧的 RGB 通道叠加给相机提供更高对比度的目标；旧方案密度高，但经过屏幕拍摄后很脆弱。
func drawQRIntoTile(dst *image.RGBA, qr *image.Gray, tile image.Rectangle) error {
	qrBounds := qr.Bounds()
	qrWidth := qrBounds.Dx()
	qrHeight := qrBounds.Dy()
	if qrWidth > tile.Dx() || qrHeight > tile.Dy() {
		return fmt.Errorf("QR image %dx%d does not fit tile %dx%d", qrWidth, qrHeight, tile.Dx(), tile.Dy())
	}

	x0 := tile.Min.X + (tile.Dx()-qrWidth)/2
	y0 := tile.Min.Y + (tile.Dy()-qrHeight)/2
	for y := 0; y < qrHeight; y++ {
		srcOffset := qr.PixOffset(qrBounds.Min.X, qrBounds.Min.Y+y)
		dstOffset := dst.PixOffset(x0, y0+y)
		for x := 0; x < qrWidth; x++ {
			value := qr.Pix[srcOffset+x]
			pixelOffset := dstOffset + x*4
			dst.Pix[pixelOffset+0] = value
			dst.Pix[pixelOffset+1] = value
			dst.Pix[pixelOffset+2] = value
			dst.Pix[pixelOffset+3] = 255
		}
	}
	return nil
}

// DecodeQRCodePayloads 加载一张抽取出的图片，并返回分层解码策略找到的每个唯一二维码载荷。
func DecodeQRCodePayloads(path string, gridSize int) ([][]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open QR image: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()

	img, _, err := image.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("decode QR image: %w", err)
	}

	payloads := decodeQRCodePayloadsFromImage(img, gridSize)
	if len(payloads) == 0 {
		return nil, fmt.Errorf("no QR code decoded")
	}
	return payloads, nil
}

// decodeQRCodePayloadsFromImage 先尝试整张图片，再尝试裁剪和扭正后的候选区域。这样能让完美生成的帧保持快速，同时给拍摄或倾斜捕获提供额外恢复机会。
func decodeQRCodePayloadsFromImage(img image.Image, gridSize int) [][]byte {
	if gridSize <= 0 {
		gridSize = defaultGridSize
	}

	acc := newPayloadAccumulator()
	addSingleDecode(acc, img)
	addMultipleDecode(acc, img)

	for _, rect := range decodeCandidateRects(img) {
		gray := imageToGray(img, rect)
		addMultipleDecode(acc, gray)
		addSingleDecode(acc, gray)
		for _, cell := range gridCellRects(gray.Bounds(), gridSize) {
			addSingleDecode(acc, cropGray(gray, cell))
		}
	}
	if warped, ok := contrastContentWarp(img); ok {
		gray := imageToGray(warped, warped.Bounds())
		addMultipleDecode(acc, gray)
		addSingleDecode(acc, gray)
		for _, cell := range gridCellRects(gray.Bounds(), gridSize) {
			addSingleDecode(acc, cropGray(gray, cell))
		}
	}

	return acc.payloads
}

// addSingleDecode 尝试普通二维码读取器，并在成功时记录载荷。很多候选裁剪区域解码失败是预期情况。
func addSingleDecode(acc *payloadAccumulator, img image.Image) {
	payload, err := decodeQRCodeBytesFromImage(img)
	if err == nil {
		acc.add(payload)
	}
}

// addMultipleDecode 对完整拼图或较大裁剪区域使用多二维码读取器，因为其中可能同时包含多个二维码。
func addMultipleDecode(acc *payloadAccumulator, img image.Image) {
	payloads, err := decodeMultipleQRCodeBytesFromImage(img)
	if err != nil {
		return
	}
	for _, payload := range payloads {
		acc.add(payload)
	}
}

// decodeQRCodeBytesFromImage 反向执行 EncodeQRPNG。有些二维码解码器会直接暴露原始字节段；如果没有，就回退到 Latin-1 文本映射来恢复精确协议字节。
func decodeQRCodeBytesFromImage(img image.Image) ([]byte, error) {
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return nil, fmt.Errorf("create QR bitmap: %w", err)
	}

	result, err := gozxingqr.NewQRCodeReader().Decode(bmp, qrDecodeHints())
	if err != nil {
		return nil, fmt.Errorf("decode QR bitmap: %w", err)
	}
	return qrResultBytes(result)
}

// decodeMultipleQRCodeBytesFromImage 解码多二维码读取器能在一个图片候选区域中识别出的每个二维码。
func decodeMultipleQRCodeBytesFromImage(img image.Image) ([][]byte, error) {
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return nil, fmt.Errorf("create multi QR bitmap: %w", err)
	}
	results, err := gozxingmultiqr.NewQRCodeMultiReader().DecodeMultiple(bmp, qrDecodeHints())
	if err != nil {
		return nil, fmt.Errorf("decode multiple QR bitmaps: %w", err)
	}

	payloads := make([][]byte, 0, len(results))
	for _, result := range results {
		payload, err := qrResultBytes(result)
		if err == nil {
			payloads = append(payloads, payload)
		}
	}
	return payloads, nil
}

// qrDecodeHints 集中管理 gozxing 解码选项，让单二维码和多二维码读取器以相同方式解释 TransferGo 载荷。
func qrDecodeHints() map[gozxing.DecodeHintType]interface{} {
	// TRY_HARDER 会消耗更多 CPU，但对从拍摄视频中抽取的帧很有用，因为这类帧常有模糊、缩放和压缩伪影。
	return map[gozxing.DecodeHintType]interface{}{
		gozxing.DecodeHintType_CHARACTER_SET:    "ISO-8859-1",
		gozxing.DecodeHintType_POSSIBLE_FORMATS: gozxing.BarcodeFormats{gozxing.BarcodeFormat_QR_CODE},
		gozxing.DecodeHintType_TRY_HARDER:       true,
	}
}

// qrResultBytes 从 gozxing 结果中提取精确协议字节。由于载荷是二进制数据，所以优先使用字节段而非文本。
func qrResultBytes(result *gozxing.Result) ([]byte, error) {
	// 可用时优先使用原始字节段。这样能避免文本转换中的歧义，也是二维码载荷最忠实的表示。
	if metadata := result.GetResultMetadata(); metadata != nil {
		if raw, ok := metadata[gozxing.ResultMetadataType_BYTE_SEGMENTS]; ok {
			if segments, ok := raw.([][]byte); ok && len(segments) > 0 {
				var out []byte
				for _, segment := range segments {
					out = append(out, segment...)
				}
				return out, nil
			}
		}
	}

	return latin1StringToBytes(result.GetText())
}

// payloadAccumulator 避免多个候选裁剪区域重叠时，解码重试多次返回同一个二维码载荷。
type payloadAccumulator struct {
	seen     map[string]struct{}
	payloads [][]byte
}

// newPayloadAccumulator 启动一个空的去重载荷收集器。
func newPayloadAccumulator() *payloadAccumulator {
	return &payloadAccumulator{seen: make(map[string]struct{})}
}

// add 在载荷尚未出现过时，存储它的防御性副本。
func (acc *payloadAccumulator) add(payload []byte) {
	key := string(payload)
	if _, ok := acc.seen[key]; ok {
		return
	}
	acc.seen[key] = struct{}{}
	acc.payloads = append(acc.payloads, append([]byte{}, payload...))
}

// imageToGray 让黑白二维码检测保持确定性。它也避免旧方案中把同一相机帧作为三个颜色通道分别尝试的昂贵行为；现在编码已经是单色，这种做法不再有帮助。
func imageToGray(src image.Image, rect image.Rectangle) *image.Gray {
	rect = rect.Intersect(src.Bounds())
	dst := image.NewGray(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			r, g, b, a := src.At(x, y).RGBA()
			value := uint8(255)
			if a != 0 {
				value = luminance(r, g, b)
			}
			dst.SetGray(x-rect.Min.X, y-rect.Min.Y, color.Gray{Y: value})
		}
	}
	return dst
}

// cropGray 把一个灰度候选区域复制到从零开始的图片中，让二维码解码器无需处理源坐标偏移。
func cropGray(src *image.Gray, rect image.Rectangle) *image.Gray {
	rect = rect.Intersect(src.Bounds())
	dst := image.NewGray(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Draw(dst, dst.Bounds(), src, rect.Min, draw.Src)
	return dst
}

// decodeCandidateRects 在整帧解码失败时返回值得重试的近似正方形区域。生成的画布是正方形，但手机录像可能包含周围屏幕或相机背景。
func decodeCandidateRects(img image.Image) []image.Rectangle {
	bounds := img.Bounds()
	rects := []image.Rectangle{bounds}
	rects = appendUniqueRect(rects, centeredSquare(bounds))
	if rect, ok := contrastContentSquare(img); ok {
		rects = appendUniqueRect(rects, rect)
	}
	return rects
}

// appendUniqueRect 让候选列表保持紧凑，避免反复解码同一个裁剪区域。
func appendUniqueRect(rects []image.Rectangle, rect image.Rectangle) []image.Rectangle {
	if rect.Empty() {
		return rects
	}
	for _, existing := range rects {
		if existing.Eq(rect) {
			return rects
		}
	}
	return append(rects, rect)
}

// centeredSquare 是最低成本的回退裁剪，用于视频位于更宽相机画面中央的录像。
func centeredSquare(bounds image.Rectangle) image.Rectangle {
	side := minInt(bounds.Dx(), bounds.Dy())
	x0 := bounds.Min.X + (bounds.Dx()-side)/2
	y0 := bounds.Min.Y + (bounds.Dy()-side)/2
	return image.Rect(x0, y0, x0+side, y0+side)
}

// contrastContentSquare 估计二维码拼图在拍摄帧中的位置。它寻找非常暗或非常亮的采样点，然后把结果扩展成正方形，因为生成的视频画布是正方形。
func contrastContentSquare(img image.Image) (image.Rectangle, bool) {
	bounds := img.Bounds()
	step := maxInt(1, minInt(bounds.Dx(), bounds.Dy())/500)
	minX, minY := bounds.Max.X, bounds.Max.Y
	maxX, maxY := bounds.Min.X, bounds.Min.Y
	count := 0

	for y := bounds.Min.Y; y < bounds.Max.Y; y += step {
		for x := bounds.Min.X; x < bounds.Max.X; x += step {
			if !isHighContrastPixel(img.At(x, y)) {
				continue
			}
			minX = minInt(minX, x)
			minY = minInt(minY, y)
			maxX = maxInt(maxX, x)
			maxY = maxInt(maxY, y)
			count++
		}
	}
	if count < qrContrastSampleMinimum {
		return image.Rectangle{}, false
	}

	rect := image.Rect(minX, minY, maxX+step, maxY+step).Intersect(bounds)
	padding := maxInt(step*2, minInt(bounds.Dx(), bounds.Dy())/50)
	return squareAroundRect(expandRect(rect, padding, bounds), bounds), true
}

// contrastContentWarp 使用高对比度极值点作为粗略四边形，并把该形状映射回正方形。它有意保持近似：手机录像不会提供真实屏幕几何，但这通常能挽救轻微倾斜的捕获。
func contrastContentWarp(img image.Image) (image.Image, bool) {
	bounds := img.Bounds()
	step := maxInt(1, minInt(bounds.Dx(), bounds.Dy())/700)
	var tl, tr, br, bl pointF
	minSum := 1 << 30
	maxSum := -1 << 30
	minDiff := 1 << 30
	maxDiff := -1 << 30
	count := 0

	for y := bounds.Min.Y; y < bounds.Max.Y; y += step {
		for x := bounds.Min.X; x < bounds.Max.X; x += step {
			if !isHighContrastPixel(img.At(x, y)) {
				continue
			}
			sum := x + y
			diff := x - y
			p := pointF{x: float64(x), y: float64(y)}
			if sum < minSum {
				minSum = sum
				tl = p
			}
			if sum > maxSum {
				maxSum = sum
				br = p
			}
			if diff > maxDiff {
				maxDiff = diff
				tr = p
			}
			if diff < minDiff {
				minDiff = diff
				bl = p
			}
			count++
		}
	}
	if count < qrContrastSampleMinimum {
		return nil, false
	}

	quad := expandQuad([4]pointF{tl, tr, br, bl}, qrWarpPadding)
	side := minInt(bounds.Dx(), bounds.Dy())
	if side <= 0 {
		return nil, false
	}
	return warpQuadToSquare(img, quad, side), true
}

// expandQuad 让检测到的四边形从中心向外扩张，以恢复可能刚好位于对比度极值之外的二维码静区。
func expandQuad(quad [4]pointF, scale float64) [4]pointF {
	var center pointF
	for _, p := range quad {
		center.x += p.x
		center.y += p.y
	}
	center.x /= float64(len(quad))
	center.y /= float64(len(quad))
	for i, p := range quad {
		quad[i] = pointF{
			x: center.x + (p.x-center.x)*scale,
			y: center.y + (p.y-center.y)*scale,
		}
	}
	return quad
}

// warpQuadToSquare 把近似屏幕四边形采样成正方形图片，为二维码解码器提供正面候选图。
func warpQuadToSquare(src image.Image, quad [4]pointF, side int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, side, side))
	for y := 0; y < side; y++ {
		v := float64(y) / float64(side-1)
		for x := 0; x < side; x++ {
			u := float64(x) / float64(side-1)
			p := bilinearPoint(quad, u, v)
			dst.Set(x, y, sampleImageNearest(src, p.x, p.y))
		}
	}
	return dst
}

// bilinearPoint 把归一化正方形坐标映射到 warpQuadToSquare 使用的源四边形中。
func bilinearPoint(quad [4]pointF, u float64, v float64) pointF {
	tl, tr, br, bl := quad[0], quad[1], quad[2], quad[3]
	return pointF{
		x: (1-u)*(1-v)*tl.x + u*(1-v)*tr.x + u*v*br.x + (1-u)*v*bl.x,
		y: (1-u)*(1-v)*tl.y + u*(1-v)*tr.y + u*v*br.y + (1-u)*v*bl.y,
	}
}

// sampleImageNearest 读取最近的源像素；对越界采样返回白色，让扭正后的边缘更利于扫描。
func sampleImageNearest(img image.Image, x float64, y float64) color.Color {
	bounds := img.Bounds()
	ix := int(x + 0.5)
	iy := int(y + 0.5)
	if ix < bounds.Min.X || ix >= bounds.Max.X || iy < bounds.Min.Y || iy >= bounds.Max.Y {
		return color.RGBA{R: qrCanvasWhite, G: qrCanvasWhite, B: qrCanvasWhite, A: 255}
	}
	return img.At(ix, iy)
}

// luminance 把 16 位 RGBA 颜色通道转换为 8 位感知亮度值，供对比度检测使用。
func luminance(r uint32, g uint32, b uint32) uint8 {
	return uint8((299*int(r>>8) + 587*int(g>>8) + 114*int(b>>8)) / 1000)
}

// isHighContrastPixel 判断一个像素是否足够接近黑色或白色，从而可能属于二维码拼图或引导边框。
func isHighContrastPixel(c color.Color) bool {
	r, g, b, a := c.RGBA()
	if a == 0 {
		return false
	}
	value := int(luminance(r, g, b))
	return value <= qrContrastDistance || value >= 255-qrContrastDistance
}

// expandRect 在保持候选矩形位于图片边界内的同时为其添加 padding。
func expandRect(rect image.Rectangle, padding int, bounds image.Rectangle) image.Rectangle {
	return image.Rect(rect.Min.X-padding, rect.Min.Y-padding, rect.Max.X+padding, rect.Max.Y+padding).Intersect(bounds)
}

// squareAroundRect 把候选区域扩展为受边界限制的正方形，因为生成的 TransferGo 视频使用正方形画布。
func squareAroundRect(rect image.Rectangle, bounds image.Rectangle) image.Rectangle {
	side := maxInt(rect.Dx(), rect.Dy())
	side = minInt(side, minInt(bounds.Dx(), bounds.Dy()))
	cx := rect.Min.X + rect.Dx()/2
	cy := rect.Min.Y + rect.Dy()/2
	x0 := cx - side/2
	y0 := cy - side/2
	if x0 < bounds.Min.X {
		x0 = bounds.Min.X
	}
	if y0 < bounds.Min.Y {
		y0 = bounds.Min.Y
	}
	if x0+side > bounds.Max.X {
		x0 = bounds.Max.X - side
	}
	if y0+side > bounds.Max.Y {
		y0 = bounds.Max.Y - side
	}
	return image.Rect(x0, y0, x0+side, y0+side)
}

// gridCellRects 把一张候选图片切成编码时使用的相同网格布局，让每个格子都可以独立解码。
func gridCellRects(bounds image.Rectangle, gridSize int) []image.Rectangle {
	rects := make([]image.Rectangle, 0, gridSize*gridSize)
	for row := 0; row < gridSize; row++ {
		for col := 0; col < gridSize; col++ {
			rects = append(rects, image.Rect(
				bounds.Min.X+col*bounds.Dx()/gridSize,
				bounds.Min.Y+row*bounds.Dy()/gridSize,
				bounds.Min.X+(col+1)*bounds.Dx()/gridSize,
				bounds.Min.Y+(row+1)*bounds.Dy()/gridSize,
			))
		}
	}
	return rects
}

// bytesToLatin1String 把每个字节映射到相同编号的 rune，让二维码写入器能通过字符串 API 携带二进制协议数据，而不会发生 UTF-8 扩展。
func bytesToLatin1String(data []byte) string {
	runes := make([]rune, len(data))
	for i, b := range data {
		runes[i] = rune(b)
	}
	return string(runes)
}

// latin1StringToBytes 拒绝 Latin-1 之外的任何 rune，因为这样的值不可能来自 EncodeQRPNG 使用的保字节映射。
func latin1StringToBytes(text string) ([]byte, error) {
	out := make([]byte, 0, len(text))
	for _, r := range text {
		if r > 255 {
			return nil, fmt.Errorf("decoded QR text contains non-Latin-1 rune U+%04X", r)
		}
		out = append(out, byte(r))
	}
	return out, nil
}

// minRenderedQRSize 返回某个二维码版本的模块数，加上编码器边距所需的静区像素。
func minRenderedQRSize(version int) int {
	return 21 + 4*(version-1) + qrQuietZoneModules*2
}
