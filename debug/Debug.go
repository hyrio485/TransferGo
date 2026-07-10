package main

import (
	"fmt"
	"os"

	"github.com/makiuchi-d/gozxing"
	gozxingqr "github.com/makiuchi-d/gozxing/qrcode"
	qrdecoder "github.com/makiuchi-d/gozxing/qrcode/decoder"
	"hyrio.xyz/transfergo/core"
	"hyrio.xyz/transfergo/hyrio"
)

func main1() {
	payload := []byte("abcdefg")
	img, err := hyrio.EncodeQRGray(payload, 27, 0)
	if err != nil {
		core.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	err = hyrio.SaveGrayAsPng(img, "abc.png")
	if err != nil {
		core.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func main2() {
	strings := []string{
		"新程步远方自履花开满径心向远方自在高歌不",
		"山河万里云舒月朗人间烟火温柔相伴长久安然",
		"春水映天竹影摇窗书声入梦岁岁平安清欢常在",
		"星灯照路心怀热望日日新程步履从容风光渐明",
		"竹润青摇窗露禾远山含黛归鸟穿云入画春光好",
		"清溪绕村柳色新茶香满院笑语随风入梦悠然热",
		"长街灯暖人潮渐散月照归途心事慢慢开花潮渐",
		"竹门半掩书卷生香水生香少年执笔写尽晴川秋",
		"晚霞铺海喜热香少渔歌渐远一舟明月载满人间",
	}
	for i, s := range strings {
		hints := map[gozxing.EncodeHintType]interface{}{
			gozxing.EncodeHintType_CHARACTER_SET:    "UTF-8",
			gozxing.EncodeHintType_ERROR_CORRECTION: qrdecoder.ErrorCorrectionLevel_L,
			gozxing.EncodeHintType_MARGIN:           2,
		}

		matrix, err := gozxingqr.NewQRCodeWriter().Encode(s, gozxing.BarcodeFormat_QR_CODE, 128, 128, hints)
		if err != nil {
			core.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
			return
		}

		img := hyrio.BitMatrixToGray(matrix)
		err = hyrio.SaveGrayAsPng(img, fmt.Sprintf("%d.png", i))
		if err != nil {
			core.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	}
}
