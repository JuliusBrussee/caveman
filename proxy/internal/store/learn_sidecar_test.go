package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestWriteLearnSidecarsPersistsZeroMovesAndPrunesToEight(t *testing.T) {
	home := t.TempDir()
	store := &Store{}
	plan := LearnPlan{
		Schema: learnSchema,
		Basis:  learnBasis,
		CaveScore: CaveScore{
			Score: 100,
			Basis: learnBasis,
			Scope: "local_setup",
		},
		Sinks: []Sink{},
	}
	start := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	for day := 0; day < 10; day++ {
		generation := ""
		if day == 9 {
			generation = "scan-generation-10"
		}
		if _, err := store.WriteLearnSidecars(home, plan, start.AddDate(0, 0, day), generation); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(DefaultLearnJSONPath(home))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot LearnSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Moves != 0 {
		t.Fatalf("moves = %d, want 0", snapshot.Moves)
	}
	if snapshot.GeneratedAt != "2026-07-10T12:00:00Z" {
		t.Fatalf("generated_at = %q", snapshot.GeneratedAt)
	}
	if snapshot.Generation != "scan-generation-10" {
		t.Fatalf("generation = %q, want scan-generation-10", snapshot.Generation)
	}
	info, err := os.Stat(DefaultLearnJSONPath(home))
	if err != nil {
		t.Fatal(err)
	}
	// POSIX permission bits are synthetic on Windows; NTFS ACLs govern there.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	history, err := filepath.Glob(filepath.Join(home, "reports", "caveman-learn.????-??-??.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 8 {
		t.Fatalf("history count = %d, want 8", len(history))
	}
	if filepath.Base(history[0]) != "caveman-learn.2026-07-03.json" {
		t.Fatalf("oldest retained = %s", filepath.Base(history[0]))
	}
}
