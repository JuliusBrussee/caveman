//go:build !js

package ccr

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// insertObjectRow adds a typed object row directly, so a test controls its
// id, session, lifecycle, age and data_ref. A reference row stores no bytes.
func insertObjectRow(db *sql.DB, id string, typ ObjectType, session string, lifecycle Lifecycle, created time.Time, data []byte, dataRef string) error {
	stored := data
	if dataRef != "" {
		stored = []byte{}
	}
	_, err := db.Exec(
		`INSERT INTO typed_objects (
		 object_id, object_type, content_hash, source, created_at, repository_state,
		 session_id, transform_version, currentness, lifecycle, dependencies_json,
		 original_byte_length, stored_byte_length, data, data_ref
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, typ, testHash(data), "test", created.UTC().Format(time.RFC3339Nano), "state-"+id,
		session, "v1", Current, lifecycle, "[]", len(data), len(data), stored, dataRef)
	return err
}

func mustInsertObjectRow(t *testing.T, db *sql.DB, id string, typ ObjectType, session string, lifecycle Lifecycle, created time.Time, data []byte, dataRef string) {
	t.Helper()
	if err := insertObjectRow(db, id, typ, session, lifecycle, created, data, dataRef); err != nil {
		t.Fatal(err)
	}
}

// fillWithObjects inserts rows from next until the page cap refuses one, and
// returns the ids that went in.
func fillWithObjects(t *testing.T, db *sql.DB, prefix string, session string, lifecycle Lifecycle, created time.Time, size int) []string {
	t.Helper()
	var ids []string
	for i := 0; i < 100000; i++ {
		id := fmt.Sprintf("ccr_obj_%s_%05d", prefix, i)
		data := append(bytes.Repeat([]byte{byte('a' + i%26)}, size), id...)
		err := insertObjectRow(db, id, ObjectFileObservation, session, lifecycle, created.Add(time.Duration(i)*time.Second), data, "")
		if isFull(err) {
			return ids
		}
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	t.Fatal("store never filled")
	return nil
}

func objectData(id string, size int, i int) []byte {
	return append(bytes.Repeat([]byte{byte('a' + i%26)}, size), id...)
}

func TestFullStoreExpiresEndedSessionsAndAcceptsNewRecovery(t *testing.T) {
	store, err := OpenWithBudget(filepath.Join(t.TempDir(), "ccr.db"), 512<<10)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var logs bytes.Buffer
	store.SetLogger(slog.New(slog.NewJSONHandler(&logs, nil)))
	now := time.Now().UTC()
	// A live session the policy must never touch.
	liveData := bytes.Repeat([]byte("live context "), 400)
	liveID, err := store.PutObject(Object{Type: ObjectCommandResult, SessionID: "live", Source: "test", Data: liveData})
	if err != nil {
		t.Fatal(err)
	}
	// A long session that ended two days ago: its early objects are old, but
	// the session itself is not, so it stays recoverable.
	longData := []byte("first object of a long session")
	mustInsertObjectRow(t, store.db, "ccr_obj_long_early", ObjectFileObservation, "long", LifecycleArchived, now.Add(-25*24*time.Hour), longData, "")
	mustInsertObjectRow(t, store.db, "ccr_obj_long_late", ObjectFileObservation, "long", LifecycleArchived, now.Add(-2*24*time.Hour), []byte("late"), "")
	// A session that ended three weeks ago fills the rest of the store.
	ended := fillWithObjects(t, store.db, "ended", "ended", LifecycleArchived, now.Add(-21*24*time.Hour), 6000)
	if len(ended) < 10 {
		t.Fatalf("filled with only %d objects", len(ended))
	}

	original := bytes.Repeat([]byte("tool output that compresses "), 1000)
	handle, err := store.Put(Recovery{ContentType: "text", Compressor: "test", Original: original})
	if err != nil {
		t.Fatalf("put into a full store with expirable objects: %v", err)
	}
	if got, err := store.Get(handle); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("new recovery: %v", err)
	}
	stats, err := store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if stats.StorageFull {
		t.Fatalf("store still full after reclaim: %q", stats.StorageFullReason)
	}
	if got, err := store.GetObject(liveID); err != nil || !bytes.Equal(got.Data, liveData) {
		t.Fatalf("live object: %v", err)
	}
	if got, err := store.GetObject("ccr_obj_long_early"); err != nil || !bytes.Equal(got.Data, longData) {
		t.Fatalf("early object of a recently ended session: %v", err)
	}
	// The oldest objects go first, and each answers "expired".
	_, err = store.GetObject(ended[0])
	if !errors.Is(err, ErrExpired) || !errors.Is(err, ErrNotFound) {
		t.Fatalf("oldest ended object: %v, want ErrExpired wrapping ErrNotFound", err)
	}
	// Nothing is deleted without a log line and a record status can read.
	if !strings.Contains(logs.String(), "expired old typed objects") || !strings.Contains(logs.String(), `"sessions":1`) {
		t.Fatalf("expiry was not logged:\n%s", logs.String())
	}
	var recorded string
	if err := store.db.QueryRow(`SELECT value FROM ccr_meta WHERE key = 'last_reclaim'`).Scan(&recorded); err != nil || !strings.Contains(recorded, `"expired_objects"`) {
		t.Fatalf("last reclaim not recorded: %q, %v", recorded, err)
	}
	// The reclaim stops at its target, so the newest ended objects stay.
	last := ended[len(ended)-1]
	if got, err := store.GetObject(last); err != nil || !bytes.Equal(got.Data, objectData(last, 6000, len(ended)-1)) {
		t.Fatalf("newest ended object was expired too or changed: %v", err)
	}
}

func TestIdleSessionExpiresOnlyAfterIdleRetention(t *testing.T) {
	store, err := OpenWithBudget(filepath.Join(t.TempDir(), "ccr.db"), 512<<10)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	// Never-ended sessions keep hot objects. 40 days idle may expire, 20 not.
	stale := fillWithObjects(t, store.db, "stale", "idle-40", Hot, now.Add(-40*24*time.Hour), 6000)
	// Shrink the stale session to a few objects so the reclaim cannot reach
	// its target without touching the 20-day session, which it must not do.
	if _, err := store.db.Exec(`DELETE FROM typed_objects WHERE session_id = 'idle-40' AND object_id NOT IN (?, ?)`, stale[0], stale[1]); err != nil {
		t.Fatal(err)
	}
	recent := fillWithObjects(t, store.db, "recent", "idle-20", Warm, now.Add(-20*24*time.Hour), 6000)

	// Larger than the two stale objects can free, so the reclaim runs out of
	// candidates rather than reaching its target.
	_, err = store.Put(Recovery{ContentType: "text", Compressor: "test", Original: bytes.Repeat([]byte("x"), 64<<10)})
	if err != nil && !errors.Is(err, ErrBudgetExceeded) {
		t.Fatal(err)
	}
	for _, id := range stale[:2] {
		if _, err := store.GetObject(id); !errors.Is(err, ErrExpired) {
			t.Fatalf("object of a 40-day idle session: %v, want ErrExpired", err)
		}
	}
	for i, id := range recent {
		got, err := store.GetObject(id)
		if err != nil || !bytes.Equal(got.Data, objectData(id, 6000, i)) {
			t.Fatalf("object of a 20-day idle session %s: %v", id, err)
		}
	}
}

func TestFullStoreWithNothingExpirableStaysRefusedAndKeepsEverything(t *testing.T) {
	store, err := OpenWithBudget(filepath.Join(t.TempDir(), "ccr.db"), 256<<10)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ids := fillWithObjects(t, store.db, "fresh", "ended-yesterday", LifecycleArchived, time.Now().Add(-24*time.Hour), 6000)
	_, err = store.Put(Recovery{ContentType: "text", Compressor: "test", Original: bytes.Repeat([]byte("y"), 64<<10)})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("put = %v, want ErrBudgetExceeded", err)
	}
	for i, id := range ids {
		got, err := store.GetObject(id)
		if err != nil || !bytes.Equal(got.Data, objectData(id, 6000, i)) {
			t.Fatalf("object %s inside the retention floor: %v", id, err)
		}
	}
	stats, err := store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if !stats.StorageFull || stats.StorageFullReason != StorageFullPageCap {
		t.Fatalf("full = %v, reason = %q, want page_cap", stats.StorageFull, stats.StorageFullReason)
	}
}

func TestExpiryKeepsAnOwnerThatAKeptReferenceReads(t *testing.T) {
	store, err := OpenWithBudget(filepath.Join(t.TempDir(), "ccr.db"), 512<<10)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	old := now.Add(-60 * 24 * time.Hour)
	shared := bytes.Repeat([]byte("repository map shared with a live session "), 300)
	alone := bytes.Repeat([]byte("repository map only old sessions read "), 300)
	// owner-shared is old, but a live session's map reads its bytes.
	mustInsertObjectRow(t, store.db, "ccr_obj_owner_shared", ObjectRepositoryMap, "ancient", LifecycleArchived, old, shared, "")
	mustInsertObjectRow(t, store.db, "ccr_obj_live_ref", ObjectRepositoryMap, "live", Warm, now, shared, "ccr_obj_owner_shared")
	// owner-alone and its only reader are both old: both may go.
	mustInsertObjectRow(t, store.db, "ccr_obj_owner_alone", ObjectRepositoryMap, "ancient", LifecycleArchived, old, alone, "")
	mustInsertObjectRow(t, store.db, "ccr_obj_old_ref", ObjectRepositoryMap, "ancient-2", LifecycleArchived, old.Add(time.Hour), alone, "ccr_obj_owner_alone")
	fillWithObjects(t, store.db, "ancient", "ancient-3", LifecycleArchived, old.Add(2*time.Hour), 6000)

	if _, err := store.Put(Recovery{ContentType: "text", Compressor: "test", Original: bytes.Repeat([]byte("z"), 32<<10)}); err != nil {
		t.Fatalf("put after reclaim: %v", err)
	}
	got, err := store.GetObject("ccr_obj_live_ref")
	if err != nil || !bytes.Equal(got.Data, shared) {
		t.Fatalf("live reference lost its owner's bytes: %v", err)
	}
	if _, err := store.GetObject("ccr_obj_owner_shared"); err != nil {
		t.Fatalf("owner a kept reference reads was expired: %v", err)
	}
	for _, id := range []string{"ccr_obj_owner_alone", "ccr_obj_old_ref"} {
		if _, err := store.GetObject(id); !errors.Is(err, ErrExpired) {
			t.Fatalf("%s: %v, want ErrExpired", id, err)
		}
	}
	// The live session lists cleanly: no reference dangles.
	if _, err := store.ListSessionObjects("live", 100); err != nil {
		t.Fatalf("list live session: %v", err)
	}
}

// Before #1023 every session stored its own full copy of an unchanged map.
// A store full of those copies now consolidates them on the first refused
// write, keeps every handle byte-exact and accepts the write.
func TestFullStoreConsolidatesDuplicateRepositoryMaps(t *testing.T) {
	store, err := OpenWithBudget(filepath.Join(t.TempDir(), "ccr.db"), 256<<10)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	data := bytes.Repeat([]byte("m"), 4*4096)
	// copy-00 is referenced by a row stored after #1023, so it must stay a
	// full copy; every other copy is free to become a reference.
	var ids []string
	for i := 0; ; i++ {
		id := fmt.Sprintf("ccr_obj_dup_%02d", i)
		err := insertObjectRow(store.db, id, ObjectRepositoryMap, fmt.Sprintf("s%02d", i), Warm, now.Add(time.Duration(i)*time.Second), data, "")
		if isFull(err) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		if i == 1 {
			mustInsertObjectRow(t, store.db, "ccr_obj_ref_to_01", ObjectRepositoryMap, "later", Warm, now, data, ids[1])
		}
	}
	if len(ids) < 3 {
		t.Fatalf("only %d copies fit", len(ids))
	}
	fresh := bytes.Repeat([]byte("a different map "), 1000)
	if _, err := store.PutObject(Object{Type: ObjectRepositoryMap, SessionID: "new", Source: "test", Data: fresh}); err != nil {
		t.Fatalf("put into a store full of duplicate maps: %v", err)
	}
	for _, id := range append(ids, "ccr_obj_ref_to_01") {
		got, err := store.GetObject(id)
		if err != nil || !bytes.Equal(got.Data, data) {
			t.Fatalf("%s after consolidation: %v", id, err)
		}
	}
	// No object was deleted, the referenced copy kept its bytes, and no row
	// points at a reference.
	var full, chained int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM typed_objects WHERE content_hash = ? AND data_ref = ''`, testHash(data)).Scan(&full); err != nil {
		t.Fatal(err)
	}
	if full != 1 {
		t.Fatalf("%d full copies remain, want 1 (the one a reference reads)", full)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM typed_objects r JOIN typed_objects o ON o.object_id = r.data_ref WHERE o.data_ref != ''`).Scan(&chained); err != nil {
		t.Fatal(err)
	}
	if chained != 0 {
		t.Fatalf("%d references point at a reference", chained)
	}
	var owner string
	if err := store.db.QueryRow(`SELECT object_id FROM typed_objects WHERE content_hash = ? AND data_ref = ''`, testHash(data)).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != ids[1] {
		t.Fatalf("owner = %s, want the already-referenced copy %s", owner, ids[1])
	}
	var expired int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM expired_objects`).Scan(&expired); err != nil {
		t.Fatal(err)
	}
	if expired != 0 {
		t.Fatalf("consolidation expired %d objects", expired)
	}
}

func TestConsolidationOwnerMustVerify(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "ccr.db"))
	data := bytes.Repeat([]byte("map"), 2000)
	broken := bytes.Repeat([]byte("other map"), 2000)
	now := time.Now().UTC()
	mustInsertObjectRow(t, store.db, "ccr_obj_a", ObjectRepositoryMap, "a", Warm, now, data, "")
	mustInsertObjectRow(t, store.db, "ccr_obj_b", ObjectRepositoryMap, "b", Warm, now.Add(time.Second), data, "")
	mustInsertObjectRow(t, store.db, "ccr_obj_c", ObjectRepositoryMap, "c", Warm, now, broken, "")
	mustInsertObjectRow(t, store.db, "ccr_obj_d", ObjectRepositoryMap, "d", Warm, now.Add(time.Second), broken, "")
	// The older copy of the first map is damaged: it must not become the
	// owner. Both copies of the second map are damaged: leave them for repair.
	for _, id := range []string{"ccr_obj_a", "ccr_obj_c", "ccr_obj_d"} {
		if _, err := store.db.Exec(`UPDATE typed_objects SET data = ? WHERE object_id = ?`, []byte("damaged "+id), id); err != nil {
			t.Fatal(err)
		}
	}
	store.mu.Lock()
	objects, _, err := store.consolidateRepositoryMaps()
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if objects != 1 {
		t.Fatalf("consolidated %d objects, want 1", objects)
	}
	got, err := store.GetObject("ccr_obj_a")
	if err != nil || !bytes.Equal(got.Data, data) {
		t.Fatalf("damaged copy should now read the verified owner: %v", err)
	}
	var refs int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM typed_objects WHERE object_id IN ('ccr_obj_c','ccr_obj_d') AND data_ref != ''`).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if refs != 0 {
		t.Fatal("a group with no verified copy was consolidated")
	}
}

func TestExpiredObjectAnswersExpiredThroughEveryReadPath(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "ccr.db"))
	if _, err := store.db.Exec(`INSERT INTO expired_objects (object_id, expired_at) VALUES ('ccr_obj_gone', '2026-11-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetObject("ccr_obj_gone"); !errors.Is(err, ErrExpired) {
		t.Fatalf("GetObject = %v, want ErrExpired", err)
	}
	// Engine.Retrieve tries the blob path first; for an object id that path
	// must carry the same answer.
	if _, err := store.Get("ccr_obj_gone"); !errors.Is(err, ErrExpired) || !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get = %v, want ErrExpired wrapping ErrNotFound", err)
	}
	if _, err := store.GetObject("ccr_obj_never"); !errors.Is(err, ErrNotFound) || errors.Is(err, ErrExpired) {
		t.Fatalf("unknown id = %v, want plain ErrNotFound", err)
	}
	// An id stored again after it expired is simply found.
	data := []byte("stored again")
	mustInsertObjectRow(t, store.db, "ccr_obj_gone", ObjectFileObservation, "s", Hot, time.Now(), data, "")
	if got, err := store.GetObject("ccr_obj_gone"); err != nil || !bytes.Equal(got.Data, data) {
		t.Fatalf("re-stored id: %v", err)
	}
}

// Each reclaim lookup must use an index: on a 500 MB store a full scan per
// candidate walks every large object's overflow pages.
func TestReclaimLookupsUseIndexes(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "ccr.db"))
	for _, c := range []struct {
		query string
		args  []any
		index string
	}{
		{referencedSQL, []any{"ccr_obj_x"}, "typed_objects_data_ref"},
		{readersSQL, []any{"ccr_obj_x"}, "typed_objects_data_ref"},
		{`SELECT content_hash FROM typed_objects
		 WHERE object_type = 'RepositoryMap' AND data_ref = ''
		 GROUP BY content_hash HAVING COUNT(*) > 1`, nil, "typed_objects_owner"},
	} {
		rows, err := store.db.Query(`EXPLAIN QUERY PLAN `+c.query, c.args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		joined := strings.Join(plan, "\n")
		if !strings.Contains(joined, "INDEX "+c.index) {
			t.Fatalf("%s\nplan:\n%s\nwant index %s", c.query, joined, c.index)
		}
		for _, line := range plan {
			if line == "SCAN typed_objects" {
				t.Fatalf("%s\nplan:\n%s\nscans every row", c.query, joined)
			}
		}
	}
}
