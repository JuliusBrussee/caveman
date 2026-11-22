package anthropic

import (
	"bytes"
	"context"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// TestStatelessAdapterNeverMutatesTopLevelEffort verifies the cache-safety
// invariant: the stateless Anthropic adapter MUST NOT mutate top-level
// `output_config.effort` (which busts Anthropic's prompt cache). Stateful
// per-message effort marks for Claude 5+ (Opus 5.5, etc.) are applied in
// `proxy/internal/gateway/local_turn_effort.go`.
func TestStatelessAdapterNeverMutatesTopLevelEffort(t *testing.T) {
	adapter := New("https://api.anthropic.com")
	policy := providers.TransformPolicy{
		RuntimeMode: "compress",
		AuthMode:    "payg",
		Optimizers:  map[string]bool{providers.TurnEffortOptimizerID: true},
	}

	for _, model := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-sonnet-4-6", "claude-opus-4-7"} {
		body := []byte(`{"model":"` + model + `","max_tokens":1024,"output_config":{"effort":"high"},"messages":[{"role":"user","content":"Check auth.go"},{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"clean"}]}]}`)
		res, err := adapter.ApplyProviderNativeTransforms(context.Background(), bytes.NewReader(body), providers.RequestMetadata{
			Provider: "anthropic", Endpoint: "/v1/messages", Model: model,
		}, policy)
		if err != nil {
			t.Fatalf("%s: %v", model, err)
		}
		if !bytes.Equal(res.Body, body) || len(res.OptimizerIDs) != 0 {
			t.Fatalf("%s stateless adapter mutated top-level effort (cache bust risk): ids=%v body=%s", model, res.OptimizerIDs, res.Body)
		}
	}

	// Verify shared turn classification helper handles Claude Code 2.1.220+
	// trailing role:"system", sibling <system-reminder>, and JSON-escaped errors.
	cleanWithReminderAndSystem := []byte(`{"model":"claude-opus-5-5","max_tokens":1024,"messages":[{"role":"user","content":"a"},{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"clean"},{"type":"text","text":"<system-reminder>Keep answers brief.</system-reminder>"}]},{"role":"system","content":"hook note"}]}`)
	if !providers.ShouldStepDownTurnEffort(cleanWithReminderAndSystem, "anthropic", "/v1/messages") {
		t.Fatalf("expected clean Claude tool_result with <system-reminder> and trailing system msg to qualify")
	}
	escapedErr := []byte(`{"model":"claude-opus-5-5","max_tokens":1024,"messages":[{"role":"user","content":"Run tests"},{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok pkg/a\nFAIL\tpkg/b\texit status 127"}]}]}`)
	if providers.ShouldStepDownTurnEffort(escapedErr, "anthropic", "/v1/messages") {
		t.Fatalf("JSON-escaped FAIL/exit-127 must NOT qualify for step-down")
	}
}
