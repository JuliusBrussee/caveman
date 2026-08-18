package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAiderSessionSourceSplitsHeadersAndRoundTripsMarkdownLineLocator(t *testing.T) {
	root := t.TempDir()
	block := strings.TrimSuffix("SHARED AIDER CONTEXT\n"+strings.Repeat("markdown history keeps exact block bytes for local recovery\n", 20), "\n")
	for index := 0; index < 3; index++ {
		path := filepath.Join(root, "project-"+itoa(index), ".aider.chat.history.md")
		history := strings.Join([]string{
			"# aider chat started at 2026-08-16T12:00:00Z",
			"#### update project " + itoa(index),
			"short assistant preface",
			"",
			block,
			"",
			"# aider chat started at 2026-08-16T13:00:00Z",
			"#### second task " + itoa(index),
			block,
		}, "\n") + "\n"
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(history), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	source := aiderSessionSource{root: root}
	refs, timeBoxed := source.discover(nil)
	if timeBoxed || len(refs) != 6 {
		t.Fatalf("discover = %d logical sessions timeBoxed=%v, want 6", len(refs), timeBoxed)
	}
	for _, ref := range refs {
		if _, ok := ref.data.(aiderLogicalSession); !ok {
			t.Fatalf("discover did not retain parsed logical session: %+v", ref)
		}
	}
	beh := behaviorScan{SkillUse: map[string]int{}, SessionsBySource: map[string]int{}}
	miner := newRecurringMiner()
	if timeBoxed := scanSessionSourceUntil(source, time.Time{}, nil, &beh, miner, nil); timeBoxed {
		t.Fatal("fixture scan unexpectedly time-boxed")
	}
	if beh.SessionsBySource["aider"] != 6 || beh.Turns != 0 || beh.DumbzoneTurns != 0 || len(beh.Contexts) != 0 {
		t.Fatalf("text-only behavior = sessions %+v turns %d dumbzone %d contexts %v", beh.SessionsBySource, beh.Turns, beh.DumbzoneTurns, beh.Contexts)
	}
	result := miner.result()
	if len(result.Repaste) != 1 {
		t.Fatalf("repaste entries = %d, want 1: %+v", len(result.Repaste), result.Repaste)
	}
	if result.Repaste[0].Sessions != 6 {
		t.Fatalf("recurrence sessions = %d, want six logical header-delimited sessions", result.Repaste[0].Sessions)
	}
	canonicalizeAiderRecurringResult(&result)
	for _, sample := range result.Repaste[0].Locators {
		if strings.Contains(sample.RelPath, aiderLogicalRelMarker) {
			t.Fatalf("private logical-session suffix leaked into locator: %+v", sample)
		}
	}
	locator := result.Repaste[0].Locators[0]
	if locator.RootKind != "aider" || locator.JSONLLine != 5 || locator.BlockIndex != 0 {
		t.Fatalf("locator = %+v, want real markdown block start line 5/block 0", locator)
	}
	raw, err := os.ReadFile(filepath.Join(root, locator.RelPath))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	start := locator.JSONLLine - 1
	end := start
	for end < len(lines) && strings.TrimSpace(lines[end]) != "" && !strings.HasPrefix(lines[end], "# aider chat started at ") && !strings.HasPrefix(lines[end], "#### ") {
		end++
	}
	located := strings.Join(lines[start:end], "\n")
	if contentSHA256(located) != locator.ContentSHA || located != block {
		t.Fatalf("locator round-trip hash/content mismatch: %+v", locator)
	}

	t.Setenv("CAVEMAN_AIDER_ROOT", "")
	if aiderRoot() != "" {
		t.Fatalf("aider root should be disabled without explicit env override: %q", aiderRoot())
	}
	if disabled, truncated := (aiderSessionSource{root: aiderRoot()}).discover(nil); truncated || len(disabled) != 0 {
		t.Fatalf("disabled aider discover = %d refs truncated=%v", len(disabled), truncated)
	}
}
