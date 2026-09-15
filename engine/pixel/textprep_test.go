// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const testCols = 100
const rowsPerImage = 90

func TestEstimateImageCount(t *testing.T) {
	if EstimateImageCount("", testCols, 1, ReadableCharsPerImage, conservativeStdParams) != 1 {
		t.Fatal("empty estimate != 1")
	}
	oneImage := strings.Join(repeatString("x", rowsPerImage), "\n")
	if EstimateImageCount(oneImage, testCols, 1, ReadableCharsPerImage, conservativeStdParams) != 1 {
		t.Fatal("one short-line image estimate mismatch")
	}
	justOver := strings.Join(repeatString("x", rowsPerImage+1), "\n")
	if EstimateImageCount(justOver, testCols, 1, ReadableCharsPerImage, conservativeStdParams) != 2 {
		t.Fatal("short-line spill estimate mismatch")
	}
	if EstimateImageCount(strings.Repeat("x", testCols*rowsPerImage+1), testCols, 1, ReadableCharsPerImage, conservativeStdParams) != 2 {
		t.Fatal("soft-wrap spill estimate mismatch")
	}
	if EstimateImageCount(strings.Join(repeatString("x", rowsPerImage*10), "\n"), testCols, 1, ReadableCharsPerImage, conservativeStdParams) != 10 {
		t.Fatal("ten image estimate mismatch")
	}
}

func repeatString(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

func TestClassifyContent(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"json object", `{"foo":"bar"}`, "structured"},
		{"json array", `[{"a":1},{"b":2}]`, "structured"},
		{"yaml", "---\ntitle: foo\n---\nbody", "structured"},
		{"diff", "diff --git a/foo.ts b/foo.ts\n--- a/foo.ts\n+++ b/foo.ts", "structured"},
		{"iso log", strings.Join(logLines("2026-05-18T12:00:%02dZ line"), "\n"), "log"},
		{"level log", strings.Join(logLines("[INFO] line %02d"), "\n"), "log"},
		{"time log", strings.Join(logLines("12:00:%02d event"), "\n"), "log"},
		{"stack trace", "[ERROR] something went wrong\n  at foo()\n  at bar()\n  at baz()", "other"},
		{"prose", strings.Repeat("The quick brown fox jumps.\n", 20), "other"},
		{"short", "one\ntwo\nthree", "other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyContent(tc.text); got != tc.want {
				t.Fatalf("ClassifyContent() = %q, want %q", got, tc.want)
			}
		})
	}
}

func logLines(format string) []string {
	out := make([]string, 20)
	for i := range out {
		out[i] = fmt.Sprintf(format, i)
	}
	return out
}

func TestTruncateForBudget(t *testing.T) {
	text := strings.Repeat("x", 1000)
	out, omitted, truncated := TruncateForBudget(text, 10, testCols, 1, DenseContentCharsPerImage, conservativeStdParams)
	if truncated || omitted != 0 || out != text {
		t.Fatal("under-budget text was truncated")
	}

	var lines []string
	for i := 0; i < 10000; i++ {
		lines = append(lines, fmt.Sprintf("2026-05-18T12:00:%02dZ entry %d", i%60, i))
	}
	log := strings.Join(lines, "\n")
	out, omitted, truncated = TruncateForBudget(log, 10, testCols, 1, DenseContentCharsPerImage, conservativeStdParams)
	if !truncated || omitted <= 0 {
		t.Fatal("log did not truncate")
	}
	if EstimateImageCount(out, testCols, 1, DenseContentCharsPerImage, conservativeStdParams) > 10 {
		t.Fatalf("truncated log estimate = %d, want <=10", EstimateImageCount(out, testCols, 1, DenseContentCharsPerImage, conservativeStdParams))
	}
	if !strings.Contains(out, "pxpipe paging:") || !strings.Contains(out, "entry 0\n") || !strings.Contains(out, "entry 9999") {
		t.Fatal("log marker/head/tail missing")
	}

	items := make([]string, 5000)
	for i := range items {
		items[i] = fmt.Sprintf(`  {"id": %d, "name": "item-%d", "payload": "%s"}`, i, i, strings.Repeat("x", 100))
	}
	jsonish := "[\n" + strings.Join(items, ",\n") + "\n]"
	out, omitted, truncated = TruncateForBudget(jsonish, 10, testCols, 1, DenseContentCharsPerImage, conservativeStdParams)
	if !truncated || omitted <= 0 {
		t.Fatal("JSON did not truncate")
	}
	if !strings.Contains(out, "tail elided") || strings.Contains(out, "item-4999") || !strings.Contains(out, `"item-0"`) {
		t.Fatal("structured truncation shape mismatch")
	}

	prose := strings.Repeat("The quick brown fox jumps over the lazy dog and goes home for dinner.\n", 8000)
	out, omitted, truncated = TruncateForBudget(prose, 10, testCols, 1, DenseContentCharsPerImage, conservativeStdParams)
	if !truncated || omitted <= 0 || !strings.Contains(out, "last ") {
		t.Fatal("prose head/tail truncation mismatch")
	}
}

func TestPagingMarkerNumbersAndDegenerate(t *testing.T) {
	lines := make([]string, 10000)
	for i := range lines {
		lines[i] = fmt.Sprintf("2026-05-18T12:00:00Z logline %d something", i)
	}
	log := strings.Join(lines, "\n")
	out, omitted, _ := TruncateForBudget(log, 10, testCols, 1, DenseContentCharsPerImage, conservativeStdParams)
	omittedRe := regexp.MustCompile(`omitted ([\d,]+) lines \(([\d,]+) chars\)`)
	originalRe := regexp.MustCompile(`Original length: ([\d,]+) chars \(([\d,]+) lines`)
	om := omittedRe.FindStringSubmatch(out)
	orig := originalRe.FindStringSubmatch(out)
	if om == nil || orig == nil {
		t.Fatal("marker numbers missing")
	}
	if parseComma(orig[1]) != jsLen(log) || parseComma(orig[2]) != len(lines) {
		t.Fatal("original marker numbers mismatch")
	}
	if parseComma(om[2]) != omitted {
		t.Fatalf("omitted chars marker = %d, want %d", parseComma(om[2]), omitted)
	}
	degenerate, _, truncated := TruncateForBudget(strings.Repeat("x", 500000), 10, testCols, 1, DenseContentCharsPerImage, conservativeStdParams)
	if truncated && !strings.Contains(degenerate, "pxpipe paging:") {
		t.Fatal("degenerate truncation missing marker")
	}
}

func parseComma(s string) int {
	n, err := strconv.Atoi(strings.ReplaceAll(s, ",", ""))
	if err != nil {
		panic(err)
	}
	return n
}

func TestCompactSlabWhitespace(t *testing.T) {
	cases := map[string]string{
		"":                       "",
		"alpha  \nbeta\t\n":      "alpha\nbeta\n",
		"a\n\n\nb":               "a\n\nb",
		"a\n\nb":                 "a\n\nb",
		"  f() {\n\treturn\n  }": "  f() {\n\treturn\n  }",
	}
	for in, want := range cases {
		if got := CompactSlabWhitespace(in); got != want {
			t.Fatalf("CompactSlabWhitespace(%q) = %q, want %q", in, got, want)
		}
	}
}
