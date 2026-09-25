package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/JuliusBrussee/caveman/shared/platform/postgresconfig"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresMiddleware is the multi-replica middleware store. Every replica runs
// against one database; the semantics the runtime relies on are SQLite's:
//
//   - Writers of one authority run one at a time on any replica. SQLite has one
//     writer per file; here a transaction-scoped advisory lock per authority
//     stands in for it. Scope takes it (re-reading afterwards, so a decision
//     never rests on a read that predates a competing commit), as do SaveScope,
//     Delete, Receipt and SaveOriginal. Every write path the runtime runs starts
//     with one of them. A scope that does not exist yet is locked on its id
//     until SaveScope names its authority. Different authorities write in
//     parallel.
//   - Renew takes only row locks: it extends live, unpurged scopes and nothing
//     else, so it cannot resurrect a revoked or swept one.
//   - One replica sweeps at a time (a try-lock; the others skip the pass), and
//     the sweep skips any scope row or authority another transaction holds.
//   - Admission counters are trigger-maintained like SQLite's, striped by
//     transaction id so writers of different authorities do not queue on one
//     counter row. Concurrent admissions read committed totals, so replicas can
//     overshoot a limit by what they have in flight at that moment.
//
// Deadlocks, serialization failures and unique violations surface as
// ErrMiddlewareConflict (identity_conflict: retryable).
type PostgresMiddleware struct {
	pool *pgxpool.Pool
}

// OpenPostgresMiddleware connects through postgresconfig, so CAVE_POSTGRES_CA_CERT
// / CAVE_POSTGRES_CA_CERT_FILE and the CAVE_ENV=prod verify-full rule apply.
// The connection is not checked here; InitMiddleware is the first round trip.
func OpenPostgresMiddleware(ctx context.Context, databaseURL string) (*PostgresMiddleware, error) {
	pool, err := postgresconfig.NewPool(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	return &PostgresMiddleware{pool: pool}, nil
}

// NewPostgresMiddleware wraps an existing pool, for callers that configure it
// themselves (a query tracer, say).
func NewPostgresMiddleware(pool *pgxpool.Pool) *PostgresMiddleware {
	return &PostgresMiddleware{pool: pool}
}

func (p *PostgresMiddleware) Close()           { p.pool.Close() }
func (p *PostgresMiddleware) Persistent() bool { return true }

// postgresMiddlewareVersion gates the DDL below: postgresMiddlewareSchema is
// version 1, postgresMiddlewareMigrations[i] takes a store to version i+2. Add
// a migration with every schema change and make it idempotent: replicas of both
// versions can start at once during a rolling update.
var postgresMiddlewareVersion = 1 + len(postgresMiddlewareMigrations)

// lockKey is the advisory lock for key $1. Advisory locks are database-wide, so
// the key includes the schema: deployments sharing one database stay independent.
const lockKey = `hashtextextended(current_schema()||'/caveman.middleware.'||$1,0)`

// usageStripes spreads the admission counters: a transaction updates stripe
// txid % usageStripes, and totals are sums.
const usageStripes = 16

var postgresMiddlewareSchema = fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS middleware_scopes (
  id TEXT PRIMARY KEY, authority TEXT NOT NULL, manifest BYTEA NOT NULL, sequence BIGINT NOT NULL,
  expires_at BIGINT NOT NULL, created_at BIGINT NOT NULL DEFAULT 0, principal TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS middleware_scopes_authority ON middleware_scopes(authority);
CREATE INDEX IF NOT EXISTS middleware_scopes_expiry ON middleware_scopes(expires_at);
CREATE INDEX IF NOT EXISTS middleware_scopes_unpurged ON middleware_scopes(expires_at) WHERE length(manifest)>0;
CREATE TABLE IF NOT EXISTS middleware_choices (
  scope TEXT NOT NULL, id TEXT NOT NULL, payload BYTEA NOT NULL, grant_id TEXT NOT NULL UNIQUE,
  ccr_handle TEXT NOT NULL, principal TEXT NOT NULL DEFAULT '', PRIMARY KEY(scope,id)
);
CREATE TABLE IF NOT EXISTS middleware_plans (
  scope TEXT NOT NULL, id TEXT NOT NULL, digest TEXT NOT NULL, payload BYTEA NOT NULL,
  expires_at BIGINT NOT NULL DEFAULT 0, principal TEXT NOT NULL DEFAULT '', PRIMARY KEY(scope,id)
);
CREATE INDEX IF NOT EXISTS middleware_plans_expiry ON middleware_plans(expires_at);
CREATE TABLE IF NOT EXISTS middleware_receipts (
  authority TEXT NOT NULL, id TEXT NOT NULL, digest TEXT NOT NULL, payload BYTEA NOT NULL,
  expires_at BIGINT NOT NULL DEFAULT 0, principal TEXT NOT NULL DEFAULT '', PRIMARY KEY(authority,id)
);
CREATE INDEX IF NOT EXISTS middleware_receipts_expiry ON middleware_receipts(expires_at);
CREATE TABLE IF NOT EXISTS middleware_originals (
  authority TEXT NOT NULL, digest TEXT NOT NULL, body BYTEA, key_id TEXT NOT NULL DEFAULT '',
  principal TEXT NOT NULL DEFAULT '', PRIMARY KEY(authority,digest)
);
CREATE TABLE IF NOT EXISTS middleware_usage (
  stripe INT PRIMARY KEY, rows BIGINT NOT NULL, bytes BIGINT NOT NULL
);
INSERT INTO middleware_usage SELECT g,0,0 FROM generate_series(0,%[1]d) g ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS middleware_principal_usage (
  principal TEXT NOT NULL, stripe INT NOT NULL, rows BIGINT NOT NULL, bytes BIGINT NOT NULL, PRIMARY KEY(principal,stripe)
);
-- middleware_count applies one statement's delta for one principal to this
-- transaction's stripe. An empty principal (none stamped) counts only globally.
CREATE OR REPLACE FUNCTION middleware_count(owner TEXT, counted BIGINT, delta BIGINT) RETURNS void LANGUAGE sql AS $fn$
  UPDATE middleware_usage SET rows = rows + counted, bytes = bytes + delta WHERE stripe = (txid_current() %% %[2]d)::INT;
  INSERT INTO middleware_principal_usage AS u SELECT owner, (txid_current() %% %[2]d)::INT, counted, delta WHERE owner <> ''
  ON CONFLICT (principal, stripe) DO UPDATE SET rows = u.rows + EXCLUDED.rows, bytes = u.bytes + EXCLUDED.bytes;
$fn$;`,
	usageStripes-1, usageStripes) + postgresMiddlewareAccounting() + postgresMiddlewareTriggers()

// postgresMiddlewareMigrations[i] takes a store from version i+1 to i+2.
var postgresMiddlewareMigrations = []string{
	// 2: choices name their original (expiry reclaims each original with the
	// last scope referencing it) and keep a legacy flag after their CCR handle
	// is blanked; an update that stamps a principal on a row moves its row count
	// too (a protocol 1.0 credit getting its body).
	`ALTER TABLE middleware_choices ADD COLUMN IF NOT EXISTS original TEXT NOT NULL DEFAULT '';
ALTER TABLE middleware_choices ADD COLUMN IF NOT EXISTS legacy INT NOT NULL DEFAULT 0;
UPDATE middleware_choices SET original=substring(encode(payload,'escape') from '"original_sha256":"([0-9a-f]{64})"')
WHERE original='' AND ccr_handle='' AND encode(payload,'escape') ~ '"original_sha256":"[0-9a-f]{64}"';
CREATE INDEX IF NOT EXISTS middleware_choices_original ON middleware_choices(original);` + postgresMiddlewareAccounting(),
}

// postgresMiddlewareAccounting maintains the counters the way SQLite's triggers
// do, but once per statement over its transition tables: a row trigger would
// update the same stripe row once per row, and a bulk delete or a large seed in
// one transaction then walks an ever longer row-version chain. Updates that
// change no size or principal (renewal, revocation) touch no counter. The sizes
// are middlewareSizes, SQLite's.
func postgresMiddlewareAccounting() string {
	var b strings.Builder
	for _, table := range middlewareSizes {
		newSize, oldSize := fmt.Sprintf(table.size, "n"), fmt.Sprintf(table.size, "o")
		fmt.Fprintf(&b, `
CREATE OR REPLACE FUNCTION %[1]s_account() RETURNS trigger LANGUAGE plpgsql AS $fn$
BEGIN
  IF TG_OP = 'INSERT' THEN
    PERFORM middleware_count(n.principal, count(*), sum(%[2]s)) FROM new_rows n GROUP BY n.principal;
  ELSIF TG_OP = 'DELETE' THEN
    PERFORM middleware_count(o.principal, -count(*), -sum(%[3]s)) FROM old_rows o GROUP BY o.principal;
  ELSE
    PERFORM middleware_count(d.principal, sum(d.counted), sum(d.size)) FROM (
      SELECT n.principal, 1 AS counted, %[2]s AS size FROM new_rows n UNION ALL SELECT o.principal, -1, -%[3]s FROM old_rows o) d
    GROUP BY d.principal HAVING sum(d.size) <> 0 OR sum(d.counted) <> 0;
  END IF;
  RETURN NULL;
END
$fn$;`, table.table, newSize, oldSize)
	}
	return b.String()
}

// postgresMiddlewareTriggers attaches postgresMiddlewareAccounting's functions.
func postgresMiddlewareTriggers() string {
	var b strings.Builder
	for _, table := range middlewareSizes {
		fmt.Fprintf(&b, `
CREATE TRIGGER %[1]s_account_insert AFTER INSERT ON %[1]s REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION %[1]s_account();
CREATE TRIGGER %[1]s_account_update AFTER UPDATE ON %[1]s REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION %[1]s_account();
CREATE TRIGGER %[1]s_account_delete AFTER DELETE ON %[1]s REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION %[1]s_account();`,
			table.table)
	}
	return b.String()
}

// InitMiddleware migrates under an advisory lock, so replicas starting together
// run it one after another; the later ones find the version current and skip.
func (p *PostgresMiddleware) InitMiddleware(ctx context.Context) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(`+lockKey+`)`, "schema"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS middleware_schema (version INT NOT NULL)`); err != nil {
		return err
	}
	var version int
	if err = tx.QueryRow(ctx, `SELECT coalesce(max(version),0) FROM middleware_schema`).Scan(&version); err != nil {
		return err
	}
	if version >= postgresMiddlewareVersion {
		return tx.Commit(ctx)
	}
	if version < 1 {
		if _, err = tx.Exec(ctx, postgresMiddlewareSchema); err != nil {
			return err
		}
	}
	for _, migration := range postgresMiddlewareMigrations[max(version-1, 0):] {
		if _, err = tx.Exec(ctx, migration); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO middleware_schema VALUES ($1)`, postgresMiddlewareVersion); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *PostgresMiddleware) ReadMiddleware(ctx context.Context, fn func(*MiddlewareTx) error) error {
	return p.run(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, false, fn)
}

func (p *PostgresMiddleware) WithMiddleware(ctx context.Context, fn func(*MiddlewareTx) error) error {
	return p.run(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, true, fn)
}

func (p *PostgresMiddleware) run(ctx context.Context, options pgx.TxOptions, write bool, fn func(*MiddlewareTx) error) error {
	err := pgx.BeginTxFunc(ctx, p.pool, options, func(tx pgx.Tx) error {
		m := &MiddlewareTx{}
		m.middlewareTxOps = &postgresMiddlewareTx{MiddlewareTx: m, tx: tx, ctx: ctx, write: write, locked: map[string]bool{}}
		if err := fn(m); err != nil {
			return err
		}
		if err := m.admitted(); err != nil {
			return err
		}
		return ctx.Err()
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "40P01" || pgErr.Code == "40001" || pgErr.Code == "23505") {
		return errors.Join(ErrMiddlewareConflict, err)
	}
	return err
}

// MiddlewareWritable assigns a transaction id, which a read-only or recovering
// (standby) server refuses, without touching a middleware row.
func (p *PostgresMiddleware) MiddlewareWritable(ctx context.Context) error {
	return pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT txid_current()`)
		return err
	})
}

func (p *PostgresMiddleware) MiddlewareUsage(ctx context.Context) (rows, bytes int64, err error) {
	err = p.pool.QueryRow(ctx, `SELECT coalesce(sum(rows),0)::BIGINT, coalesce(sum(bytes),0)::BIGINT FROM middleware_usage`).Scan(&rows, &bytes)
	return rows, bytes, err
}

func (p *PostgresMiddleware) MiddlewareKeys(ctx context.Context) (map[string]int64, error) {
	rows, err := p.pool.Query(ctx, `SELECT key_id,count(*) FROM middleware_originals WHERE body IS NOT NULL GROUP BY key_id`)
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

func (p *PostgresMiddleware) MiddlewareBacklog(ctx context.Context, now int64) (n int64, err error) {
	err = p.pool.QueryRow(ctx, middlewareBacklog("$1"), now).Scan(&n)
	return n, err
}

type postgresMiddlewareTx struct {
	*MiddlewareTx
	tx     pgx.Tx
	ctx    context.Context
	write  bool
	locked map[string]bool
}

// lock takes a transaction-scoped advisory lock once per key; read snapshots
// never lock.
func (t *postgresMiddlewareTx) lock(key string) error {
	if !t.write || t.locked[key] {
		return nil
	}
	if _, err := t.tx.Exec(t.ctx, `SELECT pg_advisory_xact_lock(`+lockKey+`)`, key); err != nil {
		return err
	}
	t.locked[key] = true
	return nil
}

func (t *postgresMiddlewareTx) scope(id string) (MiddlewareScope, error) {
	s := MiddlewareScope{ID: id}
	err := t.tx.QueryRow(t.ctx, `SELECT authority,manifest,sequence,expires_at,created_at FROM middleware_scopes WHERE id=$1`, id).
		Scan(&s.Authority, &s.Manifest, &s.Sequence, &s.ExpiresAt, &s.CreatedAt)
	return s, err
}

func (t *postgresMiddlewareTx) Scope(id string) (MiddlewareScope, error) {
	s, err := t.scope(id)
	if !t.write {
		return s, err
	}
	for {
		key := "scope:" + id
		if err == nil {
			key = "authority:" + s.Authority
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return s, err
		}
		if t.locked[key] {
			return s, err
		}
		if err := t.lock(key); err != nil {
			return s, err
		}
		// Re-read under the lock; a scope created meanwhile loops once more to
		// lock its authority.
		s, err = t.scope(id)
	}
}

func (t *postgresMiddlewareTx) SaveScope(s MiddlewareScope) error {
	if err := t.lock("authority:" + s.Authority); err != nil {
		return err
	}
	t.grew = true
	_, err := t.tx.Exec(t.ctx, `INSERT INTO middleware_scopes(id,authority,manifest,sequence,expires_at,created_at,principal) VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT(id) DO UPDATE SET manifest=excluded.manifest,sequence=excluded.sequence,expires_at=excluded.expires_at`,
		s.ID, s.Authority, s.Manifest, s.Sequence, s.ExpiresAt, s.CreatedAt, t.Principal)
	return err
}

// Revoked takes the authority's lock first, so a delete cannot commit between
// this answer and the scope a writer then creates.
func (t *postgresMiddlewareTx) Revoked(authority string) (revoked bool, err error) {
	if err := t.lock("authority:" + authority); err != nil {
		return false, err
	}
	err = t.tx.QueryRow(t.ctx, `SELECT EXISTS(SELECT 1 FROM middleware_scopes WHERE authority=$1 AND expires_at<=0)`, authority).Scan(&revoked)
	return revoked, err
}

func (t *postgresMiddlewareTx) Plan(scope, id, digest string) ([]byte, error) {
	var storedDigest string
	var body []byte
	err := t.tx.QueryRow(t.ctx, `SELECT digest,payload FROM middleware_plans WHERE scope=$1 AND id=$2`, scope, id).Scan(&storedDigest, &body)
	if err == nil && storedDigest != digest {
		return nil, ErrMiddlewareConflict
	}
	return body, err
}

func (t *postgresMiddlewareTx) SavePlan(scope, id, digest string, body []byte, expires int64) error {
	t.grew = true
	_, err := t.tx.Exec(t.ctx, `INSERT INTO middleware_plans(scope,id,digest,payload,expires_at,principal) VALUES ($1,$2,$3,$4,$5,$6)`,
		scope, id, digest, body, expires, t.Principal)
	return err
}

func (t *postgresMiddlewareTx) Choices(scope string, ids []string) (map[string]MiddlewareChoice, error) {
	found := map[string]MiddlewareChoice{}
	if len(ids) == 0 {
		return found, nil
	}
	rows, err := t.tx.Query(t.ctx, `SELECT id,payload,grant_id,ccr_handle,original FROM middleware_choices WHERE scope=$1 AND id=ANY($2)`, scope, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c MiddlewareChoice
		if err := rows.Scan(&c.ID, &c.Payload, &c.Grant, &c.Handle, &c.Original); err != nil {
			return nil, err
		}
		found[c.ID] = c
	}
	return found, rows.Err()
}

func (t *postgresMiddlewareTx) SaveChoices(scope string, choices []MiddlewareChoice) error {
	t.grew = true
	var ids, grants, handles, originals []string
	var payloads [][]byte
	for _, c := range choices {
		ids, grants, handles, originals, payloads = append(ids, c.ID), append(grants, c.Grant), append(handles, c.Handle), append(originals, c.Original), append(payloads, c.Payload)
	}
	_, err := t.tx.Exec(t.ctx, `INSERT INTO middleware_choices(scope,id,payload,grant_id,ccr_handle,original,principal)
SELECT $1,c.id,c.payload,c.grant_id,c.handle,c.original,$7 FROM unnest($2::text[],$3::bytea[],$4::text[],$5::text[],$6::text[]) AS c(id,payload,grant_id,handle,original)`,
		scope, ids, payloads, grants, handles, originals, t.Principal)
	return err
}

func (t *postgresMiddlewareTx) Grant(authority, grant string) ([]byte, string, int64, error) {
	var body []byte
	var handle string
	var expires int64
	err := t.tx.QueryRow(t.ctx, `SELECT c.payload,c.ccr_handle,s.expires_at
FROM middleware_choices c JOIN middleware_scopes s ON c.scope=s.id
WHERE s.authority=$1 AND c.grant_id=$2`, authority, grant).Scan(&body, &handle, &expires)
	return body, handle, expires, err
}

// Renew is SQLite's, plus length(manifest)>0: a scope the sweep purged while
// this replica's clock still called it live stays purged.
func (t *postgresMiddlewareTx) Renew(authority string, now, retention, maxRetention int64) error {
	_, err := t.tx.Exec(t.ctx, `UPDATE middleware_scopes
SET expires_at=CASE WHEN created_at>0 THEN least($1+$2, created_at+$3) ELSE $1+$2 END
WHERE authority=$4 AND expires_at>$1 AND length(manifest)>0`, now, retention, maxRetention, authority)
	return err
}

func (t *postgresMiddlewareTx) Delete(authority string, now int64) (MiddlewareDeleted, error) {
	var out MiddlewareDeleted
	if err := t.lock("authority:" + authority); err != nil {
		return out, err
	}
	const scopes = `SELECT id FROM middleware_scopes WHERE authority=$1`
	if err := t.tx.QueryRow(t.ctx, `SELECT count(*) FROM middleware_choices WHERE (ccr_handle<>'' OR legacy<>0) AND scope IN (`+scopes+`)`, authority).Scan(&out.Legacy); err != nil {
		return out, err
	}
	// The same steps, in the same order, as SQLite's Delete.
	for _, step := range []struct {
		count     *int64
		statement string
		args      []any
	}{
		{&out.Scopes, `UPDATE middleware_scopes SET expires_at=$1 WHERE authority=$2 AND expires_at>0`, []any{-now, authority}},
		{&out.Choices, `UPDATE middleware_choices SET payload=''::bytea,` + legacyChoice + `,ccr_handle='' WHERE (length(payload)>0 OR ccr_handle<>'') AND scope IN (` + scopes + `)`, []any{authority}},
		{&out.Originals, `DELETE FROM middleware_originals WHERE authority=$1 AND body IS NOT NULL`, []any{authority}},
		{nil, `DELETE FROM middleware_originals WHERE authority=$1`, []any{authority}},
		{nil, `DELETE FROM middleware_receipts WHERE authority=$1`, []any{authority}},
		{nil, `DELETE FROM middleware_plans WHERE scope IN (` + scopes + `)`, []any{authority}},
		{nil, `UPDATE middleware_scopes SET manifest=''::bytea WHERE authority=$1`, []any{authority}},
	} {
		tag, err := t.tx.Exec(t.ctx, step.statement, step.args...)
		if err != nil {
			return out, err
		}
		if step.count != nil {
			*step.count = tag.RowsAffected()
		}
	}
	return out, nil
}

// ExpireBatch runs SQLite's three batches (see sqliteMiddlewareTx.ExpireBatch).
// Only the replica holding the sweep lock works; the others return 0 and end
// their pass. A batch keeps only scopes it can lock without waiting, both the
// row and its authority, so it never deletes under a writer that is renewing or
// reviving that data; skipped scopes wait for the next pass.
func (t *postgresMiddlewareTx) ExpireBatch(now int64, limit int) (int64, bool, error) {
	var sweeping bool
	if err := t.tx.QueryRow(t.ctx, `SELECT pg_try_advisory_xact_lock(`+lockKey+`)`, "sweep").Scan(&sweeping); err != nil || !sweeping {
		return 0, false, err
	}
	var total int64
	full := false
	exec := func(statement string, args ...any) (int64, error) {
		tag, err := t.tx.Exec(t.ctx, statement, args...)
		total += tag.RowsAffected()
		return tag.RowsAffected(), err
	}
	for _, batch := range []struct {
		selection string
		purge     bool
		last      string
	}{
		{`SELECT id,authority FROM middleware_scopes WHERE expires_at>0 AND expires_at<=$1 AND length(manifest)>0 LIMIT %d FOR UPDATE SKIP LOCKED`, true,
			`UPDATE middleware_scopes SET manifest=''::bytea WHERE id=ANY($1)`},
		{fmt.Sprintf(`SELECT id,authority FROM middleware_scopes WHERE (expires_at>0 AND expires_at<=$1-%[1]d) OR (expires_at<=0 AND -expires_at<=$1-%[1]d) LIMIT %%d FOR UPDATE SKIP LOCKED`, MiddlewareGraceSeconds), false,
			`DELETE FROM middleware_scopes WHERE id=ANY($1)`},
	} {
		ids, authorities, selected, err := t.sweepable(fmt.Sprintf(batch.selection, limit), now)
		if err != nil {
			return total, full, err
		}
		full = full || selected == limit
		if len(ids) == 0 {
			continue
		}
		if _, err := exec(`DELETE FROM middleware_originals WHERE authority=ANY($1)
 AND NOT EXISTS (SELECT 1 FROM middleware_scopes s WHERE s.authority=middleware_originals.authority AND s.expires_at>$2)`, authorities, now); err != nil {
			return total, full, err
		}
		statements := []string{`DELETE FROM middleware_plans WHERE scope=ANY($1)`, `DELETE FROM middleware_choices WHERE scope=ANY($1)`, batch.last}
		if batch.purge {
			if _, err := exec(`UPDATE middleware_originals o SET body=NULL,key_id='' WHERE o.authority=ANY($1) AND o.body IS NOT NULL
 AND NOT EXISTS (SELECT 1 FROM middleware_choices c JOIN middleware_scopes s ON s.id=c.scope
  WHERE c.original=o.digest AND s.authority=o.authority AND s.expires_at>$2)`, authorities, now); err != nil {
				return total, full, err
			}
			statements[1] = `UPDATE middleware_choices SET payload=''::bytea,` + legacyChoice + `,ccr_handle='' WHERE scope=ANY($1) AND (length(payload)>0 OR ccr_handle<>'')`
		}
		for _, statement := range statements {
			if _, err := exec(statement, ids); err != nil {
				return total, full, err
			}
		}
	}
	for _, statement := range []string{
		`DELETE FROM middleware_receipts WHERE (authority,id) IN (SELECT authority,id FROM middleware_receipts WHERE expires_at<=$1 LIMIT %d FOR UPDATE SKIP LOCKED)`,
		`DELETE FROM middleware_plans WHERE (scope,id) IN (SELECT scope,id FROM middleware_plans WHERE expires_at<=$1 LIMIT %d FOR UPDATE SKIP LOCKED)`,
	} {
		n, err := exec(fmt.Sprintf(statement, limit), now)
		if err != nil {
			return total, full, err
		}
		full = full || n == int64(limit)
	}
	return total, full, nil
}

// sweepable selects a batch and keeps the scopes whose authority it can lock
// without waiting; selected is the batch's size before that.
func (t *postgresMiddlewareTx) sweepable(selection string, now int64) (ids, authorities []string, selected int, err error) {
	rows, err := t.tx.Query(t.ctx, selection, now)
	if err != nil {
		return nil, nil, 0, err
	}
	type candidate struct{ id, authority string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.authority); err != nil {
			rows.Close()
			return nil, nil, 0, err
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, 0, err
	}
	held := map[string]bool{}
	for _, c := range candidates {
		ok, seen := held[c.authority]
		if !seen {
			if ok = t.locked["authority:"+c.authority]; !ok {
				if err := t.tx.QueryRow(t.ctx, `SELECT pg_try_advisory_xact_lock(`+lockKey+`)`, "authority:"+c.authority).Scan(&ok); err != nil {
					return nil, nil, 0, err
				}
				t.locked["authority:"+c.authority] = ok
			}
			held[c.authority] = ok
			if ok {
				authorities = append(authorities, c.authority)
			}
		}
		if ok {
			ids = append(ids, c.id)
		}
	}
	return ids, authorities, len(candidates), nil
}

func (t *postgresMiddlewareTx) Receipt(authority, id, digest string, body []byte, expires int64) error {
	if err := t.lock("authority:" + authority); err != nil {
		return err
	}
	var old string
	err := t.tx.QueryRow(t.ctx, `SELECT digest FROM middleware_receipts WHERE authority=$1 AND id=$2`, authority, id).Scan(&old)
	if err == nil {
		if old != digest {
			return ErrMiddlewareConflict
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	t.grew = true
	_, err = t.tx.Exec(t.ctx, `INSERT INTO middleware_receipts (authority,id,digest,payload,expires_at,principal) VALUES ($1,$2,$3,$4,$5,$6)`,
		authority, id, digest, body, expires, t.Principal)
	return err
}

// Originals takes the authority's lock in a write transaction: SaveOriginals
// decides on what it returns.
func (t *postgresMiddlewareTx) Originals(authority string, digests []string) (map[string]StoredOriginal, error) {
	found := map[string]StoredOriginal{}
	if len(digests) == 0 {
		return found, nil
	}
	if err := t.lock("authority:" + authority); err != nil {
		return nil, err
	}
	rows, err := t.tx.Query(t.ctx, `SELECT digest,key_id,body IS NOT NULL FROM middleware_originals WHERE authority=$1 AND digest=ANY($2)`, authority, digests)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var digest string
		var o StoredOriginal
		if err := rows.Scan(&digest, &o.KeyID, &o.Body); err != nil {
			return nil, err
		}
		found[digest] = o
	}
	return found, rows.Err()
}

func (t *postgresMiddlewareTx) insertOriginals(authority string, originals []MiddlewareOriginal) error {
	t.grew = true
	digests, keys, bodies := columns(originals)
	_, err := t.tx.Exec(t.ctx, `INSERT INTO middleware_originals(authority,digest,body,key_id,principal)
SELECT $1,o.digest,o.body,o.key_id,$5 FROM unnest($2::text[],$3::bytea[],$4::text[]) AS o(digest,body,key_id)`, authority, digests, bodies, keys, t.Principal)
	return err
}

// updateOriginals stamps the principal as well: see sqliteMiddlewareTx's.
func (t *postgresMiddlewareTx) updateOriginals(authority string, originals []MiddlewareOriginal) error {
	t.grew = true
	digests, keys, bodies := columns(originals)
	_, err := t.tx.Exec(t.ctx, `UPDATE middleware_originals o SET body=u.body,key_id=u.key_id,principal=$5
FROM unnest($2::text[],$3::bytea[],$4::text[]) AS u(digest,body,key_id) WHERE o.authority=$1 AND o.digest=u.digest`, authority, digests, bodies, keys, t.Principal)
	return err
}

func columns(originals []MiddlewareOriginal) (digests, keys []string, bodies [][]byte) {
	for _, o := range originals {
		digests, keys, bodies = append(digests, o.Digest), append(keys, o.KeyID), append(bodies, o.Body)
	}
	return digests, keys, bodies
}

func (t *postgresMiddlewareTx) Original(authority, digest string) ([]byte, string, error) {
	var body []byte
	var keyID string
	err := t.tx.QueryRow(t.ctx, `SELECT body,key_id FROM middleware_originals WHERE authority=$1 AND digest=$2 AND body IS NOT NULL`,
		authority, digest).Scan(&body, &keyID)
	return body, keyID, err
}

func (t *postgresMiddlewareTx) usage() (rows, bytes, ownRows, ownBytes int64, err error) {
	err = t.tx.QueryRow(t.ctx, `SELECT coalesce(sum(rows),0)::BIGINT, coalesce(sum(bytes),0)::BIGINT,
 (SELECT coalesce(sum(rows),0)::BIGINT FROM middleware_principal_usage WHERE principal=$1),
 (SELECT coalesce(sum(bytes),0)::BIGINT FROM middleware_principal_usage WHERE principal=$1)
FROM middleware_usage`, t.Principal).Scan(&rows, &bytes, &ownRows, &ownBytes)
	return rows, bytes, ownRows, ownBytes, err
}
