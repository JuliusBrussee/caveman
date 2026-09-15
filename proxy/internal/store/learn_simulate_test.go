package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLearnSimulateOccurrenceAndObservedTurnArithmetic(t *testing.T) {
	fingerprint := "0123456789abcdef"
	plan := LearnPlan{
		Schema: learnSchema, Basis: learnBasis, Window: LearnWindow{Since: "30d"},
		Sinks: []Sink{
			{
				SinkID: "recurring_context:repaste:" + fingerprint, Class: classRecurringContext,
				Evidence: map[string]any{"fingerprint": fingerprint, "occurrence_tokens_total": 2000, "block_tokens": 400, "occurrences_total": 4},
			},
			{SinkID: "claude_md_weight:user", Class: classReducible, TokensPerTurn: 50, Evidence: map[string]any{"path": "/tmp/CLAUDE.md"}},
		},
	}
	out, err := simulateLearnPlan(plan, 10, []string{"claude_md_weight:user", "recurring_context:repaste:" + fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if out.Schema != learnSimulateSchema || len(out.Rows) != 2 {
		t.Fatalf("simulation envelope = %+v", out)
	}
	pointerTokens := estimateTokens(strings.Replace(learnPointerTemplate, "%s", fingerprint, 1))
	wantRecurring := int64(2000 - 400 - 4*pointerTokens)
	wantConfig := int64(500)
	rows := map[string]LearnSimulationRow{}
	for _, row := range out.Rows {
		rows[row.SinkID] = row
	}
	if got := rows["recurring_context:repaste:"+fingerprint]; got.Method != "occurrence_sum" || got.TokensWouldHaveSkipped == nil || *got.TokensWouldHaveSkipped != wantRecurring {
		t.Fatalf("recurring arithmetic = %+v, want %d", got, wantRecurring)
	}
	if got := rows["recurring_context:repaste:"+fingerprint]; got.Basis != "bytes4_estimate" {
		t.Fatalf("recurring basis = %+v", got)
	}
	if got := rows["claude_md_weight:user"]; got.Method != "per_turn_times_observed_turns" || got.TokensWouldHaveSkipped == nil || *got.TokensWouldHaveSkipped != wantConfig {
		t.Fatalf("config arithmetic = %+v, want %d", got, wantConfig)
	}
	if out.TotalTokensWouldHaveSkipped == nil || *out.TotalTokensWouldHaveSkipped != wantRecurring+wantConfig {
		t.Fatalf("simulation total = %+v", out.TotalTokensWouldHaveSkipped)
	}
}

func TestBuildLearnSimulationFilteredUsesPrimaryFilteredTurnCount(t *testing.T) {
	claudeRoot := t.TempDir()
	t.Setenv("CAVEMAN_HOME", t.TempDir())
	t.Setenv("CAVEMAN_CLAUDE_ROOT", claudeRoot)
	t.Setenv("CAVEMAN_CODEX_ROOT", t.TempDir())
	t.Setenv("CAVEMAN_GEMINI_ROOT", t.TempDir())
	t.Setenv("CAVEMAN_OPENCODE_ROOT", t.TempDir())
	t.Setenv("CAVEMAN_AIDER_ROOT", "")
	config := strings.Repeat("loaded config instruction\n", 2500)
	if err := os.WriteFile(filepath.Join(claudeRoot, "CLAUDE.md"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct{ repo, file, id string }{
		{"alpha", "a.jsonl", "a"}, {"alpha", "b.jsonl", "b"},
		{"beta", "c.jsonl", "c"}, {"beta", "d.jsonl", "d"},
	} {
		writeClaudeProject(t, claudeRoot, fixture.repo, fixture.file, []string{
			`{"type":"assistant","timestamp":"2026-08-10T12:00:00Z","message":{"id":"` + fixture.id + `","model":"claude-sonnet-4-6","usage":{"input_tokens":1000}}}`,
		})
	}
	s := openRetroTestStore(t)
	out, err := s.BuildLearnSimulationFiltered(t.TempDir(), []string{"claude"}, "365d", []string{"claude_md_weight:user"}, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != 1 || out.Rows[0].TokensWouldHaveSkipped == nil {
		t.Fatalf("filtered simulation = %+v", out)
	}
	tokens, _ := configTokenCount(config)
	if want := int64(tokens * 2); *out.Rows[0].TokensWouldHaveSkipped != want {
		t.Fatalf("filtered simulation tokens = %d, want %d from two alpha turns", *out.Rows[0].TokensWouldHaveSkipped, want)
	}
}

func TestLearnSimulateRefusesSameFileOverlap(t *testing.T) {
	plan := LearnPlan{Schema: learnSchema, Basis: learnBasis, Sinks: []Sink{
		{SinkID: "claude_md_weight:user", Class: classReducible, TokensPerTurn: 100, Evidence: map[string]any{}},
		{SinkID: "claude_md_sections:user", Class: classReducible, TokensPerTurn: 40, Evidence: map[string]any{}},
	}}
	out, err := simulateLearnPlan(plan, 5, []string{"claude_md_sections:user", "claude_md_weight:user"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != 2 || out.TotalTokensWouldHaveSkipped == nil || *out.TotalTokensWouldHaveSkipped != 500 {
		t.Fatalf("overlap simulation = %+v", out)
	}
	for _, row := range out.Rows {
		if row.SinkID == "claude_md_sections:user" {
			if row.TokensWouldHaveSkipped != nil || !containsCaveat(row.Caveats, "overlaps claude_md_weight:user") {
				t.Fatalf("overlap row not refused: %+v", row)
			}
		}
	}
}
