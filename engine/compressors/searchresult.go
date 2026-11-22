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
	lines, trailing := splitLines(input)
	if len(lines) < c.minLines {
		return nil, false
	}
	cr := crSuffix(input)

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
				out = append(out, synthLine(searchResultMarker(dropped), cr))
				dropped = 0
			}
			out = append(out, line)
		} else {
			dropped++
		}
	}
	if dropped > 0 {
		out = append(out, synthLine(searchResultMarker(dropped), cr))
	}
	grouped, didGroup := groupSearchPaths(out, cr)
	if len(out) == len(lines) && !didGroup {
		return nil, false
	}
	if didGroup {
		return joinLines(grouped, trailing), true
	}
	return joinLines(out, trailing), true
}

var searchPathLineRe = regexp.MustCompile(`^([./~A-Za-z0-9_-][^:\n]{4,240}):(\d+(?::\d+)?:.*)$`)

// groupSearchPaths factors repeated file-path prefixes across consecutive
// `path:line:match` lines into `path:\n  line:match` headers (lossless: zero
// files or match lines are dropped). Runs of < 3 consecutive hits for the same
// file stay inline so short runs never grow. Idempotent because grouped lines
// start with two spaces and no longer match searchPathLineRe.
func groupSearchPaths(lines [][]byte, cr []byte) ([][]byte, bool) {
	type parsed struct {
		path string
		rest []byte
		ok   bool
	}
	items := make([]parsed, len(lines))
	for i, line := range lines {
		raw := bytes.TrimSuffix(line, []byte("\r"))
		if m := searchPathLineRe.FindSubmatch(raw); m != nil {
			items[i] = parsed{path: string(m[1]), rest: m[2], ok: true}
		}
	}
	var out [][]byte
	changed := false
	for i := 0; i < len(lines); {
		if !items[i].ok {
			out = append(out, lines[i])
			i++
			continue
		}
		j := i + 1
		for j < len(lines) && items[j].ok && items[j].path == items[i].path {
			j++
		}
		runLen := j - i
		// Grouping replaces `runLen` copies of `path:` with 1 `path:` header
		// plus `runLen` two-space indents: net saving when runLen >= 3 and
		// len(path) >= 8.
		if runLen >= 3 && len(items[i].path) >= 8 {
			out = append(out, synthLine(items[i].path+":", cr))
			for k := i; k < j; k++ {
				indented := append(append([]byte{' ', ' '}, items[k].rest...), cr...)
				out = append(out, indented)
			}
			changed = true
		} else {
			for k := i; k < j; k++ {
				out = append(out, lines[k])
			}
		}
		i = j
	}
	return out, changed
}
