package translate

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A stream cut mid tool call never closes that call: only the error goes out.
func TestCutToolCallIsNeverClosed(t *testing.T) {
	_, reply := mustRequest(t, Messages, Chat, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`, Options{Model: "g"})
	stream := sse(`{"id":"c","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Bash","arguments":"{\"cmd\":\"rm -"}}]}}]}`)
	recorder, _, err := serve(t, reply, stream, true)
	events := anthropicEvents(t, recorder.Body.String())
	if err == nil || events[len(events)-1].name != "error" || strings.Contains(eventNames(events), "content_block_stop") {
		t.Fatalf("err %v events %s", err, eventNames(events))
	}
	_, codex := mustRequest(t, Responses, Chat, codexBody("m", "low", "", userHello), Options{Model: "g"})
	recorder, _, err = serve(t, codex, stream, true)
	if err == nil || strings.Contains(recorder.Body.String(), "response.output_item.done") || !strings.Contains(recorder.Body.String(), "response.failed") {
		t.Fatalf("err %v body %s", err, recorder.Body.String())
	}
}

// A 2xx that fails before any content writes nothing, so the request can fall back.
func TestFailureBeforeContentIsNotServed(t *testing.T) {
	for name, tc := range map[string]struct{ to, stream string }{
		"responses failed first":   {Responses, upstreamResponses(`{"type":"response.created","response":{"id":"r"}}`, `{"type":"response.failed","response":{"id":"r","error":{"code":"server_error","message":"no"}}}`)},
		"responses ends empty":     {Responses, upstreamResponses(`{"type":"response.created","response":{"id":"r"}}`)},
		"chat error chunk first":   {Chat, sse(`{"error":{"type":"overloaded_error","message":"busy"}}`)},
		"chat ends without finish": {Chat, sse(`{"id":"c","choices":[]}`)},
	} {
		for _, stream := range []bool{true, false} {
			body := `{"model":"m","stream":` + map[bool]string{true: "true", false: "false"}[stream] + `,"messages":[{"role":"user","content":"hi"}]}`
			_, reply := mustRequest(t, Messages, tc.to, body, Options{Model: "g", Route: "chatgpt"})
			if tc.to == Chat && !stream {
				continue // a non-streamed chat answer is one JSON body: TestChatAnswerFailuresAreNotServed
			}
			recorder, _, err := serve(t, reply, tc.stream, false)
			if !errors.Is(err, ErrNotServed) || recorder.Body.Len() != 0 {
				t.Errorf("%s (stream %v): err %v body %q", name, stream, err, recorder.Body.String())
			}
		}
	}
}

func TestChatAnswerFailuresAreNotServed(t *testing.T) {
	_, reply := mustRequest(t, Messages, Chat, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, Options{Model: "g"})
	for _, answer := range []string{`{"error":{"message":"quota"}}`, `{"id":"c","choices":[]}`, `<html>bad gateway</html>`} {
		recorder, _, err := serve(t, reply, answer, false)
		if !errors.Is(err, ErrNotServed) || recorder.Body.Len() != 0 {
			t.Errorf("%s: err %v body %q", answer, err, recorder.Body.String())
		}
	}
}

// Another Responses host's encrypted reasoning goes back to that host only.
func TestResponsesHostReasoningIsRouteTagged(t *testing.T) {
	stream := sse(
		`{"type":"response.created","response":{"id":"r1"}}`,
		`{"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"CHATGPT-BLOB"}}`,
		`{"type":"response.completed","response":{"id":"r1","output":[{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"CHATGPT-BLOB"}]}}`,
	)
	_, reply := mustRequest(t, Responses, Responses, codexBody("m", "low", "", userHello), Options{Model: "gpt-6-sol", Route: "chatgpt"})
	recorder, _, err := serve(t, reply, stream, false)
	if err != nil || strings.Contains(recorder.Body.String(), `"encrypted_content":"CHATGPT-BLOB"`) {
		t.Fatalf("err %v: the host's blob went out bare:\n%s", err, recorder.Body.String())
	}
	item := itemsOfType(codexAccept(t, recorder.Body.String()), "reasoning")[0]
	next := `{"model":"m","store":false,"input":[{"type":"message","role":"user","content":"hi"},` + encode(item) + `,{"type":"message","role":"user","content":"more"}]}`
	if same := rawRequest(t, Responses, Responses, next, Options{Model: "gpt-6-sol", Route: "chatgpt"}); !strings.Contains(same, `"encrypted_content":"CHATGPT-BLOB"`) {
		t.Fatalf("the same host lost its reasoning: %s", same)
	}
	for _, other := range []string{
		rawRequest(t, Responses, Responses, next, Options{Model: "gpt-6-sol", Route: "openai"}),
		rawRequest(t, Responses, Responses, next, Options{Model: "gpt-6-luna", Route: "opencode-go/gpt-6-luna"}),
		string(OpenAINative([]byte(next))),
	} {
		if strings.Contains(other, "encrypted_content") {
			t.Fatalf("another host got the reasoning: %s", other)
		}
	}
	// OpenAI's own API keeps its own reasoning untagged.
	_, native := mustRequest(t, Responses, Responses, codexBody("m", "low", "", userHello), Options{Model: "gpt-6-sol", Route: "openai"})
	if recorder, _, _ := serve(t, native, stream, false); !strings.Contains(recorder.Body.String(), `"encrypted_content":"CHATGPT-BLOB"`) {
		t.Fatalf("OpenAI's own blob was wrapped: %s", recorder.Body.String())
	}
}

func TestResponsesContentFilterIsARefusal(t *testing.T) {
	_, reply := mustRequest(t, Messages, Responses, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`, Options{Model: "g", Route: "chatgpt"})
	recorder, _, _ := serve(t, reply, upstreamResponses(`{"type":"response.output_text.delta","delta":"a"}`, `{"type":"response.incomplete","response":{"id":"r","incomplete_details":{"reason":"content_filter"}}}`), false)
	if !strings.Contains(recorder.Body.String(), `"stop_reason":"refusal"`) {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}

// A silent upstream stays behind the gate for gateHold only: after it the
// next silence tick commits the headers and pings, and a failure after that
// reaches the caller instead of falling back.
func TestGateCommitsOnSilenceAfterTheHold(t *testing.T) {
	defer func(ping, hold time.Duration) { pingInterval, gateHold = ping, hold }(pingInterval, gateHold)
	pingInterval = 5 * time.Millisecond
	slow := func(first, rest string) *http.Response {
		reader, writer := io.Pipe()
		go func() {
			_, _ = io.WriteString(writer, first)
			time.Sleep(60 * time.Millisecond)
			_, _ = io.WriteString(writer, rest)
			_ = writer.Close()
		}()
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(reader)}
	}
	roleOnly := sse(`{"id":"c","choices":[{"delta":{"role":"assistant","content":""}}]}`)
	failure := sse(`{"error":{"type":"overloaded_error","message":"busy"}}`)
	for _, tc := range []struct {
		name  string
		reply func() *Reply
		first string
		want  string
	}{
		{"messages from chat", func() *Reply {
			_, r := mustRequest(t, Messages, Chat, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`, Options{Model: "g"})
			return r
		}, roleOnly, "event: ping\n"},
		{"responses relay", func() *Reply {
			return Relay(Responses, []byte(`{"stream":true}`), "m")
		}, upstreamResponses(`{"type":"response.created","response":{"id":"r"}}`), ": keepalive\n\n"},
	} {
		gateHold = time.Hour
		recorder := httptest.NewRecorder()
		if _, err := tc.reply().Serve(recorder, slow(tc.first, failure)); !errors.Is(err, ErrNotServed) || recorder.Body.Len() != 0 {
			t.Fatalf("%s within the hold: err %v body %q", tc.name, err, recorder.Body.String())
		}
		gateHold = time.Millisecond
		recorder = httptest.NewRecorder()
		_, err := tc.reply().Serve(recorder, slow(tc.first, failure))
		if errors.Is(err, ErrNotServed) || !strings.Contains(recorder.Body.String(), tc.want) || !strings.Contains(recorder.Body.String(), "busy") {
			t.Fatalf("%s after the hold: err %v body %q", tc.name, err, recorder.Body.String())
		}
	}
}

// A chat chunk that only opens the message is not content: a failure after
// it still falls back.
func TestChatRoleChunkIsNotContent(t *testing.T) {
	stream := sse(`{"id":"c","choices":[{"delta":{"role":"assistant","content":""}}]}`, `{"error":{"type":"overloaded_error","message":"busy"}}`)
	_, chat := mustRequest(t, Chat, Chat, `{"model":"m","stream":true,"messages":[]}`, Options{Model: "g"})
	for name, reply := range map[string]*Reply{"chat to chat": chat, "relay": Relay(Chat, []byte(`{"stream":true}`), "m")} {
		recorder, _, err := serve(t, reply, stream, false)
		if !errors.Is(err, ErrNotServed) || recorder.Body.Len() != 0 {
			t.Errorf("%s: err %v body %q", name, err, recorder.Body.String())
		}
	}
}

// A relayed response.failed after content ends the answer: the caller gets
// it once, with nothing appended.
func TestRelayedFailureIsTerminal(t *testing.T) {
	stream := upstreamResponses(`{"type":"response.created","response":{"id":"r"}}`, `{"type":"response.output_text.delta","delta":"a"}`,
		`{"type":"response.failed","response":{"id":"r","error":{"code":"server_error","message":"no"}}}`)
	recorder, _, err := serve(t, Relay(Responses, []byte(`{"stream":true}`), "m"), stream, false)
	if !errors.Is(err, ErrUpstreamFailed) || strings.Count(recorder.Body.String(), "response.failed") != 1 || strings.Contains(recorder.Body.String(), "ended early") {
		t.Fatalf("err %v body %s", err, recorder.Body.String())
	}
}

// The answered effort goes out in the upstream's own shape on every
// translator direction, clamped to what that shape takes; a caller that
// turned thinking off does not change the clamping.
func TestAnsweredEffortOnEveryDirection(t *testing.T) {
	messages := `{"model":"m","max_tokens":4000,"messages":[{"role":"user","content":"hi"}]}`
	messagesOff := `{"model":"m","max_tokens":4000,"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hi"}]}`
	chat := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	for _, tc := range []struct {
		name, from, to, body, effort, model string
		field, want                         string
	}{
		{"messages to chat", Messages, Chat, messages, "low", "g", "reasoning_effort", `"low"`},
		{"messages to chat, thinking off", Messages, Chat, messagesOff, "low", "g", "reasoning_effort", `"low"`},
		{"messages to responses", Messages, Responses, messages, "max", "g", "reasoning", `{"effort":"xhigh","summary":"auto"}`},
		{"messages to responses, thinking off", Messages, Responses, messagesOff, "max", "g", "reasoning", `{"effort":"xhigh","summary":"auto"}`},
		{"messages to messages", Messages, Messages, messages, "low", "claude-opus-5-5", "output_config", `{"effort":"low"}`},
		{"messages to messages, thinking off", Messages, Messages, messagesOff, "low", "claude-opus-5-5", "output_config", `{"effort":"low"}`},
		{"responses to messages", Responses, Messages, codexBody("m", "high", "", userHello), "low", "claude-opus-5-5", "output_config", `{"effort":"low"}`},
		{"responses to chat", Responses, Chat, codexBody("m", "high", "", userHello), "low", "g", "reasoning_effort", `"low"`},
		{"responses to responses", Responses, Responses, codexBody("m", "high", "", userHello), "max", "gpt-6.1-sol", "reasoning", `{"effort":"xhigh","summary":"auto"}`},
		{"responses to responses, catalog lists max", Responses, Responses, codexBody("m", "high", "", userHello), "max", "gpt-6-sol", "reasoning", `{"effort":"max","summary":"auto"}`},
		{"chat to chat", Chat, Chat, chat, "low", "g", "reasoning_effort", `"low"`},
		{"messages to chat at max", Messages, Chat, messages, "max", "g", "reasoning_effort", `"xhigh"`},
		{"chat to chat at max", Chat, Chat, chat, "max", "g", "reasoning_effort", `"xhigh"`},
		{"responses to chat at max", Responses, Chat, codexBody("m", "high", "", userHello), "max", "g", "reasoning_effort", `"xhigh"`},
	} {
		sent, _ := mustRequest(t, tc.from, tc.to, tc.body, Options{Model: tc.model, Effort: tc.effort, Route: "fireworks"})
		if got := encode(sent[tc.field]); got != tc.want {
			t.Errorf("%s: %s = %s, want %s (body %s)", tc.name, tc.field, got, tc.want, encode(sent))
		}
	}
}

// Lines without content arriving steadily (OpenRouter's processing comments,
// Anthropic pings) do not keep the headers back past gateHold.
func TestGateHoldIsBoundedUnderSteadyHeldLines(t *testing.T) {
	defer func(ping, hold time.Duration) { pingInterval, gateHold = ping, hold }(pingInterval, gateHold)
	pingInterval = time.Hour
	busy := func(comment, failure string) *http.Response {
		reader, writer := io.Pipe()
		go func() {
			for range 30 {
				_, _ = io.WriteString(writer, comment)
				time.Sleep(2 * time.Millisecond)
			}
			_, _ = io.WriteString(writer, failure)
			_ = writer.Close()
		}()
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(reader)}
	}
	chatFailure := sse(`{"error":{"type":"overloaded_error","message":"busy"}}`)
	for _, tc := range []struct {
		name, comment, failure string
		reply                  func() *Reply
	}{
		{"messages from chat", ": OPENROUTER PROCESSING\n\n", chatFailure, func() *Reply {
			_, r := mustRequest(t, Messages, Chat, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`, Options{Model: "g"})
			return r
		}},
		{"messages from responses", ": keepalive\n\n", upstreamResponses(`{"type":"response.failed","response":{"id":"r","error":{"code":"server_error","message":"busy"}}}`), func() *Reply {
			_, r := mustRequest(t, Messages, Responses, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`, Options{Model: "g", Route: "chatgpt"})
			return r
		}},
		{"responses from chat", ": OPENROUTER PROCESSING\n\n", chatFailure, func() *Reply {
			_, r := mustRequest(t, Responses, Chat, codexBody("m", "low", "", userHello), Options{Model: "g"})
			return r
		}},
		{"messages relay", sse(`{"type":"ping"}`), sse(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`), func() *Reply {
			return Relay(Messages, []byte(`{"stream":true}`), "m")
		}},
		{"chat from messages", sse(`{"type":"ping"}`), sse(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`), func() *Reply {
			_, r := mustRequest(t, Chat, Messages, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`, Options{Model: "claude-opus-5-5"})
			return r
		}},
		{"chat from responses", ": keepalive\n\n", upstreamResponses(`{"type":"response.failed","response":{"id":"r","error":{"code":"server_error","message":"busy"}}}`), func() *Reply {
			_, r := mustRequest(t, Chat, Responses, `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`, Options{Model: "gpt-6-sol"})
			return r
		}},
	} {
		gateHold = time.Hour
		recorder := httptest.NewRecorder()
		if _, err := tc.reply().Serve(recorder, busy(tc.comment, tc.failure)); !errors.Is(err, ErrNotServed) || recorder.Body.Len() != 0 {
			t.Errorf("%s within the hold: err %v body %q", tc.name, err, recorder.Body.String())
		}
		gateHold = 10 * time.Millisecond
		recorder = httptest.NewRecorder()
		if _, err := tc.reply().Serve(recorder, busy(tc.comment, tc.failure)); errors.Is(err, ErrNotServed) || !strings.Contains(recorder.Body.String(), "busy") {
			t.Errorf("%s after the hold: err %v body %q", tc.name, err, recorder.Body.String())
		}
	}
}

// A translated stream's message_start carries an input estimate (Claude
// Code's context meter reads it), marked as one; message_delta carries the
// host's exact count, and the usage the runtime books is the exact one.
func TestTranslatedMessageStartEstimatesInput(t *testing.T) {
	text := strings.Repeat("abcd", 500) // 2000 characters: 500 tokens, "user" one more
	image := `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + strings.Repeat("A", 40000) + `"}}`
	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"` + text + `"},` + image + `]}]}`
	for name, tc := range map[string]struct {
		to, stream string
	}{
		"chat": {Chat, chatStream(`{"id":"c","choices":[{"delta":{"content":"hi"}}]}`, `{"id":"c","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2140,"completion_tokens":2}}`)},
		"responses": {Responses, upstreamResponses(`{"type":"response.output_text.delta","delta":"hi"}`,
			`{"type":"response.completed","response":{"id":"r","usage":{"input_tokens":2140,"output_tokens":2,"total_tokens":2142}}}`)},
	} {
		_, reply := mustRequest(t, Messages, tc.to, body, Options{Model: "g", Route: "chatgpt"})
		recorder, usage, err := serve(t, reply, tc.stream, false)
		events := anthropicEvents(t, recorder.Body.String())
		start := events[0].data["message"].(map[string]any)["usage"].(map[string]any)["input_tokens"]
		var final any
		for _, event := range events {
			if event.name == "message_delta" {
				final = event.data["usage"].(map[string]any)["input_tokens"]
			}
		}
		if err != nil || start != float64(500+1+1600) || final != float64(2140) || usage.InputTokens != 2140 || recorder.Header().Get("x-caveman-input-tokens") != "estimated" {
			t.Errorf("%s: err %v start %v final %v booked %d header %q", name, err, start, final, usage.InputTokens, recorder.Header().Get("x-caveman-input-tokens"))
		}
	}
	// A non-streamed caller gets the exact count only.
	_, quiet := mustRequest(t, Messages, Chat, strings.Replace(body, `"stream":true`, `"stream":false`, 1), Options{Model: "g"})
	if recorder, _, _ := serve(t, quiet, `{"id":"c","choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2140,"completion_tokens":2}}`, false); recorder.Header().Get("x-caveman-input-tokens") != "" {
		t.Fatalf("non-streamed answer marked: %v", recorder.Header())
	}
}

// The host's own answer id is kept for looking the call up there (its cost):
// OpenRouter's gen- id under a Codex caller's translated resp_ id.
func TestUpstreamIDIsKept(t *testing.T) {
	_, reply := mustRequest(t, Responses, Chat, codexBody("m", "low", "", userHello), Options{Model: "g"})
	recorder, _, err := serve(t, reply, chatStream(`{"id":"gen-123-abc","choices":[{"delta":{"content":"hi"}}]}`, `{"id":"gen-123-abc","choices":[{"delta":{},"finish_reason":"stop"}]}`), false)
	if err != nil || reply.UpstreamID() != "gen-123-abc" || strings.Contains(recorder.Body.String(), "gen-123-abc") {
		t.Fatalf("err %v id %q", err, reply.UpstreamID())
	}
}

// All input cached: message_delta's input_tokens is not 0, so a client that
// keeps message_start's count on a 0 does not add the estimate to the cache.
func TestAllCachedInputLeavesNoEstimateStanding(t *testing.T) {
	_, reply := mustRequest(t, Messages, Chat, `{"model":"m","stream":true,"messages":[{"role":"user","content":"`+strings.Repeat("x", 4000)+`"}]}`, Options{Model: "g"})
	recorder, usage, _ := serve(t, reply, chatStream(`{"id":"c","choices":[{"delta":{"content":"hi"}}]}`,
		`{"id":"c","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":300,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":300}}}`), false)
	for _, event := range anthropicEvents(t, recorder.Body.String()) {
		if event.name == "message_delta" {
			if got := encode(event.data["usage"]); got != `{"cache_creation_input_tokens":0,"cache_read_input_tokens":300,"input_tokens":1,"output_tokens":2}` {
				t.Fatalf("message_delta usage = %s", got)
			}
		}
	}
	if usage.InputTokens != 300 || usage.CacheReadTokens != 300 { // the runtime books the host's own counts
		t.Fatalf("booked usage = %+v", usage)
	}
}

func TestFitEffort(t *testing.T) {
	for _, tc := range []struct{ grammar, model, effort, body, want string }{
		{Messages, "claude-opus-5-5", "minimal", `{}`, "low"},
		{Messages, "claude-opus-5-5", "none", `{}`, ""},
		{Messages, "claude-sonnet-5", "max", `{"thinking":{"type":"disabled"}}`, "max"}, // Sonnet 5 takes thinking off at any effort
		{Messages, "claude-sonnet-5-5", "max", `{"thinking":{"type":"between_tools"}}`, "high"},
		{Messages, "claude-opus-5", "xhigh", `{"thinking":{"type":"disabled"}}`, "high"},
		{Messages, "claude-opus-5", "xhigh", `{"thinking":{"type":"adaptive"}}`, "xhigh"},
		{Responses, "gpt-6.1-sol", "max", ``, "xhigh"},
		{Responses, "gpt-6-sol", "max", ``, "max"},
	} {
		if got := FitEffort(tc.grammar, tc.model, tc.effort, []byte(tc.body)); got != tc.want {
			t.Errorf("FitEffort(%s, %s, %s, %s) = %q, want %q", tc.grammar, tc.model, tc.effort, tc.body, got, tc.want)
		}
	}
}

// A relayed stream cut anywhere inside an event reaches the agent's SDK as
// an API error, never as a parse error on a fragment: after an event line,
// mid data line, after a whole data line, mid event line. The decoders mimic
// @anthropic-ai/sdk core/streaming.js and openai-node's Stream (which Codex's
// eventsource parser matches: a blank line dispatches, data is JSON).
func TestRelayCutInsideAnEventSurfacesAnAPIError(t *testing.T) {
	type shape struct{ name, tail string }
	for _, tc := range []struct {
		grammar, head string
		shapes        []shape
	}{
		{Messages, sse(`{"type":"message_start","message":{"id":"m","model":"x","usage":{"input_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`), []shape{
			{"after the event line", "event: content_block_delta\n"},
			{"mid data line", "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"del"},
			{"mid data line, JSON whole", "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"a\"}}"},
			{"after a whole data line", "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"a\"}}\n"},
			{"mid event line", "event: content_block_del"},
		}},
		{Responses, sse(`{"type":"response.created","response":{"id":"resp_1"}}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[]}}`), []shape{
			{"after the event line", "event: response.output_text.delta\n"},
			{"mid data line", "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"del"},
			{"after a whole data line", "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"a\"}\n"},
			{"mid event line", "event: response.output_te"},
		}},
		{Chat, sse(`{"id":"c","choices":[{"delta":{"content":"a"}}]}`), []shape{
			{"mid data line", "data: {\"id\":\"c\",\"choi"},
			{"after a whole data line", "data: {\"id\":\"c\",\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n"},
		}},
	} {
		for _, shape := range tc.shapes {
			recorder, _, err := serve(t, Relay(tc.grammar, []byte(`{"stream":true}`), "m"), tc.head+shape.tail, true)
			if got := sdkDecode(tc.grammar, recorder.Body.String()); err == nil || !strings.HasPrefix(got, "APIError") {
				t.Errorf("%s %s: err %v, the SDK sees %s\n%s", tc.grammar, shape.name, err, got, recorder.Body.String())
			}
		}
	}
	// A clean end without a final newline is still a whole answer.
	recorder, _, err := serve(t, Relay(Messages, []byte(`{"stream":true}`), "m"), strings.TrimSuffix(anthropicText("hi"), "\n\n"), false)
	if err != nil || !strings.HasSuffix(recorder.Body.String(), `{"type":"message_stop"}`) {
		t.Fatalf("clean end without a newline: err %v\n%s", err, recorder.Body.String())
	}
}

// sdkDecode runs a stream through an SSE decoder the way the agents' SDKs do:
// a blank line dispatches when an event name or data is set, an event's data
// is JSON-parsed, and an error event (Anthropic), an `error` member or
// response.failed (OpenAI) is an API error.
func sdkDecode(grammar, out string) string {
	var event string
	var data []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line != "" {
			if v, ok := strings.CutPrefix(line, "event:"); ok {
				event = strings.TrimSpace(v)
			} else if v, ok := strings.CutPrefix(line, "data:"); ok {
				data = append(data, strings.TrimPrefix(v, " "))
			}
			continue
		}
		if event == "" && len(data) == 0 {
			continue
		}
		payload := strings.Join(data, "\n")
		var parsed map[string]any
		parseErr := json.Unmarshal([]byte(payload), &parsed)
		switch {
		case grammar == Messages && event == "error":
			return "APIError " + payload
		case grammar == Messages && event != "ping" && parseErr != nil:
			return "SyntaxError on " + event + ": " + payload
		case grammar != Messages && payload != "[DONE]" && parseErr != nil:
			return "SyntaxError: " + payload
		case grammar != Messages && (parsed["error"] != nil || parsed["type"] == "response.failed"):
			return "APIError " + payload
		}
		event, data = "", nil
	}
	return "no error surfaced"
}
