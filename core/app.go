package core

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// Run is the top-level CLI dispatcher. It accepts argv without the program
// name, which keeps it easy to test and leaves process concerns in main.
func Run(args []string) error {
	if len(args) == 0 {
		printUsage(os.Stderr)
		return errors.New("missing command")
	}

	switch args[0] {
	case "encode":
		return runEncode(args[1:])
	case "decode":
		return runDecode(args[1:])
	case "help", "-h", "--help":
		printUsage(os.Stdout)
		return nil
	default:
		printUsage(os.Stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// printUsage intentionally stays short: detailed command flags live on the
// command-specific FlagSet help, while this text helps users pick a subcommand.
func printUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage:
  transfergo encode -i <file> -o <video.mp4> [-p <password>]
  transfergo decode -i <video.mp4> [-o <file>] [-p <password>]

commands:
  encode  split a file into QR frames and render them into a video with ffmpeg
  decode  extract QR frames from a recorded video and rebuild the original file
`)
}
