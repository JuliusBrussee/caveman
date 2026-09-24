package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func TestMiddlewareQuotaCountersPersistAndRollback(t *testing.T) {
	eachMiddlewareBackend(t, testMiddlewareQuotaCountersPersistAndRollback)
}

func testMiddlewareQuotaCountersPersistAndRollback(t *testing.T, s middlewareBackend) {
	ctx := context.Background()
	err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
		if err := tx.SaveScope(MiddlewareScope{ID: "scope", Authority: "auth", Manifest: []byte("[]"), ExpiresAt: 100}); err != nil {
			return err
		}
		if err := tx.SaveChoice("scope", "choice", "grant", "ccr", make([]byte, 256)); err != nil {
			return err
		}
		if err := tx.SavePlan("scope", "plan", "hash", make([]byte, 128), 1<<40); err != nil {
			return err
		}
		if err := tx.Receipt("auth", "receipt", "hash", make([]byte, 64), 1<<40); err != nil {
			return err
		}
		credit, err := tx.CreditOriginal("auth", "original")
		if !credit {
			t.Error("first original was not credited")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	check := func(rows, size int64) {
		t.Helper()
		gotRows, gotSize, err := s.MiddlewareUsage(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if gotRows != rows || gotSize != size {
			t.Fatalf("quota counters=(%d,%d), want (%d,%d)", gotRows, gotSize, rows, size)
		}
	}
	check(5, 514)
	abort := errors.New("rollback")
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
		if err := tx.SaveChoice("scope", "other", "other-grant", "ccr", make([]byte, 32)); err != nil {
			return err
		}
		return abort
	}); !errors.Is(err, abort) {
		t.Fatal(err)
	}
	check(5, 514)
	other := s.reopen(t)
	if err := other.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
		credit, err := tx.CreditOriginal("auth", "original")
		if credit {
			t.Error("reopened store credited the same content twice")
		}
		if err != nil {
			return err
		}
		return tx.SaveScope(MiddlewareScope{ID: "scope", Authority: "auth", Manifest: []byte("[1]"), ExpiresAt: 100})
	}); err != nil {
		t.Fatal(err)
	}
	check(5, 515)
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { _, err := tx.Delete("auth", 50); return err }); err != nil {
		t.Fatal(err)
	}
	// Only the scope and grant tombstones remain: revocation deletes the
	// authority's plans, receipts and originals (credits included).
	check(2, 0)
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
		body, handle, expiry, err := tx.Grant("auth", "grant")
		if err == nil && (len(body) != 0 || handle != "" || expiry > 0) {
			t.Fatal("revocation retained recoverable payload")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMiddlewareExpiryReclaimsPayloadAndKeepsTypedTombstone(t *testing.T) {
	eachMiddlewareBackend(t, testMiddlewareExpiryReclaimsPayloadAndKeepsTypedTombstone)
}

func testMiddlewareExpiryReclaimsPayloadAndKeepsTypedTombstone(t *testing.T, s middlewareBackend) {
	ctx := context.Background()
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
		if err := tx.SaveScope(MiddlewareScope{ID: "expired", Authority: "auth", Manifest: []byte("[]"), ExpiresAt: 10}); err != nil {
			return err
		}
		if err := tx.SaveScope(MiddlewareScope{ID: "live", Authority: "auth", Manifest: []byte("[]"), ExpiresAt: 1 << 40}); err != nil {
			return err
		}
		if err := tx.SaveChoice("expired", "choice", "grant", "ccr", []byte("replacement")); err != nil {
			return err
		}
		if err := tx.SavePlan("expired", "plan", "digest", []byte("plan"), 1<<40); err != nil {
			return err
		}
		if _, err := tx.CreditOriginal("auth", "shared-original"); err != nil {
			return err
		}
		_, err := tx.Expire(11)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rows, size, err := s.MiddlewareUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Two scope rows (one live, one tombstoned) and the original still credited
	// to the authority the live scope shares; every payload-bearing row is gone,
	// the elapsed scope's manifest included (that is what marks it purged).
	if rows != 3 {
		t.Fatalf("expired rows=%d, want scopes plus the live authority's original", rows)
	}
	if size != 2+64 {
		t.Fatalf("expired payload bytes=%d, want the live scope's manifest plus one credit", size)
	}
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
		credit, err := tx.CreditOriginal("auth", "shared-original")
		if credit {
			t.Error("expiry dropped an original still credited to a live scope")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// The scope row outlives its payload by one grace period, so a replayed
	// marker is still answered "expired" rather than starting a silent new scope.
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
		scope, err := tx.Scope("expired")
		if err != nil || scope.ExpiresAt != 10 {
			t.Errorf("expired scope lost its typed tombstone: %v %+v", err, scope)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { _, err := tx.Expire(11 + MiddlewareGraceSeconds); return err }); err != nil {
		t.Fatal(err)
	}
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
		if _, err := tx.Scope("expired"); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("tombstone outlived its grace period: %v", err)
		}
		if _, err := tx.Scope("live"); err != nil {
			t.Errorf("grace sweep reclaimed a live scope: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A shared store used to wedge permanently: expiry reclaimed no rows, so the
// 100,000-row admission cap stayed tripped forever once a few thousand sessions
// had elapsed. Fill past the cap with elapsed scopes and prove expiry drains it.
func TestMiddlewareExpiryReclaimsCapacityFromElapsedScopes(t *testing.T) {
	eachMiddlewareBackend(t, testMiddlewareExpiryReclaimsCapacityFromElapsedScopes)
}

func testMiddlewareExpiryReclaimsCapacityFromElapsedScopes(t *testing.T, s middlewareBackend) {
	ctx := context.Background()
	const scopes, choices = 300, 100200
	seed := []string{
		fmt.Sprintf(`WITH RECURSIVE n(v) AS (SELECT 0 UNION ALL SELECT v+1 FROM n WHERE v<%d)
INSERT INTO middleware_scopes(id,authority,manifest,sequence,expires_at) SELECT 'scope-'||v,'auth-'||(v%%8),'',0,1 FROM n`, scopes-1),
		fmt.Sprintf(`WITH RECURSIVE n(v) AS (SELECT 0 UNION ALL SELECT v+1 FROM n WHERE v<%d)
INSERT INTO middleware_choices(scope,id,payload,grant_id,ccr_handle) SELECT 'scope-'||(v%%%d),'choice-'||v,'',   'grant-'||v,'' FROM n`, choices-1, scopes),
		`INSERT INTO middleware_receipts(authority,id,digest,payload,expires_at) VALUES ('auth-0','receipt','hash','',1)`,
		`INSERT INTO middleware_originals(authority,digest) VALUES ('auth-0','original')`,
	}
	for _, statement := range seed {
		s.exec(t, statement)
	}
	admit := func() error {
		return s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
			tx.Limits = MiddlewareLimits{Rows: 100000}
			return tx.SaveScope(MiddlewareScope{ID: "fresh", Authority: "fresh", Manifest: []byte("[]"), ExpiresAt: 1 << 40})
		})
	}
	if err := admit(); !errors.Is(err, ErrMiddlewareCapacity) {
		t.Fatalf("seeded store did not reach the row cap: %v", err)
	}
	// Elapsed past the tombstone grace, so the scope rows go too. Each pass is
	// one bounded batch; a wedged store recovers over a handful of requests.
	now := 1 + MiddlewareGraceSeconds + 1
	for pass := 0; pass < scopes/128+3; pass++ {
		if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { _, err := tx.Expire(now); return err }); err != nil {
			t.Fatal(err)
		}
	}
	rows, size, err := s.MiddlewareUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rows != 0 || size != 0 {
		t.Fatalf("expiry left rows=%d bytes=%d, want an empty middleware store", rows, size)
	}
	if err := admit(); err != nil {
		t.Fatalf("store stayed wedged after expiry: %v", err)
	}
}

// A revoked scope's tombstone must answer "deleted" for one full grace period,
// the same window an elapsed scope's tombstone gets, and only then reclaim:
// neither earlier (the replay guarantee) nor never (the capacity leak this fixes).
func TestMiddlewareRevocationTombstoneSurvivesGraceThenReclaims(t *testing.T) {
	eachMiddlewareBackend(t, testMiddlewareRevocationTombstoneSurvivesGraceThenReclaims)
}

func testMiddlewareRevocationTombstoneSurvivesGraceThenReclaims(t *testing.T, s middlewareBackend) {
	ctx := context.Background()
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
		if err := tx.SaveScope(MiddlewareScope{ID: "revoked", Authority: "auth", Manifest: []byte("[]"), ExpiresAt: 1 << 40}); err != nil {
			return err
		}
		if err := tx.SaveChoice("revoked", "choice", "grant", "ccr", []byte("replacement")); err != nil {
			return err
		}
		return tx.SavePlan("revoked", "plan", "digest", []byte("plan"), 1<<40)
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { _, err := tx.Delete("auth", 100); return err }); err != nil {
		t.Fatal(err)
	}
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
		scope, err := tx.Scope("revoked")
		if err != nil || scope.ExpiresAt != -100 {
			t.Errorf("revocation did not record a typed, timestamped tombstone: %v %+v", err, scope)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// One tick short of the grace deadline: the tombstone must still answer
	// "deleted" rather than let a marker issued before the revocation lapse.
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { _, err := tx.Expire(100 + MiddlewareGraceSeconds - 1); return err }); err != nil {
		t.Fatal(err)
	}
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
		if _, err := tx.Scope("revoked"); err != nil {
			t.Errorf("revoked tombstone reclaimed before its grace period elapsed: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// At the grace deadline: the tombstone, and only the tombstone, is gone.
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { _, err := tx.Expire(100 + MiddlewareGraceSeconds); return err }); err != nil {
		t.Fatal(err)
	}
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
		if _, err := tx.Scope("revoked"); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("revoked tombstone outlived its grace period: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// The same wedge as TestMiddlewareExpiryReclaimsCapacityFromElapsedScopes, but
// from the trigger that mechanism never covered: revocation traffic alone, with
// no scope ever elapsing, which used to be permanently invisible to Expire.
func TestMiddlewareExpiryReclaimsCapacityFromRevokedScopes(t *testing.T) {
	eachMiddlewareBackend(t, testMiddlewareExpiryReclaimsCapacityFromRevokedScopes)
}

func testMiddlewareExpiryReclaimsCapacityFromRevokedScopes(t *testing.T, s middlewareBackend) {
	ctx := context.Background()
	const scopes, choices = 300, 100200
	seed := []string{
		fmt.Sprintf(`WITH RECURSIVE n(v) AS (SELECT 0 UNION ALL SELECT v+1 FROM n WHERE v<%d)
INSERT INTO middleware_scopes(id,authority,manifest,sequence,expires_at) SELECT 'scope-'||v,'auth-'||(v%%8),'',0,-1 FROM n`, scopes-1),
		fmt.Sprintf(`WITH RECURSIVE n(v) AS (SELECT 0 UNION ALL SELECT v+1 FROM n WHERE v<%d)
INSERT INTO middleware_choices(scope,id,payload,grant_id,ccr_handle) SELECT 'scope-'||(v%%%d),'choice-'||v,'',   'grant-'||v,'' FROM n`, choices-1, scopes),
		`INSERT INTO middleware_receipts(authority,id,digest,payload,expires_at) VALUES ('auth-0','receipt','hash','',1)`,
		`INSERT INTO middleware_originals(authority,digest) VALUES ('auth-0','original')`,
	}
	for _, statement := range seed {
		s.exec(t, statement)
	}
	admit := func() error {
		return s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
			tx.Limits = MiddlewareLimits{Rows: 100000}
			return tx.SaveScope(MiddlewareScope{ID: "fresh", Authority: "fresh", Manifest: []byte("[]"), ExpiresAt: 1 << 40})
		})
	}
	if err := admit(); !errors.Is(err, ErrMiddlewareCapacity) {
		t.Fatalf("seeded store did not reach the row cap: %v", err)
	}
	// Revoked past the tombstone grace, exactly like the elapsed case above:
	// batched reclaim over a handful of passes, never one unbounded sweep.
	now := 1 + MiddlewareGraceSeconds + 1
	for pass := 0; pass < scopes/128+3; pass++ {
		if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { _, err := tx.Expire(now); return err }); err != nil {
			t.Fatal(err)
		}
	}
	rows, size, err := s.MiddlewareUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rows != 0 || size != 0 {
		t.Fatalf("expiry left rows=%d bytes=%d, want an empty middleware store", rows, size)
	}
	if err := admit(); err != nil {
		t.Fatalf("store stayed wedged after expiry: %v", err)
	}
}

// A revoked marker must keep answering "deleted" on the RECOVERY path for its
// whole grace period, not just on the optimize path.
//
// Two different rows carry that answer. previousPlan reads the scope row
// (Scope -> ExpiresAt <= 0 -> "deleted"). recovery.retrieve reads the CHOICE
// row (Grant -> a row with expires <= 0 -> "deleted"); Delete deliberately
// keeps that row and only zeroes its payload, because a Grant that finds no row
// at all is reported as "not_found" — a marker the caller never had — instead
// of "deleted", the marker they had and lost.
//
// So the choice row has to outlive the first Expire pass, which runs in front
// of every optimize request. Reclaiming revoked scopes at the same moment their
// payloads become collectable would collapse the revocation grace to zero on
// this path while leaving it at a full week on the other.
func TestMiddlewareRevokedGrantAnswersDeletedThroughItsGracePeriod(t *testing.T) {
	eachMiddlewareBackend(t, testMiddlewareRevokedGrantAnswersDeletedThroughItsGracePeriod)
}

func testMiddlewareRevokedGrantAnswersDeletedThroughItsGracePeriod(t *testing.T, s middlewareBackend) {
	ctx := context.Background()
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
		if err := tx.SaveScope(MiddlewareScope{ID: "revoked", Authority: "auth", Manifest: []byte("[]"), ExpiresAt: 1 << 40}); err != nil {
			return err
		}
		return tx.SaveChoice("revoked", "choice", "grant", "ccr", []byte("replacement"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { _, err := tx.Delete("auth", 100); return err }); err != nil {
		t.Fatal(err)
	}

	assertTypedDeleted := func(t *testing.T, stage string) {
		t.Helper()
		if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
			body, handle, expires, err := tx.Grant("auth", "grant")
			if errors.Is(err, sql.ErrNoRows) {
				t.Errorf("%s: revoked grant reports not_found, not deleted", stage)
				return nil
			}
			if err != nil {
				return err
			}
			if expires > 0 {
				t.Errorf("%s: revoked grant still resolves, expires=%d", stage, expires)
			}
			if len(body) != 0 || handle != "" {
				t.Errorf("%s: revocation retained recoverable payload", stage)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	assertTypedDeleted(t, "immediately after revocation")

	// Expire runs in front of every optimize request, so this is the very next
	// thing that happens to the store in practice.
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { _, err := tx.Expire(101); return err }); err != nil {
		t.Fatal(err)
	}
	assertTypedDeleted(t, "after an Expire pass inside the grace period")

	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { _, err := tx.Expire(100 + MiddlewareGraceSeconds - 1); return err }); err != nil {
		t.Fatal(err)
	}
	assertTypedDeleted(t, "one tick before the grace deadline")

	// Past the deadline the row is reclaimed with the rest of the scope; that
	// is the capacity leak this whole change exists to fix.
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { _, err := tx.Expire(100 + MiddlewareGraceSeconds); return err }); err != nil {
		t.Fatal(err)
	}
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
		if _, _, _, err := tx.Grant("auth", "grant"); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("revoked grant outlived its grace period: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A4: the purge batch used to be the first 128 elapsed scopes, and purged
// scopes stayed elapsed for the whole grace period, so every pass reselected
// the same 128 and the other scopes kept their payload until grace ended
// (reviewer repro: 300 scopes, 50 passes, 172 choices and plans left).
func TestMiddlewareExpiryPurgesEveryElapsedScopeInsideGrace(t *testing.T) {
	eachMiddlewareBackend(t, testMiddlewareExpiryPurgesEveryElapsedScopeInsideGrace)
}

func testMiddlewareExpiryPurgesEveryElapsedScopeInsideGrace(t *testing.T, s middlewareBackend) {
	ctx := context.Background()
	const scopes = 300
	for _, statement := range []string{
		fmt.Sprintf(`WITH RECURSIVE n(v) AS (SELECT 0 UNION ALL SELECT v+1 FROM n WHERE v<%d)
INSERT INTO middleware_scopes(id,authority,manifest,sequence,expires_at,created_at) SELECT 'scope-'||v,'auth-'||v,'[]',0,10,1 FROM n`, scopes-1),
		fmt.Sprintf(`WITH RECURSIVE n(v) AS (SELECT 0 UNION ALL SELECT v+1 FROM n WHERE v<%d)
INSERT INTO middleware_choices(scope,id,payload,grant_id,ccr_handle) SELECT 'scope-'||v,'choice','a','grant-'||v,'' FROM n`, scopes-1),
		fmt.Sprintf(`WITH RECURSIVE n(v) AS (SELECT 0 UNION ALL SELECT v+1 FROM n WHERE v<%d)
INSERT INTO middleware_plans(scope,id,digest,payload,expires_at) SELECT 'scope-'||v,'plan','d','a',%d FROM n`, scopes-1, int64(1)<<40),
	} {
		s.exec(t, statement)
	}
	for pass := 0; pass < 50; pass++ {
		if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { _, err := tx.Expire(11); return err }); err != nil {
			t.Fatal(err)
		}
	}
	choices, plans := s.count(t, `SELECT count(*) FROM middleware_choices`), s.count(t, `SELECT count(*) FROM middleware_plans`)
	tombstones := s.count(t, `SELECT count(*) FROM middleware_scopes WHERE length(manifest)=0`)
	if choices != 0 || plans != 0 || tombstones != scopes {
		t.Fatalf("after 50 passes inside grace: choices=%d plans=%d tombstones=%d, want 0 0 %d", choices, plans, tombstones, scopes)
	}
}

// A principal at its quota gets capacity; another principal is unaffected.
func TestMiddlewarePrincipalQuotaIsolatesPrincipals(t *testing.T) {
	eachMiddlewareBackend(t, testMiddlewarePrincipalQuotaIsolatesPrincipals)
}

func testMiddlewarePrincipalQuotaIsolatesPrincipals(t *testing.T, s middlewareBackend) {
	ctx := context.Background()
	save := func(principal, id string, manifest int) error {
		return s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
			tx.Principal, tx.Limits = principal, MiddlewareLimits{PrincipalRows: 2, PrincipalBytes: 100}
			return tx.SaveScope(MiddlewareScope{ID: id, Authority: principal, Manifest: make([]byte, manifest), ExpiresAt: 1 << 40})
		})
	}
	for _, id := range []string{"alice-1", "alice-2"} {
		if err := save("alice", id, 10); err != nil {
			t.Fatal(err)
		}
	}
	if err := save("alice", "alice-3", 10); !errors.Is(err, ErrMiddlewareCapacity) {
		t.Fatalf("row quota not enforced: %v", err)
	}
	if err := save("bob", "bob-1", 10); err != nil {
		t.Fatalf("one principal's quota blocked another: %v", err)
	}
	if err := save("bob", "bob-2", 101); !errors.Is(err, ErrMiddlewareCapacity) {
		t.Fatalf("byte quota not enforced: %v", err)
	}
	s.exec(t, `DELETE FROM middleware_scopes WHERE id='alice-1'`)
	if err := save("alice", "alice-3", 10); err != nil {
		t.Fatalf("deleted rows were not returned to the principal: %v", err)
	}
}

func TestMiddlewareDeleteCountsAndRemovesOriginals(t *testing.T) {
	eachMiddlewareBackend(t, testMiddlewareDeleteCountsAndRemovesOriginals)
}

func testMiddlewareDeleteCountsAndRemovesOriginals(t *testing.T, s middlewareBackend) {
	ctx := context.Background()
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
		tx.Principal = "alice"
		if err := tx.SaveScope(MiddlewareScope{ID: "scope", Authority: "auth", Manifest: []byte("[]"), ExpiresAt: 1 << 40}); err != nil {
			return err
		}
		if err := tx.SaveChoice("scope", "new", "grant-new", "", []byte("r")); err != nil {
			return err
		}
		if err := tx.SaveChoice("scope", "legacy", "grant-legacy", "ccr_legacy", []byte("r")); err != nil {
			return err
		}
		if _, err := tx.SaveOriginal("auth", "stored", []byte("original"), ""); err != nil {
			return err
		}
		if _, err := tx.CreditOriginal("auth", "credit-only"); err != nil {
			return err
		}
		if err := tx.SavePlan("scope", "plan", "d", []byte("p"), 1<<40); err != nil {
			return err
		}
		return tx.Receipt("auth", "receipt", "d", []byte("r"), 1<<40)
	}); err != nil {
		t.Fatal(err)
	}
	var first, second MiddlewareDeleted
	for _, out := range []*MiddlewareDeleted{&first, &second} {
		if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { var err error; *out, err = tx.Delete("auth", 50); return err }); err != nil {
			t.Fatal(err)
		}
	}
	if first != (MiddlewareDeleted{Scopes: 1, Choices: 2, Originals: 1, Legacy: 1}) || second != (MiddlewareDeleted{}) {
		t.Fatalf("delete counts first=%+v second=%+v", first, second)
	}
	if left := s.count(t, `SELECT (SELECT count(*) FROM middleware_originals)+(SELECT count(*) FROM middleware_receipts)+(SELECT count(*) FROM middleware_plans)`); left != 0 {
		t.Fatalf("revocation left %d originals/receipts/plans", left)
	}
}

// A store written by protocol 1.0 migrates in place: its counters stay right
// under the new triggers, scopes get a max-retention clock, and legacy CCR
// grants keep resolving.
func TestMiddlewareMigratesProtocol10Store(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "store.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err := s.db.Exec(`
CREATE TABLE middleware_scopes (id TEXT PRIMARY KEY, authority TEXT NOT NULL, manifest BLOB NOT NULL, sequence INTEGER NOT NULL, expires_at INTEGER NOT NULL);
CREATE TABLE middleware_choices (scope TEXT NOT NULL, id TEXT NOT NULL, payload BLOB NOT NULL, grant_id TEXT NOT NULL UNIQUE, ccr_handle TEXT NOT NULL, PRIMARY KEY(scope,id));
CREATE TABLE middleware_plans (scope TEXT NOT NULL, id TEXT NOT NULL, digest TEXT NOT NULL, payload BLOB NOT NULL, PRIMARY KEY(scope,id));
CREATE TABLE middleware_receipts (authority TEXT NOT NULL, id TEXT NOT NULL, digest TEXT NOT NULL, payload BLOB NOT NULL, expires_at INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(authority,id));
CREATE TABLE middleware_originals (authority TEXT NOT NULL, digest TEXT NOT NULL, PRIMARY KEY(authority,digest));
CREATE TABLE middleware_usage (singleton INTEGER PRIMARY KEY CHECK(singleton=1), rows INTEGER NOT NULL, bytes INTEGER NOT NULL);
CREATE TRIGGER middleware_originals_usage_insert AFTER INSERT ON middleware_originals BEGIN UPDATE middleware_usage SET rows=rows+1,bytes=bytes+64 WHERE singleton=1; END;
INSERT INTO middleware_scopes VALUES ('scope','auth','[1]',0,100);
INSERT INTO middleware_choices VALUES ('scope','choice','12345','grant','ccr_h');
INSERT INTO middleware_plans VALUES ('scope','plan','d','1234');
INSERT INTO middleware_receipts VALUES ('auth','receipt','d','12',1);
INSERT INTO middleware_usage VALUES (1,4,14);
INSERT INTO middleware_originals VALUES ('auth','digest');`); err != nil {
		t.Fatal(err)
	}
	if err := s.InitMiddleware(ctx); err != nil {
		t.Fatal(err)
	}
	usage := func() (rows, size int64) {
		t.Helper()
		if rows, size, err = s.MiddlewareUsage(ctx); err != nil {
			t.Fatal(err)
		}
		return rows, size
	}
	if rows, size := usage(); rows != 5 || size != 78 {
		t.Fatalf("migrated usage=(%d,%d), want (5,78)", rows, size)
	}
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
		scope, err := tx.Scope("scope")
		if err != nil || scope.CreatedAt <= 0 {
			t.Errorf("legacy scope has no max-retention clock: %+v %v", scope, err)
		}
		if _, handle, _, err := tx.Grant("auth", "grant"); err != nil || handle != "ccr_h" {
			t.Errorf("legacy CCR grant lost: %q %v", handle, err)
		}
		credit, err := tx.SaveOriginal("auth", "digest", []byte("body"), "")
		if credit {
			t.Error("a 1.0 credit was credited again")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if rows, size := usage(); rows != 5 || size != 82 {
		t.Fatalf("usage after filling a 1.0 credit=(%d,%d), want (5,82)", rows, size)
	}
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { _, err := tx.Expire(100 + MiddlewareGraceSeconds); return err }); err != nil {
		t.Fatal(err)
	}
	if rows, size := usage(); rows != 0 || size != 0 {
		t.Fatalf("usage after reclaiming every migrated row=(%d,%d), want (0,0)", rows, size)
	}
}
