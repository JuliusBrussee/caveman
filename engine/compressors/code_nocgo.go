//go:build !cgo

package compressors

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"

	"github.com/JuliusBrussee/caveman/engine/safety"
)

// codeCompressor is the pure-Go (cgo-free / WASM) fallback. Without tree-sitter
// it compresses Go only — via the stdlib go/ast — by emptying function bodies
// while keeping every import, signature, and type declaration. Other languages
// pass through unchanged. It is S4 (lossy); the original is recoverable via CCR.
type codeCompressor struct{ opts CodeOptions }

func newCode() Compressor { return &codeCompressor{} }

// newCodeWith accepts options for build-tag symmetry with the cgo compressor.
// The pure-Go fallback only elides Go function bodies; comment/docstring modes
// are a no-op here (they require the tree-sitter build).
func newCodeWith(opts CodeOptions) Compressor { return &codeCompressor{opts: opts} }

func (c *codeCompressor) ContentType() string       { return "code" }
func (c *codeCompressor) SafetyClass() safety.Class { return safety.S4 }

func (c *codeCompressor) Compress(input []byte) ([]byte, bool) {
	if !bytes.Contains(input, []byte("package ")) || !bytes.Contains(input, []byte("func ")) {
		return nil, false // the pure-Go fallback only handles Go
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", input, parser.SkipObjectResolution)
	if err != nil {
		return nil, false // malformed → pass-through
	}
	changed := false
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		if len(fn.Body.List) == 0 {
			return true // already empty → idempotent
		}
		fn.Body.List = nil
		changed = true
		return true
	})
	if !changed {
		return nil, false
	}
	var buf bytes.Buffer
	if err := (&printer.Config{Mode: printer.TabIndent, Tabwidth: 8}).Fprint(&buf, fset, file); err != nil {
		return nil, false
	}
	// gofmt the printed result to collapse the blank lines left where bodies
	// were. If gofmt cannot parse it back, the printed bytes are still valid Go.
	if formatted, err := format.Source(buf.Bytes()); err == nil {
		return formatted, true
	}
	return buf.Bytes(), true
}
