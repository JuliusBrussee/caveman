package translate

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/JuliusBrussee/caveman/shared/platform/catalog"
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
	case dialectOpenRouter: // OpenAI's effort levels, no "max"
		body["reasoning"] = mustJSON(map[string]string{"effort": clampEffort(effort, responsesEfforts)})
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
	default: // OpenAI's chat reasoning_effort levels, no "max"
		body["reasoning_effort"] = mustJSON(clampEffort(effort, responsesEfforts))
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

// thinkingOff is thinking off as model id takes it at effort: Sonnet 5.5
// only {type: between_tools} alone, at effort high or below; `disabled` where
// thinkingDisableAccepted allows it (raw when raw is that shape); nothing
// where neither is accepted.
func thinkingOff(raw json.RawMessage, id, effort string) (json.RawMessage, bool) {
	upToHigh := effort != "xhigh" && effort != "max"
	if sameModel(id, "claude-sonnet-5-5") {
		if !upToHigh {
			return nil, false
		}
		return json.RawMessage(`{"type":"between_tools"}`), true
	}
	for family, atAnyEffort := range thinkingDisableAccepted {
		if sameModel(id, family) && (atAnyEffort || upToHigh) {
			if bytes.Contains(raw, []byte(`"between_tools"`)) {
				return json.RawMessage(`{"type":"disabled"}`), true
			}
			return raw, true
		}
	}
	return nil, false
}

// FitEffort maps an effort chosen for another model onto the levels model
// takes on grammar's own API (the asked model when a pool target fails): the
// Claude table for Messages, else the catalog's levels for an OpenAI model,
// else OpenAI's common set. "" when nothing fits (a Claude model without
// effort levels, or "none" on one). A Messages body with thinking off
// (disabled, between_tools) caps it at high where the model takes thinking
// off only up to high (thinkingOff's rule; Sonnet 5 takes it at any effort).
func FitEffort(grammar, model, effort string, body []byte) string {
	if grammar != Messages {
		return clampEffort(effort, openAIEfforts(model))
	}
	_, _, levels, _ := claudeThinking(model)
	if len(levels) == 0 {
		return ""
	}
	effort = clampEffort(effort, levels)
	var request struct {
		Thinking struct {
			Type string `json:"type"`
		} `json:"thinking"`
	}
	if (effort == "xhigh" || effort == "max") && json.Unmarshal(body, &request) == nil &&
		(request.Thinking.Type == "disabled" || request.Thinking.Type == "between_tools") {
		if _, takesOff := thinkingOff(json.RawMessage(`{"type":"disabled"}`), claudeID(model), effort); !takesOff {
			return "high"
		}
	}
	return effort
}

// openAIEfforts are the reasoning efforts an OpenAI model takes: the
// catalog's when it lists the model, else the set every current one takes.
func openAIEfforts(model string) []string {
	if levels, ok := catalog.EffortLevels("openai", model); ok {
		return levels
	}
	return responsesEfforts
}

// sameModel: id is model, or a dated snapshot of it ("claude-sonnet-5-20260101").
func sameModel(id, model string) bool {
	return id == model || strings.HasPrefix(id, model+"-20")
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
// Matched as that exact model (or its dated snapshot), never as a family:
// Opus 5.5 refuses `disabled` at every effort and Sonnet 5.5 takes only
// between_tools. Every other adaptive-only model gets `disabled` dropped:
// thinking stays on, never a 400.
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
		// Another host's model: only Sonnet 5.5's own off switch is dropped,
		// as nothing else takes it.
		return raw, kind != "between_tools"
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
		return thinkingOff(raw, claudeID(model), effort)
	case kind == "between_tools":
		// A caller's own Sonnet 5.5 thinking-off: `disabled` where the model
		// takes that instead, nothing where it takes neither.
		if manual {
			return nil, false
		}
		return thinkingOff(json.RawMessage(`{"type":"between_tools"}`), claudeID(model), effort)
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
