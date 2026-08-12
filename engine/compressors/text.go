package compressors

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/JuliusBrussee/caveman/engine/safety"
)

var (
	textMarkerRe    = regexp.MustCompile(`sections elided \(caveman\)`)
	htmlScriptRe    = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`)
	htmlStyleRe     = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`)
	htmlSVGRe       = regexp.MustCompile(`(?is)<svg\b[^>]*>.*?</svg>`)
	htmlCommentRe   = regexp.MustCompile(`(?s)<!--.*?-->`)
	paragraphSplit  = regexp.MustCompile(`\n\s*\n+`)
	textImportantRe = regexp.MustCompile(`(?i)\b(ERROR|WARNING|IMPORTANT|NOTE|ACTION|FOLLOWUP|SECURITY|SUMMARY|CONCLUSION|DECISION|RECOMMENDATION)\b`)
)

func textMarker(n int) string { return fmt.Sprintf("… %d sections elided (caveman) …", n) }

// textCompressor collapses long prose/HTML payloads by section while retaining
// headings, the opening/closing context, and explicitly important paragraphs.
type textCompressor struct {
	keepHead int
	keepTail int
	minBytes int
}

// NewText returns the long text / HTML compressor.
func NewText() Compressor { return &textCompressor{keepHead: 2, keepTail: 2, minBytes: 1600} }

func (c *textCompressor) ContentType() string       { return "text" }
func (c *textCompressor) SafetyClass() safety.Class { return safety.S4 }

func (c *textCompressor) Compress(input []byte) ([]byte, bool) {
	return c.compress(input, "")
}

func (c *textCompressor) CompressQuery(input []byte, query string) ([]byte, bool) {
	return c.compress(input, query)
}

func (c *textCompressor) compress(input []byte, query string) ([]byte, bool) {
	if !utf8.Valid(input) || len(bytes.TrimSpace(input)) < c.minBytes {
		return nil, false
	}
	normalized := input
	if looksHTML(input) {
		normalized = compressHTMLNoise(input)
	}
	sections := splitTextSections(normalized)
	if len(sections) <= c.keepHead+c.keepTail+2 {
		return nil, false
	}

	keep := make([]bool, len(sections))
	for i, section := range sections {
		if i < c.keepHead || i >= len(sections)-c.keepTail || isTextHeading(section) || textImportantRe.Match(section) || textMarkerRe.Match(section) {
			keep[i] = true
		}
	}
	docs := make([]string, len(sections))
	for i, section := range sections {
		docs[i] = string(section)
	}
	keepQueryRelevant(keep, docs, query, 12, 0.30)
	keepNonRedundant(sections, keep)

	out := make([][]byte, 0, len(sections))
	dropped := 0
	for i, section := range sections {
		if keep[i] {
			if dropped > 0 {
				out = append(out, []byte(textMarker(dropped)))
				dropped = 0
			}
			out = append(out, bytes.TrimSpace(section))
		} else {
			dropped++
		}
	}
	if dropped > 0 {
		out = append(out, []byte(textMarker(dropped)))
	}
	if len(out) == len(sections) {
		return nil, false
	}
	result := bytes.Join(out, []byte("\n\n"))
	return result, true
}

func looksHTML(input []byte) bool {
	s := strings.ToLower(string(bytes.TrimSpace(input)))
	return strings.HasPrefix(s, "<!doctype html") || strings.HasPrefix(s, "<html") || strings.Contains(s, "<body")
}

func compressHTMLNoise(input []byte) []byte {
	out := htmlScriptRe.ReplaceAll(input, []byte(textMarker(1)))
	out = htmlStyleRe.ReplaceAll(out, []byte(textMarker(1)))
	out = htmlSVGRe.ReplaceAll(out, []byte(textMarker(1)))
	out = htmlCommentRe.ReplaceAll(out, nil)
	return out
}

func splitTextSections(input []byte) [][]byte {
	if bytes.Contains(input, []byte("\n\n")) || bytes.Contains(input, []byte("\r\n\r\n")) {
		raw := paragraphSplit.Split(string(input), -1)
		sections := make([][]byte, 0, len(raw))
		for _, part := range raw {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				sections = append(sections, []byte(trimmed))
			}
		}
		return sections
	}
	lines := bytes.Split(input, []byte("\n"))
	sections := make([][]byte, 0, len(lines))
	for _, line := range lines {
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			sections = append(sections, trimmed)
		}
	}
	return sections
}

func isTextHeading(section []byte) bool {
	trimmed := bytes.TrimSpace(section)
	if bytes.HasPrefix(trimmed, []byte("#")) {
		return true
	}
	if len(trimmed) > 96 || bytes.ContainsAny(trimmed, ".!?") {
		return false
	}
	words := bytes.Fields(trimmed)
	return len(words) > 0 && len(words) <= 8
}
