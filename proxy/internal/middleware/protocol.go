// Package middleware exposes compression without an inference hop. Native host
// clients retain their requests, tool loops, retry authority and response streams.
package middleware

import "github.com/JuliusBrussee/caveman/engine/compressors"

const (
	ProtocolVersion     = 1
	RoutePrefix         = "/caveman/v1/middleware/"
	RecoveryToolName    = "caveman_retrieve"
	DefaultRequestBytes = 2 << 20
	DefaultSegmentBytes = 512 << 10
	DefaultPageBytes    = 256 << 10
)

// Protocol 1.1 negotiation and defaults. docs/technical/middleware-protocol.md
// is normative; packages/sdk/parity/middleware-v1_1.fixtures.json pins values.
const (
	ProtocolMin = 1
	ProtocolMax = 1

	// HeaderFeatures carries the client's comma-separated features. A request
	// without it is a protocol 1.0 client and gets 1.0 behavior.
	HeaderFeatures   = "Caveman-Middleware-Features"
	HeaderClient     = "Caveman-Middleware-Client"
	HeaderRetryAfter = "Retry-After"

	FeatureTolerantReader     = "tolerant_reader"     // server ignores unknown request fields
	FeatureRevisionTolerant   = "revision_tolerant"   // accept any revision whose transforms are all supported
	FeatureHTTPStatusV2       = "http_status_v2"      // decisions are 200; 408/429/503+Retry-After mapping
	FeatureOriginalsLifecycle = "originals_lifecycle" // delete/retention cover originals; max_retention_seconds

	DefaultDeadlineMS         = 500
	DefaultRetrieveDeadlineMS = 5000
	DefaultQueueDepth         = 16
	DefaultRetrieveQueueDepth = 16
	DefaultMaxSegments        = 256
	DefaultMaxManifestItems   = 4096
	DefaultReceiptBytes       = 16 << 10
)

// Error codes (error.code). Status, 1.0 status, retryable and Retry-After per
// code: server_error_codes in the parity fixture; per-condition 1.0 mappings
// (slow body, queue wait, bypass decisions, ...): legacy_conditions.
const (
	CodeInvalidRequest      = "invalid_request"
	CodeUnsupportedVersion  = "unsupported_version"
	CodeUnknownCapability   = "unknown_capability"
	CodeInvalidRange        = "invalid_range"
	CodeUnauthorized        = "unauthorized"
	CodeForbiddenOrigin     = "forbidden_origin"
	CodeForbiddenNamespace  = "forbidden_namespace"
	CodeNotFound            = "not_found"
	CodeRequestTimeout      = "request_timeout"
	CodeEpochChanged        = "epoch_changed"
	CodeIdentityConflict    = "identity_conflict"
	CodeDeleted             = "deleted"
	CodeExpired             = "expired"
	CodePayloadLimit        = "payload_limit"
	CodeCapacity            = "capacity"
	CodeQuotaExceeded       = "quota_exceeded"
	CodeRuntimeUnavailable  = "runtime_unavailable"
	CodeRecoveryUnavailable = "recovery_unavailable"
	CodeDeadline            = "deadline"
)

// Plan reasons (plan.reason, skipped[].reason). Under http_status_v2 the
// not_smaller, cache_state_unavailable, recovery_unavailable and capacity
// outcomes of optimize are 200 bypass plans instead of 503 errors.
const (
	ReasonEligible              = "eligible"
	ReasonRecord                = "record"
	ReasonNoCandidate           = "no_candidate"
	ReasonProtected             = "protected"
	ReasonUnsupportedShape      = "unsupported_shape"
	ReasonNotSmaller            = "not_smaller"
	ReasonCacheStateUnavailable = "cache_state_unavailable"
)

type ProtocolRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

type ErrorEnvelope struct {
	SchemaVersion int     `json:"schema_version"`
	Error         Failure `json:"error"`
}

type SessionDeleteRequest struct {
	SchemaVersion int   `json:"schema_version"`
	Scope         Scope `json:"scope"`
}

type DeleteCounts struct {
	Scopes    int64 `json:"scopes"`
	Choices   int64 `json:"choices"`
	Grants    int64 `json:"grants"`
	Originals int64 `json:"originals"`
}

type SessionDeleteResponse struct {
	SchemaVersion    int           `json:"schema_version"`
	Status           string        `json:"status"` // "revoked"
	OriginalsDeleted bool          `json:"originals_deleted"`
	Deleted          *DeleteCounts `json:"deleted,omitempty"`
}

type ReceiptResponse struct {
	SchemaVersion    int    `json:"schema_version"`
	Status           string `json:"status"` // "recorded"
	Basis            string `json:"basis"`  // "client_observed"
	VerifiedSavedUSD int    `json:"verified_saved_usd"`
}

type Scope struct {
	Namespace  string `json:"namespace"`
	SessionID  string `json:"session_id"`
	BranchID   string `json:"branch_id"`
	CacheEpoch string `json:"cache_epoch"`
}

type Adapter struct {
	ID                    string `json:"id"`
	Version               string `json:"version"`
	FrameworkVersion      string `json:"framework_version"`
	SerializationRevision string `json:"serialization_revision"`
}

type Policy struct {
	Revision   string   `json:"revision"`
	Transforms []string `json:"transforms"`
}

type Segment struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	CacheRegion string `json:"cache_region"`
	Content     string `json:"content"`
	SHA256      string `json:"sha256"`
	SourceID    string `json:"source_id"`
	Protected   bool   `json:"protected"`
	Opaque      bool   `json:"opaque"`
}

type ManifestItem struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}

// Binding is an attestation by an authenticated host integration. SDKs only
// construct it after binding an actual executor; schemas alone do not qualify.
// The service cannot prove arbitrary host code will execute a requested tool.
type Binding struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	ToolName     string `json:"tool_name"`
	OverheadText string `json:"overhead_text"`
}

type OptimizeRequest struct {
	SchemaVersion   int            `json:"schema_version"`
	RequestID       string         `json:"request_id"`
	LogicalCallID   string         `json:"logical_call_id"`
	AttemptID       string         `json:"attempt_id"`
	IdempotencyKey  string         `json:"idempotency_key"`
	Scope           Scope          `json:"scope"`
	Sequence        int64          `json:"sequence"`
	Adapter         Adapter        `json:"adapter"`
	Model           *Model         `json:"model"`
	Mode            string         `json:"mode"`
	Policy          Policy         `json:"policy"`
	Segments        []Segment      `json:"segments"`
	ContextManifest []ManifestItem `json:"context_manifest"`
	RecoveryBinding *Binding       `json:"recovery_binding"`
}

type Model struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Protocol string `json:"protocol"`
}

type Replacement struct {
	SegmentID        string `json:"segment_id"`
	SourceID         string `json:"source_id"`
	OriginalSHA256   string `json:"original_sha256"`
	Text             string `json:"text"`
	SHA256           string `json:"sha256"`
	TransformID      string `json:"transform_id"`
	TransformVersion string `json:"transform_version"`
	RecoveryHandle   string `json:"recovery_handle"`
	TokensBefore     int    `json:"tokens_before"`
	TokensAfter      int    `json:"tokens_after"`
	Reused           bool   `json:"reused"`
	UniqueOriginal   bool   `json:"unique_original"`
}

type Skip struct {
	SegmentID string `json:"segment_id"`
	Reason    string `json:"reason"`
}
type Measurement struct {
	Basis                  string `json:"basis"`
	Tokenizer              string `json:"tokenizer"`
	Scope                  string `json:"scope"`
	TokensBefore           int    `json:"tokens_before"`
	TokensAfter            int    `json:"tokens_after"`
	UniqueTokensReduced    int    `json:"unique_tokens_reduced"`
	RecoveryOverheadTokens int    `json:"recovery_overhead_tokens"`
	OverheadCoverage       string `json:"overhead_coverage"`
	VerifiedSavedUSD       int    `json:"verified_saved_usd"`
}
type Stability struct {
	Native            string `json:"native"`
	ProviderBytes     string `json:"provider_bytes"`
	ProviderCacheHits string `json:"provider_cache_hits"`
}
type RecoveryState struct {
	BindingID  string `json:"binding_id"`
	Available  bool   `json:"available"`
	Persistent bool   `json:"persistent"`
	ExpiresAt  int64  `json:"expires_at"`
}
type OptimizeResponse struct {
	SchemaVersion    int           `json:"schema_version"`
	RequestID        string        `json:"request_id"`
	InputDigest      string        `json:"input_digest"`
	RuntimeBuild     string        `json:"runtime_build"`
	PolicyRevision   string        `json:"policy_revision"`
	ReplacementSetID string        `json:"replacement_set_id"`
	Status           string        `json:"status"`
	Reason           string        `json:"reason"`
	Replacements     []Replacement `json:"replacements"`
	Skipped          []Skip        `json:"skipped"`
	Measurement      Measurement   `json:"measurement"`
	Stability        Stability     `json:"stability"`
	Recovery         RecoveryState `json:"recovery"`
}

// Limits values are positive safe integers or omitted: SDK 1.1.0 rejects the
// whole capabilities document if any limits value is anything else.
type Limits struct {
	DeadlineMS             int64 `json:"deadline_ms"`
	RequestBytes           int   `json:"request_bytes"`
	SegmentBytes           int   `json:"segment_bytes"`
	PageBytes              int   `json:"page_bytes"`
	RetrieveDeadlineMS     int64 `json:"retrieve_deadline_ms,omitempty"`
	QueueDepth             int   `json:"queue_depth,omitempty"`
	RetrieveQueueDepth     int   `json:"retrieve_queue_depth,omitempty"`
	MaxSegments            int   `json:"max_segments,omitempty"`
	MaxManifestItems       int   `json:"max_manifest_items,omitempty"`
	ReceiptBytes           int   `json:"receipt_bytes,omitempty"`
	QuotaRequestsPerMinute int   `json:"quota_requests_per_minute,omitempty"` // omitted = unlimited
}
type Capabilities struct {
	SchemaVersion       int                      `json:"schema_version"`
	RuntimeBuild        string                   `json:"runtime_build"`
	PolicyRevision      string                   `json:"policy_revision"`
	Transforms          []compressors.Capability `json:"transforms"`
	Limits              Limits                   `json:"limits"`
	Persistent          bool                     `json:"persistent"`
	Recovery            bool                     `json:"recovery"`
	RetentionSeconds    int64                    `json:"retention_seconds"`
	TrustMode           string                   `json:"trust_mode"`
	Mode                string                   `json:"mode"`
	Protocol            *ProtocolRange           `json:"protocol,omitempty"`
	Features            []string                 `json:"features,omitempty"`
	MaxRetentionSeconds int64                    `json:"max_retention_seconds,omitempty"`
}

type RetrieveRequest struct {
	SchemaVersion int    `json:"schema_version"`
	Scope         Scope  `json:"scope"`
	Handle        string `json:"handle"`
	Offset        int    `json:"offset"`
	Limit         int    `json:"limit"`
	Query         string `json:"query"`
}
type RetrieveResponse struct {
	SchemaVersion  int    `json:"schema_version"`
	Handle         string `json:"handle"`
	SourceID       string `json:"source_id"`
	Text           string `json:"text"`
	OriginalSHA256 string `json:"original_sha256"`
	TotalBytes     int    `json:"total_bytes"`
	Complete       bool   `json:"complete"`
	Kind           string `json:"kind"`
	Offset         int    `json:"offset"`
	NextOffset     *int   `json:"next_offset"`
}

type Receipt struct {
	SchemaVersion         int     `json:"schema_version"`
	Scope                 Scope   `json:"scope"`
	LogicalCallID         string  `json:"logical_call_id"`
	AttemptID             string  `json:"attempt_id"`
	EventKind             string  `json:"event_kind"`
	PlanID                *string `json:"plan_id"`
	Usage                 *Usage  `json:"usage"`
	ProviderRequestSHA256 *string `json:"provider_request_sha256"`
}
type Usage struct {
	Provenance       string `json:"provenance"`
	Complete         bool   `json:"complete"`
	InputTokens      *int64 `json:"input_tokens"`
	OutputTokens     *int64 `json:"output_tokens"`
	CacheReadTokens  *int64 `json:"cache_read_tokens"`
	CacheWriteTokens *int64 `json:"cache_write_tokens"`
	ReasoningTokens  *int64 `json:"reasoning_tokens"`
}

type Failure struct {
	Code string `json:"code"`
}

func (e Failure) Error() string { return "middleware: " + e.Code }
