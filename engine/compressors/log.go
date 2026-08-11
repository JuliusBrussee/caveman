package compressors

import (
	"bytes"
	"fmt"
	"regexp"
	"unicode/utf8"

	"github.com/JuliusBrussee/caveman/engine/safety"
)

var (
	// Lines worth keeping: errors, failures, warnings, stack-trace frames.
	importantLineRe = regexp.MustCompile(`(?i)(\b(ERROR|FATAL|PANIC|EXCEPTION|TRACEBACK|FAIL|FAILED|FAILURE|WARN|WARNING)\b|^\s+at\s|^\s+File "|\.go:\d+|^\s+--->|caused by)`)
	// A run of dropped lines is collapsed into this marker, which is itself
	// kept on later passes so the compressor stays idempotent.
	logMarkerRe = regexp.MustCompile(`lines elided \(caveman\)`)
)

// logMarker renders the elision marker for a run of dropped lines. summary is
// the class-invariant description of exactly those lines (see invariants.go); it
// is empty when the run carries no extractable structure, and the marker is then
// byte-identical to the one this compressor emitted before invariants existed.
func logMarker(n int, summary string) string {
	if summary == "" {
		return fmt.Sprintf("… %d lines elided (caveman) …", n)
	}
	return fmt.Sprintf("… %d lines elided (caveman): %s …", n, summary)
}

// summarizeLogRun describes a run of dropped log lines by their logfmt fields.
func summarizeLogRun(run [][]byte) string {
	units := make([][]field, len(run))
	elidedBytes := 0
	for i, line := range run {
		units[i] = lineFields(line)
		elidedBytes += len(line) + 1
	}
	return summarizeElided(units, elidedBytes)
}

// logCompressor keeps errors, stack traces, and the first/last lines for
// context, and collapses runs of repetitive INFO/DEBUG/progress noise into a
// single marker line. It is S4 (lossy); the original is recoverable via CCR.
type logCompressor struct {
	keepHead int
	keepTail int
}

// NewLog returns the default log/build-output compressor.
func NewLog() Compressor { return &logCompressor{keepHead: 2, keepTail: 2} }

func (c *logCompressor) ContentType() string       { return "log" }
func (c *logCompressor) SafetyClass() safety.Class { return safety.S4 }

func (c *logCompressor) Compress(input []byte) ([]byte, bool) {
	return c.compress(input, "")
}

func (c *logCompressor) CompressQuery(input []byte, query string) ([]byte, bool) {
	return c.compress(input, query)
}

func (c *logCompressor) compress(input []byte, query string) ([]byte, bool) {
	if !utf8.Valid(input) {
		return nil, false // binary-ish content → pass-through
	}
	sep := []byte("\n")
	if bytes.Contains(input, []byte("\r\n")) {
		sep = []byte("\r\n")
	}
	trailing := bytes.HasSuffix(input, sep)
	body := input
	if trailing {
		body = body[:len(body)-len(sep)]
	}
	lines := bytes.Split(body, sep)
	if len(lines) < 4 {
		return nil, false // too small to be worth compressing
	}

	keep := make([]bool, len(lines))
	for i, ln := range lines {
		if i < c.keepHead {
			keep[i] = true
			continue
		}
		if importantLineRe.Match(ln) || logMarkerRe.Match(ln) || bytes.HasPrefix(ln, []byte(elisionNotePrefix)) {
			keep[i] = true
		}
	}
	// The tail window counts real log lines. Our own trailing contract line would
	// otherwise consume one of its slots, which on a re-compression pushed the
	// last real line out of the window and let a second pass elide it — output
	// that differs from the first pass busts the provider prefix cache.
	for i, budget := len(lines)-1, c.keepTail; i >= c.keepHead && budget > 0; i-- {
		keep[i] = true
		if !bytes.HasPrefix(lines[i], []byte(elisionNotePrefix)) && !logMarkerRe.Match(lines[i]) {
			budget--
		}
	}
	docs := make([]string, len(lines))
	for i, line := range lines {
		docs[i] = string(line)
	}
	keepQueryRelevant(keep, docs, query, 16, 0.30)
	keepNonRedundant(lines, keep)

	out := make([][]byte, 0, len(lines))
	var run [][]byte
	elidedBytes := 0
	flush := func() {
		if len(run) == 0 {
			return
		}
		runBytes := 0
		for _, ln := range run {
			runBytes += len(ln) + len(sep)
		}
		summary := summarizeLogRun(run)
		marker := logMarker(len(run), summary)
		if !worthEliding(len(run), len(marker), runBytes, summary) {
			// Too small a run to describe and too small to be worth a recovery
			// handle: emit the lines themselves and claim nothing for them.
			out = append(out, run...)
			run = run[:0]
			return
		}
		out = append(out, []byte(marker))
		elidedBytes += runBytes
		run = run[:0]
	}
	for i, ln := range lines {
		if keep[i] {
			flush()
			out = append(out, ln)
		} else {
			run = append(run, ln)
		}
	}
	flush()
	// One contract line per payload, and only when the elision was large enough
	// to afford it. A payload that already carries it is being re-compressed, so
	// the line is left where it is rather than duplicated.
	if wantsElisionNote(elidedBytes) && !bytes.Contains(input, []byte(elisionNotePrefix)) {
		out = append(out, []byte(elisionNote("lines")))
	}

	result := bytes.Join(out, sep)
	if trailing {
		result = append(result, sep...)
	}
	return result, true
}
