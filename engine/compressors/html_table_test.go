package compressors_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/JuliusBrussee/caveman/engine/compressors"
)

// inArticle wraps markup in a page the article extractor accepts.
func inArticle(markup string) string {
	return `<!doctype html><html><head><title>Weekly report</title></head><body>` +
		`<nav class="site-nav"><a href="/">Home</a> <a href="/reports">Reports</a></nav><article class="post">` +
		`<h1>Weekly Service Report For The Platform Team</h1>` +
		`<p>This report covers the past week, with commas, clauses, and enough prose to score the article container well above threshold.</p>` +
		`<p>The table below lists every service with its region and latency, so on-call engineers can spot the ones that need attention.</p>` +
		markup + `</article></body></html>`
}

// serviceTable is a long table in which one row in the middle breaches its SLO
// and every other row is healthy.
func serviceTable(rows, critical int) string {
	var b strings.Builder
	b.WriteString(`<table><thead><tr><th>Service</th><th>Region</th><th>Status</th><th>p99</th><th>Errors</th></tr></thead><tbody>`)
	for i := 0; i < rows; i++ {
		status, p99, errs := "healthy", 40+i%37, fmt.Sprintf("0.%02d%%", i%29)
		if i == critical {
			status, p99, errs = "CRITICAL", 4810, "38.12%"
		}
		fmt.Fprintf(&b, `<tr><td>svc-%03d</td><td>eu-west-%d</td><td><span class="badge">%s</span></td><td>%d ms</td><td>%s</td></tr>`,
			i, i%3+1, status, p99, errs)
	}
	b.WriteString(`</tbody></table>`)
	return b.String()
}

func TestHTMLArticleTableIsShaped(t *testing.T) {
	in := inArticle(serviceTable(400, 287))
	out, ok := compressors.NewHTML().Compress([]byte(in))
	if !ok {
		t.Fatal("expected article extraction")
	}
	s := string(out)
	for _, must := range []string{
		"Weekly Service Report",
		"|Service|Region|Status|p99|Errors|",
		"|svc-287|eu-west-3|CRITICAL|4810 ms|38.12%|",
		"rows elided (caveman)",
		"all Status=healthy",
	} {
		if !strings.Contains(s, must) {
			t.Errorf("article table lost %q:\n%s", must, s)
		}
	}
	if strings.Contains(s, "<td") || strings.Contains(s, "Reports") {
		t.Errorf("markup or navigation leaked into the article:\n%s", s)
	}
	if len(out)*10 > len(in) {
		t.Errorf("expected a large reduction, got %d of %d bytes", len(out), len(in))
	}
}

func TestHTMLArticleTableQueryKeepsMatchingRow(t *testing.T) {
	in := []byte(inArticle(serviceTable(200, -1)))
	c, isQueryAware := compressors.NewHTML().(compressors.QueryAwareCompressor)
	if !isQueryAware {
		t.Fatal("the HTML compressor must be query-aware so a question can keep its row")
	}
	plain, ok := c.Compress(in)
	if !ok {
		t.Fatal("expected article extraction")
	}
	if bytes.Contains(plain, []byte("|svc-123|")) {
		t.Fatal("test premise: svc-123 is an ordinary row and must be elided without a query")
	}
	asked, ok := c.CompressQuery(in, "what is the p99 of svc-123")
	if !ok {
		t.Fatal("expected article extraction with a query")
	}
	if !bytes.Contains(asked, []byte("|svc-123|")) {
		t.Errorf("the queried row must survive:\n%s", asked)
	}
}

func TestHTMLArticleTableKeepsRows(t *testing.T) {
	// The article extractor used to render a table one cell per line, which left
	// a number unattributable to its column.
	out, ok := compressors.NewHTML().Compress([]byte(inArticle(
		`<table><tr><th>Service</th><th>Old</th><th>New</th></tr>` +
			`<tr><td>gateway</td><td>2.3.1</td><td>2.4.0</td></tr>` +
			`<tr><td>billing</td><td></td><td>1.0.0</td></tr></table>`)))
	if !ok {
		t.Fatal("expected article extraction")
	}
	s := string(out)
	for _, must := range []string{"|Service|Old|New|", "|gateway|2.3.1|2.4.0|", "|billing||1.0.0|"} {
		if !strings.Contains(s, must) {
			t.Errorf("article table lost %q:\n%s", must, s)
		}
	}
}

func TestHTMLArticleWithoutTableUnchanged(t *testing.T) {
	// Byte-for-byte what the article extractor produced before tables were
	// rendered: a page with no table must not change.
	want := "The Title Of The Article\n\n" +
		"This is the first substantial paragraph of the article body. It contains several sentences, with commas, and enough text to score well above the threshold the extractor uses.\n\n" +
		"Here is a second meaningful paragraph that continues the article, again with commas, clauses, and real prose so the container accrues a high content score.\n\n" +
		"A third paragraph rounds out the body with yet more sentence-level content, commas, and substance for the reader to consume."
	out, ok := compressors.NewHTML().Compress([]byte(articleHTML))
	if !ok || string(out) != want {
		t.Errorf("article output changed:\n got %q\nwant %q", out, want)
	}
}

func TestHTMLArticleTableWithPreKeepsIndentation(t *testing.T) {
	// A cell cannot carry line breaks, so a table with <pre> in a cell is left to
	// the plain walk, which keeps <pre> verbatim.
	out, ok := compressors.NewHTML().Compress([]byte(inArticle(
		"<table><tr><th>Style</th><th>Code</th></tr><tr><td>indent</td><td><pre>func main() {\n    if x {\n        y()\n    }\n}</pre></td></tr></table>")))
	if !ok {
		t.Fatal("expected article extraction")
	}
	if !bytes.Contains(out, []byte("    if x {\n        y()")) {
		t.Errorf("<pre> in a table cell must keep its indentation:\n%s", out)
	}
}

func TestHTMLMergedCellsAreNotTabulated(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<table><tr><th colspan="2">Totals</th></tr>`)
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, `<tr><td>bucket-%02d</td><td>%d</td></tr>`, i, 1000+i)
	}
	b.WriteString(`</table>`)
	out, ok := compressors.NewHTML().Compress([]byte(inArticle(b.String())))
	if !ok {
		t.Fatal("expected article extraction")
	}
	for i := 0; i < 30; i++ {
		if !bytes.Contains(out, []byte(fmt.Sprintf("bucket-%02d", i))) {
			t.Fatalf("merged-cell table dropped bucket-%02d:\n%s", i, out)
		}
	}
	if bytes.Contains(out, []byte("elided")) {
		t.Errorf("nothing in a merged-cell table may be elided:\n%s", out)
	}
}

func TestHTMLCellTextKeepsPathsWhole(t *testing.T) {
	// coverage.py splits each path with <span class="sep">/</span>; a space at
	// every tag edge would print "/ home / dev / app.py".
	var b strings.Builder
	b.WriteString(`<table><thead><tr><th>File</th><th>Cover</th></tr></thead><tbody>`)
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, `<tr><td><span class="sep">/</span>home<span class="sep">/</span>dev<span class="sep">/</span>mod_%d.py</td><td>%d%%</td></tr>`, i, 50+i)
	}
	b.WriteString(`</tbody></table>`)
	out, ok := compressors.NewHTML().Compress([]byte(inArticle(b.String())))
	if !ok {
		t.Fatal("expected article extraction")
	}
	if !bytes.Contains(out, []byte("|/home/dev/mod_0.py|50%|")) {
		t.Errorf("path split by inline tags must stay whole:\n%s", out)
	}
}

func TestHTMLInlineEdgesKeepWordsApart(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<table><tr><th>Test</th><th>Result</th></tr>`)
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&b, `<tr><td>t%d</td><td><span class="badge">FAILED</span>exit %d</td></tr>`, i, i)
	}
	b.WriteString(`<tr><td>t9</td><td><span>build-17</span><span>failed</span></td></tr></table>`)
	out, ok := compressors.NewHTML().Compress([]byte(inArticle(b.String())))
	if !ok {
		t.Fatal("expected article extraction")
	}
	for _, must := range []string{"|t1|FAILED exit 1|", "|t9|build-17 failed|"} {
		if !bytes.Contains(out, []byte(must)) {
			t.Errorf("words on either side of a tag edge must stay apart, want %q:\n%s", must, out)
		}
	}
}

func TestHTMLPipeInCellIsEscaped(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<table><tr><th>Command</th><th>Exit</th></tr>`)
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&b, `<tr><td>cat log | grep err%d</td><td>%d</td></tr>`, i, i)
	}
	b.WriteString(`<tr><td>C:\logs\</td><td>9</td></tr></table>`)
	out, ok := compressors.NewHTML().Compress([]byte(inArticle(b.String())))
	if !ok {
		t.Fatal("expected article extraction")
	}
	if !bytes.Contains(out, []byte(`|cat log \| grep err1|1|`)) {
		t.Errorf("a pipe in a cell must be escaped, not read as a column break:\n%s", out)
	}
	if !bytes.Contains(out, []byte(`|C:\logs\ |9|`)) {
		t.Errorf("a trailing backslash must not escape the closing pipe:\n%s", out)
	}
}

func TestHTMLTableWithMarkerTextIsNotReshaped(t *testing.T) {
	// A page that quotes a caveman view (a bug report, these docs) holds marker
	// text in a cell. That table is rendered whole, and its rows keep their cells.
	var b strings.Builder
	b.WriteString(`<table><tr><th>View</th><th>Tokens</th></tr>`)
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, `<tr><td>… %d rows elided (caveman) …</td><td>%d</td></tr>`, i, 100+i)
	}
	b.WriteString(`</table>`)
	out, ok := compressors.NewHTML().Compress([]byte(inArticle(b.String())))
	if !ok {
		t.Fatal("expected article extraction")
	}
	if !bytes.Contains(out, []byte("|… 17 rows elided (caveman) …|117|")) {
		t.Errorf("a quoted marker row must keep all its cells and nothing may be elided:\n%s", out)
	}
}

func TestHTMLDuplicateColumnNamesStayApart(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<table><tr><th>Host</th><th>Count</th><th>Count</th></tr>`)
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, `<tr><td>host-%02d</td><td>0</td><td>5</td></tr>`, i)
	}
	b.WriteString(`</table>`)
	out, ok := compressors.NewHTML().Compress([]byte(inArticle(b.String())))
	if !ok {
		t.Fatal("expected article extraction")
	}
	s := string(out)
	if !strings.Contains(s, "|Host|Count|Count|") {
		t.Errorf("the header must be the page's own:\n%s", s)
	}
	if !strings.Contains(s, "Count_2=5") {
		t.Errorf("the marker must tell the two Count columns apart:\n%s", s)
	}
}

func TestHTMLArticleTableDeterministicAndIdempotent(t *testing.T) {
	c := compressors.NewHTML()
	in := []byte(inArticle(serviceTable(300, 150)))
	first, ok := c.Compress(in)
	if !ok {
		t.Fatal("expected article extraction")
	}
	again, _ := c.Compress(in)
	if !bytes.Equal(first, again) {
		t.Fatal("output must be deterministic")
	}
	if second, ok := c.Compress(first); ok && !bytes.Equal(second, first) {
		t.Errorf("output must be idempotent:\n first=%s\nsecond=%s", first, second)
	}
}

func FuzzHTMLCompress(f *testing.F) {
	f.Add(inArticle(serviceTable(30, 7)))
	f.Add(articleHTML)
	f.Add(opsDashboard(30, 7))
	f.Add(`<!doctype html><div role="grid"><div role="row"><div role="gridcell">x</div></div></div>`)
	f.Add(`<!doctype html><ul>` + strings.Repeat(`<li class="i"><b>k</b>v</li>`, 14) + `</ul>`)
	f.Add(inArticle(`<table><tr><th>a|b</th><th></th></tr><tr><td colspan="x">1</td><td>2</td></tr></table>`))
	f.Add(inArticle(`<table><tr><td><span>/</span>x</td></tr><tr><td><pre>a` + "\n" + `b</pre></td></tr></table>`))
	f.Fuzz(func(t *testing.T, doc string) {
		c := compressors.NewHTML()
		out, ok := c.Compress([]byte(doc))
		if !ok {
			return
		}
		if len(out) >= len(doc) {
			t.Fatalf("claimed a result that is not smaller: %d >= %d", len(out), len(doc))
		}
		if utf8.ValidString(doc) && !utf8.Valid(out) {
			t.Fatalf("valid UTF-8 in, invalid out: %q", out)
		}
		again, ok := c.Compress([]byte(doc))
		if !ok || !bytes.Equal(out, again) {
			t.Fatalf("non-deterministic output for %q", doc)
		}
		if second, ok := c.Compress(out); ok && !bytes.Equal(second, out) {
			t.Fatalf("not idempotent for %q", doc)
		}
	})
}
