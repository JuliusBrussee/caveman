package openai

import (
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// C8 review finding 5: this optimizer adds a client-visible SSE chunk to the
// response (an observable effect outside what byte-safe promises), so like
// reasoning-effort and output-brevity it needs OptimizerActive — flag AND a
// cleared eval gate — not just OptimizerEnabled.
func streamUsageEnabled() providers.TransformPolicy {
	return providers.TransformPolicy{
		RuntimeMode: "active",
		Optimizers:  map[string]bool{StreamUsageOptimizerID: true},
		EvalGates:   map[string]bool{StreamUsageOptimizerID: true},
	}
}

func TestStreamUsage_InjectsWhenAbsent(t *testing.T) {
	body := `{"model":"gpt-5.5","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	res := apply(t, body, streamUsageEnabled())

	if len(res.OptimizerIDs) != 1 || res.OptimizerIDs[0] != StreamUsageOptimizerID {
		t.Fatalf("optimizer ids = %v, want [%s]", res.OptimizerIDs, StreamUsageOptimizerID)
	}
	root := decode(t, res.Body)
	so, ok := root["stream_options"].(map[string]any)
	if !ok {
		t.Fatalf("expected stream_options object, got %v", root["stream_options"])
	}
	if include, ok := so["include_usage"].(bool); !ok || !include {
		t.Fatalf("expected stream_options.include_usage = true, got %v", so["include_usage"])
	}
}

func TestStreamUsage_MergesIntoExistingStreamOptionsWithoutIncludeUsage(t *testing.T) {
	body := `{"model":"gpt-5.5","stream":true,"stream_options":{"some_other_field":true},"messages":[]}`
	res := apply(t, body, streamUsageEnabled())

	if len(res.OptimizerIDs) != 1 {
		t.Fatalf("expected optimizer applied, got %v", res.OptimizerIDs)
	}
	root := decode(t, res.Body)
	so := root["stream_options"].(map[string]any)
	if include, ok := so["include_usage"].(bool); !ok || !include {
		t.Fatalf("expected include_usage = true merged in, got %v", so["include_usage"])
	}
	if other, ok := so["some_other_field"].(bool); !ok || !other {
		t.Fatalf("expected caller's other stream_options field preserved, got %v", so["some_other_field"])
	}
}

// The core rule: an explicit client choice of include_usage:false is a DELIBERATE
// choice and must never be overwritten, even though the gateway would normally
// want usage data for the money truth.
func TestStreamUsage_NeverOverwritesExplicitFalse(t *testing.T) {
	body := `{"model":"gpt-5.5","stream":true,"stream_options":{"include_usage":false},"messages":[]}`
	res := apply(t, body, streamUsageEnabled())

	if len(res.OptimizerIDs) != 0 || string(res.Body) != body {
		t.Errorf("explicit include_usage:false must be respected (passthrough), got ids=%v body=%s", res.OptimizerIDs, res.Body)
	}
}

func TestStreamUsage_IdempotentWhenAlreadyTrue(t *testing.T) {
	body := `{"model":"gpt-5.5","stream":true,"stream_options":{"include_usage":true},"messages":[]}`
	res := apply(t, body, streamUsageEnabled())

	if len(res.OptimizerIDs) != 0 || string(res.Body) != body {
		t.Errorf("already-true include_usage must be passthrough (no re-injection), got ids=%v", res.OptimizerIDs)
	}
}

func TestStreamUsage_NonStreamingRequestUntouched(t *testing.T) {
	body := `{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}`
	res := apply(t, body, streamUsageEnabled())

	if len(res.OptimizerIDs) != 0 || string(res.Body) != body {
		t.Errorf("non-streaming request must be untouched, got ids=%v", res.OptimizerIDs)
	}
}

// CW 2: stream_options is a Chat-Completions-only parameter. A streamed
// Responses request was getting one injected, which sends the provider a field
// the Responses API does not accept — the optimizer that exists to improve the
// money truth turning into a 400 on the caller's request. Mirrors the
// endpoint/shape gate applyOutputBrevity already has.
func TestStreamUsage_StreamedResponsesBodyIsUntouched(t *testing.T) {
	for name, body := range map[string]string{
		"input":                      `{"model":"gpt-5.5","stream":true,"input":"hi"}`,
		"instructions":               `{"model":"gpt-5.5","stream":true,"instructions":"be terse","input":[]}`,
		"neither messages nor input": `{"model":"gpt-5.5","stream":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			res := apply(t, body, streamUsageEnabled())
			if len(res.OptimizerIDs) != 0 || string(res.Body) != body {
				t.Errorf("non-Chat-Completions body must be untouched, got ids=%v body=%s", res.OptimizerIDs, res.Body)
			}
		})
	}
}

func TestStreamUsage_DisabledIsPassthrough(t *testing.T) {
	body := `{"model":"gpt-5.5","stream":true,"messages":[]}`
	res := apply(t, body, providers.TransformPolicy{RuntimeMode: "active", Optimizers: map[string]bool{}})
	if len(res.OptimizerIDs) != 0 || string(res.Body) != body {
		t.Errorf("disabled optimizer must pass body through unchanged, got ids=%v", res.OptimizerIDs)
	}
}

// C8 review finding 5: the flag alone must not be enough — an uncleared eval
// gate must block injection exactly like it blocks output-brevity/
// reasoning-effort, since this now goes through OptimizerActive.
func TestStreamUsage_FlagWithoutEvalGateIsPassthrough(t *testing.T) {
	body := `{"model":"gpt-5.5","stream":true,"messages":[]}`
	policy := providers.TransformPolicy{
		RuntimeMode: "active",
		Optimizers:  map[string]bool{StreamUsageOptimizerID: true},
		EvalGates:   map[string]bool{StreamUsageOptimizerID: false},
	}
	res := apply(t, body, policy)
	if len(res.OptimizerIDs) != 0 || string(res.Body) != body {
		t.Errorf("flag without a cleared eval gate must not inject, got ids=%v body=%s", res.OptimizerIDs, res.Body)
	}
}

func TestStreamUsage_MalformedBodyPassesThroughUnchanged(t *testing.T) {
	bad := apply(t, "not json", streamUsageEnabled())
	if len(bad.OptimizerIDs) != 0 || string(bad.Body) != "not json" {
		t.Errorf("invalid JSON must pass through unchanged, got ids=%v body=%s", bad.OptimizerIDs, bad.Body)
	}
}

// stream_options present but not an object shape (malformed on the client's
// part) must not be guessed at — leave it exactly as sent.
func TestStreamUsage_NonObjectStreamOptionsUntouched(t *testing.T) {
	body := `{"model":"gpt-5.5","stream":true,"stream_options":"weird","messages":[]}`
	res := apply(t, body, streamUsageEnabled())
	if len(res.OptimizerIDs) != 0 || string(res.Body) != body {
		t.Errorf("non-object stream_options must be left untouched, got ids=%v", res.OptimizerIDs)
	}
}
