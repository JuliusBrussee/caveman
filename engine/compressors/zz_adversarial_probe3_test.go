package compressors_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/compressors"
	"github.com/JuliusBrussee/caveman/engine/tokens"
)

func show3(b []byte) string {
	s := string(b)
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\n", "\\n\n")
	return s
}

// Precise false-win probe: truly DISTINCT padding (no accidental collapse),
// one short qualifying run of a tiny line. Does ok=true but output grow?
func TestProbe3_PreciseByteFalseWin(t *testing.T) {
	c := compressors.NewRepetition()
	var b strings.Builder
	for i := 0; i < 6; i++ {
		b.WriteString(fmt.Sprintf("unique padding line number %d distinct content here\n", i))
	}
	b.WriteString("q\nq\nq\n") // run of 3 of a 1-byte line
	b.WriteString("final distinct tail line goes here yes ok\n")
	in := []byte(b.String())
	out, ok := c.Compress(in)
	t.Logf("ok=%v inlen=%d outlen=%d delta=%d", ok, len(in), len(out), len(out)-len(in))
	if ok {
		t.Logf("OUTPUT:\n%s", show3(out))
		// Now measure TOKENS the way the engine does (o200k_base default counter).
		ctr := tokens.Default()
		tin := ctr.Count(in)
		tout := ctr.Count(out)
		t.Logf("TOKENS before=%d after=%d  (engine emits only if after<before)", tin, tout)
		if len(out) >= len(in) {
			t.Logf(">>> BYTE FALSE-WIN: compressor returned ok=true but bytes GREW (%d->%d). Engine's TOKEN guard is the only thing that may reject it.", len(in), len(out))
		}
		if tout >= tin {
			t.Logf(">>> TOKEN: engine would REJECT this (after>=before) -> pass-through. Good, but compressor itself lied with ok=true.")
		} else {
			t.Logf(">>> TOKEN: engine would ACCEPT (after<before).")
		}
	}
}

// Idempotence corruption: input legitimately has >=3 identical lines that ARE the marker.
// They collapse -> the emitted marker count is WRONG relative to the literal marker text,
// and the literal "7 identical" content is partly elided. CCR recovers exact bytes, but
// the COMPRESSED view shown to the model now has nonsensical/conflicting markers.
func TestProbe3_MarkerRunCollapsesIntoWrongCount(t *testing.T) {
	c := compressors.NewRepetition()
	marker := "… caveman: 999 identical lines elided …"
	// 5 identical real-looking marker lines in the ORIGINAL (e.g. a log that quotes caveman output)
	in := []byte("header distinct line for padding the input here ok\n" +
		strings.Repeat(marker+"\n", 5) +
		"trailer distinct line for padding the input here ok\n" +
		strings.Repeat("more distinct padding content to clear min bytes\n", 3))
	out, ok := c.Compress(in)
	t.Logf("ok=%v", ok)
	if ok {
		t.Logf("OUTPUT:\n%s", show3(out))
		t.Logf(">>> The model now sees marker '999' immediately followed by marker '4' — self-contradicting. (CCR still recovers exact bytes.)")
	}
}
