package translate

import (
	"errors"
	"strings"
	"testing"
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
