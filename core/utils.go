package core

import (
	"fmt"
	"io"
	"strconv"
)

// Fprint 写入面向 CLI 的文本，并有意忽略 writer 结果。命令流水线通过返回错误报告真实失败，而状态输出是尽力而为，不应让 GoLand 标记每个调用点。
func Fprint(w io.Writer, a ...any) {
	_, _ = fmt.Fprint(w, a...)
}

// Fprintf 写入面向 CLI 的格式化文本，并出于和 Fprint 相同的原因有意忽略 writer 结果。
func Fprintf(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, format, a...)
}

// Fprintln 写入一行面向 CLI 的文本，并出于和 Fprint 相同的原因有意忽略 writer 结果。
func Fprintln(w io.Writer, a ...any) {
	_, _ = fmt.Fprintln(w, a...)
}

// formatFPS 避免 ffmpeg 参数中出现尾随零，同时保留 0.5 或 29.97 这样的精确小数值。
func formatFPS(fps float64) string {
	return strconv.FormatFloat(fps, 'f', -1, 64)
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
