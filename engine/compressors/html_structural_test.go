package compressors_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/compressors"
)

// opsDashboard is a page the article extractor declines: one big service table,
// an alert banner, navigation and inline state. One row in the middle breaches
// its SLO; every other row is healthy.
func opsDashboard(rows, critical int) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><title>Ops Dashboard</title><style>.badge{padding:2px}</style>` +
		`<script>window.__STATE__={"token":"abc"}</script></head><body>` +
		`<header><nav class="nav"><ul>`)
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, `<li><a href="/p%d">Page %d</a></li>`, i, i)
	}
	b.WriteString(`</ul></nav></header><main id="dashboard"><h1>Fleet health</h1>` +
		`<div role="alert" class="alert">1 service breaching SLO</div>` +
		`<table><thead><tr><th>Service</th><th>Region</th><th>Status</th><th>p99</th><th>Errors</th><th></th></tr></thead><tbody>`)
	for i := 0; i < rows; i++ {
		status, p99, errs := "healthy", 40+i%37, fmt.Sprintf("0.%02d%%", i%29)
		if i == critical {
			status, p99, errs = "CRITICAL", 4810, "38.12%"
		}
		fmt.Fprintf(&b, `<tr><td><a href="/svc/%[1]d">svc-%03[1]d</a></td><td>eu-west-%[2]d</td>`+
			`<td><span class="badge">%[3]s</span></td><td>%[4]d ms</td><td>%[5]s</td>`+
			`<td><a href="/svc/%[1]d/logs">logs</a> <a href="/svc/%[1]d/metrics">metrics</a></td></tr>`,
			i, i%3+1, status, p99, errs)
	}
	b.WriteString(`</tbody></table></main><footer>© ops team</footer></body></html>`)
	return b.String()
}

func TestHTMLDashboardTableKeepsOutlierRow(t *testing.T) {
	in := opsDashboard(400, 287)
	out, ok := compressors.NewHTML().Compress([]byte(in))
	if !ok {
		t.Fatal("a data page the article extractor declines must get the structural view, not pass through")
	}
	s := string(out)
	for _, must := range []string{
		"Ops Dashboard",
		"# Fleet health",
		"[alert] 1 service breaching SLO",
		"|Service|Region|Status|p99|Errors|",
		"|svc-287|eu-west-3|CRITICAL|4810 ms|38.12%|logs metrics|",
		"rows elided (caveman)",
		"all Status=healthy",
	} {
		if !strings.Contains(s, must) {
			t.Errorf("structural view lost %q:\n%s", must, s)
		}
	}
	for _, gone := range []string{"<td", "__STATE__", "Page 3", "© ops team", "padding"} {
		if strings.Contains(s, gone) {
			t.Errorf("structural view kept %q:\n%s", gone, s)
		}
	}
	if len(out)*20 > len(in) {
		t.Errorf("expected a large reduction, got %d of %d bytes", len(out), len(in))
	}
}

func TestHTMLStructuralQueryKeepsMatchingRow(t *testing.T) {
	in := []byte(opsDashboard(200, -1))
	c, isQueryAware := compressors.NewHTML().(compressors.QueryAwareCompressor)
	if !isQueryAware {
		t.Fatal("the HTML compressor must be query-aware so a question can keep its row")
	}
	plain, ok := c.Compress(in)
	if !ok {
		t.Fatal("expected the structural view")
	}
	if bytes.Contains(plain, []byte("|svc-123|")) {
		t.Fatal("test premise: svc-123 is an ordinary row and must be elided without a query")
	}
	asked, ok := c.CompressQuery(in, "what is the p99 of svc-123")
	if !ok {
		t.Fatal("expected the structural view with a query")
	}
	if !bytes.Contains(asked, []byte("|svc-123|")) {
		t.Errorf("the queried row must survive:\n%s", asked)
	}
}

func TestHTMLRecordRunBecomesTable(t *testing.T) {
	// A CI page built from <div>s with no whitespace between fields, as a
	// component framework renders it. The failed job has a modifier class, so it
	// is not part of the run and is shown on its own.
	var b strings.Builder
	b.WriteString(`<!doctype html><html><body><main><h2>Pipeline 4182</h2><div class="jobs">`)
	for i := 0; i < 60; i++ {
		if i == 41 {
			b.WriteString(`<div class="job job--failed"><span class="name">integration-41</span><span class="state">failed</span><span class="took">3m12s</span></div>`)
			continue
		}
		fmt.Fprintf(&b, `<div class="job"><span class="name">unit-%02d</span><span class="state">passed</span><span class="took">%ds</span></div>`, i, 20+i)
	}
	b.WriteString(`</div></main></body></html>`)
	out, ok := compressors.NewHTML().Compress([]byte(b.String()))
	if !ok {
		t.Fatal("expected the structural view")
	}
	s := string(out)
	for _, must := range []string{"## Pipeline 4182", "|name|state|took|", "integration-41 failed 3m12s", "all state=passed"} {
		if !strings.Contains(s, must) {
			t.Errorf("record run lost %q:\n%s", must, s)
		}
	}
}

func TestHTMLARIAGridBecomesTable(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><body><div class="app"><div role="grid" aria-label="Pods">` +
		`<div role="row"><div role="columnheader">Name</div><div role="columnheader">Phase</div><div role="columnheader">Restarts</div></div>`)
	for i := 0; i < 40; i++ {
		phase, restarts := "Running", "0"
		if i == 17 {
			phase, restarts = "CrashLoopBackOff", "57"
		}
		fmt.Fprintf(&b, `<div role="row"><div role="gridcell">api-%02d</div><div role="gridcell">%s</div><div role="gridcell">%s</div></div>`, i, phase, restarts)
	}
	b.WriteString(`</div></div></body></html>`)
	out, ok := compressors.NewHTML().Compress([]byte(b.String()))
	if !ok {
		t.Fatal("expected the structural view")
	}
	s := string(out)
	for _, must := range []string{"Pods", "|Name|Phase|Restarts|", "|api-17|CrashLoopBackOff|57|", "rows elided (caveman)"} {
		if !strings.Contains(s, must) {
			t.Errorf("ARIA grid lost %q:\n%s", must, s)
		}
	}
}

func TestHTMLInlineRoleStaysInline(t *testing.T) {
	doc := `<!doctype html><html><body><div><p>Click <a role="button">Retry</a> to run job 7 again.</p>` +
		`<table><tr><th>Job</th><th>State</th></tr><tr><td>7</td><td>failed</td></tr></table></div></body></html>`
	out, ok := compressors.NewHTML().Compress([]byte(doc))
	if !ok {
		t.Fatal("expected the structural view")
	}
	if !bytes.Contains(out, []byte("Click Retry to run job 7 again.")) {
		t.Errorf("an inline element with an unhandled role must not split the sentence:\n%s", out)
	}
}

func TestHTMLStructuralPassesThrough(t *testing.T) {
	rows := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "<tr><td>row-%d</td><td>%d</td></tr>", i, i)
		}
		return b.String()
	}
	cases := map[string]string{
		"no table":                 `<!doctype html><html><body><div><span>Status</span><span>OK</span></div><div><a href="/">Home</a></div></body></html>`,
		"jinja template":           `<!doctype html><html><body><table><tr><th>A</th><th>B</th></tr>{% for r in rows %}<tr><td>{{ r.a }}</td><td>{{ r.b }}</td></tr>{% endfor %}` + rows(20) + `</table></body></html>`,
		"erb template":             `<!doctype html><html><body><table><tr><th>A</th><th>B</th></tr><% rows.each do |r| %>` + rows(20) + `<% end %></table></body></html>`,
		"invalid utf-8":            "<!doctype html><html><body><table><tr><th>A</th><th>B</th></tr>" + rows(20) + "<tr><td>\xff\xfe</td><td>1</td></tr></table></body></html>",
		"only merged table":        `<!doctype html><html><body><table><tr><th colspan="2">Totals</th></tr>` + rows(20) + `</table></body></html>`,
		"nested beyond any layout": strings.Repeat("<div>", 300) + "<table><tr><th>A</th><th>B</th></tr>" + rows(20) + "</table>" + strings.Repeat("</div>", 300),
	}
	for name, doc := range cases {
		if out, ok := compressors.NewHTML().Compress([]byte(doc)); ok {
			t.Errorf("%s: must pass through, got:\n%s", name, out)
		}
	}
}

func TestHTMLChromeAndHiddenAreDropped(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><body><nav>NAVLINK</nav><div role="navigation">ROLENAV</div>` +
		`<div hidden>HIDDENTEXT</div><span aria-hidden="true">ICONTEXT</span><main>` +
		`<article class="card"><h3>Card</h3><footer>updated 5m ago</footer></article>` +
		`<form><table><tr><th>Name</th><th>Size</th></tr>`)
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&b, `<tr><td><input type="checkbox">file-%d</td><td>%d KB</td></tr>`, i, i)
	}
	b.WriteString(`</table><button>DELETE SELECTED</button></form></main><footer>PAGEFOOTER</footer></body></html>`)
	out, ok := compressors.NewHTML().Compress([]byte(b.String()))
	if !ok {
		t.Fatal("expected the structural view")
	}
	s := string(out)
	for _, gone := range []string{"NAVLINK", "ROLENAV", "HIDDENTEXT", "ICONTEXT", "DELETE SELECTED", "PAGEFOOTER"} {
		if strings.Contains(s, gone) {
			t.Errorf("%q must be dropped:\n%s", gone, s)
		}
	}
	// A table inside a <form> (an admin list with checkboxes) and a card's own
	// footer are content.
	for _, must := range []string{"|file-1|1 KB|", "updated 5m ago"} {
		if !strings.Contains(s, must) {
			t.Errorf("%q must be kept:\n%s", must, s)
		}
	}
}

func TestHTMLStructuralDeterministicAndIdempotent(t *testing.T) {
	c := compressors.NewHTML()
	in := []byte(opsDashboard(300, 150))
	first, ok := c.Compress(in)
	if !ok {
		t.Fatal("expected the structural view")
	}
	again, _ := c.Compress(in)
	if !bytes.Equal(first, again) {
		t.Fatal("structural view must be deterministic")
	}
	if second, ok := c.Compress(first); ok && !bytes.Equal(second, first) {
		t.Errorf("structural view must be idempotent:\n first=%s\nsecond=%s", first, second)
	}
}

func TestHTMLRecordsWithDifferentFieldsAreNotTabulated(t *testing.T) {
	// Every seventh job fills the "blocked" slot instead of "done". The records
	// have the same shape and field count, but lining them up by position would
	// file "blocked" under "done" and make the marker's tally false.
	var b strings.Builder
	b.WriteString(`<!doctype html><html><body><main><table><tr><th>Queue</th><th>Depth</th></tr><tr><td>jobs</td><td>40</td></tr></table>`)
	for i := 0; i < 40; i++ {
		if i%7 == 6 {
			fmt.Fprintf(&b, `<div class="row"><span class="name">job-%02d</span><span class="done"></span><span class="blocked">blocked</span></div>`, i)
		} else {
			fmt.Fprintf(&b, `<div class="row"><span class="name">job-%02d</span><span class="done">done</span><span class="blocked"></span></div>`, i)
		}
	}
	b.WriteString(`</main></body></html>`)
	out, ok := compressors.NewHTML().Compress([]byte(b.String()))
	if !ok {
		t.Fatal("expected the structural view")
	}
	s := string(out)
	if strings.Contains(s, "|name|done|") || strings.Contains(s, "done×") {
		t.Errorf("records with different fields must not share columns:\n%s", s)
	}
	if !strings.Contains(s, "job-06 blocked") || !strings.Contains(s, "job-05 done") {
		t.Errorf("every record must still be shown with its own field:\n%s", s)
	}
}

func TestHTMLFragmentIsNotRestructured(t *testing.T) {
	// A README with inline HTML is Markdown: its line breaks are its structure.
	readme := "<p align=\"center\"><img src=\"logo.png\" alt=\"logo\"></p>\n\n" +
		"<div align=\"center\">\n\n<table><tr><th>Contributor</th><th>Commits</th></tr><tr><td>ana</td><td>12</td></tr></table>\n\n</div>\n\n" +
		"## Install\n\n```bash\nnpm install -g mytool\nmytool init --force\n```\n\n## Usage\n\n- first item\n- second item\n"
	if out, ok := compressors.NewHTML().Compress([]byte(readme)); ok {
		t.Errorf("a Markdown file with inline HTML must pass through, got:\n%s", out)
	}
}

func TestHTMLPreformattedTextKeepsLines(t *testing.T) {
	doc := `<!doctype html><html><body><main><h2>Build 812</h2>` +
		"<div class=\"log\" style=\"white-space: pre-wrap\">step one: compiling\nstep two: linking\n    FAILED at main.go:12</div>" +
		`<table><tr><th>Stage</th><th>Result</th></tr><tr><td>link</td><td>failed</td></tr></table></main></body></html>`
	out, ok := compressors.NewHTML().Compress([]byte(doc))
	if !ok {
		t.Fatal("expected the structural view")
	}
	if !bytes.Contains(out, []byte("step one: compiling\nstep two: linking\n    FAILED at main.go:12")) {
		t.Errorf("text styled white-space: pre must keep its lines and indentation:\n%s", out)
	}
}

func TestHTMLFooterInsideContentIsKept(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><body><div class="app"><table><tr><th>Node</th><th>Load</th></tr>`)
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, `<tr><td>node-%02d</td><td>0.%d</td></tr>`, i, i%9)
	}
	b.WriteString(`</table><div class="card"><footer><dl><dt>Cluster</dt><dd>prod-eu</dd><dt>Updated</dt><dd>5m ago</dd></dl></footer></div></div>` +
		`<footer>© example</footer></body></html>`)
	out, ok := compressors.NewHTML().Compress([]byte(b.String()))
	if !ok {
		t.Fatal("expected the structural view")
	}
	s := string(out)
	if !strings.Contains(s, "Cluster: prod-eu") || !strings.Contains(s, "Updated: 5m ago") {
		t.Errorf("a footer inside the content must be kept:\n%s", s)
	}
	if strings.Contains(s, "© example") {
		t.Errorf("the page footer must be dropped:\n%s", s)
	}
}

func TestHTMLARIAGridWithNestedRowsIsNotTabulated(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><body><div><table><tr><th>Bucket</th><th>Objects</th></tr><tr><td>logs</td><td>4210</td></tr></table>` +
		`<div role="treegrid"><div role="row"><div role="columnheader">Path</div><div role="columnheader">Size</div></div>`)
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&b, `<div role="row"><div role="gridcell">dir%[1]d</div><div role="gridcell">%[1]d</div>`+
			`<div role="group"><div role="row"><div role="gridcell">dir%[1]d/secret.txt</div><div role="gridcell">99</div></div></div></div>`, i)
	}
	b.WriteString(`<div class="footer">Showing 1-3 of 4,210 rows</div></div></div></body></html>`)
	out, ok := compressors.NewHTML().Compress([]byte(b.String()))
	if !ok {
		t.Fatal("expected the structural view")
	}
	for _, must := range []string{"dir1/secret.txt", "Showing 1-3 of 4,210 rows"} {
		if !bytes.Contains(out, []byte(must)) {
			t.Errorf("grid content %q was dropped:\n%s", must, out)
		}
	}
}

func TestHTMLInlineWrapperAroundTableIsABlock(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><body><div><table><tr><th>A</th><th>B</th></tr><tr><td>x</td><td>y</td></tr></table>` +
		`<center><table><tr><th>Key</th><th>Value</th></tr>`)
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&b, `<tr><td>k%d</td><td>%d</td></tr>`, i, i)
	}
	b.WriteString(`</table></center></div></body></html>`)
	out, ok := compressors.NewHTML().Compress([]byte(b.String()))
	if !ok {
		t.Fatal("expected the structural view")
	}
	if !bytes.Contains(out, []byte("|Key|Value|\n|---|---|\n|k0|0|")) {
		t.Errorf("a table inside an inline wrapper must stay a table:\n%s", out)
	}
}

func TestHTMLLabelsSitAboveTables(t *testing.T) {
	doc := `<!doctype html><html><body><div><div role="alert"><table><tr><th>Check</th><th>State</th></tr><tr><td>disk</td><td>full</td></tr></table></div>` +
		`<dl>orphan note<dt>Owner</dt><dd>sre</dd></dl></div></body></html>`
	out, ok := compressors.NewHTML().Compress([]byte(doc))
	if !ok {
		t.Fatal("expected the structural view")
	}
	s := string(out)
	if !strings.Contains(s, "[alert]\n|Check|State|") {
		t.Errorf("a label must sit on its own line above a table:\n%s", s)
	}
	if !strings.Contains(s, "orphan note") || !strings.Contains(s, "Owner: sre") {
		t.Errorf("loose text in a <dl> must be kept:\n%s", s)
	}
}
