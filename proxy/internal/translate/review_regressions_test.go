package translate

import (
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
		{"responses to responses", Responses, Responses, codexBody("m", "high", "", userHello), "max", "gpt-6-sol", "reasoning", `{"effort":"xhigh","summary":"auto"}`},
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
