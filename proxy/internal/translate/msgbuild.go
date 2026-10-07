package translate

// Building a Messages body from a Responses or chat conversation: blocks are
// rendered once into an arena (history text and base64 copied as they came),
// consecutive blocks of one role merge into one message, and the cache
// breakpoints go on at assembly, where the shape of the whole conversation is
// known.

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
)

type builtBlock struct {
	start, end int
	kind       string
	toolID     string // a tool_use block's id
}

type builtMessage struct {
	role   string
	blocks []builtBlock
}

// msgBuilder collects Anthropic content blocks.
type msgBuilder struct {
	arena    []byte
	messages []builtMessage
	dropped  map[int]bool // message index -> thinking another host signed was left out of it
	// strict: a last assistant tool turn without thinking always drops manual
	// thinking (a chat client may not have sent the thinking back at all).
	strict bool
}

// blockCount is how many blocks were pushed so far.
func (b *msgBuilder) blockCount() int {
	count := 0
	for _, message := range b.messages {
		count += len(message.blocks)
	}
	return count
}

// cacheLast puts a caller's cache_control on the last block pushed, when one
// was pushed since there were before blocks and it carries none yet; never on
// thinking, which Anthropic refuses it on.
func (b *msgBuilder) cacheLast(before int, cache []byte) {
	if b.blockCount() <= before {
		return
	}
	message := &b.messages[len(b.messages)-1]
	block := &message.blocks[len(message.blocks)-1]
	if block.kind == "thinking" || block.kind == "redacted_thinking" || block.end != len(b.arena) || bytes.Contains(b.arena[block.start:block.end], []byte(`"cache_control"`)) {
		return
	}
	b.arena = append(append(append(b.arena[:block.end-1], `,"cache_control":`...), cache...), '}')
	block.end = len(b.arena)
}

// arenas recycles the builders' scratch space: assemble copies the messages
// out, so an arena outlives no request.
var arenas sync.Pool

func newMsgBuilder(size int) *msgBuilder {
	b := &msgBuilder{dropped: map[int]bool{}}
	if pooled, ok := arenas.Get().(*[]byte); ok && cap(*pooled) >= size {
		b.arena = (*pooled)[:0]
	} else {
		size = capHint(size, 0)
		b.arena = make([]byte, 0, size+size/8+1024)
	}
	return b
}

// release hands the arena back; the builder is not used after it.
func (b *msgBuilder) release() {
	if cap(b.arena) <= 64<<20 {
		arena := b.arena[:0]
		arenas.Put(&arena)
	}
	b.arena = nil
}

// mark is where the next block starts in the arena.
func (b *msgBuilder) mark() int { return len(b.arena) }

// push files the block written to the arena since start under role, merged
// into the last message when that one has the same role.
func (b *msgBuilder) push(role, kind string, start int, toolID string) {
	block := builtBlock{start: start, end: len(b.arena), kind: kind, toolID: toolID}
	if count := len(b.messages); count > 0 && b.messages[count-1].role == role {
		b.messages[count-1].blocks = append(b.messages[count-1].blocks, block)
		return
	}
	b.messages = append(b.messages, builtMessage{role: role, blocks: []builtBlock{block}})
}

// dropForeign notes that thinking another host signed was left out of the
// assistant turn being built.
func (b *msgBuilder) dropForeign() {
	if count := len(b.messages); count > 0 && b.messages[count-1].role == "assistant" {
		b.dropped[count-1] = true
	} else {
		b.dropped[count] = true
	}
}

// textBlock pushes a text block holding the string token text (an empty one
// is skipped); cache is a caller's cache_control value to keep, or nil.
func (b *msgBuilder) textBlock(role string, prefix string, text, cache []byte) {
	if len(text) <= 2 && prefix == "" {
		return
	}
	start := b.mark()
	b.arena = appendInner(append(b.arena, `{"type":"text","text":`...), prefix, inner(text))
	if cache != nil {
		b.arena = append(append(b.arena, `,"cache_control":`...), cache...)
	}
	b.arena = append(b.arena, '}')
	b.push(role, "text", start, "")
}

// imageBlock pushes an image from a URL string token (a data URI is sent as
// base64).
func (b *msgBuilder) imageBlock(role string, url []byte) {
	if len(url) <= 2 {
		return
	}
	start := b.mark()
	b.arena = append(b.arena, `{"type":"image","source":`...)
	if media, data, ok := dataURI(url); ok {
		b.arena = appendInner(append(b.arena, `{"type":"base64","media_type":`...), "", media)
		b.arena = appendInner(append(b.arena, `,"data":`...), "", data)
	} else {
		b.arena = append(append(b.arena, `{"type":"url","url":`...), url...)
	}
	b.arena = append(b.arena, "}}"...)
	b.push(role, "image", start, "")
}

// documentBlock pushes a file (a data URI or bare base64 in data, else a URL)
// as a document block; a file only an OpenAI file id names is refused.
func (b *msgBuilder) documentBlock(role string, filename, data, url, fileID []byte) error {
	start := b.mark()
	b.arena = append(b.arena, `{"type":"document","source":`...)
	switch {
	case len(data) > 2:
		media, base64, ok := dataURI(data)
		if !ok {
			media, base64 = []byte("application/pdf"), inner(data)
		}
		b.arena = appendInner(append(b.arena, `{"type":"base64","media_type":`...), "", media)
		b.arena = appendInner(append(b.arena, `,"data":`...), "", base64)
	case len(url) > 2:
		b.arena = append(append(b.arena, `{"type":"url","url":`...), url...)
	default:
		b.arena = b.arena[:start]
		if !isNull(fileID) {
			return errors.New("a file named only by an OpenAI file id cannot go to a Messages host")
		}
		return errors.New("file part has no data")
	}
	b.arena = append(b.arena, '}')
	if len(filename) > 2 {
		b.arena = append(append(b.arena, `,"title":`...), filename...)
	}
	b.arena = append(b.arena, '}')
	b.push(role, "document", start, "")
	return nil
}

// toolUse pushes an assistant tool call; arguments is the call's arguments as
// a JSON string token (sent as the input object), or input an object already.
func (b *msgBuilder) toolUse(id, name string, arguments, input []byte) {
	start := b.mark()
	b.arena = appendString(append(b.arena, `{"type":"tool_use","id":`...), id)
	b.arena = appendString(append(b.arena, `,"name":`...), name)
	b.arena = append(b.arena, `,"input":`...)
	if input != nil {
		b.arena = append(b.arena, input...)
	} else {
		b.arena = appendToolInput(b.arena, arguments)
	}
	b.arena = append(b.arena, '}')
	b.push("assistant", "tool_use", start, id)
}

// appendToolInput appends a call's arguments (a JSON string token) as the
// input object; arguments that are no JSON object are kept under `_raw`.
func appendToolInput(dst, arguments []byte) []byte {
	text := []byte(jstr(arguments))
	if value, ok := trimValue(text); ok && value[0] == '{' && json.Valid(value) {
		return append(dst, value...)
	}
	return append(appendString(append(dst, `{"_raw":`...), text), '}') // the caller sees what the model emitted
}

// thinking pushes the reasoning a decoded envelope block carries when route
// may see it (an Anthropic-signed block for Anthropic, the route's own
// namespaced one restored for that route); anything else is left out.
func (b *msgBuilder) thinking(block json.RawMessage, route string) {
	var fields struct {
		Type      string `json:"type"`
		Thinking  string `json:"thinking"`
		Signature string `json:"signature"`
		Data      string `json:"data"`
	}
	if json.Unmarshal(block, &fields) != nil || fields.Type != "thinking" && fields.Type != "redacted_thinking" {
		return // a crafted envelope's other blocks are not thinking
	}
	value := fields.Signature
	if fields.Type == "redacted_thinking" {
		value = fields.Data
	}
	own, ours := strings.CutPrefix(value, signaturePrefix+routeTag(route))
	switch {
	case route == anthropicRoute && value != "" && !strings.HasPrefix(value, signaturePrefix):
		own = value
	case ours && route != anthropicRoute && (own != "" || fields.Type == "thinking"):
	default:
		b.dropForeign()
		return
	}
	start := b.mark()
	if fields.Type == "redacted_thinking" {
		b.arena = appendString(append(b.arena, `{"type":"redacted_thinking","data":`...), own)
	} else {
		b.arena = appendString(append(b.arena, `{"type":"thinking","thinking":`...), fields.Thinking)
		b.arena = appendString(append(b.arena, `,"signature":`...), own)
	}
	b.arena = append(b.arena, '}')
	b.push("assistant", fields.Type, start, "")
}

// lastAssistantForeign reports the last assistant turn as stripForeignThinking
// judges it: a tool call left with no thinking that came from another host.
func (b *msgBuilder) lastAssistantForeign() bool {
	for index := len(b.messages) - 1; index >= 0; index-- {
		message := b.messages[index]
		if message.role != "assistant" {
			continue
		}
		calls, foreign := false, b.dropped[index] || b.strict
		for _, block := range message.blocks {
			switch block.kind {
			case "thinking", "redacted_thinking":
				return false
			case "tool_use":
				calls = true
				foreign = foreign || !(strings.HasPrefix(block.toolID, "toolu_") || strings.HasPrefix(block.toolID, "srvtoolu_"))
			}
		}
		return calls && foreign
	}
	return false
}

// assemble renders the messages array. With breakpoints, the last message and
// the end of the previous request (the last message before the newest
// assistant turn) get a cache breakpoint on their last block that can carry
// one.
func (b *msgBuilder) assemble(breakpoints bool) []byte {
	last, previous := len(b.messages)-1, -1
	for index := last; index > 0; index-- {
		if b.messages[index].role == "assistant" && index != last {
			previous = index - 1
			break
		}
	}
	out := make([]byte, 0, capHint(len(b.arena), 256)+capHint(len(b.messages), 0)*48)
	out = append(out, '[')
	for index, message := range b.messages {
		marked := -1
		if breakpoints && (index == last || index == previous) {
			for at := len(message.blocks) - 1; at >= 0; at-- {
				if kind := message.blocks[at].kind; kind != "thinking" && kind != "redacted_thinking" {
					marked = at
					break
				}
			}
		}
		out = appendComma(out)
		out = append(append(append(out, `{"role":"`...), message.role...), `","content":[`...)
		for at, block := range message.blocks {
			if at > 0 {
				out = append(out, ',')
			}
			if at == marked {
				out = append(append(out, b.arena[block.start:block.end-1]...), `,"cache_control":{"type":"ephemeral"}}`...)
			} else {
				out = append(out, b.arena[block.start:block.end]...)
			}
		}
		out = append(out, "]}"...)
	}
	return append(out, ']')
}

// messagesFinish completes a Messages body built for a Messages host: model,
// max_tokens, the effort as fitted thinking plus output_config, the
// structured-output format, and stripForeignThinking's manual-thinking rule.
// thinking off, the caller's sampling knobs go too (Anthropic refuses them
// with thinking on).
func messagesFinish(body map[string]json.RawMessage, b *msgBuilder, opts Options, effort string, maxTokens int, format, temperature, topP []byte) {
	_, _, levels, _ := claudeThinking(opts.Model)
	effort = clampEffort(effort, levels)
	if maxTokens <= 0 {
		maxTokens = cmpOr(opts.MaxOutputTokens, anthropicDefaultMaxTokens)
	}
	body["model"], body["max_tokens"], body["stream"] = mustJSON(opts.Model), mustJSON(maxTokens), json.RawMessage(`true`)
	var thinking json.RawMessage
	if effort != "" && effort != "none" {
		if fitted, keep := nativeThinking(json.RawMessage(`{"type":"adaptive"}`), mustJSON(maxTokens), opts.Model, effort); keep {
			thinking = fitted
		}
	}
	if forcesTool(body["tool_choice"]) {
		thinking = nil // Anthropic takes thinking only with tool_choice auto or none
	}
	if thinking != nil && !(manualThinking(thinking) && b.lastAssistantForeign()) {
		body["thinking"] = thinking
	} else if thinking == nil {
		// Anthropic takes temperature in 0..1 (chat allows 0..2), and current
		// models refuse temperature and top_p together: temperature wins.
		var value float64
		switch {
		case !isNull(temperature) && json.Unmarshal(temperature, &value) == nil && value > 1:
			body["temperature"] = json.RawMessage(`1`)
		case !isNull(temperature):
			body["temperature"] = temperature
		case !isNull(topP):
			body["top_p"] = topP
		}
	}
	var output []byte
	if effort != "" && effort != "none" && len(levels) > 0 {
		output = appendString(append(output, `{"effort":`...), effort)
	}
	if format != nil {
		if output == nil {
			output = append(output, '{')
		} else {
			output = append(output, ',')
		}
		output = append(append(output, `"format":`...), format...)
	}
	if output != nil {
		body["output_config"] = append(output, '}')
	}
	fitRoute(body, opts, false)
}

// forcesTool reports a Messages tool_choice that forces a call (any, tool).
func forcesTool(choice []byte) bool {
	parsed, _ := parseObj(choice)
	kind := parsed.str("type")
	return kind == "any" || kind == "tool"
}

// messagesSchemaFormat is output_config.format for a JSON schema.
func messagesSchemaFormat(schema []byte) []byte {
	if isNull(schema) {
		return nil
	}
	return append(append([]byte(`{"type":"json_schema","schema":`), schema...), '}')
}

// hasCacheControl reports a caller that placed cache breakpoints itself.
func hasCacheControl(raw []byte) bool { return bytes.Contains(raw, []byte(`"cache_control"`)) }
