package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func makeResponsesDeferPayload(mcpCount int, withSearch bool) []byte {
	tools := []map[string]any{}
	if withSearch {
		tools = append(tools, map[string]any{"type": "tool_search"})
	}
	tools = append(tools,
		map[string]any{
			"type":        "function",
			"name":        "shell",
			"description": "Run a shell command",
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{"cmd": map[string]any{"type": "string"}}},
		},
		map[string]any{
			"type":        "function",
			"name":        "mcp__caveman__caveman_retrieve",
			"description": "Recover CCR handle",
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{"recovery_handle": map[string]any{"type": "string"}}},
		},
	)
	for i := 0; i < mcpCount; i++ {
		tools = append(tools, map[string]any{
			"type":        "function",
			"name":        fmt.Sprintf("mcp__linear__op_%02d", i),
			"description": "Linear MCP operation",
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}},
		})
	}
	raw, _ := json.Marshal(map[string]any{
		"model":        "gpt-6.1-sol",
		"instructions": "You are a coding agent.",
		"tools":        tools,
		"input":        []map[string]any{{"type": "message", "role": "user", "content": "check issues"}},
	})
	return raw
}

func TestOpenAIDeferMCPToolsOnResponsesAndComposesWithCacheKey(t *testing.T) {
	adapter := New("https://api.openai.com")
	raw := makeResponsesDeferPayload(6, false)
	meta := providers.RequestMetadata{Provider: "openai", Endpoint: "/v1/responses"}
	policy := providers.TransformPolicy{
		RuntimeMode: "compress",
		AuthMode:    "payg",
		Optimizers: map[string]bool{
			DeferMCPToolsOptimizerID: true,
			OptimizerID:              true,
		},
	}

	res, err := adapter.ApplyProviderNativeTransforms(context.Background(), bytes.NewReader(raw), meta, policy)
	if err != nil {
		t.Fatalf("ApplyProviderNativeTransforms: %v", err)
	}
	if len(res.OptimizerIDs) != 2 || res.OptimizerIDs[0] != DeferMCPToolsOptimizerID || res.OptimizerIDs[1] != OptimizerID {
		t.Fatalf("OptimizerIDs=%v want [%s %s]", res.OptimizerIDs, DeferMCPToolsOptimizerID, OptimizerID)
	}
	var doc struct {
		PromptCacheKey string           `json:"prompt_cache_key"`
		Tools          []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(res.Body, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.PromptCacheKey == "" {
		t.Fatalf("expected prompt_cache_key to compose with defer_loading")
	}
	if doc.Tools[0]["type"] != "tool_search" {
		t.Fatalf("first tool=%v want tool_search", doc.Tools[0])
	}
	deferred := 0
	for _, tool := range doc.Tools {
		name, _ := tool["name"].(string)
		def, _ := tool["defer_loading"].(bool)
		if name == "shell" || name == "mcp__caveman__caveman_retrieve" {
			if def {
				t.Fatalf("core/recovery tool %q must never be deferred", name)
			}
		}
		if def {
			deferred++
		}
	}
	if deferred != 6 {
		t.Fatalf("deferred=%d want 6", deferred)
	}

	// Deterministic across repeated turns.
	res2, _ := adapter.ApplyProviderNativeTransforms(context.Background(), bytes.NewReader(raw), meta, policy)
	if !bytes.Equal(res.Body, res2.Body) {
		t.Fatalf("defer_loading output is not deterministic")
	}
}

func TestOpenAIDeferMCPToolsRespectsGuards(t *testing.T) {
	adapter := New("https://api.openai.com")
	raw := makeResponsesDeferPayload(6, false)
	policyOn := providers.TransformPolicy{
		RuntimeMode: "compress",
		AuthMode:    "payg",
		Optimizers:  map[string]bool{DeferMCPToolsOptimizerID: true},
	}

	// 1. Chat Completions -> untouched (Responses only).
	res, _ := adapter.ApplyProviderNativeTransforms(context.Background(), bytes.NewReader(raw),
		providers.RequestMetadata{Provider: "openai", Endpoint: "/v1/chat/completions"}, policyOn)
	if !bytes.Equal(res.Body, raw) {
		t.Fatalf("must not defer on /v1/chat/completions")
	}

	// 2. Codex subscription /chatgpt -> untouched.
	res, _ = adapter.ApplyProviderNativeTransforms(context.Background(), bytes.NewReader(raw),
		providers.RequestMetadata{Provider: "openai", Endpoint: "/chatgpt/backend-api/codex/responses"}, policyOn)
	if !bytes.Equal(res.Body, raw) {
		t.Fatalf("must not defer on /chatgpt Codex endpoint")
	}

	// 3. OAuth/subscription auth -> untouched.
	res, _ = adapter.ApplyProviderNativeTransforms(context.Background(), bytes.NewReader(raw),
		providers.RequestMetadata{Provider: "openai", Endpoint: "/v1/responses"},
		providers.TransformPolicy{RuntimeMode: "compress", AuthMode: "subscription", Optimizers: map[string]bool{DeferMCPToolsOptimizerID: true}})
	if !bytes.Equal(res.Body, raw) {
		t.Fatalf("must not defer on subscription auth")
	}

	// 4. Record mode -> untouched.
	res, _ = adapter.ApplyProviderNativeTransforms(context.Background(), bytes.NewReader(raw),
		providers.RequestMetadata{Provider: "openai", Endpoint: "/v1/responses"},
		providers.TransformPolicy{RuntimeMode: "record", AuthMode: "payg", Optimizers: map[string]bool{DeferMCPToolsOptimizerID: true}})
	if !bytes.Equal(res.Body, raw) {
		t.Fatalf("must not defer in record mode")
	}

	// 5. Existing tool_search is not duplicated.
	withSearch := makeResponsesDeferPayload(6, true)
	res, _ = adapter.ApplyProviderNativeTransforms(context.Background(), bytes.NewReader(withSearch),
		providers.RequestMetadata{Provider: "openai", Endpoint: "/azure/openai/v1/responses"}, policyOn)
	var doc struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(res.Body, &doc); err != nil {
		t.Fatal(err)
	}
	searchCount := 0
	for _, tool := range doc.Tools {
		if tool["type"] == "tool_search" {
			searchCount++
		}
	}
	if searchCount != 1 {
		t.Fatalf("searchCount=%d want 1", searchCount)
	}
}
