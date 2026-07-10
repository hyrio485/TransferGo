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
		return nil, errors.New("单块数据大小必须大于 0")
	}

	chunkCount := uint64(0)
	if len(input) > 0 {
		// 先减一再相除，避免 len(input) 与 chunkSize 相加时发生整数溢出。
		chunkCount = uint64(1 + (len(input)-1)/chunkSize)
	}
	if chunkCount > uint64(^uint32(0)-1) {
		return nil, errors.New("文件拆分后的数据帧数量超过协议上限")
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
		return nil, E("序列化文件清单失败", err)
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
			return nil, E("生成加密盐值失败", err)
		}
		gcm, err = makeGCM(password, salt)
		if err != nil {
			return nil, E("创建加密器失败", err)
		}
		var body []byte
		body, err = ctx.encryptFrameBody(gcm, manifestFrame, marshalledManifest, salt)
		if err != nil {
			return nil, E("加密文件清单帧失败", err)
		}
		manifestFrame.body = ConcatByteArrays(salt, body)
	} else {
		manifestFrame.body = marshalledManifest
	}
	payload, err := marshalFrame(manifestFrame)
	if err != nil {
		return nil, E("序列化文件清单帧失败", err)
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
				return nil, fmt.Errorf("加密第 %d 个数据帧失败：%w", index, err)
			}
			dataFrame.body = body
		} else {
			dataFrame.body = append([]byte{}, chunk...)
		}
		payload, err := marshalFrame(dataFrame)
		if err != nil {
			return nil, fmt.Errorf("序列化第 %d 个数据帧失败：%w", index, err)
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
		return nil, errors.New("文件清单中的原文件名为空")
	}
	if len(name) > maxFileNameLength {
		return nil, errors.New("文件清单中的原文件名过长")
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
		return nil, E("生成数据帧随机数失败", err)
	}
	sealed := gcm.Seal(nil, nonce, plaintext, frameAAD(frame, salt))
	return ConcatByteArrays(nonce, sealed), nil
}

// marshalFrame 把协议帧头和帧体编码为二维码承载的完整字节数组。
func marshalFrame(frame transferFrame) ([]byte, error) {
	if len(frame.body) > maxFrameBodySize {
		return nil, errors.New("数据帧内容超过协议允许的最大长度")
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
		return Manifest{}, nil, errors.New("没有找到 TransferGo 传输数据帧")
	}

	frames := make(map[uint32]transferFrame)
	total := uint32(0)
	for _, payload := range payloads {
		frame, err := parseFrame(payload)
		if err != nil {
			return Manifest{}, nil, E("解析传输数据帧失败", err)
		}
		// 清单存在时可以立即拒绝不可能完整的载荷集合；清单缺失时继续收集，
		// 让 restoreFromFrames 返回更明确的 missing manifest frame。
		if frame.index == 0 && uint64(frame.total) > uint64(len(payloads)) {
			return Manifest{}, nil, fmt.Errorf("协议声明共有 %d 个传输数据帧，但当前仅识别到 %d 个二维码载荷", frame.total, len(payloads))
		}
		if total == 0 {
			total = frame.total
		} else if frame.total != total {
			return Manifest{}, nil, fmt.Errorf("第 %d 个传输数据帧声明总数为 %d，与其他帧声明的总数 %d 不一致", frame.index, frame.total, total)
		} else if frame.index >= total {
			return Manifest{}, nil, fmt.Errorf("传输数据帧序号 %d 超出总帧数 %d 的范围", frame.index, total)
		}
		// 视频抽帧通常会产生重复二维码；内容一致的重复帧可以安全忽略。
		if existing, ok := frames[frame.index]; ok {
			if !sameFrame(existing, frame) {
				return Manifest{}, nil, fmt.Errorf("发现内容冲突的重复传输数据帧，帧序号为 %d", frame.index)
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
		return transferFrame{}, errors.New("二维码载荷过短，无法包含完整的传输数据帧头")
	}
	if string(payload[0:4]) != frameMagic {
		return transferFrame{}, errors.New("二维码载荷不是 TransferGo 传输数据帧")
	}
	if payload[4] != protocolVersion {
		return transferFrame{}, fmt.Errorf("不支持 TransferGo 协议版本 %d", payload[4])
	}
	bodyLen := int(binary.BigEndian.Uint16(payload[15:17]))
	if len(payload) != frameHeaderLen+bodyLen {
		return transferFrame{}, fmt.Errorf("传输数据帧长度不一致：帧头声明内容为 %d 字节，实际为 %d 字节", bodyLen, len(payload)-frameHeaderLen)
	}
	frame := transferFrame{
		flags: payload[5],
		kind:  payload[6],
		index: binary.BigEndian.Uint32(payload[7:11]),
		total: binary.BigEndian.Uint32(payload[11:15]),
		body:  append([]byte{}, payload[frameHeaderLen:]...),
	}
	if frame.flags&^flagEncrypted != 0 {
		return transferFrame{}, fmt.Errorf("不支持传输数据帧标志 0x%02x", frame.flags)
	}
	if frame.kind != frameKindManifest && frame.kind != frameKindData {
		return transferFrame{}, fmt.Errorf("不支持传输数据帧类型 %d", frame.kind)
	}
	if frame.total == 0 {
		return transferFrame{}, errors.New("传输数据帧声明的总帧数不能为 0")
	}
	if frame.index >= frame.total {
		return transferFrame{}, fmt.Errorf("传输数据帧序号 %d 超出总帧数 %d 的范围", frame.index, frame.total)
	}
	return frame, nil
}

// unmarshalManifest 校验清单结构并按大端字节序恢复文件元数据。
func unmarshalManifest(data []byte) (Manifest, error) {
	minLen := len(manifestMagic) + len(manifestPasswordCheck) + 2 + 8 + sha256.Size
	if len(data) < minLen {
		return Manifest{}, errors.New("文件清单内容过短")
	}

	offset := 0
	if string(data[offset:offset+len(manifestMagic)]) != manifestMagic {
		return Manifest{}, errors.New("文件清单标识不匹配，内容可能已损坏")
	}
	offset += len(manifestMagic)

	if !bytes.Equal(data[offset:offset+len(manifestPasswordCheck)], manifestPasswordCheck) {
		return Manifest{}, errors.New("文件清单中的密码校验信息不匹配")
	}
	offset += len(manifestPasswordCheck)

	nameLen := int(binary.BigEndian.Uint16(data[offset : offset+2]))
	offset += 2
	if nameLen == 0 {
		return Manifest{}, errors.New("文件清单中的原文件名为空")
	}
	if nameLen > maxFileNameLength {
		return Manifest{}, errors.New("文件清单中的原文件名过长")
	}
	if len(data) != offset+nameLen+8+sha256.Size {
		return Manifest{}, errors.New("文件清单中的原文件名长度与实际内容不一致")
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
		return Manifest{}, nil, errors.New("没有找到 TransferGo 传输数据帧")
	}

	manifestFrame, ok := frames[0]
	if !ok {
		return Manifest{}, nil, errors.New("缺少文件清单帧，无法确定原文件信息")
	}
	if manifestFrame.kind != frameKindManifest {
		return Manifest{}, nil, errors.New("第 0 个传输数据帧不是文件清单帧")
	}
	if uint64(total) > uint64(len(frames)) {
		return Manifest{}, nil, errors.New("缺少一个或多个传输数据帧，无法还原完整文件")
	}
	encrypted := manifestFrame.flags&flagEncrypted != 0

	var salt []byte
	var gcm cipher.AEAD
	var manifestPlain []byte
	if encrypted {
		// 清单帧携带派生密钥所需的盐；密码错误会在清单认证阶段立即失败。
		if password == "" {
			return Manifest{}, nil, errors.New("视频内容已加密，请使用 -p 或 -password 参数提供密码")
		}
		if len(manifestFrame.body) < saltSize+nonceSize {
			return Manifest{}, nil, errors.New("加密后的文件清单内容过短，数据可能不完整")
		}
		salt = append([]byte{}, manifestFrame.body[:saltSize]...)
		var err error
		gcm, err = makeGCM(password, salt)
		if err != nil {
			return Manifest{}, nil, E("创建解密器失败", err)
		}
		manifestPlain, err = decryptFrameBody(gcm, manifestFrame, manifestFrame.body[saltSize:], salt)
		if err != nil {
			return Manifest{}, nil, errors.New("密码校验失败，请确认密码是否正确")
		}
	} else {
		manifestPlain = append([]byte{}, manifestFrame.body...)
	}

	manifest, err := unmarshalManifest(manifestPlain)
	if err != nil {
		if encrypted {
			return Manifest{}, nil, errors.New("密码校验失败，请确认密码是否正确")
		}
		return Manifest{}, nil, E("解析文件清单失败", err)
	}
	// 先确认所有编号都存在，再开始拼接，避免返回部分文件内容。
	for index := uint32(0); index < total; index++ {
		if _, ok := frames[index]; !ok {
			return Manifest{}, nil, fmt.Errorf("缺少第 %d 个传输数据帧", index)
		}
	}

	// 数据帧从序号一开始，序号零固定保留给清单帧。
	var output bytes.Buffer
	for index := uint32(1); index < total; index++ {
		frame := frames[index]
		if frame.kind != frameKindData {
			return Manifest{}, nil, fmt.Errorf("第 %d 个传输数据帧不是文件数据帧", index)
		}
		if frame.flags != manifestFrame.flags {
			return Manifest{}, nil, fmt.Errorf("第 %d 个传输数据帧的加密标志与文件清单不一致", index)
		}
		var chunk []byte
		if encrypted {
			chunk, err = decryptFrameBody(gcm, frame, frame.body, salt)
			if err != nil {
				return Manifest{}, nil, fmt.Errorf("解密第 %d 个传输数据帧失败：%w", index, err)
			}
		} else {
			chunk = frame.body
		}
		if _, err := output.Write(chunk); err != nil {
			return Manifest{}, nil, E("合并文件内容失败", err)
		}
	}

	// 长度和摘要同时匹配，才能确认还原结果没有缺失、乱序或被篡改。
	result := output.Bytes()
	if uint64(len(result)) != manifest.fileSize {
		return Manifest{}, nil, fmt.Errorf("还原后的文件大小为 %d 字节，与文件清单记录的 %d 字节不一致", len(result), manifest.fileSize)
	}
	sum := sha256.Sum256(result)
	if sum != manifest.fileSHA256 {
		return Manifest{}, nil, errors.New("还原后的文件哈希值与文件清单不一致，文件内容可能不完整或已损坏")
	}

	return manifest, result, nil
}

// decryptFrameBody 拆分 Nonce 和密文，并使用与编码端相同的认证附加数据解密。
func decryptFrameBody(gcm cipher.AEAD, frame transferFrame, body []byte, salt []byte) ([]byte, error) {
	if len(body) < nonceSize+gcm.Overhead() {
		return nil, errors.New("加密传输数据帧的内容过短，数据可能不完整")
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
		return nil, E("派生加密密钥失败", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, E("创建 AES 加密器失败", err)
	}
	return cipher.NewGCM(block)
}

// cryptoRandomBytes 从密码学安全随机源读取指定长度的字节。
func cryptoRandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, E("生成加密随机数失败", err)
	}
	return b, nil
}

// endregion
