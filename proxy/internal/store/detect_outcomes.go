package store

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/gitsafe"
)

// detect_outcomes.go asks the question every other detector dances around: did
// this session produce anything?
//
// Compression trims the cost of doing work. This finds work that produced
// nothing — sessions that burned real tokens and ended with no commit touching
// the repository they ran in. On most machines that cohort is the single
// largest recoverable line item, and it is the most user-specific signal in the
// report: it is this person's own hit rate, in their own repositories.
//
// It is CORRELATIONAL and shipped as such. A session without a commit is not
// wasted — exploration, review, reading, and debugging that informs a later
// commit all look identical here. The sink is behavioral, its framing is
// historical, and its caveat is not removable. What it offers is a number the
// user can weigh, not a verdict.
const (
	outcomeMinSessions       = 8      // below this the cohorts are noise
	outcomeMinTokens         = 50_000 // and below this there is nothing to weigh
	outcomeCommitGraceWindow = 45 * time.Minute
	outcomeGitTimeout        = 3 * time.Second
	outcomeMaxRepos          = 12
	outcomeMaxCommits        = 5000
	// outcomeGitTotalBudget caps the WHOLE join, not each call. Twelve repos at
	// the per-call timeout would add half a minute to a command users expect to
	// be fast; when the budget runs out the remaining repositories are skipped
	// and the shortfall is disclosed rather than silently dropped.
	outcomeGitTotalBudget = 6 * time.Second
)

// gitCommitTimesFn is swapped in tests. Production reads real commit times.
var gitCommitTimesFn = gitCommitTimes

// outcomeClock is swapped in tests so the total-budget path is reachable
// without sleeping.
var outcomeClock = time.Now

type sessionOutcome struct {
	Repo        string
	Start       time.Time
	End         time.Time
	Tokens      int64
	Turns       int
	ErrorTurns  int
	Compactions int
}

// outcomeCohort is one measured group of sessions.
type outcomeCohort struct {
	Sessions        int
	Tokens          int64
	Turns           int
	ErrorTurns      int
	Compactions     int
	MedianTokens    int64
	sessionTokenSet []int64
}

func (c *outcomeCohort) add(session sessionOutcome) {
	c.Sessions++
	c.Tokens += session.Tokens
	c.Turns += session.Turns
	c.ErrorTurns += session.ErrorTurns
	c.Compactions += session.Compactions
	c.sessionTokenSet = append(c.sessionTokenSet, session.Tokens)
}

func (c *outcomeCohort) finish() {
	if len(c.sessionTokenSet) == 0 {
		return
	}
	sorted := append([]int64(nil), c.sessionTokenSet...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	c.MedianTokens = sorted[len(sorted)/2]
}

// gitCommitTimes reads commit timestamps for one repository inside a window.
// Read-only, no shell, bounded by a timeout and a count. A repository that is
// not a git checkout, or a machine without git, simply yields nothing — the
// detector then omits that repository rather than treating it as commitless,
// which would manufacture a dead-end cohort out of missing evidence.
func gitCommitTimes(repo string, from, to time.Time) ([]time.Time, bool) {
	repo = strings.TrimSpace(repo)
	if repo == "" || from.IsZero() || to.IsZero() {
		return nil, false
	}
	info, err := os.Stat(repo)
	if err != nil || !info.IsDir() {
		return nil, false
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), outcomeGitTimeout)
	defer cancel()
	cmd := gitsafe.Command(ctx, repo,
		"log", "--all", "--no-merges", "--format=%ct",
		"--since="+from.Add(-time.Hour).UTC().Format(time.RFC3339),
		"--until="+to.Add(outcomeCommitGraceWindow).UTC().Format(time.RFC3339),
		"--max-count="+strconv.Itoa(outcomeMaxCommits))
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	var times []time.Time
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		epoch, convErr := strconv.ParseInt(line, 10, 64)
		if convErr != nil || epoch <= 0 {
			continue
		}
		times = append(times, time.Unix(epoch, 0).UTC())
	}
	sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
	return times, true
}

// sessionProducedCommit reports whether any commit landed while the session was
// running, or shortly after it ended. Overlap is the weakest possible claim of
// association and the only one the data supports: it does not prove the agent
// wrote the commit.
func sessionProducedCommit(session sessionOutcome, commits []time.Time) bool {
	if session.Start.IsZero() || session.End.IsZero() {
		return false
	}
	end := session.End.Add(outcomeCommitGraceWindow)
	for _, commit := range commits {
		if !commit.Before(session.Start) && !commit.After(end) {
			return true
		}
	}
	return false
}

// outcomeSink groups scanned sessions by whether a commit overlapped them, and
// reports what each cohort cost. It emits nothing unless git could actually
// answer for the repositories involved.
func outcomeSink(sessions []sessionOutcome, spend *LearnSpend) []Sink {
	byRepo := map[string][]sessionOutcome{}
	for _, session := range sessions {
		if session.Repo == "" || session.Tokens <= 0 || session.Start.IsZero() {
			continue
		}
		byRepo[session.Repo] = append(byRepo[session.Repo], session)
	}
	if len(byRepo) == 0 {
		return nil
	}
	repos := make([]string, 0, len(byRepo))
	for repo := range byRepo {
		repos = append(repos, repo)
	}
	// Largest repositories first, then bounded: a machine with 200 stale
	// transcript cwds must not run 200 git processes.
	sort.Slice(repos, func(i, j int) bool {
		if len(byRepo[repos[i]]) != len(byRepo[repos[j]]) {
			return len(byRepo[repos[i]]) > len(byRepo[repos[j]])
		}
		return repos[i] < repos[j]
	})
	if len(repos) > outcomeMaxRepos {
		repos = repos[:outcomeMaxRepos]
	}

	var shipped, quiet outcomeCohort
	reposAnswered := 0
	reposSkipped := 0
	deadline := outcomeClock().Add(outcomeGitTotalBudget)
	for _, repo := range repos {
		if outcomeClock().After(deadline) {
			reposSkipped++
			continue
		}
		group := byRepo[repo]
		from, to := group[0].Start, group[0].End
		for _, session := range group {
			if session.Start.Before(from) {
				from = session.Start
			}
			if session.End.After(to) {
				to = session.End
			}
		}
		commits, ok := gitCommitTimesFn(repo, from, to)
		if !ok {
			// Not a git checkout, or git unavailable. Omitting is mandatory:
			// counting these as commitless would invent a dead-end cohort.
			continue
		}
		reposAnswered++
		for _, session := range group {
			if sessionProducedCommit(session, commits) {
				shipped.add(session)
			} else {
				quiet.add(session)
			}
		}
	}
	total := shipped.Sessions + quiet.Sessions
	if reposAnswered == 0 || total < outcomeMinSessions {
		return nil
	}
	shipped.finish()
	quiet.finish()
	if quiet.Tokens < outcomeMinTokens {
		return nil
	}
	quietShare := float64(quiet.Tokens) * 100 / float64(shipped.Tokens+quiet.Tokens)

	evidence := map[string]any{
		"repositories_measured": reposAnswered,
		"sessions_measured":     total,
		"with_commit": map[string]any{
			"sessions": shipped.Sessions, "tokens": shipped.Tokens,
			"median_session_tokens": shipped.MedianTokens,
			"error_turns":           shipped.ErrorTurns, "compactions": shipped.Compactions,
		},
		"without_commit": map[string]any{
			"sessions": quiet.Sessions, "tokens": quiet.Tokens,
			"median_session_tokens": quiet.MedianTokens,
			"error_turns":           quiet.ErrorTurns, "compactions": quiet.Compactions,
		},
		"without_commit_token_share_pct": roundPct(quietShare),
		"association":                    "a commit overlapping the session window; this is correlation, never proof the agent wrote it",
	}
	if reposSkipped > 0 {
		evidence["repositories_skipped"] = reposSkipped
		evidence["repositories_skipped_reason"] = "the commit-history budget ran out; these repositories are excluded from both cohorts, not counted as commitless"
	}
	if ratio, ok := errorTurnRatio(shipped, quiet); ok {
		evidence["error_turn_ratio_quiet_vs_shipped"] = ratio
	}
	if spend != nil {
		if usd := spend.priceInputTokens(quiet.Tokens); usd > 0 {
			evidence["without_commit_spend_usd"] = usd
		}
	}
	return []Sink{{
		SinkID: "session_outcomes",
		Title: fmt.Sprintf("%.0f%% of scanned tokens ran in sessions with no commit in their window",
			quietShare),
		Class: classBehavioral, Basis: "provider_counted", Framing: framingHistorical,
		TokensObserved: quiet.Tokens,
		Evidence:       evidence,
		Suggestion:     "Sessions that end without a commit are not automatically wasted — reading, review and debugging all land here. The number is worth knowing because it is usually the largest single cohort, and because the error-turn ratio between the two cohorts says whether these sessions were exploring or stuck.",
	}}
}

// errorTurnRatio compares how error-prone the two cohorts were, per turn. It is
// the line that distinguishes "I was exploring" from "I was stuck", and it is
// only stated when both cohorts have turns to divide by.
func errorTurnRatio(shipped, quiet outcomeCohort) (float64, bool) {
	if shipped.Turns == 0 || quiet.Turns == 0 {
		return 0, false
	}
	shippedRate := float64(shipped.ErrorTurns) / float64(shipped.Turns)
	quietRate := float64(quiet.ErrorTurns) / float64(quiet.Turns)
	if shippedRate <= 0 {
		return 0, false
	}
	return roundMultiplier(quietRate / shippedRate), true
}
