package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/engine/pixel"
)

// The agent emits the compact TOON form; downstream recovers JSON. The two must
// be semantically identical so nothing the agent omits gets corrupted on the way
// back to a program that needs JSON.
func TestTOONEncodeDecodeRoundTrip(t *testing.T) {
	cases := [][]byte{
		[]byte(`{"users":[{"id":1,"name":"ana"},{"id":2,"name":"sam"}],"team":"hikers"}`),
		[]byte(`["ana","luis","sam"]`),
		[]byte(`{"rows":[{"id":1,"value":"a,b"},{"id":2,"value":"true"},{"id":3,"value":""}]}`),
		[]byte(`42`),
	}
	for _, in := range cases {
		toon, err := toonEncodeBytes(in)
		if err != nil {
			t.Fatalf("encode %s: %v", in, err)
		}
		back, err := toonDecodeBytes(toon)
		if err != nil {
			t.Fatalf("decode %s: %v", toon, err)
		}
		if !jsonEqual(t, in, back) {
			t.Fatalf("round-trip mismatch\nin=%s\ntoon=%s\nback=%s", in, toon, back)
		}
	}
}

// Both verbs fail closed: encode refuses shapes it cannot prove round-trip, and
// decode refuses malformed input, rather than emitting a lossy/guessed result.
func TestTOONVerbsFailClosed(t *testing.T) {
	// Non-uniform array of objects is outside the lossless subset.
	if out, err := toonEncodeBytes([]byte(`{"rows":[{"id":1,"name":"a"},{"id":2,"label":"b"}]}`)); err == nil {
		t.Fatalf("expected encode error on non-uniform shape, got %s", out)
	}
	// Invalid JSON.
	if _, err := toonEncodeBytes([]byte(`{"bad":[1,2,`)); err == nil {
		t.Fatal("expected encode error on invalid JSON")
	}
	// Tabular header claims 2 rows but only 1 follows — the structural-validation
	// check TOON advertises must fail closed rather than return a short array.
	if _, err := toonDecodeBytes([]byte("users[2]{id,name}:\n  1,ana")); err == nil {
		t.Fatal("expected decode error on row-count mismatch")
	}
}

// The summary line is what `caveman convert` gates on: it must count both
// sides (text vs. rendered image estimate) and total the per-page estimates
// exactly, so the smaller-or-pass-through decision is made on honest numbers.
func TestPixelRenderReportsSummary(t *testing.T) {
	input := []byte("# Skill body\n\nRespond terse like smart caveman. All technical substance stay.\nOnly fluff die. Keep code blocks verbatim.\n")
	images, err := pixel.RenderTextToPNGs(string(input), pixel.DefaultCols, pixel.RenderStyle{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(images) == 0 {
		t.Fatal("expected at least one rendered page")
	}
	reports, summary := pixelRenderReports(images, input, pixel.DensityConservative)
	if len(reports) != len(images) {
		t.Fatalf("got %d reports for %d images", len(reports), len(images))
	}
	if !summary.Summary || summary.Pages != len(images) {
		t.Fatalf("summary line malformed: %+v", summary)
	}
	if summary.Density != "conservative" {
		t.Fatalf("summary density = %q, want conservative", summary.Density)
	}
	for _, r := range reports {
		if r.Density != "conservative" {
			t.Fatalf("page density = %q, want conservative", r.Density)
		}
	}
	if summary.TextEstTokens <= 0 {
		t.Fatalf("textEstTokens must be counted, got %d", summary.TextEstTokens)
	}
	sum := 0
	for _, r := range reports {
		if r.EstTokens <= 0 {
			t.Fatalf("page estTokens must be positive: %+v", r)
		}
		sum += r.EstTokens
	}
	if summary.ImageEstTokens != sum {
		t.Fatalf("imageEstTokens %d != sum of pages %d", summary.ImageEstTokens, sum)
	}
}

func TestPixelRenderDefaultsToReflowWithOptOut(t *testing.T) {
	file := filepath.Join(t.TempDir(), "short-lines.txt")
	if err := os.WriteFile(file, []byte(strings.Repeat("x\n", 40)), 0o644); err != nil {
		t.Fatal(err)
	}

	defaultOut := captureStdout(t, func() {
		runPixelRender([]string{"--cols", "10", file})
	})
	noReflowOut := captureStdout(t, func() {
		runPixelRender([]string{"--cols", "10", "--no-reflow", file})
	})
	defaultReport := firstPixelRenderReport(t, defaultOut)
	noReflowReport := firstPixelRenderReport(t, noReflowOut)
	if defaultReport.Height >= noReflowReport.Height {
		t.Fatalf("default render did not reflow: default height=%d no-reflow height=%d", defaultReport.Height, noReflowReport.Height)
	}
}

func TestPixelUsageDocumentsNoReflow(t *testing.T) {
	got := captureStderr(t, pixelUsage)
	if !strings.Contains(got, "--no-reflow") || !strings.Contains(got, "reflows by default") {
		t.Fatalf("pixel usage missing reflow opt-out: %s", got)
	}
}

func firstPixelRenderReport(t *testing.T, out string) pixelRenderReport {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(out))
	var report pixelRenderReport
	if err := dec.Decode(&report); err != nil {
		t.Fatalf("decode first render report: %v\n%s", err, out)
	}
	return report
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()
	fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatalf("unmarshal a: %v", err)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		t.Fatalf("unmarshal b: %v", err)
	}
	return reflect.DeepEqual(av, bv)
}

func TestEmitCompressResultPassesOriginalThroughWhenCCRBudgetIsFull(t *testing.T) {
	input := []byte("original bytes")
	res := engine.Result{
		Output:       append([]byte(nil), input...),
		TokensBefore: 3,
		TokensAfter:  3,
		Basis:        engine.BasisInferred,
	}
	var stdout, stderr bytes.Buffer
	if err := emitCompressResult(input, res, ccr.ErrBudgetExceeded, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stdout.Bytes(), input) {
		t.Fatalf("stdout = %q, want original", stdout.Bytes())
	}
	var report map[string]any
	if err := json.Unmarshal(stderr.Bytes(), &report); err != nil {
		t.Fatalf("report is not JSON: %v", err)
	}
	if report["error"] != "cave_ccr_budget_exceeded" || report["ratio"] != float64(0) {
		t.Fatalf("report = %#v", report)
	}
}

func TestEmitCompressResultRejectsTransformedBytesOnCCRError(t *testing.T) {
	input := []byte("original bytes")
	res := engine.Result{Output: []byte("smaller"), TokensBefore: 3, TokensAfter: 2, Ratio: 1.0 / 3.0}
	if err := emitCompressResult(input, res, ccr.ErrBudgetExceeded, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("unsafe transformed result must fail")
	}
}
