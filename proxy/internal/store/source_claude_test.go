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
