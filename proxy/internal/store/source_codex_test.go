package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestCodexSessionSourceMinesHumanMessageRepaste(t *testing.T) {
	root := t.TempDir()
	humanBlock := strings.TrimSuffix("PROJECT CONTEXT\n"+strings.Repeat("billing ownership stays local and byte exact across every request\n", 18), "\n")
	toolBlock := strings.TrimSuffix("TOOL OUTPUT\n"+strings.Repeat("this repeated tool output must never become recurring context\n", 18), "\n")
	for i := 0; i < 3; i++ {
		writeCodexSessionFixture(t, root, i, humanBlock, toolBlock)
	}

	source := codexSessionSource{root: root}
	beh := behaviorScan{SkillUse: map[string]int{}, SessionsBySource: map[string]int{}}
	miner := newRecurringMiner()
	if timeBoxed := scanSessionSourceUntil(source, time.Time{}, nil, &beh, miner, nil); timeBoxed {
		t.Fatal("fixture scan unexpectedly time-boxed")
	}
	result := miner.result()
	if len(result.Repaste) != 1 {
		t.Fatalf("repaste entries = %d, want only human message block (tool output excluded): %+v", len(result.Repaste), result.Repaste)
	}
	entry := result.Repaste[0]
	if entry.Sessions != 3 || entry.Fingerprint != hashText(normalizeBlock(humanBlock)) {
		t.Fatalf("repaste entry = %+v, want human block across 3 sessions", entry)
	}
	for _, locator := range entry.Locators {
		if locator.RootKind != "codex" || locator.JSONLLine != 3 || locator.BlockIndex != 1 || locator.ContentSHA != contentSHA256(humanBlock) {
			t.Errorf("locator = %+v, want codex line 3 block 1 with exact human-block hash", locator)
		}
	}
	if beh.SessionsBySource["codex"] != 3 || beh.Turns != 3 {
		t.Fatalf("behavior scope = sessions %v turns %d, want 3 codex sessions/turns", beh.SessionsBySource, beh.Turns)
	}

	refs, truncated := source.discover(nil)
	if truncated || len(refs) == 0 {
		t.Fatalf("discover = %d refs truncated=%v", len(refs), truncated)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].relPath < refs[j].relPath })
	var usage turnEvent
	source.scanSession(refs[0], time.Time{}, func(event turnEvent) {
		if event.ContextUsagePresent {
			usage = event
		}
	}, nil)
	if usage.Model != "gpt-5.6" || usage.ProviderKey != "openai" || usage.Repo != "/repo/acme" {
		t.Fatalf("normalized usage event = %+v, want model/provider/repo from turn context and session header", usage)
	}
}

func TestBehaviorScanPlanJSONIsDeterministic(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("CAVEMAN_HOME", home)
	t.Setenv("CAVEMAN_CLAUDE_ROOT", t.TempDir())
	t.Setenv("CAVEMAN_CODEX_ROOT", root)
	humanBlock := strings.TrimSuffix("REPEATED BRIEF\n"+strings.Repeat("preserve deterministic order and inferred-only accounting\n", 20), "\n")
	for i := 0; i < 4; i++ {
		writeCodexSessionFixture(t, root, i, humanBlock, "unique tool output "+fmt.Sprint(i))
	}
	store, err := Open(filepath.Join(home, "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	cwd := t.TempDir()
	first, err := store.BuildLearnPlan(cwd, []string{"codex"}, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.BuildLearnPlan(cwd, []string{"codex"}, "")
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("behavior plan JSON changed across identical scans\nfirst:  %s\nsecond: %s", firstJSON, secondJSON)
	}
}

func TestContextWindowUsesExactCatalogThenFallback(t *testing.T) {
	if got, exact := contextWindow("openai", "gpt-5.6"); got != 1_050_000 || !exact {
		t.Fatalf("catalog window = %d exact=%v, want 1050000 exact", got, exact)
	}
	if got, exact := contextWindow("openai", "GPT-5.6"); got != 400_000 || exact {
		t.Fatalf("non-exact model fallback = %d exact=%v, want explicit OpenAI fallback 400000", got, exact)
	}
	if got, exact := contextWindow("anthropic", "claude-experimental-1m"); got != 1_000_000 || exact {
		t.Fatalf("1m marker fallback = %d exact=%v, want 1000000", got, exact)
	}
}

func writeCodexSessionFixture(t *testing.T, root string, index int, humanBlock, toolBlock string) {
	t.Helper()
	lines := []map[string]any{
		{"timestamp": "2026-08-16T12:00:00Z", "type": "session_meta", "payload": map[string]any{"cwd": "/repo/acme", "model_provider": "openai"}},
		{"timestamp": "2026-08-16T12:00:01Z", "type": "turn_context", "payload": map[string]any{"cwd": "/repo/acme", "model": "gpt-5.6"}},
		{"timestamp": "2026-08-16T12:00:02Z", "type": "response_item", "payload": map[string]any{
			"type": "message", "role": "user", "content": []any{
				map[string]any{"type": "input_text", "text": "short request"},
				map[string]any{"type": "input_text", "text": humanBlock},
			},
		}},
		{"timestamp": "2026-08-16T12:00:03Z", "type": "response_item", "payload": map[string]any{
			"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": fmt.Sprintf("unique answer %d", index)}},
		}},
		{"timestamp": "2026-08-16T12:00:04Z", "type": "response_item", "payload": map[string]any{
			"type": "custom_tool_call_output", "call_id": fmt.Sprintf("call-%d", index), "output": toolBlock,
		}},
		{"timestamp": "2026-08-16T12:00:05Z", "type": "event_msg", "payload": map[string]any{
			"type": "token_count", "info": map[string]any{"last_token_usage": map[string]any{"input_tokens": 500_000, "cached_input_tokens": 400_000}},
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
	path := filepath.Join(root, "sessions", "2026", "08", fmt.Sprintf("rollout-%02d.jsonl", index))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(encoded, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
