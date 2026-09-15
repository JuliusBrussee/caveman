package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var learnOutcomeClock = time.Now

// AppliedFixRecord is Caveman-owned outcome-ledger state. BeforeEvidence is
// retained internally as locator/measurement metadata; it never contains a
// recurring block body because sink evidence never contains one.
type AppliedFixRecord struct {
	Recorded            bool           `json:"recorded"`
	ID                  int64          `json:"id"`
	SinkID              string         `json:"sink_id"`
	PracticeID          string         `json:"practice_id,omitempty"`
	FixKind             string         `json:"fix_kind"`
	AppliedAt           string         `json:"applied_at"`
	BeforeTokensPerTurn int64          `json:"before_tokens_per_turn"`
	BeforeEvidence      map[string]any `json:"before_evidence,omitempty"`
	BeforeSource        string         `json:"before_source"`
	Note                string         `json:"note,omitempty"`
	// Target is the artifact this fix edited, fingerprinted immediately after
	// the edit. It is what lets a later scan prove the change is still in
	// place rather than assuming it. Nil for fixes with no file target.
	Target *fixTarget `json:"target,omitempty"`
}

// LearnConfirmed is an inferred longitudinal comparison. After is omitted when
// no like-for-like quantity exists; zero remains visible when zero was measured.
type LearnConfirmed struct {
	SinkID     string `json:"sink_id"`
	PracticeID string `json:"practice_id,omitempty"`
	FixKind    string `json:"fix_kind"`
	AppliedAt  string `json:"applied_at"`
	// Attribution names how the after-value was obtained and what that method
	// cannot rule out. It is required reading next to any saving: the same
	// number means very different things at different rungs.
	Attribution              LearnAttribution `json:"attribution"`
	Before                   float64          `json:"before"`
	After                    *float64         `json:"after,omitempty"`
	Unit                     string           `json:"unit"`
	SessionsAfter            int              `json:"sessions_after"`
	Verdict                  string           `json:"verdict"`
	SupportingPrefixTokens   *int             `json:"supporting_prefix_tokens,omitempty"`
	SupportingPrefixSessions int              `json:"supporting_prefix_sessions,omitempty"`
	id                       int64
}

func (s *Store) RecordAppliedFix(plan LearnPlan, sinkID, fixKind, note string) (AppliedFixRecord, error) {
	var liveSink *Sink
	for i := range plan.Sinks {
		if plan.Sinks[i].SinkID == sinkID {
			liveSink = &plan.Sinks[i]
			break
		}
	}
	appliedAtTime := learnOutcomeClock().UTC()
	// A scan run after the user's edit is post-fix evidence. Prefer newest report
	// snapshot strictly older than this plan's scan; use live plan only when no
	// older snapshot contains the sink. Missing live sinks may use snapshot only.
	sink, beforeSource := beforeSinkForAppliedFix(plan, sinkID, liveSink, appliedAtTime)
	if sink == nil {
		return AppliedFixRecord{}, fmt.Errorf("sink %q not found in current learn plan", sinkID)
	}
	fixKind = strings.TrimSpace(fixKind)
	if fixKind == "" {
		fixKind = defaultFixKind(*sink)
	}
	if fixKind == "" {
		return AppliedFixRecord{}, fmt.Errorf("sink %q has no measurable fix kind; pass --fix-kind explicitly", sinkID)
	}
	if len(fixKind) > 128 {
		return AppliedFixRecord{}, fmt.Errorf("fix kind exceeds 128 bytes")
	}
	if len(note) > 4096 {
		return AppliedFixRecord{}, fmt.Errorf("note exceeds 4096 bytes")
	}
	appliedAt := appliedAtTime.Format(time.RFC3339)
	beforeEvidence := make(map[string]any, len(sink.Evidence)+1)
	for key, value := range sink.Evidence {
		beforeEvidence[key] = value
	}
	beforeEvidence["before_source"] = beforeSource
	evidence, err := json.Marshal(beforeEvidence)
	if err != nil {
		return AppliedFixRecord{}, fmt.Errorf("encode before evidence: %w", err)
	}
	// Fingerprint the edited artifact as it stands NOW — which at
	// `caveman learn applied` time is immediately after the approved edit. This
	// is the provenance anchor: a later scan re-hashes the same path and can
	// state whether the change we proposed is still the change that is there.
	target := fingerprintFixTarget(*sink)
	targetJSON := ""
	if target != nil {
		if encoded, encodeErr := json.Marshal(target); encodeErr == nil {
			targetJSON = string(encoded)
		}
	}
	result, err := s.db.Exec(`INSERT INTO applied_fixes
		(sink_id, practice_id, fix_kind, applied_at, before_tokens_per_turn, before_evidence_json, note, target_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, sink.SinkID, sink.PracticeID, fixKind, appliedAt, sink.TokensPerTurn, string(evidence), note, targetJSON)
	if err != nil {
		return AppliedFixRecord{}, fmt.Errorf("record applied fix: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return AppliedFixRecord{}, fmt.Errorf("read applied fix id: %w", err)
	}
	return AppliedFixRecord{
		Recorded: true, ID: id, SinkID: sink.SinkID, PracticeID: sink.PracticeID,
		FixKind: fixKind, AppliedAt: appliedAt, BeforeTokensPerTurn: sink.TokensPerTurn,
		BeforeEvidence: beforeEvidence, BeforeSource: beforeSource, Note: note,
		Target: target,
	}, nil
}

func beforeSinkForAppliedFix(plan LearnPlan, sinkID string, live *Sink, fallbackCutoff time.Time) (*Sink, string) {
	cutoff := fallbackCutoff
	if parsed, err := time.Parse(time.RFC3339Nano, plan.computedAt); err == nil {
		cutoff = parsed
	}
	if snapshot := newestLearnSnapshotSink(sinkID, cutoff); snapshot != nil {
		return snapshot, "report_snapshot"
	}
	if live != nil {
		copySink := *live
		return &copySink, "live_plan"
	}
	return nil, ""
}

func newestLearnSnapshotSink(sinkID string, before time.Time) *Sink {
	home := strings.TrimSpace(os.Getenv("CAVEMAN_HOME"))
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		home = filepath.Join(userHome, ".caveman")
	}
	dir := filepath.Join(home, "reports")
	paths := []string{DefaultLearnJSONPath(home)}
	history, _ := filepath.Glob(filepath.Join(dir, "caveman-learn.????-??-??.json"))
	paths = append(paths, history...)
	sort.Strings(paths)
	var selected *Sink
	var selectedAt time.Time
	seen := map[string]bool{}
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var snapshot LearnSnapshot
		if json.Unmarshal(raw, &snapshot) != nil {
			continue
		}
		generatedAt, err := time.Parse(time.RFC3339, snapshot.GeneratedAt)
		if err != nil || !generatedAt.Before(before) || generatedAt.Before(selectedAt) {
			continue
		}
		for i := range snapshot.Sinks {
			if snapshot.Sinks[i].SinkID == sinkID {
				copySink := snapshot.Sinks[i]
				selected = &copySink
				selectedAt = generatedAt
				break
			}
		}
	}
	return selected
}

func defaultFixKind(sink Sink) string {
	if fix, _ := sink.Evidence["fix_kind"].(string); fix != "" {
		return fix
	}
	switch {
	case strings.HasPrefix(sink.SinkID, "claude_md_weight:"):
		return "claude_md_weight"
	case strings.HasPrefix(sink.SinkID, "claude_md_sections:"):
		return "claude_md_sections"
	case sink.SinkID == "config_tax:baseline":
		return "config_tax"
	case sink.SinkID == "dead_load:skills":
		return "dead_load"
	case sink.SinkID == "context_dumbzone":
		return "dumbzone_advice"
	default:
		return ""
	}
}

func addSectionConfigPaths(sinks []Sink, cfg configScan) {
	paths := map[string]string{}
	if cfg.ClaudeMDUser != nil {
		paths["user"] = cfg.ClaudeMDUser.Path
	}
	if cfg.ClaudeMDProject != nil {
		paths["project"] = cfg.ClaudeMDProject.Path
	}
	for i := range sinks {
		if !strings.HasPrefix(sinks[i].SinkID, "claude_md_sections:") || sinks[i].Evidence == nil {
			continue
		}
		scope := strings.TrimPrefix(sinks[i].SinkID, "claude_md_sections:")
		if path := paths[scope]; path != "" {
			sinks[i].Evidence["path"] = path
		}
	}
}

func (s *Store) appliedFixes() ([]AppliedFixRecord, error) {
	rows, err := s.db.Query(`SELECT id, sink_id, COALESCE(practice_id,''), fix_kind, applied_at,
		COALESCE(before_tokens_per_turn,0), COALESCE(before_evidence_json,'{}'), COALESCE(note,''),
		COALESCE(target_json,'')
		FROM applied_fixes ORDER BY applied_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []AppliedFixRecord
	for rows.Next() {
		var record AppliedFixRecord
		var evidence, targetJSON string
		if err := rows.Scan(&record.ID, &record.SinkID, &record.PracticeID, &record.FixKind, &record.AppliedAt,
			&record.BeforeTokensPerTurn, &evidence, &record.Note, &targetJSON); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(evidence), &record.BeforeEvidence)
		if strings.TrimSpace(targetJSON) != "" {
			var target fixTarget
			if json.Unmarshal([]byte(targetJSON), &target) == nil && target.Path != "" {
				record.Target = &target
			}
		}
		record.BeforeSource, _ = record.BeforeEvidence["before_source"].(string)
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) confirmedFixes(cfg configScan, sourceSet map[string]bool, repoFilter string, deadline *behaviorDeadline) ([]LearnConfirmed, bool) {
	records, err := s.appliedFixes()
	if err != nil {
		logStoreWarning(s.logger, "applied fix read failed", err)
		return nil, false
	}
	if len(records) == 0 {
		return nil, false
	}
	metricsByRecord, timeBoxed := scanConfirmedFixMetrics(sourceSet, records, repoFilter, deadline)
	if timeBoxed {
		return nil, true
	}
	confirmed := make([]LearnConfirmed, 0, len(records))
	for _, record := range records {
		if _, err := time.Parse(time.RFC3339, record.AppliedAt); err != nil {
			continue
		}
		row := confirmAppliedFix(record, cfg, metricsByRecord[record.ID])
		row.id = record.ID
		confirmed = append(confirmed, row)
	}
	sort.SliceStable(confirmed, func(i, j int) bool {
		if confirmed[i].AppliedAt != confirmed[j].AppliedAt {
			return confirmed[i].AppliedAt < confirmed[j].AppliedAt
		}
		return confirmed[i].id < confirmed[j].id
	})
	return confirmed, false
}

// scanConfirmedFixMetrics walks each transcript once from the earliest applied
// timestamp, then filters each normalized event against every in-memory ledger
// cutoff. Adding ledger rows never adds corpus walks.
func scanConfirmedFixMetrics(sourceSet map[string]bool, records []AppliedFixRecord, repoFilter string, deadline *behaviorDeadline) (map[int64][]learnSessionMetric, bool) {
	type cutoff struct {
		id int64
		at time.Time
	}
	var cutoffs []cutoff
	var earliest time.Time
	for _, record := range records {
		at, err := time.Parse(time.RFC3339, record.AppliedAt)
		if err != nil {
			continue
		}
		cutoffs = append(cutoffs, cutoff{id: record.ID, at: at})
		if earliest.IsZero() || at.Before(earliest) {
			earliest = at
		}
	}
	out := map[int64][]learnSessionMetric{}
	if len(cutoffs) == 0 {
		return out, false
	}
	timeBoxed := false
	for _, base := range learnSessionSources() {
		if !sourceSet[base.id()] {
			continue
		}
		source := sessionSource(base)
		if repoFilter != "" {
			source = repoFilteredSource{sessionSource: base, filter: repoFilter}
		}
		refs, truncated := source.discover(deadline)
		timeBoxed = timeBoxed || truncated
		sort.Slice(refs, func(i, j int) bool {
			if refs[i].relPath != refs[j].relPath {
				return refs[i].relPath < refs[j].relPath
			}
			return refs[i].path < refs[j].path
		})
		type sessionResult struct {
			metrics   []learnSessionMetric
			truncated bool
		}
		results := make([]sessionResult, len(refs))
		parallelSessionScan(len(refs), func(i int) {
			if deadline != nil && deadline.expired() {
				results[i].truncated = true
				return
			}
			metrics := make([]learnSessionMetric, len(cutoffs))
			seenUsage := make([]map[string]bool, len(cutoffs))
			for j := range cutoffs {
				metrics[j] = learnSessionMetric{Repo: refs[i].repo, Source: source.id(), Fingerprints: map[string]int{}}
				seenUsage[j] = map[string]bool{}
			}
			truncated := source.scanSession(refs[i], earliest, func(event turnEvent) {
				if event.sessionStart {
					return
				}
				var fingerprints []string
				for _, payload := range event.TextPayloads {
					for _, block := range segmentBlocks(payload) {
						if estimateTokens(block) >= minBlockTokens {
							fingerprints = append(fingerprints, hashText(normalizeBlock(block)))
						}
					}
				}
				for j, cutoff := range cutoffs {
					metric := &metrics[j]
					if event.Repo != "" {
						metric.Repo = event.Repo
					}
					if event.Timestamp.IsZero() || !event.Timestamp.After(cutoff.at) {
						continue
					}
					metric.Observed = true
					if event.ContextUsagePresent && (event.UsageMessageID == "" || !seenUsage[j][event.UsageMessageID]) {
						if event.UsageMessageID != "" {
							seenUsage[j][event.UsageMessageID] = true
						}
						metric.Turns++
						metric.Contexts = append(metric.Contexts, event.ContextTotal)
						if metric.Prefix == 0 {
							metric.Prefix = event.ContextTotal
						}
						window, _ := contextWindow(event.ProviderKey, event.Model)
						if event.ContextTotal > int(dumbzoneFraction*float64(window)) {
							metric.Dumbzone++
						}
					}
					for _, fingerprint := range fingerprints {
						metric.Fingerprints[fingerprint]++
					}
				}
			}, deadline)
			results[i] = sessionResult{metrics: metrics, truncated: truncated}
		})
		for _, result := range results {
			timeBoxed = timeBoxed || result.truncated
			for j, metric := range result.metrics {
				if metric.Observed && strings.TrimSpace(metric.Repo) != "" {
					out[cutoffs[j].id] = append(out[cutoffs[j].id], metric)
				}
			}
		}
	}
	return out, timeBoxed
}

// confirmAppliedFix measures the outcome and then attaches its attribution:
// which method produced the after-value, whether the artifact we fingerprinted
// at apply time is still intact, and what that method cannot rule out. The
// measurement and the attribution are separated on purpose — the number is the
// same either way, but what it is allowed to claim is not.
func confirmAppliedFix(record AppliedFixRecord, cfg configScan, metrics []learnSessionMetric) LearnConfirmed {
	row := measureAppliedFix(record, cfg, metrics)
	row.PracticeID = record.PracticeID
	provenance, _, _ := checkFixProvenance(record.Target)
	method := attrNone
	switch row.Unit {
	case "config_tokens_per_turn":
		// The after-value is a re-count of the very file the fix edited.
		method = attrDeterministic
	case "recurrence_present", "turns_over_half_window_pct":
		// The after-value comes from sessions that ran after the fix, with no
		// control arm. Exactly the rung interrupted time series names.
		method = attrTimeSeries
	}
	if row.Verdict == "insufficient_data" || row.After == nil {
		method = attrNone
	}
	row.Attribution = buildAttribution(method, provenance, targetPathOf(record))
	return row
}

func targetPathOf(record AppliedFixRecord) string {
	if record.Target == nil {
		return ""
	}
	return record.Target.Path
}

func measureAppliedFix(record AppliedFixRecord, cfg configScan, metrics []learnSessionMetric) LearnConfirmed {
	row := LearnConfirmed{
		SinkID: record.SinkID, FixKind: record.FixKind, AppliedAt: record.AppliedAt,
		Before: float64(record.BeforeTokensPerTurn), Unit: "unavailable",
		SessionsAfter: len(metrics), Verdict: "insufficient_data",
	}
	var prefixes []int
	for _, metric := range metrics {
		if metric.Prefix > 0 {
			prefixes = append(prefixes, metric.Prefix)
		}
	}
	if len(prefixes) >= 3 {
		prefix := medianInts(prefixes)
		row.SupportingPrefixTokens = &prefix
		row.SupportingPrefixSessions = len(prefixes)
	}

	switch {
	case isRecurringFix(record):
		row.Before = 1
		row.Unit = "recurrence_present"
		fingerprint := strings.TrimPrefix(record.SinkID, "recurring_context:repaste:")
		after := 0.0
		for _, metric := range metrics {
			if metric.Fingerprints[fingerprint] > 0 {
				after = 1
				break
			}
		}
		row.After = &after
		if len(metrics) >= 3 {
			if after == 0 {
				row.Verdict = "improved"
			} else {
				row.Verdict = "unchanged"
			}
		}
		return row

	case isDumbzoneFix(record):
		before, ok := numberFromAny(record.BeforeEvidence["pct"])
		if !ok {
			return row
		}
		row.Before = before
		row.Unit = "turns_over_half_window_pct"
		turns, dumbzone := 0, 0
		for _, metric := range metrics {
			turns += metric.Turns
			dumbzone += metric.Dumbzone
		}
		if turns == 0 {
			return row
		}
		after := float64(dumbzone) / float64(turns) * 100
		row.After = &after
		if len(metrics) >= 3 {
			delta := after - before
			switch {
			case delta <= -5:
				row.Verdict = "improved"
			case delta >= 5:
				row.Verdict = "regressed"
			default:
				row.Verdict = "unchanged"
			}
		}
		return row
	}

	afterTokens, ok := comparableConfigTokens(record, cfg)
	if !ok {
		return row
	}
	after := float64(afterTokens)
	row.After = &after
	row.Unit = "config_tokens_per_turn"
	if len(metrics) < 3 {
		return row
	}
	switch {
	case after < row.Before:
		row.Verdict = "improved"
	case after > row.Before:
		row.Verdict = "regressed"
	default:
		row.Verdict = "unchanged"
	}
	return row
}

func isRecurringFix(record AppliedFixRecord) bool {
	return record.FixKind == "cavemem_offload" || strings.HasPrefix(record.SinkID, "recurring_context:repaste:")
}

func isDumbzoneFix(record AppliedFixRecord) bool {
	kind := strings.ReplaceAll(record.FixKind, "-", "_")
	return kind == "dumbzone_advice" || record.SinkID == "context_dumbzone"
}

func comparableConfigTokens(record AppliedFixRecord, cfg configScan) (int, bool) {
	switch {
	case strings.HasPrefix(record.SinkID, "claude_md_weight:"):
		path := fmt.Sprint(record.BeforeEvidence["path"])
		return currentFileTokens(path)
	case strings.HasPrefix(record.SinkID, "claude_md_sections:"):
		path := fmt.Sprint(record.BeforeEvidence["path"])
		if strings.TrimSpace(path) == "" {
			return 0, false
		}
		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return 0, true
		}
		if err != nil {
			return 0, false
		}
		headings := map[string]bool{}
		if values, ok := record.BeforeEvidence["sections"].([]any); ok {
			for _, value := range values {
				if object, ok := value.(map[string]any); ok {
					headings[fmt.Sprint(object["heading"])] = true
				}
			}
		}
		if len(headings) == 0 {
			return 0, false
		}
		total := 0
		matched := 0
		for _, section := range segmentMarkdownSections(string(raw)) {
			if headings[section.Heading] {
				tokens, _ := configTokenCount(section.Raw)
				total += tokens
				matched++
			}
		}
		if matched != len(headings) {
			return 0, false
		}
		return total, true
	case record.SinkID == "dead_load:skills":
		wanted := map[string]bool{}
		if values, ok := record.BeforeEvidence["skills"].([]any); ok {
			for _, value := range values {
				wanted[fmt.Sprint(value)] = true
			}
		}
		if len(wanted) == 0 {
			return 0, false
		}
		if count, ok := numberFromAny(record.BeforeEvidence["skill_count"]); !ok || int(count) != len(wanted) {
			// Evidence intentionally caps its slug sample. Without every slug, a
			// current sum would use fewer units than before_tokens_per_turn.
			return 0, false
		}
		total := 0
		for _, skill := range cfg.Skills {
			slug := strings.ToLower(filepath.Base(filepath.Dir(skill.Path)))
			if wanted[slug] {
				total += skill.DescTokens
			}
		}
		return total, true
	case record.SinkID == "config_tax:baseline":
		return cfg.configTaxPerTurn(), true
	default:
		return 0, false
	}
}

func currentFileTokens(path string) (int, bool) {
	if strings.TrimSpace(path) == "" {
		return 0, false
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0, true
	}
	if err != nil {
		return 0, false
	}
	tokens, _ := configTokenCount(string(raw))
	return tokens, true
}

func numberFromAny(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}
