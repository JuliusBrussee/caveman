package engine_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine"
)

func TestDetectHTML(t *testing.T) {
	e := engine.New(nil, nil)
	html := "<!doctype html><html><body><article><p>real article prose with enough text here</p></article></body></html>"
	if got := e.Detect([]byte(html)); got != engine.TypeHTML {
		t.Errorf("Detect(html) = %q, want %q", got, engine.TypeHTML)
	}
}

func TestDetectJSXRoutesToCode(t *testing.T) {
	e := engine.New(nil, nil)
	// A React component is `<`-dense code; it must route to code, not html, or the
	// HTML extractor would shred the source.
	jsx := "import React from 'react'\n\n" +
		"export function Card({ title, items }) {\n" +
		"  return (\n" +
		"    <div className=\"card\">\n" +
		"      <h2>{title}</h2>\n" +
		"      <ul>{items.map((it) => (<li key={it.id}>{it.name}</li>))}</ul>\n" +
		"    </div>\n" +
		"  )\n}\n"
	if got := e.Detect([]byte(jsx)); got == engine.TypeHTML {
		t.Errorf("JSX must NOT route to html (would corrupt the source), got %q", got)
	}
}

func TestDetectGoWithHTMLStringsNotHTML(t *testing.T) {
	e := engine.New(nil, nil)
	src := "package render\n\nfunc wrap(s string) string {\n\treturn \"<div><p>\" + s + \"</p></div>\"\n}\n\nfunc tag() string {\n\treturn \"<span>x</span>\"\n}\n"
	if got := e.Detect([]byte(src)); got == engine.TypeHTML {
		t.Errorf("Go source embedding HTML strings must not route to html, got %q", got)
	}
}

func TestDetectLogWithCodeKeywords(t *testing.T) {
	e := engine.New(nil, nil)
	// A log whose messages contain code keywords (return/class/func) must route to
	// the log compressor, not the code one — the precedence guard.
	log := "2026-01-01T00:00:00Z [INFO] handler return 200\n" +
		"2026-01-01T00:00:01Z [WARN] class loader slow\n" +
		"2026-01-01T00:00:02Z [ERROR] func dispatch failed\n" +
		"2026-01-01T00:00:03Z [INFO] return to pool\n"
	if got := e.Detect([]byte(log)); got != engine.TypeLog {
		t.Errorf("Detect(log-with-code-keywords) = %q, want %q", got, engine.TypeLog)
	}
}

func TestDetectHTMLDocumentWithStrayCarriageReturns(t *testing.T) {
	e := engine.New(nil, nil)
	// A saved page with CRLF line ends and a few bare carriage returns (as a
	// status page served one) is still a page. The bare-CR terminal signal must
	// not outrank the document declaration.
	page := "<!DOCTYPE html>\r\n<html lang=\"en\">\r\n<head><title>Status</title></head>\r\n<body>\r\n" +
		"<div class=\"component\">API\rOperational</div>\r\n<div class=\"component\">Git\rOperational</div>\r\n" +
		"<div class=\"component\">Pages\rOperational</div>\r\n<div class=\"component\">Actions\rDegraded</div>\r\n</body></html>\r\n"
	if got := e.Detect([]byte(page)); got != engine.TypeHTML {
		t.Errorf("Detect(page with stray CRs) = %q, want %q", got, engine.TypeHTML)
	}
}

func TestDetectHTMLDeclarationNeedsATagBoundary(t *testing.T) {
	e := engine.New(nil, nil)
	if got := e.Detect([]byte("<htmlReport version=\"1\">\n  <case name=\"a\"/>\n</htmlReport>\n")); got == engine.TypeHTML {
		t.Errorf("<htmlReport> is not an <html> tag, got %q", got)
	}
	// An ANSI escape stays conclusive: a terminal capture that happens to start
	// by printing a doctype is still terminal output.
	capture := "<!doctype html>\n\x1b[31mERROR\x1b[0m render failed\n\x1b[32mok\x1b[0m retry\n"
	if got := e.Detect([]byte(capture)); got != engine.TypeTerminal {
		t.Errorf("Detect(ANSI capture) = %q, want %q", got, engine.TypeTerminal)
	}
}

func TestHTMLDataPageCompressesAndRecoversExactly(t *testing.T) {
	var b strings.Builder
	b.WriteString("<!doctype html><html><head><title>Coverage</title></head><body><div><table>" +
		"<thead><tr><th>File</th><th>Stmts</th><th>Miss</th><th>Cover</th></tr></thead><tbody>")
	for i := 0; i < 300; i++ {
		cover := "100%"
		if i == 211 {
			cover = "12%"
		}
		fmt.Fprintf(&b, "<tr><td><a href=\"f%d.html\">src/pkg/file_%03d.py</a></td><td>40</td><td>0</td><td>%s</td></tr>\n", i, i, cover)
	}
	b.WriteString("</tbody></table></div></body></html>")
	page := []byte(b.String())

	e := newEngine(t)
	res, err := e.Compress(page, engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		t.Fatal(err)
	}
	if res.PassedThrough() || res.ContentType != engine.TypeHTML {
		t.Fatalf("expected the html compressor to shrink a coverage table, got type %q passed-through=%v", res.ContentType, res.PassedThrough())
	}
	if !bytes.Contains(res.Output, []byte("|src/pkg/file_211.py|40|0|12%|")) {
		t.Errorf("the one uncovered file must stay visible:\n%s", res.Output)
	}
	original, err := e.Retrieve(res.RecoveryHandle)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, page) {
		t.Error("recovery must return the original page byte for byte")
	}
}
