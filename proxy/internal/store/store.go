// Package store is the standalone proxy's local spend store: a pure-Go SQLite
// database under ~/.caveman/ that persists one row per proxied request. It
// implements gateway.TelemetrySink. Every savings value it records is labeled
// `inferred` by the lifecycle — standalone is single-tenant and self-proving and
// never claims `verified`.
package store

import (
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
	"github.com/JuliusBrussee/caveman/proxy/internal/sessionusage"
	"github.com/JuliusBrussee/caveman/proxy/providers"

	_ "modernc.org/sqlite"
)

// storeTSLayout is how request rows persist their timestamp (UTC, space-separated,
// millisecond precision). ObserveSummarySince converts an RFC3339 --since into this
// layout for a correct lexicographic `ts >=` comparison.
const storeTSLayout = "2006-01-02 15:04:05.000"

// Store is a SQLite-backed TelemetrySink.
type Store struct {
	db     *sql.DB
	logger *slog.Logger
}

const schema = `
CREATE TABLE IF NOT EXISTS requests (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts TEXT NOT NULL,
  request_id TEXT NOT NULL,
  trace_id TEXT,
  label TEXT,
  session_id TEXT,
  session_correlation_basis TEXT NOT NULL DEFAULT 'uncorrelated',
  agent_build_sha256 TEXT,
  efficiency_plan_sha256 TEXT,
  context_bill TEXT,
  transform_trace TEXT,
  transform_location TEXT,
  cache_epoch TEXT,
  cache_prefix_sha256 TEXT,
  provider_cache_prefix_sha256 TEXT,
  provider_cache_component_sha256 TEXT,
  cache_boundary_known INTEGER,
  cache_bust INTEGER,
  compression_eligible INTEGER,
  agent_slug TEXT,
  provider TEXT,
  model TEXT,
  route_from TEXT,
  route_to TEXT,
  endpoint TEXT,
  stream INTEGER,
  status_code INTEGER,
  error_code TEXT,
  latency_ms INTEGER,
  ttfb_ms INTEGER,
  request_bytes INTEGER,
  response_bytes INTEGER,
  input_tokens INTEGER,
  output_tokens INTEGER,
  cached_input_tokens INTEGER,
  cache_creation_input_tokens INTEGER,
  reasoning_tokens INTEGER,
  total_cost_usd REAL,
  savings_usd REAL,
  basis TEXT NOT NULL,
  token_usage_basis TEXT NOT NULL DEFAULT 'unavailable',
  auth_mode TEXT NOT NULL DEFAULT 'unknown',
  runtime_mode TEXT,
  optimization_ids TEXT,
  cache_status TEXT,
  raw_request_sha256 TEXT,
  transformed_request_sha256 TEXT,
  request_hash_complete INTEGER NOT NULL DEFAULT 0,
  compression_ratio REAL,
  compression_tokens_before INTEGER,
  compression_tokens_after INTEGER,
  compression_token_count_basis TEXT,
  recovery_handle TEXT,
  would_save_tokens INTEGER,
  would_save_usd REAL
);

CREATE TABLE IF NOT EXISTS usage_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  source_kind TEXT NOT NULL,
  source_path TEXT,
  event_id TEXT NOT NULL,
  ts TEXT NOT NULL,
  agent_slug TEXT,
  provider TEXT,
  model TEXT,
  requests INTEGER,
  input_tokens INTEGER,
  output_tokens INTEGER,
  cached_input_tokens INTEGER,
  cache_creation_input_tokens INTEGER,
  reasoning_tokens INTEGER,
  total_cost_usd REAL,
  basis TEXT NOT NULL,
  metadata_json TEXT,
  UNIQUE(source_kind, event_id)
);

CREATE TABLE IF NOT EXISTS quota_snapshots (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  provider TEXT NOT NULL,
  plan_type TEXT,
  window TEXT NOT NULL,
  used_pct REAL,
  resets_at TEXT,
  basis TEXT NOT NULL,
  source_kind TEXT,
  observed_at TEXT NOT NULL,
  metadata_json TEXT,
  UNIQUE(provider, window, source_kind, observed_at)
);

CREATE TABLE IF NOT EXISTS trial_runs (
  trial_id TEXT PRIMARY KEY,
  agent_slug TEXT,
  command TEXT,
  started_at TEXT NOT NULL,
  ended_at TEXT,
  exit_code INTEGER,
  source_counts_json TEXT
);

CREATE TABLE IF NOT EXISTS trial_payloads (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  trial_id TEXT NOT NULL,
  request_id TEXT NOT NULL,
  trace_id TEXT,
  ts TEXT NOT NULL,
  request_bytes INTEGER,
  raw_request BLOB NOT NULL,
  UNIQUE(trial_id, request_id)
);

CREATE TABLE IF NOT EXISTS trial_results (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  trial_id TEXT NOT NULL,
  optimizer_id TEXT NOT NULL,
  title TEXT NOT NULL,
  safety_class TEXT NOT NULL,
  status TEXT NOT NULL,
  savings_usd_base REAL,
  confidence TEXT NOT NULL,
  evidence_json TEXT,
  UNIQUE(trial_id, optimizer_id)
);

CREATE TABLE IF NOT EXISTS learnings (
  id TEXT PRIMARY KEY,
  text TEXT NOT NULL,
  source_kind TEXT NOT NULL,
  confidence TEXT NOT NULL,
  stored_in_cavemem INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(text, source_kind)
);

CREATE TABLE IF NOT EXISTS config_snapshots (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  scope TEXT NOT NULL,
  path TEXT NOT NULL,
  kind TEXT NOT NULL,
  lines INTEGER,
  tokens INTEGER,
  observed_at TEXT NOT NULL,
  metadata_json TEXT,
  UNIQUE(scope, path, kind)
);

CREATE TABLE IF NOT EXISTS prefix_replacements (
  original_sha256 TEXT PRIMARY KEY,
  handle TEXT NOT NULL,
  replacement BLOB NOT NULL,
  created_at TEXT NOT NULL,
  last_used_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS learn_sinks (
  sink_id TEXT PRIMARY KEY,
  title TEXT NOT NULL,
  class TEXT NOT NULL,
  basis TEXT NOT NULL,
  tokens_per_turn INTEGER,
  tokens_per_day_rate INTEGER,
  framing TEXT,
  evidence_json TEXT,
  suggestion TEXT,
  computed_at TEXT NOT NULL
);`

// migrations are additive ALTERs for stores created before a column existed.
// CREATE TABLE IF NOT EXISTS never adds a column to an existing table, so each new
// column is added here and a "duplicate column name" error (already migrated) is
// ignored. New, empty databases get the columns from the schema above and skip these.
var migrations = []string{
	`ALTER TABLE requests ADD COLUMN compression_ratio REAL`,
	`ALTER TABLE requests ADD COLUMN compression_tokens_before INTEGER`,
	`ALTER TABLE requests ADD COLUMN compression_tokens_after INTEGER`,
	`ALTER TABLE requests ADD COLUMN recovery_handle TEXT`,
	`ALTER TABLE requests ADD COLUMN route_from TEXT`,
	`ALTER TABLE requests ADD COLUMN route_to TEXT`,
	`ALTER TABLE requests ADD COLUMN agent_slug TEXT`,
	`ALTER TABLE requests ADD COLUMN token_usage_basis TEXT NOT NULL DEFAULT 'unavailable'`,
	`ALTER TABLE requests ADD COLUMN auth_mode TEXT NOT NULL DEFAULT 'unknown'`,
	`ALTER TABLE requests ADD COLUMN cache_creation_input_tokens INTEGER`,
	`ALTER TABLE requests ADD COLUMN compression_token_count_basis TEXT`,
	`ALTER TABLE requests ADD COLUMN would_save_tokens INTEGER`,
	`ALTER TABLE requests ADD COLUMN would_save_usd REAL`,
	`ALTER TABLE requests ADD COLUMN session_id TEXT`,
	`ALTER TABLE requests ADD COLUMN session_correlation_basis TEXT NOT NULL DEFAULT 'uncorrelated'`,
	`ALTER TABLE requests ADD COLUMN agent_build_sha256 TEXT`,
	`ALTER TABLE requests ADD COLUMN efficiency_plan_sha256 TEXT`,
	`ALTER TABLE requests ADD COLUMN context_bill TEXT`,
	`ALTER TABLE requests ADD COLUMN transform_trace TEXT`,
	`ALTER TABLE requests ADD COLUMN transform_location TEXT`,
	`ALTER TABLE requests ADD COLUMN cache_epoch TEXT`,
	`ALTER TABLE requests ADD COLUMN cache_prefix_sha256 TEXT`,
	`ALTER TABLE requests ADD COLUMN provider_cache_prefix_sha256 TEXT`,
	`ALTER TABLE requests ADD COLUMN provider_cache_component_sha256 TEXT`,
	`ALTER TABLE requests ADD COLUMN cache_boundary_known INTEGER`,
	`ALTER TABLE requests ADD COLUMN cache_bust INTEGER`,
	`ALTER TABLE requests ADD COLUMN compression_eligible INTEGER`,
	`ALTER TABLE requests ADD COLUMN request_hash_complete INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE usage_events ADD COLUMN cache_creation_input_tokens INTEGER`,
}

// sqliteDSN builds the driver DSN for path. The two pragmas are load-bearing,
// not tuning: this store is also the prefix-replacement cache the compress path
// reads on every turn (cmd/caveman-proxy wires it as gateway.PrefixCache) and
// writes to on every request, so lookups and writes always contend. Without
// journal_mode(WAL) a reader is locked out while a write is in flight, and
// without busy_timeout a contender returns SQLITE_BUSY instead of waiting — a
// failed lookup reads as a cache miss, and the gateway then forwards the
// client's ORIGINAL bytes for a frozen block it already compressed, flipping the
// upstream prefix mid-conversation.
func sqliteDSN(path string) string {
	const pragmas = "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	if path == ":memory:" {
		return "file::memory:?" + pragmas
	}
	// A file: URI so a path containing '?' or '#' cannot be truncated into a
	// different database file by the driver's DSN split.
	u := url.URL{Scheme: "file", OmitHost: true, Path: path}
	return u.String() + "?" + pragmas
}

// Open opens (creating if needed) the SQLite database at path and migrates the
// schema. logger may be nil.
func Open(path string, logger *slog.Logger) (*Store, error) {
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate sqlite %q: %w", path, err)
	}
	if path != ":memory:" {
		_ = os.Chmod(path, 0o600)
	}
	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			_ = db.Close()
			return nil, fmt.Errorf("migrate sqlite %q: %w", path, err)
		}
	}
	return &Store{db: db, logger: logger}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Record persists one request row. It is best-effort: a write failure is logged
// but never propagated, because telemetry must never break the operator's
// traffic (the byte-safe ethos). Basis is recorded as the lifecycle set it —
// always "inferred" in standalone.
func (s *Store) Record(rec gateway.RequestRecord) {
	// Standalone is never allowed to mint Cloud verification, even if a buggy
	// embedder passes a forged Basis. Enforce provenance again at persistence
	// boundary instead of trusting lifecycle callers.
	rec.Basis = "inferred"
	if !validEvidenceToken(rec.SessionID, 256) {
		rec.SessionID = ""
	}
	if rec.SessionID == "" {
		rec.SessionCorrelationBasis = "uncorrelated"
	} else if rec.SessionCorrelationBasis == "" {
		rec.SessionCorrelationBasis = "explicit_header"
	}
	if rec.SessionCorrelationBasis != "uncorrelated" && rec.SessionCorrelationBasis != "explicit_header" && rec.SessionCorrelationBasis != "signed_marker" && rec.SessionCorrelationBasis != "unique_recent_session" && rec.SessionCorrelationBasis != "unique_recent_time_model" {
		rec.SessionID = ""
		rec.SessionCorrelationBasis = "uncorrelated"
	}
	if !validDigest(rec.AgentBuildSHA256) || !validDigest(rec.EfficiencyPlanSHA256) {
		rec.AgentBuildSHA256, rec.EfficiencyPlanSHA256 = "", ""
	}
	if !validEvidenceValue(rec.ContextBill, 2_048) {
		rec.ContextBill = ""
	}
	if !validEvidenceValue(rec.TransformTrace, 8_192) {
		rec.TransformTrace = ""
	}
	if rec.TransformLocation != "local" && rec.TransformLocation != "gateway" {
		rec.TransformLocation = ""
	}
	if !validEvidenceToken(rec.CacheEpoch, 512) {
		rec.CacheEpoch = ""
	}
	if !validDigest(rec.CachePrefixSHA256) {
		rec.CachePrefixSHA256 = ""
	}
	if !validDigest(rec.ProviderCachePrefixSHA256) {
		rec.ProviderCachePrefixSHA256 = ""
		rec.ProviderCacheComponentSHA256 = ""
		rec.CacheBoundaryKnown = false
	}
	if !validDigestList(rec.ProviderCacheComponentSHA256, 1_024) {
		rec.ProviderCachePrefixSHA256 = ""
		rec.ProviderCacheComponentSHA256 = ""
		rec.CacheBoundaryKnown = false
	}
	invalidTokens := rec.InputTokens < 0 || rec.OutputTokens < 0 || rec.CachedInputTokens < 0 ||
		rec.CacheCreationInputTokens < 0 || rec.ReasoningTokens < 0 ||
		rec.CachedInputTokens > rec.InputTokens ||
		rec.CacheCreationInputTokens > rec.InputTokens-rec.CachedInputTokens ||
		rec.ReasoningTokens > rec.OutputTokens
	switch rec.TokenUsageBasis {
	case "provider_complete", "provider_partial", "provider_malformed", "unavailable":
	default:
		rec.TokenUsageBasis = "unavailable"
	}
	switch rec.AuthMode {
	case "payg", "oauth", "subscription":
	default:
		rec.AuthMode = "unknown"
	}
	if invalidTokens {
		rec.TokenUsageBasis = "provider_malformed"
		rec.InputTokens, rec.OutputTokens = 0, 0
		rec.CachedInputTokens, rec.CacheCreationInputTokens, rec.ReasoningTokens = 0, 0, 0
		rec.TotalCostUSD, rec.SavingsUSD = 0, 0
	}
	if !providers.ListPriceEligible(rec.Provider, rec.AuthMode) || rec.TokenUsageBasis != "provider_complete" {
		rec.TotalCostUSD, rec.SavingsUSD = 0, 0
		// An unpriced/incomplete row never carries a would-have-saved dollar figure
		// (would_save_tokens, a pure count, may remain). Mirrors the cost re-zeroing:
		// no guessed price ever reaches the store.
		rec.WouldSaveUSD = nil
	}
	if rec.StatusCode >= 400 {
		// A failed upstream never "would have saved" anything for this call — zero both
		// the token estimate and its dollar figure so a broken request never inflates
		// the would-have-saved totals (mirrors the savings_usd zeroing).
		rec.SavingsUSD = 0
		rec.WouldSaveTokens = 0
		rec.WouldSaveUSD = nil
	}
	// would_save_tokens is an observe-only measurement, never a booked saving; it
	// lives in its own column and can never leak into savings_usd. Clamp bad values.
	if rec.WouldSaveTokens < 0 {
		rec.WouldSaveTokens = 0
	}
	if rec.WouldSaveTokens == 0 {
		rec.WouldSaveUSD = nil
	}
	if rec.WouldSaveUSD != nil && (math.IsNaN(*rec.WouldSaveUSD) || math.IsInf(*rec.WouldSaveUSD, 0) || *rec.WouldSaveUSD < 0) {
		rec.WouldSaveUSD = nil
	}
	if rec.CompressionTokensBefore < 0 || rec.CompressionTokensAfter < 0 || rec.CompressionTokensAfter > rec.CompressionTokensBefore {
		rec.CompressionRatio = 0
		rec.CompressionTokensBefore, rec.CompressionTokensAfter = 0, 0
		rec.CompressionTokenCountBasis = ""
	}
	if rec.CompressionTokensBefore == 0 && rec.CompressionTokensAfter == 0 {
		rec.CompressionTokenCountBasis = ""
	} else if rec.CompressionTokenCountBasis != "estimated_engine_o200k" {
		rec.CompressionTokenCountBasis = "legacy_unspecified"
	}
	rec.InputTokens = max(rec.InputTokens, 0)
	rec.OutputTokens = max(rec.OutputTokens, 0)
	rec.CachedInputTokens = max(rec.CachedInputTokens, 0)
	rec.CacheCreationInputTokens = max(rec.CacheCreationInputTokens, 0)
	rec.ReasoningTokens = max(rec.ReasoningTokens, 0)
	rec.CompressionTokensBefore = max(rec.CompressionTokensBefore, 0)
	rec.CompressionTokensAfter = max(rec.CompressionTokensAfter, 0)
	if rec.TotalCostUSD < 0 || math.IsNaN(rec.TotalCostUSD) || math.IsInf(rec.TotalCostUSD, 0) {
		rec.TotalCostUSD = 0
	}
	if math.IsNaN(rec.SavingsUSD) || math.IsInf(rec.SavingsUSD, 0) {
		rec.SavingsUSD = 0
	}
	if rec.CompressionRatio < 0 || rec.CompressionRatio > 1 || math.IsNaN(rec.CompressionRatio) || math.IsInf(rec.CompressionRatio, 0) {
		rec.CompressionRatio = 0
	}
	if !rec.RequestHashComplete {
		rec.RawRequestSHA256 = ""
		rec.TransformedRequestSHA256 = ""
	}
	_, err := s.db.Exec(
		`INSERT INTO requests (
		    ts, request_id, trace_id, label, session_id, session_correlation_basis, agent_build_sha256, efficiency_plan_sha256,
		    context_bill, transform_trace, transform_location, cache_epoch, cache_prefix_sha256,
		    provider_cache_prefix_sha256, provider_cache_component_sha256, cache_boundary_known, cache_bust, compression_eligible,
		    agent_slug, provider, model, route_from, route_to, endpoint, stream,
            status_code, error_code, latency_ms, ttfb_ms, request_bytes, response_bytes,
            input_tokens, output_tokens, cached_input_tokens, cache_creation_input_tokens,
		    reasoning_tokens, total_cost_usd, savings_usd, basis, token_usage_basis, auth_mode, runtime_mode,
            optimization_ids, cache_status, raw_request_sha256, transformed_request_sha256, request_hash_complete,
		    compression_ratio, compression_tokens_before, compression_tokens_after, compression_token_count_basis, recovery_handle,
		    would_save_tokens, would_save_usd
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		rec.Timestamp, rec.RequestID, rec.TraceID, rec.Label, rec.SessionID, rec.SessionCorrelationBasis, rec.AgentBuildSHA256, rec.EfficiencyPlanSHA256,
		rec.ContextBill, rec.TransformTrace, rec.TransformLocation, rec.CacheEpoch, rec.CachePrefixSHA256,
		rec.ProviderCachePrefixSHA256, rec.ProviderCacheComponentSHA256, rec.CacheBoundaryKnown, rec.CacheBust, rec.CompressionEligible,
		rec.AgentSlug, rec.Provider, rec.Model, rec.RouteFrom, rec.RouteTo, rec.Endpoint, rec.Stream,
		rec.StatusCode, rec.ErrorCode, rec.LatencyMS, rec.TTFBMS, rec.RequestBytes, rec.ResponseBytes,
		rec.InputTokens, rec.OutputTokens, rec.CachedInputTokens, rec.CacheCreationInputTokens,
		rec.ReasoningTokens, rec.TotalCostUSD, rec.SavingsUSD, rec.Basis, rec.TokenUsageBasis, rec.AuthMode, rec.RuntimeMode,
		strings.Join(rec.OptimizationIDs, ","), rec.CacheStatus, rec.RawRequestSHA256, rec.TransformedRequestSHA256, rec.RequestHashComplete,
		rec.CompressionRatio, rec.CompressionTokensBefore, rec.CompressionTokensAfter, rec.CompressionTokenCountBasis, rec.RecoveryHandle,
		rec.WouldSaveTokens, rec.WouldSaveUSD,
	)
	if err != nil && s.logger != nil {
		s.logger.Warn("local spend store insert failed", "error", err, "request_id", rec.RequestID)
	}
}

// Stats is an aggregate view of the local spend store for `caveman stats`.
type Stats struct {
	Requests                   int64            `json:"requests"`
	TotalCost                  float64          `json:"total_cost_usd"`
	TotalSaved                 float64          `json:"savings_usd"`
	TokenAccounting            map[string]int64 `json:"token_accounting"`
	AuthModeAccounting         map[string]int64 `json:"auth_mode_accounting"`
	CompressionTokensBefore    int64            `json:"compression_tokens_before"`
	CompressionTokensAfter     int64            `json:"compression_tokens_after"`
	CompressionTokensSaved     int64            `json:"compression_tokens_saved"`
	CompressionTokenCountBasis string           `json:"compression_token_count_basis"`
	// CacheBustRequests counts requests whose frozen prefix did not extend the prior
	// request in their session (observe-only). RequestsEligibleForCompression counts
	// requests that reached the compression path as candidates (issue #133).
	CacheBustRequests              int64 `json:"cache_bust_requests"`
	RequestsEligibleForCompression int64 `json:"requests_eligible_for_compression"`
	// WouldSaveTokens is the observe-only would-have-saved token total across all
	// rows; WouldSaveUSD is its inferred dollar total (nil when no row was
	// list-price eligible — never a guessed price). Neither is ever a booked saving.
	WouldSaveTokens int64    `json:"would_save_tokens"`
	WouldSaveUSD    *float64 `json:"would_save_usd"`
	// Basis is the provenance of the savings figure. Standalone always reports
	// "inferred" — the headline is a self-measured estimate, never a verified one.
	Basis string `json:"basis"`
}

// ObserveSummary is the compact object `stats --json [--since <RFC3339>]` emits for
// the CLI's session-end line. It carries both the observe-only would-have-saved
// numbers (populated in record+observe sessions) and the real compression cut +
// savings (populated in compress sessions), so one query drives both the "would
// have cut" funnel line and the "cut" line. Everything is `inferred`.
type ObserveSummary struct {
	Spans           int64            `json:"spans"`
	TokensIn        int64            `json:"tokens_in"`
	WouldSaveTokens int64            `json:"would_save_tokens"`
	WouldSavePct    float64          `json:"would_save_pct"`
	TokenAccounting map[string]int64 `json:"token_accounting"`
	MemBlocks       *int             `json:"mem_blocks,omitempty"`
	// CompressionTokensBefore/After are the compressed-parts totals behind
	// CompressionTokensSaved (which is their difference). They are reported so the
	// session line can show the actual before → after pair instead of the delta
	// alone; both are 0 in a session that never compressed anything.
	CompressionTokensBefore int64    `json:"compression_tokens_before"`
	CompressionTokensAfter  int64    `json:"compression_tokens_after"`
	CompressionTokensSaved  int64    `json:"compression_tokens_saved"`
	SavingsUSD              float64  `json:"savings_usd"`
	WouldSaveUSD            *float64 `json:"would_save_usd"`
	// CachedInputTokens and CacheCreationInputTokens are the provider-reported cache
	// read/write totals across the window. They sit in the same rows as the
	// compression cut but were never surfaced before — so a session that wrote 144k
	// cache tokens could still print "compression cut ~60 tokens" as a headline win.
	// A consumer (and HeadlineCompressionRefused) uses them to refuse that headline.
	CachedInputTokens        int64 `json:"cached_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	// HeadlineCompressionRefused is true when cache_creation_input_tokens exceeds
	// cached_input_tokens: the window was dominated by cache WRITES, so a small
	// compression cut is not an honest headline saving and must be refused.
	HeadlineCompressionRefused bool `json:"headline_compression_refused"`
	// CacheBustRequests counts requests in the window whose frozen prefix did not
	// extend the prior request in their session (observe-only; see prefix_monitor.go).
	CacheBustRequests int64 `json:"cache_bust_requests"`
	// RequestsEligibleForCompression counts requests that reached the compression
	// path as candidates, regardless of whether any bytes were saved (issue #133).
	RequestsEligibleForCompression int64  `json:"requests_eligible_for_compression"`
	Basis                          string `json:"basis"`
}

// RecentRequest is the pipe-safe row shape emitted by `caveman-proxy stats
// --recent N`. It intentionally exposes only telemetry metadata, not request or
// response bytes.
type RecentRequest struct {
	TS              string `json:"ts"`
	AgentSlug       string `json:"agent_slug"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	Endpoint        string `json:"endpoint"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	Basis           string `json:"basis"`
	TokenUsageBasis string `json:"token_usage_basis"`
	AuthMode        string `json:"auth_mode"`
}

// AgentEvidence is one content-blind provider request joined to a strict local
// Cave Build. It exposes hashes and usage only; request/response bodies never
// enter this surface. Basis remains inferred.
type AgentEvidence struct {
	TS                           string `json:"ts"`
	RequestID                    string `json:"request_id"`
	SessionID                    string `json:"session_id"`
	AgentBuildSHA256             string `json:"agent_build_sha256"`
	EfficiencyPlanSHA256         string `json:"efficiency_plan_sha256"`
	Provider                     string `json:"provider"`
	Model                        string `json:"model"`
	StatusCode                   int64  `json:"status_code"`
	InputTokens                  int64  `json:"input_tokens"`
	OutputTokens                 int64  `json:"output_tokens"`
	CachedInputTokens            int64  `json:"cached_input_tokens"`
	CacheCreationInputTokens     int64  `json:"cache_creation_input_tokens"`
	ReasoningTokens              int64  `json:"reasoning_tokens"`
	TokenUsageBasis              string `json:"token_usage_basis"`
	RawRequestSHA256             string `json:"raw_request_sha256"`
	TransformedRequestSHA256     string `json:"transformed_request_sha256"`
	RequestHashComplete          bool   `json:"request_hash_complete"`
	OptimizationIDs              string `json:"optimization_ids"`
	ContextBill                  string `json:"context_bill"`
	TransformTrace               string `json:"transform_trace"`
	TransformLocation            string `json:"transform_location"`
	CacheEpoch                   string `json:"cache_epoch"`
	CachePrefixSHA256            string `json:"declared_cache_prefix_sha256"`
	ProviderCachePrefixSHA256    string `json:"provider_cache_prefix_sha256"`
	ProviderCacheComponentSHA256 string `json:"provider_cache_component_sha256"`
	CacheBoundaryKnown           bool   `json:"cache_boundary_known"`
	RecoveryHandle               string `json:"recovery_handle"`
	CompressionTokensBefore      int64  `json:"compression_tokens_before"`
	CompressionTokensAfter       int64  `json:"compression_tokens_after"`
	Basis                        string `json:"basis"`
}

// AgentEvidenceForBuild returns all requests for one exact session/build/plan
// tuple in provider-call order. Partial or malformed identity fails closed.
func (s *Store) AgentEvidenceForBuild(sessionID, buildSHA256, planSHA256 string) ([]AgentEvidence, error) {
	if !validEvidenceToken(sessionID, 256) || !validDigest(buildSHA256) || !validDigest(planSHA256) {
		return nil, fmt.Errorf("invalid agent evidence identity")
	}
	rows, err := s.db.Query(
		`SELECT ts, request_id, session_id, agent_build_sha256, efficiency_plan_sha256,
		        COALESCE(provider,''), COALESCE(model,''), COALESCE(status_code,0),
		        COALESCE(input_tokens,0), COALESCE(output_tokens,0), COALESCE(cached_input_tokens,0),
		        COALESCE(cache_creation_input_tokens,0), COALESCE(reasoning_tokens,0),
		        COALESCE(token_usage_basis,'unavailable'), COALESCE(raw_request_sha256,''),
		        COALESCE(transformed_request_sha256,''), COALESCE(request_hash_complete,0), COALESCE(optimization_ids,''),
		        COALESCE(context_bill,''), COALESCE(transform_trace,''), COALESCE(transform_location,''),
		        COALESCE(cache_epoch,''), COALESCE(cache_prefix_sha256,''),
		        COALESCE(provider_cache_prefix_sha256,''), COALESCE(provider_cache_component_sha256,''),
		        COALESCE(cache_boundary_known,0), COALESCE(recovery_handle,''),
		        COALESCE(compression_tokens_before,0), COALESCE(compression_tokens_after,0), basis
		   FROM requests
		  WHERE session_id = ? AND agent_build_sha256 = ? AND efficiency_plan_sha256 = ?
		  ORDER BY id ASC
		  LIMIT 500`,
		sessionID, buildSHA256, planSHA256,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentEvidence{}
	for rows.Next() {
		var item AgentEvidence
		if err := rows.Scan(
			&item.TS, &item.RequestID, &item.SessionID, &item.AgentBuildSHA256,
			&item.EfficiencyPlanSHA256, &item.Provider, &item.Model, &item.StatusCode,
			&item.InputTokens, &item.OutputTokens, &item.CachedInputTokens,
			&item.CacheCreationInputTokens, &item.ReasoningTokens, &item.TokenUsageBasis,
			&item.RawRequestSHA256, &item.TransformedRequestSHA256, &item.RequestHashComplete, &item.OptimizationIDs,
			&item.ContextBill, &item.TransformTrace, &item.TransformLocation, &item.CacheEpoch,
			&item.CachePrefixSHA256, &item.ProviderCachePrefixSHA256,
			&item.ProviderCacheComponentSHA256, &item.CacheBoundaryKnown,
			&item.RecoveryHandle, &item.CompressionTokensBefore,
			&item.CompressionTokensAfter, &item.Basis,
		); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// SessionUsage returns content-blind provider telemetry for one exact correlated
// native session. Dollar values remain catalog list-price subtotals/inferred
// standalone savings; neither is a provider invoice or verified savings.
func (s *Store) SessionUsage(sessionID string) (sessionusage.Snapshot, error) {
	out := sessionusage.Snapshot{
		Status:       "not_observed",
		SessionID:    sessionID,
		CostBasis:    "catalog_list_price_subtotal_provider_complete_priced_rows",
		SavingsBasis: "inferred_standalone_not_verified",
	}
	if !validEvidenceToken(sessionID, 256) {
		return out, fmt.Errorf("invalid native session identity")
	}
	var compressionBases, correlationBases string
	err := s.db.QueryRow(
		`SELECT COUNT(*),
		        COALESCE(SUM(CASE WHEN token_usage_basis = 'provider_complete' THEN 1 ELSE 0 END),0),
		        COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		        COALESCE(SUM(cached_input_tokens),0), COALESCE(SUM(cache_creation_input_tokens),0),
		        COALESCE(SUM(reasoning_tokens),0), COALESCE(SUM(total_cost_usd),0),
		        COALESCE(SUM(savings_usd),0), COALESCE(SUM(compression_tokens_before),0),
		        COALESCE(SUM(compression_tokens_after),0),
		        COALESCE(GROUP_CONCAT(DISTINCT NULLIF(compression_token_count_basis,'')),''),
		        COALESCE(GROUP_CONCAT(DISTINCT NULLIF(session_correlation_basis,'')),'')
		   FROM requests WHERE session_id = ?`,
		sessionID,
	).Scan(
		&out.Requests, &out.ProviderCompleteRequests, &out.InputTokens, &out.OutputTokens,
		&out.CachedInputTokens, &out.CacheCreationInputTokens, &out.ReasoningTokens,
		&out.CatalogListPriceSubtotalUSD, &out.InferredSavingsUSD,
		&out.CompressionTokensBefore, &out.CompressionTokensAfter, &compressionBases, &correlationBases,
	)
	if err != nil {
		return out, fmt.Errorf("native session usage: %w", err)
	}
	if out.Requests == 0 {
		return out, nil
	}
	out.Status = "correlated"
	if !strings.Contains(correlationBases, ",") {
		out.CorrelationBasis = correlationBases
	} else {
		out.CorrelationBasis = "mixed"
	}
	if out.ProviderCompleteRequests == out.Requests {
		out.TokenUsageCoverage = "provider_complete"
	} else {
		out.TokenUsageCoverage = "mixed_or_unavailable"
	}
	if compressionBases == "estimated_engine_o200k" || compressionBases == "legacy_unspecified" {
		out.CompressionTokenCountBasis = compressionBases
	} else if compressionBases != "" {
		out.CompressionTokenCountBasis = "mixed"
	}
	out.CatalogListPriceSubtotalUSD = roundUSD(out.CatalogListPriceSubtotalUSD)
	out.InferredSavingsUSD = roundUSD(out.InferredSavingsUSD)
	return out, nil
}

// Summary returns aggregate spend across all recorded requests. The savings
// basis is "inferred" whenever any row carries a non-`verified` basis, which in
// standalone is always — the value is never re-projected to a monthly figure.
func (s *Store) Summary() (Stats, error) {
	out := Stats{TokenAccounting: map[string]int64{}, AuthModeAccounting: map[string]int64{}}
	var wouldSaveUSDCount int64
	var wouldSaveUSDSum float64
	row := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(total_cost_usd),0), COALESCE(SUM(savings_usd),0),
		COALESCE(SUM(compression_tokens_before),0), COALESCE(SUM(compression_tokens_after),0),
		COALESCE(SUM(would_save_tokens),0), COUNT(would_save_usd), COALESCE(SUM(would_save_usd),0),
		COALESCE(SUM(CASE WHEN cache_bust <> 0 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN compression_eligible <> 0 THEN 1 ELSE 0 END),0)
		FROM requests`)
	if err := row.Scan(&out.Requests, &out.TotalCost, &out.TotalSaved, &out.CompressionTokensBefore, &out.CompressionTokensAfter,
		&out.WouldSaveTokens, &wouldSaveUSDCount, &wouldSaveUSDSum,
		&out.CacheBustRequests, &out.RequestsEligibleForCompression); err != nil {
		return out, err
	}
	out.CompressionTokensSaved = out.CompressionTokensBefore - out.CompressionTokensAfter
	if out.CompressionTokensSaved < 0 {
		out.CompressionTokensSaved = 0
	}
	if out.WouldSaveTokens < 0 {
		out.WouldSaveTokens = 0
	}
	// would_save_usd is nil unless at least one row was list-price eligible — never
	// a fabricated zero standing in for "no price".
	if wouldSaveUSDCount > 0 {
		v := roundUSDCents(wouldSaveUSDSum)
		out.WouldSaveUSD = &v
	}
	if err := s.countRequestProvenance("token_usage_basis", out.TokenAccounting); err != nil {
		return out, err
	}
	if err := s.countRequestProvenance("auth_mode", out.AuthModeAccounting); err != nil {
		return out, err
	}
	var basisCount int
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT compression_token_count_basis)
		FROM requests WHERE COALESCE(compression_token_count_basis,'') <> ''`).Scan(&basisCount); err != nil {
		return out, err
	}
	switch {
	case basisCount == 0:
		out.CompressionTokenCountBasis = "unavailable"
	case basisCount == 1:
		_ = s.db.QueryRow(`SELECT compression_token_count_basis FROM requests
			WHERE COALESCE(compression_token_count_basis,'') <> '' LIMIT 1`).Scan(&out.CompressionTokenCountBasis)
	default:
		out.CompressionTokenCountBasis = "mixed"
	}
	out.Basis = "inferred"
	return out, nil
}

// ObserveSummarySince returns the compact observe-estimate object, optionally
// filtered to rows at or after an RFC3339 instant (the wrap session start). It
// mixes the observe-only would-have-saved numbers with the real compression cut so
// one call serves both the "would have cut" (observe) and "cut" (compress) session
// lines. Everything it reports is `inferred`; savings_usd is whatever was truly
// booked (0 in observe mode), and would_save_usd stays nil unless a priced row
// produced one.
func (s *Store) ObserveSummarySince(since string) (ObserveSummary, error) {
	out := ObserveSummary{Basis: "inferred", TokenAccounting: map[string]int64{}}
	where := ""
	var args []any
	if trimmed := strings.TrimSpace(since); trimmed != "" {
		t, err := time.Parse(time.RFC3339, trimmed)
		if err != nil {
			return out, fmt.Errorf("invalid --since %q (want RFC3339): %w", trimmed, err)
		}
		where = " WHERE ts >= ?"
		args = append(args, t.UTC().Format(storeTSLayout))
	}
	var usdCount int64
	var usdSum float64
	row := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(would_save_tokens),0),
		COALESCE(SUM(compression_tokens_before),0), COALESCE(SUM(compression_tokens_after),0),
		COALESCE(SUM(compression_tokens_before - compression_tokens_after),0),
		COALESCE(SUM(savings_usd),0), COUNT(would_save_usd), COALESCE(SUM(would_save_usd),0),
		COALESCE(SUM(cached_input_tokens),0), COALESCE(SUM(cache_creation_input_tokens),0),
		COALESCE(SUM(CASE WHEN cache_bust <> 0 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN compression_eligible <> 0 THEN 1 ELSE 0 END),0)
		FROM requests`+where, args...)
	if err := row.Scan(&out.Spans, &out.TokensIn, &out.WouldSaveTokens,
		&out.CompressionTokensBefore, &out.CompressionTokensAfter, &out.CompressionTokensSaved,
		&out.SavingsUSD, &usdCount, &usdSum,
		&out.CachedInputTokens, &out.CacheCreationInputTokens,
		&out.CacheBustRequests, &out.RequestsEligibleForCompression); err != nil {
		return out, err
	}
	if out.CachedInputTokens < 0 {
		out.CachedInputTokens = 0
	}
	if out.CacheCreationInputTokens < 0 {
		out.CacheCreationInputTokens = 0
	}
	// Refuse a headline compression saving when the window was dominated by cache
	// WRITES: a small compression cut against 144k cache-creation tokens is not a win.
	out.HeadlineCompressionRefused = out.CacheCreationInputTokens > out.CachedInputTokens
	if out.CompressionTokensBefore < 0 {
		out.CompressionTokensBefore = 0
	}
	if out.CompressionTokensAfter < 0 {
		out.CompressionTokensAfter = 0
	}
	if out.WouldSaveTokens < 0 {
		out.WouldSaveTokens = 0
	}
	if out.CompressionTokensSaved < 0 {
		out.CompressionTokensSaved = 0
	}
	// The funnel percentage is "of those sent" (§8.1): tokens compression would have
	// cut over tokens sent, clamped to [0,1]. Different tokenizers make this an
	// estimate — hence inferred.
	if out.TokensIn > 0 && out.WouldSaveTokens > 0 {
		pct := float64(out.WouldSaveTokens) / float64(out.TokensIn)
		if pct > 1 {
			pct = 1
		}
		out.WouldSavePct = pct
	}
	out.SavingsUSD = roundUSDCents(out.SavingsUSD)
	if usdCount > 0 {
		v := roundUSDCents(usdSum)
		out.WouldSaveUSD = &v
	}
	if err := s.countRequestProvenanceWhere("token_usage_basis", out.TokenAccounting, where, args...); err != nil {
		return out, err
	}
	return out, nil
}

// roundUSDCents rounds a dollar figure to cents. Local sums of already-cent-rounded
// rows can carry float noise; this keeps the reported figure honest to the penny.
func roundUSDCents(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*100) / 100
}

func (s *Store) countRequestProvenance(column string, out map[string]int64) error {
	return s.countRequestProvenanceWhere(column, out, "")
}

func (s *Store) countRequestProvenanceWhere(column string, out map[string]int64, where string, args ...any) error {
	if column != "token_usage_basis" && column != "auth_mode" {
		return fmt.Errorf("unsupported provenance column %q", column)
	}
	rows, err := s.db.Query(`SELECT COALESCE(`+column+`,'unavailable'), COUNT(*) FROM requests`+where+` GROUP BY 1`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var count int64
		if err := rows.Scan(&key, &count); err != nil {
			return err
		}
		out[key] = count
	}
	return rows.Err()
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validDigestList(value string, limit int) bool {
	parts := strings.Split(value, ",")
	if len(parts) == 0 || len(parts) > limit {
		return false
	}
	for _, part := range parts {
		if !validDigest(part) {
			return false
		}
	}
	return true
}

func validEvidenceToken(value string, limit int) bool {
	if value == "" || len(value) > limit {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && !strings.ContainsRune("._:-", char) {
			return false
		}
	}
	return true
}

func validEvidenceValue(value string, limit int) bool {
	if value == "" {
		return true
	}
	if len(value) > limit {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char > 0x7e {
			return false
		}
	}
	return true
}

// RecentRequests returns the N newest request rows. Empty stores return an empty
// array; no synthetic row is ever created to make a first-request check pass.
func (s *Store) RecentRequests(limit int) ([]RecentRequest, error) {
	if limit <= 0 {
		return []RecentRequest{}, nil
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.db.Query(
		`SELECT ts, COALESCE(agent_slug, ''), COALESCE(provider, ''), COALESCE(model, ''),
		        COALESCE(endpoint, ''), COALESCE(input_tokens, 0), COALESCE(output_tokens, 0), basis,
		        COALESCE(token_usage_basis, 'unavailable'), COALESCE(auth_mode, 'unknown')
		   FROM requests
		  ORDER BY ts DESC, id DESC
		  LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []RecentRequest{}
	for rows.Next() {
		var rec RecentRequest
		if err := rows.Scan(&rec.TS, &rec.AgentSlug, &rec.Provider, &rec.Model, &rec.Endpoint, &rec.InputTokens, &rec.OutputTokens, &rec.Basis, &rec.TokenUsageBasis, &rec.AuthMode); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
