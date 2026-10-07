package translate

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- requests and answers ---------------------------------------------------

func mustRequest(t *testing.T, from, to, body string, opts Options) (map[string]any, *Reply) {
	t.Helper()
	out, reply, err := Request(from, to, []byte(body), opts)
	if err != nil {
		t.Fatalf("Request(%s, %s): %v", from, to, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("upstream body is not JSON: %v\n%s", err, out)
	}
	return decoded, reply
}

func rawRequest(t *testing.T, from, to, body string, opts Options) string {
	t.Helper()
	out, _, err := Request(from, to, []byte(body), opts)
	if err != nil {
		t.Fatalf("Request(%s, %s): %v", from, to, err)
	}
	return string(out)
}

// cutReader ends with a broken body, the way a dropped upstream connection does.
type cutReader struct{}

func (cutReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

// serve runs reply.Serve over an upstream answering payload; cut breaks the
// body after it.
func serve(t *testing.T, reply *Reply, payload string, cut bool) (*httptest.ResponseRecorder, Usage, error) {
	t.Helper()
	body := io.Reader(strings.NewReader(payload))
	if cut {
		body = io.MultiReader(body, cutReader{})
	}
	recorder := httptest.NewRecorder()
	usage, err := reply.Serve(recorder, &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(body)})
	return recorder, usage, err
}

func decode(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, raw)
	}
	return out
}

func encode(value any) string { return string(mustJSON(value)) }

// --- SSE --------------------------------------------------------------------

// sse renders data frames; Anthropic frames also carry their event line.
func sse(frames ...string) string {
	var out strings.Builder
	for _, frame := range frames {
		var typed struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(frame), &typed) == nil && typed.Type != "" && !strings.HasPrefix(typed.Type, "response.") {
			out.WriteString("event: " + typed.Type + "\n")
		}
		out.WriteString("data: " + frame + "\n\n")
	}
	return out.String()
}

func chatStream(chunks ...string) string { return sse(chunks...) + "data: [DONE]\n\n" }

// anthropicText is a complete Anthropic stream answering `text`.
func anthropicText(text string) string {
	return sse(
		`{"type":"message_start","message":{"id":"msg_1","model":"claude-upstream","usage":{"input_tokens":1000,"output_tokens":1,"cache_read_input_tokens":200,"cache_creation_input_tokens":10}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"`+text+`"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":50}}`,
		`{"type":"message_stop"}`,
	)
}

// anthropicEvent is one parsed Anthropic SSE frame.
type anthropicEvent struct {
	name string
	data map[string]any
}

func anthropicEvents(t *testing.T, body string) []anthropicEvent {
	t.Helper()
	var out []anthropicEvent
	scanner := bufio.NewScanner(strings.NewReader(body))
	var name string
	for scanner.Scan() {
		line := scanner.Text()
		if rest, ok := strings.CutPrefix(line, "event: "); ok {
			name = rest
			continue
		}
		if rest, ok := strings.CutPrefix(line, "data: "); ok {
			var data map[string]any
			if err := json.Unmarshal([]byte(rest), &data); err != nil {
				t.Fatalf("event data %q: %v", rest, err)
			}
			if data["type"] != name {
				t.Fatalf("event %q carries type %v", name, data["type"])
			}
			out = append(out, anthropicEvent{name: name, data: data})
		}
	}
	return out
}

func eventNames(events []anthropicEvent) string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.name)
	}
	return strings.Join(names, ",")
}

// --- Codex's Responses parser -----------------------------------------------

// A small Go port of the rules Codex's Responses SSE parser applies
// (codex-rs codex-api/src/sse/responses.rs process_responses_event,
// responses_error.rs parse_failed_response, protocol/src/models.rs
// ResponseItem, response_item_id.rs is_prefixed).
type codexTurn struct {
	responseID string
	model      string
	items      []map[string]any
	text       string
	reasoning  string
	usage      map[string]any
	// failure is "" on response.completed, response.error.code on
	// response.failed, or "stream" for anything Codex reports as a stream
	// error (closed early, bad completed payload, incomplete).
	failure string
	message string
}

// codexAccept runs a response body through Codex's parser rules. Every item
// Codex would drop fails the test.
func codexAccept(t *testing.T, body string) codexTurn {
	t.Helper()
	var turn codexTurn
	for _, frame := range strings.Split(body, "\n\n") {
		var data strings.Builder
		for _, line := range strings.Split(frame, "\n") {
			if rest, ok := strings.CutPrefix(line, "data:"); ok {
				data.WriteString(strings.TrimPrefix(rest, " "))
			}
		}
		if data.Len() == 0 {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(data.String()), &event) != nil {
			continue // Codex skips an unparseable event
		}
		response, _ := event["response"].(map[string]any)
		switch event["type"] {
		case "response.created":
			if response == nil {
				t.Fatalf("response.created without response: %s", data.String())
			}
			turn.responseID, _ = response["id"].(string)
			turn.model, _ = response["model"].(string)
		case "response.output_item.added", "response.output_item.done":
			item, _ := event["item"].(map[string]any)
			codexCheckItem(t, item)
			if event["type"] == "response.output_item.done" {
				turn.items = append(turn.items, item)
			}
		case "response.output_text.delta":
			delta, ok := event["delta"].(string)
			if !ok {
				t.Fatalf("output_text.delta without delta: %s", data.String())
			}
			turn.text += delta
		case "response.reasoning_summary_text.delta":
			delta, ok := event["delta"].(string)
			if _, indexed := event["summary_index"].(float64); !ok || !indexed {
				t.Fatalf("reasoning_summary_text.delta needs delta and summary_index: %s", data.String())
			}
			turn.reasoning += delta
		case "response.failed":
			turn.failure = "stream"
			if failure, ok := response["error"].(map[string]any); ok {
				if code, ok := failure["code"].(string); ok {
					turn.failure = code
				}
				turn.message, _ = failure["message"].(string)
			}
			return turn
		case "response.incomplete":
			turn.failure = "stream"
			return turn
		case "response.completed":
			id, ok := response["id"].(string)
			if !ok {
				turn.failure = "stream"
				return turn
			}
			turn.responseID = id
			turn.model, _ = response["model"].(string)
			if usage, ok := response["usage"].(map[string]any); ok {
				for _, field := range []string{"input_tokens", "output_tokens", "total_tokens"} {
					if _, ok := usage[field].(float64); !ok {
						t.Fatalf("usage.%s missing: %v", field, usage)
					}
				}
				turn.usage = usage
			}
			return turn
		}
	}
	turn.failure = "stream" // "stream closed before response.completed"
	return turn
}

// codexCheckItem applies ResponseItem's required fields and the item-id rule.
func codexCheckItem(t *testing.T, item map[string]any) {
	t.Helper()
	if item == nil {
		t.Fatal("output item event without item")
	}
	if id, present := item["id"]; present {
		text, _ := id.(string)
		prefix, suffix, ok := strings.Cut(text, "_")
		if !ok || prefix == "" || suffix == "" {
			t.Fatalf("item id %q is not prefix_suffix", text)
		}
	}
	required := map[string][]string{
		"message":          {"role"},
		"function_call":    {"name", "arguments", "call_id"},
		"custom_tool_call": {"call_id", "name", "input"},
		"reasoning":        {},
	}
	fields, known := required[fmt.Sprint(item["type"])]
	if !known {
		t.Fatalf("unexpected item type %v", item["type"])
	}
	for _, field := range fields {
		if _, ok := item[field].(string); !ok {
			t.Fatalf("%v item: %s must be a string: %v", item["type"], field, item)
		}
	}
	if item["type"] == "reasoning" {
		if encrypted, present := item["encrypted_content"]; present && encrypted != nil {
			if _, ok := encrypted.(string); !ok {
				t.Fatalf("encrypted_content must be a string or null: %v", item)
			}
		}
	}
}

func itemsOfType(turn codexTurn, kind string) []map[string]any {
	out := []map[string]any{}
	for _, item := range turn.items {
		if item["type"] == kind {
			out = append(out, item)
		}
	}
	return out
}

// codexBody is a Codex-shaped request: store false, stream true, the whole
// input, reasoning.encrypted_content included, tool_choice auto.
func codexBody(model, effort, tools, input string) string {
	if tools == "" {
		tools = "[]"
	}
	return `{"model":"` + model + `","instructions":"You are Codex.","input":` + input + `,
	  "tools":` + tools + `,"tool_choice":"auto","parallel_tool_calls":true,
	  "reasoning":{"effort":"` + effort + `","summary":"auto"},"store":false,"stream":true,
	  "include":["reasoning.encrypted_content"],"prompt_cache_key":"thread-1"}`
}

const userHello = `[{"type":"message","role":"developer","content":[{"type":"input_text","text":"sandbox: workspace-write"}]},
  {"type":"message","role":"user","content":[{"type":"input_text","text":"say hello"}]}]`
