package counttokens

import (
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"testing"
)

func TestProjectAnthropicKeepsCountedFieldsAndDropsGenerationFields(t *testing.T) {
	original := []byte(`{
		"model":"claude-sonnet-4-20250514",
		"messages":[{"role":"user","content":"hello"}],
		"system":"be concise",
		"tools":[{"name":"lookup"}],
		"tool_choice":{"type":"auto"},
		"max_tokens":100,
		"stop_sequences":["done"],
		"temperature":0.5,
		"stream":true,
		"top_k":16,
		"top_p":0.9
	}`)

	projected, ok := ProjectAnthropic(original)
	if !ok {
		t.Fatal("ProjectAnthropic() rejected valid request")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(projected, &fields); err != nil {
		t.Fatalf("projected JSON invalid: %v", err)
	}
	for _, key := range []string{"model", "messages", "system", "tools", "tool_choice"} {
		if _, exists := fields[key]; !exists {
			t.Errorf("projected request missing %q", key)
		}
	}
	for key := range anthropicGenerationOnlyFields {
		if _, exists := fields[key]; exists {
			t.Errorf("generation-only field %q leaked into count request", key)
		}
	}
}

func TestProjectAnthropicKeepsEverySupportedCountField(t *testing.T) {
	original := []byte(`{
		"model":"claude-sonnet-4-6",
		"messages":[{"role":"user","content":"hello"}],
		"system":[{"type":"text","text":"system","cache_control":{"type":"ephemeral"}}],
		"tools":[{"name":"lookup","input_schema":{"type":"object"}}],
		"tool_choice":{"type":"tool","name":"lookup"},
		"cache_control":{"type":"ephemeral","ttl":"1h"},
		"context_management":{"edits":[{"type":"compact_20260112","trigger":{"type":"input_tokens","value":1000}}]},
		"mcp_servers":[{"type":"url","name":"docs","url":"https://example.test/mcp","authorization_token":"secret"}],
		"output_config":{"format":{"type":"json_schema","schema":{"type":"object"}},"effort":"high"},
		"output_format":{"type":"json_schema","schema":{"type":"object"}},
		"speed":"fast",
		"thinking":{"type":"enabled","budget_tokens":2048}
	}`)
	projected, ok := ProjectAnthropic(original)
	if !ok {
		t.Fatal("ProjectAnthropic() rejected a request containing supported count fields")
	}
	var got, want map[string]json.RawMessage
	if err := json.Unmarshal(projected, &got); err != nil {
		t.Fatalf("projected JSON invalid: %v", err)
	}
	if err := json.Unmarshal(original, &want); err != nil {
		t.Fatal(err)
	}
	for key := range anthropicCountFields {
		if !reflect.DeepEqual(got[key], want[key]) {
			t.Errorf("projected %q = %s, want original %s", key, got[key], want[key])
		}
	}
	if len(got) != len(anthropicCountFields) {
		t.Fatalf("projected field count=%d, want %d", len(got), len(anthropicCountFields))
	}
}

func TestProjectAnthropicRejectsUnsupportedFieldsAndShapes(t *testing.T) {
	base := `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`
	tests := map[string]string{
		"unknown prompt field":     `{"future_prompt_field":{"tokens":1}}`,
		"unsupported container":    `{"container":{"id":"container_1"}}`,
		"unsupported metadata":     `{"metadata":{"user_id":"u"}}`,
		"mcp servers object":       `{"mcp_servers":{"name":"docs"}}`,
		"context management array": `{"context_management":[]}`,
		"output config string":     `{"output_config":"json"}`,
		"thinking null":            `{"thinking":null}`,
		"tool choice array":        `{"tool_choice":[]}`,
		"tools object":             `{"tools":{"name":"lookup"}}`,
		"system number":            `{"system":1}`,
		"speed object":             `{"speed":{"mode":"fast"}}`,
		"output format null":       `{"output_format":null}`,
	}
	for name, extra := range tests {
		t.Run(name, func(t *testing.T) {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(extra), &fields); err != nil {
				t.Fatal(err)
			}
			var merged map[string]json.RawMessage
			if err := json.Unmarshal([]byte(base), &merged); err != nil {
				t.Fatal(err)
			}
			for key, value := range fields {
				merged[key] = value
			}
			input, err := json.Marshal(merged)
			if err != nil {
				t.Fatal(err)
			}
			if body, ok := ProjectAnthropic(input); ok || body != nil {
				t.Fatalf("ProjectAnthropic() = (%s, %v), want (nil, false)", body, ok)
			}
		})
	}
}

func TestProjectAnthropicRejectsDuplicateTopLevelFields(t *testing.T) {
	for _, input := range []string{
		`{"model":"claude-sonnet-4-6","model":"claude-opus-4-6","messages":[]}`,
		`{"model":"claude-sonnet-4-6","messages":[],"messages":[]}`,
		`{"model":"claude-sonnet-4-6","messages":[],"context_management":{},"context_management":null}`,
	} {
		if body, ok := ProjectAnthropic([]byte(input)); ok || body != nil {
			t.Fatalf("ProjectAnthropic(%s) = (%s, %v), want (nil, false)", input, body, ok)
		}
	}
}

func TestProjectAnthropicFailsClosed(t *testing.T) {
	tests := map[string]string{
		"invalid JSON":     `{`,
		"missing model":    `{"messages":[]}`,
		"blank model":      `{"model":"  ","messages":[]}`,
		"missing messages": `{"model":"m"}`,
		"object messages":  `{"model":"m","messages":{}}`,
		"null messages":    `{"model":"m","messages":null}`,
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if body, ok := ProjectAnthropic([]byte(input)); ok || body != nil {
				t.Fatalf("ProjectAnthropic() = (%s, %v), want (nil, false)", body, ok)
			}
		})
	}
}

func TestProjectGeminiWrapsCompleteRequest(t *testing.T) {
	original := []byte(`{"contents":[],"systemInstruction":{"parts":[{"text":"safe"}]},"tools":[{"functionDeclarations":[]}]}`)
	projected, ok := ProjectGemini(original)
	if !ok {
		t.Fatal("ProjectGemini() rejected valid request")
	}
	var wrapper struct {
		GenerateContentRequest json.RawMessage `json:"generateContentRequest"`
	}
	if err := json.Unmarshal(projected, &wrapper); err != nil {
		t.Fatalf("projected JSON invalid: %v", err)
	}
	var got, want any
	if err := json.Unmarshal(wrapper.GenerateContentRequest, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(original, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wrapped request = %#v, want %#v", got, want)
	}
}

func TestProjectGeminiFailsClosed(t *testing.T) {
	for _, input := range []string{`{`, `{}`, `{"contents":null}`, `{"contents":{}}`} {
		if body, ok := ProjectGemini([]byte(input)); ok || body != nil {
			t.Fatalf("ProjectGemini(%q) = (%s, %v), want (nil, false)", input, body, ok)
		}
	}
}

func TestParseNonNegativeInt(t *testing.T) {
	maxInt := strconv.FormatUint(uint64(math.MaxInt), 10)
	tests := []struct {
		name  string
		body  string
		field string
		want  int
		ok    bool
	}{
		{name: "zero", body: `{"count":0}`, field: "count", want: 0, ok: true},
		{name: "positive", body: `{"count":42}`, field: "count", want: 42, ok: true},
		{name: "max int", body: `{"count":` + maxInt + `}`, field: "count", want: math.MaxInt, ok: true},
		{name: "missing", body: `{}`, field: "count"},
		{name: "negative", body: `{"count":-1}`, field: "count"},
		{name: "decimal", body: `{"count":1.5}`, field: "count"},
		{name: "string", body: `{"count":"1"}`, field: "count"},
		{name: "null", body: `{"count":null}`, field: "count"},
		{name: "overflow", body: `{"count":18446744073709551615}`, field: "count"},
		{name: "duplicate target key", body: `{"count":1,"count":2}`, field: "count"},
		{name: "duplicate unrelated key", body: `{"other":1,"other":2,"count":3}`, field: "count"},
		{name: "trailing JSON value", body: `{"count":1}{}`, field: "count"},
		{name: "trailing non-whitespace", body: `{"count":1}x`, field: "count"},
		{name: "invalid JSON", body: `{`, field: "count"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := ParseNonNegativeInt([]byte(test.body), test.field)
			if got != test.want || ok != test.ok {
				t.Fatalf("ParseNonNegativeInt() = (%d, %v), want (%d, %v)", got, ok, test.want, test.ok)
			}
		})
	}
}
