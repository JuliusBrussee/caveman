package compressors_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/compressors"
)

func TestRepetitionCollapsesIdenticalRuns(t *testing.T) {
	c := compressors.NewRepetition()
	var b strings.Builder
	b.WriteString("starting build\n")
	for i := 0; i < 40; i++ {
		b.WriteString("downloading dependency... please wait\n")
	}
	b.WriteString("ERROR: undefined symbol caveman_main\n")
	b.WriteString("build failed\n")
	in := []byte(b.String())

	out, ok := c.Compress(in)
	if !ok {
		t.Fatal("expected collapse of the repeated run")
	}
	if len(out) >= len(in) {
		t.Error("expected output smaller than input")
	}
	// The unique error line and the surrounding context must survive.
	for _, must := range []string{"ERROR: undefined symbol caveman_main", "starting build", "build failed"} {
		if !bytes.Contains(out, []byte(must)) {
			t.Errorf("unique line dropped: %q", must)
		}
	}
	// The repeated line appears exactly once, plus an elision marker.
	if n := bytes.Count(out, []byte("downloading dependency")); n != 1 {
		t.Errorf("repeated line should remain once, found %d", n)
	}
	if !bytes.Contains(out, []byte("caveman:")) {
		t.Error("expected an elision marker")
	}
}

func TestRepetitionIdempotent(t *testing.T) {
	c := compressors.NewRepetition()
	in := []byte("head line\n" + strings.Repeat("same same same line\n", 30) + "tail line\n")
	first, ok := c.Compress(in)
	if !ok {
		t.Fatal("expected collapse")
	}
	second, ok := c.Compress(first)
	if ok {
		// A second pass that still reports a collapse must be byte-stable.
		if !bytes.Equal(first, second) {
			t.Errorf("not idempotent:\n first=%s\nsecond=%s", first, second)
		}
		return
	}
	// More commonly the second pass finds nothing to collapse → pass-through.
}

func TestRepetitionNoDuplicatesPassesThrough(t *testing.T) {
	c := compressors.NewRepetition()
	in := []byte("line one is unique\nline two is unique\nline three is unique\nand a fourth distinct line here\nplus a fifth one too for good measure\n")
	if _, ok := c.Compress(in); ok {
		t.Error("input with no repeated runs must pass through (ok=false)")
	}
}

func TestRepetitionTinyInputPassesThrough(t *testing.T) {
	c := compressors.NewRepetition()
	if _, ok := c.Compress([]byte("x\nx\nx\n")); ok {
		t.Error("tiny input must pass through (ok=false)")
	}
}

func TestRepetitionDeterministic(t *testing.T) {
	c := compressors.NewRepetition()
	in := []byte("alpha\n" + strings.Repeat("repeat me\n", 25) + "beta\n" + strings.Repeat("and me too\n", 25) + "gamma\n")
	a, ok1 := c.Compress(in)
	b, ok2 := c.Compress(in)
	if !ok1 || !ok2 {
		t.Fatal("expected collapse")
	}
	if !bytes.Equal(a, b) {
		t.Error("non-deterministic output")
	}
}

func TestRepetitionShortRunNotCollapsed(t *testing.T) {
	c := compressors.NewRepetition()
	// A run of 2 (< minRun=3) must not be collapsed, but the input must be large
	// enough to clear minBytes so the no-collapse path (not the size guard) is hit.
	in := []byte(strings.Repeat("a unique padding line that is reasonably long\n", 10) + "dup line\ndup line\nfinal unique line\n")
	out, ok := c.Compress(in)
	if ok && bytes.Contains(out, []byte("caveman:")) {
		// If it collapsed at all, the 2-line run must NOT be what produced a marker.
		if bytes.Count(out, []byte("dup line")) != 2 {
			t.Error("a run of 2 identical lines must not be collapsed")
		}
	}
}
