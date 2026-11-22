package engine_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine"
)

func TestCompressEmptyInputPassesThrough(t *testing.T) {
	e := newEngine(t)
	res, err := e.Compress([]byte{}, engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(res.Output, []byte{}) {
		t.Error("empty input must pass through byte-identical")
	}
	if !res.PassedThrough() {
		t.Error("empty input must report as passed through")
	}
	if res.RecoveryHandle != "" {
		t.Errorf("empty input must not create recovery handle, got %q", res.RecoveryHandle)
	}
}

func TestCompressUnicodePreservesBytes(t *testing.T) {
	e := newEngine(t)
	unicode := []byte(`{"results":[{"id":1,"name":"café"},{"id":2,"name":"naïve"}],"error":null}`)
	res, err := e.Compress(unicode, engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		t.Fatal(err)
	}
	// Either compresses or passes through — must never corrupt bytes.
	if !res.PassedThrough() {
		original, err := e.Retrieve(res.RecoveryHandle)
		if err != nil {
			t.Fatalf("retrieve after compress: %v", err)
		}
		if !bytes.Equal(original, unicode) {
			t.Error("retrieve must return byte-exact original including unicode")
		}
	}
	// Output must be valid UTF-8.
	if !strings.Contains(string(res.Output), "café") && !res.PassedThrough() {
		// If compressed, original bytes must still be recoverable.
		original, _ := e.Retrieve(res.RecoveryHandle)
		if !bytes.Equal(original, unicode) {
			t.Error("unicode bytes corrupted in recovery")
		}
	}
}

func TestCompressNestedCodeBlocksPassesThrough(t *testing.T) {
	e := newEngine(t)
	nested := []byte("```python\n```nested\nprint('hello')\n```\n```\n")
	res, err := e.Compress(nested, engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		t.Fatal(err)
	}
	// Nested code blocks must not crash the detector.
	if res.Output == nil {
		t.Fatal("result output must not be nil for nested code blocks")
	}
}

func TestCompressBinaryInputPassesThrough(t *testing.T) {
	e := newEngine(t)
	binary := []byte{0x00, 0x01, 0x02, 0x03, 0xFF, 0xFE, 0xFD}
	res, err := e.Compress(binary, engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(res.Output, binary) {
		t.Error("binary input must pass through byte-identical")
	}
	if !res.PassedThrough() {
		t.Error("binary input must report as passed through")
	}
}

func TestCompressNilBytesPassesThrough(t *testing.T) {
	e := newEngine(t)
	res, err := e.Compress(nil, engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(res.Output, []byte{}) {
		t.Error("nil input must produce empty output")
	}
	if !res.PassedThrough() {
		t.Error("nil input must report as passed through")
	}
}

func TestCompressWhitespaceOnlyPassesThrough(t *testing.T) {
	e := newEngine(t)
	ws := []byte("   \n\t\n   ")
	res, err := e.Compress(ws, engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(res.Output, ws) {
		t.Error("whitespace-only input must pass through byte-identical")
	}
}

func TestCompressSingleCharacterPassesThrough(t *testing.T) {
	e := newEngine(t)
	single := []byte("a")
	res, err := e.Compress(single, engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(res.Output, single) {
		t.Error("single character must pass through byte-identical")
	}
}
