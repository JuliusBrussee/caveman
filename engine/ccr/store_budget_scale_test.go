//go:build !js

package ccr

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"modernc.org/sqlite"
)

// pageFetches reads and resets the store connection's pager counters. Hits
// plus misses is every page the connection fetched: a deterministic stand-in
// for "how much of the database did this statement read", independent of how
// fast the machine is.
func pageFetches(t *testing.T, s *Store) int {
	t.Helper()
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	total := 0
	if err := conn.Raw(func(dc any) error {
		status, ok := dc.(sqlite.DBStatus)
		if !ok {
			return fmt.Errorf("driver connection %T exposes no DBStatus", dc)
		}
		for _, op := range []sqlite.DBStatusOp{sqlite.DBStatusCacheHit, sqlite.DBStatusCacheMiss} {
			current, _, err := status.Status(op, true)
			if err != nil {
				return err
			}
			total += current
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return total
}

// seedRows bulk-loads rows straight into both tables, bypassing the write path
// under test, the way a weeks-old home accumulates them.
func seedRows(t *testing.T, s *Store, objects, recoveries int) {
	t.Helper()
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 600)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for i := 0; i < objects; i++ {
		if _, err := tx.Exec(`INSERT INTO typed_objects (
			 object_id, object_type, content_hash, source, created_at, repository_state,
			 session_id, transform_version, currentness, lifecycle, dependencies_json,
			 original_byte_length, stored_byte_length, data, data_ref
			) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			fmt.Sprintf("ccr_obj_seed_%06d", i), ObjectTaskDecision, "seed", "native:seed", now, "",
			fmt.Sprintf("seed-session-%d", i/100), "seed", Current, Hot, "[]", len(payload), len(payload), payload, ""); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < recoveries; i++ {
		if _, err := tx.Exec(`INSERT INTO recoveries (handle, created_at, content_type, compressor, tokens_before, tokens_after, original, metadata)
			VALUES (?,?,?,?,?,?,?,?)`, fmt.Sprintf("seed_%06d", i), now, "text", "seed", 10, 5, payload, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// Issue #1279: in a weeks-old home every native.sock request came back empty.
// The ccr.db there held ~93k typed objects, and the budget check behind every
// Put and PutObject summed the length of every row in both tables. One store
// connection serves the gateway's compressor and the native runtime, so each
// write held it for a full-table scan (tens of ms at that size, more with a
// cold page cache) and the 250 ms hook budget ran out queued behind them. A
// write must cost the same whatever the store already holds.
func TestWriteBudgetCheckDoesNotScanTheStore(t *testing.T) {
	writeCost := func(t *testing.T, objects int) (put, putObject int) {
		s, err := OpenWithBudget(filepath.Join(t.TempDir(), "ccr.db"), DefaultMaxStorageBytes)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		seedRows(t, s, objects, objects/10)
		pageFetches(t, s)
		if _, err := s.PutObject(Object{Type: ObjectTaskDecision, Source: "native:session.start", SessionID: "fresh",
			TransformVersion: "native-runtime-v1", Currentness: Current, Lifecycle: Hot, Data: []byte(`{"decision_id":"d"}`)}); err != nil {
			t.Fatal(err)
		}
		putObject = pageFetches(t, s)
		if _, err := s.Put(Recovery{ContentType: "text", Compressor: "test", TokensBefore: 10, TokensAfter: 5, Original: []byte("fresh original")}); err != nil {
			t.Fatal(err)
		}
		put = pageFetches(t, s)
		return put, putObject
	}
	smallPut, smallObject := writeCost(t, 200)
	largePut, largeObject := writeCost(t, 20000)
	t.Logf("page fetches: Put %d -> %d, PutObject %d -> %d (200 -> 20000 seeded rows)", smallPut, largePut, smallObject, largeObject)
	// A B-tree insert grows with the tree's depth, a few pages; a scan grows
	// with its size, thousands here. Allow the depth, not the size.
	if largeObject > smallObject+32 {
		t.Errorf("PutObject read %d pages with 20000 rows stored vs %d with 200: the write scans the store", largeObject, smallObject)
	}
	if largePut > smallPut+32 {
		t.Errorf("Put read %d pages with 20000 rows stored vs %d with 200: the write scans the store", largePut, smallPut)
	}
}

func scannedUsage(t *testing.T, s *Store) int64 {
	t.Helper()
	var used int64
	if err := s.db.QueryRow(`SELECT ` + storageUsageScan).Scan(&used); err != nil {
		t.Fatal(err)
	}
	return used
}

func countedUsage(t *testing.T, s *Store) int64 {
	t.Helper()
	used, err := s.storageBytes(s.db)
	if err != nil {
		t.Fatal(err)
	}
	return used
}

// The counter must equal the scan it replaced after every kind of write: an
// insert, an upsert, a dedup pointer, a raw UPDATE and DELETE, and a write by
// another process that knows nothing of the counter (a pre-upgrade binary).
func TestStorageUsageCounterMatchesTheScan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	// A store from before the counter existed: rows, no counter.
	s, err := OpenWithBudget(path, DefaultMaxStorageBytes)
	if err != nil {
		t.Fatal(err)
	}
	seedRows(t, s, 300, 30)
	for _, name := range storageUsageNames[1:] {
		if _, err := s.db.Exec(`DROP TRIGGER ` + name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`DROP TABLE ccr_storage_usage`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO recoveries (handle, created_at, content_type, compressor, tokens_before, tokens_after, original, metadata)
		VALUES ('pre_upgrade', 'x', 'text', 'old', 1, 1, ?, ?)`, []byte("written before the upgrade"), []byte("meta")); err != nil {
		t.Fatal(err)
	}
	if got, want := countedUsage(t, s), scannedUsage(t, s); got != want {
		t.Fatalf("without a counter row storageBytes = %d, want the scan's %d", got, want)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = OpenWithBudget(path, DefaultMaxStorageBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	check := func(step string) {
		t.Helper()
		if got, want := countedUsage(t, s), scannedUsage(t, s); got != want {
			t.Fatalf("after %s: counter = %d, scan = %d", step, got, want)
		}
	}
	check("the upgrade's seed")

	handle, err := s.Put(Recovery{ContentType: "text", Compressor: "test", TokensBefore: 10, TokensAfter: 5, Original: []byte("an original")})
	if err != nil {
		t.Fatal(err)
	}
	check("Put")
	if _, err := s.Put(Recovery{ContentType: "text", Compressor: "test", TokensBefore: 10, TokensAfter: 5, Original: []byte("an original"), Metadata: []byte("late metadata")}); err != nil {
		t.Fatal(err)
	}
	check("an upsert that adds metadata")
	repoMap := []byte(`{"files":["a.go","b.go"]}`)
	for _, session := range []string{"one", "two"} {
		if _, err := s.PutObject(Object{Type: ObjectRepositoryMap, Source: "native:repository", SessionID: session,
			TransformVersion: "v1", Currentness: Current, Lifecycle: Warm, Data: repoMap}); err != nil {
			t.Fatal(err)
		}
	}
	check("a repository map and its dedup pointer")
	id, err := s.PutObject(Object{Type: ObjectTaskDecision, Source: "native:x", SessionID: "one",
		TransformVersion: "v1", Currentness: Current, Lifecycle: Hot, Data: []byte(`{"decision_id":"d"}`), Dependencies: []string{"dep"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetObjectLifecycle(id, Cold); err != nil {
		t.Fatal(err)
	}
	check("PutObject and a lifecycle change")
	if _, err := s.db.Exec(`UPDATE typed_objects SET data = ? WHERE object_id = ?`, []byte("a much longer replacement payload"), id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM recoveries WHERE handle = ?`, handle); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM typed_objects WHERE object_id LIKE 'ccr_obj_seed_00001%'`); err != nil {
		t.Fatal(err)
	}
	check("raw UPDATE and DELETE")

	other, err := sql.Open("sqlite", SQLiteDSN(s.path)+"&mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec(`INSERT INTO recoveries (handle, created_at, content_type, compressor, tokens_before, tokens_after, original, metadata)
		VALUES ('other_process', 'x', 'text', 'old', 1, 1, ?, NULL)`, []byte("another process wrote this")); err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	check("another process's write")
}
