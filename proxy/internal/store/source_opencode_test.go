package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenCodeSessionSourceAddsExclusiveCacheAndRoundTripsPartLocator(t *testing.T) {
	root := t.TempDir()
	block := strings.TrimSuffix("SHARED OPENCODE CONTEXT\n"+strings.Repeat("cache fields stay exclusive and part locators stay exact\n", 20), "\n")
	for index := 0; index < 3; index++ {
		writeOpenCodeSessionFixture(t, root, index, block)
	}

	beh := behaviorScan{SkillUse: map[string]int{}, SessionsBySource: map[string]int{}}
	miner := newRecurringMiner()
	source := opencodeSessionSource{root: root}
	if timeBoxed := scanSessionSourceUntil(source, time.Time{}, nil, &beh, miner, nil); timeBoxed {
		t.Fatal("fixture scan unexpectedly time-boxed")
	}
	if beh.SessionsBySource["opencode"] != 3 || beh.Turns != 3 {
		t.Fatalf("behavior = sessions %+v turns %d, want 3 opencode sessions/turns", beh.SessionsBySource, beh.Turns)
	}
	if beh.DumbzoneTurns != 3 || len(beh.Contexts) != 3 {
		t.Fatalf("dumbzone/context = %d/%v, want three usage-bearing dumbzone turns", beh.DumbzoneTurns, beh.Contexts)
	}
	for _, context := range beh.Contexts {
		if context != 704_000 {
			t.Fatalf("context = %d, want input 3000 + cache.read 700000 + cache.write 1000", context)
		}
	}
	if len(beh.LearningLoops) != 3 {
		t.Fatalf("learning loops = %+v, want one normalized error loop per session", beh.LearningLoops)
	}

	result := miner.result()
	if len(result.Repaste) != 1 {
		t.Fatalf("repaste entries = %d, want 1: %+v", len(result.Repaste), result.Repaste)
	}
	locator := result.Repaste[0].Locators[0]
	if locator.RootKind != "opencode" || locator.JSONLLine != 0 || locator.BlockIndex != 1 || !strings.Contains(filepath.ToSlash(locator.RelPath), "/prt_00_text.json") {
		t.Fatalf("locator = %+v, want part file line 0/block 1", locator)
	}
	raw, err := os.ReadFile(filepath.Join(root, locator.RelPath))
	if err != nil {
		t.Fatal(err)
	}
	var part map[string]any
	if err := json.Unmarshal(raw, &part); err != nil {
		t.Fatal(err)
	}
	located := segmentBlocks(firstString(part["text"]))[locator.BlockIndex]
	if contentSHA256(located) != locator.ContentSHA || located != block {
		t.Fatalf("locator round-trip hash/content mismatch: %+v", locator)
	}

	refs, truncated := source.discover(nil)
	if truncated || len(refs) == 0 {
		t.Fatalf("discover = %d refs truncated=%v", len(refs), truncated)
	}
	var usage turnEvent
	source.scanSession(refs[0], time.Time{}, func(event turnEvent) {
		if event.ContextUsagePresent {
			usage = event
		}
	}, nil)
	if usage.ProviderKey != "openai" || usage.Model != "gpt-5.6" || usage.Repo != "/repo/opencode" {
		t.Fatalf("normalized usage event = %+v", usage)
	}
}

func TestCrossProviderDepthAndRepasteSinks(t *testing.T) {
	geminiDir := t.TempDir()
	opencodeDir := t.TempDir()
	block := strings.TrimSuffix("CROSS AGENT CONTEXT\n"+strings.Repeat("one shared memory offload can serve every local agent safely\n", 20), "\n")
	for index := 0; index < 3; index++ {
		writeGeminiSessionFixture(t, geminiDir, index, block)
		writeOpenCodeSessionFixture(t, opencodeDir, index, block)
	}
	t.Setenv("CAVEMAN_HOME", t.TempDir())
	t.Setenv("CAVEMAN_GEMINI_ROOT", geminiDir)
	t.Setenv("CAVEMAN_OPENCODE_ROOT", opencodeDir)
	t.Setenv("CAVEMAN_CLAUDE_ROOT", t.TempDir())
	t.Setenv("CAVEMAN_CODEX_ROOT", t.TempDir())
	t.Setenv("CAVEMAN_AIDER_ROOT", "")

	store := openRetroTestStore(t)
	plan, err := store.BuildLearnPlan(t.TempDir(), []string{"gemini", "opencode"}, "")
	if err != nil {
		t.Fatal(err)
	}
	depth := learnV2Sink(t, plan, "cross_provider:depth")
	if depth.Class != classBehavioral || depth.Framing != framingHistorical || depth.TokensObserved != 0 {
		t.Fatalf("depth sink framing/tokens = %+v", depth)
	}
	if depth.Evidence["deeper_source"] != "opencode" || depth.Evidence["shallower_source"] != "gemini" || depth.Evidence["deeper_sessions"] != 3 || depth.Evidence["shallower_sessions"] != 3 {
		t.Fatalf("depth evidence = %+v", depth.Evidence)
	}
	repaste := learnV2Sink(t, plan, "cross_provider:repaste")
	if repaste.Class != classBehavioral || repaste.Framing != framingHistorical || repaste.TokensObserved != 0 {
		t.Fatalf("repaste sink framing/tokens = %+v", repaste)
	}
	roots, ok := repaste.Evidence["root_kinds"].([]string)
	if !ok || strings.Join(roots, ",") != "gemini,opencode" {
		t.Fatalf("repaste root kinds = %#v", repaste.Evidence["root_kinds"])
	}
	raw, err := json.Marshal([]Sink{depth, repaste})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "tokens_observed") {
		t.Fatalf("cross-provider sinks emitted ungrounded tokens_observed: %s", raw)
	}
}

func writeOpenCodeSessionFixture(t *testing.T, root string, index int, block string) {
	t.Helper()
	sessionID := "ses_" + itoa(index)
	userID := "msg_" + itoa(index) + "_user"
	assistantID := "msg_" + itoa(index) + "_assistant"
	writeJSONFixture(t, filepath.Join(root, "session", "project-1", sessionID+".json"), map[string]any{
		"id": sessionID, "projectID": "project-1", "directory": "/repo/opencode", "title": "fixture",
		"time": map[string]any{"created": 1_765_000_000_000 + index, "updated": 1_765_000_001_000 + index}, "version": "1",
	})
	writeJSONFixture(t, filepath.Join(root, "message", sessionID, userID+".json"), map[string]any{
		"id": userID, "sessionID": sessionID, "role": "user", "time": map[string]any{"created": 1_765_000_000_000 + index},
	})
	writeJSONFixture(t, filepath.Join(root, "message", sessionID, assistantID+".json"), map[string]any{
		"id": assistantID, "sessionID": sessionID, "role": "assistant",
		"time":    map[string]any{"created": 1_765_000_000_100 + index, "completed": 1_765_000_000_200 + index},
		"modelID": "gpt-5.6", "providerID": "openai",
		"tokens": map[string]any{"input": 3_000, "output": 100, "reasoning": 20, "cache": map[string]any{"read": 700_000, "write": 1_000}},
	})
	writeJSONFixture(t, filepath.Join(root, "part", userID, "prt_00_text.json"), map[string]any{
		"id": "prt_00_text", "messageID": userID, "type": "text", "text": "short request\n\n" + block,
	})
	writeJSONFixture(t, filepath.Join(root, "part", assistantID, "prt_00_text.json"), map[string]any{
		"id": "prt_00_text", "messageID": assistantID, "type": "text", "text": "unique answer " + itoa(index),
	})
	for toolIndex := 0; toolIndex < 3; toolIndex++ {
		writeJSONFixture(t, filepath.Join(root, "part", assistantID, "prt_0"+itoa(toolIndex+1)+"_tool.json"), map[string]any{
			"id": "prt_0" + itoa(toolIndex+1) + "_tool", "messageID": assistantID, "type": "tool",
			"tool": "bash", "callID": "call-" + itoa(toolIndex),
			"state": map[string]any{"status": "error", "input": map[string]any{"command": "go test ./..."}, "output": "fixture failure output"},
		})
	}
}

func writeJSONFixture(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
