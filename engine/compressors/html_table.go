package compressors

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// A table in an HTML page is rendered as a Markdown table and shaped by the
// tabular compressor, so its rows are elided with the same rules and invariants
// as a CSV: header, head and tail rows, numeric extrema, ERROR/FAIL rows,
// query-relevant rows and one representative of each distinct row kind always
// survive. Only a table that forms a plain grid is lifted. A table with merged
// cells, a nested table, preformatted text in a cell or ragged rows is left to
// the caller, which renders its text as before.

var htmlPreformatRe = regexp.MustCompile(`(?i)white-space\s*:\s*(pre|pre-wrap|pre-line|break-spaces)\b`)

type tableRenderer struct {
	tab         *tabularCompressor
	query       string
	lines       []string
	elidedBytes int
}

func (r *tableRenderer) line(s string) {
	if s = strings.TrimSpace(s); s != "" {
		r.lines = append(r.lines, s)
	}
}

// tableBlock renders one table as Markdown lines for the article path, which
// places the block itself.
func (r *tableRenderer) tableBlock(n *html.Node) (string, bool) {
	start := len(r.lines)
	ok := r.table(n)
	block := strings.Join(r.lines[start:], "\n")
	r.lines = r.lines[:start]
	return block, ok
}

// table lifts a native <table> into rows. It reports false, rendering nothing,
// for a table it cannot represent cell for cell.
func (r *tableRenderer) table(n *html.Node) bool {
	caption := ""
	var rows []*html.Node
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		if ch.Type != html.ElementNode {
			continue
		}
		switch ch.DataAtom {
		case atom.Caption:
			caption = inlineText(ch)
		case atom.Tr:
			rows = append(rows, ch)
		case atom.Thead, atom.Tbody, atom.Tfoot:
			for tr := ch.FirstChild; tr != nil; tr = tr.NextSibling {
				if tr.Type == html.ElementNode && tr.DataAtom == atom.Tr {
					rows = append(rows, tr)
				}
			}
		}
	}
	cells := func(tr *html.Node) []*html.Node {
		var out []*html.Node
		for td := tr.FirstChild; td != nil; td = td.NextSibling {
			if td.Type == html.ElementNode && (td.DataAtom == atom.Td || td.DataAtom == atom.Th) {
				out = append(out, td)
			}
		}
		return out
	}
	isHeader := func(cell *html.Node) bool { return cell.DataAtom == atom.Th }
	grid, hasHeader, ok := cellGrid(rows, cells, isHeader)
	if !ok {
		return false
	}
	r.emitTable(caption, grid, hasHeader)
	return true
}

// cellGrid reads each row's cells as text. It fails when the rows do not form a
// plain rectangle: fewer than two rows, a ragged row, a merged cell (colspan or
// rowspan above 1), or a cell that holds a table of its own or preformatted text
// whose line breaks a cell cannot carry.
func cellGrid(rows []*html.Node, cellsOf func(*html.Node) []*html.Node, isHeader func(*html.Node) bool) ([][]string, bool, bool) {
	if len(rows) < 2 {
		return nil, false, false
	}
	grid := make([][]string, 0, len(rows))
	hasHeader := true
	for i, row := range rows {
		cells := cellsOf(row)
		if len(cells) == 0 || (i > 0 && len(cells) != len(grid[0])) {
			return nil, false, false
		}
		texts := make([]string, len(cells))
		for j, cell := range cells {
			if spans(cell) || containsStructure(cell) {
				return nil, false, false
			}
			if i == 0 && !isHeader(cell) {
				hasHeader = false
			}
			texts[j] = inlineText(cell)
		}
		grid = append(grid, texts)
	}
	return grid, hasHeader, true
}

func spans(cell *html.Node) bool {
	for _, key := range []string{"colspan", "rowspan", "aria-colspan", "aria-rowspan"} {
		if v := strings.TrimSpace(htmlAttr(cell, key)); v != "" {
			if n, err := strconv.Atoi(v); err != nil || n != 1 {
				return true
			}
		}
	}
	return false
}

func containsStructure(n *html.Node) bool {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		if ch.Type == html.ElementNode && (ch.DataAtom == atom.Table || isARIATable(ch) || isPreformatted(ch) || containsStructure(ch)) {
			return true
		}
	}
	return false
}

func isARIATable(n *html.Node) bool {
	switch htmlAttr(n, "role") {
	case "table", "grid", "treegrid":
		return true
	}
	return false
}

func isPreformatted(n *html.Node) bool {
	return n.DataAtom == atom.Pre || htmlPreformatRe.MatchString(htmlAttr(n, "style"))
}

// emitTable renders rows as a Markdown table, shaped by the tabular compressor
// when it is long enough to shape. Without a header row a blank one is supplied
// for shaping and not rendered, so no data row is promoted to a header.
//
// Rows are written compactly (|a|b|) with trailing empty cells left off, which
// Markdown reads as empty, and a column that is empty in every row, header
// included, is dropped. Every value still sits in its own column: a flat
// rendering that drops empty cells would leave a date unattributable to the
// column it came from.
func (r *tableRenderer) emitTable(caption string, rows [][]string, hasHeader bool) {
	r.line(caption)
	if !hasHeader {
		rows = append([][]string{make([]string, len(rows[0]))}, rows...)
	}
	used := make([]bool, len(rows[0]))
	for _, row := range rows {
		for j, cell := range row {
			if cell != "" {
				used[j] = true
			}
		}
	}
	// A table whose cells already read like an elision marker is rendered whole,
	// so every marker row below is one shapeRows wrote.
	// The invariants name columns by header, so two columns both called "Count"
	// are told apart for shaping; the header the agent sees is the page's own.
	shaped := rows
	if !gridHasMarker(rows) {
		named := append([][]string{uniqueNames(rows[0])}, rows[1:]...)
		if s, elided, ok := r.tab.shapeRows(parsedTable{rows: named}, 1, r.query); ok {
			shaped = s
			shaped[0] = rows[0]
			r.elidedBytes += elided
		}
	}
	for i, row := range shaped {
		if i == 0 && !hasHeader {
			continue
		}
		if i > 0 && len(shaped) != len(rows) && tabularMarkerRe.MatchString(row[0]) {
			r.lines = append(r.lines, markdownRow([]string{row[0]}))
			continue
		}
		cells := make([]string, 0, len(row))
		for j, cell := range row {
			if used[j] {
				cells = append(cells, cell)
			}
		}
		r.lines = append(r.lines, markdownRow(cells))
		if i == 0 {
			r.lines = append(r.lines, markdownSeparator(len(cells)))
		}
	}
}

func gridHasMarker(rows [][]string) bool {
	for _, row := range rows {
		for _, cell := range row {
			if tabularMarkerRe.MatchString(cell) {
				return true
			}
		}
	}
	return false
}

func markdownRow(cells []string) string {
	for len(cells) > 1 && cells[len(cells)-1] == "" {
		cells = cells[:len(cells)-1]
	}
	var b strings.Builder
	b.WriteString("|")
	for _, cell := range cells {
		cell = strings.ReplaceAll(cell, "|", `\|`)
		if strings.HasSuffix(cell, `\`) {
			cell += " " // a trailing backslash would escape the closing pipe
		}
		b.WriteString(cell)
		b.WriteString("|")
	}
	return b.String()
}

func markdownSeparator(n int) string {
	return "|" + strings.Repeat("---|", n)
}

func uniqueNames(names []string) []string {
	seen := map[string]int{}
	out := make([]string, len(names))
	for i, name := range names {
		seen[name]++
		if name != "" && seen[name] > 1 {
			name += "_" + strconv.Itoa(seen[name])
		}
		out[i] = name
	}
	return out
}

func isStructuralBlock(n *html.Node) bool {
	switch n.DataAtom {
	case atom.Address, atom.Article, atom.Aside, atom.Blockquote, atom.Caption, atom.Dd, atom.Details,
		atom.Dialog, atom.Div, atom.Dl, atom.Dt, atom.Fieldset, atom.Figcaption, atom.Figure, atom.Form,
		atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6, atom.Header, atom.Hgroup, atom.Hr,
		atom.Legend, atom.Li, atom.Main, atom.Ol, atom.P, atom.Pre, atom.Section, atom.Summary,
		atom.Table, atom.Tbody, atom.Td, atom.Tfoot, atom.Th, atom.Thead, atom.Tr, atom.Ul:
		return true
	}
	return htmlPreformatRe.MatchString(htmlAttr(n, "style"))
}

// inlineJoiner gathers inline content the way a browser lays it out, with one
// exception. Text and the element beside it are joined without a space, so a
// path that a report splits into <span>/</span>home<span>/</span>… comes out
// as one path, unless a letter or digit would meet a letter or digit: a badge
// such as <span>FAILED</span>exit 1 relies on CSS for its gap, and "FAILEDexit"
// would hide the word from every keyword rule downstream. Two elements that
// touch are always separated, for the same reason. A block element or a <br>
// separates what is on either side of it, and an image contributes its alt
// text, which is often the only label a status icon has.
type inlineJoiner struct {
	b        strings.Builder
	last     rune
	prevElem bool
	started  bool
}

func (j *inlineJoiner) add(n *html.Node) {
	var part inlineJoiner
	part.write(n)
	text := part.b.String()
	if strings.TrimSpace(text) == "" {
		j.b.WriteString(text)
		if text != "" {
			j.last = ' '
		}
		return
	}
	isElem := n.Type == html.ElementNode
	if j.started {
		firstRune, _ := utf8.DecodeRuneInString(text)
		if (j.prevElem && isElem) || (isWordRune(j.last) && isWordRune(firstRune)) {
			j.b.WriteString(" ")
		}
	}
	j.b.WriteString(text)
	j.last, _ = utf8.DecodeLastRuneInString(text)
	j.prevElem, j.started = isElem, true
}

func (j *inlineJoiner) write(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		j.b.WriteString(n.Data)
	case html.ElementNode:
		switch {
		case n.DataAtom == atom.Img:
			if alt := collapseInline(htmlAttr(n, "alt")); alt != "" {
				j.b.WriteString(" [image: " + alt + "] ")
			}
		case n.DataAtom == atom.Br:
			j.b.WriteString(" ")
		default:
			block := isStructuralBlock(n)
			if block {
				j.b.WriteString(" ")
			}
			var inner inlineJoiner
			for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
				inner.add(ch)
			}
			j.b.WriteString(inner.String())
			if block {
				j.b.WriteString(" ")
			}
		}
	}
}

func (j *inlineJoiner) String() string { return j.b.String() }

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func inlineText(n *html.Node) string {
	var j inlineJoiner
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		j.add(ch)
	}
	return collapseInline(j.String())
}

func collapseInline(s string) string { return strings.Join(strings.Fields(s), " ") }

func htmlAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Namespace == "" && a.Key == key {
			return a.Val
		}
	}
	return ""
}
