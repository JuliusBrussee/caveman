package openai

import (
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func reasoningPolicy(flag, gate bool) providers.TransformPolicy {
	return providers.TransformPolicy{
		RuntimeMode: "active",
		Optimizers:  map[string]bool{ReasoningEffortOptimizerID: flag},
		EvalGates:   map[string]bool{ReasoningEffortOptimizerID: gate},
	}
}

func TestReasoningEffort_RequiresBothFlagAndEvalGate(t *testing.T) {
	body := `{"model":"gpt-5.5","messages":[{"role":"user","content":"solve this"}]}`
	endpoint := "/v1/chat/completions"

	// Flag on, eval gate NOT cleared -> must not apply.
	r := applyAtEndpoint(t, body, endpoint, reasoningPolicy(true, false))
	if len(r.OptimizerIDs) != 0 || string(r.Body) != body {
		t.Fatalf("reasoning-effort must not run without a cleared eval gate, got %v", r.OptimizerIDs)
	}

	// Eval gate cleared, flag off -> must not apply.
	r = applyAtEndpoint(t, body, endpoint, reasoningPolicy(false, true))
	if len(r.OptimizerIDs) != 0 || string(r.Body) != body {
		t.Fatalf("reasoning-effort must not run without the policy flag, got %v", r.OptimizerIDs)
	}

	// Both -> applies and sets reasoning_effort to the (low) hint.
	r = applyAtEndpoint(t, body, endpoint, reasoningPolicy(true, true))
	if len(r.OptimizerIDs) != 1 || r.OptimizerIDs[0] != ReasoningEffortOptimizerID {
		t.Fatalf("both flag + eval gate must apply reasoning-effort, got %v", r.OptimizerIDs)
	}
	if got := decode(t, r.Body)["reasoning_effort"]; got != reasoningEffortHint {
		t.Fatalf("reasoning_effort = %v, want %q", got, reasoningEffortHint)
	}
}

func TestReasoningEffort_LowersNotRaises(t *testing.T) {
	// The cost-saving direction must be DOWN: the hint is "low", never "high".
	if reasoningEffortHint != "low" {
		t.Fatalf("reasoning-effort hint must be a reduction (low), got %q", reasoningEffortHint)
	}
}

// TestReasoningEffort_DisabledIsPassthrough checks the adapter layer: with the
// optimizer disabled the body is untouched, including when a direct caller
// supplies record mode.
func TestReasoningEffort_DisabledIsPassthrough(t *testing.T) {
	body := `{"model":"gpt-5.5","messages":[{"role":"user","content":"solve"}]}`
	r := applyAtEndpoint(t, body, "/v1/chat/completions", providers.TransformPolicy{RuntimeMode: "record", Optimizers: map[string]bool{ReasoningEffortOptimizerID: true}, EvalGates: map[string]bool{ReasoningEffortOptimizerID: true}})
	if len(r.OptimizerIDs) != 0 || string(r.Body) != body {
		t.Fatalf("disabled optimizer must pass through unchanged")
	}
}

func TestReasoningEffort_RespectsCallerEffort(t *testing.T) {
	body := `{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`
	r := applyAtEndpoint(t, body, "/v1/chat/completions", reasoningPolicy(true, true))
	if len(r.OptimizerIDs) != 0 || string(r.Body) != body {
		t.Fatalf("reasoning-effort must respect a caller-set value, got %v", r.OptimizerIDs)
	}
}

func TestReasoningEffort_SkipsNonReasoningModel(t *testing.T) {
	// Injecting reasoning_effort on a non-reasoning model (gpt-4o) would be
	// rejected by the API, so the optimizer must leave it untouched.
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	r := applyAtEndpoint(t, body, "/v1/chat/completions", reasoningPolicy(true, true))
	if len(r.OptimizerIDs) != 0 || string(r.Body) != body {
		t.Fatalf("reasoning-effort must not touch a non-reasoning model, got %v", r.OptimizerIDs)
	}
}

func TestReasoningEffort_ByteSafeOnlyFieldAdded(t *testing.T) {
	body := `{"model":"o3","messages":[{"role":"user","content":"hi"}],"temperature":1}`
	r := applyAtEndpoint(t, body, "/v1/chat/completions", reasoningPolicy(true, true))
	if len(r.OptimizerIDs) != 1 {
		t.Fatalf("expected reasoning-effort to apply on a reasoning model, got %v", r.OptimizerIDs)
	}
	out := decode(t, r.Body)
	if out["reasoning_effort"] != reasoningEffortHint {
		t.Fatalf("reasoning_effort not set: %v", out["reasoning_effort"])
	}
	// Nothing model-visible changed: model, messages, temperature intact.
	if out["model"] != "o3" || out["temperature"] != float64(1) {
		t.Fatalf("transform altered model-visible fields: %v", out)
	}
	if _, ok := out["messages"]; !ok {
		t.Fatalf("messages dropped by transform")
	}
}

func TestReasoningEffort_ResponsesUsesNestedEffort(t *testing.T) {
	body := `{"model":"gpt-5.5","input":"solve this","temperature":0.2}`
	r := applyAtEndpoint(t, body, "/v1/responses", reasoningPolicy(true, true))
	if len(r.OptimizerIDs) != 1 || r.OptimizerIDs[0] != ReasoningEffortOptimizerID {
		t.Fatalf("expected Responses reasoning transform, got %v", r.OptimizerIDs)
	}
	out := decode(t, r.Body)
	reasoning, ok := out["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != reasoningEffortHint {
		t.Fatalf("Responses reasoning.effort = %v, want %q", out["reasoning"], reasoningEffortHint)
	}
	if _, topLevel := out["reasoning_effort"]; topLevel {
		t.Fatalf("Responses request must not receive Chat's top-level reasoning_effort: %v", out)
	}
	if out["model"] != "gpt-5.5" || out["input"] != "solve this" || out["temperature"] != float64(0.2) {
		t.Fatalf("Responses transform altered existing fields: %v", out)
	}
}

func TestReasoningEffort_ResponsesRouteAliasUsesNestedEffort(t *testing.T) {
	body := `{"model":"o3","input":"hi"}`
	r := applyAtEndpoint(t, body, "/openai/v1/responses", reasoningPolicy(true, true))
	if len(r.OptimizerIDs) != 1 {
		t.Fatalf("expected aliased Responses route to transform, got %v", r.OptimizerIDs)
	}
	reasoning, ok := decode(t, r.Body)["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != reasoningEffortHint {
		t.Fatalf("aliased Responses request missing nested effort: %v", reasoning)
	}
}

func TestReasoningEffort_ResponsesPreservesExistingReasoningFields(t *testing.T) {
	body := `{"model":"gpt-5.5","input":"hi","reasoning":{"summary":"auto","custom":"keep"}}`
	r := applyAtEndpoint(t, body, "/v1/responses", reasoningPolicy(true, true))
	if len(r.OptimizerIDs) != 1 {
		t.Fatalf("expected Responses reasoning transform, got %v", r.OptimizerIDs)
	}
	reasoning, ok := decode(t, r.Body)["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("reasoning object was not preserved: %v", r.Body)
	}
	if reasoning["effort"] != reasoningEffortHint || reasoning["summary"] != "auto" || reasoning["custom"] != "keep" {
		t.Fatalf("nested reasoning fields changed or were dropped: %v", reasoning)
	}
}

func TestReasoningEffort_PreservesCallerSettingsPerEndpoint(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		body     string
	}{
		{
			name:     "chat top-level effort",
			endpoint: "/v1/chat/completions",
			body:     `{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`,
		},
		{
			name:     "responses nested effort",
			endpoint: "/v1/responses",
			body:     `{"model":"gpt-5.5","input":"hi","reasoning":{"summary":"auto","effort":"high"}}`,
		},
		{
			name:     "responses top-level caller field",
			endpoint: "/v1/responses",
			body:     `{"model":"gpt-5.5","input":"hi","reasoning_effort":"high"}`,
		},
		{
			name:     "chat nested ambiguous field",
			endpoint: "/v1/chat/completions",
			body:     `{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}],"reasoning":{"effort":"high"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := applyAtEndpoint(t, tc.body, tc.endpoint, reasoningPolicy(true, true))
			if len(r.OptimizerIDs) != 0 || string(r.Body) != tc.body {
				t.Fatalf("caller setting must be preserved exactly, ids=%v body=%s", r.OptimizerIDs, r.Body)
			}
		})
	}
}

func TestReasoningEffort_UnknownEndpointOrShapePassesThrough(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		body     string
	}{
		{
			name:     "unknown route",
			endpoint: "/v1/responses-anything",
			body:     `{"model":"gpt-5.5","input":"hi"}`,
		},
		{
			name:     "missing route metadata",
			endpoint: "",
			body:     `{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name:     "chat body on Responses route",
			endpoint: "/v1/responses",
			body:     `{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name:     "responses body on Chat route",
			endpoint: "/v1/chat/completions",
			body:     `{"model":"gpt-5.5","input":"hi"}`,
		},
		{
			name:     "malformed chat messages",
			endpoint: "/v1/chat/completions",
			body:     `{"model":"gpt-5.5","messages":{"role":"user"}}`,
		},
		{
			name:     "empty chat messages",
			endpoint: "/v1/chat/completions",
			body:     `{"model":"gpt-5.5","messages":[]}`,
		},
		{
			name:     "malformed Responses input",
			endpoint: "/v1/responses",
			body:     `{"model":"gpt-5.5","input":{"text":"hi"}}`,
		},
		{
			name:     "invalid JSON",
			endpoint: "/v1/responses",
			body:     `{"model":"gpt-5.5","input":`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := applyAtEndpoint(t, tc.body, tc.endpoint, reasoningPolicy(true, true))
			if len(r.OptimizerIDs) != 0 || string(r.Body) != tc.body {
				t.Fatalf("ambiguous request must pass through exactly, ids=%v body=%s", r.OptimizerIDs, r.Body)
			}
		})
	}
}

func TestReasoningEffort_SkipsUnsupportedProModelsOnBothEndpoints(t *testing.T) {
	models := []string{
		"gpt-5-pro",
		"gpt-5-pro-2025-10-06",
		"gpt-5.2-pro",
		"gpt-5.2-pro-2025-12-11",
		"gpt-5.4-pro",
		"gpt-5.4-pro-2026-03-05",
		"gpt-5.5-pro",
		"gpt-5.5-pro-2026-04-23",
		"o1-pro",
		"o1-pro-2025-03-19",
		"o3-pro",
		"o3-pro-2025-06-10",
	}
	for _, model := range models {
		for _, tc := range []struct {
			name     string
			endpoint string
			body     string
		}{
			{name: "chat", endpoint: "/v1/chat/completions", body: `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`},
			{name: "responses", endpoint: "/v1/responses", body: `{"model":"` + model + `","input":"hi"}`},
		} {
			t.Run(model+"/"+tc.name, func(t *testing.T) {
				r := applyAtEndpoint(t, tc.body, tc.endpoint, reasoningPolicy(true, true))
				if len(r.OptimizerIDs) != 0 || string(r.Body) != tc.body {
					t.Fatalf("unsupported Pro model must pass through exactly, ids=%v body=%s", r.OptimizerIDs, r.Body)
				}
			})
		}
	}
}

func TestSupportsReasoningEffortForEndpointSkipsProAliases(t *testing.T) {
	for _, model := range []string{
		"gpt-5-pro",
		"gpt-5.5-pro-2026-04-23",
		"o3-pro-2025-06-10",
	} {
		for _, endpoint := range []reasoningEndpoint{reasoningEndpointChat, reasoningEndpointResponses} {
			if supportsReasoningEffortForEndpoint(model, endpoint) {
				t.Errorf("supportsReasoningEffortForEndpoint(%q,%d) = true, want false", model, endpoint)
			}
		}
	}
	for _, model := range []string{"gpt-5.5", "o3", "o4-mini"} {
		for _, endpoint := range []reasoningEndpoint{reasoningEndpointChat, reasoningEndpointResponses} {
			if !supportsReasoningEffortForEndpoint(model, endpoint) {
				t.Errorf("supportsReasoningEffortForEndpoint(%q,%d) = false, want true", model, endpoint)
			}
		}
	}
}

func TestSupportsReasoningEffort(t *testing.T) {
	yes := []string{"gpt-5", "gpt-5.5", "gpt-5-pro", "o1", "o1-2025-01-01", "o3", "o3-mini", "o3-2025-04-16", "o4-mini", "o4-mini-2025-04-16"}
	no := []string{"", "gpt-4o", "gpt-4-turbo", "gpt-50", "claude-opus-4", "gemini-2.5-pro", "o10", "o30", "o100"}
	for _, m := range yes {
		if !supportsReasoningEffort(m) {
			t.Errorf("supportsReasoningEffort(%q) = false, want true", m)
		}
	}
	for _, m := range no {
		if supportsReasoningEffort(m) {
			t.Errorf("supportsReasoningEffort(%q) = true, want false", m)
		}
	}
}
