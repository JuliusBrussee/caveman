package translate

// Chat-completions bodies (OpenCode, Aider and other OpenAI-compatible
// clients) rendered for a Messages or Responses upstream, so a chat caller
// can reach Claude and a Responses-only GPT. History is copied byte for byte.
//
// Reasoning. A chat caller gets a Messages or Responses host's reasoning as
// reasoning_content (text it may show) plus one reasoning_details entry
// {"type":"reasoning.encrypted","data":<envelope>}: the signed thinking
// blocks, or the Responses host's encrypted_content tagged with its route,
// under the runtime's "caveman" envelope (reasoningEnvelope). A client that
// sends reasoning_details back has that reasoning replayed to the host that
// wrote it and to nothing else: every other host, chat hosts and OpenAI's own
// chat API included, gets the entry and its reasoning_content removed
// (stripChatEnvelopes, ChatNative). Reasoning text without an envelope is
// never replayed to a Messages or Responses host (it carries no signature).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/JuliusBrussee/caveman/shared/platform/catalog"
)

// chatTop is the chat body fields both translations read.
type chatTop struct {
	effort    string
	maxTokens []byte
	format    obj // response_format
}

func readChatTop(top map[string]json.RawMessage, opts Options) chatTop {
	c := chatTop{effort: opts.Effort, maxTokens: top["max_completion_tokens"]}
	if isNull(c.maxTokens) {
		c.maxTokens = top["max_tokens"]
	}
	if c.effort == "" {
		c.effort = jstr(top["reasoning_effort"])
	}
	if reasoning, ok := parseObj(top["reasoning"]); ok && c.effort == "" {
		c.effort = reasoning.str("effort")
	}
	c.format, _ = parseObj(top["response_format"])
	return c
}

// eachChatPart calls fn with each part of a chat content value (a string is
// one text part).
func eachChatPart(content []byte, fn func(kind string, part obj)) {
	if isStr(content) {
		fn("text", obj{{key: []byte("text"), val: content}})
		return
	}
	eachItem(content, func(_ []byte, part obj) {
		if part != nil {
			fn(part.str("type"), part)
		}
	})
}

// chatEnvelopes are the runtime envelopes (string tokens) an assistant
// message's reasoning_details carry.
func chatEnvelopes(message obj) [][]byte {
	details := message.get("reasoning_details")
	if !bytes.Contains(details, envelopeMarker) {
		return nil
	}
	var out [][]byte
	eachItem(details, func(_ []byte, entry obj) {
		if data := entry.get("data"); bytes.HasPrefix(inner(data), envelopeMarker) {
			out = append(out, data) // whatever type a client relabelled it
		}
	})
	return out
}

// --- chat -> Anthropic Messages ----------------------------------------------------

// chatMessagesBody is a chat body translated for a Messages host. The cache
// breakpoints are the Responses translation's four, unless the caller placed
// its own (cache_control on content parts, OpenRouter's convention): those
// are kept, and none added.
func chatMessagesBody(top map[string]json.RawMessage, opts Options) (map[string]json.RawMessage, error) {
	c := readChatTop(top, opts)
	own := hasCacheControl(top["messages"])
	b := newMsgBuilder(len(top["messages"]))
	b.strict = true // a chat client may not send reasoning back: no thinking left is no thinking
	route := opts.route()
	calls := map[string]bool{} // tool call ids the conversation made
	var system []byte
	leading := true
	var err error
	ok := eachItem(top["messages"], func(_ []byte, message obj) {
		if err != nil || message == nil {
			return
		}
		role, content := message.str("role"), message.get("content")
		cache := message.get("cache_control") // a message-level breakpoint goes on its last block
		if leading && (role == "system" || role == "developer") {
			last, ownCache := -1, false
			eachChatPart(content, func(kind string, part obj) {
				if text := part.get("text"); kind == "text" && isStr(text) && len(text) > 2 {
					system = openElem(system)
					last = len(system)
					system = append(append(system, `{"type":"text","text":`...), text...)
					partCache := part.get("cache_control")
					if ownCache = partCache != nil; ownCache {
						system = append(append(system, `,"cache_control":`...), partCache...)
					}
					system = append(system, '}')
				}
			})
			if cache != nil && last >= 0 && !ownCache {
				system = append(append(append(system[:len(system)-1], `,"cache_control":`...), cache...), '}')
			}
			return
		}
		leading = false
		blocks := b.blockCount()
		defer func() {
			if cache != nil && err == nil {
				b.cacheLast(blocks, cache)
			}
		}()
		switch role {
		case "assistant":
			for _, envelope := range chatEnvelopes(message) {
				blocks, _ := envelopeBlocks(envelope)
				for _, block := range blocks {
					b.thinking(block, route)
				}
			}
			eachChatPart(content, func(kind string, part obj) {
				if kind == "text" {
					b.textBlock("assistant", "", part.get("text"), part.get("cache_control"))
				}
			})
			if refusal := message.get("refusal"); isStr(refusal) && isNull(content) {
				b.textBlock("assistant", "", refusal, nil)
			}
			eachItem(message.get("tool_calls"), func(_ []byte, call obj) {
				function, _ := parseObj(call.get("function"))
				arguments := function.get("arguments")
				if len(arguments) <= 2 {
					arguments = []byte(`"{}"`)
				}
				calls[call.str("id")] = true
				b.toolUse(safeCallID(call.str("id")), function.str("name"), arguments, nil)
			})
		case "tool":
			if id := message.str("tool_call_id"); !calls[id] {
				// A result whose call is gone (compacted away): plain text, as
				// a tool_result without its tool_use is refused.
				start := b.mark()
				b.arena = appendJoined(append(b.arena, `{"type":"text","text":`...), "Tool output ("+id+"):\n", chatTexts(content), `\n`)
				b.arena = append(b.arena, '}')
				b.push("user", "text", start, "")
				return
			}
			start := b.mark()
			b.arena = appendString(append(b.arena, `{"type":"tool_result","tool_use_id":`...), safeCallID(message.str("tool_call_id")))
			if isStr(content) {
				b.arena = append(append(b.arena, `,"content":`...), content...)
			} else {
				b.arena = appendJoined(append(b.arena, `,"content":`...), "", chatTexts(content), `\n`)
			}
			b.arena = append(b.arena, '}')
			b.push("user", "tool_result", start, "")
		default: // user, a later system or developer message (as user text), function
			err = chatPartsToBlocks(b, content)
		}
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("messages is not a list of messages")
	}
	if len(b.messages) == 0 {
		return nil, errors.New("messages has no conversation")
	}
	body := map[string]json.RawMessage{"messages": b.assemble(!own)}
	defer b.release() // messagesFinish still reads the blocks
	if system != nil {
		if !own {
			system = append(system[:len(system)-1], `,"cache_control":{"type":"ephemeral"}}`...)
		}
		body["system"] = append(system, ']')
	}
	tools, err := messagesToolsFromChat(top["tools"], !own)
	if err != nil {
		return nil, err
	}
	if tools != nil {
		body["tools"] = tools
		if choice := anthropicChoiceFromChat(top["tool_choice"], string(top["parallel_tool_calls"]) == "false"); choice != nil {
			body["tool_choice"] = choice
		}
	}
	if stop := top["stop"]; isStr(stop) {
		body["stop_sequences"] = append(append([]byte{'['}, stop...), ']')
	} else if len(items(stop)) > 0 {
		body["stop_sequences"] = stop
	}
	var maxTokens int
	_ = json.Unmarshal(c.maxTokens, &maxTokens)
	var format []byte
	if c.format.str("type") == "json_schema" {
		schema, _ := parseObj(c.format.get("json_schema"))
		format = messagesSchemaFormat(schema.get("schema"))
	}
	messagesFinish(body, b, opts, c.effort, maxTokens, format, top["temperature"], top["top_p"])
	return body, nil
}

// chatTexts are the text tokens of a chat content value.
func chatTexts(content []byte) [][]byte {
	var out [][]byte
	eachChatPart(content, func(kind string, part obj) {
		if text := part.get("text"); kind == "text" && isStr(text) && len(text) > 2 {
			out = append(out, text)
		}
	})
	return out
}

// chatPartsToBlocks pushes a user message's parts as Anthropic blocks; audio,
// which Claude does not take, is refused.
func chatPartsToBlocks(b *msgBuilder, content []byte) error {
	var err error
	eachChatPart(content, func(kind string, part obj) {
		if err != nil {
			return
		}
		switch kind {
		case "text":
			b.textBlock("user", "", part.get("text"), part.get("cache_control"))
		case "image_url":
			b.imageBlock("user", imageURLOf(part))
		case "file":
			file, _ := parseObj(part.get("file"))
			err = b.documentBlock("user", file.get("filename"), file.get("file_data"), nil, file.get("file_id"))
		default:
			err = fmt.Errorf("a %q content part cannot go to a Messages host", kind)
		}
	})
	return err
}

// messagesToolsFromChat renders chat functions as Anthropic tools, the last a
// cache breakpoint when cache is set; any other tool type is refused.
func messagesToolsFromChat(raw []byte, cache bool) ([]byte, error) {
	var out []byte
	var err error
	eachItem(raw, func(_ []byte, tool obj) {
		if err != nil {
			return
		}
		function, ok := parseObj(tool.get("function"))
		if tool.str("type") != "function" || !ok {
			err = fmt.Errorf("a %q tool cannot go to a Messages host", tool.str("type"))
			return
		}
		out = append(append(openElem(out), `{"name":`...), tok(function.get("name"))...)
		if description := function.get("description"); isStr(description) {
			out = append(append(out, `,"description":`...), description...)
		}
		schema := function.get("parameters")
		if isNull(schema) {
			schema = []byte(`{"type":"object","properties":{}}`)
		}
		out = append(append(append(out, `,"input_schema":`...), schema...), '}')
	})
	if err != nil || out == nil {
		return nil, err
	}
	if cache {
		out = append(out[:len(out)-1], `,"cache_control":{"type":"ephemeral"}}`...)
	}
	return append(out, ']'), nil
}

// anthropicChoiceFromChat maps tool_choice and parallel_tool_calls.
func anthropicChoiceFromChat(raw []byte, noParallel bool) []byte {
	var out []byte
	switch mode := jstr(raw); {
	case mode == "none":
		return []byte(`{"type":"none"}`)
	case mode == "required":
		out = []byte(`{"type":"any"`)
	case mode == "auto" || isNull(raw):
		if !noParallel && isNull(raw) {
			return nil
		}
		out = []byte(`{"type":"auto"`)
	default:
		named, _ := parseObj(raw)
		function, _ := parseObj(named.get("function"))
		if function.str("name") == "" {
			return nil
		}
		out = append([]byte(`{"type":"tool","name":`), function.get("name")...)
	}
	if noParallel {
		out = append(out, `,"disable_parallel_tool_use":true`...)
	}
	return append(out, '}')
}

// --- chat -> Responses -----------------------------------------------------------------

// chatResponsesBody is a chat body translated for a Responses host:
// stateless (store false, the whole conversation), always streamed, with
// the host's own reasoning replayed from the envelopes tagged with its route.
func chatResponsesBody(top map[string]json.RawMessage, opts Options) (map[string]json.RawMessage, error) {
	c := readChatTop(top, opts)
	route := opts.route()
	dst := make([]byte, 0, capHint(len(top["messages"]), 1024))
	dst = append(dst, '[')
	var instructions [][]byte
	leading := true
	calls := map[string]bool{} // tool call ids the conversation made
	var err error
	ok := eachItem(top["messages"], func(_ []byte, message obj) {
		if err != nil || message == nil {
			return
		}
		role, content := message.str("role"), message.get("content")
		if leading && (role == "system" || role == "developer") {
			if texts := chatTexts(content); len(texts) > 0 {
				instructions = append(instructions, appendJoined(nil, "", texts, `\n`))
			}
			return
		}
		leading = false
		switch role {
		case "assistant":
			for _, envelope := range chatEnvelopes(message) {
				if blob := envelopeBlob(envelope, route); blob != nil {
					dst = append(append(appendComma(dst), `{"type":"reasoning","summary":[],"encrypted_content":`...), blob...)
					dst = append(dst, '}')
				}
			}
			if texts := chatTexts(content); len(texts) > 0 {
				dst = appendJoined(append(appendComma(dst), `{"type":"message","role":"assistant","content":[{"type":"output_text","text":`...), "", texts, `\n`)
				dst = append(dst, "}]}"...)
			}
			eachItem(message.get("tool_calls"), func(_ []byte, call obj) {
				function, _ := parseObj(call.get("function"))
				arguments := function.get("arguments")
				if len(arguments) <= 2 {
					arguments = []byte(`"{}"`)
				}
				calls[call.str("id")] = true
				dst = appendString(append(appendComma(dst), `{"type":"function_call","call_id":`...), safeCallID(call.str("id")))
				dst = append(append(dst, `,"name":`...), tok(function.get("name"))...)
				dst = append(append(append(dst, `,"arguments":`...), arguments...), '}')
			})
		case "tool":
			if id := message.str("tool_call_id"); !calls[id] {
				// A result whose call is gone: plain text, as an output
				// without its call is refused.
				dst = appendJoined(append(appendComma(dst), `{"type":"message","role":"user","content":[{"type":"input_text","text":`...), "Tool output ("+id+"):\n", chatTexts(content), `\n`)
				dst = append(dst, "}]}"...)
				return
			}
			dst = appendString(append(appendComma(dst), `{"type":"function_call_output","call_id":`...), safeCallID(message.str("tool_call_id")))
			if isStr(content) {
				dst = append(append(dst, `,"output":`...), content...)
			} else {
				dst = appendJoined(append(dst, `,"output":`...), "", chatTexts(content), `\n`)
			}
			dst = append(dst, '}')
		default:
			if role != "user" {
				role = "developer"
			}
			var parts []byte
			if parts, err = responsesPartsFromChat(content); err == nil && parts != nil {
				dst = appendString(append(appendComma(dst), `{"type":"message","role":`...), role)
				dst = append(append(append(dst, `,"content":`...), parts...), "]}"...)
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("messages is not a list of messages")
	}
	out := map[string]json.RawMessage{"model": mustJSON(opts.Model), "input": append(dst, ']'), "store": json.RawMessage(`false`), "stream": json.RawMessage(`true`)}
	if instructions != nil {
		out["instructions"] = appendJoined(nil, "", instructions, `\n\n`)
	}
	if !isNull(c.maxTokens) && string(c.maxTokens) != "0" {
		out["max_output_tokens"] = c.maxTokens
	}
	// A model the catalog lists with effort levels reasons, so its reasoning
	// comes back (encrypted) to be replayed even when the caller named no
	// effort; any other model gets reasoning fields only when one was asked
	// ("none" not even then: there is no reasoning to switch off).
	_, reasons := catalog.EffortLevels("openai", opts.Model)
	switch effort := fitOpenAIEffort(c.effort, openAIEfforts(opts.Model)); {
	case effort == "none":
		if reasons {
			out["reasoning"] = json.RawMessage(`{"effort":"none"}`)
		}
	case effort != "" || reasons:
		reasoning := []byte(`{"summary":"auto"}`)
		if effort != "" {
			reasoning = append(appendString([]byte(`{"effort":`), effort), `,"summary":"auto"}`...)
		}
		out["reasoning"] = reasoning
		out["include"] = json.RawMessage(`["reasoning.encrypted_content"]`)
	}
	tools, err := responsesToolsFromChat(top["tools"])
	if err != nil {
		return nil, err
	}
	if tools != nil {
		out["tools"] = tools
		switch choice := top["tool_choice"]; {
		case isStr(choice):
			out["tool_choice"] = choice
		case !isNull(choice):
			named, _ := parseObj(choice)
			function, _ := parseObj(named.get("function"))
			if name := function.get("name"); isStr(name) {
				out["tool_choice"] = append(append([]byte(`{"type":"function","name":`), name...), '}')
			}
		}
		if parallel := top["parallel_tool_calls"]; string(parallel) == "true" || string(parallel) == "false" {
			out["parallel_tool_calls"] = parallel
		}
	}
	switch c.format.str("type") {
	case "json_schema":
		schema, _ := parseObj(c.format.get("json_schema"))
		if !isNull(schema.get("schema")) {
			var strict, description []byte
			if value := schema.get("strict"); string(value) == "true" || string(value) == "false" {
				strict = value
			}
			if value := schema.get("description"); isStr(value) {
				description = value
			}
			out["text"] = appendResponsesSchemaFormat(nil, tok(schema.get("name")), schema.get("schema"), strict, description)
		}
	case "json_object":
		out["text"] = json.RawMessage(`{"format":{"type":"json_object"}}`)
	}
	if key := top["prompt_cache_key"]; isStr(key) {
		out["prompt_cache_key"] = key
	}
	if opts.ChatGPTLogin {
		chatgptLoginBody(out)
	}
	dropParams(out, opts)
	return out, nil
}

// responsesPartsFromChat renders chat content as Responses input parts (an
// unclosed array, nil when empty); audio is refused.
func responsesPartsFromChat(content []byte) ([]byte, error) {
	var parts []byte
	var err error
	eachChatPart(content, func(kind string, part obj) {
		if err != nil {
			return
		}
		switch kind {
		case "text":
			if text := part.get("text"); isStr(text) {
				parts = append(append(append(openElem(parts), `{"type":"input_text","text":`...), text...), '}')
			}
		case "image_url":
			if url := imageURLOf(part); url != nil {
				parts = append(append(append(openElem(parts), `{"type":"input_image","image_url":`...), url...), '}')
			}
		case "file":
			file, _ := parseObj(part.get("file"))
			parts = append(openElem(parts), `{"type":"input_file"`...)
			found := false
			for _, field := range []string{"filename", "file_data", "file_id"} {
				if value := file.get(field); isStr(value) {
					parts = append(appendKey(parts, field), value...)
					found = found || field != "filename"
				}
			}
			parts = append(parts, '}')
			if !found {
				err = errors.New("file part has no data")
			}
		default:
			err = fmt.Errorf("a %q content part cannot go to a Responses host", kind)
		}
	})
	return parts, err
}

// responsesToolsFromChat renders chat functions as Responses functions; any
// other tool type is refused.
func responsesToolsFromChat(raw []byte) ([]byte, error) {
	var out []byte
	var err error
	eachItem(raw, func(_ []byte, tool obj) {
		if err != nil {
			return
		}
		function, ok := parseObj(tool.get("function"))
		if tool.str("type") != "function" || !ok {
			err = fmt.Errorf("a %q tool cannot go to a Responses host", tool.str("type"))
			return
		}
		out = append(append(openElem(out), `{"type":"function","name":`...), tok(function.get("name"))...)
		if description := function.get("description"); isStr(description) {
			out = append(append(out, `,"description":`...), description...)
		}
		schema := function.get("parameters")
		if isNull(schema) {
			schema = []byte(`{"type":"object","properties":{}}`)
		}
		out = append(append(out, `,"parameters":`...), schema...)
		strict := function.get("strict")
		if string(strict) != "true" {
			strict = []byte(`false`)
		}
		out = append(append(append(out, `,"strict":`...), strict...), '}')
	})
	if err != nil || out == nil {
		return nil, err
	}
	return append(out, ']'), nil
}

// --- chat -> chat ---------------------------------------------------------------------

// stripChatEnvelopes removes from a chat body every runtime envelope in an
// assistant message's reasoning_details, with that message's reasoning text
// (it is the translated reasoning of another host): a chat host never sees
// another host's reasoning. Messages without one keep their bytes; it
// reports whether the body changed.
func stripChatEnvelopes(fields map[string]json.RawMessage) bool {
	messages := fields["messages"]
	if !bytes.Contains(messages, envelopeMarker) {
		return false
	}
	edited, changed := editArray(messages, func(_ []byte, message obj) ([]byte, bool) {
		if message == nil || chatEnvelopes(message) == nil {
			return nil, false
		}
		var kept []byte
		if !eachItem(message.get("reasoning_details"), func(entry []byte, detail obj) {
			if !bytes.HasPrefix(inner(detail.get("data")), envelopeMarker) {
				kept = append(openElem(kept), entry...)
			}
		}) {
			return nil, false // a malformed array is never rewritten into a shorter valid one
		}
		out := []byte{'{'}
		for _, member := range message {
			switch string(unescapedKey(member.key)) {
			case "reasoning_content", "reasoning":
				continue
			case "reasoning_details":
				if kept == nil {
					continue
				}
				member.val = append(kept, ']')
			}
			out = append(append(append(appendComma(out), '"'), member.key...), '"', ':')
			out = append(out, member.val...)
		}
		return append(out, '}'), false
	})
	if changed {
		fields["messages"] = edited
	}
	return changed
}

// ChatNative returns a chat body bound for a chat host's own API with the
// runtime's reasoning envelopes (and the reasoning text they came with)
// removed; the input slice unchanged (byte-identical) when it carries none.
func ChatNative(body []byte) []byte {
	if !bytes.Contains(body, envelopeMarker) {
		return body
	}
	fields, err := topFields(body)
	if err != nil || !stripChatEnvelopes(fields) {
		return body
	}
	return rawObject(fields)
}
