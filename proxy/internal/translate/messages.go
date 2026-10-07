package translate

// Anthropic Messages <-> OpenAI chat-completions translation. It exists so a
// Claude Code install pointed at the runtime can run a model Anthropic never
// served: the caller keeps its own grammar, the upstream gets the one it
// speaks.
//
// The request side is messages_in.go; this file holds the answer side
// (chat -> Messages), the shared usage shapes and the thinking hygiene of
// Messages bodies.

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

func (u anthropicUsage) usage() Usage {
	return Usage{
		InputTokens:  u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
		OutputTokens: u.OutputTokens, CacheReadTokens: u.CacheReadInputTokens, CacheWriteTokens: u.CacheCreationInputTokens,
	}
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	// ReasoningContent replays a model's own reasoning to the same model
	// (DeepSeek, Kimi and GLM require it inside a tool loop).
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Index    int    `json:"index,omitempty"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// chatUsage is the OpenAI chat usage object plus DeepSeek's cache spelling.
type chatUsage struct {
	PromptTokens        int                  `json:"prompt_tokens"`
	CompletionTokens    int                  `json:"completion_tokens"`
	TotalTokens         int                  `json:"total_tokens"`
	PromptTokensDetails *promptTokensDetails `json:"prompt_tokens_details,omitempty"`
	// PromptCacheHitTokens is DeepSeek's spelling of the cached prompt tokens
	// (prompt_tokens still counts them, as OpenAI's does).
	PromptCacheHitTokens    int `json:"prompt_cache_hit_tokens,omitempty"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details,omitempty"`
}

type promptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
	// CacheWriteTokens are the prompt tokens written to the cache
	// (OpenRouter reports them; prompt_tokens counts them too).
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

// cachedTokens is the prompt tokens read from the provider's cache, in either
// usage dialect.
func (u chatUsage) cachedTokens() int {
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens > 0 {
		return u.PromptTokensDetails.CachedTokens
	}
	return u.PromptCacheHitTokens
}

func (u chatUsage) cacheWriteTokens() int {
	if u.PromptTokensDetails == nil {
		return 0
	}
	return u.PromptTokensDetails.CacheWriteTokens
}

func (u chatUsage) usage() Usage {
	return Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens, CacheReadTokens: u.cachedTokens(), CacheWriteTokens: u.cacheWriteTokens()}
}

// --- answers --------------------------------------------------------------

type chatAnswer struct {
	ID      string `json:"id"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content          string           `json:"content"`
			Reasoning        string           `json:"reasoning"`
			ReasoningContent string           `json:"reasoning_content"`
			ToolCalls        []openAIToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage chatUsage `json:"usage"`
}

// chatToAnthropic renders a non-streamed chat completion as an Anthropic
// message named `model`, its reasoning a thinking block signed `signature`
// (a "caveman:" value, so it never reaches Anthropic).
func chatToAnthropic(body []byte, model, signature string) ([]byte, chatUsage) {
	var parsed chatAnswer
	_ = json.Unmarshal(body, &parsed)
	id := responsesItemID("msg") // the host's own id stays out of the agent's answer (Reply.UpstreamID keeps it)
	blocks := []any{}
	stop := "end_turn"
	if len(parsed.Choices) > 0 {
		choice := parsed.Choices[0]
		if reasoning := choice.Message.Reasoning + choice.Message.ReasoningContent; reasoning != "" {
			blocks = append(blocks, map[string]any{"type": "thinking", "thinking": reasoning, "signature": signature})
		}
		if choice.Message.Content != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": choice.Message.Content})
		}
		for i, call := range choice.Message.ToolCalls {
			blocks = append(blocks, map[string]any{
				"type": "tool_use", "id": wireCallID(call.ID, id, i), "name": call.Function.Name,
				"input": toolInput(call.Function.Arguments),
			})
		}
		stop = anthropicStopReason(choice.FinishReason)
	}
	return mustJSON(map[string]any{
		"id": id, "type": "message", "role": "assistant",
		"model": model, "content": blocks,
		"stop_reason": stop, "stop_sequence": nil,
		"usage": anthropicUsageFromChat(parsed.Usage),
	}), parsed.Usage
}

// toolInput keeps arguments a model emitted as invalid JSON rather than losing
// them: the caller sees the raw string under `_raw`.
func toolInput(arguments string) any {
	var input map[string]any
	if arguments != "" && json.Unmarshal([]byte(arguments), &input) == nil {
		return input
	}
	return map[string]any{"_raw": arguments}
}

// chatAnswerFailed reports a 200 chat body that is no completion: an error
// envelope, no choice, or no JSON at all.
func chatAnswerFailed(body []byte) bool {
	var parsed struct {
		Error   json.RawMessage   `json:"error"`
		Choices []json.RawMessage `json:"choices"`
	}
	return json.Unmarshal(body, &parsed) != nil || len(parsed.Error) > 0 && string(parsed.Error) != "null" || len(parsed.Choices) == 0
}

func anthropicStopReason(finish string) string {
	switch finish {
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	case "content_filter":
		return "refusal"
	default: // stop, "": the turn ended.
		return "end_turn"
	}
}

func anthropicUsageFromChat(usage chatUsage) anthropicUsage {
	cached := usage.cachedTokens()
	// OpenAI counts cached tokens inside prompt_tokens; some upstreams report
	// them alongside it instead. Clamp rather than publish a negative count.
	written := usage.cacheWriteTokens()
	return anthropicUsage{InputTokens: max(usage.PromptTokens-cached-written, 0), OutputTokens: usage.CompletionTokens, CacheReadInputTokens: cached, CacheCreationInputTokens: written}
}

// --- thinking hygiene -------------------------------------------------------

// anthropicRoute is Anthropic's own API: the only host whose signatures are
// kept as they came.
const anthropicRoute = "anthropic"

// signaturePrefix marks every thinking signature Anthropic did not mint.
const signaturePrefix = "caveman:"

// routeTag is "<length>:<route>:", the route a signature names, length
// first so no route's tag starts another's (an id may hold ':', as "x" and
// "x:free" do).
func routeTag(route string) string { return strconv.Itoa(len(route)) + ":" + route + ":" }

// stripForeignThinking makes a conversation that visited another provider
// sendable to the Messages host `route` again. Anthropic rejects a thinking
// block without its own signature, and Claude Code answers that rejection by
// dropping thinking for the rest of the session.
//
// A Messages host other than Anthropic signs its thinking with its own
// signature; the runtime hands it to the harness as
// "caveman:<n>:<route>:<signature>" (redacted_thinking: its data), so it is
// restored for that same route and stripped for every other. Only Anthropic
// keeps an unprefixed signature (Anthropic minted it); unsigned thinking is
// stripped everywhere. A message left empty keeps a placeholder so the turn
// order holds.
//
// When the last assistant turn is a tool call the runtime produced and no
// thinking block leads it any more, manual `thinking` is dropped for this one
// request, because Anthropic requires a final assistant tool turn to start
// with thinking in manual mode. LiteLLM does the same (litellm/utils.py
// last_assistant_with_tool_calls_has_no_thinking_blocks). Every message it
// does not change keeps its bytes. It reports whether the body changed.
func stripForeignThinking(body map[string]json.RawMessage, route string) bool {
	messages := body["messages"]
	if !bytes.Contains(messages, []byte("thinking")) && !manualThinking(body["thinking"]) {
		return false
	}
	type edit struct {
		raw, replacement []byte
	}
	var edits []edit
	changed, foreignLast := false, false
	var lastAssistant obj
	ok := eachItem(messages, func(raw []byte, message obj) {
		edits = append(edits, edit{raw: raw})
		if message == nil {
			return
		}
		assistant := message.str("role") == "assistant"
		if assistant {
			lastAssistant, foreignLast = append(obj(nil), message...), false
		}
		if !bytes.Contains(raw, []byte("thinking")) {
			return
		}
		content, stripped, restored := stripBlocks(message.get("content"), route)
		foreignLast = foreignLast || assistant && stripped
		if content == nil {
			return
		}
		if assistant {
			lastAssistant = setMember(message, "content", content)
		}
		edits[len(edits)-1].replacement = objectWith(message, "content", content)
		changed = changed || stripped || restored
	})
	if !ok {
		return false
	}
	if lastAssistant != nil && manualThinking(body["thinking"]) && foreignToolTurn(lastAssistant, foreignLast) {
		delete(body, "thinking")
		changed = true
	}
	if !changed {
		return false
	}
	out := make([]byte, 0, capHint(len(messages), 64))
	out = append(out, '[')
	for _, e := range edits {
		out = appendComma(out)
		if e.replacement != nil {
			out = append(out, e.replacement...)
		} else {
			out = append(out, e.raw...)
		}
	}
	body["messages"] = append(out, ']')
	return true
}

// stripBlocks applies stripForeignThinking's rule to one message's blocks: the
// new content (nil when nothing changed), whether a block went, whether a
// signature was restored.
func stripBlocks(content []byte, route string) (out []byte, stripped, restored bool) {
	var kept []byte
	count := 0
	whole := eachItem(content, func(raw []byte, block obj) {
		kind := block.str("type")
		if kind != "thinking" && kind != "redacted_thinking" {
			kept = append(openElem(kept), raw...)
			count++
			return
		}
		field := "signature"
		if kind == "redacted_thinking" {
			field = "data"
		}
		signature := block.str(field)
		own, ours := strings.CutPrefix(signature, signaturePrefix+routeTag(route))
		anthropicMinted := signature != "" && !strings.HasPrefix(signature, signaturePrefix)
		switch {
		case ours && own != "":
			kept = append(openElem(kept), objectWith(block, field, appendString(nil, own))...)
			restored = true
		case anthropicMinted && route == anthropicRoute:
			kept = append(openElem(kept), raw...)
		case ours && kind == "thinking" && route != anthropicRoute:
			// The host gave no signature: it is replayed unsigned, as it came.
			kept = append(openElem(kept), objectWith(block, field, []byte(`""`))...)
			restored = true
		default:
			stripped = true
			return
		}
		count++
	})
	if !whole || !stripped && !restored {
		return nil, false, false
	}
	if count == 0 {
		return []byte(`[{"type":"text","text":"(reasoning omitted)"}]`), stripped, restored
	}
	return append(kept, ']'), stripped, restored
}

// objectWith renders o with member key's value replaced by value (appended
// when o has none); every other member keeps its bytes.
func objectWith(o obj, key string, value []byte) []byte {
	out := make([]byte, 0, 64)
	out = append(out, '{')
	found := false
	for _, member := range o {
		out = append(appendComma(out), '"')
		out = append(append(out, member.key...), '"', ':')
		if member.is(key) {
			out, found = append(out, value...), true
		} else {
			out = append(out, member.val...)
		}
	}
	if !found {
		out = append(appendKey(out, key), value...)
	}
	return append(out, '}')
}

// setMember is o with member key's value replaced.
func setMember(o obj, key string, value []byte) obj {
	out := make(obj, 0, len(o))
	for _, member := range o {
		if member.is(key) {
			member.val = value
		}
		out = append(out, member)
	}
	return out
}

// manualThinking: only extended (manual, `enabled`) thinking requires the
// final assistant tool turn to start with a thinking block; adaptive relaxes
// that (Anthropic's extended-thinking docs), so it is left alone.
func manualThinking(raw json.RawMessage) bool {
	thinking, ok := parseObj(raw)
	return ok && thinking.str("type") == "enabled"
}

// foreignToolTurn: the assistant turn calls a tool, has no thinking block
// left, and came from another provider (a stripped block, or a tool id
// Anthropic never mints; Anthropic's are toolu_… and srvtoolu_…).
func foreignToolTurn(message obj, stripped bool) bool {
	calls, foreign, thinking := false, stripped, false
	eachBlock(message.get("content"), func(kind string, block obj) {
		switch kind {
		case "thinking", "redacted_thinking":
			thinking = true
		case "tool_use":
			id := block.str("id")
			calls = true
			foreign = foreign || !(strings.HasPrefix(id, "toolu_") || strings.HasPrefix(id, "srvtoolu_"))
		}
	})
	return calls && foreign && !thinking
}

// anthropicToolIDs rewrites the tool_use and tool_result ids of a Messages
// body that Anthropic refuses (a Messages host such as OpenRouter's /messages
// hands Kimi's "functions.Bash:0" to the harness as it came) with safeCallID:
// deterministic, so a call and its result still pair. A body whose ids all
// pass is returned as it came.
func anthropicToolIDs(body []byte) []byte {
	fields, err := topFields(body)
	if err != nil || !fixToolIDs(fields) {
		return body
	}
	return rawObject(fields)
}

// fixToolIDs is anthropicToolIDs on a body's members; it reports a change.
func fixToolIDs(fields map[string]json.RawMessage) bool {
	messages := fields["messages"]
	if !bytes.Contains(messages, []byte(`tool_`)) {
		return false
	}
	edited, changed := editArray(messages, func(raw []byte, message obj) ([]byte, bool) {
		if message == nil || !bytes.Contains(raw, []byte(`tool_`)) {
			return nil, false
		}
		content, touched := editArray(message.get("content"), func(_ []byte, block obj) ([]byte, bool) {
			field := map[string]string{"tool_use": "id", "tool_result": "tool_use_id"}[block.str("type")]
			if id := block.get(field); field != "" && isStr(id) && !anthropicCallID.Match(inner(id)) {
				return objectWith(block, field, appendString(nil, safeCallID(jstr(id)))), false
			}
			return nil, false
		})
		if !touched {
			return nil, false
		}
		return objectWith(message, "content", content), false
	})
	if changed {
		fields["messages"] = edited
	}
	return changed
}

func mustJSON(value any) []byte {
	out, err := json.Marshal(value)
	if err != nil {
		return []byte(`null`)
	}
	return out
}
