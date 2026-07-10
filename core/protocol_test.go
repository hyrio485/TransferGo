package core

import (
	"bytes"
	"strings"
	"testing"
)

// 本文件测试传输协议的编码、解析、加密和完整性校验。
// 运行方式：在仓库根目录执行 go test ./...，或执行 go test ./core -run Protocol。
// 期望影响：所有数据仅保存在内存中，不创建文件，也不访问网络或外部命令。

// TestProtocolRoundTrip 验证明文和加密协议的完整往返能力。
// 前置条件：使用内存中的空文件、单块文件和多块文件，并提供可选密码。
// 执行方式：先调用 EncodeFile，再追加一个完全相同的重复帧并调用 RestoreFile。
// 期望结果：重复帧被安全忽略，文件名和文件内容与输入完全一致。
func TestProtocolRoundTrip(t *testing.T) {
	tests := []struct {
		name      string
		input     []byte
		password  string
		chunkSize int
	}{
		{name: "empty", input: []byte{}, chunkSize: 8},
		{name: "plain single chunk", input: []byte("plain"), chunkSize: 32},
		{name: "plain multiple chunks", input: []byte("TransferGo protocol round trip"), chunkSize: 7},
		{name: "encrypted", input: []byte("encrypted payload"), password: "secret", chunkSize: 5},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payloads, err := NewProtocolContext().EncodeFile(test.input, "input.bin", test.password, test.chunkSize)
			if err != nil {
				t.Fatal(err)
			}
			payloads = append(payloads, append([]byte{}, payloads[len(payloads)-1]...))
			manifest, output, err := RestoreFile(payloads, test.password)
			if err != nil {
				t.Fatal(err)
			}
			if manifest.FileName() != "input.bin" || !bytes.Equal(output, test.input) {
				t.Fatalf("protocol round trip mismatch: manifest = %q, output = %q", manifest.FileName(), output)
			}
		})
	}
}

// TestProtocolRejectsWrongPassword 验证加密清单的密码认证。
// 前置条件：使用正确密码生成一组加密协议帧。
// 执行方式：使用不同密码调用 RestoreFile。
// 期望结果：还原在清单认证阶段失败，并返回 password check failed。
func TestProtocolRejectsWrongPassword(t *testing.T) {
	payloads, err := NewProtocolContext().EncodeFile([]byte("secret data"), "input.bin", "correct", 4)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = RestoreFile(payloads, "wrong")
	if err == nil || !strings.Contains(err.Error(), "password check failed") {
		t.Fatalf("RestoreFile() error = %v", err)
	}
}

// TestProtocolRejectsMissingFrame 验证协议不会返回不完整文件。
// 前置条件：生成包含多个数据帧的有效载荷集合。
// 执行方式：删除中间一个数据帧后调用 RestoreFile。
// 期望结果：还原失败，并报告载荷数量不足或存在缺帧。
func TestProtocolRejectsMissingFrame(t *testing.T) {
	payloads, err := NewProtocolContext().EncodeFile([]byte("multiple chunks are required"), "input.bin", "", 4)
	if err != nil {
		t.Fatal(err)
	}
	payloads = append(payloads[:2], payloads[3:]...)
	_, _, err = RestoreFile(payloads, "")
	if err == nil || (!strings.Contains(err.Error(), "exceeds available payload count") && !strings.Contains(err.Error(), "missing")) {
		t.Fatalf("RestoreFile() error = %v", err)
	}
}

// TestProtocolRejectsMissingManifest 验证缺少零号清单帧时返回明确错误。
// 前置条件：生成包含清单帧和多个数据帧的有效载荷集合。
// 执行方式：删除零号清单帧后调用 RestoreFile。
// 期望结果：还原失败，并明确报告 missing manifest frame。
func TestProtocolRejectsMissingManifest(t *testing.T) {
	payloads, err := NewProtocolContext().EncodeFile([]byte("manifest must be present"), "input.bin", "", 4)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = RestoreFile(payloads[1:], "")
	if err == nil || !strings.Contains(err.Error(), "missing manifest frame") {
		t.Fatalf("RestoreFile() error = %v", err)
	}
}

// TestProtocolRejectsConflictingDuplicate 验证同序号帧的冲突检测。
// 前置条件：生成有效明文协议帧，并复制一个数据帧。
// 执行方式：修改复制帧的帧体后，把它作为同序号重复帧加入载荷集合。
// 期望结果：RestoreFile 拒绝内容不同的重复帧并返回 conflicting duplicate frame。
func TestProtocolRejectsConflictingDuplicate(t *testing.T) {
	payloads, err := NewProtocolContext().EncodeFile([]byte("conflicting duplicate"), "input.bin", "", 4)
	if err != nil {
		t.Fatal(err)
	}
	conflicting := append([]byte{}, payloads[1]...)
	conflicting[len(conflicting)-1] ^= 0xff
	payloads = append(payloads, conflicting)
	_, _, err = RestoreFile(payloads, "")
	if err == nil || !strings.Contains(err.Error(), "conflicting duplicate frame") {
		t.Fatalf("RestoreFile() error = %v", err)
	}
}

// TestProtocolDetectsTamperedPlaintext 验证未加密模式的最终摘要检查。
// 前置条件：生成一组有效明文协议帧。
// 执行方式：修改一个数据帧的内容，但保持帧头和长度合法。
// 期望结果：帧能够解析，但最终 SHA-256 不匹配，文件不会被返回。
func TestProtocolDetectsTamperedPlaintext(t *testing.T) {
	payloads, err := NewProtocolContext().EncodeFile([]byte("hash protected data"), "input.bin", "", 4)
	if err != nil {
		t.Fatal(err)
	}
	payloads[1] = append([]byte{}, payloads[1]...)
	payloads[1][len(payloads[1])-1] ^= 0xff
	_, _, err = RestoreFile(payloads, "")
	if err == nil || !strings.Contains(err.Error(), "hash does not match") {
		t.Fatalf("RestoreFile() error = %v", err)
	}
}

// TestRestoreFileRejectsImpossibleFrameTotal 验证恶意超大帧总数会被立即拒绝。
// 前置条件：构造一个格式合法但 total 远大于载荷数量的清单帧。
// 执行方式：仅把该帧传给 RestoreFile。
// 期望结果：函数在进入按序号循环前返回错误，避免超长空转。
func TestRestoreFileRejectsImpossibleFrameTotal(t *testing.T) {
	payload, err := marshalFrame(transferFrame{
		kind:  frameKindManifest,
		index: 0,
		total: 1_000_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = RestoreFile([][]byte{payload}, "")
	if err == nil || !strings.Contains(err.Error(), "exceeds available payload count") {
		t.Fatalf("RestoreFile() error = %v", err)
	}
}

// TestParseFrameRejectsInvalidHeaders 验证协议帧头的防御性校验。
// 前置条件：每个子测试构造一种格式错误的字节序列。
// 执行方式：直接调用 parseFrame，不经过二维码或文件系统。
// 期望结果：短载荷、错误魔数、错误版本和非法字段全部返回错误。
func TestParseFrameRejectsInvalidHeaders(t *testing.T) {
	valid, err := marshalFrame(transferFrame{kind: frameKindManifest, index: 0, total: 1})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		payload []byte
	}{
		{name: "too short", payload: []byte("TG")},
		{name: "bad magic", payload: mutateByte(valid, 0, 'X')},
		{name: "bad version", payload: mutateByte(valid, 4, protocolVersion+1)},
		{name: "bad flags", payload: mutateByte(valid, 5, 0x80)},
		{name: "bad kind", payload: mutateByte(valid, 6, 0xff)},
		{name: "length mismatch", payload: append(append([]byte{}, valid...), 0)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseFrame(test.payload); err == nil {
				t.Fatalf("parseFrame(%q) succeeded", test.payload)
			}
		})
	}
}

// TestProtocolRejectsInvalidEncodeInput 验证编码入口的基础约束。
// 前置条件：不需要文件或外部依赖。
// 执行方式：分别使用非法分块大小、空文件名和过长文件名编码。
// 期望结果：所有输入都在生成协议帧前返回错误。
func TestProtocolRejectsInvalidEncodeInput(t *testing.T) {
	ctx := NewProtocolContext()
	if _, err := ctx.EncodeFile([]byte("data"), "input.bin", "", 0); err == nil {
		t.Fatal("EncodeFile accepted zero chunk size")
	}
	if _, err := ctx.EncodeFile([]byte("data"), "", "", 4); err == nil {
		t.Fatal("EncodeFile accepted empty file name")
	}
	if _, err := ctx.EncodeFile([]byte("data"), strings.Repeat("a", maxFileNameLength+1), "", 4); err == nil {
		t.Fatal("EncodeFile accepted overlong file name")
	}
}

// mutateByte 返回 payload 的副本，并把指定位置替换为 value。
func mutateByte(payload []byte, index int, value byte) []byte {
	result := append([]byte{}, payload...)
	result[index] = value
	return result
}
