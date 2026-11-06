package translate

import "encoding/json"

func responsesRequestEffort(effort string, opts Options) string {
	if opts.Dialect == dialectKimi {
		return fitKimiResponsesEffort(effort)
	}
	return fitOpenAIEffort(effort, openAIEfforts(opts.Model))
}

func fitKimiResponsesEffort(effort string) string {
	if effort == "ultra" || effort == "persistent" {
		effort = "max"
	}
	return map[string]string{
		"none": "low", "minimal": "low", "low": "low",
		"medium": "high", "high": "high", "xhigh": "high", "max": "max",
	}[effort]
}

// moonshotResponsesBody fits a Responses body to Kimi's native endpoint.
// Kimi is stateless, accepts only tool_choice auto, and has no encrypted
// reasoning replay: its own summary/content is kept, while opaque envelopes
// from OpenAI-shaped hosts are removed.
func moonshotResponsesBody(body map[string]json.RawMessage, effort string) {
	for _, field := range []string{
		"store", "previous_response_id", "conversation", "background", "parallel_tool_calls",
		"temperature", "top_p", "service_tier", "metadata", "prompt_cache_retention",
	} {
		delete(body, field)
	}
	if _, set := body["tools"]; set {
		body["tool_choice"] = json.RawMessage(`"auto"`)
	} else {
		delete(body, "tool_choice")
	}
	if include, ok := filterKimiInclude(body["include"]); ok {
		body["include"] = include
	} else {
		delete(body, "include")
	}
	if input := body["input"]; !isStr(input) {
		if edited, changed := editArray(input, func(_ []byte, item obj) ([]byte, bool) {
			if item == nil || item.str("type") != "reasoning" {
				return nil, false
			}
			encrypted := item.get("encrypted_content")
			if len(encrypted) == 0 || isNull(encrypted) || string(encrypted) == `""` {
				return nil, false
			}
			// Moonshot replays reasoning as summary/content, never as an
			// encrypted blob. Preserve an item only when it has such text;
			// opaque OpenAI-shaped envelopes carry no replayable Kimi text.
			if !hasReasoningText(item.get("summary")) && !hasReasoningText(item.get("content")) {
				return nil, true
			}
			return objectWithout(item, "encrypted_content"), true
		}); changed {
			body["input"] = edited
		}
	}

	reasoning, _ := parseObj(body["reasoning"])
	fitted := objectWithout(reasoning, "summary")
	level := fitKimiResponsesEffort(effort)
	if effort == "" {
		level = fitKimiResponsesEffort(jstr(reasoning.get("effort")))
	}
	fitted = objectWithout(mustObj(fitted), "effort")
	if level != "" {
		fitted = objectWith(mustObj(fitted), "effort", mustJSON(level))
	}
	if string(fitted) != `{}` {
		body["reasoning"] = fitted
	} else {
		delete(body, "reasoning")
	}
}

func mustObj(raw []byte) obj {
	o, _ := parseObj(raw)
	return o
}

func filterKimiInclude(raw json.RawMessage) (json.RawMessage, bool) {
	var values []string
	if json.Unmarshal(raw, &values) != nil {
		return nil, false
	}
	kept := make([]string, 0, len(values))
	for _, value := range values {
		if value == "web_search_call.results" || value == "web_search_call.action.sources" {
			kept = append(kept, value)
		}
	}
	if len(kept) == 0 {
		return nil, false
	}
	return mustJSON(kept), true
}

// objectWithout renders o without member key, preserving every other value.
func objectWithout(o obj, key string) []byte {
	out := []byte{'{'}
	for _, member := range o {
		if member.is(key) {
			continue
		}
		out = append(appendComma(out), '"')
		out = append(out, member.key...)
		out = append(out, '"', ':')
		out = append(out, member.val...)
	}
	return append(out, '}')
}

func hasReasoningText(raw []byte) bool {
	found := false
	eachItem(raw, func(_ []byte, part obj) {
		if part != nil && len(part.get("text")) > 2 {
			found = true
		}
	})
	return found
}
