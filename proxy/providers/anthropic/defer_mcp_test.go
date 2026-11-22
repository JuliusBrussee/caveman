package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func makeDeferTestPayload(mcpCount int, withCallerCC bool, withSearchTool bool) []byte {
	tools := []map[string]any{}
	if withSearchTool {
		tools = append(tools, map[string]any{
			"type": "tool_search_tool_bm25_20251119",
			"name": "tool_search_tool_bm25",
		})
	}
	tools = append(tools,
		map[string]any{
			"name":         "Bash",
			"description":  "Run shell command",
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{"cmd": map[string]any{"type": "string"}}},
		},
		map[string]any{
			"name":         "mcp__caveman__caveman_retrieve",
			"description":  "Recover CCR handle",
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{"recovery_handle": map[string]any{"type": "string"}}},
		},
	)
	for i := 0; i < mcpCount; i++ {
		t := map[string]any{
			"name":         fmt.Sprintf("mcp__github__tool_%02d", i),
			"description":  "GitHub MCP tool",
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}},
		}
		if withCallerCC && i == mcpCount-1 {
			t["cache_control"] = map[string]any{"type": "ephemeral"}
		}
		tools = append(tools, t)
	}
	body, _ := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-6",
		"max_tokens": 1024,
		"system":     "You are a coding assistant.",
		"tools":      tools,
		"messages":   []map[string]any{{"role": "user", "content": "check prs"}},
	})
	return body
}

func TestDeferMCPToolsDefersNonCoreAndPrependsSearchTool(t *testing.T) {
	adapter := New("https://api.anthropic.com")
	raw := makeDeferTestPayload(6, false, false)
	policy := providers.TransformPolicy{
		RuntimeMode: "compress",
		AuthMode:    "payg",
		Optimizers: map[string]bool{
			DeferMCPToolsOptimizerID: true,
			OptimizerID:              true,
		},
	}
	meta := providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"}
	res, err := adapter.ApplyProviderNativeTransforms(context.Background(), bytes.NewReader(raw), meta, policy)
	if err != nil {
		t.Fatalf("ApplyProviderNativeTransforms: %v", err)
	}
	if len(res.OptimizerIDs) != 2 || res.OptimizerIDs[0] != DeferMCPToolsOptimizerID || res.OptimizerIDs[1] != OptimizerID {
		t.Fatalf("OptimizerIDs=%v want [%s %s]", res.OptimizerIDs, DeferMCPToolsOptimizerID, OptimizerID)
	}

	var doc struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(res.Body, &doc); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	// First tool must be the prepended BM25 search tool.
	if doc.Tools[0]["type"] != anthropicToolSearchToolType {
		t.Fatalf("first tool=%v want %s", doc.Tools[0], anthropicToolSearchToolType)
	}
	deferredCount := 0
	for _, tool := range doc.Tools {
		name, _ := tool["name"].(string)
		isDef, _ := tool["defer_loading"].(bool)
		if name == "Bash" || name == "mcp__caveman__caveman_retrieve" || name == anthropicToolSearchToolName {
			if isDef {
				t.Fatalf("core/recovery/search tool %q must NEVER be deferred", name)
			}
		}
		if isDef {
			deferredCount++
			if _, hasCC := tool["cache_control"]; hasCC {
				t.Fatalf("deferred tool %q must NOT carry cache_control", name)
			}
		}
	}
	if deferredCount != 6 {
		t.Fatalf("deferredCount=%d want 6", deferredCount)
	}

	// Determinism: identical input must produce byte-identical output.
	res2, _ := adapter.ApplyProviderNativeTransforms(context.Background(), bytes.NewReader(raw), meta, policy)
	if !bytes.Equal(res.Body, res2.Body) {
		t.Fatalf("defer_loading output is not deterministic")
	}
}

func TestDeferMCPToolsRespectsGuards(t *testing.T) {
	adapter := New("https://api.anthropic.com")
	metaMessages := providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"}
	metaCount := providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages/count_tokens"}

	// 1. Default off (optimizer not enabled) -> untouched.
	raw := makeDeferTestPayload(8, false, false)
	res, _ := adapter.ApplyProviderNativeTransforms(context.Background(), bytes.NewReader(raw), metaMessages, providers.TransformPolicy{
		RuntimeMode: "compress", AuthMode: "payg", Optimizers: map[string]bool{},
	})
	if !bytes.Equal(res.Body, raw) {
		t.Fatalf("must not defer when optimizer is off")
	}

	// 2. Record mode -> untouched.
	res, _ = adapter.ApplyProviderNativeTransforms(context.Background(), bytes.NewReader(raw), metaMessages, providers.TransformPolicy{
		RuntimeMode: "record", AuthMode: "payg", Optimizers: map[string]bool{DeferMCPToolsOptimizerID: true},
	})
	if !bytes.Equal(res.Body, raw) {
		t.Fatalf("must not defer in record mode")
	}

	// 3. Subscription / OAuth -> untouched (PAYG only).
	res, _ = adapter.ApplyProviderNativeTransforms(context.Background(), bytes.NewReader(raw), metaMessages, providers.TransformPolicy{
		RuntimeMode: "compress", AuthMode: "oauth", Optimizers: map[string]bool{DeferMCPToolsOptimizerID: true},
	})
	if !bytes.Equal(res.Body, raw) {
		t.Fatalf("must not defer on oauth")
	}

	// 4. count_tokens -> untouched.
	res, _ = adapter.ApplyProviderNativeTransforms(context.Background(), bytes.NewReader(raw), metaCount, providers.TransformPolicy{
		RuntimeMode: "compress", AuthMode: "payg", Optimizers: map[string]bool{DeferMCPToolsOptimizerID: true},
	})
	if !bytes.Equal(res.Body, raw) {
		t.Fatalf("must not defer on count_tokens")
	}

	// 5. Fewer than 5 deferrable MCP tools -> untouched.
	small := makeDeferTestPayload(4, false, false)
	res, _ = adapter.ApplyProviderNativeTransforms(context.Background(), bytes.NewReader(small), metaMessages, providers.TransformPolicy{
		RuntimeMode: "compress", AuthMode: "payg", Optimizers: map[string]bool{DeferMCPToolsOptimizerID: true},
	})
	if !bytes.Equal(res.Body, small) {
		t.Fatalf("must not defer fewer than 5 MCP tools")
	}

	// 6. Caller cache_control on last MCP tool -> that tool stays non-deferred; other 5 defer; no duplicate search tool if present.
	withCC := makeDeferTestPayload(6, true, true)
	res, _ = adapter.ApplyProviderNativeTransforms(context.Background(), bytes.NewReader(withCC), metaMessages, providers.TransformPolicy{
		RuntimeMode: "compress", AuthMode: "payg", Optimizers: map[string]bool{DeferMCPToolsOptimizerID: true, OptimizerID: true},
	})
	var doc struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(res.Body, &doc); err != nil {
		t.Fatal(err)
	}
	searchCount := 0
	deferredCount := 0
	for _, tool := range doc.Tools {
		if tool["type"] == anthropicToolSearchToolType {
			searchCount++
		}
		if def, _ := tool["defer_loading"].(bool); def {
			deferredCount++
			if _, hasCC := tool["cache_control"]; hasCC {
				t.Fatalf("caller cache_control tool must not be deferred")
			}
		}
	}
	if searchCount != 1 {
		t.Fatalf("searchCount=%d want 1 (must not duplicate existing search tool)", searchCount)
	}
	if deferredCount != 5 {
		t.Fatalf("deferredCount=%d want 5 (last tool has cache_control)", deferredCount)
	}
}

func TestDeferMCPToolsWorksAcrossAnthropicAndVertexAndSkipsUnsupportedProviders(t *testing.T) {
	adapter := New("https://api.anthropic.com")
	raw := makeDeferTestPayload(6, false, false)
	policy := providers.TransformPolicy{
		RuntimeMode: "compress",
		AuthMode:    "payg",
		Optimizers:  map[string]bool{DeferMCPToolsOptimizerID: true},
	}

	// Direct Anthropic and Vertex Claude endpoints support defer_loading + tool_search.
	supported := []providers.RequestMetadata{
		{Provider: "anthropic", Endpoint: "/v1/messages"},
		{Provider: "vertex", Endpoint: "/v1/projects/p/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-6:streamRawPredict"},
		{Provider: "vertex", Endpoint: "/v1/projects/p/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-6:rawPredict"},
	}
	for _, meta := range supported {
		res, err := adapter.ApplyProviderNativeTransforms(context.Background(), bytes.NewReader(raw), meta, policy)
		if err != nil {
			t.Fatalf("%s %s: %v", meta.Provider, meta.Endpoint, err)
		}
		if len(res.OptimizerIDs) != 1 || res.OptimizerIDs[0] != DeferMCPToolsOptimizerID {
			t.Fatalf("%s %s: OptimizerIDs=%v want [%s]", meta.Provider, meta.Endpoint, res.OptimizerIDs, DeferMCPToolsOptimizerID)
		}
	}

	// Bedrock InvokeModel (needs anthropic_beta), arbitrary openai_compatible proxies,
	// Vertex countTokens, and Bedrock Converse must NOT mutate the body.
	for _, blocked := range []providers.RequestMetadata{
		{Provider: "bedrock", Endpoint: "/model/anthropic.claude-sonnet-4-6/invoke-with-response-stream"},
		{Provider: "openai_compatible", Endpoint: "/compat/anthropic-gw/v1/messages"},
		{Provider: "vertex", Endpoint: "/v1/projects/p/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-6:countTokens"},
		{Provider: "bedrock", Endpoint: "/model/anthropic.claude-sonnet-4-6/converse"},
	} {
		res, _ := adapter.ApplyProviderNativeTransforms(context.Background(), bytes.NewReader(raw), blocked, policy)
		if !bytes.Equal(res.Body, raw) {
			t.Fatalf("%s %s must remain untouched", blocked.Provider, blocked.Endpoint)
		}
	}

	// Haiku / older models and restrictive tool_choice must NOT defer.
	haikuBody := bytes.ReplaceAll(raw, []byte("claude-sonnet-4-6"), []byte("claude-haiku-4-5-20251001"))
	if res, _ := adapter.ApplyProviderNativeTransforms(context.Background(), bytes.NewReader(haikuBody), providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"}, policy); !bytes.Equal(res.Body, haikuBody) {
		t.Fatalf("Haiku model must not receive defer_loading")
	}
	noneChoice := bytes.Replace(raw, []byte(`"max_tokens":1024`), []byte(`"max_tokens":1024,"tool_choice":{"type":"none"}`), 1)
	if res, _ := adapter.ApplyProviderNativeTransforms(context.Background(), bytes.NewReader(noneChoice), providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"}, policy); !bytes.Equal(res.Body, noneChoice) {
		t.Fatalf("tool_choice=none must not defer tools")
	}
	forcedChoice := bytes.Replace(raw, []byte(`"max_tokens":1024`), []byte(`"max_tokens":1024,"tool_choice":{"type":"tool","name":"mcp__github__tool_00"}`), 1)
	resForced, _ := adapter.ApplyProviderNativeTransforms(context.Background(), bytes.NewReader(forcedChoice), providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"}, policy)
	var forcedDoc struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(resForced.Body, &forcedDoc); err != nil {
		t.Fatalf("forcedChoice invalid json: %v", err)
	}
	for _, tool := range forcedDoc.Tools {
		if tool["name"] == "mcp__github__tool_00" {
			if def, _ := tool["defer_loading"].(bool); def {
				t.Fatalf("tool named in tool_choice must NOT be deferred: %s", resForced.Body)
			}
		}
	}
}
