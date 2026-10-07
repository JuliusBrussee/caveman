package translate

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const pdfData = "JVBERi0xLjQKJSVFT0YK"

// --- 1. documents in every cross direction ------------------------------------

func TestDocumentsCrossEveryDirection(t *testing.T) {
	doc := func(source string) string {
		return `{"model":"m","max_tokens":100,"messages":[{"role":"user","content":[{"type":"document","title":"spec.pdf","source":` + source + `},{"type":"text","text":"summarize"}]}]}`
	}
	base64Doc := doc(`{"type":"base64","media_type":"application/pdf","data":"` + pdfData + `"}`)
	urlDoc := doc(`{"type":"url","url":"https://example.com/spec.pdf"}`)
	fileDoc := doc(`{"type":"file","file_id":"file_abc"}`)
	textDoc := doc(`{"type":"text","media_type":"text/plain","data":"plain words"}`)

	// Messages -> chat: a file part; text documents as text; by URL refused.
	got, _ := mustRequest(t, Messages, Chat, base64Doc, Options{Model: "g"})
	want := `[{"content":[{"file":{"file_data":"data:application/pdf;base64,` + pdfData + `","filename":"spec.pdf"},"type":"file"},{"text":"summarize","type":"text"}],"role":"user"}]`
	if encode(got["messages"]) != want {
		t.Fatalf("m>c base64 = %s", encode(got["messages"]))
	}
	got, _ = mustRequest(t, Messages, Chat, textDoc, Options{Model: "g"})
	if encode(got["messages"]) != `[{"content":"plain words\nsummarize","role":"user"}]` {
		t.Fatalf("m>c text document = %s", encode(got["messages"]))
	}
	for _, body := range []string{urlDoc, fileDoc} {
		if _, _, err := Request(Messages, Chat, []byte(body), Options{Model: "g"}); err == nil {
			t.Errorf("m>c must refuse %s", body)
		}
	}

	// Messages -> Responses: input_file by data or URL; a Files API one refused.
	got, _ = mustRequest(t, Messages, Responses, base64Doc, Options{Model: "g"})
	if want := `{"file_data":"data:application/pdf;base64,` + pdfData + `","filename":"spec.pdf","type":"input_file"}`; encode(got["input"].([]any)[0].(map[string]any)["content"].([]any)[0]) != want {
		t.Fatalf("m>r base64 = %s", encode(got["input"]))
	}
	got, _ = mustRequest(t, Messages, Responses, urlDoc, Options{Model: "g"})
	if !strings.Contains(encode(got["input"]), `{"file_url":"https://example.com/spec.pdf","type":"input_file"}`) {
		t.Fatalf("m>r url = %s", encode(got["input"]))
	}
	if _, _, err := Request(Messages, Responses, []byte(fileDoc), Options{Model: "g"}); err == nil {
		t.Error("m>r must refuse a Files API document")
	}

	// Responses -> Messages: a document block (base64 or URL, filename as title).
	responsesDoc := func(part string) string {
		return codexBody("m", "low", "", `[{"type":"message","role":"user","content":[`+part+`,{"type":"input_text","text":"summarize"}]}]`)
	}
	got, _ = mustRequest(t, Responses, Messages, responsesDoc(`{"type":"input_file","filename":"spec.pdf","file_data":"data:application/pdf;base64,`+pdfData+`"}`), Options{Model: "claude-opus-5-5"})
	if want := `{"source":{"data":"` + pdfData + `","media_type":"application/pdf","type":"base64"},"title":"spec.pdf","type":"document"}`; encode(got["messages"].([]any)[0].(map[string]any)["content"].([]any)[0]) != want {
		t.Fatalf("r>m base64 = %s", encode(got["messages"]))
	}
	got, _ = mustRequest(t, Responses, Messages, responsesDoc(`{"type":"input_file","file_url":"https://example.com/a.pdf"}`), Options{Model: "claude-opus-5-5"})
	if !strings.Contains(encode(got["messages"]), `{"source":{"type":"url","url":"https://example.com/a.pdf"},"type":"document"}`) {
		t.Fatalf("r>m url = %s", encode(got["messages"]))
	}
	if _, _, err := Request(Responses, Messages, []byte(responsesDoc(`{"type":"input_file","file_id":"file-1"}`)), Options{Model: "claude-opus-5-5"}); err == nil {
		t.Error("r>m must refuse an OpenAI file id")
	}

	// Responses -> chat: a file part; by URL refused.
	got, _ = mustRequest(t, Responses, Chat, responsesDoc(`{"type":"input_file","filename":"spec.pdf","file_data":"data:application/pdf;base64,`+pdfData+`"}`), Options{Model: "g"})
	if !strings.Contains(encode(got["messages"]), `{"file":{"file_data":"data:application/pdf;base64,`+pdfData+`","filename":"spec.pdf"},"type":"file"}`) {
		t.Fatalf("r>c = %s", encode(got["messages"]))
	}
	if _, _, err := Request(Responses, Chat, []byte(responsesDoc(`{"type":"input_file","file_url":"https://example.com/a.pdf"}`)), Options{Model: "g"}); err == nil {
		t.Error("r>c must refuse a file by URL")
	}

	// chat -> Messages and chat -> Responses.
	chatDoc := `{"model":"m","messages":[{"role":"user","content":[{"type":"file","file":{"filename":"spec.pdf","file_data":"data:application/pdf;base64,` + pdfData + `"}},{"type":"text","text":"summarize"}]}]}`
	got, _ = mustRequest(t, Chat, Messages, chatDoc, Options{Model: "claude-opus-5-5"})
	if !strings.Contains(encode(got["messages"]), `{"source":{"data":"`+pdfData+`","media_type":"application/pdf","type":"base64"},"title":"spec.pdf","type":"document"}`) {
		t.Fatalf("c>m = %s", encode(got["messages"]))
	}
	got, _ = mustRequest(t, Chat, Responses, chatDoc, Options{Model: "gpt-6-sol"})
	if !strings.Contains(encode(got["input"]), `{"file_data":"data:application/pdf;base64,`+pdfData+`","filename":"spec.pdf","type":"input_file"}`) {
		t.Fatalf("c>r = %s", encode(got["input"]))
	}
	if _, _, err := Request(Chat, Messages, []byte(strings.Replace(chatDoc, `"file_data":"data:application/pdf;base64,`+pdfData+`"`, `"file_id":"file-1"`, 1)), Options{Model: "claude-opus-5-5"}); err == nil {
		t.Error("c>m must refuse an OpenAI file id")
	}

	// A tool result's image and document follow its tool message on chat
	// (a tool message carries text only); Responses takes them as parts.
	toolDoc := `{"model":"m","max_tokens":100,"messages":[
	  {"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}]},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"read it"},
	    {"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + pdfData + `"}}]},{"type":"text","text":"and?"}]}]}`
	got, _ = mustRequest(t, Messages, Chat, toolDoc, Options{Model: "g"})
	messages := got["messages"].([]any)
	if len(messages) != 3 || messages[1].(map[string]any)["content"] != "read it" ||
		encode(messages[2].(map[string]any)["content"]) != `[{"file":{"file_data":"data:application/pdf;base64,`+pdfData+`","filename":"document.pdf"},"type":"file"},{"text":"and?","type":"text"}]` {
		t.Fatalf("m>c tool document = %s", encode(messages))
	}
	got, _ = mustRequest(t, Messages, Responses, toolDoc, Options{Model: "g"})
	if !strings.Contains(encode(got["input"]), `"output":[{"text":"read it","type":"input_text"},{"file_data":"data:application/pdf;base64,`+pdfData+`","filename":"document.pdf","type":"input_file"}]`) {
		t.Fatalf("m>r tool document = %s", encode(got["input"]))
	}
}

// --- 2. structured output in every direction ------------------------------------

func TestStructuredOutputEveryDirection(t *testing.T) {
	schema := `{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`
	messagesBody := `{"model":"m","max_tokens":100,"output_config":{"format":{"type":"json_schema","schema":` + schema + `}},"messages":[{"role":"user","content":"json"}]}`
	got, _ := mustRequest(t, Messages, Chat, messagesBody, Options{Model: "g"})
	if want := `{"json_schema":{"name":"output","schema":` + schema + `,"strict":true},"type":"json_schema"}`; encode(got["response_format"]) != canonical(want) {
		t.Fatalf("m>c = %s", encode(got["response_format"]))
	}
	got, _ = mustRequest(t, Messages, Responses, messagesBody, Options{Model: "g"})
	if want := `{"format":{"name":"output","schema":` + schema + `,"strict":true,"type":"json_schema"}}`; encode(got["text"]) != canonical(want) {
		t.Fatalf("m>r = %s", encode(got["text"]))
	}
	// The beta's top-level output_format reads the same.
	beta := `{"model":"m","max_tokens":100,"output_format":{"type":"json_schema","schema":` + schema + `},"messages":[{"role":"user","content":"json"}]}`
	if got, _ = mustRequest(t, Messages, Chat, beta, Options{Model: "g"}); got["response_format"] == nil {
		t.Fatalf("m>c beta output_format = %s", encode(got))
	}

	responsesBody := `{"model":"m","input":"json","reasoning":{"effort":"high"},"text":{"format":{"type":"json_schema","name":"out","schema":` + schema + `,"strict":true}}}`
	got, _ = mustRequest(t, Responses, Messages, responsesBody, Options{Model: "claude-opus-5-5"})
	if want := `{"effort":"high","format":{"schema":` + schema + `,"type":"json_schema"}}`; encode(got["output_config"]) != canonical(want) {
		t.Fatalf("r>m = %s", encode(got["output_config"]))
	}
	got, _ = mustRequest(t, Responses, Chat, strings.Replace(responsesBody, `"type":"json_schema","name":"out","schema":`+schema+`,"strict":true`, `"type":"json_object"`, 1), Options{Model: "g"})
	if encode(got["response_format"]) != `{"type":"json_object"}` {
		t.Fatalf("r>c json_object = %s", encode(got["response_format"]))
	}

	chatBody := `{"model":"m","messages":[{"role":"user","content":"json"}],"response_format":{"type":"json_schema","json_schema":{"name":"out","schema":` + schema + `,"strict":true}}}`
	got, _ = mustRequest(t, Chat, Messages, chatBody, Options{Model: "claude-opus-5-5"})
	if want := `{"format":{"schema":` + schema + `,"type":"json_schema"}}`; encode(got["output_config"]) != canonical(want) {
		t.Fatalf("c>m = %s", encode(got["output_config"]))
	}
	got, _ = mustRequest(t, Chat, Responses, chatBody, Options{Model: "gpt-6-sol"})
	if want := `{"format":{"name":"out","schema":` + schema + `,"strict":true,"type":"json_schema"}}`; encode(got["text"]) != canonical(want) {
		t.Fatalf("c>r = %s", encode(got["text"]))
	}
}

func canonical(raw string) string {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		panic(err)
	}
	return encode(value)
}

// --- 3. disable_parallel_tool_use <-> parallel_tool_calls ------------------------

func TestParallelToolCallsCrossDirections(t *testing.T) {
	messagesBody := `{"model":"m","max_tokens":100,"tools":[{"name":"Bash","input_schema":{"type":"object"}}],"tool_choice":{"type":"auto","disable_parallel_tool_use":true},"messages":[{"role":"user","content":"go"}]}`
	for _, to := range []string{Chat, Responses} {
		got, _ := mustRequest(t, Messages, to, messagesBody, Options{Model: "g"})
		if got["parallel_tool_calls"] != false || got["tool_choice"] != "auto" {
			t.Errorf("m>%s: parallel_tool_calls %v tool_choice %v", to, got["parallel_tool_calls"], got["tool_choice"])
		}
	}
	// No tools, no parallel_tool_calls (OpenAI refuses it alone).
	got, _ := mustRequest(t, Messages, Chat, strings.Replace(messagesBody, `"tools":[{"name":"Bash","input_schema":{"type":"object"}}],`, "", 1), Options{Model: "g"})
	if _, set := got["parallel_tool_calls"]; set {
		t.Errorf("parallel_tool_calls without tools: %v", got)
	}
	chatBody := `{"model":"m","tools":[{"type":"function","function":{"name":"Bash","parameters":{"type":"object"}}}],"parallel_tool_calls":false,"messages":[{"role":"user","content":"go"}]}`
	got, _ = mustRequest(t, Chat, Messages, chatBody, Options{Model: "claude-opus-5-5"})
	if encode(got["tool_choice"]) != `{"disable_parallel_tool_use":true,"type":"auto"}` {
		t.Errorf("c>m tool_choice = %s", encode(got["tool_choice"]))
	}
	got, _ = mustRequest(t, Chat, Responses, chatBody, Options{Model: "gpt-6-sol"})
	if got["parallel_tool_calls"] != false {
		t.Errorf("c>r parallel_tool_calls = %v", got["parallel_tool_calls"])
	}
}

// --- 4. a Responses refusal reaches a Messages caller as a refusal ------------------

func TestResponsesRefusalReachesMessagesCaller(t *testing.T) {
	stream := upstreamResponses(
		`{"type":"response.created","response":{"id":"resp_9"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[]}}`,
		`{"type":"response.refusal.delta","item_id":"msg_1","delta":"I can't "}`,
		`{"type":"response.refusal.delta","item_id":"msg_1","delta":"help."}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"refusal","refusal":"I can't help."}]}}`,
		`{"type":"response.completed","response":{"id":"resp_9","usage":{"input_tokens":10,"output_tokens":3,"total_tokens":13}}}`,
	)
	for _, streamed := range []bool{true, false} {
		body := `{"model":"claude-opus-5-5","stream":` + map[bool]string{true: "true", false: "false"}[streamed] + `,"messages":[{"role":"user","content":"x"}]}`
		_, reply := mustRequest(t, Messages, Responses, body, Options{Model: "gpt-6-sol"})
		recorder, _, err := serve(t, reply, stream, false)
		if err != nil {
			t.Fatal(err)
		}
		if streamed {
			events := anthropicEvents(t, recorder.Body.String())
			text, stop := "", ""
			for _, event := range events {
				if delta, ok := event.data["delta"].(map[string]any); ok {
					if value, ok := delta["text"].(string); ok {
						text += value
					}
					if value, ok := delta["stop_reason"].(string); ok {
						stop = value
					}
				}
			}
			if text != "I can't help." || stop != "refusal" {
				t.Fatalf("stream: text %q stop %q\n%s", text, stop, recorder.Body.String())
			}
			continue
		}
		answer := decode(t, recorder.Body.String())
		if answer["stop_reason"] != "refusal" || encode(answer["content"]) != `[{"text":"I can't help.","type":"text"}]` {
			t.Fatalf("assembled = %s", recorder.Body.String())
		}
	}
	// A refusal that comes whole, without deltas.
	whole := upstreamResponses(
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"refusal","refusal":"No."}]}}`,
		`{"type":"response.completed","response":{"id":"r","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
	_, reply := mustRequest(t, Messages, Responses, `{"model":"c","stream":true,"messages":[{"role":"user","content":"x"}]}`, Options{Model: "gpt-6-sol"})
	recorder, _, _ := serve(t, reply, whole, false)
	if out := recorder.Body.String(); !strings.Contains(out, `"text":"No."`) || !strings.Contains(out, `"stop_reason":"refusal"`) {
		t.Fatalf("whole refusal:\n%s", out)
	}
}

// --- 5. thinking off is effort none, clamped to the model --------------------------

func TestThinkingOffIsEffortNone(t *testing.T) {
	body := `{"model":"m","max_tokens":100,"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"x"}]}`
	got, _ := mustRequest(t, Messages, Responses, body, Options{Model: "gpt-6-sol"})
	if encode(got["reasoning"]) != `{"effort":"none"}` || got["include"] != nil {
		t.Fatalf("m>r = %s", encode(got))
	}
	// A model without "none" runs at its lowest level instead of its default.
	if got := fitOpenAIEffort("none", []string{"minimal", "low", "medium", "high"}); got != "minimal" {
		t.Fatalf("fitOpenAIEffort none = %q", got)
	}
	if got := fitOpenAIEffort("max", []string{"low", "medium", "high", "xhigh"}); got != "xhigh" {
		t.Fatalf("fitOpenAIEffort max = %q", got)
	}
	got, _ = mustRequest(t, Chat, Responses, `{"model":"m","reasoning_effort":"none","messages":[{"role":"user","content":"x"}]}`, Options{Model: "gpt-6-sol"})
	if encode(got["reasoning"]) != `{"effort":"none"}` {
		t.Fatalf("c>r = %s", encode(got["reasoning"]))
	}
}

// --- 6. a Responses caller's refusal and max-tokens stops -----------------------------

func TestResponsesCallerStops(t *testing.T) {
	refusal := sse(
		`{"type":"message_start","message":{"id":"m","usage":{"input_tokens":5}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I can't help with that."}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"refusal"},"usage":{"output_tokens":6}}`,
		`{"type":"message_stop"}`)
	_, reply := mustRequest(t, Responses, Messages, codexBody("m", "low", "", userHello), Options{Model: "claude-opus-5-5"})
	recorder, _, err := serve(t, reply, refusal, false)
	out := recorder.Body.String()
	if err != nil || !strings.Contains(out, "event: response.incomplete") || !strings.Contains(out, `"incomplete_details":{"reason":"content_filter"}`) ||
		!strings.Contains(out, "I can't help with that.") || strings.Contains(out, "response.completed") {
		t.Fatalf("refusal to Codex: err %v\n%s", err, out)
	}
	// max tokens: completed with what arrived (Codex would retry an
	// incomplete one); a non-streamed caller gets status incomplete.
	maxTokens := strings.Replace(refusal, `"stop_reason":"refusal"`, `"stop_reason":"max_tokens"`, 1)
	_, reply = mustRequest(t, Responses, Messages, codexBody("m", "low", "", userHello), Options{Model: "claude-opus-5-5"})
	recorder, _, _ = serve(t, reply, maxTokens, false)
	if turn := codexAccept(t, recorder.Body.String()); turn.failure != "" {
		t.Fatalf("max tokens to Codex: %+v", turn)
	}
	_, reply = mustRequest(t, Responses, Messages, strings.Replace(codexBody("m", "low", "", userHello), `"stream":true`, `"stream":false`, 1), Options{Model: "claude-opus-5-5"})
	recorder, _, _ = serve(t, reply, maxTokens, false)
	answer := decode(t, recorder.Body.String())
	if answer["status"] != "incomplete" || encode(answer["incomplete_details"]) != `{"reason":"max_output_tokens"}` {
		t.Fatalf("non-streamed max tokens = %s", recorder.Body.String())
	}
	// From a chat host: content_filter is a refusal too.
	_, reply = mustRequest(t, Responses, Chat, codexBody("m", "low", "", userHello), Options{Model: "g"})
	recorder, _, _ = serve(t, reply, chatStream(`{"id":"c","choices":[{"delta":{"content":"no"}}]}`, `{"id":"c","choices":[{"delta":{},"finish_reason":"content_filter"}]}`), false)
	if out := recorder.Body.String(); !strings.Contains(out, `"reason":"content_filter"`) || !strings.Contains(out, "response.incomplete") {
		t.Fatalf("chat refusal to Codex:\n%s", out)
	}
}

// --- 7. chat callers on Messages and Responses hosts -----------------------------------

const chatSession = `{"model":"gpt-x","stream":true,"stream_options":{"include_usage":true},"reasoning_effort":"high",
  "tools":[{"type":"function","function":{"name":"Bash","description":"run","parameters":{"type":"object","properties":{"command":{"type":"string"}}}}}],
  "messages":[
    {"role":"system","content":"You are OpenCode."},
    {"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBOR"}}]},
    {"role":"assistant","content":"Running.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"Bash","arguments":"{\"command\":\"ls\"}"}}]},
    {"role":"tool","tool_call_id":"call_1","content":"a.txt"},
    {"role":"user","content":"and?"}]}`

func TestChatToMessagesRequest(t *testing.T) {
	got, reply := mustRequest(t, Chat, Messages, chatSession, Options{Model: "claude-opus-5-5", Route: "anthropic"})
	if !reply.Stream() || !reply.upstreamStream || got["stream"] != true {
		t.Fatal("stream flags")
	}
	if encode(got["system"]) != `[{"cache_control":{"type":"ephemeral"},"text":"You are OpenCode.","type":"text"}]` {
		t.Fatalf("system = %s", encode(got["system"]))
	}
	want := `[{"content":[{"text":"look","type":"text"},{"cache_control":{"type":"ephemeral"},"source":{"data":"iVBOR","media_type":"image/png","type":"base64"},"type":"image"}],"role":"user"},` +
		`{"content":[{"text":"Running.","type":"text"},{"id":"call_1","input":{"command":"ls"},"name":"Bash","type":"tool_use"}],"role":"assistant"},` +
		`{"content":[{"content":"a.txt","tool_use_id":"call_1","type":"tool_result"},{"cache_control":{"type":"ephemeral"},"text":"and?","type":"text"}],"role":"user"}]`
	if encode(got["messages"]) != want {
		t.Fatalf("messages =\n%s\nwant\n%s", encode(got["messages"]), want)
	}
	if encode(got["tools"]) != `[{"cache_control":{"type":"ephemeral"},"description":"run","input_schema":{"properties":{"command":{"type":"string"}},"type":"object"},"name":"Bash"}]` {
		t.Fatalf("tools = %s", encode(got["tools"]))
	}
	if encode(got["thinking"]) != `{"type":"adaptive"}` || encode(got["output_config"]) != `{"effort":"high"}` || got["max_tokens"] != 32000.0 {
		t.Fatalf("effort = %v %v %v", got["thinking"], got["output_config"], got["max_tokens"])
	}
	// A caller's own cache_control is kept and none added.
	own := strings.Replace(chatSession, `{"role":"user","content":"and?"}`, `{"role":"user","content":[{"type":"text","text":"and?","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`, 1)
	got, _ = mustRequest(t, Chat, Messages, own, Options{Model: "claude-opus-5-5"})
	if count := strings.Count(encode(got), "cache_control"); count != 1 || !strings.Contains(encode(got), `"ttl":"1h"`) {
		t.Fatalf("own markers: %d in %s", count, encode(got))
	}
	// Effort none: no thinking; audio is refused.
	got, _ = mustRequest(t, Chat, Messages, strings.Replace(chatSession, `"high"`, `"none"`, 1), Options{Model: "claude-opus-5-5"})
	if got["thinking"] != nil || got["output_config"] != nil {
		t.Fatalf("effort none = %v %v", got["thinking"], got["output_config"])
	}
	audio := `{"model":"m","messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"x","format":"wav"}}]}]}`
	if _, _, err := Request(Chat, Messages, []byte(audio), Options{Model: "claude-opus-5-5"}); err == nil {
		t.Fatal("audio must be refused")
	}
}

func TestChatToResponsesRequest(t *testing.T) {
	got, reply := mustRequest(t, Chat, Responses, chatSession, Options{Model: "gpt-6-sol", Route: "openai"})
	if !reply.upstreamStream || got["stream"] != true || got["store"] != false || got["instructions"] != "You are OpenCode." {
		t.Fatalf("envelope = %s", encode(got))
	}
	want := `[{"content":[{"text":"look","type":"input_text"},{"image_url":"data:image/png;base64,iVBOR","type":"input_image"}],"role":"user","type":"message"},` +
		`{"content":[{"text":"Running.","type":"output_text"}],"role":"assistant","type":"message"},` +
		`{"arguments":"{\"command\":\"ls\"}","call_id":"call_1","name":"Bash","type":"function_call"},` +
		`{"call_id":"call_1","output":"a.txt","type":"function_call_output"},` +
		`{"content":[{"text":"and?","type":"input_text"}],"role":"user","type":"message"}]`
	if encode(got["input"]) != want {
		t.Fatalf("input =\n%s\nwant\n%s", encode(got["input"]), want)
	}
	if encode(got["reasoning"]) != `{"effort":"high","summary":"auto"}` || encode(got["include"]) != `["reasoning.encrypted_content"]` {
		t.Fatalf("reasoning = %s %s", encode(got["reasoning"]), encode(got["include"]))
	}
	if encode(got["tools"]) != `[{"description":"run","name":"Bash","parameters":{"properties":{"command":{"type":"string"}},"type":"object"},"strict":false,"type":"function"}]` {
		t.Fatalf("tools = %s", encode(got["tools"]))
	}
}

// A chat caller's answer from a Messages host: chunks, reasoning as
// reasoning_content plus an envelope, tool calls, usage, [DONE]; the envelope
// goes back only to that host.
func TestMessagesAnswerToChatCaller(t *testing.T) {
	_, reply := mustRequest(t, Chat, Messages, chatSession, Options{Model: "claude-opus-5-5", Shown: "gpt-x", Route: "anthropic"})
	answer := sse(
		`{"type":"message_start","message":{"id":"msg_up","usage":{"input_tokens":40,"cache_read_input_tokens":500,"cache_creation_input_tokens":300}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"plan"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-anthropic"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"ok"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_9","name":"Bash","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"pwd\"}"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":12}}`,
		`{"type":"message_stop"}`)
	recorder, usage, err := serve(t, reply, answer, false)
	out := recorder.Body.String()
	if err != nil || usage != (Usage{InputTokens: 840, OutputTokens: 12, CacheReadTokens: 500, CacheWriteTokens: 300}) {
		t.Fatalf("err %v usage %+v", err, usage)
	}
	turn := chatAccept(t, out)
	if turn.content != "ok" || turn.reasoning != "plan" || turn.finish != "tool_calls" || !turn.done || turn.model != "gpt-x" ||
		len(turn.calls) != 1 || turn.calls[0][0] != "toolu_9" || turn.calls[0][2] != `{"command":"pwd"}` || turn.usage["prompt_tokens"] != 840.0 ||
		!strings.HasPrefix(turn.id, "chatcmpl-") || strings.Contains(out, "msg_up") || len(turn.details) != 1 {
		t.Fatalf("turn = %+v\n%s", turn, out)
	}
	// The next turn: the envelope replays Claude's signed thinking to
	// Anthropic, and to nothing else.
	next := strings.Replace(chatSession, `{"role":"user","content":"and?"}`,
		`{"role":"user","content":"and?"},{"role":"assistant","content":"ok","reasoning_content":"plan","reasoning_details":[`+turn.details[0]+`],"tool_calls":[{"id":"toolu_9","type":"function","function":{"name":"Bash","arguments":"{\"command\":\"pwd\"}"}}]},{"role":"tool","tool_call_id":"toolu_9","content":"/repo"}`, 1)
	toAnthropic := rawRequest(t, Chat, Messages, next, Options{Model: "claude-opus-5-5", Route: "anthropic"})
	if !strings.Contains(toAnthropic, `{"type":"thinking","thinking":"plan","signature":"sig-anthropic"}`) {
		t.Fatalf("Anthropic did not get its thinking back:\n%s", toAnthropic)
	}
	for name, out := range map[string]string{
		"another Messages host": rawRequest(t, Chat, Messages, next, Options{Model: "kimi-k3", Route: "moonshot"}),
		"a Responses host":      rawRequest(t, Chat, Responses, next, Options{Model: "gpt-6-sol", Route: "openai"}),
		"a chat host":           rawRequest(t, Chat, Chat, next, Options{Model: "deepseek-v4-pro", Route: "deepseek"}),
		"OpenAI's chat API":     string(ChatNative([]byte(next))),
	} {
		if strings.Contains(out, "sig-anthropic") || strings.Contains(out, string(envelopeMarker)) || strings.Contains(out, `"plan"`) {
			t.Errorf("%s got Claude's reasoning:\n%s", name, out)
		}
	}
	if clean := []byte(chatSession); &ChatNative(clean)[0] != &clean[0] {
		t.Error("ChatNative must return a body without envelopes as it came")
	}
}

// A chat caller's answer from a Responses host, streamed and assembled; the
// encrypted reasoning comes back tagged with the route.
func TestResponsesAnswerToChatCaller(t *testing.T) {
	_, reply := mustRequest(t, Chat, Responses, chatSession, Options{Model: "gpt-6-sol", Shown: "gpt-x", Route: "chatgpt"})
	recorder, usage, err := serve(t, reply, fullResponsesStream, false)
	if err != nil || usage.InputTokens != 1000 || usage.CacheReadTokens != 600 {
		t.Fatalf("err %v usage %+v", err, usage)
	}
	turn := chatAccept(t, recorder.Body.String())
	if turn.content != "hello world" || turn.reasoning != "thinking hard" || turn.finish != "tool_calls" || len(turn.calls) != 1 ||
		turn.calls[0][0] != "call_abc" || turn.calls[0][2] != `{"path":"x"}` || len(turn.details) != 1 || strings.Contains(recorder.Body.String(), "resp_1") {
		t.Fatalf("turn = %+v\n%s", turn, recorder.Body.String())
	}
	next := strings.Replace(chatSession, `{"role":"user","content":"and?"}`,
		`{"role":"user","content":"and?"},{"role":"assistant","content":"hello world","reasoning_details":[`+turn.details[0]+`]},{"role":"user","content":"more"}`, 1)
	if same := rawRequest(t, Chat, Responses, next, Options{Model: "gpt-6-sol", Route: "chatgpt"}); !strings.Contains(same, `{"type":"reasoning","summary":[],"encrypted_content":"ENC-XYZ"}`) {
		t.Fatalf("the route did not get its reasoning back:\n%s", same)
	}
	if other := rawRequest(t, Chat, Responses, next, Options{Model: "gpt-6-sol", Route: "openai"}); strings.Contains(other, "ENC-XYZ") || strings.Contains(other, `"reasoning","summary"`) {
		t.Fatalf("another route got the reasoning:\n%s", other)
	}
	// Non-streamed: one completion.
	quiet := strings.Replace(chatSession, `"stream":true`, `"stream":false`, 1)
	_, reply = mustRequest(t, Chat, Responses, quiet, Options{Model: "gpt-6-sol", Route: "chatgpt"})
	recorder, _, err = serve(t, reply, fullResponsesStream, false)
	answer := decode(t, recorder.Body.String())
	choice := answer["choices"].([]any)[0].(map[string]any)
	message := choice["message"].(map[string]any)
	if err != nil || answer["object"] != "chat.completion" || choice["finish_reason"] != "tool_calls" || message["content"] != "hello world" ||
		message["reasoning_content"] != "thinking hard" || len(message["tool_calls"].([]any)) != 1 || answer["usage"].(map[string]any)["prompt_tokens"] != 1000.0 {
		t.Fatalf("assembled = %s", recorder.Body.String())
	}
}

// Failures for a chat caller: before content nothing is written (the pool
// falls back); a cut after content never ends with a finish or [DONE].
func TestChatCallerFailures(t *testing.T) {
	_, reply := mustRequest(t, Chat, Messages, chatSession, Options{Model: "claude-opus-5-5"})
	recorder, _, err := serve(t, reply, sse(`{"type":"message_start","message":{"id":"m","usage":{"input_tokens":1}}}`,
		`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`), false)
	if err != ErrNotServed || recorder.Body.Len() != 0 {
		t.Fatalf("before content: err %v body %q", err, recorder.Body.String())
	}
	_, reply = mustRequest(t, Chat, Messages, chatSession, Options{Model: "claude-opus-5-5"})
	recorder, _, err = serve(t, reply, sse(`{"type":"message_start","message":{"id":"m","usage":{"input_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"comm"}}`), true)
	out := recorder.Body.String()
	if err == nil || strings.Contains(out, "[DONE]") || strings.Contains(out, `"finish_reason":"`) || !strings.Contains(out, `"error"`) {
		t.Fatalf("cut: err %v\n%s", err, out)
	}
	_, reply = mustRequest(t, Chat, Responses, chatSession, Options{Model: "gpt-6-sol"})
	recorder, _, err = serve(t, reply, upstreamResponses(`{"type":"response.created","response":{"id":"r"}}`,
		`{"type":"response.failed","response":{"error":{"code":"server_error","message":"boom"}}}`), false)
	if err != ErrNotServed || recorder.Body.Len() != 0 {
		t.Fatalf("Responses before content: err %v body %q", err, recorder.Body.String())
	}
}

// chatTurn is what an OpenAI-compatible client assembles from a stream.
type chatTurn struct {
	id, model, content, reasoning, refusal, finish string
	calls                                          [][3]string // id, name, arguments
	details                                        []string
	usage                                          map[string]any
	done                                           bool
}

func chatAccept(t *testing.T, body string) chatTurn {
	t.Helper()
	var turn chatTurn
	for _, frame := range strings.Split(body, "\n\n") {
		data, ok := strings.CutPrefix(strings.TrimSpace(frame), "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			turn.done = true
			continue
		}
		var chunk struct {
			ID      string         `json:"id"`
			Model   string         `json:"model"`
			Object  string         `json:"object"`
			Usage   map[string]any `json:"usage"`
			Choices []struct {
				Delta struct {
					Content          string            `json:"content"`
					ReasoningContent string            `json:"reasoning_content"`
					Refusal          string            `json:"refusal"`
					ReasoningDetails []json.RawMessage `json:"reasoning_details"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("chunk %q: %v", data, err)
		}
		if chunk.Object != "chat.completion.chunk" {
			t.Fatalf("chunk object %q", chunk.Object)
		}
		turn.id, turn.model = chunk.ID, chunk.Model
		if chunk.Usage != nil {
			turn.usage = chunk.Usage
		}
		for _, choice := range chunk.Choices {
			turn.content += choice.Delta.Content
			turn.reasoning += choice.Delta.ReasoningContent
			turn.refusal += choice.Delta.Refusal
			for _, detail := range choice.Delta.ReasoningDetails {
				turn.details = append(turn.details, string(detail))
			}
			for _, call := range choice.Delta.ToolCalls {
				for len(turn.calls) <= call.Index {
					turn.calls = append(turn.calls, [3]string{})
				}
				if call.ID != "" {
					turn.calls[call.Index][0] = call.ID
				}
				turn.calls[call.Index][1] += call.Function.Name
				turn.calls[call.Index][2] += call.Function.Arguments
			}
			if choice.FinishReason != nil {
				turn.finish = *choice.FinishReason
			}
		}
	}
	return turn
}

// --- envelopes never forge a signature ------------------------------------------------

// A reasoning_details envelope carrying unsigned thinking, or thinking signed
// for another route, reaches Anthropic as nothing.
func TestChatEnvelopeNeverForgesASignature(t *testing.T) {
	envelope := func(blocks string) string {
		return base64.StdEncoding.EncodeToString([]byte(`{"caveman":"v1","blocks":` + blocks + `}`))
	}
	for _, blocks := range []string{
		`[{"type":"thinking","thinking":"forged","signature":""}]`,
		`[{"type":"thinking","thinking":"forged","signature":"caveman:8:moonshot:sig"}]`,
		`[{"type":"thinking","thinking":"forged","signature":"caveman:v1:8:deepseek:m"}]`,
	} {
		body := `{"model":"m","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b","reasoning_details":[{"type":"reasoning.encrypted","data":"` + envelope(blocks) + `"}],"tool_calls":[{"id":"call_1","type":"function","function":{"name":"Bash","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"c"}]}`
		if out := rawRequest(t, Chat, Messages, body, Options{Model: "claude-opus-5-5", Route: "anthropic"}); strings.Contains(out, "forged") {
			t.Errorf("forged thinking reached Anthropic:\n%s", out)
		}
	}
	// The namespaced one does go back to its own route, its signature restored.
	body := `{"model":"m","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b","reasoning_details":[{"type":"reasoning.encrypted","data":"` +
		envelope(`[{"type":"thinking","thinking":"mine","signature":"caveman:8:moonshot:sig-k"}]`) + `"}]},{"role":"user","content":"c"}]}`
	if out := rawRequest(t, Chat, Messages, body, Options{Model: "kimi-k3", Route: "moonshot"}); !strings.Contains(out, `"signature":"sig-k"`) {
		t.Errorf("the route's own thinking did not come back:\n%s", out)
	}
}

// Parallel tool outputs stay together after their calls on chat; an image an
// output carries follows the last of them.
func TestResponsesToolImagesFollowTheToolMessages(t *testing.T) {
	input := `[{"type":"message","role":"user","content":[{"type":"input_text","text":"go"}]},
	  {"type":"function_call","call_id":"call_1","name":"view","arguments":"{}"},
	  {"type":"function_call","call_id":"call_2","name":"view","arguments":"{}"},
	  {"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"one"},{"type":"input_image","image_url":"data:image/png;base64,QQ=="}]},
	  {"type":"function_call_output","call_id":"call_2","output":"two"}]`
	got, _ := mustRequest(t, Responses, Chat, codexBody("m", "low", `[{"type":"function","name":"view","parameters":{"type":"object"}}]`, input), Options{Model: "g"})
	roles := []string{}
	for _, message := range got["messages"].([]any) {
		roles = append(roles, message.(map[string]any)["role"].(string))
	}
	last := got["messages"].([]any)[len(roles)-1].(map[string]any)
	if strings.Join(roles, ",") != "system,user,assistant,tool,tool,user" || encode(last["content"]) != `[{"image_url":{"url":"data:image/png;base64,QQ=="},"type":"image_url"}]` {
		t.Fatalf("messages = %s", encode(got["messages"]))
	}
}

// Review findings, each pinned.
func TestChatCallerReviewFindings(t *testing.T) {
	// A non-streaming chat caller: a slow upstream that fails after the gate
	// hold still writes nothing, so the pool falls back.
	defer func(ping, hold time.Duration) { pingInterval, gateHold = ping, hold }(pingInterval, gateHold)
	pingInterval, gateHold = 10*time.Millisecond, 10*time.Millisecond
	_, reply := mustRequest(t, Chat, Messages, `{"model":"x","stream":false,"messages":[{"role":"user","content":"hi"}]}`, Options{Model: "claude-opus-5-5"})
	reader, writer := io.Pipe()
	go func() {
		_, _ = io.WriteString(writer, sse(`{"type":"message_start","message":{"usage":{"input_tokens":1}}}`))
		time.Sleep(80 * time.Millisecond)
		_, _ = io.WriteString(writer, sse(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`))
		_ = writer.Close()
	}()
	recorder := httptest.NewRecorder()
	if _, err := reply.Serve(recorder, &http.Response{StatusCode: 200, Header: http.Header{}, Body: reader}); err != ErrNotServed || recorder.Body.Len() != 0 {
		t.Fatalf("non-streamed slow failure: err %v body %q", err, recorder.Body.String())
	}
	pingInterval, gateHold = 15*time.Second, 10*time.Second

	// Anthropic takes thinking only with tool_choice auto or none.
	forced := `{"model":"x","reasoning_effort":"high","tool_choice":"required","tools":[{"type":"function","function":{"name":"a","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"hi"}]}`
	if got, _ := mustRequest(t, Chat, Messages, forced, Options{Model: "claude-opus-5-5"}); got["thinking"] != nil || encode(got["tool_choice"]) != `{"type":"any"}` {
		t.Fatalf("c>m forced tool = %s", encode(got))
	}
	codexForced := strings.Replace(codexBody("m", "high", `[{"type":"function","name":"a","parameters":{"type":"object"}}]`, userHello), `"tool_choice":"auto"`, `"tool_choice":"required"`, 1)
	if got, _ := mustRequest(t, Responses, Messages, codexForced, Options{Model: "claude-opus-5-5"}); got["thinking"] != nil {
		t.Fatalf("r>m forced tool = %s", encode(got))
	}

	// An envelope a client relabelled still never reaches a chat host.
	envelope := envelopeOf(`[{"type":"thinking","thinking":"secret","signature":"EqA"}]`, "", "")
	relabelled := `{"model":"x","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b","reasoning_content":"secret","reasoning_details":[{"type":"reasoning.text","data":"` + envelope + `"}]},{"role":"user","content":"c"}]}`
	if out := rawRequest(t, Chat, Chat, relabelled, Options{Model: "deepseek-v4-pro", Route: "deepseek", Dialect: "deepseek"}); strings.Contains(out, envelope) || strings.Contains(out, "secret") {
		t.Fatalf("relabelled envelope reached a chat host: %s", out)
	}

	// Reasoning fields go to a Responses model that reasons, or when asked.
	plain := `{"model":"x","messages":[{"role":"user","content":"a"}]}`
	if got, _ := mustRequest(t, Chat, Responses, plain, Options{Model: "gpt-4.1"}); got["reasoning"] != nil || got["include"] != nil {
		t.Fatalf("non-reasoning model got reasoning fields: %s", encode(got))
	}
	if got, _ := mustRequest(t, Chat, Responses, plain, Options{Model: "gpt-6-sol"}); encode(got["include"]) != `["reasoning.encrypted_content"]` {
		t.Fatalf("reasoning model lost its reasoning include: %s", encode(got))
	}

	// A message-level cache_control lands on that message's last block.
	cached := `{"model":"x","messages":[{"role":"system","content":"s","cache_control":{"type":"ephemeral"}},{"role":"user","content":"a","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`
	got, _ := mustRequest(t, Chat, Messages, cached, Options{Model: "claude-opus-5-5"})
	if encode(got["system"]) != `[{"cache_control":{"type":"ephemeral"},"text":"s","type":"text"}]` ||
		encode(got["messages"]) != `[{"content":[{"cache_control":{"ttl":"1h","type":"ephemeral"},"text":"a","type":"text"}],"role":"user"}]` {
		t.Fatalf("message-level cache_control = %s", encode(got))
	}

	// Manual thinking with a tool turn the client sent back without its
	// thinking: thinking goes for this request (Anthropic would refuse it).
	manual := `{"model":"x","reasoning_effort":"high","max_tokens":8000,"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"a"},{"role":"assistant","content":null,"tool_calls":[{"id":"toolu_01","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"toolu_01","content":"r"}]}`
	if got, _ := mustRequest(t, Chat, Messages, manual, Options{Model: "claude-haiku-4-5"}); got["thinking"] != nil {
		t.Fatalf("manual thinking kept on a bare tool turn: %s", encode(got))
	}
}

// The rest of the review's findings, pinned.
func TestSecondReviewFindings(t *testing.T) {
	// A relayed stream that ends cleanly on an unterminated, non-final line
	// is short of its end: the line is dropped, the error stands alone.
	recorder, _, err := serve(t, Relay(Chat, []byte(`{"stream":true}`), "m"),
		"data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: {\"id\":\"x\",\"choices\":[{\"delta\":{\"content\":\" there\"}}]}", false)
	if got := sdkDecode(Chat, recorder.Body.String()); err == nil || !strings.HasPrefix(got, "APIError") || strings.Contains(recorder.Body.String(), "there") {
		t.Fatalf("unterminated tail: err %v, SDK sees %s\n%s", err, got, recorder.Body.String())
	}

	// Sampling knobs: temperature in Anthropic's 0..1, and not with top_p.
	got, _ := mustRequest(t, Chat, Messages, `{"model":"x","temperature":1.5,"top_p":0.9,"messages":[{"role":"user","content":"hi"}]}`, Options{Model: "claude-opus-5-5"})
	if got["temperature"] != 1.0 || got["top_p"] != nil {
		t.Fatalf("sampling = %v %v", got["temperature"], got["top_p"])
	}
	if got, _ = mustRequest(t, Chat, Messages, `{"model":"x","top_p":0.9,"messages":[{"role":"user","content":"hi"}]}`, Options{Model: "claude-opus-5-5"}); got["top_p"] != 0.9 {
		t.Fatalf("top_p alone = %v", got["top_p"])
	}

	// A tool result whose call was compacted away goes as text.
	orphan := `{"model":"x","messages":[{"role":"user","content":"a"},{"role":"tool","tool_call_id":"call_gone","content":"r"},{"role":"user","content":"b"}]}`
	if out := rawRequest(t, Chat, Messages, orphan, Options{Model: "claude-opus-5-5"}); strings.Contains(out, "tool_result") || !strings.Contains(out, `Tool output (call_gone):\nr`) {
		t.Fatalf("c>m orphan = %s", out)
	}
	if out := rawRequest(t, Chat, Responses, orphan, Options{Model: "gpt-6-sol"}); strings.Contains(out, "function_call_output") || !strings.Contains(out, `Tool output (call_gone):\nr`) {
		t.Fatalf("c>r orphan = %s", out)
	}

	// strict only for a schema OpenAI's strict mode takes.
	loose := `{"model":"m","max_tokens":10,"output_config":{"format":{"type":"json_schema","schema":{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a"],"additionalProperties":false}}},"messages":[{"role":"user","content":"x"}]}`
	if got, _ = mustRequest(t, Messages, Chat, loose, Options{Model: "g"}); !strings.Contains(encode(got["response_format"]), `"strict":false`) {
		t.Fatalf("loose schema = %s", encode(got["response_format"]))
	}

	// A malformed history is never rewritten into a shorter valid one.
	if out, changed := editArray([]byte(`[{"a":1},{"a":2} {"a":3}]`), func([]byte, obj) ([]byte, bool) { return []byte(`{"b":1}`), false }); changed || string(out) != `[{"a":1},{"a":2} {"a":3}]` {
		t.Fatalf("editArray on a malformed array = %s %v", out, changed)
	}
}

// An upstream that fails and then keeps the connection open: before content
// the request still falls back at once (no heartbeat commits the held
// error), after content the failure is ErrUpstreamFailed, on every
// translated direction.
func TestFailureWithALingeringUpstream(t *testing.T) {
	oldHold, oldPing := gateHold, pingInterval
	gateHold, pingInterval = 20*time.Millisecond, 5*time.Millisecond
	defer func() { gateHold, pingInterval = oldHold, oldPing }()
	callers := map[string]string{
		Chat:      `{"model":"x","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		Messages:  `{"model":"m","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		Responses: `{"model":"m","stream":true,"input":"hi"}`,
	}
	targets := map[string]Options{
		Messages:  {Model: "claude-opus-5-5", Route: "anthropic"},
		Responses: {Model: "gpt-6-sol", Route: "openai"},
		Chat:      {Model: "deepseek-v4-flash", Route: "deepseek"},
	}
	failure := map[string]string{
		Messages:  `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`,
		Responses: `{"type":"response.failed","response":{"id":"r","error":{"code":"server_error","message":"busy"}}}`,
		Chat:      `{"error":{"type":"overloaded_error","message":"busy"}}`,
	}
	content := map[string][]string{
		Messages: {`{"type":"message_start","message":{"id":"m","usage":{"input_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`},
		Responses: {`{"type":"response.created","response":{"id":"r"}}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[]}}`,
			`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"delta":"partial"}`},
		Chat: {`{"id":"c","choices":[{"delta":{"content":"partial"}}]}`},
	}
	for from, body := range callers {
		for to, opts := range targets {
			if from == to {
				continue
			}
			for _, afterContent := range []bool{false, true} {
				frames := []string{failure[to]}
				if afterContent {
					frames = append(append([]string{}, content[to]...), failure[to])
				}
				_, reply, err := Request(from, to, []byte(body), opts)
				if err != nil {
					t.Fatal(err)
				}
				upstream, feed := io.Pipe()
				go func() { _, _ = io.WriteString(feed, sse(frames...)) }() // and never closed
				recorder := httptest.NewRecorder()
				done := make(chan error, 1)
				go func() {
					_, err := reply.Serve(recorder, &http.Response{StatusCode: 200, Header: http.Header{}, Body: upstream})
					done <- err
				}()
				select {
				case err = <-done:
				case <-time.After(5 * time.Second):
					t.Fatalf("%s>%s: Serve waited on the upstream after its failure", from, to)
				}
				_ = feed.Close()
				switch {
				case !afterContent && (!errors.Is(err, ErrNotServed) || recorder.Body.Len() != 0):
					t.Errorf("%s>%s before content: err %v, wrote %q", from, to, err, recorder.Body.String())
				case afterContent && (!errors.Is(err, ErrUpstreamFailed) || !strings.Contains(recorder.Body.String(), "partial")):
					t.Errorf("%s>%s after content: err %v, wrote %q", from, to, err, recorder.Body.String())
				}
			}
		}
	}
}

func TestThirdReviewFindings(t *testing.T) {
	const bs = `\` // escapes are spelled out: the test is about how they are written
	// A schema nested past the depth cap costs linear time: not strict, and
	// kept as written.
	deep := deepSchema(3000)
	start := time.Now()
	if strictSchema([]byte(deep)) || string(canonicalJSON(json.RawMessage(deep))) != deep {
		t.Fatal("a schema past the depth cap was walked")
	}
	// The quadratic walk took about a second here; the 5 ms bound is
	// BenchmarkDeepSchema's (a test under -race on a busy machine pauses more).
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("depth 3000 took %v", elapsed)
	}
	shallow := deepSchema(20)
	var want, got any
	_ = json.Unmarshal([]byte(shallow), &want)
	canonicalized := canonicalJSON(json.RawMessage(shallow))
	if !strictSchema([]byte(shallow)) || json.Unmarshal(canonicalized, &got) != nil || encode(got) != encode(want) ||
		!strings.HasPrefix(string(canonicalized), `{"additionalProperties":false,"properties":{"a":{"additionalProperties":false,`) {
		t.Fatalf("a schema under the cap: %s", canonicalized)
	}
	// Duplicate keys: the last counts, escaped or not, as encoding/json reads them.
	if got := string(canonicalJSON(json.RawMessage(`{"b":1,"a":2,"` + bs + `u0061":3}`))); got != `{"`+bs+`u0061":3,"b":1}` {
		t.Fatalf("duplicate keys = %s", got)
	}

	// Strict: a property named "properties", an object without properties.
	for schema, strict := range map[string]bool{
		`{"type":"object","properties":{"properties":{"type":"string"}},"required":["properties"],"additionalProperties":false}`: true,
		`{"type":"object","properties":{"` + bs + `u0061":{"type":"string"}},"required":["a"],"additionalProperties":false}`:     true,
		`{"type":"object","additionalProperties":false}`:                                                                         true,
		`{"type":"object"}`:                          false,
		`{"type":["object","null"],"properties":{}}`: false,
		`{"type":"object","properties":{"a":{"type":"object"}},"required":["a"],"additionalProperties":false}`: false,
	} {
		if strictSchema([]byte(schema)) != strict {
			t.Errorf("strictSchema(%s) = %v", schema, !strict)
		}
	}

	// A key written with an escape is the same key.
	escaped := `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"hi"},{"r` + bs + `u006fle":"assistant","content":"x"},{"role":"user","content":"y"}]}`
	if out := rawRequest(t, Messages, Chat, escaped, Options{Model: "x", Route: "r"}); !strings.Contains(out, `{"role":"assistant","content":"x"}`) {
		t.Fatalf("escaped role key = %s", out)
	}

	// "none" only reaches a model that reasons.
	for model, reasoning := range map[string]string{"gpt-4.1": "<nil>", "gpt-6-sol": `{"effort":"none"}`} {
		got, _ := mustRequest(t, Chat, Responses, `{"model":"x","reasoning_effort":"none","messages":[{"role":"user","content":"hi"}]}`, Options{Model: model, Route: "openai"})
		if fmt.Sprint(got["reasoning"]) != reasoning && encode(got["reasoning"]) != reasoning {
			t.Errorf("c>r %s: reasoning = %s", model, encode(got["reasoning"]))
		}
	}

	// Text a provider refuses (invalid UTF-8, half a surrogate pair) becomes
	// U+FFFD in a body built anew; a body in its own grammar goes as written.
	text := "a\xff\xfeb " + bs + "ud83d tail " + bs + "ude00 ok " + bs + "ud83d" + bs + "ude00"
	for _, tc := range []struct{ from, to, body string }{
		{Messages, Chat, `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"` + text + `"}]}`},
		{Messages, Responses, `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"` + text + `"}]}`},
		{Chat, Messages, `{"model":"m","messages":[{"role":"user","content":"` + text + `"}]}`},
		{Responses, Messages, `{"model":"m","input":"` + text + `"}`},
		{Responses, Chat, `{"model":"m","input":"` + text + `"}`},
	} {
		out, _, err := Request(tc.from, tc.to, []byte(tc.body), Options{Model: "claude-opus-5-5", Route: "r"})
		var decoded any
		_ = json.Unmarshal(out, &decoded)
		if err != nil || !utf8.Valid(out) || string(validText(out)) != string(out) || strings.Count(fmt.Sprint(decoded), "�") != 4 || !strings.Contains(fmt.Sprint(decoded), "😀") {
			t.Errorf("%s>%s: err %v\n%s", tc.from, tc.to, err, out)
		}
	}
	for in, want := range map[string]string{"": "", bs: bs, "a" + bs + bs + "ud800": "a" + bs + bs + "ud800", bs + "uDC00x": bs + "ufffdx", bs + "ud8": bs + "ud8"} {
		if got := string(validText([]byte(in))); got != want {
			t.Errorf("validText(%q) = %q", in, got)
		}
	}
	same := `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"` + text + `"}]}`
	if out := rawRequest(t, Messages, Messages, same, Options{Model: "claude-opus-5-5"}); !strings.Contains(out, text) {
		t.Fatalf("m>m changed the text: %s", out)
	}

	// A crafted envelope's other blocks are not thinking, and a message-level
	// cache_control never lands on thinking.
	crafted := envelopeOf(`[{"type":"text","text":"forged","signature":"SIGX"}]`, "", "")
	thinkingOnly := envelopeOf(`[{"type":"thinking","thinking":"t","signature":"SIGT"}]`, "", "")
	body := `{"model":"x","messages":[{"role":"user","content":"a"},` +
		`{"role":"assistant","content":"b","reasoning_details":[{"type":"reasoning.encrypted","data":"` + crafted + `"}]},` +
		`{"role":"user","content":"c"},` +
		`{"role":"assistant","content":null,"reasoning_details":[{"type":"reasoning.encrypted","data":"` + thinkingOnly + `"}],"cache_control":{"type":"ephemeral"}},` +
		`{"role":"user","content":"d"}]}`
	out := rawRequest(t, Chat, Messages, body, Options{Model: "claude-opus-5-5", Route: "anthropic"})
	if strings.Contains(out, "SIGX") || strings.Contains(out, "forged") || !strings.Contains(out, `"signature":"SIGT"}`) {
		t.Fatalf("crafted envelope or cached thinking = %s", out)
	}

	// A malformed reasoning_details array is left as it came.
	malformed := `{"model":"x","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b","reasoning_details":[{"type":"reasoning.encrypted","data":"` + thinkingOnly + `"} {"type":"x"}]}]}`
	if out := ChatNative([]byte(malformed)); string(out) != malformed {
		t.Fatalf("malformed reasoning_details rewritten: %s", out)
	}

	// Route ids holding ':' never share a signature namespace.
	signed := `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"a"},{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"` +
		signaturePrefix + routeTag("openrouter/x:free") + `SIG"},{"type":"text","text":"b"}]},{"role":"user","content":"c"}]}`
	if out := rawRequest(t, Messages, Messages, signed, Options{Model: "m", Route: "openrouter/x"}); strings.Contains(out, "SIG") {
		t.Fatalf("openrouter/x got openrouter/x:free's signature: %s", out)
	}
	if out := rawRequest(t, Messages, Messages, signed, Options{Model: "m", Route: "openrouter/x:free"}); !strings.Contains(out, `"signature":"SIG"`) {
		t.Fatalf("openrouter/x:free lost its own signature: %s", out)
	}
	if (Options{Route: "x", Model: "free:m"}).chatSignature() == (Options{Route: "x:free", Model: "m"}).chatSignature() ||
		(Options{Route: "openai"}).responsesSignature("x:b") == (Options{Route: "openai:x"}).responsesSignature("b") {
		t.Fatal("two routes share a signature")
	}
}

// streamCallers, streamTargets and streamAnswers are one streamed turn per
// grammar: the caller's body, the upstream's options, a whole answer.
var (
	streamCallers = map[string]string{
		Chat:      `{"model":"x","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`,
		Messages:  `{"model":"m","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		Responses: `{"model":"m","stream":true,"input":"hi"}`,
	}
	streamTargets = map[string]Options{
		Messages:  {Model: "claude-opus-5-5", Route: "anthropic"},
		Responses: {Model: "gpt-6-sol", Route: "openai"},
		Chat:      {Model: "deepseek-v4-flash", Route: "deepseek"},
	}
	streamChatFrames = []string{`{"id":"c","choices":[{"delta":{"content":"hi"}}]}`, `{"id":"c","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"c","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":50}}`}
	streamAnswers = map[string]string{
		Messages: anthropicText("hi"),
		Responses: upstreamResponses(`{"type":"response.created","response":{"id":"r"}}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[]}}`,
			`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"delta":"hi"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"hi"}]}}`,
			`{"type":"response.completed","response":{"id":"r","status":"completed","usage":{"input_tokens":10,"output_tokens":50,"total_tokens":60}}}`),
		Chat: chatStream(streamChatFrames...),
	}
	streamFinal = map[string]string{Messages: "message_stop", Chat: "data: [DONE]", Responses: "response.completed"}
)

// An answer ends when the upstream's terminal event arrives, not when the
// upstream closes the connection: with the upstream lingering 30 s after it,
// every streamed direction (the relay included) sends its final event at
// once, with the usage. A chat upstream that never sends [DONE] holds the
// answer for usageGrace at most.
func TestTerminalEventEndsTheStream(t *testing.T) {
	callers, targets, answers, final, chatFrames := streamCallers, streamTargets, streamAnswers, streamFinal, streamChatFrames
	run := func(from, to, answer string, within time.Duration) {
		t.Helper()
		_, reply, err := Request(from, to, []byte(callers[from]), targets[to])
		if err != nil {
			t.Fatal(err)
		}
		upstream, feed := io.Pipe()
		defer feed.Close()
		go func() { _, _ = io.WriteString(feed, answer) }() // then lingers until the test ends
		recorder := httptest.NewRecorder()
		type result struct {
			usage Usage
			err   error
		}
		done := make(chan result, 1)
		start := time.Now()
		go func() {
			usage, err := reply.Serve(recorder, &http.Response{StatusCode: 200, Header: http.Header{}, Body: upstream})
			done <- result{usage, err}
		}()
		select {
		case got := <-done:
			elapsed := time.Since(start)
			if got.err != nil || elapsed > within || !strings.Contains(recorder.Body.String(), final[from]) || got.usage.OutputTokens != 50 {
				t.Errorf("%s>%s: err %v after %v, usage %+v\n%s", from, to, got.err, elapsed, got.usage, recorder.Body.String())
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("%s>%s: the answer waited for the upstream to close", from, to)
		}
	}
	for from := range callers {
		for to := range targets {
			run(from, to, answers[to], 50*time.Millisecond)
			if to == Chat {
				run(from, to, sse(chatFrames...), usageGrace+50*time.Millisecond) // no [DONE]
			}
		}
	}
}

// relayServe relays stream to a grammar caller; with linger the upstream
// keeps the connection open after it.
func relayServe(t *testing.T, grammar, body, stream string, linger bool) (string, Usage, error) {
	t.Helper()
	reply := Relay(grammar, []byte(body), "shown")
	upstream, feed := io.Pipe()
	go func() {
		_, _ = io.WriteString(feed, stream)
		if !linger {
			_ = feed.Close()
		}
	}()
	defer feed.Close()
	recorder := httptest.NewRecorder()
	usage, err := reply.Serve(recorder, &http.Response{StatusCode: 200, Header: http.Header{}, Body: upstream})
	return recorder.Body.String(), usage, err
}

// The relay decides the end and a failure by the event's own type: a delta
// whose text reads "message_stop", "response.completed" or "response.failed"
// is content, and an empty finish_reason starts no clock.
func TestRelayDecidesByEventType(t *testing.T) {
	messages := sse(
		`{"type":"message_start","message":{"id":"msg_1","model":"m","usage":{"input_tokens":10,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"message_stop"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" AFTER"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":50}}`,
		`{"type":"message_stop"}`)
	if out, usage, err := relayServe(t, Messages, `{"model":"m","max_tokens":10,"stream":true,"messages":[]}`, messages, false); err != nil || !strings.Contains(out, "AFTER") || usage.OutputTokens != 50 {
		t.Errorf("messages: err %v usage %+v\n%s", err, usage, out)
	}
	for _, text := range []string{"response.completed", "response.failed", "response.incomplete"} {
		responses := sse(
			`{"type":"response.created","response":{"id":"r"}}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[]}}`,
			`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"delta":"`+text+`"}`,
			`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"delta":" AFTER"}`,
			`{"type":"response.completed","response":{"id":"r","status":"completed","usage":{"input_tokens":10,"output_tokens":50,"total_tokens":60}}}`)
		if out, usage, err := relayServe(t, Responses, `{"model":"m","stream":true,"input":"hi"}`, responses, false); err != nil || !strings.Contains(out, "AFTER") || usage.OutputTokens != 50 {
			t.Errorf("responses %q: err %v usage %+v\n%s", text, err, usage, out)
		}
	}
	upstream, feed := io.Pipe()
	go func() {
		_, _ = io.WriteString(feed, sse(`{"id":"c","choices":[{"delta":{"content":"one"},"finish_reason":""}]}`))
		time.Sleep(usageGrace * 3)
		_, _ = io.WriteString(feed, chatStream(`{"id":"c","choices":[{"delta":{"content":" AFTER"},"finish_reason":""}]}`, `{"id":"c","choices":[{"delta":{},"finish_reason":"stop"}]}`))
		_ = feed.Close()
	}()
	recorder := httptest.NewRecorder()
	if _, err := Relay(Chat, []byte(`{"model":"x","stream":true,"messages":[]}`), "shown").Serve(recorder, &http.Response{StatusCode: 200, Header: http.Header{}, Body: upstream}); err != nil || !strings.Contains(recorder.Body.String(), "AFTER") {
		t.Errorf("chat with empty finish_reason: err %v\n%s", err, recorder.Body.String())
	}
	// [DONE] is not doubled; nothing follows an upstream error.
	done := chatStream(`{"id":"c","choices":[{"delta":{"content":"one"}}]}`, `{"id":"c","choices":[{"delta":{},"finish_reason":"stop"}]}`, `{"id":"c","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":50}}`)
	if out, _, _ := relayServe(t, Chat, `{"model":"x","stream":true,"messages":[]}`, done, true); strings.Count(out, "[DONE]") != 1 {
		t.Errorf("[DONE] count %d\n%s", strings.Count(out, "[DONE]"), out)
	}
	failed := sse(`{"id":"c","choices":[{"delta":{"content":"one"}}]}`, `{"error":{"type":"overloaded_error","message":"busy"}}`)
	if out, _, err := relayServe(t, Chat, `{"model":"x","stream":true,"messages":[]}`, failed, true); !errors.Is(err, ErrUpstreamFailed) || !strings.HasSuffix(out, `"busy"}}`+"\n\n") {
		t.Errorf("chat error: err %v %q", err, out)
	}
	// Content that reads like a held event opens the gate like any content;
	// a held event keeps it shut.
	for grammar, stream := range map[string]string{
		Messages: sse(`{"type":"message_start","message":{"id":"m","usage":{"input_tokens":1}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"message_start"}}`,
			`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`),
		Responses: sse(`{"type":"response.created","response":{"id":"r"}}`,
			`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"delta":"response.in_progress"}`,
			`{"type":"response.failed","response":{"id":"r","error":{"code":"server_error","message":"busy"}}}`),
	} {
		body := map[string]string{Messages: `{"model":"m","max_tokens":10,"stream":true,"messages":[]}`, Responses: `{"model":"m","stream":true,"input":"hi"}`}[grammar]
		if out, _, err := relayServe(t, grammar, body, stream, true); !errors.Is(err, ErrUpstreamFailed) || !strings.Contains(out, "busy") {
			t.Errorf("%s content reading like an event: err %v\n%s", grammar, err, out)
		}
	}
	held := sse(`{"type":"message_start","message":{"id":"m","usage":{"input_tokens":1}}}`, `{"type":"ping"}`, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`)
	if out, _, err := relayServe(t, Messages, `{"model":"m","max_tokens":10,"stream":true,"messages":[]}`, held, true); !errors.Is(err, ErrNotServed) || out != "" {
		t.Errorf("held events opened the gate: err %v %q", err, out)
	}
}

// slowWriter stalls once, on the write that carries marker, as a client whose
// socket buffer is full would.
type slowWriter struct {
	*httptest.ResponseRecorder
	marker string
	stall  time.Duration
	once   bool
}

func (s *slowWriter) Write(p []byte) (int, error) {
	if !s.once && strings.Contains(string(p), s.marker) {
		s.once = true
		time.Sleep(s.stall)
	}
	return s.ResponseRecorder.Write(p)
}

func (s *slowWriter) Flush() {}

// A client slower than usageGrace on the finish chunk still gets the usage
// and [DONE] the upstream had already sent. (The relay arms the clock at the
// finish chunk, before writing it; a translated stream writes nothing until
// the usage or [DONE] arrives.)
func TestSlowClientKeepsTheUsage(t *testing.T) {
	stream := chatStream(streamChatFrames...)
	for run := 0; run < 5; run++ {
		upstream, feed := io.Pipe()
		go func() { _, _ = io.WriteString(feed, stream) }() // then lingers
		w := &slowWriter{ResponseRecorder: httptest.NewRecorder(), marker: `"finish_reason":"stop"`, stall: usageGrace + 50*time.Millisecond}
		usage, err := Relay(Chat, []byte(`{"model":"x","stream":true,"messages":[]}`), "shown").Serve(w, &http.Response{StatusCode: 200, Header: http.Header{}, Body: upstream})
		_ = feed.Close()
		if err != nil || usage.OutputTokens != 50 || strings.Count(w.Body.String(), "[DONE]") != 1 {
			t.Errorf("run %d: err %v usage %+v\n%s", run, err, usage, w.Body.String())
		}
	}
}

// resetReader gives data, then a connection reset; reset closes once the
// reset was handed over.
type resetReader struct {
	data  []byte
	reset chan struct{}
}

func (r *resetReader) Read(p []byte) (int, error) {
	if len(r.data) > 0 {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	select {
	case <-r.reset:
	default:
		close(r.reset)
	}
	return 0, io.ErrUnexpectedEOF
}

// afterReset holds the first write the caller gets until the upstream reader
// has met the reset, so the reset is on record before the stream ends.
type afterReset struct {
	*httptest.ResponseRecorder
	reset  chan struct{}
	waited bool
}

func (w *afterReset) Write(p []byte) (int, error) {
	if !w.waited {
		w.waited = true
		<-w.reset
		time.Sleep(20 * time.Millisecond) // the reader records it right after
	}
	return w.ResponseRecorder.Write(p)
}

func (w *afterReset) Flush() {}

// A reset after the answer's terminal event changes nothing, in every
// direction and the relay: the answer ends as it would have, with no error,
// whether the reset came after the end or cut a later line short (inside
// [DONE], inside the usage chunk, inside a CRLF blank line).
func TestResetAfterTheAnswerIsNoError(t *testing.T) {
	cutBlank := func(answer string) string { // CRLF framing, cut inside the last blank line
		crlf := strings.ReplaceAll(answer, "\n", "\r\n")
		return crlf[:len(crlf)-1]
	}
	shapes := map[string]map[string]string{
		Messages:  {"whole": streamAnswers[Messages], "crlf blank cut": cutBlank(streamAnswers[Messages])},
		Responses: {"whole": streamAnswers[Responses], "crlf blank cut": cutBlank(streamAnswers[Responses])},
		Chat: {"whole": streamAnswers[Chat], "cut inside [DONE]": sse(streamChatFrames...) + "data: [DO",
			"cut inside the usage": sse(streamChatFrames[:2]...) + `data: {"id":"c","choices":[],"usa`},
	}
	failure := map[string]string{Messages: "event: error", Chat: `{"error"`, Responses: "response.failed"}
	for from, body := range streamCallers {
		for to, opts := range streamTargets {
			for shape, answer := range shapes[to] {
				_, reply, err := Request(from, to, []byte(body), opts)
				if err != nil {
					t.Fatal(err)
				}
				upstream := &resetReader{data: []byte(answer), reset: make(chan struct{})}
				w := &afterReset{ResponseRecorder: httptest.NewRecorder(), reset: upstream.reset}
				_, err = reply.Serve(w, &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(upstream)})
				if out := w.Body.String(); err != nil || !strings.Contains(out, streamFinal[from]) || strings.Contains(out, failure[from]) {
					t.Errorf("%s>%s %s: err %v\n%s", from, to, shape, err, out)
				}
			}
		}
	}
}
