package store

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type geminiSessionSource struct {
	root string
}

func (s geminiSessionSource) id() string { return "gemini" }

func (s geminiSessionSource) discover(deadline *behaviorDeadline) ([]sessionRef, bool) {
	if s.root == "" {
		return nil, false
	}
	var refs []sessionRef
	timeBoxed := false
	_ = filepath.WalkDir(filepath.Join(s.root, "tmp"), func(path string, d os.DirEntry, err error) error {
		if deadline != nil && deadline.expired() {
			timeBoxed = true
			return fs.SkipAll
		}
		if err != nil || d.IsDir() || filepath.Base(filepath.Dir(path)) != "chats" {
			return nil
		}
		base := filepath.Base(path)
		if !strings.HasPrefix(base, "session-") || !strings.HasSuffix(base, ".json") {
			return nil
		}
		relPath, relErr := filepath.Rel(s.root, path)
		if relErr != nil {
			relPath = base
		}
		refs = append(refs, sessionRef{path: path, relPath: relPath})
		return nil
	})
	return refs, timeBoxed
}

func (s geminiSessionSource) scanSession(ref sessionRef, since time.Time, emit func(turnEvent), deadline *behaviorDeadline) bool {
	if deadline != nil && deadline.expired() {
		return true
	}
	raw, err := os.ReadFile(ref.path)
	if err != nil {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var session map[string]any
	if decoder.Decode(&session) != nil {
		return false
	}
	messages, ok := session["messages"].([]any)
	if !ok {
		return false
	}
	emit(turnEvent{sessionStart: true, RelPath: ref.relPath})
	for index, rawMessage := range messages {
		if deadline != nil && deadline.expired() {
			return true
		}
		message := asMap(rawMessage)
		typeName := firstString(message["type"])
		if typeName != "user" && typeName != "gemini" {
			continue
		}
		ts := timestampFromObject(message)
		if !since.IsZero() && !ts.IsZero() && ts.Before(since) {
			continue
		}
		var payloads []string
		if text, ok := message["content"].(string); ok && text != "" {
			payloads = []string{text}
		}
		event := turnEvent{
			Timestamp: ts, TextPayloads: payloads,
			// Gemini chat files are JSON objects, not JSONL. JSONLLine is the
			// zero-based message-array index; BlockIndex addresses segmenter-v1
			// blocks within that message's string content.
			JSONLLine: index, RelPath: ref.relPath,
		}
		if typeName == "gemini" {
			event.ContextTotal, event.ContextUsagePresent = geminiContextTotal(asMap(message["tokens"]))
			// CacheUsagePresent is deliberately NOT set: Gemini reports cache
			// reads but never cache writes, and telling the churn detector
			// "zero writes" would be a fabricated observation.
			event.InputFreshTokens, event.CacheReadInputTokens, event.OutputTokens, event.BillingUsagePresent = geminiBillingUsage(asMap(message["tokens"]))
			event.UsageMessageID = firstString(message["id"])
			event.Model = firstString(message["model"])
			event.ProviderKey = "gemini"
		}
		emit(event)
	}
	return false
}

// geminiBillingUsage normalizes Gemini CLI's INCLUSIVE prompt count the same
// way codexBillingUsage does: `cached` is a subset of `input`, so fresh input
// is the difference. `thoughts` bills at the output rate and is added to it;
// `tool` is already counted inside input.
func geminiBillingUsage(tokens map[string]any) (fresh, cached, output int, present bool) {
	if len(tokens) == 0 {
		return 0, 0, 0, false
	}
	_, hasInput := tokens["input"]
	_, hasOutput := tokens["output"]
	if !hasInput && !hasOutput {
		return 0, 0, 0, false
	}
	input64 := int64FromAny(tokens["input"])
	cached64 := int64FromAny(tokens["cached"])
	out64, ok := checkedNonNegativeSum(int64FromAny(tokens["output"]), int64FromAny(tokens["thoughts"]))
	if !ok || input64 < 0 || cached64 < 0 || cached64 > input64 {
		return 0, 0, 0, false
	}
	fresh64 := input64 - cached64
	if uint64(fresh64) > uint64(^uint(0)>>1) || uint64(cached64) > uint64(^uint(0)>>1) || uint64(out64) > uint64(^uint(0)>>1) {
		return 0, 0, 0, false
	}
	return int(fresh64), int(cached64), int(out64), true
}

func geminiContextTotal(tokens map[string]any) (int, bool) {
	if len(tokens) == 0 {
		return 0, false
	}
	// Gemini CLI's input is total effective prompt context. Real files satisfy
	// total = input + output + thoughts + tool while cached remains a subset of
	// input, so adding cached would double-count the prompt just like Codex.
	input := int64FromAny(tokens["input"])
	if input <= 0 || uint64(input) > uint64(^uint(0)>>1) {
		return 0, false
	}
	return int(input), true
}
