package routing

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"sort"
	"strings"

	"github.com/JuliusBrussee/caveman/shared/platform/cost"
)

type Router interface {
	Pick(f Features, pool []Candidate, alpha float64) (Decision, error)
}

type Features struct {
	Provider         string
	Endpoint         string
	CurrentModel     string
	BaselineModel    string
	BaselinePrice    cost.Price
	BaselineP95MS    int
	WorkflowSlug     string
	AgentSlug        string
	InputBytes       int
	OutputTokenPrior int
	Stream           bool
	ToolsCount       int
	JSONMode         bool
	Vision           bool
	Audio            bool
	BodyModelRewrite bool
}

// RouteAction is the v3 routing action. Provider/model/effort form one
// indivisible identity because effort changes quality, spend, and cache
// affinity on providers that support it.
type RouteAction struct {
	Provider string
	Model    string
	Effort   string
}

type Candidate struct {
	Provider             string
	Model                string
	Effort               string
	Price                cost.Price
	Caps                 map[string]any
	DataResidency        string
	QualityProb          float64
	QualityLCB           float64
	ExpectedCostUSD      float64
	ExpectedP95LatencyMS int
	ErrorRate            float64
	Health               CandidateHealth
	MergedSpecialist     *MergedSpecialist
}

type Decision struct {
	Provider                string
	Model                   string
	Effort                  string
	ActionID                string
	Ranked                  []Candidate
	Reason                  string
	Escalatable             bool
	RouterVersion           string
	CandidatePoolHash       string
	RejectionReasons        map[string]string
	QualityProb             map[string]float64
	QualityLCB              map[string]float64
	ExpectedCostUSD         map[string]float64
	ExpectedP95LatencyMS    map[string]uint32
	ExpectedCorrectiveTurns map[string]float64
	ExpectedEscalationProb  map[string]float64
	PredictionUncertainty   map[string]float64
	TrustedRouteHintUsed    bool
	RouteHintSource         string
}

type RulesRouter struct{}

type RulesV1Router struct {
	Policy FrontierPolicy
}

type FrontierRouter struct {
	Policy FrontierPolicy
}

type FrontierPolicy struct {
	RouterVersion        string
	QualityFloor         float64
	MaxQualityDelta      float64
	MaxP95LatencyDeltaMS int
	MaxErrorDelta        float64
	MaxEscalationRate    float64
	MaxCostRatio         float64
	CandidatePoolHash    string
	DataResidency        []string
	RouteDenylist        []string
	AllowCrossProvider   bool
}

type CandidateHealth struct {
	P50LatencyMS  int
	P95LatencyMS  int
	TTFBMS        int
	ErrorRate     float64
	TimeoutRate   float64
	RateLimitRate float64
	Degraded      bool
}

type MergedSpecialist struct {
	SourceModels          []string
	MergeMethod           string
	LicenseStatus         string
	CustodyStatus         string
	EvalSuite             string
	SafetyReview          string
	DeploymentEndpoint    string
	VersionHash           string
	TenantTrainingAllowed bool
}

func (RulesRouter) Pick(f Features, pool []Candidate, alpha float64) (Decision, error) {
	if !f.BodyModelRewrite {
		return Decision{Reason: "route_requires_body_model"}, nil
	}
	baselineUnit := unitPrice(f.BaselinePrice)
	if f.BaselineModel == "" || baselineUnit <= 0 {
		return Decision{Reason: "missing_priced_baseline"}, nil
	}
	rejected := map[string]string{}
	ranked := make([]Candidate, 0, len(pool))
	for _, c := range pool {
		if reason := rulesRejectReason(f, c, baselineUnit); reason != "" {
			if label := CandidateTraceKey(c); label != "" {
				rejected[label] = reason
			}
			continue
		}
		ranked = append(ranked, c)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		return unitPrice(ranked[i].Price) < unitPrice(ranked[j].Price)
	})
	if len(ranked) == 0 {
		return Decision{
			Reason:            "no_cheaper_capable_candidate",
			RouterVersion:     "rules-v0",
			CandidatePoolHash: CandidatePoolHash(pool),
			RejectionReasons:  rejected,
		}, nil
	}
	idx := alphaIndex(alpha, len(ranked))
	chosen := ranked[idx]
	return Decision{
		Provider:          chosen.Provider,
		Model:             chosen.Model,
		Effort:            chosen.Effort,
		ActionID:          CandidateActionID(chosen),
		Ranked:            ranked,
		Reason:            "cheaper_capable_candidate",
		RouterVersion:     "rules-v0",
		CandidatePoolHash: CandidatePoolHash(pool),
		RejectionReasons:  rejected,
	}, nil
}

func (r RulesV1Router) Pick(f Features, pool []Candidate, alpha float64) (Decision, error) {
	return pickRulesV1(f, pool, alpha, r.Policy)
}

func (r FrontierRouter) Pick(f Features, pool []Candidate, alpha float64) (Decision, error) {
	if !f.BodyModelRewrite {
		return Decision{Reason: "route_requires_body_model", RouterVersion: "frontier-v1"}, nil
	}
	baselineUnit := unitPrice(f.BaselinePrice)
	if f.BaselineModel == "" || baselineUnit <= 0 {
		return Decision{Reason: "missing_priced_baseline", RouterVersion: "frontier-v1"}, nil
	}
	policy := r.Policy
	if policy.RouterVersion == "" {
		policy.RouterVersion = "frontier-v1"
	}
	rejected := map[string]string{}
	qualityProb := map[string]float64{}
	qualityLCB := map[string]float64{}
	expectedCost := map[string]float64{}
	expectedLatency := map[string]uint32{}
	predicted := make([]Candidate, 0, len(pool))
	ranked := make([]Candidate, 0, len(pool))
	for _, c := range pool {
		label := CandidateTraceKey(c)
		if reason := frontierRejectReason(f, c, baselineUnit, policy); reason != "" {
			if label != "" {
				rejected[label] = reason
			}
			continue
		}
		if label != "" {
			qualityProb[label] = c.QualityProb
			qualityLCB[label] = c.QualityLCB
			expectedCost[label] = expectedCostUSD(c)
			expectedLatency[label] = uint32(expectedP95LatencyMS(c))
		}
		predicted = append(predicted, c)
	}
	effectiveQualityFloor := candidateQualityFloor(policy, predicted, f.BaselineModel)
	for _, candidate := range predicted {
		if candidate.QualityLCB < effectiveQualityFloor {
			rejected[CandidateTraceKey(candidate)] = "quality_floor_violation"
			continue
		}
		ranked = append(ranked, candidate)
	}
	selectable := paretoCandidates(ranked, rejected)
	alpha = normalizedAlpha(alpha)
	bounds := frontierBoundsFor(selectable)
	sort.SliceStable(selectable, func(i, j int) bool {
		return frontierLess(selectable[i], selectable[j], alpha, bounds)
	})
	// Keep all otherwise-eligible actions in Ranked. Session stickiness must be
	// able to validate an existing pin even when newer evidence makes that action
	// Pareto-dominated for fresh decisions. Normal pin-switch economics then move
	// it deliberately instead of treating it as an invalid policy action.
	sort.SliceStable(ranked, func(i, j int) bool {
		return frontierLess(ranked[i], ranked[j], alpha, bounds)
	})
	hash := policy.CandidatePoolHash
	if hash == "" {
		hash = CandidatePoolHash(pool)
	}
	if len(selectable) == 0 {
		return Decision{
			Reason:               "no_frontier_candidate_passed_gates",
			RouterVersion:        policy.RouterVersion,
			CandidatePoolHash:    hash,
			RejectionReasons:     rejected,
			QualityProb:          qualityProb,
			QualityLCB:           qualityLCB,
			ExpectedCostUSD:      expectedCost,
			ExpectedP95LatencyMS: expectedLatency,
		}, nil
	}
	chosen := selectable[0]
	return Decision{
		Provider:             chosen.Provider,
		Model:                chosen.Model,
		Effort:               chosen.Effort,
		ActionID:             CandidateActionID(chosen),
		Ranked:               ranked,
		Reason:               "frontier_quality_cost_latency_candidate",
		RouterVersion:        policy.RouterVersion,
		CandidatePoolHash:    hash,
		RejectionReasons:     rejected,
		QualityProb:          qualityProb,
		QualityLCB:           qualityLCB,
		ExpectedCostUSD:      expectedCost,
		ExpectedP95LatencyMS: expectedLatency,
	}, nil
}

func pickRulesV1(f Features, pool []Candidate, alpha float64, policy FrontierPolicy) (Decision, error) {
	if !f.BodyModelRewrite {
		return Decision{Reason: "route_requires_body_model", RouterVersion: "rules-v1"}, nil
	}
	baselineUnit := unitPrice(f.BaselinePrice)
	if f.BaselineModel == "" || baselineUnit <= 0 {
		return Decision{Reason: "missing_priced_baseline", RouterVersion: "rules-v1"}, nil
	}
	if policy.RouterVersion == "" {
		policy.RouterVersion = "rules-v1"
	}
	rejected := map[string]string{}
	ranked := make([]Candidate, 0, len(pool))
	for _, c := range pool {
		if reason := rulesRejectReason(f, c, baselineUnit); reason != "" {
			if label := CandidateTraceKey(c); label != "" {
				rejected[label] = reason
			}
			continue
		}
		if reason := policyRejectReason(f, c, baselineUnit, policy); reason != "" {
			if label := CandidateTraceKey(c); label != "" {
				rejected[label] = reason
			}
			continue
		}
		ranked = append(ranked, c)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		return unitPrice(ranked[i].Price) < unitPrice(ranked[j].Price)
	})
	hash := policy.CandidatePoolHash
	if hash == "" {
		hash = CandidatePoolHash(pool)
	}
	if len(ranked) == 0 {
		return Decision{Reason: "no_policy_safe_candidate", RouterVersion: policy.RouterVersion, CandidatePoolHash: hash, RejectionReasons: rejected}, nil
	}
	idx := alphaIndex(alpha, len(ranked))
	chosen := ranked[idx]
	return Decision{
		Provider:          chosen.Provider,
		Model:             chosen.Model,
		Effort:            chosen.Effort,
		ActionID:          CandidateActionID(chosen),
		Ranked:            ranked,
		Reason:            "rules_v1_policy_safe_candidate",
		RouterVersion:     policy.RouterVersion,
		CandidatePoolHash: hash,
		RejectionReasons:  rejected,
	}, nil
}

func alphaIndex(alpha float64, n int) int {
	if n <= 1 {
		return 0
	}
	if alpha < 0 {
		alpha = 0
	}
	if alpha > 1 {
		alpha = 1
	}
	return int(alpha * float64(n-1))
}

func unitPrice(p cost.Price) float64 {
	return p.InputPerMillion + p.OutputPerMillion + p.ReasoningPerMillion
}

func rulesRejectReason(f Features, c Candidate, baselineUnit float64) string {
	if c.Provider != f.Provider {
		return "provider_mismatch"
	}
	if c.Model == "" || c.Model == f.BaselineModel || c.Model == f.CurrentModel {
		return "not_candidate_model"
	}
	if unitPrice(c.Price) <= 0 {
		return "unpriced_candidate"
	}
	if unitPrice(c.Price) >= baselineUnit {
		return "not_cheaper_than_baseline"
	}
	if !candidateSupports(c, f) {
		return "capability_or_context_mismatch"
	}
	return ""
}

func policyRejectReason(f Features, c Candidate, baselineUnit float64, policy FrontierPolicy) string {
	if !policy.AllowCrossProvider && c.Provider != f.Provider {
		return "cross_provider_disabled"
	}
	if denylisted(c, policy.RouteDenylist) {
		return "route_denylisted"
	}
	if policy.MaxCostRatio > 0 && unitPrice(c.Price)/baselineUnit > policy.MaxCostRatio {
		return "cost_ratio_exceeded"
	}
	if reason := residencyRejectReason(c, policy.DataResidency); reason != "" {
		return reason
	}
	if c.Health.Degraded {
		return "provider_health_degraded"
	}
	if policy.MaxErrorDelta > 0 && (c.ErrorRate > policy.MaxErrorDelta || c.Health.ErrorRate > policy.MaxErrorDelta) {
		return "error_slo_exceeded"
	}
	if policy.MaxP95LatencyDeltaMS > 0 {
		limit := policy.MaxP95LatencyDeltaMS
		if f.BaselineP95MS > 0 {
			limit = f.BaselineP95MS + policy.MaxP95LatencyDeltaMS
		}
		if expectedP95LatencyMS(c) > limit {
			return "latency_slo_exceeded"
		}
	}
	if c.MergedSpecialist != nil && !mergedSpecialistReady(*c.MergedSpecialist) {
		return "merged_specialist_not_ready"
	}
	return ""
}

func frontierRejectReason(f Features, c Candidate, baselineUnit float64, policy FrontierPolicy) string {
	if reason := rulesRejectReason(f, c, baselineUnit); reason != "" {
		return reason
	}
	if reason := policyRejectReason(f, c, baselineUnit, policy); reason != "" {
		return reason
	}
	if !finite(c.QualityProb) || !finite(c.QualityLCB) {
		return "invalid_quality_score"
	}
	if c.ExpectedCostUSD < 0 || !finite(c.ExpectedCostUSD) {
		return "invalid_expected_cost"
	}
	if c.QualityProb <= 0 && c.QualityLCB <= 0 {
		return "missing_quality_score"
	}
	return ""
}

func candidateQualityFloor(policy FrontierPolicy, predicted []Candidate, baselineModel string) float64 {
	floor := 0.0
	if policy.QualityFloor > 0 && policy.QualityFloor <= 1 {
		floor = policy.QualityFloor
	}
	if policy.MaxQualityDelta > 0 && policy.MaxQualityDelta < 1 {
		reference := 0.0
		for _, candidate := range predicted {
			if candidate.Model == baselineModel {
				reference = math.Max(reference, candidate.QualityLCB)
			}
		}
		if reference == 0 {
			for _, candidate := range predicted {
				reference = math.Max(reference, candidate.QualityLCB)
			}
		}
		floor = math.Max(floor, reference-policy.MaxQualityDelta)
	}
	if floor == 0 {
		return 0.95
	}
	return math.Max(0, math.Min(1, floor))
}

type frontierBounds struct {
	minCost    float64
	maxCost    float64
	minQuality float64
	maxQuality float64
}

func frontierBoundsFor(candidates []Candidate) frontierBounds {
	if len(candidates) == 0 {
		return frontierBounds{}
	}
	b := frontierBounds{
		minCost:    expectedCostUSD(candidates[0]),
		maxCost:    expectedCostUSD(candidates[0]),
		minQuality: candidates[0].QualityLCB,
		maxQuality: candidates[0].QualityLCB,
	}
	for _, c := range candidates[1:] {
		cost := expectedCostUSD(c)
		b.minCost = math.Min(b.minCost, cost)
		b.maxCost = math.Max(b.maxCost, cost)
		b.minQuality = math.Min(b.minQuality, c.QualityLCB)
		b.maxQuality = math.Max(b.maxQuality, c.QualityLCB)
	}
	return b
}

// frontierUtility is normalized regret over the quality-floor-passing set.
// alpha=0 means most capable; alpha=1 means cheapest. Normalization keeps the
// operator dial invariant when catalog currency units or candidate price scale
// change. Latency is already an eligibility gate and is used only as a
// deterministic tie-break, never as an undocumented third dial weight.
func frontierUtility(c Candidate, alpha float64, bounds frontierBounds) float64 {
	qualityRegret := normalizedRegret(bounds.maxQuality-c.QualityLCB, bounds.maxQuality-bounds.minQuality)
	costRegret := normalizedRegret(expectedCostUSD(c)-bounds.minCost, bounds.maxCost-bounds.minCost)
	return (1-alpha)*qualityRegret + alpha*costRegret
}

func frontierLess(left, right Candidate, alpha float64, bounds frontierBounds) bool {
	leftUtility := frontierUtility(left, alpha, bounds)
	rightUtility := frontierUtility(right, alpha, bounds)
	if math.Abs(leftUtility-rightUtility) > 1e-12 {
		return leftUtility < rightUtility
	}
	if leftLatency, rightLatency := frontierLatencyMS(left), frontierLatencyMS(right); leftLatency != rightLatency {
		return leftLatency < rightLatency
	}
	if leftCost, rightCost := expectedCostUSD(left), expectedCostUSD(right); leftCost != rightCost {
		return leftCost < rightCost
	}
	if left.QualityLCB != right.QualityLCB {
		return left.QualityLCB > right.QualityLCB
	}
	return CandidateTraceKey(left) < CandidateTraceKey(right)
}

// paretoCandidates removes actions weakly worse in every selection dimension
// and strictly worse in at least one. Dominated actions cannot be selected and
// cannot stretch min/max normalization enough to move the operator dial.
func paretoCandidates(candidates []Candidate, rejected map[string]string) []Candidate {
	frontier := make([]Candidate, 0, len(candidates))
	for i, candidate := range candidates {
		dominated := false
		for j, other := range candidates {
			if i != j && candidateDominates(other, candidate) {
				dominated = true
				break
			}
		}
		if dominated {
			if label := CandidateTraceKey(candidate); label != "" {
				rejected[label] = "pareto_dominated"
			}
			continue
		}
		frontier = append(frontier, candidate)
	}
	return frontier
}

func candidateDominates(left, right Candidate) bool {
	leftCost, rightCost := expectedCostUSD(left), expectedCostUSD(right)
	leftLatency, rightLatency := frontierLatencyMS(left), frontierLatencyMS(right)
	if left.QualityLCB < right.QualityLCB || leftCost > rightCost || leftLatency > rightLatency {
		return false
	}
	return left.QualityLCB > right.QualityLCB || leftCost < rightCost || leftLatency < rightLatency
}

// Unknown latency sorts last and cannot dominate measured latency. Eligibility
// policy may permit unknown latency when no latency SLO is configured, but the
// selector must never reinterpret missing measurement as a zero-millisecond win.
func frontierLatencyMS(c Candidate) int {
	latency := expectedP95LatencyMS(c)
	if latency <= 0 {
		return int(^uint(0) >> 1)
	}
	return latency
}

func normalizedRegret(numerator, denominator float64) float64 {
	if denominator <= 0 {
		return 0
	}
	regret := numerator / denominator
	return math.Max(0, math.Min(1, regret))
}

func normalizedAlpha(alpha float64) float64 {
	if !finite(alpha) || alpha < 0 {
		return 0
	}
	if alpha > 1 {
		return 1
	}
	return alpha
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func expectedCostUSD(c Candidate) float64 {
	if c.ExpectedCostUSD > 0 {
		return c.ExpectedCostUSD
	}
	return unitPrice(c.Price)
}

func expectedP95LatencyMS(c Candidate) int {
	if c.ExpectedP95LatencyMS > 0 {
		return c.ExpectedP95LatencyMS
	}
	return c.Health.P95LatencyMS
}

func denylisted(c Candidate, denylist []string) bool {
	label := CandidateLabel(c)
	for _, item := range denylist {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if item == label || item == c.Model {
			return true
		}
	}
	return false
}

func residencyRejectReason(c Candidate, allowed []string) string {
	if len(allowed) == 0 {
		return ""
	}
	if strings.TrimSpace(c.DataResidency) == "" {
		return "missing_data_residency"
	}
	for _, region := range allowed {
		if strings.EqualFold(strings.TrimSpace(region), strings.TrimSpace(c.DataResidency)) {
			return ""
		}
	}
	return "data_residency_disallowed"
}

func mergedSpecialistReady(s MergedSpecialist) bool {
	return strings.TrimSpace(s.DeploymentEndpoint) != "" &&
		strings.TrimSpace(s.VersionHash) != "" &&
		strings.EqualFold(strings.TrimSpace(s.LicenseStatus), "approved") &&
		strings.EqualFold(strings.TrimSpace(s.CustodyStatus), "approved") &&
		strings.EqualFold(strings.TrimSpace(s.SafetyReview), "approved") &&
		strings.TrimSpace(s.EvalSuite) != ""
}

func candidateSupports(c Candidate, f Features) bool {
	if f.Stream && !boolCap(c.Caps, "streaming") {
		return false
	}
	if f.ToolsCount > 0 && !anyBoolCap(c.Caps, "tools", "tool_use", "function_calling") {
		return false
	}
	if f.JSONMode && !anyBoolCap(c.Caps, "json_mode", "structured_outputs", "response_format") {
		return false
	}
	if f.Vision && !anyBoolCap(c.Caps, "vision", "image_input", "multimodal") {
		return false
	}
	if f.Audio && !anyBoolCap(c.Caps, "audio", "audio_input", "multimodal") {
		return false
	}
	if f.InputBytes > 0 && !contextFits(c.Caps, f.InputBytes) {
		return false
	}
	if capName := endpointCapability(f.Provider, f.Endpoint); capName != "" && !boolCap(c.Caps, capName) {
		return false
	}
	return true
}

func contextFits(caps map[string]any, inputBytes int) bool {
	limit := numericCap(caps, "context_window_tokens")
	if limit <= 0 {
		limit = numericCap(caps, "max_input_tokens")
	}
	if limit <= 0 {
		return false
	}
	estimatedTokens := inputBytes / 4
	if estimatedTokens <= 0 {
		estimatedTokens = 1
	}
	return float64(estimatedTokens) <= limit
}

func endpointCapability(provider, endpoint string) string {
	switch provider {
	case "openai", "azure_openai", "openai_compatible":
		if contains(endpoint, "/v1/responses") {
			return "responses_api"
		}
		if contains(endpoint, "/v1/chat/completions") {
			return "chat_completions"
		}
		if contains(endpoint, "/v1/embeddings") {
			return "embeddings"
		}
	case "anthropic":
		if contains(endpoint, "/v1/messages") {
			return "messages_api"
		}
	case "gemini":
		if contains(endpoint, ":streamGenerateContent") {
			return "stream_generate_content"
		}
		if contains(endpoint, ":generateContent") || contains(endpoint, "/models/") {
			return "generate_content"
		}
	}
	return ""
}

func contains(s, substr string) bool {
	if substr == "" {
		return true
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func CandidateLabel(c Candidate) string {
	if c.Provider == "" || c.Model == "" {
		return ""
	}
	return c.Provider + ":" + c.Model
}

// CandidateTraceKey preserves the v2 provider:model key for model-only actions
// and adds explicit effort for v3 actions. It is human-readable telemetry;
// CandidateActionID remains the canonical delimiter-safe join identity.
func CandidateTraceKey(c Candidate) string {
	label := CandidateLabel(c)
	if label == "" || c.Effort == "" {
		return label
	}
	return label + "#effort=" + c.Effort
}

// CandidateActionID is a delimiter-safe, stable identity for the complete
// provider/model/effort tuple. Sixteen digest bytes are enough for compact
// telemetry joins; plain tuple fields remain beside it for operator display.
func CandidateActionID(c Candidate) string {
	if c.Provider == "" || c.Model == "" {
		return ""
	}
	hash := sha256.New()
	var length [8]byte
	for _, field := range []string{c.Provider, c.Model, c.Effort} {
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(field))
	}
	sum := hash.Sum(nil)
	return hex.EncodeToString(sum[:16])
}

func CandidatePoolHash(candidates []Candidate) string {
	labels := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if label := CandidateLabel(c); label != "" {
			if c.Effort != "" {
				label += "\x00" + c.Effort
			}
			labels = append(labels, label)
		}
	}
	sort.Strings(labels)
	sum := sha256.Sum256([]byte(strings.Join(labels, "\n")))
	return hex.EncodeToString(sum[:])[:16]
}

func boolCap(caps map[string]any, key string) bool {
	v, ok := caps[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

func anyBoolCap(caps map[string]any, keys ...string) bool {
	for _, key := range keys {
		if boolCap(caps, key) {
			return true
		}
	}
	return false
}

func numericCap(caps map[string]any, key string) float64 {
	v, ok := caps[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case uint64:
		return float64(n)
	case float64:
		return n
	case float32:
		return float64(n)
	default:
		return 0
	}
}
