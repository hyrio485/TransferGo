package main

import (
	"fmt"
	"os"

	"hyrio.xyz/transfergo/core"
)

func main() {
	// Keep OS-facing behavior at the boundary: the core package returns errors,
	// while main decides how they are printed and which process status to use.
	if err := core.Run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
