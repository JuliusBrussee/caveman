//go:build !js

package ccr

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestPutObjectReturnsStorageFullInsteadOfMintingHandle(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "ccr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var pages int
	if err := store.db.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA max_page_count = ` + strconv.Itoa(pages)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject(Object{
		Type: ObjectFileObservation, SessionID: "storage-full", Source: "test",
		Data: bytes.Repeat([]byte("exact-context"), 1024*1024),
	}); err == nil {
		t.Fatal("full CCR storage accepted object and could expose a false recovery handle")
	}
}

func TestReplacedConnectionKeepsPageCap(t *testing.T) {
	const budget = 1 << 20
	store, err := OpenWithBudget(filepath.Join(t.TempDir(), "ccr.db"), budget)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	want := int64(budget / 4096)
	// A lifetime of one nanosecond makes database/sql drop the pooled
	// connection and open a new one for the next statement.
	store.db.SetConnMaxLifetime(time.Nanosecond)
	if _, err := store.db.Exec(`SELECT 1`); err != nil {
		t.Fatal(err)
	}
	store.db.SetConnMaxLifetime(0)
	var got int64
	if err := store.db.QueryRow(`PRAGMA max_page_count`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("max_page_count on the new connection = %d, want %d", got, want)
	}
}

func TestPageCapUsesActualPageSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	if _, err := raw.Exec(`PRAGMA page_size=8192`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE seed (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	const budget = 1 << 20
	store, err := OpenWithBudget(path, budget)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var pageSize, got int64
	if err := store.db.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	if pageSize != 8192 {
		t.Fatalf("page size = %d, want 8192", pageSize)
	}
	if err := store.db.QueryRow(`PRAGMA max_page_count`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if want := int64(budget / 8192); got != want {
		t.Fatalf("max_page_count = %d, want %d", got, want)
	}
}

func TestPageExhaustionBelowPayloadBudgetReportsPageCap(t *testing.T) {
	const budget = 64 << 10
	store, err := OpenWithBudget(filepath.Join(t.TempDir(), "ccr.db"), budget)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var putErr error
	for i := 0; i < 100000 && putErr == nil; i++ {
		_, putErr = store.Put(Recovery{
			ContentType: "text", Compressor: "test",
			Original: []byte("small payload " + strconv.Itoa(i)),
		})
	}
	if !errors.Is(putErr, ErrBudgetExceeded) {
		t.Fatalf("put error = %v, want ErrBudgetExceeded", putErr)
	}
	stats, err := store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if stats.StorageBytes >= budget {
		t.Fatalf("payload bytes %d reached the budget, so this test does not cover the page cap", stats.StorageBytes)
	}
	if !stats.StorageFull || stats.StorageFullReason != StorageFullPageCap {
		t.Fatalf("full = %v, reason = %q, want page_cap", stats.StorageFull, stats.StorageFullReason)
	}
	// Raise the cap by hand. The next write must succeed and clear the state.
	limit := store.pageCap.Load() + 16
	if _, err := store.db.Exec(`PRAGMA max_page_count = ` + strconv.FormatInt(limit, 10)); err != nil {
		t.Fatal(err)
	}
	store.pageCap.Store(limit)
	if _, err := store.Put(Recovery{ContentType: "text", Compressor: "test", Original: []byte("after raise")}); err != nil {
		t.Fatalf("put after raising the cap: %v", err)
	}
	if store.refused.Load() != refusalNone {
		t.Fatal("refused-write state stayed set after a successful write")
	}
	stats, err = store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if stats.StorageFull || stats.StorageFullReason != "" {
		t.Fatalf("full = %v, reason = %q after a successful write", stats.StorageFull, stats.StorageFullReason)
	}
}

func TestRefusedWriteBelowPageCapReportsDiskFull(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "ccr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.refused.Store(refusalDiskFull)
	stats, err := store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if !stats.StorageFull || stats.StorageFullReason != StorageFullDiskFull {
		t.Fatalf("full = %v, reason = %q, want disk_full", stats.StorageFull, stats.StorageFullReason)
	}
}

func TestPayloadExhaustionReportsPayloadBudget(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "ccr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Put(Recovery{ContentType: "text", Compressor: "test", Original: bytes.Repeat([]byte("a"), 2000)}); err != nil {
		t.Fatal(err)
	}
	// Lower the budget under the stored payload. The file is far below its page cap.
	store.maxBytes = 1000
	stats, err := store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if !stats.StorageFull || stats.StorageFullReason != StorageFullPayloadBudget {
		t.Fatalf("full = %v, reason = %q, want payload_budget", stats.StorageFull, stats.StorageFullReason)
	}
}

func TestLargeWriteBelowPageCapReportsPageCap(t *testing.T) {
	const budget = 1 << 20
	store, err := OpenWithBudget(filepath.Join(t.TempDir(), "ccr.db"), budget)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Fill most pages, then try a write that needs more pages than remain.
	if _, err := store.Put(Recovery{ContentType: "text", Compressor: "test", Original: bytes.Repeat([]byte("a"), 560<<10)}); err != nil {
		t.Fatal(err)
	}
	_, err = store.Put(Recovery{ContentType: "text", Compressor: "test", Original: bytes.Repeat([]byte("b"), 462<<10)})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("put error = %v, want ErrBudgetExceeded", err)
	}
	var pages int64
	if err := store.db.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if pages >= store.pageCap.Load() {
		t.Fatalf("page count %d reached the cap, so this test does not cover free pages", pages)
	}
	stats, err := store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if stats.StorageBytes >= budget {
		t.Fatalf("payload bytes %d reached the budget", stats.StorageBytes)
	}
	if !stats.StorageFull || stats.StorageFullReason != StorageFullPageCap {
		t.Fatalf("full = %v, reason = %q, want page_cap", stats.StorageFull, stats.StorageFullReason)
	}
}

func TestFreePagesBelowPageCapAreRoom(t *testing.T) {
	const budget = 64 << 10
	store, err := OpenWithBudget(filepath.Join(t.TempDir(), "ccr.db"), budget)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var putErr error
	for i := 0; i < 100000 && putErr == nil; i++ {
		_, putErr = store.Put(Recovery{
			ContentType: "text", Compressor: "test",
			Original: append(bytes.Repeat([]byte("p"), 1500), strconv.Itoa(i)...),
		})
	}
	if !errors.Is(putErr, ErrBudgetExceeded) {
		t.Fatalf("put error = %v, want ErrBudgetExceeded", putErr)
	}
	// Delete the rows without VACUUM. The pages stay in the file as free pages.
	if _, err := store.db.Exec(`DELETE FROM recoveries`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(Recovery{ContentType: "text", Compressor: "test", Original: []byte("fresh recovery")}); err != nil {
		t.Fatalf("put into free pages: %v", err)
	}
	var pages, free int64
	if err := store.db.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`PRAGMA freelist_count`).Scan(&free); err != nil {
		t.Fatal(err)
	}
	if pages != store.pageCap.Load() || free == 0 {
		t.Fatalf("page_count = %d, freelist_count = %d, want page_count at the cap %d and free pages", pages, free, store.pageCap.Load())
	}
	stats, err := store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if stats.StorageFull || stats.StorageFullReason != "" {
		t.Fatalf("full = %v, reason = %q, want a store with free pages to read as not full", stats.StorageFull, stats.StorageFullReason)
	}
}

func TestConsolidatedRepositoryMapsLeaveRoom(t *testing.T) {
	const budget = 64 << 10
	store, err := OpenWithBudget(filepath.Join(t.TempDir(), "ccr.db"), budget)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var pageSize int64
	if err := store.db.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	// Each map is several pages long, so its bytes live in overflow pages.
	data := bytes.Repeat([]byte("m"), int(pageSize)*4)
	hash := "sha256:" + fmt.Sprintf("%x", sha256.Sum256(data))
	// Insert full copies of one map, the way old stores hold them, until the
	// page cap refuses a row.
	var insertErr error
	for i := 0; i < 1000 && insertErr == nil; i++ {
		_, insertErr = store.db.Exec(
			`INSERT INTO typed_objects (
			 object_id, object_type, content_hash, source, created_at, repository_state,
			 session_id, transform_version, currentness, lifecycle, dependencies_json,
			 original_byte_length, stored_byte_length, data, data_ref
			) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			"ccr_obj_dup"+strconv.Itoa(i), string(ObjectRepositoryMap), hash, "test",
			time.Unix(int64(1000+i), 0).UTC().Format(time.RFC3339Nano), "state"+strconv.Itoa(i),
			"s", "v1", string(Current), string(Hot), "[]", len(data), len(data), data, "",
		)
	}
	if !isFull(insertErr) {
		t.Fatalf("insert error = %v, want a full store", insertErr)
	}
	// Fill what is left with distinct maps until the page cap refuses one.
	for i := 0; i < 100 && !errors.Is(err, ErrBudgetExceeded); i++ {
		_, err = store.PutObject(Object{
			Type: ObjectRepositoryMap, SessionID: "s", Source: "test",
			Data: append(bytes.Repeat([]byte("n"), int(pageSize)), strconv.Itoa(i)...),
		})
	}
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("put error = %v, want ErrBudgetExceeded", err)
	}
	stats, err := store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if !stats.StorageFull || stats.StorageFullReason != StorageFullPageCap {
		t.Fatalf("before: full = %v, reason = %q, want page_cap", stats.StorageFull, stats.StorageFullReason)
	}
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TEMP TABLE owners AS
		   SELECT g.content_hash,
		          (SELECT t.object_id FROM typed_objects t
		           WHERE t.object_type = 'RepositoryMap' AND t.content_hash = g.content_hash AND t.data_ref = ''
		           ORDER BY t.created_at ASC, t.object_id ASC LIMIT 1) AS owner
		   FROM (SELECT DISTINCT content_hash FROM typed_objects
		         WHERE object_type = 'RepositoryMap' AND data_ref = '') g`,
		`UPDATE typed_objects
		   SET data = x'', data_ref = (SELECT owner FROM owners o WHERE o.content_hash = typed_objects.content_hash)
		   WHERE object_type = 'RepositoryMap' AND data_ref = ''
		     AND object_id <> (SELECT owner FROM owners o WHERE o.content_hash = typed_objects.content_hash)`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var pages, free int64
	if err := store.db.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`PRAGMA freelist_count`).Scan(&free); err != nil {
		t.Fatal(err)
	}
	t.Logf("after consolidation: page_count = %d, freelist_count = %d, cap = %d", pages, free, store.pageCap.Load())
	if free == 0 {
		t.Fatal("consolidation freed no page")
	}
	if pages != store.pageCap.Load() {
		t.Fatalf("page_count = %d, want it at the cap %d", pages, store.pageCap.Load())
	}
	if _, err := store.PutObject(Object{
		Type: ObjectRepositoryMap, SessionID: "s", Source: "test",
		Data: bytes.Repeat([]byte("n"), 2000),
	}); err != nil {
		t.Fatalf("put after consolidation: %v", err)
	}
	if store.refused.Load() != refusalNone {
		t.Fatal("refused-write state stayed set after a successful write")
	}
	stats, err = store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if stats.StorageFull || stats.StorageFullReason != "" {
		t.Fatalf("after: full = %v, reason = %q, want a store with room", stats.StorageFull, stats.StorageFullReason)
	}
}

func TestPayloadBudgetRefusalReportsPayloadBudget(t *testing.T) {
	const budget = 64 << 10
	store, err := OpenWithBudget(filepath.Join(t.TempDir(), "ccr.db"), budget)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Put(Recovery{ContentType: "text", Compressor: "test", Original: bytes.Repeat([]byte("a"), 20<<10)}); err != nil {
		t.Fatal(err)
	}
	_, err = store.Put(Recovery{ContentType: "text", Compressor: "test", Original: bytes.Repeat([]byte("b"), 50<<10)})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("put error = %v, want ErrBudgetExceeded", err)
	}
	stats, err := store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if stats.StorageBytes >= budget {
		t.Fatalf("payload bytes %d reached the budget, so the refusal state is the only signal", stats.StorageBytes)
	}
	if !stats.StorageFull || stats.StorageFullReason != StorageFullPayloadBudget {
		t.Fatalf("full = %v, reason = %q, want payload_budget", stats.StorageFull, stats.StorageFullReason)
	}
	if _, err := store.Put(Recovery{ContentType: "text", Compressor: "test", Original: []byte("small")}); err != nil {
		t.Fatalf("small put: %v", err)
	}
	stats, err = store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if stats.StorageFull || stats.StorageFullReason != "" {
		t.Fatalf("full = %v, reason = %q after a successful write", stats.StorageFull, stats.StorageFullReason)
	}
}

func TestReopenOversizedDatabaseUsesAppliedCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	store, err := OpenWithBudget(path, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 1000)
		INSERT INTO recoveries (handle, created_at, content_type, compressor, tokens_before, tokens_after, original, metadata)
		SELECT 'h' || i, printf('%05d', i), 'text', 'test', 0, 0, CAST(printf('%0100d', i) AS BLOB), x'' FROM n`); err != nil {
		t.Fatal(err)
	}
	// Delete half of the rows without VACUUM, so the file keeps its pages.
	if _, err := store.db.Exec(`DELETE FROM recoveries WHERE rowid IN (SELECT rowid FROM recoveries ORDER BY created_at LIMIT 500)`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	const budget = 64 << 10
	store, err = OpenWithBudget(path, budget)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var pageSize, applied, free int64
	if err := store.db.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`PRAGMA max_page_count`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`PRAGMA freelist_count`).Scan(&free); err != nil {
		t.Fatal(err)
	}
	if applied <= budget/pageSize || free == 0 {
		t.Fatalf("applied cap = %d, free pages = %d, want a cap above %d and free pages", applied, free, budget/pageSize)
	}
	if _, err := store.Put(Recovery{ContentType: "text", Compressor: "test", Original: []byte("new small recovery")}); err != nil {
		t.Fatalf("put into free pages: %v", err)
	}
	stats, err := store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if stats.StorageFull || stats.StorageFullReason != "" {
		t.Fatalf("full = %v, reason = %q, want a store with free pages to read as not full", stats.StorageFull, stats.StorageFullReason)
	}
}

func TestUnchangedObjectKeepsRefusalState(t *testing.T) {
	const budget = 1 << 20
	store, err := OpenWithBudget(filepath.Join(t.TempDir(), "ccr.db"), budget)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	object := Object{Type: ObjectFileObservation, SessionID: "s", Source: "test", Data: []byte("same object")}
	id, err := store.PutObject(object)
	if err != nil {
		t.Fatal(err)
	}
	// Fill most pages, then try a write that needs more pages than remain.
	if _, err := store.Put(Recovery{ContentType: "text", Compressor: "test", Original: bytes.Repeat([]byte("a"), 560<<10)}); err != nil {
		t.Fatal(err)
	}
	_, err = store.Put(Recovery{ContentType: "text", Compressor: "test", Original: bytes.Repeat([]byte("b"), 462<<10)})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("put error = %v, want ErrBudgetExceeded", err)
	}
	if again, err := store.PutObject(object); err != nil || again != id {
		t.Fatalf("repeated object: id = %q, err = %v, want %q", again, err, id)
	}
	stats, err := store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if !stats.StorageFull || stats.StorageFullReason != StorageFullPageCap {
		t.Fatalf("full = %v, reason = %q after an unchanged object, want page_cap", stats.StorageFull, stats.StorageFullReason)
	}
	object.Data = []byte("new small object")
	if _, err := store.PutObject(object); err != nil {
		t.Fatalf("new object: %v", err)
	}
	stats, err = store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if stats.StorageFull || stats.StorageFullReason != "" {
		t.Fatalf("full = %v, reason = %q after a new object", stats.StorageFull, stats.StorageFullReason)
	}
}
