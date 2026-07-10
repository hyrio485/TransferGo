# TransferGo

TransferGo 是一个通过二维码视频传输文件的命令行工具。它把文件拆分为带序号的协议帧，将多个二维码排列到 PNG 图片中，再通过 ffmpeg 编码为 H.264 视频；解码时执行相反流程，并使用文件长度和 SHA-256 摘要校验还原结果。

这个项目适合无法直接建立网络连接，但可以播放、录制或转交视频的场景。它更适合几十 MB 以内的小文件，不适合作为高吞吐量或强抗丢包的通用传输协议。

## 目录

- [主要特性](#主要特性)
- [工作原理](#工作原理)
- [环境要求](#环境要求)
- [安装与构建](#安装与构建)
- [快速开始](#快速开始)
- [命令参数](#命令参数)
- [帧目录](#帧目录)
- [加密与安全](#加密与安全)
- [可靠性与限制](#可靠性与限制)
- [常见问题](#常见问题)
- [测试](#测试)
- [项目结构](#项目结构)

## 主要特性

- 使用单个可执行文件，通过 `encode` 和 `decode` 子命令完成编码与解码。
- 支持任意二进制文件，不依赖文件类型或文本编码。
- 默认在一张 `800×800` 图片中排列 `3×3` 个二维码。
- 支持自定义视频帧率、二维码尺寸、网格行列数、数据块大小和 H.264 CRF。
- 支持可选的 PBKDF2-SHA256、AES-256-GCM 密码加密。
- 每个协议帧包含版本、类型、序号、总帧数和帧体长度。
- 自动忽略内容完全相同的重复帧，并拒绝内容冲突的同序号帧。
- 还原完成后校验文件长度和 SHA-256 摘要。
- 默认拒绝覆盖已有视频或文件，必须显式传入 `-replace`。
- 自动帧目录使用随机名称，任务完成后默认清理。

## 工作原理

### 编码流程

```text
输入文件
  ↓
文件清单＋数据分块
  ↓
协议帧编码，可选 AES-GCM 加密
  ↓
多个二维码组成一张 PNG 图片
  ↓
ffmpeg 编码 H.264、yuv420p 视频
```

编码过程会执行以下步骤：

1. 把输入文件完整读入内存。
2. 计算文件长度和 SHA-256 摘要，并生成序号为零的清单帧。
3. 按 `-chunk-size` 把文件拆成若干数据帧。
4. 如果设置了密码，为清单帧和每个数据帧分别执行 AES-GCM 加密。
5. 把协议帧编码为二维码，并按 `-rows × -cols` 分组生成 PNG 图片。
6. 调用 ffmpeg 把连续编号的 PNG 图片编码为视频。

### 解码流程

```text
输入视频
  ↓
ffmpeg 按采样帧率抽取 PNG
  ↓
识别每张图片中的多个二维码
  ↓
解析、去重和检查协议帧
  ↓
可选 AES-GCM 解密
  ↓
按序拼接并校验文件
  ↓
安全写入输出文件
```

视频抽帧通常会产生重复二维码。TransferGo 会按协议帧序号去重，只有帧内容完全一致时才会忽略重复项。缺帧、冲突帧、密码错误、长度不一致或摘要不一致都会导致解码失败。

## 环境要求

- Go `1.26.2` 或兼容的 Go `1.26` 工具链。
- ffmpeg，并且包含 `libx264` 编码器。

确认环境：

```bash
go version
ffmpeg -version
```

ffmpeg 的查找顺序如下：

1. 命令行参数 `-ffmpeg`。
2. 环境变量 `FFMPEG_PATH`。
3. `PATH` 中的 `ffmpeg`。

ffmpeg 是运行时外部依赖，不会被编译进 TransferGo 可执行文件。

## 安装与构建

### 从源码构建

```bash
git clone https://github.com/hyrio485/TransferGo.git
cd TransferGo
go mod download
go build -trimpath -o transfergo .
```

构建完成后查看帮助：

```bash
./transfergo help
```

Windows 可以把输出文件名改为 `transfergo.exe`：

```powershell
go build -trimpath -o transfergo.exe .
```

### 安装到 Go 工具目录

在仓库根目录执行：

```bash
go install .
```

生成的程序会安装到 `GOBIN`，或者默认的 `GOPATH/bin`。

### 交叉编译

TransferGo 本身是纯 Go 程序，可以使用 Go 的交叉编译能力。例如生成 Linux AMD64 可执行文件：

```bash
GOOS=linux GOARCH=amd64 go build -trimpath -o transfergo-linux-amd64 .
```

目标系统仍然需要单独安装可用的 ffmpeg。

## 快速开始

下面的示例使用已经被 `.gitignore` 忽略的 `files` 目录：

```bash
mkdir -p files
```

### 编码文件

```bash
./transfergo encode \
  -i files/input.bin \
  -o files/output.mp4
```

如果输出视频已经存在，默认会拒绝覆盖。确认需要替换时传入 `-replace`：

```bash
./transfergo encode \
  -i files/input.bin \
  -o files/output.mp4 \
  -replace
```

### 使用密码加密

```bash
./transfergo encode \
  -i files/input.bin \
  -o files/encrypted.mp4 \
  -p "your-password"
```

### 解码文件

```bash
./transfergo decode \
  -i files/output.mp4 \
  -o files/restored.bin
```

解码加密视频时必须提供相同密码：

```bash
./transfergo decode \
  -i files/encrypted.mp4 \
  -o files/restored.bin \
  -p "your-password"
```

如果不传 `-o`，程序会使用清单中的原始文件名。来自清单的默认文件名必须是安全的纯文件名，不能包含绝对路径、上级目录或路径分隔符。

覆盖已有输出文件：

```bash
./transfergo decode \
  -i files/output.mp4 \
  -o files/restored.bin \
  -replace
```

### 验证还原结果

macOS 或 Linux：

```bash
cmp files/input.bin files/restored.bin
```

macOS 可以比较 SHA-256：

```bash
shasum -a 256 files/input.bin files/restored.bin
```

Linux 通常使用：

```bash
sha256sum files/input.bin files/restored.bin
```

### 指定 ffmpeg

通过命令行参数指定：

```bash
./transfergo encode \
  -i files/input.bin \
  -o files/output.mp4 \
  -ffmpeg /path/to/ffmpeg
```

或者设置环境变量：

```bash
FFMPEG_PATH=/path/to/ffmpeg ./transfergo encode \
  -i files/input.bin \
  -o files/output.mp4
```

## 命令参数

查看程序内置的完整帮助：

```bash
./transfergo help
```

命令不接受额外的位置参数。

### encode

```bash
./transfergo encode -i <文件> -o <视频> [参数]
```

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `-i`、`-in` | 无 | 输入文件，必填。 |
| `-o`、`-out` | 无 | 输出视频路径，必填。 |
| `-p`、`-password` | 空 | AES-GCM 加密密码；非空时启用加密。 |
| `-ffmpeg` | 自动查找 | ffmpeg 可执行文件路径。 |
| `-frames-dir` | 随机临时目录 | 生成的 PNG 帧目录。 |
| `-fps` | `3` | 输出视频帧率，必须是大于零的有限数值。 |
| `-qr-size` | `240` | 单个二维码的宽高像素数，必须大于零。 |
| `-width` | `800` | 输出视频宽度，必须是正偶数。 |
| `-height` | `800` | 输出视频高度，必须是正偶数。 |
| `-rows` | `3` | 每张图片中的二维码行数，必须大于零。 |
| `-cols` | `3` | 每张图片中的二维码列数，必须大于零。 |
| `-chunk-size` | `240` | 每个数据二维码承载的明文字节数，必须大于零。 |
| `-crf` | `24` | x264 CRF，取值范围为 `0` 至 `51`；数值越小，画质越高、文件越大。 |
| `-replace` | `false` | 允许替换已有输出视频。 |
| `-keep-frames` | `false` | 保留自动创建的 PNG 帧目录。 |

网格必须能够放入输出图片，即 `rows × qr-size ≤ height`，并且 `cols × qr-size ≤ width`。二维码内容超过容量或 `qr-size` 小于实际二维码矩阵时，编码会失败，不会静默裁剪二维码。

### decode

```bash
./transfergo decode -i <视频> [参数]
```

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `-i`、`-in` | 无 | 输入视频路径，必填。 |
| `-o`、`-out` | 清单中的原文件名 | 输出文件路径。 |
| `-p`、`-password` | 空 | 加密视频的解码密码。 |
| `-ffmpeg` | 自动查找 | ffmpeg 可执行文件路径。 |
| `-frames-dir` | 随机临时目录 | 从视频中抽取的 PNG 帧目录。 |
| `-sample-fps` | `9` | 视频抽帧率，必须是大于零的有限数值。 |
| `-max-frame-size` | `2048` | 二维码识别前图片最长边的像素上限，范围为 1 至 16384。 |
| `-parallel` | `true` | 是否使用多个工作协程并行解码 PNG 图片。 |
| `-replace` | `false` | 允许替换已有输出文件。 |
| `-keep-frames` | `false` | 保留自动创建的 PNG 帧目录。 |

通常建议让 `-sample-fps` 不低于编码时的 `-fps`。默认编码帧率为 `3`，默认解码采样帧率为 `9`，允许同一个协议帧被多次采样并在协议层去重。

`-max-frame-size` 会在保持宽高比的前提下限制解码图片尺寸。实拍视频包含透视、摩尔纹或较小二维码时，可以适当提高该值以保留细节；降低该值则可以减少解码时间和内存占用。

默认使用多个工作协程并行解码 PNG，并按照原始图片顺序汇总识别结果。需要降低瞬时 CPU 和内存占用或排查单帧问题时，可以传入 `-parallel=false` 切换为串行解码。

解码日志会输出运行配置、抽取图片数、工作协程数、包含二维码的图片数、空结果图片数、不可读图片数、二维码总数、唯一二维码数、重复二维码数，以及恢复文件的大小和 SHA-256 摘要。即使二维码收集失败，已完成阶段的汇总统计仍会保留在日志中，便于定位画面缺失或识别率问题。

## 帧目录

未指定 `-frames-dir` 时，程序会在当前工作目录创建带随机后缀的目录：

```text
transfergo-encode-xxxxxxxx
transfergo-decode-xxxxxxxx
```

- 程序会在日志中打印实际使用的帧目录，包括自动目录的完整路径。
- 默认情况下，自动目录会在命令结束时删除，并打印开始删除、删除完成或删除失败日志。
- 使用 `-keep-frames` 时，自动目录会保留，并在日志中输出路径。
- 显式指定的目录归调用方所有，无论是否传入 `-keep-frames` 都不会自动删除。
- 显式目录可以由程序创建，但不能已经包含 `frame_*.png` 文件，以免新旧帧混合。

## 加密与安全

设置 `-p` 后，TransferGo 使用以下参数：

- PBKDF2-SHA256，迭代次数为 `200000`。
- 随机盐长度为 `16` 字节。
- AES 密钥长度为 `256` 位。
- AES-GCM Nonce 长度为 `12` 字节。
- 每个协议帧使用独立随机 Nonce。
- 协议版本、加密标志、帧类型、帧序号、总帧数和文件盐会绑定到 AES-GCM 认证附加数据。

加密清单中包含文件名、文件长度和 SHA-256 摘要。密码错误或加密内容被篡改时，解码会在认证阶段失败。

安全注意事项：

- 密码不会以明文写入视频，但通过 `-p` 传递的密码可能出现在 Shell 历史记录或进程列表中。
- 密码安全性取决于密码强度。视频包含执行离线密码猜测所需的盐和密文，因此应使用足够长且不可预测的密码。
- 未设置密码时，协议只通过最终长度和 SHA-256 检测意外损坏，不提供机密性或抗恶意篡改认证。
- 显式传入 `-o` 时，程序认为输出路径由调用方负责；只有从外部视频清单读取的默认文件名会执行路径安全限制。
- `-replace` 会允许替换目标文件，请在自动化脚本中谨慎使用。

## 可靠性与限制

### 丢帧策略

TransferGo 当前不包含 FEC、纠删码或自动重传：

- 完整且一致的重复帧会被忽略。
- 同一序号出现不同内容时会拒绝还原。
- 缺少任意协议帧时，整个解码过程失败。
- 所有数据帧完成拼接后，还会校验文件长度和 SHA-256。

如果视频会经过平台转码、裁剪、缩放或强压缩，建议降低 `-chunk-size`、保持较低 `-fps`，并提高录制清晰度。

### 资源限制

- 编码会把整个输入文件和协议载荷保存在内存中，不适合超大文件。
- 解码会收集识别到的协议载荷，并在内存中拼接完整输出文件。
- 单张输入图片的宽高不能超过 `16384` 像素。
- 单张输入图片的像素总数不能超过 `67108864`。
- 图片进入二维码识别前，最长边会按 `-max-frame-size` 缩小，默认不超过 `2048` 像素。
- 当前实现推荐用于几十 MB 以内的文件；实际可用大小取决于内存、二维码参数、视频质量和录制环境。

### 视频与二维码限制

- 视频编码固定使用 `libx264` 和 `yuv420p`，因此宽高必须是偶数。
- 二维码纠错等级固定为 L，以提高有效容量，但抗遮挡能力相对较弱。
- 二进制载荷通过 ISO-8859-1 一一映射到二维码文本接口。
- 当前协议版本为 `1`，其他版本会被拒绝。
- 视频播放或录制时应避免播放器控件、黑边裁剪、自动旋转和明显的透视变形。

## 常见问题

### 找不到 ffmpeg

错误信息通常包含：

```text
ffmpeg not found
```

确认 ffmpeg 已安装并位于 `PATH`，或者通过 `-ffmpeg`、`FFMPEG_PATH` 指定完整路径。

### ffmpeg 提示没有 libx264

TransferGo 固定使用 `libx264`。请更换包含该编码器的 ffmpeg 构建版本。

### 输出文件已经存在

默认策略是拒绝覆盖。确认目标文件可以被替换后，重新执行命令并加入 `-replace`。

### 没有识别到 TransferGo 二维码

错误信息通常包含：

```text
no TransferGo QR payloads decoded
```

可以依次检查：

1. 输入视频是否确实由 TransferGo 生成或录制。
2. 二维码是否完整显示，且没有被播放器控件遮挡。
3. 视频是否被过度压缩、裁剪或缩放。
4. `-sample-fps` 是否过低。
5. `-max-frame-size` 是否过低，导致实拍二维码缩小后丢失细节。
6. 是否需要使用 `-keep-frames` 检查抽取出的 PNG 图片。

### 提示缺少协议帧

错误信息通常包含：

```text
one or more transfer frames are missing
```

也可能在收到的载荷数量明显少于帧总数时显示：

```text
frame total 10 exceeds available payload count 9
```

提高 `-sample-fps` 可能帮助捕获持续时间较短的视频帧；实拍二维码尺寸较小时，也可以适当提高 `-max-frame-size`。如果原视频本身已经缺失或二维码不可识别，则需要重新生成或重新录制视频。

### 密码校验失败

错误信息通常包含：

```text
password check failed
```

确认解码密码与编码密码完全一致。该错误也可能表示加密清单已损坏。

### 二维码尺寸过小

如果 `-qr-size` 小于协议载荷实际需要的二维码矩阵，编码会直接失败。请增大 `-qr-size`，或者减小 `-chunk-size`。

## 测试

运行全部测试：

```bash
go test ./...
```

单元测试不会调用真实 ffmpeg，也不会访问网络。所有测试图片、输出文件和帧目录都使用 Go 测试框架提供的 `t.TempDir`，测试完成后会自动清理，不会在仓库目录留下明显文件。

运行竞态检测和静态检查：

```bash
go test -race ./...
go vet ./...
```

执行完整视频链路测试：

```bash
./transfergo encode \
  -i files/input.bin \
  -o files/smoke.mp4 \
  -p "smoke-password" \
  -ffmpeg /path/to/ffmpeg \
  -replace

./transfergo decode \
  -i files/smoke.mp4 \
  -o files/restored.bin \
  -p "smoke-password" \
  -ffmpeg /path/to/ffmpeg \
  -replace

cmp files/input.bin files/restored.bin
```

## 项目结构

```text
.
├── main.go                 # 命令分发、编码与解码主流程
├── main_test.go            # 输出路径和文件替换策略测试
├── core/
│   ├── commands.go         # 命令参数、帮助和进度日志
│   ├── commands_test.go    # 参数边界和帮助完整性测试
│   ├── protocol.go         # 协议帧、清单、加密和文件还原
│   ├── protocol_test.go    # 协议往返和异常帧测试
│   ├── qr.go               # 二维码图片编码与解码
│   ├── qr_test.go          # 二维码尺寸保护测试
│   ├── utils.go            # 日志、错误和字节工具
│   ├── utils_test.go       # 错误包装、字节拼接和帧率格式测试
│   ├── video.go            # ffmpeg 调用和帧目录管理
│   └── video_test.go       # 临时帧目录测试
├── go.mod
├── go.sum
└── README.md
```
