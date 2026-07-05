package core

import (
	"bytes"
	"strings"
	"testing"
)

func TestProtocolMarshalParseFrameCopiesBody(t *testing.T) {
	ctx := newProtocolContext()
	frame := transferFrame{
		Kind:  frameKindData,
		Seq:   1,
		Total: 2,
		Body:  []byte("payload"),
	}

	parsed, err := ctx.parseFrame(ctx.marshalFrame(frame))
	if err != nil {
		t.Fatal(err)
	}
	if !sameFrame(parsed, frame) {
		t.Fatalf("parsed frame = %#v, want %#v", parsed, frame)
	}

	parsed.Body[0] = 'P'
	if frame.Body[0] != 'p' {
		t.Fatal("parseFrame did not copy frame body")
	}
}

func TestProtocolEncryptedRoundTripAndWrongPassword(t *testing.T) {
	ctx := deterministicProtocolContext()
	input := []byte("secret file contents that should be authenticated before restore")

	frames, _, err := ctx.buildTransferFrames(input, "secret.txt", "correct horse", 16)
	if err != nil {
		t.Fatal(err)
	}
	frameMap := framesToMap(frames)

	if _, _, err := ctx.restoreFromFrames(frameMap, uint32(len(frames)), "wrong horse"); err == nil || !strings.Contains(err.Error(), "password check failed") {
		t.Fatalf("wrong password error = %v, want password check failed", err)
	}

	meta, output, err := ctx.restoreFromFrames(frameMap, uint32(len(frames)), "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if meta.FileName != "secret.txt" {
		t.Fatalf("file name = %q, want secret.txt", meta.FileName)
	}
	if !bytes.Equal(output, input) {
		t.Fatal("restored encrypted bytes do not match input")
	}
}

func TestProtocolMissingFrameFails(t *testing.T) {
	ctx := newProtocolContext()
	frames, _, err := ctx.buildTransferFrames([]byte("0123456789abcdef"), "split.txt", "", 4)
	if err != nil {
		t.Fatal(err)
	}
	frameMap := framesToMap(frames)
	delete(frameMap, 2)

	_, _, err = ctx.restoreFromFrames(frameMap, uint32(len(frames)), "")
	if err == nil || !strings.Contains(err.Error(), "missing frame") {
		t.Fatalf("missing frame error = %v, want missing frame", err)
	}
}

func TestProtocolBuildRejectsInvalidChunkSize(t *testing.T) {
	ctx := newProtocolContext()

	_, _, err := ctx.buildTransferFrames([]byte("data"), "data.bin", "", 0)
	if err == nil || !strings.Contains(err.Error(), "chunk size must be greater than 0") {
		t.Fatalf("buildTransferFrames error = %v, want chunk size validation", err)
	}
}

func deterministicProtocolContext() protocolContext {
	ctx := newProtocolContext()
	var next byte = 1
	ctx.randomBytes = func(n int) ([]byte, error) {
		out := make([]byte, n)
		for i := range out {
			out[i] = next
			next++
		}
		return out, nil
	}
	return ctx
}

func framesToMap(frames []transferFrame) map[uint32]transferFrame {
	out := make(map[uint32]transferFrame, len(frames))
	for _, frame := range frames {
		out[frame.Seq] = frame
	}
	return out
}
