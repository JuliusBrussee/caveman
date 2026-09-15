package store

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	aiderSessionPathMarker = "\x00aider-session:"
	aiderLogicalRelMarker  = "\x00aider-logical:"
)

type aiderSessionSource struct {
	root string
}

func (s aiderSessionSource) id() string { return "aider" }

func (s aiderSessionSource) discover(deadline *behaviorDeadline) ([]sessionRef, bool) {
	if s.root == "" {
		return nil, false
	}
	var refs []sessionRef
	timeBoxed := false
	_ = filepath.WalkDir(s.root, func(path string, d os.DirEntry, err error) error {
		if deadline != nil && deadline.expired() {
			timeBoxed = true
			return fs.SkipAll
		}
		if err != nil || d.IsDir() || d.Name() != ".aider.chat.history.md" {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		sessions := parseAiderHistory(string(raw))
		relPath, relErr := filepath.Rel(s.root, path)
		if relErr != nil {
			relPath = filepath.Base(path)
		}
		for index := range sessions {
			logicalRelPath := relPath
			if index > 0 {
				// Miner session identity is root_kind+rel_path. Preserve separate
				// logical sessions from one history file during aggregation; final
				// locators strip this private suffix before evidence is emitted.
				logicalRelPath += aiderLogicalRelMarker + fmt.Sprintf("%09d", index)
			}
			refs = append(refs, sessionRef{
				path:    path + aiderSessionPathMarker + fmt.Sprintf("%09d", index),
				relPath: logicalRelPath,
				repo:    filepath.Dir(path),
				data:    sessions[index],
			})
		}
		return nil
	})
	return refs, timeBoxed
}

func (s aiderSessionSource) scanSession(ref sessionRef, since time.Time, emit func(turnEvent), deadline *behaviorDeadline) bool {
	if deadline != nil && deadline.expired() {
		return true
	}
	path, index, ok := splitAiderSessionPath(ref.path)
	if !ok {
		return false
	}
	session, parsed := ref.data.(aiderLogicalSession)
	if !parsed {
		raw, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		sessions := parseAiderHistory(string(raw))
		if index < 0 || index >= len(sessions) {
			return false
		}
		session = sessions[index]
	}
	if !since.IsZero() && !session.timestamp.IsZero() && session.timestamp.Before(since) {
		return false
	}
	emit(turnEvent{sessionStart: true, RelPath: ref.relPath, Repo: ref.repo})
	for _, block := range session.blocks {
		if deadline != nil && deadline.expired() {
			return true
		}
		emit(turnEvent{
			Timestamp: session.timestamp, TextPayloads: []string{block.text},
			// Aider is markdown, not JSONL. JSONLLine is the real one-based
			// source line where this user/assistant text block begins.
			JSONLLine: block.line, RelPath: ref.relPath, Repo: ref.repo,
		})
	}
	return false
}

type aiderLogicalSession struct {
	timestamp time.Time
	blocks    []aiderTextBlock
}

type aiderTextBlock struct {
	line int
	text string
}

func parseAiderHistory(history string) []aiderLogicalSession {
	lines := strings.Split(history, "\n")
	var sessions []aiderLogicalSession
	var current aiderLogicalSession
	active := false
	var assistant []string
	assistantLine := 0
	flushAssistant := func() {
		start := -1
		for index := 0; index <= len(assistant); index++ {
			blank := index == len(assistant) || strings.TrimSpace(assistant[index]) == ""
			if !blank && start < 0 {
				start = index
			}
			if blank && start >= 0 {
				current.blocks = append(current.blocks, aiderTextBlock{
					line: assistantLine + start,
					text: strings.Join(assistant[start:index], "\n"),
				})
				start = -1
			}
		}
		assistant = nil
		assistantLine = 0
	}
	flushSession := func() {
		flushAssistant()
		if active && len(current.blocks) > 0 {
			sessions = append(sessions, current)
		}
		current = aiderLogicalSession{}
		active = false
	}

	for index, line := range lines {
		lineNo := index + 1
		if strings.HasPrefix(line, "# aider chat started at ") {
			flushSession()
			active = true
			current.timestamp = parseAiderTimestamp(strings.TrimPrefix(line, "# aider chat started at "))
			continue
		}
		if strings.HasPrefix(line, "#### ") {
			if !active {
				active = true
			}
			flushAssistant()
			if text := strings.TrimPrefix(line, "#### "); text != "" {
				current.blocks = append(current.blocks, aiderTextBlock{line: lineNo, text: text})
			}
			assistantLine = lineNo + 1
			continue
		}
		if active && assistantLine > 0 {
			assistant = append(assistant, line)
		}
	}
	flushSession()
	return sessions
}

func splitAiderSessionPath(encoded string) (string, int, bool) {
	marker := strings.LastIndex(encoded, aiderSessionPathMarker)
	if marker < 0 {
		return "", 0, false
	}
	index, err := strconv.Atoi(encoded[marker+len(aiderSessionPathMarker):])
	if err != nil {
		return "", 0, false
	}
	return encoded[:marker], index, true
}

func parseAiderTimestamp(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05-07:00", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func canonicalizeAiderRecurringResult(result *recurringResult) {
	if result == nil {
		return
	}
	for entryIndex := range result.Repaste {
		for locatorIndex := range result.Repaste[entryIndex].Locators {
			locator := &result.Repaste[entryIndex].Locators[locatorIndex]
			if locator.RootKind != "aider" {
				continue
			}
			if marker := strings.LastIndex(locator.RelPath, aiderLogicalRelMarker); marker >= 0 {
				locator.RelPath = locator.RelPath[:marker]
			}
		}
	}
}
