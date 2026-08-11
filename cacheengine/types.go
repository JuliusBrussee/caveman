package cacheengine

import (
	"context"
	"time"
)

// Mode describes provider cache control semantics without naming a provider.
type Mode string

const (
	ModeUnsupported Mode = "unsupported"
	ModeImplicit    Mode = "implicit"
	ModeAffinity    Mode = "affinity"
	ModeExplicit    Mode = "explicit"
)

// Attribution describes what one provider-confirmed cache hit proves.
type Attribution string

const (
	AttributionNone     Attribution = "none"
	AttributionOrganic  Attribution = "organic"
	AttributionAffinity Attribution = "affinity"
	AttributionCausal   Attribution = "causal"
)

// Decision describes engine action for one plan or provider-native request.
type Decision string

const (
	DecisionApply       Decision = "apply"
	DecisionObserveOnly Decision = "observe_only"
	DecisionPassThrough Decision = "pass_through"
	DecisionNewEpoch    Decision = "new_epoch"
)

const (
	ReasonApplied              = "applied"
	ReasonProviderManaged      = "provider_managed"
	ReasonUnsupported          = "unsupported"
	ReasonRecordMode           = "record_mode"
	ReasonNonPAYG              = "non_payg"
	ReasonMalformedRequest     = "malformed_request"
	ReasonCallerManaged        = "caller_managed"
	ReasonProfileMismatch      = "profile_mismatch"
	ReasonNoStablePrefix       = "no_stable_prefix"
	ReasonVolatilePrefix       = "volatile_prefix"
	ReasonPrefixDrift          = "prefix_drift"
	ReasonBelowMinimum         = "below_minimum"
	ReasonNoExpectedReuse      = "no_expected_reuse"
	ReasonNegativeEconomics    = "negative_economics"
	ReasonTransformUnavailable = "transform_unavailable"
	ReasonAffinityFallback     = "affinity_fallback"
)

const (
	AnthropicStableOptimizerID  = "anthropic-cache-breakpoints"
	AnthropicRollingOptimizerID = "cave-cache-anthropic-rolling-v1"
	OpenAIKeyOptimizerID        = "openai-prompt-cache-key"
	OpenAIExplicitOptimizerID   = "cave-cache-openai-explicit-v1"
	BedrockCacheOptimizerID     = "bedrock-cache-points"
	BedrockRollingOptimizerID   = "cave-cache-bedrock-rolling-v1"
)

// Profile is capability data consumed by planner. Custom providers can supply
// profiles without changing planner or importing gateway code.
type Profile struct {
	ID              string
	Provider        string
	Mode            Mode
	Attribution     Attribution
	MinPrefixTokens int
	MaxBreakpoints  int
	EconomicsKnown  bool
	WriteMultiplier float64
	ReadMultiplier  float64
	TTL             time.Duration
	// Rolling means native/provider behavior advances cache boundary with agent
	// history while retaining exact-prefix reuse from prior requests.
	Rolling      bool
	RoutingKey   bool
	MaxRPMPerKey int
	OptimizerID  string
}

// Segment is one ordered prompt-prefix component. Stable is caller-owned truth:
// content that may change inside an epoch must never be labelled stable.
type Segment struct {
	Name      string
	Content   []byte
	Tokens    int
	Stable    bool
	Cacheable bool
	// ExpectedCalls is total calls expected to share this prefix while provider
	// entry remains warm. Zero inherits PlanRequest.ExpectedCalls.
	ExpectedCalls int
}

// PlanRequest contains ordered stable segments and provider capability data.
type PlanRequest struct {
	Scope                     string
	Epoch                     string
	PartitionKey              string
	ExpectedRequestsPerMinute int
	// ExpectedCalls is total calls expected to share prefix within profile TTL.
	// Zero uses conservative one-write/one-read plan.
	ExpectedCalls int
	Profile       Profile
	Segments      []Segment
}

// Breakpoint is one profitable provider-cache boundary in ordered stable input.
type Breakpoint struct {
	AfterSegment              string  `json:"after_segment"`
	PrefixSHA256              string  `json:"prefix_sha256"`
	PrefixTokens              int     `json:"prefix_tokens"`
	ExpectedCalls             int     `json:"expected_calls"`
	BreakEvenCalls            int     `json:"break_even_calls"`
	ExpectedNetInputRateUnits float64 `json:"expected_net_input_rate_units"`
	index                     int
}

// Plan uses input-rate units, not dollars. One unit is one token billed at full
// input rate. Caller may price it only with grounded provider catalog data.
type Plan struct {
	Decision                  Decision     `json:"decision"`
	Reason                    string       `json:"reason"`
	ProfileID                 string       `json:"profile_id"`
	Mode                      Mode         `json:"mode"`
	Attribution               Attribution  `json:"attribution"`
	PrefixSHA256              string       `json:"prefix_sha256,omitempty"`
	RoutingKey                string       `json:"routing_key,omitempty"`
	KeyShard                  int          `json:"key_shard"`
	KeyShardCount             int          `json:"key_shard_count"`
	Breakpoints               []Breakpoint `json:"breakpoints,omitempty"`
	ExpectedNetInputRateUnits float64      `json:"expected_net_input_rate_units"`
	EconomicsBasis            string       `json:"economics_basis"`
	Warnings                  []string     `json:"warnings,omitempty"`
}

// Config controls engine resource limits and custom-provider integration.
type Config struct {
	// MaxKeyShards bounds automatic OpenAI-style affinity partitioning. Zero uses
	// 64; valid explicit values are 1..1,000,000. Routing keys remain stable
	// within PartitionKey/Epoch.
	MaxKeyShards int
	// MaxRequestBytes bounds provider-native request bodies before any copy or
	// JSON parse and rejects larger driver output. Zero uses 64 MiB; valid
	// explicit values are 1..1 GiB.
	MaxRequestBytes int
	// MaxStablePrefixBytes bounds framed stable-prefix bytes before allocation.
	// Zero uses 64 MiB; valid explicit values are 1..1 GiB.
	MaxStablePrefixBytes int
	// ResolveProfile replaces profile lookup when non-nil. Profiles returned for
	// built-in compilers must match their fixed wire mode, TTL, and optimizer.
	ResolveProfile func(NativeRequest) (Profile, bool)
	// Drivers adds standalone wire compilers for custom providers. Driver keys
	// are normalized provider names and must remain unique after normalization.
	// Custom requests supply StableSegments. ResolveProfile and Driver may be
	// called concurrently and must be concurrency-safe.
	Drivers map[string]Driver
}

// Driver compiles one generic plan into custom provider-native request bytes.
// It must return original bytes and no optimizer IDs when safe application is
// impossible.
type Driver interface {
	Apply(context.Context, NativeRequest, Plan) DriverResult
}

// DriverResult contains custom wire output and exact applied optimizer IDs.
type DriverResult struct {
	Body         []byte
	OptimizerIDs []string
}

// DriverFunc adapts a function to Driver.
type DriverFunc func(context.Context, NativeRequest, Plan) DriverResult

// Apply calls wrapped driver function.
func (f DriverFunc) Apply(ctx context.Context, request NativeRequest, plan Plan) DriverResult {
	return f(ctx, request, plan)
}

// NativeRequest is one provider request plus cache-planning context.
type NativeRequest struct {
	Scope                     string
	Epoch                     string
	PartitionKey              string
	ExpectedRequestsPerMinute int
	// ExpectedCalls has same within-TTL meaning as PlanRequest.ExpectedCalls.
	ExpectedCalls int
	Provider      string
	Model         string
	Region        string
	Endpoint      string
	Body          []byte
	RuntimeMode   string
	AuthMode      string
	// PrefixTokens should come from provider counting when available. Zero keeps
	// transformation possible but makes threshold/economics eligibility unknown.
	PrefixTokens int
	// StableSegments bypasses built-in envelope extraction for custom providers.
	StableSegments []Segment
	// Profile bypasses resolution only for providers registered with a custom
	// Driver. Built-in wire compilers reject per-request capability overrides.
	Profile Profile
}

// NativeResult is exact upstream body plus decision and attribution metadata.
type NativeResult struct {
	Body               []byte   `json:"-"`
	Applied            bool     `json:"applied"`
	Decision           Decision `json:"decision"`
	Reason             string   `json:"reason"`
	OptimizerIDs       []string `json:"optimizer_ids,omitempty"`
	Profile            Profile  `json:"profile"`
	Plan               Plan     `json:"plan"`
	ClaimBasis         string   `json:"claim_basis"`
	VerifiedSavingsUSD float64  `json:"verified_savings_usd"`
}

// ObservationStatus classifies provider-confirmed prompt-cache telemetry.
type ObservationStatus string

const (
	ObservationUnavailable ObservationStatus = "unavailable"
	ObservationMiss        ObservationStatus = "miss"
	ObservationWrite       ObservationStatus = "write"
	ObservationHit         ObservationStatus = "hit"
)

// Observation is normalized provider cache evidence; dollars remain zero.
type Observation struct {
	Status                   ObservationStatus `json:"status"`
	Basis                    string            `json:"basis"`
	ProviderConfirmed        bool              `json:"provider_confirmed"`
	AttributedToEngine       bool              `json:"attributed_to_engine"`
	CachedInputTokens        int               `json:"cached_input_tokens"`
	CacheCreationInputTokens int               `json:"cache_creation_input_tokens"`
	VerifiedSavingsUSD       float64           `json:"verified_savings_usd"`
}

// UsageObservation is provider-neutral prompt-cache telemetry. CacheObserved
// distinguishes an observed zero/miss from a response with no cache counters.
type UsageObservation struct {
	CachedInputTokens        int    `json:"cached_input_tokens"`
	CacheCreationInputTokens int    `json:"cache_creation_input_tokens"`
	CacheObserved            bool   `json:"cache_observed"`
	CacheStatus              string `json:"cache_status"`
	Malformed                bool   `json:"malformed"`
}

// Observe normalizes provider cache telemetry. It never mints verified dollars;
// managed gateway accounting remains sole owner of that stronger claim.
func Observe(result NativeResult, usage UsageObservation) Observation {
	observation := Observation{
		Status:                   ObservationUnavailable,
		Basis:                    "unavailable",
		CachedInputTokens:        usage.CachedInputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		VerifiedSavingsUSD:       0,
	}
	if usage.Malformed || !usage.CacheObserved || usage.CachedInputTokens < 0 || usage.CacheCreationInputTokens < 0 {
		return observation
	}
	observation.ProviderConfirmed = true
	observation.Basis = "provider_observed"
	switch {
	case usage.CachedInputTokens > 0 || usage.CacheStatus == "hit":
		observation.Status = ObservationHit
	case usage.CacheCreationInputTokens > 0 || usage.CacheStatus == "write":
		observation.Status = ObservationWrite
	default:
		observation.Status = ObservationMiss
	}
	observation.AttributedToEngine = result.Applied && result.Profile.Attribution == AttributionCausal
	return observation
}
