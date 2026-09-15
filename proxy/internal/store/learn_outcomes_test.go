package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppliedFixRoundTripConfirmsPostFixSessionsAndInsufficientData(t *testing.T) {
	claudeRoot := t.TempDir()
	t.Setenv("CAVEMAN_HOME", t.TempDir())
	t.Setenv("CAVEMAN_CLAUDE_ROOT", claudeRoot)
	t.Setenv("CAVEMAN_CODEX_ROOT", t.TempDir())
	configPath := filepath.Join(claudeRoot, "CLAUDE.md")
	if err := os.WriteFile(configPath, []byte(strings.Repeat("loaded instruction ", 3000)), 0o600); err != nil {
		t.Fatal(err)
	}

	s := openRetroTestStore(t)
	before, err := s.BuildLearnPlan(t.TempDir(), []string{"claude"}, "365d")
	if err != nil {
		t.Fatal(err)
	}
	sink := learnV2Sink(t, before, "claude_md_weight:user")
	if sink.TokensPerTurn <= 0 {
		t.Fatalf("before sink has no config tokens: %+v", sink)
	}

	originalClock := learnOutcomeClock
	t.Cleanup(func() { learnOutcomeClock = originalClock })
	learnOutcomeClock = func() time.Time { return time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC) }
	first, err := s.RecordAppliedFix(before, sink.SinkID, "", "trimmed repeated guidance")
	if err != nil {
		t.Fatal(err)
	}
	if !first.Recorded || first.FixKind != "claude_md_weight" || first.BeforeTokensPerTurn != sink.TokensPerTurn {
		t.Fatalf("first applied record = %+v", first)
	}
	learnOutcomeClock = func() time.Time { return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) }
	if _, err := s.RecordAppliedFix(before, sink.SinkID, "claude_md_weight", "future fixture"); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(configPath, []byte("short config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		writeClaudeProject(t, claudeRoot, "repo", "post-"+string(rune('a'+i))+".jsonl", []string{
			`{"type":"assistant","timestamp":"2026-08-02T12:00:00Z","message":{"id":"m` + string(rune('a'+i)) + `","model":"claude-sonnet-4","usage":{"input_tokens":500}}}`,
		})
	}

	after, err := s.BuildLearnPlan(t.TempDir(), []string{"claude"}, "365d")
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Confirmed) != 2 {
		t.Fatalf("confirmed rows = %+v, want two ledger rows", after.Confirmed)
	}
	if got := after.Confirmed[0]; got.Verdict != "improved" || got.SessionsAfter != 3 || got.After == nil || *got.After >= got.Before {
		t.Fatalf("post-fix confirmation = %+v", got)
	} else if got.SupportingPrefixTokens == nil || got.SupportingPrefixSessions != 3 {
		t.Fatalf("provider prefix support = %+v", got)
	}
	if got := after.Confirmed[1]; got.Verdict != "insufficient_data" || got.SessionsAfter != 0 {
		t.Fatalf("future applied confirmation = %+v", got)
	}
}

func TestRecordAppliedFixPrefersOlderReportSnapshotAndRecoversMissingLiveSink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CAVEMAN_HOME", home)
	s := openRetroTestStore(t)
	beforeSink := Sink{
		SinkID: "claude_md_weight:user", PracticeID: "context-compression",
		Class: classReducible, TokensPerTurn: 1234, Evidence: map[string]any{"path": "/tmp/old-CLAUDE.md"},
	}
	if _, err := s.WriteLearnSidecars(home, LearnPlan{Schema: learnSchema, Basis: learnBasis, Sinks: []Sink{beforeSink}}, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), ""); err != nil {
		t.Fatal(err)
	}
	originalClock := learnOutcomeClock
	t.Cleanup(func() { learnOutcomeClock = originalClock })
	learnOutcomeClock = func() time.Time { return time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC) }

	postFix := LearnPlan{
		Sinks:      []Sink{{SinkID: beforeSink.SinkID, PracticeID: beforeSink.PracticeID, TokensPerTurn: 50, Evidence: map[string]any{"path": "/tmp/new-CLAUDE.md"}}},
		computedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
	}
	record, err := s.RecordAppliedFix(postFix, beforeSink.SinkID, "claude_md_weight", "snapshot precedence")
	if err != nil {
		t.Fatal(err)
	}
	if record.BeforeTokensPerTurn != 1234 || record.BeforeSource != "report_snapshot" || record.BeforeEvidence["before_source"] != "report_snapshot" {
		t.Fatalf("snapshot-backed record = %+v", record)
	}

	missingLive := LearnPlan{computedAt: postFix.computedAt}
	record, err = s.RecordAppliedFix(missingLive, beforeSink.SinkID, "claude_md_weight", "missing live fallback")
	if err != nil {
		t.Fatal(err)
	}
	if record.BeforeTokensPerTurn != 1234 || record.BeforeSource != "report_snapshot" {
		t.Fatalf("missing-live snapshot record = %+v", record)
	}
}

func TestComparableClaudeMDSectionsRequiresEveryRecordedHeading(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CLAUDE.md")
	if err := os.WriteFile(path, []byte("## Renamed\n"+strings.Repeat("current section text ", 20)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := AppliedFixRecord{
		SinkID: "claude_md_sections:project",
		BeforeEvidence: map[string]any{
			"path":     path,
			"sections": []any{map[string]any{"heading": "Original"}},
		},
	}
	if got, ok := comparableConfigTokens(record, configScan{}); ok || got != 0 {
		t.Fatalf("renamed section comparison = %d ok=%v, want insufficient data", got, ok)
	}
}
