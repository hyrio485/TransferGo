package hyrio

import (
	"io"
	"time"
)

type CommandContext struct {
	stdout io.Writer
	stderr io.Writer
	now    func() time.Time
}

type EncodeOptions struct {
	input     string  // 输入文件
	output    string  // 输出文件
	password  string  // 加密密码
	ffmpeg    string  // FFmpeg 文件路径
	framesDir string  // 帧文件临时保存路径
	fps       float64 // 视频帧率
}
