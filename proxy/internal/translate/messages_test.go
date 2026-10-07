package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestSupported(t *testing.T) {
	for _, tc := range []struct {
		from, to string
		want     bool
	}{
		{Messages, Messages, true}, {Chat, Chat, true}, {Responses, Responses, true},
		{Messages, Chat, true}, {Responses, Messages, true}, {Responses, Chat, true}, {Messages, Responses, true},
		{Chat, Messages, false}, {Chat, Responses, false}, {"x", Chat, false},
	} {
		if got := Supported(tc.from, tc.to); got != tc.want {
			t.Errorf("Supported(%s, %s) = %v", tc.from, tc.to, got)
		}
		if _, _, err := Request(tc.from, tc.to, []byte(`{"model":"m","messages":[],"input":"hi"}`), Options{Model: "m"}); (err == nil) != tc.want {
			t.Errorf("Request(%s, %s) err = %v", tc.from, tc.to, err)
		}
	}
}

// --- Messages -> chat: requests ---------------------------------------------

func TestMessagesToChatRequest(t *testing.T) {
	body := `{
      "model":"auto","max_tokens":1024,"temperature":0.2,"top_p":0.9,
      "stop_sequences":["STOP"],"stream":true,
      "thinking":{"type":"enabled","budget_tokens":4096},
      "metadata":{"user_id":"{\"session_id\":\"s-1\"}"},
      "system":[{"type":"text","text":"be terse"},{"type":"text","text":"and kind","cache_control":{"type":"ephemeral"}}],
      "tools":[{"name":"Bash","description":"run","input_schema":{"type":"object","properties":{"cmd":{"type":"string"}}}}],
      "tool_choice":{"type":"any"},
      "context_management":{"edits":[]},"safeguards":[],
      "messages":[
        {"role":"user","content":[{"type":"text","text":"look at this"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAB"}},{"type":"image","source":{"type":"url","url":"https://x/y.png"}}]},
        {"role":"assistant","content":[{"type":"thinking","thinking":"private","signature":"SIG"},{"type":"text","text":"running it"},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"cmd":"ls"}}]},
        {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","is_error":true,"content":[{"type":"text","text":"boom"}]},{"type":"text","text":"and now?"}]}
      ]}`
	sent, reply := mustRequest(t, Messages, Chat, body, Options{Model: "openai/gpt-5.6", Dialect: "openrouter", Route: "openrouter"})
	if !reply.Stream() {
		t.Fatal("caller asked for a stream")
	}
	if sent["model"] != "openai/gpt-5.6" || sent["max_tokens"] != 1024.0 || sent["temperature"] != 0.2 || sent["top_p"] != 0.9 || sent["stream"] != true {
		t.Fatalf("scalars = %v", sent)
	}
	if fmt.Sprint(sent["stop"]) != "[STOP]" || fmt.Sprint(sent["reasoning"]) != "map[max_tokens:4096]" || sent["tool_choice"] != "required" {
		t.Fatalf("stop/reasoning/tool_choice = %v %v %v", sent["stop"], sent["reasoning"], sent["tool_choice"])
	}
	for _, dropped := range []string{"user", "metadata", "context_management", "safeguards", "output_config", "thinking", "system"} {
		if _, ok := sent[dropped]; ok {
			t.Fatalf("%s reached the chat upstream: %v", dropped, sent)
		}
	}
	if sent["stream_options"].(map[string]any)["include_usage"] != true || sent["usage"].(map[string]any)["include"] != true {
		t.Fatalf("usage in the stream: %v", sent)
	}
	tools := sent["tools"].([]any)
	function := tools[0].(map[string]any)["function"].(map[string]any)
	if len(tools) != 1 || function["name"] != "Bash" || function["parameters"].(map[string]any)["type"] != "object" {
		t.Fatalf("tools = %v", tools)
	}
	var messages []openAIMessage
	_ = json.Unmarshal(mustJSON(sent["messages"]), &messages)
	if len(messages) != 5 {
		t.Fatalf("messages = %s", mustJSON(sent["messages"]))
	}
	if messages[0].Role != "system" || messages[0].Content != "be terse\nand kind" {
		t.Fatalf("system = %+v", messages[0])
	}
	images := encode(messages[1].Content)
	if !strings.Contains(images, "data:image/png;base64,AAAB") || !strings.Contains(images, "https://x/y.png") {
		t.Fatalf("images = %s", images)
	}
	assistant := messages[2]
	if assistant.Content != "running it" || assistant.ReasoningContent != "" || strings.Contains(encode(assistant), "private") {
		t.Fatalf("another model's thinking reached the chat upstream: %+v", assistant)
	}
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ID != "toolu_1" || assistant.ToolCalls[0].Function.Arguments != `{"cmd":"ls"}` {
		t.Fatalf("tool_calls = %+v", assistant.ToolCalls)
	}
	if result := messages[3]; result.Role != "tool" || result.ToolCallID != "toolu_1" || result.Content != "Error: boom" {
		t.Fatalf("tool_result = %+v", result)
	}
	if follow := messages[4]; follow.Role != "user" || follow.Content != "and now?" {
		t.Fatalf("text after the tool result = %+v", follow)
	}
}

func TestMessagesToChatToolChoice(t *testing.T) {
	for in, want := range map[string]string{
		`{"type":"auto"}`: `"auto"`, `{"type":"any"}`: `"required"`, `{"type":"none"}`: `"none"`,
		`{"type":"tool","name":"Read"}`: `{"function":{"name":"Read"},"type":"function"}`,
	} {
		sent, _ := mustRequest(t, Messages, Chat, `{"model":"m","messages":[{"role":"user","content":"hi"}],"tool_choice":`+in+`}`, Options{Model: "m"})
		if got := encode(sent["tool_choice"]); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
}

func TestMessagesServerToolsAreRejected(t *testing.T) {
	_, _, err := Request(Messages, Chat, []byte(`{"model":"m","messages":[],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`), Options{Model: "m"})
	if err == nil || !strings.Contains(err.Error(), "server tool") {
		t.Fatalf("server tool accepted: %v", err)
	}
}

// One golden per chat effort dialect for a Messages caller, and the effort a
// caller's own output_config or thinking stands for when Options names none.
func TestMessagesToChatEffortDialects(t *testing.T) {
	body := func(extra string) string {
		return `{"model":"m","max_tokens":4000,` + extra + `"messages":[{"role":"user","content":"hi"}]}`
	}
	for _, tc := range []struct {
		name, extra, effort, dialect string
		want                         map[string]any // field -> encoded JSON, "" = absent
	}{
		{"openai_chat", "", "high", "", map[string]any{"reasoning_effort": `"high"`, "reasoning": "", "thinking": ""}},
		{"openrouter", "", "high", "openrouter", map[string]any{"reasoning": `{"effort":"high"}`, "reasoning_effort": ""}},
		{"deepseek", "", "medium", "deepseek", map[string]any{"thinking": `{"type":"enabled"}`, "reasoning_effort": `"high"`}},
		{"deepseek off", "", "none", "deepseek", map[string]any{"thinking": `{"type":"disabled"}`, "reasoning_effort": ""}},
		{"toggle", "", "low", "toggle", map[string]any{"thinking": `{"type":"enabled"}`, "reasoning_effort": ""}},
		{"qwen", "", "medium", "qwen", map[string]any{"enable_thinking": `true`, "thinking_budget": `2000`}},
		{"none", "", "high", "none", map[string]any{"reasoning_effort": "", "reasoning": "", "thinking": ""}},
		{"caller output_config", `"output_config":{"effort":"xhigh"},`, "", "", map[string]any{"reasoning_effort": `"xhigh"`, "output_config": ""}},
		{"caller adaptive thinking", `"thinking":{"type":"adaptive"},`, "", "", map[string]any{"reasoning_effort": `"medium"`, "reasoning": ""}},
		{"caller budget on openrouter", `"thinking":{"type":"enabled","budget_tokens":2048},`, "", "openrouter", map[string]any{"reasoning": `{"max_tokens":2048}`}},
		{"caller thinking off", `"thinking":{"type":"disabled"},`, "", "", map[string]any{"reasoning_effort": "", "reasoning": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent, _ := mustRequest(t, Messages, Chat, body(tc.extra), Options{Model: "m", Effort: tc.effort, Dialect: tc.dialect})
			for field, want := range tc.want {
				got := ""
				if value, ok := sent[field]; ok {
					got = encode(value)
				}
				if got != want {
					t.Fatalf("%s = %s, want %s (body %v)", field, got, want, sent)
				}
			}
		})
	}
}

// A chat route's own reasoning goes back to that route and model as
// reasoning_content; nobody else's does.
func TestMessagesToChatReplaysOnlyTheRoutesOwnReasoning(t *testing.T) {
	own := signaturePrefix + "v1:deepseek:deepseek-v4-flash"
	body := `{"model":"auto","messages":[{"role":"user","content":"go"},
	  {"role":"assistant","content":[{"type":"thinking","thinking":"step one","signature":"` + own + `"},{"type":"thinking","thinking":"other","signature":"caveman:v1:kimi:k2"},{"type":"tool_use","id":"call_1","name":"Bash","input":{}}]},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"a b"}]}]}`
	for _, tc := range []struct {
		opts Options
		want string
	}{
		{Options{Model: "deepseek-v4-flash", Route: "deepseek", Replay: true}, "step one"},
		{Options{Model: "deepseek-v4-flash", Route: "deepseek"}, ""},
		{Options{Model: "deepseek-v4-pro", Route: "deepseek", Replay: true}, ""},
	} {
		sent := rawRequest(t, Messages, Chat, body, tc.opts)
		var parsed struct {
			Messages []openAIMessage `json:"messages"`
		}
		_ = json.Unmarshal([]byte(sent), &parsed)
		if got := parsed.Messages[1].ReasoningContent; got != tc.want || strings.Contains(sent, "other") {
			t.Fatalf("%+v: reasoning_content = %q\n%s", tc.opts, got, sent)
		}
	}
}

func TestChatParameterRules(t *testing.T) {
	sent, _ := mustRequest(t, Messages, Chat, `{"model":"m","max_tokens":4000,"stop_sequences":["X"],"messages":[{"role":"user","content":"hi"}]}`,
		Options{Model: "gpt-6", MaxTokensField: "max_completion_tokens", DropParams: []string{"stop"}})
	if sent["max_completion_tokens"] != 4000.0 || sent["max_tokens"] != nil || sent["stop"] != nil || sent["stream_options"] != nil {
		t.Fatalf("body = %v", sent)
	}
}

// --- Messages -> chat: answers ------------------------------------------------

func TestChatAnswerToMessages(t *testing.T) {
	_, reply := mustRequest(t, Messages, Chat, `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`,
		Options{Model: "gpt-5.6-upstream", Shown: "openai/gpt-5.6", Route: "openai"})
	recorder, usage, err := serve(t, reply, `{"id":"cmpl-9","model":"gpt-5.6-upstream","choices":[{"finish_reason":"tool_calls","message":{
      "reasoning":"thinking out loud","content":"on it",
      "tool_calls":[{"id":"call_1","function":{"name":"Read","arguments":"{\"path\":\"x\"}"}},
                    {"id":"functions.Bash:0","function":{"name":"Bash","arguments":"not json"}},
                    {"function":{"name":"Read","arguments":"{}"}}]}}],
      "usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":40,"cache_write_tokens":10}}}`, false)
	if err != nil || recorder.Header().Get("content-type") != "application/json" {
		t.Fatalf("err %v, headers %v", err, recorder.Header())
	}
	if usage != (Usage{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 40, CacheWriteTokens: 10}) {
		t.Fatalf("usage = %+v", usage)
	}
	answer := decode(t, recorder.Body.String())
	if answer["id"] != "msg_cmpl-9" || answer["type"] != "message" || answer["role"] != "assistant" || answer["model"] != "openai/gpt-5.6" ||
		answer["stop_reason"] != "tool_use" || answer["stop_sequence"] != nil {
		t.Fatalf("envelope = %v", answer)
	}
	if encode(answer["usage"]) != `{"cache_creation_input_tokens":0,"cache_read_input_tokens":40,"input_tokens":60,"output_tokens":20}` {
		t.Fatalf("usage = %v", answer["usage"])
	}
	content := answer["content"].([]any)
	thinking := content[0].(map[string]any)
	if len(content) != 5 || thinking["type"] != "thinking" || thinking["signature"] != "caveman:v1:openai:gpt-5.6-upstream" || content[1].(map[string]any)["text"] != "on it" {
		t.Fatalf("content = %v", content)
	}
	calls := content[2:]
	if calls[0].(map[string]any)["input"].(map[string]any)["path"] != "x" || calls[1].(map[string]any)["input"].(map[string]any)["_raw"] != "not json" {
		t.Fatalf("tool inputs = %v", calls)
	}
	for _, call := range calls {
		id := call.(map[string]any)["id"].(string)
		if !anthropicCallID.MatchString(id) || len(id) > 40 {
			t.Fatalf("tool id %q is not one Anthropic accepts", id)
		}
	}
	for finish, want := range map[string]string{"stop": "end_turn", "length": "max_tokens", "content_filter": "end_turn", "tool_calls": "tool_use", "": "end_turn"} {
		if got := anthropicStopReason(finish); got != want {
			t.Fatalf("%s -> %s, want %s", finish, got, want)
		}
	}
}

// The chat stream fixture: reasoning, text, a tool call whose arguments
// arrive in two pieces, then usage with cache reads, then [DONE].
func TestChatStreamToMessages(t *testing.T) {
	stream := chatStream(
		`{"id":"cmpl-s","choices":[{"delta":{"role":"assistant","reasoning_content":"let me "}}]}`,
		`{"id":"cmpl-s","choices":[{"delta":{"reasoning_content":"see"}}]}`,
		`{"id":"cmpl-s","choices":[{"delta":{"content":"hello "}}]}`,
		`{"id":"cmpl-s","choices":[{"delta":{"content":"world"}}]}`,
		`{"id":"cmpl-s","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Read","arguments":"{\"pa"}}]}}]}`,
		`{"id":"cmpl-s","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"x\"}"}}]}}]}`,
		`{"id":"cmpl-s","choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_cache_hit_tokens":30}}`,
	)
	_, reply := mustRequest(t, Messages, Chat, `{"model":"auto","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		Options{Model: "deepseek-v4-flash", Shown: "auto", Route: "deepseek"})
	recorder, usage, err := serve(t, reply, stream, false)
	if err != nil || recorder.Header().Get("content-type") != "text/event-stream" {
		t.Fatalf("err %v headers %v", err, recorder.Header())
	}
	if usage != (Usage{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 30}) {
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
	if message := events[0].data["message"].(map[string]any); message["model"] != "auto" || message["id"] != "msg_cmpl-s" {
		t.Fatalf("message_start = %v", message)
	}
	if signature := events[4].data["delta"].(map[string]any); signature["type"] != "signature_delta" || signature["signature"] != "caveman:v1:deepseek:deepseek-v4-flash" {
		t.Fatalf("thinking signature = %v", signature)
	}
	tool := events[10].data["content_block"].(map[string]any)
	if tool["type"] != "tool_use" || tool["name"] != "Read" || tool["id"] != "call_1" || events[10].data["index"] != 2.0 {
		t.Fatalf("tool block = %v", events[10].data)
	}
	partial := events[11].data["delta"].(map[string]any)["partial_json"].(string) + events[12].data["delta"].(map[string]any)["partial_json"].(string)
	if partial != `{"path":"x"}` {
		t.Fatalf("partial json = %q", partial)
	}
	final := events[14].data
	if final["delta"].(map[string]any)["stop_reason"] != "tool_use" || encode(final["usage"]) != `{"cache_creation_input_tokens":0,"cache_read_input_tokens":30,"input_tokens":70,"output_tokens":20}` {
		t.Fatalf("message_delta = %v", final)
	}
}

func TestChatStreamStopReasonsErrorsAndCuts(t *testing.T) {
	_, reply := mustRequest(t, Messages, Chat, `{"model":"auto","stream":true,"messages":[{"role":"user","content":"hi"}]}`, Options{Model: "m"})
	for _, tc := range []struct {
		name, stream string
		cut          bool
		last         string // the last event
		stop         string
		failed       bool
	}{
		{"length", chatStream(`{"choices":[{"delta":{"content":"a"},"finish_reason":"length"}]}`), false, "message_stop", "max_tokens", false},
		{"stop", chatStream(`{"choices":[{"delta":{"content":"a"},"finish_reason":"stop"}]}`), false, "message_stop", "end_turn", false},
		{"done only", chatStream(`{"choices":[{"delta":{"content":"a"}}]}`), false, "message_stop", "end_turn", false},
		{"error chunk", sse(`{"choices":[{"delta":{"content":"a"}}]}`, `{"error":{"type":"overloaded_error","message":"busy"}}`), false, "error", "", false},
		{"clean EOF without finish", sse(`{"choices":[{"delta":{"content":"a"}}]}`), false, "error", "", true},
		{"broken body", sse(`{"choices":[{"delta":{"content":"a"}}]}`), true, "error", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder, _, err := serve(t, reply, tc.stream, tc.cut)
			if (err != nil) != tc.failed {
				t.Fatalf("err = %v", err)
			}
			events := anthropicEvents(t, recorder.Body.String())
			last := events[len(events)-1]
			if last.name != tc.last || strings.Contains(recorder.Body.String(), "message_stop") != (tc.last == "message_stop") {
				t.Fatalf("events = %s", eventNames(events))
			}
			if tc.stop != "" && events[len(events)-2].data["delta"].(map[string]any)["stop_reason"] != tc.stop {
				t.Fatalf("stop = %v", events[len(events)-2].data)
			}
			if tc.name == "error chunk" && last.data["error"].(map[string]any)["type"] != "overloaded_error" {
				t.Fatalf("error = %v", last.data)
			}
		})
	}
}

// A chat upstream's tool call ids reach Claude Code as ids Anthropic accepts:
// a foreign one rewritten, a missing one minted, parallel calls distinct, a
// late id ignored.
func TestChatStreamToolIDsAreAnthropicSafe(t *testing.T) {
	stream := chatStream(
		`{"id":"c9","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"functions.Bash:0","type":"function","function":{"name":"Bash","arguments":""}}]}}]}`,
		`{"id":"c9","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`,
		`{"id":"c9","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"type":"function","function":{"name":"Read","arguments":"{}"}}]}}]}`,
		`{"id":"c9","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"late_id"}]}}]}`,
		`{"id":"c9","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	_, reply := mustRequest(t, Messages, Chat, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`, Options{Model: "m"})
	recorder, _, _ := serve(t, reply, stream, false)
	body := recorder.Body.String()
	ids := regexp.MustCompile(`"id":"(call_[^"]*)"`).FindAllStringSubmatch(body, -1)
	if len(ids) != 2 || ids[0][1] == ids[1][1] || strings.Contains(body, "functions.Bash:0") || strings.Contains(body, "late_id") {
		t.Fatalf("ids = %v\n%s", ids, body)
	}
	for _, id := range ids {
		if !anthropicCallID.MatchString(id[1]) || len(id[1]) > 40 {
			t.Fatalf("id %q", id[1])
		}
	}
	// Parallel calls an upstream sends all at index 0 stay separate blocks.
	recorder, _, _ = serve(t, reply, chatStream(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"Bash","arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_b","type":"function","function":{"name":"Bash","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`), false)
	if strings.Count(recorder.Body.String(), `"type":"tool_use"`) != 2 {
		t.Fatalf("parallel calls merged:\n%s", recorder.Body.String())
	}
}

// --- Messages -> Messages ------------------------------------------------------

// A non-Anthropic Messages host's stream: signatures namespaced, an unsigned
// thinking block still marked as the host's, model renamed, usage read.
func TestMessagesHostStreamIsNamespaced(t *testing.T) {
	stream := sse(
		`{"type":"message_start","message":{"id":"m","model":"deepseek-v4-flash","usage":{"input_tokens":10,"cache_read_input_tokens":90,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"DSSIG"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"redacted_thinking","data":"OPAQUE"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"call_9","name":"Bash","input":{"model":"keep me"}}}`,
		`{"type":"content_block_stop","index":3}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		`{"type":"message_stop"}`,
	)
	_, reply := mustRequest(t, Messages, Messages, `{"model":"auto","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		Options{Model: "deepseek-v4-flash", Shown: "auto", Route: "deepseek"})
	recorder, usage, err := serve(t, reply, stream, false)
	answer := recorder.Body.String()
	if err != nil || usage != (Usage{InputTokens: 100, OutputTokens: 5, CacheReadTokens: 90}) {
		t.Fatalf("err %v usage %+v", err, usage)
	}
	if !strings.Contains(answer, `"signature":"caveman:deepseek:DSSIG"`) || strings.Contains(answer, `"signature":"DSSIG"`) ||
		!strings.Contains(answer, `"data":"caveman:deepseek:OPAQUE"`) {
		t.Fatalf("the host's signatures must come back namespaced:\n%s", answer)
	}
	if !strings.Contains(answer, `{"delta":{"signature":"caveman:deepseek:","type":"signature_delta"},"index":1,"type":"content_block_delta"}`) {
		t.Fatalf("an unsigned thinking block must still be marked as the host's:\n%s", answer)
	}
	if !strings.Contains(answer, `"model":"auto"`) || strings.Contains(answer, `"model":"deepseek-v4-flash"`) || !strings.Contains(answer, `"model":"keep me"`) {
		t.Fatalf("only the answer's model is renamed:\n%s", answer)
	}
	events := anthropicEvents(t, answer)
	if eventNames(events)[:len("message_start,content_block_start")] != "message_start,content_block_start" || events[len(events)-1].name != "message_stop" {
		t.Fatalf("events = %s", eventNames(events))
	}
	// Anthropic's own stream is relayed byte for byte (model aside).
	_, native := mustRequest(t, Messages, Messages, `{"model":"claude-opus-5-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`, Options{Model: "claude-opus-5-5"})
	recorder, _, _ = serve(t, native, anthropicText("hi"), false)
	if recorder.Body.String() != strings.Replace(anthropicText("hi"), `"model":"claude-upstream"`, `"model":"claude-opus-5-5"`, 1) {
		t.Fatalf("native relay changed bytes:\n%s", recorder.Body.String())
	}
	// A cut relay ends in an error event and an error.
	recorder, _, err = serve(t, native, sse(`{"type":"message_start","message":{"usage":{"input_tokens":1}}}`), true)
	if events := anthropicEvents(t, recorder.Body.String()); err == nil || events[len(events)-1].name != "error" {
		t.Fatalf("cut relay: %v\n%s", err, recorder.Body.String())
	}
}

// An Opus -> DeepSeek -> Opus session stays valid: DeepSeek gets back its own
// thinking (signature restored), Anthropic never sees DeepSeek's, and Opus's
// own signed thinking survives the detour byte for byte.
func TestMessagesNamespaceRoundTrip(t *testing.T) {
	_, reply := mustRequest(t, Messages, Messages, `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`,
		Options{Model: "deepseek-v4-flash", Shown: "deepseek/deepseek-v4-flash", Route: "deepseek"})
	recorder, usage, _ := serve(t, reply, `{"id":"m2","type":"message","role":"assistant","model":"deepseek-v4-flash","content":[{"type":"thinking","thinking":"ds plan","signature":"DSSIG"},{"type":"tool_use","id":"call_9","name":"Bash","input":{"command":"ls"}}],"stop_reason":"tool_use","usage":{"input_tokens":5,"output_tokens":5,"cache_creation_input_tokens":7}}`, false)
	answer := decode(t, recorder.Body.String())
	dsTurn := encode(answer["content"])
	if answer["model"] != "deepseek/deepseek-v4-flash" || !strings.Contains(dsTurn, `"signature":"caveman:deepseek:DSSIG"`) {
		t.Fatalf("answer = %s", recorder.Body.String())
	}
	if usage != (Usage{InputTokens: 12, OutputTokens: 5, CacheWriteTokens: 7}) {
		t.Fatalf("usage = %+v", usage)
	}
	opusTurn := `[{"type":"thinking","thinking":"opus plan","signature":"SIG1"},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]`
	history := `{"model":"auto","max_tokens":8000,"thinking":{"type":"adaptive"},"metadata":{"user_id":"u"},"messages":[{"role":"user","content":"investigate"},` +
		`{"role":"assistant","content":` + opusTurn + `},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"a"}]},` +
		`{"role":"assistant","content":` + dsTurn + `},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_9","content":"b"}]}]}`

	toDeepSeek := rawRequest(t, Messages, Messages, history, Options{Model: "deepseek-v4-flash", Route: "deepseek"})
	if !strings.Contains(toDeepSeek, `"signature":"DSSIG"`) || strings.Contains(toDeepSeek, "caveman:") || strings.Contains(toDeepSeek, "SIG1") ||
		strings.Contains(toDeepSeek, "opus plan") || strings.Contains(toDeepSeek, `"metadata"`) || !strings.Contains(toDeepSeek, `"model":"deepseek-v4-flash"`) {
		t.Fatalf("DeepSeek must get its own signature back and nobody else's: %s", toDeepSeek)
	}
	toKimi := rawRequest(t, Messages, Messages, history, Options{Model: "kimi-k2", Route: "kimi"})
	if strings.Contains(toKimi, "DSSIG") || strings.Contains(toKimi, "SIG1") {
		t.Fatalf("another host got foreign thinking: %s", toKimi)
	}
	toAnthropic := rawRequest(t, Messages, Messages, history, Options{Model: "claude-opus-5-5", Route: "anthropic"})
	if strings.Contains(toAnthropic, "DSSIG") || strings.Contains(toAnthropic, "ds plan") || !strings.Contains(toAnthropic, `"signature":"SIG1"`) ||
		!strings.Contains(toAnthropic, `"metadata"`) || strings.Contains(toAnthropic, "functions.") {
		t.Fatalf("Anthropic-bound body: %s", toAnthropic)
	}
	if got := AnthropicNative([]byte(history)); strings.Contains(string(got), "DSSIG") || !strings.Contains(string(got), "SIG1") {
		t.Fatalf("AnthropicNative: %s", got)
	}
}

// Reasoning another provider produced is signed caveman:…, and a native
// Anthropic forward strips it (and unsigned thinking) while keeping Claude's
// own signed blocks byte for byte; manual thinking goes for the one request
// whose final tool turn lost its thinking.
func TestTranslatedThinkingIsStrippedBeforeAnthropic(t *testing.T) {
	gptTurn, _ := chatToAnthropic([]byte(`{"id":"x","choices":[{"finish_reason":"tool_calls","message":{"reasoning":"think","tool_calls":[{"id":"call_1","type":"function","function":{"name":"Bash","arguments":"{}"}}]}}]}`), "gpt", "caveman:v1:openai:gpt")
	content := encode(decode(t, string(gptTurn))["content"])
	raw := `{"model":"auto","thinking":{"type":"enabled","budget_tokens":2048},"system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}],"messages":[` +
		`{"role":"user","content":"fix it"},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"claude","signature":"SIG"},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"unsigned","signature":""}]},` +
		`{"role":"user","content":"more"},` +
		`{"role":"assistant","content":` + content + `},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"ok"}]}]}`
	for _, out := range []string{string(AnthropicNative([]byte(raw))), rawRequest(t, Messages, Messages, raw, Options{Model: "claude-opus-4-6"})} {
		if strings.Contains(out, "caveman:") || strings.Contains(out, `"thinking":"think"`) || strings.Contains(out, "unsigned") {
			t.Fatalf("foreign thinking reached Anthropic: %s", out)
		}
		if !strings.Contains(out, `"signature":"SIG"`) || !strings.Contains(out, `"cache_control":{"type":"ephemeral"}`) {
			t.Fatalf("Claude's own thinking or cache_control was lost: %s", out)
		}
		if strings.Contains(out, `"budget_tokens"`) {
			t.Fatalf("thinking must be off when the final tool turn has no thinking block left: %s", out)
		}
		if !strings.Contains(out, "(reasoning omitted)") {
			t.Fatalf("an emptied message must keep a placeholder: %s", out)
		}
	}
	// Adaptive thinking relaxes the rule: it (and its display) stays.
	adaptive := `{"model":"auto","thinking":{"type":"adaptive","display":"summarized"},"messages":[{"role":"user","content":"x"},{"role":"assistant","content":[{"type":"thinking","thinking":"gpt","signature":"caveman:v1:openai:gpt"},{"type":"tool_use","id":"call_1","name":"Bash","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"ok"}]}]}`
	if out := string(AnthropicNative([]byte(adaptive))); !strings.Contains(out, `"thinking":{"type":"adaptive","display":"summarized"}`) || strings.Contains(out, "caveman:") {
		t.Fatalf("adaptive thinking must survive, foreign blocks must not: %s", out)
	}
}

// AnthropicNative hands back the very slice it got when nothing is foreign.
func TestAnthropicNativeIsByteIdenticalOnCleanBodies(t *testing.T) {
	for _, body := range []string{
		`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`,
		`{ "model" : "claude-opus-5-5", "thinking": {"type": "adaptive"}, "messages": [{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"SIG"},{"type":"tool_use","id":"toolu_1","name":"x","input":{}}]}] }`,
		// manual thinking, Anthropic's own tool turn: parsed, nothing to change
		`{"model":"m","thinking":{"type":"enabled","budget_tokens":2048},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"SIG"},{"type":"tool_use","id":"toolu_1","name":"x","input":{}}]}]}`,
	} {
		in := []byte(body)
		out := AnthropicNative(in)
		if &out[0] != &in[0] || !bytes.Equal(out, in) {
			t.Fatalf("clean body changed: %s", out)
		}
	}
}

// Effort on a Messages wire: output_config.effort plus a thinking shape the
// model takes.
func TestMessagesEffortFitsTheModel(t *testing.T) {
	for _, tc := range []struct {
		model, thinking, effort string
		wantThinking, wantOut   string
	}{
		{"claude-opus-5-5", `{"type":"enabled","budget_tokens":4000}`, "high", `{"type":"adaptive"}`, `{"effort":"high"}`},
		{"claude-opus-5-5", `{"type":"adaptive"}`, "minimal", `{"type":"adaptive"}`, `{"effort":"low"}`},
		{"claude-sonnet-4-5", `{"type":"adaptive"}`, "high", `{"budget_tokens":5000,"type":"enabled"}`, ``},
		{"anthropic/claude-sonnet-4.6", `{"type":"enabled","budget_tokens":4000}`, "high", `{"type":"enabled","budget_tokens":4000}`, `{"effort":"high"}`},
		{"claude-opus-5-5", `{"type":"disabled"}`, "high", ``, `{"effort":"high"}`},
		{"claude-sonnet-5", `{"type":"disabled"}`, "max", `{"type":"disabled"}`, `{"effort":"max"}`},
		{"deepseek-v4-flash", `{"type":"adaptive"}`, "high", `{"type":"adaptive"}`, `{"effort":"high"}`},
	} {
		body := `{"model":"auto","max_tokens":10000,"thinking":` + tc.thinking + `,"output_config":{"effort":"low"},"reasoning_effort":"low","messages":[{"role":"user","content":"hi"}]}`
		sent, _ := mustRequest(t, Messages, Messages, body, Options{Model: tc.model, Effort: tc.effort, Route: "anthropic"})
		thinking, out := "", ""
		if sent["thinking"] != nil {
			thinking = encode(sent["thinking"])
		}
		if sent["output_config"] != nil {
			out = encode(sent["output_config"])
		}
		if thinking != strings.ReplaceAll(tc.wantThinking, `{"type":"enabled","budget_tokens":4000}`, `{"budget_tokens":4000,"type":"enabled"}`) || out != tc.wantOut || sent["reasoning_effort"] != nil {
			t.Errorf("%s %s %s: thinking %s output_config %s", tc.model, tc.thinking, tc.effort, thinking, out)
		}
	}
	// Dialect none: the body's own effort stays.
	sent, _ := mustRequest(t, Messages, Messages, `{"model":"m","output_config":{"effort":"low"},"messages":[]}`, Options{Model: "local", Effort: "high", Dialect: "none", Route: "local"})
	if encode(sent["output_config"]) != `{"effort":"low"}` {
		t.Fatalf("dialect none: %v", sent)
	}
}

// Another host's tool ids are rewritten for Anthropic, consistently, so calls
// and results still pair; a valid body is returned as it came.
func TestAnthropicToolIDs(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"functions.Bash:0","name":"Bash","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"functions.Bash:0","content":"ok"}]}]}`
	sent := rawRequest(t, Messages, Messages, body, Options{Model: "claude-opus-5-5", Route: "anthropic"})
	ids := regexp.MustCompile(`"(?:id|tool_use_id)":"([^"]*)"`).FindAllStringSubmatch(sent, -1)
	if len(ids) != 2 || ids[0][1] != ids[1][1] || !anthropicCallID.MatchString(ids[0][1]) {
		t.Fatalf("ids = %v\n%s", ids, sent)
	}
	if kept := rawRequest(t, Messages, Messages, body, Options{Model: "kimi", Route: "openrouter"}); !strings.Contains(kept, "functions.Bash:0") {
		t.Fatalf("a non-Anthropic host keeps its ids: %s", kept)
	}
	clean := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1"}]}]}`)
	if out := anthropicToolIDs(clean); &out[0] != &clean[0] {
		t.Fatal("a valid body was rewritten")
	}
}

// A silent upstream gets a ping (Messages) or a keepalive comment
// (Responses) so the caller's connection is proved alive.
func TestSilentUpstreamIsKeptAlive(t *testing.T) {
	defer func(previous time.Duration) { pingInterval = previous }(pingInterval)
	pingInterval = 5 * time.Millisecond
	slow := func(first, rest string) io.Reader {
		reader, writer := io.Pipe()
		go func() {
			_, _ = io.WriteString(writer, first)
			time.Sleep(50 * time.Millisecond)
			_, _ = io.WriteString(writer, rest)
			_ = writer.Close()
		}()
		return reader
	}
	for _, tc := range []struct{ from, want string }{{Messages, "event: ping\n"}, {Responses, ": keepalive\n\n"}} {
		_, reply := mustRequest(t, tc.from, Chat, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}],"input":"hi"}`, Options{Model: "m"})
		recorder := httptest.NewRecorder()
		upstream := &http.Response{StatusCode: 200, Body: io.NopCloser(slow(`data: {"choices":[{"delta":{"content":"a"}}]}`+"\n\n", chatStream(`{"choices":[{"delta":{"content":"b"},"finish_reason":"stop"}]}`)))}
		if _, err := reply.Serve(recorder, upstream); err != nil || !strings.Contains(recorder.Body.String(), tc.want) {
			t.Fatalf("%s: %v\n%s", tc.from, err, recorder.Body.String())
		}
	}
}
