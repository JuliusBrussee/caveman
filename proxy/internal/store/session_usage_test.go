package store

import (
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
)

func TestSessionUsageCorrelatesOnlyExactSessionAndLabelsBasis(t *testing.T) {
	s, err := Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, record := range []gateway.RequestRecord{
		{
			Timestamp: "2026-08-08 12:00:00.000", RequestID: "r1", SessionID: "claude:host-7",
			SessionCorrelationBasis: "signed_marker",
			Provider:                "openai", Model: "gpt-5.1", StatusCode: 200,
			InputTokens: 100, OutputTokens: 20, CachedInputTokens: 25, ReasoningTokens: 5,
			TotalCostUSD: 0.0123456, SavingsUSD: 0.0012345, TokenUsageBasis: "provider_complete", AuthMode: "payg",
			CompressionTokensBefore: 80, CompressionTokensAfter: 30, CompressionTokenCountBasis: "estimated_engine_o200k",
		},
		{
			Timestamp: "2026-08-08 12:00:01.000", RequestID: "r2", SessionID: "claude:host-7",
			SessionCorrelationBasis: "signed_marker",
			Provider:                "openai", Model: "gpt-5.1", StatusCode: 200,
			InputTokens: 40, OutputTokens: 10, TokenUsageBasis: "unavailable", AuthMode: "payg",
		},
		{
			Timestamp: "2026-08-08 12:00:02.000", RequestID: "other", SessionID: "claude:other",
			Provider: "openai", Model: "gpt-5.1", StatusCode: 200,
			InputTokens: 999, OutputTokens: 999, TokenUsageBasis: "provider_complete", AuthMode: "payg",
		},
	} {
		s.Record(record)
	}
	usage, err := s.SessionUsage("claude:host-7")
	if err != nil {
		t.Fatal(err)
	}
	if usage.Status != "correlated" || usage.Requests != 2 || usage.ProviderCompleteRequests != 1 {
		t.Fatalf("wrong correlation: %+v", usage)
	}
	if usage.CorrelationBasis != "signed_marker" {
		t.Fatalf("correlation basis = %q, want signed_marker", usage.CorrelationBasis)
	}
	if usage.InputTokens != 140 || usage.OutputTokens != 30 || usage.CachedInputTokens != 25 || usage.ReasoningTokens != 5 {
		t.Fatalf("wrong token totals: %+v", usage)
	}
	if usage.TokenUsageCoverage != "mixed_or_unavailable" || usage.CompressionTokenCountBasis != "estimated_engine_o200k" {
		t.Fatalf("wrong basis labels: %+v", usage)
	}
	if usage.CatalogListPriceSubtotalUSD != 0.0123456 || usage.InferredSavingsUSD != 0.0012345 {
		t.Fatalf("wrong rounded dollar subtotals: %+v", usage)
	}
	missing, err := s.SessionUsage("claude:missing")
	if err != nil || missing.Status != "not_observed" || missing.Requests != 0 {
		t.Fatalf("missing session must be honest zero: %+v err=%v", missing, err)
	}
}

func TestSessionUsagePreservesApproximateCorrelationBasis(t *testing.T) {
	s, err := Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Record(gateway.RequestRecord{
		Timestamp: "2026-08-08 12:00:00.000", RequestID: "fallback", SessionID: "claude:recent",
		SessionCorrelationBasis: "unique_recent_session", Provider: "openai", Model: "gpt-5.1", StatusCode: 200,
		CompressionTokensBefore: 80, CompressionTokensAfter: 30, CompressionTokenCountBasis: "estimated_engine_o200k",
	})
	usage, err := s.SessionUsage("claude:recent")
	if err != nil {
		t.Fatal(err)
	}
	if usage.Status != "correlated" || usage.CorrelationBasis != "unique_recent_session" {
		t.Fatalf("approximate correlation basis lost: %+v", usage)
	}
}
