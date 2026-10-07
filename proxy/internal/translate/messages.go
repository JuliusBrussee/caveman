package translate

// Anthropic Messages <-> OpenAI chat-completions translation. It exists so a
// Claude Code install pointed at the runtime can run a model Anthropic never
// served: the caller keeps its own grammar, the upstream gets the one it
// speaks.
//
// What is DROPPED on the way out, because no OpenAI-compatible upstream has a
// field for it: context_management, safeguards, output_config, cache_control
// and the anthropic-beta semantics. They are Anthropic-server features, not
// prompt content, so dropping them changes serving, not the answer. Document
// and other non-text, non-image content blocks are dropped too.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type anthropicRequest struct {
	Model         string               `json:"model"`
	System        json.RawMessage      `json:"system"`
	Messages      []anthropicMessage   `json:"messages"`
	Tools         []anthropicTool      `json:"tools"`
	ToolChoice    *anthropicToolChoice `json:"tool_choice"`
	MaxTokens     int                  `json:"max_tokens"`
	Temperature   *float64             `json:"temperature"`
	TopP          *float64             `json:"top_p"`
	StopSequences []string             `json:"stop_sequences"`
	Stream        bool                 `json:"stream"`
	Thinking      *anthropicThinking   `json:"thinking"`
	OutputConfig  *struct {
		Effort string `json:"effort"`
	} `json:"output_config"`
	Metadata *struct {
		UserID string `json:"user_id"`
	} `json:"metadata"`
}

type anthropicThinking struct {
	Type         string `json:"type"` // enabled | adaptive | disabled
	BudgetTokens int    `json:"budget_tokens"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// anthropicBlock is the union of every content block shape read here. Fields
// not on the block's own type stay zero.
type anthropicBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	// image
	Source *struct {
		Type      string `json:"type"` // base64 | url
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
	} `json:"source"`
	// tool_use
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
	// tool_result
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
	// thinking
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

type anthropicTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicToolChoice struct {
	Type string `json:"type"` // auto | any | tool | none
	Name string `json:"name"`
}

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

// anthropicTextOf joins the text of a string-or-blocks content value.
func anthropicTextOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var direct string
	if json.Unmarshal(raw, &direct) == nil {
		return direct
	}
	var blocks []anthropicBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func anthropicBlocksOf(raw json.RawMessage) []anthropicBlock {
	var direct string
	if json.Unmarshal(raw, &direct) == nil {
		if direct == "" {
			return nil
		}
		return []anthropicBlock{{Type: "text", Text: direct}}
	}
	var blocks []anthropicBlock
	_ = json.Unmarshal(raw, &blocks)
	return blocks
}

// anthropicToChat renders one Anthropic request as an OpenAI chat body for
// `model`, sending the thinking blocks signed `replay` back as
// reasoning_content ("" sends none). Server tools are an error.
func anthropicToChat(request anthropicRequest, model, replay string) (map[string]any, error) {
	messages := make([]openAIMessage, 0, len(request.Messages)+1)
	if system := anthropicTextOf(request.System); system != "" {
		messages = append(messages, openAIMessage{Role: "system", Content: system})
	}
	for _, message := range request.Messages {
		translated, err := anthropicMessageToChat(message, replay)
		if err != nil {
			return nil, err
		}
		messages = append(messages, translated...)
	}
	out := map[string]any{"model": model, "messages": messages}
	if request.MaxTokens > 0 {
		out["max_tokens"] = request.MaxTokens
	}
	if request.Temperature != nil {
		out["temperature"] = *request.Temperature
	}
	if request.TopP != nil {
		out["top_p"] = *request.TopP
	}
	if len(request.StopSequences) > 0 {
		out["stop"] = request.StopSequences
	}
	if request.Stream {
		out["stream"] = true
	}
	if request.Metadata != nil && request.Metadata.UserID != "" {
		out["user"] = request.Metadata.UserID
	}
	if reasoning := chatReasoning(request.Thinking); reasoning != nil {
		out["reasoning"] = reasoning
	}
	tools, err := chatTools(request.Tools)
	if err != nil {
		return nil, err
	}
	if len(tools) > 0 {
		out["tools"] = tools
	}
	if choice := chatToolChoice(request.ToolChoice); choice != nil {
		out["tool_choice"] = choice
	}
	return out, nil
}

// chatReasoning maps Anthropic's thinking onto OpenRouter's reasoning
// parameter. An explicit budget is a budget; "adaptive" has no number, so it
// becomes the middle effort rather than a made-up token count. fitChat
// rewrites it for every other dialect.
func chatReasoning(thinking *anthropicThinking) map[string]any {
	if thinking == nil {
		return nil
	}
	switch thinking.Type {
	case "enabled":
		if thinking.BudgetTokens > 0 {
			return map[string]any{"max_tokens": thinking.BudgetTokens}
		}
		return map[string]any{"effort": "medium"}
	case "adaptive":
		return map[string]any{"effort": "medium"}
	}
	return nil
}

// chatTools rejects Anthropic's server-side tools: they are executed by
// Anthropic's own API, so there is nothing to forward to another provider.
func chatTools(tools []anthropicTool) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		if len(tool.InputSchema) == 0 {
			return nil, fmt.Errorf("tool %q is an Anthropic server tool (type %q) and is only supported on a native Anthropic model", tool.Name, tool.Type)
		}
		function := map[string]any{"name": tool.Name, "parameters": tool.InputSchema}
		if tool.Description != "" {
			function["description"] = tool.Description
		}
		out = append(out, map[string]any{"type": "function", "function": function})
	}
	return out, nil
}

func chatToolChoice(choice *anthropicToolChoice) any {
	if choice == nil {
		return nil
	}
	switch choice.Type {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "none":
		return "none"
	case "tool":
		return map[string]any{"type": "function", "function": map[string]any{"name": choice.Name}}
	}
	return nil
}

// anthropicMessageToChat is one Anthropic message as one or more chat
// messages: a turn carrying tool results becomes one `tool` message each.
func anthropicMessageToChat(message anthropicMessage, replay string) ([]openAIMessage, error) {
	var parts []any
	var text, reasoning []string
	var calls []openAIToolCall
	out := []openAIMessage{}
	for _, block := range anthropicBlocksOf(message.Content) {
		switch block.Type {
		case "text":
			text = append(text, block.Text)
			parts = append(parts, map[string]any{"type": "text", "text": block.Text})
		case "image":
			url, err := imageURL(block)
			if err != nil {
				return nil, err
			}
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
		case "tool_use":
			call := openAIToolCall{ID: block.ID, Type: "function"}
			call.Function.Name = block.Name
			call.Function.Arguments = string(block.Input)
			if call.Function.Arguments == "" {
				call.Function.Arguments = "{}"
			}
			calls = append(calls, call)
		case "tool_result":
			content := anthropicTextOf(block.Content)
			if content == "" {
				content = strings.TrimSpace(string(block.Content))
			}
			if block.IsError {
				content = "Error: " + content
			}
			out = append(out, openAIMessage{Role: "tool", ToolCallID: block.ToolUseID, Content: content})
		case "thinking":
			// Provider-private reasoning: replayed only to the route and model
			// that produced it (its signature says which), never to another.
			if replay != "" && block.Signature == replay && message.Role == "assistant" {
				reasoning = append(reasoning, block.Thinking)
			}
		}
	}
	if len(parts) > 0 || len(calls) > 0 || len(reasoning) > 0 {
		translated := openAIMessage{Role: message.Role, ToolCalls: calls, ReasoningContent: strings.Join(reasoning, "\n")}
		if hasImage(parts) {
			translated.Content = parts
		} else if joined := strings.Join(text, "\n"); joined != "" {
			translated.Content = joined
		}
		out = append(out, translated)
	}
	return out, nil
}

func hasImage(parts []any) bool {
	for _, part := range parts {
		if typed, ok := part.(map[string]any); ok && typed["type"] == "image_url" {
			return true
		}
	}
	return false
}

func imageURL(block anthropicBlock) (string, error) {
	if block.Source == nil {
		return "", errors.New("image block has no source")
	}
	if block.Source.Type == "url" || block.Source.URL != "" {
		return block.Source.URL, nil
	}
	if block.Source.Data == "" {
		return "", errors.New("image block has no data")
	}
	return "data:" + block.Source.MediaType + ";base64," + block.Source.Data, nil
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
				"type": "tool_use", "id": wireCallID(call.ID, parsed.ID, i), "name": call.Function.Name,
				"input": toolInput(call.Function.Arguments),
			})
		}
		stop = anthropicStopReason(choice.FinishReason)
	}
	return mustJSON(map[string]any{
		"id": "msg_" + parsed.ID, "type": "message", "role": "assistant",
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

// routeBody is the caller's Messages body for the Messages host `route`:
// `model` rewritten and the thinking that host may see (stripForeignThinking).
func routeBody(raw []byte, model, route string) (map[string]json.RawMessage, error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	body["model"] = mustJSON(model)
	if bytes.Contains(raw, []byte(`thinking"`)) {
		stripForeignThinking(body, route)
	}
	return body, nil
}

// stripForeignThinking makes a conversation that visited another provider
// sendable to the Messages host `route` again. Anthropic rejects a thinking
// block without its own signature, and Claude Code answers that rejection by
// dropping thinking for the rest of the session.
//
// A Messages host other than Anthropic signs its thinking with its own
// signature; the runtime hands it to the harness as
// "caveman:<route>:<signature>" (redacted_thinking: its data), so it is
// restored for that same route and stripped for every other. Only Anthropic
// keeps an unprefixed signature (Anthropic minted it); unsigned thinking is
// stripped everywhere. A message left empty keeps a placeholder so the turn
// order holds.
//
// When the last assistant turn is a tool call the runtime produced and no
// thinking block leads it any more, manual `thinking` is dropped for this one
// request, because Anthropic requires a final assistant tool turn to start
// with thinking in manual mode. LiteLLM does the same (litellm/utils.py
// last_assistant_with_tool_calls_has_no_thinking_blocks). Anthropic's own
// messages are left byte for byte. It reports whether the body changed.
func stripForeignThinking(body map[string]json.RawMessage, route string) bool {
	var messages []map[string]json.RawMessage
	if json.Unmarshal(body["messages"], &messages) != nil {
		return false
	}
	changed, lastAssistant, foreignLast := false, -1, false
	for index, message := range messages {
		var role string
		_ = json.Unmarshal(message["role"], &role)
		var blocks []map[string]json.RawMessage
		if json.Unmarshal(message["content"], &blocks) != nil {
			continue
		}
		if role == "assistant" {
			lastAssistant, foreignLast = index, false
		}
		kept := make([]map[string]json.RawMessage, 0, len(blocks))
		restored := false
		for _, block := range blocks {
			var kind, signature string
			_ = json.Unmarshal(block["type"], &kind)
			field := "signature"
			if kind == "redacted_thinking" {
				field = "data"
			}
			_ = json.Unmarshal(block[field], &signature)
			if kind != "thinking" && kind != "redacted_thinking" {
				kept = append(kept, block)
				continue
			}
			own, ours := strings.CutPrefix(signature, signaturePrefix+route+":")
			anthropicMinted := signature != "" && !strings.HasPrefix(signature, signaturePrefix)
			switch {
			case ours && own != "":
				block[field], restored = mustJSON(own), true
			case anthropicMinted && route == anthropicRoute:
			case ours && kind == "thinking" && route != anthropicRoute:
				// The host gave no signature: it is replayed unsigned, as it came.
				block[field], restored = mustJSON(""), true
			default:
				foreignLast = foreignLast || role == "assistant"
				continue
			}
			kept = append(kept, block)
		}
		if len(kept) == len(blocks) && !restored {
			continue
		}
		if len(kept) == 0 {
			kept = append(kept, map[string]json.RawMessage{"type": mustJSON("text"), "text": mustJSON("(reasoning omitted)")})
		}
		message["content"] = mustJSON(kept)
		changed = true
	}
	if lastAssistant >= 0 && manualThinking(body["thinking"]) && foreignToolTurn(messages[lastAssistant], foreignLast) {
		delete(body, "thinking")
		changed = true
	}
	if changed {
		body["messages"] = mustJSON(messages)
	}
	return changed
}

// manualThinking: only extended (manual, `enabled`) thinking requires the
// final assistant tool turn to start with a thinking block; adaptive relaxes
// that (Anthropic's extended-thinking docs), so it is left alone.
func manualThinking(raw json.RawMessage) bool {
	var thinking anthropicThinking
	return json.Unmarshal(raw, &thinking) == nil && thinking.Type == "enabled"
}

// foreignToolTurn: the assistant turn calls a tool, has no thinking block
// left, and came from another provider (a stripped block, or a tool id
// Anthropic never mints; Anthropic's are toolu_… and srvtoolu_…).
func foreignToolTurn(message map[string]json.RawMessage, stripped bool) bool {
	var blocks []anthropicBlock
	if json.Unmarshal(message["content"], &blocks) != nil {
		return false
	}
	calls, foreign := false, stripped
	for _, block := range blocks {
		switch block.Type {
		case "thinking", "redacted_thinking":
			return false
		case "tool_use":
			calls = true
			foreign = foreign || !(strings.HasPrefix(block.ID, "toolu_") || strings.HasPrefix(block.ID, "srvtoolu_"))
		}
	}
	return calls && foreign
}

// anthropicToolIDs rewrites the tool_use and tool_result ids of a Messages
// body that Anthropic refuses (a Messages host such as OpenRouter's /messages
// hands Kimi's "functions.Bash:0" to the harness as it came) with safeCallID:
// deterministic, so a call and its result still pair. A body whose ids all
// pass is returned as it came.
func anthropicToolIDs(body []byte) []byte {
	suspect := false
	for _, match := range toolIDField.FindAllSubmatch(body, -1) {
		if suspect = !anthropicCallID.Match(match[1]); suspect {
			break
		}
	}
	var fields map[string]json.RawMessage
	var messages []map[string]json.RawMessage
	if !suspect || json.Unmarshal(body, &fields) != nil || json.Unmarshal(fields["messages"], &messages) != nil {
		return body
	}
	changed := false
	for _, message := range messages {
		var blocks []map[string]json.RawMessage
		if json.Unmarshal(message["content"], &blocks) != nil {
			continue
		}
		touched := false
		for _, block := range blocks {
			var kind, id string
			_ = json.Unmarshal(block["type"], &kind)
			field := map[string]string{"tool_use": "id", "tool_result": "tool_use_id"}[kind]
			if field == "" || json.Unmarshal(block[field], &id) != nil || anthropicCallID.MatchString(id) {
				continue
			}
			block[field], touched = mustJSON(safeCallID(id)), true
		}
		if touched {
			message["content"], changed = mustJSON(blocks), true
		}
	}
	if !changed {
		return body
	}
	fields["messages"] = mustJSON(messages)
	out, err := json.Marshal(fields)
	if err != nil {
		return body
	}
	return out
}

func mustJSON(value any) []byte {
	out, err := json.Marshal(value)
	if err != nil {
		return []byte(`null`)
	}
	return out
}
