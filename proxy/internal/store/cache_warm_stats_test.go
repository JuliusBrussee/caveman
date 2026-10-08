package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
)

// Cache warms are the proxy's own requests: spend, but never an agent request,
// a turn, a last-request view or an exported span.

func warmStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	row := gateway.RequestRecord{
		Timestamp: time.Now().UTC().Format("2006-01-02 15:04:05.000"), RequestID: "real", Label: "trial:t1",
		SessionID: "sess-1", Provider: "anthropic", Model: "claude-sonnet-5", Endpoint: "/v1/messages", StatusCode: 200,
		InputTokens: 1000, OutputTokens: 10, TotalCostUSD: 0.5, Basis: "inferred", RuntimeMode: "compress",
		TokenUsageBasis: "provider_complete", AuthMode: "payg", OptimizationIDs: []string{},
		CompressionTokensBefore: 900, CompressionTokensAfter: 600, CompressionTokenCountBasis: "estimated_engine_o200k",
	}
	s.Record(row)
	row.RequestID, row.TotalCostUSD, row.OptimizationIDs = "warm", 0.04, []string{"cache-warm"}
	row.CompressionTokensBefore, row.CompressionTokensAfter, row.CompressionTokenCountBasis = 0, 0, ""
	s.Record(row)
	return s
}

func TestSummaryDisclosesCacheWarms(t *testing.T) {
	stats, err := warmStore(t).Summary()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if stats.Requests != 2 || stats.CacheWarmRequests != 1 || stats.CacheWarmCostUSD != 0.04 || stats.TotalSaved != 0 {
		t.Fatalf("summary = %+v", stats)
	}
}

func TestSessionUsageCountsNoWarmAsARequest(t *testing.T) {
	u, err := warmStore(t).SessionUsage("sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if u.Requests != 1 || u.ProviderCompleteRequests != 1 || u.TokenUsageCoverage != "provider_complete" {
		t.Fatalf("usage = %+v", u)
	}
	if u.InputTokens != 2000 || u.CatalogListPriceSubtotalUSD != 0.54 {
		t.Fatalf("warm spend left out: %+v", u)
	}
}

func TestWrapMeasuredCountsNoWarm(t *testing.T) {
	m := warmStore(t).wrapMeasuredSince(time.Time{})
	if m == nil || m.Requests != 1 {
		t.Fatalf("wrap measured = %+v", m)
	}
}

func TestRequestOriginsCountNoWarm(t *testing.T) {
	rows, err := warmStore(t).requestOrigins("t1")
	if err != nil || len(rows) != 1 {
		t.Fatalf("origins = %+v %v", rows, err)
	}
	if rows[0].Origin.Requests != 1 || rows[0].Origin.TotalCostUSD != 0.54 {
		t.Fatalf("origin = %+v", rows[0].Origin)
	}
}

func TestExportSpansLeaveWarmsOut(t *testing.T) {
	s := warmStore(t)
	f, err := os.Create(filepath.Join(t.TempDir(), "spans.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := s.writeExportSpans(f, TrialPlan{TrialID: "t1"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(f.Name())
	if strings.Count(string(raw), "\n") != 1 || strings.Contains(string(raw), `"warm"`) {
		t.Fatalf("spans = %s", raw)
	}
}

func TestStatsReportKeepsWarmSpendOutOfRequests(t *testing.T) {
	report, err := warmStore(t).BuildStatsReport(StatsReportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tot := report.Totals
	if tot.Requests != 1 || tot.SuccessfulRequests != 1 || tot.CacheWarmRequests != 1 || tot.InputTokens != 2000 {
		t.Fatalf("totals = %+v", tot)
	}
	for _, r := range report.Receipts {
		if r.ID == "warm" {
			t.Fatal("a cache warm was listed as a request receipt")
		}
	}
}

func TestRequestViewsLeaveWarmsOut(t *testing.T) {
	s := warmStore(t)
	stats, err := s.Summary()
	if err != nil {
		t.Fatal(err)
	}
	// Spend keeps the warm; the per-request breakdowns do not.
	if stats.TotalCost != 0.54 || stats.TokenAccounting["provider_complete"] != 1 || stats.AuthModeAccounting["payg"] != 1 {
		t.Fatalf("summary = %+v", stats)
	}
	for _, since := range []string{"", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)} {
		obs, err := s.ObserveSummarySince(since)
		if err != nil {
			t.Fatal(err)
		}
		if obs.Spans != 1 || obs.TokensIn != 1000 || obs.TokenAccounting["provider_complete"] != 1 {
			t.Fatalf("observe since %q = %+v", since, obs)
		}
	}
	recent, err := s.RecentRequests(10)
	if err != nil || len(recent) != 1 {
		t.Fatalf("recent = %+v %v", recent, err)
	}
}
