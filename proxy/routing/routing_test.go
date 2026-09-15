package routing

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"testing"

	"github.com/JuliusBrussee/caveman/shared/platform/cost"
)

func TestRulesRouterRequiresPricedBaseline(t *testing.T) {
	dec, err := (RulesRouter{}).Pick(Features{Provider: "openai", BodyModelRewrite: true}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Model != "" || dec.Reason != "missing_priced_baseline" {
		t.Fatalf("decision = %+v, want no route with missing baseline", dec)
	}
}

func TestRulesRouterPicksCheapestCapableAtAlphaZero(t *testing.T) {
	f := Features{
		Provider:         "openai",
		Endpoint:         "/openai/v1/responses",
		CurrentModel:     "cave-auto",
		BaselineModel:    "gpt-5.5",
		BaselinePrice:    price(5, 30),
		Stream:           true,
		BodyModelRewrite: true,
	}
	pool := []Candidate{
		candidate("openai", "gpt-5.4-mini", price(0.6, 2.4), capsWithContext(128000, "responses_api", "streaming")),
		candidate("openai", "gpt-5.4-nano", price(0.15, 0.6), capsWithContext(128000, "responses_api", "streaming")),
	}
	dec, err := (RulesRouter{}).Pick(f, pool, 0)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Model != "gpt-5.4-nano" {
		t.Fatalf("model = %q, want cheapest capable nano", dec.Model)
	}
}

func TestRulesRouterAlphaOnePicksClosestCheaperCandidate(t *testing.T) {
	f := Features{
		Provider:         "openai",
		Endpoint:         "/openai/v1/responses",
		CurrentModel:     "cave-auto",
		BaselineModel:    "gpt-5.5",
		BaselinePrice:    price(5, 30),
		BodyModelRewrite: true,
	}
	pool := []Candidate{
		candidate("openai", "gpt-5.4-mini", price(0.6, 2.4), capsWithContext(128000, "responses_api")),
		candidate("openai", "gpt-5.4-nano", price(0.15, 0.6), capsWithContext(128000, "responses_api")),
	}
	dec, err := (RulesRouter{}).Pick(f, pool, 1)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Model != "gpt-5.4-mini" {
		t.Fatalf("model = %q, want closest cheaper mini", dec.Model)
	}
}

func TestRulesRouterRequiresEndpointCapability(t *testing.T) {
	f := Features{
		Provider:         "openai",
		Endpoint:         "/openai/v1/responses",
		CurrentModel:     "cave-auto",
		BaselineModel:    "gpt-5.5",
		BaselinePrice:    price(5, 30),
		BodyModelRewrite: true,
	}
	pool := []Candidate{
		candidate("openai", "text-embedding-3-small", price(0.02, 0), capsWithContext(8192, "embeddings")),
	}
	dec, err := (RulesRouter{}).Pick(f, pool, 0)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Model != "" || dec.Reason != "no_cheaper_capable_candidate" {
		t.Fatalf("decision = %+v, want no generation route to embedding model", dec)
	}
}

func TestRulesRouterFailsClosedForUnknownDemandedCaps(t *testing.T) {
	f := Features{
		Provider:         "openai",
		Endpoint:         "/openai/v1/responses",
		CurrentModel:     "cave-auto",
		BaselineModel:    "gpt-5.5",
		BaselinePrice:    price(5, 30),
		ToolsCount:       1,
		JSONMode:         true,
		BodyModelRewrite: true,
	}
	pool := []Candidate{
		candidate("openai", "gpt-5.4-mini", price(0.6, 2.4), capsWithContext(128000, "responses_api")),
	}
	dec, err := (RulesRouter{}).Pick(f, pool, 0)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Model != "" {
		t.Fatalf("model = %q, want no route when tools/json caps absent", dec.Model)
	}
}

func TestRulesRouterFailsClosedWhenContextCapMissingOrTooSmall(t *testing.T) {
	f := Features{
		Provider:         "openai",
		Endpoint:         "/openai/v1/responses",
		CurrentModel:     "cave-auto",
		BaselineModel:    "gpt-5.5",
		BaselinePrice:    price(5, 30),
		InputBytes:       40000,
		BodyModelRewrite: true,
	}
	pool := []Candidate{
		candidate("openai", "missing-cap", price(0.6, 2.4), caps("responses_api")),
		candidate("openai", "too-small", price(0.6, 2.4), capsWithContext(100, "responses_api")),
	}
	dec, err := (RulesRouter{}).Pick(f, pool, 0)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Model != "" {
		t.Fatalf("model = %q, want no route with missing/small context caps", dec.Model)
	}
}

func TestRulesRouterFailsClosedForVisionWithoutCap(t *testing.T) {
	f := Features{
		Provider:         "openai",
		Endpoint:         "/openai/v1/responses",
		CurrentModel:     "cave-auto",
		BaselineModel:    "gpt-5.5",
		BaselinePrice:    price(5, 30),
		Vision:           true,
		BodyModelRewrite: true,
	}
	pool := []Candidate{
		candidate("openai", "text-only", price(0.6, 2.4), capsWithContext(128000, "responses_api")),
	}
	dec, err := (RulesRouter{}).Pick(f, pool, 0)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Model != "" {
		t.Fatalf("model = %q, want no route without vision cap", dec.Model)
	}
}

func TestRulesV1RejectsDegradedHealthAndWrongResidency(t *testing.T) {
	f := Features{
		Provider:         "openai",
		Endpoint:         "/openai/v1/responses",
		CurrentModel:     "cave-auto",
		BaselineModel:    "gpt-5.5",
		BaselinePrice:    price(5, 30),
		BodyModelRewrite: true,
	}
	pool := []Candidate{
		{
			Provider:      "openai",
			Model:         "degraded",
			Price:         price(0.5, 1),
			Caps:          capsWithContext(128000, "responses_api"),
			DataResidency: "eu",
			Health:        CandidateHealth{Degraded: true},
		},
		{
			Provider:      "openai",
			Model:         "wrong-region",
			Price:         price(0.5, 1),
			Caps:          capsWithContext(128000, "responses_api"),
			DataResidency: "us",
		},
		{
			Provider:      "openai",
			Model:         "good",
			Price:         price(0.8, 1.5),
			Caps:          capsWithContext(128000, "responses_api"),
			DataResidency: "eu",
		},
	}
	dec, err := (RulesV1Router{Policy: FrontierPolicy{DataResidency: []string{"eu"}, MaxErrorDelta: 0.02}}).Pick(f, pool, 0)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Model != "good" {
		t.Fatalf("model = %q, want good", dec.Model)
	}
	if dec.RejectionReasons["openai:degraded"] != "provider_health_degraded" {
		t.Fatalf("degraded rejection = %#v", dec.RejectionReasons["openai:degraded"])
	}
	if dec.RejectionReasons["openai:wrong-region"] != "data_residency_disallowed" {
		t.Fatalf("wrong-region rejection = %#v", dec.RejectionReasons["openai:wrong-region"])
	}
}

func TestFrontierV1QualityFloorBeatsCheapestPrice(t *testing.T) {
	f := Features{
		Provider:         "openai",
		Endpoint:         "/openai/v1/responses",
		CurrentModel:     "cave-auto",
		BaselineModel:    "gpt-5.5",
		BaselinePrice:    price(5, 30),
		BodyModelRewrite: true,
	}
	pool := []Candidate{
		{
			Provider:             "openai",
			Model:                "too-weak-cheap",
			Price:                price(0.1, 0.2),
			Caps:                 capsWithContext(128000, "responses_api"),
			QualityProb:          0.90,
			QualityLCB:           0.88,
			ExpectedP95LatencyMS: 120,
		},
		{
			Provider:             "openai",
			Model:                "quality-safe",
			Price:                price(0.8, 1.5),
			Caps:                 capsWithContext(128000, "responses_api"),
			QualityProb:          0.99,
			QualityLCB:           0.97,
			ExpectedP95LatencyMS: 140,
		},
	}
	dec, err := (FrontierRouter{Policy: FrontierPolicy{QualityFloor: 0.95, MaxP95LatencyDeltaMS: 200}}).Pick(f, pool, 0)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Model != "quality-safe" {
		t.Fatalf("model = %q, want quality-safe", dec.Model)
	}
	if dec.RejectionReasons["openai:too-weak-cheap"] != "quality_floor_violation" {
		t.Fatalf("cheap rejection = %#v", dec.RejectionReasons["openai:too-weak-cheap"])
	}
	if dec.QualityLCB["openai:quality-safe"] != 0.97 {
		t.Fatalf("quality trace = %#v", dec.QualityLCB)
	}
}

func TestFrontierV1QualityDeltaIsRelativeToBaselineModel(t *testing.T) {
	f := frontierDialFeatures()
	near := []Candidate{
		frontierDialCandidate("cheap", 0.25, 0.79, 120),
		frontierDialCandidate("reference", 1.50, 0.80, 180),
	}
	dec, err := (FrontierRouter{Policy: FrontierPolicy{MaxQualityDelta: 0.02}}).Pick(f, near, 1)
	if err != nil || dec.Model != "cheap" {
		t.Fatalf("relative quality delta rejected near-parity action: decision=%+v err=%v", dec, err)
	}
	degraded := append([]Candidate(nil), near...)
	degraded[0].QualityProb, degraded[0].QualityLCB = 0.76, 0.75
	dec, err = (FrontierRouter{Policy: FrontierPolicy{MaxQualityDelta: 0.02}}).Pick(f, degraded, 1)
	if err != nil || dec.Model != "reference" || dec.RejectionReasons["openai:cheap"] != "quality_floor_violation" {
		t.Fatalf("relative quality delta admitted degraded action: decision=%+v err=%v", dec, err)
	}
}

func TestFrontierV1DialZeroPicksMostCapablePassingCandidate(t *testing.T) {
	f := frontierDialFeatures()
	pool := []Candidate{
		frontierDialCandidate("cheap", 0.25, 0.96, 120),
		frontierDialCandidate("capable", 1.50, 0.995, 180),
	}
	dec, err := (FrontierRouter{Policy: FrontierPolicy{QualityFloor: 0.95}}).Pick(f, pool, 0)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Model != "capable" {
		t.Fatalf("model = %q, want most capable passing candidate at alpha=0", dec.Model)
	}
}

func TestFrontierV1DecisionCarriesCanonicalActionIdentity(t *testing.T) {
	f := frontierDialFeatures()
	candidate := frontierDialCandidate("capable", 0.5, 0.99, 100)
	candidate.Effort = "high"
	dec, err := (FrontierRouter{Policy: FrontierPolicy{QualityFloor: 0.95}}).Pick(f, []Candidate{candidate}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Provider != "openai" || dec.Model != "capable" || dec.Effort != "high" {
		t.Fatalf("decision action = %s/%s@%s", dec.Provider, dec.Model, dec.Effort)
	}
	if dec.ActionID != CandidateActionID(candidate) || len(dec.ActionID) != 32 {
		t.Fatalf("action_id = %q, want canonical 16-byte hex", dec.ActionID)
	}
}

func TestFrontierV1TraceSeparatesEffortActionsForSameModel(t *testing.T) {
	f := frontierDialFeatures()
	low := frontierDialCandidate("capable", 0.30, 0.97, 90)
	low.Effort = "low"
	high := frontierDialCandidate("capable", 0.60, 0.995, 130)
	high.Effort = "high"
	dec, err := (FrontierRouter{Policy: FrontierPolicy{QualityFloor: 0.95}}).Pick(f, []Candidate{low, high}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Effort != "high" {
		t.Fatalf("effort = %q, want high at quality-first endpoint", dec.Effort)
	}
	lowKey, highKey := CandidateTraceKey(low), CandidateTraceKey(high)
	if lowKey == highKey || len(dec.QualityLCB) != 2 {
		t.Fatalf("effort traces collided: low=%q high=%q trace=%v", lowKey, highKey, dec.QualityLCB)
	}
	if dec.QualityLCB[lowKey] != 0.97 || dec.QualityLCB[highKey] != 0.995 {
		t.Fatalf("quality trace = %v", dec.QualityLCB)
	}
}

func TestFrontierV1DialOnePicksCheapestPassingCandidate(t *testing.T) {
	f := frontierDialFeatures()
	pool := []Candidate{
		frontierDialCandidate("cheap", 0.25, 0.96, 120),
		frontierDialCandidate("capable", 1.50, 0.995, 180),
	}
	dec, err := (FrontierRouter{Policy: FrontierPolicy{QualityFloor: 0.95}}).Pick(f, pool, 1)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Model != "cheap" {
		t.Fatalf("model = %q, want cheapest passing candidate at alpha=1", dec.Model)
	}
}

func TestFrontierV1DialUsesNormalizedRegret(t *testing.T) {
	f := frontierDialFeatures()
	pool := []Candidate{
		frontierDialCandidate("cheap", 0.10, 0.95, 100),
		frontierDialCandidate("balanced", 0.45, 0.985, 140),
		frontierDialCandidate("capable", 1.10, 1.00, 200),
	}
	dec, err := (FrontierRouter{Policy: FrontierPolicy{QualityFloor: 0.95}}).Pick(f, pool, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Model != "balanced" {
		t.Fatalf("model = %q, want balanced normalized-regret candidate", dec.Model)
	}

	// Multiplying every cost by a constant must not change the cost/quality
	// tradeoff. Raw-dollar addition makes routing depend on catalog units.
	for i := range pool {
		pool[i].ExpectedCostUSD *= 1000
	}
	decScaled, err := (FrontierRouter{Policy: FrontierPolicy{QualityFloor: 0.95}}).Pick(f, pool, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if decScaled.Model != dec.Model {
		t.Fatalf("scaled cost changed route: before=%q after=%q", dec.Model, decScaled.Model)
	}
}

func TestFrontierV1DialTieBreaksByLatencyThenLabel(t *testing.T) {
	f := frontierDialFeatures()
	pool := []Candidate{
		frontierDialCandidate("slow", 0.50, 0.98, 220),
		frontierDialCandidate("z-fast", 0.50, 0.98, 100),
		frontierDialCandidate("a-fast", 0.50, 0.98, 100),
	}
	dec, err := (FrontierRouter{Policy: FrontierPolicy{QualityFloor: 0.95}}).Pick(f, pool, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Model != "a-fast" {
		t.Fatalf("model = %q, want deterministic latency/label tie-break", dec.Model)
	}
}

func TestFrontierV1DominatedOutlierCannotMoveDial(t *testing.T) {
	f := frontierDialFeatures()
	pool := []Candidate{
		frontierDialCandidate("cheap", 0.10, 0.95, 100),
		frontierDialCandidate("balanced", 0.45, 0.985, 140),
		frontierDialCandidate("capable", 1.10, 1.00, 200),
	}
	router := FrontierRouter{Policy: FrontierPolicy{QualityFloor: 0.95}}
	before, err := router.Pick(f, pool, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if before.Model != "balanced" {
		t.Fatalf("baseline model = %q, want balanced", before.Model)
	}

	// Worse quality, cost, and latency than capable. It is never rational, and
	// its extreme cost must not stretch normalization enough to move the dial.
	pool = append(pool, frontierDialCandidate("dominated-outlier", 100, 0.96, 500))
	after, err := router.Pick(f, pool, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if after.Model != before.Model {
		t.Fatalf("dominated outlier changed route: before=%q after=%q", before.Model, after.Model)
	}
	if got := after.RejectionReasons["openai:dominated-outlier"]; got != "pareto_dominated" {
		t.Fatalf("dominated rejection = %q, want pareto_dominated", got)
	}
}

func frontierDialFeatures() Features {
	return Features{
		Provider:         "openai",
		Endpoint:         "/openai/v1/responses",
		CurrentModel:     "cave-auto",
		BaselineModel:    "gpt-5.5",
		BaselinePrice:    price(5, 30),
		BodyModelRewrite: true,
	}
}

func frontierDialCandidate(model string, expectedCost, qualityLCB float64, latencyMS int) Candidate {
	return Candidate{
		Provider:             "openai",
		Model:                model,
		Price:                price(1, 2),
		Caps:                 capsWithContext(128000, "responses_api"),
		QualityProb:          qualityLCB,
		QualityLCB:           qualityLCB,
		ExpectedCostUSD:      expectedCost,
		ExpectedP95LatencyMS: latencyMS,
	}
}

func TestFrontierV1RequiresQualityLabels(t *testing.T) {
	f := Features{
		Provider:         "openai",
		Endpoint:         "/openai/v1/responses",
		CurrentModel:     "cave-auto",
		BaselineModel:    "gpt-5.5",
		BaselinePrice:    price(5, 30),
		BodyModelRewrite: true,
	}
	pool := []Candidate{candidate("openai", "unlabeled", price(0.5, 1), capsWithContext(128000, "responses_api"))}
	dec, err := (FrontierRouter{Policy: FrontierPolicy{QualityFloor: 0.95}}).Pick(f, pool, 0)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Model != "" || dec.RejectionReasons["openai:unlabeled"] != "missing_quality_score" {
		t.Fatalf("decision = %+v, want fail closed on missing label", dec)
	}
}

func TestFrontierV1RejectsNonFinitePredictions(t *testing.T) {
	f := frontierDialFeatures()
	pool := []Candidate{
		frontierDialCandidate("nan-quality", 0.2, math.NaN(), 100),
		frontierDialCandidate("inf-quality", 0.2, math.Inf(1), 100),
		frontierDialCandidate("nan-cost", math.NaN(), 0.99, 100),
		frontierDialCandidate("inf-cost", math.Inf(1), 0.99, 100),
	}
	dec, err := (FrontierRouter{Policy: FrontierPolicy{QualityFloor: 0.95}}).Pick(f, pool, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Model != "" {
		t.Fatalf("model = %q, want no route for non-finite predictions", dec.Model)
	}
	for _, model := range []string{"nan-quality", "inf-quality"} {
		if got := dec.RejectionReasons["openai:"+model]; got != "invalid_quality_score" {
			t.Fatalf("%s rejection = %q, want invalid_quality_score", model, got)
		}
	}
	for _, model := range []string{"nan-cost", "inf-cost"} {
		if got := dec.RejectionReasons["openai:"+model]; got != "invalid_expected_cost" {
			t.Fatalf("%s rejection = %q, want invalid_expected_cost", model, got)
		}
	}
}

func TestMergedSpecialistMustBeDeploymentReady(t *testing.T) {
	f := Features{
		Provider:         "openai",
		Endpoint:         "/openai/v1/responses",
		CurrentModel:     "cave-auto",
		BaselineModel:    "gpt-5.5",
		BaselinePrice:    price(5, 30),
		BodyModelRewrite: true,
	}
	pool := []Candidate{
		{
			Provider: "openai",
			Model:    "specialist-unreviewed",
			Price:    price(0.5, 1),
			Caps:     capsWithContext(128000, "responses_api"),
			MergedSpecialist: &MergedSpecialist{
				LicenseStatus: "approved",
				CustodyStatus: "approved",
				SafetyReview:  "pending",
				EvalSuite:     "support-v1",
				VersionHash:   "abc123",
			},
		},
	}
	dec, err := (RulesV1Router{}).Pick(f, pool, 0)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Model != "" || dec.RejectionReasons["openai:specialist-unreviewed"] != "merged_specialist_not_ready" {
		t.Fatalf("decision = %+v, want fail closed specialist", dec)
	}
}

func BenchmarkRulesV1RouterFiftyCandidates(b *testing.B) {
	f, pool := benchmarkRouteInputs(50, false)
	router := RulesV1Router{Policy: FrontierPolicy{DataResidency: []string{"eu"}, MaxP95LatencyDeltaMS: 200, MaxErrorDelta: 0.05, MaxCostRatio: 0.95}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		dec, err := router.Pick(f, pool, 0.4)
		if err != nil || dec.Model == "" {
			b.Fatalf("decision = %+v err=%v", dec, err)
		}
	}
}

func BenchmarkFrontierV1RouterFiftyCandidates(b *testing.B) {
	f, pool := benchmarkRouteInputs(50, true)
	router := FrontierRouter{Policy: FrontierPolicy{QualityFloor: 0.95, DataResidency: []string{"eu"}, MaxP95LatencyDeltaMS: 200, MaxErrorDelta: 0.05, MaxCostRatio: 0.95}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		dec, err := router.Pick(f, pool, 0.4)
		if err != nil || dec.Model == "" {
			b.Fatalf("decision = %+v err=%v", dec, err)
		}
	}
}

func benchmarkRouteInputs(n int, frontier bool) (Features, []Candidate) {
	f := Features{
		Provider:         "openai",
		Endpoint:         "/openai/v1/responses",
		CurrentModel:     "cave-auto",
		BaselineModel:    "gpt-5.5",
		BaselinePrice:    price(10, 30),
		BaselineP95MS:    400,
		InputBytes:       24000,
		BodyModelRewrite: true,
	}
	pool := make([]Candidate, 0, n)
	for i := 0; i < n; i++ {
		c := Candidate{
			Provider:             "openai",
			Model:                "candidate-" + string(rune('a'+(i%26))) + "-" + string(rune('a'+((i/26)%26))),
			Price:                price(0.2+float64(i)/10, 0.8+float64(i)/10),
			Caps:                 capsWithContext(128000, "responses_api"),
			DataResidency:        "eu",
			ExpectedP95LatencyMS: 120 + i%80,
		}
		if frontier {
			c.QualityProb = 0.99
			c.QualityLCB = 0.97
			c.ExpectedCostUSD = 0.001 + float64(i)/100000
		}
		pool = append(pool, c)
	}
	return f, pool
}

func price(input, output float64) cost.Price {
	return cost.Price{InputPerMillion: input, OutputPerMillion: output}
}

func candidate(provider, model string, price cost.Price, caps map[string]any) Candidate {
	return Candidate{Provider: provider, Model: model, Price: price, Caps: caps}
}

func caps(keys ...string) map[string]any {
	out := map[string]any{}
	for _, key := range keys {
		out[key] = true
	}
	return out
}

func capsWithContext(contextTokens float64, keys ...string) map[string]any {
	out := caps(keys...)
	out["context_window_tokens"] = contextTokens
	return out
}

// CW 14: the catalog may now record a capability as an explicit null when the
// vendor's docs genuinely do not say. That is only an honest answer if the
// router treats it exactly like absent — fail CLOSED. An unverified `true`
// would route traffic to a model that may not support the feature at all;
// unknown must cost the candidate, never the caller's request.
func TestUnknownCapabilityIsFailClosedLikeAbsent(t *testing.T) {
	features := map[string]Features{
		"tools":     {ToolsCount: 1, InputBytes: 100},
		"vision":    {Vision: true, InputBytes: 100},
		"json_mode": {JSONMode: true, InputBytes: 100},
	}
	for key, f := range features {
		t.Run(key, func(t *testing.T) {
			base := map[string]any{"context_window_tokens": 200000}
			absent := Candidate{Caps: base}
			unknown := Candidate{Caps: map[string]any{"context_window_tokens": 200000, key: nil}}
			supported := Candidate{Caps: map[string]any{"context_window_tokens": 200000, key: true}}

			if candidateSupports(absent, f) {
				t.Fatalf("absent %s was treated as supported", key)
			}
			if candidateSupports(unknown, f) {
				t.Fatalf("null %s was treated as supported — unknown must fail closed, not open", key)
			}
			if !candidateSupports(supported, f) {
				t.Fatalf("explicit %s:true was rejected", key)
			}
		})
	}
}

func TestCandidateActionIDSeparatesTupleFieldsAndEffort(t *testing.T) {
	base := Candidate{Provider: "openai", Model: "gpt-5.5", Effort: "low"}
	ids := map[string]bool{}
	for _, c := range []Candidate{
		base,
		{Provider: "openai", Model: "gpt-5.5", Effort: "high"},
		{Provider: "openai:gpt", Model: "5.5", Effort: "low"},
		{Provider: "openai", Model: "gpt", Effort: "5.5:low"},
		{Provider: "a\x00b", Model: "c", Effort: ""},
		{Provider: "a", Model: "b\x00c", Effort: ""},
	} {
		id := CandidateActionID(c)
		if len(id) != 32 {
			t.Fatalf("action id %q length = %d, want 32", id, len(id))
		}
		if ids[id] {
			t.Fatalf("action id collision for %+v", c)
		}
		ids[id] = true
	}
}

func TestCandidatePoolHashIncludesEffortAndIsOrderInvariant(t *testing.T) {
	low := Candidate{Provider: "openai", Model: "gpt-5.5", Effort: "low"}
	high := Candidate{Provider: "openai", Model: "gpt-5.5", Effort: "high"}
	if CandidatePoolHash([]Candidate{low}) == CandidatePoolHash([]Candidate{high}) {
		t.Fatal("candidate pool hash ignored effort")
	}
	forward := CandidatePoolHash([]Candidate{low, high})
	backward := CandidatePoolHash([]Candidate{high, low})
	if forward != backward {
		t.Fatalf("candidate pool hash depends on order: %q != %q", forward, backward)
	}
	legacy := Candidate{Provider: "openai", Model: "gpt-5.5"}
	if got, want := CandidatePoolHash([]Candidate{legacy}), legacyCandidatePoolHashForTest(legacy); got != want {
		t.Fatalf("empty-effort pool hash changed: got=%q want=%q", got, want)
	}
}

func legacyCandidatePoolHashForTest(c Candidate) string {
	sum := sha256.Sum256([]byte(c.Provider + ":" + c.Model))
	return hex.EncodeToString(sum[:])[:16]
}
