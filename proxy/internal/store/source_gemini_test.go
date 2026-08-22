package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGeminiSessionSourceCountsInputOnceAndRoundTripsMessageIndexLocator(t *testing.T) {
	root := t.TempDir()
	block := strings.TrimSuffix("SHARED GEMINI CONTEXT\n"+strings.Repeat("preserve provider semantics and exact local locator bytes\n", 20), "\n")
	for index := 0; index < 3; index++ {
		writeGeminiSessionFixture(t, root, index, block)
	}

	beh := behaviorScan{SkillUse: map[string]int{}, SessionsBySource: map[string]int{}}
	miner := newRecurringMiner()
	source := geminiSessionSource{root: root}
	if timeBoxed := scanSessionSourceUntil(source, time.Time{}, nil, &beh, miner, nil); timeBoxed {
		t.Fatal("fixture scan unexpectedly time-boxed")
	}
	if beh.SessionsBySource["gemini"] != 3 || beh.Turns != 3 {
		t.Fatalf("behavior = sessions %+v turns %d, want 3 gemini sessions/turns", beh.SessionsBySource, beh.Turns)
	}
	if beh.DumbzoneTurns != 3 || len(beh.Contexts) != 3 {
		t.Fatalf("dumbzone/context = %d/%v, want three usage-bearing dumbzone turns", beh.DumbzoneTurns, beh.Contexts)
	}
	for _, context := range beh.Contexts {
		if context != 600_000 {
			t.Fatalf("context = %d, want input 600000 without cached subset 200000", context)
		}
	}

	result := miner.result()
	if len(result.Repaste) != 1 {
		t.Fatalf("repaste entries = %d, want 1: %+v", len(result.Repaste), result.Repaste)
	}
	locator := result.Repaste[0].Locators[0]
	if locator.RootKind != "gemini" || locator.JSONLLine != 0 || locator.BlockIndex != 1 {
		t.Fatalf("locator = %+v, want zero-based message 0/block 1", locator)
	}
	raw, err := os.ReadFile(filepath.Join(root, locator.RelPath))
	if err != nil {
		t.Fatal(err)
	}
	var session struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &session); err != nil {
		t.Fatal(err)
	}
	located := segmentBlocks(session.Messages[locator.JSONLLine].Content)[locator.BlockIndex]
	if contentSHA256(located) != locator.ContentSHA || located != block {
		t.Fatalf("locator round-trip hash/content mismatch: %+v", locator)
	}
	if got, exact := contextWindow("gemini", "gemini-unknown-preview"); got != 1_048_576 || exact {
		t.Fatalf("gemini fallback window = %d exact=%v, want 1048576 fallback", got, exact)
	}
}

func writeGeminiSessionFixture(t *testing.T, root string, index int, block string) {
	t.Helper()
	session := map[string]any{
		"sessionId":   "session-" + itoa(index),
		"projectHash": "opaque-project-hash",
		"startTime":   "2026-08-16T12:00:00Z",
		"lastUpdated": "2026-08-16T12:01:00Z",
		"messages": []any{
			map[string]any{"id": "user-" + itoa(index), "timestamp": "2026-08-16T12:00:00Z", "type": "user", "content": "short request\n\n" + block},
			map[string]any{
				"id": "gemini-" + itoa(index), "timestamp": "2026-08-16T12:00:01Z", "type": "gemini",
				"content": "unique answer " + itoa(index), "model": "gemini-2.5-pro",
				"tokens": map[string]any{"input": 600_000, "output": 100, "cached": 200_000, "thoughts": 20, "tool": 0, "total": 600_120},
			},
			map[string]any{"id": "info-" + itoa(index), "timestamp": "2026-08-16T12:00:02Z", "type": "info", "content": block},
			map[string]any{"id": "error-" + itoa(index), "timestamp": "2026-08-16T12:00:03Z", "type": "error", "content": block},
		},
	}
	raw, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "tmp", "project-hash-"+itoa(index), "chats", "session-"+itoa(index)+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
