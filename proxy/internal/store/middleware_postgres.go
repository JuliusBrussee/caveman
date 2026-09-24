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

func (p *PostgresMiddleware) Close()           { p.pool.Close() }
func (p *PostgresMiddleware) Persistent() bool { return true }

// postgresMiddlewareVersion gates the DDL below. Bump it with every schema
// change and make the change idempotent: replicas of both versions can start at
// once during a rolling update.
const postgresMiddlewareVersion = 1

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
	usageStripes-1, usageStripes) + postgresMiddlewareTriggers()

// postgresMiddlewareTriggers maintains the counters the way SQLite's triggers
// do, but once per statement over its transition tables: a row trigger would
// update the same stripe row once per row, and a bulk delete or a large seed in
// one transaction then walks an ever longer row-version chain. Updates that
// change no size (renewal, revocation) touch no counter. The sizes are
// middlewareSizes, SQLite's.
func postgresMiddlewareTriggers() string {
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
    PERFORM middleware_count(d.principal, 0, sum(d.size)) FROM (
      SELECT n.principal, %[2]s AS size FROM new_rows n UNION ALL SELECT o.principal, -%[3]s FROM old_rows o) d
    GROUP BY d.principal HAVING sum(d.size) <> 0;
  END IF;
  RETURN NULL;
END
$fn$;
CREATE TRIGGER %[1]s_account_insert AFTER INSERT ON %[1]s REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION %[1]s_account();
CREATE TRIGGER %[1]s_account_update AFTER UPDATE ON %[1]s REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION %[1]s_account();
CREATE TRIGGER %[1]s_account_delete AFTER DELETE ON %[1]s REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION %[1]s_account();`,
			table.table, newSize, oldSize)
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
	if _, err = tx.Exec(ctx, postgresMiddlewareSchema); err != nil {
		return err
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
	var old int
	err := t.tx.QueryRow(t.ctx, `SELECT length(manifest) FROM middleware_scopes WHERE id=$1`, s.ID).Scan(&old)
	rows := 0
	if errors.Is(err, pgx.ErrNoRows) {
		rows = 1
	} else if err != nil {
		return err
	}
	if err := t.capacity(len(s.Manifest)-old, rows); err != nil {
		return err
	}
	_, err = t.tx.Exec(t.ctx, `INSERT INTO middleware_scopes(id,authority,manifest,sequence,expires_at,created_at,principal) VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT(id) DO UPDATE SET manifest=excluded.manifest,sequence=excluded.sequence,expires_at=excluded.expires_at`,
		s.ID, s.Authority, s.Manifest, s.Sequence, s.ExpiresAt, s.CreatedAt, t.Principal)
	return err
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
	if err := t.capacity(len(body), 1); err != nil {
		return err
	}
	_, err := t.tx.Exec(t.ctx, `INSERT INTO middleware_plans(scope,id,digest,payload,expires_at,principal) VALUES ($1,$2,$3,$4,$5,$6)`,
		scope, id, digest, body, expires, t.Principal)
	return err
}

func (t *postgresMiddlewareTx) Choice(scope, id string) ([]byte, string, error) {
	var body []byte
	var handle string
	err := t.tx.QueryRow(t.ctx, `SELECT payload,ccr_handle FROM middleware_choices WHERE scope=$1 AND id=$2`, scope, id).Scan(&body, &handle)
	return body, handle, err
}

func (t *postgresMiddlewareTx) SaveChoice(scope, id, grant, handle string, body []byte) error {
	if err := t.capacity(len(body), 1); err != nil {
		return err
	}
	_, err := t.tx.Exec(t.ctx, `INSERT INTO middleware_choices(scope,id,payload,grant_id,ccr_handle,principal) VALUES ($1,$2,$3,$4,$5,$6)`,
		scope, id, body, grant, handle, t.Principal)
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
	if err := t.tx.QueryRow(t.ctx, `SELECT count(*) FROM middleware_choices WHERE ccr_handle<>'' AND scope IN (`+scopes+`)`, authority).Scan(&out.Legacy); err != nil {
		return out, err
	}
	// The same steps, in the same order, as SQLite's Delete.
	for _, step := range []struct {
		count     *int64
		statement string
		args      []any
	}{
		{&out.Scopes, `UPDATE middleware_scopes SET expires_at=$1 WHERE authority=$2 AND expires_at>0`, []any{-now, authority}},
		{&out.Choices, `UPDATE middleware_choices SET payload=''::bytea,ccr_handle='' WHERE (length(payload)>0 OR ccr_handle<>'') AND scope IN (` + scopes + `)`, []any{authority}},
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

// Expire runs SQLite's three batches (see sqliteMiddlewareTx.Expire). Only the
// replica holding the sweep lock works; the others return 0 and end their pass.
// A batch keeps only scopes it can lock without waiting, both the row and its
// authority, so it never deletes under a writer that is renewing or reviving
// that data; skipped scopes wait for the next pass.
func (t *postgresMiddlewareTx) Expire(now int64) (int64, error) {
	var sweeping bool
	if err := t.tx.QueryRow(t.ctx, `SELECT pg_try_advisory_xact_lock(`+lockKey+`)`, "sweep").Scan(&sweeping); err != nil || !sweeping {
		return 0, err
	}
	var total int64
	exec := func(statement string, args ...any) error {
		tag, err := t.tx.Exec(t.ctx, statement, args...)
		total += tag.RowsAffected()
		return err
	}
	for _, batch := range []struct{ selection, last string }{
		{`SELECT id,authority FROM middleware_scopes WHERE expires_at>0 AND expires_at<=$1 AND length(manifest)>0 LIMIT 128 FOR UPDATE SKIP LOCKED`,
			`UPDATE middleware_scopes SET manifest=''::bytea WHERE id=ANY($1)`},
		{fmt.Sprintf(`SELECT id,authority FROM middleware_scopes WHERE (expires_at>0 AND expires_at<=$1-%[1]d) OR (expires_at<=0 AND -expires_at<=$1-%[1]d) LIMIT 128 FOR UPDATE SKIP LOCKED`, MiddlewareGraceSeconds),
			`DELETE FROM middleware_scopes WHERE id=ANY($1)`},
	} {
		ids, authorities, err := t.sweepable(batch.selection, now)
		if err != nil {
			return total, err
		}
		if len(ids) == 0 {
			continue
		}
		if err := exec(`DELETE FROM middleware_originals WHERE authority=ANY($1)
 AND NOT EXISTS (SELECT 1 FROM middleware_scopes s WHERE s.authority=middleware_originals.authority AND s.expires_at>$2)`, authorities, now); err != nil {
			return total, err
		}
		for _, statement := range []string{`DELETE FROM middleware_plans WHERE scope=ANY($1)`, `DELETE FROM middleware_choices WHERE scope=ANY($1)`, batch.last} {
			if err := exec(statement, ids); err != nil {
				return total, err
			}
		}
	}
	for _, statement := range []string{
		`DELETE FROM middleware_receipts WHERE (authority,id) IN (SELECT authority,id FROM middleware_receipts WHERE expires_at<=$1 LIMIT 128 FOR UPDATE SKIP LOCKED)`,
		`DELETE FROM middleware_plans WHERE (scope,id) IN (SELECT scope,id FROM middleware_plans WHERE expires_at<=$1 LIMIT 128 FOR UPDATE SKIP LOCKED)`,
	} {
		if err := exec(statement, now); err != nil {
			return total, err
		}
	}
	return total, nil
}

// sweepable selects a batch and keeps the scopes whose authority it can lock
// without waiting.
func (t *postgresMiddlewareTx) sweepable(selection string, now int64) (ids, authorities []string, err error) {
	rows, err := t.tx.Query(t.ctx, selection, now)
	if err != nil {
		return nil, nil, err
	}
	type candidate struct{ id, authority string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.authority); err != nil {
			rows.Close()
			return nil, nil, err
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	held := map[string]bool{}
	for _, c := range candidates {
		ok, seen := held[c.authority]
		if !seen {
			if ok = t.locked["authority:"+c.authority]; !ok {
				if err := t.tx.QueryRow(t.ctx, `SELECT pg_try_advisory_xact_lock(`+lockKey+`)`, "authority:"+c.authority).Scan(&ok); err != nil {
					return nil, nil, err
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
	return ids, authorities, nil
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
	if err := t.capacity(len(body), 1); err != nil {
		return err
	}
	_, err = t.tx.Exec(t.ctx, `INSERT INTO middleware_receipts (authority,id,digest,payload,expires_at,principal) VALUES ($1,$2,$3,$4,$5,$6)`,
		authority, id, digest, body, expires, t.Principal)
	return err
}

func (t *postgresMiddlewareTx) SaveOriginal(authority, digest string, body []byte, keyID string) (bool, error) {
	if err := t.lock("authority:" + authority); err != nil {
		return false, err
	}
	var stored bool
	err := t.tx.QueryRow(t.ctx, `SELECT body IS NOT NULL FROM middleware_originals WHERE authority=$1 AND digest=$2`, authority, digest).Scan(&stored)
	if err == nil {
		if stored || body == nil {
			return false, nil
		}
		if err := t.capacity(len(body), 0); err != nil {
			return false, err
		}
		_, err = t.tx.Exec(t.ctx, `UPDATE middleware_originals SET body=$1,key_id=$2 WHERE authority=$3 AND digest=$4`, body, keyID, authority, digest)
		return false, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if err = t.capacity(64+len(body), 1); err != nil {
		return false, err
	}
	_, err = t.tx.Exec(t.ctx, `INSERT INTO middleware_originals(authority,digest,body,key_id,principal) VALUES ($1,$2,$3,$4,$5)`,
		authority, digest, body, keyID, t.Principal)
	return err == nil, err
}

func (t *postgresMiddlewareTx) Original(authority, digest string) ([]byte, string, error) {
	var body []byte
	var keyID string
	err := t.tx.QueryRow(t.ctx, `SELECT body,key_id FROM middleware_originals WHERE authority=$1 AND digest=$2 AND body IS NOT NULL`,
		authority, digest).Scan(&body, &keyID)
	return body, keyID, err
}

func (t *postgresMiddlewareTx) HasOriginal(authority, digest string) (bool, error) {
	var found int
	err := t.tx.QueryRow(t.ctx, `SELECT 1 FROM middleware_originals WHERE authority=$1 AND digest=$2 AND body IS NOT NULL`,
		authority, digest).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (t *postgresMiddlewareTx) capacity(extra, rows int) error {
	return admit(t.Limits, t.Principal, extra, rows, func() (count, size int64, err error) {
		err = t.tx.QueryRow(t.ctx, `SELECT coalesce(sum(rows),0)::BIGINT, coalesce(sum(bytes),0)::BIGINT FROM middleware_usage`).Scan(&count, &size)
		return count, size, err
	}, func() (count, size int64, err error) {
		err = t.tx.QueryRow(t.ctx, `SELECT coalesce(sum(rows),0)::BIGINT, coalesce(sum(bytes),0)::BIGINT FROM middleware_principal_usage WHERE principal=$1`, t.Principal).Scan(&count, &size)
		return count, size, err
	})
}
