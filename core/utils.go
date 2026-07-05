package core

import (
	"fmt"
	"io"
	"strconv"
)

// Fprint writes CLI-facing text and deliberately ignores the writer result.
// The command pipeline reports real failures through returned errors, while
// status output is best-effort and should not make GoLand flag every call site.
func Fprint(w io.Writer, a ...any) {
	_, _ = fmt.Fprint(w, a...)
}

// Fprintf writes formatted CLI-facing text and deliberately ignores the writer
// result for the same reason as Fprint.
func Fprintf(w io.Writer, format string, a ...any) {
	_, _ = fmt.Fprintf(w, format, a...)
}

// Fprintln writes one CLI-facing line and deliberately ignores the writer
// result for the same reason as Fprint.
func Fprintln(w io.Writer, a ...any) {
	_, _ = fmt.Fprintln(w, a...)
}

// formatFPS avoids trailing zeros in ffmpeg arguments while preserving precise
// decimal values such as 0.5 or 29.97.
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
