package pixel

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// These are valid JSON numbers accepted by provider tool input/schema shapes,
// but each loses information if decoded through a float64. The tests compare
// the exact source numbers while a separate, profitable text block is rendered.
func precisionFixture() (map[string]any, map[string]any) {
	values := map[string]any{
		"account_id": json.Number("9007199254740993"),
		"maximum":    json.Number("9223372036854775807"),
		"minimum":    json.Number("-9223372036854775808"),
		"decimal":    json.Number("0.123456789012345678901"),
		"tiny":       json.Number("1e-400"),
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"account_id": map[string]any{
				"type": "integer", "const": values["account_id"],
				"maximum": values["maximum"], "minimum": values["minimum"],
				"enum": []any{values["account_id"], json.Number("9007199254740995")},
			},
			"decimal": map[string]any{"type": "number", "maximum": values["decimal"], "minimum": values["tiny"]},
		},
		"required": []any{"account_id"},
	}
	return values, schema
}

func numberFixtureJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func readNumberFixture(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	// Independent assertion decode: a float64-based assertion would hide the
	// same corruption by rounding both the expected and the actual value.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestTransformAnthropicPreservesExactToolNumbers(t *testing.T) {
	for _, rewriteTools := range []bool{false, true} {
		name := "system only"
		if rewriteTools {
			name = "system and tool descriptions"
		}
		t.Run(name, func(t *testing.T) {
			values, schema := precisionFixture()
			body := numberFixtureJSON(t, map[string]any{
				"model": "claude-fable-5", "system": strings.Repeat("static slab ", 12000),
				"messages": []any{
					map[string]any{"role": "user", "content": "Look up this account."},
					map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "call1", "name": "lookup", "input": values}}},
					map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "call1", "content": "found"}}},
				},
				"tools": []any{map[string]any{"name": "lookup", "description": "Look up an account by its exact numeric ID.", "input_schema": schema}},
			})
			opts := DefaultTransformOptions("claude-fable-5")
			opts.CollapseHistory, opts.CompressToolResults, opts.CompressTools = false, false, rewriteTools
			out, info, err := TransformAnthropic(body, opts)
			if err != nil || !info.Compressed || info.ImageCount == 0 {
				t.Fatalf("fixture did not exercise a successful pixel transform: err=%v info=%+v", err, info)
			}
			got := readNumberFixture(t, out)
			assistant := got["messages"].([]any)[1].(map[string]any)
			input := assistant["content"].([]any)[0].(map[string]any)["input"]
			if !reflect.DeepEqual(input, values) {
				t.Fatalf("untouched tool arguments changed: got %#v, want %#v", input, values)
			}
			gotSchema := got["tools"].([]any)[0].(map[string]any)["input_schema"]
			if !reflect.DeepEqual(gotSchema, schema) {
				t.Fatalf("tool schema numeric constraints changed: got %#v, want %#v", gotSchema, schema)
			}
		})
	}
}

func TestTransformOpenAIPreservesExactToolSchemaNumbers(t *testing.T) {
	for _, responses := range []bool{false, true} {
		name := "chat completions"
		if responses {
			name = "responses"
		}
		t.Run(name, func(t *testing.T) {
			_, schema := precisionFixture()
			tool := map[string]any{"name": "lookup", "description": "Look up an account by its exact numeric ID.", "parameters": schema}
			system := strings.Repeat("static system detail ", 12000)
			request := map[string]any{"model": "gpt-5.6"}
			if responses {
				tool["type"] = "function"
				request["instructions"] = system
				request["input"] = []any{map[string]any{"type": "message", "role": "user", "content": "go"}}
				request["tools"] = []any{tool}
			} else {
				request["messages"] = []any{map[string]any{"role": "system", "content": system}, map[string]any{"role": "user", "content": "go"}}
				request["tools"] = []any{map[string]any{"type": "function", "function": tool}}
			}
			opts := DefaultTransformOptions("gpt-5.6")
			opts.CollapseHistory = false
			out, info, err := TransformOpenAI(numberFixtureJSON(t, request), opts)
			if err != nil || !info.Compressed || info.ImageCount == 0 {
				t.Fatalf("fixture did not exercise a successful pixel transform: err=%v info=%+v", err, info)
			}
			got := readNumberFixture(t, out)
			gotTool := got["tools"].([]any)[0].(map[string]any)
			if !responses {
				gotTool = gotTool["function"].(map[string]any)
			}
			if !reflect.DeepEqual(gotTool["parameters"], schema) {
				t.Fatalf("tool schema numeric constraints changed: got %#v, want %#v", gotTool["parameters"], schema)
			}
		})
	}
}

func TestAnthropicHistoryKeepsExactNumbersInLiveTail(t *testing.T) {
	values, _ := precisionFixture()
	messages := anthropicConvo(15, 3500)
	messages[len(messages)-2] = Message{Role: "assistant", Content: []any{
		map[string]any{"type": "tool_use", "id": "live-call", "name": "lookup", "input": values},
	}}
	messages[len(messages)-1] = Message{Role: "user", Content: []any{
		map[string]any{"type": "tool_result", "tool_use_id": "live-call", "content": "found"},
	}}
	out, info, err := collapseAnthropicHistory(messages, func(string, int) bool { return true }, historyOptions{
		ProtectedPrefix: intPtr(1),
	})
	if err != nil || info.CollapsedTurns == 0 || info.CollapsedImages == 0 {
		t.Fatalf("fixture did not collapse history: err=%v info=%+v", err, info)
	}
	if !reflect.DeepEqual(out[len(out)-4:], messages[len(messages)-4:]) {
		t.Fatal("history collapse changed exact numeric arguments in the uncollapsed live tail")
	}
}
