package gateway

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

// The route stage (ADR 0083 §4: parse, ask, compress, route). The ask starts
// before compression so the Cloud round trip overlaps it; the answer is applied
// to the bytes compression produced. Same provider only in this cut: an answer
// names another model of the provider the agent already talks to, swapped into
// the body's top-level "model". Every failure keeps the model the agent asked
// for, and the request is never held past the link's budget.

// RouteAsk is what the route stage knows about one request. Body is read-only
// and never leaves the machine: the link derives a features line and a local
// cache key from it.
type RouteAsk struct {
	Provider   string
	Endpoint   string
	Model      string
	Agent      string
	SessionID  string
	ToolsCount int
	InputBytes int
	Body       []byte
}

// RouteAnswer is the decision for one request. Model is set only when the
// request moves to it. Outcome is the runtime/v1 route outcome: routed, kept,
// degraded, paused or off.
type RouteAnswer struct {
	Model      string
	Outcome    string
	Reason     string
	DecisionID string
}

// CloudLink is the optional signed-in Cloud connection: the route stage asks
// it for a model, and every recorded request is offered to its runtime/v1
// sender. Both fail open and never block a request. Nil means a local-only
// proxy, exactly as before.
type CloudLink interface {
	// Ask starts the decision and returns the wait for it.
	Ask(ctx context.Context, ask RouteAsk) func() RouteAnswer
	Observe(rec RequestRecord)
}

// routable: API-key traffic to Anthropic Messages or OpenAI chat/responses.
// Subscription (OAuth Pro/Max) traffic has no per-request dollar cost, so
// routing does nothing on that main loop (ADR 0083 §7).
func routable(provider, endpoint string, authMode AuthMode) bool {
	if authMode != AuthModePAYG {
		return false
	}
	switch provider {
	case "anthropic":
		return strings.HasSuffix(endpoint, "/messages")
	case "openai":
		return strings.HasSuffix(endpoint, "/chat/completions") || strings.HasSuffix(endpoint, "/responses")
	}
	return false
}

// LatestHumanText is the newest human-written text in a provider request (tool
// results skipped). The route stage hashes it into a local cache key so every
// tool-loop turn of one ask reuses one decision; the text itself goes nowhere.
func LatestHumanText(provider, endpoint string, body []byte) string {
	return extractCompressionQuery(provider, endpoint, body)
}

// setModel replaces the value of the top-level "model" string and leaves every
// other byte as it was. ok is false when there is no such string field.
func setModel(body []byte, model string) ([]byte, bool) {
	root, ok := jsonsplice.Root(body)
	if !ok {
		return body, false
	}
	span, ok := jsonsplice.Field(body, root, "model")
	if _, isString := jsonsplice.String(body, span); !ok || !isString {
		return body, false
	}
	encoded, _ := json.Marshal(model)
	out, err := jsonsplice.ReplaceRaw(body, span, encoded)
	if err != nil {
		return body, false
	}
	return out, true
}
