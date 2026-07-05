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
	protocolVersion = byte(1)

	// Serialized transfer frame layout, in bytes:
	//   0..3   magic "TGQR"
	//   4      protocol version
	//   5      flags
	//   6      frame kind
	//   7..10  sequence number, big-endian
	//   11..14 total frame count, big-endian
	//   15..16 body length, big-endian
	//   17..   body bytes
	//
	// The fixed header keeps every QR self-describing, so decode can collect
	// frames from unordered images and still detect missing or foreign frames.
	frameHeaderLen = 17
	frameMagic     = "TGQR"

	// frameFlagEncrypted means the frame body is protected by AES-GCM. For the
	// manifest frame the body is salt || nonce || ciphertext || tag; for data
	// frames it is nonce || ciphertext || tag.
	frameFlagEncrypted = byte(1 << 0)

	// Frame 0 is always the manifest. Data frames start at sequence 1, which
	// lets the restore path verify metadata before accepting file bytes.
	frameKindManifest = byte(0)
	frameKindData     = byte(1)

	// Crypto parameters are fixed by the protocol. A 12-byte nonce is the GCM
	// standard size, and a 32-byte key selects AES-256.
	saltSize         = 16
	nonceSize        = 12
	aesKeySize       = 32
	pbkdf2Iterations = 200_000

	// Defaults bias toward filmed-screen decoding: each QR is monochrome, uses a
	// higher version for capacity, and leaves enough pixels per module for phone
	// recordings to survive focus, scaling, and compression.
	defaultFPS         = 3.0
	defaultSampleFPS   = 9.0
	defaultQRSize      = 240
	defaultQRVersion   = 12
	defaultVideoWidth  = 800
	defaultVideoHeight = 800
	defaultGridSize    = 3
	defaultCRF         = 24
	defaultChunkSize   = 240
	maxQRBytePayload   = 2953
	maxFrameBodyLen    = 1<<16 - 1

	// Manifest layout:
	//   magic "TGM1"
	//   password check marker
	//   original file size
	//   plaintext chunk size
	//   plaintext chunk count
	//   SHA-256 of the original file
	//   file name length
	//   file name bytes
	manifestMagic = "TGM1"
)

// manifestPasswordCheck is deliberately included inside the manifest plaintext.
// When the manifest is encrypted, a wrong password fails before any data frame
// is processed. When it is not encrypted, the marker also rejects random QR data
// that happens to have the manifest magic.
var manifestPasswordCheck = []byte("TG-PASS-OK-v1\x00\x00\x00")

// transferFrame is the protocol unit stored in one QR code. The header fields
// are authenticated when encryption is enabled, which prevents a valid encrypted
// body from being moved to another sequence number or total frame count.
type transferFrame struct {
	Flags byte
	Kind  byte
	Seq   uint32
	Total uint32
	Body  []byte
}

// manifest describes the original file and the expected stream shape. Decode
// trusts it only after the frame collection, password check, and final SHA-256
// verification have succeeded.
type manifest struct {
	FileName   string
	FileSize   uint64
	ChunkSize  uint32
	ChunkCount uint32
	SHA256     [sha256.Size]byte
}

// buildTransferFrames creates a manifest frame plus one data frame per chunk.
// If password is non-empty, the manifest and all data chunks are encrypted with
// a single key derived from that password and a per-video random salt.
func buildTransferFrames(input []byte, fileName string, password string, chunkSize int) ([]transferFrame, manifest, error) {
	if chunkSize <= 0 {
		return nil, manifest{}, errors.New("chunk size must be greater than 0")
	}

	// Frame sequence 0 is reserved for the manifest, so the maximum data chunk
	// count is one less than the uint32 sequence space.
	chunkCount := uint64(0)
	if len(input) > 0 {
		chunkCount = uint64((len(input) + chunkSize - 1) / chunkSize)
	}
	if chunkCount > uint64(^uint32(0)-1) {
		return nil, manifest{}, errors.New("too many chunks for protocol")
	}

	total := uint32(chunkCount + 1)
	encrypted := password != ""
	flags := byte(0)
	if encrypted {
		flags |= frameFlagEncrypted
	}

	meta := manifest{
		FileName:   fileName,
		FileSize:   uint64(len(input)),
		ChunkSize:  uint32(chunkSize),
		ChunkCount: uint32(chunkCount),
		SHA256:     sha256.Sum256(input),
	}

	var salt []byte
	var gcm cipher.AEAD
	if encrypted {
		// The salt is stored only once, in the manifest frame. It is also folded
		// into the AES-GCM AAD for every frame, binding all frames to one video.
		var err error
		salt, err = randomBytes(saltSize)
		if err != nil {
			return nil, manifest{}, err
		}
		gcm, err = makeGCM(password, salt)
		if err != nil {
			return nil, manifest{}, err
		}
	}

	manifestPlain, err := marshalManifest(meta)
	if err != nil {
		return nil, manifest{}, err
	}

	frames := make([]transferFrame, 0, total)
	// Sequence 0 carries the metadata required to validate every data frame and
	// the final file contents.
	manifestFrame := transferFrame{
		Flags: flags,
		Kind:  frameKindManifest,
		Seq:   0,
		Total: total,
	}
	if encrypted {
		// Store salt before the encrypted manifest so decoders can derive the
		// key before calling AES-GCM Open.
		body, err := encryptFrameBody(gcm, manifestFrame, manifestPlain, salt)
		if err != nil {
			return nil, manifest{}, err
		}
		manifestFrame.Body = append(append([]byte{}, salt...), body...)
	} else {
		manifestFrame.Body = manifestPlain
	}
	frames = append(frames, manifestFrame)

	// Data frames are numbered contiguously after the manifest. The restore path
	// requires every sequence number, so missing frames are reported explicitly.
	for seq, offset := uint32(1), 0; offset < len(input); seq++ {
		end := offset + chunkSize
		if end > len(input) {
			end = len(input)
		}
		chunk := input[offset:end]
		frame := transferFrame{
			Flags: flags,
			Kind:  frameKindData,
			Seq:   seq,
			Total: total,
		}
		if encrypted {
			body, err := encryptFrameBody(gcm, frame, chunk, salt)
			if err != nil {
				return nil, manifest{}, err
			}
			frame.Body = body
		} else {
			// Copy plaintext chunks so callers cannot mutate frames by changing
			// the original input slice after this function returns.
			frame.Body = append([]byte{}, chunk...)
		}
		frames = append(frames, frame)
		offset = end
	}

	return frames, meta, nil
}

// restoreFromFrames verifies a collected frame set and returns the original
// bytes. The function is intentionally strict: it rejects missing frames,
// conflicting metadata, wrong passwords, bad hashes, and unexpected frame kinds.
func restoreFromFrames(frames map[uint32]transferFrame, total uint32, password string) (manifest, []byte, error) {
	if total == 0 {
		return manifest{}, nil, errors.New("no transfer frames found")
	}

	// The manifest is the root of trust for the stream shape. Without it there
	// is no safe way to know the original size, chunk count, or file hash.
	manifestFrame, ok := frames[0]
	if !ok {
		return manifest{}, nil, fmt.Errorf("missing frame(s): %s", formatMissingFrames(frames, total))
	}
	if manifestFrame.Kind != frameKindManifest {
		return manifest{}, nil, errors.New("frame 0 is not a manifest")
	}
	encrypted := manifestFrame.Flags&frameFlagEncrypted != 0

	var salt []byte
	var gcm cipher.AEAD
	var manifestPlain []byte
	if encrypted {
		// The manifest salt is required to derive the AES-GCM key. A successful
		// decrypt also authenticates the password-check marker inside it.
		if password == "" {
			return manifest{}, nil, errors.New("video is encrypted; provide -p")
		}
		if len(manifestFrame.Body) < saltSize+nonceSize {
			return manifest{}, nil, errors.New("encrypted manifest is too short")
		}
		salt = append([]byte{}, manifestFrame.Body[:saltSize]...)
		var err error
		gcm, err = makeGCM(password, salt)
		if err != nil {
			return manifest{}, nil, err
		}
		manifestPlain, err = decryptFrameBody(gcm, manifestFrame, manifestFrame.Body[saltSize:], salt)
		if err != nil {
			return manifest{}, nil, errors.New("password check failed")
		}
	} else {
		manifestPlain = append([]byte{}, manifestFrame.Body...)
	}

	meta, err := parseManifest(manifestPlain)
	if err != nil {
		if encrypted {
			return manifest{}, nil, errors.New("password check failed")
		}
		return manifest{}, nil, err
	}
	if meta.ChunkCount != total-1 {
		return manifest{}, nil, fmt.Errorf("manifest chunk count %d does not match frame total %d", meta.ChunkCount, total)
	}
	// Check completeness before assembling output, so failures point to the
	// missing sequence numbers rather than a later hash mismatch.
	for seq := uint32(0); seq < total; seq++ {
		if _, ok := frames[seq]; !ok {
			return manifest{}, nil, fmt.Errorf("missing frame(s): %s", formatMissingFrames(frames, total))
		}
	}

	var output bytes.Buffer
	for seq := uint32(1); seq < total; seq++ {
		frame := frames[seq]
		if frame.Kind != frameKindData {
			return manifest{}, nil, fmt.Errorf("frame %d is not a data frame", seq)
		}
		if frame.Flags != manifestFrame.Flags {
			return manifest{}, nil, fmt.Errorf("frame %d encryption flags do not match manifest", seq)
		}
		var chunk []byte
		if encrypted {
			// AAD includes kind, sequence, total, and salt, so frame bytes cannot
			// be replayed in another position without failing authentication.
			chunk, err = decryptFrameBody(gcm, frame, frame.Body, salt)
			if err != nil {
				return manifest{}, nil, fmt.Errorf("decrypt frame %d: %w", seq, err)
			}
		} else {
			chunk = frame.Body
		}
		if _, err := output.Write(chunk); err != nil {
			return manifest{}, nil, err
		}
	}

	result := output.Bytes()
	// Size catches truncation or extra data; SHA-256 catches corruption even
	// when the byte count happens to match.
	if uint64(len(result)) != meta.FileSize {
		return manifest{}, nil, fmt.Errorf("restored file size %d does not match manifest size %d", len(result), meta.FileSize)
	}
	sum := sha256.Sum256(result)
	if sum != meta.SHA256 {
		return manifest{}, nil, errors.New("restored file hash does not match manifest")
	}

	return meta, result, nil
}

func marshalFrame(frame transferFrame) []byte {
	if len(frame.Body) > maxFrameBodyLen {
		panic("frame body too large")
	}
	// Big-endian fields make the byte layout stable across platforms and easy
	// to inspect with common binary tools.
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

// parseFrame reverses marshalFrame and validates enough structure to reject
// unrelated QR codes before they can affect frame collection.
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
	// Copy the body so the returned frame owns its bytes independently of the
	// decoder buffer.
	frame := transferFrame{
		Flags: payload[5],
		Kind:  payload[6],
		Seq:   binary.BigEndian.Uint32(payload[7:11]),
		Total: binary.BigEndian.Uint32(payload[11:15]),
		Body:  append([]byte{}, payload[frameHeaderLen:]...),
	}
	if frame.Flags&^frameFlagEncrypted != 0 {
		return transferFrame{}, fmt.Errorf("unsupported frame flags 0x%02x", frame.Flags)
	}
	if frame.Kind != frameKindManifest && frame.Kind != frameKindData {
		return transferFrame{}, fmt.Errorf("unsupported frame kind %d", frame.Kind)
	}
	if frame.Total == 0 {
		return transferFrame{}, errors.New("frame total cannot be 0")
	}
	if frame.Seq >= frame.Total {
		return transferFrame{}, fmt.Errorf("frame sequence %d is outside total %d", frame.Seq, frame.Total)
	}
	return frame, nil
}

// marshalManifest encodes metadata into a compact binary format that fits into
// the same QR payload path as data frames.
func marshalManifest(meta manifest) ([]byte, error) {
	name := []byte(meta.FileName)
	if len(name) > maxFrameBodyLen {
		return nil, errors.New("file name is too long for manifest")
	}

	// The capacity hint avoids reallocations for the fixed fields plus the file
	// name. It is not part of the protocol; the field order below is.
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

// parseManifest validates the manifest marker and then reads each fixed-width
// field in the same order written by marshalManifest.
func parseManifest(payload []byte) (manifest, error) {
	minLen := 4 + len(manifestPasswordCheck) + 8 + 4 + 4 + sha256.Size + 2
	if len(payload) < minLen {
		return manifest{}, errors.New("manifest is too short")
	}
	if string(payload[:4]) != manifestMagic {
		return manifest{}, errors.New("manifest magic mismatch")
	}
	offset := 4
	if !bytes.Equal(payload[offset:offset+len(manifestPasswordCheck)], manifestPasswordCheck) {
		return manifest{}, errors.New("manifest password check mismatch")
	}
	offset += len(manifestPasswordCheck)

	meta := manifest{}
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
		return manifest{}, errors.New("manifest file name length mismatch")
	}
	meta.FileName = string(payload[offset:])
	return meta, nil
}

// encryptFrameBody returns nonce || ciphertext || tag. The nonce is generated
// per frame and stored beside the ciphertext because GCM needs the same nonce
// for decryption but does not require it to be secret.
func encryptFrameBody(gcm cipher.AEAD, frame transferFrame, plaintext []byte, salt []byte) ([]byte, error) {
	nonce, err := randomBytes(nonceSize)
	if err != nil {
		return nil, err
	}
	aad := frameAAD(frame, salt)
	sealed := gcm.Seal(nil, nonce, plaintext, aad)
	out := make([]byte, 0, len(nonce)+len(sealed))
	out = append(out, nonce...)
	out = append(out, sealed...)
	return out, nil
}

// decryptFrameBody verifies the GCM tag before returning plaintext. Any change
// to the body or authenticated frame metadata is reported as an error.
func decryptFrameBody(gcm cipher.AEAD, frame transferFrame, body []byte, salt []byte) ([]byte, error) {
	if len(body) < nonceSize+gcm.Overhead() {
		return nil, errors.New("encrypted frame body is too short")
	}
	nonce := body[:nonceSize]
	ciphertext := body[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, frameAAD(frame, salt))
}

// frameAAD is authenticated but not encrypted. It binds ciphertext to protocol
// identity, frame kind, sequence number, total count, and the per-video salt.
func frameAAD(frame transferFrame, salt []byte) []byte {
	out := make([]byte, 0, 4+1+1+1+4+4+len(salt))
	out = append(out, []byte(frameMagic)...)
	out = append(out, protocolVersion, frame.Flags, frame.Kind)
	out = binary.BigEndian.AppendUint32(out, frame.Seq)
	out = binary.BigEndian.AppendUint32(out, frame.Total)
	out = append(out, salt...)
	return out
}

// makeGCM derives an AES-256 key from the password and salt, then wraps it in
// GCM so encryption and authentication happen together.
func makeGCM(password string, salt []byte) (cipher.AEAD, error) {
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, aesKeySize)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// randomBytes reads from crypto/rand so salts and nonces are unpredictable.
func randomBytes(n int) ([]byte, error) {
	out := make([]byte, n)
	if _, err := rand.Read(out); err != nil {
		return nil, err
	}
	return out, nil
}

// autoChunkSize chooses a camera-friendly plaintext chunk size that still fits
// the requested QR version. The cap is intentionally below the theoretical QR
// capacity because filmed screens lose fine detail long before the encoder
// itself runs out of space.
func autoChunkSize(encrypted bool, qrSize int, qrVersion int) (int, error) {
	low, high := 1, maxQRBytePayload
	best := 0
	for low <= high {
		mid := low + (high-low)/2
		if canEncodeChunkSize(mid, encrypted, qrSize, qrVersion) {
			best = mid
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	if best == 0 {
		return 0, fmt.Errorf("no data chunk size fits QR version %d", qrVersion)
	}
	if best > defaultChunkSize {
		return defaultChunkSize, nil
	}
	return best, nil
}

// canEncodeChunkSize builds a representative data frame and asks the QR encoder
// whether it fits. Encrypted frames reserve space for the GCM nonce and tag.
func canEncodeChunkSize(chunkSize int, encrypted bool, qrSize int, qrVersion int) bool {
	bodyLen := chunkSize
	if encrypted {
		bodyLen += nonceSize + 16
	}
	frame := transferFrame{
		Flags: 0,
		Kind:  frameKindData,
		Seq:   1,
		Total: 2,
		Body:  bytes.Repeat([]byte{0x80}, bodyLen),
	}
	if encrypted {
		frame.Flags = frameFlagEncrypted
	}
	_, err := encodeQRPNG(marshalFrame(frame), qrSize, qrVersion)
	return err == nil
}

// sameFrame treats identical duplicate QR captures as harmless while still
// allowing collectFramesFromImages to reject conflicting duplicates.
func sameFrame(a, b transferFrame) bool {
	return a.Flags == b.Flags &&
		a.Kind == b.Kind &&
		a.Seq == b.Seq &&
		a.Total == b.Total &&
		bytes.Equal(a.Body, b.Body)
}

// formatMissingFrames keeps error messages readable by listing the first few
// missing sequence numbers and eliding the rest.
func formatMissingFrames(frames map[uint32]transferFrame, total uint32) string {
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
