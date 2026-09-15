package store

import (
	"path/filepath"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
)

func usd(v float64) *float64 { return &v }

// TestWouldSaveColumnAndSummary proves the observe-estimate column persists, feeds
// the aggregate Summary would-save totals, and never leaks into savings_usd.
func TestWouldSaveColumnAndSummary(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	// A priced, complete observe row: would_save_tokens + would_save_usd, zero saving.
	s.Record(gateway.RequestRecord{
		Timestamp: "2026-07-22 10:00:00.000", RequestID: "obs-1",
		Provider: "openai", Model: "gpt-5.5", Endpoint: "/v1/chat/completions",
		StatusCode: 200, InputTokens: 1000, OutputTokens: 20,
		TotalCostUSD: 0.01, SavingsUSD: 0, Basis: "inferred", RuntimeMode: "record",
		TokenUsageBasis: "provider_complete", AuthMode: "payg",
		WouldSaveTokens: 300, WouldSaveUSD: usd(0.90),
		OptimizationIDs: []string{},
	})
	// A second observe row with no price (would_save_usd nil) — a pure token count.
	s.Record(gateway.RequestRecord{
		Timestamp: "2026-07-22 10:01:00.000", RequestID: "obs-2",
		Provider: "openai", Model: "gpt-5.5", Endpoint: "/v1/chat/completions",
		StatusCode: 200, InputTokens: 500, OutputTokens: 10,
		SavingsUSD: 0, Basis: "inferred", RuntimeMode: "record",
		TokenUsageBasis: "provider_partial", AuthMode: "payg",
		WouldSaveTokens: 100, WouldSaveUSD: nil,
		OptimizationIDs: []string{},
	})

	stats, err := s.Summary()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if stats.WouldSaveTokens != 400 {
		t.Errorf("summary would_save_tokens = %d, want 400", stats.WouldSaveTokens)
	}
	if stats.WouldSaveUSD == nil || *stats.WouldSaveUSD != 0.90 {
		t.Errorf("summary would_save_usd = %v, want 0.90 (only the priced row contributes)", stats.WouldSaveUSD)
	}
	if stats.TotalSaved != 0 {
		t.Errorf("observe rows must book zero savings_usd, got total_saved = %v", stats.TotalSaved)
	}
	if stats.Basis != "inferred" {
		t.Errorf("basis = %q, want inferred", stats.Basis)
	}

	// Honesty: the would-save column must be independent of savings_usd at the DB level.
	var savings float64
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(savings_usd),0) FROM requests`).Scan(&savings); err != nil {
		t.Fatalf("query savings: %v", err)
	}
	if savings != 0 {
		t.Errorf("would_save must never leak into savings_usd, got %v", savings)
	}
}

// TestWouldSaveUSDNilWhenNoPricedRow proves the aggregate would_save_usd is nil (not
// a fabricated 0) when no row was list-price eligible.
func TestWouldSaveUSDNilWhenNoPricedRow(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Record(gateway.RequestRecord{
		Timestamp: "2026-07-22 10:00:00.000", RequestID: "obs-noprice",
		Provider: "openai", Model: "gpt-5.5", Endpoint: "/v1/chat/completions",
		StatusCode: 200, InputTokens: 500, OutputTokens: 10,
		Basis: "inferred", RuntimeMode: "record", TokenUsageBasis: "provider_partial", AuthMode: "payg",
		WouldSaveTokens: 100, WouldSaveUSD: nil,
	})
	stats, err := s.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if stats.WouldSaveUSD != nil {
		t.Errorf("would_save_usd = %v, want nil when no row was priced (never a guessed 0)", *stats.WouldSaveUSD)
	}
	if stats.WouldSaveTokens != 100 {
		t.Errorf("would_save_tokens = %d, want 100", stats.WouldSaveTokens)
	}
}

// TestForgedWouldSaveUSDStrippedOnUnpricedRow proves the store re-enforces the price
// discipline: a would_save_usd handed in for a non-list-priced row is dropped, just
// like total_cost_usd/savings_usd are re-zeroed.
func TestForgedWouldSaveUSDStrippedOnUnpricedRow(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Subscription auth is not list-price eligible, so no dollar figure may survive.
	s.Record(gateway.RequestRecord{
		Timestamp: "2026-07-22 10:00:00.000", RequestID: "obs-forged",
		Provider: "openai", Model: "gpt-5.5", Endpoint: "/v1/chat/completions",
		StatusCode: 200, InputTokens: 500, OutputTokens: 10,
		Basis: "inferred", RuntimeMode: "record", TokenUsageBasis: "provider_complete", AuthMode: "subscription",
		WouldSaveTokens: 100, WouldSaveUSD: usd(9.99),
	})
	stats, err := s.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if stats.WouldSaveUSD != nil {
		t.Errorf("would_save_usd = %v, want nil — an unpriced row must not carry a dollar figure", *stats.WouldSaveUSD)
	}
	if stats.WouldSaveTokens != 100 {
		t.Errorf("would_save_tokens = %d, want 100 (the token count survives)", stats.WouldSaveTokens)
	}
}

// TestWouldSaveZeroedOnFailedUpstream proves a failed upstream (status >= 400)
// records neither would_save_tokens nor would_save_usd — a broken request never
// inflates the would-have-saved totals.
func TestWouldSaveZeroedOnFailedUpstream(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Record(gateway.RequestRecord{
		Timestamp: "2026-07-22 10:00:00.000", RequestID: "obs-5xx",
		Provider: "openai", Model: "gpt-5.5", Endpoint: "/v1/chat/completions",
		StatusCode: 502, InputTokens: 1000, OutputTokens: 0,
		Basis: "inferred", RuntimeMode: "record", TokenUsageBasis: "provider_complete", AuthMode: "payg",
		WouldSaveTokens: 400, WouldSaveUSD: usd(0.80),
	})
	stats, err := s.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if stats.WouldSaveTokens != 0 {
		t.Errorf("would_save_tokens = %d, want 0 on a failed upstream", stats.WouldSaveTokens)
	}
	if stats.WouldSaveUSD != nil {
		t.Errorf("would_save_usd = %v, want nil on a failed upstream", *stats.WouldSaveUSD)
	}
	// A row-level check too: the persisted column is 0.
	var rowTokens int64
	if err := s.db.QueryRow(`SELECT COALESCE(would_save_tokens,0) FROM requests WHERE request_id='obs-5xx'`).Scan(&rowTokens); err != nil {
		t.Fatal(err)
	}
	if rowTokens != 0 {
		t.Errorf("persisted would_save_tokens = %d, want 0", rowTokens)
	}
}

// TestObserveSummarySince proves the compact object filters by session start and
// computes the funnel percentage over tokens sent.
func TestObserveSummarySince(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Pre-session row (must be excluded by --since).
	s.Record(gateway.RequestRecord{
		Timestamp: "2026-07-22 09:00:00.000", RequestID: "pre",
		Provider: "openai", Model: "gpt-5.5", Endpoint: "/v1/chat/completions",
		StatusCode: 200, InputTokens: 999, Basis: "inferred", RuntimeMode: "record",
		TokenUsageBasis: "provider_partial", AuthMode: "payg", WouldSaveTokens: 999,
	})
	// In-session rows.
	s.Record(gateway.RequestRecord{
		Timestamp: "2026-07-22 10:00:00.000", RequestID: "in-1",
		Provider: "openai", Model: "gpt-5.5", Endpoint: "/v1/chat/completions",
		StatusCode: 200, InputTokens: 1000, Basis: "inferred", RuntimeMode: "record",
		TokenUsageBasis: "provider_complete", AuthMode: "payg",
		WouldSaveTokens: 600, WouldSaveUSD: usd(1.20),
	})
	s.Record(gateway.RequestRecord{
		Timestamp: "2026-07-22 10:05:00.000", RequestID: "in-2",
		Provider: "openai", Model: "gpt-5.5", Endpoint: "/v1/chat/completions",
		StatusCode: 200, InputTokens: 1000, Basis: "inferred", RuntimeMode: "record",
		TokenUsageBasis: "provider_complete", AuthMode: "payg",
		WouldSaveTokens: 620, WouldSaveUSD: usd(1.24),
	})

	// --since at 09:30 UTC excludes the pre-session row.
	sum, err := s.ObserveSummarySince("2026-07-22T09:30:00Z")
	if err != nil {
		t.Fatalf("observe summary: %v", err)
	}
	if sum.Spans != 2 {
		t.Errorf("spans = %d, want 2 (pre-session row excluded)", sum.Spans)
	}
	if sum.TokensIn != 2000 {
		t.Errorf("tokens_in = %d, want 2000", sum.TokensIn)
	}
	if sum.WouldSaveTokens != 1220 {
		t.Errorf("would_save_tokens = %d, want 1220", sum.WouldSaveTokens)
	}
	wantPct := 1220.0 / 2000.0
	if sum.WouldSavePct < wantPct-1e-9 || sum.WouldSavePct > wantPct+1e-9 {
		t.Errorf("would_save_pct = %v, want %v (cut over sent)", sum.WouldSavePct, wantPct)
	}
	if sum.WouldSaveUSD == nil || *sum.WouldSaveUSD != 2.44 {
		t.Errorf("would_save_usd = %v, want 2.44", sum.WouldSaveUSD)
	}
	if sum.SavingsUSD != 0 {
		t.Errorf("observe session books zero savings_usd, got %v", sum.SavingsUSD)
	}
	if sum.Basis != "inferred" {
		t.Errorf("basis = %q, want inferred", sum.Basis)
	}
	if got := sum.TokenAccounting["provider_complete"]; got != 2 {
		t.Errorf("today provider_complete = %d, want 2", got)
	}
	if len(sum.TokenAccounting) != 1 {
		t.Errorf("today token accounting leaked pre-window rows: %+v", sum.TokenAccounting)
	}

	// No --since aggregates everything, including the pre-session row.
	all, err := s.ObserveSummarySince("")
	if err != nil {
		t.Fatal(err)
	}
	if all.Spans != 3 || all.WouldSaveTokens != 2219 {
		t.Errorf("no-since summary = (spans %d, would_save %d), want (3, 2219)", all.Spans, all.WouldSaveTokens)
	}
	if got := all.TokenAccounting["provider_complete"]; got != 2 {
		t.Errorf("all-time provider_complete = %d, want 2", got)
	}
	if got := all.TokenAccounting["provider_partial"]; got != 1 {
		t.Errorf("all-time provider_partial = %d, want 1", got)
	}

	// A bad --since is an error, never a silent full-table read.
	if _, err := s.ObserveSummarySince("not-a-timestamp"); err == nil {
		t.Error("ObserveSummarySince must reject a non-RFC3339 --since")
	}
}

// TestObserveSummaryCompressedParts proves the compact object carries the
// compressed-parts before/after totals behind the saved delta, so the CLI's
// session line can render the real pair instead of inventing one.
func TestObserveSummaryCompressedParts(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.Record(gateway.RequestRecord{
		Timestamp: "2026-07-22 10:00:00.000", RequestID: "c-1",
		Provider: "anthropic", Model: "claude-fable-5", Endpoint: "/v1/messages",
		StatusCode: 200, InputTokens: 1000, Basis: "inferred", RuntimeMode: "compress",
		TokenUsageBasis: "provider_complete", AuthMode: "subscription",
		CompressionTokensBefore: 900, CompressionTokensAfter: 400,
		CompressionTokenCountBasis: "estimated_engine_o200k",
	})
	s.Record(gateway.RequestRecord{
		Timestamp: "2026-07-22 10:05:00.000", RequestID: "c-2",
		Provider: "anthropic", Model: "claude-fable-5", Endpoint: "/v1/messages",
		StatusCode: 200, InputTokens: 1000, Basis: "inferred", RuntimeMode: "compress",
		TokenUsageBasis: "provider_complete", AuthMode: "subscription",
		CompressionTokensBefore: 600, CompressionTokensAfter: 250,
		CompressionTokenCountBasis: "estimated_engine_o200k",
	})

	sum, err := s.ObserveSummarySince("")
	if err != nil {
		t.Fatalf("observe summary: %v", err)
	}
	if sum.CompressionTokensBefore != 1500 {
		t.Errorf("compression_tokens_before = %d, want 1500", sum.CompressionTokensBefore)
	}
	if sum.CompressionTokensAfter != 650 {
		t.Errorf("compression_tokens_after = %d, want 650", sum.CompressionTokensAfter)
	}
	if sum.CompressionTokensSaved != sum.CompressionTokensBefore-sum.CompressionTokensAfter {
		t.Errorf("saved %d must be before-after (%d)", sum.CompressionTokensSaved, sum.CompressionTokensBefore-sum.CompressionTokensAfter)
	}
	// Subscription rows are tokens-only: no dollar figure may ever appear.
	if sum.SavingsUSD != 0 || sum.WouldSaveUSD != nil {
		t.Errorf("subscription compression must book no dollars, got savings_usd=%v would_save_usd=%v", sum.SavingsUSD, sum.WouldSaveUSD)
	}
}
