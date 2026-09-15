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
	fenceRe         = regexp.MustCompile("^\\s{0,3}(`{3,}|~{3,})")
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

// splitTextSections cuts input into the units the compressor may independently
// drop. A fenced code block is always ONE unit, however many blank lines it
// contains: this is the fallback compressor for anything Detect does not
// recognise, so ordinary Markdown routes here, and splitting on blank lines
// alone turned a code block with a blank line in it into two or three separate
// sections. Each was separately elidable and separately TrimSpace'd, so a
// pasted function came back with lines missing and its indentation stripped —
// under a marker claiming only prose had been dropped.
func splitTextSections(input []byte) [][]byte {
	// Paragraph mode when the document has blank lines; otherwise every line is
	// its own section, or a blank-line-free document would collapse to one unit
	// and never compress.
	paragraphs := bytes.Contains(input, []byte("\n\n")) || bytes.Contains(input, []byte("\r\n\r\n"))
	lines, _ := splitLines(input)
	sections := make([][]byte, 0, len(lines))
	buf := make([][]byte, 0, 8)
	flush := func() {
		if len(buf) == 0 {
			return
		}
		if joined := bytes.TrimSpace(bytes.Join(buf, []byte("\n"))); len(joined) > 0 {
			sections = append(sections, joined)
		}
		buf = buf[:0]
	}
	var openFence []byte
	for _, line := range lines {
		marker := fenceRe.FindSubmatch(line)
		if openFence != nil {
			buf = append(buf, line)
			// A closing fence is the same character, at least as long as the opener.
			if marker != nil && marker[1][0] == openFence[0] && len(marker[1]) >= len(openFence) {
				openFence = nil
				flush()
			}
			continue
		}
		if marker != nil {
			flush()
			openFence = marker[1]
			buf = append(buf, line)
			continue
		}
		if len(bytes.TrimSpace(line)) == 0 {
			flush()
			continue
		}
		buf = append(buf, line)
		if !paragraphs {
			flush()
		}
	}
	flush() // unterminated fence: the rest of the document is one unit
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
