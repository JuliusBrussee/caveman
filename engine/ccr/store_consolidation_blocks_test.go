//go:build !js

package ccr

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testMapObject(session string, data []byte) Object {
	return Object{
		Type: ObjectRepositoryMap, Source: "test", SessionID: session,
		TransformVersion: "v1", Currentness: Current, Lifecycle: Warm, Data: data,
	}
}

func testHash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// insertFullCopy adds a full RepositoryMap row directly, so a test controls
// object_id and created_at.
func insertFullCopy(t *testing.T, db *sql.DB, id, session string, created time.Time, data []byte) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO typed_objects (
		 object_id, object_type, content_hash, source, created_at, repository_state,
		 session_id, transform_version, currentness, lifecycle, dependencies_json,
		 original_byte_length, stored_byte_length, data, data_ref
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, ObjectRepositoryMap, testHash(data), "test", created.UTC().Format(time.RFC3339Nano), "",
		session, "v1", Current, Warm, "[]", len(data), len(data), data, "")
	if err != nil {
		t.Fatal(err)
	}
}

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOwnerTieBreakUsesSmallestObjectID(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "ccr.db"))
	data := []byte("same repository map")
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, id := range []string{"owner-c", "owner-a", "owner-b"} {
		insertFullCopy(t, s.db, id, "seed-"+id, created, data)
	}
	id, err := s.PutObject(testMapObject("new-session", data))
	if err != nil {
		t.Fatal(err)
	}
	var ref string
	if err := s.db.QueryRow(`SELECT data_ref FROM typed_objects WHERE object_id=?`, id).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	if ref != "owner-a" {
		t.Fatalf("data_ref = %q, want owner-a", ref)
	}
}

func ownerPlan(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`EXPLAIN QUERY PLAN `+ownerLookupSQL, ObjectRepositoryMap, "sha256:x", "id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, detail)
	}
	return strings.Join(lines, "\n")
}

func checkOwnerPlan(t *testing.T, db *sql.DB) {
	t.Helper()
	plan := ownerPlan(t, db)
	if !strings.Contains(plan, "SEARCH typed_objects USING COVERING INDEX typed_objects_owner") {
		t.Fatalf("owner lookup does not use the owner index:\n%s", plan)
	}
	if strings.Contains(plan, "SCAN typed_objects") || strings.Contains(plan, "TEMP B-TREE") {
		t.Fatalf("owner lookup scans or sorts:\n%s", plan)
	}
}

func TestOwnerLookupUsesIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	s := openTestStore(t, path)
	checkOwnerPlan(t, s.db)

	// A store made before the index existed gets the index on open.
	if _, err := s.db.Exec(`DROP INDEX typed_objects_owner`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, path)
	checkOwnerPlan(t, s.db)
}

func TestOwnerIndexCreatedOnStoreWithoutDataRefColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = old.Exec(`CREATE TABLE typed_objects (
  object_id TEXT PRIMARY KEY, object_type TEXT NOT NULL, content_hash TEXT NOT NULL,
  source TEXT NOT NULL, created_at TEXT NOT NULL, repository_state TEXT NOT NULL,
  session_id TEXT NOT NULL, transform_version TEXT NOT NULL, currentness TEXT NOT NULL,
  lifecycle TEXT NOT NULL, dependencies_json TEXT NOT NULL,
  original_byte_length INTEGER NOT NULL, stored_byte_length INTEGER NOT NULL,
  data BLOB NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	s := openTestStore(t, path)
	checkOwnerPlan(t, s.db)
}

// seedRefChain builds owner A (full copy), B (full copy) and C that points to
// B. It returns the ids and the exact bytes.
func seedRefChain(t *testing.T, s *Store) (a, b, c string, data []byte) {
	t.Helper()
	data = bytes.Repeat([]byte("map-bytes-"), 500)
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	insertFullCopy(t, s.db, "obj-a", "sess-a", created, data)
	insertFullCopy(t, s.db, "obj-b", "sess-b", created.Add(time.Second), data)
	insertFullCopy(t, s.db, "obj-c", "sess-c", created.Add(2*time.Second), data)
	if _, err := s.db.Exec(`UPDATE typed_objects SET data=x'', data_ref='obj-b' WHERE object_id='obj-c'`); err != nil {
		t.Fatal(err)
	}
	return "obj-a", "obj-b", "obj-c", data
}

func TestReferenceToReferenceResolvesThroughFallback(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "ccr.db"))
	a, b, c, data := seedRefChain(t, s)
	if _, err := s.db.Exec(`UPDATE typed_objects SET data=x'', data_ref=? WHERE object_id=?`, a, b); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetObject(c)
	if err != nil || !bytes.Equal(got.Data, data) {
		t.Fatalf("GetObject(C): err=%v len=%d, want %d exact bytes", err, len(got.Data), len(data))
	}
	list, err := s.ListSessionObjects("sess-c", 10)
	if err != nil || len(list) != 1 || !bytes.Equal(list[0].Data, data) {
		t.Fatalf("ListSessionObjects(C): err=%v list=%d", err, len(list))
	}
	// With no valid full copy left, the read must fail as corrupt.
	if _, err := s.db.Exec(`UPDATE typed_objects SET data=x'00' WHERE object_id=?`, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetObject(c); !errors.Is(err, ErrCorruptRecovery) {
		t.Fatalf("GetObject(C) without a valid copy = %v, want ErrCorruptRecovery", err)
	}
	if _, err := s.ListSessionObjects("sess-c", 10); !errors.Is(err, ErrCorruptRecovery) {
		t.Fatalf("ListSessionObjects(C) without a valid copy = %v, want ErrCorruptRecovery", err)
	}
}

func TestFallbackReportsStorageErrorsAsErrors(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "ccr.db"))
	_, _, c, _ := seedRefChain(t, s)
	if _, err := s.db.Exec(`UPDATE typed_objects SET data=x'' WHERE object_id='obj-b'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetObject(c); err == nil || errors.Is(err, ErrCorruptRecovery) {
		t.Fatalf("read on a closed store = %v, want a non-corrupt error", err)
	}
}

// readerSnapshotSetup returns two stores on one file. The first object C points
// to B. The test hook runs a second store's write after the reader read C.
func readerSnapshotSetup(t *testing.T) (reader, writer *Store, data []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ccr.db")
	reader = openTestStore(t, path)
	writer = openTestStore(t, path)
	_, _, _, data = seedRefChain(t, reader)
	fired := false
	testAfterObjectRead = func() {
		if fired {
			return
		}
		fired = true
		// B becomes a reference to A, and A loses its bytes.
		for _, q := range []string{
			`UPDATE typed_objects SET data=x'', data_ref='obj-a' WHERE object_id='obj-b'`,
			`UPDATE typed_objects SET data=x'00' WHERE object_id='obj-a'`,
		} {
			if _, err := writer.db.Exec(q); err != nil {
				t.Error(err)
			}
		}
	}
	t.Cleanup(func() { testAfterObjectRead = nil })
	return reader, writer, data
}

func TestGetObjectReadsOneSnapshot(t *testing.T) {
	reader, _, data := readerSnapshotSetup(t)
	got, err := reader.GetObject("obj-c")
	if err != nil || !bytes.Equal(got.Data, data) {
		t.Fatalf("GetObject: err=%v len=%d, want the snapshot bytes", err, len(got.Data))
	}
	if _, err := reader.GetObject("obj-c"); !errors.Is(err, ErrCorruptRecovery) {
		t.Fatalf("read after the change = %v, want ErrCorruptRecovery", err)
	}
}

func TestListSessionObjectsReadsOneSnapshot(t *testing.T) {
	reader, _, data := readerSnapshotSetup(t)
	got, err := reader.ListSessionObjects("sess-c", 10)
	if err != nil || len(got) != 1 || !bytes.Equal(got[0].Data, data) {
		t.Fatalf("ListSessionObjects: err=%v n=%d, want the snapshot bytes", err, len(got))
	}
}

func TestConcurrentMapPutsNeverChainReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	stores := []*Store{openTestStore(t, path), openTestStore(t, path)}
	const rounds = 40
	payloads := make([][]byte, rounds)
	for i := range payloads {
		payloads[i] = []byte(fmt.Sprintf("repository map %d %s", i, strings.Repeat("x", 2000)))
	}
	var wg sync.WaitGroup
	errs := make(chan error, rounds*2)
	for si, s := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i, data := range payloads {
				if _, err := s.PutObject(testMapObject(fmt.Sprintf("session-%d-%d", si, i), data)); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var chained int
	if err := stores[0].db.QueryRow(`SELECT COUNT(*) FROM typed_objects r
		JOIN typed_objects o ON o.object_id = r.data_ref WHERE r.data_ref != '' AND o.data_ref != ''`).Scan(&chained); err != nil {
		t.Fatal(err)
	}
	if chained != 0 {
		t.Fatalf("%d rows point to a row that is itself a reference", chained)
	}
	var fullCopies int
	if err := stores[0].db.QueryRow(`SELECT COUNT(*) FROM typed_objects WHERE data_ref = ''`).Scan(&fullCopies); err != nil {
		t.Fatal(err)
	}
	if fullCopies != rounds {
		t.Fatalf("full copies = %d, want %d (one per content)", fullCopies, rounds)
	}
	for si := range stores {
		for i, data := range payloads {
			sess := fmt.Sprintf("session-%d-%d", si, i)
			list, err := stores[0].ListSessionObjects(sess, 10)
			if err != nil || len(list) != 1 || !bytes.Equal(list[0].Data, data) {
				t.Fatalf("session %s did not resolve to exact bytes: err=%v n=%d", sess, err, len(list))
			}
		}
	}
}

func TestConcurrentPutsKeepPayloadBudget(t *testing.T) {
	const budget = 600000
	path := filepath.Join(t.TempDir(), "ccr.db")
	open := func() *Store {
		s, err := OpenWithBudget(path, budget)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	stores := []*Store{open(), open()}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []error
	for si, s := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 6; i++ {
				data := bytes.Repeat([]byte{byte('a' + si*6 + i)}, 140000)
				var err error
				if i%2 == 0 {
					_, err = s.Put(Recovery{Original: data, ContentType: "text", Compressor: "t"})
				} else {
					_, err = s.PutObject(Object{Type: ObjectFileObservation, SessionID: "s", Source: fmt.Sprint(si, i), Data: data})
				}
				if err != nil {
					mu.Lock()
					failures = append(failures, err)
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	for _, err := range failures {
		if !errors.Is(err, ErrBudgetExceeded) {
			t.Fatalf("unexpected put error: %v", err)
		}
	}
	if len(failures) == 0 {
		t.Fatal("no put was refused, so the test did not reach the budget")
	}
	used, err := stores[0].storageBytes(stores[0].db)
	if err != nil {
		t.Fatal(err)
	}
	if used > budget {
		t.Fatalf("retained payload %d exceeds budget %d", used, budget)
	}
}
