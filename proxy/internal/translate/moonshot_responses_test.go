package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMoonshotK3ResponsesRequest(t *testing.T) {
	messages := `{"model":"m","max_tokens":4000,"output_config":{"effort":"low"},"tool_choice":{"type":"tool","name":"Read"},"tools":[{"name":"Read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`
	chat := `{"model":"m","max_tokens":4000,"reasoning_effort":"low","tool_choice":{"type":"function","function":{"name":"Read"}},"parallel_tool_calls":false,"tools":[{"type":"function","function":{"name":"Read","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"hi"}]}`
	responses := `{"model":"m","max_output_tokens":4000,"reasoning":{"effort":"low","summary":"auto"},"include":["reasoning.encrypted_content","web_search_call.results"],"tool_choice":{"type":"function","name":"Read"},"parallel_tool_calls":false,"store":false,"previous_response_id":"resp_old","temperature":0.2,"tools":[{"type":"function","name":"Read","parameters":{"type":"object"}}],"input":[{"type":"message","role":"user","content":"hi"},{"type":"reasoning","id":"rs_foreign","summary":[],"encrypted_content":"gAAAAABforeign"}]}`
	for _, tc := range []struct{ effort, want string }{
		{"none", "low"}, {"minimal", "low"}, {"low", "low"},
		{"medium", "high"}, {"high", "high"}, {"xhigh", "high"}, {"max", "max"},
		{"ultra", "max"}, {"persistent", "max"}, {"unknown", ""},
	} {
		t.Run(tc.effort, func(t *testing.T) {
			opts := Options{Model: "kimi-k3", Effort: tc.effort, Dialect: "kimi", Route: "moonshot"}
			for _, request := range []struct{ name, from, body string }{
				{"messages", Messages, messages}, {"chat", Chat, chat}, {"responses", Responses, responses},
			} {
				t.Run(request.name, func(t *testing.T) {
					sent, _ := mustRequest(t, request.from, Responses, request.body, opts)
					wantReasoning := `{"effort":"` + tc.want + `"}`
					if tc.want == "" {
						wantReasoning = "null"
					}
					if got := encode(sent["reasoning"]); got != wantReasoning {
						t.Fatalf("reasoning = %s, want effort %q (body %v)", got, tc.want, sent)
					}
					if sent["model"] != "kimi-k3" || sent["max_output_tokens"] != 4000.0 || sent["tool_choice"] != "auto" {
						t.Fatalf("unfitted Responses body = %v", sent)
					}
					for _, unwanted := range []string{"store", "previous_response_id", "conversation", "background", "parallel_tool_calls", "temperature", "top_p", "service_tier", "metadata", "prompt_cache_retention"} {
						if _, ok := sent[unwanted]; ok {
							t.Fatalf("unsupported %s remains (body %v)", unwanted, sent)
						}
					}
					if got := encode(sent["include"]); request.name == "responses" && got != `["web_search_call.results"]` || request.name != "responses" && got != "null" {
						t.Fatalf("include = %s (body %v)", got, sent)
					}
					if strings.Contains(encode(sent["input"]), "rs_foreign") || strings.Contains(encode(sent["input"]), "gAAAAABforeign") {
						t.Fatalf("foreign encrypted reasoning reached Moonshot: %v", sent["input"])
					}
				})
			}
		})
	}
}

func TestMoonshotResponsesReplayUsesSummary(t *testing.T) {
	envelope := *encodeThinking([]json.RawMessage{json.RawMessage(`{"type":"thinking","thinking":"plan","signature":"` + (Options{Route: "moonshot"}).responsesSignature("blob") + `"}`)})
	input := encode([]any{
		map[string]any{"type": "message", "role": "user", "content": "hi"},
		map[string]any{"type": "reasoning", "id": "rs_own", "summary": []any{map[string]any{"type": "summary_text", "text": "plan"}}, "encrypted_content": envelope},
		map[string]any{"type": "message", "role": "user", "content": "continue"},
	})
	body := `{"model":"m","input":` + input + `}`
	sent, _ := mustRequest(t, Responses, Responses, body, Options{Model: "kimi-k3", Route: "moonshot", Dialect: "kimi"})
	items, ok := sent["input"].([]any)
	if !ok || len(items) != 3 {
		t.Fatalf("input = %v", sent["input"])
	}
	reasoning := items[1].(map[string]any)
	if reasoning["id"] != "rs_own" || reasoning["summary"] == nil {
		t.Fatalf("reasoning replay = %v", reasoning)
	}
	if _, ok := reasoning["encrypted_content"]; ok {
		t.Fatalf("encrypted envelope reached Moonshot: %v", reasoning)
	}
}
