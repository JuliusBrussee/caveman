//go:build !js

package ccr

// Inspection and repair of the host recovery store (#1369). Inspection never
// writes. Repair never edits the damaged file: it copies every row whose bytes
// still match their handle or content hash into a new file, keeps the damaged
// files as a backup next to it, and reports every row it could not carry over.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ErrStoreInUse means another connection holds the recovery store open, so a
// repair could lose a write that lands after the copy. Stop every Caveman
// process (the proxy, MCP servers, agent sessions) and retry.
var ErrStoreInUse = errors.New("ccr: the recovery store is open in another process; stop caveman (caveman stop) and close agent sessions, then retry")

// StoreReport describes a recovery store file without changing it.
type StoreReport struct {
	Path            string `json:"path"`
	Exists          bool   `json:"exists"`
	FileBytes       int64  `json:"file_bytes,omitempty"`
	PageSize        int64  `json:"page_size,omitempty"`
	Pages           int64  `json:"pages,omitempty"`
	FreePages       int64  `json:"free_pages,omitempty"`
	PageCap         int64  `json:"page_cap,omitempty"`
	PayloadBytes    int64  `json:"payload_bytes,omitempty"`
	MaxPayloadBytes int64  `json:"max_payload_bytes,omitempty"`
	// Full means the file is at its page cap or payload budget, so the next
	// write needs a reclaim to succeed. FullReason is StorageFull*.
	Full       bool   `json:"full,omitempty"`
	FullReason string `json:"full_reason,omitempty"`
	// Expirable* count, for a full store, the typed objects the retention
	// policy lets the next reclaim expire. Zero means a full store stays
	// full until objects age past the policy or the budget grows.
	ExpirableObjects int64 `json:"expirable_objects,omitempty"`
	ExpirableBytes   int64 `json:"expirable_bytes,omitempty"`
	// Integrity is set only when the caller asked for a check.
	Integrity         string           `json:"integrity,omitempty"`
	IntegrityFindings []string         `json:"integrity_findings,omitempty"`
	LastPassingCheck  *IntegrityReport `json:"last_passing_check,omitempty"`
	LastReclaim       *ReclaimReport   `json:"last_reclaim,omitempty"`
	// ReadError is a failure to read the figures above, which on a store
	// that opens is itself a sign of damage.
	ReadError string `json:"read_error,omitempty"`
}

// BudgetFromEnv returns the payload budget Open applies: CAVEMAN_CCR_MAX_BYTES
// or DefaultMaxStorageBytes.
func BudgetFromEnv() (int64, error) {
	raw := strings.TrimSpace(os.Getenv("CAVEMAN_CCR_MAX_BYTES"))
	if raw == "" {
		return DefaultMaxStorageBytes, nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("CAVEMAN_CCR_MAX_BYTES must be a positive integer")
	}
	return parsed, nil
}

// InspectStore reads path over a read-only connection and never creates it.
// check also runs PRAGMA quick_check, which reads the whole file.
func InspectStore(path string, maxBytes int64, check bool) (StoreReport, error) {
	report := StoreReport{Path: path, MaxPayloadBytes: maxBytes}
	canonical, exists, err := existingStorePath(path)
	if err != nil {
		return report, err
	}
	report.Path = canonical
	if !exists {
		return report, nil
	}
	report.Exists = true
	for _, suffix := range []string{"", "-wal"} {
		if info, err := os.Stat(canonical + suffix); err == nil {
			report.FileBytes += info.Size()
		}
	}
	db, err := sql.Open("sqlite", readOnlyDSN(canonical))
	if err != nil {
		return report, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if check {
		findings, err := quickCheck(db)
		if isCorrupt(err) {
			findings, err = []string{err.Error()}, nil
		}
		if err != nil {
			return report, fmt.Errorf("integrity check: %w", err)
		}
		report.Integrity = IntegrityOK
		if findings != nil {
			report.Integrity, report.IntegrityFindings = IntegrityDamaged, findings
		}
	}
	if err := inspectFigures(db, maxBytes, &report); err != nil {
		report.ReadError = err.Error()
		if check && isCorrupt(err) && report.Integrity == IntegrityOK {
			report.Integrity, report.IntegrityFindings = IntegrityDamaged, []string{err.Error()}
		}
	}
	return report, nil
}

func inspectFigures(db *sql.DB, maxBytes int64, report *StoreReport) error {
	for _, p := range []struct {
		pragma string
		into   *int64
	}{{"page_size", &report.PageSize}, {"page_count", &report.Pages}, {"freelist_count", &report.FreePages}} {
		if err := db.QueryRow(`PRAGMA ` + p.pragma).Scan(p.into); err != nil {
			return err
		}
	}
	if report.PageSize > 0 {
		report.PageCap = maxBytes / report.PageSize
	}
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('recoveries', 'typed_objects')`).Scan(&tables); err != nil {
		return err
	}
	if tables == 2 {
		store := &Store{db: db}
		used, err := store.storageBytes(db)
		if err != nil {
			return err
		}
		report.PayloadBytes = used
	}
	switch {
	case report.PayloadBytes >= maxBytes:
		report.Full, report.FullReason = true, StorageFullPayloadBudget
	// Within refusalMarginPages of the cap a typical write already fails:
	// it needs b-tree and index pages beyond its payload.
	case report.PageCap > 0 && report.PageCap-(report.Pages-report.FreePages) < refusalMarginPages:
		report.Full, report.FullReason = true, StorageFullPageCap
	}
	if report.Full && tables == 2 {
		now := time.Now()
		if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(length(data)+length(dependencies_json)),0)
		 FROM typed_objects WHERE object_id IN (`+expireCandidatesSQL+`)`,
			now.Add(-archivedRetention).UTC().Format(time.RFC3339Nano),
			now.Add(-idleSessionRetention).UTC().Format(time.RFC3339Nano),
		).Scan(&report.ExpirableObjects, &report.ExpirableBytes); err != nil {
			return err
		}
	}
	var hasMeta int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'ccr_meta'`).Scan(&hasMeta); err != nil || hasMeta == 0 {
		return err
	}
	store := &Store{}
	if raw, err := store.metaValue(db, metaIntegrityCheck); err == nil && raw != "" {
		var last IntegrityReport
		if json.Unmarshal([]byte(raw), &last) == nil {
			report.LastPassingCheck = &last
		}
	}
	if raw, err := store.metaValue(db, metaLastReclaim); err == nil && raw != "" {
		var last ReclaimReport
		if json.Unmarshal([]byte(raw), &last) == nil {
			report.LastReclaim = &last
		}
	}
	return nil
}

// existingStorePath resolves path the way Open does, without creating it.
func existingStorePath(path string) (string, bool, error) {
	if strings.TrimSpace(path) == "" || path == ":memory:" {
		return "", false, fmt.Errorf("ccr: a recovery store file path is required")
	}
	parent, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return "", false, err
	}
	resolved, err := filepath.EvalSymlinks(parent)
	if errors.Is(err, os.ErrNotExist) {
		return filepath.Join(parent, filepath.Base(path)), false, nil
	}
	if err != nil {
		return "", false, err
	}
	canonical := filepath.Join(resolved, filepath.Base(path))
	info, err := os.Lstat(canonical)
	if errors.Is(err, os.ErrNotExist) {
		return canonical, false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("ccr: refusing non-regular recovery store %q", canonical)
	}
	return canonical, true, nil
}

// RepairCounts says what a repair carried over for one table.
type RepairCounts struct {
	Kept int `json:"kept"`
	// Dropped rows were read but their bytes do not match their handle or
	// content hash (or a reference has no verified copy left); the store
	// already refused to return them. They stay in the backup.
	Dropped    int      `json:"dropped"`
	DroppedIDs []string `json:"dropped_ids,omitempty"`
	// Unreadable ids are listed in the table's key index, but their row
	// could not be read through any path. They stay in the backup.
	Unreadable    int      `json:"unreadable"`
	UnreadableIDs []string `json:"unreadable_ids,omitempty"`
}

// RepairReport is the result of RepairStore.
type RepairReport struct {
	Path string `json:"path"`
	// Result is "intact" (nothing done) or "repaired".
	Result string `json:"result"`
	// BackupPath names the damaged database, moved aside with its -wal and
	// -shm. Nothing in it was changed or deleted.
	BackupPath    string       `json:"backup_path,omitempty"`
	Findings      []string     `json:"findings,omitempty"`
	Recoveries    RepairCounts `json:"recoveries"`
	TypedObjects  RepairCounts `json:"typed_objects"`
	ExpiredIDs    int          `json:"expired_object_ids"`
	RestartNeeded bool         `json:"restart_needed,omitempty"`
}

const repairListLimit = 200

func (c *RepairCounts) drop(id string) {
	c.Dropped++
	if len(c.DroppedIDs) < repairListLimit {
		c.DroppedIDs = append(c.DroppedIDs, id)
	}
}

func (c *RepairCounts) unreadable(id string) {
	c.Unreadable++
	if len(c.UnreadableIDs) < repairListLimit {
		c.UnreadableIDs = append(c.UnreadableIDs, id)
	}
}

// RepairStore rebuilds a damaged recovery store. It holds an exclusive lock on
// the damaged file from the first read to the swap, so it refuses to start
// (ErrStoreInUse) while any other connection has the store open, and no other
// process can write to it meanwhile. Every row whose bytes still match its
// handle or content hash is copied into a new file; the damaged database and
// its journals are renamed to BackupPath, never edited or deleted. An intact
// store is left alone unless force is set, in which case it is rebuilt the
// same way (which also returns free pages to the file system).
func RepairStore(path string, force bool) (RepairReport, error) {
	report := RepairReport{Path: path}
	canonical, exists, err := existingStorePath(path)
	if err != nil {
		return report, err
	}
	report.Path = canonical
	if !exists {
		return report, fmt.Errorf("ccr: no recovery store at %s", canonical)
	}
	u := url.URL{Scheme: "file", OmitHost: true, Path: canonical}
	old, err := sql.Open("sqlite", u.String()+"?mode=rw&_pragma=busy_timeout(2000)&_pragma=locking_mode(EXCLUSIVE)")
	if err != nil {
		return report, err
	}
	oldOpen := true
	closeOld := func() {
		if oldOpen {
			oldOpen = false
			_ = old.Close()
		}
	}
	defer closeOld()
	old.SetMaxOpenConns(1)
	// Closing this connection must not unlink journals by name: after the
	// swap those names belong to the new file.
	if err := persistSQLiteWAL(old); err != nil {
		return report, fmt.Errorf("ccr repair: %w", err)
	}
	findings, err := quickCheck(old)
	if isBusy(err) {
		return report, ErrStoreInUse
	}
	if isCorrupt(err) {
		findings, err = []string{err.Error()}, nil
	}
	if err != nil {
		return report, fmt.Errorf("ccr repair integrity check: %w", err)
	}
	report.Findings = findings
	if findings == nil && !force {
		report.Result = "intact"
		return report, nil
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	tmpPath := canonical + ".repair-" + stamp
	if _, err := os.Lstat(tmpPath); err == nil {
		return report, fmt.Errorf("ccr repair: %s already exists", tmpPath)
	}
	fresh, err := OpenWithBudget(tmpPath, 1<<40)
	if err != nil {
		return report, fmt.Errorf("ccr repair: create %s: %w", tmpPath, err)
	}
	discard := func() {
		_ = fresh.Close()
		for _, suffix := range sqliteSuffixes {
			_ = os.Remove(tmpPath + suffix)
		}
	}
	if err := salvage(old, fresh.db, &report); err != nil {
		discard()
		return report, fmt.Errorf("ccr repair: %w", err)
	}
	if problems, err := quickCheck(fresh.db); err != nil || problems != nil {
		discard()
		return report, fmt.Errorf("ccr repair: rebuilt store failed quick_check: %v %v", problems, err)
	}
	if _, err := fresh.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		discard()
		return report, fmt.Errorf("ccr repair: checkpoint rebuilt store: %w", err)
	}
	if err := fresh.Close(); err != nil {
		discard()
		return report, fmt.Errorf("ccr repair: close rebuilt store: %w", err)
	}
	report.BackupPath = canonical + ".damaged-" + stamp
	if findings == nil {
		report.BackupPath = canonical + ".before-rebuild-" + stamp
	}
	if runtime.GOOS == "windows" {
		// Windows cannot rename a file another handle holds, this one
		// included. Another process that opens the store in the moment
		// between this close and the rename makes the rename fail, and then
		// nothing has changed.
		closeOld()
	}
	if err := swapRepaired(canonical, tmpPath, report.BackupPath); err != nil {
		for _, suffix := range sqliteSuffixes {
			_ = os.Remove(tmpPath + suffix)
		}
		return report, fmt.Errorf("ccr repair: %w", err)
	}
	report.Result = "repaired"
	report.RestartNeeded = true
	return report, nil
}

// swapRepaired moves the damaged database and its journals to backup, then the
// rebuilt file into place. A failure part way moves everything back.
func swapRepaired(path, rebuilt, backup string) error {
	var moved []string
	undo := func() {
		for i := len(moved) - 1; i >= 0; i-- {
			_ = os.Rename(backup+moved[i], path+moved[i])
		}
	}
	for _, suffix := range sqliteSuffixes {
		if _, err := os.Lstat(path + suffix); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := os.Rename(path+suffix, backup+suffix); err != nil {
			undo()
			return fmt.Errorf("move %s aside: %w", path+suffix, err)
		}
		moved = append(moved, suffix)
	}
	// The rebuilt file was checkpointed and closed, so its journals hold no
	// data; they are not carried over.
	for _, suffix := range []string{"-wal", "-shm"} {
		if info, err := os.Stat(rebuilt + suffix); err == nil && suffix == "-wal" && info.Size() > 0 {
			undo()
			return fmt.Errorf("rebuilt store still has %d journal bytes", info.Size())
		}
		_ = os.Remove(rebuilt + suffix)
	}
	if err := os.Rename(rebuilt, path); err != nil {
		undo()
		return fmt.Errorf("move rebuilt store into place: %w", err)
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// salvage copies every verifiable row from old into fresh. Each table is read
// several ways, because damage that hides a row from one path (a page with
// two parents, an index out of step with its table) often leaves another.
func salvage(old, fresh *sql.DB, report *RepairReport) error {
	tx, err := fresh.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := salvageRecoveries(old, tx, &report.Recoveries); err != nil {
		return err
	}
	if err := salvageObjects(old, tx, &report.TypedObjects); err != nil {
		return err
	}
	report.ExpiredIDs = salvageSmallTable(old, tx, `SELECT object_id, expired_at FROM expired_objects`,
		`INSERT OR IGNORE INTO expired_objects (object_id, expired_at) VALUES (?, ?)`)
	// The last passing check described the damaged file, so it is not copied.
	salvageSmallTable(old, tx, `SELECT key, value FROM ccr_meta WHERE key != '`+metaIntegrityCheck+`'`,
		`INSERT OR IGNORE INTO ccr_meta (key, value) VALUES (?, ?)`)
	return tx.Commit()
}

// readPasses runs each query and hands every row to visit until the query
// ends or fails; a failure ends only that pass.
func readPasses(db *sql.DB, queries []string, visit func(*sql.Rows) error) error {
	for _, query := range queries {
		rows, err := db.Query(query)
		if err != nil {
			if isCorrupt(err) {
				continue
			}
			return err
		}
		for rows.Next() {
			if err := visit(rows); err != nil {
				_ = rows.Close()
				return err
			}
		}
		_ = rows.Close()
	}
	return nil
}

const recoveryColumns = `handle, created_at, content_type, compressor, tokens_before, tokens_after, original, metadata`

func salvageRecoveries(old *sql.DB, tx *sql.Tx, counts *RepairCounts) error {
	seen := map[string]bool{}
	visit := func(rows interface{ Scan(...any) error }) error {
		var handle, created, contentType, compressor string
		var before, after int64
		var original, metadata []byte
		if err := rows.Scan(&handle, &created, &contentType, &compressor, &before, &after, &original, &metadata); err != nil {
			return nil // an unreadable record: other passes may still reach it
		}
		if seen[handle] {
			return nil
		}
		seen[handle] = true
		if Handle(original) != handle {
			counts.drop(handle)
			return nil
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO recoveries (`+recoveryColumns+`) VALUES (?,?,?,?,?,?,?,?)`,
			handle, created, contentType, compressor, before, after, original, metadata); err != nil {
			return err
		}
		counts.Kept++
		return nil
	}
	if err := readPasses(old, []string{
		`SELECT ` + recoveryColumns + ` FROM recoveries NOT INDEXED`,
		`SELECT ` + recoveryColumns + ` FROM recoveries NOT INDEXED ORDER BY rowid DESC`,
	}, func(rows *sql.Rows) error { return visit(rows) }); err != nil {
		return err
	}
	return salvageByKey(old, `SELECT handle FROM recoveries ORDER BY handle`, seen, func(handle string) error {
		row := old.QueryRow(`SELECT `+recoveryColumns+` FROM recoveries WHERE handle = ?`, handle)
		before := counts.Kept + counts.Dropped
		if err := visit(row); err != nil {
			return err
		}
		if counts.Kept+counts.Dropped == before {
			counts.unreadable(handle)
		}
		return nil
	})
}

// salvageByKey lists every key the table's own index holds and calls fetch for
// each one no earlier pass reached.
func salvageByKey(old *sql.DB, keysQuery string, seen map[string]bool, fetch func(string) error) error {
	var keys []string
	if err := readPasses(old, []string{keysQuery}, func(rows *sql.Rows) error {
		var key string
		if rows.Scan(&key) == nil && !seen[key] {
			keys = append(keys, key)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, key := range keys {
		if seen[key] {
			continue
		}
		if err := fetch(key); err != nil {
			return err
		}
	}
	return nil
}

type rawObject struct {
	id, typ, hash, source, created, repoState, session, transform string
	currentness, lifecycle, deps                                  string
	originalLen, storedLen                                        int64
	data                                                          []byte
	ref                                                           string
}

func (o *rawObject) scan(row interface{ Scan(...any) error }) error {
	return row.Scan(&o.id, &o.typ, &o.hash, &o.source, &o.created, &o.repoState, &o.session, &o.transform,
		&o.currentness, &o.lifecycle, &o.deps, &o.originalLen, &o.storedLen, &o.data, &o.ref)
}

func (o *rawObject) verifies() bool {
	sum := sha256.Sum256(o.data)
	return "sha256:"+hex.EncodeToString(sum[:]) == o.hash
}

func (o *rawObject) insert(tx *sql.Tx) error {
	data := o.data
	if o.ref != "" {
		data = []byte{}
	}
	_, err := tx.Exec(`INSERT OR IGNORE INTO typed_objects (`+objectColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		o.id, o.typ, o.hash, o.source, o.created, o.repoState, o.session, o.transform,
		o.currentness, o.lifecycle, o.deps, o.originalLen, o.storedLen, data, o.ref)
	return err
}

func salvageObjects(old *sql.DB, tx *sql.Tx, counts *RepairCounts) error {
	seen := map[string]bool{}
	var refs []rawObject
	visit := func(row interface{ Scan(...any) error }) error {
		var o rawObject
		if o.scan(row) != nil {
			return nil
		}
		if seen[o.id] {
			return nil
		}
		seen[o.id] = true
		if o.ref != "" {
			refs = append(refs, o)
			return nil
		}
		if !o.verifies() {
			counts.drop(o.id)
			return nil
		}
		if err := o.insert(tx); err != nil {
			return err
		}
		counts.Kept++
		return nil
	}
	if err := readPasses(old, []string{
		`SELECT ` + objectColumns + ` FROM typed_objects NOT INDEXED`,
		`SELECT ` + objectColumns + ` FROM typed_objects NOT INDEXED ORDER BY rowid DESC`,
	}, func(rows *sql.Rows) error { return visit(rows) }); err != nil {
		return err
	}
	if err := salvageByKey(old, `SELECT object_id FROM typed_objects ORDER BY object_id`, seen, func(id string) error {
		before := counts.Kept + counts.Dropped + len(refs)
		if err := visit(old.QueryRow(`SELECT `+objectColumns+` FROM typed_objects WHERE object_id = ?`, id)); err != nil {
			return err
		}
		if counts.Kept+counts.Dropped+len(refs) == before {
			counts.unreadable(id)
		}
		return nil
	}); err != nil {
		return err
	}
	// A reference survives only if a verified full copy of its content made
	// it across: its own owner when that did, else any other copy.
	for _, ref := range refs {
		owner := ""
		var hash string
		err := tx.QueryRow(`SELECT content_hash FROM typed_objects WHERE object_id = ? AND data_ref = ''`, ref.ref).Scan(&hash)
		if err == nil && hash == ref.hash {
			owner = ref.ref
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		} else {
			err := tx.QueryRow(`SELECT object_id FROM typed_objects WHERE object_type = ? AND content_hash = ? AND data_ref = ''
			 ORDER BY created_at ASC, object_id ASC LIMIT 1`, ref.typ, ref.hash).Scan(&owner)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if owner == "" {
			counts.drop(ref.id)
			continue
		}
		ref.ref = owner
		if err := ref.insert(tx); err != nil {
			return err
		}
		counts.Kept++
	}
	return nil
}

// salvageSmallTable copies a two-column bookkeeping table as far as it reads.
func salvageSmallTable(old *sql.DB, tx *sql.Tx, query, insert string) int {
	copied := 0
	_ = readPasses(old, []string{query}, func(rows *sql.Rows) error {
		var a, b string
		if rows.Scan(&a, &b) == nil {
			if _, err := tx.Exec(insert, a, b); err == nil {
				copied++
			}
		}
		return nil
	})
	return copied
}
