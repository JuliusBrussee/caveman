package anthropic

import (
	"encoding/json"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

// DeferMCPToolsOptimizerID enables opt-in Anthropic native Tool Search
// (`defer_loading: true`) on non-core `mcp__*` tools for PAYG `/v1/messages`
// requests. Default OFF; enable via config `optimizers.anthropic-defer-mcp-tools: true`
// or `CAVEMAN_DEFER_MCP_TOOLS=1`.
const DeferMCPToolsOptimizerID = "anthropic-defer-mcp-tools"

const (
	deferMCPToolsMinCount       = 5
	anthropicToolSearchToolType = "tool_search_tool_bm25_20251119"
	anthropicToolSearchToolName = "tool_search_tool_bm25"
)

// deferMCPToolsEligibleEndpoint reports whether the request targets an
// Anthropic Messages-wire inference endpoint (Anthropic direct, Vertex Claude
// rawPredict/streamRawPredict, Bedrock InvokeModel Claude, or an
// Anthropic-dialect compat gateway) — never count_tokens / countTokens.
func deferMCPToolsEligibleEndpoint(meta providers.RequestMetadata) bool {
	ep := strings.ToLower(strings.TrimSpace(meta.Endpoint))
	if ep == "" {
		return false
	}
	if strings.Contains(ep, "count_tokens") || strings.Contains(ep, "counttokens") || strings.Contains(ep, "count-tokens") {
		return false
	}
	switch meta.Provider {
	case "anthropic":
		return ep == "/v1/messages" || ep == "/anthropic/v1/messages"
	case "vertex":
		return strings.Contains(ep, "/publishers/anthropic/") &&
			(strings.HasSuffix(ep, ":rawpredict") || strings.HasSuffix(ep, ":streamrawpredict"))
	default:
		// Exclude Bedrock (requires anthropic_beta + regex tool_search) and
		// third-party openai_compatible gateways that reject Anthropic server tools.
		return false
	}
}

// supportsAnthropicToolSearch reports whether model supports Anthropic's native
// server-side Tool Search (`defer_loading` + `tool_search_tool_bm25_20251119`):
// Claude Sonnet/Opus 4.5+ and Claude 5+ (never Claude 3.x, 4.0, 4.1, or Haiku).
func supportsAnthropicToolSearch(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return true // Vertex rawPredict carries model in URL path, not JSON body.
	}
	if idx := strings.Index(m, "claude-"); idx >= 0 {
		m = m[idx:]
	}
	if !strings.HasPrefix(m, "claude-") || strings.Contains(m, "haiku") {
		return false
	}
	m = strings.ReplaceAll(m, ".", "-")
	for _, marker := range []string{"-4-5", "-4-6", "-4-7", "-4-8", "-5-", "sonnet-5", "opus-5", "fable", "mythos"} {
		if strings.Contains(m, marker) {
			return true
		}
	}
	return false
}

func forcedAnthropicToolChoice(decoded map[string]any) (toolName string, disableDefer bool) {
	tc, ok := decoded["tool_choice"].(map[string]any)
	if !ok {
		return "", false
	}
	typ, _ := tc["type"].(string)
	if typ == "none" {
		return "", true
	}
	if typ == "tool" {
		name, _ := tc["name"].(string)
		return name, false
	}
	return "", false
}

// ApplyDeferMCPTools marks non-core `mcp__*` tools in an Anthropic Messages
// wire request with `"defer_loading": true` and prepends Anthropic's native
// BM25 tool search tool when absent.
//
// Invariants:
//   - Deterministic pure function of the `tools[]` array: identical tool catalogs
//     produce byte-identical output on every turn (preserves prompt cache prefix).
//   - Never defers core built-in tools (non-`mcp__*`), Caveman recovery tools
//     (`caveman_retrieve`), tools already carrying `defer_loading`, or tools
//     carrying caller `cache_control` (preserving caller breakpoints).
//   - Fires only when at least `deferMCPToolsMinCount` (5) deferrable MCP tools
//     exist so small toolsets avoid search-tool overhead.
//   - Fails closed: any parse/splice issue returns the original bytes unchanged.
func ApplyDeferMCPTools(data []byte, decoded map[string]any) ([]byte, bool) {
	return applyDeferMCPTools(data, decoded)
}

func applyDeferMCPTools(data []byte, decoded map[string]any) ([]byte, bool) {
	if model, _ := decoded["model"].(string); !supportsAnthropicToolSearch(model) {
		return nil, false
	}
	forcedTool, disableDefer := forcedAnthropicToolChoice(decoded)
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
		name, _ := tool["name"].(string)
		if strings.HasPrefix(typ, "tool_search_tool") || strings.HasPrefix(name, "tool_search_tool") ||
			name == "ToolSearch" || name == "MCPSearch" {
			hasSearchTool = true
			continue
		}
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
		if _, hasCC := tool["cache_control"]; hasCC {
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

	// Build replacement slices right-to-left inside the original buffer so every
	// earlier byte offset remains exact without re-parsing JSON.
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
		// Re-locate '[' at toolsSpan.Start (untouched because all edits were strictly > toolsSpan.Start).
		insertPos := toolsSpan.Start + 1
		searchToolJSON := []byte(`{"type":"` + anthropicToolSearchToolType + `","name":"` + anthropicToolSearchToolName + `"},`)
		rebuilt := make([]byte, 0, len(out)+len(searchToolJSON))
		rebuilt = append(rebuilt, out[:insertPos]...)
		rebuilt = append(rebuilt, searchToolJSON...)
		rebuilt = append(rebuilt, out[insertPos:]...)
		out = rebuilt
	}

	if !json.Valid(out) {
		return nil, false
	}
	return out, true
}
