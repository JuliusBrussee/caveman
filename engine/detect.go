package engine

import (
	"bytes"
	"encoding/json"
	"regexp"

	"github.com/JuliusBrussee/caveman/engine/compressors"
)

// Content type names returned by Detect and used as registry keys.
const (
	TypeJSON         = "json"
	TypeLog          = "log"
	TypeCode         = "code"
	TypeDiff         = "diff"
	TypeSearchResult = "search-result"
	TypeText         = "text"
	TypeTOON         = "toon"
	TypeHTML         = "html"
	TypeA11y         = "a11y"
	TypeTerminal     = "terminal"
	TypeTabular      = "tabular"
	TypeConfig       = "config"
)

var (
	// Log lines carry a level token or a timestamp.
	logLineRe    = regexp.MustCompile(`(?i)(\b(TRACE|DEBUG|INFO|WARN|WARNING|ERROR|FATAL|PANIC)\b|^\s*\[[A-Z]+\]|\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2}|\b\d{2}:\d{2}:\d{2}\b)`)
	diffLineRe   = regexp.MustCompile(`(?m)^(diff --git |@@ |--- |\+\+\+ |[+-][^+-])`)
	searchLineRe = regexp.MustCompile(`(?m)^([./~A-Za-z0-9_-][^:\n]{0,240}:\d+(:\d+)?:|https?://\S+)`)
	// Strong source-code signals (word-boundary keywords across common langs).
	codeKeywordRe = regexp.MustCompile(`\b(func|package|import|def|class|function|return|const|let|var|public|private|protected|static|void|struct|interface|namespace|module|fn|impl|trait|export|async|await)\b`)
	codeSymbolRe  = regexp.MustCompile(`(=>|::|->|\){|\) \{|;\n|^\s*#include|^\s*from .+ import )`)
	// A raw ANSI/CSI escape is the conclusive terminal signal — nothing but
	// terminal/command output legitimately embeds one.
	ansiEscRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
)

// Detect classifies a payload into a content type. It is deterministic and
// fail-open: anything it is not confident about is "text", which routes to a
// conservative compressor. The order is strict-JSON, diff, code, log, search.
func (e *Engine) Detect(input []byte) string {
	// Agent file tags and read-tool line-number gutters are presentation, not
	// content. Classify what the file is, not how the agent printed it.
	input, _ = unwrapInput(input)
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) == 0 {
		return TypeText
	}
	if (trimmed[0] == '{' || trimmed[0] == '[') && json.Valid(trimmed) {
		return TypeJSON
	}
	// Terminal output is detected before diff/code/log because its ANSI-escape
	// signal is conclusive: only raw command/terminal output carries it, so this
	// can never steal log/code/json traffic. Low confidence falls open.
	if looksLikeTerminal(input) {
		return TypeTerminal
	}
	if looksLikeDiff(input) {
		return TypeDiff
	}
	if looksLikeHTML(input) {
		return TypeHTML
	}
	if compressors.LooksTabular(input) {
		return TypeTabular
	}
	if looksLikeCode(input) {
		return TypeCode
	}
	if looksLikeLog(input) {
		return TypeLog
	}
	if looksLikeSearchResult(input) {
		return TypeSearchResult
	}
	if compressors.LooksConfig(input) {
		return TypeConfig
	}
	return TypeText
}

// looksLikeTerminal detects raw terminal/command output deterministically. A raw
// ANSI escape sequence is conclusive. A dense run of bare carriage returns (a
// progress bar redrawing in place) is the secondary signal; CRLF line endings are
// excluded so ordinary \r\n text never trips it.
func looksLikeTerminal(input []byte) bool {
	if ansiEscRe.Match(input) {
		return true
	}
	bareCR := bytes.Count(input, []byte("\r")) - bytes.Count(input, []byte("\r\n"))
	return bareCR >= 3
}

func looksLikeDiff(input []byte) bool {
	matches := len(diffLineRe.FindAll(input, -1))
	if matches < 4 {
		return false
	}
	return bytes.Contains(input, []byte("\n@@ ")) ||
		bytes.Contains(input, []byte("diff --git ")) ||
		(bytes.Contains(input, []byte("\n--- ")) && bytes.Contains(input, []byte("\n+++ ")))
}

func looksLikeCode(input []byte) bool {
	// A payload dominated by log level/timestamp lines is a log, even when its
	// messages contain code keywords like "return" or "class" — route it to the
	// log compressor, not the code one. This is the highest-value misroute fix.
	if looksLikeLog(input) {
		return false
	}
	if bytes.HasPrefix(bytes.TrimSpace(input), []byte("#!")) {
		return true
	}
	keywords := len(codeKeywordRe.FindAll(input, -1))
	symbols := len(codeSymbolRe.FindAll(input, -1))
	// A structural signal distinguishes code from prose that happens to use a
	// keyword: brace-delimited blocks, a Python def/class suite, or an
	// indented block.
	structural := symbols >= 1 ||
		bytes.Contains(input, []byte("{")) ||
		(bytes.Contains(input, []byte("def ")) && bytes.Contains(input, []byte(":"))) ||
		bytes.Contains(input, []byte("\n    "))
	// Require several distinct code signals so prose with one stray "class" or
	// "return" does not misroute.
	return keywords >= 3 && structural
}

// looksLikeHTML detects HTML documents/fragments deterministically. A document
// declaration is conclusive; otherwise it requires a structural container plus
// enough tag density — and the payload must NOT look like source code, so that
// JSX/TSX components and source files that merely embed markup string literals
// route to the code compressor (which understands them) instead of being
// text-extracted into garbage. Low confidence falls open to the next detector.
func looksLikeHTML(input []byte) bool {
	s := bytes.ToLower(bytes.TrimSpace(input))
	// A document declaration is conclusive — source code never starts this way.
	if bytes.HasPrefix(s, []byte("<!doctype html")) || bytes.HasPrefix(s, []byte("<html")) {
		return true
	}
	// Fragments: bail if it's source code embedding markup (JSX, HTML literals).
	if hasCodeStructure(input) {
		return false
	}
	hasContainer := bytes.Contains(s, []byte("<body")) ||
		bytes.Contains(s, []byte("<article")) ||
		bytes.Contains(s, []byte("<main")) ||
		bytes.Contains(s, []byte("<div")) ||
		bytes.Contains(s, []byte("<p>")) || bytes.Contains(s, []byte("<p "))
	return hasContainer && bytes.Count(s, []byte("<")) >= 6
}

// hasCodeStructure reports whether the payload carries source-code syntax that an
// HTML document would not — JSX/TS arrow functions, React className, module
// import/export, or a file beginning with a language declaration or comment. It
// keeps JSX/TSX and markup-bearing source out of the HTML branch (they route to
// the code compressor). A document declaration is checked before this in
// looksLikeHTML, so real HTML pages (even with inline <script>) are unaffected.
func hasCodeStructure(input []byte) bool {
	if bytes.Contains(input, []byte("=>")) ||
		bytes.Contains(input, []byte("className=")) ||
		bytes.Contains(input, []byte("});")) {
		return true
	}
	tl := bytes.TrimSpace(input)
	for _, p := range []string{"import ", "export ", "package ", "func ", "def ", "const ", "#include", "#!", "//", "/*"} {
		if bytes.HasPrefix(tl, []byte(p)) {
			return true
		}
	}
	for _, p := range []string{"\nimport ", "\nexport ", "\npackage ", "\nfunc ", "\ndef ", "\nclass ", "\nconst "} {
		if bytes.Contains(input, []byte(p)) {
			return true
		}
	}
	return false
}

func looksLikeLog(input []byte) bool {
	lines := bytes.Split(input, []byte("\n"))
	if len(lines) < 4 {
		return false
	}
	matched := 0
	for _, ln := range lines {
		if logLineRe.Match(ln) {
			matched++
		}
	}
	// A log is dominated by level/timestamp lines.
	return matched >= 3 && matched*2 >= len(lines)
}

func looksLikeSearchResult(input []byte) bool {
	lines := bytes.Split(input, []byte("\n"))
	if len(lines) < 6 {
		return false
	}
	matched := 0
	for _, ln := range lines {
		if searchLineRe.Match(ln) {
			matched++
		}
	}
	return matched >= 4 && matched*3 >= len(lines)
}
