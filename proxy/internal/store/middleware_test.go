package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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
		if err := tx.SavePlan("scope", "plan", "hash", make([]byte, 128), 0, 1<<40); err != nil {
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
	// Only the scope and grant tombstones remain, and they count as neither rows
	// nor bytes: revocation deletes the authority's plans, receipts and originals
	// (credits included).
	check(0, 0)
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
		if err := tx.SavePlan("expired", "plan", "digest", []byte("plan"), 0, 1<<40); err != nil {
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
	// The live scope and the original still credited to the authority it
	// shares. The elapsed scope and its choice stay as content-free tombstones
	// (its grant answers "expired" until grace ends), which count as no rows;
	// every payload is gone, the elapsed scope's manifest included (that is what
	// marks it purged).
	if rows != 2 {
		t.Fatalf("expired rows=%d, want the live scope and the live authority's original", rows)
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
INSERT INTO middleware_scopes(id,authority,manifest,sequence,expires_at) SELECT 'scope-'||v,'auth-'||(v%%8),'[]',0,1 FROM n`, scopes-1),
		fmt.Sprintf(`WITH RECURSIVE n(v) AS (SELECT 0 UNION ALL SELECT v+1 FROM n WHERE v<%d)
INSERT INTO middleware_choices(scope,id,payload,grant_id,ccr_handle) SELECT 'scope-'||(v%%%d),'choice-'||v,'x',  'grant-'||v,'' FROM n`, choices-1, scopes),
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
		return tx.SavePlan("revoked", "plan", "digest", []byte("plan"), 0, 1<<40)
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

// Revocation traffic alone, with no scope ever elapsing, used to wedge the store
// for a grace period: every revoked session's tombstones counted against the
// row cap. They count for nothing now, and Expire still reclaims them.
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
	if err := admit(); err != nil {
		t.Fatalf("revocation tombstones counted against the row cap: %v", err)
	}
	// Revoked past the tombstone grace, exactly like the elapsed case above:
	// batched reclaim over a handful of passes, never one unbounded sweep.
	now := 1 + MiddlewareGraceSeconds + 1
	for pass := 0; pass < scopes/128+3; pass++ {
		if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { _, err := tx.Expire(now); return err }); err != nil {
			t.Fatal(err)
		}
	}
	if left := s.count(t, `SELECT (SELECT count(*) FROM middleware_scopes WHERE id<>'fresh')+(SELECT count(*) FROM middleware_choices)+
 (SELECT count(*) FROM middleware_receipts)+(SELECT count(*) FROM middleware_originals)`); left != 0 {
		t.Fatalf("expiry left %d revoked rows behind", left)
	}
	if rows, size, err := s.MiddlewareUsage(ctx); err != nil || rows != 1 || size != 2 {
		t.Fatalf("usage=(%d,%d) %v, want only the fresh scope (1,2)", rows, size, err)
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
	choices, plans := s.count(t, `SELECT count(*) FROM middleware_choices WHERE length(payload)>0`), s.count(t, `SELECT count(*) FROM middleware_plans`)
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
		if err := tx.SavePlan("scope", "plan", "d", []byte("p"), 0, 1<<40); err != nil {
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
	// A retry deletes nothing more, and still says a CCR original was out of reach.
	if first != (MiddlewareDeleted{Scopes: 1, Choices: 2, Originals: 1, Legacy: 1}) || second != (MiddlewareDeleted{Legacy: 1}) {
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
INSERT INTO middleware_scopes VALUES ('revoked','revoked-auth','',0,0);
INSERT INTO middleware_choices VALUES ('revoked','gone','','grant-gone','');
INSERT INTO middleware_usage VALUES (1,5,14);
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
	// Recounted, not carried over: the seeded counter's 5 rows included the
	// revocation tombstone, which no longer counts.
	if rows, size := usage(); rows != 5 || size != 78 {
		t.Fatalf("migrated usage=(%d,%d), want (5,78)", rows, size)
	}
	var revokedAt int64
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
		scope, err := tx.Scope("scope")
		if err != nil || scope.CreatedAt <= 0 {
			t.Errorf("legacy scope has no max-retention clock: %+v %v", scope, err)
		}
		// 1.0 revoked with expires_at=0; the sweep reads 0 as revoked at the epoch,
		// long past grace, so the migration dates it now.
		revoked, err := tx.Scope("revoked")
		if revokedAt = -revoked.ExpiresAt; err != nil || revokedAt <= 0 {
			t.Errorf("1.0 tombstone was not dated: %+v %v", revoked, err)
		}
		if _, handle, _, err := tx.Grant("auth", "grant"); err != nil || handle != "ccr_h" {
			t.Errorf("legacy CCR grant lost: %q %v", handle, err)
		}
		// 1.0's revocation blanked this choice's CCR handle; the migration must
		// still know its original is in CCR (originals_deleted stays false).
		if deleted, err := tx.Revoke("revoked-auth", 1); err != nil || deleted.Legacy != 1 {
			t.Errorf("a choice 1.0 revoked lost its legacy flag: %+v %v", deleted, err)
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
		t.Fatalf("usage after reclaiming the migrated rows=(%d,%d), want (0,0): only uncounted tombstones remain", rows, size)
	}
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { _, err := tx.Expire(revokedAt + MiddlewareGraceSeconds); return err }); err != nil {
		t.Fatal(err)
	}
	if rows, size := usage(); rows != 0 || size != 0 {
		t.Fatalf("usage after the tombstone's grace=(%d,%d), want (0,0)", rows, size)
	}
}

// GO-7: a protocol 1.0 credit row that gets its body filled belongs to the
// principal that filled it, so the body counts against that principal's quota.
func TestMiddlewareFilledCreditCountsAgainstThePrincipal(t *testing.T) {
	eachMiddlewareBackend(t, func(t *testing.T, s middlewareBackend) {
		ctx := context.Background()
		s.exec(t, `INSERT INTO middleware_originals(authority,digest) VALUES ('auth','one'),('auth','two')`)
		fill := func(digest string) error {
			return s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
				tx.Principal, tx.Limits = "alice", MiddlewareLimits{PrincipalBytes: 300}
				_, err := tx.SaveOriginal("auth", digest, make([]byte, 200), "")
				return err
			})
		}
		if err := fill("one"); err != nil {
			t.Fatal(err)
		}
		rows := s.count(t, `SELECT coalesce(sum(rows),0) FROM middleware_principal_usage WHERE principal='alice'`)
		size := s.count(t, `SELECT coalesce(sum(bytes),0) FROM middleware_principal_usage WHERE principal='alice'`)
		if rows != 1 || size != 64+200 {
			t.Fatalf("filled credit counted as (%d,%d) for alice, want (1,264)", rows, size)
		}
		if err := fill("two"); !errors.Is(err, ErrMiddlewareCapacity) {
			t.Fatalf("a second fill bypassed alice's byte quota: %v", err)
		}
	})
}

// S7: the WAL and shared-memory files hold the same plaintext originals as the
// database, so they get its 0600 mode, not the umask's.
func TestSQLiteSidecarFilesArePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes")
	}
	path := filepath.Join(t.TempDir(), "caveman.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.InitMiddleware(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
		_, err := tx.SaveOriginal("auth", "digest", []byte("plaintext original"), "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s has mode %o, want 600", filepath.Base(path+suffix), perm)
		}
	}
}

// A store written before choices named their original gets the column
// backfilled from each choice's payload, so the first sweep after the upgrade
// does not reclaim originals that live choices still reference.
func TestMiddlewareMigrationBackfillsChoiceOriginals(t *testing.T) {
	eachMiddlewareBackend(t, func(t *testing.T, s middlewareBackend) {
		digest := strings.Repeat("ab", 32)
		s.exec(t, `DROP INDEX middleware_choices_original`)
		s.exec(t, `ALTER TABLE middleware_choices DROP COLUMN original`)
		s.exec(t, `ALTER TABLE middleware_choices DROP COLUMN legacy`)
		s.exec(t, `INSERT INTO middleware_choices(scope,id,payload,grant_id,ccr_handle) VALUES ('scope','new','{"segment_id":"tool-1","source_id":"d","original_sha256":"`+digest+`"}','grant-new','')`)
		s.exec(t, `INSERT INTO middleware_choices(scope,id,payload,grant_id,ccr_handle) VALUES ('scope','legacy','{"original_sha256":"`+digest+`"}','grant-legacy','ccr_h')`)
		if _, ok := s.MiddlewareStore.(*PostgresMiddleware); ok {
			s.exec(t, `DELETE FROM middleware_schema`)
			s.exec(t, `INSERT INTO middleware_schema VALUES (1)`)
		}
		if err := s.MiddlewareStore.InitMiddleware(context.Background()); err != nil {
			t.Fatal(err)
		}
		if n := s.count(t, `SELECT count(*) FROM middleware_choices WHERE id='new' AND original='`+digest+`'`); n != 1 {
			t.Fatal("the choice's original was not backfilled")
		}
		if n := s.count(t, `SELECT count(*) FROM middleware_choices WHERE id='legacy' AND original=''`); n != 1 {
			t.Fatal("a protocol 1.0 choice (original in CCR) was given a middleware original")
		}
	})
}

// sessions/delete revokes first, then purges in batches that each commit on
// their own: between batches the authority already answers "deleted", and
// repeating the purge resumes where it stopped. Each batch reports exactly the
// originals with a body it deleted; credit-only rows go uncounted, in their
// own bounded step.
func TestMiddlewarePurgeResumesAcrossBatches(t *testing.T) {
	eachMiddlewareBackend(t, func(t *testing.T, s middlewareBackend) {
		ctx := context.Background()
		const n, limit = 300, 128
		if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
			tx.Principal = "alice"
			if err := tx.SaveScope(MiddlewareScope{ID: "scope", Authority: "auth", Manifest: []byte("[]"), ExpiresAt: 1 << 40}); err != nil {
				return err
			}
			choices, originals := make([]MiddlewareChoice, n), make([]MiddlewareOriginal, 2*n)
			for i := range n {
				choices[i] = MiddlewareChoice{ID: fmt.Sprint("choice-", i), Grant: fmt.Sprint("grant-", i), Payload: []byte("replacement")}
				originals[i] = MiddlewareOriginal{Digest: fmt.Sprint("digest-", i), Body: []byte("original")}
				originals[n+i] = MiddlewareOriginal{Digest: fmt.Sprint("credit-", i)}
			}
			if err := tx.SaveChoices("scope", choices); err != nil {
				return err
			}
			_, err := tx.SaveOriginals("auth", originals)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		var total MiddlewareDeleted
		if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
			var err error
			total, err = tx.Revoke("auth", 50)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		batches := 0
		for more := true; more; batches++ {
			if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
				if _, _, expires, err := tx.Grant("auth", "grant-299"); err != nil || expires > 0 {
					t.Errorf("mid-purge grant is not revoked: expires=%d %v", expires, err)
				}
				batch, next, err := tx.PurgeBatch("auth", limit)
				if want := min(limit, n-int64(batches)*limit); batch.Choices != want || batch.Originals != want {
					t.Errorf("batch %d deleted %+v, want %d choices and %d originals", batches, batch, want, want)
				}
				total.Add(batch)
				more = next
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
		if batches != 3 || total != (MiddlewareDeleted{Scopes: 1, Choices: n, Originals: n}) {
			t.Fatalf("purge took %d batches, counts %+v", batches, total)
		}
		if left := s.count(t, `SELECT (SELECT count(*) FROM middleware_originals)+(SELECT count(*) FROM middleware_choices WHERE length(payload)>0)+(SELECT count(*) FROM middleware_scopes WHERE length(manifest)>0)`); left != 0 {
			t.Fatalf("purge left %d rows of content", left)
		}
		if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
			again, more, err := tx.PurgeBatch("auth", limit)
			if more || again != (MiddlewareDeleted{}) {
				t.Errorf("a repeated purge found more: %+v %v", again, more)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	})
}

// A sessions/delete nobody retried is finished by the sweep, not left holding
// content until the tombstone's grace ends, and one bounded batch per sweep
// transaction: a large session purged in one would hold SQLite's writer past
// every optimize's deadline. The batch stays MiddlewarePurgeBatch at the
// largest limit the sweep reaches. The sweep reports more while another
// unfinished delete waits, and the backlog gauge counts both until done.
func TestMiddlewareSweepFinishesAnAbandonedPurge(t *testing.T) {
	eachMiddlewareBackend(t, func(t *testing.T, s middlewareBackend) {
		ctx := context.Background()
		const n, limit = 600, 1024
		if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
			if err := tx.SaveScope(MiddlewareScope{ID: "scope", Authority: "auth", Manifest: []byte("[]"), ExpiresAt: 1 << 40}); err != nil {
				return err
			}
			choices, originals := make([]MiddlewareChoice, n), make([]MiddlewareOriginal, n)
			for i := range n {
				choices[i] = MiddlewareChoice{ID: fmt.Sprint("c-", i), Grant: fmt.Sprint("g-", i), Original: fmt.Sprint("d-", i), Payload: []byte("replacement")}
				originals[i] = MiddlewareOriginal{Digest: fmt.Sprint("d-", i), Body: []byte("original")}
				if err := tx.Receipt("auth", fmt.Sprint("r-", i), "hash", []byte("receipt"), 1<<40); err != nil {
					return err
				}
				if err := tx.SavePlan("scope", fmt.Sprint("p-", i), "hash", []byte("plan"), 0, 1<<40); err != nil {
					return err
				}
			}
			if err := tx.SaveChoices("scope", choices); err != nil {
				return err
			}
			if _, err := tx.SaveOriginals("auth", originals); err != nil {
				return err
			}
			_, err := tx.Revoke("auth", 50)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		s.exec(t, `INSERT INTO middleware_scopes(id,authority,manifest,sequence,expires_at) VALUES ('other','other','[]',0,-50)`)
		if backlog, err := s.MiddlewareBacklog(ctx, 60); err != nil || backlog != 2 {
			t.Fatalf("backlog=%d %v, want both unfinished deletes", backlog, err)
		}
		const content = `SELECT (SELECT count(*) FROM middleware_originals)+(SELECT count(*) FROM middleware_choices WHERE length(payload)>0)+
 (SELECT count(*) FROM middleware_receipts)+(SELECT count(*) FROM middleware_plans)+(SELECT count(*) FROM middleware_scopes WHERE length(manifest)>0)`
		passes := 0
		for left := s.count(t, content); left > 0; passes++ {
			if passes > 10 {
				t.Fatalf("the sweep left %d rows of an abandoned delete", left)
			}
			var full bool
			if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
				var err error
				_, full, err = tx.ExpireBatch(60, limit)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			now := s.count(t, content)
			if left-now > 4*MiddlewarePurgeBatch+1 {
				t.Fatalf("one sweep transaction purged %d rows, want at most one batch per kind", left-now)
			}
			if full != (now > 0) {
				t.Fatalf("sweep reported more=%v with %d rows left", full, now)
			}
			left = now
		}
		if passes < 3 {
			t.Fatalf("a %d-row session was purged in %d sweep transactions", 4*n, passes)
		}
		if backlog, err := s.MiddlewareBacklog(ctx, 60); err != nil || backlog != 0 {
			t.Fatalf("backlog=%d %v after the sweep finished both deletes", backlog, err)
		}
	})
}

// The sweep and the backlog gauge look for unfinished deletes on every pass:
// an index range, never a scan of a grace period's worth of tombstones.
func TestSQLiteRevokedUnpurgedLookupUsesTheIndex(t *testing.T) {
	db := sqliteBackend(t).MiddlewareStore.(*Store).db
	for _, query := range []string{`SELECT count(*)>0 FROM (` + nextRevoked + `) n`, middlewareBacklog("?1")} {
		rows, err := db.Query(`EXPLAIN QUERY PLAN `+query, 60)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan.WriteString(detail + "\n")
		}
		rows.Close()
		if strings.Contains(plan.String(), "SCAN middleware_scopes") {
			t.Fatalf("%s\nscans the scopes:\n%s", query, plan.String())
		}
	}
}

// A plan is replaced only once it has expired: a live one is never
// overwritten, whatever its writer last read.
func TestMiddlewareSavePlanReplacesOnlyAnExpiredPlan(t *testing.T) {
	eachMiddlewareBackend(t, func(t *testing.T, s middlewareBackend) {
		save := func(digest string, now int64) error {
			return s.WithMiddleware(context.Background(), func(tx *MiddlewareTx) error {
				return tx.SavePlan("scope", "plan", digest, []byte(digest), now, now+100)
			})
		}
		if err := save("first", 0); err != nil {
			t.Fatal(err)
		}
		if err := save("second", 99); !errors.Is(err, ErrMiddlewareConflict) {
			t.Fatalf("saving over a live plan: %v", err)
		}
		if err := save("third", 100); err != nil {
			t.Fatalf("saving over an expired plan: %v", err)
		}
		if n := s.count(t, `SELECT count(*) FROM middleware_plans WHERE digest='third' AND expires_at=200`); n != 1 {
			t.Fatal("the expired plan was not replaced")
		}
	})
}

// A principal at its row quota frees it by deleting a session: tombstones do
// not count.
func TestMiddlewareDeleteFreesAPrincipalsQuota(t *testing.T) {
	eachMiddlewareBackend(t, func(t *testing.T, s middlewareBackend) {
		ctx := context.Background()
		session := func(authority string) error {
			return s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
				tx.Principal, tx.Limits = "alice", MiddlewareLimits{PrincipalRows: 3}
				if err := tx.SaveScope(MiddlewareScope{ID: authority, Authority: authority, Manifest: []byte("[]"), ExpiresAt: 1 << 40}); err != nil {
					return err
				}
				if err := tx.SaveChoices(authority, []MiddlewareChoice{{ID: "c", Grant: "g-" + authority, Original: "d", Payload: []byte("r")}}); err != nil {
					return err
				}
				_, err := tx.SaveOriginal(authority, "d", []byte("original"), "")
				return err
			})
		}
		if err := session("one"); err != nil {
			t.Fatal(err)
		}
		if err := session("two"); !errors.Is(err, ErrMiddlewareCapacity) {
			t.Fatalf("second session under a 3-row quota: %v", err)
		}
		if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { _, err := tx.Delete("one", 50); return err }); err != nil {
			t.Fatal(err)
		}
		if err := session("two"); err != nil {
			t.Fatalf("a deleted session's tombstones kept the quota full: %v", err)
		}
	})
}

// Counters written by accounting that counted tombstones (or drifted any other
// way) are recomputed once, when the store is upgraded.
func TestMiddlewareUpgradeRecountsUsage(t *testing.T) {
	eachMiddlewareBackend(t, func(t *testing.T, s middlewareBackend) {
		ctx := context.Background()
		if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
			tx.Principal = "alice"
			if err := tx.SaveScope(MiddlewareScope{ID: "live", Authority: "auth", Manifest: []byte("[]"), ExpiresAt: 1 << 40}); err != nil {
				return err
			}
			if err := tx.SaveScope(MiddlewareScope{ID: "gone", Authority: "old", Manifest: []byte("[]"), ExpiresAt: 1 << 40}); err != nil {
				return err
			}
			_, err := tx.Delete("old", 50)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		s.exec(t, `UPDATE middleware_usage SET rows=rows+1000`)
		s.exec(t, `UPDATE middleware_principal_usage SET rows=rows+1000`)
		if p, ok := s.MiddlewareStore.(*PostgresMiddleware); ok {
			s.exec(t, `DELETE FROM middleware_schema`)
			s.exec(t, `INSERT INTO middleware_schema VALUES (2)`)
			if err := p.InitMiddleware(ctx); err != nil {
				t.Fatal(err)
			}
		} else {
			s.exec(t, `DROP TRIGGER middleware_choices_usage_delete`)
			if err := s.InitMiddleware(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if rows, size, err := s.MiddlewareUsage(ctx); err != nil || rows != 1 || size != 2 {
			t.Fatalf("recounted usage=(%d,%d) %v, want the live scope (1,2)", rows, size, err)
		}
		if rows := s.count(t, `SELECT coalesce(sum(rows),0) FROM middleware_principal_usage WHERE principal='alice'`); rows != 1 {
			t.Fatalf("alice's recounted rows=%d, want 1", rows)
		}
	})
}

// Receipts are deduplicated on their id: a retry with another body stores
// nothing and succeeds.
func TestMiddlewareRetriedReceiptKeepsTheFirst(t *testing.T) {
	eachMiddlewareBackend(t, func(t *testing.T, s middlewareBackend) {
		ctx := context.Background()
		for _, digest := range []string{"first", "second"} {
			if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
				return tx.Receipt("auth", "id", digest, []byte(digest), 1<<40)
			}); err != nil {
				t.Fatalf("receipt %s: %v", digest, err)
			}
		}
		if n := s.count(t, `SELECT count(*) FROM middleware_receipts WHERE digest='first'`); n != 1 {
			t.Fatal("the retried receipt replaced the first")
		}
	})
}

// Once the schema is in place a role with only DML on it restarts: Postgres
// checks CREATE on the schema even for CREATE TABLE IF NOT EXISTS on an
// existing table, so InitMiddleware must not run DDL on a current store.
func TestPostgresMiddlewareRestartsWithoutCreatePrivilege(t *testing.T) {
	base := os.Getenv("CAVEMAN_TEST_POSTGRES_URL")
	if base == "" {
		t.Skip("CAVEMAN_TEST_POSTGRES_URL is unset")
	}
	ctx := context.Background()
	owner := postgresBackend(t, base) // migrates the schema as its owner
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	var schema string
	if err := owner.MiddlewareStore.(*PostgresMiddleware).pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	role := fmt.Sprintf("mw_dml_%d", time.Now().UnixNano())
	for _, statement := range []string{
		`CREATE ROLE ` + role + ` LOGIN PASSWORD 'dml'`,
		`GRANT USAGE ON SCHEMA ` + schema + ` TO ` + role,
		`GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA ` + schema + ` TO ` + role,
	} {
		if _, err := admin.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, `DROP OWNED BY `+role)
		_, _ = admin.Exec(ctx, `DROP ROLE `+role)
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, "dml")
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	p, err := OpenPostgresMiddleware(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.InitMiddleware(ctx); err != nil {
		t.Fatalf("a DML-only role could not restart on a current store: %v", err)
	}
	if err := p.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
		return tx.SaveScope(MiddlewareScope{ID: "scope", Authority: "auth", Manifest: []byte("[]"), ExpiresAt: 1 << 40})
	}); err != nil {
		t.Fatal(err)
	}
}
