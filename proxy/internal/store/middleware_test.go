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
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.InitMiddleware(ctx); err != nil {
		t.Fatal(err)
	}
	err = s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
		if err := tx.SaveScope(MiddlewareScope{ID: "scope", Authority: "auth", Manifest: []byte("[]"), ExpiresAt: 100}); err != nil {
			return err
		}
		if err := tx.SaveChoice("scope", "choice", "grant", "ccr", make([]byte, 256)); err != nil {
			return err
		}
		if err := tx.SavePlan("scope", "plan", "hash", make([]byte, 128)); err != nil {
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
	check := func(rows, size int) {
		t.Helper()
		var gotRows, gotSize int
		if err := s.db.QueryRow(`SELECT rows,bytes FROM middleware_usage`).Scan(&gotRows, &gotSize); err != nil {
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
	other, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.InitMiddleware(ctx); err != nil {
		t.Fatal(err)
	}
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
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { return tx.Delete("auth", 50) }); err != nil {
		t.Fatal(err)
	}
	check(4, 128) // scope/grant tombstones, receipt metadata, original credit.
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
	s, err := Open(filepath.Join(t.TempDir(), "store.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.InitMiddleware(ctx); err != nil {
		t.Fatal(err)
	}
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
		if err := tx.SavePlan("expired", "plan", "digest", []byte("plan")); err != nil {
			return err
		}
		if _, err := tx.CreditOriginal("auth", "shared-original"); err != nil {
			return err
		}
		return tx.Expire(11)
	}); err != nil {
		t.Fatal(err)
	}
	var size, rows int
	if err := s.db.QueryRow(`SELECT rows,bytes FROM middleware_usage`).Scan(&rows, &size); err != nil {
		t.Fatal(err)
	}
	// Two scope rows (one live, one tombstoned) and the original still credited
	// to the authority the live scope shares; every payload-bearing row is gone.
	if rows != 3 {
		t.Fatalf("expired rows=%d, want scopes plus the live authority's original", rows)
	}
	if size != 4+64 {
		t.Fatalf("expired payload bytes=%d, want the two empty manifests plus one credit", size)
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
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { return tx.Expire(11 + MiddlewareGraceSeconds) }); err != nil {
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
	s, err := Open(filepath.Join(t.TempDir(), "store.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.InitMiddleware(ctx); err != nil {
		t.Fatal(err)
	}
	const scopes, choices = 300, 100200
	seed := []string{
		fmt.Sprintf(`WITH RECURSIVE n(v) AS (SELECT 0 UNION ALL SELECT v+1 FROM n WHERE v<%d)
INSERT INTO middleware_scopes(id,authority,manifest,sequence,expires_at) SELECT 'scope-'||v,'auth-'||(v%%8),x'',0,1 FROM n`, scopes-1),
		fmt.Sprintf(`WITH RECURSIVE n(v) AS (SELECT 0 UNION ALL SELECT v+1 FROM n WHERE v<%d)
INSERT INTO middleware_choices(scope,id,payload,grant_id,ccr_handle) SELECT 'scope-'||(v%%%d),'choice-'||v,x'',   'grant-'||v,'' FROM n`, choices-1, scopes),
		`INSERT INTO middleware_receipts(authority,id,digest,payload,expires_at) VALUES ('auth-0','receipt','hash',x'',1)`,
		`INSERT INTO middleware_originals(authority,digest) VALUES ('auth-0','original')`,
	}
	for _, statement := range seed {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	admit := func() error {
		return s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
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
		if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { return tx.Expire(now) }); err != nil {
			t.Fatal(err)
		}
	}
	var rows, size int64
	if err := s.db.QueryRow(`SELECT rows,bytes FROM middleware_usage`).Scan(&rows, &size); err != nil {
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
	s, err := Open(filepath.Join(t.TempDir(), "store.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.InitMiddleware(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
		if err := tx.SaveScope(MiddlewareScope{ID: "revoked", Authority: "auth", Manifest: []byte("[]"), ExpiresAt: 1 << 40}); err != nil {
			return err
		}
		if err := tx.SaveChoice("revoked", "choice", "grant", "ccr", []byte("replacement")); err != nil {
			return err
		}
		return tx.SavePlan("revoked", "plan", "digest", []byte("plan"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { return tx.Delete("auth", 100) }); err != nil {
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
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { return tx.Expire(100 + MiddlewareGraceSeconds - 1) }); err != nil {
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
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { return tx.Expire(100 + MiddlewareGraceSeconds) }); err != nil {
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
	s, err := Open(filepath.Join(t.TempDir(), "store.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.InitMiddleware(ctx); err != nil {
		t.Fatal(err)
	}
	const scopes, choices = 300, 100200
	seed := []string{
		fmt.Sprintf(`WITH RECURSIVE n(v) AS (SELECT 0 UNION ALL SELECT v+1 FROM n WHERE v<%d)
INSERT INTO middleware_scopes(id,authority,manifest,sequence,expires_at) SELECT 'scope-'||v,'auth-'||(v%%8),x'',0,-1 FROM n`, scopes-1),
		fmt.Sprintf(`WITH RECURSIVE n(v) AS (SELECT 0 UNION ALL SELECT v+1 FROM n WHERE v<%d)
INSERT INTO middleware_choices(scope,id,payload,grant_id,ccr_handle) SELECT 'scope-'||(v%%%d),'choice-'||v,x'',   'grant-'||v,'' FROM n`, choices-1, scopes),
		`INSERT INTO middleware_receipts(authority,id,digest,payload,expires_at) VALUES ('auth-0','receipt','hash',x'',1)`,
		`INSERT INTO middleware_originals(authority,digest) VALUES ('auth-0','original')`,
	}
	for _, statement := range seed {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	admit := func() error {
		return s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
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
		if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { return tx.Expire(now) }); err != nil {
			t.Fatal(err)
		}
	}
	var rows, size int64
	if err := s.db.QueryRow(`SELECT rows,bytes FROM middleware_usage`).Scan(&rows, &size); err != nil {
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
	s, err := Open(filepath.Join(t.TempDir(), "store.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.InitMiddleware(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
		if err := tx.SaveScope(MiddlewareScope{ID: "revoked", Authority: "auth", Manifest: []byte("[]"), ExpiresAt: 1 << 40}); err != nil {
			return err
		}
		return tx.SaveChoice("revoked", "choice", "grant", "ccr", []byte("replacement"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { return tx.Delete("auth", 100) }); err != nil {
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
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { return tx.Expire(101) }); err != nil {
		t.Fatal(err)
	}
	assertTypedDeleted(t, "after an Expire pass inside the grace period")

	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { return tx.Expire(100 + MiddlewareGraceSeconds - 1) }); err != nil {
		t.Fatal(err)
	}
	assertTypedDeleted(t, "one tick before the grace deadline")

	// Past the deadline the row is reclaimed with the rest of the scope; that
	// is the capacity leak this whole change exists to fix.
	if err := s.WithMiddleware(ctx, func(tx *MiddlewareTx) error { return tx.Expire(100 + MiddlewareGraceSeconds) }); err != nil {
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
