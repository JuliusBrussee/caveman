package compressors

import (
	"bytes"
	"fmt"
	"regexp"
	"unicode/utf8"

	"github.com/JuliusBrussee/caveman/engine/safety"
)

var diffMarkerRe = regexp.MustCompile(`context lines elided \(caveman\)`)

func diffMarker(n int) string { return fmt.Sprintf("… %d context lines elided (caveman) …", n) }

// diffCompressor keeps file/hunk headers plus changed lines and collapses long
// runs of unchanged context. It is S4: full context is recoverable via CCR.
type diffCompressor struct {
	keepContext int
	minLines    int
}

// NewDiff returns the unified-diff compressor.
func NewDiff() Compressor { return &diffCompressor{keepContext: 2, minLines: 12} }

func (c *diffCompressor) ContentType() string       { return "diff" }
func (c *diffCompressor) SafetyClass() safety.Class { return safety.S4 }

func (c *diffCompressor) Compress(input []byte) ([]byte, bool) {
	if !utf8.Valid(input) {
		return nil, false
	}
	lines, trailing := splitLines(input)
	if len(lines) < c.minLines {
		return nil, false
	}
	cr := crSuffix(input)

	keep := make([]bool, len(lines))
	for i, line := range lines {
		if isDiffStructuralLine(line) || diffMarkerRe.Match(line) {
			keep[i] = true
			for j := max(0, i-c.keepContext); j <= min(len(lines)-1, i+c.keepContext); j++ {
				keep[j] = true
			}
		}
	}

	out := make([][]byte, 0, len(lines))
	dropped := 0
	for i, line := range lines {
		if keep[i] {
			if dropped > 0 {
				out = append(out, synthLine(diffMarker(dropped), cr))
				dropped = 0
			}
			out = append(out, line)
		} else {
			dropped++
		}
	}
	if dropped > 0 {
		out = append(out, synthLine(diffMarker(dropped), cr))
	}
	if len(out) == len(lines) {
		return nil, false
	}
	return joinLines(out, trailing), true
}

func isDiffStructuralLine(line []byte) bool {
	return bytes.HasPrefix(line, []byte("diff --git ")) ||
		bytes.HasPrefix(line, []byte("index ")) ||
		bytes.HasPrefix(line, []byte("--- ")) ||
		bytes.HasPrefix(line, []byte("+++ ")) ||
		bytes.HasPrefix(line, []byte("@@ ")) ||
		(isChangedDiffLine(line))
}

// isChangedDiffLine reports whether line is an added/removed hunk line.
//
// It deliberately does NOT exclude lines whose second byte is '+'/'-'. That test
// was meant to skip the "--- a/x" / "+++ b/x" file headers, but those are
// already matched by prefix above — and it also swallowed every real change
// whose own content starts with '-' or '+' ("-  - name: build" in YAML,
// "+--verbose" in a shell script, a deleted "----" rule in markdown). Those
// lines were classified as context and elided under a marker claiming only
// context was dropped.
//
// A bare "-" or "+" is a change too — it is how git renders the deletion or
// addition of a blank line — so the length floor is 1, not 2.
func isChangedDiffLine(line []byte) bool {
	return len(line) >= 1 && (line[0] == '+' || line[0] == '-')
}
