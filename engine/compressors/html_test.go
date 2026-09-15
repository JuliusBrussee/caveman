package compressors_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/compressors"
)

const articleHTML = `<!doctype html>
<html><head><title>Test</title><style>.x{color:red}</style></head>
<body>
<nav class="site-nav"><a href="/">Home</a> <a href="/about">About</a> <a href="/blog">Blog</a> <a href="/contact">Contact</a></nav>
<header class="banner"><a href="/login">Log in</a> <a href="/signup">Sign up</a></header>
<article class="post-content">
<h1>The Title Of The Article</h1>
<p>This is the first substantial paragraph of the article body. It contains several sentences, with commas, and enough text to score well above the threshold the extractor uses.</p>
<p>Here is a second meaningful paragraph that continues the article, again with commas, clauses, and real prose so the container accrues a high content score.</p>
<p>A third paragraph rounds out the body with yet more sentence-level content, commas, and substance for the reader to consume.</p>
</article>
<aside class="sidebar"><a href="/a">Related 1</a><a href="/b">Related 2</a><a href="/c">Related 3</a></aside>
<footer class="site-footer"><a href="/tos">Terms</a> <a href="/privacy">Privacy</a> copyright notice here</footer>
<script>console.log("tracking pixel and analytics noise that should never survive")</script>
</body></html>`

func TestHTMLExtractsArticleDropsBoilerplate(t *testing.T) {
	c := compressors.NewHTML()
	out, ok := c.Compress([]byte(articleHTML))
	if !ok {
		t.Fatal("expected extraction of the article")
	}
	s := string(out)
	// Article content survives.
	for _, must := range []string{"The Title Of The Article", "first substantial paragraph", "third paragraph"} {
		if !strings.Contains(s, must) {
			t.Errorf("article content dropped: %q\n--- got ---\n%s", must, s)
		}
	}
	// Boilerplate and noise are gone.
	for _, gone := range []string{"tracking pixel", "color:red", "Sign up", "Privacy"} {
		if strings.Contains(s, gone) {
			t.Errorf("boilerplate %q should have been dropped:\n%s", gone, s)
		}
	}
	if len(out) >= len(articleHTML) {
		t.Error("expected real compression")
	}
}

func TestHTMLDeterministic(t *testing.T) {
	c := compressors.NewHTML()
	a, ok1 := c.Compress([]byte(articleHTML))
	b, ok2 := c.Compress([]byte(articleHTML))
	if !ok1 || !ok2 {
		t.Fatal("expected extraction")
	}
	if !bytes.Equal(a, b) {
		t.Errorf("non-deterministic:\n a=%s\n b=%s", a, b)
	}
}

func TestHTMLLinkFarmBails(t *testing.T) {
	// A nav/index page that is almost all links must bail (claim nothing) rather
	// than emit a link dump as if it were an article.
	var b strings.Builder
	b.WriteString("<!doctype html><html><body><div class=\"links\">")
	for i := 0; i < 40; i++ {
		b.WriteString(`<p><a href="/x">A link with a little text</a></p>`)
	}
	b.WriteString("</div></body></html>")
	if _, ok := compressors.NewHTML().Compress([]byte(b.String())); ok {
		t.Error("a link-dense page must bail to pass-through")
	}
}

func TestHTMLInlineTagsKeepWordBoundary(t *testing.T) {
	doc := `<!doctype html><html><body><article>
<h1>Pricing Headline Here For The Article Body</h1>
<p>The price is <b>fifty</b><i>dollars</i> total, and you can <a href="/x">read the docs</a>here for more information about everything involved.</p>
<p>A second paragraph with more than enough words and several commas, clauses, and content to score the container well above the extractor threshold here.</p>
</article></body></html>`
	out, ok := compressors.NewHTML().Compress([]byte(doc))
	if !ok {
		t.Fatal("expected extraction")
	}
	s := string(out)
	if strings.Contains(s, "fiftydollars") || strings.Contains(s, "docshere") {
		t.Errorf("inline tags must not join words:\n%s", s)
	}
}

func TestHTMLKeepsAllSections(t *testing.T) {
	doc := `<!doctype html><html><body><main>
<section><h1>INTRO_HEADLINE</h1><p>The introduction section has enough prose, with commas, to score, and it opens the piece with substance for the reader.</p></section>
<section><p>The middle section continues the article with more sentences, commas, and the kind of body text the extractor is built to keep around.</p></section>
<section><p>The outro section closes things out, again with real prose, commas, and a UNIQUE_OUTRO_MARKER token that must survive extraction.</p></section>
</main></body></html>`
	out, ok := compressors.NewHTML().Compress([]byte(doc))
	if !ok {
		t.Fatal("expected extraction")
	}
	s := string(out)
	for _, must := range []string{"INTRO_HEADLINE", "middle section", "UNIQUE_OUTRO_MARKER"} {
		if !strings.Contains(s, must) {
			t.Errorf("multi-section article truncated — %q dropped:\n%s", must, s)
		}
	}
}

func TestHTMLPreservesPreIndentation(t *testing.T) {
	doc := "<!doctype html><html><body><article>" +
		"<h1>Article With A Code Block Inside The Body Here</h1>" +
		"<p>Here is an explanatory paragraph with commas, clauses, and enough text to score the article container well above threshold for the reader.</p>" +
		"<pre>func main() {\n    if x == 1 {\n        run()\n    }\n}</pre>" +
		"</article></body></html>"
	out, ok := compressors.NewHTML().Compress([]byte(doc))
	if !ok {
		t.Fatal("expected extraction")
	}
	if !bytes.Contains(out, []byte("    if x == 1 {")) {
		t.Errorf("<pre> indentation must be preserved:\n%s", out)
	}
}

func TestHTMLNonDocumentBails(t *testing.T) {
	if _, ok := compressors.NewHTML().Compress([]byte("just a tiny <b>fragment</b>")); ok {
		t.Error("a tiny fragment with no real article must pass through")
	}
}
