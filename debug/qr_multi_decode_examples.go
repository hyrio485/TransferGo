package main

import (
	"fmt"
	"image"

	// 这两个空白导入非常重要。
	// image.Decode 本身只负责调度解码器，具体支持哪些图片格式取决于是否注册了对应解码器。
	// 导入 image/jpeg 后，程序才能解码 jpg、jpeg 文件。
	// 导入 image/png 后，程序也可以顺手支持 png 文件，方便后续替换测试图片。
	_ "image/jpeg"
	_ "image/png"
	"os"

	"github.com/makiuchi-d/gozxing"
	gozxingmultiqr "github.com/makiuchi-d/gozxing/multi/qrcode"
)

// imagePath 是本示例固定读取的图片路径。
// 这里按你的要求不接收命令行参数，方便直接运行和复现实验结果。
// 如果后续要迁到生产代码里，通常可以把这个常量改成函数入参、上传文件路径，或者字节流解码后的 image.Image。
const imagePath = "/Users/hyrio/Workspace/Projects/hyrio/services/TransferGo/files/img.png"

func main() {
	// 输出图片路径，方便在终端日志里确认当前识别的是哪一张图。
	// 生产环境里可以换成结构化日志字段，比如 image_path。
	fmt.Println("图片路径：", imagePath)

	// 主流程只跑纯 Go 的 gozxing 方案，不依赖 OpenCV、pkg-config 或系统原生动态库。
	// 这里保留错误输出而不是 panic，是为了让调试示例更接近服务端代码的处理方式。
	if err := runGozxingExample(); err != nil {
		fmt.Println("gozxing 识别失败：", err)
	}
}

// runGozxingExample 串起完整识别流程。
// 当前流程分三步：
// 一、从固定路径读取图片。
// 二、把图片交给 QRCodeMultiReader 一次性查找并解码多个二维码。
// 三、把每个二维码的内容和定位点坐标打印出来。
func runGozxingExample() error {
	fmt.Println("方式：gozxing，QRCodeMultiReader")

	// 先把磁盘文件解码成 Go 标准库的 image.Image。
	// gozxing 可以直接从 image.Image 创建亮度源，所以这里不需要额外的图片格式转换。
	img, err := loadImage(imagePath)
	if err != nil {
		return err
	}

	// DecodeMultiple 会尝试在整张图里找出多个二维码。
	// 对你的样例图来说，9 个二维码都是黑码白底，所以只做普通图识别，不做反色兜底。
	results, err := decodeMultipleWithGozxing(img)
	if err != nil {
		return err
	}

	// 正常情况下，gozxing 找不到二维码时通常会返回 NotFoundException。
	// 这里仍保留 len 判断，是为了防御未来库行为变化，或者上层包装返回空切片的情况。
	if len(results) == 0 {
		fmt.Println("未识别到二维码")
		return nil
	}

	// results 里每一项代表一个二维码识别结果。
	// GetText 是二维码内容。
	// GetResultPoints 是二维码定位点，通常是三个 finder pattern 的坐标，不一定是完整四个角。
	fmt.Printf("识别到 %d 个二维码\n", len(results))
	for i, result := range results {
		fmt.Printf("[%d] 内容：%s\n", i+1, result.GetText())
		fmt.Printf("[%d] 位置：%s\n", i+1, formatGozxingResultPoints(result.GetResultPoints()))
	}

	return nil
}

// loadImage 从磁盘读取图片，并解码为 image.Image。
// 这个函数只关心输入图片的读取和格式解析，不包含任何二维码识别逻辑。
// 这样拆开以后，后续要改成从 HTTP 上传文件、对象存储或内存字节流读取时，只需要替换这一层。
func loadImage(path string) (image.Image, error) {
	// os.Open 只打开文件句柄，不会一次性把整个文件读入内存。
	// image.Decode 会从这个句柄中逐步读取图片数据。
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	// defer Close 确保函数返回前关闭文件句柄。
	// 对短命令行程序影响不大，但服务端长期运行时必须养成这个习惯。
	defer file.Close()

	// image.Decode 会根据文件头自动判断图片格式。
	// 是否支持对应格式，取决于上面的 image/jpeg、image/png 等解码器是否已经被导入注册。
	img, _, err := image.Decode(file)
	if err != nil {
		return nil, err
	}

	return img, nil
}

// decodeMultipleWithGozxing 使用 gozxing 的多二维码读取器识别整张图。
// 这里选择 QRCodeMultiReader，而不是普通 QRCodeReader。
// 普通 QRCodeReader 更适合一张图只有一个二维码的场景。
// QRCodeMultiReader 会在图中搜索多个 finder pattern 组合，从而尽可能返回多个二维码结果。
func decodeMultipleWithGozxing(img image.Image) ([]*gozxing.Result, error) {
	// LuminanceSource 是 gozxing 的亮度源抽象。
	// 二维码识别本质上需要判断每个像素是偏黑还是偏白，所以第一步是把彩色图片转成亮度数据。
	source := gozxing.NewLuminanceSourceFromImage(img)

	// HybridBinarizer 会把亮度图转换成黑白二值图。
	// 它比简单全局阈值更适合拍照场景，因为照片里常见屏幕亮度不均、阴影、轻微反光等问题。
	bitmap, err := gozxing.NewBinaryBitmap(gozxing.NewHybridBinarizer(source))
	if err != nil {
		return nil, err
	}

	// QRCodeMultiReader 是专门用于“同一张图片里存在多个二维码”的读取器。
	// 这正好匹配你的生产样例图：一张屏幕照片中有 3 行 3 列共 9 个二维码。
	reader := gozxingmultiqr.NewQRCodeMultiReader()

	// hints 用来告诉解码器可以更努力一点，以及只关心二维码格式。
	// TRY_HARDER 会让识别器多做一些搜索，速度可能慢一点，但对拍照倾斜、模糊、位置分散的图片更友好。
	// CHARACTER_SET 指定 UTF-8，避免中文内容被按其他字符集解释。
	// POSSIBLE_FORMATS 限定为 QR_CODE，减少对其他条码格式的无效尝试。
	hints := map[gozxing.DecodeHintType]interface{}{
		gozxing.DecodeHintType_TRY_HARDER:    true,
		gozxing.DecodeHintType_CHARACTER_SET: "UTF-8",
		gozxing.DecodeHintType_POSSIBLE_FORMATS: []gozxing.BarcodeFormat{
			gozxing.BarcodeFormat_QR_CODE,
		},
	}

	// DecodeMultiple 返回所有识别到的二维码。
	// 如果没有找到足够的二维码定位点，通常会返回 NotFoundException。
	// 当前示例不吞掉这个错误，让调用方能清楚看到失败原因。
	return reader.DecodeMultiple(bitmap, hints)
}

// formatGozxingResultPoints 把 gozxing 返回的定位点格式化成人眼好读的字符串。
// 注意：这些点不是业务内容的一部分，只是识别器认为有助于定位二维码的关键点。
// 在二维码识别里，常见返回值是三个定位图案的中心点坐标，而不是完整二维码外框的四个角。
func formatGozxingResultPoints(points []gozxing.ResultPoint) string {
	// 预先用 len(points) 作为容量，避免 append 过程中产生不必要的扩容。
	parts := make([]string, 0, len(points))
	for _, point := range points {
		// 坐标保留一位小数，便于看出位置，又不会让日志太长。
		// 输出里使用中文逗号，和终端里的中文内容风格保持一致。
		parts = append(parts, fmt.Sprintf("(%.1f，%.1f)", point.GetX(), point.GetY()))
	}
	return fmt.Sprintf("[%s]", joinStrings(parts, "，"))
}

// joinStrings 是一个极小的字符串拼接工具。
// 标准库 strings.Join 也可以完成同样的事情。
// 这里单独写出来，是为了让这个示例不再额外引入 strings 包，并且便于看到最终输出格式的构造过程。
func joinStrings(values []string, separator string) string {
	// 空切片直接返回空字符串，避免访问 values[0] 时越界。
	if len(values) == 0 {
		return ""
	}

	// 从第一个元素开始累加，后面的每个元素前面都补上分隔符。
	// 这样可以避免输出结果开头或结尾多一个分隔符。
	result := values[0]
	for _, value := range values[1:] {
		result += separator + value
	}

	return result
}
