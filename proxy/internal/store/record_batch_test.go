package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
)

func benchRow(i int) gateway.RequestRecord {
	return gateway.RequestRecord{
		Timestamp: "2026-10-07 00:00:00.000", RequestID: "req-" + strconv.Itoa(i), TraceID: "trace",
		Label: "local", AgentSlug: "unlabeled-agent", Provider: "anthropic", Model: "claude-sonnet-4-5",
		RouteFrom: "claude-sonnet-4-5", RouteTo: "claude-sonnet-4-5", Endpoint: "/v1/messages",
		StatusCode: 200, InputTokens: 1200, OutputTokens: 60, CachedInputTokens: 48000,
		TotalCostUSD: 0.02, Basis: "inferred", TokenUsageBasis: "provider_complete", AuthMode: "payg",
		RuntimeMode: "record", OptimizationIDs: []string{}, CacheStatus: "hit",
		RawRequestSHA256: "aa", TransformedRequestSHA256: "aa", RequestHashComplete: true,
	}
}

// BenchmarkRecord is one request row into a file-backed store, one caller
// (serial) or eight at once (parallel, run with -cpu 8).
func BenchmarkRecord(b *testing.B) {
	for _, parallel := range []bool{false, true} {
		b.Run("parallel="+strconv.FormatBool(parallel), func(b *testing.B) {
			s, err := Open(filepath.Join(b.TempDir(), "caveman.db"), nil)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			if parallel {
				b.RunParallel(func(pb *testing.PB) {
					for i := 0; pb.Next(); i++ {
						s.Record(benchRow(i))
					}
				})
			} else {
				for i := 0; i < b.N; i++ {
					s.Record(benchRow(i))
				}
			}
			b.StopTimer()
			_ = s.Close()
		})
	}
}

// BenchmarkRecordBatch is the async writer's insert: rows a few at a time,
// one transaction each, reported per row.
func BenchmarkRecordBatch(b *testing.B) {
	for _, size := range []int{8, 64} {
		b.Run("rows="+strconv.Itoa(size), func(b *testing.B) {
			s, err := Open(filepath.Join(b.TempDir(), "caveman.db"), nil)
			if err != nil {
				b.Fatal(err)
			}
			batch := make([]gateway.RequestRecord, size)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i += size {
				for j := range batch {
					batch[j] = benchRow(i + j)
				}
				s.RecordBatch(batch)
			}
			b.StopTimer()
			_ = s.Close()
		})
	}
}

func storedRequestRows(t *testing.T, s *Store) [][]any {
	t.Helper()
	rows, err := s.db.Query(`SELECT * FROM requests ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, _ := rows.Columns()
	var out [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		out = append(out, values)
	}
	return out
}

// TestRecordBatchStoresWhatRecordStores: one transaction for several rows
// stores, column for column, what one Record per row stores, the
// persistence boundary's re-zeroing included.
func TestRecordBatchStoresWhatRecordStores(t *testing.T) {
	delta, negative := 0.0012, -1.0
	rows := []gateway.RequestRecord{benchRow(1), benchRow(2), benchRow(3)}
	rows[1].SessionID, rows[1].SessionCorrelationBasis, rows[1].CacheBust = "session-1", "signed_marker", true
	rows[1].RequestTokensBefore, rows[1].RequestTokensAfter, rows[1].RequestTokenBasis = 900, 800, "estimated_request_json_o200k_v1"
	rows[1].RequestMeasurementStatus, rows[1].RequestEstimatedInputDeltaUSD = "measured", &delta
	rows[2].InputTokens, rows[2].CachedInputTokens = 10, 20 // malformed: re-zeroed on the way in
	rows[2].WouldSaveTokens, rows[2].WouldSaveUSD = 5, &negative
	single, err := Open(filepath.Join(t.TempDir(), "single.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer single.Close()
	batch, err := Open(filepath.Join(t.TempDir(), "batch.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Close()
	for _, row := range rows {
		single.Record(row)
	}
	batch.RecordBatch(rows)
	want, got := storedRequestRows(t, single), storedRequestRows(t, batch)
	if len(want) != len(rows) || !reflect.DeepEqual(got, want) {
		t.Fatalf("batch rows differ:\n got %v\nwant %v", got, want)
	}
}

// TestStoreConnectionPragmas: every pooled connection waits on a busy
// database, uses WAL and keeps synchronous FULL, also after RecordBatch
// committed on one of them with NORMAL.
func TestStoreConnectionPragmas(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.RecordBatch([]gateway.RequestRecord{benchRow(1), benchRow(2)})
	if rows := storedRequestRows(t, s); len(rows) != 2 {
		t.Fatalf("batch stored %d rows, want 2", len(rows))
	}
	conns := make([]*sql.Conn, 3) // the batch's connection is among them
	for i := range conns {
		if conns[i], err = s.db.Conn(t.Context()); err != nil {
			t.Fatal(err)
		}
		defer conns[i].Close()
	}
	for _, conn := range conns {
		var synchronous, busy int
		var journal string
		for pragma, dst := range map[string]any{"synchronous": &synchronous, "busy_timeout": &busy, "journal_mode": &journal} {
			if err := conn.QueryRowContext(t.Context(), "PRAGMA "+pragma).Scan(dst); err != nil {
				t.Fatal(err)
			}
		}
		if got := fmt.Sprintf("%d %d %s", synchronous, busy, journal); got != "2 5000 wal" {
			t.Fatalf("synchronous busy_timeout journal_mode = %s, want 2 5000 wal", got)
		}
	}
}
