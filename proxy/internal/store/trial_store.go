package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
	"github.com/JuliusBrussee/caveman/shared/platform/env"
)

const (
	trialSchema     = "caveman.trial.v1"
	trialBasis      = "inferred"
	observedLocal   = "observed_local"
	sourceCaveProxy = "caveman_proxy"
	statusNeedsEval = "needs_eval"
	statusSafeNow   = "safe_now"
	statusNoData    = "insufficient_data"
	statusDoNot     = "do_not_enable"
	classS1         = "S1_PROVIDER_NATIVE"
	classS2         = "S2_STRUCTURAL"
	classS3         = "S3_BEHAVIORAL"

	compressionReplayCaveat    = "Compression replay reports a one-trial local engine estimated-o200k token delta on captured payloads. It is not provider-counted, a rate, a provider invoice, causal or verified savings, or task-outcome evidence."
	defaultTrialReplayMaxBytes = 256 << 20
)

// RecordPayload stores raw request payloads only for explicit trial labels. The
// database file is chmod 0600 by Open; payloads are never included in reports.
func (s *Store) RecordPayload(label, requestID, traceID string, body []byte) {
	trialID := strings.TrimPrefix(label, "trial:")
	if trialID == "" || trialID == label {
		return
	}
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO trial_payloads
		  (trial_id, request_id, trace_id, ts, request_bytes, raw_request)
		  VALUES (?, ?, ?, ?, ?, ?)`,
		trialID, requestID, traceID, time.Now().UTC().Format(time.RFC3339), len(body), append([]byte(nil), body...),
	)
	if err != nil && s.logger != nil {
		s.logger.Warn("local trial payload insert failed", "error", err, "request_id", requestID)
	}
}

func (s *Store) StartTrial(trialID, agentSlug, command string) error {
	if trialID == "" {
		return fmt.Errorf("trial_id is required")
	}
	_, err := s.db.Exec(
		`INSERT INTO trial_runs (trial_id, agent_slug, command, started_at)
		  VALUES (?, ?, ?, ?)
		  ON CONFLICT(trial_id) DO UPDATE SET
		    agent_slug = excluded.agent_slug,
		    command = excluded.command`,
		trialID, agentSlug, command, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

func (s *Store) FinishTrial(trialID string, exitCode int) error {
	if trialID == "" {
		return fmt.Errorf("trial_id is required")
	}
	_, err := s.db.Exec(
		`UPDATE trial_runs SET ended_at = ?, exit_code = ? WHERE trial_id = ?`,
		time.Now().UTC().Format(time.RFC3339), exitCode, trialID,
	)
	return err
}

func (s *Store) LatestTrialID() (string, error) {
	var trialID string
	err := s.db.QueryRow(`SELECT trial_id FROM trial_runs ORDER BY started_at DESC LIMIT 1`).Scan(&trialID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return trialID, err
}

func (s *Store) InsertUsageEvents(events []UsageEvent) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(
		`INSERT OR IGNORE INTO usage_events (
		  source_kind, source_path, event_id, ts, agent_slug, provider, model,
		  requests, input_tokens, output_tokens, cached_input_tokens, cache_creation_input_tokens, reasoning_tokens,
		  total_cost_usd, basis, metadata_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	inserted := 0
	for _, ev := range events {
		if ev.EventID == "" || ev.SourceKind == "" {
			continue
		}
		if ev.Timestamp == "" {
			ev.Timestamp = time.Now().UTC().Format(time.RFC3339)
		}
		ev.Requests = nonNegativeInt64(ev.Requests)
		ev.InputTokens = nonNegativeInt64(ev.InputTokens)
		ev.OutputTokens = nonNegativeInt64(ev.OutputTokens)
		ev.CachedInputTokens = nonNegativeInt64(ev.CachedInputTokens)
		ev.CacheCreationInputTokens = nonNegativeInt64(ev.CacheCreationInputTokens)
		if ev.CachedInputTokens > ev.InputTokens || ev.CacheCreationInputTokens > ev.InputTokens-ev.CachedInputTokens {
			// Both cache counters are subsets of effective input. A malformed split
			// cannot poison later cache-heavy headline decisions.
			ev.CachedInputTokens, ev.CacheCreationInputTokens = 0, 0
		}
		ev.ReasoningTokens = nonNegativeInt64(ev.ReasoningTokens)
		if ev.TotalCostUSD < 0 || math.IsNaN(ev.TotalCostUSD) || math.IsInf(ev.TotalCostUSD, 0) {
			ev.TotalCostUSD = 0
		}
		ev.Basis = normalizeUsageBasis(ev.Basis)
		res, err := stmt.Exec(ev.SourceKind, ev.SourcePath, ev.EventID, ev.Timestamp, ev.AgentSlug, ev.Provider, ev.Model,
			ev.Requests, ev.InputTokens, ev.OutputTokens, ev.CachedInputTokens, ev.CacheCreationInputTokens, ev.ReasoningTokens,
			roundUSD(ev.TotalCostUSD), ev.Basis, ev.MetadataJSON)
		if err != nil {
			return inserted, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted += int(n)
		}
	}
	if err := tx.Commit(); err != nil {
		return inserted, err
	}
	return inserted, nil
}

func (s *Store) InsertQuotaEvents(events []QuotaEvent) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(
		`INSERT OR IGNORE INTO quota_snapshots (
		  provider, plan_type, window, used_pct, resets_at, basis, source_kind,
		  observed_at, metadata_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	inserted := 0
	for _, ev := range events {
		if ev.Provider == "" || ev.Window == "" {
			continue
		}
		if ev.ObservedAt == "" {
			ev.ObservedAt = time.Now().UTC().Format(time.RFC3339)
		}
		if ev.UsedPct < 0 || ev.UsedPct > 100 || math.IsNaN(ev.UsedPct) || math.IsInf(ev.UsedPct, 0) {
			ev.UsedPct = 0
		}
		ev.Basis = normalizeQuotaBasis(ev.Basis)
		res, err := stmt.Exec(ev.Provider, ev.PlanType, ev.Window, ev.UsedPct, ev.ResetsAt, ev.Basis, ev.SourceKind, ev.ObservedAt, ev.MetadataJSON)
		if err != nil {
			return inserted, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted += int(n)
		}
	}
	if err := tx.Commit(); err != nil {
		return inserted, err
	}
	return inserted, nil
}

func (s *Store) DeleteQuotaProvider(provider string) error {
	_, err := s.db.Exec(`DELETE FROM quota_snapshots WHERE provider = ?`, provider)
	return err
}

// AnalyzeTrial runs local-only replay checks against captured payloads, stores
// the resulting moves, then returns the public TrialPlan. Local never verifies.
func (s *Store) AnalyzeTrial(trialID, ccrPath string) (TrialPlan, error) {
	if trialID == "" {
		return TrialPlan{}, fmt.Errorf("trial_id is required")
	}
	move, caveat, err := s.replayCompressionMove(trialID, ccrPath)
	if err != nil {
		return TrialPlan{}, err
	}
	if move.OptimizerID != "" {
		if err := s.upsertMove(trialID, move, map[string]any{"basis": trialBasis, "trial_id": trialID}); err != nil {
			return TrialPlan{}, err
		}
	}
	plan, err := s.BuildTrialPlan(trialID)
	if err != nil {
		return plan, err
	}
	if caveat != "" {
		plan.Caveats = appendUnique(plan.Caveats, caveat)
	}
	return plan, nil
}

func (s *Store) BuildTrialPlan(trialID string) (TrialPlan, error) {
	plan := TrialPlan{
		Schema:         trialSchema,
		TrialID:        trialID,
		Basis:          trialBasis,
		Origins:        []UsageOrigin{},
		QuotaSnapshots: []QuotaSnapshot{},
		Moves:          []TrialMove{},
		Learnings:      []Learning{},
		Caveats: []string{
			"Local trial results are inferred or observed-local only. verified_savings remains 0 without supported provider-causal, provider-complete, catalog-priced active Cloud evidence.",
		},
	}
	if trialID != "" {
		plan.Caveats = append(plan.Caveats, "Trial payloads are retained only in the local SQLite database for replay, with file mode 0600, and are never included in HTML reports.")
	}

	from, to := "", ""
	addWindow := func(a, b string) {
		if a != "" && (from == "" || a < from) {
			from = a
		}
		if b != "" && (to == "" || b > to) {
			to = b
		}
	}

	reqOrigins, err := s.requestOrigins(trialID)
	if err != nil {
		return plan, err
	}
	for _, origin := range reqOrigins {
		plan.Origins = append(plan.Origins, origin.Origin)
		addWindow(origin.From, origin.To)
		plan.Headline.Requests += origin.Origin.Requests
		plan.Headline.InputTokens += origin.Origin.InputTokens
		plan.Headline.OutputTokens += origin.OutputTokens
		plan.Headline.TotalCostUSD += origin.Origin.TotalCostUSD
	}

	imported, err := s.importedOrigins()
	if err != nil {
		return plan, err
	}
	importedContext := false
	for _, origin := range imported {
		plan.Origins = append(plan.Origins, origin.Origin)
		if trialID == "" {
			addWindow(origin.From, origin.To)
			plan.Headline.Requests += origin.Origin.Requests
			plan.Headline.InputTokens += origin.Origin.InputTokens
			plan.Headline.OutputTokens += origin.OutputTokens
			plan.Headline.TotalCostUSD += origin.Origin.TotalCostUSD
		} else {
			importedContext = true
		}
	}
	if importedContext {
		plan.Caveats = appendUnique(plan.Caveats, "Imported Claude/Codex history is shown as context only; the trial headline counts proxied traffic from this trial run.")
	}

	plan.Headline.TotalCostUSD = roundUSD(plan.Headline.TotalCostUSD)
	plan.Window = TrialWindow{From: from, To: to}
	plan.QuotaSnapshots, err = s.latestQuotaSnapshots()
	if err != nil {
		return plan, err
	}
	plan.Moves, err = s.trialMoves(trialID, plan)
	if err != nil {
		return plan, err
	}
	for _, move := range plan.Moves {
		if move.Status == statusSafeNow || move.Status == statusNeedsEval {
			plan.Headline.EstimatedSavingsUSD += move.SavingsUSDBase
		}
	}
	plan.Headline.EstimatedSavingsUSD = roundUSD(plan.Headline.EstimatedSavingsUSD)
	if hasTrialMoveStatus(plan.Moves, "context-compression", statusNeedsEval) {
		plan.Caveats = appendUnique(plan.Caveats, compressionReplayCaveat)
	}
	plan.Caveats = appendUnique(plan.Caveats, "Recommendations without an observed counterfactual carry $0 savings; Caveman never invents a fixed percentage from spend.")
	plan.Learnings, err = s.latestLearnings()
	if err != nil {
		return plan, err
	}
	if hasEstimatedOrigin(plan.Origins) {
		plan.Caveats = appendUnique(plan.Caveats, "Some local-history token totals lack provider input/output buckets and are labeled estimated; they are never used as billing truth.")
	}
	if hasUnpricedOrigin(plan.Origins) {
		plan.Caveats = appendUnique(plan.Caveats, "Dollar cost is $0 where the model is unpriced or local history does not prove PAYG auth, service tier, and region; subscription usage is never priced at API list rates.")
	}
	sort.Slice(plan.Origins, func(i, j int) bool {
		if plan.Origins[i].TotalCostUSD == plan.Origins[j].TotalCostUSD {
			return plan.Origins[i].InputTokens > plan.Origins[j].InputTokens
		}
		return plan.Origins[i].TotalCostUSD > plan.Origins[j].TotalCostUSD
	})
	return plan, nil
}

type originRow struct {
	Origin       UsageOrigin
	From, To     string
	OutputTokens int64
}

func (s *Store) requestOrigins(trialID string) ([]originRow, error) {
	label := ""
	if trialID != "" {
		label = "trial:" + trialID
	}
	rows, err := s.db.Query(
		`SELECT COALESCE(MIN(ts), ''), COALESCE(MAX(ts), ''),
		        COALESCE(agent_slug, ''), COALESCE(provider, ''), COALESCE(model, ''),
		        COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(total_cost_usd), 0)
		   FROM requests
		  WHERE (? = '' OR label = ?)
		  GROUP BY COALESCE(agent_slug, ''), COALESCE(provider, ''), COALESCE(model, '')`,
		label, label,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []originRow{}
	for rows.Next() {
		var r originRow
		if err := rows.Scan(&r.From, &r.To, &r.Origin.AgentSlug, &r.Origin.Provider, &r.Origin.Model, &r.Origin.Requests, &r.Origin.InputTokens, &r.OutputTokens, &r.Origin.TotalCostUSD); err != nil {
			return nil, err
		}
		r.Origin.SourceKind = sourceCaveProxy
		r.Origin.Basis = trialBasis
		if r.Origin.AgentSlug == "" {
			r.Origin.AgentSlug = "unknown"
		}
		r.Origin.TotalCostUSD = roundUSD(r.Origin.TotalCostUSD)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) importedOrigins() ([]originRow, error) {
	rows, err := s.db.Query(
		`SELECT COALESCE(MIN(ts), ''), COALESCE(MAX(ts), ''),
		        source_kind, COALESCE(agent_slug, ''), COALESCE(provider, ''), COALESCE(model, ''),
		        COALESCE(SUM(requests), 0), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(total_cost_usd), 0), basis
		   FROM usage_events
		  GROUP BY source_kind, COALESCE(agent_slug, ''), COALESCE(provider, ''), COALESCE(model, ''), basis`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []originRow{}
	for rows.Next() {
		var r originRow
		if err := rows.Scan(&r.From, &r.To, &r.Origin.SourceKind, &r.Origin.AgentSlug, &r.Origin.Provider, &r.Origin.Model, &r.Origin.Requests, &r.Origin.InputTokens, &r.OutputTokens, &r.Origin.TotalCostUSD, &r.Origin.Basis); err != nil {
			return nil, err
		}
		if r.Origin.AgentSlug == "" {
			r.Origin.AgentSlug = "unknown"
		}
		r.Origin.TotalCostUSD = roundUSD(r.Origin.TotalCostUSD)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) latestQuotaSnapshots() ([]QuotaSnapshot, error) {
	rows, err := s.db.Query(
		`SELECT provider, COALESCE(plan_type, 'unknown'), window, COALESCE(used_pct, 0), COALESCE(resets_at, ''), basis
		   FROM quota_snapshots
		  ORDER BY observed_at DESC, id DESC
		  LIMIT 20`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QuotaSnapshot{}
	for rows.Next() {
		var q QuotaSnapshot
		if err := rows.Scan(&q.Provider, &q.PlanType, &q.Window, &q.UsedPct, &q.ResetsAt, &q.Basis); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func (s *Store) trialMoves(trialID string, plan TrialPlan) ([]TrialMove, error) {
	moves := []TrialMove{}
	rows, err := s.db.Query(
		`SELECT optimizer_id, title, safety_class, status, COALESCE(savings_usd_base, 0), confidence
		   FROM trial_results
		  WHERE (? = '' OR trial_id = ?)
		  ORDER BY savings_usd_base DESC, optimizer_id`,
		trialID, trialID,
	)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var m TrialMove
		if err := rows.Scan(&m.OptimizerID, &m.Title, &m.SafetyClass, &m.Status, &m.SavingsUSDBase, &m.Confidence); err != nil {
			_ = rows.Close()
			return nil, err
		}
		var keep bool
		m, keep = sanitizeStoredTrialMove(m)
		if !keep {
			continue
		}
		moves = append(moves, m)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(moves) > 0 {
		return moves, nil
	}
	return heuristicMoves(trialID, plan), nil
}

func heuristicMoves(_ string, plan TrialPlan) []TrialMove {
	moves := []TrialMove{}
	if plan.Headline.Requests > 0 {
		if plan.Headline.InputTokens/plan.Headline.Requests > 8000 {
			moves = append(moves, TrialMove{
				OptimizerID:    "context-compression",
				Title:          "Replay CCR-backed context compression on captured payloads",
				SafetyClass:    classS2,
				Status:         statusNoData,
				SavingsUSDBase: 0,
				Confidence:     "low",
			})
		}
	}
	if len(moves) == 0 {
		moves = append(moves, TrialMove{
			OptimizerID:    "need-more-local-traffic",
			Title:          "Record more local traffic before enabling moves",
			SafetyClass:    classS1,
			Status:         statusNoData,
			SavingsUSDBase: 0,
			Confidence:     "low",
		})
	}
	return moves
}

// sanitizeStoredTrialMove keeps old local databases from reopening retired
// opportunities or replay-derived money after the report contract tightens.
// Historical rows stay on disk; current reports fail closed at read time.
func sanitizeStoredTrialMove(move TrialMove) (TrialMove, bool) {
	switch move.OptimizerID {
	case "model-right-sizing", "context-exploration-offload",
		"anthropic-cache-breakpoints", "openai-prompt-cache-key":
		return TrialMove{}, false
	case "context-compression":
		legacyCounterfactual := move.SavingsUSDBase != 0 ||
			move.Status != statusNoData && !strings.HasPrefix(move.Title, "Local engine replay estimated ")
		move.SavingsUSDBase = 0
		if legacyCounterfactual {
			move.Title = "Legacy local compression estimate is unavailable; re-run trial analysis"
			move.Status = statusNoData
		} else if move.Status != statusNoData {
			move.Status = statusNeedsEval
		}
		move.SafetyClass = classS2
		move.Confidence = "low"
	default:
		// AnalyzeTrial currently persists only context-compression. Treat every
		// unknown stored optimizer identity as unavailable until its evidence and
		// report contract are explicitly added here.
		return TrialMove{}, false
	}
	return move, true
}

func hasTrialMoveStatus(moves []TrialMove, optimizerID, status string) bool {
	for _, move := range moves {
		if move.OptimizerID == optimizerID && move.Status == status {
			return true
		}
	}
	return false
}

func (s *Store) replayCompressionMove(trialID, ccrPath string) (TrialMove, string, error) {
	maxBytes := int64(env.Int("CAVE_TRIAL_REPLAY_MAX_BYTES", defaultTrialReplayMaxBytes))
	if maxBytes <= 0 || maxBytes > 1<<30 {
		maxBytes = defaultTrialReplayMaxBytes
	}
	rows, err := s.db.Query(
		`SELECT CASE WHEN length(p.raw_request) <= ? THEN p.raw_request ELSE NULL END,
		        length(p.raw_request), COALESCE(r.provider, ''), COALESCE(r.model, ''), COALESCE(r.endpoint, '')
		   FROM trial_payloads p
		   LEFT JOIN requests r ON r.request_id = p.request_id AND r.label = ('trial:' || p.trial_id)
		  WHERE p.trial_id = ?
		  ORDER BY p.id`,
		maxBytes, trialID,
	)
	if err != nil {
		return TrialMove{}, "", err
	}
	defer rows.Close()

	var recovery *ccr.Store
	defer func() {
		if recovery != nil {
			_ = recovery.Close()
		}
	}()
	var eng *engine.Engine
	var totalBytes int64
	var payloadCount, before, after, transformed int
	for rows.Next() {
		var p struct {
			raw                       []byte
			bytes                     int64
			provider, model, endpoint string
		}
		if err := rows.Scan(&p.raw, &p.bytes, &p.provider, &p.model, &p.endpoint); err != nil {
			return TrialMove{}, "", err
		}
		payloadCount++
		if p.bytes < 0 || p.bytes > maxBytes || totalBytes > maxBytes-p.bytes {
			return TrialMove{}, "", fmt.Errorf("trial compression replay exceeds %d-byte payload budget", maxBytes)
		}
		totalBytes += p.bytes
		adapter := replayAdapter(p.provider)
		if adapter == nil {
			continue
		}
		segments, reassemble, ok := adapter.ExtractCompressible(p.raw, providers.RequestMetadata{
			Provider: p.provider,
			Model:    p.model,
			Endpoint: p.endpoint,
		})
		if !ok || len(segments) == 0 {
			continue
		}
		if eng == nil {
			if err := os.MkdirAll(filepath.Dir(ccrPath), 0o700); err != nil {
				return TrialMove{}, "", err
			}
			recovery, err = ccr.Open(ccrPath)
			if err != nil {
				return TrialMove{}, "", err
			}
			eng = engine.New(recovery, nil)
		}
		replacements := make([][]byte, len(segments))
		var payloadBefore, payloadAfter int
		var payloadChanged bool
		for i, segment := range segments {
			res, err := eng.Compress(segment, engine.Options{Mode: engine.ModeCompress})
			if err != nil {
				replacements[i] = segment
				continue
			}
			if res.PassedThrough() {
				replacements[i] = segment
				continue
			}
			if res.RecoveryHandle != "" {
				original, err := eng.Retrieve(res.RecoveryHandle)
				if err != nil || !bytes.Equal(original, segment) {
					replacements[i] = segment
					continue
				}
			}
			if res.TokensAfter >= res.TokensBefore {
				replacements[i] = segment
				continue
			}
			replacements[i] = res.Output
			payloadBefore += res.TokensBefore
			payloadAfter += res.TokensAfter
			payloadChanged = true
		}
		if !payloadChanged {
			continue
		}
		if _, err := reassemble(replacements); err != nil {
			continue
		}
		before += payloadBefore
		after += payloadAfter
		transformed++
	}
	if err := rows.Err(); err != nil {
		return TrialMove{}, "", err
	}
	if payloadCount == 0 {
		return TrialMove{
			OptimizerID: "context-compression",
			Title:       "Replay CCR-backed context compression on captured payloads",
			SafetyClass: classS2,
			Status:      statusNoData,
			Confidence:  "low",
		}, "No trial payloads were captured, so compression replay remains insufficient_data.", nil
	}
	if transformed == 0 {
		return TrialMove{
			OptimizerID: "context-compression",
			Title:       "Replay CCR-backed context compression on captured payloads",
			SafetyClass: classS2,
			Status:      statusNoData,
			Confidence:  "low",
		}, "Captured trial payloads did not produce a safe smaller replay result.", nil
	}
	return TrialMove{
		OptimizerID:    "context-compression",
		Title:          fmt.Sprintf("Local engine replay estimated %d to %d o200k tokens across %d captured payloads; task outcome not evaluated", before, after, transformed),
		SafetyClass:    classS2,
		Status:         statusNeedsEval,
		SavingsUSDBase: 0,
		Confidence:     "low",
	}, compressionReplayCaveat, nil
}

func replayAdapter(provider string) providers.Adapter {
	switch provider {
	case "openai", "azure_openai", "openai_compatible":
		return openai.New("http://127.0.0.1")
	case "anthropic":
		return anthropic.New("http://127.0.0.1")
	default:
		return nil
	}
}

func (s *Store) upsertMove(trialID string, move TrialMove, evidence map[string]any) error {
	raw, _ := json.Marshal(evidence)
	_, err := s.db.Exec(
		`INSERT INTO trial_results
		  (trial_id, optimizer_id, title, safety_class, status, savings_usd_base, confidence, evidence_json)
		  VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		  ON CONFLICT(trial_id, optimizer_id) DO UPDATE SET
		    title = excluded.title,
		    safety_class = excluded.safety_class,
		    status = excluded.status,
		    savings_usd_base = excluded.savings_usd_base,
		    confidence = excluded.confidence,
		    evidence_json = excluded.evidence_json`,
		trialID, move.OptimizerID, move.Title, move.SafetyClass, move.Status, move.SavingsUSDBase, move.Confidence, string(raw),
	)
	return err
}

func (s *Store) latestLearnings() ([]Learning, error) {
	rows, err := s.db.Query(
		`SELECT id, text, source_kind, confidence, stored_in_cavemem
		   FROM learnings
		  ORDER BY created_at DESC
		  LIMIT 20`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Learning
	for rows.Next() {
		var l Learning
		var stored int
		if err := rows.Scan(&l.ID, &l.Text, &l.SourceKind, &l.Confidence, &stored); err != nil {
			return nil, err
		}
		l.StoredInCavemem = stored != 0
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) insertLearning(l Learning) error {
	if l.ID == "" || strings.TrimSpace(l.Text) == "" {
		return nil
	}
	stored := 0
	if l.StoredInCavemem {
		stored = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO learnings (id, text, source_kind, confidence, stored_in_cavemem, created_at)
		  VALUES (?, ?, ?, ?, ?, ?)
		  ON CONFLICT(text, source_kind) DO UPDATE SET
		    confidence = excluded.confidence,
		    stored_in_cavemem = excluded.stored_in_cavemem`,
		l.ID, l.Text, l.SourceKind, nonEmpty(l.Confidence, "low"), stored, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

func hasEstimatedOrigin(origins []UsageOrigin) bool {
	for _, origin := range origins {
		if origin.Basis == "estimated" {
			return true
		}
	}
	return false
}

func hasUnpricedOrigin(origins []UsageOrigin) bool {
	for _, origin := range origins {
		if origin.InputTokens > 0 && origin.TotalCostUSD == 0 {
			return true
		}
	}
	return false
}

func appendUnique(values []string, next string) []string {
	if strings.TrimSpace(next) == "" {
		return values
	}
	for _, v := range values {
		if v == next {
			return values
		}
	}
	return append(values, next)
}

func nonEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func nonNegativeInt64(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

func normalizeUsageBasis(v string) string {
	switch v {
	case trialBasis, observedLocal, "observed_provider", "estimated", "manual_import":
		return v
	default:
		// Local/imported evidence can never self-promote to verified. Unknown or
		// missing provenance fails closed to inferred.
		return trialBasis
	}
}

func normalizeQuotaBasis(v string) string {
	switch v {
	case "linked_api", "local_session", "manual_import":
		return v
	default:
		return "manual_import"
	}
}

func roundUSD(v float64) float64 {
	if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	rounded := math.Round(v*10_000_000_000) / 10_000_000_000
	if math.IsNaN(rounded) || math.IsInf(rounded, 0) {
		return 0
	}
	return rounded
}

func logStoreWarning(logger *slog.Logger, msg string, err error) {
	if logger != nil && err != nil {
		logger.Warn(msg, "error", err)
	}
}

var _ = sql.ErrNoRows
