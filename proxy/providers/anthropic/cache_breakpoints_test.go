package anthropic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func enabled() providers.TransformPolicy {
	return providers.TransformPolicy{RuntimeMode: "active", Optimizers: map[string]bool{OptimizerID: true}}
}

func apply(t *testing.T, body string, policy providers.TransformPolicy) providers.TransformResult {
	t.Helper()
	a := New("http://upstream").(Adapter)
	res, err := a.ApplyProviderNativeTransforms(context.Background(), strings.NewReader(body), providers.RequestMetadata{Provider: "anthropic"}, policy)
	if err != nil {
		t.Fatalf("transform error: %v", err)
	}
	return res
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("result not valid JSON: %v", err)
	}
	return m
}

func TestCacheBreakpoint_ToolsGetBreakpoint(t *testing.T) {
	body := `{"model":"claude-sonnet-4-6","tools":[{"name":"a","input_schema":{}},{"name":"b","input_schema":{}}],"messages":[{"role":"user","content":"hi"}]}`
	res := apply(t, body, enabled())

	if len(res.OptimizerIDs) != 1 || res.OptimizerIDs[0] != OptimizerID {
		t.Fatalf("optimizer ids = %v, want [%s]", res.OptimizerIDs, OptimizerID)
	}
	root := decode(t, res.Body)
	tools := root["tools"].([]any)
	last := tools[1].(map[string]any)
	cc, ok := last["cache_control"].(map[string]any)
	if !ok || cc["type"] != "ephemeral" {
		t.Errorf("last tool missing ephemeral cache_control: %v", last)
	}
	// First tool untouched; model + messages preserved.
	if _, has := tools[0].(map[string]any)["cache_control"]; has {
		t.Error("only the last tool should carry the breakpoint")
	}
	if root["model"] != "claude-sonnet-4-6" {
		t.Error("model not preserved")
	}
	if msgs := root["messages"].([]any); len(msgs) != 1 {
		t.Error("messages not preserved")
	}
}

func TestCacheBreakpoint_SkipsDeferredTool(t *testing.T) {
	body := `{"model":"claude-sonnet-4-6","tools":[{"name":"loaded","input_schema":{}},{"name":"deferred","defer_loading":true,"input_schema":{}}],"messages":[{"role":"user","content":"hi"}]}`
	res := apply(t, body, enabled())

	if len(res.OptimizerIDs) != 1 || res.OptimizerIDs[0] != OptimizerID {
		t.Fatalf("optimizer ids = %v, want [%s]", res.OptimizerIDs, OptimizerID)
	}
	root := decode(t, res.Body)
	tools := root["tools"].([]any)
	loaded := tools[0].(map[string]any)
	deferred := tools[1].(map[string]any)
	if cc, _ := loaded["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Fatalf("last non-deferred tool missing cache_control: %v", loaded)
	}
	if _, exists := deferred["cache_control"]; exists {
		t.Fatalf("deferred tool must not carry cache_control: %v", deferred)
	}
}

func TestCacheBreakpoint_AllDeferredToolsFallsBackToSystem(t *testing.T) {
	body := `{"model":"claude-sonnet-4-6","tools":[{"name":"deferred","defer_loading":true,"input_schema":{}}],"system":"stable prefix","messages":[{"role":"user","content":"hi"}]}`
	res := apply(t, body, enabled())

	if len(res.OptimizerIDs) != 1 || res.OptimizerIDs[0] != OptimizerID {
		t.Fatalf("optimizer ids = %v, want [%s]", res.OptimizerIDs, OptimizerID)
	}
	root := decode(t, res.Body)
	deferred := root["tools"].([]any)[0].(map[string]any)
	if _, exists := deferred["cache_control"]; exists {
		t.Fatalf("deferred tool must not carry cache_control: %v", deferred)
	}
	system := root["system"].([]any)[0].(map[string]any)
	if cc, _ := system["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Fatalf("system fallback missing cache_control: %v", system)
	}
}

func TestCacheBreakpoint_SystemStringConverted(t *testing.T) {
	body := `{"model":"claude-sonnet-4-6","system":"You are a careful assistant.","messages":[{"role":"user","content":"hi"}]}`
	res := apply(t, body, enabled())
	if len(res.OptimizerIDs) != 1 {
		t.Fatalf("expected optimizer applied, got %v", res.OptimizerIDs)
	}
	root := decode(t, res.Body)
	sys, ok := root["system"].([]any)
	if !ok || len(sys) != 1 {
		t.Fatalf("system not converted to block array: %v", root["system"])
	}
	block := sys[0].(map[string]any)
	if block["text"] != "You are a careful assistant." {
		t.Errorf("system text not preserved: %v", block["text"])
	}
	if cc, _ := block["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Errorf("system block missing cache_control: %v", block)
	}
}

func TestCacheBreakpoint_SystemArrayLastBlock(t *testing.T) {
	body := `{"system":[{"type":"text","text":"a"},{"type":"text","text":"b"}],"messages":[]}`
	res := apply(t, body, enabled())
	root := decode(t, res.Body)
	sys := root["system"].([]any)
	if _, has := sys[0].(map[string]any)["cache_control"]; has {
		t.Error("only the last system block should carry the breakpoint")
	}
	if cc, _ := sys[1].(map[string]any)["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Error("last system block missing cache_control")
	}
}

func TestCacheBreakpoint_PreservesUntouchedRawBytes(t *testing.T) {
	body := "{\n \"model\" : \"claude-sonnet-4-6\", \"tools\" : [ { \"name\" : \"a\", \"description\":\"<>&\" } ],\n \"messages\" : [ {\"role\":\"user\",\"content\":\"hi\"} ]\n}"
	res := apply(t, body, enabled())
	want := "{\n \"model\" : \"claude-sonnet-4-6\", \"tools\" : [ { \"name\" : \"a\", \"description\":\"<>&\",\"cache_control\":{\"type\":\"ephemeral\"} } ],\n \"messages\" : [ {\"role\":\"user\",\"content\":\"hi\"} ]\n}"
	if string(res.Body) != want {
		t.Fatalf("raw insertion reserialized untouched bytes:\n got %s\nwant %s", res.Body, want)
	}
}

func TestCacheBreakpoint_SystemStringPreservesEscapeSpelling(t *testing.T) {
	body := `{"system":"You are \u003cexact\u003e","messages":[]}`
	res := apply(t, body, enabled())
	if !strings.Contains(string(res.Body), `"text":"You are \u003cexact\u003e"`) {
		t.Fatalf("system string escape spelling changed: %s", res.Body)
	}
}

func TestCacheBreakpoint_DisabledIsPassthrough(t *testing.T) {
	body := `{"tools":[{"name":"a"}],"messages":[]}`
	res := apply(t, body, providers.TransformPolicy{RuntimeMode: "active", Optimizers: map[string]bool{}})
	if len(res.OptimizerIDs) != 0 {
		t.Errorf("disabled optimizer should not apply, got %v", res.OptimizerIDs)
	}
	if string(res.Body) != body {
		t.Errorf("disabled optimizer must pass body through unchanged")
	}
}

func TestCacheBreakpoint_NonPAYGIsPassthrough(t *testing.T) {
	body := `{"system":"stable prefix","messages":[{"role":"user","content":"hi"}]}`
	policy := enabled()
	policy.AuthMode = "subscription"
	res := apply(t, body, policy)
	if len(res.OptimizerIDs) != 0 {
		t.Fatalf("subscription must not apply cache breakpoint, got %v", res.OptimizerIDs)
	}
	if string(res.Body) != body {
		t.Fatalf("subscription cache-breakpoint gate must preserve bytes:\n got %s\nwant %s", res.Body, body)
	}
}

func TestCacheBreakpoint_IdempotentAndRespectsExisting(t *testing.T) {
	// Caller already manages caching -> passthrough, no double-claim.
	body := `{"tools":[{"name":"a","cache_control":{"type":"ephemeral"}}],"messages":[]}`
	res := apply(t, body, enabled())
	if len(res.OptimizerIDs) != 0 || string(res.Body) != body {
		t.Errorf("existing cache_control must be respected (passthrough), got ids=%v", res.OptimizerIDs)
	}

	// Applying our own output again is a no-op (idempotent).
	once := apply(t, `{"tools":[{"name":"a"}],"messages":[]}`, enabled())
	twice := apply(t, string(once.Body), enabled())
	if len(twice.OptimizerIDs) != 0 {
		t.Errorf("re-applying should be idempotent, got %v", twice.OptimizerIDs)
	}
}

func TestCacheBreakpoint_UnicodeEscapedCallerKeyCannotMintAttribution(t *testing.T) {
	body := `{"tools":[{"name":"a","cache\u005fcontrol":{"type":"ephemeral"}}],"messages":[]}`
	res := apply(t, body, enabled())
	if len(res.OptimizerIDs) != 0 || string(res.Body) != body {
		t.Fatalf("decoded caller cache_control must remain pass-through, got ids=%v body=%s", res.OptimizerIDs, res.Body)
	}
}

func TestCacheBreakpoint_NoStablePrefixOrBadJSON(t *testing.T) {
	// No tools, no system -> nothing to cache.
	res := apply(t, `{"messages":[{"role":"user","content":"hi"}]}`, enabled())
	if len(res.OptimizerIDs) != 0 {
		t.Errorf("no stable prefix should be passthrough, got %v", res.OptimizerIDs)
	}
	// Invalid JSON -> passthrough, no error.
	bad := apply(t, `not json`, enabled())
	if len(bad.OptimizerIDs) != 0 || string(bad.Body) != "not json" {
		t.Errorf("invalid JSON must pass through unchanged")
	}
}
