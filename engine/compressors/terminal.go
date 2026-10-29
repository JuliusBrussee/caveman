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

	// Clean each line independently, detaching that line's own CR first so a CRLF
	// break is never mistaken for an intra-line redraw. Re-attaching it per line
	// means a document with mixed endings keeps every one of them; the old code
	// normalized the whole buffer and re-terminated it from a single
	// `Contains("\r\n")` flag, so one embedded CRLF (what `curl -i` emits, or a
	// Windows tool's output inside a Unix CI log) rewrote every line in the file.
	lines, trailing := splitLines(input)
	cr := crSuffix(input)
	for i, ln := range lines {
		lineCR := bytes.HasSuffix(ln, []byte("\r"))
		if lineCR {
			ln = ln[:len(ln)-1]
		}
		ln = collapseCarriageReturns(ln) // intra-line \r redraws → final state
		ln = ansiRe.ReplaceAll(ln, nil)  // strip display control sequences
		if lineCR {
			ln = append(ln[:len(ln):len(ln)], '\r') // cap==len: append copies, never writes into input
		}
		lines[i] = ln
	}

	out := lines // < 4 lines: ANSI/\r cleanup alone may still have shrunk it
	if len(lines) >= 4 {
		out = elideMiddle(lines, c.keepHead, c.keepTail, cr)
	}

	result := joinLines(out, trailing)
	if bytes.Equal(result, input) {
		return nil, false // cleanup and elision changed nothing → claim nothing
	}
	return result, true
}

// collapseCarriageReturns rewrites a physical line to its final redraw state.
// A bare \r rewinds the cursor to the start of the line, so a progress bar that
// reprints "10%\r20%\r100% done" was only ever showing "100% done" — the earlier
// frames are visual history, not content.
//
// But a \r is only a redraw when the frames it overwrote said the same thing. The
// same byte terminates lines in a classic-Mac document and turns up as a paste
// artifact in ordinary text, and there the overwritten segments ARE the content:
// keeping only the last non-empty one collapsed an 84-byte CR-terminated report
// to its signature line and rewrote "Hello \rWorld" to "World", both with ok=true
// and no marker (#1294). Detect now withholds the bare-CR classification from
// LF-less payloads, but an ANSI escape is conclusive for it and `caveman shrink`
// forces this compressor outright, so the guard has to live here.
//
// The decision is ALL OR NOTHING, per line, and that is the load-bearing part. A
// first cut dropped the segments keepNonRedundant judged redundant and kept the
// rest, which is the #1294 bug in miniature: "Report\rTotal: 10\rTotal: 20\r
// Signature" lost `Total: 20` on its own, because digit masking makes two
// numbered records one class, and a line has nowhere to put an elision marker
// saying so. So either every overwritten segment is redundant — a real redraw,
// collapsed to the final frame, which is also what a terminal displays — or the
// carriage returns are carrying content and the line is returned untouched. Then
// Compress's not-smaller check passes the payload through and claims nothing.
//
// Segments are judged by what the user SAW: Compress strips ANSI only after this
// runs, so comparing the raw bytes let a frame that merely gained a color read as
// new content and kept the stale frame beside it.
func collapseCarriageReturns(b []byte) []byte {
	if !bytes.Contains(b, []byte("\r")) {
		return b
	}
	segs := bytes.Split(b, []byte("\r"))
	visible := make([][]byte, len(segs))
	last := -1
	for i, seg := range segs {
		visible[i] = ansiRe.ReplaceAll(seg, nil)
		if len(visible[i]) > 0 {
			last = i
		}
	}
	if last < 0 {
		return b // nothing visible; no final state to choose
	}
	// Seed the gate with the frame left on screen and let the earlier ones elide
	// against what the user actually ended up looking at.
	keep := make([]bool, len(segs))
	keep[last] = true
	keepNonRedundant(visible, keep)
	for i := range segs {
		if keep[i] && i != last {
			return b // an overwritten segment carries content of its own
		}
	}
	return segs[last]
}

// elideMiddle keeps the head, the tail, and every signal-bearing line, collapsing
// each run of dropped lines into a single marker. It mirrors the log compressor's
// proven head/tail/importance pass so terminal output and build logs elide the
// same way once the terminal-specific ANSI/\r noise is gone.
func elideMiddle(lines [][]byte, head, tail int, cr []byte) [][]byte {
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
			out = append(out, synthLine(termMarker(dropped), cr))
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
