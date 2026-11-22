//go:build cgo

package compressors

import (
	"bytes"
	"context"
	"regexp"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/c"
	"github.com/smacker/go-tree-sitter/cpp"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/rust"
	"github.com/smacker/go-tree-sitter/typescript/tsx"
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

const (
	goSyntheticPackagePrefix = "package _\n"
	minPartialSliceBytes     = 512
)

var (
	goPackageRe = regexp.MustCompile(`(?m)^[ \t]*package[ \t]+[A-Za-z_][A-Za-z0-9_]*`)
	goFuncRe    = regexp.MustCompile(`(?m)^[ \t]*func[ \t]+(?:\([^)\n]+\)[ \t]*)?[A-Za-z_][A-Za-z0-9_]*[ \t]*\(`)
)

func (c *codeCompressor) Compress(input []byte) ([]byte, bool) {
	if isGDScript(input) {
		return compressGDScript(input) // no grammar for it; see code_gdscript.go
	}
	l := sniffLanguage(input)
	if l == nil {
		return nil, false // unsupported language → pass-through
	}
	parseSrc := input
	prefixShift := uint32(0)
	if l.name == "go" && !goPackageRe.Match(input) {
		// Mid-file Go slices (`Read` with offset, `sed -n`) omit the top-level
		// `package` declaration that tree-sitter's Go grammar requires. Parse
		// with a synthetic package line and map replacement spans back onto the
		// caller's original slice.
		parseSrc = append([]byte(goSyntheticPackagePrefix), input...)
		prefixShift = uint32(len(goSyntheticPackagePrefix))
	}

	root, tree, missing, errNodes, ok := parseAccepted(l, parseSrc, len(input) >= minPartialSliceBytes)
	if !ok && l.name == "ts" {
		// JSX is not TypeScript: a React component fails the TypeScript grammar
		// at its first `<div>`. The TSX grammar is a superset, so retry with it.
		l = &lang{name: "tsx", language: tsx.GetLanguage(), braceBody: true}
		root, tree, missing, errNodes, ok = parseAccepted(l, parseSrc, len(input) >= minPartialSliceBytes)
	}
	if !ok {
		return nil, false
	}
	defer tree.Close()

	var repls []replacement
	elision := pyElision
	if l.braceBody {
		elision = braceElision
	}
	collectBodies(root, parseSrc, elision, &repls)
	if c.opts.Comments == CommentRemove || c.opts.Docstrings == CommentRemove {
		collectComments(root, parseSrc, c.opts, &repls)
	}
	if len(repls) == 0 {
		return nil, false
	}
	if prefixShift > 0 {
		shifted := repls[:0]
		for _, r := range repls {
			if r.start < prefixShift {
				continue
			}
			shifted = append(shifted, replacement{
				start: r.start - prefixShift,
				end:   r.end - prefixShift,
				text:  r.text,
			})
		}
		repls = shifted
		if len(repls) == 0 {
			return nil, false
		}
	}
	out := applyReplacements(input, repls)

	verifySrc := out
	if prefixShift > 0 {
		verifySrc = append([]byte(goSyntheticPackagePrefix), out...)
	}

	// Byte-safe guarantee: the result must re-parse at least as cleanly,
	// introducing zero new ERROR or MISSING nodes.
	_, vtree, vmissing, verrNodes, ok := parseAccepted(l, verifySrc, errNodes > 0)
	if !ok {
		return nil, false
	}
	vtree.Close()
	if vmissing > missing || verrNodes > errNodes {
		return nil, false
	}
	return out, true
}

// parseAccepted parses src with l and reports whether the tree may be edited:
// normally zero ERROR nodes and only tolerable MISSING tokens (`;` or an
// identifier). When allowPartial is true (multi-line slices >= 512 bytes such
// as `Read(offset, limit)`, `sed -n`, or 32 KB-truncated files), bounded parse
// errors (< 25% of input bytes) are tolerated so every complete, error-free
// function in the slice still compresses while incomplete boundary fragments
// stay untouched.
func parseAccepted(l *lang, src []byte, allowPartial bool) (*sitter.Node, *sitter.Tree, int, int, bool) {
	root, tree, ok := parse(l, src)
	if !ok {
		return nil, nil, 0, 0, false
	}
	if !root.HasError() {
		return root, tree, 0, 0, true
	}
	missing, tolerable := recoveredTokens(root)
	if tolerable {
		return root, tree, missing, 0, true
	}
	if !allowPartial {
		tree.Close()
		return nil, nil, 0, 0, false
	}
	errNodes, errBytes, missingCount := errorStats(root)
	if errNodes == 0 || errBytes*4 >= len(src) {
		tree.Close()
		return nil, nil, 0, 0, false
	}
	return root, tree, missingCount, errNodes, true
}

func errorStats(n *sitter.Node) (errNodes, errBytes, missing int) {
	if n.IsError() {
		return 1, int(n.EndByte() - n.StartByte()), 0
	}
	if n.IsMissing() {
		return 0, 0, 1
	}
	if !n.HasError() {
		return 0, 0, 0
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		en, eb, m := errorStats(n.Child(i))
		errNodes += en
		errBytes += eb
		missing += m
	}
	return errNodes, errBytes, missing
}

// recoveredTokens walks a tree that has errors and reports how many MISSING
// nodes it holds and whether every error in it is such a node of a tolerable
// kind. Subtrees without errors are not entered.
func recoveredTokens(n *sitter.Node) (missing int, tolerable bool) {
	if n.IsError() {
		return 0, false
	}
	if n.IsMissing() {
		t := n.Type()
		if t == ";" || strings.HasSuffix(t, "identifier") {
			return 1, true
		}
		return 0, false
	}
	if !n.HasError() {
		return 0, true
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		m, ok := recoveredTokens(n.Child(i))
		if !ok {
			return 0, false
		}
		missing += m
	}
	return missing, true
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

// collectBodies records the byte range of each outermost error-free function
// body. It never touches an ERROR subtree or an incomplete function at a slice
// boundary, and does not descend into a body it is about to elide so nested
// closures are covered by the enclosing replacement and ranges never overlap.
func collectBodies(n *sitter.Node, src []byte, elision string, out *[]replacement) {
	if n.IsError() {
		return
	}
	if isFuncLike(n.Type()) && !n.HasError() {
		if body := n.ChildByFieldName("body"); body != nil && isBlock(body.Type()) && !body.HasError() {
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

// pyDefRe is the Python signal: a `def name(` that starts a line. A bare
// "def " substring also matches `#ifdef`, `#ifndef` and `typedef`, which sent
// guarded C and C++ files to the Python grammar.
var pyDefRe = regexp.MustCompile(`(?m)^[ \t]*(?:async[ \t]+)?def[ \t]+[A-Za-z_][A-Za-z0-9_]*[ \t]*\(`)

// sniffLanguage picks a tree-sitter grammar from cheap byte signals. Order
// matters: the broad TypeScript arm (class/const/function) is last so it cannot
// swallow Java/C/C++/Rust. A mis-sniff is self-correcting — the wrong grammar
// yields a parse error or a re-parse failure, which falls back to pass-through.
func sniffLanguage(input []byte) *lang {
	has := func(s string) bool { return bytes.Contains(input, []byte(s)) }
	switch {
	case (has("package ") && has("func ")) ||
		(goFuncRe.Match(input) && (has(":=") || has("err != nil") || has(") error {") || has("func ("))):
		return &lang{name: "go", language: golang.GetLanguage(), braceBody: true}
	case pyDefRe.Match(input) && has(":"):
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
	case has("#include") || (has("static inline ") && has("{") && has(";")) ||
		((has("#ifndef ") || has("#ifdef ")) && has("#define ") && has("struct ") && has(";")):
		// C++ (with its stronger signals) is already handled above; any remaining
		// #include / C-header payload is treated as C.
		return &lang{name: "c", language: c.GetLanguage(), braceBody: true}
	case has("function ") || has("=>") || has("const ") || has("let ") || has("interface ") || has("class "):
		// The TypeScript grammar is a superset of JavaScript, so it parses both.
		return &lang{name: "ts", language: typescript.GetLanguage(), braceBody: true}
	}
	return nil
}
