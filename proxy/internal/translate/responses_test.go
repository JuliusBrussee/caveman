package translate

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// --- Responses -> Messages --------------------------------------------------

func TestResponsesToMessagesText(t *testing.T) {
	input := `[{"type":"message","role":"developer","content":[{"type":"input_text","text":"sandbox: workspace-write"}]},
	  {"type":"message","role":"user","content":[{"type":"input_text","text":"look"},{"type":"input_image","image_url":"data:image/png;base64,AAAB"},{"type":"input_image","image_url":"https://x/y.png"}]},
	  {"type":"message","role":"assistant","content":[{"type":"output_text","text":"seen"}]},
	  {"type":"message","role":"user","content":"again"}]`
	sent, reply := mustRequest(t, Responses, Messages, codexBody("anthropic/claude-opus-5-5", "medium", "", input), Options{Model: "claude-opus-5-5", Shown: "anthropic/claude-opus-5-5"})
	if sent["model"] != "claude-opus-5-5" || sent["stream"] != true || sent["max_tokens"] != float64(anthropicDefaultMaxTokens) || !reply.Stream() {
		t.Fatalf("body = %v", sent)
	}
	if encode(sent["thinking"]) != `{"type":"adaptive"}` || encode(sent["output_config"]) != `{"effort":"medium"}` {
		t.Fatalf("effort = %v %v", sent["thinking"], sent["output_config"])
	}
	system := sent["system"].([]any)[0].(map[string]any)
	if system["text"] != "You are Codex.\n\nsandbox: workspace-write" || system["cache_control"] == nil {
		t.Fatalf("system = %v", system)
	}
	messages := sent["messages"].([]any)
	images := encode(messages[0])
	if len(messages) != 3 || !strings.Contains(images, `"source":{"data":"AAAB","media_type":"image/png","type":"base64"}`) || !strings.Contains(images, `"source":{"type":"url","url":"https://x/y.png"}`) {
		t.Fatalf("messages = %s", encode(messages))
	}
	last := messages[2].(map[string]any)["content"].([]any)
	if last[len(last)-1].(map[string]any)["cache_control"] == nil || messages[0].(map[string]any)["content"].([]any)[2].(map[string]any)["cache_control"] == nil {
		t.Fatalf("cache breakpoints missing: %s", encode(messages))
	}

	recorder, usage, err := serve(t, reply, anthropicText("hello"), false)
	if err != nil || recorder.Header().Get("content-type") != "text/event-stream" {
		t.Fatalf("err %v headers %v", err, recorder.Header())
	}
	turn := codexAccept(t, recorder.Body.String())
	if turn.failure != "" || turn.text != "hello" || !strings.HasPrefix(turn.responseID, "resp_") || turn.model != "anthropic/claude-opus-5-5" {
		t.Fatalf("turn = %+v", turn)
	}
	// Usage: input counts cached and cache-write tokens, as OpenAI does.
	if turn.usage["input_tokens"] != 1210.0 || turn.usage["output_tokens"] != 50.0 || turn.usage["total_tokens"] != 1260.0 ||
		encode(turn.usage["input_tokens_details"]) != `{"cache_write_tokens":10,"cached_tokens":200}` {
		t.Fatalf("usage = %v", turn.usage)
	}
	if usage != (Usage{InputTokens: 1210, OutputTokens: 50, CacheReadTokens: 200, CacheWriteTokens: 10}) {
		t.Fatalf("Usage = %+v", usage)
	}

	// A non-streaming caller gets the final response object.
	_, quiet := mustRequest(t, Responses, Messages, strings.Replace(codexBody("m", "low", "", userHello), `"stream":true`, `"stream":false`, 1), Options{Model: "claude-opus-5-5"})
	recorder, _, _ = serve(t, quiet, anthropicText("hi"), false)
	answer := decode(t, recorder.Body.String())
	if quiet.Stream() || recorder.Header().Get("content-type") != "application/json" || answer["status"] != "completed" || answer["model"] != "claude-opus-5-5" ||
		!strings.Contains(encode(answer["output"]), `"text":"hi"`) {
		t.Fatalf("non-streamed = %s", recorder.Body.String())
	}
}

const applyPatchTool = `[{"type":"custom","name":"apply_patch","description":"Apply a patch.","format":{"type":"grammar","syntax":"lark","definition":"start: begin_patch hunk+ end_patch"}}]`

func TestResponsesToMessagesCustomToolRoundTrip(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: a.txt\n+hi\n*** End Patch"
	escaped, _ := json.Marshal(string(mustJSON(map[string]string{"input": patch})))
	stream := sse(
		`{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"apply_patch","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":`+string(escaped)+`}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_2","name":"shell","input":{"cmd":"ls"}}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":30}}`,
		`{"type":"message_stop"}`,
	)
	tools := `[{"type":"custom","name":"apply_patch","description":"Apply a patch.","format":{"type":"grammar","syntax":"lark","definition":"start: begin_patch hunk+ end_patch"}},
	  {"type":"function","name":"shell","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}]`
	sent, reply := mustRequest(t, Responses, Messages, codexBody("m", "low", tools, userHello), Options{Model: "claude-sonnet-5"})
	tool := sent["tools"].([]any)[0].(map[string]any)
	schema := encode(tool["input_schema"])
	if tool["name"] != "apply_patch" || !strings.Contains(tool["description"].(string), "start: begin_patch") || !strings.Contains(schema, `"required":["input"]`) ||
		encode(sent["tool_choice"]) != `{"type":"auto"}` || sent["tools"].([]any)[1].(map[string]any)["cache_control"] == nil {
		t.Fatalf("tools = %v %v", sent["tools"], sent["tool_choice"])
	}
	recorder, _, _ := serve(t, reply, stream, false)
	turn := codexAccept(t, recorder.Body.String())
	custom, function := itemsOfType(turn, "custom_tool_call"), itemsOfType(turn, "function_call")
	if turn.failure != "" || len(custom) != 1 || custom[0]["input"] != patch || custom[0]["call_id"] != "toolu_1" || !strings.HasPrefix(custom[0]["id"].(string), "ctc_") {
		t.Fatalf("custom = %+v", turn)
	}
	if len(function) != 1 || function[0]["arguments"] != `{"cmd":"ls"}` { // whole input, no deltas
		t.Fatalf("function = %v", function)
	}

	input := `[{"type":"message","role":"user","content":[{"type":"input_text","text":"add a.txt"}]},
	  {"type":"custom_tool_call","id":"ctc_1","call_id":"toolu_1","name":"apply_patch","input":` + encode(patch) + `},
	  {"type":"custom_tool_call_output","call_id":"toolu_1","output":"Done!"},
	  {"type":"function_call","call_id":"fc.weird:id/1","name":"shell","arguments":"{\"cmd\":\"ls\"}"},
	  {"type":"function_call_output","call_id":"fc.weird:id/1","output":[{"type":"input_text","text":"a.go"}]}]`
	sent, _ = mustRequest(t, Responses, Messages, codexBody("m", "low", tools, input), Options{Model: "claude-sonnet-5"})
	messages := sent["messages"].([]any)
	assistant := messages[1].(map[string]any)["content"].([]any)
	results := messages[2].(map[string]any)["content"].([]any)
	use, result := assistant[0].(map[string]any), results[0].(map[string]any)
	if use["type"] != "tool_use" || use["input"].(map[string]any)["input"] != patch || result["type"] != "tool_result" || result["tool_use_id"] != "toolu_1" {
		t.Fatalf("replayed = %s", encode(messages))
	}
	call := messages[3].(map[string]any)["content"].([]any)[0].(map[string]any)
	output := messages[4].(map[string]any)["content"].([]any)[0].(map[string]any)
	if call["id"] != output["tool_use_id"] || !anthropicCallID.MatchString(call["id"].(string)) || !strings.Contains(encode(output), "a.go") {
		t.Fatalf("foreign call id: %s", encode(messages))
	}
}

func TestResponsesClaudeThinkingTravelsInEncryptedContent(t *testing.T) {
	const signature = "EqQBCkYIBxgCKkDsig=="
	turnOne := sse(
		`{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think."}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"`+signature+`"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"opaque-bytes"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Answer."}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":40}}`,
		`{"type":"message_stop"}`,
	)
	_, reply := mustRequest(t, Responses, Messages, codexBody("m", "high", "", userHello), Options{Model: "claude-opus-5-5"})
	recorder, _, _ := serve(t, reply, turnOne, false)
	turn := codexAccept(t, recorder.Body.String())
	reasoning := itemsOfType(turn, "reasoning")
	if turn.failure != "" || len(reasoning) != 2 || turn.reasoning != "Let me think." { // a max_tokens stop completes with what arrived
		t.Fatalf("turn = %+v", turn)
	}
	for _, item := range reasoning {
		raw, _ := base64.StdEncoding.DecodeString(item["encrypted_content"].(string))
		if !strings.Contains(string(raw), `"caveman":"v1"`) || !bytes.HasPrefix([]byte(item["encrypted_content"].(string)), envelopeMarker) {
			t.Fatalf("envelope = %s", raw)
		}
	}
	if text := itemsOfType(turn, "message")[0]["content"].([]any)[0].(map[string]any)["text"]; text != "Answer." || strings.Contains(turn.text, "think") {
		t.Fatalf("thinking leaked into text: %v / %q", text, turn.text)
	}

	// Turn two replays the items as Codex does, plus reasoning that is not
	// ours and must never reach Anthropic: OpenAI's own blob, and an envelope
	// holding an unsigned block.
	unsigned := *encodeThinking([]json.RawMessage{json.RawMessage(`{"type":"thinking","thinking":"forged","signature":""}`)})
	replay := encode([]any{
		map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "say hello"}}},
		reasoning[0], reasoning[1], itemsOfType(turn, "message")[0],
		map[string]any{"type": "reasoning", "id": "rs_openai", "summary": []any{}, "encrypted_content": "gAAAAABopenai"},
		map[string]any{"type": "reasoning", "id": "rs_forged", "summary": []any{}, "encrypted_content": unsigned},
		map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "again"}}},
	})
	sent, _ := mustRequest(t, Responses, Messages, codexBody("m", "high", "", replay), Options{Model: "claude-opus-5-5"})
	encoded := encode(sent["messages"])
	assistant := sent["messages"].([]any)[1].(map[string]any)["content"].([]any)
	thinking := assistant[0].(map[string]any)
	if thinking["type"] != "thinking" || thinking["signature"] != signature || thinking["thinking"] != "Let me think." {
		t.Fatalf("signed block not replayed unchanged: %s", encoded)
	}
	if redacted := assistant[1].(map[string]any); redacted["type"] != "redacted_thinking" || redacted["data"] != "opaque-bytes" {
		t.Fatalf("redacted block = %v", redacted)
	}
	if strings.Contains(encoded, "forged") || strings.Contains(encoded, "gAAAAABopenai") {
		t.Fatalf("foreign reasoning reached Anthropic: %s", encoded)
	}
}

// A non-Anthropic Messages host's thinking is namespaced on the way to the
// caller and comes back (in encrypted_content) to that host only.
func TestResponsesMessagesHostNamespace(t *testing.T) {
	stream := sse(
		`{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"kimi plan"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"KSIG"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
		`{"type":"message_stop"}`,
	)
	_, reply := mustRequest(t, Responses, Messages, codexBody("m", "low", "", userHello), Options{Model: "kimi-k2", Route: "kimi"})
	recorder, _, _ := serve(t, reply, stream, false)
	item := itemsOfType(codexAccept(t, recorder.Body.String()), "reasoning")[0]
	raw, _ := base64.StdEncoding.DecodeString(item["encrypted_content"].(string))
	if !strings.Contains(string(raw), `"signature":"caveman:4:kimi:KSIG"`) {
		t.Fatalf("envelope = %s", raw)
	}
	input := encode([]any{map[string]any{"type": "message", "role": "user", "content": "go"}, item, map[string]any{"type": "message", "role": "user", "content": "more"}})
	toKimi := rawRequest(t, Responses, Messages, codexBody("m", "low", "", input), Options{Model: "kimi-k2", Route: "kimi"})
	toAnthropic := rawRequest(t, Responses, Messages, codexBody("m", "low", "", input), Options{Model: "claude-opus-5-5"})
	if !strings.Contains(toKimi, `"signature":"KSIG"`) || strings.Contains(toKimi, "caveman:") || strings.Contains(toAnthropic, "KSIG") || strings.Contains(toAnthropic, "kimi plan") {
		t.Fatalf("kimi %s\nanthropic %s", toKimi, toAnthropic)
	}
}

func TestResponsesEffortOnMessages(t *testing.T) {
	for _, tc := range []struct {
		model, effort string
		opts          Options
		thinking, out string
		maxTokens     float64
	}{
		{"claude-opus-5-5", "minimal", Options{}, `{"type":"adaptive"}`, `{"effort":"low"}`, anthropicDefaultMaxTokens},
		{"claude-opus-5-5", "ultra", Options{}, `{"type":"adaptive"}`, `{"effort":"max"}`, anthropicDefaultMaxTokens},
		{"claude-sonnet-4-5", "medium", Options{MaxOutputTokens: 8000}, `{"budget_tokens":4000,"type":"enabled"}`, ``, 8000},
		{"claude-opus-5-5", "none", Options{}, ``, ``, anthropicDefaultMaxTokens},
		{"claude-opus-5-5", "low", Options{Effort: "high"}, `{"type":"adaptive"}`, `{"effort":"high"}`, anthropicDefaultMaxTokens},
	} {
		tc.opts.Model = tc.model
		sent, _ := mustRequest(t, Responses, Messages, codexBody("m", tc.effort, "", userHello), tc.opts)
		thinking, out := "", ""
		if sent["thinking"] != nil {
			thinking = encode(sent["thinking"])
		}
		if sent["output_config"] != nil {
			out = encode(sent["output_config"])
		}
		if thinking != tc.thinking || out != tc.out || sent["max_tokens"] != tc.maxTokens {
			t.Errorf("%s %s: thinking %s output_config %s max_tokens %v", tc.model, tc.effort, thinking, out, sent["max_tokens"])
		}
	}
	// Sampling knobs only go with thinking off.
	body := strings.Replace(codexBody("m", "none", "", userHello), `"store":false`, `"store":false,"temperature":0.3`, 1)
	if sent, _ := mustRequest(t, Responses, Messages, body, Options{Model: "claude-opus-5-5"}); sent["temperature"] != 0.3 {
		t.Fatalf("temperature = %v", sent["temperature"])
	}
}

func TestResponsesStreamFailures(t *testing.T) {
	cutMessages := sse(
		`{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"half"}}`,
	)
	cutChat := sse(`{"choices":[{"delta":{"content":"half"}}]}`)
	for _, tc := range []struct {
		name, to, stream string
		cut              bool
		code, message    string
		failed           bool
	}{
		{"messages clean EOF", Messages, cutMessages, false, "server_error", "", true},
		{"messages broken body", Messages, cutMessages, true, "server_error", "", true},
		{"chat clean EOF", Chat, cutChat, false, "server_error", "", true},
		{"chat broken body", Chat, cutChat, true, "server_error", "", true},
		{"messages error event", Messages, sse(`{"type":"message_start","message":{"usage":{"input_tokens":10}}}`, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`), false, "server_error", "Overloaded", false},
		{"messages rate limit event", Messages, sse(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`), false, "rate_limit_exceeded", "slow down", false},
		{"chat overflow string code", Chat, chatStream(`{"error":{"code":"context_length_exceeded","message":"This model's maximum context length is 128000 tokens."}}`), false, "context_length_exceeded", responsesContextMessage, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, reply := mustRequest(t, Responses, tc.to, codexBody("m", "low", "", userHello), Options{Model: "claude-opus-5-5"})
			recorder, _, err := serve(t, reply, tc.stream, tc.cut)
			if !tc.failed {
				// An error before any content reaches nobody: the request falls back.
				if !errors.Is(err, ErrNotServed) || recorder.Body.Len() != 0 {
					t.Fatalf("err %v body %q", err, recorder.Body.String())
				}
				return
			}
			turn := codexAccept(t, recorder.Body.String())
			// server_error is retryable for Codex: the half answer never completes.
			if (err != nil) != tc.failed || turn.failure != tc.code || tc.message != "" && turn.message != tc.message || strings.Contains(recorder.Body.String(), "response.completed") {
				t.Fatalf("err %v turn %+v", err, turn)
			}
		})
	}
}

// Overflow wording: a prompt over the window is an overflow (the harness
// compacts); a completion that does not fit is not.
func TestResponsesFailureCodes(t *testing.T) {
	for body, want := range map[string]string{
		`{"error":{"message":"This model's maximum context length is 16384 tokens. However, you requested 122946 tokens (112946 in the messages, 10000 in the completion). Please reduce the length of the messages or completion.","code":"context_length_exceeded"}}`: "context_length_exceeded",
		`{"error":{"message":"This model's maximum context length is 128000 tokens. However, you requested 140000 tokens (10000 in the messages, 130000 in the completion).","code":"context_length_exceeded"}}`:                                                        "invalid_prompt",
		`{"error":{"message":"max_tokens is too large: 32768. This model supports at most 4096 completion tokens.","code":"invalid_value"}}`:                                                                                                                            "invalid_prompt",
		`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 250000 tokens > 200000 maximum"}}`:                                                                                                                                      "context_length_exceeded",
	} {
		if failure := responsesFailureFor(400, []byte(body), ""); failure.Code != want {
			t.Errorf("%s -> %+v, want %s", body, failure, want)
		}
	}
	if failure := responsesFailureFor(429, []byte(`{"error":{"message":"slow down"}}`), "42"); failure.Code != "rate_limit_exceeded" || failure.Message != "slow down Please try again in 42s." {
		t.Errorf("429 -> %+v", failure)
	}
	if failure := responsesFailureFor(500, []byte(`{"error":{"message":"context window cache miss"}}`), ""); failure.Code != "server_error" {
		t.Errorf("5xx -> %+v", failure)
	}
}

// --- Responses -> chat -------------------------------------------------------

func TestResponsesToChatFunctionCalls(t *testing.T) {
	tools := `[{"type":"function","name":"shell","description":"run","strict":false,"parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}]`
	for _, tc := range []struct {
		name, stream, id, arguments string
	}{
		{"argument deltas", chatStream(
			`{"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"shell","arguments":""}}]}}]}`,
			`{"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"cmd\":"}}]}}]}`,
			`{"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\"}"}}]},"finish_reason":"tool_calls"}]}`,
			`{"id":"c1","choices":[],"usage":{"prompt_tokens":900,"completion_tokens":20,"total_tokens":920,"prompt_tokens_details":{"cached_tokens":300},"completion_tokens_details":{"reasoning_tokens":7}}}`,
		), "call_a", `{"cmd":"ls"}`},
		{"whole call in one chunk", chatStream(
			`{"id":"c2","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_b","type":"function","function":{"name":"shell","arguments":"{\"cmd\":\"pwd\"}"}}]},"finish_reason":"tool_calls"}]}`,
		), "call_b", `{"cmd":"pwd"}`},
		{"no id", chatStream(
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"type":"function","function":{"name":"shell","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
		), "call_", `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent, reply := mustRequest(t, Responses, Chat, codexBody("openai/gpt-5.6", "high", tools, userHello), Options{Model: "gpt-5.6", Shown: "openai/gpt-5.6"})
			if sent["stream"] != true || encode(sent["stream_options"]) != `{"include_usage":true}` || sent["reasoning_effort"] != "high" || sent["parallel_tool_calls"] != true || sent["tool_choice"] != "auto" {
				t.Fatalf("body = %v", sent)
			}
			recorder, usage, _ := serve(t, reply, tc.stream, false)
			turn := codexAccept(t, recorder.Body.String())
			calls := itemsOfType(turn, "function_call")
			if turn.failure != "" || len(calls) != 1 || !strings.HasPrefix(calls[0]["call_id"].(string), tc.id) || calls[0]["arguments"] != tc.arguments ||
				calls[0]["name"] != "shell" || !strings.HasPrefix(calls[0]["id"].(string), "fc_") || turn.model != "openai/gpt-5.6" {
				t.Fatalf("turn = %+v", turn)
			}
			if tc.id == "call_a" {
				if turn.usage["input_tokens"] != 900.0 || encode(turn.usage["output_tokens_details"]) != `{"reasoning_tokens":7}` ||
					usage != (Usage{InputTokens: 900, OutputTokens: 20, CacheReadTokens: 300}) {
					t.Fatalf("usage = %v %+v", turn.usage, usage)
				}
			}
		})
	}

	// Turn two replays the call and its output; a user message between the
	// call and its output moves after the output.
	input := `[{"type":"message","role":"user","content":[{"type":"input_text","text":"list files"}]},
	  {"type":"function_call","id":"fc_1","call_id":"call_a","name":"shell","arguments":"{\"cmd\":\"ls\"}"},
	  {"type":"message","role":"user","content":"also check the logs"},
	  {"type":"function_call_output","call_id":"call_a","output":"a.go\nb.go"}]`
	var body struct {
		Messages []openAIMessage `json:"messages"`
	}
	_ = json.Unmarshal([]byte(rawRequest(t, Responses, Chat, codexBody("m", "high", tools, input), Options{Model: "gpt-5.6"})), &body)
	if len(body.Messages) != 5 || body.Messages[2].Role != "assistant" || len(body.Messages[2].ToolCalls) != 1 ||
		body.Messages[3].Role != "tool" || body.Messages[3].ToolCallID != "call_a" || body.Messages[3].Content != "a.go\nb.go" || body.Messages[4].Content != "also check the logs" {
		t.Fatalf("messages = %+v", body.Messages)
	}
}

func TestResponsesToChatToolNamesAndParallelCalls(t *testing.T) {
	long := strings.Repeat("very_long_tool_name_", 5) // 100 characters
	hashed := wireToolName("", long)
	tools := `[{"type":"namespace","name":"mcp__github__","description":"GitHub tools.","tools":[
	    {"type":"function","name":"create_issue","description":"Open an issue.","parameters":{"type":"object","properties":{}}}]},
	  {"type":"function","name":"` + long + `","description":"long","parameters":{"type":"object","properties":{}}},
	  {"type":"custom","name":"apply_patch","description":"patch"},{"type":"web_search"}]`
	sent, reply := mustRequest(t, Responses, Chat, codexBody("m", "low", tools, userHello), Options{Model: "gemini-3.7-flash"})
	declared := encode(sent["tools"])
	if len(hashed) != 64 || !strings.Contains(declared, `"name":"mcp__github__create_issue"`) || !strings.Contains(declared, `"description":"GitHub tools.\n\nOpen an issue."`) ||
		!strings.Contains(declared, `"name":"`+hashed+`"`) || strings.Contains(declared, "web_search") {
		t.Fatalf("tools = %s", declared)
	}
	stream := chatStream(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"mcp__github__create_issue","arguments":"{\"t\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"`+hashed+`","arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":2,"id":"call_3","type":"function","function":{"arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":2,"function":{"name":"apply_patch","arguments":"{\"input\":\"*** Begin Patch\"}"}}]},"finish_reason":"tool_calls"}]}`,
	)
	recorder, _, _ := serve(t, reply, stream, false)
	turn := codexAccept(t, recorder.Body.String())
	calls, custom := itemsOfType(turn, "function_call"), itemsOfType(turn, "custom_tool_call")
	if turn.failure != "" || len(calls) != 2 || len(custom) != 1 {
		t.Fatalf("turn = %+v", turn)
	}
	if calls[0]["name"] != "create_issue" || calls[0]["namespace"] != "mcp__github__" || calls[0]["arguments"] != `{"t":1}` || calls[1]["name"] != long || calls[1]["namespace"] != nil {
		t.Fatalf("calls = %v", calls)
	}
	if custom[0]["input"] != "*** Begin Patch" || custom[0]["call_id"] != "call_3" || strings.Contains(recorder.Body.String(), `"name":"apply_patch","status":"in_progress","type":"function_call"`) {
		t.Fatalf("custom = %v", custom)
	}
	// Parallel calls an upstream sends all at index 0 stay separate.
	recorder, _, _ = serve(t, reply, chatStream(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"`+hashed+`","arguments":"{\"cmd\":\"a\"}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_2","type":"function","function":{"name":"`+hashed+`","arguments":"{\"cmd\":\"b\"}"}}]},"finish_reason":"tool_calls"}]}`), false)
	if calls := itemsOfType(codexAccept(t, recorder.Body.String()), "function_call"); len(calls) != 2 || calls[0]["arguments"] != `{"cmd":"a"}` || calls[1]["arguments"] != `{"cmd":"b"}` {
		t.Fatalf("index-0 parallel calls = %v", calls)
	}
	// With no index and no id either (Gemini's chat wire), a name after
	// arguments starts the next call.
	recorder, _, _ = serve(t, reply, chatStream(
		`{"choices":[{"delta":{"tool_calls":[{"id":"","type":"function","function":{"name":"`+hashed+`","arguments":"{\"cmd\":\"a\"}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"id":"","type":"function","function":{"name":"`+hashed+`","arguments":"{\"cmd\":\"b\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`), false)
	if calls := itemsOfType(codexAccept(t, recorder.Body.String()), "function_call"); len(calls) != 2 || calls[0]["arguments"] != `{"cmd":"a"}` || calls[1]["arguments"] != `{"cmd":"b"}` || calls[0]["call_id"] == calls[1]["call_id"] {
		t.Fatalf("id-less parallel calls = %v", calls)
	}
	// History names map the same way.
	input := `[{"type":"message","role":"user","content":"x"},
	  {"type":"function_call","call_id":"call_1","name":"create_issue","namespace":"mcp__github__","arguments":"{}"},
	  {"type":"function_call_output","call_id":"call_1","output":"ok"}]`
	if replayed := rawRequest(t, Responses, Chat, codexBody("m", "low", tools, input), Options{Model: "m"}); !strings.Contains(replayed, `"name":"mcp__github__create_issue"`) {
		t.Fatalf("replayed = %s", replayed)
	}
}

// A chat route's reasoning travels in encrypted_content signed for the
// route and model, and goes back as reasoning_content to that pair only.
func TestResponsesToChatReasoningReplay(t *testing.T) {
	opts := Options{Model: "deepseek-v4-flash", Route: "deepseek", Replay: true, Dialect: "deepseek"}
	sent, reply := mustRequest(t, Responses, Chat, codexBody("m", "medium", "", userHello), opts)
	if encode(sent["thinking"]) != `{"type":"enabled"}` || sent["reasoning_effort"] != "high" {
		t.Fatalf("deepseek effort = %v", sent)
	}
	recorder, _, _ := serve(t, reply, chatStream(
		`{"choices":[{"delta":{"reasoning_content":"step one"}}]}`,
		`{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`), false)
	turn := codexAccept(t, recorder.Body.String())
	reasoning := itemsOfType(turn, "reasoning")
	if turn.failure != "" || turn.reasoning != "step one" || len(reasoning) != 1 || reasoning[0]["encrypted_content"] == nil {
		t.Fatalf("turn = %+v", turn)
	}
	input := encode([]any{map[string]any{"type": "message", "role": "user", "content": "go"}, reasoning[0], itemsOfType(turn, "message")[0],
		map[string]any{"type": "message", "role": "user", "content": "more"}})
	var body struct {
		Messages []openAIMessage `json:"messages"`
	}
	_ = json.Unmarshal([]byte(rawRequest(t, Responses, Chat, codexBody("m", "medium", "", input), opts)), &body)
	if body.Messages[2].Role != "assistant" || body.Messages[2].ReasoningContent != "step one" || body.Messages[2].Content != "done" {
		t.Fatalf("messages = %+v", body.Messages)
	}
	if other := rawRequest(t, Responses, Chat, codexBody("m", "medium", "", input), Options{Model: "glm-5", Route: "zai", Replay: true}); strings.Contains(other, "step one") {
		t.Fatalf("another route got the reasoning: %s", other)
	}
	// Without Replay, the reasoning is shown but carries an empty envelope:
	// never a bare id OpenAI would look up (store:false) on the harness's path.
	_, plain := mustRequest(t, Responses, Chat, codexBody("m", "medium", "", userHello), Options{Model: "m"})
	recorder, _, _ = serve(t, plain, chatStream(`{"choices":[{"delta":{"reasoning":"hm"}}]}`, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`), false)
	item := itemsOfType(codexAccept(t, recorder.Body.String()), "reasoning")
	encrypted, _ := item[0]["encrypted_content"].(string)
	if len(item) != 1 || !strings.HasPrefix(encrypted, string(envelopeMarker)) {
		t.Fatalf("reasoning = %v", item)
	}
	if _, empty := decodeThinking(&encrypted, anthropicRoute); !empty {
		t.Fatal("the empty envelope is not read as the runtime's own")
	}
	// Back on the harness's own OpenAI path, that item goes.
	next, _ := json.Marshal(map[string]any{"model": "gpt-6-sol", "store": false, "input": []any{
		map[string]any{"type": "message", "role": "user", "content": "hello"}, item[0],
		map[string]any{"type": "message", "role": "user", "content": "more"}}})
	if out := OpenAINative(next); strings.Contains(string(out), "encrypted_content") || !strings.Contains(string(out), "more") {
		t.Fatalf("harness body = %s", out)
	}
}

func TestResponsesToChatOpenRouterAndParameters(t *testing.T) {
	body := strings.Replace(codexBody("m", "xhigh", "", userHello), `"store":false`, `"store":false,"max_output_tokens":900,"text":{"format":{"type":"json_schema","name":"out","schema":{"type":"object"},"strict":true}}`, 1)
	sent, _ := mustRequest(t, Responses, Chat, body, Options{Model: "anthropic/claude-sonnet-5", Dialect: "openrouter", MaxTokensField: "max_completion_tokens"})
	if encode(sent["reasoning"]) != `{"effort":"xhigh"}` || encode(sent["usage"]) != `{"include":true}` || encode(sent["cache_control"]) != `{"type":"ephemeral"}` ||
		sent["max_completion_tokens"] != 900.0 || sent["max_tokens"] != nil || sent["response_format"].(map[string]any)["json_schema"].(map[string]any)["strict"] != true {
		t.Fatalf("body = %v", sent)
	}
	if sent, _ := mustRequest(t, Responses, Chat, body, Options{Model: "gpt-6", Dialect: "openrouter"}); sent["cache_control"] != nil {
		t.Fatalf("cache_control on a non-Claude model: %v", sent)
	}
}

// --- Responses -> Responses ---------------------------------------------------

func TestResponsesNativeRequest(t *testing.T) {
	ours := *encodeThinking([]json.RawMessage{json.RawMessage(`{"type":"thinking","thinking":"x","signature":"sig"}`)})
	input := encode([]any{
		map[string]any{"type": "message", "role": "user", "content": "hi"},
		map[string]any{"type": "reasoning", "id": "rs_1", "summary": []any{}, "encrypted_content": "gAAAAABopenai"},
		map[string]any{"type": "reasoning", "id": "rs_2", "summary": []any{}, "encrypted_content": ours},
		map[string]any{"type": "reasoning", "id": "rs_3", "summary": []any{}, "encrypted_content": nil},
	})
	body := codexBody("openai/gpt-5.6", "low", "", input)
	sent, _ := mustRequest(t, Responses, Responses, body, Options{Model: "gpt-5.6", Effort: "xhigh"})
	items := encode(sent["input"])
	if sent["model"] != "gpt-5.6" || sent["reasoning"].(map[string]any)["effort"] != "xhigh" || sent["reasoning"].(map[string]any)["summary"] != "auto" ||
		!strings.Contains(items, "gAAAAABopenai") || strings.Contains(items, "rs_2") || strings.Contains(items, "rs_3") {
		t.Fatalf("native body = %v", sent)
	}
	if sent, _ := mustRequest(t, Responses, Responses, body, Options{Model: "gpt-5.6"}); sent["reasoning"].(map[string]any)["effort"] != "low" {
		t.Fatalf("the caller's own effort must stay: %v", sent)
	}

	// OpenAINative: envelopes only, byte-identical when there are none.
	stripped := string(OpenAINative([]byte(body)))
	if strings.Contains(stripped, "rs_2") || !strings.Contains(stripped, "rs_1") || !strings.Contains(stripped, "rs_3") {
		t.Fatalf("OpenAINative = %s", stripped)
	}
	clean := []byte(codexBody("openai/gpt-5.6", "low", "", `[{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"gAAAAABopenai"},{"type":"message","role":"user","content":"hi"}]`))
	if out := OpenAINative(clean); &out[0] != &clean[0] || !bytes.Equal(out, clean) {
		t.Fatalf("clean body changed: %s", out)
	}
}

func TestResponsesNativeRelay(t *testing.T) {
	native := sse(
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_up","model":"gpt-5.6-2026-08-01","status":"in_progress","output":[]}}`,
		`{"type":"response.output_item.done","sequence_number":1,"output_index":0,"item":{"type":"message","id":"msg_up","role":"assistant","content":[{"type":"output_text","text":"native"}]}}`,
		`{"type":"response.completed","sequence_number":2,"response":{"id":"resp_up","model":"gpt-5.6-2026-08-01","status":"completed","output":[],"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":40},"output_tokens":5,"output_tokens_details":{"reasoning_tokens":2},"total_tokens":105}}}`,
	)
	_, reply := mustRequest(t, Responses, Responses, codexBody("openai/gpt-5.6", "low", "", userHello), Options{Model: "gpt-5.6", Shown: "openai/gpt-5.6"})
	recorder, usage, err := serve(t, reply, native, false)
	turn := codexAccept(t, recorder.Body.String())
	if err != nil || turn.failure != "" || turn.responseID != "resp_up" || turn.model != "openai/gpt-5.6" || strings.Contains(recorder.Body.String(), "2026-08-01") {
		t.Fatalf("turn = %+v\n%s", turn, recorder.Body.String())
	}
	if usage != (Usage{InputTokens: 100, OutputTokens: 5, CacheReadTokens: 40}) {
		t.Fatalf("usage = %+v", usage)
	}
	// A relay cut before content reaches nobody; one cut after it ends in response.failed.
	recorder, _, err = serve(t, reply, sse(`{"type":"response.created","response":{"id":"resp_up"}}`), true)
	if !errors.Is(err, ErrNotServed) || recorder.Body.Len() != 0 {
		t.Fatalf("cut before content = %v %q", err, recorder.Body.String())
	}
	recorder, _, err = serve(t, reply, sse(`{"type":"response.created","response":{"id":"resp_up"}}`, `{"type":"response.output_text.delta","item_id":"m","delta":"half"}`), true)
	if strings.Count(recorder.Body.String(), "half") != 1 || err == nil || errors.Is(err, ErrNotServed) || !strings.Contains(recorder.Body.String(), "response.failed") {
		t.Fatalf("cut = %v %s", err, recorder.Body.String())
	}
	// A non-streamed answer is renamed and its usage read.
	_, quiet := mustRequest(t, Responses, Responses, `{"model":"m","input":"hi"}`, Options{Model: "gpt-5.6", Shown: "openai/gpt-5.6"})
	recorder, usage, _ = serve(t, quiet, `{"id":"resp_1","model":"gpt-5.6-x","output":[],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4,"input_tokens_details":{"cached_tokens":0}}}`, false)
	if decode(t, recorder.Body.String())["model"] != "openai/gpt-5.6" || usage.InputTokens != 3 {
		t.Fatalf("non-streamed = %s %+v", recorder.Body.String(), usage)
	}
}

func TestChatGPTLoginFitting(t *testing.T) {
	body := `{"model":"openai/gpt-6-astra","instructions":"x","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],` +
		`"tools":[{"type":"function","name":"exec_command","parameters":{}},{"type":"web_search"}],"temperature":0.5,"user":"u","max_output_tokens":10,"store":true,"stream":false}`
	sent, reply := mustRequest(t, Responses, Responses, body, Options{Model: "gpt-6-astra", ChatGPTLogin: true})
	if sent["store"] != false || sent["stream"] != true || sent["temperature"] != nil || sent["user"] != nil || sent["max_output_tokens"] != nil ||
		encode(sent["tools"]) != `[{"type":"web_search"}]` || encode(sent["additional_tools"]) != `[{"name":"exec_command","parameters":{},"type":"function"}]` {
		t.Fatalf("preview shape: %v", sent)
	}
	// The caller did not ask for a stream: the preview's stream is assembled.
	stream := sse(
		`{"type":"response.created","response":{"id":"resp_1","model":"gpt-6-astra"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}`,
		`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-6-astra","status":"completed","output":[],"usage":{"input_tokens":7,"output_tokens":1,"total_tokens":8}}}`,
	)
	recorder, usage, err := serve(t, reply, stream, false)
	answer := decode(t, recorder.Body.String())
	if err != nil || reply.Stream() || recorder.Header().Get("content-type") != "application/json" || answer["model"] != "gpt-6-astra" ||
		!strings.Contains(encode(answer["output"]), `"text":"ok"`) || usage.InputTokens != 7 {
		t.Fatalf("assembled = %s %+v %v", recorder.Body.String(), usage, err)
	}
	recorder, _, err = serve(t, reply, sse(`{"type":"response.created","response":{"id":"resp_1"}}`), false)
	if !errors.Is(err, ErrNotServed) || recorder.Body.Len() != 0 {
		t.Fatalf("an unfinished preview stream: %v %q", err, recorder.Body.String())
	}
}

// --- chat -> chat --------------------------------------------------------------

// One golden per chat dialect, through a chat caller.
func TestChatDialectGoldens(t *testing.T) {
	body := `{"model":"auto","max_tokens":4000,"reasoning_effort":"%s","stop":["X"],"presence_penalty":0.1,"user":"u1","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	for _, tc := range []struct {
		name, effort string
		opts         Options
		check        func(map[string]any) bool
	}{
		{"openrouter", "high", Options{Model: "anthropic/claude-sonnet-5.5", Dialect: "openrouter"}, func(b map[string]any) bool {
			return encode(b["reasoning"]) == `{"effort":"high"}` && b["usage"] != nil && b["cache_control"] != nil && b["reasoning_effort"] == nil && b["model"] == "anthropic/claude-sonnet-5.5"
		}},
		{"toggle", "low", Options{Model: "glm-5.3", Dialect: "toggle"}, func(b map[string]any) bool {
			return encode(b["thinking"]) == `{"type":"enabled"}` && b["reasoning_effort"] == nil
		}},
		{"qwen", "medium", Options{Model: "qwen3.8-max", Dialect: "qwen"}, func(b map[string]any) bool {
			return b["enable_thinking"] == true && b["thinking_budget"] == 2000.0
		}},
		{"xai drops params", "high", Options{Model: "grok-4.7", DropParams: []string{"stop", "presence_penalty"}}, func(b map[string]any) bool {
			return b["stop"] == nil && b["presence_penalty"] == nil && b["reasoning_effort"] == "high"
		}},
		{"openai names", "high", Options{Model: "gpt-6-astra", MaxTokensField: "max_completion_tokens"}, func(b map[string]any) bool {
			return b["max_completion_tokens"] == 4000.0 && b["max_tokens"] == nil && b["user"] == nil && b["reasoning_effort"] == "high" &&
				encode(b["stream_options"]) == `{"include_usage":true}`
		}},
		{"routed effort", "low", Options{Model: "gpt-6", Effort: "xhigh"}, func(b map[string]any) bool { return b["reasoning_effort"] == "xhigh" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent, _ := mustRequest(t, Chat, Chat, strings.Replace(body, "%s", tc.effort, 1), tc.opts)
			if !tc.check(sent) {
				t.Fatalf("body = %v", sent)
			}
		})
	}
	// OpenRouter's `reasoning` from a chat caller is OpenAI's reasoning_effort elsewhere.
	sent, _ := mustRequest(t, Chat, Chat, `{"model":"m","reasoning":{"effort":"low"},"messages":[]}`, Options{Model: "gpt-6"})
	if sent["reasoning"] != nil || sent["reasoning_effort"] != "low" {
		t.Fatalf("reasoning = %v", sent)
	}
}

func TestChatRelay(t *testing.T) {
	_, reply := mustRequest(t, Chat, Chat, `{"model":"auto","stream":true,"messages":[]}`, Options{Model: "gpt-6", Shown: "auto"})
	stream := chatStream(
		`{"id":"c1","model":"gpt-6-2026","choices":[{"delta":{"content":"{\"model\":\"x\"}"}}]}`,
		`{"id":"c1","model":"gpt-6-2026","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":4}}}`,
	)
	recorder, usage, err := serve(t, reply, stream, false)
	out := recorder.Body.String()
	if err != nil || strings.Contains(out, "gpt-6-2026") || strings.Count(out, `"model":"auto"`) != 2 || !strings.Contains(out, `{\"model\":\"x\"}`) || !strings.HasSuffix(out, "data: [DONE]\n\n") {
		t.Fatalf("relay = %s", out)
	}
	if usage != (Usage{InputTokens: 10, OutputTokens: 2, CacheReadTokens: 4}) {
		t.Fatalf("usage = %+v", usage)
	}
	recorder, _, err = serve(t, reply, sse(`{"choices":[{"delta":{"content":"a"}}]}`), true)
	if err == nil || !strings.Contains(recorder.Body.String(), `data: {"error":`) {
		t.Fatalf("cut relay = %v %s", err, recorder.Body.String())
	}
	_, quiet := mustRequest(t, Chat, Chat, `{"model":"auto","messages":[]}`, Options{Model: "gpt-6"})
	recorder, usage, _ = serve(t, quiet, `{"id":"c","model":"gpt-6-2026","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":1,"prompt_cache_hit_tokens":3}}`, false)
	if decode(t, recorder.Body.String())["model"] != "gpt-6" || usage != (Usage{InputTokens: 5, OutputTokens: 1, CacheReadTokens: 3}) {
		t.Fatalf("non-streamed = %s %+v", recorder.Body.String(), usage)
	}
}
