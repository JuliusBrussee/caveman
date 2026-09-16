package gateway

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestServerRetrieveSupportedOnlyForImplementedWireGrammars(t *testing.T) {
	tests := []struct {
		provider string
		path     string
		want     bool
	}{
		{"openai", "/openai/v1/chat/completions", true},
		{"openai", "/openai/v1/responses", true},
		{"openai_compatible", "/compat/groq/v1/chat/completions", true},
		{"anthropic", "/anthropic/v1/messages", true},
		{"gemini", "/gemini/v1beta/models/gemini-2.5-pro:generateContent", true},
		{"vertex", "/vertex/v1/projects/p/locations/global/publishers/google/models/gemini-2.5-pro:generateContent", true},
		{"bedrock", "/bedrock/model/anthropic.claude-sonnet-4-6/converse", false},
	}
	for _, tc := range tests {
		if got := serverRetrieveSupported(tc.provider, tc.path); got != tc.want {
			t.Errorf("serverRetrieveSupported(%q, %q)=%v, want %v", tc.provider, tc.path, got, tc.want)
		}
	}
}

func TestInjectRetrieveToolStaticOpenAIPreservesMessageBytes(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hello"}]}`)
	out1, ok := injectRetrieveTool("openai", "/v1/chat/completions", body)
	if !ok {
		t.Fatal("injectRetrieveTool ok=false")
	}
	out2, ok := injectRetrieveTool("openai", "/v1/chat/completions", body)
	if !ok {
		t.Fatal("second injectRetrieveTool ok=false")
	}
	if !bytes.Equal(out1, out2) {
		t.Fatalf("static injection must be deterministic:\n%s\n%s", out1, out2)
	}
	if !bytes.Contains(out1, []byte(retrieveToolName)) || bytes.Contains(out1, []byte("ccr_abc")) {
		t.Fatalf("tool must be static and handle-free: %s", out1)
	}
	if !bytes.Contains(out1, []byte(`"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hello"}]`)) {
		t.Fatalf("messages bytes were not preserved: %s", out1)
	}
}

func TestInjectRetrieveToolAppendsToExistingAnthropicTools(t *testing.T) {
	body := []byte(`{"model":"claude","tools":[{"name":"read","description":"existing","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`)
	out, ok := injectRetrieveTool("anthropic", "/v1/messages", body)
	if !ok {
		t.Fatal("injectRetrieveTool ok=false")
	}
	if !bytes.Contains(out, []byte(`{"name":"read","description":"existing","input_schema":{"type":"object"}}`)) {
		t.Fatalf("existing tool bytes changed: %s", out)
	}
	if strings.Count(string(out), retrieveToolName) != 1 {
		t.Fatalf("retrieve tool count = %d, want 1: %s", strings.Count(string(out), retrieveToolName), out)
	}
	if _, ok := injectRetrieveTool("anthropic", "/v1/messages", out); ok {
		t.Fatal("second injection should refuse duplicate caveman_retrieve")
	}
}

func TestParseRetrieveCallReadsHandleAndQuery(t *testing.T) {
	openAI := []byte(`{"choices":[{"message":{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"caveman_retrieve","arguments":"{\"handle\":\"ccr_block\",\"query\":\"postgres\"}"}}]}}]}`)
	id, handle, query, ok := parseRetrieveCall("openai", "/v1/chat/completions", openAI)
	if !ok || id != "call_1" || handle != "ccr_block" || query != "postgres" {
		t.Fatalf("openai parse = id=%q handle=%q query=%q ok=%v", id, handle, query, ok)
	}

	anthropic := []byte(`{"content":[{"type":"tool_use","id":"toolu_1","name":"caveman_retrieve","input":{"handle":"ccr_anthropic","query":"schema"}}]}`)
	id, handle, query, ok = parseRetrieveCall("anthropic", "/v1/messages", anthropic)
	if !ok || id != "toolu_1" || handle != "ccr_anthropic" || query != "schema" {
		t.Fatalf("anthropic parse = id=%q handle=%q query=%q ok=%v", id, handle, query, ok)
	}
}

func TestResponsesRetrieveWireRoundTrip(t *testing.T) {
	reqBody := []byte(`{"model":"gpt-5.5","input":[{"role":"user","content":[{"type":"input_text","text":"compressed <<ccr:ccr_responses>>"}]}]}`)
	injected, ok := injectRetrieveTool("openai", "/v1/responses", reqBody)
	if !ok {
		t.Fatal("injectRetrieveTool ok=false")
	}
	var injectedRoot map[string]any
	if err := json.Unmarshal(injected, &injectedRoot); err != nil {
		t.Fatal(err)
	}
	tools, _ := injectedRoot["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %#v", tools)
	}
	tool, _ := tools[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != retrieveToolName || tool["function"] != nil {
		t.Fatalf("Responses tool must use flat wire shape: %#v", tool)
	}

	respBody := []byte(`{"id":"resp_1","status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[]},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"caveman_retrieve","arguments":"{\"handle\":\"ccr_responses\",\"query\":\"schema\"}"}]}`)
	id, handle, query, ok := parseRetrieveCall("openai", "/v1/responses", respBody)
	if !ok || id != "call_1" || handle != "ccr_responses" || query != "schema" {
		t.Fatalf("parse = id=%q handle=%q query=%q ok=%v", id, handle, query, ok)
	}

	continued, ok := appendRetrieveResult("openai", "/v1/responses", injected, respBody, id, "original bytes")
	if !ok {
		t.Fatal("appendRetrieveResult ok=false")
	}
	var continuedRoot map[string]any
	if err := json.Unmarshal(continued, &continuedRoot); err != nil {
		t.Fatal(err)
	}
	input, _ := continuedRoot["input"].([]any)
	if len(input) != 4 {
		t.Fatalf("continued input = %#v", input)
	}
	result, _ := input[3].(map[string]any)
	if result["type"] != "function_call_output" || result["call_id"] != "call_1" || result["output"] != "original bytes" {
		t.Fatalf("function result = %#v", result)
	}

	cleaned, ok := stripRetrieveCall("openai", "/v1/responses", respBody)
	if !ok || strings.Contains(string(cleaned), retrieveToolName) || !strings.Contains(string(cleaned), `"type":"reasoning"`) {
		t.Fatalf("cleaned Responses body = %s, ok=%v", cleaned, ok)
	}
}

func TestGeminiRetrieveWireRoundTrip(t *testing.T) {
	const route = "/v1beta/models/gemini-2.5-flash:generateContent"
	reqBody := []byte(`{"contents":[{"role":"user","parts":[{"text":"compressed <<ccr:ccr_gemini>>"}]}]}`)
	injected, ok := injectRetrieveTool("gemini", route, reqBody)
	if !ok {
		t.Fatal("injectRetrieveTool ok=false")
	}
	var injectedRoot map[string]any
	if err := json.Unmarshal(injected, &injectedRoot); err != nil {
		t.Fatal(err)
	}
	tools, _ := injectedRoot["tools"].([]any)
	tool, _ := tools[0].(map[string]any)
	declarations, _ := tool["functionDeclarations"].([]any)
	declaration, _ := declarations[0].(map[string]any)
	if len(tools) != 1 || len(declarations) != 1 || declaration["name"] != retrieveToolName {
		t.Fatalf("Gemini tool wire shape = %#v", tools)
	}
	if _, ok := injectRetrieveTool("gemini", route, injected); ok {
		t.Fatal("second injection should reject duplicate nested declaration")
	}

	respBody := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"sig"},{"functionCall":{"id":"call_g","name":"caveman_retrieve","args":{"handle":"ccr_gemini","query":"schema"}}}]},"finishReason":"STOP"}]}`)
	id, handle, query, ok := parseRetrieveCall("gemini", route, respBody)
	if !ok || id != "call_g" || handle != "ccr_gemini" || query != "schema" {
		t.Fatalf("parse = id=%q handle=%q query=%q ok=%v", id, handle, query, ok)
	}

	continued, ok := appendRetrieveResult("gemini", route, injected, respBody, id, "original bytes")
	if !ok {
		t.Fatal("appendRetrieveResult ok=false")
	}
	var continuedRoot map[string]any
	if err := json.Unmarshal(continued, &continuedRoot); err != nil {
		t.Fatal(err)
	}
	contents, _ := continuedRoot["contents"].([]any)
	if len(contents) != 3 {
		t.Fatalf("continued contents = %#v", contents)
	}
	resultContent, _ := contents[2].(map[string]any)
	parts, _ := resultContent["parts"].([]any)
	part, _ := parts[0].(map[string]any)
	result, _ := part["functionResponse"].(map[string]any)
	if result["name"] != retrieveToolName || result["id"] != "call_g" {
		t.Fatalf("function response = %#v", result)
	}

	cleaned, ok := stripRetrieveCall("gemini", route, respBody)
	if !ok || strings.Contains(string(cleaned), retrieveToolName) || !strings.Contains(string(cleaned), `"thoughtSignature":"sig"`) {
		t.Fatalf("cleaned Gemini body = %s, ok=%v", cleaned, ok)
	}
}

func TestRetrieveToolSchemaValid(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(retrieveToolSchema, &schema); err != nil {
		t.Fatalf("schema invalid: %v", err)
	}
	if !strings.Contains(retrieveToolDescription, "<<ccr:") {
		t.Fatalf("description must direct model to in-block markers: %q", retrieveToolDescription)
	}
}

// TestRetrieveRewritesPreserveLargeIntegerLiterals pins the gateway siblings of
// the Bedrock cache-points defect (#1057). Both rewrites in the recovery loop
// decode into map[string]any and re-marshal the whole document, so every
// integer literal above 2^53 anywhere else in the body is silently rounded to
// the nearest float64 on the way out.
//
// The two paths have different blast radii and both matter:
//   - stripRetrieveCall rewrites the RESPONSE handed back to the agent, so a
//     rounded id lands in a sibling tool_use's arguments that the agent then
//     executes.
//   - appendRetrieveResult rewrites the REQUEST re-sent upstream, so the whole
//     conversation history is rewritten on every recovery round trip.
func TestRetrieveRewritesPreserveLargeIntegerLiterals(t *testing.T) {
	const largeInt = "9007199254740993" // 2^53 + 1, not representable as float64

	t.Run("stripRetrieveCall/anthropic", func(t *testing.T) {
		respBody := []byte(`{"id":"msg_1","type":"message","role":"assistant","stop_reason":"tool_use","content":[` +
			`{"type":"tool_use","id":"tu_1","name":"charge_account","input":{"amount_cents":` + largeInt + `}},` +
			`{"type":"tool_use","id":"tu_2","name":"caveman_retrieve","input":{"handle":"ccr_1","query":"schema"}}` +
			`]}`)
		cleaned, ok := stripRetrieveCall("anthropic", "/v1/messages", respBody)
		if !ok {
			t.Fatal("stripRetrieveCall did not strip the retrieve call")
		}
		if strings.Contains(string(cleaned), retrieveToolName) {
			t.Fatalf("retrieve tool survived: %s", cleaned)
		}
		if !bytes.Contains(cleaned, []byte(largeInt)) {
			t.Fatalf("sibling tool_use argument %s was silently rounded: %s", largeInt, cleaned)
		}
	})

	t.Run("stripRetrieveCall/openai", func(t *testing.T) {
		respBody := []byte(`{"id":"cc_1","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[` +
			`{"id":"c1","type":"function","function":{"name":"charge_account","arguments":"{}"}},` +
			`{"id":"c2","type":"function","function":{"name":"caveman_retrieve","arguments":"{}"}}` +
			`]}}],"usage":{"prompt_tokens":10,"request_id_numeric":` + largeInt + `}}`)
		cleaned, ok := stripRetrieveCall("openai", "/v1/chat/completions", respBody)
		if !ok {
			t.Fatal("stripRetrieveCall did not strip the retrieve call")
		}
		if !bytes.Contains(cleaned, []byte(largeInt)) {
			t.Fatalf("response integer %s was silently rounded: %s", largeInt, cleaned)
		}
	})

	t.Run("appendRetrieveResult/anthropic", func(t *testing.T) {
		reqBody := []byte(`{"model":"claude-fable-5","max_tokens":64,"messages":[` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_0","content":"{\"ledger_id\":1}"},` +
			`{"type":"text","text":"go on"}]}],"metadata":{"upstream_event_id":` + largeInt + `}}`)
		respBody := []byte(`{"id":"msg_1","type":"message","role":"assistant","stop_reason":"tool_use","content":[` +
			`{"type":"tool_use","id":"tu_1","name":"caveman_retrieve","input":{"handle":"ccr_1","query":"schema"}}]}`)
		callID, _, _, ok := parseRetrieveCall("anthropic", "/v1/messages", respBody)
		if !ok {
			t.Fatal("parseRetrieveCall ok=false")
		}
		continued, ok := appendRetrieveResult("anthropic", "/v1/messages", reqBody, respBody, callID, "original bytes")
		if !ok {
			t.Fatal("appendRetrieveResult ok=false")
		}
		if !bytes.Contains(continued, []byte(largeInt)) {
			t.Fatalf("request history integer %s was silently rounded: %s", largeInt, continued)
		}
	})
}

// Same guard on the gateway side: switching from json.Unmarshal to a Decoder
// must not start accepting a document with trailing bytes and silently drop
// them on the re-marshal. Both rewrites hand the body back untouched instead.
func TestRetrieveRewritesRejectTrailingBytes(t *testing.T) {
	base := `{"id":"msg_1","type":"message","role":"assistant","stop_reason":"tool_use","content":[` +
		`{"type":"tool_use","id":"tu_1","name":"caveman_retrieve","input":{"handle":"ccr_1","query":"schema"}}` +
		`]}`
	// The closing-delimiter rows are the ones decoder.More() gets wrong: it
	// answers "another element in the current array or object", and a stray `]`
	// or `}` is not one, so More() reports false and the byte is silently lost.
	for _, suffix := range []string{"TRAILING", " {\"second\":1}", "]", "}", ","} {
		t.Run("suffix="+suffix, func(t *testing.T) {
			respBody := []byte(base + suffix)
			cleaned, ok := stripRetrieveCall("anthropic", "/v1/messages", respBody)
			if ok || !bytes.Equal(cleaned, respBody) {
				t.Fatalf("trailing bytes were accepted and rewritten: ok=%v body=%s", ok, cleaned)
			}
		})
	}

	reqBody := []byte(`{"model":"claude-fable-5","max_tokens":64,"messages":[]} {"second":"value"}`)
	valid := []byte(`{"id":"msg_1","type":"message","role":"assistant","stop_reason":"tool_use","content":[` +
		`{"type":"tool_use","id":"tu_1","name":"caveman_retrieve","input":{"handle":"ccr_1","query":"schema"}}]}`)
	if _, ok := appendRetrieveResult("anthropic", "/v1/messages", reqBody, valid, "tu_1", "original bytes"); ok {
		t.Fatal("a request body with a second top-level value was accepted for rewrite")
	}
}
