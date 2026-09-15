package anthropic

import (
	"context"
	"encoding/json"
	"io"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

// OptimizerID is the policy flag and x-cave-optimization label for this optimizer.
const OptimizerID = "anthropic-cache-breakpoints"

// ApplyProviderNativeTransforms applies at most one enabled Anthropic caching
// strategy. OptimizerID places an explicit breakpoint on the last non-deferred
// tool (or last system block). AutomaticPromptCacheOptimizerID adds Anthropic's
// top-level automatic 5-minute marker to a direct Messages request.
//
// It is conservative and idempotent: it runs only when policy enables one
// strategy (gateway callers own the request-wide runtime-mode gate), it never
// touches a request that already carries any cache_control (the caller is
// managing caching), and on any parse problem it passes the body through
// unchanged. The response stream to the client is unaffected — only the
// upstream request prefix is annotated.
func (a Adapter) ApplyProviderNativeTransforms(ctx context.Context, body providers.BodyReader, meta providers.RequestMetadata, policy providers.TransformPolicy) (providers.TransformResult, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return providers.TransformResult{Body: data, OptimizerIDs: []string{}}, nil
	}
	passthrough := providers.TransformResult{Body: data, OptimizerIDs: []string{}}
	// Record mode is an unconditional wire-preservation boundary. Both gateways
	// normally skip this adapter in record mode, but the shared adapter must be
	// safe when called directly (or by a future caller) too.
	if policy.RuntimeMode == "record" {
		return passthrough, nil
	}

	explicitEnabled := policy.OptimizerEnabled(OptimizerID)
	automaticEnabled := policy.OptimizerEnabled(AutomaticPromptCacheOptimizerID)
	if !explicitEnabled && !automaticEnabled {
		return passthrough, nil
	}
	// The provider counts an automatic marker toward the four-breakpoint limit.
	// Do not combine Caveman strategies because precedence depends on caller
	// breakpoints and TTLs that the adapter cannot safely infer.
	if explicitEnabled && automaticEnabled {
		return passthrough, nil
	}
	if policy.AuthMode != "" && policy.AuthMode != "payg" {
		return passthrough, nil
	}
	if automaticEnabled && !automaticPromptCacheMessagesEndpoint(meta) {
		return passthrough, nil
	}
	var root map[string]any
	if json.Unmarshal(data, &root) != nil {
		return passthrough, nil
	}
	// Respect caller-managed cache breakpoints after JSON decoding. Raw byte
	// search is insufficient because a valid key may use a Unicode escape such
	// as cache\u005fcontrol; attributing that caller cache to this optimizer
	// would mint false verified savings.
	if containsJSONKey(root, "cache_control") {
		return passthrough, nil
	}

	if automaticEnabled {
		out, ok := injectAutomaticPromptCacheRaw(data)
		if !ok || !json.Valid(out) {
			return passthrough, nil
		}
		return providers.TransformResult{Body: out, OptimizerIDs: []string{AutomaticPromptCacheOptimizerID}}, nil
	}

	out, ok := injectBreakpointRaw(data, root)
	if !ok {
		return passthrough, nil
	}
	if !json.Valid(out) {
		return passthrough, nil
	}
	return providers.TransformResult{Body: out, OptimizerIDs: []string{OptimizerID}}, nil
}

func injectBreakpointRaw(data []byte, decoded map[string]any) ([]byte, bool) {
	root, ok := jsonsplice.Root(data)
	if !ok {
		return nil, false
	}
	cacheControl := []byte(`{"type":"ephemeral"}`)

	if tools, found := jsonsplice.Field(data, root, "tools"); found {
		if elements, valid := jsonsplice.Elements(data, tools); valid {
			decodedTools, _ := decoded["tools"].([]any)
			for i := len(elements) - 1; i >= 0; i-- {
				if i < len(decodedTools) && deferredTool(decodedTools[i]) {
					continue
				}
				element := elements[i]
				if element.Start < element.End && data[element.Start] == '{' {
					out, err := jsonsplice.AppendObjectFields(data, element,
						jsonsplice.FieldInsertion{Name: "cache_control", Value: cacheControl},
					)
					return out, err == nil
				}
			}
		}
	}

	system, found := jsonsplice.Field(data, root, "system")
	if !found || system.Start >= system.End {
		return nil, false
	}
	switch data[system.Start] {
	case '"':
		replacement := make([]byte, 0, system.End-system.Start+72)
		replacement = append(replacement, []byte(`[{"type":"text","text":`)...)
		replacement = append(replacement, data[system.Start:system.End]...)
		replacement = append(replacement, []byte(`,"cache_control":{"type":"ephemeral"}}]`)...)
		out, err := jsonsplice.ReplaceRaw(data, system, replacement)
		return out, err == nil
	case '[':
		elements, valid := jsonsplice.Elements(data, system)
		if !valid {
			return nil, false
		}
		for i := len(elements) - 1; i >= 0; i-- {
			element := elements[i]
			if element.Start < element.End && data[element.Start] == '{' {
				out, err := jsonsplice.AppendObjectFields(data, element,
					jsonsplice.FieldInsertion{Name: "cache_control", Value: cacheControl},
				)
				return out, err == nil
			}
		}
	}
	return nil, false
}

func containsJSONKey(value any, key string) bool {
	switch node := value.(type) {
	case map[string]any:
		for k, child := range node {
			if k == key || containsJSONKey(child, key) {
				return true
			}
		}
	case []any:
		for _, child := range node {
			if containsJSONKey(child, key) {
				return true
			}
		}
	}
	return false
}

func deferredTool(value any) bool {
	tool, ok := value.(map[string]any)
	if !ok {
		return false
	}
	deferred, _ := tool["defer_loading"].(bool)
	return deferred
}

// injectBreakpoint mutates root in place, placing one ephemeral cache_control on
// the stable prefix. Returns true if a breakpoint was placed.
func injectBreakpoint(root map[string]any) bool {
	// Prefer caching tools (they sit before the system+messages and are the most
	// stable, largest reusable prefix). A cache_control on the last tool caches
	// all tools.
	if tools, ok := root["tools"].([]any); ok && len(tools) > 0 {
		for i := len(tools) - 1; i >= 0; i-- {
			if deferredTool(tools[i]) {
				continue
			}
			if tool, ok := tools[i].(map[string]any); ok {
				tool["cache_control"] = ephemeral()
				return true
			}
		}
	}

	// No tools: cache the system prompt.
	switch sys := root["system"].(type) {
	case string:
		if sys == "" {
			return false
		}
		root["system"] = []any{map[string]any{
			"type":          "text",
			"text":          sys,
			"cache_control": ephemeral(),
		}}
		return true
	case []any:
		for i := len(sys) - 1; i >= 0; i-- {
			if block, ok := sys[i].(map[string]any); ok {
				block["cache_control"] = ephemeral()
				return true
			}
		}
	}
	return false
}

func ephemeral() map[string]any {
	return map[string]any{"type": "ephemeral"}
}
