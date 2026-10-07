package translate

import (
	"errors"
	"strings"
	"testing"
)

// --- Messages -> Responses: requests ----------------------------------------

func TestMessagesToResponsesRequest(t *testing.T) {
	body := `{
      "model":"claude-opus-5-5","max_tokens":2048,"temperature":0.2,"top_p":0.9,"stop_sequences":["STOP"],"stream":true,
      "system":[{"type":"text","text":"You are Claude Code","cache_control":{"type":"ephemeral"}}],
      "output_config":{"effort":"max"},"metadata":{"user_id":"device-123"},
      "tools":[{"name":"Read","description":"read a file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}],
      "tool_choice":{"type":"tool","name":"Read"},
      "messages":[
        {"role":"user","content":[{"type":"text","text":"look at this"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]},
        {"role":"assistant","content":[
          {"type":"thinking","thinking":"mine","signature":"caveman:r1:chatgpt:ENC-1"},
          {"type":"thinking","thinking":"anthropic's","signature":"sig-anthropic"},
          {"type":"thinking","thinking":"other route","signature":"caveman:r1:openai:ENC-2"},
          {"type":"text","text":"Reading."},
          {"type":"tool_use","id":"toolu_01","name":"Read","input":{"path":"a.go"}}]},
        {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":[{"type":"text","text":"package a"}]},{"type":"text","text":"and?"}]},
        {"role":"assistant","content":[{"type":"tool_use","id":"functions.Bash:0","name":"Bash","input":{}}]},
        {"role":"user","content":[{"type":"tool_result","tool_use_id":"functions.Bash:0","is_error":true,"content":"boom"}]}]}`
	got, reply := mustRequest(t, Messages, Responses, body, Options{Model: "gpt-6.1-sol", Shown: "claude-opus-5-5", Route: "chatgpt"})
	if !reply.Stream() || !reply.upstreamStream {
		t.Fatal("stream flags")
	}
	want := map[string]any{
		"model": "gpt-6.1-sol", "store": false, "stream": true, "instructions": "You are Claude Code", "max_output_tokens": 2048.0,
		"reasoning": map[string]any{"effort": "xhigh", "summary": "auto"}, "include": []any{"reasoning.encrypted_content"},
		"tools":       []any{map[string]any{"type": "function", "name": "Read", "description": "read a file", "strict": false, "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}},
		"tool_choice": map[string]any{"type": "function", "name": "Read"},
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": []any{
				map[string]any{"type": "input_text", "text": "look at this"}, map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAAA"}}},
			map[string]any{"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "mine"}}, "encrypted_content": "ENC-1"},
			map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "Reading."}}},
			map[string]any{"type": "function_call", "call_id": "toolu_01", "name": "Read", "arguments": `{"path":"a.go"}`},
			map[string]any{"type": "function_call_output", "call_id": "toolu_01", "output": "package a"},
			map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "and?"}}},
			map[string]any{"type": "function_call", "call_id": safeCallID("functions.Bash:0"), "name": "Bash", "arguments": `{}`},
			map[string]any{"type": "function_call_output", "call_id": safeCallID("functions.Bash:0"), "output": "Error: boom"},
		},
	}
	if encode(got) != encode(want) {
		t.Fatalf("body =\n%s\nwant\n%s", encode(got), encode(want))
	}
	for _, leaked := range []string{"device-123", "STOP", "temperature", "cache_control", "sig-anthropic", "ENC-2"} {
		if strings.Contains(encode(got), leaked) {
			t.Errorf("%s reached the Responses host", leaked)
		}
	}
}

func TestMessagesToResponsesEffortAndChoices(t *testing.T) {
	base := func(extra string) string {
		return `{"model":"m","max_tokens":10` + extra + `,"messages":[{"role":"user","content":"hi"}]}`
	}
	for _, tc := range []struct {
		name, extra string
		opts        Options
		reasoning   string
	}{
		{"options win", `,"output_config":{"effort":"low"}`, Options{Model: "g", Effort: "high"}, `{"effort":"high","summary":"auto"}`},
		{"adaptive thinking", `,"thinking":{"type":"adaptive"}`, Options{Model: "g"}, `{"effort":"medium","summary":"auto"}`},
		{"none", ``, Options{Model: "g", Effort: "none"}, `{"effort":"none"}`},
		{"nothing set", ``, Options{Model: "g"}, ``},
	} {
		got, _ := mustRequest(t, Messages, Responses, base(tc.extra), tc.opts)
		if reasoning, set := got["reasoning"]; (set && encode(reasoning) != tc.reasoning) || (!set && tc.reasoning != "") {
			t.Errorf("%s: reasoning = %v", tc.name, encode(reasoning))
		}
	}
	for choice, want := range map[string]string{`{"type":"auto"}`: `"auto"`, `{"type":"any"}`: `"required"`, `{"type":"none"}`: `"none"`} {
		got, _ := mustRequest(t, Messages, Responses, base(`,"tool_choice":`+choice), Options{Model: "g"})
		if encode(got["tool_choice"]) != want {
			t.Errorf("tool_choice %s = %v", choice, got["tool_choice"])
		}
	}
	if _, _, err := Request(Messages, Responses, []byte(base(`,"tools":[{"type":"web_search_20250305","name":"web_search"}]`)), Options{Model: "g"}); err == nil {
		t.Error("a server tool must be refused")
	}
	// The Sign in with ChatGPT preview: tools move to additional_tools, refused fields go.
	got, _ := mustRequest(t, Messages, Responses, base(`,"tools":[{"name":"Read","input_schema":{"type":"object"}}]`), Options{Model: "g", ChatGPTLogin: true, Route: "chatgpt"})
	if _, set := got["max_output_tokens"]; set || got["tools"] != nil || got["additional_tools"] == nil || got["store"] != false || got["stream"] != true {
		t.Fatalf("chatgpt login body = %s", encode(got))
	}
}

// --- Messages -> Responses: answers -----------------------------------------

func upstreamResponses(frames ...string) string { return sse(frames...) }

var fullResponsesStream = upstreamResponses(
	`{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`,
	`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}`,
	`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","delta":"thinking "}`,
	`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","delta":"hard"}`,
	`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"thinking hard"}],"encrypted_content":"ENC-XYZ"}}`,
	`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg_1","role":"assistant","content":[]}}`,
	`{"type":"response.output_text.delta","item_id":"msg_1","delta":"hello "}`,
	`{"type":"response.output_text.delta","item_id":"msg_1","delta":"world"}`,
	`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"hello world"}]}}`,
	`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"Read","arguments":""}}`,
	`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"pa"}`,
	`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"th\":\"x\"}"}`,
	`{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_abc","name":"Read","arguments":"{\"path\":\"x\"}"}}`,
	`{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":600},"output_tokens":80,"output_tokens_details":{"reasoning_tokens":40},"total_tokens":1080}}}`,
)

func TestResponsesStreamToMessages(t *testing.T) {
	_, reply := mustRequest(t, Messages, Responses, `{"model":"claude-opus-5-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		Options{Model: "gpt-6.1-sol", Shown: "claude-opus-5-5", Route: "chatgpt"})
	recorder, usage, err := serve(t, reply, fullResponsesStream, false)
	if err != nil || recorder.Header().Get("content-type") != "text/event-stream" {
		t.Fatalf("err %v headers %v", err, recorder.Header())
	}
	if usage != (Usage{InputTokens: 1000, OutputTokens: 80, CacheReadTokens: 600}) {
		t.Fatalf("usage = %+v", usage)
	}
	events := anthropicEvents(t, recorder.Body.String())
	want := "message_start," +
		"content_block_start,content_block_delta,content_block_delta,content_block_delta,content_block_stop," +
		"content_block_start,content_block_delta,content_block_delta,content_block_stop," +
		"content_block_start,content_block_delta,content_block_delta,content_block_stop," +
		"message_delta,message_stop"
	if got := eventNames(events); got != want {
		t.Fatalf("events = %s\nwant     %s", got, want)
	}
	if message := events[0].data["message"].(map[string]any); message["model"] != "claude-opus-5-5" || message["id"] != "msg_resp_1" {
		t.Fatalf("message_start = %v", message)
	}
	if delta := events[4].data["delta"].(map[string]any); delta["type"] != "signature_delta" || delta["signature"] != "caveman:r1:chatgpt:ENC-XYZ" {
		t.Fatalf("thinking signature = %v", delta)
	}
	if text := events[7].data["delta"].(map[string]any)["text"].(string) + events[8].data["delta"].(map[string]any)["text"].(string); text != "hello world" {
		t.Fatalf("text = %q", text)
	}
	tool := events[10].data["content_block"].(map[string]any)
	if tool["type"] != "tool_use" || tool["name"] != "Read" || tool["id"] != "call_abc" {
		t.Fatalf("tool block = %v", tool)
	}
	if partial := events[11].data["delta"].(map[string]any)["partial_json"].(string) + events[12].data["delta"].(map[string]any)["partial_json"].(string); partial != `{"path":"x"}` {
		t.Fatalf("partial json = %q", partial)
	}
	final := events[14].data
	if final["delta"].(map[string]any)["stop_reason"] != "tool_use" || encode(final["usage"]) != `{"cache_creation_input_tokens":0,"cache_read_input_tokens":600,"input_tokens":400,"output_tokens":80}` {
		t.Fatalf("message_delta = %v", final)
	}
}

func TestResponsesStreamToMessagesStopsErrorsAndCuts(t *testing.T) {
	_, reply := mustRequest(t, Messages, Responses, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`, Options{Model: "g", Route: "chatgpt"})
	text := `{"type":"response.output_text.delta","item_id":"m1","delta":"a"}`
	for _, tc := range []struct {
		name, stream string
		cut          bool
		last, stop   string
		failed       bool
	}{
		{"completed", upstreamResponses(text, `{"type":"response.completed","response":{"id":"r"}}`), false, "message_stop", "end_turn", false},
		{"incomplete", upstreamResponses(text, `{"type":"response.incomplete","response":{"id":"r","incomplete_details":{"reason":"max_output_tokens"}}}`), false, "message_stop", "max_tokens", false},
		{"arguments only at done", upstreamResponses(
			`{"type":"response.output_item.added","item":{"type":"function_call","id":"fc","call_id":"c1","name":"Bash"}}`,
			`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc","call_id":"c1","name":"Bash","arguments":"{\"cmd\":\"ls\"}"}}`,
			`{"type":"response.completed","response":{"id":"r"}}`), false, "message_stop", "tool_use", false},
		{"failed", upstreamResponses(text, `{"type":"response.failed","response":{"id":"r","error":{"code":"rate_limit_exceeded","message":"slow down"}}}`), false, "error", "", false},
		{"clean EOF without completed", upstreamResponses(text), false, "error", "", true},
		{"broken body", upstreamResponses(text), true, "error", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder, _, err := serve(t, reply, tc.stream, tc.cut)
			if (err != nil) != tc.failed {
				t.Fatalf("err = %v", err)
			}
			events := anthropicEvents(t, recorder.Body.String())
			last := events[len(events)-1]
			if last.name != tc.last {
				t.Fatalf("events = %s", eventNames(events))
			}
			if tc.stop != "" && events[len(events)-2].data["delta"].(map[string]any)["stop_reason"] != tc.stop {
				t.Fatalf("stop = %v", events[len(events)-2].data)
			}
			if tc.name == "failed" && last.data["error"].(map[string]any)["type"] != "rate_limit_error" {
				t.Fatalf("error = %v", last.data)
			}
			if tc.name == "arguments only at done" && !strings.Contains(recorder.Body.String(), `"partial_json":"{\"cmd\":\"ls\"}"`) {
				t.Fatalf("arguments never streamed: %s", recorder.Body.String())
			}
		})
	}
}

func TestResponsesAnswerAssembledForANonStreamingCaller(t *testing.T) {
	got, reply := mustRequest(t, Messages, Responses, `{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`,
		Options{Model: "gpt-6.1-sol", Shown: "claude-opus-5-5", Route: "chatgpt"})
	if got["stream"] != true || reply.Stream() {
		t.Fatalf("the upstream always streams; the caller did not ask to: %v %v", got["stream"], reply.Stream())
	}
	recorder, _, err := serve(t, reply, fullResponsesStream, false)
	if err != nil || recorder.Header().Get("content-type") != "application/json" {
		t.Fatalf("err %v headers %v", err, recorder.Header())
	}
	answer := decode(t, recorder.Body.String())
	want := `{"content":[{"signature":"caveman:r1:chatgpt:ENC-XYZ","thinking":"thinking hard","type":"thinking"},{"text":"hello world","type":"text"},{"id":"call_abc","input":{"path":"x"},"name":"Read","type":"tool_use"}],"id":"msg_resp_1","model":"claude-opus-5-5","role":"assistant","stop_reason":"tool_use","stop_sequence":null,"type":"message","usage":{"cache_creation_input_tokens":0,"cache_read_input_tokens":600,"input_tokens":400,"output_tokens":80}}`
	if encode(answer) != want {
		t.Fatalf("answer =\n%s\nwant\n%s", encode(answer), want)
	}
	// A cut stream for a non-streaming caller writes nothing: the request falls back.
	recorder, _, err = serve(t, reply, upstreamResponses(`{"type":"response.output_text.delta","delta":"a"}`), false)
	if !errors.Is(err, ErrNotServed) || recorder.Body.Len() != 0 {
		t.Fatalf("a cut stream for a non-streaming caller = %v %q", err, recorder.Body.String())
	}
}

// The reasoning a Responses host wrote goes back to that host next turn and
// to no other, Anthropic included.
func TestResponsesReasoningRoundTrip(t *testing.T) {
	_, reply := mustRequest(t, Messages, Responses, `{"model":"claude-opus-5-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		Options{Model: "gpt-6.1-sol", Route: "chatgpt"})
	recorder, _, _ := serve(t, reply, fullResponsesStream, false)
	signature := ""
	for _, event := range anthropicEvents(t, recorder.Body.String()) {
		if delta, ok := event.data["delta"].(map[string]any); ok && delta["type"] == "signature_delta" {
			signature = delta["signature"].(string)
		}
	}
	next := `{"model":"claude-opus-5-5","max_tokens":10,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"thinking","thinking":"thinking hard","signature":` + encode(signature) + `},{"type":"text","text":"hello world"}]},{"role":"user","content":"more"}]}`
	same := rawRequest(t, Messages, Responses, next, Options{Model: "gpt-6.1-sol", Route: "chatgpt"})
	if !strings.Contains(same, `"encrypted_content":"ENC-XYZ"`) {
		t.Fatalf("the same route lost its reasoning: %s", same)
	}
	if other := rawRequest(t, Messages, Responses, next, Options{Model: "gpt-6.1-sol", Route: "openai"}); strings.Contains(other, "ENC-XYZ") {
		t.Fatalf("another route got the reasoning: %s", other)
	}
	if native := string(AnthropicNative([]byte(next))); strings.Contains(native, "ENC-XYZ") || !strings.Contains(native, "hello world") {
		t.Fatalf("Anthropic got the reasoning: %s", native)
	}
}
