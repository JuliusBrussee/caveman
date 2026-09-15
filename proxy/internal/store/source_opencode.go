package store

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type opencodeSessionSource struct {
	root string
}

func (s opencodeSessionSource) id() string { return "opencode" }

func (s opencodeSessionSource) discover(deadline *behaviorDeadline) ([]sessionRef, bool) {
	if s.root == "" {
		return nil, false
	}
	var refs []sessionRef
	timeBoxed := false
	_ = filepath.WalkDir(filepath.Join(s.root, "session"), func(path string, d os.DirEntry, err error) error {
		if deadline != nil && deadline.expired() {
			timeBoxed = true
			return fs.SkipAll
		}
		if err != nil || d.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if !strings.HasPrefix(base, "ses_") || !strings.HasSuffix(base, ".json") {
			return nil
		}
		relPath, relErr := filepath.Rel(s.root, path)
		if relErr != nil {
			relPath = base
		}
		// Session metadata is decoded once, lazily, by scanSession. Eager decode
		// here doubled I/O and retained no information needed for discovery.
		refs = append(refs, sessionRef{path: path, relPath: relPath})
		return nil
	})
	return refs, timeBoxed
}

func (s opencodeSessionSource) scanSession(ref sessionRef, since time.Time, emit func(turnEvent), deadline *behaviorDeadline) bool {
	if deadline != nil && deadline.expired() {
		return true
	}
	meta, err := readOpenCodeObject(ref.path)
	if err != nil {
		return false
	}
	sessionID := firstString(meta["id"])
	if sessionID == "" {
		return false
	}
	repo := firstString(meta["directory"], ref.repo)
	messages, timeBoxed := s.messages(sessionID, deadline)
	if timeBoxed {
		return true
	}
	emit(turnEvent{sessionStart: true, RelPath: ref.relPath, Repo: repo})
	toolPosition := 0
	for _, message := range messages {
		if deadline != nil && deadline.expired() {
			return true
		}
		object, readErr := readOpenCodeObject(message.path)
		if readErr != nil {
			continue
		}
		message.object = object
		ts := epochMillisTime(asMap(message.object["time"])["created"])
		if !since.IsZero() && !ts.IsZero() && ts.Before(since) {
			continue
		}
		role := firstString(message.object["role"])
		if role == "assistant" {
			tokens := asMap(message.object["tokens"])
			ctx, hasUsage := opencodeContextTotal(tokens)
			cacheRead, cacheCreation, hasCacheUsage := opencodeCacheUsage(tokens)
			fresh, out, hasBilling := opencodeBillingUsage(tokens)
			emit(turnEvent{
				Timestamp: ts, ContextTotal: ctx, ContextUsagePresent: hasUsage,
				CacheReadInputTokens: cacheRead, CacheCreationInputTokens: cacheCreation,
				CacheUsagePresent: hasCacheUsage,
				InputFreshTokens:  fresh, OutputTokens: out, BillingUsagePresent: hasBilling,
				UsageMessageID: firstString(message.object["id"]), Model: firstString(message.object["modelID"]),
				ProviderKey: firstString(message.object["providerID"]), RelPath: ref.relPath, Repo: repo,
			})
		}
		parts, truncated := s.parts(firstString(message.object["id"]), deadline)
		if truncated {
			return true
		}
		for _, part := range parts {
			object, readErr := readOpenCodeObject(part.path)
			if readErr != nil {
				continue
			}
			part.object = object
			typeName := firstString(part.object["type"])
			switch typeName {
			case "text":
				if role != "user" && role != "assistant" {
					continue
				}
				text, ok := part.object["text"].(string)
				if !ok || text == "" {
					continue
				}
				emit(turnEvent{
					Timestamp: ts, TextPayloads: []string{text},
					// Part files hold one JSON object. Repaste locators therefore use
					// JSONLLine 0 and address segmenter-v1 blocks inside that part text.
					JSONLLine: 0, RelPath: part.relPath, Repo: repo,
				})
			case "tool":
				state := asMap(part.object["state"])
				input := normalizeOpenCodeToolInput(state["input"])
				outputText := ""
				if output, present := state["output"]; present {
					outputText = toolResultText(output)
				}
				emit(turnEvent{
					Timestamp: ts, JSONLLine: toolPosition, RelPath: part.relPath, Repo: repo,
					ToolCalls: []turnToolCall{{
						Name: firstString(part.object["tool"]), InputSummary: toolInputSummary(input),
						IsError: firstString(state["status"]) == "error", OutputText: outputText,
					}},
				})
				toolPosition++
			}
		}
	}
	return false
}

type opencodeObject struct {
	path    string
	relPath string
	created int64
	id      string
	object  map[string]any
}

func (s opencodeSessionSource) messages(sessionID string, deadline *behaviorDeadline) ([]opencodeObject, bool) {
	dir := filepath.Join(s.root, "message", sessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	var objects []opencodeObject
	for _, entry := range entries {
		if deadline != nil && deadline.expired() {
			return nil, true
		}
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "msg_") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		id, owner, created, readErr := readOpenCodeMessageIndex(path)
		if readErr != nil || owner != sessionID {
			continue
		}
		objects = append(objects, opencodeObject{
			path: path, created: created, id: id,
		})
	}
	sort.Slice(objects, func(i, j int) bool {
		if objects[i].created != objects[j].created {
			return objects[i].created < objects[j].created
		}
		if objects[i].id != objects[j].id {
			return objects[i].id < objects[j].id
		}
		return objects[i].path < objects[j].path
	})
	return objects, false
}

func (s opencodeSessionSource) parts(messageID string, deadline *behaviorDeadline) ([]opencodeObject, bool) {
	if messageID == "" {
		return nil, false
	}
	dir := filepath.Join(s.root, "part", messageID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	var objects []opencodeObject
	for _, entry := range entries {
		if deadline != nil && deadline.expired() {
			return nil, true
		}
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "prt_") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		id, owner, readErr := readOpenCodePartIndex(path)
		if readErr != nil || owner != messageID {
			continue
		}
		relPath, relErr := filepath.Rel(s.root, path)
		if relErr != nil {
			relPath = filepath.Base(path)
		}
		objects = append(objects, opencodeObject{
			path: path, relPath: relPath, id: id,
		})
	}
	sort.Slice(objects, func(i, j int) bool {
		if objects[i].id != objects[j].id {
			return objects[i].id < objects[j].id
		}
		return objects[i].path < objects[j].path
	})
	return objects, false
}

func readOpenCodeMessageIndex(path string) (id, sessionID string, created int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", 0, err
	}
	defer f.Close()
	var index struct {
		ID        string `json:"id"`
		SessionID string `json:"sessionID"`
		Time      struct {
			Created int64 `json:"created"`
		} `json:"time"`
	}
	err = json.NewDecoder(f).Decode(&index)
	return index.ID, index.SessionID, index.Time.Created, err
}

func readOpenCodePartIndex(path string) (id, messageID string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	var index struct {
		ID        string `json:"id"`
		MessageID string `json:"messageID"`
	}
	err = json.NewDecoder(f).Decode(&index)
	return index.ID, index.MessageID, err
}

func readOpenCodeObject(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	err = decoder.Decode(&object)
	return object, err
}

// opencodeBillingUsage reads opencode's DISJOINT shape: tokens.input excludes
// the cache buckets, so it is already the fresh-input figure.
func opencodeBillingUsage(tokens map[string]any) (fresh, output int, present bool) {
	if len(tokens) == 0 {
		return 0, 0, false
	}
	_, hasInput := tokens["input"]
	_, hasOutput := tokens["output"]
	if !hasInput && !hasOutput {
		return 0, 0, false
	}
	fresh64 := int64FromAny(tokens["input"])
	out64 := int64FromAny(tokens["output"])
	if fresh64 < 0 || out64 < 0 || uint64(fresh64) > uint64(^uint(0)>>1) || uint64(out64) > uint64(^uint(0)>>1) {
		return 0, 0, false
	}
	return int(fresh64), int(out64), true
}

func opencodeContextTotal(tokens map[string]any) (int, bool) {
	if len(tokens) == 0 {
		return 0, false
	}
	cache := asMap(tokens["cache"])
	total, ok := checkedNonNegativeSum(
		int64FromAny(tokens["input"]),
		int64FromAny(cache["read"]),
		int64FromAny(cache["write"]),
	)
	if !ok || total <= 0 || uint64(total) > uint64(^uint(0)>>1) {
		return 0, false
	}
	return int(total), true
}

// opencodeCacheUsage preserves tokens.cache.read/write only when that split is
// present in the stored message. Other sources do not get guessed components.
func opencodeCacheUsage(tokens map[string]any) (read, creation int, present bool) {
	cache := asMap(tokens["cache"])
	_, hasRead := cache["read"]
	_, hasWrite := cache["write"]
	if !hasRead && !hasWrite {
		return 0, 0, false
	}
	read64 := int64FromAny(cache["read"])
	write64 := int64FromAny(cache["write"])
	if read64 < 0 || write64 < 0 || uint64(read64) > uint64(^uint(0)>>1) || uint64(write64) > uint64(^uint(0)>>1) {
		return 0, 0, false
	}
	return int(read64), int(write64), true
}

func epochMillisTime(value any) time.Time {
	millis := int64FromAny(value)
	if millis <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(millis).UTC()
}

func normalizeOpenCodeToolInput(value any) any {
	input := asMap(value)
	if len(input) == 0 {
		return value
	}
	copyInput := make(map[string]any, len(input)+1)
	for key, raw := range input {
		copyInput[key] = raw
	}
	if _, exists := copyInput["file_path"]; !exists {
		copyInput["file_path"] = copyInput["filePath"]
	}
	return copyInput
}
