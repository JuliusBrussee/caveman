//go:build !js

// The host (non-js/wasm) recovery store: a local SQLite database under
// ~/.caveman/. The js/wasm build uses the in-memory store in store_wasm.go.
package ccr

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store is a SQLite-backed recovery store.
type Store struct {
	db       *sql.DB
	maxBytes int64
}

const DefaultMaxStorageBytes int64 = 512 << 20 // 512 MiB retained payloads

const schema = `
CREATE TABLE IF NOT EXISTS recoveries (
  handle TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  content_type TEXT NOT NULL,
  compressor TEXT NOT NULL,
  tokens_before INTEGER NOT NULL,
  tokens_after INTEGER NOT NULL,
  original BLOB NOT NULL,
  metadata BLOB
);
CREATE TABLE IF NOT EXISTS typed_objects (
  object_id TEXT PRIMARY KEY,
  object_type TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  source TEXT NOT NULL,
  created_at TEXT NOT NULL,
  repository_state TEXT NOT NULL,
  session_id TEXT NOT NULL,
  transform_version TEXT NOT NULL,
  currentness TEXT NOT NULL,
  lifecycle TEXT NOT NULL,
  dependencies_json TEXT NOT NULL,
  original_byte_length INTEGER NOT NULL,
  stored_byte_length INTEGER NOT NULL,
  data BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS typed_objects_session_created
  ON typed_objects(session_id, created_at DESC);`

// SQLiteDSN builds the driver DSN for path. The two pragmas are load-bearing,
// not tuning: this recovery store is opened once per CLI process (caveman-mcp,
// cavemem, caveman-engine, caveman-browse) yet several such processes share one
// ~/.caveman/*.db file. Without journal_mode(WAL) a reader is locked out while a
// write is in flight, and without busy_timeout a contender returns SQLITE_BUSY
// instead of waiting — a lost Put means an elided payload with no recoverable
// original, and a lost Get reads as an unknown handle. This mirrors the exact
// reasoning used by the other local SQLite store; the two must not diverge.
func SQLiteDSN(path string) string {
	const pragmas = "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	if path == ":memory:" {
		return "file::memory:?" + pragmas
	}
	// A file: URI so a path containing '?' or '#' cannot be truncated into a
	// different database file by the driver's DSN split.
	u := url.URL{Scheme: "file", OmitHost: true, Path: path}
	return u.String() + "?" + pragmas
}

// RetryOnBusy runs fn, retrying while SQLite reports the database is locked
// (SQLITE_BUSY). Short-lived local processes can initialize the same database
// concurrently, and a multi-statement migration can hold the write lock longer
// than a single connection's busy_timeout. Retries have a five-second wall-clock
// budget; one already-running SQLite call may finish after that deadline, but no
// new attempt starts after it.
// Runtime single-statement writes are covered by busy_timeout alone and must
// NOT route through here — a retry loop around ordinary contention would only
// mask a real stall.
func RetryOnBusy(fn func() error) error {
	return retryOnBusy(fn, 5*time.Second, time.Now, time.Sleep)
}

func retryOnBusy(fn func() error, maxWait time.Duration, now func() time.Time, sleep func(time.Duration)) error {
	const attempts = 40
	deadline := now().Add(maxWait)
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 && !now().Before(deadline) {
			return err
		}
		if err = fn(); err == nil || !isBusy(err) {
			return err
		}
		remaining := deadline.Sub(now())
		if remaining <= 0 {
			return err
		}
		delay := time.Duration(25*(i+1)) * time.Millisecond
		if delay > remaining {
			delay = remaining
		}
		sleep(delay)
	}
	return err
}

func isBusy(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "database is locked") || strings.Contains(s, "SQLITE_BUSY")
}

// Open opens (creating if needed) the SQLite database at path and migrates the
// schema. Use ":memory:" for an ephemeral store (tests, the eval harness).
func Open(path string) (*Store, error) {
	maxBytes := DefaultMaxStorageBytes
	if raw := strings.TrimSpace(os.Getenv("CAVEMAN_CCR_MAX_BYTES")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			return nil, fmt.Errorf("CAVEMAN_CCR_MAX_BYTES must be a positive integer")
		}
		maxBytes = parsed
	}
	return OpenWithBudget(path, maxBytes)
}

// OpenWithBudget opens a persistent store with an explicit retained-payload
// budget. New writes fail with ErrBudgetExceeded; existing recovery rows are
// never evicted because an emitted transformed request may still reference any
// handle.
func OpenWithBudget(path string, maxBytes int64) (*Store, error) {
	return openWithBudget(path, maxBytes, nil)
}

func openWithBudget(path string, maxBytes int64, afterPrepare func()) (*Store, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("ccr max storage bytes must be positive")
	}
	canonicalPath, err := PrepareSQLitePathCanonical(path)
	if err != nil {
		return nil, err
	}
	if afterPrepare != nil {
		afterPrepare()
	}
	db, err := sql.Open("sqlite", SQLiteDSN(canonicalPath))
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", canonicalPath, err)
	}
	// One writer connection. With a single connection every statement serializes
	// in-process (no self-contention) while busy_timeout absorbs cross-process
	// contention; it is also mandatory for an in-memory DSN, where a second
	// pooled connection would be a second, empty database.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := RetryOnBusy(func() error { _, e := db.Exec(schema); return e }); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate sqlite %q: %w", canonicalPath, err)
	}
	if err := RetryOnBusy(func() error { return ensureMetadataColumn(db) }); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate sqlite metadata %q: %w", canonicalPath, err)
	}
	if err := configureStorageBudget(db, maxBytes); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure sqlite storage budget %q: %w", canonicalPath, err)
	}
	if err := secureSQLiteFiles(canonicalPath); err != nil {
		_ = db.Close()
		return nil, err
	}
	// CCR is one local embedded database. Serialize access through one connection:
	// this prevents SQLITE_BUSY under background capture and keeps :memory:
	// stores on the same schema-bearing connection.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return &Store{db: db, maxBytes: maxBytes}, nil
}

// PrepareSQLitePath creates or tightens a persistent SQLite file before the
// driver sees it. Recovery databases can contain prompts, credentials, and tool
// results, so neither a permissive umask nor a caller-supplied symlink may widen
// access. Existing WAL/SHM files receive the same checks.
func PrepareSQLitePath(path string) error {
	_, err := PrepareSQLitePathCanonical(path)
	return err
}

// PrepareSQLitePathCanonical secures path and returns the one canonical path
// callers must use for every subsequent open and sidecar check. Returning the
// resolved parent closes the check/use gap from a swappable parent symlink.
func PrepareSQLitePathCanonical(path string) (string, error) {
	if path == ":memory:" {
		return path, nil
	}
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("ccr sqlite path is required")
	}
	parent := filepath.Dir(path)
	absoluteParent, err := filepath.Abs(parent)
	if err != nil {
		return "", fmt.Errorf("resolve sqlite parent %q: %w", parent, err)
	}
	resolvedParent, err := filepath.EvalSymlinks(absoluteParent)
	if err != nil {
		return "", fmt.Errorf("resolve sqlite parent %q: %w", parent, err)
	}
	parentInfo, err := os.Stat(resolvedParent)
	if err != nil {
		return "", fmt.Errorf("inspect sqlite parent %q: %w", resolvedParent, err)
	}
	if !parentInfo.IsDir() {
		return "", fmt.Errorf("sqlite parent %q must be a directory", resolvedParent)
	}
	if err := validateSQLiteParentSecurity(resolvedParent, parentInfo); err != nil {
		return "", err
	}
	canonicalPath := filepath.Join(resolvedParent, filepath.Base(path))
	if err := secureSQLiteFile(canonicalPath, true); err != nil {
		return "", fmt.Errorf("secure sqlite %q: %w", canonicalPath, err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := secureSQLiteFile(canonicalPath+suffix, false); err != nil {
			return "", fmt.Errorf("secure sqlite sidecar %q: %w", canonicalPath+suffix, err)
		}
	}
	return canonicalPath, nil
}

func secureSQLiteFiles(path string) error {
	if err := PrepareSQLitePath(path); err != nil {
		return err
	}
	return nil
}

func secureSQLiteFile(path string, create bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && create {
		file, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if createErr == nil {
			return file.Close()
		}
		if !errors.Is(createErr, os.ErrExist) {
			return createErr
		}
		info, err = os.Lstat(path)
	}
	if errors.Is(err, os.ErrNotExist) && !create {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("refusing non-regular file")
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) && !create {
		// The sidecar vanished between Lstat and open — a concurrent process
		// checkpointed the WAL and removed it. Nothing left to secure.
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, opened) {
		return fmt.Errorf("file changed while opening")
	}
	return file.Chmod(0o600)
}

func configureStorageBudget(db *sql.DB, maxBytes int64) error {
	var pageSize int64
	if err := db.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		return fmt.Errorf("read page size: %w", err)
	}
	if pageSize <= 0 {
		return errors.New("invalid sqlite page size")
	}
	maxPages := maxBytes / pageSize
	if maxPages < 16 {
		return fmt.Errorf("budget %d is below SQLite minimum %d", maxBytes, 16*pageSize)
	}
	var applied int64
	if err := db.QueryRow(fmt.Sprintf(`PRAGMA max_page_count=%d`, maxPages)).Scan(&applied); err != nil {
		return err
	}
	if applied < maxPages {
		maxPages = applied
	}
	autoCheckpoint := maxPages / 16
	if autoCheckpoint < 1 {
		autoCheckpoint = 1
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA wal_autocheckpoint=%d`, autoCheckpoint)); err != nil {
		return err
	}
	_, err := db.Exec(fmt.Sprintf(`PRAGMA journal_size_limit=%d`, maxBytes/8))
	return err
}

func ensureMetadataColumn(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(recoveries)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == "metadata" {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(`ALTER TABLE recoveries ADD COLUMN metadata BLOB`)
	return err
}

// OpenMemory opens an ephemeral in-memory store.
func OpenMemory() (*Store, error) { return OpenWithBudget(":memory:", DefaultMaxStorageBytes) }

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Put stores a recovery and returns its handle. It is idempotent: storing the
// same original twice (the handle is derived from the bytes) overwrites the row
// with identical content and returns the same handle.
func (s *Store) Put(rec Recovery) (string, error) {
	handle := Handle(rec.Original)
	unchanged, err := s.recoveryUnchanged(handle, rec)
	if err != nil {
		return "", fmt.Errorf("ccr put: %w", err)
	}
	if unchanged {
		return handle, nil
	}
	if err := s.checkRecoveryBudget(handle, int64(len(rec.Original)+len(rec.Metadata))); err != nil {
		return "", fmt.Errorf("ccr put: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO recoveries (handle, created_at, content_type, compressor, tokens_before, tokens_after, original, metadata)
		 VALUES (?,?,?,?,?,?,?,?)
		 ON CONFLICT(handle) DO UPDATE SET
		   created_at=excluded.created_at,
		   content_type=CASE WHEN excluded.tokens_before = 0 AND recoveries.tokens_before > 0 THEN recoveries.content_type ELSE excluded.content_type END,
		   compressor=CASE WHEN excluded.tokens_before = 0 AND recoveries.tokens_before > 0 THEN recoveries.compressor ELSE excluded.compressor END,
		   tokens_before=CASE WHEN excluded.tokens_before = 0 AND recoveries.tokens_before > 0 THEN recoveries.tokens_before ELSE excluded.tokens_before END,
		   tokens_after=CASE WHEN excluded.tokens_before = 0 AND recoveries.tokens_before > 0 THEN recoveries.tokens_after ELSE excluded.tokens_after END,
		   original=excluded.original,
		   metadata=CASE
		     WHEN recoveries.metadata IS NULL OR length(recoveries.metadata) = 0 THEN excluded.metadata
		     ELSE recoveries.metadata
		   END`,
		handle, time.Now().UTC().Format(time.RFC3339Nano),
		rec.ContentType, rec.Compressor, rec.TokensBefore, rec.TokensAfter, rec.Original, rec.Metadata,
	)
	if err != nil {
		if isFull(err) {
			return "", fmt.Errorf("ccr put: %w", ErrBudgetExceeded)
		}
		return "", fmt.Errorf("ccr put: %w", err)
	}
	return handle, nil
}

func (s *Store) recoveryUnchanged(handle string, rec Recovery) (bool, error) {
	var contentType, compressor string
	var tokensBefore, tokensAfter int
	var original, metadata []byte
	err := s.db.QueryRow(
		`SELECT content_type, compressor, tokens_before, tokens_after, original, metadata
		 FROM recoveries WHERE handle=?`,
		handle,
	).Scan(&contentType, &compressor, &tokensBefore, &tokensAfter, &original, &metadata)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return contentType == rec.ContentType &&
		compressor == rec.Compressor &&
		tokensBefore == rec.TokensBefore &&
		tokensAfter == rec.TokensAfter &&
		bytes.Equal(original, rec.Original) &&
		bytes.Equal(metadata, rec.Metadata), nil
}

func (s *Store) checkRecoveryBudget(handle string, newBytes int64) error {
	used, err := s.storageBytes()
	if err != nil {
		return err
	}
	var existing int64
	err = s.db.QueryRow(`SELECT COALESCE(length(original),0)+COALESCE(length(metadata),0) FROM recoveries WHERE handle=?`, handle).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		existing = 0
	} else if err != nil {
		return err
	}
	if used-existing+newBytes > s.maxBytes {
		return ErrBudgetExceeded
	}
	return nil
}

func (s *Store) storageBytes() (int64, error) {
	var used int64
	err := s.db.QueryRow(`SELECT
	  COALESCE((SELECT SUM(length(original)+COALESCE(length(metadata),0)) FROM recoveries),0) +
	  COALESCE((SELECT SUM(length(data)+length(dependencies_json)) FROM typed_objects),0)`).Scan(&used)
	return used, err
}

func isFull(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "database or disk is full") || strings.Contains(text, "sqlite_full")
}

// Get returns the exact original bytes for a handle, or ErrNotFound.
func (s *Store) Get(handle string) ([]byte, error) {
	if strings.HasPrefix(handle, "ccr_obj_") {
		obj, err := s.GetObject(handle)
		if err != nil {
			return nil, err
		}
		return append([]byte(nil), obj.Data...), nil
	}
	var original []byte
	row := s.db.QueryRow(`SELECT original FROM recoveries WHERE handle = ?`, handle)
	switch err := row.Scan(&original); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("ccr get: %w", err)
	}
	return original, nil
}

// GetMetadata returns optional compressor metadata stored with a handle. A known
// handle with no metadata returns nil, nil; an unknown handle returns ErrNotFound.
func (s *Store) GetMetadata(handle string) ([]byte, error) {
	if strings.HasPrefix(handle, "ccr_obj_") {
		if _, err := s.GetObject(handle); err != nil {
			return nil, err
		}
		return nil, nil
	}
	var metadata []byte
	row := s.db.QueryRow(`SELECT metadata FROM recoveries WHERE handle = ?`, handle)
	switch err := row.Scan(&metadata); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("ccr metadata get: %w", err)
	}
	return metadata, nil
}

// PutObject stores one immutable typed working-memory object. Repeated puts of
// the same content-derived ID are idempotent; currentness changes use
// SetObjectCurrentness so invalidation stays explicit.
func (s *Store) PutObject(input Object) (string, error) {
	obj, err := prepareObject(input)
	if err != nil {
		return "", err
	}
	deps, err := json.Marshal(obj.Dependencies)
	if err != nil {
		return "", fmt.Errorf("ccr typed object dependencies: %w", err)
	}
	used, err := s.storageBytes()
	if err != nil {
		return "", fmt.Errorf("ccr typed object budget: %w", err)
	}
	var existingBytes int64
	err = s.db.QueryRow(`SELECT length(data)+length(dependencies_json) FROM typed_objects WHERE object_id=?`, obj.ID).Scan(&existingBytes)
	if errors.Is(err, sql.ErrNoRows) {
		existingBytes = 0
	} else if err != nil {
		return "", fmt.Errorf("ccr typed object budget: %w", err)
	}
	if used-existingBytes+int64(len(obj.Data)+len(deps)) > s.maxBytes {
		return "", fmt.Errorf("ccr typed object put: %w", ErrBudgetExceeded)
	}
	result, err := s.db.Exec(
		`INSERT INTO typed_objects (
		 object_id, object_type, content_hash, source, created_at, repository_state,
		 session_id, transform_version, currentness, lifecycle, dependencies_json,
		 original_byte_length, stored_byte_length, data
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(object_id) DO NOTHING`,
		obj.ID, obj.Type, obj.ContentHash, obj.Source, obj.CreatedAt.Format(time.RFC3339Nano),
		obj.RepositoryState, obj.SessionID, obj.TransformVersion, obj.Currentness,
		obj.Lifecycle, string(deps), obj.OriginalByteLength, obj.StoredByteLength, obj.Data,
	)
	if err != nil {
		if isFull(err) {
			return "", fmt.Errorf("ccr typed object put: %w", ErrBudgetExceeded)
		}
		return "", fmt.Errorf("ccr typed object put: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("ccr typed object put rows: %w", err)
	}
	if inserted == 0 {
		existing, err := s.GetObject(obj.ID)
		if err != nil {
			return "", fmt.Errorf("ccr typed object collision lookup: %w", err)
		}
		if !sameImmutableObject(existing, obj) {
			return "", errors.New("ccr: typed object id collision")
		}
	}
	return obj.ID, nil
}

func scanObject(scanner interface{ Scan(...any) error }) (Object, error) {
	var obj Object
	var created, deps string
	if err := scanner.Scan(
		&obj.ID, &obj.Type, &obj.ContentHash, &obj.Source, &created, &obj.RepositoryState,
		&obj.SessionID, &obj.TransformVersion, &obj.Currentness, &obj.Lifecycle, &deps,
		&obj.OriginalByteLength, &obj.StoredByteLength, &obj.Data,
	); err != nil {
		return Object{}, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Object{}, fmt.Errorf("ccr typed object created_at: %w", err)
	}
	obj.CreatedAt = parsed
	if err := json.Unmarshal([]byte(deps), &obj.Dependencies); err != nil {
		return Object{}, fmt.Errorf("ccr typed object dependencies decode: %w", err)
	}
	return obj, nil
}

const objectColumns = `object_id, object_type, content_hash, source, created_at,
 repository_state, session_id, transform_version, currentness, lifecycle,
 dependencies_json, original_byte_length, stored_byte_length, data`

func (s *Store) GetObject(id string) (Object, error) {
	obj, err := scanObject(s.db.QueryRow(`SELECT `+objectColumns+` FROM typed_objects WHERE object_id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Object{}, ErrNotFound
	}
	if err != nil {
		return Object{}, fmt.Errorf("ccr typed object get: %w", err)
	}
	return obj, nil
}

func (s *Store) SetObjectCurrentness(id string, currentness Currentness) error {
	if err := validateCurrentness(currentness); err != nil {
		return err
	}
	result, err := s.db.Exec(`UPDATE typed_objects SET currentness = ? WHERE object_id = ?`, currentness, id)
	if err != nil {
		return fmt.Errorf("ccr typed object currentness: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("ccr typed object currentness rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetObjectLifecycle(id string, lifecycle Lifecycle) error {
	if err := validateLifecycle(lifecycle); err != nil {
		return err
	}
	result, err := s.db.Exec(`UPDATE typed_objects SET lifecycle = ? WHERE object_id = ?`, lifecycle, id)
	if err != nil {
		return fmt.Errorf("ccr typed object lifecycle: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("ccr typed object lifecycle rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListSessionObjects(sessionID string, limit int) ([]Object, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 10000 {
		limit = 10000
	}
	rows, err := s.db.Query(`SELECT `+objectColumns+` FROM typed_objects WHERE session_id = ? ORDER BY created_at DESC, object_id DESC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("ccr typed object list: %w", err)
	}
	defer rows.Close()
	objects := make([]Object, 0)
	for rows.Next() {
		obj, err := scanObject(rows)
		if err != nil {
			return nil, fmt.Errorf("ccr typed object list scan: %w", err)
		}
		objects = append(objects, obj)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ccr typed object list rows: %w", err)
	}
	return objects, nil
}

// FindTaskDecision returns one Decision Ledger object by its stable decision
// ID. Callers still validate the versioned decision schema before rendering it.
func (s *Store) FindTaskDecision(decisionID string) (Object, error) {
	obj, err := scanObject(s.db.QueryRow(
		`SELECT `+objectColumns+` FROM typed_objects
		 WHERE object_type = ? AND json_extract(CAST(data AS TEXT), '$.decision_id') = ?
		 ORDER BY created_at DESC, object_id DESC LIMIT 1`,
		ObjectTaskDecision, decisionID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Object{}, ErrNotFound
	}
	if err != nil {
		return Object{}, fmt.Errorf("ccr task decision find: %w", err)
	}
	return obj, nil
}

// Summary aggregates stored recoveries into totals + per-content-type buckets.
func (s *Store) Summary() (Stats, error) {
	out := Stats{ByContentType: map[string]Bucket{}, Basis: "inferred"}
	rows, err := s.db.Query(
		`SELECT content_type, COUNT(*), COALESCE(SUM(tokens_before),0), COALESCE(SUM(tokens_after),0)
		 FROM recoveries GROUP BY content_type`)
	if err != nil {
		return out, fmt.Errorf("ccr summary: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ct string
		var b Bucket
		if err := rows.Scan(&ct, &b.Count, &b.TokensBefore, &b.TokensAfter); err != nil {
			return out, fmt.Errorf("ccr summary scan: %w", err)
		}
		b.Ratio = ratio(b.TokensBefore, b.TokensAfter)
		out.ByContentType[ct] = b
		out.Totals.Count += b.Count
		out.Totals.TokensBefore += b.TokensBefore
		out.Totals.TokensAfter += b.TokensAfter
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("ccr summary rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return out, fmt.Errorf("ccr summary close: %w", err)
	}
	out.Totals.Ratio = ratio(out.Totals.TokensBefore, out.Totals.TokensAfter)
	used, err := s.storageBytes()
	if err != nil {
		return out, fmt.Errorf("ccr summary storage: %w", err)
	}
	out.StorageBytes = used
	out.MaxStorageBytes = s.maxBytes
	out.StorageFull = used >= s.maxBytes
	return out, nil
}
