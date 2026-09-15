package routing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	SessionValueArtifactSchema               = "caveman.router.session-value-policy.v1"
	SessionValueStateSchema                  = "router-state-metadata-v1"
	SessionValueRouterVersion                = "session-value-v1"
	SessionValuePromotionMinEffectiveSamples = 100
	SessionValuePromotionMaxCalibrationError = 0.10
	SessionValuePromotionMaxBrierScore       = 0.25
)

// ValidateSessionValuePromotionEvidence applies evidence floors beyond
// structural artifact validity. Runtime may evaluate a structurally valid
// shadow artifact; traffic promotion requires calibrated sample floors.
func ValidateSessionValuePromotionEvidence(artifact SessionValuePolicyArtifact) error {
	if len(artifact.Actions) == 0 {
		return errors.New("session-value promotion artifact has no actions")
	}
	for _, action := range artifact.Actions {
		if action.RewardEffectiveSampleSize < SessionValuePromotionMinEffectiveSamples ||
			action.CostEffectiveSampleSize < SessionValuePromotionMinEffectiveSamples ||
			action.QualityCalibrationError > SessionValuePromotionMaxCalibrationError ||
			action.RewardBrierScore > SessionValuePromotionMaxBrierScore {
			return fmt.Errorf("session-value action %s lacks promotion evidence", action.ActionID)
		}
	}
	return nil
}

var sessionValueFeatureVocabulary = map[string]struct{}{
	"turn_index": {}, "user_turns": {}, "subagent_turns": {}, "tool_calls": {},
	"corrective_turns": {}, "input_tokens_estimate": {}, "tools_count": {},
	"stream": {}, "json_mode": {}, "vision": {}, "audio": {},
	"cache_read_tokens": {}, "cache_write_tokens": {}, "uncached_tokens": {},
	"cache_age_ms": {}, "spent_session_usd": {}, "budget_remaining_usd": {},
	"output_token_prior": {}, "recent_error_count": {}, "interrupted_recently": {},
	"complexity_ordinal": {},
}

type SessionValueFeatureSpec struct {
	Name     string  `json:"name"`
	Mean     float64 `json:"mean"`
	Scale    float64 `json:"scale"`
	Min      float64 `json:"min"`
	Max      float64 `json:"max"`
	Required bool    `json:"required"`
}

// SessionValueLinearModel uses feature-order-aligned coefficients. Missing
// coefficients make absence explicit rather than silently treating missing as
// observed zero.
type SessionValueLinearModel struct {
	Intercept           float64   `json:"intercept"`
	Coefficients        []float64 `json:"coefficients"`
	MissingCoefficients []float64 `json:"missing_coefficients"`
	ResidualStdDev      float64   `json:"residual_stddev"`
}

type SessionValueActionModel struct {
	ActionID                   string                  `json:"action_id"`
	Provider                   string                  `json:"provider"`
	Model                      string                  `json:"model"`
	Effort                     string                  `json:"effort,omitempty"`
	RewardLogit                SessionValueLinearModel `json:"reward_logit"`
	ResidualFutureCostLog1P    SessionValueLinearModel `json:"residual_future_cost_log1p"`
	CorrectiveTurnsLog1P       SessionValueLinearModel `json:"corrective_turns_log1p"`
	EscalationLogit            SessionValueLinearModel `json:"escalation_logit"`
	RewardEffectiveSampleSize  float64                 `json:"reward_effective_sample_size"`
	CostEffectiveSampleSize    float64                 `json:"cost_effective_sample_size"`
	QualityCalibrationError    float64                 `json:"quality_calibration_error"`
	RewardBrierScore           float64                 `json:"reward_brier_score"`
	RewardCalibrationIntercept float64                 `json:"reward_calibration_intercept"`
	RewardCalibrationSlope     float64                 `json:"reward_calibration_slope"`
}

type SessionValuePolicyArtifact struct {
	Schema                 string                    `json:"schema"`
	ArtifactHash           string                    `json:"artifact_hash"`
	OrganizationID         string                    `json:"organization_id"`
	ProjectID              string                    `json:"project_id"`
	PolicyVersion          int                       `json:"policy_version"`
	RouterVersion          string                    `json:"router_version"`
	EstimatorVersion       string                    `json:"estimator_version"`
	CandidatePoolHash      string                    `json:"candidate_pool_hash"`
	StateSchemaVersion     string                    `json:"state_schema_version"`
	TrainingManifestHash   string                    `json:"training_manifest_hash"`
	TrainingExtractor      string                    `json:"training_extractor_version"`
	OutcomeContractVersion string                    `json:"outcome_contract_version"`
	ValidFrom              time.Time                 `json:"valid_from"`
	ValidUntil             time.Time                 `json:"valid_until"`
	RollbackParentHash     string                    `json:"rollback_parent_hash,omitempty"`
	QualityUncertaintyZ    float64                   `json:"quality_uncertainty_z"`
	MaxInversePropensity   float64                   `json:"max_inverse_propensity"`
	FeatureSpecs           []SessionValueFeatureSpec `json:"feature_specs"`
	Actions                []SessionValueActionModel `json:"actions"`
}

type SessionValueFeatureValue struct {
	Value     float64
	Available bool
}

type SessionValueContext struct {
	OrganizationID         string
	ProjectID              string
	PolicyVersion          int
	CandidatePoolHash      string
	Now                    time.Time
	StateSchemaVersion     string
	State                  map[string]SessionValueFeatureValue
	ImmediateCostUSD       map[string]float64
	CacheTransitionCostUSD map[string]float64
}

type SessionValueRouter struct {
	Policy   FrontierPolicy
	Artifact SessionValuePolicyArtifact
}

type SessionValuePrediction struct {
	ActionID                string
	RewardProbability       float64
	QualityLCB              float64
	ExpectedResidualCostUSD float64
	ExpectedCostUSD         float64
	CostUCBUSD              float64
	ExpectedCorrectiveTurns float64
	EscalationProbability   float64
	QualityUncertainty      float64
}

func ComputeSessionValueArtifactHash(artifact SessionValuePolicyArtifact) (string, error) {
	artifact.ArtifactHash = ""
	raw, err := json.Marshal(artifact)
	if err != nil {
		return "", fmt.Errorf("marshal session-value artifact: %w", err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func SealSessionValueArtifact(artifact SessionValuePolicyArtifact) (SessionValuePolicyArtifact, error) {
	hash, err := ComputeSessionValueArtifactHash(artifact)
	if err != nil {
		return SessionValuePolicyArtifact{}, err
	}
	artifact.ArtifactHash = hash
	return artifact, nil
}

func ValidateSessionValueArtifact(artifact SessionValuePolicyArtifact, organizationID, projectID, candidatePoolHash string, now time.Time) error {
	if artifact.Schema != SessionValueArtifactSchema || artifact.StateSchemaVersion != SessionValueStateSchema {
		return errors.New("session-value artifact schema mismatch")
	}
	if strings.TrimSpace(artifact.OrganizationID) == "" || artifact.OrganizationID != strings.TrimSpace(organizationID) ||
		strings.TrimSpace(artifact.ProjectID) == "" || artifact.ProjectID != strings.TrimSpace(projectID) {
		return errors.New("session-value artifact tenant scope mismatch")
	}
	if artifact.PolicyVersion <= 0 || artifact.RouterVersion != SessionValueRouterVersion || strings.TrimSpace(artifact.EstimatorVersion) == "" {
		return errors.New("session-value artifact version identity invalid")
	}
	if !validCompactPoolHash(artifact.CandidatePoolHash) || artifact.CandidatePoolHash != candidatePoolHash {
		return errors.New("session-value artifact candidate pool mismatch")
	}
	if !validSHA256Ref(artifact.TrainingManifestHash) || strings.TrimSpace(artifact.TrainingExtractor) == "" || strings.TrimSpace(artifact.OutcomeContractVersion) == "" {
		return errors.New("session-value artifact training lineage invalid")
	}
	if artifact.ValidFrom.IsZero() || artifact.ValidUntil.IsZero() || !artifact.ValidUntil.After(artifact.ValidFrom) || now.Before(artifact.ValidFrom) || !now.Before(artifact.ValidUntil) {
		return errors.New("session-value artifact outside validity window")
	}
	if artifact.RollbackParentHash != "" && (!validSHA256Ref(artifact.RollbackParentHash) || artifact.RollbackParentHash == artifact.ArtifactHash) {
		return errors.New("session-value artifact rollback lineage invalid")
	}
	if !finite(artifact.QualityUncertaintyZ) || artifact.QualityUncertaintyZ <= 0 || artifact.QualityUncertaintyZ > 5 ||
		!finite(artifact.MaxInversePropensity) || artifact.MaxInversePropensity < 1 || artifact.MaxInversePropensity > 100 {
		return errors.New("session-value artifact confidence policy invalid")
	}
	featureNames := SessionValueFeatureNames()
	if len(artifact.FeatureSpecs) != len(featureNames) || len(artifact.Actions) == 0 {
		return errors.New("session-value artifact has no features or actions")
	}
	for i, spec := range artifact.FeatureSpecs {
		if spec.Name != featureNames[i] || (i > 0 && artifact.FeatureSpecs[i-1].Name >= spec.Name) {
			return errors.New("session-value artifact feature vocabulary or order invalid")
		}
		if spec.Name == "turn_index" && !spec.Required {
			return errors.New("session-value artifact must require turn_index")
		}
		if !finite(spec.Mean) || !finite(spec.Scale) || spec.Scale <= 0 || !finite(spec.Min) || !finite(spec.Max) || spec.Min < 0 || spec.Max < spec.Min {
			return fmt.Errorf("session-value feature %q bounds invalid", spec.Name)
		}
	}
	seenActions := map[string]struct{}{}
	artifactPool := make([]Candidate, 0, len(artifact.Actions))
	for i, action := range artifact.Actions {
		wantID := CandidateActionID(Candidate{Provider: action.Provider, Model: action.Model, Effort: action.Effort})
		if wantID == "" || action.ActionID != wantID || (i > 0 && artifact.Actions[i-1].ActionID >= action.ActionID) {
			return errors.New("session-value artifact action identity or order invalid")
		}
		if _, duplicate := seenActions[action.ActionID]; duplicate {
			return errors.New("session-value artifact duplicate action")
		}
		seenActions[action.ActionID] = struct{}{}
		artifactPool = append(artifactPool, Candidate{Provider: action.Provider, Model: action.Model, Effort: action.Effort})
		if !finite(action.RewardEffectiveSampleSize) || action.RewardEffectiveSampleSize <= 0 ||
			!finite(action.CostEffectiveSampleSize) || action.CostEffectiveSampleSize <= 0 ||
			!probabilityMetric(action.QualityCalibrationError) || !probabilityMetric(action.RewardBrierScore) ||
			!finite(action.RewardCalibrationIntercept) || !finite(action.RewardCalibrationSlope) || action.RewardCalibrationSlope <= 0 || action.RewardCalibrationSlope > 10 {
			return errors.New("session-value artifact action evidence invalid")
		}
		for _, model := range []SessionValueLinearModel{action.RewardLogit, action.ResidualFutureCostLog1P, action.CorrectiveTurnsLog1P, action.EscalationLogit} {
			if err := validateSessionValueLinearModel(model, len(artifact.FeatureSpecs)); err != nil {
				return err
			}
		}
	}
	if CandidatePoolHash(artifactPool) != artifact.CandidatePoolHash {
		return errors.New("session-value artifact action registry does not cover candidate pool")
	}
	wantHash, err := ComputeSessionValueArtifactHash(artifact)
	if err != nil || artifact.ArtifactHash != wantHash {
		return errors.New("session-value artifact hash mismatch")
	}
	return nil
}

func (r SessionValueRouter) PickSession(features Features, pool []Candidate, alpha float64, ctx SessionValueContext) (Decision, error) {
	poolHash := strings.TrimSpace(ctx.CandidatePoolHash)
	if !validCompactPoolHash(poolHash) {
		return Decision{Reason: "missing_session_value_candidate_pool", RouterVersion: SessionValueRouterVersion}, nil
	}
	now := ctx.Now.UTC()
	if now.IsZero() {
		return Decision{Reason: "missing_session_value_time", RouterVersion: SessionValueRouterVersion, CandidatePoolHash: poolHash}, nil
	}
	if ctx.StateSchemaVersion != SessionValueStateSchema {
		return Decision{Reason: "session_value_state_schema_mismatch", RouterVersion: SessionValueRouterVersion, CandidatePoolHash: poolHash}, nil
	}
	if ctx.PolicyVersion <= 0 || ctx.PolicyVersion != r.Artifact.PolicyVersion {
		return Decision{Reason: "session_value_policy_version_mismatch", RouterVersion: SessionValueRouterVersion, CandidatePoolHash: poolHash}, nil
	}
	if err := ValidateSessionValueArtifact(r.Artifact, ctx.OrganizationID, ctx.ProjectID, poolHash, now); err != nil {
		return Decision{Reason: "invalid_session_value_artifact", RouterVersion: SessionValueRouterVersion, CandidatePoolHash: poolHash}, nil
	}
	vector, missing, err := sessionValueVector(r.Artifact.FeatureSpecs, ctx.State)
	if err != nil {
		return Decision{Reason: "invalid_session_value_state", RouterVersion: SessionValueRouterVersion, CandidatePoolHash: poolHash}, nil
	}
	if !features.BodyModelRewrite {
		return Decision{Reason: "route_requires_body_model", RouterVersion: SessionValueRouterVersion, CandidatePoolHash: poolHash}, nil
	}
	baselineUnit := unitPrice(features.BaselinePrice)
	if features.BaselineModel == "" || baselineUnit <= 0 {
		return Decision{Reason: "missing_priced_baseline", RouterVersion: SessionValueRouterVersion, CandidatePoolHash: poolHash}, nil
	}

	models := make(map[string]SessionValueActionModel, len(r.Artifact.Actions))
	for _, model := range r.Artifact.Actions {
		models[model.ActionID] = model
	}
	rejected := map[string]string{}
	qualityProb := map[string]float64{}
	qualityLCB := map[string]float64{}
	expectedCost := map[string]float64{}
	expectedCorrections := map[string]float64{}
	escalationProb := map[string]float64{}
	uncertainty := map[string]float64{}
	predicted := make([]Candidate, 0, len(pool))
	ranked := make([]Candidate, 0, len(pool))
	for _, candidate := range pool {
		label := CandidateTraceKey(candidate)
		actionID := CandidateActionID(candidate)
		model, ok := models[actionID]
		if !ok {
			rejected[label] = "missing_action_value_model"
			continue
		}
		if reason := sessionValueEligibilityReason(features, candidate, baselineUnit, r.Policy); reason != "" {
			rejected[label] = reason
			continue
		}
		immediate, immediateOK := ctx.ImmediateCostUSD[actionID]
		transition, transitionOK := ctx.CacheTransitionCostUSD[actionID]
		if !immediateOK || !transitionOK || !finite(immediate) || immediate <= 0 || !finite(transition) || transition < 0 {
			rejected[label] = "missing_analytical_action_cost"
			continue
		}
		prediction, predictionErr := predictSessionValueAction(r.Artifact, model, vector, missing, immediate, transition)
		if predictionErr != nil {
			rejected[label] = "invalid_session_value_prediction"
			continue
		}
		candidate.QualityProb = prediction.RewardProbability
		candidate.QualityLCB = prediction.QualityLCB
		candidate.ExpectedCostUSD = prediction.CostUCBUSD
		candidate.ErrorRate = prediction.EscalationProbability
		qualityProb[label], qualityLCB[label], expectedCost[label] = prediction.RewardProbability, prediction.QualityLCB, candidate.ExpectedCostUSD
		expectedCorrections[label], escalationProb[label], uncertainty[label] = prediction.ExpectedCorrectiveTurns, prediction.EscalationProbability, prediction.QualityUncertainty
		predicted = append(predicted, candidate)
	}
	effectiveQualityFloor := candidateQualityFloor(r.Policy, predicted, features.BaselineModel)
	for _, candidate := range predicted {
		label := CandidateTraceKey(candidate)
		if candidate.QualityLCB < effectiveQualityFloor {
			rejected[label] = "quality_floor_violation"
			continue
		}
		if r.Policy.MaxEscalationRate > 0 && candidate.ErrorRate > r.Policy.MaxEscalationRate {
			rejected[label] = "escalation_rate_exceeded"
			continue
		}
		ranked = append(ranked, candidate)
	}
	selectable := paretoCandidates(ranked, rejected)
	bounds := frontierBoundsFor(selectable)
	alpha = normalizedAlpha(alpha)
	sort.SliceStable(selectable, func(i, j int) bool { return frontierLess(selectable[i], selectable[j], alpha, bounds) })
	sort.SliceStable(ranked, func(i, j int) bool { return frontierLess(ranked[i], ranked[j], alpha, bounds) })
	decision := Decision{
		Ranked: ranked, RouterVersion: r.Artifact.RouterVersion, CandidatePoolHash: poolHash,
		RejectionReasons: rejected, QualityProb: qualityProb, QualityLCB: qualityLCB,
		ExpectedCostUSD: expectedCost, ExpectedCorrectiveTurns: expectedCorrections,
		ExpectedEscalationProb: escalationProb, PredictionUncertainty: uncertainty,
	}
	if len(selectable) == 0 {
		decision.Reason = "no_session_value_candidate_passed_gates"
		return decision, nil
	}
	chosen := selectable[0]
	decision.Provider, decision.Model, decision.Effort = chosen.Provider, chosen.Model, chosen.Effort
	decision.ActionID = CandidateActionID(chosen)
	decision.Reason = "session_value_quality_cost_candidate"
	return decision, nil
}

// PredictSessionValueAction is shared scorer primitive for online selection and
// offline evaluation. It validates artifact, scope, state, and analytical costs
// before returning any estimate.
func PredictSessionValueAction(artifact SessionValuePolicyArtifact, actionID string, ctx SessionValueContext) (SessionValuePrediction, error) {
	now := ctx.Now.UTC()
	if now.IsZero() || ctx.StateSchemaVersion != SessionValueStateSchema || ctx.PolicyVersion <= 0 || ctx.PolicyVersion != artifact.PolicyVersion {
		return SessionValuePrediction{}, errors.New("session-value prediction context invalid")
	}
	if err := ValidateSessionValueArtifact(artifact, ctx.OrganizationID, ctx.ProjectID, ctx.CandidatePoolHash, now); err != nil {
		return SessionValuePrediction{}, err
	}
	vector, missing, err := sessionValueVector(artifact.FeatureSpecs, ctx.State)
	if err != nil {
		return SessionValuePrediction{}, err
	}
	var action SessionValueActionModel
	found := false
	for _, candidate := range artifact.Actions {
		if candidate.ActionID == actionID {
			action, found = candidate, true
			break
		}
	}
	if !found {
		return SessionValuePrediction{}, errors.New("session-value action model missing")
	}
	immediate, immediateOK := ctx.ImmediateCostUSD[actionID]
	transition, transitionOK := ctx.CacheTransitionCostUSD[actionID]
	if !immediateOK || !transitionOK || !finite(immediate) || immediate <= 0 || !finite(transition) || transition < 0 {
		return SessionValuePrediction{}, errors.New("session-value analytical action cost missing")
	}
	return predictSessionValueAction(artifact, action, vector, missing, immediate, transition)
}

func predictSessionValueAction(artifact SessionValuePolicyArtifact, model SessionValueActionModel, vector, missing []float64, immediate, transition float64) (SessionValuePrediction, error) {
	rawRewardLogit := predictSessionValue(model.RewardLogit, vector, missing)
	reward := logistic(model.RewardCalibrationIntercept + model.RewardCalibrationSlope*rawRewardLogit)
	residualCostLog := predictSessionValue(model.ResidualFutureCostLog1P, vector, missing)
	residualCost := math.Max(0, math.Expm1(residualCostLog))
	corrections := math.Max(0, math.Expm1(predictSessionValue(model.CorrectiveTurnsLog1P, vector, missing)))
	escalation := logistic(predictSessionValue(model.EscalationLogit, vector, missing))
	qualityError := math.Max(model.QualityCalibrationError, math.Sqrt(math.Max(reward*(1-reward), 1e-9)/model.RewardEffectiveSampleSize))
	lcb := math.Max(0, reward-artifact.QualityUncertaintyZ*qualityError)
	costUCBResidual := math.Expm1(residualCostLog + artifact.QualityUncertaintyZ*math.Max(0, model.ResidualFutureCostLog1P.ResidualStdDev))
	costUCBResidual = math.Max(residualCost, costUCBResidual)
	prediction := SessionValuePrediction{
		ActionID: model.ActionID, RewardProbability: reward, QualityLCB: lcb,
		ExpectedResidualCostUSD: residualCost, ExpectedCostUSD: immediate + transition + residualCost,
		CostUCBUSD: immediate + transition + costUCBResidual, ExpectedCorrectiveTurns: corrections,
		EscalationProbability: escalation, QualityUncertainty: qualityError,
	}
	for _, value := range []float64{prediction.RewardProbability, prediction.QualityLCB, prediction.ExpectedResidualCostUSD, prediction.ExpectedCostUSD, prediction.CostUCBUSD, prediction.ExpectedCorrectiveTurns, prediction.EscalationProbability, prediction.QualityUncertainty} {
		if !finite(value) || value < 0 {
			return SessionValuePrediction{}, errors.New("session-value prediction non-finite")
		}
	}
	return prediction, nil
}

func sessionValueVector(specs []SessionValueFeatureSpec, state map[string]SessionValueFeatureValue) ([]float64, []float64, error) {
	for name := range state {
		if _, known := sessionValueFeatureVocabulary[name]; !known {
			return nil, nil, fmt.Errorf("unknown session-value feature %q", name)
		}
	}
	values := make([]float64, len(specs))
	missing := make([]float64, len(specs))
	for i, spec := range specs {
		feature, present := state[spec.Name]
		if !present || !feature.Available {
			if spec.Required {
				return nil, nil, fmt.Errorf("required session-value feature %q unavailable", spec.Name)
			}
			missing[i] = 1
			continue
		}
		if !finite(feature.Value) || feature.Value < spec.Min || feature.Value > spec.Max {
			return nil, nil, fmt.Errorf("session-value feature %q outside artifact bounds", spec.Name)
		}
		values[i] = (feature.Value - spec.Mean) / spec.Scale
	}
	return values, missing, nil
}

func sessionValueEligibilityReason(features Features, candidate Candidate, baselineUnit float64, policy FrontierPolicy) string {
	if candidate.Provider == "" || candidate.Model == "" || unitPrice(candidate.Price) <= 0 {
		return "unpriced_or_invalid_candidate"
	}
	if !policy.AllowCrossProvider && candidate.Provider != features.Provider {
		return "cross_provider_disabled"
	}
	if !candidateSupports(candidate, features) {
		return "capability_or_context_mismatch"
	}
	return policyRejectReason(features, candidate, baselineUnit, policy)
}

func predictSessionValue(model SessionValueLinearModel, values, missing []float64) float64 {
	out := model.Intercept
	for i := range values {
		out += model.Coefficients[i]*values[i] + model.MissingCoefficients[i]*missing[i]
	}
	return out
}

func validateSessionValueLinearModel(model SessionValueLinearModel, features int) error {
	if len(model.Coefficients) != features || len(model.MissingCoefficients) != features || !finite(model.Intercept) || !finite(model.ResidualStdDev) || model.ResidualStdDev < 0 {
		return errors.New("session-value linear model shape invalid")
	}
	for _, value := range append(append([]float64{}, model.Coefficients...), model.MissingCoefficients...) {
		if !finite(value) {
			return errors.New("session-value linear model coefficient invalid")
		}
	}
	return nil
}

func logistic(value float64) float64 {
	if value >= 0 {
		z := math.Exp(-math.Min(value, 700))
		return 1 / (1 + z)
	}
	z := math.Exp(math.Max(value, -700))
	return z / (1 + z)
}

func probabilityMetric(value float64) bool {
	return finite(value) && value >= 0 && value <= 1
}

func validCompactPoolHash(value string) bool {
	if len(value) != 16 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func validSHA256Ref(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

// SessionValueFeatureNames returns stable metadata-only vocabulary used by
// extractors, trainers, and hot-path evaluators.
func SessionValueFeatureNames() []string {
	names := make([]string, 0, len(sessionValueFeatureVocabulary))
	for name := range sessionValueFeatureVocabulary {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
