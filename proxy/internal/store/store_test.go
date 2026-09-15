package store

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
)

func TestRecordAndSummary_InferredBasis(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	s.Record(gateway.RequestRecord{
		Timestamp: "2026-06-16 00:00:00.000", RequestID: "req-1",
		Provider: "openai", Model: "gpt-5.5", RouteFrom: "gpt-5.5", RouteTo: "gpt-5.5", Endpoint: "/openai/v1/responses",
		StatusCode: 200, InputTokens: 1000, OutputTokens: 120,
		TotalCostUSD: 0.0126, SavingsUSD: 0, Basis: "inferred", RuntimeMode: "record",
		TokenUsageBasis: "provider_complete", AuthMode: "payg",
		OptimizationIDs: []string{},
	})

	stats, err := s.Summary()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if stats.Requests != 1 {
		t.Errorf("requests = %d, want 1", stats.Requests)
	}
	if stats.TotalCost <= 0 {
		t.Errorf("total cost = %v, want > 0", stats.TotalCost)
	}
	if stats.Basis != "inferred" {
		t.Errorf("basis = %q, want inferred", stats.Basis)
	}
	if stats.TokenAccounting["provider_complete"] != 1 || stats.AuthModeAccounting["payg"] != 1 {
		t.Errorf("provenance counts = token %+v auth %+v", stats.TokenAccounting, stats.AuthModeAccounting)
	}
	if stats.CompressionTokenCountBasis != "unavailable" {
		t.Errorf("compression token basis = %q, want unavailable", stats.CompressionTokenCountBasis)
	}

	// Honesty: no row may ever be persisted as `verified` in standalone.
	var verified int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM requests WHERE basis = 'verified'`).Scan(&verified); err != nil {
		t.Fatalf("query basis: %v", err)
	}
	if verified != 0 {
		t.Errorf("found %d verified rows; standalone must only ever record inferred", verified)
	}

	var routeFrom, routeTo string
	if err := s.db.QueryRow(`SELECT route_from, route_to FROM requests WHERE request_id = 'req-1'`).Scan(&routeFrom, &routeTo); err != nil {
		t.Fatalf("query route attribution: %v", err)
	}
	if routeFrom != "gpt-5.5" || routeTo != "gpt-5.5" {
		t.Errorf("route_from/to = %s/%s, want gpt-5.5/gpt-5.5", routeFrom, routeTo)
	}
}

func TestRecordForcesInferredAndSanitizesInvalidAccounting(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Record(gateway.RequestRecord{
		Timestamp: "2026-06-16 00:00:00.000", RequestID: "forged",
		Basis: "verified", InputTokens: -1, OutputTokens: -2,
		TotalCostUSD: math.Inf(1), SavingsUSD: math.NaN(), CompressionRatio: 2,
	})
	var basis string
	var input, output int
	var total, savings, ratio float64
	if err := s.db.QueryRow(`SELECT basis,input_tokens,output_tokens,total_cost_usd,savings_usd,compression_ratio FROM requests WHERE request_id='forged'`).Scan(&basis, &input, &output, &total, &savings, &ratio); err != nil {
		t.Fatal(err)
	}
	if basis != "inferred" || input != 0 || output != 0 || total != 0 || savings != 0 || ratio != 0 {
		t.Fatalf("persisted basis=%q input=%d output=%d cost=%v savings=%v ratio=%v", basis, input, output, total, savings, ratio)
	}
}

func TestRecentRequests(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	empty, err := s.RecentRequests(5)
	if err != nil {
		t.Fatalf("recent empty: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty recent len = %d, want 0", len(empty))
	}

	s.Record(gateway.RequestRecord{
		Timestamp: "2026-06-16 00:00:00.000", RequestID: "req-old", AgentSlug: "codex",
		Provider: "openai", Model: "gpt-4.1", Endpoint: "/w/codex/openai/v1/responses",
		StatusCode: 200, InputTokens: 100, OutputTokens: 10,
		TotalCostUSD: 0.001, SavingsUSD: 0, Basis: "inferred", RuntimeMode: "record",
		TokenUsageBasis: "provider_complete", AuthMode: "payg",
		OptimizationIDs: []string{},
	})
	s.Record(gateway.RequestRecord{
		Timestamp: "2026-06-16 00:00:01.000", RequestID: "req-new", AgentSlug: "checkout",
		Provider: "anthropic", Model: "claude-sonnet-4-5", Endpoint: "/w/checkout/v1/messages",
		StatusCode: 200, InputTokens: 200, OutputTokens: 20,
		TotalCostUSD: 0.002, SavingsUSD: 0, Basis: "inferred", RuntimeMode: "record",
		TokenUsageBasis: "provider_complete", AuthMode: "payg",
		OptimizationIDs: []string{},
	})

	recent, err := s.RecentRequests(1)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(recent) != 1 {
		t.Fatalf("recent len = %d, want 1", len(recent))
	}
	if recent[0].AgentSlug != "checkout" || recent[0].Provider != "anthropic" || recent[0].InputTokens != 200 || recent[0].Basis != "inferred" {
		t.Fatalf("recent[0] = %+v, want newest checkout anthropic row", recent[0])
	}
}
