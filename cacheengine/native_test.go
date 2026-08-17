package cacheengine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func optimizeRequest(provider, model, endpoint, epoch, body string) NativeRequest {
	return NativeRequest{
		Scope:         "org-a/project-a",
		Epoch:         epoch,
		PartitionKey:  "session-1",
		Provider:      provider,
		Model:         model,
		Endpoint:      endpoint,
		Body:          []byte(body),
		RuntimeMode:   "optimize",
		AuthMode:      "payg",
		PrefixTokens:  5000,
		ExpectedCalls: 4,
	}
}

func decodeObject(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatalf("decode result: %v\n%s", err, body)
	}
	return root
}

func TestOptimizeRejectsOversizedBodyBeforeResolver(t *testing.T) {
	resolverCalls := 0
	engine, err := NewChecked(Config{
		MaxRequestBytes: 4,
		ResolveProfile: func(NativeRequest) (Profile, bool) {
			resolverCalls++
			return Profile{}, false
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Optimize(context.Background(), NativeRequest{Body: []byte(`{"x":1}`)})
	if err == nil || !strings.Contains(err.Error(), "byte limit") || resolverCalls != 0 || result.Body != nil {
		t.Fatalf("result=%#v resolver_calls=%d err=%v", result, resolverCalls, err)
	}
}

func TestOptimizeHonorsCancellationBeforeResolver(t *testing.T) {
	resolverCalls := 0
	engine := New(Config{ResolveProfile: func(NativeRequest) (Profile, bool) {
		resolverCalls++
		return Profile{}, false
	}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := engine.Optimize(ctx, optimizeRequest("openai", "gpt-5.6", "/v1/responses", "cancelled", `{"model":"gpt-5.6","input":"x"}`))
	if !errors.Is(err, context.Canceled) || resolverCalls != 0 || result.Body != nil {
		t.Fatalf("result=%#v calls=%d err=%v", result, resolverCalls, err)
	}
}

func TestOptimizePassesThroughInvalidIdentity(t *testing.T) {
	request := optimizeRequest("openai\x00", "gpt-5.6", "/v1/responses", "identity", `{"model":"gpt-5.6","input":"x"}`)
	result, err := New(Config{}).Optimize(context.Background(), request)
	if err != nil || result.Reason != ReasonMalformedRequest || !bytes.Equal(result.Body, request.Body) || result.Applied {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestOptimizeAnthropicCombinesStableAndRollingBreakpoints(t *testing.T) {
	engine := New(Config{})
	req := optimizeRequest("anthropic", "claude-sonnet-4-6", "/v1/messages", "anthropic-1", `{
		"model":"claude-sonnet-4-6",
		"system":"You are a careful compiler.",
		"tools":[{"name":"read_file","description":"Read one file","input_schema":{"type":"object"}}],
		"messages":[{"role":"user","content":"Inspect this repository"}],
		"max_tokens":256
	}`)
	result, err := engine.Optimize(context.Background(), req)
	if err != nil {
		t.Fatalf("optimize: %v", err)
	}
	if result.Decision != DecisionApply || !result.Applied {
		t.Fatalf("result = %#v", result)
	}
	root := decodeObject(t, result.Body)
	if _, ok := root["cache_control"]; !ok {
		t.Fatal("missing Anthropic rolling top-level cache_control")
	}
	tools := root["tools"].([]any)
	if _, ok := tools[len(tools)-1].(map[string]any)["cache_control"]; !ok {
		t.Fatal("missing reused stable tool breakpoint")
	}
	if !containsString(result.OptimizerIDs, AnthropicStableOptimizerID) || !containsString(result.OptimizerIDs, AnthropicRollingOptimizerID) {
		t.Fatalf("optimizer ids = %#v", result.OptimizerIDs)
	}
	if result.ClaimBasis != "inferred" || result.VerifiedSavingsUSD != 0 {
		t.Fatalf("standalone result made verified claim: %#v", result)
	}
}

func TestOptimizeOpenAI56AddsScopedKeyAndExplicitBreakpoint(t *testing.T) {
	engine := New(Config{MaxKeyShards: 16})
	req := optimizeRequest("openai", "gpt-5.6", "/v1/chat/completions", "openai-56", `{
		"model":"gpt-5.6",
		"messages":[
			{"role":"system","content":"You are a support assistant with a long stable policy."},
			{"role":"user","content":"What should I do next?"}
		],
		"stream":true
	}`)
	result, err := engine.Optimize(context.Background(), req)
	if err != nil {
		t.Fatalf("optimize: %v", err)
	}
	if result.Decision != DecisionApply || !result.Applied {
		t.Fatalf("result = %#v", result)
	}
	root := decodeObject(t, result.Body)
	key, _ := root["prompt_cache_key"].(string)
	if len(key) != 32 || key != result.Plan.RoutingKey {
		t.Fatalf("prompt_cache_key = %q, plan = %q", key, result.Plan.RoutingKey)
	}
	options, _ := root["prompt_cache_options"].(map[string]any)
	if options["mode"] != "explicit" {
		t.Fatalf("prompt_cache_options = %#v", options)
	}
	messages := root["messages"].([]any)
	content, ok := messages[len(messages)-1].(map[string]any)["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("latest content = %#v", messages[len(messages)-1])
	}
	breakpoint := content[0].(map[string]any)["prompt_cache_breakpoint"].(map[string]any)
	if breakpoint["mode"] != "explicit" {
		t.Fatalf("breakpoint = %#v", breakpoint)
	}
	if !containsString(result.OptimizerIDs, OpenAIKeyOptimizerID) || !containsString(result.OptimizerIDs, OpenAIExplicitOptimizerID) {
		t.Fatalf("optimizer ids = %#v", result.OptimizerIDs)
	}
	if bytes.Contains(result.Body, []byte("org-a/project-a")) {
		t.Fatal("tenant scope leaked into provider-visible routing key")
	}
}

func TestOptimizeOpenAI56KeepsStableAndRollingBreakpointWindow(t *testing.T) {
	engine := New(Config{})
	req := optimizeRequest("openai", "gpt-5.6", "/v1/chat/completions", "openai-rolling", `{
		"model":"gpt-5.6",
		"messages":[
			{"role":"system","content":"stable system"},
			{"role":"user","content":"first task"},
			{"role":"assistant","content":"first answer"},
			{"role":"user","content":"latest task"}
		]
	}`)
	result, err := engine.Optimize(context.Background(), req)
	if err != nil || !result.Applied {
		t.Fatalf("result = %#v, err=%v", result, err)
	}
	messages := decodeObject(t, result.Body)["messages"].([]any)
	for index, raw := range messages {
		message := raw.(map[string]any)
		blocks, isBlocks := message["content"].([]any)
		hasBreakpoint := false
		if isBlocks {
			for _, rawBlock := range blocks {
				block, _ := rawBlock.(map[string]any)
				if _, ok := block["prompt_cache_breakpoint"]; ok {
					hasBreakpoint = true
				}
			}
		}
		if !hasBreakpoint {
			t.Fatalf("message %d missing retained breakpoint: %#v", index, messages)
		}
	}
}

func TestOptimizeOpenAI56ResponsesKeepsStableAndRollingBreakpointWindow(t *testing.T) {
	engine := New(Config{})
	req := optimizeRequest("openai", "gpt-5.6", "/v1/responses", "openai-responses-rolling", `{
		"model":"gpt-5.6",
		"input":[
			{"role":"developer","content":"stable policy"},
			{"role":"user","content":"first task"},
			{"role":"assistant","content":"first answer"},
			{"role":"user","content":"latest task"}
		]
	}`)
	result, err := engine.Optimize(context.Background(), req)
	if err != nil || !result.Applied {
		t.Fatalf("result = %#v, err=%v", result, err)
	}
	input := decodeObject(t, result.Body)["input"].([]any)
	latest := input[len(input)-1].(map[string]any)["content"].([]any)
	if _, ok := latest[0].(map[string]any)["prompt_cache_breakpoint"]; !ok {
		t.Fatalf("latest input has no breakpoint: %#v", input)
	}
	stable := input[0].(map[string]any)["content"].([]any)
	if _, ok := stable[0].(map[string]any)["prompt_cache_breakpoint"]; !ok {
		t.Fatalf("stable input has no breakpoint: %#v", input)
	}
}

func TestOptimizeOpenAI56CapsBreakpointsAtStablePlusLatestThree(t *testing.T) {
	engine := New(Config{})
	req := optimizeRequest("openai", "gpt-5.6", "/v1/chat/completions", "openai-window", `{
		"model":"gpt-5.6",
		"messages":[
			{"role":"system","content":"stable system"},
			{"role":"user","content":"task one"},
			{"role":"assistant","content":"answer one"},
			{"role":"user","content":"task two"},
			{"role":"assistant","content":"answer two"},
			{"role":"user","content":"task three"}
		]
	}`)
	result, err := engine.Optimize(context.Background(), req)
	if err != nil || !result.Applied {
		t.Fatalf("result = %#v, err=%v", result, err)
	}
	messages := decodeObject(t, result.Body)["messages"].([]any)
	marked := make([]int, 0, 4)
	for index, raw := range messages {
		blocks, _ := raw.(map[string]any)["content"].([]any)
		if len(blocks) > 0 {
			if _, ok := blocks[0].(map[string]any)["prompt_cache_breakpoint"]; ok {
				marked = append(marked, index)
			}
		}
	}
	if got, want := fmt.Sprint(marked), "[0 3 4 5]"; got != want {
		t.Fatalf("marked = %s, want %s; messages=%#v", got, want, messages)
	}
}

func TestOptimizeOpenAI56AnchorsAfterAllLeadingStableMessages(t *testing.T) {
	engine := New(Config{})
	req := optimizeRequest("openai", "gpt-5.6", "/v1/chat/completions", "openai-stable-anchor", `{
		"model":"gpt-5.6",
		"messages":[
			{"role":"system","content":"stable system"},
			{"role":"developer","content":"stable developer"},
			{"role":"user","content":"task one"},
			{"role":"assistant","content":"answer one"},
			{"role":"user","content":"task two"},
			{"role":"assistant","content":"answer two"},
			{"role":"user","content":"task three"}
		]
	}`)
	result, err := engine.Optimize(context.Background(), req)
	if err != nil || !result.Applied {
		t.Fatalf("result = %#v, err=%v", result, err)
	}
	messages := decodeObject(t, result.Body)["messages"].([]any)
	marked := make([]int, 0, 4)
	for index, raw := range messages {
		blocks, _ := raw.(map[string]any)["content"].([]any)
		if len(blocks) > 0 {
			if _, ok := blocks[0].(map[string]any)["prompt_cache_breakpoint"]; ok {
				marked = append(marked, index)
			}
		}
	}
	if got, want := fmt.Sprint(marked), "[1 4 5 6]"; got != want {
		t.Fatalf("marked = %s, want %s; messages=%#v", got, want, messages)
	}
}

func TestOptimizeOpenAI56FallsBackToAffinityWhenNoMarkableBlock(t *testing.T) {
	engine := New(Config{})
	req := optimizeRequest("openai", "gpt-5.6", "/v1/responses", "openai-fallback", `{
		"model":"gpt-5.6",
		"instructions":"Stable instructions",
		"input":"current question"
	}`)
	result, err := engine.Optimize(context.Background(), req)
	if err != nil {
		t.Fatalf("optimize: %v", err)
	}
	root := decodeObject(t, result.Body)
	if _, ok := root["prompt_cache_key"]; !ok {
		t.Fatal("missing affinity key fallback")
	}
	if _, ok := root["prompt_cache_options"]; ok {
		t.Fatal("explicit mode without a breakpoint would disable useful implicit caching")
	}
	if containsString(result.OptimizerIDs, OpenAIExplicitOptimizerID) {
		t.Fatalf("false explicit optimizer attribution: %#v", result.OptimizerIDs)
	}
	if result.Profile.Attribution != AttributionAffinity || result.Plan.Attribution != AttributionAffinity || result.Reason != ReasonAffinityFallback {
		t.Fatalf("affinity fallback retained causal attribution: %#v", result)
	}
	observed := Observe(result, UsageObservation{
		CachedInputTokens: 1000, CacheObserved: true, CacheStatus: "hit",
	})
	if observed.AttributedToEngine {
		t.Fatalf("affinity hit attributed causally: %#v", observed)
	}
}

func TestOptimizeRespectsCallerManagedCachingIdempotently(t *testing.T) {
	engine := New(Config{})
	body := `{"model":"gpt-5.6","prompt_cache_key":"caller-key","messages":[{"role":"user","content":"hello"}]}`
	req := optimizeRequest("openai", "gpt-5.6", "/v1/chat/completions", "caller-managed", body)
	result, err := engine.Optimize(context.Background(), req)
	if err != nil {
		t.Fatalf("optimize: %v", err)
	}
	if result.Decision != DecisionPassThrough || result.Reason != ReasonCallerManaged || !bytes.Equal(result.Body, []byte(body)) {
		t.Fatalf("result = %#v", result)
	}
	if len(result.OptimizerIDs) != 0 {
		t.Fatalf("caller cache attributed to engine: %#v", result.OptimizerIDs)
	}
}

func TestOptimizeDetectsUnicodeEscapedCallerCacheKey(t *testing.T) {
	engine := New(Config{})
	body := `{"model":"gpt-5.6","prompt_cache\u005fkey":"caller-key","messages":[{"role":"user","content":"hello"}]}`
	req := optimizeRequest("openai", "gpt-5.6", "/v1/chat/completions", "caller-managed-escaped", body)
	result, err := engine.Optimize(context.Background(), req)
	if err != nil {
		t.Fatalf("optimize: %v", err)
	}
	if result.Decision != DecisionPassThrough || result.Reason != ReasonCallerManaged || !bytes.Equal(result.Body, []byte(body)) {
		t.Fatalf("result = %#v", result)
	}
}

func TestOptimizeIgnoresCacheLikeToolSchemaProperty(t *testing.T) {
	engine := New(Config{})
	openAIBody := `{"model":"gpt-5.6","tools":[{"type":"function","function":{"name":"inspect","parameters":{"type":"object","properties":{"prompt_cache_key":{"type":"string"}}}}}],"messages":[{"role":"user","content":"hello"}]}`
	result, err := engine.Optimize(context.Background(), optimizeRequest("openai", "gpt-5.6", "/v1/chat/completions", "schema-key", openAIBody))
	if err != nil || result.Decision != DecisionApply {
		t.Fatalf("OpenAI result=%#v err=%v", result, err)
	}
	anthropicBody := `{"model":"claude-sonnet-4-6","max_tokens":32,"tools":[{"name":"inspect","description":"inspect","input_schema":{"type":"object","properties":{"cache_control":{"type":"string"}}}}],"messages":[{"role":"user","content":"hello"}]}`
	result, err = engine.Optimize(context.Background(), optimizeRequest("anthropic", "claude-sonnet-4-6", "/v1/messages", "schema-control", anthropicBody))
	if err != nil || result.Decision != DecisionApply {
		t.Fatalf("Anthropic result=%#v err=%v", result, err)
	}
}

func TestOptimizeDetectsProviderCacheMarkerAtActiveWirePath(t *testing.T) {
	engine := New(Config{})
	body := `{"model":"gpt-5.6","messages":[{"role":"user","content":[{"type":"text","text":"hello","prompt_cache_breakpoint":{"mode":"explicit"}}]}]}`
	result, err := engine.Optimize(context.Background(), optimizeRequest("openai", "gpt-5.6", "/v1/chat/completions", "active-marker", body))
	if err != nil {
		t.Fatalf("optimize: %v", err)
	}
	if result.Decision != DecisionPassThrough || result.Reason != ReasonCallerManaged || !bytes.Equal(result.Body, []byte(body)) {
		t.Fatalf("result = %#v", result)
	}
}

func TestOptimizeGeminiImplicitAndUnsupportedStayByteIdentical(t *testing.T) {
	engine := New(Config{})
	body := `{"systemInstruction":{"parts":[{"text":"stable"}]},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`
	gemini := optimizeRequest("gemini", "gemini-2.5-pro", "generateContent", "gemini", body)
	result, err := engine.Optimize(context.Background(), gemini)
	if err != nil {
		t.Fatalf("gemini optimize: %v", err)
	}
	if result.Decision != DecisionObserveOnly || result.Reason != ReasonProviderManaged || !bytes.Equal(result.Body, []byte(body)) {
		t.Fatalf("gemini result = %#v", result)
	}
	unknown := optimizeRequest("unknown", "model", "/infer", "unknown", body)
	result, err = engine.Optimize(context.Background(), unknown)
	if err != nil {
		t.Fatalf("unknown optimize: %v", err)
	}
	if result.Decision != DecisionPassThrough || result.Reason != ReasonUnsupported || !bytes.Equal(result.Body, []byte(body)) {
		t.Fatalf("unknown result = %#v", result)
	}
	if result.Profile.Mode != ModeUnsupported || result.Plan.Mode != ModeUnsupported || result.ClaimBasis != "none" {
		t.Fatalf("unknown result invented support or claim: %#v", result)
	}
}

func TestUnknownBuiltInModelsFailClosedToOriginal(t *testing.T) {
	engine := New(Config{})
	tests := []NativeRequest{
		optimizeRequest("anthropic", "claude-future-unknown", "/v1/messages", "unknown-anthropic", `{"model":"claude-future-unknown","system":"stable","messages":[{"role":"user","content":"hello"}],"max_tokens":64}`),
		optimizeRequest("openai", "gpt-future-unknown", "/v1/responses", "unknown-openai", `{"model":"gpt-future-unknown","instructions":"stable","input":"hello","max_output_tokens":64}`),
		optimizeRequest("gemini", "gemini-future-unknown", "generateContent", "unknown-gemini", `{"systemInstruction":{"parts":[{"text":"stable"}]},"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"maxOutputTokens":64}}`),
	}
	for _, request := range tests {
		result, err := engine.Optimize(context.Background(), request)
		if err != nil || result.Decision != DecisionPassThrough || result.Reason != ReasonUnsupported || result.Applied || !bytes.Equal(result.Body, request.Body) {
			t.Fatalf("provider=%s result=%#v err=%v", request.Provider, result, err)
		}
	}
}

func TestBuiltInEndpointAndBodyModelMustMatch(t *testing.T) {
	engine := New(Config{})
	tests := []NativeRequest{
		optimizeRequest("openai", "gpt-5.6", "/v1/unknown", "bad-endpoint", `{"model":"gpt-5.6","messages":[{"role":"user","content":"hello"}]}`),
		optimizeRequest("openai", "gpt-5.6", "/v1/responses", "bad-openai-model", `{"model":"gpt-5.5","instructions":"stable","input":"hello"}`),
		optimizeRequest("anthropic", "claude-sonnet-4-6", "/v1/messages", "bad-anthropic-model", `{"model":"claude-opus-4-8","system":"stable","messages":[{"role":"user","content":"hello"}]}`),
		optimizeRequest("gemini", "gemini-2.5-pro", "unknown", "bad-gemini-endpoint", `{"systemInstruction":{"parts":[{"text":"stable"}]},"contents":[]}`),
	}
	for _, request := range tests {
		result, err := engine.Optimize(context.Background(), request)
		if err != nil || result.Applied || result.Decision != DecisionPassThrough || !bytes.Equal(result.Body, request.Body) {
			t.Fatalf("request=%#v result=%#v err=%v", request, result, err)
		}
	}
}

func TestOptimizeRecordMalformedAndDriftFailOpenToOriginal(t *testing.T) {
	engine := New(Config{})
	body := `{"model":"gpt-5.6","messages":[{"role":"system","content":"stable"},{"role":"user","content":"one"}]}`
	record := optimizeRequest("openai", "gpt-5.6", "/v1/chat/completions", "record", body)
	record.RuntimeMode = "record"
	result, err := engine.Optimize(context.Background(), record)
	if err != nil || !bytes.Equal(result.Body, []byte(body)) || result.Reason != ReasonRecordMode {
		t.Fatalf("record result = %#v, err=%v", result, err)
	}
	malformed := optimizeRequest("openai", "gpt-5.6", "/v1/chat/completions", "malformed", `{"model":`)
	result, err = engine.Optimize(context.Background(), malformed)
	if err != nil || !bytes.Equal(result.Body, malformed.Body) || result.Reason != ReasonMalformedRequest {
		t.Fatalf("malformed result = %#v, err=%v", result, err)
	}
	duplicateBody := `{"model":"gpt-5.6","model":"gpt-5.5","messages":[{"role":"user","content":"hello"}]}`
	duplicate := optimizeRequest("openai", "gpt-5.6", "/v1/chat/completions", "duplicate", duplicateBody)
	result, err = engine.Optimize(context.Background(), duplicate)
	if err != nil || !bytes.Equal(result.Body, []byte(duplicateBody)) || result.Reason != ReasonMalformedRequest {
		t.Fatalf("duplicate = %#v, err=%v", result, err)
	}
	first := optimizeRequest("openai", "gpt-5.6", "/v1/chat/completions", "drift-native", body)
	if result, err = engine.Optimize(context.Background(), first); err != nil || !result.Applied {
		t.Fatalf("first = %#v, err=%v", result, err)
	}
	driftBody := strings.Replace(body, `"stable"`, `"changed"`, 1)
	drift := optimizeRequest("openai", "gpt-5.6", "/v1/chat/completions", "drift-native", driftBody)
	result, err = engine.Optimize(context.Background(), drift)
	if err != nil || result.Reason != ReasonPrefixDrift || !bytes.Equal(result.Body, []byte(driftBody)) {
		t.Fatalf("drift = %#v, err=%v", result, err)
	}
}

func TestOpenAIStableDigestIncludesMarkedLeadingMessage(t *testing.T) {
	engine := New(Config{})
	body := `{"model":"gpt-5.6","instructions":"stable instructions","tools":[{"type":"function","name":"search"}],"messages":[{"role":"system","content":"policy one"},{"role":"user","content":"question"}]}`
	request := optimizeRequest("openai", "gpt-5.6", "/v1/chat/completions", "leading-message", body)
	first, err := engine.Optimize(context.Background(), request)
	if err != nil || !first.Applied {
		t.Fatalf("first = %#v, err=%v", first, err)
	}
	request.Body = []byte(strings.Replace(body, "policy one", "policy two", 1))
	drift, err := engine.Optimize(context.Background(), request)
	if err != nil || drift.Reason != ReasonPrefixDrift || !bytes.Equal(drift.Body, request.Body) {
		t.Fatalf("drift = %#v, err=%v", drift, err)
	}
}

func TestOpenAIStableDigestExcludesChangingFirstUserTask(t *testing.T) {
	engine := New(Config{})
	body := `{"model":"gpt-5.6","messages":[{"role":"system","content":"stable policy"},{"role":"user","content":"task one"}]}`
	request := optimizeRequest("openai", "gpt-5.6", "/v1/chat/completions", "changing-user", body)
	first, err := engine.Optimize(context.Background(), request)
	if err != nil || !first.Applied {
		t.Fatalf("first = %#v, err=%v", first, err)
	}
	request.Body = []byte(strings.Replace(body, "task one", "task two", 1))
	second, err := engine.Optimize(context.Background(), request)
	if err != nil || !second.Applied || second.Reason == ReasonPrefixDrift {
		t.Fatalf("changing user task poisoned stable prefix: %#v, err=%v", second, err)
	}
}

func TestNativeProfileProviderMismatchFailsClosed(t *testing.T) {
	engine := New(Config{})
	request := optimizeRequest("anthropic", "claude-sonnet-4-6", "/v1/messages", "profile-mismatch", `{"system":"stable","messages":[{"role":"user","content":"question"}]}`)
	request.Profile = explicitProfile()
	request.Profile.Provider = "openai"
	result, err := engine.Optimize(context.Background(), request)
	if err != nil || result.Reason != ReasonProfileMismatch || !bytes.Equal(result.Body, request.Body) {
		t.Fatalf("result = %#v, err=%v", result, err)
	}
	request.Profile.Provider = ""
	result, err = engine.Optimize(context.Background(), request)
	if err != nil || result.Reason != ReasonProfileMismatch || !bytes.Equal(result.Body, request.Body) {
		t.Fatalf("unbound profile result = %#v, err=%v", result, err)
	}
	request = optimizeRequest("openai", "gpt-5.6", "/v1/chat/completions", "profile-builtin", `{"model":"gpt-5.6","messages":[{"role":"system","content":"stable"},{"role":"user","content":"question"}]}`)
	request.Profile = explicitProfile()
	request.Profile.Provider = "openai"
	result, err = engine.Optimize(context.Background(), request)
	if err != nil || result.Reason != ReasonProfileMismatch || result.Applied || !bytes.Equal(result.Body, request.Body) {
		t.Fatalf("built-in profile override accepted: %#v, err=%v", result, err)
	}
}

func TestBuiltInResolverCannotClaimUnsupportedWireTTL(t *testing.T) {
	profile, ok := defaultProfile(optimizeRequest("anthropic", "claude-sonnet-4-6", "/v1/messages", "profile-ttl", `{"system":"stable","messages":[{"role":"user","content":"question"}]}`))
	if !ok {
		t.Fatal("missing default profile")
	}
	profile.TTL = time.Hour
	engine := New(Config{ResolveProfile: func(NativeRequest) (Profile, bool) { return profile, true }})
	request := optimizeRequest("anthropic", "claude-sonnet-4-6", "/v1/messages", "profile-ttl", `{"system":"stable","messages":[{"role":"user","content":"question"}]}`)
	result, err := engine.Optimize(context.Background(), request)
	if err != nil || result.Reason != ReasonProfileMismatch || result.Applied || !bytes.Equal(result.Body, request.Body) {
		t.Fatalf("unsupported TTL profile accepted: %#v, err=%v", result, err)
	}
}

func TestCustomDriverMakesNativeEngineProviderAgnostic(t *testing.T) {
	profile := explicitProfile()
	profile.ID = "acme-cache-v1"
	profile.Provider = "acme"
	engine := New(Config{
		ResolveProfile: func(request NativeRequest) (Profile, bool) { return profile, request.Provider == "acme" },
		Drivers: map[string]Driver{
			"acme": DriverFunc(func(_ context.Context, request NativeRequest, plan Plan) DriverResult {
				return DriverResult{
					Body:         append(append([]byte(nil), request.Body...), []byte("|cache="+plan.RoutingKey)...),
					OptimizerIDs: []string{"acme-native-cache"},
				}
			}),
		},
	})
	request := NativeRequest{
		Scope: "org-a/project-a", Epoch: "acme", Provider: "acme", Model: "any-model",
		Body: []byte("opaque-wire-body"), RuntimeMode: "optimize", AuthMode: "payg", ExpectedCalls: 3,
		StableSegments: []Segment{{Name: "prefix", Content: []byte("stable opaque prefix"), Tokens: 900, Stable: true, Cacheable: true}},
	}
	result, err := engine.Optimize(context.Background(), request)
	if err != nil {
		t.Fatalf("optimize: %v", err)
	}
	if !result.Applied || string(result.Body) != "opaque-wire-body|cache="+result.Plan.RoutingKey {
		t.Fatalf("result = %#v", result)
	}
	if !containsString(result.OptimizerIDs, "acme-native-cache") || result.VerifiedSavingsUSD != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestCustomDriverEmptyBodyPassesThrough(t *testing.T) {
	profile := explicitProfile()
	profile.Provider = "acme"
	engine := New(Config{
		ResolveProfile: func(NativeRequest) (Profile, bool) { return profile, true },
		Drivers: map[string]Driver{"acme": DriverFunc(func(_ context.Context, _ NativeRequest, _ Plan) DriverResult {
			return DriverResult{Body: []byte("invented"), OptimizerIDs: []string{"acme-cache"}}
		})},
	})
	request := NativeRequest{
		Scope: "org-a/project-a", Epoch: "empty", Provider: "acme", RuntimeMode: "optimize", AuthMode: "payg",
		StableSegments: []Segment{{Name: "prefix", Content: []byte("stable"), Tokens: 1024, Stable: true, Cacheable: true}},
	}
	result, err := engine.Optimize(context.Background(), request)
	if err != nil || result.Applied || result.Reason != ReasonMalformedRequest || len(result.Body) != 0 {
		t.Fatalf("result = %#v, err=%v", result, err)
	}
}

func TestCustomCallbacksCannotMutateCallerRequest(t *testing.T) {
	profile := explicitProfile()
	profile.Provider = "acme"
	engine := New(Config{
		ResolveProfile: func(request NativeRequest) (Profile, bool) {
			request.Body[0] = 'X'
			request.StableSegments[0].Content[0] = 'X'
			return profile, true
		},
		Drivers: map[string]Driver{"acme": DriverFunc(func(_ context.Context, request NativeRequest, _ Plan) DriverResult {
			original := append([]byte(nil), request.Body...)
			request.Body[0] = 'Y'
			request.StableSegments[0].Content[0] = 'Y'
			return DriverResult{Body: append(original, []byte("|cached")...), OptimizerIDs: []string{"acme-cache-v1"}}
		})},
	})
	request := NativeRequest{
		Scope: "scope", Epoch: "epoch", Provider: "acme", Model: "model", Body: []byte("original"),
		RuntimeMode: "optimize", AuthMode: "payg", ExpectedCalls: 2,
		StableSegments: []Segment{{Name: "stable", Content: []byte("prefix"), Tokens: 1024, Stable: true, Cacheable: true}},
	}
	originalBody := append([]byte(nil), request.Body...)
	originalPrefix := append([]byte(nil), request.StableSegments[0].Content...)
	result, err := engine.Optimize(context.Background(), request)
	if err != nil || !result.Applied {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if !bytes.Equal(request.Body, originalBody) || !bytes.Equal(request.StableSegments[0].Content, originalPrefix) {
		t.Fatalf("callback mutated caller request: body=%q prefix=%q", request.Body, request.StableSegments[0].Content)
	}
}

func TestCustomDriverInvalidOptimizerIdentityPassesThrough(t *testing.T) {
	profile := explicitProfile()
	profile.Provider = "acme"
	tests := [][]string{{""}, {" duplicate", "duplicate"}, {"duplicate", "duplicate"}, {"bad\x00id"}, {strings.Repeat("x", 257)}}
	for index, optimizerIDs := range tests {
		engine := New(Config{
			ResolveProfile: func(NativeRequest) (Profile, bool) { return profile, true },
			Drivers: map[string]Driver{"acme": DriverFunc(func(context.Context, NativeRequest, Plan) DriverResult {
				return DriverResult{Body: []byte("transformed"), OptimizerIDs: optimizerIDs}
			})},
		})
		request := NativeRequest{
			Scope: "scope", Epoch: fmt.Sprintf("epoch-%d", index), Provider: "acme", Model: "model", Body: []byte("original"),
			RuntimeMode: "optimize", AuthMode: "payg", ExpectedCalls: 2,
			StableSegments: []Segment{{Name: "stable", Content: []byte("prefix"), Tokens: 1024, Stable: true, Cacheable: true}},
		}
		result, err := engine.Optimize(context.Background(), request)
		if err != nil || result.Applied || result.Reason != ReasonTransformUnavailable || !bytes.Equal(result.Body, request.Body) {
			t.Fatalf("case %d result=%#v err=%v", index, result, err)
		}
	}
}

func TestCustomDriverOutputCannotExceedRequestByteLimit(t *testing.T) {
	profile := explicitProfile()
	profile.Provider = "acme"
	engine := New(Config{
		MaxRequestBytes: len("original"),
		ResolveProfile:  func(NativeRequest) (Profile, bool) { return profile, true },
		Drivers: map[string]Driver{"acme": DriverFunc(func(context.Context, NativeRequest, Plan) DriverResult {
			return DriverResult{Body: []byte("transformed"), OptimizerIDs: []string{"acme-cache-v1"}}
		})},
	})
	request := NativeRequest{
		Scope: "scope", Epoch: "bounded-output", Provider: "acme", Model: "model", Body: []byte("original"),
		RuntimeMode: "optimize", AuthMode: "payg", ExpectedCalls: 2,
		StableSegments: []Segment{{Name: "stable", Content: []byte("prefix"), Tokens: 1024, Stable: true, Cacheable: true}},
	}
	result, err := engine.Optimize(context.Background(), request)
	if err != nil || result.Applied || result.Reason != ReasonTransformUnavailable || !bytes.Equal(result.Body, request.Body) {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestOptimizeConcurrentSameEpochKeepsOneStableTransform(t *testing.T) {
	engine := New(Config{})
	body := `{"model":"gpt-5.6","messages":[{"role":"system","content":"stable shared prefix"},{"role":"user","content":"question"}]}`
	request := optimizeRequest("openai", "gpt-5.6", "/v1/chat/completions", "concurrent", body)
	const workers = 32
	results := make([]NativeResult, workers)
	errs := make([]error, workers)
	var group sync.WaitGroup
	for index := range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			results[index], errs[index] = engine.Optimize(context.Background(), request)
		}()
	}
	group.Wait()
	for index := range workers {
		if errs[index] != nil || !results[index].Applied {
			t.Fatalf("worker %d = %#v, err=%v", index, results[index], errs[index])
		}
		if !bytes.Equal(results[index].Body, results[0].Body) {
			t.Fatalf("worker %d emitted different cache bytes", index)
		}
	}
}

func FuzzOptimizeMalformedBuiltinsPassThrough(f *testing.F) {
	f.Add([]byte(`{"model":"gpt-5.6","messages":[]}`))
	f.Add([]byte(`{"model":`))
	f.Add([]byte{0, 1, 2, 3})
	f.Fuzz(func(t *testing.T, body []byte) {
		engine := New(Config{})
		request := optimizeRequest("openai", "gpt-5.6", "/v1/chat/completions", "fuzz", string(body))
		result, err := engine.Optimize(context.Background(), request)
		if err != nil {
			return
		}
		if !validUniqueJSONObject(body) && !bytes.Equal(result.Body, body) {
			t.Fatalf("malformed input mutated: %x -> %x", body, result.Body)
		}
	})
}

func TestOptimizeBedrockReusesCatalogGatedCachePoints(t *testing.T) {
	engine := New(Config{})
	req := optimizeRequest("bedrock", "global.anthropic.claude-sonnet-4-6", "converse", "bedrock", `{
		"system":[{"text":"stable instructions"}],
		"messages":[{"role":"user","content":[{"text":"hello"}]}]
	}`)
	result, err := engine.Optimize(context.Background(), req)
	if err != nil {
		t.Fatalf("optimize: %v", err)
	}
	if !result.Applied || !containsString(result.OptimizerIDs, BedrockCacheOptimizerID) {
		t.Fatalf("result = %#v", result)
	}
	root := decodeObject(t, result.Body)
	system := root["system"].([]any)
	if len(system) != 2 {
		t.Fatalf("system = %#v", system)
	}
	if _, ok := system[1].(map[string]any)["cachePoint"]; !ok {
		t.Fatalf("missing Bedrock cache point: %#v", system)
	}
	messages := root["messages"].([]any)
	latest := messages[len(messages)-1].(map[string]any)["content"].([]any)
	if _, ok := latest[len(latest)-1].(map[string]any)["cachePoint"]; !ok {
		t.Fatalf("missing rolling Bedrock message cache point: %#v", messages)
	}
}

func TestOptimizeBedrockInvokeAddsStableAndRollingCachePoints(t *testing.T) {
	engine := New(Config{})
	req := optimizeRequest("bedrock", "global.anthropic.claude-sonnet-4-6", "invoke", "bedrock-invoke", `{
		"system":"stable instructions",
		"messages":[
			{"role":"user","content":"first task"},
			{"role":"assistant","content":"first answer"},
			{"role":"user","content":"latest task"}
		]
	}`)
	result, err := engine.Optimize(context.Background(), req)
	if err != nil || !result.Applied {
		t.Fatalf("result = %#v, err=%v", result, err)
	}
	if !containsString(result.OptimizerIDs, BedrockCacheOptimizerID) || !containsString(result.OptimizerIDs, BedrockRollingOptimizerID) {
		t.Fatalf("optimizer ids = %#v", result.OptimizerIDs)
	}
	root := decodeObject(t, result.Body)
	system := root["system"].([]any)
	if _, ok := system[0].(map[string]any)["cache_control"]; !ok {
		t.Fatalf("missing stable cache point: %#v", system)
	}
	messages := root["messages"].([]any)
	latest := messages[len(messages)-1].(map[string]any)["content"].([]any)
	if _, ok := latest[0].(map[string]any)["cache_control"]; !ok {
		t.Fatalf("missing rolling cache point: %#v", latest)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func BenchmarkOptimizeOpenAIExplicit(b *testing.B) {
	body := `{"model":"gpt-5.6","messages":[{"role":"system","content":"` + strings.Repeat("stable policy ", 1000) + `"},{"role":"user","content":"question"}]}`
	engine := New(Config{})
	request := optimizeRequest("openai", "gpt-5.6", "/v1/chat/completions", "bench", body)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := engine.Optimize(context.Background(), request); err != nil {
			b.Fatal(err)
		}
	}
}
