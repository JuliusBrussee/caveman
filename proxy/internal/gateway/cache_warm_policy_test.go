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

// A subagent can still be running when its parent talks: the parent's request
// stops the parent's own warms, never its children's (on the measured
// sessions stopping them lost more misses than the warms it spared).
func TestCacheWarmParentRequestLeavesSubagentWarms(t *testing.T) {
	rt := &warmTransport{}
	f := newWarmFixture(t, rt, anthropicAPI, nil)
	child := map[string]string{"x-claude-code-agent-id": "agent-7"}
	for k, v := range warmHeaders {
		child[k] = v
	}
	f.serve(t, reqBody(""), child)
	f.clock.advance(100 * time.Second)
	f.serve(t, reqBody(""), warmHeaders) // the parent talks again
	f.clock.advance(170 * time.Second)   // the child's warm is due
	if rows := f.warmRows(); rt.count() != 3 || len(rows) != 1 {
		t.Fatalf("the subagent's warm did not survive its parent's request: %d requests, %d warms", rt.count(), len(rows))
	}
	f.clock.advance(100 * time.Second) // the parent's own warm
	if rt.count() != 4 {
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

func TestCacheWarmSubscription429PausesTheLogin(t *testing.T) {
	sub := map[string]string{"authorization": "Bearer sk-ant-oat01-login", "anthropic-version": "2023-06-01", "x-cave-session": "s1"}
	rt := &warmTransport{warmStatus: http.StatusTooManyRequests, warmResp: `{"type":"error","error":{"type":"rate_limit_error","message":"no"}}`}
	f := newWarmFixture(t, rt, anthropicAPI, nil)
	f.clock.now = time.Date(2026, 10, 8, 23, 30, 0, 0, time.Local)
	f.serve(t, reqBody(""), sub)
	f.clock.advance(270 * time.Second)
	if rt.count() != 2 {
		t.Fatalf("requests = %d", rt.count())
	}
	if rows := f.warmRows(); len(rows) != 1 || rows[0].AuthMode != string(AuthModeSubscription) {
		t.Fatalf("warm rows = %+v", rows)
	}
	// The 429 named no reset: another session on the login is not warmed for
	// an hour, midnight or not.
	sub["x-cave-session"] = "s2"
	rt.warmStatus = 0
	f.clock.advance(20 * time.Minute)
	f.serve(t, reqBody(""), sub)
	f.clock.advance(30 * time.Minute)
	if rt.count() != 3 {
		t.Fatalf("warmed a rate-limited login within the hour: %d", rt.count())
	}
	f.clock.advance(15 * time.Minute)
	f.serve(t, reqBody(""), sub)
	f.clock.advance(270 * time.Second)
	if rt.count() != 5 {
		t.Fatalf("pause outlived the hour: %d", rt.count())
	}
	// The response's own reset wins: epoch seconds or RFC 3339, the later of it
	// and Retry-After; one in the past or absurdly far is not believed.
	now := time.Unix(1_800_000_000, 0)
	for name, tc := range map[string]struct {
		h    http.Header
		want time.Duration
	}{
		"unified reset":     {http.Header{"Anthropic-Ratelimit-Unified-Reset": {"1800010800"}}, 3 * time.Hour},
		"rfc 3339":          {http.Header{"Anthropic-Ratelimit-Unified-Reset": {now.Add(2 * time.Hour).UTC().Format(time.RFC3339)}}, 2 * time.Hour},
		"retry-after":       {http.Header{"Retry-After": {"120"}}, 2 * time.Minute},
		"the later of both": {http.Header{"Retry-After": {"120"}, "Anthropic-Ratelimit-Unified-Reset": {"1800010800"}}, 3 * time.Hour},
		"reset in the past": {http.Header{"Anthropic-Ratelimit-Unified-Reset": {"1700000000"}}, time.Hour},
		"reset next year":   {http.Header{"Anthropic-Ratelimit-Unified-Reset": {"1900000000"}}, time.Hour},
		"nothing":           {http.Header{}, time.Hour},
		"garbage":           {http.Header{"Anthropic-Ratelimit-Unified-Reset": {"soon"}, "Retry-After": {"-4"}}, time.Hour},
	} {
		if got := backoffUntil(now, tc.h, AuthModeSubscription).Sub(now); got != tc.want {
			t.Errorf("%s: paused %v, want %v", name, got, tc.want)
		}
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
	w.arm("s", "main/end/5m", &warmRequest{ttl: 5 * time.Minute, cred: "k", plan: []bool{false, true, true}}, c.Now())
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
	w.arm("s", "main/end/5m", &warmRequest{ttl: 5 * time.Minute, plan: []bool{false, true, true}}, c.Now())
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
	w.arm("s", "main/end/5m", &warmRequest{ttl: 5 * time.Minute, plan: []bool{false, true, true, true}}, c.Now())
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
	w.arm("s", "main/end/5m", &warmRequest{ttl: 5 * time.Minute, plan: []bool{false, true}}, c.Now())
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
		w.arm("s"+itoa(int64(i)), "main/end/5m", &warmRequest{ttl: 5 * time.Minute, plan: []bool{false, true}}, c.Now())
	}
	if len(w.entries) != cacheWarmMaxEntries || w.lru.Len() != cacheWarmMaxEntries {
		t.Fatalf("entries = %d / %d", len(w.entries), w.lru.Len())
	}
	// Bytes: five 64 MiB bodies do not fit under 256 MiB with the rest.
	w, c = newUnitWarmer(func(context.Context, *warmRequest) bool { return true })
	big := make([]byte, 64<<20)
	for i := 0; i < 5; i++ {
		w.arm("b"+itoa(int64(i)), "main/end/5m", &warmRequest{ttl: 5 * time.Minute, body: big, plan: []bool{false, true}}, c.Now())
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

// Auto on Anthropic: the warm belongs to the stream, replays the bytes that
// went upstream (the routed model, never the literal Auto id), is priced at
// that model, and the stream's next turn stops it even when Auto routes that
// turn to another model.
func TestCacheWarmFollowsAutoAcrossModels(t *testing.T) {
	rt := &warmTransport{}
	f := newWarmFixture(t, rt, anthropicAPI, nil)
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "routed", Model: "claude-haiku-5-5"}}
	f.srv.cloud = cloud
	body := auto(t, reqBody(""))
	model := func(i int) string {
		var doc struct {
			Model     string `json:"model"`
			MaxTokens *int   `json:"max_tokens"`
		}
		if err := json.Unmarshal([]byte(rt.body(i)), &doc); err != nil || doc.MaxTokens == nil {
			t.Fatalf("upstream body %d: %v %s", i, err, rt.body(i))
		}
		if strings.Contains(rt.body(i), AutoModel) {
			t.Fatalf("the literal Auto id went upstream in request %d: %s", i, rt.body(i))
		}
		return doc.Model + "/" + map[bool]string{true: "warm", false: "real"}[*doc.MaxTokens == 0]
	}
	f.serve(t, body, warmHeaders)
	f.clock.advance(270 * time.Second)
	if rt.count() != 2 || model(0) != "claude-haiku-5-5/real" || model(1) != "claude-haiku-5-5/warm" {
		t.Fatalf("turn 1: %d requests, %s then %s", rt.count(), model(0), model(1))
	}
	// Turn 2 at 370 s goes to another model: Haiku's second warm (540 s) is off.
	f.clock.advance(100 * time.Second)
	cloud.mu.Lock()
	cloud.answer = RouteAnswer{Outcome: "routed", Model: "claude-opus-5"}
	cloud.mu.Unlock()
	f.serve(t, body, warmHeaders)
	f.clock.advance(200 * time.Second) // 570 s
	if rt.count() != 3 || model(2) != "claude-opus-5/real" {
		t.Fatalf("the previous model's warm survived the next turn: %d requests, last %s", rt.count(), model(rt.count()-1))
	}
	f.clock.advance(70 * time.Second) // 640 s: 270 s after turn 2
	if rt.count() != 4 || model(3) != "claude-opus-5/warm" {
		t.Fatalf("turn 2 was not warmed on its own model: %d requests, last %s", rt.count(), model(rt.count()-1))
	}
	rows := f.warmRows()
	if len(rows) != 2 || rows[0].Model != "claude-haiku-5-5" || rows[1].Model != "claude-opus-5" {
		t.Fatalf("warm rows %+v", rows)
	}
	if !(rows[0].TotalCostUSD > 0 && rows[1].TotalCostUSD > rows[0].TotalCostUSD) {
		t.Fatalf("warms were not priced at the model sent: haiku %v, opus %v", rows[0].TotalCostUSD, rows[1].TotalCostUSD)
	}
	// The plan is priced at the model sent too: a routed model the catalog
	// does not price is never warmed, although the asked model is priced.
	cloud.mu.Lock()
	cloud.answer = RouteAnswer{Outcome: "routed", Model: "claude-not-in-the-catalog"}
	cloud.mu.Unlock()
	f.serve(t, body, warmHeaders)
	sent := rt.count()
	f.clock.advance(30 * time.Minute)
	if rt.count() != sent || model(sent-1) != "claude-not-in-the-catalog/real" {
		t.Fatalf("an unpriced routed model was warmed: %d -> %d, last real %s", sent, rt.count(), model(sent-1))
	}
}
