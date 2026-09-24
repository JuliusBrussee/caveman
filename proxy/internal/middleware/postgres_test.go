package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/store"
	"github.com/jackc/pgx/v5"
)

// postgresReplicas returns n runtimes, each with its own connection pool, on
// one fresh schema of the database at CAVEMAN_TEST_POSTGRES_URL: n replicas of
// an HA deployment. Skipped when the variable is unset.
func postgresReplicas(t *testing.T, n int) []*Runtime {
	t.Helper()
	base := os.Getenv("CAVEMAN_TEST_POSTGRES_URL")
	if base == "" {
		t.Skip("CAVEMAN_TEST_POSTGRES_URL is unset")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("mw_runtime_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(ctx)
	})
	u, _ := url.Parse(base)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	replicas := make([]*Runtime, n)
	for i := range replicas {
		shared, err := store.OpenPostgresMiddleware(ctx, u.String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(shared.Close)
		// Replicas start together; migrations must not trip over each other.
		if replicas[i], err = New(Config{Store: shared, Mode: "compress", Limits: Limits{DeadlineMS: 10000}, Identify: bearerIdentity}); err != nil {
			t.Fatal(err)
		}
	}
	return replicas
}

// A5: a handle minted by one replica is retrieved, continued and revoked
// through another; no affinity needed.
func TestPostgresHandleMintedOnOneReplicaServesOnAnother(t *testing.T) {
	replicas := postgresReplicas(t, 2)
	a, b := replicas[0], replicas[1]
	if _, caps := capabilities(t, a, clientFeatures); caps["persistent"] != true {
		t.Fatal("Postgres store reported ephemeral")
	}
	req := requestFor(a)
	first := optimizeOK(t, a, req).Replacements[0]
	page := recovered(t, b, req.Scope, first.RecoveryHandle, "alice")
	if page.Text != req.Segments[0].Content || !page.Complete {
		t.Fatal("replica B did not return replica A's exact original")
	}
	if w := send(t, b, "retrieve", RetrieveRequest{SchemaVersion: 1, Scope: req.Scope, Handle: first.RecoveryHandle}, "bob", clientFeatures); w.Code != 404 {
		t.Fatalf("another principal reached the handle through replica B: %d", w.Code)
	}
	// The next turn on replica B reuses A's choice byte for byte.
	req.Sequence, req.RequestID, req.IdempotencyKey = 1, "req-2", "req-2"
	req.Segments[0].CacheRegion = "frozen_prefix"
	req.ContextManifest = append(req.ContextManifest, ManifestItem{"msg-2", digest([]byte("turn 2"))})
	next := optimizeOK(t, b, req)
	if len(next.Replacements) != 1 || next.Replacements[0].Text != first.Text || !next.Replacements[0].Reused {
		t.Fatalf("replica B changed replica A's bytes: %+v", next.Replacements)
	}
	if w := send(t, b, "sessions/delete", SessionDeleteRequest{SchemaVersion: 1, Scope: req.Scope}, "alice", clientFeatures); w.Code != 200 {
		t.Fatalf("delete on B: %d %s", w.Code, w.Body)
	}
	if w := send(t, a, "retrieve", RetrieveRequest{SchemaVersion: 1, Scope: req.Scope, Handle: first.RecoveryHandle}, "alice", clientFeatures); w.Code != 410 {
		t.Fatalf("replica A still served a session deleted on B: %d %s", w.Code, w.Body)
	}
}

// The SQLite single-writer guarantee, across replicas: concurrent first turns
// of one scope publish exactly one durable replacement and all succeed.
func TestPostgresConcurrentReplicasChooseOneDurableReplacement(t *testing.T) {
	replicas := postgresReplicas(t, 3)
	const writers = 24
	var wg sync.WaitGroup
	results := make(chan OptimizeResponse, writers)
	failures := make(chan error, writers)
	alice, _ := everyNamespace("alice")
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := replicas[i%len(replicas)]
			req := requestFor(r)
			req.RequestID = fmt.Sprintf("req-%d", i)
			req.IdempotencyKey = req.RequestID
			b, _ := json.Marshal(req)
			out, err := r.optimize(context.Background(), alice, req, digest(b), negotiated{})
			if err != nil {
				failures <- err
				return
			}
			results <- out
		}(i)
	}
	wg.Wait()
	close(failures)
	close(results)
	for err := range failures {
		t.Fatal(err)
	}
	text, unique := "", 0
	for out := range results {
		if len(out.Replacements) != 1 {
			t.Fatal("missing replacement")
		}
		p := out.Replacements[0]
		if text != "" && text != p.Text {
			t.Fatal("competing durable choices")
		}
		text = p.Text
		if !p.Reused {
			unique++
		}
	}
	if unique != 1 {
		t.Fatalf("booked %d unique reductions", unique)
	}
	// Revocation racing new turns on other replicas never leaves a live scope
	// behind: once delete returns, every later turn is refused.
	req := requestFor(replicas[0])
	var race sync.WaitGroup
	for i := 1; i < len(replicas); i++ {
		race.Add(1)
		go func(i int) {
			defer race.Done()
			turn := req
			turn.Sequence, turn.RequestID, turn.IdempotencyKey = 1, fmt.Sprintf("racer-%d", i), fmt.Sprintf("racer-%d", i)
			_ = send(t, replicas[i], "optimize", turn, "alice", clientFeatures)
		}(i)
	}
	if w := send(t, replicas[0], "sessions/delete", SessionDeleteRequest{SchemaVersion: 1, Scope: req.Scope}, "alice", clientFeatures); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	race.Wait()
	req.Sequence, req.RequestID, req.IdempotencyKey = 2, "after-delete", "after-delete"
	for _, r := range replicas {
		if w := send(t, r, "optimize", req, "alice", clientFeatures); w.Code != 410 {
			t.Fatalf("turn after delete: %d %s", w.Code, w.Body)
		}
	}
}
