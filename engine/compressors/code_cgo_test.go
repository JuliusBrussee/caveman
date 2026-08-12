//go:build cgo

package compressors_test

import (
	"bytes"
	"testing"
)

const pySource = `import os

def add(a, b):
    """Add two numbers."""
    total = a + b
    print(total)
    return total

class Calc:
    def mul(self, a, b):
        result = 0
        for _ in range(b):
            result += a
        return result
`

const tsSource = `export interface Point {
  x: number;
  y: number;
}

export function add(a: number, b: number): number {
  const total = a + b;
  console.log(total);
  return total;
}

export function mul(a: number, b: number): number {
  let result = 0;
  for (let i = 0; i < b; i++) {
    result += a;
  }
  return result;
}
`

// With cgo (tree-sitter) the code compressor handles Python and TypeScript too.
// A successful compression implies the output re-parsed without error, because
// the compressor validates the re-parse before returning ok.
func TestCodeCompressorPython(t *testing.T) {
	c := codeCompressor(t)
	out, ok := c.Compress([]byte(pySource))
	if !ok {
		t.Fatal("expected Python to compress")
	}
	if len(out) >= len(pySource) {
		t.Error("expected Python source to shrink")
	}
	for _, want := range []string{"def add(a, b):", "class Calc:", "def mul(self, a, b):", "import os"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("output must keep %q:\n%s", want, out)
		}
	}
	if bytes.Contains(out, []byte("total = a + b")) {
		t.Error("Python body should be elided")
	}
}

func TestCodeCompressorTypeScript(t *testing.T) {
	c := codeCompressor(t)
	out, ok := c.Compress([]byte(tsSource))
	if !ok {
		t.Fatal("expected TypeScript to compress")
	}
	if len(out) >= len(tsSource) {
		t.Error("expected TypeScript source to shrink")
	}
	for _, want := range []string{"export function add(a: number, b: number): number", "interface Point", "x: number"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("output must keep %q:\n%s", want, out)
		}
	}
	if bytes.Contains(out, []byte("console.log(total)")) {
		t.Error("TS body should be elided")
	}
}

func TestCodeCompressorPythonIdempotent(t *testing.T) {
	c := codeCompressor(t)
	first, ok := c.Compress([]byte(pySource))
	if !ok {
		t.Fatal("expected compression")
	}
	if second, ok := c.Compress(first); ok && !bytes.Equal(first, second) {
		t.Errorf("Python compression not idempotent:\n first=%s\nsecond=%s", first, second)
	}
}
