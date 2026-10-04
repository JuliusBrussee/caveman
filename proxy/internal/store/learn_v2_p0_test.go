package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLearnV2MeasuredPrefixUsesFirstDeduplicatedTurnAcrossSources(t *testing.T) {
	claudeDir := t.TempDir()
	codexDir := t.TempDir()
	t.Setenv("CAVEMAN_CLAUDE_ROOT", claudeDir)
	t.Setenv("CAVEMAN_CODEX_ROOT", codexDir)
	if err := os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), []byte("keep config grounded\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeClaudeProject(t, claudeDir, "repo", "a.jsonl", []string{
		`{"type":"assistant","message":{"id":"m1","model":"claude","usage":{"input_tokens":100,"cache_read_input_tokens":100,"cache_creation_input_tokens":100}}}`,
		`{"type":"assistant","message":{"id":"m1","model":"claude","usage":{"input_tokens":100,"cache_read_input_tokens":100,"cache_creation_input_tokens":100}}}`,
		`{"type":"assistant","message":{"id":"m2","model":"claude","usage":{"input_tokens":999}}}`,
	})
	writeClaudeProject(t, claudeDir, "repo", "b.jsonl", []string{
		`{"type":"assistant","message":{"id":"m3","model":"claude","usage":{"input_tokens":100}}}`,
	})
	codexPath := filepath.Join(codexDir, "sessions", "2026", "rollout-c.jsonl")
	if err := os.MkdirAll(filepath.Dir(codexPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codexPath, []byte(strings.Join([]string{
		`{"payload":{"model":"gpt-5.5","info":{"last_token_usage":{"input_tokens":200}}}}`,
		`{"payload":{"model":"gpt-5.5","info":{"last_token_usage":{"input_tokens":999}}}}`,
	}, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := openRetroTestStore(t)
	plan, err := s.BuildLearnPlan(t.TempDir(), []string{"claude", "codex"}, "30d")
	if err != nil {
		t.Fatal(err)
	}
	sink := learnV2Sink(t, plan, "config_tax:baseline")
	if got := sink.Evidence["measured_prefix_tokens"]; got != 200 {
		t.Fatalf("measured prefix = %v, want median 200 from first session turns [300,100,200]", got)
	}
	if got := sink.Evidence["measured_prefix_sessions"]; got != 3 {
		t.Fatalf("measured prefix sessions = %v, want 3", got)
	}
	if got := sink.Evidence["measured_prefix_source"]; got != retroSourceSessionUsage {
		t.Fatalf("measured prefix source = %v", got)
	}
	static := int(sink.TokensPerTurn)
	if got := sink.Evidence["unexplained_prefix_tokens"]; got != max(0, 200-static) {
		t.Fatalf("unexplained prefix = %v, want %d", got, max(0, 200-static))
	}
	if !containsCaveat(plan.Caveats, "The first-message size includes your first prompt") {
		t.Fatalf("measured-prefix caveat missing: %v", plan.Caveats)
	}

	withoutUsage := configSinksWithBehavior(configScan{
		ClaudeMDUser: &ConfigSnapshot{Tokens: 10}, TokenBasis: "o200k",
	}, behaviorScan{}, 1)
	if _, ok := withoutUsage[0].Evidence["measured_prefix_tokens"]; ok {
		t.Fatalf("usage-free config sink emitted measured prefix: %+v", withoutUsage[0].Evidence)
	}
}

func TestLearnV2BehavioralTokensObservedAndDailyEquivalentRanking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join([]string{
		`{"type":"assistant","message":{"id":"a","model":"claude-sonnet-4-6","usage":{"input_tokens":520000}}}`,
		`{"type":"assistant","message":{"id":"b","model":"claude-sonnet-4-6","usage":{"input_tokens":490000}}}`,
	}, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	beh := behaviorScan{SkillUse: map[string]int{}, SessionsBySource: map[string]int{}}
	scanClaudeTranscriptBehavior(path, "repo/session.jsonl", time.Time{}, nil, &beh, newRecurringMiner())
	dumbzone := dumbzoneSink(beh)
	if len(dumbzone) != 1 || dumbzone[0].TokensObserved != 20_000 {
		t.Fatalf("dumbzone sink = %+v, want conservative 20k excess-over-half floor", dumbzone)
	}
	if got := dumbzone[0].Evidence["excess_tokens_observed"]; got != int64(20_000) {
		t.Fatalf("dumbzone evidence = %v", got)
	}

	loop := learningLoopSinks([]learningLoop{{
		Kind: "error_loop", Tool: "Bash", SessionRef: "s", SignatureHash: "h", Calls: 3, OutputTokenFloor: 1_000,
	}})[0]
	if loop.TokensObserved != 1_000 {
		t.Fatalf("learning-loop tokens_observed = %d", loop.TokensObserved)
	}
	if loop.Evidence["tokens_observed_basis"] != "bytes4_estimate" {
		t.Fatalf("learning-loop basis = %+v", loop.Evidence)
	}
	sinks := []Sink{
		{SinkID: "forward", TokensPerDayRate: 150},
		loop,
		{SinkID: "tie-a", TokensPerDayRate: 10},
		{SinkID: "tie-b", TokensPerDayRate: 10},
	}
	rankLearnSinks(sinks, 5)
	if got := []string{sinks[0].SinkID, sinks[1].SinkID, sinks[2].SinkID, sinks[3].SinkID}; strings.Join(got, ",") != "forward,tie-a,tie-b,"+loop.SinkID {
		t.Fatalf("ranked sink ids = %v", got)
	}
	countOnly := subagentSink(behaviorScan{TaskSpawns: 2, SessionsWithTasks: 1, SessionsScanned: 1})[0]
	if countOnly.TokensObserved != 0 {
		t.Fatalf("subagent sink minted tokens_observed = %d", countOnly.TokensObserved)
	}
	raw, err := json.Marshal(countOnly)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "tokens_observed") {
		t.Fatalf("zero tokens_observed was not omitted: %s", raw)
	}
}

func TestFallbackContextWindowKeepsDepthCountsButOmitsTokenFloorAndCrossProviderDepth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fallback.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"assistant","message":{"id":"a","model":"claude-unknown","usage":{"input_tokens":120000}}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	beh := behaviorScan{SkillUse: map[string]int{}, SessionsBySource: map[string]int{}}
	scanClaudeTranscriptBehavior(path, "repo/fallback.jsonl", time.Time{}, nil, &beh, newRecurringMiner())
	if beh.Turns != 1 || beh.DumbzoneTurns != 1 || beh.DumbzoneExcessTokens != 0 || !beh.FallbackWindowSources["claude"] {
		t.Fatalf("fallback-window behavior = %+v", beh)
	}
	sink := dumbzoneSink(beh)[0]
	if sink.TokensObserved != 0 {
		t.Fatalf("fallback window minted token floor: %+v", sink)
	}
	if _, ok := sink.Evidence["excess_tokens_observed"]; ok {
		t.Fatalf("fallback window emitted excess evidence: %+v", sink.Evidence)
	}

	beh.SessionsBySource = map[string]int{"claude": 1, "codex": 1}
	beh.SessionPeakPctBySource = map[string][]int{"claude": {60}, "codex": {20}}
	if sinks := crossProviderSinks(recurringResult{}, beh); len(sinks) != 0 {
		t.Fatalf("fallback source entered cross-provider depth: %+v", sinks)
	}
}

func TestLearnV2StructuredSkillUseRejectsBareSubstring(t *testing.T) {
	slugs := []string{"alpha", "beta", "gamma", "delta"}
	seen := map[string]bool{}
	known := knownSkillSlugs(slugs)
	recordClaudeStructuredSkillUse(map[string]any{
		"type": "assistant",
		"message": map[string]any{"content": []any{
			map[string]any{"type": "tool_use", "name": "Skill", "input": map[string]any{"skill": "alpha", "command": "/beta extra"}},
			map[string]any{"type": "tool_use", "name": "Task", "input": map[string]any{"subagent_type": "delta"}},
		}},
	}, known, seen)
	recordClaudeStructuredSkillUse(map[string]any{
		"type":    "user",
		"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "<command-name>/gamma</command-name>"}}},
	}, known, seen)
	for _, slug := range slugs {
		if !seen[slug] {
			t.Fatalf("structured detector missed %q: %+v", slug, seen)
		}
	}

	path := filepath.Join(t.TempDir(), "guard.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"content":"epsilon appears only as bare text"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	beh := behaviorScan{SkillUse: map[string]int{}, SessionsBySource: map[string]int{}}
	scanClaudeTranscriptBehavior(path, "repo/guard.jsonl", time.Time{}, []string{"epsilon"}, &beh, newRecurringMiner())
	if beh.SkillUse["epsilon"] != 0 {
		t.Fatalf("bare prose must not count as skill use: %+v", beh.SkillUse)
	}
	dead := deadLoadSink(10, []string{"unused"}, behaviorScan{SessionsScanned: 1}, 1)[0]
	if dead.Evidence["detection"] != "structured" {
		t.Fatalf("dead-load detection evidence = %+v", dead.Evidence)
	}
}

func TestLearnV2ConfigSnapshotsUseO200kAndNameBasis(t *testing.T) {
	claudeDir := t.TempDir()
	t.Setenv("CAVEMAN_CLAUDE_ROOT", claudeDir)
	t.Setenv("CAVEMAN_CODEX_ROOT", t.TempDir())
	content := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"
	if err := os.WriteFile(filepath.Join(claudeDir, "CLAUDE.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(claudeDir, "skills", "alpha")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: alpha\ndescription: use alpha carefully\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := scanConfig("")
	if cfg.TokenBasis != "o200k" {
		t.Fatalf("config token basis = %q, want o200k", cfg.TokenBasis)
	}
	want, basis := configTokenCount(content)
	if basis != "o200k" || cfg.ClaudeMDUser == nil || cfg.ClaudeMDUser.Tokens != want {
		t.Fatalf("markdown tokens = %+v, want %d/%s", cfg.ClaudeMDUser, want, basis)
	}
	if cfg.ClaudeMDUser.Tokens == estimateTokens(content) {
		t.Fatalf("fixture did not distinguish o200k count from bytes/4: %d", cfg.ClaudeMDUser.Tokens)
	}
	if fallback, fallbackBasis := configTokenCountWith(nil, content); fallback != estimateTokens(content) || fallbackBasis != "bytes4" {
		t.Fatalf("fallback = %d/%s, want %d/bytes4", fallback, fallbackBasis, estimateTokens(content))
	}
	for _, snap := range cfg.Snapshots {
		var meta map[string]any
		if err := json.Unmarshal([]byte(snap.MetadataJSON), &meta); err != nil {
			t.Fatalf("snapshot metadata %q: %v", snap.MetadataJSON, err)
		}
		if meta["token_basis"] != "o200k" {
			t.Fatalf("snapshot %s metadata = %+v", snap.Kind, meta)
		}
	}
	sink := configSinksWithBehavior(cfg, behaviorScan{}, 1)[0]
	if sink.Evidence["token_basis"] != "o200k" {
		t.Fatalf("config sink token basis = %+v", sink.Evidence)
	}
}

func TestConfigTaxMarksPluginTokensUnmeasured(t *testing.T) {
	sinks := configSinks(configScan{
		SkillDescTokens: 10,
		PluginCount:     2,
		TokenBasis:      "o200k",
	}, 1)
	if len(sinks) == 0 {
		t.Fatal("missing config-tax sink")
	}
	evidence := sinks[0].Evidence
	if value, exists := evidence["plugin_desc_tokens"]; !exists || value != nil {
		t.Fatalf("plugin token value = %#v, want explicit null", value)
	}
	if evidence["plugin_token_measurement"] != "unavailable" || evidence["config_tax_coverage"] != "partial" {
		t.Fatalf("plugin measurement evidence = %+v", evidence)
	}
}

func TestScanSkillsSkipsMissingAndDanglingSkillFiles(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "valid")
	missing := filepath.Join(root, "missing")
	dangling := filepath.Join(root, "dangling")
	for _, dir := range []string{valid, missing, dangling} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(valid, "SKILL.md"), []byte("---\nname: valid\ndescription: real\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "does-not-exist"), filepath.Join(dangling, "SKILL.md")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	got := scanSkills(root)
	if len(got) != 1 || got[0].Name != "valid" {
		t.Fatalf("skills = %+v, want only valid file", got)
	}
}

func TestScanConfigCountsAncestorClaudeFiles(t *testing.T) {
	project := t.TempDir()
	cwd := filepath.Join(project, "nested", "work")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	rootText := "root project instructions\n"
	nearText := "nearest project instructions\n"
	if err := os.WriteFile(filepath.Join(project, "CLAUDE.md"), []byte(rootText), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "CLAUDE.md"), []byte(nearText), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAVEMAN_CLAUDE_ROOT", t.TempDir())
	t.Setenv("CAVEMAN_CODEX_ROOT", t.TempDir())
	cfg := scanConfig(cwd)
	if len(cfg.ClaudeMDProjects) != 2 || cfg.ClaudeMDProject == nil || cfg.ClaudeMDProject.Path != filepath.Join(cwd, "CLAUDE.md") {
		t.Fatalf("project config chain = %+v, nearest=%+v", cfg.ClaudeMDProjects, cfg.ClaudeMDProject)
	}
	wantRoot, _ := configTokenCount(rootText)
	wantNear, _ := configTokenCount(nearText)
	if got := cfg.configTaxPerTurn(); got != wantRoot+wantNear {
		t.Fatalf("config tax = %d, want %d", got, wantRoot+wantNear)
	}
}

func TestLearnV2ConfigSnapshotHistoryAppendsOnlyChangedCounts(t *testing.T) {
	s := openRetroTestStore(t)
	base := ConfigSnapshot{Scope: "project", Path: "/repo/CLAUDE.md", Kind: "claude_md", Lines: 10, Tokens: 20, ObservedAt: "2026-08-16T10:00:00Z", MetadataJSON: `{"token_basis":"o200k"}`}
	if _, err := s.InsertConfigSnapshots([]ConfigSnapshot{base}); err != nil {
		t.Fatal(err)
	}
	unchanged := base
	unchanged.ObservedAt = "2026-08-16T11:00:00Z"
	if _, err := s.InsertConfigSnapshots([]ConfigSnapshot{unchanged}); err != nil {
		t.Fatal(err)
	}
	changed := unchanged
	changed.ObservedAt = "2026-08-16T12:00:00Z"
	changed.Lines = 11
	changed.Tokens = 25
	if _, err := s.InsertConfigSnapshots([]ConfigSnapshot{changed}); err != nil {
		t.Fatal(err)
	}

	var historyRows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM config_snapshot_history WHERE scope=? AND path=? AND kind=?`, base.Scope, base.Path, base.Kind).Scan(&historyRows); err != nil {
		t.Fatal(err)
	}
	if historyRows != 2 {
		t.Fatalf("history rows = %d, want first state plus changed state", historyRows)
	}
	var currentLines, currentTokens int
	if err := s.db.QueryRow(`SELECT lines, tokens FROM config_snapshots WHERE scope=? AND path=? AND kind=?`, base.Scope, base.Path, base.Kind).Scan(&currentLines, &currentTokens); err != nil {
		t.Fatal(err)
	}
	if currentLines != 11 || currentTokens != 25 {
		t.Fatalf("current snapshot = %d lines/%d tokens", currentLines, currentTokens)
	}
}

func learnV2Sink(t *testing.T, plan LearnPlan, id string) Sink {
	t.Helper()
	for _, sink := range plan.Sinks {
		if sink.SinkID == id {
			return sink
		}
	}
	t.Fatalf("sink %q missing from %s", id, sinkIDs(plan))
	return Sink{}
}

func containsCaveat(caveats []string, needle string) bool {
	for _, caveat := range caveats {
		if strings.Contains(caveat, needle) {
			return true
		}
	}
	return false
}

func TestSessionContextPastFallbackWindowInfersLargerWindow(t *testing.T) {
	// An id the catalog cannot know, so the 200k fallback applies.
	scan := func(contexts ...int) behaviorScan {
		t.Helper()
		var lines strings.Builder
		for i, ctx := range contexts {
			fmt.Fprintf(&lines, `{"type":"assistant","message":{"id":"m%d","model":"claude-uncataloged-9","usage":{"input_tokens":%d}}}`+"\n", i, ctx)
		}
		path := filepath.Join(t.TempDir(), "s.jsonl")
		if err := os.WriteFile(path, []byte(lines.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		beh := behaviorScan{SkillUse: map[string]int{}, SessionsBySource: map[string]int{}}
		scanClaudeTranscriptBehavior(path, "repo/s.jsonl", time.Time{}, nil, &beh, newRecurringMiner())
		return beh
	}
	// 300k cannot fit a 200k window, so the whole session is a 1M session:
	// the earlier 150k turn is not dumbzone either.
	big := scan(150_000, 300_000)
	if big.Turns != 2 || big.DumbzoneTurns != 0 || !big.InferredWindowSources["claude"] || len(big.SessionPeakPct) != 1 || big.SessionPeakPct[0] != 30 {
		t.Fatalf("inferred-window behavior = %+v", big)
	}
	claudeDir := t.TempDir()
	t.Setenv("CAVEMAN_CLAUDE_ROOT", claudeDir)
	writeClaudeProject(t, claudeDir, "repo", "a.jsonl", []string{
		`{"type":"assistant","cwd":"/r","timestamp":"2026-09-20T10:00:00Z","message":{"id":"a","model":"claude-uncataloged-9","usage":{"input_tokens":150000}}}`,
		`{"type":"assistant","cwd":"/r","timestamp":"2026-09-20T10:01:00Z","message":{"id":"b","model":"claude-uncataloged-9","usage":{"input_tokens":300000}}}`,
	})
	metrics := scanLearnSessionMetrics(map[string]bool{"claude": true}, time.Time{}, "", false)
	for _, m := range metrics {
		if m.Dumbzone != 0 || m.Turns != 2 || len(metrics) != 1 {
			t.Fatalf("session metric kept the 200k window: %+v", m)
		}
	}
	plan, err := openRetroTestStore(t).BuildLearnPlan(t.TempDir(), []string{"claude"}, "3650d")
	if err != nil {
		t.Fatal(err)
	}
	if !containsCaveat(plan.Caveats, "assumed the next bigger window") {
		t.Fatalf("plan does not say the window was inferred: %v", plan.Caveats)
	}
	small := scan(150_000, 190_000)
	if small.DumbzoneTurns != 2 || small.InferredWindowSources["claude"] || small.SessionPeakPct[0] != 95 {
		t.Fatalf("capped session must stay on the 200k fallback: %+v", small)
	}
}

func TestInferredWindowOnCatalogModelIsNotAFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	body := `{"type":"assistant","message":{"id":"a","model":"claude-sonnet-4-5","usage":{"input_tokens":120000}}}` + "\n" +
		`{"type":"assistant","message":{"id":"b","model":"claude-sonnet-4-5","usage":{"input_tokens":300000}}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	beh := behaviorScan{SkillUse: map[string]int{}, SessionsBySource: map[string]int{}}
	scanClaudeTranscriptBehavior(path, "repo/s.jsonl", time.Time{}, nil, &beh, newRecurringMiner())
	if !beh.InferredWindowSources["claude"] || beh.FallbackWindowSources["claude"] || beh.DumbzoneTurns != 0 || beh.DumbzoneExcessTokens != 0 {
		t.Fatalf("catalog session past its 200k window = %+v", beh)
	}
}

func TestProjectClaudeMDRateCountsOnlyThatProjectsClaudeSessions(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "proj")
	cfg := configScan{ClaudeMDProject: &ConfigSnapshot{Scope: "project", Kind: "claude_md", Path: filepath.Join(project, "CLAUDE.md"), Lines: 400, Tokens: 3000}}
	beh := behaviorScan{Turns: 100, SessionMetrics: []learnSessionMetric{
		{Repo: project, Source: "claude", Turns: 10},
		{Repo: filepath.Join(project, "sub"), Source: "claude", Turns: 5},
		{Repo: project, Source: "codex", Turns: 20},                // Codex reads AGENTS.md, not CLAUDE.md
		{Repo: project + "-worktree", Source: "claude", Turns: 30}, // sibling dir, own CLAUDE.md
		{Repo: filepath.Join(root, "other"), Source: "claude", Turns: 35},
	}}
	projectSink := func(beh behaviorScan) Sink {
		for _, sink := range configSinksWithBehavior(cfg, beh, 50) {
			if sink.SinkID == "claude_md_weight:project" {
				return sink
			}
		}
		t.Fatal("missing claude_md_weight:project")
		return Sink{}
	}
	// 15 of 100 turns at 50 turns/day -> 7.5 turns/day x 3000 tokens.
	sink := projectSink(beh)
	if sink.TokensPerDayRate != 22500 || sink.Evidence["turns_per_day_basis"] != "claude_sessions_under_project" {
		t.Fatalf("rate = %d basis %v, want 22500 claude_sessions_under_project", sink.TokensPerDayRate, sink.Evidence["turns_per_day_basis"])
	}
	beh.SessionMetrics = beh.SessionMetrics[3:]
	if sink := projectSink(beh); sink.TokensPerDayRate != 0 || sink.Evidence["turns_per_day_basis"] != "no_matching_sessions" ||
		!strings.Contains(sink.Suggestion, "could not be measured") {
		t.Fatalf("project with no sessions of its own = %d basis %v suggestion %q", sink.TokensPerDayRate, sink.Evidence["turns_per_day_basis"], sink.Suggestion)
	}
	beh.SessionMetrics = nil
	if sink := projectSink(beh); sink.TokensPerDayRate != 150000 || sink.Evidence["turns_per_day_basis"] != "all_scanned_sessions" {
		t.Fatalf("fallback rate = %d basis %v", sink.TokensPerDayRate, sink.Evidence["turns_per_day_basis"])
	}
}

func TestProjectConfigRateMatchesSessionsThroughSymlinksAndCase(t *testing.T) {
	real := filepath.Join(t.TempDir(), "code", "proj")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "code")
	if err := os.Symlink(filepath.Dir(real), link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	// The config path arrives in $PWD form, through the link; Claude Code
	// records the resolved getcwd path.
	configPath := filepath.Join(link, "proj", "CLAUDE.md")
	beh := behaviorScan{Turns: 100, SessionMetrics: []learnSessionMetric{{Repo: resolved, Source: "claude", Turns: 20}}}
	if got, basis := configTurnsPerDay("project", "claude_md", configPath, beh, 50); got != 10 || basis != "claude_sessions_under_project" {
		t.Fatalf("symlinked config = %v %s, want 10 claude_sessions_under_project", got, basis)
	}
	if runtime.GOOS == "darwin" {
		beh.SessionMetrics[0].Repo = strings.ToUpper(resolved)
		if got, _ := configTurnsPerDay("project", "claude_md", configPath, beh, 50); got != 10 {
			t.Fatalf("case-differing repo on darwin = %v, want 10", got)
		}
	}
}

func TestClaudeProviderModelSplitsOnlyKnownVendorPrefixes(t *testing.T) {
	for in, want := range map[string][2]string{
		"claude-opus-5-5":         {"anthropic", "claude-opus-5-5"},
		"google/gemini-3.7-flash": {"gemini", "gemini-3.7-flash"},
		"openai/gpt-6-sol":        {"openai", "gpt-6-sol"},
		"arn:aws:bedrock:us-east-1:1:application-inference-profile/x": {"anthropic", "arn:aws:bedrock:us-east-1:1:application-inference-profile/x"},
	} {
		if p, m := claudeProviderModel(in); p != want[0] || m != want[1] {
			t.Errorf("claudeProviderModel(%q) = %s/%s, want %s/%s", in, p, m, want[0], want[1])
		}
	}
}

func TestUserClaudeMDAndCodexAgentsRatesCountOnlyTheirOwnSource(t *testing.T) {
	cfg := configScan{
		ClaudeMDUser: &ConfigSnapshot{Scope: "user", Kind: "claude_md", Path: "/home/u/.claude/CLAUDE.md", Lines: 400, Tokens: 3000},
		CodexAgents:  &ConfigSnapshot{Scope: "user", Kind: "agents_md", Path: "/home/u/.codex/AGENTS.md", Lines: 400, Tokens: 3000},
	}
	// 10 of Claude's 60 turns ran in a session with no repo: SessionMetrics
	// drops it, but the user-scope file still loaded there.
	beh := behaviorScan{Turns: 100, TurnsBySource: map[string]int{"claude": 60, "codex": 40}, SessionMetrics: []learnSessionMetric{
		{Repo: "/a", Source: "claude", Turns: 50},
		{Repo: "/b", Source: "codex", Turns: 40},
	}}
	want := map[string][2]any{
		"claude_md_weight:user":  {int64(90000), "claude_sessions"}, // 60/100 x 50/day x 3000
		"claude_md_weight:codex": {int64(60000), "codex_sessions"},
	}
	for _, sink := range configSinksWithBehavior(cfg, beh, 50) {
		w, ok := want[sink.SinkID]
		if !ok {
			continue
		}
		delete(want, sink.SinkID)
		if sink.TokensPerDayRate != w[0] || sink.Evidence["turns_per_day_basis"] != w[1] {
			t.Errorf("%s = %d basis %v, want %v %v", sink.SinkID, sink.TokensPerDayRate, sink.Evidence["turns_per_day_basis"], w[0], w[1])
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing sinks: %v", want)
	}
}

func TestConfigGrowthChargesEachFileAtItsOwnSessionsRate(t *testing.T) {
	project := filepath.Join(t.TempDir(), "proj")
	rows := []configTrendRow{
		{Scope: "project", Kind: "claude_md", Path: filepath.Join(project, "CLAUDE.md"), FirstTokens: 1000, LastTokens: 2000, Observations: 2},
		{Scope: "user", Kind: "agents_md", Path: "/home/u/.codex/AGENTS.md", FirstTokens: 1000, LastTokens: 1500, Observations: 2},
	}
	beh := behaviorScan{Turns: 100, TurnsBySource: map[string]int{"claude": 60, "codex": 40}, SessionMetrics: []learnSessionMetric{
		{Repo: project, Source: "claude", Turns: 10},
		{Repo: "/elsewhere", Source: "claude", Turns: 50},
		{Repo: "/elsewhere", Source: "codex", Turns: 40},
	}}
	sinks := configTrendSink(rows, beh, 50, nil)
	if len(sinks) != 1 {
		t.Fatalf("sinks = %+v", sinks)
	}
	// project: 1000 x (10/100 x 50) = 5000; codex: 500 x (40/100 x 50) = 10000.
	if got := sinks[0].TokensPerDayRate; got != 15000 {
		t.Fatalf("config_growth rate = %d, want 15000 (all-session rate would be 75000)", got)
	}
	if sinks[0].Evidence["turns_per_day_basis"] != "per_file" {
		t.Fatalf("basis = %v", sinks[0].Evidence["turns_per_day_basis"])
	}
}
