package store

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	if !containsCaveat(plan.Caveats, "Turn-1 context includes the first user prompt") {
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
