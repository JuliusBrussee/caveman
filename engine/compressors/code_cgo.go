//go:build cgo

package compressors

import (
	"bytes"
	"context"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/c"
	"github.com/smacker/go-tree-sitter/cpp"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/rust"
	"github.com/smacker/go-tree-sitter/typescript/typescript"

	"github.com/JuliusBrussee/caveman/engine/safety"
)

// Elided function bodies. Braces keep Go/JS/TS/Rust/Java/C/C++ syntax valid;
// "..." keeps Python valid. Both are detected on re-entry so the compressor is
// idempotent.
const (
	braceElision = "{ /* caveman: body elided */ }"
	pyElision    = "..."
)

type lang struct {
	name      string
	language  *sitter.Language
	braceBody bool // bodies are brace-delimited ({...}) vs Python suites
}

// codeCompressor keeps imports, signatures, and type/class declarations and
// elides function/method bodies using a tree-sitter parse, so the structural
// outline survives at a fraction of the tokens. With CommentRemove options it
// also elides comment / Python-docstring nodes. It is S4 (lossy); the original
// is recoverable via CCR. On any parse problem — including output that does not
// cleanly re-parse — it reports !ok and the caller forwards the bytes unchanged.
type codeCompressor struct{ opts CodeOptions }

// newCode returns the code compressor with default options (bodies elided,
// comments/docstrings kept) — the historical behavior.
func newCode() Compressor { return &codeCompressor{} }

// newCodeWith constructs the code compressor with explicit options (used by
// callers/tests that want comment or docstring removal).
func newCodeWith(opts CodeOptions) Compressor { return &codeCompressor{opts: opts} }

func (c *codeCompressor) ContentType() string       { return "code" }
func (c *codeCompressor) SafetyClass() safety.Class { return safety.S4 }

func (c *codeCompressor) Compress(input []byte) ([]byte, bool) {
	l := sniffLanguage(input)
	if l == nil {
		return nil, false // unsupported language → pass-through
	}
	root, tree, ok := parse(l, input)
	if !ok {
		return nil, false
	}
	defer tree.Close()
	if root.HasError() {
		return nil, false // never edit code we cannot cleanly parse
	}

	var repls []replacement
	elision := pyElision
	if l.braceBody {
		elision = braceElision
	}
	collectBodies(root, input, elision, &repls)
	if c.opts.Comments == CommentRemove || c.opts.Docstrings == CommentRemove {
		collectComments(root, input, c.opts, &repls)
	}
	if len(repls) == 0 {
		return nil, false
	}
	out := applyReplacements(input, repls)

	// Byte-safe guarantee: the result must re-parse without error.
	vroot, vtree, ok := parse(l, out)
	if !ok {
		return nil, false
	}
	defer vtree.Close()
	if vroot.HasError() {
		return nil, false
	}
	return out, true
}

func parse(l *lang, src []byte) (*sitter.Node, *sitter.Tree, bool) {
	p := sitter.NewParser()
	p.SetLanguage(l.language)
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil || tree == nil {
		return nil, nil, false
	}
	root := tree.RootNode()
	if root == nil {
		tree.Close()
		return nil, nil, false
	}
	return root, tree, true
}

type replacement struct {
	start, end uint32
	text       string
}

func isFuncLike(t string) bool {
	switch t {
	case "function_declaration", "method_declaration", "func_literal", // Go
		"function_definition",                                  // Python / C / C++
		"method_definition", "function", "function_expression", // JS/TS
		"generator_function_declaration", "generator_function", "arrow_function",
		"function_item",           // Rust
		"constructor_declaration": // Java constructors (methods are method_declaration)
		return true
	}
	return false
}

func isBlock(t string) bool {
	switch t {
	case "block", "statement_block", // Go / Python / JS / TS / Rust / Java methods
		"constructor_body",   // Java constructors
		"compound_statement", // C / C++
		"try_statement":      // C++ function-try-block bodies
		return true
	}
	return false
}

// collectBodies records the byte range of each outermost function body. It does
// not descend into a body it is about to elide, so nested closures are covered
// by the enclosing replacement and ranges never overlap.
func collectBodies(n *sitter.Node, src []byte, elision string, out *[]replacement) {
	if isFuncLike(n.Type()) {
		if body := n.ChildByFieldName("body"); body != nil && isBlock(body.Type()) {
			if strings.TrimSpace(body.Content(src)) != strings.TrimSpace(elision) {
				*out = append(*out, replacement{body.StartByte(), body.EndByte(), elision})
			}
			return
		}
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		collectBodies(n.NamedChild(i), src, elision, out)
	}
}

func isCommentNode(t string) bool {
	return t == "comment" || t == "line_comment" || t == "block_comment"
}

// collectComments records byte ranges to elide for comment nodes (when
// Comments==Remove) and Python module/class docstrings (when Docstrings==Remove).
// A range that lands inside an already-collected body is harmless: applyReplacements
// drops overlapping ranges, and Compress re-parses the result before using it, so
// any removal that would break the parse falls back to pass-through. It walks all
// children (not just named) because comments are tree-sitter "extra" nodes.
func collectComments(n *sitter.Node, src []byte, opts CodeOptions, out *[]replacement) {
	t := n.Type()
	if opts.Comments == CommentRemove && isCommentNode(t) {
		// A directive comment carries machine semantics that survive a re-parse —
		// keep it. Only prose comments are removed.
		if !isDirectiveComment(n.Content(src)) {
			*out = append(*out, replacement{n.StartByte(), n.EndByte(), ""})
		}
		return
	}
	if opts.Docstrings == CommentRemove && isPythonDocstring(n, src) {
		*out = append(*out, replacement{n.StartByte(), n.EndByte(), ""})
		return
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		collectComments(n.Child(i), src, opts, out)
	}
}

// isDirectiveComment reports whether a comment carries machine semantics and so
// must NOT be removed: Go magic comments (//go:build, //go:embed, //go:generate,
// //go:linkname, …), cgo //export, the legacy // +build constraint, and
// TypeScript /// triple-slash directives. Stripping any of these re-parses fine
// but silently changes build/embed/linkage behavior — exactly what the re-parse
// gate cannot catch. When unsure a comment is prose, keep it (the honest zero).
func isDirectiveComment(text string) bool {
	t := strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(t, "//go:"):
		return true
	case strings.HasPrefix(t, "// +build"), strings.HasPrefix(t, "//+build"):
		return true
	case strings.HasPrefix(t, "//export "):
		return true
	case strings.HasPrefix(t, "///"): // TypeScript /// <reference ... />
		return true
	}
	return false
}

// isPythonDocstring reports whether n is a module/class docstring: an
// expression_statement whose sole child is a plain (non-f, non-bytes) string and
// which is the first statement of a module or block with more than one statement
// (so removing it never empties a suite — invalid Python). Docstrings inside a
// function body are already covered by the body elision.
func isPythonDocstring(n *sitter.Node, src []byte) bool {
	if n.Type() != "expression_statement" || n.NamedChildCount() != 1 {
		return false
	}
	child := n.NamedChild(0)
	if child == nil || child.Type() != "string" {
		return false
	}
	// tree-sitter types f-strings and bytes literals as "string" too, but neither
	// is a docstring (CPython's __doc__ is nil for both) and an f-string may have
	// side effects — never remove them. Plain and r/u strings are real docstrings.
	if content := child.Content(src); len(content) > 0 {
		switch content[0] {
		case 'f', 'F', 'b', 'B':
			return false
		}
	}
	parent := n.Parent()
	if parent == nil {
		return false
	}
	if pt := parent.Type(); pt != "module" && pt != "block" {
		return false
	}
	if parent.NamedChildCount() <= 1 {
		return false // sole statement — removing it would empty the suite
	}
	first := parent.NamedChild(0)
	return first != nil && first.StartByte() == n.StartByte()
}

func applyReplacements(src []byte, repls []replacement) []byte {
	sort.Slice(repls, func(i, j int) bool { return repls[i].start < repls[j].start })
	var buf bytes.Buffer
	var pos uint32
	for _, r := range repls {
		if r.start < pos {
			continue // defensive: skip any overlap
		}
		buf.Write(src[pos:r.start])
		buf.WriteString(r.text)
		pos = r.end
	}
	buf.Write(src[pos:])
	return buf.Bytes()
}

// sniffLanguage picks a tree-sitter grammar from cheap byte signals. Order
// matters: the broad TypeScript arm (class/const/function) is last so it cannot
// swallow Java/C/C++/Rust. A mis-sniff is self-correcting — the wrong grammar
// yields a parse error or a re-parse failure, which falls back to pass-through.
func sniffLanguage(input []byte) *lang {
	has := func(s string) bool { return bytes.Contains(input, []byte(s)) }
	switch {
	case has("package ") && has("func "):
		return &lang{name: "go", language: golang.GetLanguage(), braceBody: true}
	case has("def ") && has(":"):
		return &lang{name: "python", language: python.GetLanguage(), braceBody: false}
	case has("fn ") && (has("->") || has("impl ") || has("pub ") || has("let mut ")):
		return &lang{name: "rust", language: rust.GetLanguage(), braceBody: true}
	case has("public class ") || has("public static void main") ||
		(has("import ") && has("class ") && has(";") && !has("#include") &&
			(has("public ") || has("private ") || has("protected "))):
		return &lang{name: "java", language: java.GetLanguage(), braceBody: true}
	case has("#include") && (has("std::") || has("template<") || has("template <") ||
		has("namespace ") || has("::") || has("public:") || has("private:")):
		return &lang{name: "cpp", language: cpp.GetLanguage(), braceBody: true}
	case has("#include"):
		// C++ (with its stronger signals) is already handled above; any remaining
		// #include payload is treated as C.
		return &lang{name: "c", language: c.GetLanguage(), braceBody: true}
	case has("function ") || has("=>") || has("const ") || has("let ") || has("interface ") || has("class "):
		// The TypeScript grammar is a superset of JavaScript, so it parses both.
		return &lang{name: "ts", language: typescript.GetLanguage(), braceBody: true}
	}
	return nil
}
