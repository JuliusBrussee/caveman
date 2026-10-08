package store

import (
	"path/filepath"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
)

// Cache warms are the proxy's own requests: counted and priced as spend, and
// disclosed separately so they never read as user requests or savings.
func TestSummaryDisclosesCacheWarms(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	row := gateway.RequestRecord{
		Timestamp: "2026-10-08 00:00:00.000", RequestID: "real", Provider: "anthropic", Model: "claude-sonnet-5",
		Endpoint: "/v1/messages", StatusCode: 200, TotalCostUSD: 0.5, Basis: "inferred", RuntimeMode: "compress",
		TokenUsageBasis: "provider_complete", AuthMode: "payg", OptimizationIDs: []string{},
	}
	s.Record(row)
	row.RequestID, row.TotalCostUSD, row.OptimizationIDs = "warm", 0.04, []string{"cache-warm"}
	s.Record(row)
	stats, err := s.Summary()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if stats.Requests != 2 || stats.CacheWarmRequests != 1 || stats.CacheWarmCostUSD != 0.04 || stats.TotalSaved != 0 {
		t.Fatalf("summary = %+v", stats)
	}
}
