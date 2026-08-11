package store

import (
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
)

func TestAgentEvidenceForBuildReturnsExactContentBlindRequests(t *testing.T) {
	store, err := Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	build := strings.Repeat("a", 64)
	plan := strings.Repeat("b", 64)
	prefix := strings.Repeat("c", 64)
	raw := strings.Repeat("d", 64)
	transformed := strings.Repeat("e", 64)
	store.Record(gateway.RequestRecord{
		Timestamp:                    "2026-07-31 12:00:00.000",
		RequestID:                    "request-1",
		SessionID:                    "support:session-1",
		AgentBuildSHA256:             build,
		EfficiencyPlanSHA256:         plan,
		Provider:                     "anthropic",
		Model:                        "claude-sonnet-4-6",
		StatusCode:                   200,
		InputTokens:                  20,
		OutputTokens:                 8,
		CachedInputTokens:            5,
		CacheCreationInputTokens:     3,
		ReasoningTokens:              2,
		TokenUsageBasis:              "provider_complete",
		AuthMode:                     "payg",
		RawRequestSHA256:             raw,
		TransformedRequestSHA256:     transformed,
		RequestHashComplete:          true,
		OptimizationIDs:              []string{"caveman.engine.text.v1"},
		ContextBill:                  "instruction=12,user_intent=4",
		TransformTrace:               "caveman.engine.text.v1|history|S4|applied|10|4|exact_ccr|0|1",
		TransformLocation:            "gateway",
		CacheEpoch:                   "support:session-1:epoch-1",
		CachePrefixSHA256:            prefix,
		ProviderCachePrefixSHA256:    prefix,
		ProviderCacheComponentSHA256: prefix,
		CacheBoundaryKnown:           true,
		RecoveryHandle:               "ccr_fixture",
		Basis:                        "verified",
	})

	rows, err := store.AgentEvidenceForBuild("support:session-1", build, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	got := rows[0]
	if got.RequestID != "request-1" || got.RawRequestSHA256 != raw ||
		got.TransformedRequestSHA256 != transformed || got.ReasoningTokens != 2 ||
		got.TokenUsageBasis != "provider_complete" || got.Basis != "inferred" ||
		got.AgentBuildSHA256 != build || got.EfficiencyPlanSHA256 != plan ||
		got.ProviderCachePrefixSHA256 != prefix ||
		got.ProviderCacheComponentSHA256 != prefix || !got.CacheBoundaryKnown || !got.RequestHashComplete {
		t.Fatalf("unexpected evidence: %+v", got)
	}
	if _, err := store.AgentEvidenceForBuild("bad session", build, plan); err == nil {
		t.Fatal("malformed session identity accepted")
	}
}

func TestAgentEvidenceMarksIncompleteRequestHashes(t *testing.T) {
	store, err := Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	build := strings.Repeat("a", 64)
	plan := strings.Repeat("b", 64)
	store.Record(gateway.RequestRecord{
		Timestamp:                "2026-08-11 12:00:00.000",
		RequestID:                "request-incomplete",
		SessionID:                "support:session-incomplete",
		AgentBuildSHA256:         build,
		EfficiencyPlanSHA256:     plan,
		RawRequestSHA256:         strings.Repeat("c", 64),
		TransformedRequestSHA256: strings.Repeat("d", 64),
		RequestHashComplete:      false,
		StatusCode:               502,
		Basis:                    "inferred",
	})

	rows, err := store.AgentEvidenceForBuild("support:session-incomplete", build, plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].RequestHashComplete || rows[0].RawRequestSHA256 != "" || rows[0].TransformedRequestSHA256 != "" {
		t.Fatalf("incomplete prefix persisted as exact evidence: %+v", rows)
	}
}
