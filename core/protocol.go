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
)

const (
	// 协议版本号
	protocolVersion = byte(1)

	// 帧信息相关
	frameMagic    = "TGQR"
	manifestMagic = "TGMF"

	// 帧标志位
	flagEncrypted = byte(1 << 0)

	// 帧类型
	frameKindManifest = byte(0)
	frameKindData     = byte(1)

	// 加密参数
	saltSize         = 16
	nonceSize        = 12
	aesKeySize       = 32
	pbkdf2Iterations = 200_000

	// 限制信息
	maxFileNameLength = 255
	maxFrameBodySize  = 1<<16 - 1
)

var frameHeaderLen = len(frameMagic) + 1 + 1 + 1 + 4 + 4 + 2
var manifestPasswordCheck = []byte("TG-PASS-OK-v1\x00\x00\x00")

// ProtocolContext 保存协议编码时使用的随机字节生成器。
type ProtocolContext struct {
	randomBytes func(int) ([]byte, error)
}

// NewProtocolContext 创建使用密码学安全随机源的协议上下文。
func NewProtocolContext() ProtocolContext {
	return ProtocolContext{
		randomBytes: cryptoRandomBytes,
	}
}

// Manifest 保存还原文件所需的名称、长度和 SHA-256 摘要。
type Manifest struct {
	fileName   string
	fileSize   uint64
	fileSHA256 [sha256.Size]byte
}

// FileName 返回清单中记录的原始文件名。
func (ctx Manifest) FileName() string {
	return ctx.fileName
}

// NewManifest 根据文件元数据创建清单。
func NewManifest(fileName string, fileSize uint64, fileSHA256 [sha256.Size]byte) Manifest {
	return Manifest{
		fileName:   fileName,
		fileSize:   fileSize,
		fileSHA256: fileSHA256,
	}
}

// transferFrame 表示一个尚未序列化或已经解析的协议帧。
type transferFrame struct {
	flags byte
	kind  byte
	index uint32
	total uint32
	// 加密清单帧：Salt（16）＋Nonce（12）＋Ciphertext。
	// 加密数据帧：Nonce（12）＋Ciphertext。
	// 未加密清单帧：Manifest。
	// 未加密数据帧：Data。
	body []byte
}

// region Encode

// EncodeFile 把文件内容编码为一个清单帧和若干按序编号的数据帧。
// password 非空时，每个帧体都会使用同一派生密钥和独立随机 Nonce 进行 AES-GCM 加密。
func (ctx ProtocolContext) EncodeFile(input []byte, fileName string, password string, chunkSize int) ([][]byte, error) {
	if chunkSize <= 0 {
		return nil, errors.New("chunk size must be greater than 0")
	}

	chunkCount := uint64(0)
	if len(input) > 0 {
		// 先减一再相除，避免 len(input) 与 chunkSize 相加时发生整数溢出。
		chunkCount = uint64(1 + (len(input)-1)/chunkSize)
	}
	if chunkCount > uint64(^uint32(0)-1) {
		return nil, errors.New("too many chunks for protocol")
	}

	frameCount := uint32(chunkCount + 1)
	encrypted := password != ""

	flags := byte(0)
	if encrypted {
		flags |= flagEncrypted
	}

	manifest := Manifest{
		fileName:   fileName,
		fileSize:   uint64(len(input)),
		fileSHA256: sha256.Sum256(input),
	}

	marshalledManifest, err := marshalManifest(manifest)
	if err != nil {
		return nil, E("marshal manifest", err)
	}

	result := make([][]byte, 0, frameCount)
	manifestFrame := transferFrame{
		flags: flags,
		kind:  frameKindManifest,
		index: 0,
		total: frameCount,
	}

	var salt []byte
	var gcm cipher.AEAD
	if encrypted {
		// 一个文件只生成一份盐和派生密钥，但每个帧仍会生成独立 Nonce。
		salt, err = ctx.randomBytes(saltSize)
		if err != nil {
			return nil, E("generate encryption salt", err)
		}
		gcm, err = makeGCM(password, salt)
		if err != nil {
			return nil, E("create encryption cipher", err)
		}
		var body []byte
		body, err = ctx.encryptFrameBody(gcm, manifestFrame, marshalledManifest, salt)
		if err != nil {
			return nil, E("encrypt manifest frame", err)
		}
		manifestFrame.body = ConcatByteArrays(salt, body)
	} else {
		manifestFrame.body = marshalledManifest
	}
	payload, err := marshalFrame(manifestFrame)
	if err != nil {
		return nil, E("marshal manifest frame", err)
	}
	result = append(result, payload)

	for index, offset := uint32(1), 0; offset < len(input); index++ {
		// 使用剩余长度判断末块，避免 offset 与 chunkSize 相加时发生整数溢出。
		end := len(input)
		if chunkSize < len(input)-offset {
			end = offset + chunkSize
		}
		chunk := input[offset:end]
		dataFrame := transferFrame{
			flags: flags,
			kind:  frameKindData,
			index: index,
			total: frameCount,
		}
		if encrypted {
			body, err := ctx.encryptFrameBody(gcm, dataFrame, chunk, salt)
			if err != nil {
				return nil, fmt.Errorf("encrypt data frame %d: %w", index, err)
			}
			dataFrame.body = body
		} else {
			dataFrame.body = append([]byte{}, chunk...)
		}
		payload, err := marshalFrame(dataFrame)
		if err != nil {
			return nil, fmt.Errorf("marshal data frame %d: %w", index, err)
		}
		result = append(result, payload)
		offset = end
	}

	return result, nil
}

// marshalManifest 按固定字段顺序把文件清单编码为大端字节序。
func marshalManifest(manifest Manifest) ([]byte, error) {
	name := []byte(manifest.fileName)
	if len(name) == 0 {
		return nil, errors.New("manifest file name is empty")
	}
	if len(name) > maxFileNameLength {
		return nil, errors.New("manifest file name is too long")
	}

	out := make([]byte, 0, len(manifestMagic)+len(manifestPasswordCheck)+2+len(name)+8+sha256.Size)
	out = append(out, []byte(manifestMagic)...)
	out = append(out, manifestPasswordCheck...)
	out = binary.BigEndian.AppendUint16(out, uint16(len(name)))
	out = append(out, name...)
	out = binary.BigEndian.AppendUint64(out, manifest.fileSize)
	out = append(out, manifest.fileSHA256[:]...)
	return out, nil
}

// encryptFrameBody 为单个帧生成随机 Nonce，并使用帧元数据作为认证附加数据。
func (ctx ProtocolContext) encryptFrameBody(gcm cipher.AEAD, frame transferFrame, plaintext []byte, salt []byte) ([]byte, error) {
	nonce, err := ctx.randomBytes(nonceSize)
	if err != nil {
		return nil, E("generate frame nonce", err)
	}
	sealed := gcm.Seal(nil, nonce, plaintext, frameAAD(frame, salt))
	return ConcatByteArrays(nonce, sealed), nil
}

// marshalFrame 把协议帧头和帧体编码为二维码承载的完整字节数组。
func marshalFrame(frame transferFrame) ([]byte, error) {
	if len(frame.body) > maxFrameBodySize {
		return nil, errors.New("frame body is too large")
	}

	out := make([]byte, 0, frameHeaderLen+len(frame.body))
	out = append(out, []byte(frameMagic)...)
	out = append(out, protocolVersion)
	out = append(out, frame.flags)
	out = append(out, frame.kind)
	out = binary.BigEndian.AppendUint32(out, frame.index)
	out = binary.BigEndian.AppendUint32(out, frame.total)
	out = binary.BigEndian.AppendUint16(out, uint16(len(frame.body)))
	out = append(out, frame.body...)
	return out, nil
}

// endregion

// region Decode

// RestoreFile 解析、去重并校验协议载荷，最后按帧序号还原原始文件。
func RestoreFile(payloads [][]byte, password string) (Manifest, []byte, error) {
	if len(payloads) == 0 {
		return Manifest{}, nil, errors.New("no transfer frames found")
	}

	frames := make(map[uint32]transferFrame)
	total := uint32(0)
	for _, payload := range payloads {
		frame, err := parseFrame(payload)
		if err != nil {
			return Manifest{}, nil, E("parse frame", err)
		}
		if uint64(frame.total) > uint64(len(payloads)) {
			return Manifest{}, nil, fmt.Errorf("frame total %d exceeds available payload count %d", frame.total, len(payloads))
		}
		if total == 0 {
			total = frame.total
		} else if frame.total != total {
			return Manifest{}, nil, fmt.Errorf("frame %d reports total %d, expected %d", frame.index, frame.total, total)
		} else if frame.index >= total {
			return Manifest{}, nil, fmt.Errorf("frame %d is outside total %d", frame.index, total)
		}
		// 视频抽帧通常会产生重复二维码；内容一致的重复帧可以安全忽略。
		if existing, ok := frames[frame.index]; ok {
			if !sameFrame(existing, frame) {
				return Manifest{}, nil, fmt.Errorf("conflicting duplicate frame %d", frame.index)
			}
			continue
		}
		frames[frame.index] = frame
	}

	return restoreFromFrames(frames, total, password)
}

// parseFrame 校验协议头、版本、标志位和长度，并把载荷解析为帧结构。
func parseFrame(payload []byte) (transferFrame, error) {
	if len(payload) < frameHeaderLen {
		return transferFrame{}, errors.New("payload too short")
	}
	if string(payload[0:4]) != frameMagic {
		return transferFrame{}, errors.New("not a TransferGo QR frame")
	}
	if payload[4] != protocolVersion {
		return transferFrame{}, fmt.Errorf("unsupported protocol version %d", payload[4])
	}
	bodyLen := int(binary.BigEndian.Uint16(payload[15:17]))
	if len(payload) != frameHeaderLen+bodyLen {
		return transferFrame{}, fmt.Errorf("frame length mismatch: header says %d body bytes, got %d", bodyLen, len(payload)-frameHeaderLen)
	}
	frame := transferFrame{
		flags: payload[5],
		kind:  payload[6],
		index: binary.BigEndian.Uint32(payload[7:11]),
		total: binary.BigEndian.Uint32(payload[11:15]),
		body:  append([]byte{}, payload[frameHeaderLen:]...),
	}
	if frame.flags&^flagEncrypted != 0 {
		return transferFrame{}, fmt.Errorf("unsupported frame flags 0x%02x", frame.flags)
	}
	if frame.kind != frameKindManifest && frame.kind != frameKindData {
		return transferFrame{}, fmt.Errorf("unsupported frame kind %d", frame.kind)
	}
	if frame.total == 0 {
		return transferFrame{}, errors.New("frame total cannot be 0")
	}
	if frame.index >= frame.total {
		return transferFrame{}, fmt.Errorf("frame index %d is outside total %d", frame.index, frame.total)
	}
	return frame, nil
}

// unmarshalManifest 校验清单结构并按大端字节序恢复文件元数据。
func unmarshalManifest(data []byte) (Manifest, error) {
	minLen := len(manifestMagic) + len(manifestPasswordCheck) + 2 + 8 + sha256.Size
	if len(data) < minLen {
		return Manifest{}, errors.New("manifest is too short")
	}

	offset := 0
	if string(data[offset:offset+len(manifestMagic)]) != manifestMagic {
		return Manifest{}, errors.New("manifest magic mismatch")
	}
	offset += len(manifestMagic)

	if !bytes.Equal(data[offset:offset+len(manifestPasswordCheck)], manifestPasswordCheck) {
		return Manifest{}, errors.New("manifest password check mismatch")
	}
	offset += len(manifestPasswordCheck)

	nameLen := int(binary.BigEndian.Uint16(data[offset : offset+2]))
	offset += 2
	if nameLen == 0 {
		return Manifest{}, errors.New("manifest file name is empty")
	}
	if nameLen > maxFileNameLength {
		return Manifest{}, errors.New("manifest file name is too long")
	}
	if len(data) != offset+nameLen+8+sha256.Size {
		return Manifest{}, errors.New("manifest file name length mismatch")
	}

	manifest := Manifest{}
	manifest.fileName = string(data[offset : offset+nameLen])
	offset += nameLen
	manifest.fileSize = binary.BigEndian.Uint64(data[offset : offset+8])
	offset += 8
	copy(manifest.fileSHA256[:], data[offset:offset+sha256.Size])
	return manifest, nil
}

// restoreFromFrames 校验完整帧集合，处理可选解密，并拼接、校验最终文件。
func restoreFromFrames(frames map[uint32]transferFrame, total uint32, password string) (Manifest, []byte, error) {
	if total == 0 {
		return Manifest{}, nil, errors.New("no transfer frames found")
	}
	if uint64(total) > uint64(len(frames)) {
		return Manifest{}, nil, errors.New("one or more transfer frames are missing")
	}

	manifestFrame, ok := frames[0]
	if !ok {
		return Manifest{}, nil, errors.New("missing manifest frame")
	}
	if manifestFrame.kind != frameKindManifest {
		return Manifest{}, nil, errors.New("frame 0 is not a manifest")
	}
	encrypted := manifestFrame.flags&flagEncrypted != 0

	var salt []byte
	var gcm cipher.AEAD
	var manifestPlain []byte
	if encrypted {
		// 清单帧携带派生密钥所需的盐；密码错误会在清单认证阶段立即失败。
		if password == "" {
			return Manifest{}, nil, errors.New("video is encrypted; provide password")
		}
		if len(manifestFrame.body) < saltSize+nonceSize {
			return Manifest{}, nil, errors.New("encrypted manifest is too short")
		}
		salt = append([]byte{}, manifestFrame.body[:saltSize]...)
		var err error
		gcm, err = makeGCM(password, salt)
		if err != nil {
			return Manifest{}, nil, E("create decryption cipher", err)
		}
		manifestPlain, err = decryptFrameBody(gcm, manifestFrame, manifestFrame.body[saltSize:], salt)
		if err != nil {
			return Manifest{}, nil, errors.New("password check failed")
		}
	} else {
		manifestPlain = append([]byte{}, manifestFrame.body...)
	}

	manifest, err := unmarshalManifest(manifestPlain)
	if err != nil {
		if encrypted {
			return Manifest{}, nil, errors.New("password check failed")
		}
		return Manifest{}, nil, E("unmarshal manifest", err)
	}
	// 先确认所有编号都存在，再开始拼接，避免返回部分文件内容。
	for index := uint32(0); index < total; index++ {
		if _, ok := frames[index]; !ok {
			return Manifest{}, nil, fmt.Errorf("missing frame %d", index)
		}
	}

	// 数据帧从序号一开始，序号零固定保留给清单帧。
	var output bytes.Buffer
	for index := uint32(1); index < total; index++ {
		frame := frames[index]
		if frame.kind != frameKindData {
			return Manifest{}, nil, fmt.Errorf("frame %d is not a data frame", index)
		}
		if frame.flags != manifestFrame.flags {
			return Manifest{}, nil, fmt.Errorf("frame %d encryption flags do not match manifest", index)
		}
		var chunk []byte
		if encrypted {
			chunk, err = decryptFrameBody(gcm, frame, frame.body, salt)
			if err != nil {
				return Manifest{}, nil, fmt.Errorf("decrypt frame %d: %w", index, err)
			}
		} else {
			chunk = frame.body
		}
		if _, err := output.Write(chunk); err != nil {
			return Manifest{}, nil, E("assemble output bytes", err)
		}
	}

	// 长度和摘要同时匹配，才能确认还原结果没有缺失、乱序或被篡改。
	result := output.Bytes()
	if uint64(len(result)) != manifest.fileSize {
		return Manifest{}, nil, fmt.Errorf("restored file size %d does not match manifest size %d", len(result), manifest.fileSize)
	}
	sum := sha256.Sum256(result)
	if sum != manifest.fileSHA256 {
		return Manifest{}, nil, errors.New("restored file hash does not match manifest")
	}

	return manifest, result, nil
}

// decryptFrameBody 拆分 Nonce 和密文，并使用与编码端相同的认证附加数据解密。
func decryptFrameBody(gcm cipher.AEAD, frame transferFrame, body []byte, salt []byte) ([]byte, error) {
	if len(body) < nonceSize+gcm.Overhead() {
		return nil, errors.New("encrypted frame body is too short")
	}
	nonce := body[:nonceSize]
	ciphertext := body[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, frameAAD(frame, salt))
}

// sameFrame 判断两个相同序号的帧是否完全一致。
func sameFrame(a, b transferFrame) bool {
	return a.flags == b.flags &&
		a.kind == b.kind &&
		a.index == b.index &&
		a.total == b.total &&
		bytes.Equal(a.body, b.body)
}

// endregion

// region tools

// frameAAD 构造 AES-GCM 认证附加数据，把协议版本、帧元数据和文件盐绑定到密文。
func frameAAD(frame transferFrame, salt []byte) []byte {
	out := make([]byte, 0, len(frameMagic)+1+1+1+4+4+len(salt))
	out = append(out, []byte(frameMagic)...)
	out = append(out, protocolVersion, frame.flags, frame.kind)
	out = binary.BigEndian.AppendUint32(out, frame.index)
	out = binary.BigEndian.AppendUint32(out, frame.total)
	out = append(out, salt...)
	return out
}

// makeGCM 使用 PBKDF2-SHA256 派生 AES-256 密钥，并创建 GCM 实例。
func makeGCM(password string, salt []byte) (cipher.AEAD, error) {
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, aesKeySize)
	if err != nil {
		return nil, E("derive encryption key", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, E("create AES cipher", err)
	}
	return cipher.NewGCM(block)
}

// cryptoRandomBytes 从密码学安全随机源读取指定长度的字节。
func cryptoRandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, E("generate random bytes", err)
	}
	return b, nil
}

// endregion
