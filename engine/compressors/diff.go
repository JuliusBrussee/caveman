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
	if len(lines) < c.minLines {
		return nil, false
	}

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
				out = append(out, []byte(diffMarker(dropped)))
				dropped = 0
			}
			out = append(out, line)
		} else {
			dropped++
		}
	}
	if dropped > 0 {
		out = append(out, []byte(diffMarker(dropped)))
	}
	if len(out) == len(lines) {
		return nil, false
	}
	result := bytes.Join(out, sep)
	if trailing {
		result = append(result, sep...)
	}
	return result, true
}

func isDiffStructuralLine(line []byte) bool {
	return bytes.HasPrefix(line, []byte("diff --git ")) ||
		bytes.HasPrefix(line, []byte("index ")) ||
		bytes.HasPrefix(line, []byte("--- ")) ||
		bytes.HasPrefix(line, []byte("+++ ")) ||
		bytes.HasPrefix(line, []byte("@@ ")) ||
		(isChangedDiffLine(line))
}

func isChangedDiffLine(line []byte) bool {
	if len(line) < 2 {
		return false
	}
	if line[0] != '+' && line[0] != '-' {
		return false
	}
	return line[1] != '+' && line[1] != '-'
}
