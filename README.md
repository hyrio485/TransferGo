# TransferGo

`TransferGo` 是一个通过二维码视频传输文件的命令行工具。编码端把文件拆成多个字节块，每个块编码成二维码帧，再用 ffmpeg 合成为视频；解码端从录制回来的视频中抽帧、识别二维码、校验序号并还原文件。

它适合传输几十 MB 以内的小文件。更大的文件理论上可行，但视频时长会变长，拍摄和解码成本也会明显增加。

## 功能

- 单个可执行文件，使用 `encode` / `decode` 子命令区分功能。
- 支持把任意文件编码成 MP4 视频。
- 支持从视频文件中解码并还原原文件。
- 每个二维码帧包含序号和总帧数，缺帧会直接失败并提示缺失序号。
- 默认输出 `800x800` 视频，每帧放置 `3x3` 个二维码格子，并在每个格子中重叠红、绿、蓝三个通道。
- 支持可选密码加密。
- 加密模式使用 AES-GCM，并在 manifest 帧中做密码认证；密码错误会直接拒绝，不会等到文件损坏后才发现。
- 二维码纠错等级使用较低的 L 级，以优先提升容量。

## 依赖

- Go
- ffmpeg

ffmpeg 查找顺序为：`-ffmpeg` 参数、`FFMPEG_PATH` 环境变量、`PATH` 中的 `ffmpeg` 命令。

## 构建

```bash
go build -o transfergo .
```

构建后的可执行文件是：

```bash
./transfergo
```

## 快速开始

建议把测试数据放在 `files/` 目录中。这个目录已经被 `.gitignore` 忽略。

### 编码文件为视频

```bash
./transfergo encode \
  -i files/input.bin \
  -o files/output.mp4
```

如果 ffmpeg 不在 `PATH` 中：

```bash
./transfergo encode \
  -i files/input.bin \
  -o files/output.mp4 \
  -ffmpeg /Users/hyrio/Workspace/Environments/ffmpeg/ffmpeg
```

也可以通过环境变量指定：

```bash
FFMPEG_PATH=/Users/hyrio/Workspace/Environments/ffmpeg/ffmpeg ./transfergo encode \
  -i files/input.bin \
  -o files/output.mp4
```

### 使用密码加密

```bash
./transfergo encode \
  -i files/input.bin \
  -o files/output.mp4 \
  -p "your-password"
```

### 从视频解码还原文件

```bash
./transfergo decode \
  -i files/output.mp4 \
  -o files/restored.bin
```

如果视频加密过，解码时必须传入相同密码：

```bash
./transfergo decode \
  -i files/output.mp4 \
  -o files/restored.bin \
  -p "your-password"
```

覆盖已有输出文件：

```bash
./transfergo decode \
  -i files/output.mp4 \
  -o files/restored.bin \
  -p "your-password" \
  -force
```

验证还原结果：

```bash
cmp files/input.bin files/restored.bin
```

## 参数

### encode

```bash
./transfergo encode -i <file> -o <video.mp4> [options]
```

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `-i` | 无 | 输入文件，必填 |
| `-o` | 无 | 输出视频路径，必填 |
| `-p` | 空 | 可选密码；设置后会启用 AES-GCM 加密 |
| `-ffmpeg` | `FFMPEG_PATH` 或 `ffmpeg` | ffmpeg 可执行文件路径 |
| `-fps` | `3` | 输出视频帧率 |
| `-qr-size` | `240` | 每个格子内二维码的像素尺寸 |
| `-qr-version` | `8` | 二维码版本，1 到 40；较低版本更容易拍摄识别 |
| `-width` / `-video-width` | `800` | 输出视频宽度 |
| `-height` / `-video-height` | `800` | 输出视频高度 |
| `-grid-size` | `3` | 每帧二维码网格行列数，默认 `3x3` |
| `-chunk-size` | `0` | 每个数据二维码承载的明文字节数；0 表示使用更适合手机录像的默认值 |
| `-crf` | `0` | x264 CRF；0 表示无损 |
| `-frames-dir` | 当前目录下的时间戳目录 | 指定二维码帧输出目录 |
| `-keep-frames` | `false` | 保留生成的二维码 PNG 帧 |

### decode

```bash
./transfergo decode -i <video.mp4> [options]
```

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `-i` | 无 | 输入视频路径，必填 |
| `-o` | manifest 中的原文件名 | 输出文件路径 |
| `-p` | 空 | 加密视频的解码密码 |
| `-ffmpeg` | `FFMPEG_PATH` 或 `ffmpeg` | ffmpeg 可执行文件路径 |
| `-sample-fps` | `9` | 解码时从视频抽帧的采样帧率 |
| `-grid-size` | `3` | 解码时尝试的二维码网格行列数 |
| `-frames-dir` | 当前目录下的时间戳目录 | 指定抽帧输出目录 |
| `-keep-frames` | `false` | 保留抽取出的 PNG 帧 |
| `-force` | `false` | 允许覆盖已有输出文件 |

## 传输流程

编码时：

1. 读取输入文件。
2. 按 chunk 拆分为多个字节块。
3. 生成 manifest 帧，保存文件名、文件大小、chunk 数量和 SHA-256。
4. 可选地用密码派生 AES-GCM 密钥。
5. 将 manifest 和每个数据块编码成协议二维码。
6. 按 `3x3` 网格把二维码打包到固定尺寸 PNG 中，并把同一格子的三个二维码分别写入红、绿、蓝通道。
7. 调用 ffmpeg 将 PNG 帧合成为 MP4。

解码时：

1. 调用 ffmpeg 从视频中抽取 PNG 帧。
2. 跳过无二维码的噪声图片，并对整图、中心区域、彩色内容区域、九宫格小块和 RGB 通道分别尝试识别。
3. 从每张图片中收集多个二维码载荷。
4. 按序号去重并收集帧。
5. 读取 manifest。
6. 如果视频已加密，先用 manifest 中的认证块校验密码。
7. 检查是否缺帧。
8. 解密数据帧并按序拼接。
9. 校验文件大小和 SHA-256。
10. 写出还原文件。

## 加密说明

启用 `-p` 后：

- 使用 PBKDF2-SHA256 从密码派生 256-bit AES 密钥。
- 使用 AES-GCM 做认证加密。
- manifest 帧包含一个加密的密码校验内容。
- 每个数据帧单独加密，并使用独立 nonce。
- 密码错误时，解码会在 manifest 阶段失败，错误为 `password check failed`。

注意：密码不会明文写入视频。

## 丢帧和失败策略

每个二维码帧都包含：

- 协议版本
- 帧类型
- 当前序号
- 总帧数
- 数据长度

如果某一帧没有被识别到，解码会失败并提示类似：

```text
missing frame(s): 3, 4
```

当前实现不做 FEC 或自动修复。录制质量足够好时，这种策略更简单，也更容易知道失败原因。

## 建议使用方式

- 播放视频时尽量全屏。
- 避免播放器控件遮挡二维码。
- 手机拍摄时保持画面稳定、对焦清晰。
- 默认 `3 fps` 会让每帧保持足够长的显示时间；如果调高 `-fps`，录制端也需要稳定捕获每一帧。
- 如果视频经过二次压缩或平台转码，建议保持较低的 `-qr-version`，必要时继续降低 `-chunk-size`。
- 如果只是在本机生成并直接解码视频，可以手动提高 `-chunk-size`；如果要手机拍屏录像，建议先使用默认值。

## 项目结构

```text
.
├── main.go              # 最外层入口
└── core/
    ├── app.go           # 子命令分发
    ├── commands.go      # encode/decode CLI 参数和流程
    ├── protocol.go      # 帧协议、manifest、AES-GCM 加密和还原
    ├── qr.go            # 二维码 PNG 编码和解码
    ├── video.go         # ffmpeg 调用、抽帧和帧目录处理
    └── app_test.go      # 协议、二维码、加密和缺帧测试
```

## 测试

```bash
go test ./...
```

完整视频链路可以这样测试：

```bash
./transfergo encode \
  -i files/smoke-input.bin \
  -o files/smoke-output.mp4 \
  -p smoke-pass \
  -ffmpeg /path/to/ffmpeg

./transfergo decode \
  -i files/smoke-output.mp4 \
  -o files/smoke-restored.bin \
  -p smoke-pass \
  -ffmpeg /path/to/ffmpeg \
  -force

cmp files/smoke-input.bin files/smoke-restored.bin
```
