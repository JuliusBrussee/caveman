package compressors

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/JuliusBrussee/caveman/engine/safety"
)

// HTMLType is the content type the HTML article extractor handles. It matches
// engine.TypeHTML and is reached via the content router (Detect → "html").
const HTMLType = "html"

var (
	htmlPositiveRe = regexp.MustCompile(`(?i)(article|body|content|entry|main|page|post|story|text|blog)`)
	htmlNegativeRe = regexp.MustCompile(`(?i)(comment|nav|sidebar|footer|header|menu|promo|banner|ad-|share|social|related|breadcrumb|cookie|popup|modal|widget)`)
)

// htmlCompressor extracts the main article text from an HTML document, dropping
// boilerplate (nav, sidebars, scripts, styles, footers) using a deterministic
// readability-style heuristic: block elements score by text volume and a
// class/id signal, scores propagate to their container, and the container is
// discounted by its link density (link-heavy nodes are navigation, not prose).
// It is pure-Go (works in the cgo and WASM builds alike) and S4 (lossy): the
// original is recoverable via CCR. It bails to pass-through on any doubt — a
// parse failure, no <body>, no clear main container, a link-dense winner, or an
// extraction that is too small — so it never claims a result it cannot stand by.
type htmlCompressor struct {
	minOutputBytes int
}

// NewHTML returns the default HTML article extractor.
func NewHTML() Compressor { return &htmlCompressor{minOutputBytes: 200} }

func (c *htmlCompressor) ContentType() string       { return HTMLType }
func (c *htmlCompressor) SafetyClass() safety.Class { return safety.S4 }

func (c *htmlCompressor) Compress(input []byte) ([]byte, bool) {
	if len(bytes.TrimSpace(input)) == 0 {
		return nil, false
	}
	doc, err := html.Parse(bytes.NewReader(input))
	if err != nil {
		return nil, false
	}
	stripNoise(doc)
	body := findFirst(doc, atom.Body)
	if body == nil {
		return nil, false // not a document we can localize content within
	}

	scores := map[*html.Node]int{}
	accumulateScores(body, scores)
	best := pickBest(body, scores)
	if best == nil || best == body {
		return nil, false // could not localize a main container → claim nothing
	}
	// Reject a link-dense winner (an index/nav block, not an article): bail when
	// at least half the text is anchor text.
	total, link := textLen(best), anchorTextLen(best)
	if total == 0 || link*2 >= total {
		return nil, false
	}

	text := strings.TrimSpace(renderText(best))
	if len(text) < c.minOutputBytes {
		return nil, false
	}
	out := []byte(text)
	if len(out) >= len(input) {
		return nil, false // extraction did not shrink the payload
	}
	return out, true
}

// stripNoise removes elements that never carry article content.
func stripNoise(n *html.Node) {
	var toRemove []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			if ch.Type == html.ElementNode {
				switch ch.DataAtom {
				case atom.Script, atom.Style, atom.Noscript, atom.Svg, atom.Iframe, atom.Form, atom.Button, atom.Input, atom.Template:
					toRemove = append(toRemove, ch)
					continue
				}
			}
			walk(ch)
		}
	}
	walk(n)
	for _, r := range toRemove {
		if r.Parent != nil {
			r.Parent.RemoveChild(r)
		}
	}
}

// isScorable reports whether an element's text volume should contribute a score.
func isScorable(a atom.Atom) bool {
	switch a {
	case atom.P, atom.Pre, atom.Td, atom.Blockquote, atom.Li,
		atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		return true
	}
	return false
}

// isContainer reports whether an element can be a content container candidate.
func isContainer(a atom.Atom) bool {
	switch a {
	case atom.Div, atom.Article, atom.Main, atom.Section, atom.Td, atom.Body:
		return true
	}
	return false
}

// accumulateScores walks the document and adds a content score for each scorable
// block to its nearest container ancestor (full) and the next container up
// (half), then folds in the container's class/id signal once.
func accumulateScores(root *html.Node, scores map[*html.Node]int) {
	seen := map[*html.Node]bool{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && isScorable(n.DataAtom) {
			txt := textContent(n)
			if len(strings.TrimSpace(txt)) >= 25 {
				base := 1 + strings.Count(txt, ",") + minInt(len(txt)/100, 3)
				// Credit every container ancestor (full), not just the nearest two,
				// so an outer container holding several sections always outscores any
				// single inner section — otherwise a multi-section article truncates
				// to its highest-scoring section. <body> is never credited: it is
				// excluded as a winner, and crediting it would make it the top scorer
				// (sum of everything) and force a spurious "couldn't localize" bail.
				for p := containerAncestor(n); p != nil; p = containerAncestor(p) {
					if p.DataAtom == atom.Body {
						continue
					}
					if !seen[p] {
						seen[p] = true
						scores[p] += classIDWeight(p)
					}
					scores[p] += base
				}
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(root)
}

// pickBest returns the highest-scoring container, discounting each by its link
// density. It walks in document order so ties resolve deterministically (first
// wins), never by map iteration order.
func pickBest(root *html.Node, scores map[*html.Node]int) *html.Node {
	var best *html.Node
	bestScore := 0
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if raw, ok := scores[n]; ok && raw > 0 {
			total := textLen(n)
			final := raw
			if total > 0 {
				final = raw * (total - anchorTextLen(n)) / total // integer link-density discount
			}
			if final > bestScore {
				bestScore = final
				best = n
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(root)
	return best
}

func classIDWeight(n *html.Node) int {
	var b strings.Builder
	for _, a := range n.Attr {
		if a.Key == "class" || a.Key == "id" {
			b.WriteByte(' ')
			b.WriteString(a.Val)
		}
	}
	s := b.String()
	if s == "" {
		return 0
	}
	w := 0
	if htmlPositiveRe.MatchString(s) {
		w += 25
	}
	if htmlNegativeRe.MatchString(s) {
		w -= 25
	}
	return w
}

// containerAncestor returns the nearest ancestor that can hold content.
func containerAncestor(n *html.Node) *html.Node {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && isContainer(p.DataAtom) {
			return p
		}
	}
	return nil
}

func findFirst(n *html.Node, a atom.Atom) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == a {
		return n
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		if got := findFirst(ch, a); got != nil {
			return got
		}
	}
	return nil
}

func textContent(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(n)
	return b.String()
}

func textLen(n *html.Node) int { return len(strings.TrimSpace(textContent(n))) }

func anchorTextLen(n *html.Node) int {
	total := 0
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.A {
			total += len(strings.TrimSpace(textContent(n)))
			return // do not double-count nested anchors
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(n)
	return total
}

// renderText serializes a node subtree to readable plain text. Block-level
// elements introduce paragraph breaks; inline elements emit a space boundary so
// adjacent text never merges across a tag (e.g. "<b>fifty</b><i>dollars</i>"
// stays "fifty dollars", not "fiftydollars"). <pre> content is preserved
// verbatim (its whole contract is whitespace), protected from the collapse pass
// via a placeholder. Deterministic.
func renderText(n *html.Node) string {
	var b strings.Builder
	var pres []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.Pre {
			idx := len(pres)
			pres = append(pres, strings.Trim(textContent(n), "\n"))
			b.WriteString("\n\x00PRE")
			b.WriteString(strconv.Itoa(idx))
			b.WriteString("\x00\n")
			return // do not descend; pre text is captured verbatim
		}
		switch n.Type {
		case html.TextNode:
			b.WriteString(n.Data)
		case html.ElementNode:
			if isBlockLevel(n.DataAtom) {
				b.WriteString("\n")
			} else {
				b.WriteString(" ") // inline boundary
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
		if n.Type == html.ElementNode {
			if isBlockLevel(n.DataAtom) {
				b.WriteString("\n")
			} else {
				b.WriteString(" ")
			}
		}
	}
	walk(n)
	out := collapseWhitespace(b.String())
	for i, p := range pres {
		out = strings.Replace(out, "\x00PRE"+strconv.Itoa(i)+"\x00", p, 1)
	}
	return out
}

func isBlockLevel(a atom.Atom) bool {
	switch a {
	case atom.P, atom.Div, atom.Br, atom.Li, atom.Ul, atom.Ol, atom.Pre, atom.Blockquote,
		atom.Section, atom.Article, atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6,
		atom.Tr, atom.Table, atom.Hr:
		return true
	}
	return false
}

var (
	htmlSpaceRe = regexp.MustCompile(`[ \t]+`)
	htmlBlankRe = regexp.MustCompile(`\n[ \t]*\n[ \t]*(\n[ \t]*)*`)
)

func collapseWhitespace(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimSpace(htmlSpaceRe.ReplaceAllString(ln, " "))
	}
	joined := strings.Join(lines, "\n")
	return strings.TrimSpace(htmlBlankRe.ReplaceAllString(joined, "\n\n"))
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
