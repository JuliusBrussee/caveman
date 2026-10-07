package translate

// Anthropic Messages SSE / OpenAI Responses SSE -> OpenAI chat completions,
// for a chat caller on a Messages or Responses host: chunks as the upstream
// sends them (role first, then content, reasoning_content and tool-call
// deltas, a finish_reason, the usage chunk when stream_options asked for it,
// [DONE]), or the assembled completion for a non-streaming caller (the
// upstream is always asked to stream). A cut stream never ends with a
// finish_reason or [DONE]: it ends with an error chunk, and the runtime then
// aborts the connection.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

type chatCallOut struct {
	id, name  string
	arguments strings.Builder
	streamed  bool // arguments went out as deltas
}

// chatEmitter writes one chat completion.
type chatEmitter struct {
	w            http.ResponseWriter
	stream       bool
	includeUsage bool
	id, model    string
	created      int64
	started      bool
	content      strings.Builder
	reasoning    strings.Builder
	refusal      strings.Builder
	details      [][]byte
	calls        []*chatCallOut
	finish       string
	usage        chatUsage
	failed       bool
	failStatus   int
	failMessage  string
	failType     string
}

func newChatEmitter(w http.ResponseWriter, stream, includeUsage bool, model string) *chatEmitter {
	e := &chatEmitter{w: w, stream: stream, includeUsage: includeUsage, id: responsesItemID("chatcmpl"), model: model, created: time.Now().Unix()}
	e.id = strings.Replace(e.id, "chatcmpl_", "chatcmpl-", 1)
	if stream {
		startSSE(w)
	}
	return e
}

// chunk writes one stream chunk with delta (a JSON object) and finish; content
// commits the gate.
func (e *chatEmitter) chunk(delta []byte, finish string, content bool) {
	if !e.stream {
		return
	}
	out := make([]byte, 0, len(delta)+160)
	out = append(out, `data: {"id":`...)
	out = appendString(out, e.id)
	out = append(out, `,"object":"chat.completion.chunk","created":`...)
	out = appendInt(out, e.created)
	out = appendString(append(out, `,"model":`...), e.model)
	out = append(append(append(out, `,"choices":[{"index":0,"delta":`...), delta...), `,"finish_reason":`...)
	if finish == "" {
		out = append(out, "null"...)
	} else {
		out = appendString(out, finish)
	}
	out = append(out, "}]}\n\n"...)
	_, _ = e.w.Write(out)
	if content {
		commitOn(e.w)
	}
	flushWriter(e.w)
}

func appendInt(dst []byte, n int64) []byte { return append(dst, mustJSON(n)...) }

func flushWriter(w io.Writer) {
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

// keepalive proves a silent upstream alive: an SSE comment, which chat
// clients skip.
func (e *chatEmitter) keepalive() {
	heartbeat(e.w)
	if e.stream {
		_, _ = e.w.Write([]byte(": keepalive\n\n"))
		flushWriter(e.w)
	}
}

func (e *chatEmitter) start() {
	if !e.started {
		e.started = true
		e.chunk([]byte(`{"role":"assistant","content":""}`), "", false)
	}
}

func (e *chatEmitter) text(delta string) {
	if delta == "" {
		return
	}
	e.start()
	e.content.WriteString(delta)
	e.chunk(append(appendString([]byte(`{"content":`), delta), '}'), "", true)
}

func (e *chatEmitter) reasoningText(delta string) {
	if delta == "" {
		return
	}
	e.start()
	e.reasoning.WriteString(delta)
	e.chunk(append(appendString([]byte(`{"reasoning_content":`), delta), '}'), "", true)
}

func (e *chatEmitter) refusalText(delta string) {
	if delta == "" {
		return
	}
	e.start()
	e.refusal.WriteString(delta)
	e.chunk(append(appendString([]byte(`{"refusal":`), delta), '}'), "", true)
}

// detail sends one reasoning_details entry carrying an envelope.
func (e *chatEmitter) detail(envelope reasoningEnvelope) {
	entry := appendString([]byte(`{"type":"reasoning.encrypted","data":`), base64.StdEncoding.EncodeToString(mustJSON(envelope)))
	entry = append(append(entry, `,"index":`...), mustJSON(len(e.details))...)
	entry = append(entry, '}')
	e.details = append(e.details, entry)
	e.start()
	e.chunk(append(append([]byte(`{"reasoning_details":[`), entry...), "]}"...), "", true)
}

// toolStart opens a call; its index is the order calls opened in.
func (e *chatEmitter) toolStart(id, name string) *chatCallOut {
	e.start()
	call := &chatCallOut{id: id, name: name}
	e.calls = append(e.calls, call)
	delta := append([]byte(`{"tool_calls":[{"index":`), mustJSON(len(e.calls)-1)...)
	delta = appendString(append(delta, `,"id":`...), id)
	delta = appendString(append(delta, `,"type":"function","function":{"name":`...), name)
	delta = append(delta, `,"arguments":""}}]}`...)
	e.chunk(delta, "", true)
	return call
}

func (e *chatEmitter) toolArgs(call *chatCallOut, arguments string) {
	if arguments == "" {
		return
	}
	call.arguments.WriteString(arguments)
	call.streamed = true
	index := 0
	for at, open := range e.calls {
		if open == call {
			index = at
		}
	}
	delta := append([]byte(`{"tool_calls":[{"index":`), mustJSON(index)...)
	delta = appendString(append(delta, `,"function":{"arguments":`...), arguments)
	e.chunk(append(delta, "}}]}"...), "", true)
}

// done ends the answer: the finish chunk, the usage chunk when asked for,
// [DONE]; for a non-streaming caller the assembled completion.
func (e *chatEmitter) done() {
	if e.failed {
		return
	}
	if e.finish == "" {
		e.finish = "stop"
		if len(e.calls) > 0 {
			e.finish = "tool_calls"
		}
	}
	if !e.stream {
		writeJSON(e.w, e.completion())
		return
	}
	e.start()
	e.chunk([]byte(`{}`), e.finish, true)
	if e.includeUsage {
		out := appendString([]byte(`data: {"id":`), e.id)
		out = append(append(out, `,"object":"chat.completion.chunk","created":`...), mustJSON(e.created)...)
		out = appendString(append(out, `,"model":`...), e.model)
		out = append(append(append(out, `,"choices":[],"usage":`...), mustJSON(e.usage)...), "}\n\n"...)
		_, _ = e.w.Write(out)
	}
	_, _ = e.w.Write([]byte("data: [DONE]\n\n"))
	flushWriter(e.w)
}

// fail reports an upstream failure: before content nothing reaches the caller
// (the gate keeps it); after content an error chunk ends the stream, without
// a finish_reason or [DONE].
func (e *chatEmitter) fail(status int, kind, message string) {
	if e.failed {
		return
	}
	e.failed, e.failStatus, e.failType, e.failMessage = true, status, kind, message
	body := mustJSON(map[string]any{"error": map[string]any{"type": kind, "message": message}})
	if !e.stream {
		e.w.Header().Set("content-type", "application/json")
		e.w.WriteHeader(status)
		_, _ = e.w.Write(body)
		return
	}
	_, _ = e.w.Write(append(append([]byte("data: "), body...), '\n', '\n'))
	flushWriter(e.w)
}

// completion is the assembled chat completion.
func (e *chatEmitter) completion() []byte {
	message := []byte(`{"role":"assistant","content":`)
	if e.content.Len() > 0 || e.refusal.Len() == 0 && len(e.calls) == 0 {
		message = appendString(message, e.content.String())
	} else {
		message = append(message, "null"...)
	}
	if e.refusal.Len() > 0 {
		message = appendString(append(message, `,"refusal":`...), e.refusal.String())
	}
	if e.reasoning.Len() > 0 {
		message = appendString(append(message, `,"reasoning_content":`...), e.reasoning.String())
	}
	if len(e.details) > 0 {
		message = append(append(append(message, `,"reasoning_details":[`...), bytes.Join(e.details, []byte(","))...), ']')
	}
	if len(e.calls) > 0 {
		message = append(message, `,"tool_calls":[`...)
		for at, call := range e.calls {
			if at > 0 {
				message = append(message, ',')
			}
			message = appendString(append(message, `{"id":`...), call.id)
			message = appendString(append(message, `,"type":"function","function":{"name":`...), call.name)
			message = appendString(append(message, `,"arguments":`...), cmpOrString(call.arguments.String(), "{}"))
			message = append(message, "}}"...)
		}
		message = append(message, ']')
	}
	message = append(message, '}')
	out := appendString([]byte(`{"id":`), e.id)
	out = append(append(out, `,"object":"chat.completion","created":`...), mustJSON(e.created)...)
	out = appendString(append(out, `,"model":`...), e.model)
	out = append(append(out, `,"choices":[{"index":0,"message":`...), message...)
	out = appendString(append(out, `,"finish_reason":`...), e.finish)
	out = append(append(append(out, `}],"usage":`...), mustJSON(e.usage)...), '}')
	return out
}

func cmpOrString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// chatFinish is the chat finish_reason a Messages stop reason stands for.
func chatFinish(stop string) string {
	switch stop {
	case "max_tokens", "model_context_window_exceeded":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter"
	}
	return "stop"
}

// chatUsageOf is a chat usage object from total input, cache reads and
// writes, and output.
func chatUsageOf(input, cacheRead, cacheWrite, output, reasoning int) chatUsage {
	usage := chatUsage{PromptTokens: input, CompletionTokens: output, TotalTokens: input + output,
		PromptTokensDetails: &promptTokensDetails{CachedTokens: cacheRead, CacheWriteTokens: cacheWrite}}
	if reasoning > 0 {
		usage.CompletionTokensDetails = &struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		}{ReasoningTokens: reasoning}
	}
	return usage
}

// --- Anthropic Messages SSE -> chat ------------------------------------------------

// streamAnthropicToChat translates one Messages stream. Thinking goes out as
// reasoning_content and, signed, as a reasoning_details envelope (unsigned
// thinking is shown but never carried).
func streamAnthropicToChat(e *chatEmitter, upstream io.Reader) error {
	lines, cut, stop := sseLines(upstream)
	defer stop()
	type block struct {
		kind      string
		thinking  strings.Builder
		signature strings.Builder
		call      *chatCallOut
		input     json.RawMessage // a tool_use input sent whole at the start
		start     json.RawMessage
	}
	blocks := map[int]*block{}
	var usage anthropicUsage
	stopped := false
	for line := range lines {
		if line == nil {
			e.keepalive()
			continue
		}
		if e.failed {
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
			e.start()
		case "content_block_start":
			var start struct {
				Type  string          `json:"type"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			_ = json.Unmarshal(event.ContentBlock, &start)
			b := &block{kind: start.Type, start: event.ContentBlock}
			if start.Type == "tool_use" {
				b.call = e.toolStart(start.ID, start.Name)
				if len(start.Input) > 0 && string(start.Input) != "{}" {
					b.input = start.Input
				}
			}
			blocks[event.Index] = b
		case "content_block_delta":
			b := blocks[event.Index]
			if b == nil {
				continue
			}
			switch event.Delta.Type {
			case "text_delta":
				e.text(event.Delta.Text)
			case "thinking_delta":
				b.thinking.WriteString(event.Delta.Thinking)
				e.reasoningText(event.Delta.Thinking)
			case "signature_delta":
				b.signature.WriteString(event.Delta.Signature)
			case "input_json_delta":
				if b.call != nil {
					e.toolArgs(b.call, event.Delta.PartialJSON)
				}
			}
		case "content_block_stop":
			b := blocks[event.Index]
			if b == nil {
				continue
			}
			delete(blocks, event.Index)
			switch b.kind {
			case "thinking":
				if b.signature.Len() > 0 {
					e.detail(reasoningEnvelope{Caveman: reasoningEnvelopeVersion, Blocks: []json.RawMessage{mustJSON(map[string]string{
						"type": "thinking", "thinking": b.thinking.String(), "signature": b.signature.String(),
					})}})
				}
			case "redacted_thinking":
				e.detail(reasoningEnvelope{Caveman: reasoningEnvelopeVersion, Blocks: []json.RawMessage{b.start}})
			case "tool_use":
				if !b.call.streamed && b.input != nil {
					e.toolArgs(b.call, string(b.input)) // whole input, no deltas
				}
			}
		case "message_delta":
			if event.Delta.StopReason != "" {
				e.finish = chatFinish(event.Delta.StopReason)
			}
			if event.Usage != nil {
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
			e.fail(anthropicErrorStatus(event.Error.Type), cmpOrString(event.Error.Type, "api_error"), event.Error.Message)
		}
	}
	e.usage = chatUsageOf(usage.InputTokens+usage.CacheReadInputTokens+usage.CacheCreationInputTokens, usage.CacheReadInputTokens, usage.CacheCreationInputTokens, usage.OutputTokens, 0)
	if e.failed {
		return nil
	}
	if err := cut(); err != nil || !stopped {
		e.fail(http.StatusBadGateway, "api_error", "upstream stream ended before the answer completed")
		return errStreamTruncated
	}
	e.done()
	return nil
}

// --- Responses SSE -> chat -----------------------------------------------------------

// streamResponsesToChat translates one Responses stream. Reasoning summaries
// go out as reasoning_content, the item's encrypted_content as a
// reasoning_details envelope tagged with the route, so only that route gets
// it back.
func streamResponsesToChat(e *chatEmitter, upstream io.Reader, route string) error {
	lines, cut, stop := sseLines(upstream)
	defer stop()
	calls := map[string]*chatCallOut{} // item id -> call
	texted := map[string]bool{}        // message item id -> its text streamed
	finished := false
	for line := range lines {
		if line == nil {
			e.keepalive()
			continue
		}
		if e.failed || finished {
			continue
		}
		data, ok := sseData(line)
		if !ok {
			continue
		}
		var event responsesEvent
		if json.Unmarshal(data, &event) != nil {
			continue
		}
		switch event.Type {
		case "response.created", "response.in_progress":
			e.start()
		case "response.output_item.added":
			var item responsesOutputItem
			if json.Unmarshal(event.Item, &item) == nil && item.Type == "function_call" {
				calls[item.ID] = e.toolStart(item.CallID, item.Name)
			}
		case "response.function_call_arguments.delta":
			if call := calls[event.ItemID]; call != nil {
				e.toolArgs(call, event.Delta)
			}
		case "response.output_text.delta":
			texted[event.ItemID] = true
			e.text(event.Delta)
		case "response.refusal.delta":
			texted[event.ItemID] = true
			e.refusalText(event.Delta)
		case "response.reasoning_summary_text.delta":
			e.reasoningText(event.Delta)
		case "response.output_item.done":
			var item responsesOutputItem
			if json.Unmarshal(event.Item, &item) != nil {
				continue
			}
			switch item.Type {
			case "function_call":
				call := calls[item.ID]
				if call == nil {
					call = e.toolStart(item.CallID, item.Name)
				}
				if !call.streamed {
					e.toolArgs(call, item.Arguments)
				}
			case "reasoning":
				if item.EncryptedContent != nil && *item.EncryptedContent != "" {
					e.detail(reasoningEnvelope{Caveman: reasoningEnvelopeVersion, Blocks: []json.RawMessage{}, Route: route, Blob: *item.EncryptedContent})
				}
			case "message":
				if !texted[item.ID] {
					for _, part := range item.Content {
						if part.Type == "refusal" {
							e.refusalText(part.Refusal)
						} else {
							e.text(part.Text)
						}
					}
				}
			}
		case "response.completed", "response.incomplete":
			finished = true
			if response := event.Response; response != nil {
				if details := response.IncompleteDetails; details != nil {
					e.finish = map[string]string{"max_output_tokens": "length", "content_filter": "content_filter"}[details.Reason]
				}
				if usage := response.Usage; usage != nil {
					e.usage = chatUsageOf(usage.InputTokens, usage.InputTokensDetails.CachedTokens, usage.InputTokensDetails.CacheWriteTokens,
						usage.OutputTokens, usage.OutputTokensDetails.ReasoningTokens)
				}
			}
		case "response.failed", "error":
			message, code := event.Message, event.Code
			if event.Response != nil && event.Response.Error != nil {
				message, code = event.Response.Error.Message, event.Response.Error.Code
			}
			kind, status := "api_error", http.StatusBadGateway
			if strings.Contains(code, "rate_limit") {
				kind, status = "rate_limit_error", http.StatusTooManyRequests
			}
			e.fail(status, kind, cmpOrString(message, "upstream response failed"))
		}
	}
	if e.failed {
		return nil
	}
	if err := cut(); err != nil || !finished {
		e.fail(http.StatusBadGateway, "api_error", "upstream stream ended before the response completed")
		return errStreamTruncated
	}
	e.done()
	return nil
}
