package compressors

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// A single CRLF anywhere used to decide the line ending for the whole document:
// terminal/config/tabular rewrote every LF to CRLF, and log/searchresult/diff
// split the whole payload on "\r\n" and so saw one giant line and compressed
// nothing.

func TestTerminalKeepsMixedLineEndings(t *testing.T) {
	var b strings.Builder
	b.WriteString("HTTP/1.1 200 OK\r\n") // what `curl -i` emits, inside an LF capture
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "line %d of ordinary unix output\n", i)
	}
	in := []byte(b.String())

	out, ok := NewTerminal().Compress(in)
	if !ok {
		t.Fatal("expected the capture to compress")
	}
	if got, want := bytes.Count(out, []byte("\r\n")), 1; got != want {
		t.Errorf("CRLF count = %d, want %d — LF lines were rewritten: %q", got, want, out)
	}
}

func TestLogCompressesMixedLineEndings(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 200; i++ {
		if i%40 == 0 {
			fmt.Fprintf(&b, "worker %d: waiting for the queue to drain\r\n", i)
			continue
		}
		fmt.Fprintf(&b, "worker %d: waiting for the queue to drain\n", i)
	}
	in := []byte(b.String())

	out, ok := NewLog().Compress(in)
	if !ok {
		t.Fatal("mixed-ending log did not compress at all")
	}
	if len(out) >= len(in) {
		t.Errorf("output %d bytes, input %d — no reduction", len(out), len(in))
	}
}

func TestDiffKeepsChangeLinesStartingWithDash(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/doc.md b/doc.md\n")
	b.WriteString("--- a/doc.md\n")
	b.WriteString("+++ b/doc.md\n")
	b.WriteString("@@ -1,30 +1,29 @@\n")
	for i := 0; i < 24; i++ {
		fmt.Fprintf(&b, " context line %d that is unchanged\n", i)
	}
	b.WriteString("----\n") // deleting a markdown rule renders as four dashes
	for i := 0; i < 24; i++ {
		fmt.Fprintf(&b, " trailing context line %d\n", i)
	}
	in := []byte(b.String())

	out, ok := NewDiff().Compress(in)
	if !ok {
		t.Fatal("expected the diff to compress")
	}
	if !bytes.Contains(out, []byte("----")) {
		t.Errorf("the only changed line was elided as context: %q", out)
	}
}

func TestTextKeepsFencedBlockWhole(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&b, "Paragraph %d of surrounding prose that carries no signal at all and exists only to give the compressor something it is willing to drop.\n\n", i)
	}
	b.WriteString("```python\ndef handler(event):\n    payload = parse(event)\n\n    return respond(payload)\n```\n\n")
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&b, "Trailing paragraph %d of surrounding prose that carries no signal at all and exists only to pad the document out.\n\n", i)
	}
	in := []byte(b.String())

	sections := splitTextSections(in)
	for _, s := range sections {
		if !bytes.HasPrefix(s, []byte("```")) {
			continue
		}
		if !bytes.Contains(s, []byte("return respond(payload)")) {
			t.Fatalf("fenced block was split at its blank line: %q", s)
		}
		if !bytes.Contains(s, []byte("    payload = parse(event)")) {
			t.Errorf("fenced block lost its indentation: %q", s)
		}
		return
	}
	t.Fatal("no fenced section found")
}
