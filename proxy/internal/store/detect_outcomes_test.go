package store

import (
	"strings"
	"testing"
	"time"
)

func outcomeAt(repo string, day int, tokens int64, turns, errors int) sessionOutcome {
	start := time.Date(2026, 8, day, 9, 0, 0, 0, time.UTC)
	return sessionOutcome{
		Repo: repo, Start: start, End: start.Add(90 * time.Minute),
		Tokens: tokens, Turns: turns, ErrorTurns: errors,
	}
}

// TestOutcomeCohortsSplitOnCommitOverlap proves the core join: sessions whose
// window contains a commit land in one cohort, the rest in the other, and the
// dead-end cohort's token share is what gets reported.
func TestOutcomeCohortsSplitOnCommitOverlap(t *testing.T) {
	sessions := []sessionOutcome{
		outcomeAt("/repo/a", 1, 100_000, 40, 2),
		outcomeAt("/repo/a", 2, 100_000, 40, 2),
		outcomeAt("/repo/a", 3, 300_000, 40, 20),
		outcomeAt("/repo/a", 4, 300_000, 40, 20),
		outcomeAt("/repo/a", 5, 100_000, 40, 2),
		outcomeAt("/repo/a", 6, 100_000, 40, 2),
		outcomeAt("/repo/a", 7, 300_000, 40, 20),
		outcomeAt("/repo/a", 8, 300_000, 40, 20),
	}
	// Commits land only on days 1, 2, 5, 6 — the cheap, low-error sessions.
	commits := []time.Time{
		time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC),
	}
	restore := gitCommitTimesFn
	gitCommitTimesFn = func(string, time.Time, time.Time) ([]time.Time, bool) { return commits, true }
	defer func() { gitCommitTimesFn = restore }()

	sinks := outcomeSink(sessions, &LearnSpend{EffectiveInputUSDPerMTok: 3.0})
	if len(sinks) != 1 {
		t.Fatalf("expected one outcome sink, got %d", len(sinks))
	}
	sink := sinks[0]
	if sink.TokensObserved != 1_200_000 {
		t.Fatalf("dead-end cohort tokens = %d, want 1200000", sink.TokensObserved)
	}
	share, _ := sink.Evidence["without_commit_token_share_pct"].(float64)
	if share < 74 || share > 76 {
		t.Fatalf("dead-end share = %v, want ~75", share)
	}
	ratio, ok := sink.Evidence["error_turn_ratio_quiet_vs_shipped"].(float64)
	if !ok || ratio < 9.9 || ratio > 10.1 {
		t.Fatalf("error ratio = %v (ok=%v), want ~10", ratio, ok)
	}
	if _, priced := sink.Evidence["without_commit_spend_usd"]; !priced {
		t.Fatal("a priced window must price the dead-end cohort")
	}
	// The correlational framing is not optional.
	if !strings.Contains(sink.Suggestion, "not automatically wasted") {
		t.Fatalf("suggestion must refuse the waste verdict: %q", sink.Suggestion)
	}
	assoc, _ := sink.Evidence["association"].(string)
	if !strings.Contains(assoc, "correlation") {
		t.Fatalf("evidence must name the association as correlation: %q", assoc)
	}
	if sink.Class != classBehavioral || sink.Framing != framingHistorical {
		t.Fatalf("outcome findings must stay behavioral/historical: %+v", sink)
	}
}

// TestOutcomeOmitsRepositoriesGitCannotAnswer is the fail-closed guard. A
// directory that is not a git checkout must be DROPPED, never counted as
// commitless — that would manufacture a dead-end cohort out of missing
// evidence, which is the most damaging possible bug in this detector.
func TestOutcomeOmitsRepositoriesGitCannotAnswer(t *testing.T) {
	var sessions []sessionOutcome
	for day := 1; day <= 10; day++ {
		sessions = append(sessions, outcomeAt("/not/a/checkout", day, 500_000, 30, 1))
	}
	restore := gitCommitTimesFn
	gitCommitTimesFn = func(string, time.Time, time.Time) ([]time.Time, bool) { return nil, false }
	defer func() { gitCommitTimesFn = restore }()

	if got := outcomeSink(sessions, nil); len(got) != 0 {
		t.Fatalf("an unanswerable repository must produce no finding, got %+v", got)
	}
}

// TestOutcomeSilentOnThinHistory keeps cohorts off a machine where they would
// be noise.
func TestOutcomeSilentOnThinHistory(t *testing.T) {
	restore := gitCommitTimesFn
	gitCommitTimesFn = func(string, time.Time, time.Time) ([]time.Time, bool) { return nil, true }
	defer func() { gitCommitTimesFn = restore }()

	few := []sessionOutcome{outcomeAt("/repo/a", 1, 900_000, 30, 1), outcomeAt("/repo/a", 2, 900_000, 30, 1)}
	if got := outcomeSink(few, nil); len(got) != 0 {
		t.Fatalf("too few sessions must emit nothing: %+v", got)
	}

	var tiny []sessionOutcome
	for day := 1; day <= 10; day++ {
		tiny = append(tiny, outcomeAt("/repo/a", day, 100, 3, 0))
	}
	if got := outcomeSink(tiny, nil); len(got) != 0 {
		t.Fatalf("trivial token volume must emit nothing: %+v", got)
	}
}

// TestSessionProducedCommitBoundaries pins the overlap window, including the
// grace period for a commit made just after the agent stopped.
func TestSessionProducedCommitBoundaries(t *testing.T) {
	session := outcomeAt("/repo/a", 1, 1000, 5, 0)
	inside := session.Start.Add(30 * time.Minute)
	grace := session.End.Add(outcomeCommitGraceWindow - time.Minute)
	late := session.End.Add(outcomeCommitGraceWindow + time.Hour)
	early := session.Start.Add(-time.Hour)

	if !sessionProducedCommit(session, []time.Time{inside}) {
		t.Fatal("a commit during the session must associate")
	}
	if !sessionProducedCommit(session, []time.Time{grace}) {
		t.Fatal("a commit inside the grace window must associate")
	}
	if sessionProducedCommit(session, []time.Time{late}) {
		t.Fatal("a commit long after must not associate")
	}
	if sessionProducedCommit(session, []time.Time{early}) {
		t.Fatal("a commit before the session must not associate")
	}
	if sessionProducedCommit(sessionOutcome{}, []time.Time{inside}) {
		t.Fatal("a session with no window must never associate")
	}
}

// TestOutcomeBudgetSkipsRatherThanMiscounts proves the total git budget bounds
// the whole join, and that a repository skipped for time is EXCLUDED and
// disclosed — never folded into the dead-end cohort, which would turn a
// performance guard into a false finding.
func TestOutcomeBudgetSkipsRatherThanMiscounts(t *testing.T) {
	var sessions []sessionOutcome
	for day := 1; day <= 5; day++ {
		sessions = append(sessions, outcomeAt("/repo/fast", day, 200_000, 30, 1))
		sessions = append(sessions, outcomeAt("/repo/fast", day+10, 200_000, 30, 1))
		sessions = append(sessions, outcomeAt("/repo/slow", day, 900_000, 30, 1))
	}

	// The clock jumps past the budget after the first repository is answered.
	calls := 0
	restoreClock := outcomeClock
	base := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	outcomeClock = func() time.Time {
		calls++
		if calls > 2 {
			return base.Add(outcomeGitTotalBudget + time.Second)
		}
		return base
	}
	defer func() { outcomeClock = restoreClock }()

	var queried []string
	restoreGit := gitCommitTimesFn
	gitCommitTimesFn = func(repo string, _, _ time.Time) ([]time.Time, bool) {
		queried = append(queried, repo)
		return []time.Time{time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)}, true
	}
	defer func() { gitCommitTimesFn = restoreGit }()

	sinks := outcomeSink(sessions, nil)
	if len(queried) != 1 {
		t.Fatalf("the budget must stop further git calls, got %v", queried)
	}
	if len(sinks) != 1 {
		t.Fatalf("the answered repository must still produce a finding, got %d", len(sinks))
	}
	skipped, _ := sinks[0].Evidence["repositories_skipped"].(int)
	if skipped != 1 {
		t.Fatalf("the skipped repository must be disclosed, got %v", sinks[0].Evidence["repositories_skipped"])
	}
	// /repo/slow contributed 4.5M tokens. If it leaked into a cohort the
	// measured session count would exceed what was actually answered.
	measured, _ := sinks[0].Evidence["sessions_measured"].(int)
	if measured != 10 {
		t.Fatalf("only the answered repository's sessions may be counted, got %d", measured)
	}
}
