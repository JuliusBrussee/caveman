package routing

import "testing"

const benchmarkArtifactHashFixture = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestBenchmarkRouterDeterministicSchedulesUseCanonicalActionOrder(t *testing.T) {
	pool := []Candidate{
		{Provider: "anthropic", Model: "sonnet", Effort: "high"},
		{Provider: "anthropic", Model: "haiku", Effort: "low"},
		{Provider: "anthropic", Model: "opus", Effort: "xhigh"},
	}
	canonical := append([]Candidate(nil), pool...)
	sortCandidatesByActionID(canonical)
	features := Features{BodyModelRewrite: true}
	ctx := BenchmarkContext{SessionID: "bench-session", TurnIndex: 1}

	fixed, err := (BenchmarkRouter{Version: BenchmarkFixedRouterVersion, Policy: BenchmarkPolicy{
		Enabled: true, SystemArtifactHash: benchmarkArtifactHashFixture, FixedActionID: CandidateActionID(canonical[2]),
	}}).PickSession(features, pool, ctx)
	if err != nil || fixed.ActionID != CandidateActionID(canonical[2]) {
		t.Fatalf("fixed decision = %+v err=%v", fixed, err)
	}
	roundRobin, err := (BenchmarkRouter{Version: BenchmarkRoundRobinRouterVersion, Policy: BenchmarkPolicy{Enabled: true, SystemArtifactHash: benchmarkArtifactHashFixture}}).PickSession(features, pool, ctx)
	if err != nil || roundRobin.ActionID != CandidateActionID(canonical[1]) {
		t.Fatalf("round-robin decision = %+v err=%v", roundRobin, err)
	}
	randomRouter := BenchmarkRouter{Version: BenchmarkRandomRouterVersion, Policy: BenchmarkPolicy{Enabled: true, SystemArtifactHash: benchmarkArtifactHashFixture, RandomSeed: "seed-v1"}}
	first, err := randomRouter.PickSession(features, pool, ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := randomRouter.PickSession(features, []Candidate{pool[2], pool[0], pool[1]}, ctx)
	if err != nil || first.ActionID != second.ActionID {
		t.Fatalf("random decision drifted: first=%+v second=%+v err=%v", first, second, err)
	}
}

func TestBenchmarkRouterFailsClosedOnMissingContract(t *testing.T) {
	pool := []Candidate{{Provider: "anthropic", Model: "opus", Effort: "high"}}
	features := Features{BodyModelRewrite: true}
	for name, router := range map[string]BenchmarkRouter{
		"disabled":           {Version: BenchmarkFixedRouterVersion},
		"fixed outside pool": {Version: BenchmarkFixedRouterVersion, Policy: BenchmarkPolicy{Enabled: true, SystemArtifactHash: benchmarkArtifactHashFixture, FixedActionID: "missing"}},
		"random no seed":     {Version: BenchmarkRandomRouterVersion, Policy: BenchmarkPolicy{Enabled: true, SystemArtifactHash: benchmarkArtifactHashFixture}},
		"unknown version":    {Version: "benchmark-mystery-v1", Policy: BenchmarkPolicy{Enabled: true, SystemArtifactHash: benchmarkArtifactHashFixture}},
	} {
		t.Run(name, func(t *testing.T) {
			decision, err := router.PickSession(features, pool, BenchmarkContext{SessionID: "session", TurnIndex: 0})
			if err != nil || decision.Model != "" || decision.Reason == "" {
				t.Fatalf("decision = %+v err=%v", decision, err)
			}
		})
	}
}
