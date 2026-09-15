package openai

import "strings"

// ReasoningEffortOptimizerID is the policy flag / x-cave-optimization label for
// the reasoning-effort optimizer.
const ReasoningEffortOptimizerID = "reasoning-effort"

// reasoningEffortHint is the provider-native reasoning effort value this
// optimizer applies when the caller has not set one. It is deliberately LOW,
// not high: the whole point is to spend fewer thinking tokens on work that does
// not need them. The endpoint-specific wire spelling is selected below (Chat
// Completions: reasoning_effort; Responses: reasoning.effort).
const reasoningEffortHint = "low"

type reasoningEndpoint uint8

const (
	reasoningEndpointUnknown reasoningEndpoint = iota
	reasoningEndpointChat
	reasoningEndpointResponses
)

// reasoningEndpointKind deliberately recognizes only the routes owned by this
// adapter. Unknown paths, path prefixes, and malformed aliases fail closed so
// a route cannot receive the grammar of a different OpenAI API surface.
func reasoningEndpointKind(endpoint string) reasoningEndpoint {
	switch endpoint {
	case "/v1/chat/completions", "/openai/v1/chat/completions":
		return reasoningEndpointChat
	case "/v1/responses", "/openai/v1/responses":
		return reasoningEndpointResponses
	default:
		return reasoningEndpointUnknown
	}
}

// applyReasoningEffort adds a provider-native reasoning effort hint when the
// caller has not set one AND the request targets a known endpoint/model/body
// shape that supports it. Returns true if it changed root.
//
// Why eval-gated rather than purely byte-safe: the gateway never alters
// model-visible bytes here (reasoning is request envelope, not message
// content), but a lower thinking budget CAN change the model's answer — exactly
// like output-brevity. So it runs only behind both a policy flag and a cleared
// eval gate, and it is the gate that protects the hard-task tail where dialing
// reasoning down silently degrades quality.
//
// Conservative and idempotent: never overrides a caller-set reasoning value,
// and never injects the field on a model that does not support it (which would
// turn an optimization into a 400 from the provider).
func applyReasoningEffort(root map[string]any, endpoint string) bool {
	kind := reasoningEndpointKind(endpoint)
	if kind == reasoningEndpointUnknown || !reasoningRequestShape(root, kind) {
		return false
	}
	// Read the model from the upstream-bound body (the bytes the capability check
	// must agree with), not from telemetry meta — a missing or non-string model
	// yields "" and a safe no-op.
	model, _ := root["model"].(string)
	if !supportsReasoningEffortForEndpoint(model, kind) {
		return false
	}

	switch kind {
	case reasoningEndpointChat:
		// A caller-provided value, including null or a malformed type, remains
		// authoritative. A nested Responses-style field is ambiguous on Chat
		// and must not be rewritten into a second grammar.
		if _, set := root["reasoning_effort"]; set {
			return false
		}
		if _, set := root["reasoning"]; set {
			return false
		}
		root["reasoning_effort"] = reasoningEffortHint
		return true

	case reasoningEndpointResponses:
		// Responses has no top-level reasoning_effort field. Preserve one if a
		// caller supplied it rather than trying to repair an ambiguous request.
		if _, set := root["reasoning_effort"]; set {
			return false
		}
		if raw, set := root["reasoning"]; set {
			reasoning, ok := raw.(map[string]any)
			if !ok {
				return false
			}
			if _, set := reasoning["effort"]; set {
				return false
			}
			reasoning["effort"] = reasoningEffortHint
			return true
		}
		root["reasoning"] = map[string]any{"effort": reasoningEffortHint}
		return true
	}

	return false
}

// reasoningRequestShape proves enough of the request grammar to avoid adding
// an endpoint-specific field to an unrelated or malformed JSON object. It is
// intentionally conservative: unknown fields remain untouched, but the
// endpoint's distinguishing input field must have the documented JSON type.
func reasoningRequestShape(root map[string]any, endpoint reasoningEndpoint) bool {
	model, ok := root["model"].(string)
	if !ok || strings.TrimSpace(model) == "" {
		return false
	}

	switch endpoint {
	case reasoningEndpointChat:
		messages, ok := root["messages"].([]any)
		return ok && len(messages) > 0

	case reasoningEndpointResponses:
		// A Chat body on the Responses route is not a safe basis for a nested
		// transform. Responses requests identify their input via input,
		// instructions, or a previous response id.
		if _, hasMessages := root["messages"]; hasMessages {
			return false
		}
		if input, exists := root["input"]; exists {
			switch input.(type) {
			case string, []any:
				return true
			default:
				return false
			}
		}
		if instructions, exists := root["instructions"]; exists {
			_, ok := instructions.(string)
			return ok
		}
		if previous, exists := root["previous_response_id"]; exists {
			_, ok := previous.(string)
			return ok
		}
	}

	return false
}

// supportsReasoningEffort reports whether an OpenAI model belongs to a current
// reasoning family. Only reasoning models accept this parameter; setting it on
// a non-reasoning model (e.g. gpt-4o) is rejected by the API, so we gate on
// capability to stay byte-safe in spirit — an optimizer must never break a
// request it cannot improve.
//
// A family prefix matches when the model is exactly the prefix or continues
// with a version separator ('-' or '.'), so "gpt-5" matches "gpt-5",
// "gpt-5-pro", and "gpt-5.5" but not "gpt-50". (This intentionally treats
// '.' as a separator, unlike detectors.isPremiumModel, because gpt-5.5 IS a
// reasoning model.)
func supportsReasoningEffort(model string) bool {
	if model == "" {
		return false
	}
	// gpt-5 family (gpt-5, gpt-5.x, gpt-5-*) are reasoning models.
	if hasModelPrefix(model, "gpt-5") {
		return true
	}
	// o-series reasoning models: o1, o3, o4 (and dated/suffixed variants).
	for _, fam := range []string{"o1", "o3", "o4"} {
		if hasModelPrefix(model, fam) {
			return true
		}
	}
	return false
}

// supportsReasoningEffortForEndpoint keeps the existing reasoning-family
// capability check while excluding Pro variants whose current model contracts
// do not document the low effort this optimizer uses. This applies to both
// endpoint grammars: mapping upward to medium/high would no longer be the
// savings optimization, so unsupported Pro aliases and dated snapshots pass
// through instead of risking a provider 4xx.
func supportsReasoningEffortForEndpoint(model string, _ reasoningEndpoint) bool {
	if !supportsReasoningEffort(model) || hasUnsupportedProReasoningEffort(model) {
		return false
	}
	return true
}

// hasUnsupportedProReasoningEffort recognizes a Pro segment anywhere after a
// supported reasoning-family prefix. OpenAI documents Pro slugs and dated
// snapshots with this segment (gpt-5-pro, gpt-5.4-pro-..., o3-pro-...); a
// segment-aware check also fail-closes a newly introduced alias without
// inventing a model-specific allowlist. GPT-5.6's supported `reasoning.mode:
// pro` is not a `-pro` model slug and therefore remains eligible when its
// documented low effort is supported.
func hasUnsupportedProReasoningEffort(model string) bool {
	for _, family := range []string{"gpt-5", "o1", "o3", "o4"} {
		if !hasModelPrefix(model, family) {
			continue
		}
		suffix := model[len(family):]
		for _, segment := range strings.FieldsFunc(suffix, func(r rune) bool { return r == '-' || r == '.' }) {
			if segment == "pro" {
				return true
			}
		}
	}
	return false
}

// hasModelPrefix reports whether model equals prefix or begins with prefix
// followed by a version/variant separator ('-' or '.'). This keeps "gpt-5"
// from matching "gpt-50" while still matching "gpt-5.5" and "gpt-5-pro".
func hasModelPrefix(model, prefix string) bool {
	if model == prefix {
		return true
	}
	if !strings.HasPrefix(model, prefix) {
		return false
	}
	next := model[len(prefix)]
	return next == '-' || next == '.'
}
