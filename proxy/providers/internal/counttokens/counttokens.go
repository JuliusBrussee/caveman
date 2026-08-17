package counttokens

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

type anthropicRequest struct {
	Model             string          `json:"model"`
	Messages          json.RawMessage `json:"messages"`
	System            json.RawMessage `json:"system,omitempty"`
	Tools             json.RawMessage `json:"tools,omitempty"`
	ToolChoice        json.RawMessage `json:"tool_choice,omitempty"`
	CacheControl      json.RawMessage `json:"cache_control,omitempty"`
	ContextManagement json.RawMessage `json:"context_management,omitempty"`
	MCPServers        json.RawMessage `json:"mcp_servers,omitempty"`
	OutputConfig      json.RawMessage `json:"output_config,omitempty"`
	OutputFormat      json.RawMessage `json:"output_format,omitempty"`
	Speed             json.RawMessage `json:"speed,omitempty"`
	Thinking          json.RawMessage `json:"thinking,omitempty"`
}

// These are the fields the provider's count_tokens request currently accepts.
// Keep this list explicit: an unrecognised field may be a newly introduced
// prompt-affecting beta field, and silently dropping it would let a count of a
// different request mint provider-counted savings.
var anthropicCountFields = map[string]struct{}{
	"cache_control":      {},
	"context_management": {},
	"mcp_servers":        {},
	"messages":           {},
	"model":              {},
	"output_config":      {},
	"output_format":      {},
	"speed":              {},
	"system":             {},
	"thinking":           {},
	"tool_choice":        {},
	"tools":              {},
}

// These Create Message fields are generation/transport controls and are not
// part of the provider count_tokens request. They are deliberately stripped,
// preserving the existing count projection contract. All other fields fail
// closed above rather than being guessed as generation-only.
var anthropicGenerationOnlyFields = map[string]struct{}{
	"max_tokens":     {},
	"stop_sequences": {},
	"stream":         {},
	"temperature":    {},
	"top_k":          {},
	"top_p":          {},
}

// ProjectAnthropic projects a Messages request onto the provider's current
// count_tokens input surface. Every proven count field is copied as its raw
// JSON value; only documented generation-only controls disappear. Unknown or
// unsupported fields return ok=false so the gateway records unmeasured rather
// than minting a count for a partial request.
func ProjectAnthropic(original []byte) ([]byte, bool) {
	fields, ok := decodeJSONObjectFields(original)
	if !ok {
		return nil, false
	}
	var model string
	if err := json.Unmarshal(fields["model"], &model); err != nil || strings.TrimSpace(model) == "" {
		return nil, false
	}
	messages := fields["messages"]
	if !jsonArray(messages) {
		return nil, false
	}
	for key := range fields {
		if _, ok := anthropicCountFields[key]; ok {
			continue
		}
		if _, ok := anthropicGenerationOnlyFields[key]; ok {
			continue
		}
		return nil, false
	}
	for key, shape := range map[string]func(json.RawMessage) bool{
		"cache_control":      jsonObject,
		"context_management": jsonObject,
		"mcp_servers":        jsonArray,
		"output_config":      jsonObject,
		"output_format":      jsonObject,
		"speed":              jsonString,
		"system":             jsonStringOrArray,
		"thinking":           jsonObject,
		"tool_choice":        jsonObject,
		"tools":              jsonArray,
	} {
		if raw, present := fields[key]; present && !shape(raw) {
			return nil, false
		}
	}
	projected, err := json.Marshal(anthropicRequest{
		Model:             model,
		Messages:          messages,
		System:            fields["system"],
		Tools:             fields["tools"],
		ToolChoice:        fields["tool_choice"],
		CacheControl:      fields["cache_control"],
		ContextManagement: fields["context_management"],
		MCPServers:        fields["mcp_servers"],
		OutputConfig:      fields["output_config"],
		OutputFormat:      fields["output_format"],
		Speed:             fields["speed"],
		Thinking:          fields["thinking"],
	})
	if err != nil {
		return nil, false
	}
	return projected, true
}

// ProjectGemini wraps the complete GenerateContentRequest so system
// instructions and function declarations are counted with the prompt.
func ProjectGemini(original []byte) ([]byte, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(original, &fields); err != nil || !jsonArray(fields["contents"]) {
		return nil, false
	}
	projected, err := json.Marshal(struct {
		GenerateContentRequest json.RawMessage `json:"generateContentRequest"`
	}{GenerateContentRequest: json.RawMessage(original)})
	if err != nil {
		return nil, false
	}
	return projected, true
}

func ParseNonNegativeInt(response []byte, field string) (int, bool) {
	// Provider count responses are evidence, not a best-effort decode. A map
	// unmarshal would silently keep the last duplicate member, while different
	// JSON implementations are allowed to keep the first, reject the object,
	// or expose every value. Keep the response usable only when its top-level
	// object has unique names and contains exactly one complete JSON value.
	fields, ok := decodeJSONObjectFields(response)
	if !ok {
		return 0, false
	}
	raw, exists := fields[field]
	if !exists {
		return 0, false
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] < '0' || trimmed[0] > '9' {
		return 0, false
	}
	var n int64
	if err := json.Unmarshal(trimmed, &n); err != nil || uint64(n) > uint64(^uint(0)>>1) {
		return 0, false
	}
	return int(n), true
}

func jsonArray(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) >= 2 && trimmed[0] == '[' && trimmed[len(trimmed)-1] == ']'
}

func jsonObject(raw json.RawMessage) bool {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	return value != nil
}

func jsonString(raw json.RawMessage) bool {
	var value string
	return json.Unmarshal(raw, &value) == nil
}

func jsonStringOrArray(raw json.RawMessage) bool {
	return jsonString(raw) || jsonArray(raw)
}

// decodeJSONObjectFields preserves each top-level value while rejecting
// duplicate keys. json.Unmarshal into a map would silently keep only the last
// duplicate, but provider duplicate-key handling is not part of our evidence
// contract: counting a collapsed projection could differ from the served body.
func decodeJSONObjectFields(raw []byte) (map[string]json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil {
		return nil, false
	}
	if delimiter, ok := first.(json.Delim); !ok || delimiter != '{' {
		return nil, false
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, false
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, false
		}
		if _, exists := fields[key]; exists {
			return nil, false
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, false
		}
		fields[key] = value
	}
	last, err := decoder.Token()
	if err != nil {
		return nil, false
	}
	if delimiter, ok := last.(json.Delim); !ok || delimiter != '}' {
		return nil, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	return fields, true
}
