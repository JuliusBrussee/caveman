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
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// responsesPart is one message content part (input_text, output_text,
// input_image) or one structured tool output item.
type responsesPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Refusal  string `json:"refusal"`
	ImageURL string `json:"image_url"`
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

// --- Responses -> Anthropic Messages ---------------------------------------

// canonicalJSON is raw compacted with every object's keys sorted (the last
// of duplicates kept) and every scalar as written; raw itself when it does
// not parse. Byte-level: a schema is never decoded.
func canonicalJSON(raw json.RawMessage) json.RawMessage {
	value, ok := trimValue(raw)
	if !ok {
		return raw
	}
	out, ok := appendCanonical(make([]byte, 0, len(value)), value)
	if !ok {
		return raw
	}
	return out
}

func appendCanonical(dst, value []byte) ([]byte, bool) {
	switch value[0] {
	case '{':
		members, ok := objectKVs(value, nil)
		if !ok {
			return dst, false
		}
		slices.SortStableFunc(members, func(a, b kv) int { return bytes.Compare(a.key, b.key) })
		dst = append(dst, '{')
		for at, member := range members {
			if at+1 < len(members) && bytes.Equal(member.key, members[at+1].key) {
				continue // a duplicate: the last one counts
			}
			dst = append(appendComma(dst), '"')
			dst = append(append(dst, member.key...), '"', ':')
			if dst, ok = appendCanonical(dst, member.val); !ok {
				return dst, false
			}
		}
		return append(dst, '}'), true
	case '[':
		elements, ok := arrayItems(value, nil)
		if !ok {
			return dst, false
		}
		dst = append(dst, '[')
		for at, element := range elements {
			if at > 0 {
				dst = append(dst, ',')
			}
			if dst, ok = appendCanonical(dst, element); !ok {
				return dst, false
			}
		}
		return append(dst, ']'), true
	}
	return append(dst, value...), true
}

// --- Responses -> OpenAI chat ---------------------------------------------

// --- native OpenAI Responses -----------------------------------------------
