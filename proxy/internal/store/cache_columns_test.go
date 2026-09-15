package store

import (
	"path/filepath"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
)

// TestObserveSummaryCacheColumnsAndEligibleCount proves issue #133's honest cache
// columns and the compression-eligibility contract field: ObserveSummarySince now
// sums cached_input_tokens / cache_creation_input_tokens, counts cache_bust and
// compression-eligible requests, and refuses a headline compression saving when the
// window was dominated by cache writes.
func TestObserveSummaryCacheColumnsAndEligibleCount(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	// A cache-write-dominated row: 144k cache creation, only 50k cache reads, plus a
	// tiny compression cut. This is the exact shape that used to print a headline win.
	s.Record(gateway.RequestRecord{
		Timestamp: "2026-08-08 10:00:00.000", RequestID: "row-a",
		Provider: "openai", Model: "gpt-5.5", Endpoint: "/v1/chat/completions",
		StatusCode: 200, InputTokens: 200000, OutputTokens: 20,
		CachedInputTokens: 50000, CacheCreationInputTokens: 144000,
		Basis: "inferred", RuntimeMode: "compress",
		TokenUsageBasis: "provider_complete", AuthMode: "payg",
		CompressionTokensBefore: 100, CompressionTokensAfter: 40,
		CompressionEligible: true, CacheBust: false,
		OptimizationIDs: []string{},
	})
	// A compression-eligible row that also busted its session prefix.
	s.Record(gateway.RequestRecord{
		Timestamp: "2026-08-08 10:01:00.000", RequestID: "row-b",
		Provider: "openai", Model: "gpt-5.5", Endpoint: "/v1/chat/completions",
		StatusCode: 200, InputTokens: 30000, OutputTokens: 10,
		CachedInputTokens: 10000, CacheCreationInputTokens: 0,
		Basis: "inferred", RuntimeMode: "compress",
		TokenUsageBasis: "provider_complete", AuthMode: "payg",
		CompressionEligible: true, CacheBust: true,
		OptimizationIDs: []string{},
	})

	out, err := s.ObserveSummarySince("")
	if err != nil {
		t.Fatalf("observe summary: %v", err)
	}
	if out.CachedInputTokens != 60000 {
		t.Errorf("cached_input_tokens = %d, want 60000", out.CachedInputTokens)
	}
	if out.CacheCreationInputTokens != 144000 {
		t.Errorf("cache_creation_input_tokens = %d, want 144000", out.CacheCreationInputTokens)
	}
	if !out.HeadlineCompressionRefused {
		t.Errorf("cache_creation (144000) > cached (60000) must refuse a headline saving")
	}
	if out.CacheBustRequests != 1 {
		t.Errorf("cache_bust_requests = %d, want 1", out.CacheBustRequests)
	}
	if out.RequestsEligibleForCompression != 2 {
		t.Errorf("requests_eligible_for_compression = %d, want 2", out.RequestsEligibleForCompression)
	}

	// Summary (bare stats) surfaces the same cache_bust / eligibility counts.
	stats, err := s.Summary()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if stats.CacheBustRequests != 1 || stats.RequestsEligibleForCompression != 2 {
		t.Errorf("summary cache_bust=%d eligible=%d, want 1 and 2", stats.CacheBustRequests, stats.RequestsEligibleForCompression)
	}
}

// TestObserveSummaryOldDBTolerated proves the ADD COLUMN migration path: a store
// opened, closed, and reopened (simulating an upgrade over an existing DB) still
// reports the new columns without error and never double-adds them.
func TestObserveSummaryOldDBTolerated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caveman.db")
	s1, err := Open(path, nil)
	if err != nil {
		t.Fatalf("open 1: %v", err)
	}
	if _, err := s1.db.Exec(`ALTER TABLE usage_events DROP COLUMN cache_creation_input_tokens`); err != nil {
		t.Fatalf("make legacy usage_events shape: %v", err)
	}
	if _, err := s1.db.Exec(`ALTER TABLE requests DROP COLUMN cache_creation_input_tokens`); err != nil {
		t.Fatalf("make legacy requests shape: %v", err)
	}
	_ = s1.Close()
	s2, err := Open(path, nil)
	if err != nil {
		t.Fatalf("reopen must tolerate already-migrated columns: %v", err)
	}
	defer s2.Close()
	var migrated int
	if err := s2.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('usage_events') WHERE name='cache_creation_input_tokens'`).Scan(&migrated); err != nil {
		t.Fatalf("inspect migrated usage_events: %v", err)
	}
	if migrated != 1 {
		t.Fatalf("cache_creation_input_tokens migration count = %d, want 1", migrated)
	}
	if err := s2.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('requests') WHERE name='cache_creation_input_tokens'`).Scan(&migrated); err != nil {
		t.Fatalf("inspect migrated requests: %v", err)
	}
	if migrated != 1 {
		t.Fatalf("requests cache_creation_input_tokens migration count = %d, want 1", migrated)
	}
	if _, err := s2.ObserveSummarySince(""); err != nil {
		t.Fatalf("observe summary after reopen: %v", err)
	}
}
