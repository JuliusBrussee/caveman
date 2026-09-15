package openai

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/internal/counttokens"
)

var _ providers.TokenCounter = Adapter{}

// CountTokensRequest projects a Responses request onto POST
// /v1/responses/input_tokens, OpenAI's free count endpoint.
//
// Responses ONLY. Chat Completions has no count endpoint, and a local
// tokenizer is not a substitute: GPT-5-era formatting and channel tokens are
// not in tiktoken's vocabulary, so a tiktoken estimate structurally
// under-counts the prompt the model is charged for. Under-counting the
// baseline would make every later comparison flatter it, so the honest answer
// for a non-Responses request is "no rung-A count is available here" — the
// caller then stays at rung E rather than substituting a guess.
func (a Adapter) CountTokensRequest(original []byte, meta providers.RequestMetadata) (string, []byte, bool) {
	var prefix string
	switch {
	case meta.Endpoint == "/v1/responses":
	case meta.Endpoint == "/openai/v1/responses":
		prefix = "/openai"
	default:
		return "", nil, false
	}
	body, ok := projectResponses(original)
	if !ok {
		return "", nil, false
	}
	return prefix + "/v1/responses/input_tokens", body, true
}

func (a Adapter) ParseCountTokens(response []byte) (int, bool) {
	return counttokens.ParseNonNegativeInt(response, "input_tokens")
}

// responsesCountRequest is the projection: everything that becomes prompt
// bytes, and nothing that only steers generation.
//
// `instructions` and `tools` are included deliberately — they are prompt
// surface the model is charged for, and a baseline that omitted them would be
// smaller than the real prompt on exactly the traffic (agents with large tool
// schemas) this product exists to measure. `stream`, sampling parameters and
// output limits are excluded: they change nothing about the input count, and
// `stream` would make the count endpoint answer with an event stream.
type responsesCountRequest struct {
	Model        string          `json:"model"`
	Input        json.RawMessage `json:"input"`
	Instructions json.RawMessage `json:"instructions,omitempty"`
	Tools        json.RawMessage `json:"tools,omitempty"`
	ToolChoice   json.RawMessage `json:"tool_choice,omitempty"`
}

// unprojectableResponsesFields name SERVER-HELD prompt surface: content the
// model is charged for that never appears in the request body, so this
// projection provably cannot reproduce it and the request has no rung-A
// baseline at all.
//
//   - previous_response_id / conversation: the server holds the thread. The
//     model is charged for every prior turn, but the request body carries only
//     the new one, so counting the body counts a fraction of the real prompt.
//     A 100k-token conversation continued with "ok" would produce a baseline of
//     a few tokens — and since every later comparison is measured against the
//     baseline, that under-count would read as an enormous saving that never
//     happened. Silently dropping these fields (the previous behavior) turned an
//     unmeasurable request into a flattering measurement, which is precisely the
//     fake-savings shape the honesty rules exist to prevent.
//   - prompt: a stored/reusable prompt template referenced by id. Its text
//     lives on OpenAI's side and is billed as input; the body carries only the
//     id and any variables. Same under-count, same flattering baseline.
//
// A structured-output schema is server-injected prompt surface too, but it
// needs a value check rather than a name check — see structuredOutputRequested.
//
// Returning ok=false here leaves the caller at rung E — "no rung-A count is
// available for this request" — which is the honest answer.
//
// Refusal is the DEFAULT, not a claim that these requests are uncountable in
// principle. A thread continued by previous_response_id has a real token count
// OpenAI itself knows, and /v1/responses/input_tokens accepts the same
// previous_response_id — so a future version can very likely obtain a true
// rung-A baseline for these by forwarding the reference instead of dropping it.
// That needs its own verification against the live endpoint (does the returned
// count include the resolved thread?), which is why it is future work and not
// this change: until it is proven, no baseline beats a flattering one.
var unprojectableResponsesFields = []string{"previous_response_id", "conversation", "prompt"}

func projectResponses(original []byte) ([]byte, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(original, &fields); err != nil {
		return nil, false
	}
	var model string
	if err := json.Unmarshal(fields["model"], &model); err != nil || strings.TrimSpace(model) == "" {
		return nil, false
	}
	for _, name := range unprojectableResponsesFields {
		if raw, present := fields[name]; present && !isJSONNull(raw) {
			return nil, false
		}
	}
	if structuredOutputRequested(fields["text"]) {
		return nil, false
	}
	// `input` is required and may be either a string or an array of items;
	// both are counted. Its absence means this is not a Responses request we
	// can project, so fail closed rather than count an empty prompt.
	input, ok := fields["input"]
	if !ok || len(input) == 0 {
		return nil, false
	}
	projected, err := json.Marshal(responsesCountRequest{
		Model:        model,
		Input:        input,
		Instructions: fields["instructions"],
		Tools:        fields["tools"],
		ToolChoice:   fields["tool_choice"],
	})
	if err != nil {
		return nil, false
	}
	return projected, true
}

// isJSONNull treats an explicit null as absence: a client that sends
// "previous_response_id": null is starting a fresh thread, and that request is
// projectable.
func isJSONNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

// structuredOutputRequested reports whether the Responses `text` block asks for
// a schema-constrained output. Those schemas are prompt surface OpenAI injects
// server-side and this projection does not reproduce, so the request has no
// honest rung-A baseline.
//
// It checks text.format.TYPE rather than merely the presence of text.format:
// {"type":"text"} is the API default that several SDKs send explicitly and adds
// no prompt surface at all, so refusing on it would drop a legitimate baseline
// for a large slice of ordinary traffic. An unrecognized shape fails closed
// (treated as structured): a body this cannot parse is not evidence that no
// schema is present.
func structuredOutputRequested(raw json.RawMessage) bool {
	if len(raw) == 0 || isJSONNull(raw) {
		return false
	}
	var text struct {
		Format json.RawMessage `json:"format"`
	}
	if err := json.Unmarshal(raw, &text); err != nil {
		return true
	}
	if len(text.Format) == 0 || isJSONNull(text.Format) {
		return false
	}
	var format struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(text.Format, &format); err != nil {
		return true
	}
	return strings.TrimSpace(format.Type) != "text"
}
