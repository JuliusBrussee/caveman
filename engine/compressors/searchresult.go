package compressors

import (
	"bytes"
	"fmt"
	"regexp"
	"unicode/utf8"

	"github.com/JuliusBrussee/caveman/engine/safety"
)

var (
	searchResultMarkerRe    = regexp.MustCompile(`search result lines elided \(caveman\)`)
	searchResultImportantRe = regexp.MustCompile(`(?i)\b(ERROR|FAIL|FAILED|PANIC|ACTION|FOLLOWUP|SECURITY|SECRET|TOKEN|PASSWORD|KEY)\b`)
)

func searchResultMarker(n int) string {
	return fmt.Sprintf("… %d search result lines elided (caveman) …", n)
}

// searchResultCompressor keeps the top and bottom hits plus obviously important
// diagnostic/security lines, collapsing the middle of large grep/web result sets.
type searchResultCompressor struct {
	keepHead int
	keepTail int
	minLines int
}

// NewSearchResult returns the line-oriented search-result compressor.
func NewSearchResult() Compressor {
	return &searchResultCompressor{keepHead: 8, keepTail: 4, minLines: 18}
}

func (c *searchResultCompressor) ContentType() string       { return "search-result" }
func (c *searchResultCompressor) SafetyClass() safety.Class { return safety.S4 }

func (c *searchResultCompressor) Compress(input []byte) ([]byte, bool) {
	return c.compress(input, "")
}

func (c *searchResultCompressor) CompressQuery(input []byte, query string) ([]byte, bool) {
	return c.compress(input, query)
}

func (c *searchResultCompressor) compress(input []byte, query string) ([]byte, bool) {
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
		if i < c.keepHead || i >= len(lines)-c.keepTail || searchResultImportantRe.Match(line) || searchResultMarkerRe.Match(line) {
			keep[i] = true
		}
	}
	docs := make([]string, len(lines))
	for i, line := range lines {
		docs[i] = string(line)
	}
	keepQueryRelevant(keep, docs, query, 24, 0.30)
	keepNonRedundant(lines, keep)

	out := make([][]byte, 0, len(lines))
	dropped := 0
	for i, line := range lines {
		if keep[i] {
			if dropped > 0 {
				out = append(out, []byte(searchResultMarker(dropped)))
				dropped = 0
			}
			out = append(out, line)
		} else {
			dropped++
		}
	}
	if dropped > 0 {
		out = append(out, []byte(searchResultMarker(dropped)))
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
