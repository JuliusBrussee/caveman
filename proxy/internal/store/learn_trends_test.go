package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func trendSessionsAt(start time.Time, count int, tokens int64) []trendSession {
	out := make([]trendSession, count)
	for i := range out {
		out[i] = trendSession{Source: "claude", Start: start.Add(time.Duration(i) * time.Hour), Turns: 10, Tokens: tokens, PeakPct: 20, FirstTurn: 3000, CacheRead: 90, CacheContext: 100}
	}
	return out
}

func trendMetric(t *testing.T, trends *LearnTrends, key string) LearnTrendMetric {
	t.Helper()
	for _, m := range trends.Metrics {
		if m.Key == key {
			return m
		}
	}
	t.Fatalf("metric %s missing", key)
	return LearnTrendMetric{}
}

func TestISOWeekBoundariesAreUTC(t *testing.T) {
	cases := []struct {
		in    time.Time
		week  string
		start string
	}{
		{time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), "2026-W01", "2025-12-29"},
		{time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC), "2026-W53", "2026-12-28"},
		{time.Date(2026, 9, 27, 23, 59, 59, 0, time.UTC), "2026-W39", "2026-09-21"},
		{time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC), "2026-W40", "2026-09-28"},
		// Monday 00:30 at UTC+14 is still Sunday in UTC: previous week.
		{time.Date(2026, 9, 28, 0, 30, 0, 0, time.FixedZone("LINT", 14*3600)), "2026-W39", "2026-09-21"},
	}
	for _, c := range cases {
		start := isoWeekStart(c.in)
		if got := isoWeekLabel(start); got != c.week || start.Format("2006-01-02") != c.start {
			t.Errorf("%v: got %s %s, want %s %s", c.in, got, start.Format("2006-01-02"), c.week, c.start)
		}
	}
}

func TestTrendDirectionDeadBand(t *testing.T) {
	cases := []struct {
		cur, prior float64
		better     string
		band       float64
		want       string
	}{
		{105, 100, "lower", 0, "flat"},
		{89, 100, "lower", 0, "improved"},
		{115, 100, "lower", 0, "worse"},
		{0.5, 0.2, "lower", 1, "flat"}, // +150% but under the absolute band
		{0, 0, "lower", 0, "flat"},
		{5, 0, "lower", 0, "worse"},
		{80, 95, "higher", 1, "worse"},
		{97, 80, "higher", 1, "improved"},
	}
	for _, c := range cases {
		delta, got := trendDirection(c.cur, c.prior, c.better, c.band)
		if got != c.want {
			t.Errorf("trendDirection(%v,%v,%s,%v)=%s want %s", c.cur, c.prior, c.better, c.band, got, c.want)
		}
		if c.prior == 0 && delta != nil {
			t.Errorf("prior 0 must not produce a relative delta")
		}
	}
}

func TestBuildLearnTrendsBucketsAndCompares(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) // Wednesday, 2026-W40
	since := now.Add(-30 * 24 * time.Hour)               // 2026-08-31 12:00, Monday of W36
	var sessions []trendSession
	for _, monday := range []string{"2026-08-31", "2026-09-07", "2026-09-14"} {
		d, _ := time.Parse("2006-01-02", monday)
		sessions = append(sessions, trendSessionsAt(d.Add(13*time.Hour), 5, 1000)...)
	}
	sessions = append(sessions, trendSessionsAt(time.Date(2026, 9, 21, 13, 0, 0, 0, time.UTC), 5, 800)...)
	// The week in progress has enough sessions and a wild value; it is plotted
	// but must never become the compared week.
	sessions = append(sessions, trendSessionsAt(time.Date(2026, 9, 28, 1, 0, 0, 0, time.UTC), 6, 5000)...)
	sessions = append(sessions, trendSession{Source: "codex", Turns: 3, Tokens: 10}) // undated

	trends := buildLearnTrends(sessions, since, now)
	if trends == nil {
		t.Fatal("expected trends")
	}
	if len(trends.Weeks) != 5 || trends.Weeks[0].Week != "2026-W36" || trends.CurrentWeek != "2026-W39" {
		t.Fatalf("weeks = %+v current %s", trends.Weeks, trends.CurrentWeek)
	}
	if !trends.Weeks[0].Partial || trends.Weeks[1].Partial || !trends.Weeks[4].Partial ||
		trends.Weeks[0].InProgress || trends.Weeks[3].InProgress || !trends.Weeks[4].InProgress {
		t.Fatalf("partial/in-progress flags wrong: %+v", trends.Weeks)
	}
	if trends.UndatedSessions != 1 || trends.PriorWeeks != 3 {
		t.Fatalf("undated=%d prior=%d", trends.UndatedSessions, trends.PriorWeeks)
	}
	m := trendMetric(t, trends, "tokens_per_session")
	if *m.Current != 800 || *m.Prior != 1000 || *m.DeltaPct != -20 || m.Direction != "improved" || m.CurrentSessions != 5 || m.PriorSessions != 15 {
		t.Fatalf("tokens metric = %+v", m)
	}
	if last := m.Series[len(m.Series)-1]; last == nil || *last != 5000 {
		t.Fatalf("in-progress week must still be plotted: %+v", m.Series)
	}
	// A window inside the week in progress holds no complete week.
	if buildLearnTrends(sessions, time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC), now) != nil {
		t.Fatal("no complete week must omit trends")
	}
	if c := trendMetric(t, trends, "cache_read_pct"); *c.Current != 90 || c.Direction != "flat" || c.Better != "higher" {
		t.Fatalf("cache metric = %+v", c)
	}
	// Tool errors are zero everywhere: measured zero, flat, never omitted.
	if e := trendMetric(t, trends, "tool_errors_per_100_turns"); e.Direction != "flat" {
		t.Fatalf("errors metric = %+v", e)
	}
}

func TestBuildLearnTrendsInsufficientData(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	since := now.Add(-30 * 24 * time.Hour)
	w39 := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	sessions := append(trendSessionsAt(w39, 6, 1000), trendSessionsAt(time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC), 3, 50)...)
	sessions = append(sessions, trendSessionsAt(time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC), 2, 1000)...)

	trends := buildLearnTrends(sessions, since, now)
	// The last complete week is compared, never the one in progress.
	if trends.CurrentWeek != "2026-W39" {
		t.Fatalf("current week %s", trends.CurrentWeek)
	}
	m := trendMetric(t, trends, "tokens_per_session")
	last := len(m.Series) - 1
	if m.Series[last] != nil || !trends.Weeks[last].InsufficientData || trends.Weeks[last].Sessions != 3 {
		t.Fatalf("under-min bucket must be null + flagged: %+v %+v", m.Series, trends.Weeks[last])
	}
	// Prior weeks pool only 2 sessions: no comparison is stated.
	if m.Direction != "insufficient_data" || m.Prior != nil || m.DeltaPct != nil || m.PriorSessions != 2 {
		t.Fatalf("metric = %+v", m)
	}
	if buildLearnTrends([]trendSession{{Turns: 1}}, since, now) != nil {
		t.Fatal("undated-only input must omit trends")
	}
}

func TestSessionConsumerRecordsTrendSession(t *testing.T) {
	beh := behaviorScan{}
	c := newSessionEventConsumer("claude", sessionRef{relPath: "a.jsonl", repo: "/r"}, nil, &beh, newRecurringMiner())
	start := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	c.consume(turnEvent{sessionStart: true})
	c.consume(turnEvent{Timestamp: start, ContextTotal: 150_000, ContextUsagePresent: true, CacheReadInputTokens: 100_000, CacheUsagePresent: true, UsageMessageID: "m1", ProviderKey: "anthropic", Model: "unknown-model"})
	c.consume(turnEvent{Timestamp: start.Add(time.Minute), ContextTotal: 50_000, ContextUsagePresent: true, UsageMessageID: "m2", ToolCalls: []turnToolCall{{Name: "Bash", IsError: true}}, TaskSpawns: 1})
	c.finish()
	if len(beh.TrendSessions) != 1 {
		t.Fatalf("trend sessions = %+v", beh.TrendSessions)
	}
	s := beh.TrendSessions[0]
	if !s.Start.Equal(start) || s.Turns != 2 || s.Tokens != 200_000 || s.Dumbzone != 1 || s.FirstTurn != 150_000 ||
		s.ToolErrors != 1 || s.TaskSpawns != 1 || s.CacheRead != 100_000 || s.CacheContext != 150_000 || s.PeakPct != 75 {
		t.Fatalf("trend session = %+v", s)
	}
}

func writeTrendSnapshot(t *testing.T, home string, at time.Time, score int, sinks []Sink) {
	t.Helper()
	raw, _ := json.Marshal(LearnSnapshot{LearnPlan: LearnPlan{CaveScore: CaveScore{Score: score}, Sinks: sinks}, GeneratedAt: at.UTC().Format(time.RFC3339)})
	dir := filepath.Join(home, "reports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "caveman-learn."+at.UTC().Format("2006-01-02")+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAttachLearnTrendHistoryMoversAndScore(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	writeTrendSnapshot(t, home, now.Add(-10*24*time.Hour), 70, []Sink{
		{SinkID: "a", Title: "A", TokensPerTurn: 100}, {SinkID: "b", Title: "B", TokensPerTurn: 500}, {SinkID: "gone", Title: "G", TokensPerTurn: 40},
	})
	writeTrendSnapshot(t, home, now.Add(-2*24*time.Hour), 75, []Sink{{SinkID: "a", TokensPerTurn: 9999}})
	writeTrendSnapshot(t, home, now.Add(-2*time.Hour), 1, nil) // same day: superseded by this run

	plan := LearnPlan{
		CaveScore: CaveScore{Score: 80},
		Sinks:     []Sink{{SinkID: "a", Title: "A", TokensPerTurn: 300}, {SinkID: "b", Title: "B", TokensPerTurn: 200}, {SinkID: "new", Title: "N", TokensPerTurn: 50}},
		Trends:    &LearnTrends{Score: &LearnTrendScore{}},
	}
	AttachLearnTrendHistory(&plan, home, now)
	h := plan.Trends.Score.History
	if plan.Trends.Score.HistorySource != "snapshots" || len(h) != 3 || h[0].Score != 70 || h[2].Score != 80 {
		t.Fatalf("history = %+v", plan.Trends.Score)
	}
	mv := plan.Trends.Movers
	if mv == nil || mv.Days != 10 || mv.Since != "2026-09-20" {
		t.Fatalf("movers = %+v", mv)
	}
	if len(mv.Grew) != 2 || mv.Grew[0].SinkID != "a" || mv.Grew[0].DeltaTokensPerTurn != 200 || mv.Grew[1].Status != "new" {
		t.Fatalf("grew = %+v", mv.Grew)
	}
	if len(mv.Shrank) != 2 || mv.Shrank[0].SinkID != "b" || mv.Shrank[0].DeltaTokensPerTurn != -300 || mv.Shrank[1].Status != "gone" {
		t.Fatalf("shrank = %+v", mv.Shrank)
	}

	empty := LearnPlan{Trends: &LearnTrends{Score: &LearnTrendScore{}}}
	AttachLearnTrendHistory(&empty, t.TempDir(), now)
	if empty.Trends.Movers != nil || empty.Trends.Score.History != nil {
		t.Fatal("no snapshots must attach nothing")
	}
}

func TestLearnReportRendersTrendsSection(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	var sessions []trendSession
	for i, monday := range []string{"2026-09-07", "2026-09-14", "2026-09-21", "2026-09-28"} {
		d, _ := time.Parse("2006-01-02", monday)
		sessions = append(sessions, trendSessionsAt(d.Add(time.Hour), 6, int64(1000+i*100))...)
	}
	sessions = append(sessions, trendSessionsAt(time.Date(2026, 8, 31, 1, 0, 0, 0, time.UTC), 2, 1000)...)
	plan := LearnPlan{Schema: learnSchema, Basis: learnBasis, Sinks: []Sink{}, Trends: buildLearnTrends(sessions, now.Add(-30*24*time.Hour), now)}
	out := filepath.Join(t.TempDir(), "learn.html")
	if err := (&Store{}).WriteLearnHTML(plan, out); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(out)
	html := string(raw)
	trendsAt, sinksAt := strings.Index(html, "<h2>Trends</h2>"), strings.Index(html, "<h2>Where your tokens go</h2>")
	if trendsAt < 0 || trendsAt > sinksAt {
		t.Fatal("Trends section missing or not before the findings")
	}
	section := html[trendsAt:sinksAt]
	for _, want := range []string{
		"A trend is not a saving", "tokens per session", "1,200", "before: 1,000 · &#43;20% · 6 sessions",
		"Aug 31* · 2 sessions", "Sep 28 (still running) · 6 sessions", "(still running) · 1,300", "not enough data</title>", "<polyline", "worse",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("trends section missing %q", want)
		}
	}
	if strings.Contains(section, "$") || strings.Contains(section, "USD") {
		t.Error("trends section must carry no currency")
	}
	if learnTrendsHTML(nil) != "" {
		t.Error("nil trends must render nothing")
	}
}
