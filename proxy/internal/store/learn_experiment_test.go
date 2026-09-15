package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func experimentStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func fixedClock(t *testing.T, at time.Time) {
	t.Helper()
	restore := learnOutcomeClock
	learnOutcomeClock = func() time.Time { return at }
	t.Cleanup(func() { learnOutcomeClock = restore })
}

func armSession(day int, tokens int64, turns, errors int) sessionOutcome {
	start := time.Date(2026, 8, day, 10, 0, 0, 0, time.UTC)
	return sessionOutcome{
		Repo: "/repo/a", Start: start, End: start.Add(time.Hour),
		Tokens: tokens, Turns: turns, ErrorTurns: errors,
	}
}

// TestExperimentArmsRecordIntervals proves the arm ledger: starting opens the
// on-arm, switching closes it and opens the other, and switching to the arm
// already running does not fragment the history.
func TestExperimentArmsRecordIntervals(t *testing.T) {
	store := experimentStore(t)
	fixedClock(t, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	experiment, err := store.StartExperiment("pytest-loop", "procedure_repeat:abc", "skill_distillation", "trying the distilled loop")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(experiment.Arms) != 1 || experiment.Arms[0].Arm != experimentArmOn {
		t.Fatalf("start must open the on-arm: %+v", experiment.Arms)
	}

	fixedClock(t, time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC))
	if _, err := store.SwitchExperimentArm("pytest-loop", experimentArmOn); err != nil {
		t.Fatalf("idempotent switch: %v", err)
	}
	again, _ := store.LoadExperiment("pytest-loop")
	if len(again.Arms) != 1 {
		t.Fatalf("switching to the running arm must not fragment history: %+v", again.Arms)
	}

	switched, err := store.SwitchExperimentArm("pytest-loop", experimentArmOff)
	if err != nil {
		t.Fatalf("switch: %v", err)
	}
	if len(switched.Arms) != 2 || switched.Arms[0].EndedAt == "" || switched.Arms[1].Arm != experimentArmOff {
		t.Fatalf("switch must close the old arm and open the new: %+v", switched.Arms)
	}

	fixedClock(t, time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC))
	stopped, err := store.StopExperiment("pytest-loop")
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if stopped.StoppedAt == "" || stopped.Arms[1].EndedAt == "" {
		t.Fatalf("stop must close the open arm: %+v", stopped)
	}
	if _, err := store.SwitchExperimentArm("pytest-loop", experimentArmOn); err == nil {
		t.Fatal("a stopped experiment must refuse further arm switches")
	}
	if _, err := store.LoadExperiment("nope"); err == nil {
		t.Fatal("an unknown label must error")
	}
}

// TestExperimentAssignsSessionsByStartTime pins the assignment rule: a session
// belongs to the arm it BEGAN in, so one straddling a switch counts once.
func TestExperimentAssignsSessionsByStartTime(t *testing.T) {
	experiment := Experiment{
		Label: "x",
		Arms: []ExperimentArm{
			{Arm: experimentArmOn, StartedAt: "2026-08-01T00:00:00Z", EndedAt: "2026-08-10T00:00:00Z"},
			{Arm: experimentArmOff, StartedAt: "2026-08-10T00:00:00Z"},
		},
	}
	if arm, ok := armFor(experiment, time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)); !ok || arm != experimentArmOn {
		t.Fatalf("mid-first-arm = %q ok=%v", arm, ok)
	}
	if arm, ok := armFor(experiment, time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)); !ok || arm != experimentArmOff {
		t.Fatalf("open arm must catch later sessions: %q ok=%v", arm, ok)
	}
	if _, ok := armFor(experiment, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)); ok {
		t.Fatal("a session before the experiment must belong to no arm")
	}
}

// TestExperimentReportGradesAHoldout is the payoff: with both arms populated,
// the harness produces a controlled verdict at the holdout rung, prices the
// per-session saving, and still ships its confounder.
func TestExperimentReportGradesAHoldout(t *testing.T) {
	experiment := Experiment{
		Label: "pytest-loop", SinkID: "procedure_repeat:abc", FixKind: "skill_distillation",
		Arms: []ExperimentArm{
			{Arm: experimentArmOn, StartedAt: "2026-08-01T00:00:00Z", EndedAt: "2026-08-10T00:00:00Z"},
			{Arm: experimentArmOff, StartedAt: "2026-08-10T00:00:00Z"},
		},
	}
	var sessions []sessionOutcome
	for day := 2; day <= 7; day++ {
		sessions = append(sessions, armSession(day, 60_000, 30, 1)) // on-arm: cheaper
	}
	for day := 11; day <= 16; day++ {
		sessions = append(sessions, armSession(day, 100_000, 30, 1)) // off-arm
	}
	report := buildExperimentReport(experiment, sessions, &LearnSpend{EffectiveInputUSDPerMTok: 3.0, Currency: "USD"})
	if report.Verdict != "improved" {
		t.Fatalf("a 40%% median drop must read as improved, got %q (delta %v)", report.Verdict, report.DeltaPct)
	}
	if report.Attributed.Method != attrHoldout || report.Attributed.Confidence != "high" {
		t.Fatalf("a powered holdout must sit at the holdout rung: %+v", report.Attributed)
	}
	if report.SavedUSD == nil || *report.SavedUSD <= 0 {
		t.Fatalf("an improved holdout must price its per-session saving: %+v", report.SavedUSD)
	}
	joined := strings.Join(report.Caveats, " ")
	if !strings.Contains(joined, "different times against different work") {
		t.Fatalf("the standing confounder must ship with the win: %v", report.Caveats)
	}
	if !strings.Contains(strings.Join(report.Attributed.Confounders, " "), "not randomized") {
		t.Fatalf("the rung's own confounder must be attached: %v", report.Attributed.Confounders)
	}
}

// TestExperimentRefusesUnderpoweredVerdict proves an under-powered holdout
// drops to unattributed rather than emitting a weak-but-official-looking win.
func TestExperimentRefusesUnderpoweredVerdict(t *testing.T) {
	experiment := Experiment{
		Label: "thin",
		Arms: []ExperimentArm{
			{Arm: experimentArmOn, StartedAt: "2026-08-01T00:00:00Z", EndedAt: "2026-08-10T00:00:00Z"},
			{Arm: experimentArmOff, StartedAt: "2026-08-10T00:00:00Z"},
		},
	}
	sessions := []sessionOutcome{armSession(2, 10_000, 5, 0), armSession(11, 90_000, 5, 0)}
	report := buildExperimentReport(experiment, sessions, nil)
	if report.Verdict != "insufficient_data" {
		t.Fatalf("two sessions must not produce a verdict, got %q", report.Verdict)
	}
	if report.Attributed.Method != attrNone {
		t.Fatalf("an under-powered holdout is not a holdout: %+v", report.Attributed)
	}
	if report.SavedUSD != nil {
		t.Fatal("no verdict means no priced saving")
	}
}

// TestExperimentFlagsCheaperButWorse guards the failure mode that makes token
// savings dangerous: an arm that is cheaper because it fails more.
func TestExperimentFlagsCheaperButWorse(t *testing.T) {
	experiment := Experiment{
		Label: "risky",
		Arms: []ExperimentArm{
			{Arm: experimentArmOn, StartedAt: "2026-08-01T00:00:00Z", EndedAt: "2026-08-10T00:00:00Z"},
			{Arm: experimentArmOff, StartedAt: "2026-08-10T00:00:00Z"},
		},
	}
	var sessions []sessionOutcome
	for day := 2; day <= 7; day++ {
		sessions = append(sessions, armSession(day, 50_000, 20, 12)) // cheap, error-ridden
	}
	for day := 11; day <= 16; day++ {
		sessions = append(sessions, armSession(day, 100_000, 20, 1))
	}
	report := buildExperimentReport(experiment, sessions, nil)
	if report.Verdict != "improved" {
		t.Fatalf("token median did drop; verdict = %q", report.Verdict)
	}
	if !strings.Contains(strings.Join(report.Caveats, " "), "fails more is not a saving") {
		t.Fatalf("a cheaper-but-failing arm must be called out: %v", report.Caveats)
	}
}
