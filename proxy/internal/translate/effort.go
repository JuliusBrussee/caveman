package translate

import (
	"encoding/json"
	"strings"
)

// Effort dialects: how a chat-completions upstream is told the reasoning
// effort (Options.Dialect). A Messages upstream is not a dialect here: it
// always takes output_config.effort (applyNativeEffort).
const (
	dialectOpenAIChat = "openai_chat" // reasoning_effort: <effort> (OpenAI, xAI, Groq, Gemini's compatible endpoint, ...)
	dialectOpenRouter = "openrouter"  // reasoning: {effort}
	dialectDeepSeek   = "deepseek"    // thinking: {type} + reasoning_effort low|high|max
	dialectToggle     = "toggle"      // thinking: {type: enabled|disabled} (Z.ai GLM, Kimi K2.x)
	dialectQwen       = "qwen"        // enable_thinking + thinking_budget (DashScope)
	dialectNone       = "none"        // no effort field at all (local servers)
)

// effortFields are every field a dialect may set: an incoming value is
// dropped before the effort is written.
var effortFields = []string{"reasoning", "reasoning_effort", "enable_thinking", "thinking_budget"}

// effortBudgetShare is the share of max_tokens a thinking budget gets per
// effort (OpenRouter's documented ratios for budget models).
var effortBudgetShare = map[string]float64{"minimal": 0.1, "low": 0.2, "medium": 0.5, "high": 0.8, "xhigh": 0.9, "max": 0.95}

// applyChatEffort replaces whatever effort a chat body carries with `effort`
// in `dialect`. "none" turns thinking off where the dialect can say so.
func applyChatEffort(body map[string]json.RawMessage, effort, dialect string) {
	for _, field := range effortFields {
		delete(body, field)
	}
	delete(body, "thinking")
	off := effort == "none"
	switch dialect {
	case dialectNone:
	case dialectOpenRouter:
		body["reasoning"] = mustJSON(map[string]string{"effort": effort})
	case dialectDeepSeek:
		if off {
			body["thinking"] = mustJSON(map[string]string{"type": "disabled"})
			return
		}
		// DeepSeek takes low | high | max (its docs map the rest the same way).
		level := map[string]string{"minimal": "low", "low": "low", "medium": "high", "high": "high", "xhigh": "high", "max": "max"}[effort]
		body["thinking"] = mustJSON(map[string]string{"type": "enabled"})
		if level != "" {
			body["reasoning_effort"] = mustJSON(level)
		}
	case dialectToggle:
		kind := "enabled"
		if off {
			kind = "disabled"
		}
		body["thinking"] = mustJSON(map[string]string{"type": kind})
	case dialectQwen:
		body["enable_thinking"] = mustJSON(!off)
		var limit int
		_ = json.Unmarshal(body["max_tokens"], &limit)
		if share := effortBudgetShare[effort]; !off && share > 0 && limit > 0 {
			// DashScope accepts 1..32768.
			body["thinking_budget"] = mustJSON(min(max(int(share*float64(limit)), 1024), 32768))
		}
	default:
		body["reasoning_effort"] = mustJSON(effort)
	}
}

// applyNativeEffort sets the effort on a Messages body: output_config.effort
// (where the model takes one) and a `thinking` the model accepts.
func applyNativeEffort(body map[string]json.RawMessage, model, effort string) {
	for _, field := range effortFields {
		delete(body, field)
	}
	_, _, levels, known := claudeThinking(model)
	if known {
		effort = clampEffort(effort, levels)
	}
	if thinking, keep := nativeThinking(body["thinking"], body["max_tokens"], model, effort); keep {
		body["thinking"] = thinking
	} else {
		delete(body, "thinking")
	}
	var output map[string]json.RawMessage
	if json.Unmarshal(body["output_config"], &output) == nil {
		delete(output, "effort")
	}
	if effort != "" && (!known || len(levels) > 0) {
		if output == nil {
			output = map[string]json.RawMessage{}
		}
		output["effort"] = mustJSON(effort)
	}
	if len(output) > 0 {
		body["output_config"] = mustJSON(output)
	} else {
		delete(body, "output_config")
	}
}

// claudeEffortLevels are the output_config.effort values the current Claude
// models take.
var claudeEffortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// claudeThinking is the thinking shapes and effort levels a Claude model
// takes, read off its id in any spelling (Anthropic's, OpenRouter's
// "anthropic/claude-sonnet-4.5", Bedrock's and Vertex's). known is false for
// every other model: its body passes as it came. id is the normalised id.
//
//   - adaptive only (Opus 4.7+, Sonnet 5, Opus 5.x, Fable): `enabled` with a
//     budget is a 400;
//   - adaptive and manual (the 4.6 models);
//   - manual only (Haiku 4.5, Sonnet/Opus 4.5 and earlier): `adaptive` is a
//     400, and there is no output_config.effort.
//
// ponytail: a name table kept by hand as Anthropic ships models; move the
// shapes into Options if it drifts.
func claudeThinking(model string) (adaptive, manual bool, levels []string, known bool) {
	id := claudeID(model)
	switch {
	case id == "":
	case hasPrefix(id, "claude-opus-4-7", "claude-opus-4-8", "claude-sonnet-5", "claude-opus-5", "claude-fable", "claude-haiku-5"):
		return true, false, claudeEffortLevels, true
	case strings.Contains(id, "-4-6"):
		return true, true, []string{"low", "medium", "high", "max"}, true
	case hasPrefix(id, "claude-3", "claude-haiku-4", "claude-sonnet-4", "claude-opus-4"):
		return false, true, nil, true
	}
	return false, false, nil, false
}

// claudeID is model from "claude-" on, lower case, dots as dashes; "" when
// it names no Claude model.
func claudeID(model string) string {
	id := strings.ToLower(model)
	at := strings.Index(id, "claude-")
	if at < 0 {
		return ""
	}
	return strings.ReplaceAll(id[at:], ".", "-")
}

func hasPrefix(value string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

// thinkingDisableAccepted: adaptive-only models whose thinking can be turned
// off, mapped to whether that holds at xhigh/max (Opus 5 rejects it there).
// Every other adaptive-only model gets `disabled` dropped: thinking stays on,
// never a 400.
var thinkingDisableAccepted = map[string]bool{"claude-sonnet-5": true, "claude-opus-5": false}

// nativeThinking keeps the caller's Anthropic `thinking` where the model
// accepts that shape and translates it where it does not. The effort sets the
// depth through output_config.effort; deleting `thinking` would silently turn
// thinking OFF on models where it is off unless asked for.
//
//   - adaptive only: `enabled` becomes {type: adaptive}; `disabled` is
//     dropped unless thinkingDisableAccepted says the model takes it;
//   - adaptive and manual: passed through;
//   - manual only: `adaptive` becomes {type: enabled} with half of
//     max_tokens as the budget; under the 1,024-token minimum it is dropped;
//   - any other model: passed through.
//
// https://platform.claude.com/docs/en/build-with-claude/extended-thinking
// https://platform.claude.com/docs/en/build-with-claude/effort
func nativeThinking(raw, maxTokens json.RawMessage, model, effort string) (json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var thinking map[string]json.RawMessage
	if json.Unmarshal(raw, &thinking) != nil {
		return raw, true
	}
	var kind string
	_ = json.Unmarshal(thinking["type"], &kind)
	adaptive, manual, _, known := claudeThinking(model)
	if !known {
		return raw, true
	}
	reshaped := map[string]json.RawMessage{}
	if display, ok := thinking["display"]; ok {
		reshaped["display"] = display
	}
	switch {
	case kind == "enabled" && !manual:
		reshaped["type"] = mustJSON("adaptive")
		return mustJSON(reshaped), true
	case kind == "disabled" && !manual:
		// A family name covers its versions: claude-sonnet-5 is claude-sonnet-5-5 too.
		atAnyEffort, accepted := false, false
		for family, any := range thinkingDisableAccepted {
			if hasPrefix(claudeID(model), family) {
				atAnyEffort, accepted = any, true
			}
		}
		if accepted && (atAnyEffort || (effort != "xhigh" && effort != "max")) {
			return raw, true
		}
		return nil, false
	case kind == "adaptive" && !adaptive:
		var limit int
		if json.Unmarshal(maxTokens, &limit) != nil || limit/2 < 1024 {
			return nil, false
		}
		reshaped["type"], reshaped["budget_tokens"] = mustJSON("enabled"), mustJSON(limit/2)
		return mustJSON(reshaped), true
	}
	return raw, true
}
