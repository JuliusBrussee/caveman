package translate

// Anthropic Messages SSE / OpenAI chat SSE -> OpenAI Responses SSE, in the
// grammar Codex's parser accepts (codex-rs codex-api/src/sse/responses.rs):
//
//   - response.created carries response.id;
//   - every output item opens with response.output_item.added and closes with
//     response.output_item.done carrying the COMPLETE item, because Codex
//     builds items from .done only (argument deltas are ignored there);
//   - item ids are prefix_suffix, or Codex drops them (response_item_id.rs
//     is_prefixed);
//   - the stream ends with response.completed carrying id and usage (usage
//     drives auto-compaction), or response.failed carrying response.error.
//     A stream that closes before either is an error Codex retries, so a cut
//     upstream becomes response.failed with a retryable code, never a
//     completed answer.
//   - response.incomplete ends the turn only with reason "interrupted"; with
//     "content_filter" Codex raises its content-filter error (a guidance note,
//     retries, then the error shown), with any other reason a stream error it
//     retries (codex-rs codex-api/src/sse/responses.rs process_responses_event,
//     https://github.com/openai/codex/blob/18e28fe1b96db7e1d6b13584bec37c41f71b8b0e/codex-rs/codex-api/src/sse/responses.rs#L418-L464).
//     So a refusal ends in response.incomplete {reason: content_filter}, as
//     OpenAI's own content filter does, and a max-tokens stop completes with
//     what arrived: as max_output_tokens Codex would resend the same request
//     up to five times. A non-streamed answer carries status incomplete and
//     the reason for both.

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// responsesUsage is the Responses usage object. InputTokens INCLUDES cached
// and cache-write tokens, as OpenAI counts them; Codex reads the fill of the
// context window from it.
type responsesUsage struct {
	InputTokens        int `json:"input_tokens"`
	InputTokensDetails struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
	} `json:"input_tokens_details"`
	OutputTokens        int `json:"output_tokens"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
	TotalTokens int `json:"total_tokens"`
}

func (u *responsesUsage) usage() Usage {
	if u == nil {
		return Usage{}
	}
	return Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
		CacheReadTokens: u.InputTokensDetails.CachedTokens, CacheWriteTokens: u.InputTokensDetails.CacheWriteTokens}
}

func responsesUsageFromAnthropic(usage anthropicUsage) *responsesUsage {
	out := &responsesUsage{
		InputTokens:  usage.InputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens,
		OutputTokens: usage.OutputTokens,
	}
	out.InputTokensDetails.CachedTokens = usage.CacheReadInputTokens
	out.InputTokensDetails.CacheWriteTokens = usage.CacheCreationInputTokens
	out.TotalTokens = out.InputTokens + out.OutputTokens
	return out
}

func responsesItemID(prefix string) string {
	var raw [12]byte
	_, _ = rand.Read(raw[:])
	return prefix + "_" + hex.EncodeToString(raw[:])
}

// --- the emitter ------------------------------------------------------------

// responsesStream writes one Responses event stream and keeps the final
// response object. With a nil writer it only assembles (a non-streamed call).
type responsesStream struct {
	out      *sseWriter
	id       string
	model    string
	created  int64
	sequence int
	output   []any
	usage    *responsesUsage
	failure  *responsesFailure
	finished bool
	// incomplete is why the answer stopped short: max_output_tokens or
	// content_filter ("" for a whole one).
	incomplete string
}

type streamItem struct {
	index  int
	id     string
	kind   string // message | reasoning | function_call | custom_tool_call
	text   strings.Builder
	callID string
	origin toolOrigin
}

func newResponsesStream(w http.ResponseWriter, model string, stream bool) *responsesStream {
	s := &responsesStream{id: responsesItemID("resp"), model: model, created: time.Now().Unix()}
	if stream {
		writer := newSSEWriter(w)
		s.out = &writer
	}
	return s
}

func (s *responsesStream) emit(kind string, fields map[string]any) {
	if s.out == nil {
		return
	}
	fields["type"] = kind
	fields["sequence_number"] = s.sequence
	s.sequence++
	s.out.event(kind, fields)
}

func (s *responsesStream) keepalive() {
	if s.out != nil {
		heartbeat(s.out.w)
		_, _ = s.out.w.Write([]byte(": keepalive\n\n"))
		s.out.flush()
	}
}

func (s *responsesStream) response(status string) map[string]any {
	output := make([]any, 0, len(s.output))
	for _, item := range s.output {
		if item != nil {
			output = append(output, item)
		}
	}
	response := map[string]any{
		"id": s.id, "object": "response", "created_at": s.created, "status": status,
		"model": s.model, "output": output, "usage": nil, "error": nil, "incomplete_details": nil,
	}
	if s.usage != nil {
		response["usage"] = s.usage
	}
	if s.failure != nil {
		response["error"] = map[string]any{"code": s.failure.Code, "message": s.failure.Message}
	}
	if status == "incomplete" {
		response["incomplete_details"] = map[string]any{"reason": s.incomplete}
	}
	return response
}

func (s *responsesStream) start() {
	s.emit("response.created", map[string]any{"response": s.response("in_progress")})
	s.emit("response.in_progress", map[string]any{"response": s.response("in_progress")})
}

func (s *responsesStream) begin(kind, prefix string, item map[string]any) *streamItem {
	open := &streamItem{index: len(s.output), id: responsesItemID(prefix), kind: kind}
	s.output = append(s.output, nil)
	item["id"] = open.id
	s.emit("response.output_item.added", map[string]any{"output_index": open.index, "item": item})
	return open
}

func (s *responsesStream) done(open *streamItem, item map[string]any) {
	item["id"] = open.id
	s.output[open.index] = item
	s.emit("response.output_item.done", map[string]any{"output_index": open.index, "item": item})
}

func (s *responsesStream) beginText() *streamItem {
	open := s.begin("message", "msg", map[string]any{"type": "message", "role": "assistant", "status": "in_progress", "content": []any{}})
	s.emit("response.content_part.added", map[string]any{
		"item_id": open.id, "output_index": open.index, "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
	})
	return open
}

func (s *responsesStream) textDelta(open *streamItem, delta string) {
	open.text.WriteString(delta)
	s.emit("response.output_text.delta", map[string]any{"item_id": open.id, "output_index": open.index, "content_index": 0, "delta": delta})
}

func (s *responsesStream) endText(open *streamItem) {
	text := open.text.String()
	part := map[string]any{"type": "output_text", "text": text, "annotations": []any{}}
	s.emit("response.output_text.done", map[string]any{"item_id": open.id, "output_index": open.index, "content_index": 0, "text": text})
	s.emit("response.content_part.done", map[string]any{"item_id": open.id, "output_index": open.index, "content_index": 0, "part": part})
	s.done(open, map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{part}})
}

// Reasoning text is shown as the item's summary; it is never replayed as
// text. What is replayed is encrypted_content.
func (s *responsesStream) beginReasoning() *streamItem {
	open := s.begin("reasoning", "rs", map[string]any{"type": "reasoning", "summary": []any{}})
	s.emit("response.reasoning_summary_part.added", map[string]any{
		"item_id": open.id, "output_index": open.index, "summary_index": 0,
		"part": map[string]any{"type": "summary_text", "text": ""},
	})
	return open
}

func (s *responsesStream) reasoningDelta(open *streamItem, delta string) {
	open.text.WriteString(delta)
	s.emit("response.reasoning_summary_text.delta", map[string]any{"item_id": open.id, "output_index": open.index, "summary_index": 0, "delta": delta})
}

func (s *responsesStream) endReasoning(open *streamItem, encrypted *string) {
	text := open.text.String()
	summary := []any{}
	if text != "" {
		summary = append(summary, map[string]any{"type": "summary_text", "text": text})
	}
	s.emit("response.reasoning_summary_text.done", map[string]any{"item_id": open.id, "output_index": open.index, "summary_index": 0, "text": text})
	s.emit("response.reasoning_summary_part.done", map[string]any{
		"item_id": open.id, "output_index": open.index, "summary_index": 0,
		"part": map[string]any{"type": "summary_text", "text": text},
	})
	if encrypted == nil {
		// An empty envelope rather than null: Codex sends store:false, so
		// OpenAI cannot look a bare reasoning id up and would refuse the
		// item on a later turn back on the harness's own path. OpenAINative
		// drops envelopes, empty ones included.
		empty := base64.StdEncoding.EncodeToString(mustJSON(reasoningEnvelope{Caveman: reasoningEnvelopeVersion, Blocks: []json.RawMessage{}}))
		encrypted = &empty
	}
	item := map[string]any{"type": "reasoning", "summary": summary, "encrypted_content": *encrypted}
	s.done(open, item)
}

func (s *responsesStream) beginTool(callID string, origin toolOrigin) *streamItem {
	kind, prefix := "function_call", "fc"
	item := map[string]any{"call_id": callID, "name": origin.Name, "status": "in_progress"}
	if origin.Custom {
		kind, prefix = "custom_tool_call", "ctc"
		item["input"] = ""
	} else {
		item["arguments"] = ""
	}
	if origin.Namespace != "" {
		item["namespace"] = origin.Namespace
	}
	item["type"] = kind
	open := s.begin(kind, prefix, item)
	open.callID, open.origin = callID, origin
	return open
}

// endTool closes a call with its COMPLETE arguments: Codex reads them from
// this event alone.
func (s *responsesStream) endTool(open *streamItem, arguments string) {
	if strings.TrimSpace(arguments) == "" {
		arguments = "{}"
	}
	item := map[string]any{"type": open.kind, "call_id": open.callID, "name": open.origin.Name, "status": "completed"}
	if open.origin.Custom {
		item["input"] = customInput(arguments)
	} else {
		item["arguments"] = arguments
	}
	if open.origin.Namespace != "" {
		item["namespace"] = open.origin.Namespace
	}
	s.done(open, item)
}

func (s *responsesStream) complete() {
	if s.finished {
		return
	}
	s.finished = true
	if s.incomplete == "content_filter" {
		s.emit("response.incomplete", map[string]any{"response": s.response("incomplete")})
		return
	}
	s.emit("response.completed", map[string]any{"response": s.response("completed")})
}

func (s *responsesStream) fail(failure responsesFailure) {
	if s.finished {
		return
	}
	s.finished, s.failure = true, &failure
	s.emit("response.failed", map[string]any{"response": s.response("failed")})
}

// answer is the final response object: what a non-streamed caller receives
// (an OpenAI error body instead when the answer failed).
func (s *responsesStream) answer() []byte {
	status := "completed"
	switch {
	case s.failure != nil:
		status = "failed"
	case s.incomplete != "":
		status = "incomplete"
	}
	return mustJSON(s.response(status))
}

// --- failures ---------------------------------------------------------------

// responsesFailure is response.error: a code Codex's parser acts on
// (codex-api/src/sse/responses_error.rs parse_failed_response) and a message.
type responsesFailure struct {
	Code    string
	Message string
}

// responsesContextMessage is OpenAI's own wording for a prompt over the
// window. With code context_length_exceeded Codex raises ContextWindowExceeded:
// the turn fails, Codex marks the context window full
// (core/src/session/turn.rs set_total_tokens_full), and its next turn
// auto-compacts before sampling. Any other code would be retried as-is or
// shown as a plain error, and the session would never compact.
const responsesContextMessage = "Your input exceeds the context window of this model. Please adjust your input and try again."

// Prompt-overflow detection for the Responses path. A prompt
// over the window is an overflow: the harness compacts. A completion that does
// not fit is not: compacting would not fix it. Only explicit completion-limit
// wording excludes, because OpenAI's own prompt overflow also names the
// completion: "you requested N tokens (A in the messages, B in the
// completion). Please reduce the length of the messages or completion."
var (
	contextOverflowMarkers = []string{"context_length_exceeded", "maximum context length", "context window", "prompt is too long",
		"input is too long", "too many tokens", "reduce the length", "exceeds the context"}
	completionLimitMarkers = []string{"max_tokens", "max_completion_tokens", "max_output_tokens", "maximum output", "completion tokens"}
	openAIRequestedSplit   = regexp.MustCompile(`\((\d+) in the messages, (\d+) in the completion\)`)
)

// promptTooLong: a 400/413/422 whose wording says the prompt does not fit.
// OpenAI's split template counts as the prompt's when the messages are the
// larger part.
// ponytail: "larger part" is a heuristic; the window minus a reserve would be
// exact but needs the caller's max_tokens.
func promptTooLong(status int, body []byte) bool {
	if status != http.StatusBadRequest && status != http.StatusRequestEntityTooLarge && status != http.StatusUnprocessableEntity {
		return false
	}
	lower := strings.ToLower(string(body))
	for _, marker := range completionLimitMarkers {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	if split := openAIRequestedSplit.FindStringSubmatch(lower); split != nil {
		messages, _ := strconv.Atoi(split[1])
		completion, _ := strconv.Atoi(split[2])
		return messages >= completion
	}
	for _, marker := range contextOverflowMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// responsesFailureFor maps an upstream error to the code Codex handles
// the right way:
//   - prompt over the window: context_length_exceeded (the turn fails and the
//     next one compacts);
//   - 429: rate_limit_exceeded, with the upstream's Retry-After in the
//     "try again in Ns" wording Codex parses a delay from;
//   - out of credit: insufficient_quota (not retried);
//   - 5xx, overload, a cut stream or an unreachable upstream: server_error,
//     which Codex treats as retryable;
//   - any other 4xx: invalid_prompt, which Codex shows and does not retry
//     (an unknown code would be retried forever on a request that cannot pass).
func responsesFailureFor(status int, body []byte, retryAfter string) responsesFailure {
	message := upstreamErrorMessage(body)
	if message == "" {
		message = http.StatusText(status)
	}
	lower := strings.ToLower(string(body))
	if promptTooLong(status, body) {
		return responsesFailure{Code: "context_length_exceeded", Message: responsesContextMessage}
	}
	switch {
	case status == http.StatusTooManyRequests:
		if seconds, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && seconds >= 0 {
			message += " Please try again in " + strconv.Itoa(seconds) + "s."
		}
		return responsesFailure{Code: "rate_limit_exceeded", Message: message}
	case status == http.StatusPaymentRequired || strings.Contains(lower, "insufficient_quota") || strings.Contains(lower, "credit balance"):
		return responsesFailure{Code: "insufficient_quota", Message: message}
	case status >= 400 && status < 500:
		return responsesFailure{Code: "invalid_prompt", Message: "Upstream " + strconv.Itoa(status) + ": " + message}
	}
	return responsesFailure{Code: "server_error", Message: message}
}

// anthropicErrorStatus is the HTTP status an in-stream Anthropic error type
// stands for.
func anthropicErrorStatus(kind string) int {
	switch kind {
	case "rate_limit_error":
		return http.StatusTooManyRequests
	case "invalid_request_error":
		return http.StatusBadRequest
	case "authentication_error":
		return http.StatusUnauthorized
	case "permission_error":
		return http.StatusForbidden
	case "not_found_error":
		return http.StatusNotFound
	case "request_too_large":
		return http.StatusRequestEntityTooLarge
	case "billing_error":
		return http.StatusPaymentRequired
	}
	return http.StatusServiceUnavailable // overloaded_error, api_error, unknown
}

// upstreamErrorMessage reads error.message out of an OpenAI- or
// Anthropic-shaped error body, or the body itself when it is short text.
func upstreamErrorMessage(body []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &parsed) == nil {
		if parsed.Error.Message != "" {
			return parsed.Error.Message
		}
		if parsed.Message != "" {
			return parsed.Message
		}
	}
	text := strings.TrimSpace(string(body))
	if len(text) > 500 || json.Valid(body) {
		return ""
	}
	return text
}

var streamCut = responsesFailure{Code: "server_error", Message: "upstream stream ended before the response completed"}

// --- Anthropic Messages SSE -> Responses ------------------------------------

type anthropicStreamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		Usage anthropicUsage `json:"usage"`
	} `json:"message"`
	ContentBlock json.RawMessage `json:"content_block"`
	Delta        struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *anthropicUsage `json:"usage"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type anthropicStreamBlock struct {
	item      *streamItem
	kind      string
	thinking  strings.Builder
	signature strings.Builder
	arguments strings.Builder
	start     json.RawMessage // the block as content_block_start sent it
}

// streamAnthropicToResponses translates one Anthropic stream. Each content
// block is one output item: thinking and redacted_thinking become reasoning
// whose encrypted_content carries the signed block for the next turn,
// tool_use becomes function_call or custom_tool_call under the name Codex
// declared.
func streamAnthropicToResponses(out *responsesStream, upstream io.Reader, bridge toolBridge) {
	out.start()
	lines, _, stop, _ := sseLines(upstream)
	defer stop()
	blocks := map[int]*anthropicStreamBlock{}
	var usage anthropicUsage
	stopped := false
	for line := range lines {
		if line == nil {
			out.keepalive()
			continue
		}
		data, ok := sseData(line)
		if !ok {
			continue
		}
		var event anthropicStreamEvent
		if json.Unmarshal(data, &event) != nil {
			continue
		}
		switch event.Type {
		case "message_start":
			usage = event.Message.Usage
		case "content_block_start":
			var start struct {
				Type  string          `json:"type"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			_ = json.Unmarshal(event.ContentBlock, &start)
			block := &anthropicStreamBlock{kind: start.Type, start: event.ContentBlock}
			switch start.Type {
			case "text":
				block.item = out.beginText()
			case "thinking", "redacted_thinking":
				block.item = out.beginReasoning()
			case "tool_use":
				block.item = out.beginTool(start.ID, bridge.origin(start.Name))
				if len(start.Input) > 0 && string(start.Input) != "{}" {
					block.start = start.Input
				} else {
					block.start = nil
				}
			default:
				continue // server tools and anything newer: not a Codex item
			}
			blocks[event.Index] = block
		case "content_block_delta":
			block := blocks[event.Index]
			if block == nil {
				continue
			}
			switch event.Delta.Type {
			case "text_delta":
				out.textDelta(block.item, event.Delta.Text)
			case "thinking_delta":
				block.thinking.WriteString(event.Delta.Thinking)
				out.reasoningDelta(block.item, event.Delta.Thinking)
			case "signature_delta":
				block.signature.WriteString(event.Delta.Signature)
			case "input_json_delta":
				block.arguments.WriteString(event.Delta.PartialJSON)
			}
		case "content_block_stop":
			block := blocks[event.Index]
			if block == nil {
				continue
			}
			delete(blocks, event.Index)
			switch block.kind {
			case "text":
				out.endText(block.item)
			case "thinking":
				// Unsigned thinking is shown but never carried: it could not
				// be replayed to Anthropic.
				var carried *string
				if block.signature.Len() > 0 {
					carried = encodeThinking([]json.RawMessage{mustJSON(map[string]string{
						"type": "thinking", "thinking": block.thinking.String(), "signature": block.signature.String(),
					})})
				}
				out.endReasoning(block.item, carried)
			case "redacted_thinking":
				out.endReasoning(block.item, encodeThinking([]json.RawMessage{block.start}))
			case "tool_use":
				arguments := block.arguments.String()
				if arguments == "" && block.start != nil {
					arguments = string(block.start) // whole input, no deltas
				}
				out.endTool(block.item, arguments)
			}
		case "message_delta":
			out.incomplete = incompleteReason(event.Delta.StopReason)
			if event.Usage != nil {
				// message_delta counts are cumulative; a field it leaves out
				// keeps message_start's value.
				usage.OutputTokens = event.Usage.OutputTokens
				if event.Usage.InputTokens > 0 {
					usage.InputTokens = event.Usage.InputTokens
				}
				if event.Usage.CacheReadInputTokens > 0 {
					usage.CacheReadInputTokens = event.Usage.CacheReadInputTokens
				}
				if event.Usage.CacheCreationInputTokens > 0 {
					usage.CacheCreationInputTokens = event.Usage.CacheCreationInputTokens
				}
			}
		case "message_stop":
			stopped = true
		case "error":
			out.fail(responsesFailureFor(anthropicErrorStatus(event.Error.Type), mustJSON(map[string]any{"error": event.Error}), ""))
		}
		if out.finished || stopped {
			break // the answer is over: an upstream that lingers holds neither it nor the fallback
		}
	}
	if out.finished {
		return
	}
	if !stopped { // past message_stop a reset changes nothing
		out.fail(streamCut)
		return
	}
	out.usage = responsesUsageFromAnthropic(usage)
	out.complete()
}

// incompleteReason is the Responses incomplete reason a Messages stop reason
// or chat finish reason stands for ("" for a whole answer).
func incompleteReason(stop string) string {
	switch stop {
	case "max_tokens", "length", "model_context_window_exceeded":
		return "max_output_tokens"
	case "refusal", "content_filter":
		return "content_filter"
	}
	return ""
}

// --- OpenAI chat SSE -> Responses -------------------------------------------

// chatStreamCall is one tool call index. Its item opens only once the name is
// known: whether it is a function_call or a custom_tool_call depends on it.
type chatStreamCall struct {
	item      *streamItem
	id, name  string
	upstream  bool // id is the upstream's own, not minted
	arguments strings.Builder
	thought   string // the thought signature the upstream put on it (Gemini)
}

func (c *chatStreamCall) open(out *responsesStream, bridge toolBridge) {
	if c.item != nil {
		return
	}
	if c.id == "" {
		c.id = responsesItemID("call") // some chat upstreams send none
	}
	c.item = out.beginTool(c.id, bridge.origin(c.name))
}

// streamChatToResponses translates one chat stream. Reasoning deltas open a
// reasoning item (encrypted_content null, unless the route takes its own
// reasoning back: then an envelope signed `replay`, which responsesToChat
// returns as reasoning_content), content opens a message, and each tool call
// index opens a call whose complete arguments are sent when the stream ends.
// The calls' thought signatures follow them in a reasoning item, each
// carried under `thoughts` (thoughtTag), which responsesChatBody puts back.
func streamChatToResponses(out *responsesStream, upstream io.Reader, bridge toolBridge, replay, thoughts string) {
	out.start()
	lines, _, stop, endBy := sseLines(upstream)
	defer stop()
	var reasoning, text *streamItem
	// calls is keyed by the order calls opened in: an upstream that sends
	// parallel calls all at index 0 (Gemini, Ollama) is told apart by id.
	calls := map[int]*chatStreamCall{}
	order := []int{}
	current := map[int]int{} // upstream index -> key in calls
	closeReasoning := func() {
		if reasoning != nil {
			var carried *string
			if text := reasoning.text.String(); replay != "" && text != "" {
				carried = encodeThinking([]json.RawMessage{mustJSON(map[string]string{"type": "thinking", "thinking": text, "signature": replay})})
			}
			out.endReasoning(reasoning, carried)
			reasoning = nil
		}
	}
	closeText := func() {
		if text != nil {
			out.endText(text)
			text = nil
		}
	}
	finished := false
	for line := range lines {
		if line == nil {
			out.keepalive()
			continue
		}
		data, ok := sseData(line)
		if !ok {
			continue
		}
		if bytes.Equal(data, []byte("[DONE]")) {
			finished = true
			break // the answer is over: an upstream that lingers holds nothing
		}
		var chunk chatStreamChunk
		if json.Unmarshal(data, &chunk) != nil {
			continue
		}
		if chunk.Error != nil {
			status := http.StatusServiceUnavailable
			switch code := chunk.Error.Code.(type) {
			case float64:
				status = int(code)
			case string:
				// OpenAI's own error codes are strings: an overflow must reach
				// Codex as one, so it compacts instead of retrying.
				if code == "context_length_exceeded" {
					status = http.StatusBadRequest
				}
			}
			out.fail(responsesFailureFor(status, data, ""))
			break // an upstream that lingers holds neither the answer nor the fallback
		}
		if chunk.Usage != nil {
			usage := &responsesUsage{InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens, TotalTokens: chunk.Usage.TotalTokens}
			usage.InputTokensDetails.CachedTokens = chunk.Usage.cachedTokens()
			usage.InputTokensDetails.CacheWriteTokens = chunk.Usage.cacheWriteTokens()
			if chunk.Usage.CompletionTokensDetails != nil {
				usage.OutputTokensDetails.ReasoningTokens = chunk.Usage.CompletionTokensDetails.ReasoningTokens
			}
			if usage.TotalTokens == 0 {
				usage.TotalTokens = usage.InputTokens + usage.OutputTokens
			}
			out.usage = usage
		}
		for _, choice := range chunk.Choices {
			if delta := choice.Delta.Reasoning + choice.Delta.ReasoningContent; delta != "" {
				closeText()
				if reasoning == nil {
					reasoning = out.beginReasoning()
				}
				out.reasoningDelta(reasoning, delta)
			}
			if choice.Delta.Content != "" {
				closeReasoning()
				if text == nil {
					text = out.beginText()
				}
				out.textDelta(text, choice.Delta.Content)
			}
			for _, call := range choice.Delta.ToolCalls {
				closeReasoning()
				closeText()
				key, known := current[call.Index]
				// A minted id is no upstream id: a late id is the same call. A
				// name with no id after whole arguments is the next call
				// (Gemini sends whole calls with no id at all).
				if !known || call.ID != "" && calls[key].upstream && calls[key].id != call.ID || nextCall(call, calls[key].arguments.String()) {
					key = len(order)
					current[call.Index] = key
					calls[key] = &chatStreamCall{}
					order = append(order, key)
				}
				open := calls[key]
				if call.ID != "" && open.item == nil {
					open.id, open.upstream = call.ID, true
				}
				open.name += call.Function.Name
				if thought := call.thought(); thought != "" {
					open.thought = thought
				}
				if open.name != "" {
					open.open(out, bridge)
				}
				// Arguments in the very first chunk count too (LiteLLM #27144).
				open.arguments.WriteString(call.Function.Arguments)
			}
			if choice.FinishReason != "" {
				finished = true
				out.incomplete = incompleteReason(choice.FinishReason)
			}
		}
		if finished {
			endBy(finishGrace(out.usage != nil)) // only the usage chunk and [DONE] may follow
		}
	}
	if out.finished {
		return
	}
	if !finished { // past the finish a reset changes nothing
		// Only the failure: no output_item.done for a half-finished call, so
		// Codex never runs a tool on cut-off arguments.
		out.fail(streamCut)
		return
	}
	closeReasoning()
	closeText()
	var carried []json.RawMessage
	for _, index := range order {
		calls[index].open(out, bridge)
		out.endTool(calls[index].item, calls[index].arguments.String())
		if thought := calls[index].thought; thought != "" {
			carried = append(carried, mustJSON(map[string]string{"type": "redacted_thinking", "data": thoughts + calls[index].id + ":" + thought}))
		}
	}
	if carried != nil {
		// The host refuses the next request without them (Gemini 3).
		out.endReasoning(out.beginReasoning(), encodeThinking(carried))
	}
	out.complete()
}

// --- answering a Responses caller -------------------------------------------

// finishTranslated writes a translated answer: events as they arrive (a 200
// stream), or the final response object for a non-streaming caller (an
// OpenAI error body when the answer failed).
func finishTranslated(w http.ResponseWriter, stream bool, model string, translate func(*responsesStream)) *responsesStream {
	if stream {
		startResponses(w, true)
	}
	out := newResponsesStream(w, model, stream)
	translate(out)
	if !stream {
		if out.failure != nil {
			writeResponsesFailure(w, false, model, *out.failure)
		} else {
			startResponses(w, false)
			_, _ = w.Write(out.answer())
			commitOn(w)
		}
	}
	return out
}

// writeResponsesFailure ends a request that produced nothing: a streaming
// caller gets a 200 stream holding response.failed (Codex only maps
// context_length_exceeded and the other codes from there, never from an HTTP
// error body); a non-streaming caller gets an OpenAI error body.
func writeResponsesFailure(w http.ResponseWriter, stream bool, model string, failure responsesFailure) {
	if stream {
		startResponses(w, true)
		newResponsesStream(w, model, true).fail(failure)
		return
	}
	status := http.StatusBadGateway
	switch failure.Code {
	case "context_length_exceeded", "invalid_prompt":
		status = http.StatusBadRequest
	case "rate_limit_exceeded":
		status = http.StatusTooManyRequests
	case "insufficient_quota":
		status = http.StatusPaymentRequired
	}
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(mustJSON(map[string]any{"error": map[string]any{
		"message": failure.Message, "type": "upstream_error", "code": failure.Code,
	}}))
}

// startResponses writes the success headers.
func startResponses(w http.ResponseWriter, stream bool) {
	if stream {
		w.Header().Set("content-type", "text/event-stream")
		w.Header().Set("cache-control", "no-cache")
	} else {
		w.Header().Set("content-type", "application/json")
	}
	w.WriteHeader(http.StatusOK)
}
