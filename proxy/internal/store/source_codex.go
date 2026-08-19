package store

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type codexSessionSource struct {
	root string
}

func (s codexSessionSource) id() string { return "codex" }

func (s codexSessionSource) discover(deadline *behaviorDeadline) ([]sessionRef, bool) {
	if s.root == "" {
		return nil, false
	}
	paths, timeBoxed, _ := codexPathsUntil(s.root, func() bool {
		return deadline != nil && deadline.expired()
	})
	refs := make([]sessionRef, 0, len(paths))
	for _, path := range paths {
		relPath, err := filepath.Rel(s.root, path)
		if err != nil {
			relPath = filepath.Base(path)
		}
		refs = append(refs, sessionRef{path: path, relPath: relPath})
	}
	return refs, timeBoxed
}

func (s codexSessionSource) scanSession(ref sessionRef, since time.Time, emit func(turnEvent), deadline *behaviorDeadline) bool {
	if deadline != nil && deadline.expired() {
		return true
	}
	f, err := os.Open(ref.path)
	if err != nil {
		return false
	}
	defer f.Close()
	emit(turnEvent{sessionStart: true, RelPath: ref.relPath, Repo: ref.repo})

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16<<20)
	pendingTools := map[string]turnToolCall{}
	model := ""
	repo := ref.repo
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		if deadline != nil && deadline.expired() {
			return true
		}
		line := scanner.Bytes()
		var obj map[string]any
		if json.Unmarshal(line, &obj) != nil {
			continue
		}
		payload := asMap(obj["payload"])
		switch firstString(obj["type"]) {
		case "session_meta":
			repo = firstString(payload["cwd"], repo)
		case "turn_context":
			model = firstString(payload["model"], model)
			repo = firstString(payload["cwd"], repo)
		}
		ts := timestampFromObject(obj)
		if !since.IsZero() && !ts.IsZero() && ts.Before(since) {
			continue
		}

		info := asMap(payload["info"])
		usage := asMap(info["last_token_usage"])
		if len(usage) == 0 {
			usage = asMap(payload["last_token_usage"])
		}
		ctx, hasUsage := codexContextTotal(usage)
		payloads := codexTextPayloads(obj)
		skillHaystack := ""
		role := firstString(payload["role"])
		if firstString(obj["type"]) == "response_item" && firstString(payload["type"]) == "message" && (role == "user" || role == "assistant") {
			skillHaystack = strings.ToLower(string(line))
		}
		emit(turnEvent{
			Timestamp: ts, ContextTotal: ctx, ContextUsagePresent: hasUsage,
			UsageMessageID: firstString(payload["id"], info["id"], payload["turn_id"], info["turn_id"]),
			Model:          firstString(payload["model"], info["model"], obj["model"], model), ProviderKey: "openai",
			ToolCalls: codexTurnToolCalls(payload, pendingTools), TextPayloads: payloads,
			TaskSpawns: codexTaskSpawns(payload), SkillUses: codexStructuredSkillReferences(payload, payloads),
			SkillHaystack: skillHaystack, Compaction: strings.Contains(strings.ToLower(firstString(payload["type"])), "compact"),
			JSONLLine: lineNo, RelPath: ref.relPath, Repo: repo,
		})
	}
	return false
}

func codexContextTotal(usage map[string]any) (int, bool) {
	if len(usage) == 0 {
		return 0, false
	}
	// Codex/OpenAI input_tokens already includes cached input. Cached tokens are
	// a subset and must never be added again.
	ctx64 := int64FromAny(usage["input_tokens"])
	if ctx64 <= 0 || uint64(ctx64) > uint64(^uint(0)>>1) {
		return 0, false
	}
	return int(ctx64), true
}

func codexTextPayloads(obj map[string]any) []string {
	if firstString(obj["type"]) != "response_item" {
		return nil
	}
	payload := asMap(obj["payload"])
	if firstString(payload["type"]) != "message" {
		return nil
	}
	role := firstString(payload["role"])
	if role != "user" && role != "assistant" {
		return nil
	}
	content, _ := payload["content"].([]any)
	var texts []string
	for _, raw := range content {
		block := asMap(raw)
		switch firstString(block["type"]) {
		case "input_text", "output_text":
			if text := firstString(block["text"]); text != "" {
				texts = append(texts, text)
			}
		}
	}
	return texts
}

func codexStructuredSkillReferences(payload map[string]any, texts []string) []string {
	var refs []string
	for _, text := range texts {
		for _, match := range commandNameMarker.FindAllStringSubmatch(text, -1) {
			if len(match) == 2 {
				refs = append(refs, match[1])
			}
		}
	}
	if codexCallType(firstString(payload["type"])) && strings.EqualFold(codexToolName(payload), "Skill") {
		input := codexToolInput(payload)
		if object := asMap(input); len(object) > 0 {
			refs = append(refs, firstString(object["skill"]), firstString(object["command"]))
		}
	}
	return refs
}

func codexTaskSpawns(payload map[string]any) int {
	if !codexCallType(firstString(payload["type"])) {
		return 0
	}
	name := strings.ToLower(codexToolName(payload))
	if name == "task" || name == "spawn_agent" || strings.HasSuffix(name, ".spawn_agent") || strings.HasSuffix(name, "__spawn_agent") {
		return 1
	}
	return 0
}

func codexTurnToolCalls(payload map[string]any, pending map[string]turnToolCall) []turnToolCall {
	typeName := firstString(payload["type"])
	if codexCallType(typeName) {
		id := firstString(payload["call_id"], payload["id"])
		name := codexToolName(payload)
		if id != "" && name != "" {
			pending[id] = turnToolCall{Name: name, InputSummary: codexToolInputSummary(payload)}
		}
		return nil
	}
	if typeName != "function_call_output" && typeName != "custom_tool_call_output" {
		return nil
	}
	id := firstString(payload["call_id"], payload["id"])
	call, ok := pending[id]
	if !ok {
		return nil
	}
	delete(pending, id)
	call.IsError, _ = payload["is_error"].(bool)
	call.OutputText = toolResultText(payload["output"])
	return []turnToolCall{call}
}

func codexCallType(typeName string) bool {
	return typeName == "function_call" || typeName == "custom_tool_call"
}

func codexToolName(payload map[string]any) string {
	return firstString(payload["name"])
}

func codexToolInput(payload map[string]any) any {
	value := payload["arguments"]
	if value == nil {
		value = payload["input"]
	}
	if text, ok := value.(string); ok {
		var decoded any
		if json.Unmarshal([]byte(text), &decoded) == nil {
			return decoded
		}
	}
	return value
}

func codexToolInputSummary(payload map[string]any) string {
	value := codexToolInput(payload)
	if text, ok := value.(string); ok {
		return text
	}
	return toolInputSummary(value)
}
