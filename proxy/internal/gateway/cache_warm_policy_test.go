package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// returnsAt is a table whose every class comes back exactly at `at` (in that
// 30 s bin); a negative `at` never comes back.
func returnsAt(at time.Duration) *warmTable {
	p := make([]float64, 241)
	if at < 0 {
		p[240] = 1
	} else {
		p[int(at/(30*time.Second))] = 1
	}
	return &warmTable{bin: 30 * time.Second, bins: 240, counts: map[string][]float64{},
		prior: map[string]warmClass{"all": {N: 1, Realized: 1, P: p}}}
}

func TestCacheWarmPlanCountsTheWholeChain(t *testing.T) {
	table := returnsAt(20 * time.Minute)
	// Coming back at 20 min takes 4 warms (270, 540, 810, 1080 s; the last keeps
	// the cache to 23 min). Worth 1.00: four warms at 0.20 pay, at 0.30 they do
	// not, although any single one of them would.
	plan := table.plan("main/end/5m", 5*time.Minute, cacheWarmHorizon, 1.0, 0.20)
	if !plan[1] || !plan[4] || plan[5] {
		t.Fatalf("plan at 0.20 = %v", plan[:7])
	}
	plan = table.plan("main/end/5m", 5*time.Minute, cacheWarmHorizon, 1.0, 0.30)
	if plan[1] {
		t.Fatalf("a chain costing 1.20 to save 1.00 was started: %v", plan[:7])
	}
	if table.plan("unknown/class/5m", 5*time.Minute, cacheWarmHorizon, 1, 0.1) == nil {
		t.Fatal("an unseen class did not fall back to all")
	}
	if (&warmTable{bin: 30 * time.Second, bins: 240}).plan("main/end/5m", 5*time.Minute, cacheWarmHorizon, 1, 0.1) != nil {
		t.Fatal("planned without any table")
	}
}

func TestCacheWarmShippedTable(t *testing.T) {
	table := newWarmTable("")
	for _, class := range []string{"main/end/5m", "main/tool/5m", "main/end/1h", "subagent/tool/5m", "subagent/end/5m"} {
		p, realized, ok := table.probs(class)
		sum := 0.0
		for _, x := range p {
			sum += x
		}
		if !ok || sum < 0.99 || sum > 1.01 || realized <= 0 || realized > 1 {
			t.Fatalf("%s: ok=%v sum=%v realized=%v", class, ok, sum, realized)
		}
	}
	var doc struct {
		Provenance map[string]any `json:"provenance"`
	}
	if err := json.Unmarshal(cacheWarmTableJSON, &doc); err != nil || doc.Provenance["transitions"] == nil || doc.Provenance["computed"] == nil {
		t.Fatalf("table carries no provenance: %v %v", err, doc.Provenance)
	}
}

func TestCacheWarmLearnsAndPersistsGaps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gaps.json")
	table := newWarmTable(path)
	before, _, _ := table.probs("main/end/5m")
	for i := 0; i < 20; i++ {
		table.observe("main/end/5m", 10*time.Minute)
	}
	after, _, _ := table.probs("main/end/5m")
	bin := 20 // 10 min / 30 s
	if !(after[bin] > before[bin]) {
		t.Fatalf("observing did not move the class toward the user's gaps: %v -> %v", before[bin], after[bin])
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("gaps not persisted 0600: %v %v", err, info)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "sess") {
		t.Fatal("persisted more than counts")
	}
	reloaded := newWarmTable(path)
	again, _, _ := reloaded.probs("main/end/5m")
	if again[bin] != after[bin] {
		t.Fatalf("reload lost the counts: %v vs %v", again[bin], after[bin])
	}
}

func TestCacheWarmToolStopAcrossChunks(t *testing.T) {
	var s toolStop
	_, _ = s.Write([]byte(`data: {"delta":{"stop_rea`))
	_, _ = s.Write([]byte(`son":"tool_use"}}`))
	if !s.seen {
		t.Fatal("tool_use split across reads not seen")
	}
	var e toolStop
	_, _ = e.Write([]byte(`{"stop_reason":"end_turn"}`))
	if e.seen {
		t.Fatal("end_turn read as a tool call")
	}
}

func TestCacheWarmClassComesFromTheAnswer(t *testing.T) {
	// Tool turns come back at 20 min, finished turns never: only a tool turn warms.
	table := returnsAt(20 * time.Minute)
	never := make([]float64, 241)
	never[240] = 1
	table.prior["main/end"] = warmClass{N: 1, Realized: 1, P: never}
	for name, tc := range map[string]struct {
		stop  string
		warms bool
	}{"tool": {"tool_use", true}, "end": {"end_turn", false}} {
		t.Run(name, func(t *testing.T) {
			rt := &warmTransport{realResp: strings.Replace(messageResp(warmModel, usageJSON(0, 200_000, "5m")), "end_turn", tc.stop, 1)}
			f := newWarmFixture(t, rt, anthropicAPI, nil)
			f.srv.warmer.table = table
			f.serve(t, reqBody(""), warmHeaders)
			f.clock.advance(270 * time.Second)
			if (rt.count() == 2) != tc.warms {
				t.Fatalf("requests = %d", rt.count())
			}
		})
	}
}

func TestCacheWarmHeadersAndBytesAreTheAnsweredRequests(t *testing.T) {
	// The planner adds a frontier marker; the upstream refuses it, the original
	// bytes are retried and answered: the warm replays those, with the retry's
	// headers.
	body := `{"model":"` + warmModel + `","max_tokens":4096,` +
		`"tools":[{"name":"read","input_schema":{},"cache_control":{"type":"ephemeral"}}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`
	for name, statuses := range map[string][]int{"planned": nil, "fail-open": {http.StatusBadRequest}} {
		t.Run(name, func(t *testing.T) {
			rt := &warmTransport{realStatuses: statuses}
			f := newWarmFixture(t, rt, anthropicAPI, nil)
			f.srv.breakpointPlan = breakpointPlanModeFrontier
			f.serve(t, body, warmHeaders)
			real := rt.count() - 1
			if name == "fail-open" && real != 1 {
				t.Fatalf("no fail-open retry: %d", rt.count())
			}
			if name == "planned" && rt.body(0) == body {
				t.Fatal("the planner did not change the request; the test proves nothing")
			}
			f.clock.advance(270 * time.Second)
			if rt.count() != real+2 {
				t.Fatalf("requests = %d", rt.count())
			}
			want := strings.Replace(rt.body(real), `"max_tokens":4096`, `"max_tokens":0`, 1)
			if rt.body(real+1) != want {
				t.Fatalf("warm bytes:\n got %s\nwant %s", rt.body(real+1), want)
			}
			sent, warm := rt.headers[real].Clone(), rt.headers[real+1].Clone()
			sent.Del("Accept-Encoding")
			warm.Del("Accept-Encoding")
			if len(sent) != len(warm) {
				t.Fatalf("header sets differ:\n sent %v\n warm %v", sent, warm)
			}
			for k, v := range sent {
				if strings.Join(warm[k], "\x00") != strings.Join(v, "\x00") {
					t.Fatalf("header %s: sent %q, warm %q", k, v, warm[k])
				}
			}
		})
	}
}

func TestCacheWarmReplaysTheRoutedRequest(t *testing.T) {
	// The route stage set the effort: the warm replays the bytes that carried it,
	// with the headers they went with, so it reads the entry real traffic wrote.
	rt := &warmTransport{}
	f := newWarmFixture(t, rt, anthropicAPI, nil)
	f.srv.cloud = &fakeCloud{answer: RouteAnswer{Outcome: "routed", Effort: "low"}}
	f.serve(t, auto(t, reqBody("")), warmHeaders)
	if !strings.Contains(rt.body(0), `"effort":"low"`) || strings.Contains(rt.body(0), AutoModel) {
		t.Fatalf("the route stage did not change the request; the test proves nothing: %s", rt.body(0))
	}
	f.clock.advance(270 * time.Second)
	if rt.count() != 2 || rt.body(1) != strings.Replace(rt.body(0), `"max_tokens":4096`, `"max_tokens":0`, 1) {
		t.Fatalf("warm = %d %s", rt.count(), rt.body(1))
	}
	sent, warm := rt.headers[0].Clone(), rt.headers[1].Clone()
	sent.Del("Accept-Encoding")
	warm.Del("Accept-Encoding")
	for k, v := range sent {
		if strings.Join(warm[k], "\x00") != strings.Join(v, "\x00") {
			t.Fatalf("header %s: sent %q, warm %q", k, v, warm[k])
		}
	}
	if len(sent) != len(warm) {
		t.Fatalf("header sets differ: %v / %v", sent, warm)
	}
}

func TestCacheWarmParentRequestStopsSubagentWarms(t *testing.T) {
	rt := &warmTransport{}
	f := newWarmFixture(t, rt, anthropicAPI, nil)
	child := map[string]string{"x-claude-code-agent-id": "agent-7"}
	for k, v := range warmHeaders {
		child[k] = v
	}
	f.serve(t, reqBody(""), child)
	f.clock.advance(100 * time.Second)
	f.serve(t, reqBody(""), warmHeaders) // the parent talks again
	f.clock.advance(170 * time.Second)   // the child's warm was due now
	if rt.count() != 2 {
		t.Fatalf("a finished subagent was warmed after its parent moved on: %d", rt.count())
	}
	f.clock.advance(100 * time.Second) // the parent's own warm
	if rt.count() != 3 {
		t.Fatalf("the parent was not warmed: %d", rt.count())
	}
}

func TestCacheWarmModelSwitchStopsTheOldModel(t *testing.T) {
	rt := &warmTransport{}
	f := newWarmFixture(t, rt, anthropicAPI, nil)
	f.serve(t, reqBody(""), warmHeaders)
	f.clock.advance(100 * time.Second)
	// The same session on another (unpriced, so never warmed) model.
	f.serve(t, strings.Replace(reqBody(""), warmModel, "claude-unpriced-9", 1), warmHeaders)
	f.clock.advance(time.Hour)
	if rt.count() != 2 {
		t.Fatalf("the old model kept warming after the switch: %d", rt.count())
	}
}

func TestCacheWarmSubscription429PausesTheLoginForTheDay(t *testing.T) {
	sub := map[string]string{"authorization": "Bearer sk-ant-oat01-login", "anthropic-version": "2023-06-01", "x-cave-session": "s1"}
	rt := &warmTransport{warmStatus: http.StatusTooManyRequests, warmResp: `{"type":"error","error":{"type":"rate_limit_error","message":"no"}}`}
	f := newWarmFixture(t, rt, anthropicAPI, nil)
	f.clock.now = time.Date(2026, 10, 8, 9, 0, 0, 0, time.Local)
	f.serve(t, reqBody(""), sub)
	f.clock.advance(270 * time.Second)
	if rt.count() != 2 {
		t.Fatalf("requests = %d", rt.count())
	}
	if rows := f.warmRows(); len(rows) != 1 || rows[0].AuthMode != string(AuthModeSubscription) {
		t.Fatalf("warm rows = %+v", rows)
	}
	// Another session on the same login, hours later the same day: no warm.
	sub["x-cave-session"] = "s2"
	rt.warmStatus = 0
	f.clock.advance(5 * time.Hour)
	f.serve(t, reqBody(""), sub)
	f.clock.advance(time.Hour)
	if rt.count() != 3 {
		t.Fatalf("warmed a rate-limited login the same day: %d", rt.count())
	}
	// The next day it warms again.
	f.clock.advance(12 * time.Hour)
	f.serve(t, reqBody(""), sub)
	f.clock.advance(270 * time.Second)
	if rt.count() != 5 {
		t.Fatalf("pause outlived the day: %d", rt.count())
	}
}

func TestCacheWarmRealRequest429BacksOff(t *testing.T) {
	rt := &warmTransport{realStatuses: []int{http.StatusTooManyRequests}}
	f := newWarmFixture(t, rt, anthropicAPI, nil)
	limited := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqBody("")))
	for k, v := range warmHeaders {
		limited.Header.Set(k, v)
	}
	f.srv.Handler().ServeHTTP(httptest.NewRecorder(), limited) // 429: one-minute backoff on this key
	f.serve(t, reqBody(""), warmHeaders)
	f.clock.advance(270 * time.Second)
	if rt.count() != 3 {
		t.Fatalf("backoff outlasted Retry-After's default or blocked the warm: %d", rt.count())
	}
	if until := backoffUntil(time.Unix(0, 0), http.Header{"Retry-After": {"600"}}, AuthModePAYG); until != time.Unix(600, 0) {
		t.Fatalf("Retry-After ignored: %v", until)
	}
	w, c := newUnitWarmer(func(context.Context, *warmRequest) bool { t.Fatal("sent during a backoff"); return false })
	w.backoff("k", c.Now().Add(10*time.Minute))
	w.arm("s", "", "main/end/5m", &warmRequest{ttl: 5 * time.Minute, cred: "k", plan: []bool{false, true, true}}, c.Now())
	c.advance(time.Hour)
}

func TestCacheWarmBeginNeverBlocks(t *testing.T) {
	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	w, c := newUnitWarmer(func(context.Context, *warmRequest) bool {
		defer wg.Done()
		<-release
		return true
	})
	w.arm("s", "", "main/end/5m", &warmRequest{ttl: 5 * time.Minute, plan: []bool{false, true, true}}, c.Now())
	go c.advance(270 * time.Second)
	time.Sleep(10 * time.Millisecond)
	done := make(chan struct{})
	go func() { w.begin("s")(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a real request waited on an in-flight warm")
	}
	close(release)
	wg.Wait()
	if e := w.entries["s"]; e.timer != nil {
		t.Fatal("a warm that finished after a real request rescheduled itself")
	}
}

func TestCacheWarmWaitsForInFlightRealRequest(t *testing.T) {
	sent := 0
	w, c := newUnitWarmer(func(context.Context, *warmRequest) bool { sent++; return true })
	w.arm("s", "", "main/end/5m", &warmRequest{ttl: 5 * time.Minute, plan: []bool{false, true, true, true}}, c.Now())
	w.mu.Lock()
	w.real["s"]++ // a real request of the stream is in flight
	w.mu.Unlock()
	c.advance(270 * time.Second)
	if sent != 0 {
		t.Fatal("warm sent while a real request of the stream was in flight")
	}
	w.mu.Lock()
	w.real["s"]--
	w.mu.Unlock()
	c.advance(cacheWarmBusyRetry)
	if sent != 1 {
		t.Fatalf("warm not retried after the real request ended: %d", sent)
	}
}

func TestCacheWarmWallClockDeadline(t *testing.T) {
	if strings.Contains(systemClock{}.Now().String(), "m=") {
		t.Fatal("system clock keeps the monotonic reading, which stops during sleep")
	}
	sent := 0
	w, c := newUnitWarmer(func(context.Context, *warmRequest) bool { sent++; return true })
	w.arm("s", "", "main/end/5m", &warmRequest{ttl: 5 * time.Minute, plan: []bool{false, true}}, c.Now())
	// The laptop slept: the timer fires an hour late, the entry is long gone.
	c.mu.Lock()
	c.now = c.now.Add(time.Hour)
	c.mu.Unlock()
	c.advance(0)
	if sent != 0 {
		t.Fatal("warm sent after its deadline passed in wall time")
	}
}

func TestCacheWarmBounds(t *testing.T) {
	w, c := newUnitWarmer(func(context.Context, *warmRequest) bool { return true })
	for i := 0; i < cacheWarmMaxEntries+10; i++ {
		w.arm("s"+itoa(int64(i)), "", "main/end/5m", &warmRequest{ttl: 5 * time.Minute, plan: []bool{false, true}}, c.Now())
	}
	if len(w.entries) != cacheWarmMaxEntries || w.lru.Len() != cacheWarmMaxEntries {
		t.Fatalf("entries = %d / %d", len(w.entries), w.lru.Len())
	}
	// Bytes: five 64 MiB bodies do not fit under 256 MiB with the rest.
	w, c = newUnitWarmer(func(context.Context, *warmRequest) bool { return true })
	big := make([]byte, 64<<20)
	for i := 0; i < 5; i++ {
		w.arm("b"+itoa(int64(i)), "", "main/end/5m", &warmRequest{ttl: 5 * time.Minute, body: big, plan: []bool{false, true}}, c.Now())
	}
	if w.bytes > cacheWarmMaxBytes || len(w.entries) != 4 {
		t.Fatalf("held %d bytes in %d entries", w.bytes, len(w.entries))
	}
	if _, ok := w.entries["b0"]; ok {
		t.Fatal("oldest body not evicted first")
	}
}

func TestCacheWarmCostsExample(t *testing.T) {
	// 150k-token Sonnet 5.5 prefix, 5m, 10 uncached tokens.
	usage := providers.UsageObservation{InputTokens: 150_010, OutputTokens: 5, CachedInputTokens: 150_000,
		InputTokensReported: true, OutputTokensReported: true}
	full, warm, ok := cacheWarmCosts(usage, providers.RequestMetadata{Provider: "anthropic", Model: "claude-sonnet-5-5"}, AuthModeSubscription, 5*time.Minute)
	// (2.50 - 0.10) * 0.15 = 0.36; 0.10 * 0.15 + 2.00 * 0.00001 = 0.01502.
	if !ok || full < 0.35999 || full > 0.36001 || warm < 0.01501 || warm > 0.01503 {
		t.Fatalf("full=%v warm=%v ok=%v", full, warm, ok)
	}
	if _, _, ok := cacheWarmCosts(usage, providers.RequestMetadata{Provider: "anthropic", Model: "claude-unpriced-9"}, AuthModePAYG, 5*time.Minute); ok {
		t.Fatal("an unpriced model was costed")
	}
}

func TestCacheWarmDelay(t *testing.T) {
	for ttl, want := range map[time.Duration]time.Duration{
		5 * time.Minute:  270 * time.Second,
		time.Hour:        54 * time.Minute,
		60 * time.Second: 50 * time.Second,
		10 * time.Second: 0,
	} {
		if got := cacheWarmDelay(ttl); got != want {
			t.Fatalf("delay(%v) = %v, want %v", ttl, got, want)
		}
	}
}

func newUnitWarmer(send func(context.Context, *warmRequest) bool) (*cacheWarmer, *fakeClock) {
	w := newCacheWarmer(func() bool { return true }, send, returnsAt(30*time.Minute))
	c := &fakeClock{now: time.Now().Round(0)}
	w.clock = c
	return w, c
}
