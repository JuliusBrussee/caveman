package compressors

import (
	"bytes"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// The structural view is for the HTML the article extractor declines: pages made
// of data rather than prose. It keeps the page's visible text in document order
// (headings, paragraphs, lists, definition lists, alert regions, <pre> verbatim)
// and drops what the agent cannot use (scripts, styles, navigation, the page
// footer, form controls, hidden elements, attribute soup). Every table — native
// <table> or an ARIA grid — and every run of same-shaped sibling records (a card
// grid, a long <li> list) becomes a Markdown table shaped by the tabular
// compressor, so its rows are elided with the same rules and invariants as a CSV:
// header, head and tail rows, numeric extrema, ERROR/FAIL rows, query-relevant
// rows and one representative of each distinct row kind always survive.
//
// It claims nothing unless the page has at least one such table, so a page with
// no data in it passes through exactly as before. It also passes through on
// anything it cannot represent faithfully: a fragment rather than a document (a
// README with inline HTML is Markdown, and its line breaks matter), invalid
// UTF-8, a server-side or component template (the agent came to edit the markup,
// not read it), text already carrying an elision marker, or nesting deeper than
// any real layout. A table with merged cells, a nested table, preformatted text
// in a cell or ragged rows is not tabulated, and neither is a record run whose
// records do not carry the same fields; their text is rendered as is and nothing
// in it is dropped.

const (
	// structuralMaxDepth is the deepest element nesting the structural view
	// accepts. Real layouts stay far below it.
	structuralMaxDepth = 256
	// structuralMinRecords is how many consecutive same-shaped siblings make a
	// record run. It matches the tabular compressor's minimum row count, below
	// which a lifted table would not be shaped anyway.
	structuralMinRecords = 12
	// structuralSigDepth is how deep two records must match to share a shape.
	structuralSigDepth = 4
	// structuralMaxFields caps the columns a record run may produce.
	structuralMaxFields = 32
)

var (
	htmlTemplateRe = regexp.MustCompile(`\{\{|\{%|<%|<\?php`)
	htmlDigitsRe   = regexp.MustCompile(`[0-9]+`)
	htmlDocumentRe = regexp.MustCompile(`(?i)^\s*(<!doctype\s+html|<html[\s>])|<body[\s>]`)
)

func (c *htmlCompressor) structural(input []byte, query string) ([]byte, bool) {
	if !utf8.Valid(input) || !htmlDocumentRe.Match(input) || htmlTemplateRe.Match(input) || tabularMarkerRe.Match(input) {
		return nil, false
	}
	doc, err := html.Parse(bytes.NewReader(input))
	if err != nil {
		return nil, false
	}
	body := findFirst(doc, atom.Body)
	if body == nil {
		return nil, false
	}
	if treeDepth(body) > structuralMaxDepth {
		return nil, false
	}
	r := &structuralRenderer{tableRenderer: c.renderer(query), sigs: map[*html.Node]string{}, holds: map[*html.Node]bool{}}
	if head := findFirst(doc, atom.Head); head != nil {
		if title := findFirst(head, atom.Title); title != nil {
			r.line(inlineText(title))
		}
	}
	stripChrome(body)
	r.children(body)
	if r.tables == 0 {
		return nil, false
	}
	if wantsElisionNote(r.elidedBytes) {
		r.line(elisionNote("rows"))
	}
	out := []byte(strings.Join(r.lines, "\n"))
	if bytes.HasSuffix(input, []byte("\n")) {
		out = append(out, '\n')
	}
	if len(out) >= len(input) {
		return nil, false
	}
	return out, true
}

// structuralRenderer renders a whole page. It counts the tables it lifts,
// because a page with none is passed through.
type structuralRenderer struct {
	*tableRenderer
	tables int
	sigs   map[*html.Node]string
	holds  map[*html.Node]bool
}

// children renders n's children in order. Inline content between block children
// is gathered into one paragraph line, so text never merges across a block edge
// and a sentence split by <b> or <a> stays one line.
func (r *structuralRenderer) children(n *html.Node) {
	var inline inlineJoiner
	flush := func() {
		r.line(collapseInline(inline.String()))
		inline = inlineJoiner{}
	}
	for ch := n.FirstChild; ch != nil; {
		if ch.Type == html.ElementNode {
			if run := r.recordRun(ch); run != nil {
				flush()
				if !r.records(run) {
					for _, rec := range run {
						r.element(rec)
					}
				}
				ch = run[len(run)-1].NextSibling
				continue
			}
			if ch.DataAtom == atom.Br {
				flush()
				ch = ch.NextSibling
				continue
			}
			if isStructuralBlock(ch) || r.holdsBlock(ch) {
				flush()
				r.element(ch)
				ch = ch.NextSibling
				continue
			}
		}
		inline.add(ch)
		ch = ch.NextSibling
	}
	flush()
}

func (r *structuralRenderer) element(n *html.Node) {
	role := htmlAttr(n, "role")
	switch role {
	case "table", "grid", "treegrid":
		if r.ariaTable(n) {
			r.tables++
		} else {
			r.children(n)
		}
		return
	case "alert", "status":
		r.prefixed(n, "["+role+"] ", "")
		return
	}
	switch htmlAttr(n, "aria-live") {
	case "assertive":
		r.prefixed(n, "[alert] ", "")
		return
	case "polite":
		r.prefixed(n, "[status] ", "")
		return
	}
	switch n.DataAtom {
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		if text := inlineText(n); text != "" {
			r.line(strings.Repeat("#", int(n.Data[1]-'0')) + " " + text)
		}
	case atom.Table:
		if r.table(n) {
			r.tables++
		} else {
			r.children(n)
		}
	case atom.Li:
		bullet := "- "
		if n.Parent != nil && n.Parent.DataAtom == atom.Ol {
			bullet = strconv.Itoa(listPosition(n)) + ". "
		}
		r.prefixed(n, bullet, "  ")
	case atom.Dl:
		r.definitions(n)
	case atom.Pre:
		r.preformatted(n)
	default:
		if isPreformatted(n) {
			r.preformatted(n)
			return
		}
		r.children(n)
	}
}

// preformatted keeps an element's text verbatim: <pre>, or any element styled
// white-space: pre, whose line breaks and indentation are its content.
func (r *structuralRenderer) preformatted(n *html.Node) {
	if text := strings.Trim(textContent(n), "\n"); strings.TrimSpace(text) != "" {
		r.lines = append(r.lines, text)
	}
}

// holdsBlock reports whether an inline element wraps block content, as a
// <center> around a table or an <a> around a card does. Such an element is
// rendered as a block, so its rows and paragraphs are not run into one line.
func (r *structuralRenderer) holdsBlock(n *html.Node) bool {
	if held, ok := r.holds[n]; ok {
		return held
	}
	held := false
	for ch := n.FirstChild; ch != nil && !held; ch = ch.NextSibling {
		if ch.Type == html.ElementNode {
			held = isStructuralBlock(ch) || isPreformatted(ch) || r.holdsBlock(ch)
		}
	}
	r.holds[n] = held
	return held
}

// treeDepth returns the deepest element nesting under root, walking iteratively
// so that a pathological page cannot exhaust the stack before it is refused.
func treeDepth(root *html.Node) int {
	type frame struct {
		n     *html.Node
		depth int
	}
	deepest := 0
	stack := []frame{{root, 0}}
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if f.depth > deepest {
			deepest = f.depth
		}
		for ch := f.n.FirstChild; ch != nil; ch = ch.NextSibling {
			if ch.Type == html.ElementNode {
				stack = append(stack, frame{ch, f.depth + 1})
			}
		}
	}
	return deepest
}

// prefixed renders n's children and marks the first line they produced with
// first and every later one with rest, so a list item or an alert keeps its
// label even when its content spans several lines or holds a table.
func (r *structuralRenderer) prefixed(n *html.Node, first, rest string) {
	start := len(r.lines)
	r.children(n)
	if start < len(r.lines) && strings.HasPrefix(r.lines[start], "|") {
		// A label glued to a table row would read as part of its first cell.
		r.lines = append(r.lines[:start+1], r.lines[start:]...)
		r.lines[start] = strings.TrimSpace(first)
		for i := start + 1; i < len(r.lines); i++ {
			r.lines[i] = rest + r.lines[i]
		}
		return
	}
	for i := start; i < len(r.lines); i++ {
		if i == start {
			r.lines[i] = first + r.lines[i]
		} else {
			r.lines[i] = rest + r.lines[i]
		}
	}
}

func listPosition(li *html.Node) int {
	pos := 1
	for s := li.PrevSibling; s != nil; s = s.PrevSibling {
		if s.Type == html.ElementNode && s.DataAtom == atom.Li {
			pos++
		}
	}
	return pos
}

// definitions renders a <dl> as one "term: description" line per description.
// <div> wrappers around dt/dd groups are allowed; a nested <dl> is not entered.
func (r *structuralRenderer) definitions(dl *html.Node) {
	term, described := "", false
	flushTerm := func() {
		if term != "" && !described {
			r.line(term)
		}
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			if ch.Type == html.TextNode {
				r.line(collapseInline(ch.Data))
				continue
			}
			if ch.Type != html.ElementNode {
				continue
			}
			switch ch.DataAtom {
			case atom.Dt:
				flushTerm()
				term, described = inlineText(ch), false
			case atom.Dd:
				text := inlineText(ch)
				if term != "" {
					r.line(term + ": " + text)
				} else {
					r.line(text)
				}
				described = true
			case atom.Div:
				walk(ch)
			default:
				r.line(inlineText(ch))
			}
		}
	}
	walk(dl)
	flushTerm()
}

// ariaTable lifts an element with role=table/grid/treegrid, as rendered by
// component libraries that build tables out of <div>s.
func (r *tableRenderer) ariaTable(n *html.Node) bool {
	// Everything in the grid must sit in a row and every row must be flat. Text
	// outside the rows (a pagination total) or a row nested in another (a
	// treegrid's children) has no place in the lifted table, so the grid is
	// rendered as text instead.
	var rows []*html.Node
	stray := false
	var findRows func(*html.Node)
	findRows = func(n *html.Node) {
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			switch {
			case ch.Type == html.TextNode:
				stray = stray || strings.TrimSpace(ch.Data) != ""
			case ch.Type != html.ElementNode:
			case ch.DataAtom == atom.Table || isARIATable(ch):
				stray = true
			case htmlAttr(ch, "role") == "row":
				rows = append(rows, ch)
			default:
				findRows(ch)
			}
		}
	}
	findRows(n)
	if stray {
		return false
	}
	cells := func(row *html.Node) []*html.Node {
		var out []*html.Node
		var walk func(*html.Node)
		walk = func(n *html.Node) {
			for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
				if ch.Type == html.ElementNode && htmlAttr(ch, "role") == "row" {
					stray = true
					return
				}
				if ch.Type != html.ElementNode {
					continue
				}
				switch htmlAttr(ch, "role") {
				case "cell", "gridcell", "columnheader", "rowheader":
					out = append(out, ch)
				default:
					walk(ch)
				}
			}
		}
		walk(row)
		return out
	}
	isHeader := func(cell *html.Node) bool { return htmlAttr(cell, "role") == "columnheader" }
	grid, hasHeader, ok := cellGrid(rows, cells, isHeader)
	if !ok || stray {
		return false
	}
	r.emitTable(htmlAttr(n, "aria-label"), grid, hasHeader)
	return true
}

// recordRun returns start and the siblings after it that share its shape, when
// there are at least structuralMinRecords of them. Two records share a shape when
// their tags, digit-masked classes and element children match to
// structuralSigDepth, so a row that differs in kind (an extra badge, a
// "card--critical" class) ends the run and is rendered on its own.
func (r *structuralRenderer) recordRun(start *html.Node) []*html.Node {
	switch start.DataAtom {
	case atom.Div, atom.Li, atom.Article, atom.Section:
	default:
		return nil
	}
	if hasHandledRole(start) {
		return nil
	}
	sig := r.signature(start, 0)
	run := []*html.Node{start}
	for ch := start.NextSibling; ch != nil; ch = ch.NextSibling {
		if ch.Type == html.TextNode && strings.TrimSpace(ch.Data) == "" {
			continue
		}
		if ch.Type != html.ElementNode || hasHandledRole(ch) || r.signature(ch, 0) != sig {
			break
		}
		run = append(run, ch)
	}
	if len(run) < structuralMinRecords {
		return nil
	}
	return run
}

func (r *structuralRenderer) signature(n *html.Node, depth int) string {
	if depth == 0 {
		if s, ok := r.sigs[n]; ok {
			return s
		}
	}
	var b strings.Builder
	b.WriteString(n.Data)
	if classes := strings.Fields(htmlDigitsRe.ReplaceAllString(htmlAttr(n, "class"), "#")); len(classes) > 0 {
		sort.Strings(classes)
		b.WriteString(".")
		b.WriteString(strings.Join(classes, "."))
	}
	if depth < structuralSigDepth {
		b.WriteString("(")
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			if ch.Type == html.ElementNode {
				b.WriteString(r.signature(ch, depth+1))
				b.WriteString(",")
			}
		}
		b.WriteString(")")
	}
	s := b.String()
	if depth == 0 {
		r.sigs[n] = s
	}
	return s
}

// records lifts a record run into a table with one column per text field. It
// reports false unless every record carries the same fields, named alike and in
// the same order: two records that each fill a different optional slot have the
// same shape and the same field count, and lining them up by position would put
// a value under the wrong column. It also reports false when a record holds
// structure (a table, a list, <pre>) that a cell cannot carry.
func (r *structuralRenderer) records(run []*html.Node) bool {
	rows := [][]string{nil}
	var first []string
	for i, rec := range run {
		names, values, ok := recordFields(rec)
		if !ok || len(values) == 0 || len(values) > structuralMaxFields {
			return false
		}
		if i == 0 {
			first = names
			rows[0] = uniqueNames(names)
		} else if !slices.Equal(names, first) {
			return false
		}
		rows = append(rows, values)
	}
	r.emitTable("", rows, true)
	r.tables++
	return true
}

// recordFields returns each non-blank text node under rec, in document order,
// named after the class (or tag) of the element holding it.
func recordFields(rec *html.Node) ([]string, []string, bool) {
	var names, values []string
	ok := true
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for ch := n.FirstChild; ch != nil && ok; ch = ch.NextSibling {
			switch ch.Type {
			case html.TextNode:
				if text := collapseInline(ch.Data); text != "" {
					names = append(names, fieldName(n))
					values = append(values, text)
				}
			case html.ElementNode:
				switch ch.DataAtom {
				case atom.Table, atom.Ul, atom.Ol, atom.Dl, atom.Pre:
					ok = false
					return
				case atom.Img:
					if alt := collapseInline(htmlAttr(ch, "alt")); alt != "" {
						names = append(names, fieldName(ch))
						values = append(values, alt)
					}
				default:
					walk(ch)
				}
			}
		}
	}
	walk(rec)
	return names, values, ok
}

func fieldName(n *html.Node) string {
	if classes := strings.Fields(htmlAttr(n, "class")); len(classes) > 0 {
		return classes[0]
	}
	return n.Data
}

// stripChrome removes what never carries page content for the agent: scripts,
// styles and embedded media, navigation, the page footer, form controls,
// comments, and elements the page itself hides. Only a <footer> directly under
// <body>, or one marked role=contentinfo, is the page's: a footer anywhere else
// may close a card or a section and hold its data ("updated 5m ago").
func stripChrome(root *html.Node) {
	var drop []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			if ch.Type == html.CommentNode || (ch.Type == html.ElementNode && isChrome(ch)) {
				drop = append(drop, ch)
				continue
			}
			walk(ch)
		}
	}
	walk(root)
	for _, n := range drop {
		n.Parent.RemoveChild(n)
	}
}

func isChrome(n *html.Node) bool {
	switch n.DataAtom {
	case atom.Script, atom.Style, atom.Noscript, atom.Template, atom.Svg, atom.Math, atom.Iframe,
		atom.Canvas, atom.Object, atom.Embed, atom.Link, atom.Meta, atom.Nav,
		atom.Button, atom.Select, atom.Input, atom.Textarea:
		return true
	case atom.Footer:
		return n.Parent != nil && n.Parent.DataAtom == atom.Body
	}
	switch htmlAttr(n, "role") {
	case "navigation", "contentinfo":
		return true
	}
	if htmlAttr(n, "aria-hidden") == "true" {
		return true
	}
	for _, a := range n.Attr {
		if a.Namespace == "" && a.Key == "hidden" {
			return true
		}
	}
	return false
}

// hasHandledRole reports whether n carries a role the renderer gives its own
// treatment: a table or grid, or an alert, status or live region.
func hasHandledRole(n *html.Node) bool {
	switch htmlAttr(n, "role") {
	case "table", "grid", "treegrid", "alert", "status":
		return true
	}
	switch htmlAttr(n, "aria-live") {
	case "assertive", "polite":
		return true
	}
	return false
}
