package compressors_test

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/compressors"
	"github.com/JuliusBrussee/caveman/engine/safety"
)

func csvFixture() []byte {
	var b strings.Builder
	b.WriteString("name,region,revenue,status\n")
	for i := 0; i < 80; i++ {
		status := "ok"
		if i == 43 {
			status = "FAILED chargeback review"
		}
		fmt.Fprintf(&b, "customer-%02d,%s,%d,%s\n", i, []string{"eu", "us", "apac"}[i%3], 100+i, status)
	}
	return []byte(b.String())
}

func TestTabularCSVStaysValidAndKeepsHeaderExtremaAndErrors(t *testing.T) {
	c := compressors.NewTabular()
	if c.SafetyClass() != safety.S4 {
		t.Fatal("tabular compressor must be S4")
	}
	out, ok := c.Compress(csvFixture())
	if !ok {
		t.Fatal("expected CSV compression")
	}
	if len(out) >= len(csvFixture()) {
		t.Fatalf("expected smaller output, got %d >= %d", len(out), len(csvFixture()))
	}
	rows, err := csv.NewReader(bytes.NewReader(out)).ReadAll()
	if err != nil {
		t.Fatalf("compressed CSV must remain valid: %v\n%s", err, out)
	}
	if got := rows[0]; len(got) != 4 || got[0] != "name" || got[2] != "revenue" {
		t.Fatalf("header changed: %#v", got)
	}
	for _, want := range [][]byte{
		[]byte("customer-00"),
		[]byte("customer-43"),
		[]byte("FAILED chargeback review"),
		[]byte("customer-79"),
		[]byte("rows elided (caveman)"),
	} {
		if !bytes.Contains(out, want) {
			t.Fatalf("compressed CSV missing %q:\n%s", want, out)
		}
	}
}

func TestTabularQueryKeepsRelevantMiddleRows(t *testing.T) {
	c, ok := compressors.NewTabular().(compressors.QueryAwareCompressor)
	if !ok {
		t.Fatal("tabular compressor must be query-aware")
	}
	input := csvFixture()
	out, ok := c.CompressQuery(input, "customer-37 apac")
	if !ok {
		t.Fatal("expected query-aware CSV compression")
	}
	if !bytes.Contains(out, []byte("customer-37")) {
		t.Fatalf("query-relevant row missing:\n%s", out)
	}
	if len(out) >= len(input) {
		t.Fatal("query-aware output must still shrink")
	}
}

func TestTabularTSVStaysValidAndKeepsQueryRow(t *testing.T) {
	var b strings.Builder
	b.WriteString("name\tregion\trevenue\tstatus\n")
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&b, "customer-%02d\t%s\t%d\tok\n", i, []string{"eu", "us", "apac"}[i%3], 100+i)
	}
	input := []byte(b.String())
	c := compressors.NewTabular().(compressors.QueryAwareCompressor)
	out, ok := c.CompressQuery(input, "customer-31 us")
	if !ok {
		t.Fatal("expected TSV compression")
	}
	reader := csv.NewReader(bytes.NewReader(out))
	reader.Comma = '\t'
	rows, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("compressed TSV must remain valid: %v\n%s", err, out)
	}
	if len(rows) >= 50 || !bytes.Contains(out, []byte("customer-31")) {
		t.Fatalf("TSV did not shrink around query row:\n%s", out)
	}
	for _, row := range rows {
		if len(row) != 4 {
			t.Fatalf("TSV row has %d fields, want 4: %#v", len(row), row)
		}
	}
}

func TestTabularMarkdownKeepsShapeAndIsIdempotent(t *testing.T) {
	var b strings.Builder
	b.WriteString("| name | region | revenue |\n| --- | --- | ---: |\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "| customer-%02d | %s | %d |\n", i, []string{"eu", "us"}[i%2], 100+i)
	}
	c := compressors.NewTabular()
	first, ok := c.Compress([]byte(b.String()))
	if !ok {
		t.Fatal("expected Markdown table compression")
	}
	if !bytes.Contains(first, []byte("| --- | --- | ---: |")) {
		t.Fatalf("separator row missing:\n%s", first)
	}
	if !bytes.Contains(first, []byte("rows elided (caveman)")) {
		t.Fatalf("marker missing:\n%s", first)
	}
	if second, ok := c.Compress(first); ok && !bytes.Equal(first, second) {
		t.Fatalf("not idempotent:\nfirst=%s\nsecond=%s", first, second)
	}
}

func TestTabularMalformedOrTinyPassesThrough(t *testing.T) {
	c := compressors.NewTabular()
	for _, input := range [][]byte{
		[]byte("a,b\n1,2\n"),
		[]byte("a,b\n1,2\n3,\"unterminated\n4,5\n"),
		{0xff, 0xfe, 0xfd},
	} {
		if out, ok := c.Compress(input); ok {
			t.Fatalf("unsafe input must pass through, got %q", out)
		}
	}
}

func TestTabularCSVMarkerHasStableFieldCount(t *testing.T) {
	c := compressors.NewTabular()
	out, ok := c.Compress(csvFixture())
	if !ok {
		t.Fatal("expected compression")
	}
	r := csv.NewReader(bytes.NewReader(out))
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(row) != 4 {
			t.Fatalf("row has %d fields, want 4: %#v", len(row), row)
		}
	}
}

func TestTabularCSVPreservesCRLFWithoutAddingTrailingNewline(t *testing.T) {
	input := bytes.ReplaceAll(bytes.TrimSuffix(csvFixture(), []byte("\n")), []byte("\n"), []byte("\r\n"))
	out, ok := compressors.NewTabular().Compress(input)
	if !ok {
		t.Fatal("expected CSV compression")
	}
	if bytes.HasSuffix(out, []byte{'\r'}) || bytes.HasSuffix(out, []byte{'\n'}) {
		t.Fatalf("unexpected trailing newline bytes: %q", out[len(out)-4:])
	}
	if bytes.Contains(bytes.ReplaceAll(out, []byte("\r\n"), nil), []byte("\n")) {
		t.Fatal("compressed CSV mixed newline styles")
	}
}
