package store

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type claudeSessionSource struct {
	root string
}

func (s claudeSessionSource) id() string { return "claude" }

func (s claudeSessionSource) discover(deadline *behaviorDeadline) ([]sessionRef, bool) {
	if s.root == "" {
		return nil, false
	}
	projects := filepath.Join(s.root, "projects")
	var refs []sessionRef
	timeBoxed := false
	_ = filepath.WalkDir(projects, func(path string, d os.DirEntry, err error) error {
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
		refs = append(refs, sessionRef{path: path, relPath: relPath, repo: claudeRepoFromRelPath(relPath), repoProvisional: true})
		return nil
	})
	return refs, timeBoxed
}

func (s claudeSessionSource) scanSession(ref sessionRef, since time.Time, emit func(turnEvent), deadline *behaviorDeadline) bool {
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
		repo = firstString(obj["cwd"], repo)
		ts := timestampFromObject(obj)
		if !since.IsZero() && !ts.IsZero() && ts.Before(since) {
			continue
		}
		ctx, hasUsage := claudeTurnContext(obj)
		cacheRead, cacheCreation, hasCacheUsage := claudeCacheUsage(obj)
		fresh, out, hasBilling := claudeBillingUsage(obj)
		lower := strings.ToLower(string(line))
		emit(turnEvent{
			Timestamp: ts, ContextTotal: ctx, ContextUsagePresent: hasUsage,
			CacheReadInputTokens: cacheRead, CacheCreationInputTokens: cacheCreation,
			CacheUsagePresent: hasCacheUsage,
			InputFreshTokens:  fresh, OutputTokens: out, BillingUsagePresent: hasBilling,
			UsageMessageID: claudeUsageMessageID(obj), Model: claudeModel(obj), ProviderKey: "anthropic",
			ToolCalls: claudeTurnToolCalls(obj, pendingTools), TextPayloads: claudeTextPayloads(obj),
			TaskSpawns: strings.Count(lower, `"name":"task"`), SkillUses: claudeStructuredSkillReferences(obj),
			Compaction: claudeCompactionMarker(obj),
			JSONLLine:  lineNo, RelPath: ref.relPath, Repo: repo, Side: obj["isSidechain"] == true,
		})
	}
	return false
}

// claudeBillingUsage returns the DISJOINT billable buckets Anthropic reports:
// input_tokens is already uncached-only (cache reads and writes are separate
// fields), so no subtraction is needed. Absence of the usage block leaves the
// buckets unstated rather than zero — a fabricated zero would price real
// traffic at nothing.
func claudeBillingUsage(obj map[string]any) (fresh, output int, present bool) {
	msg := asMap(obj["message"])
	usage := asMap(msg["usage"])
	if len(usage) == 0 {
		usage = asMap(obj["usage"])
	}
	if len(usage) == 0 {
		return 0, 0, false
	}
	_, hasInput := usage["input_tokens"]
	_, hasOutput := usage["output_tokens"]
	if !hasInput && !hasOutput {
		return 0, 0, false
	}
	fresh64 := int64FromAny(usage["input_tokens"])
	out64 := int64FromAny(usage["output_tokens"])
	if fresh64 < 0 || out64 < 0 || uint64(fresh64) > uint64(^uint(0)>>1) || uint64(out64) > uint64(^uint(0)>>1) {
		return 0, 0, false
	}
	return int(fresh64), int(out64), true
}

// claudeCacheUsage preserves provider-reported cache components. Presence is
// separate from value so an absent field never becomes a fabricated zero.
func claudeCacheUsage(obj map[string]any) (read, creation int, present bool) {
	msg := asMap(obj["message"])
	usage := asMap(msg["usage"])
	if len(usage) == 0 {
		usage = asMap(obj["usage"])
	}
	_, hasRead := usage["cache_read_input_tokens"]
	_, hasCreation := usage["cache_creation_input_tokens"]
	if !hasRead && !hasCreation {
		return 0, 0, false
	}
	read64 := int64FromAny(usage["cache_read_input_tokens"])
	creation64 := int64FromAny(usage["cache_creation_input_tokens"])
	if read64 < 0 || creation64 < 0 || uint64(read64) > uint64(^uint(0)>>1) || uint64(creation64) > uint64(^uint(0)>>1) {
		return 0, 0, false
	}
	return int(read64), int(creation64), true
}

// Real Claude Code transcripts on this machine carry one compaction as two
// adjacent records: subtype:"compact_boundary", then isCompactSummary:true.
// Both are recognized; readActivityTracker coalesces adjacent marker records.
func claudeCompactionMarker(obj map[string]any) bool {
	return firstString(obj["subtype"]) == "compact_boundary" || obj["isCompactSummary"] == true
}

func claudeRepoFromRelPath(relPath string) string {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(relPath)), "/")
	if len(parts) < 2 || parts[0] != "projects" || parts[1] == "" {
		return ""
	}
	return filepath.FromSlash(strings.ReplaceAll(parts[1], "-", "/"))
}

func claudeTextPayloads(obj map[string]any) []string {
	message := asMap(obj["message"])
	content := message["content"]
	if content == nil {
		content = obj["content"]
	}
	switch typed := content.(type) {
	case string:
		return []string{typed}
	case []any:
		var payloads []string
		for _, raw := range typed {
			block := asMap(raw)
			if firstString(block["type"]) != "text" {
				continue
			}
			if text := firstString(block["text"]); text != "" {
				payloads = append(payloads, text)
			}
		}
		return payloads
	default:
		return nil
	}
}

func claudeStructuredSkillReferences(obj map[string]any) []string {
	var refs []string
	message := asMap(obj["message"])
	if strings.EqualFold(firstString(obj["type"]), "user") || strings.EqualFold(firstString(message["role"]), "user") {
		texts := append(claudeMessageTexts(message["content"]), claudeMessageTexts(obj["content"])...)
		for _, text := range texts {
			for _, match := range commandNameMarker.FindAllStringSubmatch(text, -1) {
				if len(match) == 2 {
					refs = append(refs, match[1])
				}
			}
		}
	}
	content, _ := message["content"].([]any)
	for _, raw := range content {
		block := asMap(raw)
		if !strings.EqualFold(firstString(block["type"]), "tool_use") {
			continue
		}
		input := asMap(block["input"])
		switch {
		case strings.EqualFold(firstString(block["name"]), "Skill"):
			refs = append(refs, firstString(input["skill"]), firstString(input["command"]))
		case strings.EqualFold(firstString(block["name"]), "Task"):
			refs = append(refs, firstString(input["subagent_type"]))
		}
	}
	return refs
}

func claudeTurnToolCalls(obj map[string]any, pending map[string]turnToolCall) []turnToolCall {
	message := asMap(obj["message"])
	content, _ := message["content"].([]any)
	var completed []turnToolCall
	for _, raw := range content {
		block := asMap(raw)
		switch firstString(block["type"]) {
		case "tool_use":
			id := firstString(block["id"])
			name := firstString(block["name"])
			if id != "" && name != "" {
				pending[id] = turnToolCall{Name: name, InputSummary: toolInputSummary(block["input"])}
			}
		case "tool_result":
			id := firstString(block["tool_use_id"])
			call, ok := pending[id]
			if !ok {
				continue
			}
			delete(pending, id)
			call.IsError, _ = block["is_error"].(bool)
			call.OutputText = toolResultText(block["content"])
			completed = append(completed, call)
		}
	}
	return completed
}
