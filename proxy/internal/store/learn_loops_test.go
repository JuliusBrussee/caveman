package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDetectLearningLoopsRequiresThreeCallsWithinOneSession(t *testing.T) {
	twoFailures := []learnToolCall{
		{Name: "Bash", Input: "cat missing.txt", IsError: true, OutputTokens: 10, Position: 2},
		{Name: "Bash", Input: "cat missing.txt", IsError: true, OutputTokens: 12, Position: 4},
	}
	if got := detectLearningLoops(twoFailures, "session-a"); len(got) != 0 {
		t.Fatalf("two calls must not become a loop: %+v", got)
	}
	// Calls from unrelated sessions are evaluated independently; two plus two
	// never become one cross-session loop.
	if got := detectLearningLoops(twoFailures, "session-b"); len(got) != 0 {
		t.Fatalf("cross-session recurrence must not become a loop: %+v", got)
	}

	threeFailures := append(append([]learnToolCall(nil), twoFailures...),
		learnToolCall{Name: "Bash", Input: "cat missing.txt", IsError: true, OutputTokens: 14, Position: 6})
	got := detectLearningLoops(threeFailures, "session-a")
	if len(got) != 1 || got[0].Kind != "error_loop" || got[0].OutputTokenFloor != 36 {
		t.Fatalf("error loop = %+v", got)
	}
}

func TestDetectLearningLoopsDistinguishesPaginationVariants(t *testing.T) {
	variants := []learnToolCall{
		{Name: "Bash", Input: "rg error build.log | head -50", OutputTokens: 30, Position: 1},
		{Name: "Bash", Input: "rg error build.log | head -100", OutputTokens: 50, Position: 2},
		{Name: "Bash", Input: "rg error build.log | head -200", OutputTokens: 70, Position: 3},
	}
	got := detectLearningLoops(variants, "session")
	if len(got) != 1 || got[0].Kind != "refetch_loop" || got[0].OutputTokenFloor != 80 {
		t.Fatalf("re-fetch loop = %+v", got)
	}

	exactRepeats := []learnToolCall{
		{Name: "Bash", Input: "go test ./...", OutputTokens: 10},
		{Name: "Bash", Input: "go test ./...", OutputTokens: 10},
		{Name: "Bash", Input: "go test ./...", OutputTokens: 10},
	}
	if got := detectLearningLoops(exactRepeats, "session"); len(got) != 0 {
		t.Fatalf("exact successful repeats may be intentional and must stay silent: %+v", got)
	}
}

func TestBuildLearnPlanSurfacesHashedFailureLoopEvidence(t *testing.T) {
	previousSinceClock := sinceClock
	sinceClock = func() time.Time { return time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { sinceClock = previousSinceClock })
	home := t.TempDir()
	claudeDir := t.TempDir()
	t.Setenv("CAVEMAN_HOME", home)
	t.Setenv("CAVEMAN_CLAUDE_ROOT", claudeDir)
	t.Setenv("CAVEMAN_CODEX_ROOT", t.TempDir())

	var lines []string
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("tool-%d", i)
		lines = append(lines,
			fmt.Sprintf(`{"type":"assistant","timestamp":"2026-07-28T12:00:0%dZ","message":{"model":"claude","content":[{"type":"tool_use","id":"%s","name":"Bash","input":{"command":"cat missing-secret-path.txt"}}]}}`, i, id),
			fmt.Sprintf(`{"type":"user","timestamp":"2026-07-28T12:00:1%dZ","message":{"content":[{"type":"tool_result","tool_use_id":"%s","is_error":true,"content":"No such file or directory: missing-secret-path.txt"}]}}`, i, id),
		)
	}
	path := filepath.Join(claudeDir, "projects", "repo", "loop.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := Open(filepath.Join(home, "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	plan, err := store.BuildLearnPlan(t.TempDir(), []string{"claude"}, "30d")
	if err != nil {
		t.Fatal(err)
	}
	var loop *Sink
	for i := range plan.Sinks {
		if strings.HasPrefix(plan.Sinks[i].SinkID, "learning_loop:error_loop:") {
			loop = &plan.Sinks[i]
			break
		}
	}
	if loop == nil {
		t.Fatalf("learning loop sink missing: %v", sinkIDs(plan))
	}
	if loop.Class != classBehavioral || loop.Framing != framingHistorical {
		t.Fatalf("loop classification = %+v", loop)
	}
	serialized := fmt.Sprintf("%+v", loop)
	if strings.Contains(serialized, "missing-secret-path") {
		t.Fatalf("loop finding leaked raw tool input/output: %s", serialized)
	}
	if loop.Evidence["calls"] != 3 {
		t.Fatalf("loop evidence = %+v", loop.Evidence)
	}
}
