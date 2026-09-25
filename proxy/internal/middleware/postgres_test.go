package middleware

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// postgresReplicas returns n runtimes, each with its own connection pool, on
// one fresh schema of the database at CAVEMAN_TEST_POSTGRES_URL: n replicas of
// an HA deployment. Skipped when the variable is unset.
func postgresReplicas(t *testing.T, n int) []*Runtime {
	t.Helper()
	u := postgresSchema(t)
	ctx := context.Background()
	replicas := make([]*Runtime, n)
	for i := range replicas {
		shared, err := store.OpenPostgresMiddleware(ctx, u)
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

// postgresSchema returns CAVEMAN_TEST_POSTGRES_URL pointed at a fresh schema,
// dropped when t ends. Skipped when the variable is unset.
func postgresSchema(t *testing.T) string {
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
	return u.String()
}

// backend is one middleware store under test, with raw SQL both dialects
// accept for seeding and assertions.
type backend struct {
	store store.MiddlewareStore
	exec  func(t *testing.T, query string)
	count func(t *testing.T, query string) int64
}

// eachStore runs test on SQLite and, when CAVEMAN_TEST_POSTGRES_URL is set, on
// a fresh Postgres schema.
func eachStore(t *testing.T, test func(t *testing.T, b backend)) {
	t.Run("sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "spend.db")
		s, err := store.Open(path, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		test(t, backend{s, func(t *testing.T, query string) {
			t.Helper()
			if _, err := db.Exec(query); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, query string) (n int64) {
			t.Helper()
			if err := db.QueryRow(query).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}})
	})
	t.Run("postgres", func(t *testing.T) {
		u := postgresSchema(t)
		ctx := context.Background()
		p, err := store.OpenPostgresMiddleware(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		conn, err := pgx.Connect(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close(ctx) })
		test(t, backend{p, func(t *testing.T, query string) {
			t.Helper()
			if _, err := conn.Exec(ctx, query); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, query string) (n int64) {
			t.Helper()
			if err := conn.QueryRow(ctx, query).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}})
	})
}

// runtimeOn is a compress-mode runtime over s with the fixtures' identity.
func runtimeOn(t *testing.T, s store.MiddlewareStore, change func(*Config)) *Runtime {
	t.Helper()
	cfg := Config{Store: s, Mode: "compress", Limits: Limits{DeadlineMS: 5000}, Identify: bearerIdentity}
	if change != nil {
		change(&cfg)
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
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

// statements counts every statement a pool sends, BEGIN and COMMIT included.
type statements struct{ n atomic.Int64 }

func (s *statements) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	s.n.Add(1)
	return ctx
}
func (s *statements) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// GO-1: an optimize is a constant number of round trips to Postgres, however
// many segments it carries; a first turn and a turn reusing every choice alike.
func TestPostgresOptimizeStatementsDoNotGrowWithSegments(t *testing.T) {
	u := postgresSchema(t)
	config, err := pgxpool.ParseConfig(u)
	if err != nil {
		t.Fatal(err)
	}
	counter := &statements{}
	config.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	r := runtimeOn(t, store.NewPostgresMiddleware(pool), nil)
	turns := func(n int) (first, reuse int64) {
		req := requestFor(r)
		req.Scope.SessionID = fmt.Sprint("segments-", n)
		req.Segments, req.ContextManifest = nil, nil
		for i := range n {
			content := noisy() + fmt.Sprintf("[ERROR] segment %d of %d\r\n", i, n)
			s := Segment{ID: fmt.Sprint("tool-", i), Kind: "tool_result", CacheRegion: "live_zone", Content: content, SHA256: digest([]byte(content)), SourceID: fmt.Sprint("document-", i)}
			req.Segments = append(req.Segments, s)
			req.ContextManifest = append(req.ContextManifest, ManifestItem{s.ID, s.SHA256})
		}
		for turn, count := range []*int64{&first, &reuse} {
			if turn == 1 {
				req = nextTurn(req, "reuse")
			}
			counter.n.Store(0)
			if plan := optimizeOK(t, r, req); len(plan.Replacements) != n || plan.Replacements[0].Reused != (turn == 1) {
				t.Fatalf("%d segments, turn %d: %d replacements", n, turn+1, len(plan.Replacements))
			}
			*count = counter.n.Load()
		}
		return first, reuse
	}
	first2, reuse2 := turns(2)
	first32, reuse32 := turns(32)
	t.Logf("statements per optimize: first turn %d (2 segments) / %d (32); reuse %d / %d", first2, first32, reuse2, reuse32)
	if first2 != first32 || reuse2 != reuse32 {
		t.Fatalf("statements grow with segments: first turn %d -> %d, reuse %d -> %d", first2, first32, reuse2, reuse32)
	}
}
