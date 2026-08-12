package compressors_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/compressors"
)

func show2(b []byte) string {
	s := string(b)
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\n", "\\n\n")
	return s
}

// A run of 3+ IDENTICAL marker-string lines in the ORIGINAL input.
// The compressor collapses them and emits its own marker. On a second pass
// (or visually) this is indistinguishable from a real elision.
func TestProbe2_RunOfMarkerLines(t *testing.T) {
	c := compressors.NewRepetition()
	marker := "… caveman: 7 identical lines elided …"
	in := []byte(strings.Repeat(marker+"\n", 5) + strings.Repeat("realdistinct padding line content here yes ok\n", 4))
	out, ok := c.Compress(in)
	t.Logf("ok=%v", ok)
	if ok {
		t.Logf("OUTPUT:\n%s", show2(out))
		// The original had 5 identical "elided 7" lines. Output keeps one + marker "4 elided".
		// A reader/recovery now sees marker "7" then marker "4". Confusing but CCR recovers exact bytes.
		out2, ok2 := c.Compress(out)
		t.Logf("pass2 ok=%v equal=%v", ok2, bytes.Equal(out, out2))
		if ok2 {
			t.Logf("OUTPUT2:\n%s", show2(out2))
		}
	}
}

// Idempotence stress: feed output that STILL contains a collapsible run because
// the marker insertion created adjacency. Construct: A A A B A A A where after
// collapse we get A <m> B A <m> ... can two markers ever become adjacent+identical?
func TestProbe2_AdjacentMarkersAfterCollapse(t *testing.T) {
	c := compressors.NewRepetition()
	// Two separate runs of the SAME line, separated by one differing line that is ALSO
	// going to vanish? It can't vanish. But: runN-1 == runM-1 makes identical markers.
	// A x4 , B(single) , A x4  -> A <m:3> B A <m:3>. Markers identical but not adjacent.
	// Now what if B is removed? It won't be. Try: A x4, A x4 is just one run.
	// Force identical adjacent markers: run of A (count c1) then run of C (count c2) with c1==c2
	// and the kept lines A and C differ, markers identical "… caveman: N …" but separated by C.
	line := "the same repeated payload line goes right here ok\n"
	other := "different content sitting between the two runs here\n"
	in := []byte(strings.Repeat(line, 4) + other + strings.Repeat(line, 4) + "tail padding distinct line for bytes here\n")
	out, ok := c.Compress(in)
	t.Logf("ok=%v", ok)
	if ok {
		t.Logf("OUTPUT:\n%s", show2(out))
		out2, ok2 := c.Compress(out)
		t.Logf("pass2 ok=%v equal=%v", ok2, bytes.Equal(out, out2))
		if ok2 {
			t.Logf("OUTPUT2:\n%s", show2(out2))
		}
	}
}

// TRUE minimal false-win in BYTES: minRun=3 of a 1-char line.
// kept: "x" (1) + "\n" + marker(~45 bytes). replaced 3 lines "x\nx\nx" = 5 bytes.
// So locally the marker is far bigger than what it replaced. Whole-input may still
// shrink if there are MANY such lines, but a SINGLE qualifying short run GROWS bytes.
// The compressor returns ok=true regardless; only the engine's TOKEN guard catches it.
// Question: can a whole input be byte-LARGER yet token-SMALLER, or vice versa, falsely claimed?
func TestProbe2_SingleShortRunGrowsWholeInput(t *testing.T) {
	c := compressors.NewRepetition()
	// One short run of 3, everything else unique, padded over minBytes.
	pad := strings.Repeat("unique padding line number to clear the byte floor here\n", 4)
	in := []byte(pad + "y\ny\ny\n" + "z final unique\n")
	out, ok := c.Compress(in)
	t.Logf("ok=%v inlen=%d outlen=%d (delta %d)", ok, len(in), len(out), len(out)-len(in))
	if ok && len(out) >= len(in) {
		t.Errorf("COMPRESSOR CLAIMED ok=true BUT OUTPUT IS NOT SMALLER IN BYTES (%d->%d)", len(in), len(out))
		t.Logf("OUTPUT:\n%s", show2(out))
	}
}

// The existing idempotence test only checks the case where pass2 reports ok.
// Verify the marker line itself never gets collapsed when repeated 3x via a real elision
// producing 3 adjacent identical markers. Can that happen? Only if 3 separate runs of the
// same line with the same (runLen-1) are adjacent with the kept line dropped — impossible
// since kept lines sit between markers. Confirm structurally.
func TestProbe2_MarkerNeverFormsCollapsibleRun(t *testing.T) {
	c := compressors.NewRepetition()
	A := "aaaa repeated payload one here padding\n"
	B := "bbbb repeated payload two here padding\n"
	C := "cccc repeated payload three here padding\n"
	// three runs, each of length 4, same runLen-1=3 => three identical markers,
	// each separated by the kept line of the NEXT run. So markers are never adjacent.
	in := []byte(strings.Repeat(A, 4) + strings.Repeat(B, 4) + strings.Repeat(C, 4) + "tail\n")
	out, ok := c.Compress(in)
	if ok {
		t.Logf("OUTPUT:\n%s", show2(out))
		t.Logf("adjacent-identical-marker count: %d", bytes.Count(out, []byte("… caveman: 3 identical")))
	}
}
