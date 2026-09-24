package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Middleware state lives beside the existing replacement cache. Unlike its LRU,
// live middleware leases are never evicted: capacity stops new plans. These
// tables own scoped grants, immutable choices, observations and, since protocol
// 1.1, the originals themselves (middleware_originals.body): the process-global
// CCR is only read for grants an older runtime issued.
const middlewareSchema = `
CREATE TABLE IF NOT EXISTS middleware_scopes (
  id TEXT PRIMARY KEY, authority TEXT NOT NULL, manifest BLOB NOT NULL,
  sequence INTEGER NOT NULL, expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS middleware_choices (
  scope TEXT NOT NULL, id TEXT NOT NULL, payload BLOB NOT NULL,
  grant_id TEXT NOT NULL UNIQUE, ccr_handle TEXT NOT NULL,
  PRIMARY KEY(scope,id)
);
CREATE INDEX IF NOT EXISTS middleware_scopes_authority ON middleware_scopes(authority);
CREATE INDEX IF NOT EXISTS middleware_scopes_expiry ON middleware_scopes(expires_at);
CREATE TABLE IF NOT EXISTS middleware_plans (
  scope TEXT NOT NULL, id TEXT NOT NULL, digest TEXT NOT NULL,
  payload BLOB NOT NULL, PRIMARY KEY(scope,id)
);
CREATE TABLE IF NOT EXISTS middleware_receipts (
  authority TEXT NOT NULL, id TEXT NOT NULL, digest TEXT NOT NULL,
  payload BLOB NOT NULL, expires_at INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(authority,id)
);
CREATE INDEX IF NOT EXISTS middleware_receipts_expiry ON middleware_receipts(expires_at);
CREATE TABLE IF NOT EXISTS middleware_originals (
  authority TEXT NOT NULL, digest TEXT NOT NULL, PRIMARY KEY(authority,digest)
);
CREATE TABLE IF NOT EXISTS middleware_usage (
  singleton INTEGER PRIMARY KEY CHECK(singleton=1), rows INTEGER NOT NULL, bytes INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS middleware_principal_usage (
  principal TEXT PRIMARY KEY, rows INTEGER NOT NULL, bytes INTEGER NOT NULL
);`

// CREATE TABLE IF NOT EXISTS never adds a column to a store written by an older
// runtime, so every column added since protocol 1.0 arrives through ALTER, on a
// fresh store too: one path, identical tables. A duplicate-column error means
// the migration already ran; every other failure is real. Rows written before a
// column existed take its DEFAULT: receipts and plans with expires_at 0 are
// reclaimed by the next sweep, and rows with an empty principal count only globally.
var middlewareColumns = []string{
	`ALTER TABLE middleware_receipts ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE middleware_scopes ADD COLUMN created_at INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE middleware_plans ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE middleware_originals ADD COLUMN body BLOB`,
	`ALTER TABLE middleware_originals ADD COLUMN key_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE middleware_scopes ADD COLUMN principal TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE middleware_choices ADD COLUMN principal TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE middleware_plans ADD COLUMN principal TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE middleware_receipts ADD COLUMN principal TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE middleware_originals ADD COLUMN principal TEXT NOT NULL DEFAULT ''`,
}

// Byte accounting per table. %[1]s is NEW or OLD. An original is its 64-byte
// digest key plus its (possibly sealed) body; credit-only rows have no body.
// Compound sizes are parenthesized: triggers subtract them.
var middlewareSizes = []struct{ table, size string }{
	{"middleware_scopes", "length(%[1]s.manifest)"}, {"middleware_choices", "length(%[1]s.payload)"},
	{"middleware_plans", "length(%[1]s.payload)"}, {"middleware_receipts", "length(%[1]s.payload)"},
	{"middleware_originals", "(64+coalesce(length(%[1]s.body),0))"},
}

var (
	ErrMiddlewareConflict = errors.New("middleware: identity conflict")
	ErrMiddlewareCapacity = errors.New("middleware: capacity")
)

// Default admission limits. Originals moved out of CCR (512 MiB budget) into
// this store, so the byte cap is that budget plus the 64 MiB metadata cap the
// store had before.
const (
	DefaultMiddlewareRows  int64 = 1_000_000
	DefaultMiddlewareBytes int64 = 64<<20 + 512<<20
)

// MiddlewareLimits bounds admissions. Rows/Bytes cap the whole store; the
// Principal caps bound what one principal may hold, so a principal at its quota
// gets capacity while every other principal keeps working. Zero global values
// take the defaults; zero principal values mean only the global cap applies.
type MiddlewareLimits struct {
	Rows, Bytes                   int64
	PrincipalRows, PrincipalBytes int64
}

func (s *Store) InitMiddleware(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, middlewareSchema); err != nil {
		return err
	}
	for _, statement := range middlewareColumns {
		if _, err = tx.ExecContext(ctx, statement); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}
	// Scopes written before created_at existed start their max-retention clock at
	// migration: counting from 0 would expire every one of them at once.
	for _, statement := range []string{
		`UPDATE middleware_scopes SET created_at=CAST(strftime('%s','now') AS INTEGER) WHERE created_at=0`,
		`CREATE INDEX IF NOT EXISTS middleware_plans_expiry ON middleware_plans(expires_at)`,
		// Elapsed scopes whose payload is not purged yet. The partial index keeps a
		// sweep from rescanning a grace period's worth of tombstones every batch.
		`CREATE INDEX IF NOT EXISTS middleware_scopes_unpurged ON middleware_scopes(expires_at) WHERE length(manifest)>0`,
	} {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	// Backfill only at migration time. Trigger-maintained counters make each
	// admission O(1), including writes by another process using this store.
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO middleware_usage SELECT 1,
 (SELECT count(*) FROM middleware_scopes)+(SELECT count(*) FROM middleware_choices)+
 (SELECT count(*) FROM middleware_plans)+(SELECT count(*) FROM middleware_receipts)+(SELECT count(*) FROM middleware_originals),
 coalesce((SELECT sum(length(payload)) FROM middleware_choices),0)+coalesce((SELECT sum(length(payload)) FROM middleware_plans),0)+
 coalesce((SELECT sum(length(payload)) FROM middleware_receipts),0)+coalesce((SELECT sum(length(manifest)) FROM middleware_scopes),0)+
 coalesce((SELECT sum(64+coalesce(length(body),0)) FROM middleware_originals),0) WHERE NOT EXISTS(SELECT 1 FROM middleware_usage)`); err != nil {
		return err
	}
	// Triggers are dropped and recreated so a store migrated from an older
	// runtime gets current definitions: originals used to count a constant 64.
	// Per-principal counters skip an empty principal (rows older than the column).
	for _, table := range middlewareSizes {
		newSize, oldSize := fmt.Sprintf(table.size, "NEW"), fmt.Sprintf(table.size, "OLD")
		statements := fmt.Sprintf(`
DROP TRIGGER IF EXISTS %[1]s_usage_insert; DROP TRIGGER IF EXISTS %[1]s_usage_delete; DROP TRIGGER IF EXISTS %[1]s_usage_update;
DROP TRIGGER IF EXISTS %[1]s_quota_insert; DROP TRIGGER IF EXISTS %[1]s_quota_delete; DROP TRIGGER IF EXISTS %[1]s_quota_update;
CREATE TRIGGER %[1]s_usage_insert AFTER INSERT ON %[1]s BEGIN UPDATE middleware_usage SET rows=rows+1,bytes=bytes+%[2]s WHERE singleton=1; END;
CREATE TRIGGER %[1]s_usage_delete AFTER DELETE ON %[1]s BEGIN UPDATE middleware_usage SET rows=rows-1,bytes=bytes-%[3]s WHERE singleton=1; END;
CREATE TRIGGER %[1]s_usage_update AFTER UPDATE ON %[1]s BEGIN UPDATE middleware_usage SET bytes=bytes+%[2]s-%[3]s WHERE singleton=1; END;
CREATE TRIGGER %[1]s_quota_insert AFTER INSERT ON %[1]s WHEN NEW.principal<>'' BEGIN
 INSERT OR IGNORE INTO middleware_principal_usage VALUES (NEW.principal,0,0);
 UPDATE middleware_principal_usage SET rows=rows+1,bytes=bytes+%[2]s WHERE principal=NEW.principal; END;
CREATE TRIGGER %[1]s_quota_delete AFTER DELETE ON %[1]s WHEN OLD.principal<>'' BEGIN
 UPDATE middleware_principal_usage SET rows=rows-1,bytes=bytes-%[3]s WHERE principal=OLD.principal; END;
CREATE TRIGGER %[1]s_quota_update AFTER UPDATE ON %[1]s WHEN NEW.principal<>'' BEGIN
 UPDATE middleware_principal_usage SET bytes=bytes+%[2]s-%[3]s WHERE principal=NEW.principal; END;`, table.table, newSize, oldSize)
		if _, err = tx.ExecContext(ctx, statements); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type MiddlewareScope struct {
	ID, Authority                  string
	Manifest                       []byte
	Sequence, ExpiresAt, CreatedAt int64
}

// MiddlewareTx serializes decisions across processes, not just Go goroutines.
// Its first statement takes SQLite's write lock before any decision is read.
//
// Principal is stamped on every row the transaction writes and Limits bound its
// admissions; set both before the first write.
type MiddlewareTx struct {
	tx        *sql.Tx
	ctx       context.Context
	Principal string
	Limits    MiddlewareLimits
}

// ReadMiddleware takes a consistent snapshot without reserving SQLite's writer.
// Callers must recheck every decision under WithMiddleware before publishing it.
func (s *Store) ReadMiddleware(ctx context.Context, fn func(*MiddlewareTx) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(&MiddlewareTx{tx: tx, ctx: ctx}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) WithMiddleware(ctx context.Context, fn func(*MiddlewareTx) error) error {
	// Avoid SQLite's busy-handler sleep/backoff between local requests. This
	// queue is cancellable; SQLite still arbitrates against other processes.
	select {
	case s.middlewareWriter <- struct{}{}:
		defer func() { <-s.middlewareWriter }()
	case <-ctx.Done():
		return ctx.Err()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE middleware_scopes SET sequence=sequence WHERE id=''`); err != nil {
		return err
	}
	if err = fn(&MiddlewareTx{tx: tx, ctx: ctx}); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return tx.Commit()
}

// MiddlewareWritable proves the store accepts a write: it rewrites one existing
// row in a transaction and commits, so a read-only file, a full disk or a held
// lock fails readiness instead of the next optimize.
func (s *Store) MiddlewareWritable(ctx context.Context) error {
	return s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
		_, err := tx.tx.ExecContext(ctx, `UPDATE middleware_usage SET rows=rows WHERE singleton=1`)
		return err
	})
}

// MiddlewareUsage reports the whole store's admitted rows and bytes.
func (s *Store) MiddlewareUsage(ctx context.Context) (rows, bytes int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT rows,bytes FROM middleware_usage WHERE singleton=1`).Scan(&rows, &bytes)
	return rows, bytes, err
}

func (t *MiddlewareTx) Scope(id string) (MiddlewareScope, error) {
	s := MiddlewareScope{ID: id}
	err := t.tx.QueryRowContext(t.ctx, `SELECT authority,manifest,sequence,expires_at,created_at FROM middleware_scopes WHERE id=?`, id).
		Scan(&s.Authority, &s.Manifest, &s.Sequence, &s.ExpiresAt, &s.CreatedAt)
	return s, err
}

// SaveScope inserts or advances a scope. created_at is set once, at insert.
func (t *MiddlewareTx) SaveScope(s MiddlewareScope) error {
	var old int
	err := t.tx.QueryRowContext(t.ctx, `SELECT length(manifest) FROM middleware_scopes WHERE id=?`, s.ID).Scan(&old)
	rows := 0
	if errors.Is(err, sql.ErrNoRows) {
		rows = 1
	} else if err != nil {
		return err
	}
	if err := t.capacity(len(s.Manifest)-old, rows); err != nil {
		return err
	}
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO middleware_scopes(id,authority,manifest,sequence,expires_at,created_at,principal) VALUES (?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET manifest=excluded.manifest,sequence=excluded.sequence,expires_at=excluded.expires_at`,
		s.ID, s.Authority, s.Manifest, s.Sequence, s.ExpiresAt, s.CreatedAt, t.Principal)
	return err
}

func (t *MiddlewareTx) Plan(scope, id, digest string) ([]byte, error) {
	var storedDigest string
	var body []byte
	err := t.tx.QueryRowContext(t.ctx, `SELECT digest,payload FROM middleware_plans WHERE scope=? AND id=?`, scope, id).Scan(&storedDigest, &body)
	if err == nil && storedDigest != digest {
		return nil, ErrMiddlewareConflict
	}
	return body, err
}

// SavePlan records an idempotent replay. Plans carry their own expiry: a scope
// renewed on every call would otherwise keep one row per call until it lapses.
func (t *MiddlewareTx) SavePlan(scope, id, digest string, body []byte, expires int64) error {
	if err := t.capacity(len(body), 1); err != nil {
		return err
	}
	_, err := t.tx.ExecContext(t.ctx, `INSERT INTO middleware_plans(scope,id,digest,payload,expires_at,principal) VALUES (?,?,?,?,?,?)`,
		scope, id, digest, body, expires, t.Principal)
	return err
}

func (t *MiddlewareTx) Choice(scope, id string) ([]byte, string, error) {
	var body []byte
	var handle string
	err := t.tx.QueryRowContext(t.ctx, `SELECT payload,ccr_handle FROM middleware_choices WHERE scope=? AND id=?`, scope, id).Scan(&body, &handle)
	return body, handle, err
}

// SaveChoice publishes an immutable choice. handle is a CCR handle only for
// choices an older runtime made; this runtime's choices store their original in
// middleware_originals and pass "".
func (t *MiddlewareTx) SaveChoice(scope, id, grant, handle string, body []byte) error {
	if err := t.capacity(len(body), 1); err != nil {
		return err
	}
	_, err := t.tx.ExecContext(t.ctx, `INSERT INTO middleware_choices(scope,id,payload,grant_id,ccr_handle,principal) VALUES (?,?,?,?,?,?)`,
		scope, id, body, grant, handle, t.Principal)
	return err
}

// Grant never accepts a global CCR hash as authority. Even possession of the
// random grant requires the authenticated principal and matching session scope.
func (t *MiddlewareTx) Grant(authority, grant string) ([]byte, string, int64, error) {
	var body []byte
	var handle string
	var expires int64
	err := t.tx.QueryRowContext(t.ctx, `SELECT c.payload,c.ccr_handle,s.expires_at
FROM middleware_choices c JOIN middleware_scopes s ON c.scope=s.id
WHERE s.authority=? AND c.grant_id=?`, authority, grant).Scan(&body, &handle, &expires)
	return body, handle, expires, err
}

// Renew slides every live scope of an authority to now+retention, never past
// created_at+maxRetention. Scopes with no created_at (written by an older
// runtime after migration) slide uncapped rather than expiring at once.
func (t *MiddlewareTx) Renew(authority string, now, retention, maxRetention int64) error {
	_, err := t.tx.ExecContext(t.ctx, `UPDATE middleware_scopes
SET expires_at=CASE WHEN created_at>0 THEN min(?1+?2, created_at+?3) ELSE ?1+?2 END
WHERE authority=?4 AND expires_at>?1`, now, retention, maxRetention, authority)
	return err
}

// MiddlewareDeleted counts what a revocation removed. Legacy counts revoked
// grants whose original lives in the process-global CCR, which this store
// cannot delete: those originals persist until CCR itself drops them.
type MiddlewareDeleted struct {
	Scopes, Choices, Originals, Legacy int64
}

func (t *MiddlewareTx) Delete(authority string, now int64) (MiddlewareDeleted, error) {
	var out MiddlewareDeleted
	const scopes = `SELECT id FROM middleware_scopes WHERE authority=?`
	if err := t.tx.QueryRowContext(t.ctx, `SELECT count(*) FROM middleware_choices WHERE ccr_handle<>'' AND scope IN (`+scopes+`)`, authority).Scan(&out.Legacy); err != nil {
		return out, err
	}
	// Retain bounded metadata tombstones, not replacement text. Old markers
	// still return a typed revoked result; no epoch silently resumes old grants.
	// expires_at<=0 records when, so Expire can eventually reclaim it (below).
	// Already-revoked scopes keep their first revocation time.
	for _, step := range []struct {
		count     *int64
		statement string
		args      []any
	}{
		{&out.Scopes, `UPDATE middleware_scopes SET expires_at=? WHERE authority=? AND expires_at>0`, []any{-now, authority}},
		{&out.Choices, `UPDATE middleware_choices SET payload=x'',ccr_handle='' WHERE (length(payload)>0 OR ccr_handle<>'') AND scope IN (` + scopes + `)`, []any{authority}},
		{&out.Originals, `DELETE FROM middleware_originals WHERE authority=? AND body IS NOT NULL`, []any{authority}},
		{nil, `DELETE FROM middleware_originals WHERE authority=?`, []any{authority}},
		{nil, `DELETE FROM middleware_receipts WHERE authority=?`, []any{authority}},
		{nil, `DELETE FROM middleware_plans WHERE scope IN (` + scopes + `)`, []any{authority}},
		{nil, `UPDATE middleware_scopes SET manifest=x'' WHERE authority=?`, []any{authority}},
	} {
		result, err := t.tx.ExecContext(t.ctx, step.statement, step.args...)
		if err != nil {
			return out, err
		}
		if step.count != nil {
			if *step.count, err = result.RowsAffected(); err != nil {
				return out, err
			}
		}
	}
	return out, nil
}

// MiddlewareGraceSeconds is how long an elapsed scope keeps a metadata-only
// tombstone after its payloads are reclaimed. The tombstone is what turns a
// replayed marker from a dead session into a typed "expired" answer instead of
// a silent new scope over unrecoverable text.
const MiddlewareGraceSeconds int64 = 7 * 24 * 60 * 60

// Expire reclaims one bounded batch and reports how many rows it touched; a
// sweeper calls it until it returns 0. Three batches, each on its own clock:
//
//  1. Elapsed scopes whose payload is still present lose it at once: plans,
//     choices, manifest and, once no live scope shares the authority, its
//     originals. An emptied manifest marks the scope purged, so the next batch
//     moves on to other scopes instead of reselecting these for a whole grace
//     period (A4). A live scope never loses originals its choices reference.
//  2. Tombstones past the grace period go entirely. Revoked scopes
//     (expires_at<=0, the negated revocation time) are purged by Delete and wait
//     here: recovery reads the typed "deleted" answer off the choice row, and a
//     Grant that finds no row reports "not_found", a marker the caller never
//     had, instead of "deleted", the one they had and lost.
//  3. Receipts and plans past their own expiry.
//
// Every statement is keyed on an indexed column. SQLite is not built with
// UPDATE/DELETE LIMIT here, so batches are selected first.
func (t *MiddlewareTx) Expire(now int64) (int64, error) {
	var total int64
	exec := func(statement string, args ...any) error {
		result, err := t.tx.ExecContext(t.ctx, statement, args...)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		total += n
		return err
	}
	// ?1 is now in every statement; a batch's ids bind as ?2..?n+1.
	unowned := `DELETE FROM middleware_originals WHERE authority IN (SELECT authority FROM middleware_scopes WHERE id IN (%[1]s))
 AND NOT EXISTS (SELECT 1 FROM middleware_scopes s WHERE s.authority=middleware_originals.authority AND s.expires_at>?1)`
	for _, batch := range []struct {
		selection  string
		statements []string
	}{
		{`SELECT id FROM middleware_scopes WHERE expires_at>0 AND expires_at<=?1 AND length(manifest)>0 LIMIT 128`, []string{
			unowned, `DELETE FROM middleware_plans WHERE scope IN (%[1]s)`, `DELETE FROM middleware_choices WHERE scope IN (%[1]s)`,
			`UPDATE middleware_scopes SET manifest=x'' WHERE id IN (%[1]s)`,
		}},
		{fmt.Sprintf(`SELECT id FROM middleware_scopes WHERE (expires_at>0 AND expires_at<=?1-%[1]d) OR (expires_at<=0 AND -expires_at<=?1-%[1]d) LIMIT 128`, MiddlewareGraceSeconds), []string{
			unowned, `DELETE FROM middleware_plans WHERE scope IN (%[1]s)`, `DELETE FROM middleware_choices WHERE scope IN (%[1]s)`,
			`DELETE FROM middleware_scopes WHERE id IN (%[1]s)`,
		}},
	} {
		args, err := t.ids(batch.selection, now)
		if err != nil {
			return total, err
		}
		if len(args) == 1 {
			continue
		}
		in := make([]string, len(args)-1)
		for i := range in {
			in[i] = fmt.Sprintf("?%d", i+2)
		}
		for _, statement := range batch.statements {
			if err := exec(fmt.Sprintf(statement, strings.Join(in, ",")), args...); err != nil {
				return total, err
			}
		}
	}
	for _, statement := range []string{
		`DELETE FROM middleware_receipts WHERE rowid IN (SELECT rowid FROM middleware_receipts WHERE expires_at<=? LIMIT 128)`,
		`DELETE FROM middleware_plans WHERE rowid IN (SELECT rowid FROM middleware_plans WHERE expires_at<=? LIMIT 128)`,
	} {
		if err := exec(statement, now); err != nil {
			return total, err
		}
	}
	return total, nil
}

// ids returns now followed by the selected scope ids, ready to bind as ?1..?n+1.
func (t *MiddlewareTx) ids(query string, now int64) ([]any, error) {
	rows, err := t.tx.QueryContext(t.ctx, query, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	args := []any{now}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		args = append(args, id)
	}
	return args, rows.Err()
}

func (t *MiddlewareTx) Receipt(authority, id, digest string, body []byte, expires int64) error {
	var old string
	err := t.tx.QueryRowContext(t.ctx, `SELECT digest FROM middleware_receipts WHERE authority=? AND id=?`, authority, id).Scan(&old)
	if err == nil {
		if old != digest {
			return ErrMiddlewareConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err := t.capacity(len(body), 1); err != nil {
		return err
	}
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO middleware_receipts (authority,id,digest,payload,expires_at,principal) VALUES (?,?,?,?,?,?)`,
		authority, id, digest, body, expires, t.Principal)
	return err
}

func (t *MiddlewareTx) capacity(extra, rows int) error {
	limits := t.Limits
	if limits.Rows <= 0 {
		limits.Rows = DefaultMiddlewareRows
	}
	if limits.Bytes <= 0 {
		limits.Bytes = DefaultMiddlewareBytes
	}
	var count, size int64
	err := t.tx.QueryRowContext(t.ctx, `SELECT rows,bytes FROM middleware_usage WHERE singleton=1`).Scan(&count, &size)
	if err != nil {
		return err
	}
	if count+int64(rows) > limits.Rows || size+int64(extra) > limits.Bytes {
		return ErrMiddlewareCapacity
	}
	if t.Principal == "" || (limits.PrincipalRows <= 0 && limits.PrincipalBytes <= 0) {
		return nil
	}
	count, size = 0, 0
	err = t.tx.QueryRowContext(t.ctx, `SELECT rows,bytes FROM middleware_principal_usage WHERE principal=?`, t.Principal).Scan(&count, &size)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if (limits.PrincipalRows > 0 && count+int64(rows) > limits.PrincipalRows) || (limits.PrincipalBytes > 0 && size+int64(extra) > limits.PrincipalBytes) {
		return ErrMiddlewareCapacity
	}
	return nil
}
