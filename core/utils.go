package core

import (
	"fmt"
	"io"
	"os"
	"strconv"
)

// region 日志输出

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	prefixError = "[ERROR] "
	prefixInfo  = " [INFO] "
	prefixWarn  = " [WARN] "
)

// log 为格式化内容附加级别前缀和终端颜色，并忽略日志输出失败。
func log(w io.Writer, color string, prefix string, format string, a ...any) {
	_, _ = fmt.Fprintf(w, color+prefix+format+colorReset, a...)
}

// LogE 向标准错误输出红色错误日志。
func LogE(format string, a ...any) {
	log(os.Stderr, colorRed, prefixError, format, a...)
}

// LogI 向标准输出输出绿色信息日志。
func LogI(format string, a ...any) {
	log(os.Stdout, colorGreen, prefixInfo, format, a...)
}

// LogW 向标准错误输出黄色警告日志。
func LogW(format string, a ...any) {
	log(os.Stderr, colorYellow, prefixWarn, format, a...)
}

// endregion

// region 其他工具方法

// CloseFile 尽力关闭文件，适用于调用方已经无法处理关闭错误的清理路径。
func CloseFile(file *os.File) {
	_ = file.Close()
}

// E 为底层错误补充当前处理步骤，同时保留错误链。
func E(proc string, err error) error {
	return fmt.Errorf("%s: %w", proc, err)
}

// ConcatByteArrays 预分配所需容量，并按参数顺序拼接多个字节切片。
func ConcatByteArrays(arrays ...[]byte) []byte {
	total := 0
	for _, array := range arrays {
		total += len(array)
	}

	out := make([]byte, 0, total)
	for _, array := range arrays {
		out = append(out, array...)
	}
	return out
}

// formatFPS 使用不丢失精度且不带多余零的十进制格式输出帧率。
func formatFPS(fps float64) string {
	return strconv.FormatFloat(fps, 'f', -1, 64)
}

// endregion
