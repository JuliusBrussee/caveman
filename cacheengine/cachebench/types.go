// Package cachebench evaluates cacheengine against deterministic real-agent-
// shaped traces and provider-observed usage replays. Simulated and observed
// evidence never blend.
package cachebench

import (
	"encoding/json"
	"time"

	"github.com/JuliusBrussee/caveman/cacheengine"
)

const (
	Schema               = "caveman.cachebench.v1"
	TraceSchemaV1        = "caveman.cachebench.trace.v1"
	TraceSchemaV2        = "caveman.cachebench.trace.v2"
	TraceSchema          = "caveman.cachebench.trace.v3"
	ObservationSchemaV2  = "caveman.cachebench.observation.v2"
	ObservationSchema    = "caveman.cachebench.observation.v3"
	BasisSimulated       = "benchmark_simulated"
	BasisObserved        = "provider_observed"
	QualityEquivalence   = "model_visible_request_equivalence"
	QualityTask          = "external_task_verifier"
	TimingGrounded       = "grounded_global_timestamps"
	TimingPerPartition   = "per_partition_timestamps_only"
	TimingSynthetic      = "synthetic_schedule"
	TokenProviderCounted = "provider_counted_input_tokens"
)

// Target defines strict request/token hit gates and minimum sample size.
type Target struct {
	RequestHitRate     float64 `json:"request_hit_rate"`
	TokenHitRate       float64 `json:"token_hit_rate"`
	MinEligibleRequest int     `json:"min_eligible_requests"`
}

// DefaultTarget requires 97% request and token hits over at least 100 requests.
func DefaultTarget() Target {
	return Target{RequestHitRate: 0.97, TokenHitRate: 0.97, MinEligibleRequest: 100}
}

// Scenario defines deterministic tool-using agent workload shape.
type Scenario struct {
	Name             string        `json:"name"`
	Turns            int           `json:"turns"`
	CompactionEvery  int           `json:"compaction_every"`
	StaticTokens     int           `json:"static_tokens"`
	UserTokens       int           `json:"user_tokens"`
	AssistantTokens  int           `json:"assistant_tokens"`
	ToolResultTokens int           `json:"tool_result_tokens"`
	SummaryTokens    int           `json:"summary_tokens"`
	Step             time.Duration `json:"-"`
	AssumedTTL       time.Duration `json:"-"`
}

// DefaultScenario returns 128-turn agent workload used by golden benchmark.
func DefaultScenario() Scenario {
	return Scenario{
		Name: "tool-using-agent-128-turn", Turns: 128, CompactionEvery: 64,
		StaticTokens: 8_192, UserTokens: 64, AssistantTokens: 96,
		ToolResultTokens: 256, SummaryTokens: 512,
		Step: 3 * time.Second, AssumedTTL: 5 * time.Minute,
	}
}

// ProviderConfig binds one benchmark provider, model, region, and endpoint.
type ProviderConfig struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Region   string `json:"region,omitempty"`
	Endpoint string `json:"endpoint"`
}

// DefaultProviders returns built-in Anthropic, OpenAI, Bedrock, and Gemini lanes.
func DefaultProviders() []ProviderConfig {
	return []ProviderConfig{
		{Provider: "anthropic", Model: "claude-sonnet-4-6", Endpoint: "/v1/messages"},
		{Provider: "openai", Model: "gpt-5.6", Endpoint: "/v1/chat/completions"},
		{Provider: "bedrock", Model: "global.anthropic.claude-sonnet-4-6", Region: "us-east-1", Endpoint: "converse"},
		{Provider: "gemini", Model: "gemini-2.5-pro", Endpoint: "generateContent"},
	}
}

// PrefixSegment is one identity/token component in simulated provider prefix.
type PrefixSegment struct {
	ID     string `json:"id"`
	Tokens int    `json:"tokens"`
}

// TraceRequest combines provider-native input with simulated prefix metadata.
type TraceRequest struct {
	ID                  string
	At                  time.Time
	Native              cacheengine.NativeRequest
	Prefix              []PrefixSegment
	StableSegmentCount  int
	DeclaredInputTokens int
	MaxOutputTokens     int
}

// Trace is one ordered provider workload plus evidence bases.
type Trace struct {
	Provider                  ProviderConfig
	Scenario                  Scenario
	Requests                  []TraceRequest
	TokenBasis                string
	TimingBasis               string
	AssumeCrossPartitionReuse bool
}

// RequestResult records one cache simulation or observation outcome.
type RequestResult struct {
	RequestID        string                  `json:"request_id"`
	Epoch            string                  `json:"epoch"`
	Decision         cacheengine.Decision    `json:"decision"`
	Reason           string                  `json:"reason"`
	Attribution      cacheengine.Attribution `json:"attribution"`
	Eligible         bool                    `json:"eligible"`
	EligibleTokens   int                     `json:"eligible_tokens"`
	CacheReadTokens  int                     `json:"cache_read_tokens"`
	CacheWriteTokens int                     `json:"cache_write_tokens"`
	Hit              bool                    `json:"hit"`
	ColdWrite        bool                    `json:"cold_write"`
	Invalidated      bool                    `json:"invalidated"`
	Equivalent       bool                    `json:"model_visible_equivalent"`
	Error            string                  `json:"error,omitempty"`
}

// ProviderReport contains strict metrics and gate outcome for one provider.
type ProviderReport struct {
	Provider                      string                  `json:"provider"`
	Model                         string                  `json:"model"`
	Mode                          cacheengine.Mode        `json:"mode"`
	Attribution                   cacheengine.Attribution `json:"attribution"`
	Rolling                       bool                    `json:"rolling"`
	EvaluatedRequests             int                     `json:"evaluated_requests"`
	EligibleRequests              int                     `json:"eligible_requests"`
	IneligibleRequests            int                     `json:"ineligible_requests"`
	RequestHits                   int                     `json:"request_hits"`
	ColdWrites                    int                     `json:"cold_writes"`
	Invalidations                 int                     `json:"invalidations"`
	EligibleTokens                int64                   `json:"eligible_tokens"`
	CacheReadTokens               int64                   `json:"cache_read_tokens"`
	CacheWriteTokens              int64                   `json:"cache_write_tokens"`
	AttributedReadTokens          int64                   `json:"attributed_read_tokens"`
	ReusableOpportunityRequests   int                     `json:"reusable_opportunity_requests"`
	ReusableOpportunityTokens     int64                   `json:"reusable_opportunity_tokens"`
	RequestHitRate                float64                 `json:"request_hit_rate"`
	TokenHitRate                  float64                 `json:"token_hit_rate"`
	AttributedTokenHitRate        float64                 `json:"attributed_token_hit_rate"`
	OpportunityRequestCaptureRate float64                 `json:"opportunity_request_capture_rate"`
	OpportunityTokenCaptureRate   float64                 `json:"opportunity_token_capture_rate"`
	QualityPassRate               float64                 `json:"quality_pass_rate"`
	SafetyFailures                int                     `json:"safety_failures"`
	InvalidSamples                int                     `json:"invalid_samples"`
	GatePassed                    bool                    `json:"gate_passed"`
	BlockingReasons               []string                `json:"blocking_reasons,omitempty"`
	Requests                      []RequestResult         `json:"requests,omitempty"`
}

// Report is complete cachebench result with explicit evidence limitations.
type Report struct {
	Schema              string           `json:"schema"`
	Basis               string           `json:"basis"`
	Status              string           `json:"status"`
	Publishable         bool             `json:"publishable"`
	GeneratedAt         string           `json:"generated_at"`
	Target              Target           `json:"target"`
	Scenario            ScenarioSummary  `json:"scenario"`
	QualityBasis        string           `json:"quality_basis"`
	Providers           []ProviderReport `json:"providers"`
	Overall             ProviderReport   `json:"overall"`
	Corpus              *CorpusSummary   `json:"corpus,omitempty"`
	EvidenceLimitations []string         `json:"evidence_limitations"`
}

// ScenarioSummary is JSON-safe scenario metadata embedded in reports.
type ScenarioSummary struct {
	Name             string `json:"name"`
	Turns            int    `json:"turns"`
	CompactionEvery  int    `json:"compaction_every"`
	StaticTokens     int    `json:"static_tokens"`
	UserTokens       int    `json:"user_tokens"`
	AssistantTokens  int    `json:"assistant_tokens"`
	ToolResultTokens int    `json:"tool_result_tokens"`
	SummaryTokens    int    `json:"summary_tokens"`
	Step             string `json:"step"`
	AssumedTTL       string `json:"assumed_ttl"`
	TokenBasis       string `json:"token_basis"`
}

// ObservationRecord binds provider usage and quality evidence to one request.
type ObservationRecord struct {
	Schema                 string                  `json:"schema"`
	RequestID              string                  `json:"request_id"`
	RequestBodySHA256      string                  `json:"request_body_sha256"`
	ProviderEvidenceSHA256 string                  `json:"provider_evidence_sha256"`
	Provider               string                  `json:"provider"`
	Epoch                  string                  `json:"epoch"`
	EligibleInputTokens    int                     `json:"eligible_input_tokens"`
	CacheEligible          bool                    `json:"cache_eligible"`
	Applied                bool                    `json:"applied"`
	EngineDecision         cacheengine.Decision    `json:"engine_decision"`
	EngineReason           string                  `json:"engine_reason"`
	ProfileID              string                  `json:"profile_id"`
	Attribution            cacheengine.Attribution `json:"attribution"`
	OptimizerIDs           []string                `json:"optimizer_ids"`
	QualityPassed          bool                    `json:"quality_passed"`
	QualityVerifier        string                  `json:"quality_verifier"`
	QualityEvidenceSHA256  string                  `json:"quality_evidence_sha256"`
	Usage                  json.RawMessage         `json:"usage"`
}

// TaskVerification is external task-quality verdict plus retained evidence.
type TaskVerification struct {
	Passed   bool
	Verifier string
	Evidence []byte
}

// TraceRecord is strict JSONL wire form used for observation joins and replay.
type TraceRecord struct {
	Schema              string          `json:"schema"`
	RequestID           string          `json:"request_id"`
	At                  string          `json:"at"`
	Provider            string          `json:"provider"`
	Model               string          `json:"model"`
	Region              string          `json:"region,omitempty"`
	Endpoint            string          `json:"endpoint"`
	Epoch               string          `json:"epoch"`
	Scope               string          `json:"scope"`
	TokenBasis          string          `json:"token_basis"`
	TimingBasis         string          `json:"timing_basis"`
	PartitionKey        string          `json:"partition_key"`
	ExpectedRPM         int             `json:"expected_requests_per_minute"`
	ExpectedCalls       int             `json:"expected_calls"`
	RuntimeMode         string          `json:"runtime_mode"`
	AuthMode            string          `json:"auth_mode"`
	PrefixTokens        int             `json:"prefix_tokens"`
	DeclaredInputTokens int             `json:"declared_total_input_tokens,omitempty"`
	MaxOutputTokens     int             `json:"max_output_tokens,omitempty"`
	Prefix              []PrefixSegment `json:"prefix"`
	StableSegmentCount  int             `json:"stable_segment_count"`
	Body                json.RawMessage `json:"body"`
	BodySHA256          string          `json:"body_sha256"`
}
