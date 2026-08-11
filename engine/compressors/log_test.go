package compressors_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/compressors"
)

func noisyLog() []byte {
	var b strings.Builder
	b.WriteString("[INFO] starting\n")
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&b, "[INFO] step %d ok\n", i)
	}
	b.WriteString("[ERROR] kaboom: undefined symbol\n")
	b.WriteString("  at frame (file.go:10)\n")
	b.WriteString("[INFO] done\n")
	return []byte(b.String())
}

func TestLogKeepsErrorsDropsNoise(t *testing.T) {
	c := compressors.NewLog()
	out, ok := c.Compress(noisyLog())
	if !ok {
		t.Fatal("expected log compression")
	}
	if !bytes.Contains(out, []byte("[ERROR] kaboom")) {
		t.Error("error line must be kept")
	}
	if !bytes.Contains(out, []byte("undefined symbol")) {
		t.Error("error detail must be kept")
	}
	if !bytes.Contains(out, []byte("lines elided (caveman)")) {
		t.Errorf("expected an elision marker: %s", out)
	}
	if len(out) >= len(noisyLog()) {
		t.Error("expected the log to shrink")
	}
}

func TestLogIdempotent(t *testing.T) {
	c := compressors.NewLog()
	first, ok := c.Compress(noisyLog())
	if !ok {
		t.Fatal("expected compression")
	}
	second, ok := c.Compress(first)
	if ok && !bytes.Equal(first, second) {
		t.Errorf("not idempotent:\n first=%s\nsecond=%s", first, second)
	}
}

func TestLogPreservesCRLF(t *testing.T) {
	c := compressors.NewLog()
	var b strings.Builder
	b.WriteString("[INFO] a\r\n")
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, "[INFO] noise %d\r\n", i)
	}
	b.WriteString("[ERROR] bad\r\n")
	out, ok := c.Compress([]byte(b.String()))
	if !ok {
		t.Fatal("expected compression")
	}
	if !bytes.Contains(out, []byte("\r\n")) {
		t.Error("CRLF line endings must be preserved")
	}
	if bytes.Contains(bytes.ReplaceAll(out, []byte("\r\n"), []byte("")), []byte("\n")) {
		t.Error("must not introduce bare LF when input was CRLF")
	}
}

func TestLogInvalidUTF8PassesThrough(t *testing.T) {
	c := compressors.NewLog()
	if _, ok := c.Compress([]byte{0xff, 0xfe, 0x00, 0x01, '\n', 0xff}); ok {
		t.Error("invalid UTF-8 must pass through (!ok)")
	}
}

func TestLogTooShortPassesThrough(t *testing.T) {
	c := compressors.NewLog()
	if _, ok := c.Compress([]byte("[INFO] one\n[INFO] two\n")); ok {
		t.Error("a tiny log must pass through (!ok)")
	}
}

func TestLogQueryKeepsRelevantNonErrorLine(t *testing.T) {
	var b strings.Builder
	b.WriteString("[INFO] starting\n")
	for i := 0; i < 50; i++ {
		message := fmt.Sprintf("[INFO] routine worker %d\n", i)
		if i == 27 {
			message = "[INFO] websocket reconnect backoff selected\n"
		}
		b.WriteString(message)
	}
	b.WriteString("[INFO] done\n")
	c := compressors.NewLog().(compressors.QueryAwareCompressor)
	out, ok := c.CompressQuery([]byte(b.String()), "websocket reconnect")
	if !ok {
		t.Fatal("expected query-aware log compression")
	}
	if !bytes.Contains(out, []byte("websocket reconnect backoff")) {
		t.Fatalf("query-relevant log line missing:\n%s", out)
	}
}
