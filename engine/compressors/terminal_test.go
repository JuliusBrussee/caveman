package compressors

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestTerminalStripsANSI(t *testing.T) {
	in := []byte("\x1b[32mPASS\x1b[0m build ok\n")
	out, ok := NewTerminal().Compress(in)
	if !ok {
		t.Fatal("expected ANSI-bearing line to compress")
	}
	if bytes.ContainsRune(out, 0x1b) {
		t.Errorf("output still contains a raw ESC byte: %q", out)
	}
	if bytes.Contains(out, []byte("[32m")) || bytes.Contains(out, []byte("[0m")) {
		t.Errorf("output still contains ANSI artifacts: %q", out)
	}
	if !bytes.Contains(out, []byte("PASS build ok")) {
		t.Errorf("output dropped visible text: %q", out)
	}
}

func TestTerminalCollapsesCarriageReturns(t *testing.T) {
	in := []byte("Downloading\rDownloading 50%\rDownloading 100% done\n")
	out, ok := NewTerminal().Compress(in)
	if !ok {
		t.Fatal("expected carriage-return redraws to collapse")
	}
	if bytes.Contains(out, []byte("50%")) {
		t.Errorf("intermediate progress frame survived: %q", out)
	}
	if !bytes.Contains(out, []byte("Downloading 100% done")) {
		t.Errorf("final progress state lost: %q", out)
	}
	if bytes.ContainsRune(out, '\r') {
		t.Errorf("bare carriage return survived: %q", out)
	}
}

func TestTerminalKeepsSignalElidesMiddle(t *testing.T) {
	var b strings.Builder
	b.WriteString("$ npm test\n")
	b.WriteString("> project@1.0.0 test\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "  \x1b[2m✓ passes assertion %d\x1b[0m\n", i)
	}
	b.WriteString("ERROR: assertion 'auth token expiry' failed\n")
	b.WriteString("  File \"auth.py\", line 42, in check\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "  \x1b[2m✓ passes assertion %d\x1b[0m\n", 40+i)
	}
	b.WriteString("1 failed, 80 passed\n")
	in := []byte(b.String())

	out, ok := NewTerminal().Compress(in)
	if !ok {
		t.Fatal("expected long terminal output to compress")
	}
	if len(out) >= len(in) {
		t.Errorf("compression did not shrink: %d -> %d bytes", len(in), len(out))
	}
	for _, must := range []string{
		"ERROR: assertion 'auth token expiry' failed",
		"File \"auth.py\", line 42, in check",
		"1 failed, 80 passed",
		"lines elided (caveman)",
	} {
		if !bytes.Contains(out, []byte(must)) {
			t.Errorf("output dropped a line it must keep: %q\n--- got ---\n%s", must, out)
		}
	}
	if bytes.ContainsRune(out, 0x1b) {
		t.Errorf("ANSI escapes survived elision: %q", out)
	}
}

func TestTerminalPassesThroughPlainShort(t *testing.T) {
	if _, ok := NewTerminal().Compress([]byte("hello\nworld\n")); ok {
		t.Error("plain short output with nothing to strip or elide must pass through (ok=false)")
	}
	if _, ok := NewTerminal().Compress(nil); ok {
		t.Error("empty input must pass through (ok=false)")
	}
}

func TestTerminalIsIdempotent(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, "\x1b[36mINFO\x1b[0m step %d ok\n", i)
	}
	b.WriteString("ERROR: boom\n")
	first, ok := NewTerminal().Compress([]byte(b.String()))
	if !ok {
		t.Fatal("first pass should compress")
	}
	if _, ok := NewTerminal().Compress(first); ok {
		t.Errorf("second pass must be a no-op (ok=false), got further change:\n%s", first)
	}
}
