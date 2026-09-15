package engine_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
)

func TestDetectTerminal(t *testing.T) {
	e := engine.New(nil, nil)
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"ansi colors", "\x1b[32mPASS\x1b[0m all green\nbuilding...\ndone\n", engine.TypeTerminal},
		{"progress redraws", "fetch 10%\rfetch 40%\rfetch 80%\rfetch done\n", engine.TypeTerminal},
		{"plain prose stays text", "The quick brown fox jumps over the lazy dog.", engine.TypeText},
		{"crlf text not terminal", "line one\r\nline two\r\nline three\r\n", engine.TypeText},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := e.Detect([]byte(c.in)); got != c.want {
				t.Errorf("Detect(%s) = %q, want %q", c.name, got, c.want)
			}
		})
	}
}

// TestTerminalCompressRoundTrip checks the recoverable-RTK contract end to end:
// noisy command output is auto-detected as terminal, compressed (lossy, S4), and
// the byte-exact original is restored from the disclosed CCR handle.
func TestTerminalCompressRoundTrip(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := engine.New(store, nil)

	var b strings.Builder
	b.WriteString("$ pytest -q\n")
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "\x1b[32m.\x1b[0m collected test_module_%d ok\n", i)
	}
	b.WriteString("E   AssertionError: token expiry uses < not <=\n")
	b.WriteString("auth/token.py:42: AssertionError\n")
	b.WriteString("1 failed, 60 passed in 2.10s\n")
	in := []byte(b.String())

	res, err := e.Compress(in, engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		t.Fatal(err)
	}
	if res.ContentType != engine.TypeTerminal {
		t.Errorf("content type = %q, want terminal", res.ContentType)
	}
	if res.PassedThrough() {
		t.Fatal("expected noisy terminal output to compress")
	}
	if res.Ratio <= 0 || res.TokensAfter >= res.TokensBefore {
		t.Errorf("expected a positive ratio, got %v (%d->%d)", res.Ratio, res.TokensBefore, res.TokensAfter)
	}
	if res.Basis != engine.BasisInferred {
		t.Errorf("basis = %q, want inferred", res.Basis)
	}
	original, err := e.Retrieve(res.RecoveryHandle)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if !bytes.Equal(original, in) {
		t.Error("retrieve must return the byte-exact original (ANSI escapes and all)")
	}
}
