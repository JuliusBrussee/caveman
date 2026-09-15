package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func trendStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func snapshotAt(path string, tokens, lines int, at string) ConfigSnapshot {
	return ConfigSnapshot{
		Scope: "project", Path: path, Kind: "claude_md",
		Lines: lines, Tokens: tokens, ObservedAt: at,
	}
}

// TestConfigTrendComparesAgainstTheUsersOwnHistory proves the finding is
// growth measured against this machine's recorded past, not a static guideline.
func TestConfigTrendComparesAgainstTheUsersOwnHistory(t *testing.T) {
	store := trendStore(t)
	if _, err := store.InsertConfigSnapshots([]ConfigSnapshot{
		snapshotAt("/repo/CLAUDE.md", 1200, 300, "2026-07-14T00:00:00Z"),
	}); err != nil {
		t.Fatalf("insert first: %v", err)
	}
	if _, err := store.InsertConfigSnapshots([]ConfigSnapshot{
		snapshotAt("/repo/CLAUDE.md", 1900, 460, "2026-08-14T00:00:00Z"),
	}); err != nil {
		t.Fatalf("insert second: %v", err)
	}
	rows, err := store.configTrendRows(time.Time{})
	if err != nil {
		t.Fatalf("trend rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one tracked file, got %d: %+v", len(rows), rows)
	}
	if rows[0].FirstTokens != 1200 || rows[0].LastTokens != 1900 {
		t.Fatalf("endpoints = %d -> %d, want 1200 -> 1900", rows[0].FirstTokens, rows[0].LastTokens)
	}

	sinks := configTrendSink(rows, 10, &LearnSpend{EffectiveInputUSDPerMTok: 3.0})
	if len(sinks) != 1 {
		t.Fatalf("expected a growth sink, got %d", len(sinks))
	}
	sink := sinks[0]
	if sink.TokensPerTurn != 700 {
		t.Fatalf("growth per turn = %d, want 700 (the DELTA, not the size)", sink.TokensPerTurn)
	}
	files, _ := sink.Evidence["files"].([]map[string]any)
	if len(files) != 1 || files[0]["path"] != "/repo/CLAUDE.md" {
		t.Fatalf("evidence must name the file that grew: %+v", files)
	}
	if pct, _ := files[0]["growth_pct"].(float64); pct < 58 || pct > 59 {
		t.Fatalf("growth pct = %v, want ~58.3", pct)
	}
	comparison, _ := sink.Evidence["comparison"].(string)
	if !strings.Contains(comparison, "own recorded config history") {
		t.Fatalf("the finding must name its baseline: %q", comparison)
	}
	if _, priced := sink.Evidence["spend_usd_per_day"]; !priced {
		t.Fatalf("a priced window must price the growth: %+v", sink.Evidence)
	}
}

// TestConfigTrendSilentWithoutHistory refuses to report a trend from one
// observation, which would be a flat line nobody measured.
func TestConfigTrendSilentWithoutHistory(t *testing.T) {
	store := trendStore(t)
	if _, err := store.InsertConfigSnapshots([]ConfigSnapshot{
		snapshotAt("/repo/CLAUDE.md", 4000, 900, "2026-08-14T00:00:00Z"),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	rows, err := store.configTrendRows(time.Time{})
	if err != nil {
		t.Fatalf("trend rows: %v", err)
	}
	if got := configTrendSink(rows, 10, nil); len(got) != 0 {
		t.Fatalf("a single observation is not a trend: %+v", got)
	}
}

// TestConfigTrendIgnoresDriftAndShrinkage keeps noise and improvements out of a
// finding whose whole subject is unwanted growth.
func TestConfigTrendIgnoresDriftAndShrinkage(t *testing.T) {
	drift := []configTrendRow{{
		Scope: "project", Path: "/repo/CLAUDE.md", Kind: "claude_md",
		FirstTokens: 2000, LastTokens: 2100, Observations: 2,
	}}
	if got := configTrendSink(drift, 10, nil); len(got) != 0 {
		t.Fatalf("5%% drift must not be a finding: %+v", got)
	}
	shrunk := []configTrendRow{{
		Scope: "project", Path: "/repo/CLAUDE.md", Kind: "claude_md",
		FirstTokens: 4000, LastTokens: 1200, Observations: 2,
	}}
	if got := configTrendSink(shrunk, 10, nil); len(got) != 0 {
		t.Fatalf("a file that shrank must not be reported as growth: %+v", got)
	}
}
