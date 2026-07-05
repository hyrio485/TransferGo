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

// pointF stores a floating-point image coordinate used while approximating a
// filmed screen quadrilateral before warping it back into a square.
type pointF struct {
	x float64
	y float64
}

const (
	// A smaller QR margin increases payload density without removing the quiet
	// zone entirely. Two modules is a practical compromise for filmed screens.
	qrEncodeMargin     = 2
	qrQuietZoneModules = qrEncodeMargin * 2
	// qrCanvasWhite and qrGuideBlack are fixed grayscale anchors used for the
	// generated video canvas and tile borders.
	qrCanvasWhite = 255
	qrGuideBlack  = 16
	// qrGuideWidth makes each tile visible to both humans and simple contrast
	// detection without crowding the QR quiet zone.
	qrGuideWidth = 6
	// High-contrast sampling is used only to find likely QR regions inside a
	// larger camera frame; the actual QR decoder still validates the payload.
	qrContrastDistance      = 96
	qrContrastSampleMinimum = 24
	// qrWarpPadding expands the estimated screen quadrilateral slightly so
	// clipped QR borders are more likely to survive the perspective warp.
	qrWarpPadding = 1.14
)

// qrRenderOptions collects the geometry that both encoding and validation need
// when multiple QR payloads are packed into one video frame.
type qrRenderOptions struct {
	qrSize      int
	qrVersion   int
	videoWidth  int
	videoHeight int
	gridSize    int
}

// validate rejects render options that would make QR codes too small to encode
// or too large to fit inside the requested grid.
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

// slotsPerImage returns how many protocol frames fit in one rendered video
// image for the current grid size.
func (opt qrRenderOptions) slotsPerImage() int {
	return opt.gridSize * opt.gridSize
}

// minTileSize returns the limiting tile side in pixels, accounting for non-even
// divisions of the output canvas.
func (opt qrRenderOptions) minTileSize() int {
	return minInt(opt.videoWidth/opt.gridSize, opt.videoHeight/opt.gridSize)
}

// effectiveQRSize clamps the requested QR size to the available tile size so a
// user can request a generous size without overflowing the grid.
func (opt qrRenderOptions) effectiveQRSize() int {
	return minInt(opt.qrSize, opt.minTileSize())
}

// tileRect returns the exact canvas rectangle for one zero-based grid slot.
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

// renderedFrameCount computes the PNG count needed to hold all protocol frames
// once the grid packing factor is applied.
func renderedFrameCount(protocolFrames int, opt qrRenderOptions) int {
	if protocolFrames <= 0 {
		return 0
	}
	slots := opt.slotsPerImage()
	return (protocolFrames + slots - 1) / slots
}

// writeQRPayloadFrames serializes each payload into a zero-padded PNG sequence.
// The file naming convention is chosen to match ffmpeg's frame_%06d.png input
// pattern and to keep lexicographic sorting equal to frame order.
func writeQRPayloadFrames(payloads [][]byte, dir string, opt qrRenderOptions, progress func(done int, total int)) error {
	if err := opt.validate(); err != nil {
		return fmt.Errorf("validate QR render options: %w", err)
	}
	qrSize := opt.effectiveQRSize()
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
			progress(imageIndex, renderedFrameCount(len(payloads), opt))
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
		return nil, fmt.Errorf("encode QR image: %w", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode QR PNG: %w", err)
	}
	return buf.Bytes(), nil
}

// encodeQRGray creates the raw grayscale QR bitmap used by both validation and
// PNG rendering. It keeps payload encoding separate from file I/O.
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

// newMosaicCanvas creates a white RGBA canvas large enough to hold a full QR
// grid. RGBA is used here because later drawing code writes directly into
// pixel channels for speed and deterministic output.
func newMosaicCanvas(width int, height int) *image.RGBA {
	bounds := image.Rect(0, 0, width, height)
	img := image.NewRGBA(bounds)
	bg := image.NewUniform(color.RGBA{R: qrCanvasWhite, G: qrCanvasWhite, B: qrCanvasWhite, A: 255})
	draw.Draw(img, bounds, bg, image.Point{}, draw.Src)
	return img
}

// drawMosaicGuides outlines each tile so filmed frames have strong rectangular
// contrast that can be rediscovered during decode.
func drawMosaicGuides(dst *image.RGBA, opt qrRenderOptions) {
	for tile := 0; tile < opt.gridSize*opt.gridSize; tile++ {
		drawTileGuide(dst, opt.tileRect(tile))
	}
}

// drawTileGuide paints a simple dark border around one tile. Very small tiles
// skip the guide because the border would consume too much QR space.
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

// drawQRIntoTile centers one plain black-and-white QR code inside a tile.
// Monochrome output gives the camera a higher-contrast target than the old RGB
// channel overlay, which was dense but fragile after screen filming.
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

// decodeQRCodePayloads loads one extracted image and returns every unique QR
// payload found by the layered decode strategy.
func decodeQRCodePayloads(path string, gridSize int) ([][]byte, error) {
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

// decodeQRCodePayloadsFromImage tries the whole image first, then cropped and
// warped candidates. This keeps perfect generated frames fast while giving
// filmed or tilted captures extra recovery attempts.
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

// addSingleDecode tries the ordinary QR reader and records the payload if it
// succeeds. Decode failures are expected for many candidate crops.
func addSingleDecode(acc *payloadAccumulator, img image.Image) {
	payload, err := decodeQRCodeBytesFromImage(img)
	if err == nil {
		acc.add(payload)
	}
}

// addMultipleDecode uses the multi-QR reader for full mosaics or larger crops
// that may contain several QR codes at once.
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
		return nil, fmt.Errorf("create QR bitmap: %w", err)
	}

	result, err := gozxingqr.NewQRCodeReader().Decode(bmp, qrDecodeHints())
	if err != nil {
		return nil, fmt.Errorf("decode QR bitmap: %w", err)
	}
	return qrResultBytes(result)
}

// decodeMultipleQRCodeBytesFromImage decodes every QR code the multi-reader can
// identify in one image candidate.
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

// qrDecodeHints centralizes gozxing decode options so single and multi readers
// interpret TransferGo payloads the same way.
func qrDecodeHints() map[gozxing.DecodeHintType]interface{} {
	// TRY_HARDER costs more CPU but is useful for frames extracted from filmed
	// video, where blur, scaling, and compression artifacts are common.
	return map[gozxing.DecodeHintType]interface{}{
		gozxing.DecodeHintType_CHARACTER_SET:    "ISO-8859-1",
		gozxing.DecodeHintType_POSSIBLE_FORMATS: gozxing.BarcodeFormats{gozxing.BarcodeFormat_QR_CODE},
		gozxing.DecodeHintType_TRY_HARDER:       true,
	}
}

// qrResultBytes extracts exact protocol bytes from a gozxing result, preferring
// byte segments over text because the payload is binary data.
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

// payloadAccumulator keeps decode retries from returning the same QR payload
// multiple times when several candidate crops overlap.
type payloadAccumulator struct {
	seen     map[string]struct{}
	payloads [][]byte
}

// newPayloadAccumulator starts an empty de-duplicating payload collector.
func newPayloadAccumulator() *payloadAccumulator {
	return &payloadAccumulator{seen: make(map[string]struct{})}
}

// add stores a defensive copy of a payload if it has not already been seen.
func (acc *payloadAccumulator) add(payload []byte) {
	key := string(payload)
	if _, ok := acc.seen[key]; ok {
		return
	}
	acc.seen[key] = struct{}{}
	acc.payloads = append(acc.payloads, append([]byte{}, payload...))
}

// imageToGray makes black-white QR detection deterministic. It also avoids the
// expensive old behavior of trying the same camera frame as three color
// channels, which no longer helps now that encoding is monochrome.
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

// cropGray copies one grayscale candidate region into a zero-based image so the
// QR decoder does not need to handle source-coordinate offsets.
func cropGray(src *image.Gray, rect image.Rectangle) *image.Gray {
	rect = rect.Intersect(src.Bounds())
	dst := image.NewGray(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Draw(dst, dst.Bounds(), src, rect.Min, draw.Src)
	return dst
}

// decodeCandidateRects returns square-ish regions worth retrying when the whole
// frame decode fails. The generated canvas is square, but phone recordings may
// include surrounding screen or camera background.
func decodeCandidateRects(img image.Image) []image.Rectangle {
	bounds := img.Bounds()
	rects := []image.Rectangle{bounds}
	rects = appendUniqueRect(rects, centeredSquare(bounds))
	if rect, ok := contrastContentSquare(img); ok {
		rects = appendUniqueRect(rects, rect)
	}
	return rects
}

// appendUniqueRect keeps the candidate list compact so the same crop is not
// decoded repeatedly.
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

// centeredSquare is the cheapest fallback crop for recordings that preserve the
// video in the middle of a wider camera frame.
func centeredSquare(bounds image.Rectangle) image.Rectangle {
	side := minInt(bounds.Dx(), bounds.Dy())
	x0 := bounds.Min.X + (bounds.Dx()-side)/2
	y0 := bounds.Min.Y + (bounds.Dy()-side)/2
	return image.Rect(x0, y0, x0+side, y0+side)
}

// contrastContentSquare estimates where the QR mosaic sits inside a filmed
// frame. It looks for very dark or very bright samples, then expands the result
// into a square because the generated video canvas is square.
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

// contrastContentWarp uses the high-contrast extremes as a rough quadrilateral
// and maps that shape back into a square. It is deliberately approximate: phone
// recordings do not provide the real screen geometry, but this often rescues
// mildly tilted captures.
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

// expandQuad grows a detected quadrilateral away from its center to recover
// QR quiet zones that might sit just outside the contrast extrema.
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

// warpQuadToSquare samples an approximate screen quadrilateral into a square
// image, giving the QR decoder a front-facing candidate.
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

// bilinearPoint maps normalized square coordinates into the source
// quadrilateral used by warpQuadToSquare.
func bilinearPoint(quad [4]pointF, u float64, v float64) pointF {
	tl, tr, br, bl := quad[0], quad[1], quad[2], quad[3]
	return pointF{
		x: (1-u)*(1-v)*tl.x + u*(1-v)*tr.x + u*v*br.x + (1-u)*v*bl.x,
		y: (1-u)*(1-v)*tl.y + u*(1-v)*tr.y + u*v*br.y + (1-u)*v*bl.y,
	}
}

// sampleImageNearest reads the closest source pixel and returns white for
// out-of-bounds samples so warped edges remain scanner-friendly.
func sampleImageNearest(img image.Image, x float64, y float64) color.Color {
	bounds := img.Bounds()
	ix := int(x + 0.5)
	iy := int(y + 0.5)
	if ix < bounds.Min.X || ix >= bounds.Max.X || iy < bounds.Min.Y || iy >= bounds.Max.Y {
		return color.RGBA{R: qrCanvasWhite, G: qrCanvasWhite, B: qrCanvasWhite, A: 255}
	}
	return img.At(ix, iy)
}

// luminance converts 16-bit RGBA color channels into an 8-bit perceptual
// brightness value used by contrast detection.
func luminance(r uint32, g uint32, b uint32) uint8 {
	return uint8((299*int(r>>8) + 587*int(g>>8) + 114*int(b>>8)) / 1000)
}

// isHighContrastPixel reports whether a pixel is close enough to black or white
// to plausibly belong to a QR mosaic or guide border.
func isHighContrastPixel(c color.Color) bool {
	r, g, b, a := c.RGBA()
	if a == 0 {
		return false
	}
	value := int(luminance(r, g, b))
	return value <= qrContrastDistance || value >= 255-qrContrastDistance
}

// expandRect pads a candidate rectangle while keeping it inside image bounds.
func expandRect(rect image.Rectangle, padding int, bounds image.Rectangle) image.Rectangle {
	return image.Rect(rect.Min.X-padding, rect.Min.Y-padding, rect.Max.X+padding, rect.Max.Y+padding).Intersect(bounds)
}

// squareAroundRect expands a candidate to a bounded square because generated
// TransferGo videos use square canvases.
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

// gridCellRects splits one candidate image into the same grid layout used while
// encoding, allowing each tile to be decoded independently.
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

// minRenderedQRSize returns the module count for a QR version plus the quiet
// zone pixels required by the encoder margin.
func minRenderedQRSize(version int) int {
	return 21 + 4*(version-1) + qrQuietZoneModules*2
}
