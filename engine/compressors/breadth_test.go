package compressors_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/compressors"
	"github.com/JuliusBrussee/caveman/engine/safety"
)

func TestDiffCompressorKeepsChangesAndElidesContext(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/app.go b/app.go\nindex 111..222 100644\n--- a/app.go\n+++ b/app.go\n@@ -1,30 +1,30 @@\n")
	for i := 0; i < 18; i++ {
		fmt.Fprintf(&b, " unchanged line %02d\n", i)
	}
	b.WriteString("-old behavior\n+new behavior\n")
	for i := 18; i < 36; i++ {
		fmt.Fprintf(&b, " unchanged line %02d\n", i)
	}

	c := compressors.NewDiff()
	if c.SafetyClass() != safety.S4 {
		t.Fatal("diff compressor must be S4")
	}
	out, ok := c.Compress([]byte(b.String()))
	if !ok {
		t.Fatal("expected diff compression")
	}
	if len(out) >= b.Len() {
		t.Fatalf("expected smaller output, got %d >= %d", len(out), b.Len())
	}
	for _, want := range [][]byte{[]byte("@@ -1,30"), []byte("-old behavior"), []byte("+new behavior"), []byte("context lines elided")} {
		if !bytes.Contains(out, want) {
			t.Fatalf("compressed diff missing %q:\n%s", want, out)
		}
	}
}

// A bare "-" or "+" is how git renders the deletion or addition of a blank
// line. It was elided as context under a marker claiming only context was
// dropped — the change vanished with nothing saying so.
func TestDiffCompressorKeepsBlankLineChanges(t *testing.T) {
	for _, change := range []string{"-", "+"} {
		var b strings.Builder
		b.WriteString("diff --git a/app.go b/app.go\nindex 111..222 100644\n--- a/app.go\n+++ b/app.go\n@@ -1,30 +1,30 @@\n")
		for i := 0; i < 18; i++ {
			fmt.Fprintf(&b, " unchanged line %02d\n", i)
		}
		b.WriteString(change + "\n")
		for i := 18; i < 36; i++ {
			fmt.Fprintf(&b, " unchanged line %02d\n", i)
		}

		out, ok := compressors.NewDiff().Compress([]byte(b.String()))
		if !ok {
			continue // pass-through keeps the change by definition
		}
		if !bytes.Contains(out, []byte("\n"+change+"\n")) {
			t.Fatalf("compressed diff dropped the bare %q change line:\n%s", change, out)
		}
	}
}

// A file whose last line has no trailing newline is annotated by git with
// "\ No newline at end of file". It is metadata, not context, but it landed
// outside the keep window and was replaced by an elision marker — the diff
// stopped saying that the file is unterminated.
func TestDiffCompressorKeepsNoNewlineMarker(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/app.go b/app.go\nindex 111..222 100644\n--- a/app.go\n+++ b/app.go\n@@ -1,20 +1,20 @@\n unchanged line 01\n unchanged line 02\n")
	b.WriteString("-old_value = 1\n+new_value = 2\n")
	for i := 3; i < 12; i++ {
		fmt.Fprintf(&b, " unchanged line %02d\n", i)
	}
	b.WriteString("\\ No newline at end of file\n")

	out, ok := compressors.NewDiff().Compress([]byte(b.String()))
	if !ok {
		t.Fatal("expected diff compression")
	}
	if !bytes.Contains(out, []byte("\\ No newline at end of file")) {
		t.Fatalf("compressed diff dropped the no-newline marker:\n%s", out)
	}
}

// A pure rename is rendered as "diff --git" + "similarity index" +
// "rename from" + "rename to". The ±2 keep window around "diff --git" covers
// only the first three lines, so the new path — the whole point of the block —
// was elided as context.
func TestDiffCompressorKeepsRenameTarget(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/old_name.py b/new_name.py\nsimilarity index 100%\nrename from old_name.py\nrename to new_name.py\n")
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&b, " unchanged line %02d\n", i)
	}

	out, ok := compressors.NewDiff().Compress([]byte(b.String()))
	if !ok {
		t.Fatal("expected diff compression")
	}
	for _, want := range []string{"rename from old_name.py", "rename to new_name.py"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Fatalf("compressed diff dropped %q:\n%s", want, out)
		}
	}
}

// A hunk line whose own text starts with a header word is still a hunk line —
// it keeps its leading ' ', '+' or '-' — so the extended-header prefixes must
// not capture it.
func TestDiffCompressorKeepsHunkLinesThatLookLikeHeaders(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/notes.md b/notes.md\nindex 111..222 100644\n--- a/notes.md\n+++ b/notes.md\n@@ -1,20 +1,20 @@\n")
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&b, " unchanged line %02d\n", i)
	}
	b.WriteString("+rename from old\n-new file mode gone\n")
	for i := 9; i < 20; i++ {
		fmt.Fprintf(&b, " unchanged line %02d\n", i)
	}

	out, ok := compressors.NewDiff().Compress([]byte(b.String()))
	if !ok {
		t.Fatal("expected diff compression")
	}
	for _, want := range []string{"\n+rename from old", "\n-new file mode gone"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Fatalf("compressed diff dropped hunk line %q:\n%s", want, out)
		}
	}
}

func TestSearchResultCompressorKeepsImportantHits(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 28; i++ {
		text := "ordinary match"
		if i == 15 {
			text = "ACTION investigate credential leak"
		}
		fmt.Fprintf(&b, "src/file_%02d.go:%d:%s\n", i, i+10, text)
	}

	c := compressors.NewSearchResult()
	out, ok := c.Compress([]byte(b.String()))
	if !ok {
		t.Fatal("expected search-result compression")
	}
	if len(out) >= b.Len() {
		t.Fatalf("expected smaller output, got %d >= %d", len(out), b.Len())
	}
	for _, want := range [][]byte{[]byte("src/file_01.go"), []byte("ACTION investigate"), []byte("src/file_28.go"), []byte("search result lines elided")} {
		if !bytes.Contains(out, want) {
			t.Fatalf("compressed search results missing %q:\n%s", want, out)
		}
	}
}

func TestSearchResultQueryKeepsRelevantMiddleHit(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 40; i++ {
		text := "ordinary cache match"
		if i == 23 {
			text = "checkout websocket reconnect state machine"
		}
		fmt.Fprintf(&b, "src/file_%02d.go:%d:%s\n", i, i+10, text)
	}
	c := compressors.NewSearchResult().(compressors.QueryAwareCompressor)
	out, ok := c.CompressQuery([]byte(b.String()), "websocket reconnect")
	if !ok {
		t.Fatal("expected query-aware search compression")
	}
	if !bytes.Contains(out, []byte("src/file_23.go")) {
		t.Fatalf("query-relevant search hit missing:\n%s", out)
	}
}

func TestTextCompressorKeepsHeadingsAndImportantSections(t *testing.T) {
	var sections []string
	sections = append(sections, "# Incident Report", "Summary: checkout latency increased.")
	for i := 0; i < 18; i++ {
		sections = append(sections, strings.Repeat(fmt.Sprintf("Background paragraph %02d with routine context. ", i), 8))
	}
	sections = append(sections, "Decision: roll back the new cache key format immediately.")
	sections = append(sections, strings.Repeat("Appendix detail with low signal. ", 30))
	sections = append(sections, "Conclusion: monitor error rate for one hour.")
	input := strings.Join(sections, "\n\n")

	c := compressors.NewText()
	out, ok := c.Compress([]byte(input))
	if !ok {
		t.Fatal("expected text compression")
	}
	if len(out) >= len(input) {
		t.Fatalf("expected smaller output, got %d >= %d", len(out), len(input))
	}
	for _, want := range [][]byte{[]byte("# Incident Report"), []byte("Decision: roll back"), []byte("Conclusion:"), []byte("sections elided")} {
		if !bytes.Contains(out, want) {
			t.Fatalf("compressed text missing %q:\n%s", want, out)
		}
	}
}

func TestTextQueryKeepsRelevantMiddleSection(t *testing.T) {
	var sections []string
	sections = append(sections, "# Architecture", "Summary: system overview.")
	for i := 0; i < 20; i++ {
		body := strings.Repeat(fmt.Sprintf("Routine section %02d with background material. ", i), 12)
		if i == 13 {
			body = strings.Repeat("Websocket reconnect ownership and backoff state machine. ", 12)
		}
		sections = append(sections, body)
	}
	sections = append(sections, "Conclusion: review complete.")
	input := strings.Join(sections, "\n\n")
	c := compressors.NewText().(compressors.QueryAwareCompressor)
	out, ok := c.CompressQuery([]byte(input), "websocket reconnect backoff")
	if !ok {
		t.Fatal("expected query-aware text compression")
	}
	if !bytes.Contains(out, []byte("Websocket reconnect")) {
		t.Fatalf("query-relevant prose missing:\n%s", out)
	}
}

func TestTextCompressorRejectsTinyOrBinaryInput(t *testing.T) {
	c := compressors.NewText()
	if _, ok := c.Compress([]byte("short prose")); ok {
		t.Fatal("tiny prose must pass through")
	}
	if _, ok := c.Compress([]byte{0xff, 0xfe, 0xfd}); ok {
		t.Fatal("binary input must pass through")
	}
}
