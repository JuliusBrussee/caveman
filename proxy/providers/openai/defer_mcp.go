package openai

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

// DeferMCPToolsOptimizerID enables opt-in OpenAI Responses API native Tool
// Search (`defer_loading: true` + `{"type":"tool_search"}`) on non-core
// `mcp__*` function tools for PAYG `/v1/responses` and Azure Foundry
// `/openai/v1/responses` requests. Default OFF; enable via
// `optimizers.openai-defer-mcp-tools: true` or `CAVEMAN_DEFER_MCP_TOOLS=1`.
const DeferMCPToolsOptimizerID = "openai-defer-mcp-tools"

const deferMCPToolsMinCount = 5

func deferMCPToolsResponsesEndpoint(provider, endpoint string) bool {
	if provider != "" && provider != "openai" && provider != "azure_openai" {
		return false
	}
	ep := strings.ToLower(strings.TrimSpace(endpoint))
	if ep == "" || strings.Contains(ep, "/chatgpt/") || strings.Contains(ep, "/input_tokens") || strings.Contains(ep, "/compat/") {
		return false
	}
	return ep == "/v1/responses" || ep == "/openai/v1/responses" || ep == "/openai/responses" ||
		strings.HasSuffix(ep, "/v1/responses") || strings.HasSuffix(ep, "/openai/responses")
}

// supportsOpenAIToolSearch reports whether the OpenAI Responses model supports
// native `{"type":"tool_search"}` + `defer_loading: true` (gpt-5.4+ and gpt-6+,
// excluding non-reasoning chat-latest aliases).
func supportsOpenAIToolSearch(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return true // Azure deployment alias or unit test without model field
	}
	if strings.Contains(m, "chat-latest") {
		return false
	}
	if strings.HasPrefix(m, "gpt-6") {
		return true
	}
	if strings.HasPrefix(m, "gpt-5.") {
		rest := m[len("gpt-5."):]
		if len(rest) > 0 && rest[0] >= '4' && rest[0] <= '9' {
			return true
		}
	}
	return false
}

func forcedOpenAIToolChoice(decoded map[string]any) (toolName string, disableDefer bool) {
	if s, ok := decoded["tool_choice"].(string); ok {
		return "", s == "none"
	}
	tc, ok := decoded["tool_choice"].(map[string]any)
	if !ok {
		return "", false
	}
	if name, ok := tc["name"].(string); ok && name != "" {
		return name, false
	}
	if fn, ok := tc["function"].(map[string]any); ok {
		if name, ok := fn["name"].(string); ok && name != "" {
			return name, false
		}
	}
	return "", false
}

func applyDeferMCPTools(data []byte, decoded map[string]any) ([]byte, bool) {
	if model, _ := decoded["model"].(string); !supportsOpenAIToolSearch(model) {
		return nil, false
	}
	forcedTool, disableDefer := forcedOpenAIToolChoice(decoded)
	if disableDefer {
		return nil, false
	}
	root, ok := jsonsplice.Root(data)
	if !ok {
		return nil, false
	}
	toolsSpan, found := jsonsplice.Field(data, root, "tools")
	if !found || toolsSpan.Start >= toolsSpan.End || data[toolsSpan.Start] != '[' {
		return nil, false
	}
	elements, valid := jsonsplice.Elements(data, toolsSpan)
	if !valid || len(elements) < deferMCPToolsMinCount {
		return nil, false
	}
	decodedTools, ok := decoded["tools"].([]any)
	if !ok || len(decodedTools) != len(elements) {
		return nil, false
	}

	hasSearchTool := false
	deferIndices := make([]int, 0, len(decodedTools))
	for i, rawTool := range decodedTools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := tool["type"].(string)
		if typ == "tool_search" || strings.HasPrefix(typ, "tool_search_") {
			hasSearchTool = true
			continue
		}
		if typ != "function" {
			continue
		}
		name, _ := tool["name"].(string)
		if !strings.HasPrefix(name, "mcp__") {
			continue
		}
		if forcedTool != "" && name == forcedTool {
			continue
		}
		if providers.IsRecoveryToolName(name) {
			continue
		}
		if _, already := tool["defer_loading"]; already {
			continue
		}
		if elements[i].Start >= elements[i].End || data[elements[i].Start] != '{' {
			continue
		}
		deferIndices = append(deferIndices, i)
	}
	if len(deferIndices) < deferMCPToolsMinCount {
		return nil, false
	}

	out := data
	trueLit := []byte("true")
	for k := len(deferIndices) - 1; k >= 0; k-- {
		idx := deferIndices[k]
		var err error
		out, err = jsonsplice.AppendObjectFields(out, elements[idx],
			jsonsplice.FieldInsertion{Name: "defer_loading", Value: trueLit},
		)
		if err != nil {
			return nil, false
		}
	}

	if !hasSearchTool {
		insertPos := toolsSpan.Start + 1
		searchToolJSON := []byte(`{"type":"tool_search"},`)
		out = slices.Concat(out[:insertPos], searchToolJSON, out[insertPos:])
	}

	if !json.Valid(out) {
		return nil, false
	}
	return out, true
}
