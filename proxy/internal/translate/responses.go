package translate

// OpenAI Responses -> Anthropic Messages / OpenAI chat-completions. It exists
// so Codex, whose only wire protocol is Responses (codex-rs
// model-provider-info/src/lib.rs WireApi), can run a model OpenAI never served.
//
// Codex sends the whole conversation every turn (store:false, no
// previous_response_id on HTTP), so every translation is stateless: the tool
// name map and the reasoning carried in encrypted_content are rebuilt from the
// request itself.
//
// Ported from LiteLLM (litellm/responses/litellm_completion_transformation/):
//   - custom (freeform) tools become a function with one string argument and
//     come back as custom_tool_call (custom_tools.py);
//   - namespace tools are flattened to one function each and the namespace is
//     restored on the way back (transformation.py _namespace_chat_tools);
//   - Claude's signed thinking travels inside reasoning.encrypted_content and
//     is decoded next turn (transformation.py _encode_thinking_blocks,
//     _decode_thinking_blocks_from_input_item);
//   - names over OpenAI's 64-character limit are hashed, with a reverse map
//     (litellm/llms/anthropic/pass_through/adapters/transformation.py
//     truncate_tool_name).
//
// And three LiteLLM bugs deliberately not ported: arguments that arrive whole
// in the first chunk are kept (#27144), parallel_tool_calls is forwarded
// (#38612), and thinking is never turned into text (#26916).

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

type responsesRequest struct {
	Model             string            `json:"model"`
	Instructions      json.RawMessage   `json:"instructions"`
	Input             json.RawMessage   `json:"input"`
	Tools             []json.RawMessage `json:"tools"`
	ToolChoice        json.RawMessage   `json:"tool_choice"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls"`
	Reasoning         *struct {
		Effort string `json:"effort"`
	} `json:"reasoning"`
	Stream          bool     `json:"stream"`
	MaxOutputTokens int      `json:"max_output_tokens"`
	Temperature     *float64 `json:"temperature"`
	TopP            *float64 `json:"top_p"`
	PromptCacheKey  string   `json:"prompt_cache_key"`
	Text            *struct {
		Format json.RawMessage `json:"format"`
	} `json:"text"`
}

func (r responsesRequest) effort() string {
	if r.Reasoning == nil {
		return ""
	}
	return strings.TrimSpace(r.Reasoning.Effort)
}

// responsesItem is the union of every input item shape read here. Unknown
// types (web_search_call, local_shell_call, tool_search_*, compaction, ...)
// are tolerated and dropped on a translated path.
type responsesItem struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content"`
	CallID           string          `json:"call_id"`
	Name             string          `json:"name"`
	Namespace        string          `json:"namespace"`
	Arguments        string          `json:"arguments"`
	Input            string          `json:"input"`
	Output           json.RawMessage `json:"output"`
	EncryptedContent *string         `json:"encrypted_content"`
}

// responsesPart is one message content part (input_text, output_text,
// input_image) or one structured tool output item.
type responsesPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL string `json:"image_url"`
}

// responsesItems reads `input`: a bare string is one user message.
func responsesItems(raw json.RawMessage) ([]responsesItem, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []responsesItem{{Type: "message", Role: "user", Content: mustJSON(text)}}, nil
	}
	var items []responsesItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, errors.New("input is neither a string nor a list of items")
	}
	for index := range items {
		switch {
		case items[index].Type == "" && items[index].Role != "":
			items[index].Type = "message" // EasyInputMessage carries no type
		case items[index].Type == "agent_message":
			// MultiAgentV2 hands a child its task (and later messages) this way
			// (codex-rs core/src/agent/control/delivery.rs): the text is already
			// attributed, and encrypted parts carry no text to translate.
			items[index].Type, items[index].Role = "message", "user"
		}
	}
	return orderToolOutputs(items), nil
}

// orderToolOutputs moves a user or developer message that sits between tool
// calls and their outputs to after the outputs; a call that never gets an
// output holds nothing past the next assistant message. Anthropic wants tool_result
// first in the next user turn and chat wants `tool` right after the call;
// either would answer 400 on the order as sent.
func orderToolOutputs(items []responsesItem) []responsesItem {
	pending := map[string]bool{}
	out, held := make([]responsesItem, 0, len(items)), []responsesItem{}
	for _, item := range items {
		switch {
		case item.Type == "function_call" || item.Type == "custom_tool_call":
			pending[item.CallID] = true
		case item.Type == "function_call_output" || item.Type == "custom_tool_call_output":
			delete(pending, item.CallID)
		case item.Type == "message" && item.Role != "assistant" && len(pending) > 0:
			held = append(held, item)
			continue
		case item.Type == "message" && len(held) > 0:
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

// responsesParts reads a string-or-parts content (or tool output) value.
func responsesParts(raw json.RawMessage) []responsesPart {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []responsesPart{{Type: "input_text", Text: text}}
	}
	var parts []responsesPart
	_ = json.Unmarshal(raw, &parts)
	return parts
}

func partsText(parts []responsesPart) string {
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Type != "input_image" && part.Text != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func textOfInstructions(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	return partsText(responsesParts(raw))
}

// --- tools ----------------------------------------------------------------

const (
	toolNameMaxLength  = 64 // OpenAI's limit; Anthropic's is the same pattern
	toolNameHashLength = 8
)

var toolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// wireToolName is the name an upstream sees for a Responses tool: the
// namespace and name joined, and, when that is not a valid function name, a
// 55-character prefix plus an 8-hex hash of the whole (LiteLLM
// truncate_tool_name). Deterministic, so history and tools always agree.
func wireToolName(namespace, name string) string {
	full := joinedToolName(namespace, name)
	if toolNamePattern.MatchString(full) {
		return full
	}
	return hashedToolName(full)
}

func joinedToolName(namespace, name string) string {
	if namespace == "" {
		return name
	}
	separator := "__"
	if strings.HasSuffix(namespace, "_") {
		separator = ""
	}
	return namespace + separator + name
}

// hashedToolName is always the hashed form, also used for the second of two
// tools whose joined names collide (a function mcp__srv__x and namespace
// mcp__srv__ + x). The hash covers the namespace separately, so the two differ.
func hashedToolName(full string) string {
	sum := sha256.Sum256([]byte(full))
	clean := strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, full)
	prefix := toolNameMaxLength - toolNameHashLength - 1
	if len(clean) > prefix {
		clean = clean[:prefix]
	}
	return clean + "_" + hex.EncodeToString(sum[:])[:toolNameHashLength]
}

type toolOrigin struct {
	Name      string
	Namespace string
	Custom    bool
}

// toolBridge maps the names an upstream answers with back to the Responses
// tool they stand for.
type toolBridge map[string]toolOrigin

func (b toolBridge) origin(wire string) toolOrigin {
	if origin, ok := b[wire]; ok {
		return origin
	}
	return toolOrigin{Name: wire}
}

// wire is the name a declared tool was given (history calls must use the same
// one, collision-renamed or not); an undeclared tool gets wireToolName.
func (b toolBridge) wire(namespace, name string) string {
	for wire, origin := range b {
		if origin.Name == name && origin.Namespace == namespace {
			return wire
		}
	}
	return wireToolName(namespace, name)
}

// bridgedTool is one Responses tool as a plain function.
type bridgedTool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Format      *struct {
		Syntax     string `json:"syntax"`
		Definition string `json:"definition"`
	} `json:"format"`
	Tools []responsesTool `json:"tools"`
}

// customToolParameters is the schema a freeform tool becomes: one string, the
// raw text the grammar describes (LiteLLM custom_tools.py uses `content`; the
// field is named after Codex's own custom_tool_call.input).
var customToolParameters = json.RawMessage(`{"type":"object","properties":{"input":{"type":"string","description":"The raw tool input, exactly in the format described above."}},"required":["input"],"additionalProperties":false}`)

// bridgeTools flattens the request's tools into plain functions. Tools only an
// OpenAI server runs (web_search, tool_search, image_generation, ...) have
// nothing to forward to and are dropped.
func bridgeTools(raw []json.RawMessage) ([]bridgedTool, toolBridge) {
	out, bridge := []bridgedTool{}, toolBridge{}
	var add func(tool responsesTool, namespace, namespaceDescription string)
	add = func(tool responsesTool, namespace, namespaceDescription string) {
		description := tool.Description
		if namespaceDescription != "" {
			description = strings.TrimSpace(namespaceDescription + "\n\n" + description)
		}
		if tool.Type != "function" && tool.Type != "custom" {
			return
		}
		wire := wireToolName(namespace, tool.Name)
		if _, taken := bridge[wire]; taken {
			wire = hashedToolName(namespace + "\x00" + tool.Name)
		}
		switch tool.Type {
		case "function":
			parameters := tool.Parameters
			if len(parameters) == 0 || string(parameters) == "null" {
				parameters = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			out = append(out, bridgedTool{Name: wire, Description: description, Parameters: parameters})
			bridge[wire] = toolOrigin{Name: tool.Name, Namespace: namespace}
		case "custom":
			// The grammar goes into the description so the model can follow
			// it (LiteLLM custom_tool_grammar_suffix).
			if tool.Format != nil && tool.Format.Definition != "" {
				description += "\n\nFormat:\n```" + tool.Format.Syntax + "\n" + tool.Format.Definition + "\n```"
			}
			out = append(out, bridgedTool{Name: wire, Description: description, Parameters: customToolParameters})
			bridge[wire] = toolOrigin{Name: tool.Name, Namespace: namespace, Custom: true}
		}
	}
	for _, entry := range raw {
		var tool responsesTool
		if json.Unmarshal(entry, &tool) != nil || tool.Name == "" && tool.Type != "namespace" {
			continue
		}
		if tool.Type == "namespace" {
			for _, child := range tool.Tools {
				add(child, tool.Name, tool.Description)
			}
			continue
		}
		add(tool, "", "")
	}
	return out, bridge
}

// customInput unwraps a bridged custom tool's arguments back to the raw text;
// arguments that are not the wrapper are returned as they came.
func customInput(arguments string) string {
	var wrapped map[string]json.RawMessage
	if json.Unmarshal([]byte(arguments), &wrapped) == nil {
		var input string
		if json.Unmarshal(wrapped["input"], &input) == nil {
			return input
		}
	}
	return arguments
}

// callIDPattern is what every upstream accepts as a tool call id: Anthropic
// requires ^[a-zA-Z0-9_-]+$, OpenAI chat caps ids at 40 characters.
var callIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,40}$`)

// safeCallID rewrites a call id another provider minted into one this
// upstream accepts. Deterministic, so a call and its output keep matching.
func safeCallID(id string) string {
	if callIDPattern.MatchString(id) {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return "call_" + hex.EncodeToString(sum[:12])
}

var (
	// anthropicCallID is the tool id pattern Anthropic enforces.
	anthropicCallID = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	// toolIDField finds every id-like value in a Messages body (a cheap scan
	// before the full parse).
	toolIDField = regexp.MustCompile(`"(?:id|tool_use_id)"\s*:\s*"((?:[^"\\]|\\.)*)"`)
)

// wireCallID is safeCallID for a tool_use id handed to an Anthropic-shaped
// caller (Claude Code): another provider's id (e.g. OpenRouter's
// "functions.Bash:0") would make the next Anthropic-bound request fail. A
// call that came without an id gets one minted from its message id and
// index (deterministic; parallel calls stay distinct).
func wireCallID(id, messageID string, index int) string {
	if id == "" {
		return safeCallID(fmt.Sprintf("%s#%d", messageID, index)) // never matches the pattern: always hashed
	}
	return safeCallID(id)
}

// --- reasoning ------------------------------------------------------------

// reasoningEnvelope is what the runtime writes into a reasoning item's
// encrypted_content when the reasoning came from Anthropic: the signed
// thinking blocks, byte for byte, under a marker. Only an envelope carrying
// the marker is ever decoded, so OpenAI's own opaque blobs are never mistaken
// for it, and only signed blocks are ever replayed to Anthropic.
type reasoningEnvelope struct {
	Caveman string            `json:"caveman"`
	Blocks  []json.RawMessage `json:"blocks"`
	// Route and Blob carry another Responses host's own encrypted_content
	// (the ChatGPT plan, OpenCode Go): restored only for that route.
	Route string `json:"route,omitempty"`
	Blob  string `json:"blob,omitempty"`
}

// encryptedContentRE finds one encrypted_content value (base64, no escapes).
var encryptedContentRE = regexp.MustCompile(`("encrypted_content"\s*:\s*)"([A-Za-z0-9+/=_-]+)"`)

// tagReasoning wraps the encrypted_content a Responses host other than
// OpenAI's own API wrote into an envelope naming that host, so its reasoning
// goes back to it and to nothing else (OpenAI's API included). A no-op for
// every other answer.
func (r *Reply) tagReasoning(data []byte) []byte {
	route := r.opts.Route
	if r.from != Responses || r.to != Responses || route == "" || route == "openai" || !bytes.Contains(data, []byte("encrypted_content")) {
		return data
	}
	return encryptedContentRE.ReplaceAllFunc(data, func(match []byte) []byte {
		parts := encryptedContentRE.FindSubmatch(match)
		if bytes.HasPrefix(parts[2], envelopeMarker) {
			return match
		}
		wrapped := base64.StdEncoding.EncodeToString(mustJSON(reasoningEnvelope{Caveman: reasoningEnvelopeVersion, Blocks: []json.RawMessage{}, Route: route, Blob: string(parts[2])}))
		return append(append([]byte{}, parts[1]...), mustJSON(wrapped)...)
	})
}

// restoreReasoning puts back the encrypted_content a route's own envelopes
// carry, for a request to that route.
func restoreReasoning(body map[string]json.RawMessage, route string) {
	var items []map[string]json.RawMessage
	if json.Unmarshal(body["input"], &items) != nil {
		return
	}
	changed := false
	for _, item := range items {
		var kind, encrypted string
		_ = json.Unmarshal(item["type"], &kind)
		_ = json.Unmarshal(item["encrypted_content"], &encrypted)
		if kind != "reasoning" || !strings.HasPrefix(encrypted, string(envelopeMarker)) {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(encrypted)
		var envelope reasoningEnvelope
		if err == nil && json.Unmarshal(raw, &envelope) == nil && envelope.Route == route && envelope.Blob != "" {
			item["encrypted_content"], changed = mustJSON(envelope.Blob), true
		}
	}
	if changed {
		body["input"] = mustJSON(items)
	}
}

const reasoningEnvelopeVersion = "v1"

// envelopeMarker is the base64 every envelope starts with: the encoding of its
// fixed `{"caveman":"v1",` prefix, cut at the last whole 3-byte group, so a
// body is checked for envelopes without decoding a thing.
var envelopeMarker = []byte(base64.StdEncoding.EncodeToString([]byte(`{"caveman":"v1",`))[:20])

func encodeThinking(blocks []json.RawMessage) *string {
	if len(blocks) == 0 {
		return nil
	}
	encoded := base64.StdEncoding.EncodeToString(mustJSON(reasoningEnvelope{Caveman: reasoningEnvelopeVersion, Blocks: blocks}))
	return &encoded
}

// decodeThinking returns the blocks an envelope carries that the Messages
// host `route` may see, or false for anything the runtime did not write:
// Anthropic-signed blocks, plus that host's own namespaced ones
// ("caveman:<route>:…"), which stripForeignThinking restores.
func decodeThinking(encrypted *string, route string) ([]json.RawMessage, bool) {
	if encrypted == nil || *encrypted == "" {
		return nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(*encrypted)
	if err != nil {
		return nil, false
	}
	var envelope reasoningEnvelope
	if json.Unmarshal(raw, &envelope) != nil || envelope.Caveman != reasoningEnvelopeVersion {
		return nil, false
	}
	signed := make([]json.RawMessage, 0, len(envelope.Blocks))
	for _, block := range envelope.Blocks {
		var fields struct {
			Type      string `json:"type"`
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
			Data      string `json:"data"`
		}
		if json.Unmarshal(block, &fields) != nil {
			continue
		}
		// Rebuilt from the known fields only: nothing else in the envelope
		// reaches Anthropic.
		switch {
		case fields.Type == "thinking" && (fields.Signature != "" && !strings.HasPrefix(fields.Signature, signaturePrefix) || route != anthropicRoute && strings.HasPrefix(fields.Signature, signaturePrefix+route+":")):
			signed = append(signed, mustJSON(map[string]string{"type": "thinking", "thinking": fields.Thinking, "signature": fields.Signature}))
		case fields.Type == "redacted_thinking" && fields.Data != "":
			signed = append(signed, mustJSON(map[string]string{"type": "redacted_thinking", "data": fields.Data}))
		}
	}
	return signed, true
}

// replayedReasoning is the text of the thinking an envelope carries signed
// `replay` (a chat route's own reasoning, streamChatToResponses), "" for
// anything else.
func replayedReasoning(encrypted *string, replay string) string {
	if replay == "" || encrypted == nil {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(*encrypted)
	var envelope reasoningEnvelope
	if err != nil || json.Unmarshal(raw, &envelope) != nil || envelope.Caveman != reasoningEnvelopeVersion {
		return ""
	}
	var parts []string
	for _, block := range envelope.Blocks {
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

// effortOrder ranks the effort names Codex and the runtime use.
var effortOrder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// clampEffort maps a requested effort onto the levels a model accepts: the
// level itself, else the nearest one below, else the lowest above. "none" on
// a model without it is "" (reasoning off). Unknown levels pass through.
func clampEffort(effort string, levels []string) string {
	switch effort {
	case "":
		return ""
	case "ultra", "persistent":
		effort = "max"
	}
	if len(levels) == 0 || slices.Contains(levels, effort) {
		return effort
	}
	if effort == "none" {
		return ""
	}
	rank := slices.Index(effortOrder, effort)
	if rank < 0 {
		return effort
	}
	below, above := "", ""
	for _, level := range levels {
		switch at := slices.Index(effortOrder, level); {
		case at < 1: // unknown, or "none": never chosen for a reasoning ask
		case at <= rank && (below == "" || at > slices.Index(effortOrder, below)):
			below = level
		case at > rank && (above == "" || at < slices.Index(effortOrder, above)):
			above = level
		}
	}
	if below != "" {
		return below
	}
	return above
}

// --- developer messages -----------------------------------------------------

// splitSystem returns the instructions plus every developer/system message
// that comes before the conversation starts, as the system prompt; developer
// messages later in the conversation stay where they are, as user text.
func splitSystem(request responsesRequest, items []responsesItem) (string, []responsesItem) {
	system := []string{}
	if text := textOfInstructions(request.Instructions); text != "" {
		system = append(system, text)
	}
	start := 0
	for ; start < len(items); start++ {
		item := items[start]
		if item.Type != "message" || (item.Role != "developer" && item.Role != "system") {
			break
		}
		if text := partsText(responsesParts(item.Content)); text != "" {
			system = append(system, text)
		}
	}
	return strings.Join(system, "\n\n"), items[start:]
}

// --- Responses -> Anthropic Messages ---------------------------------------

// anthropicDefaultMaxTokens is max_tokens when neither the request nor
// Options.MaxOutputTokens names one; every current Claude model accepts it.
const anthropicDefaultMaxTokens = 32000

// responsesToAnthropic renders a Responses request as a streaming Messages
// body for `model` on the Messages host `route` (a runtime provider id;
// "anthropic" for Anthropic itself): the thinking it may see follows
// stripForeignThinking. thinking is the already-fitted `thinking` value, or
// nil for reasoning off.
func responsesToAnthropic(request responsesRequest, model string, maxTokens int, thinking json.RawMessage, route string) (map[string]json.RawMessage, toolBridge, error) {
	items, err := responsesItems(request.Input)
	if err != nil {
		return nil, nil, err
	}
	tools, bridge := bridgeTools(request.Tools)
	system, items := splitSystem(request, items)

	type message struct {
		Role    string           `json:"role"`
		Content []map[string]any `json:"content"`
	}
	messages := []message{}
	push := func(role string, block map[string]any) {
		if count := len(messages); count > 0 && messages[count-1].Role == role {
			messages[count-1].Content = append(messages[count-1].Content, block)
			return
		}
		messages = append(messages, message{Role: role, Content: []map[string]any{block}})
	}
	calls := map[string]bool{}
	for _, item := range items {
		switch item.Type {
		case "message":
			role := item.Role
			if role != "assistant" {
				role = "user"
			}
			for _, part := range responsesParts(item.Content) {
				if block, ok := anthropicPart(part); ok {
					push(role, block)
				}
			}
		case "reasoning":
			// Only the runtime's envelope, and only its signed blocks: foreign
			// or unsigned reasoning never reaches Anthropic.
			if blocks, ok := decodeThinking(item.EncryptedContent, route); ok {
				for _, block := range blocks {
					var decoded map[string]any
					if json.Unmarshal(block, &decoded) == nil {
						push("assistant", decoded)
					}
				}
			}
		case "function_call":
			calls[item.CallID] = true
			push("assistant", map[string]any{
				"type": "tool_use", "id": safeCallID(item.CallID), "name": bridge.wire(item.Namespace, item.Name),
				"input": toolInput(item.Arguments),
			})
		case "custom_tool_call":
			calls[item.CallID] = true
			push("assistant", map[string]any{
				"type": "tool_use", "id": safeCallID(item.CallID), "name": bridge.wire(item.Namespace, item.Name),
				"input": map[string]any{"input": item.Input},
			})
		case "function_call_output", "custom_tool_call_output":
			parts := responsesParts(item.Output)
			if !calls[item.CallID] {
				// A call this translation dropped (local_shell_call, ...):
				// its output still informs the model, as plain text.
				push("user", map[string]any{"type": "text", "text": "Tool output (" + item.CallID + "):\n" + partsText(parts)})
				continue
			}
			content := []map[string]any{}
			for _, part := range parts {
				if block, ok := anthropicPart(part); ok {
					content = append(content, block)
				}
			}
			result := map[string]any{"type": "tool_result", "tool_use_id": safeCallID(item.CallID), "content": content}
			if len(content) == 0 {
				result["content"] = ""
			}
			push("user", result)
		}
	}
	if len(messages) == 0 {
		return nil, nil, errors.New("input has no messages")
	}
	// The whole conversation is resent every turn, so without breakpoints
	// Anthropic re-reads it all at full price. Four breakpoints (Anthropic's
	// cap): the last tool, the system prompt, the end of the previous request
	// (the last message before the newest assistant turn: that request wrote
	// its cache there, so this one reads it exactly however many blocks the new
	// turn adds; Anthropic's own lookback stops at 20 blocks), and the end of
	// this request, which the next one reads.
	last := len(messages) - 1
	for index := last; index >= 0; index-- {
		if messages[index].Role != "assistant" || index == last {
			continue
		}
		if index > 0 {
			markBreakpoint(messages[index-1].Content)
		}
		break
	}
	markBreakpoint(messages[last].Content)

	body := map[string]json.RawMessage{
		"model": mustJSON(model), "max_tokens": mustJSON(maxTokens), "stream": mustJSON(true),
		"messages": mustJSON(messages),
	}
	if system != "" {
		body["system"] = mustJSON([]map[string]any{{"type": "text", "text": system, "cache_control": map[string]string{"type": "ephemeral"}}})
	}
	if len(tools) > 0 {
		// Byte-stable across turns whatever order the caller lists its tools
		// in or serialises their schemas with: sorted by name, keys sorted.
		slices.SortStableFunc(tools, func(a, b bridgedTool) int { return strings.Compare(a.Name, b.Name) })
		out := make([]map[string]any, 0, len(tools))
		for _, tool := range tools {
			out = append(out, map[string]any{"name": tool.Name, "description": tool.Description, "input_schema": canonicalJSON(tool.Parameters)})
		}
		out[len(out)-1]["cache_control"] = map[string]string{"type": "ephemeral"}
		body["tools"] = mustJSON(out)
		if choice := anthropicChoiceFromResponses(request.ToolChoice, request.ParallelToolCalls, bridge); choice != nil {
			body["tool_choice"] = mustJSON(choice)
		}
	}
	if thinking != nil {
		body["thinking"] = thinking
	} else {
		// Anthropic rejects sampling knobs while thinking is on.
		if request.Temperature != nil {
			body["temperature"] = mustJSON(*request.Temperature)
		}
		if request.TopP != nil {
			body["top_p"] = mustJSON(*request.TopP)
		}
	}
	// A last assistant tool turn with no signed thinking left (it came from
	// another model) cannot be sent with thinking on: the same rule the
	// Messages path applies.
	stripForeignThinking(body, route)
	return body, bridge, nil
}

// markBreakpoint puts a cache breakpoint on a message's last block that can
// carry one (thinking blocks cannot).
func markBreakpoint(content []map[string]any) {
	for at := len(content) - 1; at >= 0; at-- {
		if kind := content[at]["type"]; kind != "thinking" && kind != "redacted_thinking" {
			content[at]["cache_control"] = map[string]string{"type": "ephemeral"}
			return
		}
	}
}

// canonicalJSON is raw with every object's keys sorted and numbers kept as
// written; raw itself when it does not parse.
func canonicalJSON(raw json.RawMessage) json.RawMessage {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return raw
	}
	return mustJSON(value)
}

func anthropicPart(part responsesPart) (map[string]any, bool) {
	switch part.Type {
	case "input_text", "output_text", "text":
		if part.Text == "" {
			return nil, false
		}
		return map[string]any{"type": "text", "text": part.Text}, true
	case "input_image":
		if media, data, ok := strings.Cut(strings.TrimPrefix(part.ImageURL, "data:"), ";base64,"); ok && strings.HasPrefix(part.ImageURL, "data:") {
			return map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": media, "data": data}}, true
		}
		if part.ImageURL != "" {
			return map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": part.ImageURL}}, true
		}
	}
	return nil, false
}

// anthropicChoiceFromResponses maps tool_choice and parallel_tool_calls.
func anthropicChoiceFromResponses(raw json.RawMessage, parallel *bool, bridge toolBridge) map[string]any {
	choice := map[string]any{"type": "auto"}
	var named struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	var mode string
	switch {
	case json.Unmarshal(raw, &mode) == nil:
		switch mode {
		case "required":
			choice["type"] = "any"
		case "none":
			return map[string]any{"type": "none"}
		}
	case json.Unmarshal(raw, &named) == nil && named.Type == "function" && named.Name != "":
		choice = map[string]any{"type": "tool", "name": bridge.wire("", named.Name)}
	}
	if parallel != nil && !*parallel {
		choice["disable_parallel_tool_use"] = true
	}
	return choice
}

// --- Responses -> OpenAI chat ---------------------------------------------

// responsesToChat renders a Responses request as a streaming chat body for
// `model`. Reasoning items are dropped (chat has no field that carries another
// model's reasoning back, and replaying it as text would be wrong), except
// the reasoning the runtime signed `replay` (the same chat route and model's
// own, carried in encrypted_content): that goes back as reasoning_content on
// the assistant message it led ("" sends none).
func responsesToChat(request responsesRequest, model, replay string) (map[string]any, toolBridge, error) {
	items, err := responsesItems(request.Input)
	if err != nil {
		return nil, nil, err
	}
	tools, bridge := bridgeTools(request.Tools)
	system, items := splitSystem(request, items)
	messages := []openAIMessage{}
	if system != "" {
		messages = append(messages, openAIMessage{Role: "system", Content: system})
	}
	// assistant is the open assistant message tool calls attach to; pending
	// is replayed reasoning waiting for the assistant message it led.
	assistant, pending := -1, ""
	openAssistant := func(content any) {
		messages = append(messages, openAIMessage{Role: "assistant", Content: content, ReasoningContent: pending})
		assistant, pending = len(messages)-1, ""
	}
	calls := map[string]bool{}
	addCall := func(id, name, arguments string) {
		if assistant < 0 {
			openAssistant(nil)
		}
		call := openAIToolCall{ID: safeCallID(id), Type: "function"}
		call.Function.Name, call.Function.Arguments = name, arguments
		messages[assistant].ToolCalls = append(messages[assistant].ToolCalls, call)
		calls[id] = true
	}
	for _, item := range items {
		switch item.Type {
		case "message":
			parts := responsesParts(item.Content)
			if item.Role == "assistant" {
				// Text after a call joins the same assistant message: its tool
				// results must follow the message that holds the calls.
				if assistant >= 0 {
					text, _ := messages[assistant].Content.(string)
					messages[assistant].Content = strings.TrimPrefix(text+"\n"+partsText(parts), "\n")
					continue
				}
				openAssistant(partsText(parts))
				continue
			}
			assistant, pending = -1, ""
			messages = append(messages, openAIMessage{Role: "user", Content: chatUserContent(parts)})
		case "reasoning":
			if text := replayedReasoning(item.EncryptedContent, replay); text != "" && assistant >= 0 {
				messages[assistant].ReasoningContent = strings.TrimPrefix(messages[assistant].ReasoningContent+"\n"+text, "\n")
			} else if text != "" {
				pending = strings.TrimPrefix(pending+"\n"+text, "\n")
			}
		case "function_call":
			arguments := item.Arguments
			if arguments == "" {
				arguments = "{}"
			}
			addCall(item.CallID, bridge.wire(item.Namespace, item.Name), arguments)
		case "custom_tool_call":
			addCall(item.CallID, bridge.wire(item.Namespace, item.Name), string(mustJSON(map[string]string{"input": item.Input})))
		case "function_call_output", "custom_tool_call_output":
			assistant, pending = -1, ""
			text := partsText(responsesParts(item.Output))
			if !calls[item.CallID] {
				messages = append(messages, openAIMessage{Role: "user", Content: "Tool output (" + item.CallID + "):\n" + text})
				continue
			}
			messages = append(messages, openAIMessage{Role: "tool", ToolCallID: safeCallID(item.CallID), Content: text})
		}
	}
	out := map[string]any{"model": model, "messages": messages, "stream": true, "stream_options": map[string]any{"include_usage": true}}
	if len(tools) > 0 {
		functions := make([]map[string]any, 0, len(tools))
		for _, tool := range tools {
			functions = append(functions, map[string]any{"type": "function", "function": map[string]any{
				"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters,
			}})
		}
		out["tools"] = functions
		if choice := chatChoiceFromResponses(request.ToolChoice, bridge); choice != nil {
			out["tool_choice"] = choice
		}
		if request.ParallelToolCalls != nil {
			out["parallel_tool_calls"] = *request.ParallelToolCalls
		}
	}
	if request.MaxOutputTokens > 0 {
		out["max_tokens"] = request.MaxOutputTokens
	}
	if request.Temperature != nil {
		out["temperature"] = *request.Temperature
	}
	if request.TopP != nil {
		out["top_p"] = *request.TopP
	}
	if format := chatResponseFormat(request); format != nil {
		out["response_format"] = format
	}
	return out, bridge, nil
}

func chatUserContent(parts []responsesPart) any {
	images := false
	out := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "input_image":
			if part.ImageURL != "" {
				images = true
				out = append(out, map[string]any{"type": "image_url", "image_url": map[string]any{"url": part.ImageURL}})
			}
		default:
			if part.Text != "" {
				out = append(out, map[string]any{"type": "text", "text": part.Text})
			}
		}
	}
	if !images {
		return partsText(parts)
	}
	return out
}

func chatChoiceFromResponses(raw json.RawMessage, bridge toolBridge) any {
	var mode string
	if json.Unmarshal(raw, &mode) == nil && mode != "" {
		return mode // auto | required | none
	}
	var named struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &named) == nil && named.Type == "function" && named.Name != "" {
		return map[string]any{"type": "function", "function": map[string]any{"name": bridge.wire("", named.Name)}}
	}
	return nil
}

// chatResponseFormat carries `text.format` (codex exec --output-schema).
func chatResponseFormat(request responsesRequest) map[string]any {
	if request.Text == nil || len(request.Text.Format) == 0 {
		return nil
	}
	var format struct {
		Type   string          `json:"type"`
		Name   string          `json:"name"`
		Schema json.RawMessage `json:"schema"`
		Strict *bool           `json:"strict"`
	}
	if json.Unmarshal(request.Text.Format, &format) != nil || format.Type != "json_schema" {
		return nil
	}
	schema := map[string]any{"name": format.Name, "schema": format.Schema}
	if format.Strict != nil {
		schema["strict"] = *format.Strict
	}
	return map[string]any{"type": "json_schema", "json_schema": schema}
}

// --- native OpenAI Responses -----------------------------------------------

// responsesNativeBody is the caller's body for OpenAI's own /responses:
// `model` rewritten, effort set when given. Reasoning OpenAI cannot verify is
// removed: the runtime's Anthropic envelopes, and items with no
// encrypted_content at all (another model's, which store:false could not look
// up). OpenAI's own encrypted_content passes untouched.
func responsesNativeBody(raw []byte, model, effort, route string) (map[string]json.RawMessage, error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	body["model"] = mustJSON(model)
	if route != "" && route != "openai" {
		restoreReasoning(body, route)
	}
	dropReasoning(body, func(encrypted *string) bool { return encrypted == nil || *encrypted == "" })
	if effort = clampEffort(effort, responsesEfforts); effort != "" {
		var reasoning map[string]json.RawMessage
		_ = json.Unmarshal(body["reasoning"], &reasoning)
		if reasoning == nil {
			reasoning = map[string]json.RawMessage{}
		}
		reasoning["effort"] = mustJSON(effort)
		body["reasoning"] = mustJSON(reasoning)
	}
	return body, nil
}

// dropReasoning removes from body's input every reasoning item carrying the
// runtime's envelope, and every one `also` picks; it reports whether any went.
func dropReasoning(body map[string]json.RawMessage, also func(encrypted *string) bool) bool {
	var items []json.RawMessage
	if json.Unmarshal(body["input"], &items) != nil {
		return false
	}
	kept := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		var fields responsesItem
		if json.Unmarshal(item, &fields) == nil && fields.Type == "reasoning" {
			if _, ours := decodeThinking(fields.EncryptedContent, anthropicRoute); ours || also(fields.EncryptedContent) {
				continue
			}
		}
		kept = append(kept, item)
	}
	if len(kept) == len(items) {
		return false
	}
	body["input"] = mustJSON(kept)
	return true
}
