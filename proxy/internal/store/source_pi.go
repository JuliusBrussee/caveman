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
		repo = firstString(obj["cwd"], repo)
		ts := timestampFromObject(obj)
		if !since.IsZero() && !ts.IsZero() && ts.Before(since) {
			continue
		}
		payloads := piTextPayloads(obj)
		msg := asMap(obj["message"])
		role := firstString(msg["role"])
		// pi uses OpenAI-style responses; extract usage from message/usage if present
		if role == "assistant" {
			usage := asMap(msg["usage"])
			ctx, hasUsage := piContextTotal(usage)
			cacheRead, cacheCreation, hasCache := piCacheUsage(usage)
			fresh, out, hasBilling := piBillingUsage(usage)
			provider := firstString(obj["provider"], "pi")
			model := firstString(obj["model"], msg["model"], modelFromPi(obj))
			emit(turnEvent{
				Timestamp: ts, ContextTotal: ctx, ContextUsagePresent: hasUsage,
				CacheReadInputTokens: cacheRead, CacheCreationInputTokens: cacheCreation, CacheUsagePresent: hasCache,
				InputFreshTokens: fresh, OutputTokens: out, BillingUsagePresent: hasBilling,
				UsageMessageID: firstString(obj["id"], msg["id"], firstString(obj["responseId"])),
				Model:          model, ProviderKey: provider,
				ToolCalls:      piToolCalls(msg, pendingTools), TextPayloads: payloads,
				TaskSpawns:     piTaskSpawns(msg), JSONLLine: lineNo, RelPath: ref.relPath, Repo: repo,
			})
		} else {
			emit(turnEvent{
				Timestamp: ts, TextPayloads: payloads, JSONLLine: lineNo, RelPath: ref.relPath, Repo: repo,
			})
		}
	}
	return false
}

func modelFromPi(obj map[string]any) string {
	m := asMap(obj["message"])
	if firstString(m["model"]) != "" {
		return firstString(m["model"])
	}
	return ""
}

func piContextTotal(usage map[string]any) (int, bool) {
	if len(usage) == 0 {
		return 0, false
	}
	// Pi/Codeex-like: total context not always split; prefer input_total or sum
	total := int64FromAny(usage["total_tokens"])
	if total <= 0 {
		total = int64FromAny(usage["input_total_tokens"])
	}
	if total <= 0 {
		sum := int64FromAny(usage["input"]) + int64FromAny(usage["cacheRead"]) + int64FromAny(usage["cacheCreation"]) + int64FromAny(usage["output"])
		total = sum
	}
	if total <= 0 || uint64(total) > uint64(^uint(0)>>1) {
		return 0, false
	}
	return int(total), true
}

func piCacheUsage(usage map[string]any) (read, creation int, present bool) {
	if len(usage) == 0 {
		return 0, 0, false
	}
	r := int64FromAny(usage["cache_read"]) + int64FromAny(usage["cacheRead"])
	c := int64FromAny(usage["cache_write"]) + int64FromAny(usage["cacheWrite"]) + int64FromAny(usage["cache_creation"]) + int64FromAny(usage["cacheCreation"])
	if r < 0 || c < 0 {
		return 0, 0, false
	}
	if r == 0 && c == 0 {
		return 0, 0, false
	}
	return int(r), int(c), true
}

func piBillingUsage(usage map[string]any) (fresh, output int, present bool) {
	if len(usage) == 0 {
		return 0, 0, false
	}
	f := int64FromAny(usage["input"]) + int64FromAny(usage["input_tokens"])
	cached := int64FromAny(usage["cache_read"]) + int64FromAny(usage["cacheRead"])
	if f < cached {
		f = cached
	}
	fresh64 := f - cached
	out := int64FromAny(usage["output"]) + int64FromAny(usage["output_tokens"])
	if fresh64 < 0 || out < 0 {
		return 0, 0, false
	}
	if fresh64 == 0 && out == 0 {
		return 0, 0, false
	}
	return int(fresh64), int(out), true
}

func piTextPayloads(obj map[string]any) []string {
	var out []string
	msg := asMap(obj["message"])
	content := msg["content"]
	if arr, ok := content.([]any); ok {
		for _, item := range arr {
			if m, ok := item.(map[string]any); ok {
				if s, ok := m["text"].(string); ok && s != "" {
					out = append(out, s)
				}
				if t, ok := m["thought"].(string); ok && t != "" {
					out = append(out, t)
				}
			}
		}
	} else if s, ok := content.(string); ok && s != "" {
		out = append(out, s)
	}
	return out
}

func piToolCalls(msg map[string]any, pending map[string]turnToolCall) []turnToolCall {
	content := msg["content"]
	if arr, ok := content.([]any); ok {
		var calls []turnToolCall
		for _, item := range arr {
			if m, ok := item.(map[string]any); ok {
				if tc, ok := m["toolCall"].(map[string]any); ok {
					name := firstString(tc["name"], tc["tool"])
					input := tc["arguments"]
					calls = append(calls, turnToolCall{Name: name, InputSummary: toolInputSummary(input)})
				}
				if tr, ok := m["toolResult"].(map[string]any); ok {
					callID := firstString(tr["toolCallId"])
					res := tr["content"]
					pending[callID] = turnToolCall{OutputText: toolResultText(res), IsError: firstString(tr["status"]) == "error"}
				}
			}
		}
		return calls
	}
	return nil
}

func piTaskSpawns(msg map[string]any) int {
	content := msg["content"]
	if arr, ok := content.([]any); ok {
		n := 0
		for _, item := range arr {
			if m, ok := item.(map[string]any); ok {
				if _, ok := m["toolCall"].(map[string]any); ok {
					n++
				}
			}
		}
		return n
	}
	return 0
}
