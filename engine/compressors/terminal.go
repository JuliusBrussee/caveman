package compressors

import (
	"bytes"
	"fmt"
	"regexp"
	"unicode/utf8"

	"github.com/JuliusBrussee/caveman/engine/safety"
)

// TerminalType is the content type for terminal/command-output compression. It is
// auto-selected by Detect when a payload carries raw ANSI escapes (see
// engine/detect.go) and is forced by `caveman shrink`, which wraps a command and
// shrinks its output before a model reads it.
const TerminalType = "terminal"

var (
	// ansiRe matches ANSI/VT control sequences: CSI (ESC [ … final byte), OSC
	// (ESC ] … BEL/ST), and the standalone two-byte Fe escapes. These are
	// display-only noise a model never needs; the original — escapes and all —
	// stays byte-exact in CCR, so stripping them is recoverable, not destructive.
	ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\x5c-_]`)
	// termImportantRe keeps lines that carry signal: errors, failures, warnings,
	// stack-trace frames, compiler error locations, and non-zero exits. These are
	// never elided no matter where they sit in the output. The `[A-Za-z]+Error` /
	// `[A-Za-z]+Exception` alternatives catch camelCase types (AssertionError,
	// ValueError, NullPointerException) that a bare `\bERROR\b` would miss — the
	// most common way a real failure hides from naive head/tail sampling.
	termImportantRe = regexp.MustCompile(`(?i)(\b(ERROR|ERR|FATAL|PANIC|EXCEPTION|TRACEBACK|FAIL|FAILED|FAILURE|WARN|WARNING)\b|[A-Za-z]+Error\b|[A-Za-z]+Exception\b|^\s+at\s|^\s+File "|\.go:\d+|:\d+:\d+:|^\s*--->|caused by|\bexit status [1-9])`)
	// termMarkerRe recognizes our own elision marker so a second pass never
	// collapses it again — the compressor stays idempotent.
	termMarkerRe = regexp.MustCompile(`lines elided \(caveman\)`)
)

func termMarker(n int) string { return fmt.Sprintf("… %d lines elided (caveman) …", n) }

// terminalCompressor shrinks raw terminal/command output before a model reads it:
// it strips ANSI control sequences, collapses carriage-return progress redraws to
// their final visible state, and elides the boring middle while keeping the head,
// the tail, and every error/traceback/warning line verbatim. It is S4 (lossy):
// the byte-exact original — ANSI, progress frames, and all — is recoverable via
// CCR, so this is the recoverable counterpart to a crude shell rewriter. It is the
// engine primitive behind `caveman shrink`. Deterministic, idempotent, and
// fail-closed: on any anomaly, or when it cannot actually shrink the payload, it
// returns ok=false and the caller forwards the original bytes unchanged.
type terminalCompressor struct {
	keepHead int
	keepTail int
}

// NewTerminal returns the default terminal/command-output compressor.
func NewTerminal() Compressor { return &terminalCompressor{keepHead: 3, keepTail: 3} }

func (c *terminalCompressor) ContentType() string       { return TerminalType }
func (c *terminalCompressor) SafetyClass() safety.Class { return safety.S4 }

func (c *terminalCompressor) Compress(input []byte) ([]byte, bool) {
	if len(input) == 0 || !utf8.Valid(input) {
		return nil, false // empty or binary-ish content → pass-through
	}

	// Normalize CRLF to LF for analysis; the file's separator is restored on
	// output so we never silently rewrite line endings.
	crlf := bytes.Contains(input, []byte("\r\n"))
	work := input
	if crlf {
		work = bytes.ReplaceAll(work, []byte("\r\n"), []byte("\n"))
	}
	work = collapseCarriageReturns(work) // intra-line \r redraws → final state
	work = ansiRe.ReplaceAll(work, nil)  // strip display control sequences

	trailing := bytes.HasSuffix(work, []byte("\n"))
	body := work
	if trailing {
		body = body[:len(body)-1]
	}
	lines := bytes.Split(body, []byte("\n"))

	out := lines // < 4 lines: ANSI/\r cleanup alone may still have shrunk it
	if len(lines) >= 4 {
		out = elideMiddle(lines, c.keepHead, c.keepTail)
	}

	result := bytes.Join(out, []byte("\n"))
	if trailing {
		result = append(result, '\n')
	}
	if crlf {
		result = bytes.ReplaceAll(result, []byte("\n"), []byte("\r\n"))
	}
	if bytes.Equal(result, input) {
		return nil, false // cleanup and elision changed nothing → claim nothing
	}
	return result, true
}

// collapseCarriageReturns rewrites each physical line to its final redraw state.
// A bare \r rewinds the cursor to the start of the line, so a progress bar that
// reprints "10%\r20%\r100% done" was only ever showing "100% done" — the earlier
// frames are visual history, not content. It keeps the last non-empty \r segment.
func collapseCarriageReturns(b []byte) []byte {
	if !bytes.Contains(b, []byte("\r")) {
		return b
	}
	lines := bytes.Split(b, []byte("\n"))
	for i, ln := range lines {
		if !bytes.Contains(ln, []byte("\r")) {
			continue
		}
		chosen := []byte(nil)
		for _, seg := range bytes.Split(ln, []byte("\r")) {
			if len(seg) > 0 {
				chosen = seg
			}
		}
		lines[i] = chosen
	}
	return bytes.Join(lines, []byte("\n"))
}

// elideMiddle keeps the head, the tail, and every signal-bearing line, collapsing
// each run of dropped lines into a single marker. It mirrors the log compressor's
// proven head/tail/importance pass so terminal output and build logs elide the
// same way once the terminal-specific ANSI/\r noise is gone.
func elideMiddle(lines [][]byte, head, tail int) [][]byte {
	keep := make([]bool, len(lines))
	for i, ln := range lines {
		if i < head || i >= len(lines)-tail || termImportantRe.Match(ln) || termMarkerRe.Match(ln) {
			keep[i] = true
		}
	}
	keepNonRedundant(lines, keep)
	out := make([][]byte, 0, len(lines))
	dropped := 0
	flush := func() {
		if dropped > 0 {
			out = append(out, []byte(termMarker(dropped)))
			dropped = 0
		}
	}
	for i, ln := range lines {
		if keep[i] {
			flush()
			out = append(out, ln)
			continue
		}
		dropped++
	}
	flush()
	return out
}
