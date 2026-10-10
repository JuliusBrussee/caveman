package store

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type piSessionSource struct {
	root string
}

func (s piSessionSource) id() string { return "pi" }

func (s piSessionSource) discover(deadline *behaviorDeadline) ([]sessionRef, bool) {
	if s.root == "" {
		return nil, false
	}
	agentSessions := filepath.Join(s.root, "agent", "sessions")
	var refs []sessionRef
	timeBoxed := false
	_ = filepath.WalkDir(agentSessions, func(path string, d os.DirEntry, err error) error {
		if deadline != nil && deadline.expired() {
			timeBoxed = true
			return fs.SkipAll
		}
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		relPath, relErr := filepath.Rel(s.root, path)
		if relErr != nil {
			relPath = filepath.Base(path)
		}
		refs = append(refs, sessionRef{path: path, relPath: relPath, repoProvisional: true})
		return nil
	})
	return refs, timeBoxed
}

// scanSession reads Pi's JSONL tree: every line is a session entry carrying
// type/id/parentId/timestamp, and message entries nest an AgentMessage under
// "message". Assistant messages hold the provider usage, toolCall content
// blocks, and text/thinking; tool results arrive as their own role:"toolResult"
// entries keyed by toolCallId, so calls are completed from a later line.
func (s piSessionSource) scanSession(ref sessionRef, since time.Time, emit func(turnEvent), deadline *behaviorDeadline) bool {
	if deadline != nil && deadline.expired() {
		return true
	}
	f, err := os.Open(ref.path)
	if err != nil {
		return false
	}
	defer f.Close()
	emit(turnEvent{sessionStart: true, RelPath: ref.relPath})

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16<<20)
	pendingTools := map[string]turnToolCall{}
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
		msg := asMap(obj["message"])
		// The working directory lives in the system message's sections, not the
		// entry, so decode it from there before falling back to any path guess.
		repo = firstString(asMap(msg["sections"])["cwd"], obj["cwd"], repo)
		ts := timestampFromObject(obj)
		if !since.IsZero() && !ts.IsZero() && ts.Before(since) {
			continue
		}
		role := firstString(msg["role"])
		payloads := piTextPayloads(role, msg)
		toolCalls := piToolCalls(msg, pendingTools)
		if role == "assistant" {
			usage := asMap(msg["usage"])
			ctx, hasUsage := piContextTotal(usage)
			cacheRead, cacheCreation, hasCache := piCacheUsage(usage)
			fresh, out, hasBilling := piBillingUsage(usage)
			emit(turnEvent{
				Timestamp: ts, ContextTotal: ctx, ContextUsagePresent: hasUsage,
				CacheReadInputTokens: cacheRead, CacheCreationInputTokens: cacheCreation, CacheUsagePresent: hasCache,
				InputFreshTokens: fresh, OutputTokens: out, BillingUsagePresent: hasBilling,
				UsageMessageID: firstString(obj["id"], msg["id"], obj["responseId"]),
				Model:          firstString(msg["model"], obj["model"]), ProviderKey: firstString(msg["provider"], obj["provider"], "pi"),
				ToolCalls: toolCalls, TextPayloads: payloads,
				TaskSpawns: piTaskSpawns(msg), JSONLLine: lineNo, RelPath: ref.relPath, Repo: repo,
			})
			continue
		}
		emit(turnEvent{
			Timestamp: ts, ToolCalls: toolCalls, TextPayloads: payloads,
			JSONLLine: lineNo, RelPath: ref.relPath, Repo: repo,
		})
	}
	return false
}

// piField returns the first present key's value, so a payload that carries two
// spellings of the same field (Pi's bundle emits both `cacheRead` and
// `cache_read`, `cache_creation` and `cacheCreation`) is read once, never summed.
func piField(usage map[string]any, keys ...string) (int64, bool) {
	for _, key := range keys {
		if value, ok := usage[key]; ok {
			return int64FromAny(value), true
		}
	}
	return 0, false
}

func piContextTotal(usage map[string]any) (int, bool) {
	if len(usage) == 0 {
		return 0, false
	}
	total, ok := piField(usage, "totalTokens", "total_tokens", "input_total_tokens")
	if !ok || total <= 0 {
		input, _ := piField(usage, "input", "input_tokens")
		cacheRead, _ := piField(usage, "cacheRead", "cache_read")
		cacheWrite, _ := piField(usage, "cacheWrite", "cache_write", "cache_creation", "cacheCreation")
		output, _ := piField(usage, "output", "output_tokens")
		sum, ok := checkedNonNegativeSum(input, cacheRead, cacheWrite, output)
		if !ok {
			return 0, false
		}
		total = sum
	}
	if total <= 0 || total > math.MaxInt {
		return 0, false
	}
	return int(total), true
}

func piCacheUsage(usage map[string]any) (read, creation int, present bool) {
	if len(usage) == 0 {
		return 0, 0, false
	}
	read64, hasRead := piField(usage, "cacheRead", "cache_read")
	creation64, hasCreation := piField(usage, "cacheWrite", "cache_write", "cache_creation", "cacheCreation")
	if !hasRead && !hasCreation {
		return 0, 0, false
	}
	if read64 < 0 || creation64 < 0 || read64 > math.MaxInt || creation64 > math.MaxInt {
		return 0, 0, false
	}
	if read64 == 0 && creation64 == 0 {
		return 0, 0, false
	}
	return int(read64), int(creation64), true
}

func piBillingUsage(usage map[string]any) (fresh, output int, present bool) {
	if len(usage) == 0 {
		return 0, 0, false
	}
	input64, hasInput := piField(usage, "input", "input_tokens")
	cached64, _ := piField(usage, "cacheRead", "cache_read")
	out64, hasOutput := piField(usage, "output", "output_tokens")
	if !hasInput && !hasOutput {
		return 0, 0, false
	}
	if input64 < 0 || cached64 < 0 || out64 < 0 {
		return 0, 0, false
	}
	// Pi's prompt count is inclusive of the cached share (OpenAI-style), so the
	// fresh bucket is the difference; a cached share larger than the total is
	// clamped rather than emitted as a negative.
	fresh64 := input64
	if cached64 > fresh64 {
		fresh64 = cached64
	}
	fresh64 -= cached64
	if fresh64 == 0 && out64 == 0 {
		return 0, 0, false
	}
	if fresh64 > math.MaxInt || out64 > math.MaxInt {
		return 0, 0, false
	}
	return int(fresh64), int(out64), true
}

// piTextPayloads collects only human/assistant text; tool-result text is
// attached to its completed tool call instead so it never becomes repaste
// evidence (the same exclusion the sibling sources make).
func piTextPayloads(role string, msg map[string]any) []string {
	if role != "user" && role != "assistant" {
		return nil
	}
	content, ok := msg["content"].([]any)
	if !ok {
		if text, ok := msg["content"].(string); ok && text != "" {
			return []string{text}
		}
		return nil
	}
	var out []string
	for _, item := range content {
		block, ok := item.(map[string]any)
		if !ok || firstString(block["type"]) != "text" {
			continue
		}
		if text := firstString(block["text"]); text != "" {
			out = append(out, text)
		}
	}
	return out
}

// piToolCalls pairs an assistant toolCall with the later toolResult entry that
// carries its output, so the completed call reaches the analysis with its
// output text and error status.
func piToolCalls(msg map[string]any, pending map[string]turnToolCall) []turnToolCall {
	if firstString(msg["role"]) == "toolResult" {
		id := firstString(msg["toolCallId"], msg["tool_call_id"])
		call, ok := pending[id]
		if !ok {
			return nil
		}
		delete(pending, id)
		call.IsError, _ = msg["isError"].(bool)
		call.OutputText = toolResultText(msg["content"])
		return []turnToolCall{call}
	}
	content, ok := msg["content"].([]any)
	if !ok {
		return nil
	}
	for _, item := range content {
		block, ok := item.(map[string]any)
		if !ok || firstString(block["type"]) != "toolCall" {
			continue
		}
		id := firstString(block["id"])
		name := firstString(block["name"])
		if id != "" && name != "" {
			pending[id] = turnToolCall{Name: name, InputSummary: toolInputSummary(block["arguments"])}
		}
	}
	return nil
}

// piTaskSpawns counts only subagent-spawning tool calls; an ordinary tool-using
// turn reports zero, matching isSubagentSpawnTool for the sibling sources plus
// Pi's `subagent` extension tool.
func piTaskSpawns(msg map[string]any) int {
	content, ok := msg["content"].([]any)
	if !ok {
		return 0
	}
	spawns := 0
	for _, item := range content {
		block, ok := item.(map[string]any)
		if !ok || firstString(block["type"]) != "toolCall" {
			continue
		}
		if piSubagentSpawn(firstString(block["name"])) {
			spawns++
		}
	}
	return spawns
}

func piSubagentSpawn(name string) bool {
	if isSubagentSpawnTool(name) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(name), "subagent")
}
