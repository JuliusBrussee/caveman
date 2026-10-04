package compressors

import (
	"bytes"
	"regexp"
	"strings"
)

// GDScript (Godot) has no tree-sitter grammar in this module, and the pure-Go
// build has no grammar at all, so its function bodies are elided by a line
// scanner instead of a parser. That is safe enough because GDScript is
// indentation-scoped: a function's body is exactly the run of lines after its
// `func name(...):` header that are indented deeper than the header (or blank),
// up to the first line that is not. Only whole body lines are removed and one
// placeholder line is inserted, so every annotation, signal, declaration, and
// signature survives byte for byte, and the file stays valid GDScript. Both
// builds route GDScript here before any grammar sniffing. It is S4 (lossy); the
// original is recoverable via CCR.

// gdElision is valid GDScript, so an elided file still parses in the editor,
// and it is recognized on re-entry so the transform is idempotent.
const gdElision = "pass # caveman: body elided"

// gdMaxHeaderLines bounds how far a parameter list may run before the header is
// given up on and the function left untouched.
const gdMaxHeaderLines = 16

var (
	// gdFuncStartRe matches the start of a named function header, with any
	// same-line annotations (`@rpc("any_peer") func ping():`) in front. Lambdas
	// (`func(x):`) carry no name and are never elided on their own; a lambda
	// inside a body goes with the body.
	gdFuncStartRe = regexp.MustCompile(`^([ \t]*)(?:@[A-Za-z_]+(?:\([^)]*\))?[ \t]+)*(?:static[ \t]+)?func[ \t]+[A-Za-z_][A-Za-z0-9_]*[ \t]*\(`)
	// gdMarkerRe matches lines only GDScript has. One of them plus one complete
	// function header is the detection bar.
	gdMarkerRe = regexp.MustCompile(`(?m)^[ \t]*(?:extends[ \t]+[A-Za-z_"']|class_name[ \t]+[A-Za-z_]|@tool\b|@export|@onready\b|@icon\(|signal[ \t]+[A-Za-z_])`)
	// gdForeignRe matches lines that belong to the languages GDScript is most
	// easily confused with: Go (`package`), Python (`def`, `import`, `from`),
	// and TypeScript (`import`). GDScript has none of these statements.
	gdForeignRe = regexp.MustCompile(`(?m)^[ \t]*(?:package[ \t]+[A-Za-z_]|def[ \t]+[A-Za-z_][A-Za-z0-9_]*[ \t]*\(|import[ \t]+[A-Za-z_({"']|from[ \t]+[A-Za-z_.]+[ \t]+import[ \t])`)
)

// isGDScript reports whether input is a GDScript file: it carries a marker line
// only GDScript has, no statement from a neighbouring language, and at least one
// complete `func name(...):` header on a single line.
func isGDScript(input []byte) bool {
	if !bytes.Contains(input, []byte("func ")) || !gdMarkerRe.Match(input) || gdForeignRe.Match(input) {
		return false
	}
	for _, line := range bytes.Split(input, []byte("\n")) {
		if !gdFuncStartRe.Match(line) {
			continue
		}
		if _, ok := gdHeaderColon(string(line)); ok {
			return true
		}
	}
	return false
}

// compressGDScript elides every named function body and reports whether
// anything changed. Headers may span several lines (a wrapped parameter list);
// a function whose body sits on the header line (`func f(): return 1`) is left
// alone, as is a body that is only `pass` or already elided. Lines inside
// triple-quoted strings are never treated as headers or as body boundaries.
func compressGDScript(input []byte) ([]byte, bool) {
	lines := strings.Split(string(input), "\n")
	out := make([]string, 0, len(lines))
	changed := false
	inString := false
	for i := 0; i < len(lines); {
		line := lines[i]
		m := gdFuncStartRe.FindStringSubmatch(line)
		if inString || m == nil {
			out = append(out, line)
			inString = gdToggleTripleQuote(line, inString)
			i++
			continue
		}
		indent := m[1]
		headerEnd, colon, joined, ok := gdHeaderSpan(lines, i)
		if !ok || strings.TrimSpace(gdStripComment(joined[colon+1:])) != "" {
			// Incomplete header, or a one-line body on the header line: keep.
			out = append(out, line)
			inString = gdToggleTripleQuote(line, inString)
			i++
			continue
		}
		bodyStart := headerEnd + 1
		bodyEnd := gdBodyEnd(lines, bodyStart, indent)
		out = append(out, lines[i:headerEnd+1]...)
		body := lines[bodyStart:bodyEnd]
		if first := gdFirstNonBlank(body); first < 0 || gdNothingToElide(body) {
			out = append(out, body...)
		} else {
			out = append(out, gdLeadingWhitespace(body[first])+gdElision)
			changed = true
		}
		i = bodyEnd
	}
	if !changed {
		return nil, false
	}
	return []byte(strings.Join(out, "\n")), true
}

// gdHeaderSpan joins lines from start until the header's closing colon is found,
// returning the index of the last header line, the colon's offset in the joined
// text, and the joined text. A header longer than gdMaxHeaderLines is not one.
func gdHeaderSpan(lines []string, start int) (end, colon int, joined string, ok bool) {
	var b strings.Builder
	for j := start; j < len(lines) && j < start+gdMaxHeaderLines; j++ {
		if j > start {
			b.WriteByte('\n')
		}
		b.WriteString(lines[j])
		if c, found := gdHeaderColon(b.String()); found {
			return j, c, b.String(), true
		}
	}
	return 0, 0, "", false
}

// gdHeaderColon finds the colon that closes a function header: the one after the
// balanced parameter list and the optional `-> Type`. Quotes and `#` comments
// inside the parameter list are skipped. It reports false when the text ends
// before that colon, which for a header means it continues on the next line.
func gdHeaderColon(text string) (int, bool) {
	loc := gdFuncStartRe.FindStringIndex(text)
	if loc == nil {
		return 0, false
	}
	// The match ends just past the parameter list's opening parenthesis, so a
	// parenthesised annotation in front of `func` is skipped.
	closeParen, ok := gdMatchParen(text, loc[1]-1)
	if !ok {
		return 0, false
	}
	i := closeParen + 1
	for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	if strings.HasPrefix(text[i:], "->") {
		k := strings.IndexByte(text[i:], ':')
		if k < 0 {
			return 0, false
		}
		return i + k, true
	}
	if i < len(text) && text[i] == ':' {
		return i, true
	}
	return 0, false
}

// gdMatchParen returns the index of the parenthesis closing the one at open,
// ignoring brackets inside string literals and `#` comments.
func gdMatchParen(text string, open int) (int, bool) {
	depth := 0
	var quote byte
	for i := open; i < len(text); i++ {
		c := text[i]
		if quote != 0 {
			switch c {
			case '\\':
				i++
			case quote:
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case '#':
			nl := strings.IndexByte(text[i:], '\n')
			if nl < 0 {
				return 0, false
			}
			i += nl
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				return i, c == ')'
			}
		}
	}
	return 0, false
}

// gdBodyEnd returns the index of the first line after the body that starts at
// bodyStart: the first non-blank line not indented deeper than indent, with
// trailing blank lines handed back so the spacing after the function survives.
// Lines inside a triple-quoted string belong to the body whatever their indent.
func gdBodyEnd(lines []string, bodyStart int, indent string) int {
	k := bodyStart
	inString := false
	for k < len(lines) {
		line := lines[k]
		if !inString && strings.TrimSpace(line) != "" && !gdDeeper(line, indent) {
			break
		}
		inString = gdToggleTripleQuote(line, inString)
		k++
	}
	for k > bodyStart && strings.TrimSpace(lines[k-1]) == "" {
		k--
	}
	return k
}

// gdDeeper reports whether line is indented deeper than indent.
func gdDeeper(line, indent string) bool {
	if !strings.HasPrefix(line, indent) || len(line) == len(indent) {
		return false
	}
	c := line[len(indent)]
	return c == ' ' || c == '\t'
}

// gdNothingToElide reports whether a body is a single `pass` or an elision
// placeholder, so eliding it would gain nothing and the transform stays
// idempotent.
func gdNothingToElide(body []string) bool {
	var only string
	count := 0
	for _, line := range body {
		if t := strings.TrimSpace(line); t != "" {
			only = t
			count++
		}
	}
	return count == 1 && (only == "pass" || only == gdElision)
}

func gdFirstNonBlank(lines []string) int {
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			return i
		}
	}
	return -1
}

func gdLeadingWhitespace(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

// gdStripComment drops a trailing `#` comment that sits outside quotes.
func gdStripComment(s string) string {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			switch c {
			case '\\':
				i++
			case quote:
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case '#':
			return s[:i]
		}
	}
	return s
}

// gdToggleTripleQuote returns the multi-line string state after line: every
// `"""` or `”'` on the line flips it.
func gdToggleTripleQuote(line string, in bool) bool {
	for i := 0; i+3 <= len(line); {
		if line[i:i+3] == `"""` || line[i:i+3] == "'''" {
			in = !in
			i += 3
			continue
		}
		i++
	}
	return in
}
