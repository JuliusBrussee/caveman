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

type Limits struct {
	DeadlineMS   int64 `json:"deadline_ms"`
	RequestBytes int   `json:"request_bytes"`
	SegmentBytes int   `json:"segment_bytes"`
	PageBytes    int   `json:"page_bytes"`
}
type Capabilities struct {
	SchemaVersion    int                      `json:"schema_version"`
	RuntimeBuild     string                   `json:"runtime_build"`
	PolicyRevision   string                   `json:"policy_revision"`
	Transforms       []compressors.Capability `json:"transforms"`
	Limits           Limits                   `json:"limits"`
	Persistent       bool                     `json:"persistent"`
	Recovery         bool                     `json:"recovery"`
	RetentionSeconds int64                    `json:"retention_seconds"`
	TrustMode        string                   `json:"trust_mode"`
	Mode             string                   `json:"mode"`
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
