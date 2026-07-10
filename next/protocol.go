package next

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

type ProtocolContext struct {
	randomBytes func(int) ([]byte, error)
}

func NewProtocolContext() ProtocolContext {
	return ProtocolContext{
		randomBytes: cryptoRandomBytes,
	}
}

type Manifest struct {
	fileName   string
	fileSize   uint64
	fileSHA256 [sha256.Size]byte
}

func (ctx Manifest) FileName() string {
	return ctx.fileName
}

func NewManifest(fileName string, fileSize uint64, fileSHA256 [sha256.Size]byte) Manifest {
	return Manifest{
		fileName:   fileName,
		fileSize:   fileSize,
		fileSHA256: fileSHA256,
	}
}

type transferFrame struct {
	flags byte
	kind  byte
	index uint32
	total uint32
	// Encrypted manifest: Salt(16) + Nonce(12) + Ciphertext
	// Encrypted data: Nonce(12) + Ciphertext
	// Non-encrypted manifest: manifest
	// Non-encrypted data: Data
	body []byte
}

// region Encode

func (ctx ProtocolContext) EncodeFile(input []byte, fileName string, password string, chunkSize int) ([][]byte, error) {
	if chunkSize <= 0 {
		return nil, errors.New("chunk size must be greater than 0")
	}

	chunkCount := uint64(0)
	if len(input) > 0 {
		chunkCount = uint64((len(input) + chunkSize - 1) / chunkSize)
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
		end := offset + chunkSize
		if end > len(input) {
			end = len(input)
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

func (ctx ProtocolContext) encryptFrameBody(gcm cipher.AEAD, frame transferFrame, plaintext []byte, salt []byte) ([]byte, error) {
	nonce, err := ctx.randomBytes(nonceSize)
	if err != nil {
		return nil, E("generate frame nonce", err)
	}
	sealed := gcm.Seal(nil, nonce, plaintext, frameAAD(frame, salt))
	return ConcatByteArrays(nonce, sealed), nil
}

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
		if total == 0 {
			total = frame.total
		} else if frame.total != total {
			return Manifest{}, nil, fmt.Errorf("frame %d reports total %d, expected %d", frame.index, frame.total, total)
		} else if frame.index >= total {
			return Manifest{}, nil, fmt.Errorf("frame %d is outside total %d", frame.index, total)
		}
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

func restoreFromFrames(frames map[uint32]transferFrame, total uint32, password string) (Manifest, []byte, error) {
	if total == 0 {
		return Manifest{}, nil, errors.New("no transfer frames found")
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
	for index := uint32(0); index < total; index++ {
		if _, ok := frames[index]; !ok {
			return Manifest{}, nil, fmt.Errorf("missing frame %d", index)
		}
	}

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

func decryptFrameBody(gcm cipher.AEAD, frame transferFrame, body []byte, salt []byte) ([]byte, error) {
	if len(body) < nonceSize+gcm.Overhead() {
		return nil, errors.New("encrypted frame body is too short")
	}
	nonce := body[:nonceSize]
	ciphertext := body[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, frameAAD(frame, salt))
}

func sameFrame(a, b transferFrame) bool {
	return a.flags == b.flags &&
		a.kind == b.kind &&
		a.index == b.index &&
		a.total == b.total &&
		bytes.Equal(a.body, b.body)
}

// endregion

// region tools

func frameAAD(frame transferFrame, salt []byte) []byte {
	out := make([]byte, 0, len(frameMagic)+1+1+1+4+4+len(salt))
	out = append(out, []byte(frameMagic)...)
	out = append(out, protocolVersion, frame.flags, frame.kind)
	out = binary.BigEndian.AppendUint32(out, frame.index)
	out = binary.BigEndian.AppendUint32(out, frame.total)
	out = append(out, salt...)
	return out
}

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

func cryptoRandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, E("generate random bytes", err)
	}
	return b, nil
}

// endregion
