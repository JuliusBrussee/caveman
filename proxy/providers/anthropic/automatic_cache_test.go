package anthropic

import (
	"context"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func automaticPolicy(ids ...string) providers.TransformPolicy {
	enabled := make(map[string]bool, len(ids))
	for _, id := range ids {
		enabled[id] = true
	}
	return providers.TransformPolicy{RuntimeMode: "active", Optimizers: enabled}
}

func applyAutomatic(t *testing.T, body string, meta providers.RequestMetadata, policy providers.TransformPolicy) providers.TransformResult {
	t.Helper()
	a := New("http://upstream").(Adapter)
	res, err := a.ApplyProviderNativeTransforms(context.Background(), strings.NewReader(body), meta, policy)
	if err != nil {
		t.Fatalf("transform error: %v", err)
	}
	return res
}

func TestAutomaticPromptCacheAddsOnlyTopLevelFiveMinuteMarker(t *testing.T) {
	body := "{\n \"model\" : \"claude-sonnet-4-6\", \"messages\" : [ {\"role\":\"user\",\"content\":\"hi\"} ]\n}"
	res := applyAutomatic(t, body, providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"}, automaticPolicy(AutomaticPromptCacheOptimizerID))
	want := "{\n \"model\" : \"claude-sonnet-4-6\", \"messages\" : [ {\"role\":\"user\",\"content\":\"hi\"} ],\"cache_control\":{\"type\":\"ephemeral\"}\n}"
	if string(res.Body) != want {
		t.Fatalf("automatic cache insertion changed unrelated bytes:\n got %s\nwant %s", res.Body, want)
	}
	if len(res.OptimizerIDs) != 1 || res.OptimizerIDs[0] != AutomaticPromptCacheOptimizerID {
		t.Fatalf("optimizer ids = %v, want only %q", res.OptimizerIDs, AutomaticPromptCacheOptimizerID)
	}
}

func TestAutomaticPromptCacheConflictsPassThroughByteIdentically(t *testing.T) {
	base := `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`
	tests := []struct {
		name   string
		body   string
		meta   providers.RequestMetadata
		policy providers.TransformPolicy
	}{
		{"both Caveman cache strategies", base, providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"}, automaticPolicy(OptimizerID, AutomaticPromptCacheOptimizerID)},
		{"caller block marker", `{"system":[{"type":"text","text":"stable","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]}`, providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"}, automaticPolicy(AutomaticPromptCacheOptimizerID)},
		{"caller escaped top-level marker", `{"cache\u005fcontrol":{"type":"ephemeral"},"messages":[{"role":"user","content":"hi"}]}`, providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"}, automaticPolicy(AutomaticPromptCacheOptimizerID)},
		{"count tokens endpoint", base, providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages/count_tokens"}, automaticPolicy(AutomaticPromptCacheOptimizerID)},
		{"bedrock metadata", base, providers.RequestMetadata{Provider: "bedrock", Endpoint: "/v1/messages"}, automaticPolicy(AutomaticPromptCacheOptimizerID)},
		{"unknown endpoint", base, providers.RequestMetadata{Provider: "anthropic"}, automaticPolicy(AutomaticPromptCacheOptimizerID)},
		{"subscription", base, providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"}, func() providers.TransformPolicy {
			p := automaticPolicy(AutomaticPromptCacheOptimizerID)
			p.AuthMode = "subscription"
			return p
		}()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := applyAutomatic(t, tc.body, tc.meta, tc.policy)
			if string(res.Body) != tc.body || len(res.OptimizerIDs) != 0 {
				t.Fatalf("unsafe/unsupported request changed: ids=%v body=%s", res.OptimizerIDs, res.Body)
			}
		})
	}
}

func TestAutomaticPromptCacheDoesNotGuessModelTokenThreshold(t *testing.T) {
	// Anthropic silently declines cache creation below the current model-specific
	// minimum. The adapter has no provider tokenizer and must not maintain a
	// drifting model threshold table or fabricate whether the provider cached it.
	body := `{"model":"future-active-claude","messages":[{"role":"user","content":"x"}]}`
	res := applyAutomatic(t, body, providers.RequestMetadata{Provider: "anthropic", Endpoint: "/anthropic/v1/messages"}, automaticPolicy(AutomaticPromptCacheOptimizerID))
	if len(res.OptimizerIDs) != 1 || res.OptimizerIDs[0] != AutomaticPromptCacheOptimizerID {
		t.Fatalf("provider-owned below-minimum decision was guessed locally: %v", res.OptimizerIDs)
	}
	if !strings.Contains(string(res.Body), `"cache_control":{"type":"ephemeral"}`) {
		t.Fatalf("top-level marker missing: %s", res.Body)
	}
}

func TestAnthropicCacheStrategiesRecordModeAreByteIdentical(t *testing.T) {
	body := `{"model":"claude-sonnet-4-6","system":"stable","messages":[{"role":"user","content":"hi"}]}`
	for _, optimizerID := range []string{OptimizerID, AutomaticPromptCacheOptimizerID} {
		t.Run(optimizerID, func(t *testing.T) {
			policy := automaticPolicy(optimizerID)
			policy.RuntimeMode = "record"
			res := applyAutomatic(t, body, providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"}, policy)
			if string(res.Body) != body || len(res.OptimizerIDs) != 0 {
				t.Fatalf("record mode changed bytes or attributed an optimizer: ids=%v body=%s", res.OptimizerIDs, res.Body)
			}
		})
	}
}
