package translate

// Messages bodies (Claude Code) rendered for a chat or Responses upstream.
// History is copied byte for byte (text, tool inputs, base64), so the
// rendering of an earlier turn never changes as the conversation grows.
//
// Dropped, because neither wire has a field for it: cache_control,
// context_management, safeguards, the anthropic-beta semantics and Claude
// Code's metadata. Documents go as files (chat `file` parts, Responses
// input_file); one the target cannot take (a URL or Files API document on
// chat, a Files API one on Responses) refuses the request, so the pool falls
// back instead of losing it. Server tools are refused the same way.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/JuliusBrussee/caveman/shared/platform/catalog"
)

// messagesTop is the Messages body fields both translations read.
type messagesTop struct {
	top                     map[string]json.RawMessage
	thinking                string // thinking.type
	budget                  int    // thinking.budget_tokens
	effort                  string // output_config.effort
	schema                  []byte // output_config.format's (or the beta's output_format's) schema: a json_schema format
	toolChoice              obj
	disableParallel, stream bool
}

func readMessagesTop(top map[string]json.RawMessage) messagesTop {
	m := messagesTop{top: top, stream: string(top["stream"]) == "true"}
	if thinking, ok := parseObj(top["thinking"]); ok {
		m.thinking = thinking.str("type")
		_ = json.Unmarshal(thinking.get("budget_tokens"), &m.budget)
	}
	if output, ok := parseObj(top["output_config"]); ok {
		m.effort = output.str("effort")
		m.schema = jsonSchemaOf(output.get("format"))
	}
	if m.schema == nil {
		m.schema = jsonSchemaOf(top["output_format"])
	}
	if choice, ok := parseObj(top["tool_choice"]); ok {
		m.toolChoice = choice
		m.disableParallel = string(choice.get("disable_parallel_tool_use")) == "true"
	}
	return m
}

// jsonSchemaOf is a Messages format object's schema when it is a
// json_schema one.
func jsonSchemaOf(raw []byte) []byte {
	format, ok := parseObj(raw)
	if !ok || format.str("type") != "json_schema" || isNull(format.get("schema")) {
		return nil
	}
	return format.get("schema")
}

// effortFor is the effort a translated Messages body runs at: Options' own,
// else output_config's, else thinking off as "none" and thinking on as the
// middle effort ("" leaves the upstream's default).
func (m messagesTop) effortFor(opts Options) string {
	switch {
	case opts.Effort != "":
		return opts.Effort
	case m.effort != "":
		return m.effort
	case m.thinking == "disabled":
		return "none"
	case m.thinking == "enabled" || m.thinking == "adaptive":
		return "medium"
	}
	return ""
}

// systemTokens are the text tokens of `system` (a string or text blocks).
func systemTokens(raw []byte) [][]byte {
	if isStr(raw) {
		if len(raw) == 2 {
			return nil
		}
		return [][]byte{raw}
	}
	var out [][]byte
	eachItem(raw, func(_ []byte, block obj) {
		if text := block.get("text"); block.str("type") == "text" && isStr(text) && len(text) > 2 {
			out = append(out, text)
		}
	})
	return out
}

// eachBlock calls fn with each content block of a message (a string content
// is one text block).
func eachBlock(content []byte, fn func(kind string, block obj)) {
	if isStr(content) {
		if len(content) > 2 {
			fn("text", obj{{key: []byte("text"), val: content}})
		}
		return
	}
	eachItem(content, func(_ []byte, block obj) {
		if block != nil {
			fn(block.str("type"), block)
		}
	})
}

// mediaSource is an image or document block's source.
type mediaSource struct {
	kind              string // base64 | url | text | content | file
	media, data, url  []byte // escaped string contents
	content           []byte
	title, dataString []byte // the block's title token; data as its token (text sources)
}

func sourceOf(block obj) (mediaSource, error) {
	source, ok := parseObj(block.get("source"))
	if !ok {
		return mediaSource{}, errors.New("media block has no source")
	}
	out := mediaSource{kind: source.str("type"), media: inner(source.get("media_type")), data: inner(source.get("data")),
		url: inner(source.get("url")), content: source.get("content"), dataString: source.get("data")}
	if title := block.get("title"); isStr(title) && len(title) > 2 {
		out.title = title
	}
	if out.kind == "" && out.url != nil {
		out.kind = "url"
	}
	return out, nil
}

// imageURLToken appends an image source as one URL string (a data URI for
// base64).
func appendImageURL(dst []byte, block obj) ([]byte, error) {
	source, err := sourceOf(block)
	switch {
	case err != nil:
		return dst, err
	case source.kind == "url" || source.url != nil:
		return appendInner(dst, "", source.url), nil
	case len(source.data) == 0:
		return dst, errors.New("image block has no data")
	}
	return appendDataURI(dst, source.media, source.data), nil
}

// documentText is a text or content document's text tokens (nil for a file).
func (s mediaSource) documentText() [][]byte {
	switch s.kind {
	case "text":
		if isStr(s.dataString) {
			return [][]byte{s.dataString}
		}
	case "content":
		if isStr(s.content) {
			return [][]byte{s.content}
		}
		return systemTokens(s.content)
	}
	return nil
}

// filename is the name a document file goes under: its title, else one named
// after its media type.
func (s mediaSource) filename() []byte {
	if s.title != nil {
		return s.title
	}
	if bytes.Equal(s.media, []byte("application/pdf")) {
		return []byte(`"document.pdf"`)
	}
	return []byte(`"document"`)
}

var errDocumentURL = errors.New("a document by URL or file id cannot go to this upstream")

// --- Messages -> chat -----------------------------------------------------------

// messagesChatBody is a Messages body translated for a chat upstream.
func messagesChatBody(top map[string]json.RawMessage, opts Options) (map[string]json.RawMessage, error) {
	m := readMessagesTop(top)
	messages, err := messagesToChat(top, opts)
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{"model": mustJSON(opts.Model), "messages": messages}
	for from, to := range map[string]string{"max_tokens": "max_tokens", "temperature": "temperature", "top_p": "top_p", "stop_sequences": "stop"} {
		if value := top[from]; !isNull(value) && !(from == "max_tokens" && string(value) == "0") && (from != "stop_sequences" || len(items(value)) > 0) {
			out[to] = value
		}
	}
	if m.stream {
		out["stream"] = json.RawMessage(`true`)
	}
	if reasoning := chatReasoning(m.thinking, m.budget); reasoning != nil {
		out["reasoning"] = reasoning
	}
	tools, err := chatTools(top["tools"])
	if err != nil {
		return nil, err
	}
	if tools != nil {
		out["tools"] = tools
		if m.disableParallel {
			out["parallel_tool_calls"] = json.RawMessage(`false`)
		}
	}
	if choice := chatToolChoice(m.toolChoice); choice != nil {
		out["tool_choice"] = choice
	}
	if m.schema != nil {
		out["response_format"] = appendChatSchemaFormat(nil, []byte(`"output"`), m.schema, strictFor(m.schema), nil)
	}
	effort := m.effortFor(opts)
	switch {
	case effort == "medium" && m.effort == "" && opts.Effort == "" && opts.dialect() == dialectOpenRouter:
		effort = "" // chatReasoning wrote OpenRouter's field; it takes it as is
	case effort == "none" && opts.Effort == "" && m.effort == "" && opts.dialect() == dialectOpenAIChat:
		// Thinking off where the dialect has no off switch of its own: "none"
		// only for a model the catalog lists with levels (the lowest where it
		// has no "none"); another model may refuse reasoning_effort at all.
		levels, known := catalog.EffortLevels("openai", opts.Model)
		effort = ""
		if known {
			effort = fitOpenAIEffort("none", levels)
		}
	}
	fitChat(out, opts, m.stream, effort)
	return out, nil
}

// strictFor is the strict flag an Anthropic schema goes with on OpenAI: true
// (Anthropic constrains its output to the schema) when the schema meets
// OpenAI's strict rules, every object closed and every property required;
// false otherwise, which OpenAI would refuse under strict.
func strictFor(schema []byte) []byte {
	if strictSchema(schema) {
		return []byte(`true`)
	}
	return []byte(`false`)
}

func strictSchema(value []byte) bool {
	nodes, ok := parseTree(value)
	return ok && strictNode(nodes, 0)
}

// strictNode checks the schema at nodes[at] and every schema under it: an
// object type or properties needs additionalProperties false, every property
// required. A properties map's keys are names, not keywords.
func strictNode(nodes []node, at int) bool {
	n := nodes[at]
	switch n.val[0] {
	case '[':
		for child := at + 1; child < n.next; child = nodes[child].next {
			if !strictNode(nodes, child) {
				return false
			}
		}
		return true
	case '{':
	default:
		return true
	}
	properties, object, closed := -1, false, false
	var required []byte
	for child := at + 1; child < n.next; child = nodes[child].next {
		value := nodes[child].val
		switch string(unescapedKey(nodes[child].key)) {
		case "properties":
			if value[0] == '{' {
				properties = child
				continue // checked below, as names
			}
		case "additionalProperties":
			closed = string(value) == "false"
		case "required":
			required = value
		case "type":
			object = string(value) == `"object"` || value[0] == '[' && bytes.Contains(value, []byte(`"object"`))
		}
		if !strictNode(nodes, child) {
			return false
		}
	}
	if (object || properties >= 0) && !closed {
		return false
	}
	if properties < 0 {
		return true
	}
	names := map[string]bool{}
	for _, name := range items(required) {
		names[jstr(name)] = true
	}
	for property := properties + 1; property < nodes[properties].next; property = nodes[property].next {
		if !names[string(unescapedKey(nodes[property].key))] || !strictNode(nodes, property) {
			return false
		}
	}
	return true
}

// appendChatSchemaFormat is chat's response_format for a JSON schema.
func appendChatSchemaFormat(dst, name, schema, strict, description []byte) []byte {
	dst = append(dst, `{"type":"json_schema","json_schema":{"name":`...)
	dst = append(dst, name...)
	if description != nil {
		dst = append(append(dst, `,"description":`...), description...)
	}
	dst = append(append(dst, `,"schema":`...), schema...)
	if strict != nil {
		dst = append(append(dst, `,"strict":`...), strict...)
	}
	return append(dst, "}}"...)
}

// chatReasoning maps Anthropic's thinking onto OpenRouter's reasoning
// parameter. An explicit budget is a budget; "adaptive" has no number, so it
// becomes the middle effort rather than a made-up token count. fitChat
// rewrites it for every other dialect.
func chatReasoning(kind string, budget int) json.RawMessage {
	switch {
	case kind == "enabled" && budget > 0:
		return mustJSON(map[string]int{"max_tokens": budget})
	case kind == "enabled" || kind == "adaptive":
		return json.RawMessage(`{"effort":"medium"}`)
	}
	return nil
}

// chatTools renders Anthropic tools as chat functions; server tools (run by
// Anthropic's own API, nothing to forward) are refused.
func chatTools(raw []byte) (json.RawMessage, error) {
	var out []byte
	var err error
	eachItem(raw, func(_ []byte, tool obj) {
		if err != nil {
			return
		}
		schema := tool.get("input_schema")
		if isNull(schema) {
			err = fmt.Errorf("tool %q is an Anthropic server tool (type %q) and is only supported on a native Anthropic model", tool.str("name"), tool.str("type"))
			return
		}
		out = append(openElem(out), `{"type":"function","function":{"name":`...)
		out = append(out, tok(tool.get("name"))...)
		if description := tool.get("description"); isStr(description) && len(description) > 2 {
			out = append(append(out, `,"description":`...), description...)
		}
		out = append(append(append(out, `,"parameters":`...), schema...), "}}"...)
	})
	if err != nil || out == nil {
		return nil, err
	}
	return append(out, ']'), nil
}

func chatToolChoice(choice obj) json.RawMessage {
	switch choice.str("type") {
	case "auto":
		return json.RawMessage(`"auto"`)
	case "any":
		return json.RawMessage(`"required"`)
	case "none":
		return json.RawMessage(`"none"`)
	case "tool":
		return append(append([]byte(`{"type":"function","function":{"name":`), tok(choice.get("name"))...), "}}"...)
	}
	return nil
}

// messagesToChat renders the system prompt and messages as chat messages,
// sending the thinking blocks signed for the route back as reasoning_content
// (Options.replay) and the thought signatures of its tool calls back on them
// (Options.thoughts).
func messagesToChat(top map[string]json.RawMessage, opts Options) (json.RawMessage, error) {
	replay := opts.replay()
	thoughts, standIn := opts.thoughts()
	dst := make([]byte, 0, capHint(len(top["messages"]), capHint(len(top["system"]), 1024)))
	dst = append(dst, '[')
	if system := systemTokens(top["system"]); system != nil {
		dst = appendJoined(append(dst, `{"role":"system","content":`...), "", system, `\n`)
		dst = append(dst, '}')
	}
	var err error
	ok := eachItem(top["messages"], func(_ []byte, message obj) {
		if err == nil && message != nil {
			dst, err = appendChatMessage(dst, message.str("role"), message.get("content"), replay, thoughts, standIn)
		}
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("messages is not a list of messages")
	}
	return append(dst, ']'), nil
}

// appendChatMessage renders one Anthropic message: one `tool` message per
// tool result, then the message itself (text, images and files as parts,
// tool calls with the thought signatures carried for them, replayed
// reasoning). Images and files inside tool results, which a tool message
// cannot carry, open that message.
func appendChatMessage(dst []byte, role string, content []byte, replay, thoughts, standIn string) ([]byte, error) {
	// segments are the message's parts in order: a text token, or a rendered
	// image or file part. Texts are rendered as parts only when the message
	// carries media; otherwise they join into one string, copied once.
	type segment struct{ text, part []byte }
	var segments []segment
	var calls []byte
	var texts, reasoning [][]byte
	media := false
	var err error
	addText := func(token []byte) {
		texts = append(texts, token)
		segments = append(segments, segment{text: token})
	}
	addPart := func(part []byte) { media, segments = true, append(segments, segment{part: part}) }
	var signatures map[string]string // call id -> the thought signature carried for it
	if thoughts != "" && bytes.Contains(content, []byte(thoughts)) {
		signatures = map[string]string{}
		eachBlock(content, func(kind string, block obj) {
			if call, signature, ok := thoughtOf(block.str("data"), thoughts); kind == "redacted_thinking" && ok {
				signatures[call] = signature
			}
		})
	}
	eachBlock(content, func(kind string, block obj) {
		if err != nil {
			return
		}
		switch kind {
		case "text":
			text := block.get("text")
			if !isStr(text) {
				return
			}
			addText(text)
		case "image":
			part := []byte(`{"type":"image_url","image_url":{"url":`)
			if part, err = appendImageURL(part, block); err == nil {
				addPart(append(part, "}}"...))
			}
		case "document":
			var part []byte
			var text [][]byte
			if part, text, err = chatDocument(block); err == nil {
				if part != nil {
					addPart(part)
				}
				for _, token := range text {
					addText(token)
				}
			}
		case "tool_use":
			first := calls == nil
			calls = append(append(openElem(calls), `{"id":`...), tok(block.get("id"))...)
			calls = append(append(calls, `,"type":"function","function":{"name":`...), tok(block.get("name"))...)
			calls = append(calls, `,"arguments":`...)
			if input := block.get("input"); len(input) > 0 {
				calls = appendString(calls, input)
			} else {
				calls = append(calls, `"{}"`...)
			}
			calls = append(appendThought(append(calls, '}'), signatures[block.str("id")], standIn, first), '}')
		case "tool_result":
			var attached []byte
			dst, attached, err = appendToolMessage(dst, block)
			if attached != nil {
				addPart(attached)
			}
		case "thinking":
			// Provider-private reasoning: replayed only to the route and model
			// that produced it (its signature says which), never to another.
			if thinking := block.get("thinking"); replay != "" && role == "assistant" && block.str("signature") == replay && isStr(thinking) {
				reasoning = append(reasoning, thinking)
			}
		}
	})
	if err != nil {
		return dst, err
	}
	if segments == nil && calls == nil && reasoning == nil {
		return dst, nil
	}
	dst = appendComma(dst)
	dst = appendString(append(dst, `{"role":`...), role)
	switch {
	case media:
		dst = append(dst, `,"content":[`...)
		for at, segment := range segments {
			if at > 0 {
				dst = append(dst, ',')
			}
			if segment.part != nil {
				dst = append(dst, segment.part...)
			} else {
				dst = append(append(append(dst, `{"type":"text","text":`...), segment.text...), '}')
			}
		}
		dst = append(dst, ']')
	case len(texts) > 0 && !(len(texts) == 1 && len(texts[0]) == 2):
		dst = appendJoined(append(dst, `,"content":`...), "", texts, `\n`)
	}
	if calls != nil {
		dst = append(append(append(dst, `,"tool_calls":`...), calls...), ']')
	}
	if reasoning != nil {
		dst = appendJoined(append(dst, `,"reasoning_content":`...), "", reasoning, `\n`)
	}
	return append(dst, '}'), nil
}

// chatDocument is a document block as a chat file part, or the text of a
// text document; a document by URL or file id is refused.
func chatDocument(block obj) (part []byte, text [][]byte, err error) {
	source, err := sourceOf(block)
	if err != nil {
		return nil, nil, err
	}
	switch source.kind {
	case "base64":
		part = append([]byte(`{"type":"file","file":{"filename":`), source.filename()...)
		part = appendDataURI(append(part, `,"file_data":`...), source.media, source.data)
		return append(part, "}}"...), nil, nil
	case "text", "content":
		return nil, source.documentText(), nil
	}
	return nil, nil, errDocumentURL
}

// appendToolMessage renders a tool_result as a `tool` message; its images and
// documents come back as parts for the message that follows.
func appendToolMessage(dst []byte, block obj) ([]byte, []byte, error) {
	content := block.get("content")
	var texts [][]byte
	var attached []byte
	var err error
	if isStr(content) {
		texts = append(texts, content)
	} else {
		eachItem(content, func(_ []byte, part obj) {
			if err != nil || part == nil {
				return
			}
			switch part.str("type") {
			case "text":
				if text := part.get("text"); isStr(text) && len(text) > 2 {
					texts = append(texts, text)
				}
			case "image":
				image := []byte(`{"type":"image_url","image_url":{"url":`)
				if image, err = appendImageURL(image, part); err == nil {
					attached = append(appendComma(attached), append(image, "}}"...)...)
				}
			case "document":
				var file []byte
				var text [][]byte
				if file, text, err = chatDocument(part); err == nil {
					if file != nil {
						attached = append(appendComma(attached), file...)
					}
					texts = append(texts, text...)
				}
			}
		})
	}
	if err != nil {
		return dst, nil, err
	}
	prefix := ""
	if string(block.get("is_error")) == "true" {
		prefix = "Error: "
	}
	dst = appendComma(dst)
	dst = append(dst, `{"role":"tool","content":`...)
	switch {
	case len(texts) > 0:
		dst = appendJoined(dst, prefix, texts, `\n`)
	case attached != nil:
		dst = appendInner(dst, prefix, []byte("(attached below)"))
	default:
		dst = appendString(dst, prefix+string(bytes.TrimSpace(content)))
	}
	dst = append(append(dst, `,"tool_call_id":`...), tok(block.get("tool_use_id"))...)
	return append(dst, '}'), attached, nil
}

// --- Messages -> Responses ------------------------------------------------------

// messagesResponsesBody is a Messages body translated for a Responses host:
// stateless (store false, the whole conversation), always streamed.
func messagesResponsesBody(top map[string]json.RawMessage, opts Options) (map[string]json.RawMessage, error) {
	m := readMessagesTop(top)
	input, err := messagesToResponses(top["messages"], opts)
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{"model": mustJSON(opts.Model), "input": input, "store": json.RawMessage(`false`), "stream": json.RawMessage(`true`)}
	if system := systemTokens(top["system"]); system != nil {
		out["instructions"] = appendJoined(nil, "", system, `\n`)
	}
	if limit := top["max_tokens"]; !isNull(limit) && string(limit) != "0" {
		out["max_output_tokens"] = limit
	}
	// "none" only for a model the catalog lists with levels: one that does
	// not reason (gpt-4.1) refuses any reasoning field.
	_, reasons := catalog.EffortLevels("openai", opts.Model)
	switch effort := fitOpenAIEffort(m.effortFor(opts), openAIEfforts(opts.Model)); {
	case effort == "none":
		if reasons {
			out["reasoning"] = json.RawMessage(`{"effort":"none"}`)
		}
	case effort != "":
		out["reasoning"] = mustJSON(map[string]string{"effort": effort, "summary": "auto"})
		out["include"] = json.RawMessage(`["reasoning.encrypted_content"]`)
	}
	tools, err := responsesTools(top["tools"])
	if err != nil {
		return nil, err
	}
	if tools != nil {
		out["tools"] = tools
		if m.disableParallel {
			out["parallel_tool_calls"] = json.RawMessage(`false`)
		}
	}
	switch m.toolChoice.str("type") {
	case "auto", "none":
		out["tool_choice"] = mustJSON(m.toolChoice.str("type"))
	case "any":
		out["tool_choice"] = json.RawMessage(`"required"`)
	case "tool":
		out["tool_choice"] = append(append([]byte(`{"type":"function","name":`), tok(m.toolChoice.get("name"))...), '}')
	}
	if m.schema != nil {
		out["text"] = appendResponsesSchemaFormat(nil, []byte(`"output"`), m.schema, strictFor(m.schema), nil)
	}
	if opts.ChatGPTLogin {
		chatgptLoginBody(out)
	}
	dropParams(out, opts)
	return out, nil
}

// appendResponsesSchemaFormat is Responses' text field for a JSON schema.
func appendResponsesSchemaFormat(dst, name, schema, strict, description []byte) []byte {
	dst = append(dst, `{"format":{"type":"json_schema","name":`...)
	dst = append(dst, name...)
	if description != nil {
		dst = append(append(dst, `,"description":`...), description...)
	}
	dst = append(append(dst, `,"schema":`...), schema...)
	if strict != nil {
		dst = append(append(dst, `,"strict":`...), strict...)
	}
	return append(dst, "}}"...)
}

// fitOpenAIEffort is clampEffort for an OpenAI-shaped upstream, where no
// effort field means the model's default: "none" on a model without it is
// its lowest level, not "".
func fitOpenAIEffort(effort string, levels []string) string {
	if effort == "none" && len(levels) > 0 && !slicesContains(levels, "none") {
		lowest := ""
		for _, level := range levels {
			if at := effortRank(level); at > 0 && (lowest == "" || at < effortRank(lowest)) {
				lowest = level
			}
		}
		return lowest
	}
	return clampEffort(effort, levels)
}

// responsesTools renders Anthropic tools as Responses functions; server tools
// are refused.
func responsesTools(raw []byte) (json.RawMessage, error) {
	var out []byte
	var err error
	eachItem(raw, func(_ []byte, tool obj) {
		if err != nil {
			return
		}
		schema := tool.get("input_schema")
		if isNull(schema) {
			err = fmt.Errorf("tool %q is an Anthropic server tool (type %q) and is only supported on a native Anthropic model", tool.str("name"), tool.str("type"))
			return
		}
		out = append(append(openElem(out), `{"type":"function","name":`...), tok(tool.get("name"))...)
		if description := tool.get("description"); isStr(description) && len(description) > 2 {
			out = append(append(out, `,"description":`...), description...)
		}
		out = append(append(append(out, `,"parameters":`...), schema...), `,"strict":false}`...)
	})
	if err != nil || out == nil {
		return nil, err
	}
	return append(out, ']'), nil
}

// messagesToResponses renders the messages as Responses input items: tool
// results become function_call_output items (first, as Anthropic orders
// them), tool_use blocks function_call items, the route's own thinking a
// reasoning item, and text, images and documents one message.
func messagesToResponses(messages []byte, opts Options) (json.RawMessage, error) {
	dst := make([]byte, 0, capHint(len(messages), 1024))
	dst = append(dst, '[')
	own := opts.responsesSignature("")
	var err error
	ok := eachItem(messages, func(_ []byte, message obj) {
		if err != nil || message == nil {
			return
		}
		role := message.str("role")
		assistant := role == "assistant"
		var parts []byte
		flush := func() {
			if parts != nil {
				dst = appendString(append(appendComma(dst), `{"type":"message","role":`...), role)
				dst = append(append(append(dst, `,"content":`...), parts...), "]}"...)
				parts = nil
			}
		}
		eachBlock(message.get("content"), func(kind string, block obj) {
			if err != nil {
				return
			}
			switch kind {
			case "text":
				if text := block.get("text"); isStr(text) {
					kind := `{"type":"input_text","text":`
					if assistant {
						kind = `{"type":"output_text","text":`
					}
					parts = append(append(append(openElem(parts), kind...), text...), '}')
				}
			case "image":
				part := []byte(`{"type":"input_image","image_url":`)
				if part, err = appendImageURL(part, block); err == nil {
					parts = append(append(openElem(parts), part...), '}')
				}
			case "document":
				var part []byte
				var texts [][]byte
				if part, texts, err = responsesDocument(block); err == nil {
					if part != nil {
						parts = append(openElem(parts), part...)
					}
					for _, text := range texts {
						parts = append(append(append(openElem(parts), `{"type":"input_text","text":`...), text...), '}')
					}
				}
			case "tool_use":
				flush()
				dst = append(appendComma(dst), `{"type":"function_call","call_id":`...)
				dst = appendString(dst, safeCallID(block.str("id")))
				dst = append(append(dst, `,"name":`...), tok(block.get("name"))...)
				dst = append(dst, `,"arguments":`...)
				if input := block.get("input"); len(input) > 0 {
					dst = appendString(dst, input)
				} else {
					dst = append(dst, `"{}"`...)
				}
				dst = append(dst, '}')
			case "tool_result":
				flush()
				dst = append(appendComma(dst), `{"type":"function_call_output","call_id":`...)
				dst = appendString(dst, safeCallID(block.str("tool_use_id")))
				dst = append(dst, `,"output":`...)
				dst, err = appendResponsesToolOutput(dst, block)
				dst = append(dst, '}')
			case "thinking":
				// The route's own reasoning goes back to it; nothing else does.
				signature := block.get("signature")
				if encrypted, found := bytes.CutPrefix(inner(signature), []byte(own)); found && assistant && len(encrypted) > 0 {
					flush()
					dst = append(appendComma(dst), `{"type":"reasoning","summary":[`...)
					if thinking := block.get("thinking"); isStr(thinking) && len(thinking) > 2 {
						dst = append(append(append(dst, `{"type":"summary_text","text":`...), thinking...), '}')
					}
					dst = appendInner(append(dst, `],"encrypted_content":`...), "", encrypted)
					dst = append(dst, '}')
				}
			}
		})
		flush()
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("messages is not a list of messages")
	}
	return append(dst, ']'), nil
}

// responsesDocument is a document block as an input_file part (base64 or by
// URL), or the text of a text document; a Files API document is refused.
func responsesDocument(block obj) (part []byte, text [][]byte, err error) {
	source, err := sourceOf(block)
	if err != nil {
		return nil, nil, err
	}
	switch source.kind {
	case "base64":
		part = append([]byte(`{"type":"input_file","filename":`), source.filename()...)
		part = appendDataURI(append(part, `,"file_data":`...), source.media, source.data)
		return append(part, '}'), nil, nil
	case "url":
		return append(appendInner([]byte(`{"type":"input_file","file_url":`), "", source.url), '}'), nil, nil
	case "text", "content":
		return nil, source.documentText(), nil
	}
	return nil, nil, errDocumentURL
}

// appendResponsesToolOutput is a tool_result's output: its text, or parts
// when it carries images or documents.
func appendResponsesToolOutput(dst []byte, block obj) ([]byte, error) {
	content := block.get("content")
	prefix := ""
	if string(block.get("is_error")) == "true" {
		prefix = "Error: "
	}
	if isStr(content) {
		return appendJoined(dst, prefix, [][]byte{content}, ""), nil
	}
	var texts [][]byte
	var parts []byte
	media := false
	var err error
	eachItem(content, func(_ []byte, part obj) {
		if err != nil || part == nil {
			return
		}
		switch part.str("type") {
		case "text":
			if text := part.get("text"); isStr(text) {
				texts = append(texts, text)
				parts = append(append(append(openElem(parts), `{"type":"input_text","text":`...), text...), '}')
			}
		case "image":
			image := []byte(`{"type":"input_image","image_url":`)
			if image, err = appendImageURL(image, part); err == nil {
				media = true
				parts = append(append(openElem(parts), image...), '}')
			}
		case "document":
			var file []byte
			var text [][]byte
			if file, text, err = responsesDocument(part); err == nil {
				if file != nil {
					media = true
					parts = append(openElem(parts), file...)
				}
				for _, token := range text {
					texts = append(texts, token)
					parts = append(append(append(openElem(parts), `{"type":"input_text","text":`...), token...), '}')
				}
			}
		}
	})
	switch {
	case err != nil:
		return dst, err
	case media:
		if prefix != "" {
			dst = append(append(dst, `[{"type":"input_text","text":"Error:"},`...), parts[1:]...)
			return append(dst, ']'), nil
		}
		return append(append(dst, parts...), ']'), nil
	case len(texts) > 0:
		return appendJoined(dst, prefix, texts, `\n`), nil
	}
	return appendString(dst, prefix+string(bytes.TrimSpace(content))), nil
}
