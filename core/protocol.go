package core

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	// protocolVersion 让未来的读取器能在尝试解析二进制帧其余部分前，拒绝不兼容的二维码载荷。
	protocolVersion = byte(1)

	// 序列化传输帧布局，单位为字节：
	//   0..3   magic "TGQR"
	//   4      协议版本
	//   5      标志位
	//   6      帧类型
	//   7..10  序列号，big-endian
	//   11..14 总帧数，big-endian
	//   15..16 body 长度，big-endian
	//   17..   body 字节
	//
	// 固定帧头让每个二维码都能自描述，因此 decode 可以从无序图片中收集帧，并仍能检测缺失帧或外来帧。
	frameHeaderLen = 17
	frameMagic     = "TGQR"

	// frameFlagEncrypted 表示帧 body 受 AES-GCM 保护。对 Manifest 帧，body 是 salt || nonce || ciphertext || tag；对数据帧，body 是 nonce || ciphertext || tag。
	frameFlagEncrypted = byte(1 << 0)

	// 帧 0 始终是 Manifest。数据帧从序列 1 开始，让还原路径能在接受文件字节前校验元数据。
	frameKindManifest = byte(0)
	frameKindData     = byte(1)

	// 加密参数由协议固定。12 字节 nonce 是 GCM 标准大小，32 字节密钥表示使用 AES-256。
	saltSize         = 16
	nonceSize        = 12
	aesKeySize       = 32
	pbkdf2Iterations = 200_000

	// maxFrameBodyLen 对应帧头中的两字节 body 长度字段，因此序列化能在产生无效字节前快速失败。
	maxFrameBodyLen = 1<<16 - 1

	// Manifest 布局：
	//   magic "TGM1"
	//   密码检查标记
	//   原始文件大小
	//   明文分块大小
	//   明文分块数量
	//   原始文件的 SHA-256
	//   文件名长度
	//   文件名字节
	manifestMagic = "TGM1"
)

// manifestPasswordCheck 被有意包含在 Manifest 明文中。Manifest 加密时，错误密码会在任何数据帧被处理前失败；未加密时，这个标记也能拒绝碰巧带有 Manifest magic 的随机二维码数据。
var manifestPasswordCheck = []byte("TG-PASS-OK-v1\x00\x00\x00")

// ProtocolContext 持有协议依赖。随机字节可注入，因此加密测试可以保持确定性，而不触碰二维码或 app 代码。
type ProtocolContext struct {
	randomBytes func(int) ([]byte, error)
}

// TransferFrame 是存储在一个二维码中的协议单元。启用加密时，帧头字段也会被认证，防止有效的加密 body 被移动到另一个序列号或总帧数下。
type TransferFrame struct {
	Flags byte
	Kind  byte
	Seq   uint32
	Total uint32
	Body  []byte
}

// Manifest 描述原始文件和预期的数据流形状。decode 只有在帧收集、密码检查和最终 SHA-256 校验成功后才信任它。
type Manifest struct {
	FileName   string
	FileSize   uint64
	ChunkSize  uint32
	ChunkCount uint32
	SHA256     [sha256.Size]byte
}

// NewProtocolContext 连接生产环境的加密随机源。
func NewProtocolContext() ProtocolContext {
	return ProtocolContext{randomBytes: cryptoRandomBytes}
}

// BuildTransferFrames 创建一个 Manifest 帧和每个分块对应的一个数据帧。如果 password 非空，Manifest 和所有数据分块都会用同一个密钥加密，该密钥由密码和每个视频随机生成的 salt 派生。
func (ctx ProtocolContext) BuildTransferFrames(input []byte, fileName string, password string, chunkSize int) ([]TransferFrame, Manifest, error) {
	if chunkSize <= 0 {
		return nil, Manifest{}, errors.New("chunk size must be greater than 0")
	}

	// 帧序列 0 预留给 Manifest，因此最大数据分块数量比 uint32 序列空间少 1。
	chunkCount := uint64(0)
	if len(input) > 0 {
		chunkCount = uint64((len(input) + chunkSize - 1) / chunkSize)
	}
	if chunkCount > uint64(^uint32(0)-1) {
		return nil, Manifest{}, errors.New("too many chunks for protocol")
	}

	total := uint32(chunkCount + 1)
	encrypted := password != ""
	flags := byte(0)
	if encrypted {
		flags |= frameFlagEncrypted
	}

	meta := Manifest{
		FileName:   fileName,
		FileSize:   uint64(len(input)),
		ChunkSize:  uint32(chunkSize),
		ChunkCount: uint32(chunkCount),
		SHA256:     sha256.Sum256(input),
	}

	var salt []byte
	var gcm cipher.AEAD
	if encrypted {
		// salt 只存储一次，位于 Manifest 帧中。它也会被纳入每帧的 AES-GCM AAD，从而把所有帧绑定到同一个视频。
		var err error
		salt, err = ctx.randomBytes(saltSize)
		if err != nil {
			return nil, Manifest{}, fmt.Errorf("generate encryption salt: %w", err)
		}
		gcm, err = makeGCM(password, salt)
		if err != nil {
			return nil, Manifest{}, fmt.Errorf("create encryption cipher: %w", err)
		}
	}

	manifestPlain, err := marshalManifest(meta)
	if err != nil {
		return nil, Manifest{}, fmt.Errorf("marshal manifest: %w", err)
	}

	frames := make([]TransferFrame, 0, total)
	// 序列 0 携带校验每个数据帧和最终文件内容所需的元数据。
	manifestFrame := TransferFrame{
		Flags: flags,
		Kind:  frameKindManifest,
		Seq:   0,
		Total: total,
	}
	if encrypted {
		// 把 salt 存在加密后的 Manifest 前面，让解码器能在调用 AES-GCM Open 前派生密钥。
		body, err := ctx.encryptFrameBody(gcm, manifestFrame, manifestPlain, salt)
		if err != nil {
			return nil, Manifest{}, fmt.Errorf("encrypt manifest frame: %w", err)
		}
		manifestFrame.Body = append(append([]byte{}, salt...), body...)
	} else {
		manifestFrame.Body = manifestPlain
	}
	frames = append(frames, manifestFrame)

	// 数据帧在 Manifest 后连续编号。还原路径要求每个序列号都存在，因此会明确报告缺失帧。
	for seq, offset := uint32(1), 0; offset < len(input); seq++ {
		end := offset + chunkSize
		if end > len(input) {
			end = len(input)
		}
		chunk := input[offset:end]
		frame := TransferFrame{
			Flags: flags,
			Kind:  frameKindData,
			Seq:   seq,
			Total: total,
		}
		if encrypted {
			body, err := ctx.encryptFrameBody(gcm, frame, chunk, salt)
			if err != nil {
				return nil, Manifest{}, fmt.Errorf("encrypt data frame %d: %w", seq, err)
			}
			frame.Body = body
		} else {
			// 复制明文分块，避免调用方在函数返回后通过修改原始输入切片来改变帧。
			frame.Body = append([]byte{}, chunk...)
		}
		frames = append(frames, frame)
		offset = end
	}

	return frames, meta, nil
}

// RestoreFromFrames 校验已收集的帧集合并返回原始字节。这个函数有意保持严格：会拒绝缺失帧、冲突元数据、错误密码、错误哈希和意外帧类型。
func (ctx ProtocolContext) RestoreFromFrames(frames map[uint32]TransferFrame, total uint32, password string) (Manifest, []byte, error) {
	if total == 0 {
		return Manifest{}, nil, errors.New("no transfer frames found")
	}

	// Manifest 是数据流形状的信任根。没有它，就无法安全得知原始大小、分块数量或文件哈希。
	manifestFrame, ok := frames[0]
	if !ok {
		return Manifest{}, nil, fmt.Errorf("missing frame(s): %s", formatMissingFrames(frames, total))
	}
	if manifestFrame.Kind != frameKindManifest {
		return Manifest{}, nil, errors.New("frame 0 is not a manifest")
	}
	encrypted := manifestFrame.Flags&frameFlagEncrypted != 0

	var salt []byte
	var gcm cipher.AEAD
	var manifestPlain []byte
	if encrypted {
		// Manifest salt 是派生 AES-GCM 密钥所必需的。成功解密也会认证其中的密码检查标记。
		if password == "" {
			return Manifest{}, nil, errors.New("video is encrypted; provide -p")
		}
		if len(manifestFrame.Body) < saltSize+nonceSize {
			return Manifest{}, nil, errors.New("encrypted manifest is too short")
		}
		salt = append([]byte{}, manifestFrame.Body[:saltSize]...)
		var err error
		gcm, err = makeGCM(password, salt)
		if err != nil {
			return Manifest{}, nil, fmt.Errorf("create decryption cipher: %w", err)
		}
		manifestPlain, err = decryptFrameBody(gcm, manifestFrame, manifestFrame.Body[saltSize:], salt)
		if err != nil {
			return Manifest{}, nil, errors.New("password check failed")
		}
	} else {
		manifestPlain = append([]byte{}, manifestFrame.Body...)
	}

	meta, err := parseManifest(manifestPlain)
	if err != nil {
		if encrypted {
			return Manifest{}, nil, errors.New("password check failed")
		}
		return Manifest{}, nil, fmt.Errorf("parse manifest: %w", err)
	}
	if meta.ChunkCount != total-1 {
		return Manifest{}, nil, fmt.Errorf("manifest chunk count %d does not match frame total %d", meta.ChunkCount, total)
	}
	// 在组装输出前检查完整性，让失败指向缺失的序列号，而不是后续的哈希不匹配。
	for seq := uint32(0); seq < total; seq++ {
		if _, ok := frames[seq]; !ok {
			return Manifest{}, nil, fmt.Errorf("missing frame(s): %s", formatMissingFrames(frames, total))
		}
	}

	var output bytes.Buffer
	for seq := uint32(1); seq < total; seq++ {
		frame := frames[seq]
		if frame.Kind != frameKindData {
			return Manifest{}, nil, fmt.Errorf("frame %d is not a data frame", seq)
		}
		if frame.Flags != manifestFrame.Flags {
			return Manifest{}, nil, fmt.Errorf("frame %d encryption flags do not match manifest", seq)
		}
		var chunk []byte
		if encrypted {
			// AAD 包含类型、序列、总数和 salt，因此帧字节无法被重放到另一个位置而不触发认证失败。
			chunk, err = decryptFrameBody(gcm, frame, frame.Body, salt)
			if err != nil {
				return Manifest{}, nil, fmt.Errorf("decrypt frame %d: %w", seq, err)
			}
		} else {
			chunk = frame.Body
		}
		if _, err := output.Write(chunk); err != nil {
			return Manifest{}, nil, fmt.Errorf("assemble output bytes: %w", err)
		}
	}

	result := output.Bytes()
	// 大小检查能捕获截断或额外数据；即使字节数恰好匹配，SHA-256 也能捕获损坏。
	if uint64(len(result)) != meta.FileSize {
		return Manifest{}, nil, fmt.Errorf("restored file size %d does not match manifest size %d", len(result), meta.FileSize)
	}
	sum := sha256.Sum256(result)
	if sum != meta.SHA256 {
		return Manifest{}, nil, errors.New("restored file hash does not match manifest")
	}

	return meta, result, nil
}

// MarshalFrame 把一个传输帧序列化为存入二维码载荷的精确字节布局。过大的 body 会触发 panic，因为调用方会在渲染前校验容量。
func (ctx ProtocolContext) MarshalFrame(frame TransferFrame) []byte {
	if len(frame.Body) > maxFrameBodyLen {
		panic("frame body too large")
	}
	// Big-endian 字段让字节布局跨平台稳定，并且便于用常见二进制工具检查。
	out := make([]byte, frameHeaderLen+len(frame.Body))
	copy(out[0:4], frameMagic)
	out[4] = protocolVersion
	out[5] = frame.Flags
	out[6] = frame.Kind
	binary.BigEndian.PutUint32(out[7:11], frame.Seq)
	binary.BigEndian.PutUint32(out[11:15], frame.Total)
	binary.BigEndian.PutUint16(out[15:17], uint16(len(frame.Body)))
	copy(out[frameHeaderLen:], frame.Body)
	return out
}

// ParseFrame 反向执行 MarshalFrame，并校验足够的结构，以便在无关二维码影响帧收集前拒绝它们。
func (ctx ProtocolContext) ParseFrame(payload []byte) (TransferFrame, error) {
	if len(payload) < frameHeaderLen {
		return TransferFrame{}, errors.New("payload too short")
	}
	if string(payload[0:4]) != frameMagic {
		return TransferFrame{}, errors.New("not a TransferGo QR frame")
	}
	if payload[4] != protocolVersion {
		return TransferFrame{}, fmt.Errorf("unsupported protocol version %d", payload[4])
	}
	bodyLen := int(binary.BigEndian.Uint16(payload[15:17]))
	if len(payload) != frameHeaderLen+bodyLen {
		return TransferFrame{}, fmt.Errorf("frame length mismatch: header says %d body bytes, got %d", bodyLen, len(payload)-frameHeaderLen)
	}
	// 复制 body，让返回的帧独立持有自己的字节，不依赖解码器缓冲区。
	frame := TransferFrame{
		Flags: payload[5],
		Kind:  payload[6],
		Seq:   binary.BigEndian.Uint32(payload[7:11]),
		Total: binary.BigEndian.Uint32(payload[11:15]),
		Body:  append([]byte{}, payload[frameHeaderLen:]...),
	}
	if frame.Flags&^frameFlagEncrypted != 0 {
		return TransferFrame{}, fmt.Errorf("unsupported frame flags 0x%02x", frame.Flags)
	}
	if frame.Kind != frameKindManifest && frame.Kind != frameKindData {
		return TransferFrame{}, fmt.Errorf("unsupported frame kind %d", frame.Kind)
	}
	if frame.Total == 0 {
		return TransferFrame{}, errors.New("frame total cannot be 0")
	}
	if frame.Seq >= frame.Total {
		return TransferFrame{}, fmt.Errorf("frame sequence %d is outside total %d", frame.Seq, frame.Total)
	}
	return frame, nil
}

// marshalManifest 把元数据编码成紧凑的二进制格式，使其能走和数据帧相同的二维码载荷路径。
func marshalManifest(meta Manifest) ([]byte, error) {
	name := []byte(meta.FileName)
	if len(name) > maxFrameBodyLen {
		return nil, errors.New("file name is too long for manifest")
	}

	// 容量提示避免固定字段加文件名产生重复分配。它不是协议的一部分；下面的字段顺序才是。
	out := make([]byte, 0, 70+len(name))
	out = append(out, []byte(manifestMagic)...)
	out = append(out, manifestPasswordCheck...)
	out = binary.BigEndian.AppendUint64(out, meta.FileSize)
	out = binary.BigEndian.AppendUint32(out, meta.ChunkSize)
	out = binary.BigEndian.AppendUint32(out, meta.ChunkCount)
	out = append(out, meta.SHA256[:]...)
	out = binary.BigEndian.AppendUint16(out, uint16(len(name)))
	out = append(out, name...)
	return out, nil
}

// parseManifest 校验 Manifest 标记，然后按 marshalManifest 写入时的相同顺序读取每个固定宽度字段。
func parseManifest(payload []byte) (Manifest, error) {
	minLen := 4 + len(manifestPasswordCheck) + 8 + 4 + 4 + sha256.Size + 2
	if len(payload) < minLen {
		return Manifest{}, errors.New("manifest is too short")
	}
	if string(payload[:4]) != manifestMagic {
		return Manifest{}, errors.New("manifest magic mismatch")
	}
	offset := 4
	if !bytes.Equal(payload[offset:offset+len(manifestPasswordCheck)], manifestPasswordCheck) {
		return Manifest{}, errors.New("manifest password check mismatch")
	}
	offset += len(manifestPasswordCheck)

	meta := Manifest{}
	meta.FileSize = binary.BigEndian.Uint64(payload[offset : offset+8])
	offset += 8
	meta.ChunkSize = binary.BigEndian.Uint32(payload[offset : offset+4])
	offset += 4
	meta.ChunkCount = binary.BigEndian.Uint32(payload[offset : offset+4])
	offset += 4
	copy(meta.SHA256[:], payload[offset:offset+sha256.Size])
	offset += sha256.Size
	nameLen := int(binary.BigEndian.Uint16(payload[offset : offset+2]))
	offset += 2
	if len(payload) != offset+nameLen {
		return Manifest{}, errors.New("manifest file name length mismatch")
	}
	meta.FileName = string(payload[offset:])
	return meta, nil
}

// encryptFrameBody 返回 nonce || ciphertext || tag。nonce 按帧生成，并存储在密文旁边，因为 GCM 解密需要相同 nonce，但不要求它保密。
func (ctx ProtocolContext) encryptFrameBody(gcm cipher.AEAD, frame TransferFrame, plaintext []byte, salt []byte) ([]byte, error) {
	nonce, err := ctx.randomBytes(nonceSize)
	if err != nil {
		return nil, fmt.Errorf("generate frame nonce: %w", err)
	}
	aad := frameAAD(frame, salt)
	sealed := gcm.Seal(nil, nonce, plaintext, aad)
	out := make([]byte, 0, len(nonce)+len(sealed))
	out = append(out, nonce...)
	out = append(out, sealed...)
	return out, nil
}

// decryptFrameBody 在返回明文前校验 GCM tag。body 或已认证帧元数据的任何变化都会被报告为错误。
func decryptFrameBody(gcm cipher.AEAD, frame TransferFrame, body []byte, salt []byte) ([]byte, error) {
	if len(body) < nonceSize+gcm.Overhead() {
		return nil, errors.New("encrypted frame body is too short")
	}
	nonce := body[:nonceSize]
	ciphertext := body[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, frameAAD(frame, salt))
}

// frameAAD 会被认证但不会被加密。它把密文绑定到协议身份、帧类型、序列号、总数和每个视频的 salt。
func frameAAD(frame TransferFrame, salt []byte) []byte {
	out := make([]byte, 0, 4+1+1+1+4+4+len(salt))
	out = append(out, []byte(frameMagic)...)
	out = append(out, protocolVersion, frame.Flags, frame.Kind)
	out = binary.BigEndian.AppendUint32(out, frame.Seq)
	out = binary.BigEndian.AppendUint32(out, frame.Total)
	out = append(out, salt...)
	return out
}

// makeGCM 从密码和 salt 派生 AES-256 密钥，然后包装为 GCM，让加密和认证一起发生。
func makeGCM(password string, salt []byte) (cipher.AEAD, error) {
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, aesKeySize)
	if err != nil {
		return nil, fmt.Errorf("derive encryption key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	return cipher.NewGCM(block)
}

// cryptoRandomBytes 从 crypto/rand 读取数据，让 salt 和 nonce 不可预测。
func cryptoRandomBytes(n int) ([]byte, error) {
	out := make([]byte, n)
	if _, err := rand.Read(out); err != nil {
		return nil, fmt.Errorf("read random bytes: %w", err)
	}
	return out, nil
}

// SameFrame 把完全相同的重复二维码捕获视为无害，同时仍允许 collectFramesFromImages 拒绝冲突的重复帧。
func SameFrame(a, b TransferFrame) bool {
	return a.Flags == b.Flags &&
		a.Kind == b.Kind &&
		a.Seq == b.Seq &&
		a.Total == b.Total &&
		bytes.Equal(a.Body, b.Body)
}

// formatMissingFrames 通过列出前几个缺失序列号并省略其余部分，让错误消息保持可读。
func formatMissingFrames(frames map[uint32]TransferFrame, total uint32) string {
	missing := make([]string, 0)
	for seq := uint32(0); seq < total; seq++ {
		if _, ok := frames[seq]; !ok {
			missing = append(missing, strconv.FormatUint(uint64(seq), 10))
			if len(missing) == 20 {
				break
			}
		}
	}
	if len(missing) == 0 {
		return "none"
	}
	if uint32(len(missing)) < total-uint32(len(frames)) {
		missing = append(missing, "...")
	}
	return strings.Join(missing, ", ")
}
