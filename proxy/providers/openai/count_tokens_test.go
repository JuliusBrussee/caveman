package openai

import (
	"encoding/json"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// The Responses API is the only OpenAI surface with a count endpoint. The
// projection has to carry everything the model will actually be charged for —
// instructions and tool schemas are prompt bytes, and dropping them would make
// the baseline systematically smaller than the real prompt.
func TestCountTokensRequestProjectsResponsesInput(t *testing.T) {
	original := []byte(`{
		"model":"gpt-5.5",
		"input":[{"role":"user","content":"hello"}],
		"instructions":"be terse",
		"tools":[{"type":"function","name":"lookup"}],
		"tool_choice":"auto",
		"stream":true,
		"temperature":0.4,
		"max_output_tokens":128
	}`)

	route, body, ok := Adapter{}.CountTokensRequest(original, providers.RequestMetadata{Endpoint: "/v1/responses", Model: "gpt-5.5"})
	if !ok {
		t.Fatal("CountTokensRequest rejected a valid Responses request")
	}
	if route != "/v1/responses/input_tokens" {
		t.Fatalf("route = %q, want /v1/responses/input_tokens", route)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("projected body is not JSON: %v", err)
	}
	for _, field := range []string{"model", "input", "instructions", "tools", "tool_choice"} {
		if _, ok := got[field]; !ok {
			t.Fatalf("projected body dropped %q, which is charged prompt surface", field)
		}
	}
	// Generation-only fields are not prompt bytes and must not travel: they
	// change nothing about the count and `stream` in particular would make the
	// count endpoint answer with a stream.
	for _, field := range []string{"stream", "temperature", "max_output_tokens"} {
		if _, ok := got[field]; ok {
			t.Fatalf("projected body carried generation-only field %q", field)
		}
	}
}

// Fails CLOSED on anything that is not a Responses request. Chat Completions
// has no count endpoint; projecting one onto /v1/responses/input_tokens would
// count a body the model never sees in that shape.
func TestCountTokensRequestFailsClosed(t *testing.T) {
	valid := `{"model":"gpt-5.5","input":[{"role":"user","content":"hi"}]}`
	cases := []struct {
		name string
		body string
		meta providers.RequestMetadata
	}{
		{"chat completions", valid, providers.RequestMetadata{Endpoint: "/v1/chat/completions"}},
		{"embeddings", valid, providers.RequestMetadata{Endpoint: "/v1/embeddings"}},
		{"unknown endpoint", valid, providers.RequestMetadata{Endpoint: ""}},
		{"not json", `not json`, providers.RequestMetadata{Endpoint: "/v1/responses"}},
		{"missing model", `{"input":[{"role":"user","content":"hi"}]}`, providers.RequestMetadata{Endpoint: "/v1/responses"}},
		{"blank model", `{"model":"  ","input":[]}`, providers.RequestMetadata{Endpoint: "/v1/responses"}},
		{"missing input", `{"model":"gpt-5.5"}`, providers.RequestMetadata{Endpoint: "/v1/responses"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, ok := (Adapter{}).CountTokensRequest([]byte(tc.body), tc.meta); ok {
				t.Fatal("CountTokensRequest accepted an unprojectable request")
			}
		})
	}
}

// CW 5: server-held prompt surface has no projectable prompt.
// previous_response_id / conversation / prompt mean the model is charged for
// content the request body does not contain, and a text.format schema is
// injected server-side. Dropping those fields silently (the previous behavior)
// turned an unmeasurable request into a flattering measurement: a 100k-token
// thread continued with a one-word turn produced a rung-A baseline of a few
// tokens, and every later comparison against that baseline would have read as an
// enormous saving that never happened. Those requests must produce no baseline.
func TestCountTokensRequestRefusesServerHeldThreads(t *testing.T) {
	lastTurn := `{"role":"user","content":"ok"}`
	for name, body := range map[string]string{
		"previous_response_id": `{"model":"gpt-5.5","previous_response_id":"resp_abc123","input":[` + lastTurn + `]}`,
		"conversation":         `{"model":"gpt-5.5","conversation":"conv_abc123","input":[` + lastTurn + `]}`,
		"conversation object":  `{"model":"gpt-5.5","conversation":{"id":"conv_abc123"},"input":[` + lastTurn + `]}`,
		// A stored prompt template: its text is billed as input but lives on
		// OpenAI's side, so the body counts a fraction of the real prompt.
		"stored prompt":      `{"model":"gpt-5.5","prompt":{"id":"pmpt_abc123","version":"2"},"input":[` + lastTurn + `]}`,
		"text.format schema": `{"model":"gpt-5.5","input":"hi","text":{"format":{"type":"json_schema","name":"r","schema":{}}}}`,
		"text.format object": `{"model":"gpt-5.5","input":"hi","text":{"format":{"type":"json_object"}}}`,
		"unparseable text":   `{"model":"gpt-5.5","input":"hi","text":"weird"}`,
		"unparseable format": `{"model":"gpt-5.5","input":"hi","text":{"format":"weird"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, ok := (Adapter{}).CountTokensRequest([]byte(body), providers.RequestMetadata{Endpoint: "/v1/responses"}); ok {
				t.Fatal("an unprojectable request became a rung-A baseline")
			}
		})
	}
	// An explicit null is absence, not a thread — those requests stay
	// projectable. So does {"type":"text"}: it is the Responses API default that
	// several SDKs send explicitly and adds no prompt surface, so refusing on it
	// would drop a legitimate baseline for a large slice of ordinary traffic.
	for name, body := range map[string]string{
		"null previous_response_id": `{"model":"gpt-5.5","previous_response_id":null,"input":"hi"}`,
		"null conversation":         `{"model":"gpt-5.5","conversation":null,"input":"hi"}`,
		"null prompt":               `{"model":"gpt-5.5","prompt":null,"input":"hi"}`,
		"text without format":       `{"model":"gpt-5.5","input":"hi","text":{"verbosity":"low"}}`,
		"null text.format":          `{"model":"gpt-5.5","input":"hi","text":{"format":null}}`,
		"explicit plain text":       `{"model":"gpt-5.5","input":"hi","text":{"format":{"type":"text"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, ok := (Adapter{}).CountTokensRequest([]byte(body), providers.RequestMetadata{Endpoint: "/v1/responses"}); !ok {
				t.Fatal("a projectable request was refused")
			}
		})
	}
}

// The prefixed route the managed gateway mounts must resolve to the prefixed
// count path, not the bare one.
func TestCountTokensRequestKeepsRoutePrefix(t *testing.T) {
	original := []byte(`{"model":"gpt-5.5","input":"hi"}`)
	route, _, ok := Adapter{}.CountTokensRequest(original, providers.RequestMetadata{Endpoint: "/openai/v1/responses"})
	if !ok {
		t.Fatal("CountTokensRequest rejected a valid prefixed Responses request")
	}
	if route != "/openai/v1/responses/input_tokens" {
		t.Fatalf("route = %q, want /openai/v1/responses/input_tokens", route)
	}
}

// A string `input` is as valid as an array in the Responses API.
func TestCountTokensRequestAcceptsStringInput(t *testing.T) {
	if _, _, ok := (Adapter{}).CountTokensRequest([]byte(`{"model":"gpt-5.5","input":"hi"}`), providers.RequestMetadata{Endpoint: "/v1/responses"}); !ok {
		t.Fatal("CountTokensRequest rejected a string input")
	}
}

func TestParseCountTokens(t *testing.T) {
	if got, ok := (Adapter{}).ParseCountTokens([]byte(`{"input_tokens":1234}`)); !ok || got != 1234 {
		t.Fatalf("ParseCountTokens = (%d,%v), want (1234,true)", got, ok)
	}
	// Fails closed on every shape that is not a plain non-negative integer.
	for _, body := range []string{
		`{"input_tokens":"1234"}`,
		`{"input_tokens":-1}`,
		`{}`,
		`nope`,
		`{"tokens":5}`,
		`{"input_tokens":1,"input_tokens":2}`,
		`{"other":1,"other":2,"input_tokens":3}`,
		`{"input_tokens":1}{}`,
	} {
		if _, ok := (Adapter{}).ParseCountTokens([]byte(body)); ok {
			t.Fatalf("ParseCountTokens accepted %q", body)
		}
	}
}
