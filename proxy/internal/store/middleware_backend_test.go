package store

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// middlewareBackend is one middleware store under test, with raw SQL for seeding
// and assertions. Seeds are written in the SQL both dialects accept.
type middlewareBackend struct {
	MiddlewareStore
	exec  func(t *testing.T, query string)
	count func(t *testing.T, query string) int64
	// reopen returns a second, independent store over the same data: another
	// process on the SQLite file, another replica on Postgres.
	reopen func(t *testing.T) MiddlewareStore
}

// eachMiddlewareBackend runs test on SQLite, and on Postgres when
// CAVEMAN_TEST_POSTGRES_URL names a database the test may create schemas in.
// Each Postgres run gets its own schema, dropped afterwards.
func eachMiddlewareBackend(t *testing.T, test func(t *testing.T, b middlewareBackend)) {
	t.Run("sqlite", func(t *testing.T) { test(t, sqliteBackend(t)) })
	t.Run("postgres", func(t *testing.T) {
		base := os.Getenv("CAVEMAN_TEST_POSTGRES_URL")
		if base == "" {
			t.Skip("CAVEMAN_TEST_POSTGRES_URL is unset")
		}
		test(t, postgresBackend(t, base))
	})
}

func sqliteBackend(t *testing.T) middlewareBackend {
	path := filepath.Join(t.TempDir(), "store.db")
	open := func(t *testing.T) *Store {
		s, err := Open(path, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		if err := s.InitMiddleware(context.Background()); err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := open(t)
	return middlewareBackend{
		MiddlewareStore: s,
		exec: func(t *testing.T, query string) {
			t.Helper()
			if _, err := s.db.Exec(query); err != nil {
				t.Fatal(err)
			}
		},
		count: func(t *testing.T, query string) (n int64) {
			t.Helper()
			if err := s.db.QueryRow(query).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		},
		reopen: func(t *testing.T) MiddlewareStore { return open(t) },
	}
}

func postgresBackend(t *testing.T, base string) middlewareBackend {
	ctx := context.Background()
	u := postgresTestSchema(t, base)
	open := func(t *testing.T) *PostgresMiddleware {
		p, err := OpenPostgresMiddleware(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		if err := p.InitMiddleware(ctx); err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := open(t)
	return middlewareBackend{
		MiddlewareStore: p,
		exec: func(t *testing.T, query string) {
			t.Helper()
			if _, err := p.pool.Exec(ctx, query); err != nil {
				t.Fatal(err)
			}
		},
		count: func(t *testing.T, query string) (n int64) {
			t.Helper()
			if err := p.pool.QueryRow(ctx, query).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		},
		reopen: func(t *testing.T) MiddlewareStore { return open(t) },
	}
}

// postgresTestSchema creates a fresh schema in the database at base and returns
// base with search_path pointing at it; the schema is dropped when t ends.
func postgresTestSchema(t *testing.T, base string) string {
	t.Helper()
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("mw_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(ctx)
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}
