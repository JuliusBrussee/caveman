package mem

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(Options{InMemory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRememberIsIdempotentAndDurable(t *testing.T) {
	s := newStore(t)
	m1, err := s.Remember("the deploy key lives in vault under ops/deploy")
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	m2, err := s.Remember("the deploy key lives in vault under ops/deploy")
	if err != nil {
		t.Fatalf("remember again: %v", err)
	}
	if m1.ID != m2.ID {
		t.Errorf("identical text must yield the same id: %s vs %s", m1.ID, m2.ID)
	}
	if m1.CreatedAt != m2.CreatedAt {
		t.Errorf("repeat remember must keep the original created_at")
	}
}

func TestRememberRejectsExpiredHistoricalTextInsteadOfReportingFalseSuccess(t *testing.T) {
	s := newStore(t)
	old, err := s.Remember("deployment region is eu-west-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Supersede(old.ID, "deployment region is eu-central-1"); err != nil {
		t.Fatal(err)
	}

	remembered, err := s.Remember(old.Text)
	if err == nil || !strings.Contains(err.Error(), "expired historical version") {
		t.Fatalf("re-remember expired memory = %+v, %v; want explicit lifecycle error", remembered, err)
	}
	hits, err := s.Recall("eu-west-1 deployment region", RecallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range hits {
		if hit.ID == old.ID {
			t.Fatalf("rejected re-remember resurrected expired memory: %+v", hits)
		}
	}
}

func TestRememberRejectsEmpty(t *testing.T) {
	s := newStore(t)
	if _, err := s.Remember("   "); err == nil {
		t.Fatal("expected an error remembering empty text")
	}
}

func TestCount(t *testing.T) {
	s := newStore(t)
	if got, err := s.Count(); err != nil || got != 0 {
		t.Fatalf("empty Count() = %d, %v; want 0, nil", got, err)
	}
	if _, err := s.Remember("first"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remember("second"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remember("first"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Count(); err != nil || got != 2 {
		t.Fatalf("Count() = %d, %v; want 2, nil", got, err)
	}
}

func TestRecallReturnsRelevantAboveThreshold(t *testing.T) {
	s := newStore(t)
	s.Remember("kubernetes ingress is configured with nginx and cert-manager")
	s.Remember("the quarterly budget review happens every march")
	s.Remember("favourite pizza topping is mushroom and basil")

	hits, err := s.Recall("how is kubernetes ingress configured", RecallOptions{})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one hit for a strongly matching query")
	}
	if !strings.Contains(hits[0].Text, "kubernetes") {
		t.Errorf("top hit should be the kubernetes memory, got %q", hits[0].Text)
	}
	if hits[0].Basis != "inferred" {
		t.Errorf("basis = %q, want inferred", hits[0].Basis)
	}
	if hits[0].Score <= 0 {
		t.Errorf("a hit must carry a positive score, got %v", hits[0].Score)
	}
}

func TestRecallFailsTowardNothing(t *testing.T) {
	s := newStore(t)
	s.Remember("kubernetes ingress is configured with nginx")
	s.Remember("the quarterly budget review happens every march")

	// A query with zero term overlap must recall nothing — never a guess.
	hits, err := s.Recall("photosynthesis chlorophyll wavelength", RecallOptions{})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("off-topic query must recall nothing, got %d hits", len(hits))
	}
}

func TestRecallThresholdGates(t *testing.T) {
	s := newStore(t)
	s.Remember("kubernetes ingress is configured with nginx")

	// An impossibly high threshold filters even a real match (fail toward nothing).
	hits, err := s.Recall("kubernetes ingress", RecallOptions{Threshold: 1000})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("high threshold must gate all hits, got %d", len(hits))
	}
}

func TestRecallByteSafeRoundTrip(t *testing.T) {
	s := newStore(t)
	// A repetitive JSON memory compresses (S4) and yields a recovery handle.
	big := `{"runbook":[` + strings.Repeat(`{"step":"restart","svc":"api"},`, 40) + `{"step":"restart","svc":"api"}]}`
	m, err := s.Remember(big)
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	hits, err := s.Recall("runbook restart api step", RecallOptions{})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	var hit *Hit
	for i := range hits {
		if hits[i].ID == m.ID {
			hit = &hits[i]
		}
	}
	if hit == nil {
		t.Fatal("expected the runbook memory in the recall")
	}
	if hit.RecoveryHandle == "" {
		t.Fatalf("a compressible memory should expose a recovery handle (ratio note: tokens_added=%d)", hit.TokensAdded)
	}
	original, err := s.Recover(hit.RecoveryHandle)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if string(original) != big {
		t.Error("recovered memory is not byte-identical to the original")
	}
}

func TestForget(t *testing.T) {
	s := newStore(t)
	m, _ := s.Remember("ephemeral note about the staging cluster")
	ok, err := s.Forget(m.ID)
	if err != nil || !ok {
		t.Fatalf("forget should remove the memory: ok=%v err=%v", ok, err)
	}
	again, _ := s.Forget(m.ID)
	if again {
		t.Error("forgetting an absent id must report false")
	}
	hits, _ := s.Recall("staging cluster note", RecallOptions{})
	if len(hits) != 0 {
		t.Error("a forgotten memory must not be recalled")
	}
}

func TestForgetRepairsHistoryWithoutResurrectingExpiredMemory(t *testing.T) {
	s := newStore(t)
	first, _ := s.Remember("service region version one")
	middle, err := s.Supersede(first.ID, "service region version two")
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Supersede(middle.ID, "service region version three")
	if err != nil {
		t.Fatal(err)
	}

	if ok, err := s.Forget(middle.ID); err != nil || !ok {
		t.Fatalf("forget middle: ok=%v err=%v", ok, err)
	}
	history, err := s.History(current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].ID != first.ID || history[1].ID != current.ID {
		t.Fatalf("repaired history = %+v", history)
	}
	if history[0].SupersededBy != current.ID || history[1].Supersedes != first.ID {
		t.Fatalf("neighbors were not relinked: %+v", history)
	}

	if ok, err := s.Forget(current.ID); err != nil || !ok {
		t.Fatalf("forget current: ok=%v err=%v", ok, err)
	}
	if count, err := s.Count(); err != nil || count != 0 {
		t.Fatalf("forget current must not resurrect predecessor: count=%d err=%v", count, err)
	}
	history, err = s.History(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].ID != first.ID || history[0].ValidUntil == nil {
		t.Fatalf("expired predecessor changed after forgetting current: %+v", history)
	}
}

func TestSupersedeExpiresOldMemoryAndRecallUsesCurrentVersion(t *testing.T) {
	s := newStore(t)
	old, err := s.Remember("deployment region is eu-west-1")
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Supersede(old.ID, "deployment region is eu-central-1")
	if err != nil {
		t.Fatalf("supersede: %v", err)
	}
	if current.Supersedes != old.ID {
		t.Fatalf("replacement lineage = %+v, want supersedes %s", current, old.ID)
	}
	if count, err := s.Count(); err != nil || count != 1 {
		t.Fatalf("current Count() = %d, %v; want 1, nil", count, err)
	}

	oldHits, err := s.Recall("eu-west-1 deployment region", RecallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range oldHits {
		if hit.ID == old.ID {
			t.Fatalf("superseded memory must not be recalled, got %+v", oldHits)
		}
	}
	newHits, err := s.Recall("eu-central-1 deployment region", RecallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(newHits) != 1 || newHits[0].ID != current.ID {
		t.Fatalf("current memory recall = %+v, want %s", newHits, current.ID)
	}

	history, err := s.History(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].ID != old.ID || history[1].ID != current.ID {
		t.Fatalf("history from old = %+v", history)
	}
	if history[0].ValidUntil == nil || history[0].SupersededBy != current.ID {
		t.Fatalf("old version was not expired: %+v", history[0])
	}
	history, err = s.History(current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].ID != old.ID || history[1].ID != current.ID {
		t.Fatalf("history from current = %+v", history)
	}
}

func TestSupersedeFailsClosedWithoutCorruptingLineage(t *testing.T) {
	s := newStore(t)
	old, _ := s.Remember("database host is db-old.internal")
	current, err := s.Supersede(old.ID, "database host is db-new.internal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Supersede(old.ID, "database host is db-third.internal"); err == nil {
		t.Fatal("expired memory must not be superseded again")
	}
	if _, err := s.Supersede(current.ID, current.Text); err == nil {
		t.Fatal("identical replacement must be rejected")
	}
	history, err := s.History(current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("failed supersedes changed lineage: %+v", history)
	}
}

func TestSupersedeRejectsOversizedReplacementWithoutExpiringCurrent(t *testing.T) {
	s := newStore(t)
	current, err := s.Remember("database host is db-current.internal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Supersede(current.ID, strings.Repeat("x", MaxMemoryBytes+1)); !errors.Is(err, ErrMemoryTooLarge) {
		t.Fatalf("oversized supersede error = %v, want ErrMemoryTooLarge", err)
	}
	stored, err := s.memoryByID(current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ValidUntil != nil || stored.SupersededBy != "" {
		t.Fatalf("rejected replacement expired current memory: %+v", stored)
	}
	if count, err := s.Count(); err != nil || count != 1 {
		t.Fatalf("current count after rejected replacement = %d, %v; want 1, nil", count, err)
	}
}

func TestOpenMigratesLegacyMemorySchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mem.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE memories (
		  id TEXT PRIMARY KEY,
		  text TEXT NOT NULL,
		  created_at TEXT NOT NULL
		);
		INSERT INTO memories (id, text, created_at)
		VALUES ('mem_legacy', 'legacy deployment fact', '2026-01-02T03:04:05Z');
	`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(Options{Dir: dir})
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	defer s.Close()
	history, err := s.History("mem_legacy")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].ValidFrom != "2026-01-02T03:04:05Z" || history[0].ValidUntil != nil {
		t.Fatalf("legacy memory migration = %+v", history)
	}
	hits, err := s.Recall("legacy deployment fact", RecallOptions{})
	if err != nil || len(hits) != 1 {
		t.Fatalf("migrated memory recall = %+v, %v", hits, err)
	}
}
