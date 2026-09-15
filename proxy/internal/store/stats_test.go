package store

import (
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
)

func statsTestRecord(id, auth string) gateway.RequestRecord {
	delta := .002575
	return gateway.RequestRecord{
		Timestamp: "2026-09-08 12:00:00.000", RequestID: id, Provider: "anthropic", Model: "exact-model", AgentSlug: "claude", AuthMode: auth,
		StatusCode: 200, TokenUsageBasis: "provider_complete", InputTokens: 1000, OutputTokens: 100, CachedInputTokens: 600, CacheCreationInputTokens: 100, CacheCreation1hTokens: 40, ReasoningTokens: 20,
		RequestTokensBefore: 2000, RequestTokensAfter: 1500, RequestTokenBasis: "estimated_request_json_o200k_v1", RequestMeasurementStatus: "measured", RequestEstimatedInputDeltaUSD: &delta, RequestSavingsBasis: "estimated_tokens_x_observed_cache_mix",
		PricingKnown: true, PricingProvider: "anthropic", PricingModel: "exact-model", PricingCatalogVersion: "test-snapshot-2026-09-08", PriceInputPerMillion: 10, PriceOutputPerMillion: 30, PriceCacheReadPerMillion: 1, PriceCacheWritePerMillion: 12.5, PriceCacheWrite1hPerMillion: 20, PriceReasoningPerMillion: 30,
	}
}

func statsTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "stats.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func statsTestReport(t *testing.T, s *Store, opts StatsReportOptions) StatsReport {
	t.Helper()
	opts.Now = time.Date(2026, 9, 8, 13, 0, 0, 0, time.UTC)
	r, err := s.BuildStatsReport(opts)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func statsAssertMoney(t *testing.T, got *float64, want float64) {
	t.Helper()
	if got == nil || math.Abs(*got-want) > 1e-10 {
		t.Fatalf("money = %v, want %.10f", got, want)
	}
}

func TestStatsReportKeepsAPISavingsAndSubscriptionEquivalentSeparate(t *testing.T) {
	s := statsTestStore(t)
	s.Record(statsTestRecord("api", "payg"))
	s.Record(statsTestRecord("oauth", "oauth"))
	s.Record(statsTestRecord("subscription", "subscription"))
	r := statsTestReport(t, s, StatsReportOptions{Days: 30})
	if r.Totals.Requests != 3 || r.Totals.SavedTokens != 1500 || r.Totals.MeasuredRequests != 3 {
		t.Fatalf("totals: %+v", r.Totals)
	}
	statsAssertMoney(t, r.Totals.APISavingsUSD, .002575)
	statsAssertMoney(t, r.Totals.APIEquivalentSavingsUSD, .00515)
	// Fresh 300, read 600, 5m write 60, 1h write 40, output 80 + reasoning 20.
	statsAssertMoney(t, r.Totals.APISpendUSD, .00815)
	statsAssertMoney(t, r.Totals.APIEquivalentSpendUSD, .0163)
	if r.Totals.APISpendRequests != 1 || r.Totals.EquivalentSpendRequests != 2 || len(r.Groups) != 3 || len(r.AuthModes) != 3 {
		t.Fatalf("coverage: %+v", r)
	}
	if len(r.Evidence.CatalogVersions) != 1 || r.Evidence.CatalogVersions[0] != "test-snapshot-2026-09-08" {
		t.Fatalf("snapshot lost: %+v", r.Evidence)
	}
	// This deliberately nonexistent model can only price from the recorded
	// snapshot, never from a present-day model-family fallback.
	if r.Receipts[0].Price == nil || r.Receipts[0].Price.Model != "exact-model" {
		t.Fatal("price snapshot missing")
	}
	statsAssertMoney(t, r.Receipts[0].EffectiveInputRatePerMillion, 5.15)
}

func TestStatsReportUnknownAndIncompleteStayNull(t *testing.T) {
	s := statsTestStore(t)
	unknown := statsTestRecord("unknown", "unknown")
	s.Record(unknown)
	partial := statsTestRecord("partial", "payg")
	partial.TokenUsageBasis = "provider_partial"
	s.Record(partial)
	unpriced := statsTestRecord("unpriced", "payg")
	unpriced.PricingKnown = false
	s.Record(unpriced)
	r := statsTestReport(t, s, StatsReportOptions{})
	if r.Totals.APISpendUSD != nil || r.Totals.APISavingsUSD != nil || r.Totals.APIEquivalentSpendUSD != nil || r.Totals.APIEquivalentSavingsUSD != nil {
		t.Fatalf("unknown money became zero/guess: %+v", r.Totals)
	}
	if r.Totals.UnknownAuthRequests != 1 || r.Totals.UnpricedRequests != 2 || r.Totals.MeasuredRequests != 3 {
		t.Fatalf("coverage: %+v", r.Totals)
	}
	for _, receipt := range r.Receipts {
		if receipt.ID == "partial" && receipt.EstimatedInputDeltaUSD != nil {
			t.Fatal("partial usage minted savings")
		}
	}
}

func TestStatsReportKeepsRegressionsAndExcludesFailedAndLegacySavings(t *testing.T) {
	s := statsTestStore(t)
	expanded := statsTestRecord("expanded", "payg")
	expanded.RequestTokensBefore = 100
	expanded.RequestTokensAfter = 120
	negative := -.000103
	expanded.RequestEstimatedInputDeltaUSD = &negative
	s.Record(expanded)
	failed := statsTestRecord("failed", "payg")
	failed.StatusCode = 503
	s.Record(failed)
	redirected := statsTestRecord("redirected", "payg")
	redirected.StatusCode = 302
	s.Record(redirected)
	legacy := statsTestRecord("legacy", "payg")
	legacy.RequestTokenBasis = ""
	legacy.RequestMeasurementStatus = ""
	legacy.CompressionTokensBefore = 900
	legacy.CompressionTokensAfter = 100
	legacy.CompressionTokenCountBasis = "estimated_engine_o200k"
	legacy.PricingKnown = false
	s.Record(legacy)
	r := statsTestReport(t, s, StatsReportOptions{})
	if r.Totals.SavedTokens != -20 || r.Totals.MeasuredRequests != 1 || r.Totals.ExpandedRequests != 1 || r.Totals.FailedRequests != 2 {
		t.Fatalf("net measurements: %+v", r.Totals)
	}
	if r.Totals.LegacyRequests != 1 || r.Totals.LegacySavedTokens != 800 {
		t.Fatalf("legacy separate: %+v", r.Totals)
	}
	statsAssertMoney(t, r.Totals.APISavingsUSD, negative)
	for _, receipt := range r.Receipts {
		if (receipt.ID == "failed" || receipt.ID == "redirected") && (receipt.SavedTokens != nil || receipt.EstimatedInputDeltaUSD != nil) {
			t.Fatal("failed request credited")
		}
	}
}

func TestStatsReportFiltersJointGroupsAndUTCWindow(t *testing.T) {
	s := statsTestStore(t)
	for i, row := range []struct{ ts, provider, model, agent, auth string }{
		{"2026-09-07 23:59:59.999", "anthropic", "exact-model", "claude", "payg"},
		{"2026-09-08 00:00:00.000", "anthropic", "exact-model", "claude", "payg"},
		{"2026-09-08 12:00:00.000", "anthropic", "other-model", "claude", "payg"},
		{"2026-09-08 12:00:00.000", "vertex", "exact-model", "gemini", "oauth"},
		{"2026-09-08 14:00:00.000", "anthropic", "exact-model", "claude", "payg"},
	} {
		rec := statsTestRecord(string(rune('a'+i)), row.auth)
		rec.Timestamp, rec.Provider, rec.Model, rec.AgentSlug = row.ts, row.provider, row.model, row.agent
		s.Record(rec)
	}
	r := statsTestReport(t, s, StatsReportOptions{Days: 1, Provider: "anthropic", Model: "exact-model", Agent: "claude", AuthMode: "payg"})
	if r.Totals.Requests != 1 || len(r.Groups) != 1 || r.Groups[0].Day != "2026-09-08" || r.Window.From != "2026-09-08T00:00:00Z" {
		t.Fatalf("filter/window: %+v", r)
	}
	injected := statsTestReport(t, s, StatsReportOptions{Provider: "anthropic' OR 1=1 --"})
	if injected.Totals.Requests != 0 {
		t.Fatal("filter escaped parameter")
	}
	vertex := statsTestReport(t, s, StatsReportOptions{Provider: "vertex"})
	if vertex.Totals.APISpendUSD == nil || vertex.Totals.APIEquivalentSpendUSD != nil {
		t.Fatal("billed Vertex OAuth mislabeled subscription equivalent")
	}
}

func TestStatsReportEmptyAndZeroDiffer(t *testing.T) {
	s := statsTestStore(t)
	empty := statsTestReport(t, s, StatsReportOptions{Days: 30})
	if empty.Totals.APISpendUSD != nil || empty.Totals.APISavingsUSD != nil {
		t.Fatal("empty money must be unknown")
	}
	rec := statsTestRecord("zero", "payg")
	rec.InputTokens, rec.OutputTokens, rec.CachedInputTokens, rec.CacheCreationInputTokens, rec.CacheCreation1hTokens, rec.ReasoningTokens = 0, 0, 0, 0, 0, 0
	rec.RequestTokensAfter = rec.RequestTokensBefore
	zero := 0.0
	rec.RequestEstimatedInputDeltaUSD = &zero
	s.Record(rec)
	r := statsTestReport(t, s, StatsReportOptions{})
	statsAssertMoney(t, r.Totals.APISpendUSD, 0)
	statsAssertMoney(t, r.Totals.APISavingsUSD, 0)
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"saved_tokens":0`) {
		t.Fatalf("receipt signed scalar serialization missing: %s", data)
	}
}

func TestStatsReportMissingIdentityFiltersMatchDisplayedLabels(t *testing.T) {
	s := statsTestStore(t)
	for _, id := range []string{"empty", "null"} {
		rec := statsTestRecord(id, "unknown")
		rec.Provider, rec.Model, rec.AgentSlug = "", "", ""
		s.Record(rec)
	}
	if _, err := s.db.Exec(`UPDATE requests SET provider=NULL, model=NULL, agent_slug=NULL, auth_mode='' WHERE request_id='null'`); err != nil {
		t.Fatal(err)
	}
	r := statsTestReport(t, s, StatsReportOptions{Provider: "unknown", Model: "unknown", Agent: "unlabeled-agent", AuthMode: "unknown"})
	if r.Totals.Requests != 2 || len(r.Groups) != 1 || r.Groups[0].AuthMode != "unknown" {
		t.Fatalf("displayed identity did not filter its own rows: %+v", r)
	}
}

func TestStatsReportMixedHistoricalTimestampsUseUTCInstants(t *testing.T) {
	s := statsTestStore(t)
	for _, test := range []struct{ id, ts string }{
		{"old-space", "2026-09-08 10:00:00.000"},
		{"old-rfc", "2026-09-08T12:00:00.000Z"},
		{"offset-in-window", "2026-09-07T23:30:00-02:00"},
		{"offset-before-window", "2026-09-08T00:30:00+02:00"},
		{"future", "2026-09-08T13:00:01Z"},
		{"invalid", "not-a-date"},
	} {
		rec := statsTestRecord(test.id, "payg")
		rec.Timestamp = test.ts
		s.Record(rec)
	}
	r := statsTestReport(t, s, StatsReportOptions{Days: 1})
	if r.Totals.Requests != 3 || len(r.Daily) != 1 || r.Daily[0].Day != "2026-09-08" {
		t.Fatalf("mixed dates selected wrong window: %+v", r)
	}
	if r.Receipts[0].ID != "old-rfc" || r.Receipts[1].ID != "old-space" || r.Receipts[2].Timestamp != "2026-09-08T01:30:00Z" {
		t.Fatalf("date ordering or UTC normalization wrong: %+v", r.Receipts)
	}
	all := statsTestReport(t, s, StatsReportOptions{})
	if all.Totals.Requests != 4 || all.Window.From != "2026-09-07T22:30:00Z" || len(all.Daily) != 2 {
		t.Fatalf("lifetime UTC range wrong: %+v", all)
	}
}

func TestStatsPersistenceRejectsInvalidSnapshotAndTokenBuckets(t *testing.T) {
	s := statsTestStore(t)
	bad := statsTestRecord("bad-price", "payg")
	bad.PriceInputPerMillion = math.NaN()
	s.Record(bad)
	badUsage := statsTestRecord("bad-write", "payg")
	badUsage.CacheCreation1hTokens = badUsage.CacheCreationInputTokens + 1
	s.Record(badUsage)
	badBasis := statsTestRecord("bad-basis", "payg")
	badBasis.RequestTokenBasis = "made-up"
	s.Record(badBasis)
	r := statsTestReport(t, s, StatsReportOptions{})
	if r.Totals.APISavingsUSD != nil {
		t.Fatalf("invalid evidence minted savings: %+v", r.Totals)
	}
	if r.Totals.MeasuredRequests != 2 || r.Totals.UnpricedRequests != 2 {
		t.Fatalf("invalid evidence coverage: %+v", r.Totals)
	}
}

func TestStatsReceiptBoundDoesNotTruncateTotals(t *testing.T) {
	s := statsTestStore(t)
	for i := 0; i < statsReceiptLimit+2; i++ {
		rec := statsTestRecord(strings.Repeat("x", i+1), "payg")
		s.Record(rec)
	}
	r := statsTestReport(t, s, StatsReportOptions{})
	if len(r.Receipts) != statsReceiptLimit || !r.Evidence.ReceiptsTruncated || r.Totals.Requests != statsReceiptLimit+2 {
		t.Fatalf("bounded receipts changed totals: %d %+v", len(r.Receipts), r.Totals)
	}
}

func TestStatsPersistenceKeepsReceiptEquationConsistent(t *testing.T) {
	s := statsTestStore(t)
	rec := statsTestRecord("wrong-arithmetic", "payg")
	wrong := 500.0
	rec.RequestEstimatedInputDeltaUSD = &wrong
	s.Record(rec)
	r := statsTestReport(t, s, StatsReportOptions{})
	statsAssertMoney(t, r.Totals.APISavingsUSD, .002575)
	statsAssertMoney(t, r.Receipts[0].EffectiveInputRatePerMillion, 5.15)
	if r.Receipts[0].SavedTokens == nil || *r.Receipts[0].SavedTokens != 500 {
		t.Fatalf("receipt delta changed: %+v", r.Receipts[0])
	}
}

func TestStatsMissingObservedBucketRateStaysUnpriced(t *testing.T) {
	for _, test := range []struct {
		name   string
		remove func(*gateway.RequestRecord)
	}{
		{"fresh", func(r *gateway.RequestRecord) { r.PriceInputPerMillion = 0 }},
		{"output", func(r *gateway.RequestRecord) { r.PriceOutputPerMillion = 0 }},
		{"cache_read", func(r *gateway.RequestRecord) { r.PriceCacheReadPerMillion = 0 }},
		{"cache_write", func(r *gateway.RequestRecord) { r.PriceCacheWritePerMillion = 0 }},
		{"cache_write_1h", func(r *gateway.RequestRecord) { r.PriceCacheWrite1hPerMillion = 0 }},
		{"reasoning", func(r *gateway.RequestRecord) { r.PriceReasoningPerMillion = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := statsTestStore(t)
			rec := statsTestRecord("missing-rate", "payg")
			test.remove(&rec)
			s.Record(rec)
			r := statsTestReport(t, s, StatsReportOptions{})
			if r.Totals.UnpricedRequests != 1 || r.Totals.APISpendUSD != nil || r.Totals.APISavingsUSD != nil || r.Receipts[0].Price != nil {
				t.Fatalf("missing rate became priced zero: %+v", r.Totals)
			}
			if r.Totals.SavedTokens != 500 {
				t.Fatal("missing price discarded available token measurement")
			}
		})
	}
	t.Run("unused_bucket", func(t *testing.T) {
		s := statsTestStore(t)
		rec := statsTestRecord("unused-rate", "payg")
		rec.CacheCreationInputTokens, rec.CacheCreation1hTokens = 0, 0
		rec.PriceCacheWritePerMillion, rec.PriceCacheWrite1hPerMillion = 0, 0
		s.Record(rec)
		r := statsTestReport(t, s, StatsReportOptions{})
		if r.Totals.PricedRequests != 1 || r.Totals.APISpendUSD == nil || r.Totals.APISavingsUSD == nil {
			t.Fatal("unused rate prevented valid pricing")
		}
	})
}
