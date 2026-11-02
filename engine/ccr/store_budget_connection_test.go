//go:build !js

package ccr

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"strconv"
	"testing"
)

func TestStorageBudgetAppliesToEveryConnection(t *testing.T) {
	for _, pageSize := range []int64{4096, 8192} {
		t.Run(strconv.FormatInt(pageSize, 10), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ccr.db")
			initial, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := initial.Exec("PRAGMA page_size=" + strconv.FormatInt(pageSize, 10) + "; CREATE TABLE fixture(value TEXT)"); err != nil {
				t.Fatal(err)
			}
			if err := initial.Close(); err != nil {
				t.Fatal(err)
			}
			const budget = int64(1 << 20)
			store, err := OpenWithBudget(path, budget)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			first, err := store.db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close()
			// Keep the original open so a second physical connection is forced
			// without recreating the database's immutable journal generation.
			store.db.SetMaxOpenConns(2)
			second, err := store.db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer second.Close()
			for _, conn := range []*sql.Conn{first, second} {
				for pragma, want := range map[string]int64{
					"page_size":          pageSize,
					"max_page_count":     budget / pageSize,
					"wal_autocheckpoint": budget / pageSize / 16,
					"journal_size_limit": budget / 8,
				} {
					var got int64
					if err := conn.QueryRowContext(context.Background(), "PRAGMA "+pragma).Scan(&got); err != nil {
						t.Fatal(err)
					}
					if got != want {
						t.Errorf("%s = %d, want %d on every connection", pragma, got, want)
					}
				}
			}
		})
	}
}

func TestSummaryReportsPhysicalPageCapAndPreservesRecoveries(t *testing.T) {
	store, err := OpenWithBudget(filepath.Join(t.TempDir(), "ccr.db"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	original := []byte("existing exact recovery")
	handle, err := store.Put(Recovery{Original: original})
	if err != nil {
		t.Fatal(err)
	}
	var pages int64
	if err := store.db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("PRAGMA max_page_count=" + strconv.FormatInt(pages, 10)); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Put(Recovery{Original: bytes.Repeat([]byte("new"), 32<<10)}); !errors.Is(err, ErrBudgetExceeded) || got != "" {
		t.Fatalf("full store minted handle %q or wrong error: %v", got, err)
	}
	stats, err := store.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if !stats.StorageFull || stats.StorageBytes >= stats.MaxStorageBytes {
		t.Fatalf("physical limit not reported separately from payload usage: %+v", stats)
	}
	got, err := store.Get(handle)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("existing recovery changed: %q, %v", got, err)
	}
}

func TestStorageBudgetSurvivesRepeatedConnectionReplacement(t *testing.T) {
	const budget = int64(1 << 20)
	store, err := OpenWithBudget(filepath.Join(t.TempDir(), "ccr.db"), budget)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	original := []byte("recoverable across expired connections")
	handle, err := store.Put(Recovery{Original: original})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		conn, err := store.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		// ErrBadConn makes database/sql discard this physical connection.
		if err := conn.Raw(func(any) error { return driver.ErrBadConn }); !errors.Is(err, driver.ErrBadConn) {
			t.Fatal(err)
		}
		_ = conn.Close()
		got, err := store.Get(handle)
		if err != nil || !bytes.Equal(got, original) {
			t.Fatalf("replacement %d invalidated existing recovery: %v", i, err)
		}
		var pages, pageSize int64
		if err := store.db.QueryRow("PRAGMA max_page_count").Scan(&pages); err != nil {
			t.Fatal(err)
		}
		if err := store.db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
			t.Fatal(err)
		}
		if pages != budget/pageSize {
			t.Fatalf("replacement %d lost cap: pages=%d", i, pages)
		}
	}
	// The shared DSN is also used by unrelated memory stores. CCR's cap must
	// remain scoped to its connector, not installed on the global driver.
	db, err := sql.Open("sqlite", SQLiteDSN(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var maxPages int64
	if err := db.QueryRow("PRAGMA max_page_count").Scan(&maxPages); err != nil || maxPages <= budget/512 {
		t.Fatalf("CCR page cap leaked to another database: %d, %v", maxPages, err)
	}
}

func TestSummaryDoesNotTreatFreelistPagesAsFull(t *testing.T) {
	store, err := OpenWithBudget(filepath.Join(t.TempDir(), "ccr.db"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// A test-only table creates reusable pages without deleting recoveries.
	if _, err := store.db.Exec("CREATE TABLE fixture(data BLOB); INSERT INTO fixture VALUES (zeroblob(65536)); DROP TABLE fixture"); err != nil {
		t.Fatal(err)
	}
	var pages, free int64
	if err := store.db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow("PRAGMA freelist_count").Scan(&free); err != nil || free == 0 {
		t.Fatalf("fixture has no reusable pages: %d, %v", free, err)
	}
	if _, err := store.db.Exec("PRAGMA max_page_count=" + strconv.FormatInt(pages, 10)); err != nil {
		t.Fatal(err)
	}
	stats, err := store.Summary()
	if err != nil || stats.StorageFull {
		t.Fatalf("reusable pages reported full: %+v, %v", stats, err)
	}
	if _, err := store.Put(Recovery{Original: bytes.Repeat([]byte("fits"), 1024)}); err != nil {
		t.Fatalf("reusable pages did not accept recovery: %v", err)
	}
}

func TestStorageBudgetKeepsLegacyOverBudgetRecoveriesReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	original := bytes.Repeat([]byte("existing"), 16<<10)
	handle := Handle(original)
	// Old databases can already exceed a newly configured budget. Schema
	// migration must still finish so those existing handles remain readable.
	if _, err := db.Exec(`CREATE TABLE recoveries (
	  handle TEXT PRIMARY KEY, created_at TEXT NOT NULL,
	  content_type TEXT NOT NULL, compressor TEXT NOT NULL,
	  tokens_before INTEGER NOT NULL, tokens_after INTEGER NOT NULL,
	  original BLOB NOT NULL);
	  INSERT INTO recoveries VALUES (?, '', 'text', 'test', 0, 0, ?)`, handle, original); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenWithBudget(path, 64<<10)
	if err != nil {
		t.Fatalf("over-budget legacy store became unreadable: %v", err)
	}
	defer store.Close()
	got, err := store.Get(handle)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("legacy recovery changed: length=%d, %v", len(got), err)
	}
	stats, err := store.Summary()
	if err != nil || !stats.StorageFull {
		t.Fatalf("over-budget legacy store status: %+v, %v", stats, err)
	}
}
