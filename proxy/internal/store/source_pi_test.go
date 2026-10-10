package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Pi writes one JSONL tree per session under ~/.pi/agent/sessions/<encoded-cwd>/,
// nesting an AgentMessage under "message" and pairing each assistant toolCall
// with a later role:"toolResult" entry. This fixture carries the human block in
// three sessions so the repaste miner can see it, a distinct per-session answer,
// and a tool output in every session that must never become repaste evidence.
func writePiSessionFixture(t *testing.T, root string, index int, humanBlock, toolBlock string) {
	t.Helper()
	readID := fmt.Sprintf("call-%d-read", index)
	spawnID := fmt.Sprintf("call-%d-spawn", index)
	lines := []map[string]any{
		{"type": "session", "id": fmt.Sprintf("sess-%d", index), "timestamp": "2026-08-16T12:00:00Z", "version": 1, "cwd": "/repo/pi"},
		{"type": "message", "id": fmt.Sprintf("%d-a", index), "parentId": nil, "timestamp": "2026-08-16T12:00:01Z", "message": map[string]any{
			"role": "system", "content": "", "sections": map[string]any{"cwd": "/repo/pi", "preamble": "You are an expert coding assistant"},
		}},
		{"type": "message", "id": fmt.Sprintf("%d-b", index), "parentId": fmt.Sprintf("%d-a", index), "timestamp": "2026-08-16T12:00:02Z", "message": map[string]any{
			"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "short request"},
				map[string]any{"type": "text", "text": humanBlock},
			},
		}},
		{"type": "message", "id": fmt.Sprintf("%d-c", index), "parentId": fmt.Sprintf("%d-b", index), "timestamp": "2026-08-16T12:00:03Z", "message": map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "thinking", "thinking": fmt.Sprintf("unique reasoning %d", index)},
				map[string]any{"type": "text", "text": fmt.Sprintf("unique answer %d", index)},
				map[string]any{"type": "toolCall", "id": readID, "name": "read", "arguments": map[string]any{"path": "/repo/pi/main.go"}},
				map[string]any{"type": "toolCall", "id": spawnID, "name": "task", "arguments": map[string]any{"task": "explore"}},
			},
			"provider": "anthropic", "model": "claude-sonnet-4-5",
			"usage": map[string]any{
				// Both spellings of the cache fields are present on purpose;
				// a parser that sums them would double-count the cached share.
				"input": 800000, "output": 100,
				"cacheRead": 700000, "cache_read": 424242,
				"cacheWrite": 1000, "cacheCreation": 999999,
				"totalTokens": 704100,
			},
		}},
		{"type": "message", "id": fmt.Sprintf("%d-d", index), "parentId": fmt.Sprintf("%d-c", index), "timestamp": "2026-08-16T12:00:04Z", "message": map[string]any{
			"role": "toolResult", "toolCallId": readID, "toolName": "read", "isError": false,
			"content": []any{map[string]any{"type": "text", "text": toolBlock}},
		}},
		{"type": "message", "id": fmt.Sprintf("%d-e", index), "parentId": fmt.Sprintf("%d-d", index), "timestamp": "2026-08-16T12:00:05Z", "message": map[string]any{
			"role": "toolResult", "toolCallId": spawnID, "toolName": "task", "isError": true,
			"content": []any{map[string]any{"type": "text", "text": fmt.Sprintf("subagent failed %d", index)}},
		}},
	}
	encoded := make([]string, 0, len(lines))
	for _, line := range lines {
		raw, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, string(raw))
	}
	path := filepath.Join(root, "agent", "sessions", "--repo-pi--", fmt.Sprintf("20260816_%02d.jsonl", index))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(encoded, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPiSessionSourceReadsUsageAndCompletesToolCalls(t *testing.T) {
	root := t.TempDir()
	humanBlock := strings.TrimSuffix("PROJECT CONTEXT\n"+strings.Repeat("agent context stays local and byte exact across every request\n", 18), "\n")
	toolBlock := strings.TrimSuffix("TOOL OUTPUT\n"+strings.Repeat("this repeated tool output must never become recurring context\n", 18), "\n")
	for index := 0; index < 3; index++ {
		writePiSessionFixture(t, root, index, humanBlock, toolBlock)
	}

	source := piSessionSource{root: root}
	beh := behaviorScan{SkillUse: map[string]int{}, SessionsBySource: map[string]int{}}
	miner := newRecurringMiner()
	if timeBoxed := scanSessionSourceUntil(source, time.Time{}, nil, &beh, miner, nil); timeBoxed {
		t.Fatal("fixture scan unexpectedly time-boxed")
	}
	if beh.SessionsBySource["pi"] != 3 || beh.Turns != 3 {
		t.Fatalf("behavior scope = sessions %v turns %d, want 3 pi sessions/turns", beh.SessionsBySource, beh.Turns)
	}
	if beh.TaskSpawns != 3 || beh.SessionsWithTasks != 3 {
		t.Fatalf("spawns = %d across %d sessions, want one task spawn per session", beh.TaskSpawns, beh.SessionsWithTasks)
	}
	if len(beh.SessionMetrics) != 3 || beh.SessionMetrics[0].Repo != "/repo/pi" {
		t.Fatalf("session metrics = %+v, want repo taken from message.sections.cwd", beh.SessionMetrics)
	}

	result := miner.result()
	if len(result.Repaste) != 1 {
		t.Fatalf("repaste entries = %d, want only the human block: %+v", len(result.Repaste), result.Repaste)
	}
	if entry := result.Repaste[0]; entry.Sessions != 3 || entry.Fingerprint != hashText(normalizeBlock(humanBlock)) {
		t.Fatalf("repaste entry = %+v, want human block across 3 sessions", entry)
	}

	refs, truncated := source.discover(nil)
	if truncated || len(refs) == 0 {
		t.Fatalf("discover = %d refs truncated=%v", len(refs), truncated)
	}
	var usage turnEvent
	completed := map[string]turnToolCall{}
	for _, ref := range refs {
		if ref.relPath != filepath.Join("agent", "sessions", "--repo-pi--", "20260816_00.jsonl") {
			continue
		}
		source.scanSession(ref, time.Time{}, func(event turnEvent) {
			if event.ContextUsagePresent {
				usage = event
			}
			for _, call := range event.ToolCalls {
				completed[call.Name] = call
			}
		}, nil)
	}
	if usage.Model != "claude-sonnet-4-5" || usage.ProviderKey != "anthropic" || usage.Repo != "/repo/pi" {
		t.Fatalf("normalized usage event = %+v, want model/provider/repo from the assistant message", usage)
	}
	if usage.ContextTotal != 704100 {
		t.Fatalf("ContextTotal = %d, want reported totalTokens 704100", usage.ContextTotal)
	}
	if !usage.CacheUsagePresent || usage.CacheReadInputTokens != 700000 || usage.CacheCreationInputTokens != 1000 {
		t.Fatalf("cache = %d/%d present=%v, want first-present spelling 700000/1000 (never summed)", usage.CacheReadInputTokens, usage.CacheCreationInputTokens, usage.CacheUsagePresent)
	}
	if !usage.BillingUsagePresent || usage.InputFreshTokens != 100000 || usage.OutputTokens != 100 {
		t.Fatalf("billing = fresh %d out %d, want 100000/100", usage.InputFreshTokens, usage.OutputTokens)
	}
	read, ok := completed["read"]
	if !ok || completed["task"].Name != "task" {
		t.Fatalf("completed tool calls = %+v, want read and task paired with their results", completed)
	}
	if !strings.Contains(read.OutputText, "TOOL OUTPUT") || completed["task"].IsError != true {
		t.Fatalf("completed calls = %+v, want read output attached and task marked error", completed)
	}
}

func TestPiTaskSpawnsCountsOnlySpawnTools(t *testing.T) {
	msg := map[string]any{"content": []any{
		map[string]any{"type": "toolCall", "id": "1", "name": "read"},
		map[string]any{"type": "toolCall", "id": "2", "name": "bash"},
		map[string]any{"type": "toolCall", "id": "3", "name": "minimodel"},
		map[string]any{"type": "toolCall", "id": "4", "name": "task"},
		map[string]any{"type": "toolCall", "id": "5", "name": "subagent"},
	}}
	if got := piTaskSpawns(msg); got != 2 {
		t.Fatalf("piTaskSpawns = %d, want 2 (task + subagent only)", got)
	}
}

func TestPiUsageReadsOneSpellingNeverSums(t *testing.T) {
	usage := map[string]any{
		"input": 1000, "input_tokens": 500000,
		"output": 100, "output_tokens": 900000,
		"cacheRead": 400, "cache_read": 600,
		"cacheWrite": 50, "cache_creation": 70, "cacheCreation": 90,
	}
	read, creation, ok := piCacheUsage(usage)
	if !ok || read != 400 || creation != 50 {
		t.Fatalf("piCacheUsage = (%d, %d, %v), want first-present 400/50", read, creation, ok)
	}
	fresh, out, ok := piBillingUsage(usage)
	if !ok || fresh != 600 || out != 100 {
		t.Fatalf("piBillingUsage = (%d, %d, %v), want 600/100 from first-present input minus cacheRead", fresh, out, ok)
	}

	total := map[string]any{"input": 3000, "output": 100, "cacheRead": 700000, "cacheWrite": 1000}
	if got, ok := piContextTotal(total); !ok || got != 704100 {
		t.Fatalf("piContextTotal fallback = (%d, %v), want summed 704100", got, ok)
	}
	if got, ok := piContextTotal(map[string]any{"totalTokens": 12345, "input": 1}); !ok || got != 12345 {
		t.Fatalf("piContextTotal = (%d, %v), want reported totalTokens 12345 to win", got, ok)
	}
}
