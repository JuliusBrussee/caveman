package compressors_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/compressors"
)

// bibliographyLike is a document whose every section is distinct and which an
// agent may need to audit exhaustively. Eliding any of it deletes part of the
// answer.
func bibliographyLike() []byte {
	entries := []string{
		"@article{Jumper2021,\n  author  = {Jumper, John and Evans, Richard},\n  title   = {Highly Accurate Protein Structure Prediction with AlphaFold},\n  journal = {Nature},\n  year    = {2021}",
		"@inproceedings{patel2023blockchain,\n  author = {Aisha Patel and Carlos Ramirez},\n  title = {Blockchain Applications in Supply Chain Management},\n  booktitle = {International Conference on Information Systems},\n  year = {2023}",
		"@article{Watson1953,\n  author  = {Watson, James D. and Crick, Francis H. C.},\n  title   = {Molecular Structure of Nucleic Acids},\n  journal = {Nature},\n  year    = {1953}",
		"@inproceedings{lila,\n  author = {Swaroop Mishra and Matthew Finlayson},\n  title = {LILA: A Unified Benchmark for Mathematical Reasoning},\n  booktitle = {EMNLP},\n  year = {2022}",
		"@article{Doudna2014,\n  author  = {Doudna, Jennifer A. and Charpentier, Emmanuelle},\n  title   = {The New Frontier of Genome Engineering with CRISPR-Cas9},\n  journal = {Science},\n  year    = {2014}",
		"@inproceedings{wilson2021neural,\n  author = {Emma Wilson and David Chen},\n  title = {Neural Networks in Deep Learning: A Comprehensive Review},\n  booktitle = {Machine Learning Symposium},\n  year = {2021}",
		"@article{Vaswani2017,\n  author  = {Vaswani, Ashish and Shazeer, Noam},\n  title   = {Attention Is All You Need},\n  journal = {NeurIPS},\n  year    = {2017}",
		"@inproceedings{smith2020ai,\n  author = {John Smith and Mary Jones},\n  title = {Advances in Artificial Intelligence for Natural Language Processing},\n  booktitle = {AI Conference},\n  year = {2020}",
		"@article{He2016,\n  author  = {He, Kaiming and Zhang, Xiangyu},\n  title   = {Deep Residual Learning for Image Recognition},\n  journal = {CVPR},\n  year    = {2016}",
		"@inproceedings{joshi2017triviaqa,\n  author = {Joshi, Mandar and Choi, Eunsol},\n  title = {TriviaQA: A Large Scale Distantly Supervised Challenge Dataset},\n  booktitle = {ACL},\n  year = {2017}",
	}
	return []byte(strings.Join(entries, "\n\n"))
}

// assertNothingElided checks the property that matters: not a byte of the payload
// was dropped. Some compressors report ok=true while returning their input
// unchanged and let the engine's "not actually smaller" check pass it through, so
// asserting on the ok flag alone would test the wrong layer.
func assertNothingElided(t *testing.T, input, out []byte, ok bool) {
	t.Helper()
	if ok && !bytes.Equal(out, input) {
		t.Fatalf("expected pass-through, but %d bytes became %d:\n%s", len(input), len(out), out)
	}
}

// A document of distinct records must survive intact. Eliding it deletes part of
// the answer and can force the agent to recover the source repeatedly.
func TestElisionRefusesDocumentOfDistinctRecords(t *testing.T) {
	input := bibliographyLike()
	out, ok := compressors.NewText().Compress(input)
	assertNothingElided(t, input, out, ok)
}

// The complement, and the reason this is a class rule rather than a refusal: a
// payload that is mostly repetition still elides, and the repeated class stays
// visible through one surviving representative.
func TestElisionKeepsOneRepresentativeOfEachClass(t *testing.T) {
	var sections []string
	sections = append(sections, "# Incident Report", "Summary: checkout latency increased.")
	for i := 0; i < 18; i++ {
		sections = append(sections, strings.Repeat(fmt.Sprintf("Background paragraph %02d with routine context. ", i), 8))
	}
	sections = append(sections, "Conclusion: monitor error rate for one hour.")
	input := []byte(strings.Join(sections, "\n\n"))

	out, ok := compressors.NewText().Compress(input)
	if !ok {
		t.Fatal("a payload of eighteen interchangeable paragraphs must still elide")
	}
	if len(out) >= len(input) {
		t.Fatalf("expected smaller output, got %d >= %d", len(out), len(input))
	}
	if !bytes.Contains(out, []byte("sections elided")) {
		t.Fatalf("expected an elision marker:\n%s", out)
	}
	survivors := 0
	for _, section := range bytes.Split(out, []byte("\n\n")) {
		if bytes.Contains(section, []byte("Background paragraph")) {
			survivors++
		}
	}
	if survivors != 1 {
		t.Fatalf("exactly one representative of the repeated class must survive, got %d:\n%s", survivors, out)
	}
}

// Repetition is recognised through the values that vary between copies, not
// despite them: two log lines differing only in a timestamp and a request id are
// the same class, so all but the first elide.
func TestElisionTreatsDigitVaryingLinesAsOneClass(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "2026-07-30T10:%02d:%02dZ level=INFO service=catalog request=req-%04d status=200 message=completed\n", i/60, i%60, i)
	}
	out, ok := compressors.NewLog().Compress([]byte(b.String()))
	if !ok {
		t.Fatal("expected repetitive log lines to elide")
	}
	if len(out)*4 > len(b.String()) {
		t.Fatalf("expected a large reduction on a single-class log, got %d of %d bytes", len(out), b.Len())
	}
}

// Distinct log lines are a document, not a stream, and must survive.
func TestElisionRefusesLogOfDistinctLines(t *testing.T) {
	lines := []string{
		"2026-07-30T10:00:01Z level=INFO starting migration of the accounts table",
		"2026-07-30T10:00:02Z level=INFO connected to primary replica in frankfurt",
		"2026-07-30T10:00:03Z level=INFO applying column rename for customer identifiers",
		"2026-07-30T10:00:04Z level=INFO backfilling nullable settlement references",
		"2026-07-30T10:00:05Z level=INFO rebuilding the composite ordering index",
		"2026-07-30T10:00:06Z level=INFO vacuuming dead tuples from the audit journal",
		"2026-07-30T10:00:07Z level=INFO switching traffic to the upgraded schema",
		"2026-07-30T10:00:08Z level=INFO retiring the legacy compatibility view",
		"2026-07-30T10:00:09Z level=INFO refreshing materialised revenue rollups",
		"2026-07-30T10:00:10Z level=INFO releasing the advisory migration lock",
		"2026-07-30T10:00:11Z level=INFO notifying downstream consumers of the cutover",
		"2026-07-30T10:00:12Z level=INFO archiving the pre-migration snapshot to cold storage",
	}
	input := []byte(strings.Join(lines, "\n"))
	out, ok := compressors.NewLog().Compress(input)
	assertNothingElided(t, input, out, ok)
}

// The array counterpart of the bibliography: rows that each say something
// different are a list of distinct entities, and dropping most of them deletes
// the answer to any question about the list.
func TestJSONElisionRefusesArrayOfDistinctRecords(t *testing.T) {
	input := []byte(`{"results":[` +
		`{"id":0,"name":"alice","team":"platform"},{"id":1,"name":"bob","team":"payments"},` +
		`{"id":2,"name":"carol","team":"identity"},{"id":3,"name":"dan","team":"search"},` +
		`{"id":4,"name":"erin","team":"checkout"},{"id":5,"name":"frank","team":"billing"},` +
		`{"id":6,"name":"grace","team":"catalog"},{"id":7,"name":"heidi","team":"fraud"},` +
		`{"id":8,"name":"ivan","team":"growth"},{"id":9,"name":"judy","team":"support"},` +
		`{"id":10,"name":"karl","team":"infra"},{"id":11,"name":"lena","team":"security"}]}`)
	out, ok := compressors.NewJSON().Compress(input)
	assertNothingElided(t, input, out, ok)
}

// A uniform array of the same event repeated is the shape elision exists for, and
// it must keep working.
func TestJSONElisionStillCollapsesRepeatedRecords(t *testing.T) {
	rows := make([]string, 0, 200)
	for i := 0; i < 200; i++ {
		rows = append(rows, fmt.Sprintf(`{"seq":%d,"status":"healthy","region":"eu-west-1"}`, i))
	}
	input := []byte(`{"events":[` + strings.Join(rows, ",") + `]}`)
	out, ok := compressors.NewJSON().Compress(input)
	if !ok || len(out) >= len(input) {
		t.Fatalf("a uniform array of repeated events must still elide (ok=%v, %d -> %d)", ok, len(input), len(out))
	}
	if !bytes.Contains(out, []byte(compressors.ElidedKey)) {
		t.Fatalf("expected an elision marker:\n%s", out)
	}
}

// The gate runs on the proxy hot path, so its cost has to stay bounded on the
// worst realistic input: a large payload with no repetition at all, where every
// unit is a candidate for promotion.
func BenchmarkElisionGateOnLargeDistinctPayload(b *testing.B) {
	var sb strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&sb, "line %d reporting a wholly distinct observation about subsystem %s with detail %s\n",
			i, strings.Repeat("x", i%17+1), strings.Repeat("y", i%23+1))
	}
	input := []byte(sb.String())
	b.ReportAllocs()
	for b.Loop() {
		compressors.NewLog().Compress(input)
	}
}

// A compressed block re-serializes on every later turn of a conversation; if the
// gate were order- or map-dependent the upstream prefix would rotate and every
// turn would miss the provider cache the previous turn paid to create.
func TestElisionIsDeterministic(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&b, "worker-%02d processed batch %04d in %dms with status ok\n", i%7, i, 12+i%40)
	}
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, "distinct finding %d: %s\n", i, strings.Repeat("x", i+1))
	}
	input := []byte(b.String())

	first, ok := compressors.NewLog().Compress(input)
	if !ok {
		t.Skip("payload did not elide; determinism is trivially held")
	}
	for i := 0; i < 5; i++ {
		again, ok := compressors.NewLog().Compress(input)
		if !ok || !bytes.Equal(first, again) {
			t.Fatalf("elision must be a pure function of content (run %d differed)", i)
		}
	}
}
