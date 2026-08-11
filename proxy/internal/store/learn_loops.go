package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

const minLearningLoopCalls = 3

type learnToolCall struct {
	Name         string
	Input        string
	IsError      bool
	OutputTokens int
	Position     int
}

type learningLoop struct {
	Kind             string
	Tool             string
	SignatureHash    string
	SessionRef       string
	Calls            int
	Positions        []int
	OutputTokenFloor int
}

var (
	loopPagination = regexp.MustCompile(`(?i)(\|\s*(head|tail)\s+(-n\s*)?\d+|--max-count[= ]\d+|--lines[= ]\d+|\b(limit|offset)[= ]\d+)`)
	loopIntegers   = regexp.MustCompile(`\b\d+\b`)
	loopWhitespace = regexp.MustCompile(`\s+`)
)

func detectLearningLoops(calls []learnToolCall, sessionRef string) []learningLoop {
	groups := map[string][]learnToolCall{}
	for _, call := range calls {
		signature := canonicalToolSignature(call.Name, call.Input)
		if signature == "" {
			continue
		}
		groups[signature] = append(groups[signature], call)
	}
	var loops []learningLoop
	for signature, grouped := range groups {
		if len(grouped) < minLearningLoopCalls {
			continue
		}
		errors := 0
		variants := map[string]bool{}
		var positions, tokenCosts []int
		for _, call := range grouped {
			if call.IsError {
				errors++
			}
			variants[strings.TrimSpace(call.Input)] = true
			positions = append(positions, call.Position)
			tokenCosts = append(tokenCosts, call.OutputTokens)
		}
		kind := ""
		tokenFloor := 0
		if errors*2 >= len(grouped) {
			kind = "error_loop"
			for _, cost := range tokenCosts {
				tokenFloor += cost
			}
		} else if errors == 0 && len(variants) > 1 {
			// Successful repeats become a re-fetch loop only when pagination or
			// output-limit variants collapsed to one signature. Exact repeated
			// commands may be intentional test/build loops and stay silent.
			kind = "refetch_loop"
			sort.Sort(sort.Reverse(sort.IntSlice(tokenCosts)))
			for i := 1; i < len(tokenCosts); i++ {
				tokenFloor += tokenCosts[i]
			}
		}
		if kind == "" {
			continue
		}
		sum := sha256.Sum256([]byte(signature))
		loops = append(loops, learningLoop{
			Kind:             kind,
			Tool:             grouped[0].Name,
			SignatureHash:    hex.EncodeToString(sum[:8]),
			SessionRef:       sessionRef,
			Calls:            len(grouped),
			Positions:        positions,
			OutputTokenFloor: tokenFloor,
		})
	}
	sort.SliceStable(loops, func(i, j int) bool {
		if loops[i].OutputTokenFloor != loops[j].OutputTokenFloor {
			return loops[i].OutputTokenFloor > loops[j].OutputTokenFloor
		}
		return loops[i].SignatureHash < loops[j].SignatureHash
	})
	return loops
}

func canonicalToolSignature(name, input string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	input = strings.ToLower(strings.TrimSpace(input))
	if name == "" || input == "" {
		return ""
	}
	switch name {
	case "bash", "shell", "run_command":
		input = loopPagination.ReplaceAllString(input, " ")
		input = loopIntegers.ReplaceAllString(input, "N")
	}
	input = loopWhitespace.ReplaceAllString(input, " ")
	return name + "::" + strings.TrimSpace(input)
}

func claudeToolCalls(obj map[string]any, pending map[string]learnToolCall, position int) []learnToolCall {
	message, _ := obj["message"].(map[string]any)
	content, _ := message["content"].([]any)
	var completed []learnToolCall
	for _, raw := range content {
		block, _ := raw.(map[string]any)
		switch block["type"] {
		case "tool_use":
			id, _ := block["id"].(string)
			name, _ := block["name"].(string)
			if id == "" || name == "" {
				continue
			}
			pending[id] = learnToolCall{
				Name:     name,
				Input:    toolInputSummary(block["input"]),
				Position: position,
			}
		case "tool_result":
			id, _ := block["tool_use_id"].(string)
			call, ok := pending[id]
			if !ok {
				continue
			}
			delete(pending, id)
			call.IsError, _ = block["is_error"].(bool)
			call.OutputTokens = estimateTokens(toolResultText(block["content"]))
			call.Position = position
			completed = append(completed, call)
		}
	}
	return completed
}

func toolInputSummary(value any) string {
	if input, ok := value.(map[string]any); ok {
		for _, key := range []string{"command", "path", "file_path", "query", "pattern"} {
			if text, ok := input[key].(string); ok && strings.TrimSpace(text) != "" {
				return text
			}
		}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

func toolResultText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		var parts []string
		for _, raw := range typed {
			if block, ok := raw.(map[string]any); ok {
				if text, ok := block["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		raw, _ := json.Marshal(value)
		return string(raw)
	}
}

func learningLoopSinks(loops []learningLoop) []Sink {
	sinks := make([]Sink, 0, len(loops))
	for _, loop := range loops {
		label := "failed"
		suggestion := "Review the first failure before retrying the same call; record a project-local guardrail only after confirming the corrected command."
		if loop.Kind == "refetch_loop" {
			label = "re-fetched"
			suggestion = "Request enough output once, or narrow the query before repeating pagination variants. Treat this as a measured pattern, not proof that every repeat was waste."
		}
		sinks = append(sinks, Sink{
			SinkID: "learning_loop:" + loop.Kind + ":" + loop.SessionRef + ":" + loop.SignatureHash,
			Title:  loop.Tool + " " + label + " " + itoa(loop.Calls) + " times in one session",
			Class:  classBehavioral,
			Basis:  observedLocal,
			Evidence: map[string]any{
				"kind":                        loop.Kind,
				"tool":                        loop.Tool,
				"session_ref":                 loop.SessionRef,
				"signature_sha256_prefix":     loop.SignatureHash,
				"calls":                       loop.Calls,
				"positions":                   loop.Positions,
				"observed_output_token_floor": loop.OutputTokenFloor,
			},
			Suggestion: suggestion,
			Framing:    framingHistorical,
		})
	}
	return sinks
}

func itoa(value int) string {
	// JSON's integer rendering is deterministic and avoids a second formatting
	// dependency in finding titles.
	raw, _ := json.Marshal(value)
	return string(raw)
}
