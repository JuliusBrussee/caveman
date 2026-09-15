package compressors_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/compressors"
)

func show(b []byte) string {
	s := string(b)
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\n", "\\n\n")
	return s
}

// ATTACK 1: CRLF (Windows build/log output) — does \r break run equality / output?
func TestProbe_CRLF(t *testing.T) {
	c := compressors.NewRepetition()
	// 30 identical lines, CRLF terminated, plus padding to clear minBytes.
	head := "starting build the windows way here padding padding\r\n"
	dup := "downloading dependency please wait a moment longer now\r\n"
	in := []byte(head + strings.Repeat(dup, 30) + "ERROR boom\r\n")
	t.Logf("input bytes=%d", len(in))
	out, ok := c.Compress(in)
	t.Logf("ok=%v outlen=%d", ok, len(out))
	if ok {
		t.Logf("OUTPUT:\n%s", show(out))
		// Does the kept line still carry its \r? Does the marker land cleanly?
		t.Logf("dup count in out=%d", bytes.Count(out, []byte("downloading dependency")))
		t.Logf("has marker=%v", bytes.Contains(out, []byte("caveman:")))
	} else {
		t.Logf("CRLF input: ok=false (no collapse). show first 200 bytes:\n%s", show(in[:200]))
	}
}

// ATTACK 2: idempotence when INPUT contains lines that look like the marker, repeated.
func TestProbe_MarkerLooklike(t *testing.T) {
	c := compressors.NewRepetition()
	marker := "… caveman: 5 identical lines elided …"
	in := []byte(strings.Repeat(marker+"\n", 10) + strings.Repeat("padding line to clear min bytes here\n", 6))
	out, ok := c.Compress(in)
	t.Logf("ok=%v", ok)
	if ok {
		t.Logf("OUTPUT:\n%s", show(out))
		// Now compress again - is it stable?
		out2, ok2 := c.Compress(out)
		t.Logf("second pass ok=%v stable=%v", ok2, bytes.Equal(out, out2))
		if ok2 {
			t.Logf("OUTPUT2:\n%s", show(out2))
		}
	}
}

// ATTACK 3: false-win — marker longer than the lines it replaces (bytes).
func TestProbe_MarkerBytesLargerThanReplaced(t *testing.T) {
	c := compressors.NewRepetition()
	// short repeated line "x", run of exactly minRun=3, padded to clear minBytes.
	in := []byte(strings.Repeat("padding distinct line number to clear the min bytes guard\n", 5) + "x\nx\nx\n" + "tail distinct line for good measure here ok\n")
	t.Logf("input bytes=%d", len(in))
	out, ok := c.Compress(in)
	t.Logf("ok=%v inlen=%d outlen=%d delta=%d", ok, len(in), len(out), len(out)-len(in))
	if ok && len(out) > len(in) {
		t.Logf("BYTE-LARGER OUTPUT (compressor claimed a win but grew bytes):\n%s", show(out))
	}
}

// ATTACK 4: trailing newline / no trailing newline round-trip structure.
func TestProbe_TrailingNewline(t *testing.T) {
	c := compressors.NewRepetition()
	base := "head padding line long enough to matter here yes\n" + strings.Repeat("dupdupdup line repeated many times over here\n", 10)
	withNL := []byte(base + "tail\n")
	noNL := []byte(base + "tail")
	for _, tc := range []struct {
		name string
		in   []byte
	}{{"withNL", withNL}, {"noNL", noNL}} {
		out, ok := c.Compress(tc.in)
		t.Logf("[%s] ok=%v inLastByte=%q outLastByte=%q", tc.name, ok, lastByte(tc.in), lastByte(out))
	}
}

func lastByte(b []byte) string {
	if len(b) == 0 {
		return "<empty>"
	}
	return fmt.Sprintf("%q", b[len(b)-1])
}

// ATTACK 5: blank-line run must NOT collapse; whitespace-only run.
func TestProbe_BlankAndWhitespaceRuns(t *testing.T) {
	c := compressors.NewRepetition()
	pad := strings.Repeat("padding line content here that is distinct enough x\n", 5)
	// run of 10 blank lines
	blanks := []byte(pad + strings.Repeat("\n", 10) + "tail line here ok done\n")
	out, ok := c.Compress(blanks)
	t.Logf("blank-run ok=%v (must be... depends: no other run, so ok=false expected) outlen=%d inlen=%d", ok, len(out), len(blanks))
	// run of 10 whitespace-only (spaces) lines: TrimSpace empty => not collapsed
	ws := []byte(pad + strings.Repeat("   \n", 10) + "tail line here ok done\n")
	out2, ok2 := c.Compress(ws)
	t.Logf("whitespace-run ok=%v outlen=%d inlen=%d", ok2, len(out2), len(ws))
	if ok2 {
		t.Logf("WS OUTPUT (did whitespace-only run collapse?):\n%s", show(out2))
	}
}

// ATTACK 6: marker collision count — what does recall look like for a real CRLF log fixture sized run?
func TestProbe_CRLF_smaller_guard(t *testing.T) {
	c := compressors.NewRepetition()
	// CRLF run where the kept-line+marker may not be smaller because each line keeps \r
	dup := "retry\r\n"
	in := []byte(strings.Repeat("xpadding distinct AAAA line goes here to clear bytes\r\n", 4) + strings.Repeat(dup, 50) + "end\r\n")
	out, ok := c.Compress(in)
	t.Logf("ok=%v inlen=%d outlen=%d", ok, len(in), len(out))
	if ok {
		// Critical: is the kept 'retry' line followed by its \r and then marker, i.e. does \r survive on kept line?
		idx := bytes.Index(out, []byte("retry"))
		if idx >= 0 && idx+7 < len(out) {
			t.Logf("bytes around kept retry: %q", out[idx:idx+30])
		}
	}
}
