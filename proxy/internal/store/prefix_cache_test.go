package store

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestPrefixReplacementRoundTripAndDurability pins the property the whole cross-turn
// cache-prefix fix rests on: the same original always resolves to the same stored
// replacement bytes, in this process and in the next one.
func TestPrefixReplacementRoundTripAndDurability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caveman.db")
	original := []byte("the original live-zone block bytes the agent sent")
	replacement := []byte("COMPRESSED\n<<ccr:ccr_deadbeef>>")

	first, err := Open(path, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, _, ok := first.LookupReplacement("unlocked", original); ok {
		t.Fatal("a fresh store must report a miss, not a guess")
	}
	stored, err := first.RememberReplacement("unlocked", original, replacement, "ccr_deadbeef")
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	if !bytes.Equal(stored, replacement) {
		t.Fatalf("stored = %q, want %q", stored, replacement)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// A restart is what the durability claim is about: the replacement must survive
	// the process that created it.
	second, err := Open(path, nil)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer second.Close()
	got, handle, ok := second.LookupReplacement("unlocked", original)
	if !ok {
		t.Fatal("replacement did not survive the restart")
	}
	if !bytes.Equal(got, replacement) || handle != "ccr_deadbeef" {
		t.Fatalf("after restart got %q/%q, want %q/ccr_deadbeef", got, handle, replacement)
	}
	if _, _, ok := second.LookupReplacement("unlocked", []byte("some other block")); ok {
		t.Fatal("an unrelated block must miss")
	}
}

// TestPrefixReplacementFirstWriteWins pins the race rule: once a replacement exists
// for an original, a later writer gets the stored bytes back rather than replacing
// them, so two in-flight requests can never put two different prefixes on the wire.
func TestPrefixReplacementFirstWriteWins(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	original := []byte("one logical message")
	if _, err := s.RememberReplacement("unlocked", original, []byte("FIRST"), "ccr_1"); err != nil {
		t.Fatalf("remember first: %v", err)
	}
	stored, err := s.RememberReplacement("unlocked", original, []byte("SECOND"), "ccr_1")
	if err != nil {
		t.Fatalf("remember second: %v", err)
	}
	if string(stored) != "FIRST" {
		t.Fatalf("stored = %q, want FIRST — the first write is authoritative", stored)
	}
	got, _, ok := s.LookupReplacement("unlocked", original)
	if !ok || string(got) != "FIRST" {
		t.Fatalf("lookup = %q (ok=%v), want FIRST", got, ok)
	}
}

func TestPrefixReplacementIsScopedByLockedTransform(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	original := []byte("same provider-visible bytes")
	firstScope := "locked:history::caveman.engine.json.v1"
	secondScope := "locked:history::caveman.engine.text.v1"
	if _, err := s.RememberReplacement(firstScope, original, []byte("JSON"), "ccr_json"); err != nil {
		t.Fatalf("remember first transform: %v", err)
	}
	if _, _, ok := s.LookupReplacement(secondScope, original); ok {
		t.Fatal("different locked transform reused stale replacement")
	}
	stored, err := s.RememberReplacement(secondScope, original, []byte("TEXT"), "ccr_text")
	if err != nil {
		t.Fatalf("remember second transform: %v", err)
	}
	if string(stored) != "TEXT" {
		t.Fatalf("second transform stored %q", stored)
	}
}

// TestPrefixReplacementIncompleteEntryRejected pins fail-closed storage: an entry
// the store cannot honor later is refused now, so the gateway forwards the original
// instead of sending a rewrite it could not reproduce.
func TestPrefixReplacementIncompleteEntryRejected(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	for _, tc := range []struct {
		name                 string
		original, replacment []byte
		handle               string
	}{
		{"no original", nil, []byte("R"), "ccr_1"},
		{"no replacement", []byte("O"), nil, "ccr_1"},
		{"no handle", []byte("O"), []byte("R"), ""},
	} {
		if _, err := s.RememberReplacement("unlocked", tc.original, tc.replacment, tc.handle); err == nil {
			t.Fatalf("%s: expected an error, got nil", tc.name)
		}
	}
}

// TestPrefixReplacementEviction pins bounded storage: the table never grows past
// prefixCacheMaxEntries. An evicted entry becomes a plain miss, which the gateway
// degrades to forwarding the client's original bytes.
func TestPrefixReplacementEviction(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	// Seed past the cap directly so the test does not have to write 10k blobs.
	for i := 0; i < prefixCacheMaxEntries+5; i++ {
		key := prefixCacheKey("unlocked", []byte{byte(i / 256), byte(i % 256)})
		if _, err := s.db.Exec(
			`INSERT INTO prefix_replacements (original_sha256, handle, replacement, created_at, last_used_at) VALUES (?,?,?,?,?)`,
			key, "ccr_seed", []byte("R"), prefixCacheNow(), prefixCacheNow(),
		); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	if _, err := s.RememberReplacement("unlocked", []byte("newest block"), []byte("NEW"), "ccr_new"); err != nil {
		t.Fatalf("remember: %v", err)
	}

	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM prefix_replacements`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count > prefixCacheMaxEntries {
		t.Fatalf("rows = %d, want <= %d", count, prefixCacheMaxEntries)
	}
	if _, _, ok := s.LookupReplacement("unlocked", []byte("newest block")); !ok {
		t.Fatal("eviction must keep the most recently used entry")
	}
}

// TestPrefixReplacementConcurrentWrites pins the write half under contention. The
// compress path calls RememberReplacement synchronously on the hot path while the
// same database is taking telemetry inserts, so a SQLITE_BUSY here would stop
// compression for that block. Without the DSN's WAL + busy_timeout pragmas this
// fails ~31/32.
func TestPrefixReplacementConcurrentWrites(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	const n = 32
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			original := []byte(fmt.Sprintf("block-%d-%s", i, strings.Repeat("x", 512)))
			_, errs[i] = s.RememberReplacement("unlocked", original, []byte("R"), "ccr_c")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent RememberReplacement %d failed: %v", i, err)
		}
	}
}

// TestPrefixReplacementLookupUnderWriteContention is the decisive one. A lookup
// that errors is reported as a miss, and the gateway then forwards the client's
// ORIGINAL bytes for a frozen block it already compressed — the upstream prefix
// flips, non-deterministically, per turn. Without the DSN pragmas this misses
// ~9/24.
func TestPrefixReplacementLookupUnderWriteContention(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	frozen := []byte("a frozen block compressed on an earlier turn")
	if _, err := s.RememberReplacement("unlocked", frozen, []byte("STABLE-REPLACEMENT"), "ccr_hot"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const readers, writers = 24, 24
	var wg sync.WaitGroup
	var mu sync.Mutex
	misses := 0
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = s.RememberReplacement("unlocked", []byte(fmt.Sprintf("churn-%d", i)), []byte("R"), "ccr_c")
		}(i)
	}
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, ok := s.LookupReplacement("unlocked", frozen); !ok {
				mu.Lock()
				misses++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if misses > 0 {
		t.Fatalf("%d/%d lookups of a STORED replacement reported a miss under contention — each one flips the upstream prefix back to the client's originals", misses, readers)
	}
}
