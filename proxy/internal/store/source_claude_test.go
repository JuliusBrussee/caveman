package store

import (
	"strings"
	"testing"
	"time"
)

func TestClaudeSessionSourceUsesTranscriptCWDForHyphenatedRepo(t *testing.T) {
	root := t.TempDir()
	realRepo := "/Users/julb/Desktop/GitHub/Caveman-Cloud"
	otherRepo := "/Users/julb/Desktop/GitHub/Other-Repo"
	for _, fixture := range []struct {
		slug, file, cwd, id string
	}{
		{"-Users-julb-Desktop-GitHub-Caveman-Cloud", "a.jsonl", realRepo, "a"},
		{"-Users-julb-Desktop-GitHub-Caveman-Cloud", "b.jsonl", realRepo, "b"},
		{"-Users-julb-Desktop-GitHub-Other-Repo", "c.jsonl", otherRepo, "c"},
		{"-Users-julb-Desktop-GitHub-Other-Repo", "d.jsonl", otherRepo, "d"},
	} {
		writeClaudeProject(t, root, fixture.slug, fixture.file, []string{
			`{"type":"assistant","cwd":"` + fixture.cwd + `","timestamp":"2026-08-10T12:00:00Z","message":{"id":"` + fixture.id + `","model":"claude-sonnet-4-6","usage":{"input_tokens":1000}}}`,
		})
	}

	beh := behaviorScan{SkillUse: map[string]int{}, SessionsBySource: map[string]int{}}
	if truncated := scanSessionSourceUntil(claudeSessionSource{root: root}, time.Time{}, nil, &beh, newRecurringMiner(), nil); truncated {
		t.Fatal("fixture scan truncated")
	}
	repos := learnRepos(beh.SessionMetrics)
	if len(repos) != 2 {
		t.Fatalf("repos = %+v, want two merged real cwd rows", repos)
	}
	for _, repo := range repos {
		if strings.Contains(repo.Repo, "/Caveman/Cloud") || strings.Contains(repo.Repo, "/Other/Repo") {
			t.Fatalf("fabricated dash-decoded repo survived: %+v", repos)
		}
	}

	filtered := behaviorScan{SkillUse: map[string]int{}, SessionsBySource: map[string]int{}}
	source := repoFilteredSource{sessionSource: claudeSessionSource{root: root}, filter: "Caveman-Cloud"}
	if truncated := scanSessionSourceUntil(source, time.Time{}, nil, &filtered, newRecurringMiner(), nil); truncated {
		t.Fatal("filtered fixture scan truncated")
	}
	if filtered.SessionsScanned != 2 || len(filtered.SessionMetrics) != 2 {
		t.Fatalf("hyphenated --repo filter = sessions %d metrics %+v", filtered.SessionsScanned, filtered.SessionMetrics)
	}
}

func TestClaudeSessionSourceExcludesStaleSessionOnlyFromSessionsScanned(t *testing.T) {
	root := t.TempDir()
	writeClaudeProject(t, root, "repo", "stale.jsonl", []string{
		`{"type":"assistant","timestamp":"2026-01-01T00:00:00Z","message":{"model":"claude-sonnet-5","usage":{"input_tokens":100,"output_tokens":10}}}`,
	})
	writeClaudeProject(t, root, "repo", "fresh.jsonl", []string{
		`{"type":"assistant","timestamp":"2026-09-16T00:00:00Z","message":{"model":"claude-sonnet-5","usage":{"input_tokens":100,"output_tokens":10}}}`,
	})

	beh := behaviorScan{SkillUse: map[string]int{}, SessionsBySource: map[string]int{}}
	since := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if truncated := scanSessionSourceUntil(claudeSessionSource{root: root}, since, nil, &beh, newRecurringMiner(), nil); truncated {
		t.Fatal("fixture scan truncated")
	}
	if beh.SessionsScanned != 1 {
		t.Fatalf("SessionsScanned = %d, want 1 (the all-stale session must not count)", beh.SessionsScanned)
	}
}

func TestClaudeTaskSpawnsCountsAgentToolName(t *testing.T) {
	root := t.TempDir()
	writeClaudeProject(t, root, "repo", "s.jsonl", []string{
		`{"type":"assistant","timestamp":"2026-09-16T00:00:00Z","message":{"model":"claude-sonnet-5","content":[{"type":"tool_use","id":"t1","name":"Agent","input":{}}],"usage":{"input_tokens":100,"output_tokens":10}}}`,
	})

	beh := behaviorScan{SkillUse: map[string]int{}, SessionsBySource: map[string]int{}}
	if truncated := scanSessionSourceUntil(claudeSessionSource{root: root}, time.Time{}, nil, &beh, newRecurringMiner(), nil); truncated {
		t.Fatal("fixture scan truncated")
	}
	if beh.TaskSpawns != 1 {
		t.Fatalf("TaskSpawns = %d, want 1 for an Agent-named tool_use block", beh.TaskSpawns)
	}
}

// The structured reference scan classified a spawn by the literal name "Task",
// so an Agent-named tool_use block never contributed its subagent_type. That
// is the label the learn report uses to say WHICH subagent ran, so the
// per-type breakdown went blank on current transcripts even once the spawn
// count itself was fixed (#1075).
func TestClaudeStructuredSkillReferencesReadsAgentSubagentType(t *testing.T) {
	obj := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type":  "tool_use",
					"id":    "t1",
					"name":  "Agent",
					"input": map[string]any{"subagent_type": "Explore"},
				},
			},
		},
	}
	refs := claudeStructuredSkillReferences(obj)
	found := false
	for _, ref := range refs {
		if ref == "Explore" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Agent spawn did not contribute its subagent_type: refs = %+v", refs)
	}
}

// claudeTaskSpawns counted raw JSON substrings, so ANY nested object carrying
// name:"agent" satisfied the byte sequence regardless of the enclosing tool's
// real name. Adding the "agent" alternative for the Task -> Agent rename made
// that pre-existing weakness reachable by ordinary payloads: a tool whose own
// input has a "name" field, or a tool_result quoting a transcript, would book
// phantom spawns and inflate exactly the subagent findings the rename fix was
// meant to make trustworthy. Count decoded tool_use blocks instead.
func TestClaudeTaskSpawnsIgnoresNestedAgentFields(t *testing.T) {
	root := t.TempDir()
	writeClaudeProject(t, root, "repo", "s.jsonl", []string{
		// The invoked tool is Configure; "agent" is just one of its arguments.
		`{"type":"assistant","timestamp":"2026-09-16T00:00:00Z","message":{"model":"claude-sonnet-5","content":[{"type":"tool_use","id":"t1","name":"Configure","input":{"name":"agent"}}],"usage":{"input_tokens":100,"output_tokens":10}}}`,
		// A tool result that happens to quote a spawn block is not a spawn.
		`{"type":"user","timestamp":"2026-09-16T00:00:01Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"wrote {\"name\":\"agent\"} to config"}]}}`,
	})

	beh := behaviorScan{SkillUse: map[string]int{}, SessionsBySource: map[string]int{}}
	if truncated := scanSessionSourceUntil(claudeSessionSource{root: root}, time.Time{}, nil, &beh, newRecurringMiner(), nil); truncated {
		t.Fatal("fixture scan truncated")
	}
	if beh.TaskSpawns != 0 {
		t.Fatalf("TaskSpawns = %d, want 0 (no tool_use block is actually a spawn)", beh.TaskSpawns)
	}
}
