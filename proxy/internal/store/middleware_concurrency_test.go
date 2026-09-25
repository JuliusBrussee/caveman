package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"
)

// heldWhile runs first in a write transaction that stays open until the
// returned release is called, and second in another write transaction started
// once first is inside. It reports whether second finished before release.
func heldWhile(t *testing.T, s MiddlewareStore, first, second func(*MiddlewareTx) error) (early bool, release func()) {
	t.Helper()
	ctx := context.Background()
	inside, hold, firstDone, secondDone := make(chan struct{}), make(chan struct{}), make(chan error, 1), make(chan error, 1)
	go func() {
		firstDone <- s.WithMiddleware(ctx, func(tx *MiddlewareTx) error {
			if err := first(tx); err != nil {
				return err
			}
			close(inside)
			<-hold
			return nil
		})
	}()
	select {
	case <-inside:
	case err := <-firstDone:
		t.Fatalf("first writer: %v", err)
	}
	go func() { secondDone <- s.WithMiddleware(ctx, second) }()
	select {
	case err := <-secondDone:
		early = true
		if err != nil {
			t.Errorf("second writer: %v", err)
		}
	case <-time.After(300 * time.Millisecond):
	}
	return early, func() {
		close(hold)
		if err := <-firstDone; err != nil {
			t.Fatalf("first writer: %v", err)
		}
		if !early {
			if err := <-secondDone; err != nil {
				t.Errorf("second writer: %v", err)
			}
		}
	}
}

// Writers of one scope or authority never interleave: a decision read waits
// for a competing writer's commit and then sees it. SQLite gets this from its
// single writer; Postgres from its per-authority lock, which a scope that does
// not exist yet takes on its id.
func TestMiddlewareWritersOfOneAuthorityDoNotInterleave(t *testing.T) {
	eachMiddlewareBackend(t, testMiddlewareWritersOfOneAuthorityDoNotInterleave)
}

func testMiddlewareWritersOfOneAuthorityDoNotInterleave(t *testing.T, s middlewareBackend) {
	var seen MiddlewareScope
	var seenErr error
	read := func(tx *MiddlewareTx) error { seen, seenErr = tx.Scope("scope"); return nil }

	// A new scope: the second first-turn must see the first one's manifest (and
	// so check its own against it), not "no scope yet".
	early, release := heldWhile(t, s, func(tx *MiddlewareTx) error {
		if _, err := tx.Scope("scope"); !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return tx.SaveScope(MiddlewareScope{ID: "scope", Authority: "auth", Manifest: []byte("[1]"), ExpiresAt: 1 << 40, CreatedAt: 1})
	}, read)
	if early {
		t.Fatalf("second writer read a scope another writer was creating: %+v %v", seen, seenErr)
	}
	release()
	if seenErr != nil || string(seen.Manifest) != "[1]" {
		t.Fatalf("second writer did not see the committed scope: %+v %v", seen, seenErr)
	}

	// An existing scope being revoked: a turn must see the revocation, never
	// the live scope it could then renew over it.
	early, release = heldWhile(t, s, func(tx *MiddlewareTx) error { _, err := tx.Delete("auth", 50); return err }, read)
	if early {
		t.Fatalf("second writer read a scope during its revocation: %+v", seen)
	}
	release()
	if seenErr != nil || seen.ExpiresAt != -50 {
		t.Fatalf("second writer missed the revocation: %+v %v", seen, seenErr)
	}
}

// Only one replica sweeps at a time, and a sweep never waits for, or deletes
// under, a transaction writing the same data: an authority's writer, or a
// replica whose clock still renews a scope this one sees elapsed.
func TestPostgresSweepIsExclusiveAndSkipsBusyAuthorities(t *testing.T) {
	eachMiddlewareBackend(t, func(t *testing.T, s middlewareBackend) {
		if _, ok := s.MiddlewareStore.(*PostgresMiddleware); !ok {
			t.Skip("SQLite has one writer; there is nothing to skip")
		}
		ctx := context.Background()
		s.exec(t, `INSERT INTO middleware_scopes(id,authority,manifest,sequence,expires_at,created_at) VALUES
 ('busy','busy-auth','[]',0,10,1),('renewing','renewing-auth','[]',0,10,1),('idle','idle-auth','[]',0,10,1)`)
		purged := func(id string) bool {
			return s.count(t, `SELECT count(*) FROM middleware_scopes WHERE id='`+id+`' AND length(manifest)=0`) == 1
		}
		sweep := func(tx *MiddlewareTx) error { _, err := tx.Expire(11); return err }
		// A second replica's sweep while another holds the sweep does nothing,
		// at once, even with work the first has not reached.
		var n int64
		early, release := heldWhile(t, s, func(tx *MiddlewareTx) error { _, err := tx.Expire(5); return err },
			func(tx *MiddlewareTx) error { var err error; n, err = tx.Expire(11); return err })
		if !early || n != 0 || purged("idle") {
			t.Fatalf("concurrent sweep: returned early=%v, touched %d rows", early, n)
		}
		release()
		// One writer holds busy-auth; another replica, its clock behind, renews
		// renewing-auth. The sweep purges idle only, without waiting.
		early, release = heldWhile(t, s, func(tx *MiddlewareTx) error {
			if _, err := tx.Scope("busy"); err != nil {
				return err
			}
			return tx.Renew("renewing-auth", 5, 100, 1000)
		}, sweep)
		if !early || !purged("idle") || purged("busy") || purged("renewing") {
			t.Fatalf("sweep under writers: early=%v idle=%v busy=%v renewing=%v", early, purged("idle"), purged("busy"), purged("renewing"))
		}
		release()
		if err := s.WithMiddleware(ctx, sweep); err != nil {
			t.Fatal(err)
		}
		if !purged("busy") || purged("renewing") {
			t.Fatalf("once free: busy purged=%v, renewed scope purged=%v", purged("busy"), purged("renewing"))
		}
	})
}

// Replicas that start together migrate one after another under the advisory
// lock; without it, concurrent CREATE ... IF NOT EXISTS collide in the catalog.
func TestPostgresMigrationsAreSafeFromConcurrentReplicas(t *testing.T) {
	base := os.Getenv("CAVEMAN_TEST_POSTGRES_URL")
	if base == "" {
		t.Skip("CAVEMAN_TEST_POSTGRES_URL is unset")
	}
	ctx := context.Background()
	u := postgresTestSchema(t, base)
	const replicas = 8
	errs := make(chan error, replicas)
	stores := make([]*PostgresMiddleware, replicas)
	for i := range stores {
		p, err := OpenPostgresMiddleware(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		stores[i] = p
	}
	for _, p := range stores {
		go func() { errs <- p.InitMiddleware(ctx) }()
	}
	for range stores {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent migration: %v", err)
		}
	}
	var versions, stripes int
	if err := stores[0].pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM middleware_schema),(SELECT count(*) FROM middleware_usage)`).Scan(&versions, &stripes); err != nil {
		t.Fatal(err)
	}
	if versions != 1 || stripes != usageStripes {
		t.Fatalf("migration ran %d times, %d counter stripes", versions, stripes)
	}
}
