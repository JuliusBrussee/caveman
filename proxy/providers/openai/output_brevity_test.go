package openai

import (
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func brevityPolicy(flag, gate bool) providers.TransformPolicy {
	return providers.TransformPolicy{
		RuntimeMode: "active",
		Optimizers:  map[string]bool{BrevityOptimizerID: flag},
		EvalGates:   map[string]bool{BrevityOptimizerID: gate},
	}
}

func TestBrevity_RequiresBothFlagAndEvalGate(t *testing.T) {
	body := `{"model":"gpt-5.5","input":"hi"}`

	// Flag on, eval gate NOT cleared -> must not apply (the whole point of W8).
	r := apply(t, body, brevityPolicy(true, false))
	if len(r.OptimizerIDs) != 0 || string(r.Body) != body {
		t.Fatalf("brevity must not run without a cleared eval gate, got %v", r.OptimizerIDs)
	}

	// Eval gate cleared, flag off -> must not apply.
	r = apply(t, body, brevityPolicy(false, true))
	if len(r.OptimizerIDs) != 0 {
		t.Fatalf("brevity must not run without the policy flag, got %v", r.OptimizerIDs)
	}

	// Both -> applies and adds the provider-native cap.
	r = apply(t, body, brevityPolicy(true, true))
	if len(r.OptimizerIDs) != 1 || r.OptimizerIDs[0] != BrevityOptimizerID {
		t.Fatalf("both flag + eval gate must apply brevity, got %v", r.OptimizerIDs)
	}
	if got := decode(t, r.Body)["max_output_tokens"]; got != float64(brevityMaxOutputTokens) {
		t.Fatalf("max_output_tokens = %v, want %d", got, brevityMaxOutputTokens)
	}
}

func TestBrevity_RecordModePassThrough(t *testing.T) {
	body := `{"model":"gpt-5.5","input":"hi"}`
	r := apply(t, body, providers.TransformPolicy{RuntimeMode: "record", Optimizers: map[string]bool{}, EvalGates: map[string]bool{}})
	if len(r.OptimizerIDs) != 0 || string(r.Body) != body {
		t.Fatalf("record/no-optimizer must pass through unchanged")
	}
}

func TestBrevity_RespectsCallerCap(t *testing.T) {
	body := `{"model":"gpt-5.5","input":"hi","max_output_tokens":99}`
	r := apply(t, body, brevityPolicy(true, true))
	if len(r.OptimizerIDs) != 0 {
		t.Fatalf("brevity must respect a caller-set cap, got %v", r.OptimizerIDs)
	}
}

func TestBrevity_ChatCompletionsUsesMaxCompletionTokens(t *testing.T) {
	body := `{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}]}`
	r := apply(t, body, brevityPolicy(true, true))
	if got := decode(t, r.Body)["max_completion_tokens"]; got != float64(brevityMaxOutputTokens) {
		t.Fatalf("max_completion_tokens = %v, want %d", got, brevityMaxOutputTokens)
	}
}

// Both optimizers can run together (pipeline): cache key + brevity.
func TestBrevity_ComposesWithCacheKey(t *testing.T) {
	body := `{"model":"gpt-5.5","instructions":"You are terse.","input":"hi"}`
	policy := providers.TransformPolicy{
		RuntimeMode: "active",
		Optimizers:  map[string]bool{OptimizerID: true, BrevityOptimizerID: true},
		EvalGates:   map[string]bool{BrevityOptimizerID: true},
	}
	r := apply(t, body, policy)
	if len(r.OptimizerIDs) != 2 {
		t.Fatalf("expected both optimizers applied, got %v", r.OptimizerIDs)
	}
	root := decode(t, r.Body)
	if root["prompt_cache_key"] == nil || root["max_output_tokens"] == nil {
		t.Fatalf("expected both prompt_cache_key and max_output_tokens, got %v", root)
	}
}
