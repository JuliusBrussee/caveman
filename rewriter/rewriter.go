// Package rewriter is the aged-zone reflection rewriter: an external LLM
// condenses one already-aged trajectory step, and a deterministic gate decides
// whether the result may replace it.
//
// The mechanism is AgentDiet's (arXiv 2509.23586), transported into a wire
// proxy rather than an agent loop, parameter for parameter: an external model
// the agent never sees, a lag of a=2 steps with b=1 step of extra context, the
// dual θ=500 gate (skip below θ, apply only if the saving clears θ), and
// rewrite-never-delete — removed text is replaced by a short takeaway, not
// dropped.
//
// # Contracts the caller owns
//
// An accepted rewrite ends with a `<<ccr:…>>` recovery marker, the same syntax
// the proxy's compression path emits (proxy/internal/gateway/proxy.go
// appendCCRMarker) and the retrieve tool consumes. The handle is
// ccr.Handle(StepBytes). THE CALLER MUST PERSIST THE ORIGINAL BYTES UNDER THAT
// HANDLE — in a CCR store the retrieve path can reach — BEFORE putting an
// accepted rewrite on the wire. A marker whose handle resolves to nothing is
// worse than no rewrite at all: the agent is told recovery exists and it does
// not.
//
// A rewrite store must key on SHA-256(StepBytes) || PromptVersion, not on the
// step hash alone; see PromptVersion.
//
// # Properties
//
//   - Fail closed. An API error, a truncated or empty completion, or any gate
//     violation yields Accepted=false and an empty Rewritten. The caller keeps
//     the original block. This package never returns bytes it did not receive
//     from the rewriter model.
//   - Meter honestly. InputTokens/OutputTokens are the rewriter's own
//     provider-reported usage, reported on rejected calls too, so a claimed
//     saving can be netted against what the rewriter itself cost. Everything
//     this package measures with its own token counter is an `inferred`
//     estimate and is never presented as a provider count.
//
// # Named non-guarantees
//
//   - Additive fabrication is not caught. Every gate check is a survival check:
//     it proves that things present in the original are still present in the
//     rewrite. Nothing constrains what the model ADDS, so an invented
//     reassurance ("the remaining failures are unrelated") passes. The defence
//     against that is the harm tripwire on live Δturns, not this gate.
//   - Warning-dense blocks become permanently unflippable. Source locations are
//     protected unconditionally, so a 200-warning compiler log can only be
//     rewritten by keeping all 200 locations, which will not clear θ. That is
//     shipped deliberately. A carve-out is only defensible once the replay grid
//     prices that block class as material.
package rewriter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/JuliusBrussee/caveman/engine/ccr"
)

// DefaultTheta is AgentDiet's tuned threshold (§4.2.5): both the invocation and
// the application gate use it. It is a replay-tunable parameter, not a constant
// of nature — it is what makes the mechanism corpus-adaptive, self-disabling on
// small-block corpora and firing on repo-scale ones.
const DefaultTheta = 500

// Output ceiling bounds, in tokens. A rewrite can never legitimately be larger
// than the step it replaces, so the original's own token count is the honest
// ceiling; the bounds keep a tiny step from getting an unusable budget and a
// huge one from buying an unbounded completion. Hitting the ceiling is a
// rejection (reasonTruncated), never a silently clipped rewrite.
const (
	minOutputTokens = 256
	maxOutputTokens = 16384
)

// Client rewrites aged trajectory steps against one provider and model.
// It is safe for concurrent use.
type Client struct {
	provider string
	model    string
	apiKey   string
	theta    int
	doer     func(*http.Request) (*http.Response, error)
}

// Config configures a Client.
type Config struct {
	Provider string // "anthropic" | "openai"
	Model    string // e.g. "claude-haiku-4-5", "gpt-5-mini"
	APIKey   string
	Theta    int                                         // dual gate threshold in tokens, default 500
	HTTPDoer func(*http.Request) (*http.Response, error) // nil = http.DefaultClient.Do; tests inject fakes
}

// Request is one reflection call.
type Request struct {
	StepBytes   []byte   // serialized target step (the block at lag 2)
	ToolName    string   // producing tool, for the prompt
	WindowBytes [][]byte // steps s-3..s serialized, per the paper's a+b window
}

// Result is one reflection call's outcome.
//
// RejectReason is one of: "below_theta" (the step is at or under θ, no API call
// was made), "api_error", "gate_failure_truncated" (the completion hit the
// output ceiling), "gate_failure_step_wrapper", "gate_failure_fence",
// "gate_failure_empty", "gate_failure_tokens" (the saving did not clear θ),
// "gate_failure_failure_signal", "gate_failure_failure_detail", "gate_failure_count",
// "gate_failure_exit_code", or "gate_failure_reference".
type Result struct {
	Rewritten    []byte // includes trailing recovery-pointer line; empty when !Accepted
	Accepted     bool
	RejectReason string // non-empty when !Accepted; e.g. "below_theta", "gate_failure_tokens", "api_error"
	InputTokens  int    // rewriter's own metered usage, for cost netting
	OutputTokens int
	// CandidateTokens is the inferred size of what the model produced, reported
	// on rejected calls so a corpus study can size the rewrites the gate threw
	// away. The bytes themselves never leave on a rejection.
	CandidateTokens int
}

// New validates cfg and returns a Client. An unknown provider is an error
// rather than a default: a silently mis-routed rewriter would produce bytes no
// gate was designed for.
func New(cfg Config) (*Client, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if provider != providerAnthropic && provider != providerOpenAI {
		return nil, fmt.Errorf("rewriter: unsupported provider %q", cfg.Provider)
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		return nil, errors.New("rewriter: model is required")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("rewriter: api key is required")
	}
	theta := cfg.Theta
	if theta == 0 {
		theta = DefaultTheta
	}
	if theta < 0 {
		return nil, fmt.Errorf("rewriter: theta must not be negative, got %d", theta)
	}
	doer := cfg.HTTPDoer
	if doer == nil {
		doer = http.DefaultClient.Do
	}
	return &Client{provider: provider, model: model, apiKey: cfg.APIKey, theta: theta, doer: doer}, nil
}

// Rewrite condenses req.StepBytes, or explains why it did not.
//
// Gate 1 (invocation) skips the API call entirely when the step is at or under
// θ tokens. Gate 2 (application) plus the fidelity checks in Accept decide
// whether the completion may replace the original. Only an accepted result
// carries bytes; every other path returns an empty Rewritten and a reason.
func (c *Client) Rewrite(ctx context.Context, req Request) (Result, error) {
	originalTokens := countTokens(req.StepBytes)
	if originalTokens <= c.theta {
		return Result{RejectReason: reasonBelowTheta}, nil
	}

	out, err := c.complete(ctx, buildUserMessage(req), outputCeiling(originalTokens))
	if err != nil {
		// Usage is unknown on a failed call, so it stays zero — an estimate here
		// would be a fabricated cost.
		return Result{RejectReason: reasonAPIError}, err
	}
	usage := Result{InputTokens: out.inputTokens, OutputTokens: out.outputTokens}

	// A completion clipped at the ceiling is unusable even if what arrived looks
	// well formed: the elided tail is exactly where a preserved failure would
	// have been. The prompt's "return it unchanged" path makes this reachable.
	if truncatedStopReasons[strings.ToLower(strings.TrimSpace(out.stopReason))] {
		usage.CandidateTokens = countTokens([]byte(out.text))
		usage.RejectReason = reasonTruncated
		return usage, nil
	}

	condensed := strings.TrimSpace(out.text)
	usage.CandidateTokens = countTokens([]byte(condensed))
	if condensed == "" {
		usage.RejectReason = reasonEmpty
		return usage, nil
	}

	// The model was told to emit a bare body. A <step> wrapper means it echoed
	// the I/O envelope instead, which would put the harness's own framing into
	// the agent's context.
	if strings.Contains(condensed, "<step") || strings.Contains(condensed, "</step>") {
		usage.RejectReason = reasonStepWrapper
		return usage, nil
	}
	unfenced, ok := stripWrappingFence(condensed)
	if !ok {
		usage.RejectReason = reasonFence
		return usage, nil
	}
	condensed = unfenced
	if condensed == "" {
		usage.RejectReason = reasonEmpty
		return usage, nil
	}

	// The recovery pointer ships with the block, so the saving is measured on
	// the bytes that actually go on the wire, pointer included.
	candidate := []byte(condensed + recoveryPointer(req.StepBytes))
	usage.CandidateTokens = countTokens(candidate)
	if ok, reason := Accept(req.StepBytes, candidate, c.theta); !ok {
		usage.RejectReason = reason
		return usage, nil
	}

	usage.Rewritten = candidate
	usage.Accepted = true
	return usage, nil
}

// stripWrappingFence removes a markdown code fence the model wrapped the whole
// body in. An unbalanced opening fence is rejected (ok=false) rather than
// repaired: it is the signature of a clipped or malformed completion.
// Fences inside the body are left alone — a condensed step may legitimately
// contain one.
func stripWrappingFence(text string) (string, bool) {
	if !strings.HasPrefix(text, "```") {
		return text, true
	}
	newline := strings.IndexByte(text, '\n')
	if newline < 0 {
		return "", false
	}
	body := text[newline+1:]
	idx := strings.LastIndex(body, "```")
	if idx < 0 {
		return "", false
	}
	if strings.TrimSpace(body[idx+3:]) != "" {
		return "", false
	}
	return strings.TrimSpace(body[:idx]), true
}

// recoveryPointer names the CCR handle for the original bytes using the marker
// syntax the proxy already emits and the retrieve tool already parses, so an
// accepted rewrite's worst case is a visible extra recovery turn rather than a
// silent loss. The caller must have stored the original under this handle.
func recoveryPointer(original []byte) string {
	return "\n[condensed by caveman; original recoverable via <<ccr:" + ccr.Handle(original) + ">>]"
}

func outputCeiling(originalTokens int) int {
	switch {
	case originalTokens < minOutputTokens:
		return minOutputTokens
	case originalTokens > maxOutputTokens:
		return maxOutputTokens
	default:
		return originalTokens
	}
}
