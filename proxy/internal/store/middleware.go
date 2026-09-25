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
	choicesOriginalColumn,
	`ALTER TABLE middleware_choices ADD COLUMN legacy INTEGER NOT NULL DEFAULT 0`,
}

// choicesOriginalColumn names the digest of the middleware original a choice
// recovers ("" for a protocol 1.0 choice, whose original is in CCR), so expiry
// can reclaim each original with the last scope that references it. The
// migration that adds it backfills it from the choice payload.
const choicesOriginalColumn = `ALTER TABLE middleware_choices ADD COLUMN original TEXT NOT NULL DEFAULT ''`

// legacyChoice is a choice's legacy flag after its CCR handle is blanked
// (revocation, expiry): whether it was ever a protocol 1.0 grant, whose
// original stays in CCR where no delete reaches it.
const legacyChoice = `legacy=CASE WHEN legacy<>0 OR ccr_handle<>'' THEN 1 ELSE 0 END`

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
	backfill := false
	for _, statement := range middlewareColumns {
		if _, err = tx.ExecContext(ctx, statement); err == nil {
			backfill = backfill || statement == choicesOriginalColumn
		} else if !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}
	if backfill {
		if _, err = tx.ExecContext(ctx, `UPDATE middleware_choices SET original=coalesce(json_extract(CAST(payload AS TEXT),'$.original_sha256'),'')
WHERE ccr_handle='' AND json_valid(CAST(payload AS TEXT))`); err != nil {
			return err
		}
	}
	// Scopes written before created_at existed start their max-retention clock at
	// migration: counting from 0 would expire every one of them at once. Protocol
	// 1.0 revoked a scope with expires_at=0, which the sweep reads as revoked at
	// the epoch, long past grace: those tombstones are dated at migration too.
	for _, statement := range []string{
		`UPDATE middleware_scopes SET created_at=CAST(strftime('%s','now') AS INTEGER) WHERE created_at=0`,
		`UPDATE middleware_scopes SET expires_at=-CAST(strftime('%s','now') AS INTEGER) WHERE expires_at=0`,
		`CREATE INDEX IF NOT EXISTS middleware_plans_expiry ON middleware_plans(expires_at)`,
		`CREATE INDEX IF NOT EXISTS middleware_choices_original ON middleware_choices(original)`,
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
	// An update that stamps a principal on such a row (a protocol 1.0 credit
	// getting its body) moves the whole row onto that principal.
	for _, table := range middlewareSizes {
		newSize, oldSize := fmt.Sprintf(table.size, "NEW"), fmt.Sprintf(table.size, "OLD")
		statements := fmt.Sprintf(`
DROP TRIGGER IF EXISTS %[1]s_usage_insert; DROP TRIGGER IF EXISTS %[1]s_usage_delete; DROP TRIGGER IF EXISTS %[1]s_usage_update;
DROP TRIGGER IF EXISTS %[1]s_quota_insert; DROP TRIGGER IF EXISTS %[1]s_quota_delete; DROP TRIGGER IF EXISTS %[1]s_quota_update;
DROP TRIGGER IF EXISTS %[1]s_quota_move_out; DROP TRIGGER IF EXISTS %[1]s_quota_move_in;
CREATE TRIGGER %[1]s_usage_insert AFTER INSERT ON %[1]s BEGIN UPDATE middleware_usage SET rows=rows+1,bytes=bytes+%[2]s WHERE singleton=1; END;
CREATE TRIGGER %[1]s_usage_delete AFTER DELETE ON %[1]s BEGIN UPDATE middleware_usage SET rows=rows-1,bytes=bytes-%[3]s WHERE singleton=1; END;
CREATE TRIGGER %[1]s_usage_update AFTER UPDATE ON %[1]s BEGIN UPDATE middleware_usage SET bytes=bytes+%[2]s-%[3]s WHERE singleton=1; END;
CREATE TRIGGER %[1]s_quota_insert AFTER INSERT ON %[1]s WHEN NEW.principal<>'' BEGIN
 INSERT OR IGNORE INTO middleware_principal_usage VALUES (NEW.principal,0,0);
 UPDATE middleware_principal_usage SET rows=rows+1,bytes=bytes+%[2]s WHERE principal=NEW.principal; END;
CREATE TRIGGER %[1]s_quota_delete AFTER DELETE ON %[1]s WHEN OLD.principal<>'' BEGIN
 UPDATE middleware_principal_usage SET rows=rows-1,bytes=bytes-%[3]s WHERE principal=OLD.principal; END;
CREATE TRIGGER %[1]s_quota_update AFTER UPDATE ON %[1]s WHEN NEW.principal<>'' AND NEW.principal=OLD.principal BEGIN
 UPDATE middleware_principal_usage SET bytes=bytes+%[2]s-%[3]s WHERE principal=NEW.principal; END;
CREATE TRIGGER %[1]s_quota_move_out AFTER UPDATE ON %[1]s WHEN OLD.principal<>'' AND NEW.principal<>OLD.principal BEGIN
 UPDATE middleware_principal_usage SET rows=rows-1,bytes=bytes-%[3]s WHERE principal=OLD.principal; END;
CREATE TRIGGER %[1]s_quota_move_in AFTER UPDATE ON %[1]s WHEN NEW.principal<>'' AND NEW.principal<>OLD.principal BEGIN
 INSERT OR IGNORE INTO middleware_principal_usage VALUES (NEW.principal,0,0);
 UPDATE middleware_principal_usage SET rows=rows+1,bytes=bytes+%[2]s WHERE principal=NEW.principal; END;`, table.table, newSize, oldSize)
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

// MiddlewareStore is the middleware runtime's storage backend: the SQLite Store
// (the default: one writer per file) or PostgresMiddleware (shared by every
// replica). Both hand the runtime a *MiddlewareTx with the same semantics.
type MiddlewareStore interface {
	InitMiddleware(ctx context.Context) error
	// ReadMiddleware runs fn on a consistent snapshot. Callers must recheck every
	// decision under WithMiddleware before publishing it.
	ReadMiddleware(ctx context.Context, fn func(*MiddlewareTx) error) error
	// WithMiddleware runs fn in a write transaction whose reads see every write
	// committed by a competing writer of the same data before fn's decisions.
	WithMiddleware(ctx context.Context, fn func(*MiddlewareTx) error) error
	MiddlewareWritable(ctx context.Context) error
	MiddlewareUsage(ctx context.Context) (rows, bytes int64, err error)
	// MiddlewareKeys counts stored original bodies by the key id that sealed
	// them ("" is plaintext).
	MiddlewareKeys(ctx context.Context) (map[string]int64, error)
	// MiddlewareBacklog counts what has expired by now and is not reclaimed
	// yet: elapsed scopes still holding payload, plans and receipts.
	MiddlewareBacklog(ctx context.Context, now int64) (int64, error)
	// Persistent reports whether choices and originals survive a restart.
	Persistent() bool
}

// MiddlewareChoice is one persisted choice. Handle is the CCR handle of a
// choice protocol 1.0 made, "" otherwise; Original is the digest of the
// middleware original the choice recovers ("" for protocol 1.0).
type MiddlewareChoice struct {
	ID, Grant, Handle, Original string
	Payload                     []byte
}

// MiddlewareOriginal is one original to store under an authority. A nil Body
// records only the unique-content credit.
type MiddlewareOriginal struct {
	Digest, KeyID string
	Body          []byte
}

// StoredOriginal is what an authority holds for one digest: a body sealed with
// KeyID ("" is plaintext), or, with Body false, only the credit.
type StoredOriginal struct {
	KeyID string
	Body  bool
}

// middlewareTxOps is one backend's transaction: exactly the operations the
// middleware runtime performs, batched so a request costs a constant number of
// round trips whatever its segment count. Bodies arrive already sealed.
type middlewareTxOps interface {
	Scope(id string) (MiddlewareScope, error)
	SaveScope(s MiddlewareScope) error
	// Revoked reports whether sessions/delete revoked the authority (its
	// tombstones answer "deleted" until their grace ends).
	Revoked(authority string) (bool, error)
	Plan(scope, id, digest string) ([]byte, error)
	SavePlan(scope, id, digest string, body []byte, expires int64) error
	// Choices returns the scope's choices among ids, keyed by id.
	Choices(scope string, ids []string) (map[string]MiddlewareChoice, error)
	SaveChoices(scope string, choices []MiddlewareChoice) error
	Grant(authority, grant string) ([]byte, string, int64, error)
	Renew(authority string, now, retention, maxRetention int64) error
	Delete(authority string, now int64) (MiddlewareDeleted, error)
	// ExpireBatch reclaims one batch of at most limit rows per kind, and
	// reports how many rows it touched and whether any kind filled its batch.
	ExpireBatch(now int64, limit int) (int64, bool, error)
	Receipt(authority, id, digest string, body []byte, expires int64) error
	// Originals returns what the authority stores for each of digests.
	Originals(authority string, digests []string) (map[string]StoredOriginal, error)
	Original(authority, digest string) ([]byte, string, error)
	insertOriginals(authority string, originals []MiddlewareOriginal) error
	updateOriginals(authority string, originals []MiddlewareOriginal) error
	// usage is the store's totals and Principal's, this transaction's writes
	// included.
	usage() (rows, bytes, ownRows, ownBytes int64, err error)
}

// MiddlewareTx is one middleware transaction on either backend.
//
// Principal is stamped on every row the transaction writes and Limits bound its
// admissions; set both before the first write. Usable, when set, reports whether
// a stored original's key id can still be opened: SaveOriginals replaces a body
// sealed with a key that cannot.
type MiddlewareTx struct {
	middlewareTxOps
	Principal string
	Limits    MiddlewareLimits
	Usable    func(keyID string) bool
	// grew records an admission: a write that can add rows or bytes.
	grew bool
}

// MiddlewareSweepBatch is the default number of rows per kind one expiry batch
// reclaims.
const MiddlewareSweepBatch = 128

// Expire reclaims one default-sized batch (see ExpireBatch).
func (t *MiddlewareTx) Expire(now int64) (int64, error) {
	n, _, err := t.ExpireBatch(now, MiddlewareSweepBatch)
	return n, err
}

// Choice is Choices for one id; sql.ErrNoRows when the scope has none.
func (t *MiddlewareTx) Choice(scope, id string) ([]byte, string, error) {
	found, err := t.Choices(scope, []string{id})
	if err != nil {
		return nil, "", err
	}
	c, ok := found[id]
	if !ok {
		return nil, "", sql.ErrNoRows
	}
	return c.Payload, c.Handle, nil
}

// SaveChoice publishes one immutable choice (see SaveChoices).
func (t *MiddlewareTx) SaveChoice(scope, id, grant, handle string, body []byte) error {
	return t.SaveChoices(scope, []MiddlewareChoice{{ID: id, Grant: grant, Handle: handle, Payload: body}})
}

// SaveOriginal is SaveOriginals for one original.
func (t *MiddlewareTx) SaveOriginal(authority, digest string, body []byte, keyID string) (bool, error) {
	credits, err := t.SaveOriginals(authority, []MiddlewareOriginal{{Digest: digest, KeyID: keyID, Body: body}})
	return err == nil && credits[0], err
}

// CreditOriginal counts identical content once inside an authenticated session
// scope, without storing it or merging document/source identities or grants.
func (t *MiddlewareTx) CreditOriginal(authority, digest string) (bool, error) {
	return t.SaveOriginal(authority, digest, nil, "")
}

// SaveOriginals stores originals in the transaction that publishes the plan
// using them, so an aborted or not_smaller plan leaves none behind, and reports
// for each whether the authority saw its digest for the first time (the
// unique-content credit; a repeated digest is credited once). A credit-only row
// gets its body filled in without a second credit, and so does a body sealed
// with a key that is no longer Usable: that original is as good as absent.
func (t *MiddlewareTx) SaveOriginals(authority string, originals []MiddlewareOriginal) ([]bool, error) {
	digests := make([]string, len(originals))
	for i, o := range originals {
		digests[i] = o.Digest
	}
	stored, err := t.Originals(authority, digests)
	if err != nil {
		return nil, err
	}
	credits := make([]bool, len(originals))
	var inserts, updates []MiddlewareOriginal
	seen := map[string]bool{}
	for i, o := range originals {
		if seen[o.Digest] {
			continue
		}
		seen[o.Digest] = true
		row, ok := stored[o.Digest]
		switch {
		case !ok:
			credits[i] = true
			inserts = append(inserts, o)
		case o.Body != nil && (!row.Body || (t.Usable != nil && !t.Usable(row.KeyID))):
			updates = append(updates, o)
		}
	}
	if len(inserts) > 0 {
		if err := t.insertOriginals(authority, inserts); err != nil {
			return nil, err
		}
	}
	if len(updates) > 0 {
		if err := t.updateOriginals(authority, updates); err != nil {
			return nil, err
		}
	}
	return credits, nil
}

// admitted is the admission check, run once before a write transaction that
// grew commits: the totals it leaves behind, its own writes included, must fit
// the limits. A principal at its quota gets capacity; others are unaffected.
func (t *MiddlewareTx) admitted() error {
	if !t.grew {
		return nil
	}
	limits := t.Limits
	if limits.Rows <= 0 {
		limits.Rows = DefaultMiddlewareRows
	}
	if limits.Bytes <= 0 {
		limits.Bytes = DefaultMiddlewareBytes
	}
	rows, bytes, ownRows, ownBytes, err := t.usage()
	if err != nil {
		return err
	}
	if rows > limits.Rows || bytes > limits.Bytes || (t.Principal != "" &&
		((limits.PrincipalRows > 0 && ownRows > limits.PrincipalRows) || (limits.PrincipalBytes > 0 && ownBytes > limits.PrincipalBytes))) {
		return ErrMiddlewareCapacity
	}
	return nil
}

// sqliteMiddlewareTx serializes decisions across processes, not just Go
// goroutines: WithMiddleware's first statement takes SQLite's write lock before
// any decision is read. Its embedded *MiddlewareTx supplies Principal and Limits.
type sqliteMiddlewareTx struct {
	*MiddlewareTx
	tx  *sql.Tx
	ctx context.Context
}

func newSQLiteMiddlewareTx(ctx context.Context, tx *sql.Tx) *MiddlewareTx {
	m := &MiddlewareTx{}
	m.middlewareTxOps = &sqliteMiddlewareTx{MiddlewareTx: m, tx: tx, ctx: ctx}
	return m
}

// ReadMiddleware takes a consistent snapshot without reserving SQLite's writer.
// Callers must recheck every decision under WithMiddleware before publishing it.
func (s *Store) ReadMiddleware(ctx context.Context, fn func(*MiddlewareTx) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(newSQLiteMiddlewareTx(ctx, tx)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) WithMiddleware(ctx context.Context, fn func(*MiddlewareTx) error) error {
	return s.middlewareWrite(ctx, func(tx *sql.Tx) error {
		m := newSQLiteMiddlewareTx(ctx, tx)
		if err := fn(m); err != nil {
			return err
		}
		return m.admitted()
	})
}

func (s *Store) middlewareWrite(ctx context.Context, fn func(*sql.Tx) error) error {
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
	if err = fn(tx); err != nil {
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
	return s.middlewareWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE middleware_usage SET rows=rows WHERE singleton=1`)
		return err
	})
}

// MiddlewareUsage reports the whole store's admitted rows and bytes.
func (s *Store) MiddlewareUsage(ctx context.Context) (rows, bytes int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT rows,bytes FROM middleware_usage WHERE singleton=1`).Scan(&rows, &bytes)
	return rows, bytes, err
}

func (s *Store) MiddlewareKeys(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key_id,count(*) FROM middleware_originals WHERE body IS NOT NULL GROUP BY key_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := map[string]int64{}
	for rows.Next() {
		var id string
		var n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		keys[id] = n
	}
	return keys, rows.Err()
}

func (s *Store) MiddlewareBacklog(ctx context.Context, now int64) (n int64, err error) {
	err = s.db.QueryRowContext(ctx, middlewareBacklog("?1"), now).Scan(&n)
	return n, err
}

// middlewareBacklog is MiddlewareBacklog's query with now bound as param.
func middlewareBacklog(param string) string {
	return strings.ReplaceAll(`SELECT (SELECT count(*) FROM middleware_scopes WHERE expires_at>0 AND expires_at<=$now AND length(manifest)>0)
 +(SELECT count(*) FROM middleware_plans WHERE expires_at<=$now)+(SELECT count(*) FROM middleware_receipts WHERE expires_at<=$now)`, "$now", param)
}

func (t *sqliteMiddlewareTx) Scope(id string) (MiddlewareScope, error) {
	s := MiddlewareScope{ID: id}
	err := t.tx.QueryRowContext(t.ctx, `SELECT authority,manifest,sequence,expires_at,created_at FROM middleware_scopes WHERE id=?`, id).
		Scan(&s.Authority, &s.Manifest, &s.Sequence, &s.ExpiresAt, &s.CreatedAt)
	return s, err
}

// SaveScope inserts or advances a scope. created_at is set once, at insert.
func (t *sqliteMiddlewareTx) SaveScope(s MiddlewareScope) error {
	t.grew = true
	_, err := t.tx.ExecContext(t.ctx, `INSERT INTO middleware_scopes(id,authority,manifest,sequence,expires_at,created_at,principal) VALUES (?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET manifest=excluded.manifest,sequence=excluded.sequence,expires_at=excluded.expires_at`,
		s.ID, s.Authority, s.Manifest, s.Sequence, s.ExpiresAt, s.CreatedAt, t.Principal)
	return err
}

func (t *sqliteMiddlewareTx) Revoked(authority string) (revoked bool, err error) {
	err = t.tx.QueryRowContext(t.ctx, `SELECT EXISTS(SELECT 1 FROM middleware_scopes WHERE authority=? AND expires_at<=0)`, authority).Scan(&revoked)
	return revoked, err
}

func (t *sqliteMiddlewareTx) Plan(scope, id, digest string) ([]byte, error) {
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
func (t *sqliteMiddlewareTx) SavePlan(scope, id, digest string, body []byte, expires int64) error {
	t.grew = true
	_, err := t.tx.ExecContext(t.ctx, `INSERT INTO middleware_plans(scope,id,digest,payload,expires_at,principal) VALUES (?,?,?,?,?,?)`,
		scope, id, digest, body, expires, t.Principal)
	return err
}

// Choices looks each id up in turn: SQLite runs in process, so a lookup costs
// no round trip.
func (t *sqliteMiddlewareTx) Choices(scope string, ids []string) (map[string]MiddlewareChoice, error) {
	found := map[string]MiddlewareChoice{}
	for _, id := range ids {
		c := MiddlewareChoice{ID: id}
		err := t.tx.QueryRowContext(t.ctx, `SELECT payload,grant_id,ccr_handle,original FROM middleware_choices WHERE scope=? AND id=?`, scope, id).
			Scan(&c.Payload, &c.Grant, &c.Handle, &c.Original)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		found[id] = c
	}
	return found, nil
}

// SaveChoices publishes immutable choices. Handle is a CCR handle only for
// choices an older runtime made; this runtime's choices store their original in
// middleware_originals, pass "" and name its digest in Original.
func (t *sqliteMiddlewareTx) SaveChoices(scope string, choices []MiddlewareChoice) error {
	t.grew = true
	for _, c := range choices {
		if _, err := t.tx.ExecContext(t.ctx, `INSERT INTO middleware_choices(scope,id,payload,grant_id,ccr_handle,original,principal) VALUES (?,?,?,?,?,?,?)`,
			scope, c.ID, c.Payload, c.Grant, c.Handle, c.Original, t.Principal); err != nil {
			return err
		}
	}
	return nil
}

// Grant never accepts a global CCR hash as authority. Even possession of the
// random grant requires the authenticated principal and matching session scope.
func (t *sqliteMiddlewareTx) Grant(authority, grant string) ([]byte, string, int64, error) {
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
func (t *sqliteMiddlewareTx) Renew(authority string, now, retention, maxRetention int64) error {
	_, err := t.tx.ExecContext(t.ctx, `UPDATE middleware_scopes
SET expires_at=CASE WHEN created_at>0 THEN min(?1+?2, created_at+?3) ELSE ?1+?2 END
WHERE authority=?4 AND expires_at>?1`, now, retention, maxRetention, authority)
	return err
}

// MiddlewareDeleted counts what a revocation removed. Legacy counts revoked
// grants whose original lives in the process-global CCR, which this store
// cannot delete: those originals persist until CCR itself drops them. A choice
// row keeps that fact (its legacy flag) after revocation or expiry blank its
// handle, so a retried delete reports it again.
type MiddlewareDeleted struct {
	Scopes, Choices, Originals, Legacy int64
}

func (t *sqliteMiddlewareTx) Delete(authority string, now int64) (MiddlewareDeleted, error) {
	var out MiddlewareDeleted
	const scopes = `SELECT id FROM middleware_scopes WHERE authority=?`
	if err := t.tx.QueryRowContext(t.ctx, `SELECT count(*) FROM middleware_choices WHERE (ccr_handle<>'' OR legacy<>0) AND scope IN (`+scopes+`)`, authority).Scan(&out.Legacy); err != nil {
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
		{&out.Choices, `UPDATE middleware_choices SET payload=x'',` + legacyChoice + `,ccr_handle='' WHERE (length(payload)>0 OR ccr_handle<>'') AND scope IN (` + scopes + `)`, []any{authority}},
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

// ExpireBatch reclaims one bounded batch and reports how many rows it touched,
// and whether any kind filled its batch (more is likely waiting); a sweeper
// calls it again while it does. Three batches, each on its own clock:
//
//  1. Elapsed scopes whose payload is still present lose it at once: plans,
//     choice payloads and CCR handles, manifest and originals no live scope's
//     choice references (all of the authority's once no live scope shares it).
//     A choice keeps a content-free row, so its grant answers "expired", not
//     "not_found", until grace ends. An emptied manifest marks the scope purged,
//     so the next batch moves on to other scopes instead of reselecting these
//     for a whole grace period (A4).
//  2. Tombstones past the grace period go entirely. Revoked scopes
//     (expires_at<=0, the negated revocation time) are purged by Delete and wait
//     here: recovery reads the typed "deleted" answer off the choice row, and a
//     Grant that finds no row reports "not_found", a marker the caller never
//     had, instead of "deleted", the one they had and lost.
//  3. Receipts and plans past their own expiry.
//
// Every statement is keyed on an indexed column. SQLite is not built with
// UPDATE/DELETE LIMIT here, so batches are selected first.
func (t *sqliteMiddlewareTx) ExpireBatch(now int64, limit int) (int64, bool, error) {
	var total int64
	full := false
	exec := func(statement string, args ...any) (int64, error) {
		result, err := t.tx.ExecContext(t.ctx, statement, args...)
		if err != nil {
			return 0, err
		}
		n, err := result.RowsAffected()
		total += n
		return n, err
	}
	// ?1 is now in every statement; a batch's ids bind as ?2..?n+1.
	unowned := `DELETE FROM middleware_originals WHERE authority IN (SELECT authority FROM middleware_scopes WHERE id IN (%[1]s))
 AND NOT EXISTS (SELECT 1 FROM middleware_scopes s WHERE s.authority=middleware_originals.authority AND s.expires_at>?1)`
	unreferenced := `UPDATE middleware_originals SET body=NULL,key_id='' WHERE body IS NOT NULL
 AND authority IN (SELECT authority FROM middleware_scopes WHERE id IN (%[1]s))
 AND NOT EXISTS (SELECT 1 FROM middleware_choices c JOIN middleware_scopes s ON s.id=c.scope
  WHERE c.original=middleware_originals.digest AND s.authority=middleware_originals.authority AND s.expires_at>?1)`
	for _, batch := range []struct {
		selection  string
		statements []string
	}{
		{`SELECT id FROM middleware_scopes WHERE expires_at>0 AND expires_at<=?1 AND length(manifest)>0 LIMIT %d`, []string{
			unowned, unreferenced, `DELETE FROM middleware_plans WHERE scope IN (%[1]s)`,
			`UPDATE middleware_choices SET payload=x'',` + legacyChoice + `,ccr_handle='' WHERE scope IN (%[1]s) AND (length(payload)>0 OR ccr_handle<>'')`,
			`UPDATE middleware_scopes SET manifest=x'' WHERE id IN (%[1]s)`,
		}},
		{fmt.Sprintf(`SELECT id FROM middleware_scopes WHERE (expires_at>0 AND expires_at<=?1-%[1]d) OR (expires_at<=0 AND -expires_at<=?1-%[1]d) LIMIT %%d`, MiddlewareGraceSeconds), []string{
			unowned, `DELETE FROM middleware_plans WHERE scope IN (%[1]s)`, `DELETE FROM middleware_choices WHERE scope IN (%[1]s)`,
			`DELETE FROM middleware_scopes WHERE id IN (%[1]s)`,
		}},
	} {
		args, err := t.ids(fmt.Sprintf(batch.selection, limit), now)
		if err != nil {
			return total, full, err
		}
		if len(args) == 1 {
			continue
		}
		full = full || len(args)-1 == limit
		in := make([]string, len(args)-1)
		for i := range in {
			in[i] = fmt.Sprintf("?%d", i+2)
		}
		for _, statement := range batch.statements {
			if _, err := exec(fmt.Sprintf(statement, strings.Join(in, ",")), args...); err != nil {
				return total, full, err
			}
		}
	}
	for _, statement := range []string{
		`DELETE FROM middleware_receipts WHERE rowid IN (SELECT rowid FROM middleware_receipts WHERE expires_at<=? LIMIT %d)`,
		`DELETE FROM middleware_plans WHERE rowid IN (SELECT rowid FROM middleware_plans WHERE expires_at<=? LIMIT %d)`,
	} {
		n, err := exec(fmt.Sprintf(statement, limit), now)
		if err != nil {
			return total, full, err
		}
		full = full || n == int64(limit)
	}
	return total, full, nil
}

// ids returns now followed by the selected scope ids, ready to bind as ?1..?n+1.
func (t *sqliteMiddlewareTx) ids(query string, now int64) ([]any, error) {
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

func (t *sqliteMiddlewareTx) Receipt(authority, id, digest string, body []byte, expires int64) error {
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
	t.grew = true
	_, err = t.tx.ExecContext(t.ctx, `INSERT INTO middleware_receipts (authority,id,digest,payload,expires_at,principal) VALUES (?,?,?,?,?,?)`,
		authority, id, digest, body, expires, t.Principal)
	return err
}

func (t *sqliteMiddlewareTx) usage() (rows, bytes, ownRows, ownBytes int64, err error) {
	err = t.tx.QueryRowContext(t.ctx, `SELECT rows,bytes,
 coalesce((SELECT rows FROM middleware_principal_usage WHERE principal=?1),0),
 coalesce((SELECT bytes FROM middleware_principal_usage WHERE principal=?1),0)
FROM middleware_usage WHERE singleton=1`, t.Principal).Scan(&rows, &bytes, &ownRows, &ownBytes)
	return rows, bytes, ownRows, ownBytes, err
}
