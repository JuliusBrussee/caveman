package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
)

// Issue #1371: the cache component digest list (up to 1,024 digests, 66 KB)
// sat in the requests row before the accounting columns, so every stats read
// walked its overflow chain. These tests pin where the list lives now.

var (
	evidenceBuild = strings.Repeat("a", 64)
	evidencePlan  = strings.Repeat("b", 64)
)

func componentList(n int, seed byte) string {
	parts := make([]string, n)
	for i := range parts {
		digit := "0123456789abcdef"[(int(seed)+i)%16]
		parts[i] = strings.Repeat(string(digit), 63) + string("0123456789abcdef"[i%16])
	}
	return strings.Join(parts, ",")
}

func evidenceRow(id string, components string) gateway.RequestRecord {
	rec := benchRow(0)
	rec.RequestID = id
	rec.SessionID = "support:session-1371"
	rec.AgentBuildSHA256 = evidenceBuild
	rec.EfficiencyPlanSHA256 = evidencePlan
	if components != "" {
		rec.ProviderCachePrefixSHA256 = strings.Repeat("c", 64)
		rec.ProviderCacheComponentSHA256 = components
		rec.CacheBoundaryKnown = true
	}
	return rec
}

func openEvidenceStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// legacyComponentColumns returns request_id -> the legacy requests column.
func legacyComponentColumns(t *testing.T, s *Store) map[string]sql.NullString {
	t.Helper()
	rows, err := s.db.Query(`SELECT request_id, provider_cache_component_sha256 FROM requests ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]sql.NullString{}
	for rows.Next() {
		var id string
		var value sql.NullString
		if err := rows.Scan(&id, &value); err != nil {
			t.Fatal(err)
		}
		out[id] = value
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// sideComponentRows returns request_id -> the side-table digest list.
func sideComponentRows(t *testing.T, s *Store) map[string]string {
	t.Helper()
	rows, err := s.db.Query(`SELECT r.request_id, c.digests FROM request_cache_components c JOIN requests r ON r.id = c.request_id`)
	if err != nil {
		t.Fatalf("read request_cache_components: %v", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, digests string
		if err := rows.Scan(&id, &digests); err != nil {
			t.Fatal(err)
		}
		out[id] = digests
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

type evidenceView struct {
	components string
	available  bool
}

func evidenceByRequest(t *testing.T, s *Store) (map[string]evidenceView, []string) {
	t.Helper()
	rows, err := s.AgentEvidenceForBuild("support:session-1371", evidenceBuild, evidencePlan)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]evidenceView{}
	order := []string{}
	for _, row := range rows {
		out[row.RequestID] = evidenceView{components: row.ProviderCacheComponentSHA256, available: row.ProviderCacheComponentsAvailable}
		order = append(order, row.RequestID)
	}
	return out, order
}

// assertComponentsOutsideRequestsRow checks the storage shape for rows written
// by the current proxy: no digest bytes in the requests row, the exact list in
// the side table, and the same list back through the evidence reader.
func assertComponentsOutsideRequestsRow(t *testing.T, s *Store, want map[string]string) {
	t.Helper()
	for id, value := range legacyComponentColumns(t, s) {
		if value.Valid {
			t.Fatalf("request %s keeps %d digest bytes in its requests row", id, len(value.String))
		}
	}
	side := sideComponentRows(t, s)
	evidence, _ := evidenceByRequest(t, s)
	for id, list := range want {
		if list == "" {
			if _, ok := side[id]; ok {
				t.Fatalf("request %s has a side row without a component list", id)
			}
			if got := evidence[id]; got.components != "" || got.available {
				t.Fatalf("request %s evidence = %+v, want unavailable", id, got)
			}
			continue
		}
		if side[id] != list {
			t.Fatalf("request %s side row = %q, want %q", id, side[id], list)
		}
		if got := evidence[id]; got.components != list || !got.available {
			t.Fatalf("request %s evidence = %+v, want the exact list, available", id, got)
		}
	}
}

func TestRecordStoresCacheComponentsOutsideRequestsRow(t *testing.T) {
	s := openEvidenceStore(t)
	want := map[string]string{"with": componentList(300, 1), "without": ""}
	s.Record(evidenceRow("with", want["with"]))
	s.Record(evidenceRow("without", ""))
	assertComponentsOutsideRequestsRow(t, s, want)
}

func TestRecordBatchStoresCacheComponentsOutsideRequestsRow(t *testing.T) {
	s := openEvidenceStore(t)
	want := map[string]string{"b1": componentList(5, 2), "b2": "", "b3": componentList(1024, 3)}
	s.RecordBatch([]gateway.RequestRecord{evidenceRow("b1", want["b1"]), evidenceRow("b2", ""), evidenceRow("b3", want["b3"])})
	assertComponentsOutsideRequestsRow(t, s, want)
	if _, order := evidenceByRequest(t, s); strings.Join(order, ",") != "b1,b2,b3" {
		t.Fatalf("evidence order = %v, want provider-call order", order)
	}
}

// TestRecordBatchFallbackStoresCacheComponentsOutsideRequestsRow: one row that
// cannot be inserted fails the batch; every other row then goes through Record
// and must land in the same shape.
func TestRecordBatchFallbackStoresCacheComponentsOutsideRequestsRow(t *testing.T) {
	s := openEvidenceStore(t)
	if _, err := s.db.Exec(`CREATE TRIGGER poison_row BEFORE INSERT ON requests WHEN NEW.request_id = 'poison'
		BEGIN SELECT RAISE(ABORT, 'poison row'); END`); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"f1": componentList(7, 4), "f2": componentList(2, 5)}
	s.RecordBatch([]gateway.RequestRecord{evidenceRow("f1", want["f1"]), evidenceRow("poison", componentList(3, 6)), evidenceRow("f2", want["f2"])})
	if got := len(legacyComponentColumns(t, s)); got != 2 {
		t.Fatalf("fallback stored %d rows, want 2", got)
	}
	assertComponentsOutsideRequestsRow(t, s, want)
	if _, ok := sideComponentRows(t, s)["poison"]; ok {
		t.Fatal("the refused row left a side row behind")
	}
	var orphans int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM request_cache_components WHERE request_id NOT IN (SELECT id FROM requests)`).Scan(&orphans); err != nil || orphans != 0 {
		t.Fatalf("orphan side rows = %d, err = %v", orphans, err)
	}
}

// TestRefusedComponentListIsUnavailable: sanitizeRequestRecord blanks a list it
// refuses, so the stored row has no list and the evidence says so.
func TestRefusedComponentListIsUnavailable(t *testing.T) {
	s := openEvidenceStore(t)
	refused := evidenceRow("refused", strings.Repeat("z", 64))
	s.Record(refused)
	assertComponentsOutsideRequestsRow(t, s, map[string]string{"refused": ""})
}

var migrationNow = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func storeTS(at time.Time) string { return at.UTC().Format(storeTSLayout) }

// insertLegacyRow writes a row the way a proxy before #1371 did: the list in
// the requests row, nothing in request_cache_components.
func insertLegacyRow(t *testing.T, s *Store, id string, at time.Time, legacy any) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO requests (ts, request_id, session_id, session_correlation_basis,
		agent_build_sha256, efficiency_plan_sha256, provider, model, auth_mode, token_usage_basis,
		input_tokens, output_tokens, cached_input_tokens, total_cost_usd, basis,
		provider_cache_prefix_sha256, provider_cache_component_sha256, cache_boundary_known)
		VALUES (?, ?, 'support:session-1371', 'explicit_header', ?, ?, 'anthropic', 'claude-sonnet-4-5', 'payg',
		'provider_complete', 1200, 60, 400, 0.02, 'inferred', ?, ?, 1)`,
		storeTS(at), id, evidenceBuild, evidencePlan, strings.Repeat("c", 64), legacy); err != nil {
		t.Fatal(err)
	}
}

// accountingSnapshot is every accounting view the migration must not change.
func accountingSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	summary, err := s.Summary()
	if err != nil {
		t.Fatal(err)
	}
	var reports []StatsReport
	for _, days := range []int{0, 7, 30} {
		report, err := s.BuildStatsReport(StatsReportOptions{Days: days, Now: migrationNow})
		if err != nil {
			t.Fatal(err)
		}
		reports = append(reports, report)
	}
	encoded, err := json.Marshal(map[string]any{"summary": summary, "reports": reports})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func migratedMark(t *testing.T, s *Store) int64 {
	t.Helper()
	var mark int64
	err := s.db.QueryRow(`SELECT value FROM store_maintenance WHERE key = ?`, cacheComponentsMigratedKey).Scan(&mark)
	if errors.Is(err, sql.ErrNoRows) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return mark
}

func TestMigrateMovesLegacyComponentListsOutOfRequestsRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caveman.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	lists := map[string]string{"recent": componentList(300, 7), "old": componentList(40, 8), "late": componentList(9, 9), "current": componentList(3, 10)}
	insertLegacyRow(t, s, "recent", migrationNow.Add(-24*time.Hour), lists["recent"])
	insertLegacyRow(t, s, "old", migrationNow.Add(-20*24*time.Hour), lists["old"])
	insertLegacyRow(t, s, "empty", migrationNow.Add(-time.Hour), "")
	insertLegacyRow(t, s, "null", migrationNow.Add(-time.Hour), nil)
	current := evidenceRow("current", lists["current"])
	current.Timestamp = storeTS(migrationNow.Add(-30 * time.Minute))
	s.Record(current)
	before := accountingSnapshot(t, s)

	// An interrupted migration: one batch, then the process stops.
	defer func(rows int64) { cacheComponentBatchRows = rows }(cacheComponentBatchRows)
	cacheComponentBatchRows = 2
	if done, err := s.migrateCacheComponentBatch(t.Context(), cacheComponentCutoff(migrationNow)); err != nil || done {
		t.Fatalf("first batch: done = %v, err = %v", done, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(path, nil); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.migrateCacheComponents(t.Context(), migrationNow); err != nil {
		t.Fatal(err)
	}

	for id, value := range legacyComponentColumns(t, s) {
		if value.Valid {
			t.Fatalf("request %s keeps a legacy list after the migration: %q", id, value.String)
		}
	}
	side := sideComponentRows(t, s)
	if len(side) != 2 || side["recent"] != lists["recent"] || side["current"] != lists["current"] {
		t.Fatalf("side rows after migration = %d rows, recent ok %v, current ok %v", len(side), side["recent"] == lists["recent"], side["current"] == lists["current"])
	}
	evidence, _ := evidenceByRequest(t, s)
	want := map[string]evidenceView{
		"recent":  {lists["recent"], true},
		"current": {lists["current"], true},
		"old":     {"", false}, // outside retention: cleared, not copied
		"empty":   {"", false},
		"null":    {"", false},
	}
	for id, w := range want {
		if evidence[id] != w {
			t.Fatalf("evidence %s = %+v, want %+v", id, evidence[id], w)
		}
	}
	if after := accountingSnapshot(t, s); after != before {
		t.Fatalf("the migration changed accounting:\nbefore %s\n after %s", before, after)
	}

	// A second run finds nothing to do.
	mark := migratedMark(t, s)
	if err := s.migrateCacheComponents(t.Context(), migrationNow); err != nil {
		t.Fatal(err)
	}
	if again := sideComponentRows(t, s); len(again) != 2 || migratedMark(t, s) != mark {
		t.Fatalf("re-run changed the store: %d side rows, mark %d -> %d", len(again), mark, migratedMark(t, s))
	}

	// An older proxy (after a downgrade) wrote a legacy list above the mark.
	insertLegacyRow(t, s, "late", migrationNow.Add(-2*time.Hour), lists["late"])
	if err := s.migrateCacheComponents(t.Context(), migrationNow); err != nil {
		t.Fatal(err)
	}
	if got := sideComponentRows(t, s)["late"]; got != lists["late"] {
		t.Fatalf("late legacy list not moved: %q", got)
	}
	if value := legacyComponentColumns(t, s)["late"]; value.Valid {
		t.Fatal("late legacy list still in its requests row")
	}
}

func TestExpireCacheComponentsKeepsAccounting(t *testing.T) {
	s := openEvidenceStore(t)
	lists := map[string]string{"fresh": componentList(20, 11), "stale": componentList(20, 12)}
	fresh := evidenceRow("fresh", lists["fresh"])
	fresh.Timestamp = storeTS(migrationNow.Add(-13 * 24 * time.Hour))
	stale := evidenceRow("stale", lists["stale"])
	stale.Timestamp = storeTS(migrationNow.Add(-15 * 24 * time.Hour))
	s.RecordBatch([]gateway.RequestRecord{stale, fresh})
	before := accountingSnapshot(t, s)

	expired, err := s.expireCacheComponents(t.Context(), migrationNow)
	if err != nil {
		t.Fatal(err)
	}
	if expired != 1 {
		t.Fatalf("expired %d lists, want 1", expired)
	}
	side := sideComponentRows(t, s)
	if len(side) != 1 || side["fresh"] != lists["fresh"] {
		t.Fatalf("side rows after expiry = %v", side)
	}
	evidence, _ := evidenceByRequest(t, s)
	if evidence["stale"] != (evidenceView{"", false}) || evidence["fresh"] != (evidenceView{lists["fresh"], true}) {
		t.Fatalf("evidence after expiry = %+v", evidence)
	}
	if after := accountingSnapshot(t, s); after != before {
		t.Fatalf("expiry changed accounting:\nbefore %s\n after %s", before, after)
	}
}

func TestCacheComponentMaintenanceStopsWithContext(t *testing.T) {
	s := openEvidenceStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.RunCacheComponentMaintenance(ctx)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance did not stop when its context ended")
	}
}
