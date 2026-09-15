package store

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

type repoUnknownTestSource struct{ events int }

func (s repoUnknownTestSource) id() string { return "unknown" }
func (s repoUnknownTestSource) discover(*behaviorDeadline) ([]sessionRef, bool) {
	return []sessionRef{{path: "unknown", relPath: "unknown"}}, false
}
func (s repoUnknownTestSource) scanSession(_ sessionRef, _ time.Time, emit func(turnEvent), _ *behaviorDeadline) bool {
	emit(turnEvent{sessionStart: true})
	for i := 0; i < s.events; i++ {
		emit(turnEvent{TextPayloads: []string{"payload"}})
	}
	return false
}

func TestLearnPortfolioGroupingConfidenceAndBestNextMoveTieBreak(t *testing.T) {
	sinks := []Sink{
		{SinkID: "claude_md_weight:user", Title: "Loaded user config", PracticeID: "context-compression", Class: classReducible, TokensPerDayRate: 100, Evidence: map[string]any{}},
		{SinkID: "claude_md_sections:user", Title: "Unechoed sections", PracticeID: "context-compression", Class: classReducible, TokensPerDayRate: 20, Evidence: map[string]any{"measured_prefix_source": retroSourceSessionUsage}},
		{SinkID: "recurring_context:repaste:fp", PracticeID: "prompt-prefix-stability", Class: classRecurringContext, TokensPerDayRate: 120, Evidence: map[string]any{"fix_kind": "cavemem_offload", "recurrence_sessions": 3}},
		{SinkID: "config_tax:baseline", PracticeID: "context-compression", Class: classLoadBearing, TokensPerDayRate: 10, Evidence: map[string]any{}},
	}
	portfolio := buildLearnPortfolio(sinks, 30)
	if portfolio == nil || len(portfolio.Groups) != 3 || portfolio.BestNextMove == nil {
		t.Fatalf("portfolio = %+v", portfolio)
	}
	groups := map[string]LearnPortfolioGroup{}
	for _, group := range portfolio.Groups {
		groups[group.FixLabel] = group
	}
	trim := groups["Trim loaded config"]
	if trim.CombinedRatePerDay != 120 || trim.CombinedObservedInWindow != 0 || trim.Confidence != "measured_usage" || len(trim.SinkIDs) != 2 {
		t.Fatalf("trim group = %+v", trim)
	}
	if trim.TopSinkID != "claude_md_weight:user" || trim.TopSinkTitle == "" {
		t.Fatalf("trim top sink = %+v", trim)
	}
	offload := groups["Offload recurring context to cavemem"]
	if offload.Confidence != "transcript_inferred" || offload.NetNote == "" {
		t.Fatalf("offload group = %+v", offload)
	}
	if portfolio.BestNextMove.FixLabel != "Trim loaded config" {
		t.Fatalf("tie-break best next move = %+v", portfolio.BestNextMove)
	}
	raw, err := json.Marshal(portfolio)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"combined_daily_equivalent"`) || !strings.Contains(string(raw), `"combined_rate_per_day"`) || !strings.Contains(string(raw), `"combined_observed_in_window"`) {
		t.Fatalf("portfolio JSON contract = %s", raw)
	}
}

func TestRepoFilterSkipsRepoUnknownSourcesAndCapsPendingEvents(t *testing.T) {
	geminiRoot := t.TempDir()
	writeGeminiSessionFixture(t, geminiRoot, 0, "short block")
	gemini := repoFilteredSource{sessionSource: geminiSessionSource{root: geminiRoot}, filter: "repo"}
	if refs, truncated := gemini.discover(nil); truncated || len(refs) != 0 {
		t.Fatalf("repo-unknown Gemini discover = %d truncated=%v, want skipped", len(refs), truncated)
	}

	beh := behaviorScan{}
	unknown := repoFilteredSource{sessionSource: repoUnknownTestSource{events: maxRepoFilterPendingEvents + 10}, filter: "repo"}
	if truncated := scanSessionSourceUntil(unknown, time.Time{}, nil, &beh, newRecurringMiner(), nil); truncated {
		t.Fatal("defense-in-depth pending-cap fixture truncated")
	}
	if beh.SessionsScanned != 0 || len(beh.SessionTexts) != 0 {
		t.Fatalf("repo-unknown buffered events leaked after cap: %+v", beh)
	}
}

func TestLearnRepoAggregationAndPreDetectionFilter(t *testing.T) {
	claudeRoot := t.TempDir()
	t.Setenv("CAVEMAN_CLAUDE_ROOT", claudeRoot)
	t.Setenv("CAVEMAN_CODEX_ROOT", t.TempDir())
	for _, fixture := range []struct {
		repo, file, id string
		context        int
	}{
		{repo: "alpha", file: "a.jsonl", id: "a", context: 120000},
		{repo: "alpha", file: "b.jsonl", id: "b", context: 110000},
		{repo: "beta", file: "c.jsonl", id: "c", context: 1000},
		{repo: "beta", file: "d.jsonl", id: "d", context: 2000},
	} {
		writeClaudeProject(t, claudeRoot, fixture.repo, fixture.file, []string{
			`{"type":"assistant","timestamp":"2026-08-10T12:00:00Z","message":{"id":"` + fixture.id + `","model":"claude-sonnet-4","usage":{"input_tokens":` + strconv.Itoa(fixture.context) + `}}}`,
		})
	}

	metrics := scanLearnSessionMetrics(map[string]bool{"claude": true}, time.Date(2025, 8, 1, 0, 0, 0, 0, time.UTC), "", false)
	repos := learnRepos(metrics)
	if len(repos) != 2 || repos[0].Repo != "alpha" || repos[0].Sessions != 2 || repos[0].MeasuredPrefixTokens == nil {
		t.Fatalf("repo summaries = %+v", repos)
	}

	s := openRetroTestStore(t)
	alpha, err := s.BuildLearnPlanFilteredWithRetro(t.TempDir(), []string{"claude"}, "365d", RetroOptions{}, "ALPHA")
	if err != nil {
		t.Fatal(err)
	}
	if alpha.SessionsScanned != 2 || alpha.SessionsBySource["claude"] != 2 {
		t.Fatalf("filtered sessions = %d %+v", alpha.SessionsScanned, alpha.SessionsBySource)
	}
	if _, ok := findSink(alpha, "context_dumbzone"); !ok {
		t.Fatalf("alpha detector did not see high-context turns: %v", sinkIDs(alpha))
	}
	beta, err := s.BuildLearnPlanFilteredWithRetro(t.TempDir(), []string{"claude"}, "365d", RetroOptions{}, "beta")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findSink(beta, "context_dumbzone"); ok {
		t.Fatalf("beta filter leaked alpha turns into detection: %v", sinkIDs(beta))
	}
}

func findSink(plan LearnPlan, id string) (Sink, bool) {
	for _, sink := range plan.Sinks {
		if sink.SinkID == id {
			return sink, true
		}
	}
	return Sink{}, false
}
