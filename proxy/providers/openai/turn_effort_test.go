package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// TestStatelessAdapterNeverMutatesTopLevelTurnEffort verifies the cache-safety
// invariant: the stateless OpenAI adapter MUST NOT mutate top-level
// `reasoning.effort` / `reasoning_effort` for `turn-effort-step` (which busts
// OpenAI's prompt cache). Stateful per-message `configuration_update` items
// for GPT-6+ are applied in `proxy/internal/gateway/local_turn_effort.go`.
func TestStatelessAdapterNeverMutatesTopLevelTurnEffort(t *testing.T) {
	adapter := New("https://api.openai.com")
	policy := providers.TransformPolicy{
		RuntimeMode: "compress",
		AuthMode:    "payg",
		Optimizers:  map[string]bool{providers.TurnEffortOptimizerID: true},
	}

	respBody := []byte(`{"model":"gpt-5.4","reasoning":{"effort":"xhigh"},"input":[{"role":"user","content":"a"},{"type":"function_call","call_id":"c1","name":"exec_command","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"}]}`)
	if res, _ := adapter.ApplyProviderNativeTransforms(context.Background(), bytes.NewReader(respBody), providers.RequestMetadata{Provider: "openai", Endpoint: "/v1/responses", Model: "gpt-5.4"}, policy); !bytes.Equal(res.Body, respBody) || len(res.OptimizerIDs) != 0 {
		t.Fatalf("stateless Responses adapter must not mutate top-level effort: ids=%v body=%s", res.OptimizerIDs, res.Body)
	}
	chatBody := []byte(`{"model":"gpt-6-sol","reasoning_effort":"high","messages":[{"role":"user","content":"a"},{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"ok"}]}`)
	if res, _ := adapter.ApplyProviderNativeTransforms(context.Background(), bytes.NewReader(chatBody), providers.RequestMetadata{Provider: "openai", Endpoint: "/v1/chat/completions", Model: "gpt-6-sol"}, policy); !bytes.Equal(res.Body, chatBody) || len(res.OptimizerIDs) != 0 {
		t.Fatalf("stateless Chat adapter must not mutate top-level reasoning_effort: ids=%v body=%s", res.OptimizerIDs, res.Body)
	}
}

func TestCachedPrefixComponentsIgnoresReasoningEnvelopeAndTracksPrompt(t *testing.T) {
	base := []byte(`{
		"model":"gpt-5.4",
		"instructions":"You are Codex.",
		"reasoning":{"effort":"xhigh"},
		"tools":[{"type":"function","name":"exec_command","parameters":{"type":"object"}}],
		"input":[{"role":"user","content":"Inspect server.go"}]
	}`)
	stepped := []byte(`{
		"model":"gpt-5.4",
		"instructions":"You are Codex.",
		"reasoning":{"effort":"medium"},
		"prompt_cache_key":"cave_1234",
		"tools":[{"type":"function","name":"exec_command","parameters":{"type":"object"}}],
		"input":[{"role":"user","content":"Inspect server.go"}]
	}`)
	c1, cached1, ok1 := CachedPrefixComponents(base)
	c2, cached2, ok2 := CachedPrefixComponents(stepped)
	if !ok1 || !ok2 || len(c1) != 3 || len(c2) != 3 || cached1 != 3 || cached2 != 3 {
		t.Fatalf("unexpected components: ok=(%v,%v) lens=(%d,%d) cached=(%d,%d)", ok1, ok2, len(c1), len(c2), cached1, cached2)
	}
	for i := range c1 {
		if !bytes.Equal(c1[i], c2[i]) {
			t.Fatalf("component %d changed when only reasoning/prompt_cache_key changed", i)
		}
	}

	// Chat Completions leading system message folds into component 0 so
	// sharedComponents == 2 and conversationComponents == 3 align with Anthropic.
	chatA := []byte(`{"model":"gpt-5.4","reasoning_effort":"high","tools":[{"type":"function","function":{"name":"read"}}],"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi A"}]}`)
	chatB := []byte(`{"model":"gpt-5.4","reasoning_effort":"low","tools":[{"type":"function","function":{"name":"read"}}],"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi B"}]}`)
	ca, _, okA := CachedPrefixComponents(chatA)
	cb, _, okB := CachedPrefixComponents(chatB)
	if !okA || !okB || len(ca) != 3 || len(cb) != 3 {
		t.Fatalf("unexpected chat components: ok=(%v,%v) lens=(%d,%d)", okA, okB, len(ca), len(cb))
	}
	if !bytes.Equal(ca[0], cb[0]) || !bytes.Equal(ca[1], cb[1]) || bytes.Equal(ca[2], cb[2]) {
		t.Fatalf("chat components must share [system, tools] and differ on first user message")
	}

	// Non-caching endpoints return ok=false.
	adapter := Adapter{}
	if _, _, ok := adapter.CachedPrefixComponents(base, providers.RequestMetadata{Endpoint: "/v1/responses/input_tokens"}); ok {
		t.Fatalf("input_tokens endpoint must not report cached prefix")
	}
	if _, _, ok := adapter.CachedPrefixComponents(base, providers.RequestMetadata{Endpoint: "/v1/embeddings"}); ok {
		t.Fatalf("embeddings endpoint must not report cached prefix")
	}

	// Codex leading additional_tools input item folds into component 1 (tools)
	// so two different Codex sessions don't collide at components[2].
	codexSessA := []byte(`{"model":"gpt-5.4","instructions":"sys","tools":[{"type":"function","name":"exec"}],"input":[{"type":"additional_tools","tools":[{"name":"mcp_a"}]},{"role":"user","content":"task A"}]}`)
	codexSessB := []byte(`{"model":"gpt-5.4","instructions":"sys","tools":[{"type":"function","name":"exec"}],"input":[{"type":"additional_tools","tools":[{"name":"mcp_a"}]},{"role":"user","content":"task B"}]}`)
	xa, _, okXA := CachedPrefixComponents(codexSessA)
	xb, _, okXB := CachedPrefixComponents(codexSessB)
	if !okXA || !okXB || len(xa) != 3 || len(xb) != 3 {
		t.Fatalf("codex additional_tools components unexpected: ok=(%v,%v) lens=(%d,%d)", okXA, okXB, len(xa), len(xb))
	}
	if !bytes.Equal(xa[1], xb[1]) || bytes.Equal(xa[2], xb[2]) {
		t.Fatalf("additional_tools should fold into shared component[1] and leave user prompts distinct in component[2]")
	}
}

func TestTurnEffortSignalsHermesErrorsMechanicalUserAndRecoveryNames(t *testing.T) {
	// 1. Hermes JSON tool error shapes (`"returncode": 1` and `"error": "..."`) suppress step-down.
	for _, hermesErr := range []string{
		`{"output":"ls: no such file","returncode":1}`,
		`{"error":"Permission denied reading /etc/shadow"}`,
	} {
		escaped, _ := json.Marshal(hermesErr)
		body := []byte(`{"model":"gpt-5.4","reasoning_effort":"high","messages":[{"role":"user","content":"check"},{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"terminal","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":` + string(escaped) + `}]}`)
		if providers.ShouldStepDownTurnEffort(body, "openai", "/v1/chat/completions") {
			t.Fatalf("Hermes error payload %s must NOT qualify for step-down", hermesErr)
		}
	}

	// 2. GPT-6 mechanical user continuation detection accepts short acks and rejects substantive prompts.
	ackBody := []byte(`{"model":"gpt-6-sol","input":[{"role":"user","content":"plan"},{"role":"assistant","content":"ok"},{"role":"user","content":"continue"}]}`)
	if !providers.IsMechanicalUserContinuation(ackBody) {
		t.Fatalf("expected 'continue' to be recognized as mechanical continuation")
	}
	subBody := []byte(`{"model":"gpt-6-sol","input":[{"role":"user","content":"plan"},{"role":"assistant","content":"ok"},{"role":"user","content":"Why did the auth test fail on line 42?"}]}`)
	if providers.IsMechanicalUserContinuation(subBody) {
		t.Fatalf("substantive question must NOT be recognized as mechanical continuation")
	}

	// 3. Hermes single-underscore MCP recovery spellings recognized by IsRecoveryToolName.
	for _, name := range []string{
		"mcp_caveman_native_caveman_retrieve",
		"mcp_caveman_codemode_caveman_retrieve",
		"mcp_caveman_tools_caveman_retrieve",
		"mcp_caveman_caveman_retrieve",
		"mcp__caveman-codemode__caveman_retrieve",
		"mcp__caveman-tools__caveman_retrieve",
	} {
		if !providers.IsRecoveryToolName(name) {
			t.Errorf("IsRecoveryToolName(%q) = false, want true", name)
		}
	}
}
