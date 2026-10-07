package translate

// OpenAI chat SSE -> Anthropic Messages SSE. Claude Code reads the Anthropic
// event grammar and nothing else, so every chunk is re-emitted as the event it
// would have been had Anthropic served it. Flushed per chunk: the first token
// must reach the CLI before the upstream has finished.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// pingInterval is how long a silent upstream goes before the connection is
// proved alive (an Anthropic ping, a Responses keepalive comment).
var pingInterval = 15 * time.Second

// errStreamTruncated: the upstream stream ended before the answer did (the
// body broke, or a clean EOF came before any finish). The caller already got
// an error event in its own grammar.
var errStreamTruncated = errors.New("upstream stream truncated")

type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func newSSEWriter(w http.ResponseWriter) sseWriter {
	flusher, _ := w.(http.Flusher)
	return sseWriter{w: w, flusher: flusher}
}

func (s sseWriter) event(name string, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		return
	}
	_, _ = s.w.Write([]byte("event: " + name + "\ndata: " + string(payload) + "\n\n"))
	if !heldEvents[name] {
		commitOn(s.w)
	}
	s.flush()
}

func (s sseWriter) flush() {
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

// sseLines reads upstream lines on a goroutine so the caller can emit a ping
// on silence: a nil line means "nothing arrived for pingInterval". The
// returned func reports how the body ended once the channel is closed: nil
// for a clean EOF, the read error otherwise.
// ponytail: one goroutine pair per stream, ended by the upstream body closing.
func sseLines(upstream io.Reader) (<-chan []byte, func() error) {
	raw := make(chan []byte)
	var cut error
	go func() {
		defer close(raw)
		reader := bufio.NewReader(upstream)
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				raw <- line
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					cut = err
				}
				return
			}
		}
	}()
	out := make(chan []byte)
	go func() {
		defer close(out)
		timer := time.NewTimer(pingInterval)
		defer timer.Stop()
		for {
			select {
			case line, ok := <-raw:
				if !ok {
					return
				}
				out <- line
			case <-timer.C:
				out <- nil
			}
			timer.Reset(pingInterval)
		}
	}()
	return out, func() error { return cut }
}

// sseData is the payload of one `data:` line, false for any other line.
func sseData(line []byte) ([]byte, bool) {
	data, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:"))
	if !ok {
		return nil, false
	}
	data = bytes.TrimSpace(data)
	return data, len(data) > 0
}

type chatStreamChunk struct {
	ID      string `json:"id"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Delta        struct {
			Content          string           `json:"content"`
			Reasoning        string           `json:"reasoning"`
			ReasoningContent string           `json:"reasoning_content"`
			ToolCalls        []openAIToolCall `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Code    any    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// messageStream tracks the one content block that is open.
type messageStream struct {
	out      sseWriter
	model    string
	index    int
	open     string // "", text, thinking, tool_use
	order    []int
	current  map[int]int    // upstream index -> call key (the order calls opened in)
	upstream map[int]string // call key -> the id the upstream gave it ("" = minted)
	stop     string
	started  bool
	errored  bool
	finished bool // a finish_reason or [DONE] arrived: the upstream ended the answer itself
	usage    chatUsage
	id       string
	// signature marks translated thinking as the runtime's (caveman:…).
	signature string
}

// streamChatToAnthropic re-emits a chat SSE stream as Anthropic events, with
// pings on silence, and returns the usage the upstream reported plus
// errStreamTruncated (wrapped) when the upstream cut the stream.
func streamChatToAnthropic(w http.ResponseWriter, upstream io.Reader, model, signature string) (chatUsage, error) {
	stream := &messageStream{
		out: newSSEWriter(w), model: model, signature: signature,
		current: map[int]int{}, upstream: map[int]string{}, stop: "end_turn", id: "msg_stream",
	}
	lines, cut := sseLines(upstream)
	for line := range lines {
		if line == nil { // silence
			stream.out.event("ping", map[string]any{"type": "ping"})
			continue
		}
		stream.consume(line)
	}
	var truncated error
	switch err := cut(); {
	case err != nil:
		// The body ended without a clean EOF: a truncated answer must not read
		// as a finished turn.
		stream.fail("api_error", "upstream stream ended early: "+err.Error())
		truncated = fmt.Errorf("%w: %w", errStreamTruncated, err)
	case !stream.finished && !stream.errored:
		// A clean EOF before any finish_reason or [DONE] is a cut too: half an
		// answer or half a tool call must never read as end_turn.
		stream.fail("api_error", "upstream stream ended without a finish_reason")
		truncated = fmt.Errorf("%w: no finish_reason", errStreamTruncated)
	default:
		stream.finish()
	}
	return stream.usage, truncated
}

// fail reports an upstream failure inside the stream and stops every later
// event: Anthropic's grammar has exactly one event for this, and after it a
// message_stop would turn a broken answer into a finished one.
func (m *messageStream) fail(kind, message string) {
	if m.errored {
		return
	}
	m.start()
	// No content_block_stop for a half-finished block: a tool call cut off
	// mid-arguments must never read as a finished one.
	m.out.event("error", map[string]any{"type": "error", "error": map[string]any{"type": kind, "message": message}})
	m.errored = true
}

func (m *messageStream) consume(line []byte) {
	if m.errored {
		return
	}
	data, ok := sseData(line)
	if !ok {
		return
	}
	if bytes.Equal(data, []byte("[DONE]")) {
		m.finished = true
		return
	}
	var chunk chatStreamChunk
	if json.Unmarshal(data, &chunk) != nil {
		return
	}
	if chunk.ID != "" && m.id == "msg_stream" {
		m.id = "msg_" + chunk.ID
	}
	m.start()
	if chunk.Error != nil {
		// An upstream that fails after the headers are out can only be reported
		// inside the stream.
		kind := chunk.Error.Type
		if kind == "" {
			kind = "api_error"
		}
		m.fail(kind, chunk.Error.Message)
		return
	}
	if chunk.Usage != nil {
		m.usage = *chunk.Usage
	}
	for _, choice := range chunk.Choices {
		if reasoning := choice.Delta.Reasoning + choice.Delta.ReasoningContent; reasoning != "" {
			m.openBlock("thinking", map[string]any{"type": "thinking", "thinking": "", "signature": ""})
			m.delta(map[string]any{"type": "thinking_delta", "thinking": reasoning})
		}
		if choice.Delta.Content != "" {
			m.openBlock("text", map[string]any{"type": "text", "text": ""})
			m.delta(map[string]any{"type": "text_delta", "text": choice.Delta.Content})
		}
		for _, call := range choice.Delta.ToolCalls {
			m.toolCall(call)
		}
		if choice.FinishReason != "" {
			m.stop, m.finished = anthropicStopReason(choice.FinishReason), true
		}
	}
}

// start emits message_start once, before any content. Input usage is not known
// yet on this path, so it is reported as zero rather than guessed.
func (m *messageStream) start() {
	if m.started {
		return
	}
	m.started = true
	m.out.event("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": m.id, "type": "message", "role": "assistant", "model": m.model,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": anthropicUsage{},
		},
	})
}

func (m *messageStream) openBlock(kind string, block map[string]any) {
	if m.open == kind && kind != "tool_use" {
		return
	}
	m.closeBlock()
	m.open = kind
	m.out.event("content_block_start", map[string]any{"type": "content_block_start", "index": m.index, "content_block": block})
}

func (m *messageStream) closeBlock() {
	if m.open == "" {
		return
	}
	if m.open == "thinking" {
		// Claude Code keeps a thinking block only with its signature; this one
		// marks the reasoning as the runtime's so it never reaches Anthropic.
		m.delta(map[string]any{"type": "signature_delta", "signature": m.signature})
	}
	m.out.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": m.index})
	m.open = ""
	m.index++
}

func (m *messageStream) delta(delta map[string]any) {
	m.out.event("content_block_delta", map[string]any{"type": "content_block_delta", "index": m.index, "delta": delta})
}

// toolCall opens a tool_use block the first time a chat tool index appears and
// streams its arguments as partial JSON after that. An upstream that sends
// parallel calls all at index 0 (Gemini, Ollama) is told apart by id: a new
// upstream id at a known index is a new call.
func (m *messageStream) toolCall(call openAIToolCall) {
	key, known := m.current[call.Index]
	if known && call.ID != "" && m.upstream[key] != "" && m.upstream[key] != call.ID {
		known = false
	}
	if !known {
		key = len(m.order)
		id := wireCallID(call.ID, m.id, call.Index) // the id Claude Code gets is fixed when the block opens
		m.current[call.Index], m.upstream[key] = key, call.ID
		m.order = append(m.order, key)
		// Arguments always stream into the block that is open. Providers emit
		// tool calls one after another; an interleaved pair would need its own
		// block bookkeeping, and none observed does that.
		m.openBlock("tool_use", map[string]any{"type": "tool_use", "id": id, "name": call.Function.Name, "input": map[string]any{}})
	}
	if call.Function.Arguments == "" {
		return
	}
	m.delta(map[string]any{"type": "input_json_delta", "partial_json": call.Function.Arguments})
}

func (m *messageStream) finish() {
	if m.errored {
		return
	}
	m.start()
	m.closeBlock()
	m.out.event("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": m.stop, "stop_sequence": nil},
		"usage": anthropicUsageFromChat(m.usage),
	})
	m.out.event("message_stop", map[string]any{"type": "message_stop"})
}

// writeAnthropicError is Anthropic's error envelope with a status.
func writeAnthropicError(w http.ResponseWriter, status int, kind, message string) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(mustJSON(map[string]any{"type": "error", "error": map[string]any{"type": kind, "message": message}}))
}

// anthropicStreamUsage reads the usage out of one Messages SSE data payload:
// message_start's, then message_delta's cumulative counts (a field it leaves
// out, or zero, keeps message_start's value; some hosts fill input there).
func anthropicStreamUsage(data []byte, usage *anthropicUsage) {
	if !bytes.Contains(data, []byte(`usage`)) {
		return
	}
	var event struct {
		Type    string `json:"type"`
		Message struct {
			Usage anthropicUsage `json:"usage"`
		} `json:"message"`
		Usage *anthropicUsage `json:"usage"`
	}
	if json.Unmarshal(data, &event) != nil {
		return
	}
	switch event.Type {
	case "message_start":
		*usage = event.Message.Usage
	case "message_delta":
		if event.Usage == nil {
			return
		}
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
}
