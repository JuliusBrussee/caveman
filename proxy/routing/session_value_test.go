package routing

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/shared/platform/cost"
)

func TestSessionValueRouterSelectsBySessionCostAndQuality(t *testing.T) {
	now := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	pool := sessionValuePool()
	artifact := validSessionValueArtifact(t, pool, now)
	router := SessionValueRouter{Policy: FrontierPolicy{QualityFloor: 0.95, MaxCostRatio: 2}, Artifact: artifact}
	ctx := validSessionValueContext(pool, now)
	decision, err := router.PickSession(sessionValueFeatures(), pool, 1, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Model != "cheap" || decision.Effort != "low" || decision.Reason != "session_value_quality_cost_candidate" {
		t.Fatalf("decision = %+v, want cheap low", decision)
	}
	cheapKey := CandidateTraceKey(pool[0])
	if decision.ExpectedCostUSD[cheapKey] <= 0 || decision.QualityLCB[cheapKey] < 0.95 || decision.ExpectedCorrectiveTurns[cheapKey] < 0 {
		t.Fatalf("missing decision evidence: %+v", decision)
	}

	// Analytical warm-cache loss reverses cost ordering without changing learned
	// reward estimates.
	ctx.CacheTransitionCostUSD[CandidateActionID(pool[0])] = 2
	decision, err = router.PickSession(sessionValueFeatures(), pool, 1, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Model != "strong" || decision.Effort != "high" {
		t.Fatalf("cache-aware decision = %+v, want strong high", decision)
	}
}

func TestSessionValueRouterAllowsSameModelDifferentEffortActions(t *testing.T) {
	now := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	pool := sessionValuePool()
	pool[0].Model = "same"
	pool[1].Model = "same"
	features := sessionValueFeatures()
	features.BaselineModel = "same"
	artifact := validSessionValueArtifact(t, pool, now)
	ctx := validSessionValueContext(pool, now)
	decision, err := (SessionValueRouter{Policy: FrontierPolicy{QualityFloor: 0.95}, Artifact: artifact}).PickSession(features, pool, 1, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Model != "same" || decision.Effort != "low" {
		t.Fatalf("same-model effort action not selected: %+v", decision)
	}
}

func TestSessionValueRouterAppliesQualityDeltaRelativeToBaselineModel(t *testing.T) {
	now := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	pool := sessionValuePool()
	artifact := validSessionValueArtifact(t, pool, now)
	for index := range artifact.Actions {
		reward := 0.79
		if artifact.Actions[index].Model == "strong" {
			reward = 0.80
		}
		artifact.Actions[index].RewardLogit.Intercept = logitFixture(reward)
	}
	artifact, err := SealSessionValueArtifact(artifact)
	if err != nil {
		t.Fatal(err)
	}
	router := SessionValueRouter{Policy: FrontierPolicy{MaxQualityDelta: 0.02}, Artifact: artifact}
	decision, err := router.PickSession(sessionValueFeatures(), pool, 1, validSessionValueContext(pool, now))
	if err != nil || decision.Model != "cheap" {
		t.Fatalf("baseline-relative quality delta rejected near-parity cheap action: decision=%+v err=%v", decision, err)
	}

	for index := range artifact.Actions {
		if artifact.Actions[index].Model == "cheap" {
			artifact.Actions[index].RewardLogit.Intercept = logitFixture(0.75)
		}
	}
	artifact, err = SealSessionValueArtifact(artifact)
	if err != nil {
		t.Fatal(err)
	}
	decision, err = (SessionValueRouter{Policy: FrontierPolicy{MaxQualityDelta: 0.02}, Artifact: artifact}).PickSession(sessionValueFeatures(), pool, 1, validSessionValueContext(pool, now))
	if err != nil || decision.Model != "strong" || decision.RejectionReasons[CandidateTraceKey(pool[0])] != "quality_floor_violation" {
		t.Fatalf("baseline-relative quality delta admitted degraded action: decision=%+v err=%v", decision, err)
	}
}

func TestSessionValueRouterAllowsPolicyEligibleSubsetButBindsConfiguredPool(t *testing.T) {
	now := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	configuredPool := sessionValuePool()
	artifact := validSessionValueArtifact(t, configuredPool, now)
	ctx := validSessionValueContext(configuredPool, now)
	decision, err := (SessionValueRouter{Policy: FrontierPolicy{QualityFloor: 0.95}, Artifact: artifact}).PickSession(sessionValueFeatures(), configuredPool[:1], 1, ctx)
	if err != nil || decision.ActionID != CandidateActionID(configuredPool[0]) {
		t.Fatalf("eligible subset rejected: decision=%+v err=%v", decision, err)
	}
	ctx.CandidatePoolHash = strings.Repeat("0", 16)
	decision, err = (SessionValueRouter{Policy: FrontierPolicy{QualityFloor: 0.95}, Artifact: artifact}).PickSession(sessionValueFeatures(), configuredPool[:1], 1, ctx)
	if err != nil || decision.ActionID != "" || decision.Reason != "invalid_session_value_artifact" {
		t.Fatalf("configured pool drift accepted: decision=%+v err=%v", decision, err)
	}
}

func TestSessionValueRouterFailsClosedOnArtifactStateOrCostMismatch(t *testing.T) {
	now := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	pool := sessionValuePool()
	baseArtifact := validSessionValueArtifact(t, pool, now)
	baseContext := validSessionValueContext(pool, now)
	tests := map[string]struct {
		artifact SessionValuePolicyArtifact
		context  SessionValueContext
		reason   string
	}{
		"tampered artifact": {artifact: func() SessionValuePolicyArtifact {
			a := cloneSessionValueArtifact(t, baseArtifact)
			a.Actions[0].RewardLogit.Intercept++
			return a
		}(), context: baseContext, reason: "invalid_session_value_artifact"},
		"cross tenant":    {artifact: baseArtifact, context: func() SessionValueContext { c := baseContext; c.ProjectID = "other"; return c }(), reason: "invalid_session_value_artifact"},
		"expired":         {artifact: baseArtifact, context: func() SessionValueContext { c := baseContext; c.Now = now.Add(48 * time.Hour); return c }(), reason: "invalid_session_value_artifact"},
		"schema mismatch": {artifact: baseArtifact, context: func() SessionValueContext { c := baseContext; c.StateSchemaVersion = "unknown"; return c }(), reason: "session_value_state_schema_mismatch"},
		"policy mismatch": {artifact: baseArtifact, context: func() SessionValueContext { c := baseContext; c.PolicyVersion++; return c }(), reason: "session_value_policy_version_mismatch"},
		"required unavailable": {artifact: baseArtifact, context: func() SessionValueContext {
			c := cloneSessionValueContext(baseContext)
			c.State["turn_index"] = SessionValueFeatureValue{}
			return c
		}(), reason: "invalid_session_value_state"},
		"ood": {artifact: baseArtifact, context: func() SessionValueContext {
			c := cloneSessionValueContext(baseContext)
			c.State["turn_index"] = SessionValueFeatureValue{Value: 999, Available: true}
			return c
		}(), reason: "invalid_session_value_state"},
		"missing action cost": {artifact: baseArtifact, context: func() SessionValueContext {
			c := cloneSessionValueContext(baseContext)
			c.ImmediateCostUSD = map[string]float64{}
			return c
		}(), reason: "no_session_value_candidate_passed_gates"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			decision, err := (SessionValueRouter{Policy: FrontierPolicy{QualityFloor: 0.95}, Artifact: tc.artifact}).PickSession(sessionValueFeatures(), pool, 1, tc.context)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Model != "" || decision.Reason != tc.reason {
				t.Fatalf("decision = %+v, want fail-closed reason %q", decision, tc.reason)
			}
		})
	}
}

func TestValidateSessionValueArtifactRejectsUnsortedAndMalformedPolicy(t *testing.T) {
	now := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	pool := sessionValuePool()
	base := validSessionValueArtifact(t, pool, now)
	tests := map[string]func(SessionValuePolicyArtifact) SessionValuePolicyArtifact{
		"unsorted features": func(a SessionValuePolicyArtifact) SessionValuePolicyArtifact {
			a.FeatureSpecs[0], a.FeatureSpecs[1] = a.FeatureSpecs[1], a.FeatureSpecs[0]
			return a
		},
		"unsorted actions": func(a SessionValuePolicyArtifact) SessionValuePolicyArtifact {
			a.Actions[0], a.Actions[1] = a.Actions[1], a.Actions[0]
			return a
		},
		"wrong coefficient shape": func(a SessionValuePolicyArtifact) SessionValuePolicyArtifact {
			a.Actions[0].RewardLogit.Coefficients = nil
			return a
		},
		"nan evidence": func(a SessionValuePolicyArtifact) SessionValuePolicyArtifact {
			a.Actions[0].RewardEffectiveSampleSize = math.NaN()
			return a
		},
		"missing pool action": func(a SessionValuePolicyArtifact) SessionValuePolicyArtifact {
			a.Actions = a.Actions[:1]
			return a
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			artifact := mutate(cloneSessionValueArtifact(t, base))
			artifact, err := SealSessionValueArtifact(artifact)
			if err != nil {
				return
			}
			if err := ValidateSessionValueArtifact(artifact, "org-a", "project-a", CandidatePoolHash(pool), now); err == nil {
				t.Fatal("malformed artifact accepted")
			}
		})
	}
}

func validSessionValueArtifact(t *testing.T, pool []Candidate, now time.Time) SessionValuePolicyArtifact {
	t.Helper()
	specs := make([]SessionValueFeatureSpec, 0, len(SessionValueFeatureNames()))
	for _, name := range SessionValueFeatureNames() {
		spec := SessionValueFeatureSpec{Name: name, Scale: 1, Min: 0, Max: 10000, Required: name == "turn_index"}
		if name == "cache_read_tokens" {
			spec.Mean, spec.Scale = 100, 100
		}
		if name == "turn_index" {
			spec.Mean, spec.Scale, spec.Max = 5, 5, 100
		}
		specs = append(specs, spec)
	}
	actions := make([]SessionValueActionModel, 0, len(pool))
	for _, candidate := range pool {
		reward := 0.98
		residual := 0.2
		if candidate.Model == "strong" || candidate.Effort == "high" {
			reward = 0.995
			residual = 0.4
		}
		actions = append(actions, SessionValueActionModel{
			ActionID: CandidateActionID(candidate), Provider: candidate.Provider, Model: candidate.Model, Effort: candidate.Effort,
			RewardLogit:               linearFixture(logitFixture(reward), len(specs), 0.05),
			ResidualFutureCostLog1P:   linearFixture(math.Log1p(residual), len(specs), 0.02),
			CorrectiveTurnsLog1P:      linearFixture(math.Log1p(0.2), len(specs), 0.05),
			EscalationLogit:           linearFixture(logitFixture(0.05), len(specs), 0.05),
			RewardEffectiveSampleSize: 1000, CostEffectiveSampleSize: 1000,
			QualityCalibrationError: 0.002, RewardBrierScore: 0.02,
			RewardCalibrationSlope: 1,
		})
	}
	sortSessionValueActions(actions)
	artifact, err := SealSessionValueArtifact(SessionValuePolicyArtifact{
		Schema: SessionValueArtifactSchema, OrganizationID: "org-a", ProjectID: "project-a", PolicyVersion: 7,
		RouterVersion: SessionValueRouterVersion, EstimatorVersion: "session-value-estimator-v1",
		CandidatePoolHash: CandidatePoolHash(pool), StateSchemaVersion: SessionValueStateSchema,
		TrainingManifestHash: "sha256:" + strings.Repeat("a", 64), TrainingExtractor: "metadata-v1", OutcomeContractVersion: "outcome-v1",
		ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(24 * time.Hour), QualityUncertaintyZ: 1,
		MaxInversePropensity: 20, FeatureSpecs: specs, Actions: actions,
	})
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func sessionValuePool() []Candidate {
	caps := map[string]any{"responses_api": true, "context_window_tokens": float64(128000)}
	return []Candidate{
		{Provider: "openai", Model: "cheap", Effort: "low", Price: cost.Price{InputPerMillion: 1, OutputPerMillion: 4}, Caps: caps},
		{Provider: "openai", Model: "strong", Effort: "high", Price: cost.Price{InputPerMillion: 5, OutputPerMillion: 20}, Caps: caps},
	}
}

func sessionValueFeatures() Features {
	return Features{Provider: "openai", Endpoint: "/v1/responses", CurrentModel: "cave-auto", BaselineModel: "strong", BaselinePrice: cost.Price{InputPerMillion: 5, OutputPerMillion: 20}, BodyModelRewrite: true}
}

func validSessionValueContext(pool []Candidate, now time.Time) SessionValueContext {
	ctx := SessionValueContext{
		OrganizationID: "org-a", ProjectID: "project-a", PolicyVersion: 7, CandidatePoolHash: CandidatePoolHash(pool), Now: now, StateSchemaVersion: SessionValueStateSchema,
		State:            map[string]SessionValueFeatureValue{"turn_index": {Value: 2, Available: true}, "cache_read_tokens": {Value: 500, Available: true}},
		ImmediateCostUSD: map[string]float64{}, CacheTransitionCostUSD: map[string]float64{},
	}
	for i, candidate := range pool {
		ctx.ImmediateCostUSD[CandidateActionID(candidate)] = 0.1 + float64(i)*0.2
		ctx.CacheTransitionCostUSD[CandidateActionID(candidate)] = 0
	}
	return ctx
}

func cloneSessionValueContext(in SessionValueContext) SessionValueContext {
	out := in
	out.State = map[string]SessionValueFeatureValue{}
	out.ImmediateCostUSD = map[string]float64{}
	out.CacheTransitionCostUSD = map[string]float64{}
	for key, value := range in.State {
		out.State[key] = value
	}
	for key, value := range in.ImmediateCostUSD {
		out.ImmediateCostUSD[key] = value
	}
	for key, value := range in.CacheTransitionCostUSD {
		out.CacheTransitionCostUSD[key] = value
	}
	return out
}

func cloneSessionValueArtifact(t *testing.T, in SessionValuePolicyArtifact) SessionValuePolicyArtifact {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out SessionValuePolicyArtifact
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func linearFixture(intercept float64, features int, residual float64) SessionValueLinearModel {
	return SessionValueLinearModel{Intercept: intercept, Coefficients: make([]float64, features), MissingCoefficients: make([]float64, features), ResidualStdDev: residual}
}

func logitFixture(probability float64) float64 { return math.Log(probability / (1 - probability)) }

func sortSessionValueActions(actions []SessionValueActionModel) {
	for i := 0; i < len(actions); i++ {
		for j := i + 1; j < len(actions); j++ {
			if actions[j].ActionID < actions[i].ActionID {
				actions[i], actions[j] = actions[j], actions[i]
			}
		}
	}
}
