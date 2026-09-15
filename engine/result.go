package engine

// BasisInferred identifies token figures produced by the engine's local counter.
const BasisInferred = "inferred"

// Mode controls whether the engine transforms bytes.
type Mode string

const (
	// ModeRecord is always pass-through: the output is byte-identical to the
	// input and no recovery is stored. It is the default.
	ModeRecord Mode = "record"
	// ModeCompress runs the routed compressor.
	ModeCompress Mode = "compress"
)

// Options configures a single Compress call.
type Options struct {
	// Mode defaults to ModeRecord (pass-through) when empty. Any unknown mode
	// also falls back to ModeRecord — unknown enum cases fail closed.
	Mode Mode
	// Type forces a content type; empty means auto-detect.
	Type string
	// Query, when non-empty, lets a query-aware compressor bias what it keeps
	// toward items relevant to it (deterministic BM25; no embeddings). Empty
	// means query-agnostic — identical to the historical behavior. The field is
	// additive and optional: a compressor that does not implement
	// compressors.QueryAwareCompressor ignores it entirely.
	Query string
	// ExternalRecovery lets an embedding gateway provide byte-exact recovery
	// outside the engine's local CCR store. It may only be used when the caller
	// stores the original before forwarding compressed bytes.
	ExternalRecovery bool
}

func (m Mode) normalized() Mode {
	switch m {
	case ModeCompress:
		return ModeCompress
	default:
		// Empty or unknown → record (pass-through). Fail closed.
		return ModeRecord
	}
}

// Result is the outcome of a Compress call.
type Result struct {
	// Output is the compressed bytes, or the original bytes on pass-through.
	Output []byte `json:"-"`
	// ContentType is the detected (or forced) content type.
	ContentType string `json:"content_type"`
	// TokensBefore / TokensAfter are local token estimates.
	TokensBefore int `json:"tokens_before"`
	TokensAfter  int `json:"tokens_after"`
	// TokenCountBasis names estimator used for both token counts. Provider usage
	// is not available before compression.
	TokenCountBasis string `json:"token_count_basis"`
	// Ratio is the fraction of tokens removed (0..1); 0 on pass-through.
	Ratio float64 `json:"ratio"`
	// Basis identifies the token-count basis.
	Basis string `json:"basis"`
	// RecoveryHandle is the CCR handle for the original; empty on pass-through.
	RecoveryHandle string `json:"recovery_handle,omitempty"`
	// Method identifies the concrete transform chosen by a compressor.
	Method string `json:"method,omitempty"`
	// LosslessToModel is present on transformed outputs and tells callers
	// whether the model-visible output kept the full value without data dropped.
	LosslessToModel *bool `json:"lossless_to_model,omitempty"`
}

// PassedThrough reports whether the call returned the input unchanged.
func (r Result) PassedThrough() bool { return r.RecoveryHandle == "" && r.Method == "" && r.Ratio == 0 }

// SimResult is the dry-run accounting from Simulate: what Compress WOULD do to a
// payload, computed without storing anything (no CCR Put) or making any network
// call. Its token figures are local estimates, not provider usage or billing.
type SimResult struct {
	// ContentType is the detected (or forced) content type.
	ContentType string `json:"content_type"`
	// Compressor is the compressor that would run; empty on pass-through.
	Compressor string `json:"compressor,omitempty"`
	// SafetyClass is the S0..S4 class of the matched compressor; empty on pass-through.
	SafetyClass string `json:"safety_class,omitempty"`
	// TokensBefore / TokensAfter / TokensSaved are local token estimates.
	TokensBefore int `json:"tokens_before"`
	TokensAfter  int `json:"tokens_after"`
	TokensSaved  int `json:"tokens_saved"`
	// TokenCountBasis names the estimator used for before/after/saved.
	TokenCountBasis string `json:"token_count_basis"`
	// Ratio is the fraction of tokens removed (0..1); 0 on pass-through.
	Ratio float64 `json:"ratio"`
	// Lossy is true when the model-visible output dropped data.
	Lossy bool `json:"lossy"`
	// RequiresCCR is true when byte-exact recovery requires a stored original.
	RequiresCCR bool `json:"requires_ccr"`
	// Recoverable reports whether a real Compress could actually emit this reduction
	// in the current engine configuration: always true for byte-safe compressors,
	// but false for a lossy compressor when no store backs recovery. A false here
	// marks the reduction as requiring CCR before it can be emitted; callers must
	// not treat it as available when recovery is not configured.
	Recoverable bool `json:"recoverable"`
	// Method identifies the concrete transform chosen by a compressor.
	Method string `json:"method,omitempty"`
	// LosslessToModel is present on simulated transformed outputs and tells
	// callers whether the model-visible output keeps the full value.
	LosslessToModel *bool `json:"lossless_to_model,omitempty"`
	// Basis identifies the token-count basis.
	Basis string `json:"basis"`
}
