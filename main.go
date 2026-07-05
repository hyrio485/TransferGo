package main

import (
	"os"

	"hyrio.xyz/transfergo/core"
)

func main() {
	// 把面向操作系统的行为留在边界：core 包返回错误，main 决定如何打印错误以及使用哪种进程状态。
	if err := core.Run(os.Args[1:]); err != nil {
		core.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
