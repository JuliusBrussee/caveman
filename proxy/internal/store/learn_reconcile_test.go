package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExport(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage.csv")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write export: %v", err)
	}
	return path
}

// TestReconcileFindsTheUnattributedShare is the value of the whole feature: the
// gap between the bill and what transcripts explain is where untracked spend
// lives, and it is the one number no detector can produce.
func TestReconcileFindsTheUnattributedShare(t *testing.T) {
	billed, err := parseUsageExport(writeExport(t, strings.Join([]string{
		"model,input_tokens,output_tokens,cache_read_input_tokens",
		"claude-opus-5,1000000,200000,3000000",
		"claude-haiku-4-5,500000,50000,0",
	}, "\n")))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if billed["claude-opus-5"] != 4_200_000 || billed["claude-haiku-4-5"] != 550_000 {
		t.Fatalf("billed totals wrong: %+v", billed)
	}

	// Transcripts explain the opus traffic but none of the haiku traffic — a
	// script running elsewhere, which is exactly what this is meant to reveal.
	spend := &LearnSpend{Models: []LearnSpendModel{
		{Provider: "anthropic", Model: "claude-opus-5", Tokens: 4_200_000},
	}}
	report := buildLearnReconcile(billed, spend, "usage.csv")
	if report.BilledTokens != 4_750_000 {
		t.Fatalf("billed = %d", report.BilledTokens)
	}
	if report.Unattributed != 550_000 {
		t.Fatalf("unattributed = %d, want 550000", report.Unattributed)
	}
	if report.CoveragePct < 88 || report.CoveragePct > 89 {
		t.Fatalf("coverage = %v, want ~88.4", report.CoveragePct)
	}
	if !strings.Contains(strings.Join(report.Caveats, " "), "traffic learn cannot see") {
		t.Fatalf("the gap must be explained: %v", report.Caveats)
	}
	if strings.Contains(strings.Join(report.Caveats, " "), "verified") &&
		!strings.Contains(strings.Join(report.Caveats, " "), "does not promote") {
		t.Fatalf("reconciliation must not read as a promotion to verified: %v", report.Caveats)
	}
}

// TestReconcileFailsClosedOnUnknownSchema is the safety property. A provider
// changing its export must produce an error naming the problem, never a
// confident wrong gap read off column positions.
func TestReconcileFailsClosedOnUnknownSchema(t *testing.T) {
	if _, err := parseUsageExport(writeExport(t, "a,b,c\n1,2,3\n")); err == nil {
		t.Fatal("an unrecognizable header must fail, not be guessed by position")
	}
	if _, err := parseUsageExport(writeExport(t, "model,cost_usd\nclaude-opus-5,12.40\n")); err == nil {
		t.Fatal("an export with no token columns must fail")
	}
	if _, err := parseUsageExport(writeExport(t, "model,input_tokens\n")); err == nil {
		t.Fatal("an export with no rows must fail")
	}
	if _, err := parseUsageExport(filepath.Join(t.TempDir(), "missing.csv")); err == nil {
		t.Fatal("a missing file must fail")
	}
}

// TestReconcileAcceptsProviderHeaderVariants covers the shapes the two major
// exports actually ship, including a BOM and thousands separators.
func TestReconcileAcceptsProviderHeaderVariants(t *testing.T) {
	openai, err := parseUsageExport(writeExport(t,
		"\ufeffmodel,n_context_tokens_total,n_generated_tokens_total\ngpt-5.6,\"1,000\",250\n"))
	if err != nil {
		t.Fatalf("openai-shaped export: %v", err)
	}
	if openai["gpt-5.6"] != 1250 {
		t.Fatalf("openai totals = %+v, want 1250", openai)
	}
	anthropic, err := parseUsageExport(writeExport(t,
		"Model,Input Tokens,Output Tokens,Cache Creation Input Tokens\nclaude-sonnet-5,100,20,5\n"))
	if err != nil {
		t.Fatalf("anthropic-shaped export: %v", err)
	}
	if anthropic["claude-sonnet-5"] != 125 {
		t.Fatalf("anthropic totals = %+v, want 125", anthropic)
	}
}

// TestReconcileNeverBorrowsAnotherModelsCoverage keeps model matching exact
// (plus the dated alias), so coverage cannot be manufactured.
func TestReconcileNeverBorrowsAnotherModelsCoverage(t *testing.T) {
	billed := map[string]int64{"claude-opus-5-20260401": 1_000_000, "claude-sonnet-5": 1_000_000}
	spend := &LearnSpend{Models: []LearnSpendModel{
		{Model: "claude-opus-5", Tokens: 1_000_000},
	}}
	report := buildLearnReconcile(billed, spend, "usage.csv")
	byModel := map[string]LearnReconcileRow{}
	for _, row := range report.Rows {
		byModel[row.Model] = row
	}
	if byModel["claude-opus-5-20260401"].MeasuredTokens != 1_000_000 {
		t.Fatalf("the dated alias must match its undated measurement: %+v", byModel)
	}
	if byModel["claude-sonnet-5"].MeasuredTokens != 0 {
		t.Fatalf("an unmeasured model must show zero coverage, not a sibling's: %+v", byModel)
	}
}

// TestReconcileFlagsMismatchedWindows catches the most likely user error before
// they read a nonsense coverage figure.
func TestReconcileFlagsMismatchedWindows(t *testing.T) {
	report := buildLearnReconcile(
		map[string]int64{"claude-opus-5": 100},
		&LearnSpend{Models: []LearnSpendModel{{Model: "claude-opus-5", Tokens: 900_000}}},
		"usage.csv")
	if report.CoveragePct != 100 {
		t.Fatalf("coverage must cap at 100, got %v", report.CoveragePct)
	}
	if !strings.Contains(strings.Join(report.Caveats, " "), "windows probably differ") &&
		!strings.Contains(strings.Join(report.Caveats, " "), "window and the scan window probably differ") {
		t.Fatalf("a window mismatch must be called out: %v", report.Caveats)
	}
}
