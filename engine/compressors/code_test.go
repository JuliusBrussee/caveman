package compressors_test

import (
	"bytes"
	"go/parser"
	"go/token"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/compressors"
)

func codeCompressor(t *testing.T) compressors.Compressor {
	t.Helper()
	c, ok := compressors.Default().For("code")
	if !ok {
		t.Fatal("no code compressor registered")
	}
	return c
}

const goSource = `package demo

import "fmt"

// Add sums two ints.
func Add(a, b int) int {
	total := a + b
	fmt.Println(total)
	return total
}

func Mul(a, b int) int {
	result := 0
	for i := 0; i < b; i++ {
		result += a
	}
	return result
}
`

func TestCodeCompressorGoElidesBodiesKeepsSignatures(t *testing.T) {
	c := codeCompressor(t)
	out, ok := c.Compress([]byte(goSource))
	if !ok {
		t.Fatal("expected Go to compress")
	}
	if len(out) >= len(goSource) {
		t.Error("expected the source to shrink")
	}
	// Signatures and imports survive.
	for _, want := range []string{"func Add(a, b int) int", "func Mul(a, b int) int", `import "fmt"`} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("output must keep %q:\n%s", want, out)
		}
	}
	// Body detail is gone.
	if bytes.Contains(out, []byte("total := a + b")) {
		t.Error("function body should be elided")
	}
	// Output must still parse as valid Go.
	if _, err := parser.ParseFile(token.NewFileSet(), "", out, 0); err != nil {
		t.Errorf("compressed Go must re-parse: %v\n%s", err, out)
	}
}

func TestCodeCompressorGoIdempotent(t *testing.T) {
	c := codeCompressor(t)
	first, ok := c.Compress([]byte(goSource))
	if !ok {
		t.Fatal("expected compression")
	}
	if second, ok := c.Compress(first); ok && !bytes.Equal(first, second) {
		t.Errorf("not idempotent:\n first=%s\nsecond=%s", first, second)
	}
}

func TestCodeCompressorMalformedPassesThrough(t *testing.T) {
	c := codeCompressor(t)
	if _, ok := c.Compress([]byte("this is not source code at all, just words")); ok {
		t.Error("non-code must pass through (!ok)")
	}
	if _, ok := c.Compress([]byte("package x\nfunc broken( {\n")); ok {
		t.Error("unparseable Go must pass through (!ok)")
	}
}
