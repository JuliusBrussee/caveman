package cacheengine

import (
	"encoding/json"

	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

func applyAnthropicStable(body []byte) ([]byte, bool) {
	var decoded map[string]any
	if json.Unmarshal(body, &decoded) != nil {
		return body, false
	}
	root, ok := jsonsplice.Root(body)
	if !ok {
		return body, false
	}
	cacheControl := []byte(`{"type":"ephemeral"}`)
	if tools, found := jsonsplice.Field(body, root, "tools"); found {
		if elements, valid := jsonsplice.Elements(body, tools); valid {
			decodedTools, _ := decoded["tools"].([]any)
			for index := len(elements) - 1; index >= 0; index-- {
				if index < len(decodedTools) && anthropicDeferredTool(decodedTools[index]) {
					continue
				}
				element := elements[index]
				if element.Start < element.End && body[element.Start] == '{' {
					out, err := jsonsplice.AppendObjectFields(body, element, jsonsplice.FieldInsertion{Name: "cache_control", Value: cacheControl})
					return out, err == nil
				}
			}
		}
	}
	system, found := jsonsplice.Field(body, root, "system")
	if !found || system.Start >= system.End {
		return body, false
	}
	switch body[system.Start] {
	case '"':
		replacement := make([]byte, 0, system.End-system.Start+72)
		replacement = append(replacement, []byte(`[{"type":"text","text":`)...)
		replacement = append(replacement, body[system.Start:system.End]...)
		replacement = append(replacement, []byte(`,"cache_control":{"type":"ephemeral"}}]`)...)
		out, err := jsonsplice.ReplaceRaw(body, system, replacement)
		return out, err == nil
	case '[':
		elements, valid := jsonsplice.Elements(body, system)
		if !valid {
			return body, false
		}
		for index := len(elements) - 1; index >= 0; index-- {
			element := elements[index]
			if element.Start < element.End && body[element.Start] == '{' {
				out, err := jsonsplice.AppendObjectFields(body, element, jsonsplice.FieldInsertion{Name: "cache_control", Value: cacheControl})
				return out, err == nil
			}
		}
	}
	return body, false
}

func anthropicDeferredTool(value any) bool {
	tool, ok := value.(map[string]any)
	if !ok {
		return false
	}
	deferred, _ := tool["defer_loading"].(bool)
	return deferred
}

func applyBedrockStable(body []byte, endpoint string) ([]byte, bool) {
	var root map[string]any
	if json.Unmarshal(body, &root) != nil {
		return body, false
	}
	var injected bool
	switch endpoint {
	case "converse", "converse-stream":
		injected = injectBedrockConverseStable(root)
	case "invoke", "invoke-with-response-stream":
		injected = injectBedrockInvokeStable(root)
	default:
		return body, false
	}
	if !injected {
		return body, false
	}
	out, err := json.Marshal(root)
	return out, err == nil
}

func injectBedrockConverseStable(root map[string]any) bool {
	if rawToolConfig, exists := root["toolConfig"]; exists {
		toolConfig, ok := rawToolConfig.(map[string]any)
		if !ok {
			return false
		}
		rawTools, exists := toolConfig["tools"]
		if !exists {
			return false
		}
		tools, ok := rawTools.([]any)
		if !ok || len(tools) == 0 {
			return false
		}
		for _, tool := range tools {
			if _, ok := tool.(map[string]any); !ok {
				return false
			}
		}
		toolConfig["tools"] = append(tools, map[string]any{"cachePoint": map[string]any{"type": "default"}})
		return true
	}
	rawSystem, exists := root["system"]
	if !exists {
		return false
	}
	system, ok := rawSystem.([]any)
	if !ok || len(system) == 0 {
		return false
	}
	for _, block := range system {
		if _, ok := block.(map[string]any); !ok {
			return false
		}
	}
	root["system"] = append(system, map[string]any{"cachePoint": map[string]any{"type": "default"}})
	return true
}

func injectBedrockInvokeStable(root map[string]any) bool {
	if tools, ok := root["tools"].([]any); ok && len(tools) > 0 {
		for index := len(tools) - 1; index >= 0; index-- {
			if tool, ok := tools[index].(map[string]any); ok {
				tool["cache_control"] = map[string]any{"type": "ephemeral"}
				return true
			}
		}
	}
	switch system := root["system"].(type) {
	case string:
		if system == "" {
			return false
		}
		root["system"] = []any{map[string]any{
			"type": "text", "text": system,
			"cache_control": map[string]any{"type": "ephemeral"},
		}}
		return true
	case []any:
		for index := len(system) - 1; index >= 0; index-- {
			if block, ok := system[index].(map[string]any); ok {
				block["cache_control"] = map[string]any{"type": "ephemeral"}
				return true
			}
		}
	}
	return false
}
