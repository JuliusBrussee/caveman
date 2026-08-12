package compressors

import (
	"bytes"
	"fmt"
	"unicode/utf8"

	"github.com/JuliusBrussee/caveman/engine/safety"
)

// RepetitionType is the forced content type for the repetition compressor. It is
// never returned by Detect (run-length elision is only ever applied on request),
// so it must not be added to the content router — reach it via Options.Type.
const RepetitionType = "repetition"

// repMarkerPrefix opens the elision marker. It is detected on re-entry so the
// compressor is idempotent (a marker line is never itself collapsed).
const repMarkerPrefix = "… caveman: "

func repMarker(n int) string {
	return fmt.Sprintf("%s%d identical lines elided …", repMarkerPrefix, n)
}

// repetitionCompressor collapses runs of consecutive identical lines to a single
// occurrence plus a count marker — the honest "stop paying to read the same thing
// twice" win for logs and echoed output (duplicated progress lines, repeated
// "PASSED", retry spam). It is deterministic (a single sequential scan) and S4
// (lossy): the dropped lines are recoverable in full via CCR. It only touches
// exactly-identical adjacent lines, so it never changes the order or content of
// distinct lines. On any problem — invalid UTF-8, too small, or nothing to
// collapse — it reports !ok and the caller forwards the bytes unchanged.
type repetitionCompressor struct {
	minRun   int // collapse a run only when it has at least this many identical lines
	minBytes int // skip inputs smaller than this
}

// NewRepetition returns the default repetition (run-length) compressor.
func NewRepetition() Compressor { return &repetitionCompressor{minRun: 3, minBytes: 200} }

func (c *repetitionCompressor) ContentType() string       { return RepetitionType }
func (c *repetitionCompressor) SafetyClass() safety.Class { return safety.S4 }

func (c *repetitionCompressor) Compress(input []byte) ([]byte, bool) {
	if len(input) < c.minBytes || !utf8.Valid(input) {
		return nil, false
	}
	lines := bytes.Split(input, []byte("\n"))
	out := make([][]byte, 0, len(lines))
	collapsed := false

	i := 0
	for i < len(lines) {
		j := i + 1
		for j < len(lines) && bytes.Equal(lines[j], lines[i]) {
			j++
		}
		runLen := j - i
		out = append(out, lines[i]) // always keep the first occurrence
		// Collapse only a long enough run of a non-blank line — collapsing one or
		// two lines, or a blank run, would not reliably shrink the token count. A
		// line that is itself an elision marker is never collapsed, so our own
		// markers survive a second pass unchanged (enforced idempotence).
		if runLen >= c.minRun && len(bytes.TrimSpace(lines[i])) > 0 &&
			!bytes.HasPrefix(lines[i], []byte(repMarkerPrefix)) {
			out = append(out, []byte(repMarker(runLen-1)))
			collapsed = true
		} else {
			for k := i + 1; k < j; k++ {
				out = append(out, lines[k])
			}
		}
		i = j
	}

	if !collapsed {
		return nil, false // nothing repeated → pass-through, claim nothing
	}
	result := bytes.Join(out, []byte("\n"))
	if len(result) >= len(input) {
		// The marker(s) did not actually shrink the payload (short lines / small
		// runs where the marker is longer than what it replaced) — claim nothing
		// rather than report a false win and lean on the engine's token guard.
		return nil, false
	}
	return result, true
}
