package compressors

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestTerminalStripsANSI(t *testing.T) {
	in := []byte("\x1b[32mPASS\x1b[0m build ok\n")
	out, ok := NewTerminal().Compress(in)
	if !ok {
		t.Fatal("expected ANSI-bearing line to compress")
	}
	if bytes.ContainsRune(out, 0x1b) {
		t.Errorf("output still contains a raw ESC byte: %q", out)
	}
	if bytes.Contains(out, []byte("[32m")) || bytes.Contains(out, []byte("[0m")) {
		t.Errorf("output still contains ANSI artifacts: %q", out)
	}
	if !bytes.Contains(out, []byte("PASS build ok")) {
		t.Errorf("output dropped visible text: %q", out)
	}
}

func TestTerminalCollapsesCarriageReturns(t *testing.T) {
	in := []byte("Downloading\rDownloading 50%\rDownloading 100% done\n")
	out, ok := NewTerminal().Compress(in)
	if !ok {
		t.Fatal("expected carriage-return redraws to collapse")
	}
	if bytes.Contains(out, []byte("50%")) {
		t.Errorf("intermediate progress frame survived: %q", out)
	}
	if !bytes.Contains(out, []byte("Downloading 100% done")) {
		t.Errorf("final progress state lost: %q", out)
	}
	if bytes.ContainsRune(out, '\r') {
		t.Errorf("bare carriage return survived: %q", out)
	}
}

func TestTerminalKeepsSignalElidesMiddle(t *testing.T) {
	var b strings.Builder
	b.WriteString("$ npm test\n")
	b.WriteString("> project@1.0.0 test\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "  \x1b[2m✓ passes assertion %d\x1b[0m\n", i)
	}
	b.WriteString("ERROR: assertion 'auth token expiry' failed\n")
	b.WriteString("  File \"auth.py\", line 42, in check\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "  \x1b[2m✓ passes assertion %d\x1b[0m\n", 40+i)
	}
	b.WriteString("1 failed, 80 passed\n")
	in := []byte(b.String())

	out, ok := NewTerminal().Compress(in)
	if !ok {
		t.Fatal("expected long terminal output to compress")
	}
	if len(out) >= len(in) {
		t.Errorf("compression did not shrink: %d -> %d bytes", len(in), len(out))
	}
	for _, must := range []string{
		"ERROR: assertion 'auth token expiry' failed",
		"File \"auth.py\", line 42, in check",
		"1 failed, 80 passed",
		"lines elided (caveman)",
	} {
		if !bytes.Contains(out, []byte(must)) {
			t.Errorf("output dropped a line it must keep: %q\n--- got ---\n%s", must, out)
		}
	}
	if bytes.ContainsRune(out, 0x1b) {
		t.Errorf("ANSI escapes survived elision: %q", out)
	}
}

func TestTerminalPassesThroughPlainShort(t *testing.T) {
	if _, ok := NewTerminal().Compress([]byte("hello\nworld\n")); ok {
		t.Error("plain short output with nothing to strip or elide must pass through (ok=false)")
	}
	if _, ok := NewTerminal().Compress(nil); ok {
		t.Error("empty input must pass through (ok=false)")
	}
}

func TestTerminalProgressRedrawWithLFAndANSI(t *testing.T) {
	in := []byte("\x1b[34mDownloading\x1b[0m 10%\r\x1b[34mDownloading\x1b[0m 50%\r\x1b[32mDownloading 100% done\x1b[0m\n")
	out, ok := NewTerminal().Compress(in)
	if !ok {
		t.Fatal("expected progress redraw with LF and ANSI to compress")
	}
	if bytes.Contains(out, []byte("10%")) || bytes.Contains(out, []byte("50%")) {
		t.Errorf("intermediate progress frames survived: %q", out)
	}
	if !bytes.Contains(out, []byte("Downloading 100% done")) {
		t.Errorf("final progress state lost: %q", out)
	}
	if bytes.ContainsRune(out, 0x1b) {
		t.Errorf("ANSI escapes survived: %q", out)
	}
}

func TestTerminalANSIColoredOutput(t *testing.T) {
	in := []byte("\x1b[31merror:\x1b[0m something went wrong\n")
	out, ok := NewTerminal().Compress(in)
	if !ok {
		t.Fatal("expected ANSI colored output to compress")
	}
	if bytes.ContainsRune(out, 0x1b) {
		t.Errorf("ANSI escapes survived: %q", out)
	}
	if !bytes.Contains(out, []byte("error: something went wrong")) {
		t.Errorf("text content lost: %q", out)
	}
}

func TestTerminalCRLFDocumentBehavior(t *testing.T) {
	in := []byte("line one\r\nline two\r\nline three\r\n")
	// Short CRLF text with no ANSI, no bare CR redraws, and < 4 lines should pass through.
	if _, ok := NewTerminal().Compress(in); ok {
		t.Error("short CRLF document should pass through (ok=false)")
	}
}

func TestTerminalIsIdempotent(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, "\x1b[36mINFO\x1b[0m step %d ok\n", i)
	}
	b.WriteString("ERROR: boom\n")
	first, ok := NewTerminal().Compress([]byte(b.String()))
	if !ok {
		t.Fatal("first pass should compress")
	}
	if _, ok := NewTerminal().Compress(first); ok {
		t.Errorf("second pass must be a no-op (ok=false), got further change:\n%s", first)
	}
}

// A bare \r is only a redraw when the frame it overwrote said the same thing.
// The same byte terminates lines in a classic-Mac document and shows up as a
// paste artifact in ordinary text; there the overwritten segments are the
// content, not visual history (#1294).

func TestTerminalCRTerminatedDocumentKeepsEveryLine(t *testing.T) {
	// Reachable with no LF and no ANSI whenever the terminal compressor is
	// forced rather than detected — what `caveman shrink` does to the output of
	// the command it wraps.
	in := []byte("Quarterly Report\rRevenue: $12,400\rExpenses: $9,100\rNet: $3,300\rSignatures: CEO, CFO\r")
	out, ok := NewTerminal().Compress(in)
	if ok && !bytes.Equal(out, in) {
		for _, want := range []string{"Quarterly Report", "Revenue: $12,400", "Expenses: $9,100", "Net: $3,300"} {
			if !bytes.Contains(out, []byte(want)) {
				t.Errorf("CR-terminated document lost %q: %q", want, out)
			}
		}
	}
	if ok && bytes.Equal(out, []byte("Signatures: CEO, CFO\r")) {
		t.Errorf("document collapsed to its last line with ok=true and no marker: %q", out)
	}
}

func TestTerminalCRTerminatedDocumentWithANSIKeepsEveryLine(t *testing.T) {
	// An ANSI escape is conclusive for Detect, so this routes to the terminal
	// compressor on the auto-detect path, not just the forced one.
	in := []byte("\x1b[32mQuarterly Report\r\x1b[0mRevenue: $12,400\rExpenses: $9,100\rNet: $3,300\rSignatures: CEO, CFO\r")
	out, ok := NewTerminal().Compress(in)
	if !ok {
		return // pass-through loses nothing
	}
	for _, want := range []string{"Quarterly Report", "Revenue: $12,400", "Expenses: $9,100", "Net: $3,300", "Signatures: CEO, CFO"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("CR-terminated ANSI document lost %q: %q", want, out)
		}
	}
}

func TestTerminalIntraLineCRKeepsDistinctSegments(t *testing.T) {
	// Three bare CRs alongside LFs is enough for Detect to classify this as
	// terminal output; every line then got truncated at its last CR.
	in := []byte("Hello \rWorld\nAlpha \rBeta\nGamma \rDelta\nkeep me\n")
	out, ok := NewTerminal().Compress(in)
	if !ok {
		return // pass-through loses nothing
	}
	for _, want := range []string{"Hello", "World", "Alpha", "Beta", "Gamma", "Delta", "keep me"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("distinct CR segment %q dropped silently: %q", want, out)
		}
	}
}

func TestTerminalProgressRedrawStillCollapsesWithoutLF(t *testing.T) {
	// The guard above must not cost the compressor its reason to exist: frames
	// that restate the same thing are still visual history.
	in := []byte("Downloading  10%\rDownloading  20%\rDownloading 100% done\r")
	out, ok := NewTerminal().Compress(in)
	if !ok {
		t.Fatal("expected a genuine progress redraw to collapse")
	}
	for _, gone := range []string{"10%", "20%"} {
		if bytes.Contains(out, []byte(gone)) {
			t.Errorf("intermediate progress frame %q survived: %q", gone, out)
		}
	}
	if !bytes.Contains(out, []byte("Downloading 100% done")) {
		t.Errorf("final progress state lost: %q", out)
	}
}

// The collapse decision is all or nothing per line. Dropping only the segments
// the redundancy gate judges redundant reintroduced #1294 at a smaller scale:
// digit masking makes two numbered records one class, and a line has nowhere to
// put a marker saying one went missing.
func TestTerminalCRNumberedRecordsAllSurvive(t *testing.T) {
	in := []byte("Report\rTotal: 10\rTotal: 20\rSignature\r")
	out, ok := NewTerminal().Compress(in)
	if !ok {
		return // pass-through loses nothing
	}
	for _, want := range []string{"Total: 10", "Total: 20"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("numbered record %q dropped with no marker: %q", want, out)
		}
	}
}

// Compress strips ANSI only after collapsing, so the segments have to be
// compared by their visible text. Judging the raw bytes let a frame that merely
// gained a color read as new content and kept the stale frame beside it.
func TestTerminalColoredFinalFrameCollapses(t *testing.T) {
	in := []byte("Downloading 10%\r\x1b[32mDownloading 100%\x1b[0m\n")
	out, ok := NewTerminal().Compress(in)
	if !ok {
		t.Fatal("expected a recolored progress redraw to collapse")
	}
	if bytes.Contains(out, []byte("10%")) {
		t.Errorf("stale progress frame survived a recolored redraw: %q", out)
	}
	if !bytes.Contains(out, []byte("Downloading 100%")) {
		t.Errorf("final progress state lost: %q", out)
	}
}
