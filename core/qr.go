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

// collectStats records how noisy the extracted image set was. Decode reports
// these numbers so users can tell the difference between missing payload frames
// and images that simply were not readable QR codes.
type collectStats struct {
	images         int
	decoded        int
	ignored        int
	duplicates     int
	decodeFailures int
}

type qrColorChannel int

type pointF struct {
	x float64
	y float64
}

const (
	qrChannelRed qrColorChannel = iota
	qrChannelGreen
	qrChannelBlue
)

const (
	qrChannelsToDecode       = 3
	qrQuietZoneModules       = 4
	qrCanvasWhite            = 255
	qrSoftWhite              = 224
	qrSoftBlack              = 48
	qrGuideBlack             = 16
	qrGuideWidth             = 6
	qrSaturatedColorDistance = 35
	qrWarpPadding            = 1.14
)

type qrRenderOptions struct {
	qrSize      int
	qrVersion   int
	videoWidth  int
	videoHeight int
	gridSize    int
}

func (opt qrRenderOptions) validate() error {
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

func (opt qrRenderOptions) slotsPerImage() int {
	return opt.gridSize * opt.gridSize * qrChannelsToDecode
}

func (opt qrRenderOptions) minTileSize() int {
	return minInt(opt.videoWidth/opt.gridSize, opt.videoHeight/opt.gridSize)
}

func (opt qrRenderOptions) effectiveQRSize() int {
	return minInt(opt.qrSize, opt.minTileSize())
}

func (opt qrRenderOptions) tileRect(tile int) image.Rectangle {
	row := tile / opt.gridSize
	col := tile % opt.gridSize
	return image.Rect(
		col*opt.videoWidth/opt.gridSize,
		row*opt.videoHeight/opt.gridSize,
		(col+1)*opt.videoWidth/opt.gridSize,
		(row+1)*opt.videoHeight/opt.gridSize,
	)
}

func renderedFrameCount(protocolFrames int, opt qrRenderOptions) int {
	if protocolFrames <= 0 {
		return 0
	}
	slots := opt.slotsPerImage()
	return (protocolFrames + slots - 1) / slots
}

// writeQRFrames serializes each protocol frame into a zero-padded PNG sequence.
// The file naming convention is chosen to match ffmpeg's frame_%06d.png input
// pattern and to keep lexicographic sorting equal to frame order.
func writeQRFrames(frames []transferFrame, dir string, opt qrRenderOptions) error {
	if err := opt.validate(); err != nil {
		return err
	}
	qrSize := opt.effectiveQRSize()
	slots := opt.slotsPerImage()

	for start, imageIndex := 0, 1; start < len(frames); start, imageIndex = start+slots, imageIndex+1 {
		img := newMosaicCanvas(opt.videoWidth, opt.videoHeight)
		drawMosaicGuides(img, opt)
		end := minInt(start+slots, len(frames))
		for frameIndex := start; frameIndex < end; frameIndex++ {
			slot := frameIndex - start
			tile := slot / qrChannelsToDecode
			channel := qrColorChannel(slot % qrChannelsToDecode)
			qrImg, err := encodeQRGray(marshalFrame(frames[frameIndex]), qrSize, opt.qrVersion)
			if err != nil {
				return fmt.Errorf("encode QR frame %d: %w", frames[frameIndex].Seq, err)
			}
			if err := drawQRIntoChannel(img, qrImg, opt.tileRect(tile), channel); err != nil {
				return fmt.Errorf("render QR frame %d: %w", frames[frameIndex].Seq, err)
			}
		}

		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return err
		}
		path := filepath.Join(dir, fmt.Sprintf("frame_%06d.png", imageIndex))
		if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
			return err
		}
	}
	return nil
}

// encodeQRPNG stores arbitrary bytes in a QR code. gozxing's writer accepts
// text, so bytes are mapped through ISO-8859-1 where every byte value maps to
// the same code point. Error correction level L is used to maximize capacity.
func encodeQRPNG(payload []byte, size int, version int) ([]byte, error) {
	img, err := encodeQRGray(payload, size, version)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeQRGray(payload []byte, size int, version int) (*image.Gray, error) {
	content := bytesToLatin1String(payload)
	hints := map[gozxing.EncodeHintType]interface{}{
		gozxing.EncodeHintType_CHARACTER_SET:    "ISO-8859-1",
		gozxing.EncodeHintType_ERROR_CORRECTION: qrdecoder.ErrorCorrectionLevel_L,
		gozxing.EncodeHintType_MARGIN:           4,
	}
	if version > 0 {
		hints[gozxing.EncodeHintType_QR_VERSION] = version
	}

	matrix, err := gozxingqr.NewQRCodeWriter().Encode(content, gozxing.BarcodeFormat_QR_CODE, size, size, hints)
	if err != nil {
		return nil, err
	}

	return bitMatrixToGray(matrix), nil
}

// bitMatrixToGray converts the QR bit matrix into a plain black-on-white image.
// A grayscale image is compact, deterministic, and sufficient for PNG output.
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

func newMosaicCanvas(width int, height int) *image.RGBA {
	bounds := image.Rect(0, 0, width, height)
	img := image.NewRGBA(bounds)
	bg := image.NewUniform(color.RGBA{R: qrCanvasWhite, G: qrCanvasWhite, B: qrCanvasWhite, A: 255})
	draw.Draw(img, bounds, bg, image.Point{}, draw.Src)
	return img
}

func drawMosaicGuides(dst *image.RGBA, opt qrRenderOptions) {
	for tile := 0; tile < opt.gridSize*opt.gridSize; tile++ {
		drawTileGuide(dst, opt.tileRect(tile))
	}
}

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

func drawQRIntoChannel(dst *image.RGBA, qr *image.Gray, tile image.Rectangle, channel qrColorChannel) error {
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
			if qr.Pix[srcOffset+x] >= 128 {
				continue
			}
			pixelOffset := dstOffset + x*4
			for c := 0; c < qrChannelsToDecode; c++ {
				if dst.Pix[pixelOffset+c] > qrSoftWhite {
					dst.Pix[pixelOffset+c] = qrSoftWhite
				}
			}
			dst.Pix[pixelOffset+int(channel)] = qrSoftBlack
		}
	}
	return nil
}

func decodeQRCodePayloads(path string, gridSize int) ([][]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = file.Close()
	}()

	img, _, err := image.Decode(file)
	if err != nil {
		return nil, err
	}

	payloads := decodeQRCodePayloadsFromImage(img, gridSize)
	if len(payloads) == 0 {
		return nil, fmt.Errorf("no QR code decoded")
	}
	return payloads, nil
}

func decodeQRCodePayloadsFromImage(img image.Image, gridSize int) [][]byte {
	if gridSize <= 0 {
		gridSize = defaultGridSize
	}

	acc := newPayloadAccumulator()
	addSingleDecode(acc, img)
	addMultipleDecode(acc, img)

	for _, rect := range decodeCandidateRects(img) {
		for _, channel := range []qrColorChannel{qrChannelRed, qrChannelGreen, qrChannelBlue} {
			channelImg := imageChannelToGray(img, channel, rect)
			addMultipleDecode(acc, channelImg)
			addSingleDecode(acc, channelImg)
			for _, cell := range gridCellRects(channelImg.Bounds(), gridSize) {
				addSingleDecode(acc, cropGray(channelImg, cell))
			}
		}
	}
	if warped, ok := saturatedContentWarp(img); ok {
		for _, channel := range []qrColorChannel{qrChannelRed, qrChannelGreen, qrChannelBlue} {
			channelImg := imageChannelToGray(warped, channel, warped.Bounds())
			addMultipleDecode(acc, channelImg)
			addSingleDecode(acc, channelImg)
			for _, cell := range gridCellRects(channelImg.Bounds(), gridSize) {
				addSingleDecode(acc, cropGray(channelImg, cell))
			}
		}
	}

	return acc.payloads
}

func addSingleDecode(acc *payloadAccumulator, img image.Image) {
	payload, err := decodeQRCodeBytesFromImage(img)
	if err == nil {
		acc.add(payload)
	}
}

func addMultipleDecode(acc *payloadAccumulator, img image.Image) {
	payloads, err := decodeMultipleQRCodeBytesFromImage(img)
	if err != nil {
		return
	}
	for _, payload := range payloads {
		acc.add(payload)
	}
}

// decodeQRCodeBytesFromImage reverses encodeQRPNG. Some QR decoders expose
// original byte segments directly; when they do not, the Latin-1 text mapping
// is used as a fallback to recover the exact protocol bytes.
func decodeQRCodeBytesFromImage(img image.Image) ([]byte, error) {
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return nil, err
	}

	result, err := gozxingqr.NewQRCodeReader().Decode(bmp, qrDecodeHints())
	if err != nil {
		return nil, err
	}
	return qrResultBytes(result)
}

func decodeMultipleQRCodeBytesFromImage(img image.Image) ([][]byte, error) {
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return nil, err
	}
	results, err := gozxingmultiqr.NewQRCodeMultiReader().DecodeMultiple(bmp, qrDecodeHints())
	if err != nil {
		return nil, err
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

func qrDecodeHints() map[gozxing.DecodeHintType]interface{} {
	// TRY_HARDER costs more CPU but is useful for frames extracted from filmed
	// video, where blur, scaling, and compression artifacts are common.
	return map[gozxing.DecodeHintType]interface{}{
		gozxing.DecodeHintType_CHARACTER_SET:    "ISO-8859-1",
		gozxing.DecodeHintType_POSSIBLE_FORMATS: gozxing.BarcodeFormats{gozxing.BarcodeFormat_QR_CODE},
		gozxing.DecodeHintType_TRY_HARDER:       true,
	}
}

func qrResultBytes(result *gozxing.Result) ([]byte, error) {
	// Prefer raw byte segments when available. This avoids any ambiguity in text
	// conversion and is the most faithful representation of the QR payload.
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

type payloadAccumulator struct {
	seen     map[string]struct{}
	payloads [][]byte
}

func newPayloadAccumulator() *payloadAccumulator {
	return &payloadAccumulator{seen: make(map[string]struct{})}
}

func (acc *payloadAccumulator) add(payload []byte) {
	key := string(payload)
	if _, ok := acc.seen[key]; ok {
		return
	}
	acc.seen[key] = struct{}{}
	acc.payloads = append(acc.payloads, append([]byte{}, payload...))
}

func imageChannelToGray(src image.Image, channel qrColorChannel, rect image.Rectangle) *image.Gray {
	rect = rect.Intersect(src.Bounds())
	dst := image.NewGray(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			r, g, b, a := src.At(x, y).RGBA()
			value := uint8(255)
			if a != 0 {
				switch channel {
				case qrChannelRed:
					value = uint8(r >> 8)
				case qrChannelGreen:
					value = uint8(g >> 8)
				case qrChannelBlue:
					value = uint8(b >> 8)
				}
			}
			dst.SetGray(x-rect.Min.X, y-rect.Min.Y, color.Gray{Y: value})
		}
	}
	return dst
}

func cropGray(src *image.Gray, rect image.Rectangle) *image.Gray {
	rect = rect.Intersect(src.Bounds())
	dst := image.NewGray(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Draw(dst, dst.Bounds(), src, rect.Min, draw.Src)
	return dst
}

func decodeCandidateRects(img image.Image) []image.Rectangle {
	bounds := img.Bounds()
	rects := []image.Rectangle{bounds}
	rects = appendUniqueRect(rects, centeredSquare(bounds))
	if rect, ok := saturatedContentSquare(img); ok {
		rects = appendUniqueRect(rects, rect)
	}
	return rects
}

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

func centeredSquare(bounds image.Rectangle) image.Rectangle {
	side := minInt(bounds.Dx(), bounds.Dy())
	x0 := bounds.Min.X + (bounds.Dx()-side)/2
	y0 := bounds.Min.Y + (bounds.Dy()-side)/2
	return image.Rect(x0, y0, x0+side, y0+side)
}

func saturatedContentSquare(img image.Image) (image.Rectangle, bool) {
	bounds := img.Bounds()
	step := maxInt(1, minInt(bounds.Dx(), bounds.Dy())/500)
	minX, minY := bounds.Max.X, bounds.Max.Y
	maxX, maxY := bounds.Min.X, bounds.Min.Y
	count := 0

	for y := bounds.Min.Y; y < bounds.Max.Y; y += step {
		for x := bounds.Min.X; x < bounds.Max.X; x += step {
			r, g, b, _ := img.At(x, y).RGBA()
			rr, gg, bb := int(r>>8), int(g>>8), int(b>>8)
			if maxInt3(rr, gg, bb)-minInt3(rr, gg, bb) < qrSaturatedColorDistance {
				continue
			}
			minX = minInt(minX, x)
			minY = minInt(minY, y)
			maxX = maxInt(maxX, x)
			maxY = maxInt(maxY, y)
			count++
		}
	}
	if count < 24 {
		return image.Rectangle{}, false
	}

	rect := image.Rect(minX, minY, maxX+step, maxY+step).Intersect(bounds)
	padding := maxInt(step*2, minInt(bounds.Dx(), bounds.Dy())/50)
	return squareAroundRect(expandRect(rect, padding, bounds), bounds), true
}

func saturatedContentWarp(img image.Image) (image.Image, bool) {
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
			r, g, b, _ := img.At(x, y).RGBA()
			rr, gg, bb := int(r>>8), int(g>>8), int(b>>8)
			if maxInt3(rr, gg, bb)-minInt3(rr, gg, bb) < qrSaturatedColorDistance {
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
	if count < 24 {
		return nil, false
	}

	quad := expandQuad([4]pointF{tl, tr, br, bl}, qrWarpPadding)
	side := minInt(bounds.Dx(), bounds.Dy())
	if side <= 0 {
		return nil, false
	}
	return warpQuadToSquare(img, quad, side), true
}

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

func bilinearPoint(quad [4]pointF, u float64, v float64) pointF {
	tl, tr, br, bl := quad[0], quad[1], quad[2], quad[3]
	return pointF{
		x: (1-u)*(1-v)*tl.x + u*(1-v)*tr.x + u*v*br.x + (1-u)*v*bl.x,
		y: (1-u)*(1-v)*tl.y + u*(1-v)*tr.y + u*v*br.y + (1-u)*v*bl.y,
	}
}

func sampleImageNearest(img image.Image, x float64, y float64) color.Color {
	bounds := img.Bounds()
	ix := int(x + 0.5)
	iy := int(y + 0.5)
	if ix < bounds.Min.X || ix >= bounds.Max.X || iy < bounds.Min.Y || iy >= bounds.Max.Y {
		return color.RGBA{R: qrCanvasWhite, G: qrCanvasWhite, B: qrCanvasWhite, A: 255}
	}
	return img.At(ix, iy)
}

func expandRect(rect image.Rectangle, padding int, bounds image.Rectangle) image.Rectangle {
	return image.Rect(rect.Min.X-padding, rect.Min.Y-padding, rect.Max.X+padding, rect.Max.Y+padding).Intersect(bounds)
}

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

// bytesToLatin1String maps each byte to the same-numbered rune so the QR writer
// can carry binary protocol data through a string API without UTF-8 expansion.
func bytesToLatin1String(data []byte) string {
	runes := make([]rune, len(data))
	for i, b := range data {
		runes[i] = rune(b)
	}
	return string(runes)
}

// latin1StringToBytes rejects any rune outside Latin-1 because such a value
// could not have come from the byte-preserving mapping used by encodeQRPNG.
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

// collectFramesFromImages decodes every extracted PNG and keeps only valid
// TransferGo frames. Unreadable or unrelated images are counted and skipped, but
// conflicting protocol frames fail fast because they would make restore unsafe.
func collectFramesFromImages(paths []string, gridSize int) (map[uint32]transferFrame, uint32, collectStats, error) {
	frames := make(map[uint32]transferFrame)
	var total uint32
	stats := collectStats{images: len(paths)}

	for _, path := range paths {
		payloads, err := decodeQRCodePayloads(path, gridSize)
		if err != nil {
			stats.decodeFailures++
			continue
		}
		for _, payload := range payloads {
			// A readable QR might belong to another app or an older protocol.
			// Treat it as noise rather than aborting the whole decode.
			frame, err := parseFrame(payload)
			if err != nil {
				stats.ignored++
				continue
			}
			stats.decoded++
			if total == 0 {
				total = frame.Total
			} else if total != frame.Total {
				return nil, 0, stats, fmt.Errorf("frame %d reports total %d, expected %d", frame.Seq, frame.Total, total)
			}
			if existing, ok := frames[frame.Seq]; ok {
				// Duplicate captures are expected when sample FPS is higher than
				// the source video FPS. Identical duplicates are harmless;
				// different bytes for the same sequence mean the recording is
				// ambiguous.
				if !sameFrame(existing, frame) {
					return nil, 0, stats, fmt.Errorf("conflicting duplicate frame %d", frame.Seq)
				}
				stats.duplicates++
				continue
			}
			frames[frame.Seq] = frame
		}
	}

	if total == 0 {
		return nil, 0, stats, fmt.Errorf("no TransferGo QR frames decoded from %d extracted image(s)", len(paths))
	}
	return frames, total, stats, nil
}

func minRenderedQRSize(version int) int {
	return 21 + 4*(version-1) + qrQuietZoneModules*2
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt3(a int, b int, c int) int {
	return minInt(minInt(a, b), c)
}

func maxInt3(a int, b int, c int) int {
	return maxInt(maxInt(a, b), c)
}
