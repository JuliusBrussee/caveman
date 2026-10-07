package translate

// Responses bodies (Codex) rendered for a Messages or chat upstream, and
// fitted for a Responses one. History is copied byte for byte.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

// rItem is one Responses input item, its large values left as raw JSON.
type rItem struct {
	kind, role, callID, name, namespace string
	content, output, arguments, input   []byte
	encrypted                           []byte // encrypted_content (a string token, or nil)
}

// responsesInput reads `input`: a bare string is one user message.
func responsesInput(raw []byte) ([]rItem, error) {
	if isNull(raw) {
		return nil, nil
	}
	if isStr(raw) {
		return []rItem{{kind: "message", role: "user", content: raw}}, nil
	}
	var out []rItem
	ok := eachItem(raw, func(_ []byte, item obj) {
		if item == nil {
			return
		}
		next := rItem{kind: item.str("type"), role: item.str("role"), callID: item.str("call_id"), name: item.str("name"),
			namespace: item.str("namespace"), content: item.get("content"), output: item.get("output"),
			arguments: item.get("arguments"), input: item.get("input"), encrypted: item.get("encrypted_content")}
		switch {
		case next.kind == "" && next.role != "":
			next.kind = "message" // EasyInputMessage carries no type
		case next.kind == "agent_message":
			// MultiAgentV2 hands a child its task (and later messages) this way
			// (codex-rs core/src/agent/control/delivery.rs): the text is already
			// attributed, and encrypted parts carry no text to translate.
			next.kind, next.role = "message", "user"
		}
		out = append(out, next)
	})
	if !ok {
		return nil, errors.New("input is neither a string nor a list of items")
	}
	return orderToolOutputs(out), nil
}

// orderToolOutputs moves a user or developer message that sits between tool
// calls and their outputs to after the outputs; a call that never gets an
// output holds nothing past the next assistant message. Anthropic wants
// tool_result first in the next user turn and chat wants `tool` right after
// the call; either would answer 400 on the order as sent.
func orderToolOutputs(items []rItem) []rItem {
	pending := map[string]bool{}
	out, held := make([]rItem, 0, len(items)), []rItem{}
	for _, item := range items {
		switch {
		case item.kind == "function_call" || item.kind == "custom_tool_call":
			pending[item.callID] = true
		case item.kind == "function_call_output" || item.kind == "custom_tool_call_output":
			delete(pending, item.callID)
		case item.kind == "message" && item.role != "assistant" && len(pending) > 0:
			held = append(held, item)
			continue
		case item.kind == "message" && len(held) > 0:
			// The conversation went on past a call that never got an output:
			// the held messages keep their place before this reply.
			out, held = append(out, held...), held[:0]
		}
		out = append(out, item)
		if len(pending) == 0 && len(held) > 0 {
			out, held = append(out, held...), held[:0]
		}
	}
	return append(out, held...)
}

// eachPart calls fn with each content part of a string-or-parts value (a
// string is one input_text part).
func eachPart(raw []byte, fn func(kind string, part obj)) {
	if isStr(raw) {
		fn("input_text", obj{{key: []byte("text"), val: raw}})
		return
	}
	eachItem(raw, func(_ []byte, part obj) {
		if part != nil {
			fn(part.str("type"), part)
		}
	})
}

// partTexts are the text tokens of a string-or-parts value (every part that
// is not an image or file and carries text, a refusal's included).
func partTexts(raw []byte) [][]byte {
	var out [][]byte
	eachPart(raw, func(kind string, part obj) {
		text := part.get("text")
		if kind == "refusal" {
			text = part.get("refusal")
		}
		if kind != "input_image" && kind != "input_file" && isStr(text) && len(text) > 2 {
			out = append(out, text)
		}
	})
	return out
}

// appendPartsText appends one string: the text tokens joined by newlines.
func appendPartsText(dst []byte, prefix string, raw []byte) []byte {
	return appendJoined(dst, prefix, partTexts(raw), `\n`)
}

// splitSystem returns the instructions plus every developer/system message
// that comes before the conversation starts, as the system prompt's text
// tokens; developer messages later in the conversation stay where they are,
// as user text.
func splitSystem(instructions []byte, items []rItem) ([][]byte, []rItem) {
	var system [][]byte
	if text := partTexts(instructions); len(text) > 0 {
		system = append(system, appendJoined(nil, "", text, `\n`))
	}
	start := 0
	for ; start < len(items); start++ {
		item := items[start]
		if item.kind != "message" || (item.role != "developer" && item.role != "system") {
			break
		}
		if text := partTexts(item.content); len(text) > 0 {
			system = append(system, appendJoined(nil, "", text, `\n`))
		}
	}
	return system, items[start:]
}

// responsesEffort is the effort a Responses body asks for.
func responsesEffort(top map[string]json.RawMessage) string {
	reasoning, _ := parseObj(top["reasoning"])
	return strings.TrimSpace(reasoning.str("effort"))
}

// --- Responses -> Anthropic Messages -------------------------------------------

// anthropicDefaultMaxTokens is max_tokens when neither the request nor
// Options.MaxOutputTokens names one; every current Claude model accepts it.
const anthropicDefaultMaxTokens = 32000

// responsesMessagesBody is a Responses body translated for a Messages host:
// the effort becomes thinking fitted to the model (adaptive, or a manual
// budget where adaptive is not accepted) plus output_config.effort where the
// model takes one. The thinking it may see follows stripForeignThinking.
//
// The whole conversation is resent every turn, so without breakpoints
// Anthropic re-reads it all at full price. Four breakpoints (Anthropic's
// cap): the last tool, the system prompt, the end of the previous request
// (the last message before the newest assistant turn: that request wrote its
// cache there, so this one reads it exactly however many blocks the new turn
// adds; Anthropic's own lookback stops at 20 blocks), and the end of this
// request, which the next one reads.
func responsesMessagesBody(top map[string]json.RawMessage, opts Options) (map[string]json.RawMessage, toolBridge, error) {
	items, err := responsesInput(top["input"])
	if err != nil {
		return nil, nil, err
	}
	tools, bridge := bridgeTools(items0(top["tools"]))
	system, items := splitSystem(top["instructions"], items)
	b := newMsgBuilder(len(top["input"]))
	calls := map[string]bool{}
	route := opts.route()
	for _, item := range items {
		switch item.kind {
		case "message":
			role := item.role
			if role != "assistant" {
				role = "user"
			}
			if err := responsesPartsToBlocks(b, role, item.content); err != nil {
				return nil, nil, err
			}
		case "reasoning":
			// Only the runtime's envelope, and only its signed blocks: foreign
			// or unsigned reasoning never reaches Anthropic.
			if blocks, ok := envelopeBlocks(item.encrypted); ok {
				for _, block := range blocks {
					b.thinking(block, route)
				}
			}
		case "function_call":
			calls[item.callID] = true
			b.toolUse(safeCallID(item.callID), bridge.wire(item.namespace, item.name), item.arguments, nil)
		case "custom_tool_call":
			calls[item.callID] = true
			b.toolUse(safeCallID(item.callID), bridge.wire(item.namespace, item.name), nil,
				append(append([]byte(`{"input":`), tok(item.input)...), '}'))
		case "function_call_output", "custom_tool_call_output":
			if !calls[item.callID] {
				// A call this translation dropped (local_shell_call, ...):
				// its output still informs the model, as plain text.
				start := b.mark()
				b.arena = appendPartsText(append(b.arena, `{"type":"text","text":`...), "Tool output ("+item.callID+"):\n", item.output)
				b.arena = append(b.arena, '}')
				b.push("user", "text", start, "")
				continue
			}
			if err := toolResultBlock(b, safeCallID(item.callID), item.output); err != nil {
				return nil, nil, err
			}
		}
	}
	if len(b.messages) == 0 {
		return nil, nil, errors.New("input has no messages")
	}
	body := map[string]json.RawMessage{"messages": b.assemble(true)}
	defer b.release() // messagesFinish still reads the blocks
	if len(system) > 0 {
		body["system"] = append(appendJoined([]byte(`[{"type":"text","text":`), "", system, `\n\n`), `,"cache_control":{"type":"ephemeral"}}]`...)
	}
	if len(tools) > 0 {
		body["tools"] = messagesToolsFromBridge(tools)
		parallel := top["parallel_tool_calls"]
		if choice := anthropicChoiceFromResponses(top["tool_choice"], string(parallel) == "false", bridge); choice != nil {
			body["tool_choice"] = mustJSON(choice)
		}
	}
	effort := opts.Effort
	if effort == "" {
		effort = responsesEffort(top)
	}
	var maxTokens int
	_ = json.Unmarshal(top["max_output_tokens"], &maxTokens)
	messagesFinish(body, b, opts, effort, maxTokens, responsesSchema(top["text"]), top["temperature"], top["top_p"])
	return body, bridge, nil
}

func items0(raw []byte) []json.RawMessage {
	var out []json.RawMessage
	eachItem(raw, func(element []byte, _ obj) { out = append(out, element) })
	return out
}

// messagesToolsFromBridge renders bridged tools for Anthropic: byte-stable
// across turns whatever order the caller lists its tools in or serialises
// their schemas with (sorted by name, keys sorted), the last one a cache
// breakpoint.
func messagesToolsFromBridge(tools []bridgedTool) []byte {
	slices.SortStableFunc(tools, func(a, b bridgedTool) int { return strings.Compare(a.Name, b.Name) })
	var out []byte
	for at, tool := range tools {
		out = appendString(append(openElem(out), `{"name":`...), tool.Name)
		out = appendString(append(out, `,"description":`...), tool.Description)
		out = append(append(out, `,"input_schema":`...), canonicalJSON(tool.Parameters)...)
		if at == len(tools)-1 {
			out = append(out, `,"cache_control":{"type":"ephemeral"}`...)
		}
		out = append(out, '}')
	}
	return append(out, ']')
}

// responsesPartsToBlocks pushes a message's parts as Anthropic blocks.
func responsesPartsToBlocks(b *msgBuilder, role string, content []byte) error {
	var err error
	eachPart(content, func(kind string, part obj) {
		if err != nil {
			return
		}
		switch kind {
		case "input_text", "output_text", "text":
			b.textBlock(role, "", part.get("text"), nil)
		case "refusal":
			b.textBlock(role, "", part.get("refusal"), nil)
		case "input_image":
			b.imageBlock(role, imageURLOf(part))
		case "input_file":
			err = b.documentBlock(role, part.get("filename"), part.get("file_data"), part.get("file_url"), part.get("file_id"))
		}
	})
	return err
}

// imageURLOf is an input_image's (or chat image_url's) URL token.
func imageURLOf(part obj) []byte {
	url := part.get("image_url")
	if object, ok := parseObj(url); ok {
		url = object.get("url")
	}
	if !isStr(url) {
		return nil
	}
	return url
}

// toolResultBlock pushes a tool output as a tool_result: its parts as blocks
// (text, images, files), "" when it has none.
func toolResultBlock(b *msgBuilder, id string, output []byte) error {
	inner := newMsgBuilder(len(output))
	if err := responsesPartsToBlocks(inner, "user", output); err != nil {
		return err
	}
	start := b.mark()
	b.arena = appendString(append(b.arena, `{"type":"tool_result","tool_use_id":`...), id)
	if len(inner.messages) == 0 {
		b.arena = append(b.arena, `,"content":""}`...)
	} else {
		b.arena = append(b.arena, `,"content":[`...)
		for at, block := range inner.messages[0].blocks {
			if at > 0 {
				b.arena = append(b.arena, ',')
			}
			b.arena = append(b.arena, inner.arena[block.start:block.end]...)
		}
		b.arena = append(b.arena, "]}"...)
	}
	inner.release()
	b.push("user", "tool_result", start, "")
	return nil
}

// responsesSchema is output_config.format for a Responses text.format that is
// a JSON schema; nil otherwise.
func responsesSchema(text []byte) []byte {
	options, _ := parseObj(text)
	format, _ := parseObj(options.get("format"))
	if format.str("type") != "json_schema" {
		return nil
	}
	return messagesSchemaFormat(format.get("schema"))
}

// envelopeBlocks are the blocks a runtime envelope (an encrypted_content
// string token) carries; false for anything the runtime did not write.
func envelopeBlocks(encrypted []byte) ([]json.RawMessage, bool) {
	if !bytes.HasPrefix(inner(encrypted), envelopeMarker) {
		return nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(jstr(encrypted))
	if err != nil {
		return nil, false
	}
	var envelope reasoningEnvelope
	if json.Unmarshal(raw, &envelope) != nil || envelope.Caveman != reasoningEnvelopeVersion {
		return nil, false
	}
	return envelope.Blocks, true
}

// anthropicChoiceFromResponses maps tool_choice and parallel_tool_calls.
func anthropicChoiceFromResponses(raw json.RawMessage, noParallel bool, bridge toolBridge) map[string]any {
	choice := map[string]any{"type": "auto"}
	switch {
	case isStr(raw):
		switch jstr(raw) {
		case "required":
			choice["type"] = "any"
		case "none":
			return map[string]any{"type": "none"}
		}
	default:
		named, _ := parseObj(raw)
		if named.str("type") == "function" && named.str("name") != "" {
			choice = map[string]any{"type": "tool", "name": bridge.wire("", named.str("name"))}
		}
	}
	if noParallel {
		choice["disable_parallel_tool_use"] = true
	}
	return choice
}

// --- Responses -> OpenAI chat ----------------------------------------------------

// responsesChatBody is a Responses body translated for a chat upstream.
// Reasoning items are dropped (chat has no field that carries another model's
// reasoning back, and replaying it as text would be wrong), except the
// reasoning the runtime signed `replay` (the same chat route and model's own,
// carried in encrypted_content): that goes back as reasoning_content on the
// assistant message it led.
func responsesChatBody(top map[string]json.RawMessage, opts Options) (map[string]json.RawMessage, toolBridge, error) {
	items, err := responsesInput(top["input"])
	if err != nil {
		return nil, nil, err
	}
	tools, bridge := bridgeTools(items0(top["tools"]))
	system, items := splitSystem(top["instructions"], items)
	replay := opts.replay()
	w := chatWriter{dst: make([]byte, 0, len(top["input"])+len(top["instructions"])+1024)}
	w.dst = append(w.dst, '[')
	if len(system) > 0 {
		w.dst = appendJoined(append(w.dst, `{"role":"system","content":`...), "", system, `\n\n`)
		w.dst = append(w.dst, '}')
	}
	calls := map[string]bool{}
	for _, item := range items {
		switch item.kind {
		case "message":
			if item.role == "assistant" {
				w.assistantText(partTexts(item.content))
				continue
			}
			if err := w.user(item.content); err != nil {
				return nil, nil, err
			}
		case "reasoning":
			if text := replayedReasoning(item.encrypted, replay); text != "" {
				w.addReasoning(text)
			}
		case "function_call":
			arguments := item.arguments
			if len(arguments) <= 2 {
				arguments = []byte(`"{}"`)
			}
			w.call(safeCallID(item.callID), bridge.wire(item.namespace, item.name), arguments)
			calls[item.callID] = true
		case "custom_tool_call":
			w.call(safeCallID(item.callID), bridge.wire(item.namespace, item.name),
				appendString(nil, append(append([]byte(`{"input":`), tok(item.input)...), '}')))
			calls[item.callID] = true
		case "function_call_output", "custom_tool_call_output":
			if !calls[item.callID] {
				w.flush()
				w.dst = appendPartsText(append(appendComma(w.dst), `{"role":"user","content":`...), "Tool output ("+item.callID+"):\n", item.output)
				w.dst = append(w.dst, '}')
				continue
			}
			if err := w.tool(safeCallID(item.callID), item.output); err != nil {
				return nil, nil, err
			}
		}
	}
	w.flush()
	out := map[string]json.RawMessage{"model": mustJSON(opts.Model), "messages": append(w.dst, ']'), "stream": json.RawMessage(`true`),
		"stream_options": json.RawMessage(`{"include_usage":true}`)}
	if len(tools) > 0 {
		var functions []byte
		for _, tool := range tools {
			functions = appendString(append(openElem(functions), `{"type":"function","function":{"name":`...), tool.Name)
			functions = appendString(append(functions, `,"description":`...), tool.Description)
			functions = append(append(append(functions, `,"parameters":`...), tool.Parameters...), "}}"...)
		}
		out["tools"] = append(functions, ']')
		if choice := chatChoiceFromResponses(top["tool_choice"], bridge); choice != nil {
			out["tool_choice"] = mustJSON(choice)
		}
		if parallel := top["parallel_tool_calls"]; string(parallel) == "true" || string(parallel) == "false" {
			out["parallel_tool_calls"] = parallel
		}
	}
	if limit := top["max_output_tokens"]; !isNull(limit) && string(limit) != "0" {
		out["max_tokens"] = limit
	}
	for _, knob := range []string{"temperature", "top_p"} {
		if value := top[knob]; !isNull(value) {
			out[knob] = value
		}
	}
	if format := chatFormatFromResponses(top["text"]); format != nil {
		out["response_format"] = format
	}
	effort := opts.Effort
	if effort == "" {
		effort = responsesEffort(top)
	}
	fitChat(out, opts, true, clampEffort(effort, nil))
	return out, bridge, nil
}

func chatChoiceFromResponses(raw json.RawMessage, bridge toolBridge) any {
	if mode := jstr(raw); mode != "" {
		return mode // auto | required | none
	}
	named, _ := parseObj(raw)
	if named.str("type") == "function" && named.str("name") != "" {
		return map[string]any{"type": "function", "function": map[string]any{"name": bridge.wire("", named.str("name"))}}
	}
	return nil
}

// chatFormatFromResponses carries `text.format` (codex exec --output-schema)
// as response_format.
func chatFormatFromResponses(text []byte) []byte {
	options, _ := parseObj(text)
	format, _ := parseObj(options.get("format"))
	switch format.str("type") {
	case "json_schema":
		if isNull(format.get("schema")) {
			return nil
		}
		var strict, description []byte
		if value := format.get("strict"); string(value) == "true" || string(value) == "false" {
			strict = value
		}
		if value := format.get("description"); isStr(value) {
			description = value
		}
		return appendChatSchemaFormat(nil, tok(format.get("name")), format.get("schema"), strict, description)
	case "json_object":
		return []byte(`{"type":"json_object"}`)
	}
	return nil
}

// replayedReasoning is the text of the thinking an envelope carries signed
// `replay` (a chat route's own reasoning, streamChatToResponses), "" for
// anything else.
func replayedReasoning(encrypted []byte, replay string) string {
	if replay == "" {
		return ""
	}
	blocks, _ := envelopeBlocks(encrypted)
	var parts []string
	for _, block := range blocks {
		var fields struct {
			Type      string `json:"type"`
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
		}
		if json.Unmarshal(block, &fields) == nil && fields.Type == "thinking" && fields.Signature == replay {
			parts = append(parts, fields.Thinking)
		}
	}
	return strings.Join(parts, "\n")
}

// --- native OpenAI Responses ------------------------------------------------------

// responsesNativeBody fits the caller's body for a Responses host: `model`
// rewritten, effort set when given. Reasoning the host cannot verify is
// removed: the runtime's Anthropic envelopes, and items with no
// encrypted_content at all (another model's, which store:false could not look
// up). A route's own envelopes are restored for that route; OpenAI's own
// encrypted_content passes untouched.
func responsesNativeBody(body map[string]json.RawMessage, model, effort, route string) error {
	body["model"] = mustJSON(model)
	restore := route != "" && route != "openai"
	if input := body["input"]; !isStr(input) && bytes.Contains(input, []byte(`"reasoning"`)) {
		if edited, changed := editReasoning(input, func(encrypted []byte) ([]byte, bool) {
			if restore {
				if blob := envelopeBlob(encrypted, route); blob != nil {
					return blob, true
				}
			}
			if _, ours := envelopeBlocks(encrypted); ours || isNull(encrypted) || string(encrypted) == `""` {
				return nil, false
			}
			return encrypted, true
		}); changed {
			body["input"] = edited
		}
	}
	if effort = fitOpenAIEffort(effort, openAIEfforts(model)); effort != "" {
		reasoning, _ := parseObj(body["reasoning"])
		body["reasoning"] = objectWith(reasoning, "effort", appendString(nil, effort))
	}
	return nil
}

// envelopeBlob is the encrypted_content a route's own envelope carries for
// that route (a string token), nil for anything else.
func envelopeBlob(encrypted []byte, route string) []byte {
	if !isStr(encrypted) || !bytes.HasPrefix(inner(encrypted), envelopeMarker) {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(jstr(encrypted))
	var envelope reasoningEnvelope
	if err != nil || json.Unmarshal(raw, &envelope) != nil || envelope.Route != route || envelope.Blob == "" {
		return nil
	}
	return appendString(nil, envelope.Blob)
}

// editReasoning rewrites the reasoning items of an input array: keep returns
// the encrypted_content to send (nil keeps the item as it came when it is
// unchanged) or false to drop the item. Every other item keeps its bytes.
func editReasoning(input []byte, keep func(encrypted []byte) ([]byte, bool)) ([]byte, bool) {
	return editArray(input, func(_ []byte, item obj) ([]byte, bool) {
		if item == nil || item.str("type") != "reasoning" {
			return nil, false
		}
		encrypted := item.get("encrypted_content")
		sent, kept := keep(encrypted)
		switch {
		case !kept:
			return nil, true
		case sent != nil && string(sent) != string(encrypted):
			return objectWith(item, "encrypted_content", sent), false
		}
		return nil, false
	})
}

// dropReasoning removes from body's input every reasoning item carrying the
// runtime's envelope; it reports whether any went.
func dropReasoning(body map[string]json.RawMessage) bool {
	edited, changed := editReasoning(body["input"], func(encrypted []byte) ([]byte, bool) {
		_, ours := envelopeBlocks(encrypted)
		return nil, !ours
	})
	if changed {
		body["input"] = edited
	}
	return changed
}

// --- chat messages ---------------------------------------------------------------

// chatWriter renders chat messages. An assistant message stays open so the
// tool calls and replayed reasoning that follow attach to it (chat wants a
// call's results right after the message that holds it); images and files a
// tool output carries follow the last tool message, as a user message.
type chatWriter struct {
	dst       []byte
	open      bool     // an assistant message is open
	text      [][]byte // its text tokens
	textSet   bool     // it was opened by text (content "" included)
	calls     []byte
	reasoning []string // its replayed reasoning
	pending   []string // replayed reasoning waiting for the assistant message it led
	attached  []byte   // parts tool outputs carried
}

func (w *chatWriter) openAssistant() {
	if !w.open {
		w.flushAttached()
		w.open, w.reasoning, w.pending = true, w.pending, nil
	}
}

func (w *chatWriter) assistantText(tokens [][]byte) {
	w.openAssistant()
	w.textSet = true
	for _, token := range tokens {
		if len(token) > 2 {
			w.text = append(w.text, token)
		}
	}
}

func (w *chatWriter) addReasoning(text string) {
	if w.open {
		w.reasoning = append(w.reasoning, text)
	} else {
		w.pending = append(w.pending, text)
	}
}

func (w *chatWriter) call(id, name string, arguments []byte) {
	w.openAssistant()
	w.calls = appendString(append(openElem(w.calls), `{"id":`...), id)
	w.calls = appendString(append(w.calls, `,"type":"function","function":{"name":`...), name)
	w.calls = append(append(append(w.calls, `,"arguments":`...), arguments...), "}}"...)
}

// flush writes the open assistant message and any parts tool outputs left.
func (w *chatWriter) flush() {
	w.flushAssistant()
	w.flushAttached()
}

func (w *chatWriter) flushAssistant() {
	if !w.open {
		return
	}
	w.dst = append(appendComma(w.dst), `{"role":"assistant"`...)
	if w.textSet {
		w.dst = appendJoined(append(w.dst, `,"content":`...), "", w.text, `\n`)
	}
	if w.calls != nil {
		w.dst = append(append(append(w.dst, `,"tool_calls":`...), w.calls...), ']')
	}
	if len(w.reasoning) > 0 {
		w.dst = appendString(append(w.dst, `,"reasoning_content":`...), strings.Join(w.reasoning, "\n"))
	}
	w.dst = append(w.dst, '}')
	w.open, w.text, w.textSet, w.calls, w.reasoning = false, nil, false, nil, nil
}

func (w *chatWriter) flushAttached() {
	if w.attached != nil {
		w.dst = append(append(append(appendComma(w.dst), `{"role":"user","content":`...), w.attached...), "]}"...)
		w.attached = nil
	}
}

// user writes a user message: its text, or parts when it carries images or
// files.
func (w *chatWriter) user(content []byte) error {
	w.flush()
	w.pending = nil
	parts, media, err := chatParts(content)
	if err != nil {
		return err
	}
	w.dst = append(appendComma(w.dst), `{"role":"user","content":`...)
	if media {
		w.dst = append(append(w.dst, parts...), ']')
	} else {
		w.dst = appendPartsText(w.dst, "", content)
	}
	w.dst = append(w.dst, '}')
	return nil
}

// tool writes a tool message; the output's images and files wait for the
// last tool message of the run.
func (w *chatWriter) tool(id string, output []byte) error {
	w.flushAssistant() // a tool message follows the calls or another tool message
	w.pending = nil
	parts, media, err := chatParts(output)
	if err != nil {
		return err
	}
	w.dst = append(appendComma(w.dst), `{"role":"tool","content":`...)
	w.dst = appendPartsText(w.dst, "", output)
	w.dst = appendString(append(w.dst, `,"tool_call_id":`...), id)
	w.dst = append(w.dst, '}')
	if media {
		eachItem(append(parts, ']'), func(raw []byte, part obj) {
			if kind := part.str("type"); kind == "image_url" || kind == "file" {
				w.attached = append(openElem(w.attached), raw...)
			}
		})
	}
	return nil
}

// chatParts renders Responses content parts as chat parts (an unclosed
// array) and reports whether any is an image or file; a file by URL, which
// chat cannot take, is refused.
func chatParts(content []byte) ([]byte, bool, error) {
	var parts []byte
	media := false
	var err error
	eachPart(content, func(kind string, part obj) {
		if err != nil {
			return
		}
		switch kind {
		case "input_image":
			if url := imageURLOf(part); url != nil {
				media = true
				parts = append(append(append(openElem(parts), `{"type":"image_url","image_url":{"url":`...), url...), "}}"...)
			}
		case "input_file":
			media = true
			var file []byte
			if file, err = chatFile(part); err == nil {
				parts = append(openElem(parts), file...)
			}
		default:
			text := part.get("text")
			if kind == "refusal" {
				text = part.get("refusal")
			}
			if isStr(text) && len(text) > 2 {
				parts = append(append(append(openElem(parts), `{"type":"text","text":`...), text...), '}')
			}
		}
	})
	return parts, media, err
}

// chatFile is a Responses input_file (or a chat file part's file object) as
// a chat file part.
func chatFile(part obj) ([]byte, error) {
	data, id := part.get("file_data"), part.get("file_id")
	switch {
	case isStr(data) && len(data) > 2:
		out := []byte(`{"type":"file","file":{"filename":`)
		if name := part.get("filename"); isStr(name) && len(name) > 2 {
			out = append(out, name...)
		} else {
			out = append(out, `"document.pdf"`...)
		}
		out = append(append(append(out, `,"file_data":`...), data...), "}}"...)
		return out, nil
	case isStr(id) && len(id) > 2:
		return append(append([]byte(`{"type":"file","file":{"file_id":`), id...), "}}"...), nil
	}
	return nil, errDocumentURL
}
