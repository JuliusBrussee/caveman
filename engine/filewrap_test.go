package engine

import (
	"bytes"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/ccr"
)

func TestUnwrapFileWrapper(t *testing.T) {
	goSrc := []byte("package main\n\nfunc add(a, b int) int {\n\treturn a + b\n}\n")
	wrapped := []byte(`<file name="C:\x\main.go">` + "\n" + string(goSrc) + "\n</file>\n")

	inner, w := unwrapFileWrapper(wrapped)
	if !w.present {
		t.Fatal("wrapper not detected")
	}
	if !bytes.Equal(bytes.TrimSpace(inner), bytes.TrimSpace(goSrc)) {
		t.Fatalf("inner mismatch:\n%s", inner)
	}
	rewrapped := w.rewrap([]byte("compressed"))
	if !bytes.HasPrefix(rewrapped, []byte(`<file name="C:\x\main.go">`)) || !bytes.HasSuffix(rewrapped, []byte("</file>")) {
		t.Fatalf("rewrap lost wrapper: %q", rewrapped)
	}

	// Non-wrapper inputs must pass through untouched.
	for _, in := range [][]byte{
		[]byte("plain text"),
		[]byte("package main\nfunc f() {}\n"),
		[]byte(`<html><body>x</body></html>`),
		[]byte(`<file>` + "\n" + string(goSrc) + "\n</file>\n<file>second</file>"),
		[]byte(`<file name="a">`),
		[]byte(`</file>`),
		[]byte(`<files>x</files>`),
	} {
		got, ww := unwrapFileWrapper(in)
		if ww.present {
			t.Fatalf("false positive wrapper on %q", in)
		}
		if !bytes.Equal(got, in) {
			t.Fatalf("no-op path changed bytes: %q", in)
		}
	}
}

func TestCompressFileWrappedGo(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	defer store.Close()
	eng := New(store, nil)
	input := []byte(`<file name="main.go">
package main

import "fmt"

func handler000(w *Widget) string {
	body := fmt.Sprintf("widget %s costs %.2f", w.Name, w.Price)
	if w.Price > 100.0 {
		body += " (expensive)"
	}
	return fmt.Sprintf("000: %s", body)
}

func handler001(w *Widget) string {
	body := fmt.Sprintf("widget %s costs %.2f", w.Name, w.Price)
	if w.Price > 100.0 {
		body += " (expensive)"
	}
	return fmt.Sprintf("001: %s", body)
}
</file>`)
	res, err := eng.Compress(input, Options{Mode: ModeCompress})
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	if res.TokensAfter >= res.TokensBefore {
		t.Fatalf("wrapped Go did not compress: before=%d after=%d", res.TokensBefore, res.TokensAfter)
	}
	if !bytes.HasPrefix(res.Output, []byte("<file")) || !bytes.HasSuffix(res.Output, []byte("</file>")) {
		t.Fatalf("compressed output lost wrapper: %q", res.Output[:60])
	}
	if !bytes.Contains(res.Output, []byte("func handler000")) || !bytes.Contains(res.Output, []byte("func handler001")) {
		t.Fatalf("compressed output lost signatures: %q", res.Output)
	}
	if res.RecoveryHandle == "" {
		t.Fatal("no recovery handle minted")
	}
	got, err := eng.Retrieve(res.RecoveryHandle)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if !bytes.Equal(got, input) {
		t.Fatal("retrieved bytes differ from wrapped original")
	}
}
