package store

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// learn_experiment.go is the only local mechanism that produces a CONTROLLED
// savings number.
//
// Every other rung compares a fix against history: the user changed something,
// and later sessions looked different. That cannot separate the fix from the
// week it happened in. A holdout can. The user runs with the change on for a
// while, off for a while, and the harness compares the two arms over their own
// sessions — same person, same repositories, same habits.
//
// It exists because of skill distillation. A distilled procedure ADDS prefix
// tokens to every session and pays back only on the sessions that hit its
// pattern, so "did it help?" is genuinely unanswerable by re-counting a file.
// Nothing else here can grade it honestly.
//
// It is still not `verified`: arms are consecutive, not randomized, and task
// difficulty differs between them. That confounder ships with every report.

const (
	experimentSchema = "caveman.learn.experiment.v1"
	// experimentMinArmSessions is the floor for stating any verdict. Below it
	// the arms are two anecdotes.
	experimentMinArmSessions = 5
	// experimentMeaningfulDelta is how far the medians must separate before the
	// harness calls anything. Anything smaller is reported as unchanged rather
	// than dressed up as a win.
	experimentMeaningfulDelta = 0.10
	experimentArmOn           = "on"
	experimentArmOff          = "off"
)

// Experiment is one holdout, with its arm intervals.
type Experiment struct {
	Label     string          `json:"label"`
	SinkID    string          `json:"sink_id,omitempty"`
	FixKind   string          `json:"fix_kind,omitempty"`
	Note      string          `json:"note,omitempty"`
	CreatedAt string          `json:"created_at"`
	StoppedAt string          `json:"stopped_at,omitempty"`
	Arms      []ExperimentArm `json:"arms"`
	id        int64
}

// ExperimentArm is one contiguous interval during which the change was on or off.
type ExperimentArm struct {
	Arm       string `json:"arm"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at,omitempty"`
}

// ExperimentArmResult is one arm's measured behavior.
type ExperimentArmResult struct {
	Arm                 string  `json:"arm"`
	Sessions            int     `json:"sessions"`
	Turns               int     `json:"turns"`
	Tokens              int64   `json:"tokens"`
	MedianSessionTokens int64   `json:"median_session_tokens"`
	ErrorTurnsPerTurn   float64 `json:"error_turns_per_turn"`
	ActiveDays          float64 `json:"active_days"`
}

// ExperimentReport is what `caveman learn experiment report` renders.
type ExperimentReport struct {
	Schema     string                `json:"schema"`
	Basis      string                `json:"basis"`
	Label      string                `json:"label"`
	SinkID     string                `json:"sink_id,omitempty"`
	FixKind    string                `json:"fix_kind,omitempty"`
	Arms       []ExperimentArmResult `json:"arms"`
	Verdict    string                `json:"verdict"`
	DeltaPct   *float64              `json:"median_session_tokens_delta_pct,omitempty"`
	SavedUSD   *float64              `json:"saved_usd_per_session,omitempty"`
	Currency   string                `json:"currency,omitempty"`
	Attributed LearnAttribution      `json:"attribution"`
	Caveats    []string              `json:"caveats"`
}

func (s *Store) StartExperiment(label, sinkID, fixKind, note string) (Experiment, error) {
	label = strings.TrimSpace(label)
	if label == "" || len(label) > 128 {
		return Experiment{}, fmt.Errorf("experiment label must be 1-128 bytes")
	}
	if len(note) > 4096 {
		return Experiment{}, fmt.Errorf("note exceeds 4096 bytes")
	}
	now := learnOutcomeClock().UTC().Format(time.RFC3339)
	result, err := s.db.Exec(`INSERT INTO experiments (label, sink_id, fix_kind, note, created_at)
		VALUES (?, ?, ?, ?, ?)`, label, sinkID, fixKind, note, now)
	if err != nil {
		return Experiment{}, fmt.Errorf("start experiment %q: %w", label, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Experiment{}, fmt.Errorf("read experiment id: %w", err)
	}
	if _, err := s.db.Exec(`INSERT INTO experiment_arms (experiment_id, arm, started_at)
		VALUES (?, ?, ?)`, id, experimentArmOn, now); err != nil {
		return Experiment{}, fmt.Errorf("open first arm: %w", err)
	}
	return s.LoadExperiment(label)
}

// SwitchExperimentArm closes the open arm and opens the other one. Switching to
// the arm already running is a no-op rather than an error: it keeps a repeated
// call from fragmenting the interval history.
func (s *Store) SwitchExperimentArm(label, arm string) (Experiment, error) {
	arm = strings.ToLower(strings.TrimSpace(arm))
	if arm != experimentArmOn && arm != experimentArmOff {
		return Experiment{}, fmt.Errorf("arm must be %q or %q", experimentArmOn, experimentArmOff)
	}
	experiment, err := s.LoadExperiment(label)
	if err != nil {
		return Experiment{}, err
	}
	if experiment.StoppedAt != "" {
		return Experiment{}, fmt.Errorf("experiment %q is stopped", experiment.Label)
	}
	for _, existing := range experiment.Arms {
		if existing.EndedAt == "" && existing.Arm == arm {
			return experiment, nil
		}
	}
	now := learnOutcomeClock().UTC().Format(time.RFC3339)
	if _, err := s.db.Exec(`UPDATE experiment_arms SET ended_at = ?
		WHERE experiment_id = ? AND ended_at IS NULL`, now, experiment.id); err != nil {
		return Experiment{}, fmt.Errorf("close open arm: %w", err)
	}
	if _, err := s.db.Exec(`INSERT INTO experiment_arms (experiment_id, arm, started_at)
		VALUES (?, ?, ?)`, experiment.id, arm, now); err != nil {
		return Experiment{}, fmt.Errorf("open arm %q: %w", arm, err)
	}
	return s.LoadExperiment(label)
}

func (s *Store) StopExperiment(label string) (Experiment, error) {
	experiment, err := s.LoadExperiment(label)
	if err != nil {
		return Experiment{}, err
	}
	now := learnOutcomeClock().UTC().Format(time.RFC3339)
	if _, err := s.db.Exec(`UPDATE experiment_arms SET ended_at = ?
		WHERE experiment_id = ? AND ended_at IS NULL`, now, experiment.id); err != nil {
		return Experiment{}, fmt.Errorf("close open arm: %w", err)
	}
	if _, err := s.db.Exec(`UPDATE experiments SET stopped_at = ? WHERE id = ?`, now, experiment.id); err != nil {
		return Experiment{}, fmt.Errorf("stop experiment: %w", err)
	}
	return s.LoadExperiment(label)
}

func (s *Store) LoadExperiment(label string) (Experiment, error) {
	label = strings.TrimSpace(label)
	row := s.db.QueryRow(`SELECT id, label, COALESCE(sink_id,''), COALESCE(fix_kind,''),
		COALESCE(note,''), created_at, COALESCE(stopped_at,'') FROM experiments WHERE label = ?`, label)
	var experiment Experiment
	if err := row.Scan(&experiment.id, &experiment.Label, &experiment.SinkID, &experiment.FixKind,
		&experiment.Note, &experiment.CreatedAt, &experiment.StoppedAt); err != nil {
		if err == sql.ErrNoRows {
			return Experiment{}, fmt.Errorf("no experiment named %q", label)
		}
		return Experiment{}, err
	}
	rows, err := s.db.Query(`SELECT arm, started_at, COALESCE(ended_at,'')
		FROM experiment_arms WHERE experiment_id = ? ORDER BY started_at, id`, experiment.id)
	if err != nil {
		return Experiment{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var arm ExperimentArm
		if err := rows.Scan(&arm.Arm, &arm.StartedAt, &arm.EndedAt); err != nil {
			return Experiment{}, err
		}
		experiment.Arms = append(experiment.Arms, arm)
	}
	return experiment, rows.Err()
}

func (s *Store) ListExperiments() ([]Experiment, error) {
	rows, err := s.db.Query(`SELECT label FROM experiments ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var labels []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return nil, err
		}
		labels = append(labels, label)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Experiment, 0, len(labels))
	for _, label := range labels {
		experiment, err := s.LoadExperiment(label)
		if err != nil {
			continue
		}
		out = append(out, experiment)
	}
	return out, nil
}

// armFor returns which arm was active at a moment, if any. A session that
// straddles a switch belongs to the arm its START falls in: the change was in
// force when the session began, and re-attributing mid-session would let one
// session count twice.
func armFor(experiment Experiment, at time.Time) (string, bool) {
	for _, arm := range experiment.Arms {
		start, err := time.Parse(time.RFC3339, arm.StartedAt)
		if err != nil || at.Before(start) {
			continue
		}
		if arm.EndedAt == "" {
			return arm.Arm, true
		}
		end, err := time.Parse(time.RFC3339, arm.EndedAt)
		if err != nil || !at.Before(end) {
			continue
		}
		return arm.Arm, true
	}
	return "", false
}

// BuildExperimentReport compares the arms over the user's own sessions.
func (s *Store) BuildExperimentReport(cwd, label string, sources []string, sinceExpr string) (ExperimentReport, error) {
	experiment, err := s.LoadExperiment(label)
	if err != nil {
		return ExperimentReport{}, err
	}
	plan, err := s.BuildLearnPlan(cwd, sources, sinceExpr)
	if err != nil {
		return ExperimentReport{}, err
	}
	return buildExperimentReport(experiment, plan.sessionOutcomes, plan.Spend), nil
}

func buildExperimentReport(experiment Experiment, sessions []sessionOutcome, spend *LearnSpend) ExperimentReport {
	report := ExperimentReport{
		Schema: experimentSchema, Basis: learnBasis, Label: experiment.Label,
		SinkID: experiment.SinkID, FixKind: experiment.FixKind, Verdict: "insufficient_data",
	}
	byArm := map[string][]sessionOutcome{}
	for _, session := range sessions {
		if session.Start.IsZero() || session.Tokens <= 0 {
			continue
		}
		if arm, ok := armFor(experiment, session.Start); ok {
			byArm[arm] = append(byArm[arm], session)
		}
	}
	for _, arm := range []string{experimentArmOn, experimentArmOff} {
		if result, ok := summarizeExperimentArm(arm, byArm[arm]); ok {
			report.Arms = append(report.Arms, result)
		}
	}
	on, off := findArm(report.Arms, experimentArmOn), findArm(report.Arms, experimentArmOff)

	method := attrHoldout
	if on == nil || off == nil || on.Sessions < experimentMinArmSessions || off.Sessions < experimentMinArmSessions {
		// Not enough evidence for a controlled claim. The rung drops rather
		// than the verdict softening: an under-powered holdout is not a weak
		// holdout, it is not a holdout.
		method = attrNone
		report.Attributed = buildAttribution(method, provenanceUnfingerprnt, "")
		report.Caveats = append(report.Caveats, fmt.Sprintf(
			"Each arm needs at least %d sessions before a verdict. Keep switching arms and re-run.", experimentMinArmSessions))
		return report
	}

	if off.MedianSessionTokens > 0 {
		delta := (float64(on.MedianSessionTokens) - float64(off.MedianSessionTokens)) / float64(off.MedianSessionTokens)
		pct := roundPct(delta * 100)
		report.DeltaPct = &pct
		switch {
		case delta <= -experimentMeaningfulDelta:
			report.Verdict = "improved"
		case delta >= experimentMeaningfulDelta:
			report.Verdict = "regressed"
		default:
			report.Verdict = "unchanged"
		}
		if report.Verdict == "improved" && spend != nil {
			saved := off.MedianSessionTokens - on.MedianSessionTokens
			if usd := spend.priceInputTokens(saved); usd > 0 {
				report.SavedUSD = &usd
				report.Currency = spend.Currency
			}
		}
	}
	report.Attributed = buildAttribution(method, provenanceUnfingerprnt, "")
	report.Caveats = append(report.Caveats,
		"Arms are compared on median tokens per session over your own history. Sessions are assigned by their start time, so a session that straddles a switch counts once, in the arm it began in.",
		"This is the strongest local evidence available and it is still inferred: the arms ran at different times against different work.",
	)
	if on.ErrorTurnsPerTurn > off.ErrorTurnsPerTurn*1.25 {
		report.Caveats = append(report.Caveats,
			"The on-arm hit more tool errors per turn than the off-arm. A cheaper session that fails more is not a saving; weigh the verdict against that.")
	}
	return report
}

func summarizeExperimentArm(arm string, sessions []sessionOutcome) (ExperimentArmResult, bool) {
	if len(sessions) == 0 {
		return ExperimentArmResult{}, false
	}
	result := ExperimentArmResult{Arm: arm, Sessions: len(sessions)}
	tokens := make([]int64, 0, len(sessions))
	var errorTurns int
	var first, last time.Time
	for _, session := range sessions {
		result.Tokens += session.Tokens
		result.Turns += session.Turns
		errorTurns += session.ErrorTurns
		tokens = append(tokens, session.Tokens)
		if first.IsZero() || session.Start.Before(first) {
			first = session.Start
		}
		if session.End.After(last) {
			last = session.End
		}
	}
	sort.Slice(tokens, func(i, j int) bool { return tokens[i] < tokens[j] })
	result.MedianSessionTokens = tokens[len(tokens)/2]
	if result.Turns > 0 {
		result.ErrorTurnsPerTurn = roundMultiplier(float64(errorTurns) / float64(result.Turns))
	}
	if !first.IsZero() && last.After(first) {
		result.ActiveDays = roundMultiplier(last.Sub(first).Hours() / 24)
	}
	return result, true
}

func findArm(arms []ExperimentArmResult, name string) *ExperimentArmResult {
	for i := range arms {
		if arms[i].Arm == name {
			return &arms[i]
		}
	}
	return nil
}
