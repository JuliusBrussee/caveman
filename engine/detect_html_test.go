package engine_test

import (
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
