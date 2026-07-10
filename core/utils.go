package core

import (
	"fmt"
	"io"
	"os"
	"strconv"
)

// region 通用输出

func Fprintf(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, format, a...)
}

func Fprintln(w io.Writer, a ...any) {
	_, _ = fmt.Fprintln(w, a...)
}

func Fprint(w io.Writer, a ...any) {
	_, _ = fmt.Fprint(w, a...)
}

// endregion

// region 日志输出

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
)

func log(color string, format string, a ...any) {
	Fprintf(os.Stdout, color+format+colorReset, a...)
}

func LogE(format string, a ...any) {
	log(colorRed, format, a...)
}

func LogI(format string, a ...any) {
	log(colorGreen, format, a...)
}

func LogW(format string, a ...any) {
	log(colorYellow, format, a...)
}

// endregion

// region 其他工具方法

func CloseFile(file *os.File) {
	_ = file.Close()
}

func E(proc string, err error) error {
	return fmt.Errorf("%s: %w", proc, err)
}

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

func formatFPS(fps float64) string {
	return strconv.FormatFloat(fps, 'f', -1, 64)
}

// endregion
