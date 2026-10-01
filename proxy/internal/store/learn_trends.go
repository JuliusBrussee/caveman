package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// learn_trends.go answers "am I getting better or worse?" week over week. The
// session-derived half is computed from the sessions the base scan already
// read, so it works on the first run; the snapshot-derived half (score
// history, per-sink movers) is attached by the caller that owns the reports
// directory. Everything is observation: medians per UTC ISO week, n printed
// per bucket, no currency, no projection, and a trend is never a saving.

const (
	trendWeeksMax    = 8
	trendPriorWeeks  = 4
	trendMinSessions = 5
	// A change inside either band is "flat": relative percent for every
	// metric, plus an absolute band for metrics already expressed as a share
	// or per-100 rate, where a relative change on a tiny base is noise.
	trendDeadBandPct = 10.0
	trendAbsBand     = 1.0
	trendMoversMax   = 3
	// trendMoverFloor drops sub-noise moves (a recurring block's 1 tok/turn share).
	trendMoverFloor = 10
)

// trendSession is the per-session summary one consumer contributes; every
// field is read from state the consumer already keeps.
type trendSession struct {
	Source       string
	Start        time.Time
	Turns        int
	Tokens       int64
	PeakPct      int
	Dumbzone     int
	FirstTurn    int
	CacheRead    int64
	CacheContext int64
	ToolErrors   int
	TaskSpawns   int
}

func (c *sessionEventConsumer) trendSession() (trendSession, bool) {
	if c.metric.Turns == 0 {
		return trendSession{}, false
	}
	s := trendSession{
		Source: c.sourceID, Start: c.outcome.Start, Turns: c.metric.Turns, Tokens: c.outcome.Tokens,
		PeakPct: c.sessionPeakPct, Dumbzone: c.metric.Dumbzone, FirstTurn: c.metric.Prefix,
		ToolErrors: c.outcome.ErrorTurns, TaskSpawns: c.sessionTasks,
	}
	for _, turn := range c.cacheHygiene.turns {
		s.CacheRead += int64(turn.CacheRead)
		s.CacheContext += int64(turn.ContextTotal)
	}
	return s, true
}

// LearnTrends is the week-over-week block. Weeks are UTC ISO weeks inside the
// scanned window (at most eight); every metric series is aligned to Weeks and
// holds null where the bucket had fewer than MinSessions sessions.
type LearnTrends struct {
	Basis  string `json:"basis"`
	Bucket string `json:"bucket"` // "iso_week_utc"
	// CurrentWeek is the bucket compared against the prior weeks: always the
	// last complete week. The week in progress is shown as a trailing point
	// (InProgress) but never drives the headline change.
	CurrentWeek     string             `json:"current_week"`
	PriorWeeks      int                `json:"prior_weeks"`
	MinSessions     int                `json:"min_sessions"`
	DeadBandPct     float64            `json:"dead_band_pct"`
	UndatedSessions int                `json:"undated_sessions,omitempty"`
	Weeks           []LearnTrendWeek   `json:"weeks"`
	Metrics         []LearnTrendMetric `json:"metrics"`
	Score           *LearnTrendScore   `json:"score,omitempty"`
	Movers          *LearnTrendMovers  `json:"movers,omitempty"`
	Note            string             `json:"note"`
}

type LearnTrendWeek struct {
	Week  string `json:"week"`  // e.g. 2026-W39
	Start string `json:"start"` // Monday, UTC, YYYY-MM-DD
	// Partial marks a week cut by the window start or still in progress.
	Partial bool `json:"partial,omitempty"`
	// InProgress marks the trailing week that has not ended yet.
	InProgress       bool           `json:"in_progress,omitempty"`
	Sessions         int            `json:"sessions"`
	Turns            int            `json:"turns"`
	SessionsBySource map[string]int `json:"sessions_by_source,omitempty"`
	InsufficientData bool           `json:"insufficient_data,omitempty"`
}

// LearnTrendMetric compares CurrentWeek against the pooled sessions of up to
// PriorWeeks weeks before it. Statistic names how a bucket reduces: "median"
// over sessions, "pooled_share" over turns, or "recomputed" for score
// components. Direction is improved|worse|flat|insufficient_data.
type LearnTrendMetric struct {
	Key             string     `json:"key"`
	Label           string     `json:"label"`
	Unit            string     `json:"unit"`   // tokens|pct|per_100_turns|points
	Better          string     `json:"better"` // lower|higher
	Statistic       string     `json:"statistic"`
	Series          []*float64 `json:"series"`
	Current         *float64   `json:"current,omitempty"`
	Prior           *float64   `json:"prior,omitempty"`
	CurrentSessions int        `json:"current_sessions"`
	PriorSessions   int        `json:"prior_sessions"`
	DeltaPct        *float64   `json:"delta_pct,omitempty"`
	Direction       string     `json:"direction"`
}

// LearnTrendScore names where score history comes from. Only the
// session-derived components (dumbzone, subagent pressure) are recomputed per
// week — config tax and dead load compare today's config, so replaying them
// against old sessions would invent history. The full score's history comes
// from dated report snapshots only.
type LearnTrendScore struct {
	Source        string                 `json:"source"` // sessions_recomputed
	Components    []string               `json:"components"`
	Omitted       string                 `json:"omitted"`
	HistorySource string                 `json:"history_source,omitempty"` // snapshots
	History       []LearnTrendScorePoint `json:"history,omitempty"`
}

type LearnTrendScorePoint struct {
	Date  string `json:"date"`
	Score int    `json:"score"`
}

// LearnTrendMovers compares sink tokens/turn against the same prior snapshot
// the CLI's "since your last run" diff uses (newest ≥7 days old, else newest).
type LearnTrendMovers struct {
	Source string            `json:"source"` // snapshot
	Since  string            `json:"since"`
	Days   int               `json:"days"`
	Grew   []LearnTrendMover `json:"grew,omitempty"`
	Shrank []LearnTrendMover `json:"shrank,omitempty"`
}

type LearnTrendMover struct {
	SinkID             string `json:"sink_id"`
	Title              string `json:"title"`
	Status             string `json:"status"` // changed|new|gone
	PriorTokensPerTurn int64  `json:"prior_tokens_per_turn"`
	TokensPerTurn      int64  `json:"tokens_per_turn"`
	DeltaTokensPerTurn int64  `json:"delta_tokens_per_turn"`
}

type trendMetricSpec struct {
	key, label, unit, better, statistic string
	absBand                             float64
	value                               func([]trendSession) (float64, bool)
}

var trendMetricSpecs = []trendMetricSpec{
	{"tokens_per_session", "tokens per session", "tokens", "lower", "median", 0, func(b []trendSession) (float64, bool) {
		return trendMedian(b, func(s trendSession) float64 { return float64(s.Tokens) })
	}},
	{"peak_context_pct", "peak context used", "pct", "lower", "median", trendAbsBand, func(b []trendSession) (float64, bool) {
		return trendMedian(b, func(s trendSession) float64 { return float64(s.PeakPct) })
	}},
	{"dumbzone_turn_pct", "overloaded messages", "pct", "lower", "pooled_share", trendAbsBand, func(b []trendSession) (float64, bool) {
		return trendShare(b, func(s trendSession) (float64, float64) { return float64(s.Dumbzone), float64(s.Turns) })
	}},
	{"first_turn_tokens", "first-message size", "tokens", "lower", "median", 0, func(b []trendSession) (float64, bool) {
		return trendMedian(b, func(s trendSession) float64 { return float64(s.FirstTurn) })
	}},
	{"cache_read_pct", "read from cache", "pct", "higher", "pooled_share", trendAbsBand, func(b []trendSession) (float64, bool) {
		return trendShare(b, func(s trendSession) (float64, float64) { return float64(s.CacheRead), float64(s.CacheContext) })
	}},
	{"tool_errors_per_100_turns", "tool errors", "per_100_turns", "lower", "pooled_share", trendAbsBand, func(b []trendSession) (float64, bool) {
		return trendShare(b, func(s trendSession) (float64, float64) { return float64(s.ToolErrors), float64(s.Turns) })
	}},
	{"score_session_penalty", "score lost to habits", "points", "lower", "recomputed", trendAbsBand, func(b []trendSession) (float64, bool) {
		return float64(trendSessionPenalty(b)), len(b) > 0
	}},
}

func trendMedian(b []trendSession, f func(trendSession) float64) (float64, bool) {
	if len(b) == 0 {
		return 0, false
	}
	vals := make([]float64, len(b))
	for i, s := range b {
		vals[i] = f(s)
	}
	sort.Float64s(vals)
	mid := len(vals) / 2
	if len(vals)%2 == 0 {
		return (vals[mid-1] + vals[mid]) / 2, true
	}
	return vals[mid], true
}

// trendShare is a pooled ratio ×100 (share of turns, or per 100 turns).
func trendShare(b []trendSession, f func(trendSession) (float64, float64)) (float64, bool) {
	var num, den float64
	for _, s := range b {
		n, d := f(s)
		num += n
		den += d
	}
	if den <= 0 {
		return 0, false
	}
	return num / den * 100, true
}

// trendSessionPenalty reuses caveScore on a week's sessions with no config, so
// only the session-derived components can carry a penalty.
func trendSessionPenalty(b []trendSession) int {
	beh := behaviorScan{SessionsScanned: len(b)}
	for _, s := range b {
		beh.Turns += s.Turns
		beh.DumbzoneTurns += s.Dumbzone
		beh.TaskSpawns += s.TaskSpawns
	}
	penalty := 0
	for _, c := range caveScore(configScan{}, beh, 0, 0).Components {
		if c.Key == scoreKeyDumbzone || c.Key == scoreKeySubagent {
			penalty += c.Penalty
		}
	}
	return penalty
}

func isoWeekStart(t time.Time) time.Time {
	t = t.UTC()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	offset := (int(day.Weekday()) + 6) % 7 // Monday = 0
	return day.AddDate(0, 0, -offset)
}

func isoWeekLabel(t time.Time) string {
	year, week := t.ISOWeek()
	return fmt.Sprintf("%d-W%02d", year, week)
}

func trendRound(v float64, unit string) float64 {
	if unit == "tokens" || unit == "points" {
		return math.Round(v)
	}
	return math.Round(v*10) / 10
}

func trendDirection(cur, prior float64, better string, absBand float64) (*float64, string) {
	var delta *float64
	if prior != 0 {
		d := math.Round((cur-prior)/prior*1000) / 10
		delta = &d
	}
	diff := cur - prior
	if math.Abs(diff) <= absBand || (delta != nil && math.Abs(*delta) < trendDeadBandPct) {
		return delta, "flat"
	}
	if (diff < 0) == (better == "lower") {
		return delta, "improved"
	}
	return delta, "worse"
}

// buildLearnTrends buckets sessions by the UTC ISO week they started in. Nil
// when no dated session falls inside the bucketed weeks, or when the window
// holds no complete week to compare.
func buildLearnTrends(sessions []trendSession, since, now time.Time) *LearnTrends {
	end := isoWeekStart(now)
	first := end.AddDate(0, 0, -7*(trendWeeksMax-1))
	if !since.IsZero() {
		if s := isoWeekStart(since); s.After(first) {
			first = s
		}
	}
	n := int(end.Sub(first).Hours()/(24*7)) + 1
	if n < 2 {
		return nil
	}
	buckets := make([][]trendSession, n)
	undated, dated := 0, 0
	for _, s := range sessions {
		if s.Start.IsZero() {
			undated++
			continue
		}
		i := int(isoWeekStart(s.Start).Sub(first).Hours() / (24 * 7))
		if i < 0 || i >= n {
			continue
		}
		buckets[i] = append(buckets[i], s)
		dated++
	}
	if dated == 0 {
		return nil
	}
	current := n - 2 // the last bucket is the week in progress
	priorFrom := max(0, current-trendPriorWeeks)
	var prior []trendSession
	for _, b := range buckets[priorFrom:current] {
		prior = append(prior, b...)
	}

	t := &LearnTrends{
		Basis: learnBasis, Bucket: "iso_week_utc", CurrentWeek: isoWeekLabel(first.AddDate(0, 0, 7*current)),
		PriorWeeks: current - priorFrom, MinSessions: trendMinSessions, DeadBandPct: trendDeadBandPct,
		UndatedSessions: undated,
		Note:            "How your own sessions changed week to week. A trend is not a saving, and it does not show what caused the change.",
		Score: &LearnTrendScore{
			Source:     "sessions_recomputed",
			Components: []string{scoreKeyDumbzone, scoreKeySubagent},
			Omitted:    "instruction size and unused skills are judged on today's setup, so they are not re-scored for past weeks",
		},
	}
	for i, b := range buckets {
		start := first.AddDate(0, 0, 7*i)
		w := LearnTrendWeek{
			Week: isoWeekLabel(start), Start: start.Format("2006-01-02"), Sessions: len(b),
			Partial:          i == n-1 || (!since.IsZero() && start.Before(since)),
			InProgress:       i == n-1,
			InsufficientData: len(b) < trendMinSessions,
		}
		for _, s := range b {
			w.Turns += s.Turns
			if w.SessionsBySource == nil {
				w.SessionsBySource = map[string]int{}
			}
			w.SessionsBySource[s.Source]++
		}
		t.Weeks = append(t.Weeks, w)
	}
	for _, spec := range trendMetricSpecs {
		m := LearnTrendMetric{
			Key: spec.key, Label: spec.label, Unit: spec.unit, Better: spec.better, Statistic: spec.statistic,
			CurrentSessions: len(buckets[current]), PriorSessions: len(prior), Direction: "insufficient_data",
		}
		any := false
		for _, b := range buckets {
			var point *float64
			if len(b) >= trendMinSessions {
				if v, ok := spec.value(b); ok {
					v = trendRound(v, spec.unit)
					point, any = &v, true
				}
			}
			m.Series = append(m.Series, point)
		}
		if !any {
			continue // e.g. no scanned source reports cache buckets
		}
		m.Current = m.Series[current]
		if len(prior) >= trendMinSessions {
			if v, ok := spec.value(prior); ok {
				v = trendRound(v, spec.unit)
				m.Prior = &v
			}
		}
		if m.Current != nil && m.Prior != nil {
			m.DeltaPct, m.Direction = trendDirection(*m.Current, *m.Prior, spec.better, spec.absBand)
		}
		t.Metrics = append(t.Metrics, m)
	}
	return t
}

// AttachLearnTrendHistory adds the snapshot-derived score history and sink
// movers from home/reports. Call it before the current plan is written so the
// run never compares against itself. No-op when the plan carries no trends.
func AttachLearnTrendHistory(plan *LearnPlan, home string, now time.Time) {
	if plan == nil || plan.Trends == nil {
		return
	}
	type snapSink struct {
		SinkID        string `json:"sink_id"`
		Title         string `json:"title"`
		TokensPerTurn int64  `json:"tokens_per_turn"`
	}
	type snap struct {
		GeneratedAt string `json:"generated_at"`
		CaveScore   struct {
			Score int `json:"score"`
		} `json:"cave_score"`
		Sinks []snapSink `json:"sinks"`
		at    time.Time
	}
	paths, _ := filepath.Glob(filepath.Join(home, "reports", "caveman-learn.????-??-??.json"))
	var snaps []snap
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var s snap
		if json.Unmarshal(raw, &s) != nil {
			continue
		}
		if s.at, err = time.Parse(time.RFC3339, s.GeneratedAt); err != nil || !s.at.Before(now.Add(-time.Minute)) {
			continue
		}
		snaps = append(snaps, s)
	}
	if len(snaps) == 0 {
		return
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].at.Before(snaps[j].at) })
	if plan.Trends.Score != nil {
		plan.Trends.Score.HistorySource = "snapshots"
		for _, s := range snaps {
			plan.Trends.Score.History = append(plan.Trends.Score.History, LearnTrendScorePoint{Date: s.at.UTC().Format("2006-01-02"), Score: s.CaveScore.Score})
		}
		today := LearnTrendScorePoint{Date: now.UTC().Format("2006-01-02"), Score: plan.CaveScore.Score}
		if h := plan.Trends.Score.History; h[len(h)-1].Date == today.Date {
			h[len(h)-1] = today // this run supersedes an earlier same-day snapshot
		} else {
			plan.Trends.Score.History = append(h, today)
		}
	}

	prior := snaps[len(snaps)-1]
	for i := len(snaps) - 1; i >= 0; i-- {
		if now.Sub(snaps[i].at) >= 7*24*time.Hour {
			prior = snaps[i]
			break
		}
	}
	before := map[string]snapSink{}
	for _, s := range prior.Sinks {
		before[s.SinkID] = s
	}
	var moves []LearnTrendMover
	seen := map[string]bool{}
	for _, s := range plan.Sinks {
		seen[s.SinkID] = true
		p, ok := before[s.SinkID]
		m := LearnTrendMover{SinkID: s.SinkID, Title: s.Title, Status: "changed", PriorTokensPerTurn: p.TokensPerTurn, TokensPerTurn: s.TokensPerTurn}
		if !ok {
			m.Status = "new"
		}
		moves = append(moves, m)
	}
	for _, p := range prior.Sinks {
		if !seen[p.SinkID] {
			moves = append(moves, LearnTrendMover{SinkID: p.SinkID, Title: p.Title, Status: "gone", PriorTokensPerTurn: p.TokensPerTurn})
		}
	}
	movers := &LearnTrendMovers{Source: "snapshot", Since: prior.at.UTC().Format("2006-01-02"), Days: max(1, int(now.Sub(prior.at).Hours()/24))}
	for i := range moves {
		moves[i].DeltaTokensPerTurn = moves[i].TokensPerTurn - moves[i].PriorTokensPerTurn
	}
	sort.SliceStable(moves, func(i, j int) bool { return moves[i].DeltaTokensPerTurn > moves[j].DeltaTokensPerTurn })
	for _, m := range moves {
		if m.DeltaTokensPerTurn < trendMoverFloor || len(movers.Grew) == trendMoversMax {
			break
		}
		movers.Grew = append(movers.Grew, m)
	}
	for i := len(moves) - 1; i >= 0 && moves[i].DeltaTokensPerTurn <= -trendMoverFloor && len(movers.Shrank) < trendMoversMax; i-- {
		movers.Shrank = append(movers.Shrank, moves[i])
	}
	if len(movers.Grew)+len(movers.Shrank) > 0 {
		plan.Trends.Movers = movers
	}
}

// --- HTML ------------------------------------------------------------------

type trendCard struct {
	Label, Value, Prior, Delta, Direction, Tone string
	N                                           int
	SVG                                         template.HTML
}

func trendFmt(v *float64, unit string) string {
	if v == nil {
		return "—"
	}
	switch unit {
	case "tokens":
		return humanTokens(int64(*v))
	case "pct":
		return strings.TrimSuffix(fmt.Sprintf("%.1f", *v), ".0") + "%"
	case "per_100_turns":
		return strings.TrimSuffix(fmt.Sprintf("%.1f", *v), ".0") + " per 100 messages"
	case "points":
		return fmt.Sprintf("−%.0f points", *v)
	}
	return fmt.Sprintf("%g", *v)
}

// shortDate renders YYYY-MM-DD as "Sep 21"; anything else passes through.
func shortDate(day string) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return t.Format("Jan 2")
}

// weekName is the plain label for a week: "week of Sep 21".
func weekName(w LearnTrendWeek) string {
	if w.Start == "" {
		return w.Week
	}
	return "week of " + shortDate(w.Start)
}

// trendSparkSVG draws one metric's weekly series: a polyline broken at null
// buckets, one dot per measured week, the compared week emphasized, and a
// dashed line at the prior-weeks value. The week in progress hangs off a
// dotted connector as a hollow dot. Sized by viewBox, so it scales.
func trendSparkSVG(series []*float64, current int, prior *float64, weeks []LearnTrendWeek, unit string, w float64) template.HTML {
	const h, pad = 48.0, 5.0
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, v := range series {
		if v != nil {
			lo, hi = math.Min(lo, *v), math.Max(hi, *v)
		}
	}
	if prior != nil {
		lo, hi = math.Min(lo, *prior), math.Max(hi, *prior)
	}
	if math.IsInf(lo, 0) {
		return ""
	}
	if hi == lo {
		lo, hi = lo-1, hi+1
	}
	step := 0.0
	if len(series) > 1 {
		step = (w - 2*pad) / float64(len(series)-1)
	}
	x := func(i int) float64 { return pad + step*float64(i) }
	y := func(v float64) float64 { return h - pad - (v-lo)/(hi-lo)*(h-2*pad) }
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %.0f %.0f" width="100%%" height="%.0f" role="img" aria-label="weekly series">`, w, h, h)
	if prior != nil {
		fmt.Fprintf(&b, `<line x1="%.1f" x2="%.1f" y1="%.1f" y2="%.1f" stroke="#cfcdc7" stroke-dasharray="3 3"/>`, pad, w-pad, y(*prior), y(*prior))
	}
	var pts []string
	flush := func() {
		if len(pts) > 1 {
			fmt.Fprintf(&b, `<polyline fill="none" stroke="#57564f" stroke-width="1.5" stroke-linejoin="round" points="%s"/>`, strings.Join(pts, " "))
		}
		pts = nil
	}
	for i, v := range series {
		if v == nil || weeks[i].InProgress {
			flush()
			continue
		}
		pts = append(pts, fmt.Sprintf("%.1f,%.1f", x(i), y(*v)))
	}
	flush()
	if i := len(series) - 1; i > 0 && weeks[i].InProgress && series[i] != nil && series[i-1] != nil {
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#b4b3ae" stroke-width="1.5" stroke-dasharray="2 2"/>`, x(i-1), y(*series[i-1]), x(i), y(*series[i]))
	}
	for i, v := range series {
		label := weekName(weeks[i])
		if v == nil {
			fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="2" fill="none" stroke="#cfcdc7"><title>%s · %s · not enough data</title></circle>`, x(i), h-pad, template.HTMLEscapeString(label), plural(weeks[i].Sessions, "session"))
			continue
		}
		r, fill, stroke := 2.5, "#9b9a97", "none"
		if i == current {
			r, fill = 3.5, "#37352f"
		}
		if weeks[i].InProgress {
			fill, stroke, label = "#fff", "#b4b3ae", label+" (still running)"
		}
		sessions := ""
		if weeks[i].Sessions > 0 {
			sessions = " · " + plural(weeks[i].Sessions, "session")
		}
		fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="%.1f" fill="%s" stroke="%s"><title>%s · %s%s</title></circle>`, x(i), y(*v), r, fill, stroke, template.HTMLEscapeString(label), template.HTMLEscapeString(trendFmt(v, unit)), sessions)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String()) // every interpolated string above is escaped or numeric
}

func trendCurrentIndex(t *LearnTrends) int {
	for i, w := range t.Weeks {
		if w.Week == t.CurrentWeek {
			return i
		}
	}
	return len(t.Weeks) - 1
}

func trendCards(t *LearnTrends) []trendCard {
	cur := trendCurrentIndex(t)
	var cards []trendCard
	for _, m := range t.Metrics {
		c := trendCard{
			Label: m.Label, Value: trendFmt(m.Current, m.Unit), Prior: trendFmt(m.Prior, m.Unit),
			Direction: strings.Replace(m.Direction, "insufficient_data", "not enough data", 1), N: m.CurrentSessions,
			SVG: trendSparkSVG(m.Series, cur, m.Prior, t.Weeks, m.Unit, 220),
		}
		switch {
		case m.Unit == "pct" && m.Current != nil && m.Prior != nil:
			// Share metrics move in percentage points; a percent of a percent overstates.
			diff, unit := *m.Current-*m.Prior, " points"
			if math.Abs(diff) == 1 {
				unit = " point"
			}
			c.Delta = strings.TrimSuffix(fmt.Sprintf("%+.1f", diff), ".0") + unit
		case m.DeltaPct != nil:
			c.Delta = fmt.Sprintf("%+.0f%%", *m.DeltaPct)
		}
		c.Tone = map[string]string{"improved": "green", "worse": "red"}[m.Direction]
		if c.Tone == "" {
			c.Tone = "gray"
		}
		cards = append(cards, c)
	}
	return cards
}

func trendScoreSVG(history []LearnTrendScorePoint) template.HTML {
	if len(history) < 2 {
		return ""
	}
	series := make([]*float64, len(history))
	weeks := make([]LearnTrendWeek, len(history))
	for i, p := range history {
		v := float64(p.Score)
		series[i], weeks[i] = &v, LearnTrendWeek{Week: shortDate(p.Date)}
	}
	return trendSparkSVG(series, len(history)-1, nil, weeks, "", 600)
}

var trendsTemplate = template.Must(template.New("trends").Funcs(template.FuncMap{
	"cards": trendCards, "scoreSVG": trendScoreSVG, "comma": func(v int64) string { return commaInt(v) },
	"weekName": weekName, "shortDate": shortDate, "plural": plural,
	"currentWeek": func(t *LearnTrends) string {
		for _, w := range t.Weeks {
			if w.Week == t.CurrentWeek {
				return weekName(w)
			}
		}
		return t.CurrentWeek
	},
	"signed": func(v int64) string {
		if v > 0 {
			return "+" + commaInt(v)
		}
		return "−" + commaInt(-v)
	},
}).Parse(`<style>
.tgrid{display:grid;grid-template-columns:repeat(auto-fill,minmax(200px,1fr));gap:12px}
.tcard{border:1px solid #ededec;border-radius:10px;padding:14px 16px 10px;min-width:0}
.tcard .tl{font-size:12px;color:#787774;display:flex;justify-content:space-between;gap:8px}
.tcard .tv{font-size:22px;font-weight:700;letter-spacing:-.02em;font-variant-numeric:tabular-nums;line-height:1.2;margin:2px 0 6px}
.tcard .tp{font-size:12px;color:#9b9a97;font-variant-numeric:tabular-nums}
.tweeks{display:flex;flex-wrap:wrap;gap:4px 14px;font-size:12px;color:#787774;margin:14px 0 0;font-variant-numeric:tabular-nums}
.tweeks .ins{color:#b4b3ae}
.tmov{font-size:14px;margin:6px 0 0;padding-left:20px}
.tmov li{margin:3px 0}
</style>
<h2>Trends</h2>
<p class="note">{{.Note}} Weeks run Monday to Sunday (UTC). Token counts use the middle session of each week; percentages count all messages together. The bold dot is the {{currentWeek .}}, compared with the {{.PriorWeeks}} weeks before it taken together (dashed line). Weeks with fewer than {{.MinSessions}} sessions are too small to show. Changes under {{printf "%.0f" .DeadBandPct}}% count as flat. Tokens only, no dollars.</p>
<div class="tgrid">
{{range cards .}}
  <div class="tcard">
    <div class="tl"><span>{{.Label}}</span><span class="pill {{.Tone}}">{{.Direction}}</span></div>
    <div class="tv">{{.Value}}</div>
    {{.SVG}}
    <div class="tp">before: {{.Prior}}{{if .Delta}} · {{.Delta}}{{end}} · {{plural .N "session"}}</div>
  </div>
{{end}}
</div>
<div class="tweeks">{{range .Weeks}}<span{{if or .InsufficientData .InProgress}} class="ins"{{end}} title="{{.Start}}{{if .InProgress}} · still running{{else if .Partial}} · partial week{{end}}">{{shortDate .Start}}{{if .InProgress}} (still running){{else if .Partial}}*{{end}} · {{plural .Sessions "session"}}</span>{{end}}</div>
<p class="fine">* Only part of this week falls inside the period scanned. The current week is drawn hollow and is never used for the comparison. “Score lost to habits” re-scores only overloaded messages and subagent use for each week; {{.Score.Omitted}}.{{if .UndatedSessions}} {{plural .UndatedSessions "session"}} had no date, so they are not in any week.{{end}}</p>
{{with .Score}}{{if .History}}{{$svg := scoreSVG .History}}{{if $svg}}
<div class="dcard" style="margin-top:16px">
  <div class="kicker">Setup Score across your saved reports</div>
  {{$svg}}
  <div class="tweeks">{{range .History}}<span>{{shortDate .Date}}: {{.Score}}</span>{{end}}</div>
</div>
{{end}}{{end}}{{end}}
{{with .Movers}}
<div class="dcard" style="margin-top:16px">
  <div class="kicker">Biggest changes since your report of {{shortDate .Since}} ({{plural .Days "day"}} ago) · tokens per message</div>
  {{if .Grew}}<div class="tp">grew</div><ul class="tmov">{{range .Grew}}<li>{{.Title}} <span class="tp">{{signed .DeltaTokensPerTurn}} · {{.Status}}</span></li>{{end}}</ul>{{end}}
  {{if .Shrank}}<div class="tp" style="margin-top:8px">shrank</div><ul class="tmov">{{range .Shrank}}<li>{{.Title}} <span class="tp">{{signed .DeltaTokensPerTurn}} · {{.Status}}</span></li>{{end}}</ul>{{end}}
</div>
{{end}}`))

// learnTrendsHTML renders the report's Trends section; empty when absent.
func learnTrendsHTML(t *LearnTrends) template.HTML {
	if t == nil || len(t.Metrics) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := trendsTemplate.Execute(&buf, t); err != nil {
		return ""
	}
	return template.HTML(buf.String()) // produced by html/template, already escaped
}
