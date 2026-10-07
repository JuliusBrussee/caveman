package translate

// Anthropic Messages -> OpenAI Responses, so a Claude Code session can run on
// a Responses-only host (the ChatGPT plan's Sign in with ChatGPT, OpenCode
// Go's GPT models). The request is stateless (store:false, the whole
// conversation every turn); the upstream is always asked to stream, and a
// non-streaming caller gets the message assembled from that stream.
//
// Reasoning: the upstream's summary becomes a thinking block whose signature
// carries the item's encrypted_content ("caveman:r1:<route>:<blob>"), so the
// next turn to the same route replays it as a reasoning item and every other
// host, Anthropic included, gets it stripped (AnthropicNative).
//
// DROPPED on the way out, as on the chat path: cache_control,
// context_management, output_config (its effort becomes reasoning.effort),
// stop_sequences, temperature and top_p (reasoning models refuse them), and
// document blocks. Server tools are an error.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// responsesEfforts are the reasoning efforts the Responses API takes.
var responsesEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh"}

// responsesSignature is the thinking signature that carries a Responses
// reasoning item's encrypted_content back to the route that wrote it.
func (o Options) responsesSignature(encrypted string) string {
	return signaturePrefix + "r1:" + o.route() + ":" + encrypted
}

// messagesResponsesBody is a Messages body translated for a Responses host.
func messagesResponsesBody(raw []byte, opts Options) (map[string]json.RawMessage, error) {
	var request anthropicRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	input := []any{}
	for _, message := range request.Messages {
		items, err := anthropicMessageToResponses(message, opts)
		if err != nil {
			return nil, err
		}
		input = append(input, items...)
	}
	out := map[string]any{"model": opts.Model, "input": input, "store": false, "stream": true}
	if system := anthropicTextOf(request.System); system != "" {
		out["instructions"] = system
	}
	if request.MaxTokens > 0 {
		out["max_output_tokens"] = request.MaxTokens
	}
	effort := opts.Effort
	if effort == "" && request.OutputConfig != nil {
		effort = request.OutputConfig.Effort
	}
	if effort == "" && request.Thinking != nil && (request.Thinking.Type == "enabled" || request.Thinking.Type == "adaptive") {
		effort = "medium"
	}
	if effort = clampEffort(effort, responsesEfforts); effort != "" {
		reasoning := map[string]any{"effort": effort}
		if effort != "none" {
			reasoning["summary"] = "auto"
			out["include"] = []string{"reasoning.encrypted_content"}
		}
		out["reasoning"] = reasoning
	}
	tools := make([]map[string]any, 0, len(request.Tools))
	for _, tool := range request.Tools {
		if len(tool.InputSchema) == 0 {
			return nil, fmt.Errorf("tool %q is an Anthropic server tool (type %q) and is only supported on a native Anthropic model", tool.Name, tool.Type)
		}
		function := map[string]any{"type": "function", "name": tool.Name, "parameters": tool.InputSchema, "strict": false}
		if tool.Description != "" {
			function["description"] = tool.Description
		}
		tools = append(tools, function)
	}
	if len(tools) > 0 {
		out["tools"] = tools
	}
	if choice := request.ToolChoice; choice != nil {
		switch choice.Type {
		case "auto", "none":
			out["tool_choice"] = choice.Type
		case "any":
			out["tool_choice"] = "required"
		case "tool":
			out["tool_choice"] = map[string]any{"type": "function", "name": choice.Name}
		}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(mustJSON(out), &fields); err != nil {
		return nil, err
	}
	if opts.ChatGPTLogin {
		chatgptLoginBody(fields)
	}
	dropParams(fields, opts)
	return fields, nil
}

// anthropicMessageToResponses is one Anthropic message as Responses input
// items: tool results become function_call_output items (first, as Anthropic
// orders them), tool_use blocks function_call items, the route's own thinking
// a reasoning item, and text and images one message.
func anthropicMessageToResponses(message anthropicMessage, opts Options) ([]any, error) {
	assistant := message.Role == "assistant"
	var items, parts []any
	flush := func() {
		if len(parts) > 0 {
			items = append(items, map[string]any{"type": "message", "role": message.Role, "content": parts})
			parts = nil
		}
	}
	own := opts.responsesSignature("")
	for _, block := range anthropicBlocksOf(message.Content) {
		switch block.Type {
		case "text":
			kind := "input_text"
			if assistant {
				kind = "output_text"
			}
			parts = append(parts, map[string]any{"type": kind, "text": block.Text})
		case "image":
			url, err := imageURL(block)
			if err != nil {
				return nil, err
			}
			parts = append(parts, map[string]any{"type": "input_image", "image_url": url})
		case "tool_use":
			flush()
			arguments := string(block.Input)
			if arguments == "" {
				arguments = "{}"
			}
			items = append(items, map[string]any{"type": "function_call", "call_id": safeCallID(block.ID), "name": block.Name, "arguments": arguments})
		case "tool_result":
			flush()
			output := anthropicTextOf(block.Content)
			if output == "" {
				output = strings.TrimSpace(string(block.Content))
			}
			if block.IsError {
				output = "Error: " + output
			}
			items = append(items, map[string]any{"type": "function_call_output", "call_id": safeCallID(block.ToolUseID), "output": output})
		case "thinking":
			// The route's own reasoning goes back to it; nothing else does.
			if encrypted, ok := strings.CutPrefix(block.Signature, own); ok && assistant && encrypted != "" {
				flush()
				summary := []any{}
				if block.Thinking != "" {
					summary = append(summary, map[string]any{"type": "summary_text", "text": block.Thinking})
				}
				items = append(items, map[string]any{"type": "reasoning", "summary": summary, "encrypted_content": encrypted})
			}
		}
	}
	flush()
	return items, nil
}

// responsesEvent is the union of the Responses stream events read here.
type responsesEvent struct {
	Type     string          `json:"type"`
	ItemID   string          `json:"item_id"`
	Delta    string          `json:"delta"`
	Item     json.RawMessage `json:"item"`
	Response *struct {
		ID                string          `json:"id"`
		Output            json.RawMessage `json:"output"`
		Usage             *responsesUsage `json:"usage"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"response"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// responsesOutputItem is one output item: reasoning, message or function_call.
type responsesOutputItem struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	CallID           string          `json:"call_id"`
	Name             string          `json:"name"`
	Arguments        string          `json:"arguments"`
	EncryptedContent *string         `json:"encrypted_content"`
	Summary          []responsesPart `json:"summary"`
	Content          []responsesPart `json:"content"`
}

// responsesToMessages reads one Responses stream and drives a Messages answer:
// the events on stream (nil for a non-streaming caller, which gets the
// assembled message instead).
type responsesToMessages struct {
	m        *messageStream
	opts     Options
	args     map[string]bool // function_call item id -> its arguments streamed
	calls    bool
	finished bool
	failed   string // the upstream's error, when it failed
	output   []responsesOutputItem
	usage    chatUsage
	stop     string
}

func (t *responsesToMessages) consume(line []byte) {
	data, ok := sseData(line)
	if !ok || t.m.errored {
		return
	}
	var event responsesEvent
	if json.Unmarshal(data, &event) != nil {
		return
	}
	if event.Response != nil && event.Response.ID != "" && t.m.id == "msg_stream" {
		t.m.id = "msg_" + event.Response.ID
	}
	switch event.Type {
	case "response.created", "response.in_progress":
		t.m.start()
	case "response.output_item.added":
		var item responsesOutputItem
		if json.Unmarshal(event.Item, &item) == nil && item.Type == "function_call" {
			t.calls = true
			t.m.toolCall(openAIToolCall{ID: item.CallID, Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: item.Name}, Index: len(t.args)})
			t.args[item.ID] = false
		}
	case "response.output_text.delta":
		t.m.start()
		t.m.openBlock("text", map[string]any{"type": "text", "text": ""})
		t.m.delta(map[string]any{"type": "text_delta", "text": event.Delta})
	case "response.reasoning_summary_text.delta":
		t.m.start()
		t.m.signature = t.opts.responsesSignature("")
		t.m.openBlock("thinking", map[string]any{"type": "thinking", "thinking": "", "signature": ""})
		t.m.delta(map[string]any{"type": "thinking_delta", "thinking": event.Delta})
	case "response.function_call_arguments.delta":
		if _, known := t.args[event.ItemID]; known && event.Delta != "" && t.m.open == "tool_use" {
			t.args[event.ItemID] = true
			t.m.delta(map[string]any{"type": "input_json_delta", "partial_json": event.Delta})
		}
	case "response.output_item.done":
		var item responsesOutputItem
		if json.Unmarshal(event.Item, &item) != nil {
			return
		}
		t.output = append(t.output, item)
		switch item.Type {
		case "reasoning":
			encrypted := ""
			if item.EncryptedContent != nil {
				encrypted = *item.EncryptedContent
			}
			if t.m.open != "thinking" && encrypted != "" {
				// No summary streamed: an empty thinking block still carries
				// the reasoning back to this route next turn.
				t.m.start()
				t.m.openBlock("thinking", map[string]any{"type": "thinking", "thinking": "", "signature": ""})
			}
			if t.m.open == "thinking" {
				t.m.signature = t.opts.responsesSignature(encrypted)
				t.m.closeBlock()
			}
		case "function_call":
			if streamed, known := t.args[item.ID]; known && !streamed && item.Arguments != "" && t.m.open == "tool_use" {
				t.m.delta(map[string]any{"type": "input_json_delta", "partial_json": item.Arguments})
			}
			t.m.closeBlock()
		case "message":
			if t.m.open == "text" {
				t.m.closeBlock()
			}
		}
	case "response.completed", "response.incomplete":
		t.finished = true
		t.stop = "end_turn"
		if t.calls {
			t.stop = "tool_use"
		}
		if event.Response != nil {
			if details := event.Response.IncompleteDetails; details != nil {
				switch details.Reason {
				case "max_output_tokens":
					t.stop = "max_tokens"
				case "content_filter":
					t.stop = "refusal"
				}
			}
			if usage := event.Response.Usage; usage != nil {
				t.usage = chatUsage{PromptTokens: usage.InputTokens, CompletionTokens: usage.OutputTokens, PromptTokensDetails: &promptTokensDetails{
					CachedTokens: usage.InputTokensDetails.CachedTokens, CacheWriteTokens: usage.InputTokensDetails.CacheWriteTokens,
				}}
			}
			if len(t.output) == 0 && len(event.Response.Output) > 0 {
				_ = json.Unmarshal(event.Response.Output, &t.output)
			}
		}
		t.m.stop, t.m.usage, t.m.finished = t.stop, t.usage, true
	case "response.failed", "error":
		message, code := event.Message, event.Code
		if event.Response != nil && event.Response.Error != nil {
			message, code = event.Response.Error.Message, event.Response.Error.Code
		}
		if message == "" {
			message = "upstream response failed"
		}
		t.failed = message
		kind := "api_error"
		if strings.Contains(code, "rate_limit") {
			kind = "rate_limit_error"
		}
		t.m.fail(kind, message)
	}
}

// streamResponsesToAnthropic re-emits a Responses stream as Anthropic events
// (stream true) or assembles the message (stream false), with the usage the
// upstream reported and errStreamTruncated when it cut the stream.
func streamResponsesToAnthropic(w http.ResponseWriter, upstream io.Reader, stream bool, opts Options) (Usage, error) {
	var sink bytes.Buffer
	out := newSSEWriter(w)
	if !stream {
		out = sseWriter{w: discardWriter{header: http.Header{}, w: &sink}}
	} else {
		startSSE(w)
	}
	t := &responsesToMessages{opts: opts, args: map[string]bool{}, m: &messageStream{
		out: out, model: opts.shown(), signature: opts.responsesSignature(""),
		current: map[int]int{}, upstream: map[int]string{}, stop: "end_turn", id: "msg_stream",
	}}
	lines, cut := sseLines(upstream)
	for line := range lines {
		if line == nil {
			if stream {
				heartbeat(t.m.out.w)
				t.m.out.event("ping", map[string]any{"type": "ping"})
			}
			continue
		}
		t.consume(line)
	}
	usage := anthropicUsageFromChat(t.usage).usage()
	var truncated error
	switch err := cut(); {
	case err != nil:
		t.m.fail("api_error", "upstream stream ended early: "+err.Error())
		truncated = fmt.Errorf("%w: %w", errStreamTruncated, err)
	case !t.finished && !t.m.errored:
		t.m.fail("api_error", "upstream stream ended without response.completed")
		truncated = fmt.Errorf("%w: no response.completed", errStreamTruncated)
	default:
		t.m.finish()
	}
	if stream {
		return usage, truncated
	}
	switch {
	case t.failed != "":
		writeAnthropicError(w, http.StatusBadGateway, "api_error", t.failed)
		return usage, nil
	case truncated != nil:
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "upstream stream ended early")
		return usage, truncated
	}
	writeJSON(w, t.assembled())
	return usage, nil
}

// assembled is the whole Anthropic message for a non-streaming caller, built
// from the output items.
func (t *responsesToMessages) assembled() []byte {
	blocks := []any{}
	for i, item := range t.output {
		switch item.Type {
		case "reasoning":
			encrypted := ""
			if item.EncryptedContent != nil {
				encrypted = *item.EncryptedContent
			}
			texts := []string{}
			for _, part := range item.Summary {
				texts = append(texts, part.Text)
			}
			if encrypted != "" || len(texts) > 0 {
				blocks = append(blocks, map[string]any{"type": "thinking", "thinking": strings.Join(texts, "\n"), "signature": t.opts.responsesSignature(encrypted)})
			}
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" && part.Text != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": part.Text})
				}
			}
		case "function_call":
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": wireCallID(item.CallID, t.m.id, i), "name": item.Name, "input": toolInput(item.Arguments)})
		}
	}
	return mustJSON(map[string]any{
		"id": t.m.id, "type": "message", "role": "assistant", "model": t.opts.shown(), "content": blocks,
		"stop_reason": t.stop, "stop_sequence": nil, "usage": anthropicUsageFromChat(t.usage),
	})
}

// discardWriter collects the events a non-streaming caller does not see.
type discardWriter struct {
	header http.Header
	w      io.Writer
}

func (d discardWriter) Header() http.Header         { return d.header }
func (d discardWriter) Write(p []byte) (int, error) { return d.w.Write(p) }
func (d discardWriter) WriteHeader(int)             {}
