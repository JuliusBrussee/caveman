package gateway

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
)

// fakeClock fires timers only when the test advances it, each at its own due
// time, so warming is tested without a single real sleep.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
	c       *fakeClock
}

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := !t.stopped
	t.stopped = true
	return was
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) interface{ Stop() bool } {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), f: f, c: c}
	c.timers = append(c.timers, t)
	return t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var next *fakeTimer
		for _, t := range c.timers {
			if !t.stopped && !t.at.After(target) && (next == nil || t.at.Before(next.at)) {
				next = t
			}
		}
		if next == nil {
			c.now = target
			c.mu.Unlock()
			return
		}
		next.stopped = true
		c.now = next.at
		c.mu.Unlock()
		next.f()
	}
}

// warmTransport answers the real request with realResp and every warm with
// warmResp (status warmStatus), counting what reached the upstream.
type warmTransport struct {
	mu         sync.Mutex
	bodies     [][]byte
	headers    []http.Header
	realResp   string
	sse        bool
	warmResp   string
	warmStatus int
	block      chan struct{} // when set, warms wait on it (or their context)
}

func (t *warmTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	t.mu.Lock()
	t.bodies = append(t.bodies, body)
	t.headers = append(t.headers, r.Header.Clone())
	block := t.block
	t.mu.Unlock()
	isWarm := bytes.Contains(body, []byte(`"max_tokens":0`))
	status, resp, ct := http.StatusOK, t.realResp, "application/json"
	if isWarm {
		if block != nil {
			select {
			case <-block:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		resp = t.warmResp
		if t.warmStatus != 0 {
			status = t.warmStatus
		}
	} else if t.sse {
		ct = "text/event-stream"
	}
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Request: r,
		Header: http.Header{"Content-Type": {ct}}, Body: io.NopCloser(strings.NewReader(resp))}, nil
}

func (t *warmTransport) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.bodies)
}

func (t *warmTransport) body(i int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.bodies[i])
}

// usageJSON is an Anthropic usage object for a 200k-token cached prefix.
func usageJSON(read, write int, ttl string) string {
	creation := `"cache_creation":{"ephemeral_5m_input_tokens":` + strconv.Itoa(write) + `,"ephemeral_1h_input_tokens":0}`
	if ttl == "1h" {
		creation = `"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":` + strconv.Itoa(write) + `}`
	}
	return `{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":` + strconv.Itoa(read) +
		`,"cache_creation_input_tokens":` + strconv.Itoa(write) + `,` + creation + `}`
}

func messageResp(model, usage string) string {
	return `{"id":"msg","type":"message","model":"` + model + `","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":` + usage + `}`
}

const warmModel = "claude-sonnet-5" // catalog: input 2.00, read 0.20, write 2.50 / 1h 4.00 per M

func warmBody(extra string) string {
	return `{"model":"` + warmModel + `","max_tokens":4096,` + extra +
		`"system":[{"type":"text","text":"You are an agent.","cache_control":{"type":"ephemeral"}}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}]}`
}

var warmHeaders = map[string]string{
	"x-api-key":         "sk-ant-api-secret-key",
	"anthropic-version": "2023-06-01",
	"anthropic-beta":    "context-1m-2025-08-07",
	"x-cave-session":    "sess-warm-1",
}

type warmFixture struct {
	srv   *Server
	sink  *captureSink
	rt    *warmTransport
	clock *fakeClock
	on    *bool
}

func newWarmFixture(t *testing.T, rt *warmTransport, base string, logger *slog.Logger) *warmFixture {
	t.Helper()
	if rt.realResp == "" {
		rt.realResp = messageResp(warmModel, usageJSON(0, 200_000, "5m"))
	}
	if rt.warmResp == "" {
		rt.warmResp = `{"id":"msg","type":"message","model":"` + warmModel + `","content":[],"stop_reason":"max_tokens","usage":` + usageJSON(200_000, 0, "5m") + `}`
	}
	on := true
	sink := &captureSink{}
	srv := New(Config{
		Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "active"}},
		Adapters:   []providers.Adapter{anthropic.New(base)},
		Creds:      passthroughTestCreds{},
		Sink:       sink,
		HTTPClient: &http.Client{Transport: rt},
		Logger:     logger,
		CacheWarm:  func() bool { return on },
	})
	clock := &fakeClock{now: time.Now()}
	srv.warmer.clock = clock
	return &warmFixture{srv: srv, sink: sink, rt: rt, clock: clock, on: &on}
}

func (f *warmFixture) serve(t *testing.T, body string, headers map[string]string) {
	t.Helper()
	serveBody(t, f.srv, "/v1/messages", body, headers)
}

func (f *warmFixture) warmRows() []RequestRecord {
	f.sink.mu.Lock()
	defer f.sink.mu.Unlock()
	var out []RequestRecord
	for _, row := range f.sink.rows {
		if slices.Contains(row.OptimizationIDs, cacheWarmOptimizerID) {
			out = append(out, row)
		}
	}
	return out
}

const anthropicAPI = "https://api.anthropic.com"

func TestCacheWarmReplaysExactBytesAtZeroOutput(t *testing.T) {
	rt := &warmTransport{}
	f := newWarmFixture(t, rt, anthropicAPI, nil)
	body := warmBody(`"stream":false,"thinking":{"type":"adaptive"},`)
	f.serve(t, body, warmHeaders)

	f.clock.advance(269 * time.Second)
	if rt.count() != 1 {
		t.Fatalf("warm sent before 90%% of the 5m lifetime: %d requests", rt.count())
	}
	f.clock.advance(time.Second)
	if rt.count() != 2 {
		t.Fatalf("no warm at 270s: %d requests", rt.count())
	}
	want := strings.Replace(rt.body(0), `"max_tokens":4096`, `"max_tokens":0`, 1)
	if rt.body(1) != want {
		t.Fatalf("warm bytes differ beyond max_tokens:\n got %s\nwant %s", rt.body(1), want)
	}
	h := rt.headers[1]
	for _, name := range []string{"x-api-key", "anthropic-version", "anthropic-beta"} {
		if h.Get(name) != rt.headers[0].Get(name) || h.Get(name) == "" {
			t.Fatalf("warm header %s = %q, real sent %q", name, h.Get(name), rt.headers[0].Get(name))
		}
	}
	// The warm read the cache: the next one is due 270s after the warm started.
	f.clock.advance(269 * time.Second)
	if rt.count() != 2 {
		t.Fatalf("second warm early: %d", rt.count())
	}
	f.clock.advance(time.Second)
	if rt.count() != 3 {
		t.Fatalf("second warm missing: %d", rt.count())
	}
	rows := f.warmRows()
	if len(rows) != 2 {
		t.Fatalf("warm rows = %d, want 2", len(rows))
	}
	for _, row := range rows {
		if row.SavingsUSD != 0 || row.Basis != "inferred" || row.SessionID != "sess-warm-1" || row.CachedInputTokens != 200_000 {
			t.Fatalf("warm row = %+v", row)
		}
		if row.TotalCostUSD <= 0 {
			t.Fatalf("a PAYG warm is real spend and must be priced: %+v", row)
		}
	}
}

func TestCacheWarmStreamingRequestWarmsWithoutStream(t *testing.T) {
	sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"model\":\"" + warmModel +
		"\",\"content\":[],\"usage\":" + usageJSON(0, 200_000, "5m") + "}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	rt := &warmTransport{realResp: sse, sse: true}
	f := newWarmFixture(t, rt, anthropicAPI, nil)
	f.serve(t, warmBody(`"stream":true,`), warmHeaders)
	f.clock.advance(270 * time.Second)
	if rt.count() != 2 {
		t.Fatalf("no warm after a streamed request: %d", rt.count())
	}
	want := strings.Replace(strings.Replace(rt.body(0), `"max_tokens":4096`, `"max_tokens":0`, 1), `"stream":true`, `"stream":false`, 1)
	if rt.body(1) != want {
		t.Fatalf("warm bytes:\n got %s\nwant %s", rt.body(1), want)
	}
}

func TestCacheWarmSkipsRequestsZeroOutputCannotReplay(t *testing.T) {
	for name, extra := range map[string]string{
		"budget thinking":   `"thinking":{"type":"enabled","budget_tokens":2048},`,
		"forced tool":       `"tool_choice":{"type":"tool","name":"read"},`,
		"any tool":          `"tool_choice":{"type":"any"},`,
		"structured output": `"output_config":{"format":{"type":"json_schema","schema":{}}},`,
	} {
		t.Run(name, func(t *testing.T) {
			rt := &warmTransport{}
			f := newWarmFixture(t, rt, anthropicAPI, nil)
			f.serve(t, warmBody(extra), warmHeaders)
			f.clock.advance(2 * time.Hour)
			if rt.count() != 1 {
				t.Fatalf("warmed a request max_tokens 0 cannot replay: %d requests", rt.count())
			}
		})
	}
	// Effort alone is replayable: it stays in the bytes, so the key is the same.
	rt := &warmTransport{}
	f := newWarmFixture(t, rt, anthropicAPI, nil)
	f.serve(t, warmBody(`"output_config":{"effort":"high"},"tool_choice":{"type":"auto"},`), warmHeaders)
	f.clock.advance(270 * time.Second)
	if rt.count() != 2 || !strings.Contains(rt.body(1), `"output_config":{"effort":"high"}`) {
		t.Fatalf("effort request not warmed as sent: %d", rt.count())
	}
}

func TestCacheWarmOneHourLifetime(t *testing.T) {
	rt := &warmTransport{realResp: messageResp(warmModel, usageJSON(0, 200_000, "1h")),
		warmResp: messageResp(warmModel, usageJSON(200_000, 0, "1h"))}
	f := newWarmFixture(t, rt, anthropicAPI, nil)
	body := strings.ReplaceAll(warmBody(""), `{"type":"ephemeral"}`, `{"type":"ephemeral","ttl":"1h"}`)
	f.serve(t, body, warmHeaders)
	f.clock.advance(53 * time.Minute)
	if rt.count() != 1 {
		t.Fatalf("1h entry warmed on the 5m schedule: %d", rt.count())
	}
	f.clock.advance(time.Minute)
	if rt.count() != 2 {
		t.Fatalf("1h entry not warmed at 54m: %d", rt.count())
	}
	// The next would be due at 108m, past the 60-minute horizon.
	f.clock.advance(3 * time.Hour)
	if rt.count() != 2 {
		t.Fatalf("warmed past the horizon: %d", rt.count())
	}

	// All markers 1h but the response wrote a 5-minute entry: lifetime unknown.
	rt = &warmTransport{}
	f = newWarmFixture(t, rt, anthropicAPI, nil)
	f.serve(t, body, warmHeaders)
	f.clock.advance(2 * time.Hour)
	if rt.count() != 1 {
		t.Fatalf("warmed a contradicting lifetime: %d", rt.count())
	}
}

func TestCacheWarmStopsAtTheHorizon(t *testing.T) {
	rt := &warmTransport{}
	f := newWarmFixture(t, rt, anthropicAPI, nil)
	f.serve(t, warmBody(""), warmHeaders)
	f.clock.advance(3 * time.Hour)
	// Warms at 270s steps from the real request: 13 fit in 60 minutes.
	if got := rt.count() - 1; got != 13 {
		t.Fatalf("warms = %d, want 13 within the 60-minute horizon", got)
	}
}

func TestCacheWarmNewRequestRearms(t *testing.T) {
	rt := &warmTransport{}
	f := newWarmFixture(t, rt, anthropicAPI, nil)
	f.serve(t, warmBody(""), warmHeaders)
	f.clock.advance(200 * time.Second)
	f.serve(t, warmBody(""), warmHeaders)
	if rt.count() != 2 {
		t.Fatalf("requests = %d", rt.count())
	}
	f.clock.advance(70 * time.Second) // 270s after the first request
	if rt.count() != 2 {
		t.Fatal("the first request's warm survived a newer request")
	}
	f.clock.advance(200 * time.Second) // 270s after the second
	if rt.count() != 3 {
		t.Fatalf("the newer request was not warmed: %d", rt.count())
	}
}

func TestCacheWarmStopsOnErrorAndOnLostCache(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadRequest} {
		rt := &warmTransport{warmStatus: status, warmResp: `{"type":"error","error":{"type":"x","message":"no"}}`}
		f := newWarmFixture(t, rt, anthropicAPI, nil)
		f.serve(t, warmBody(""), warmHeaders)
		f.clock.advance(2 * time.Hour)
		if rt.count() != 2 {
			t.Fatalf("status %d: requests = %d, want the real one and one warm", status, rt.count())
		}
		if rows := f.warmRows(); len(rows) != 1 || rows[0].ErrorCode != "provider_"+strconv.Itoa(status) {
			t.Fatalf("status %d: warm rows %+v", status, rows)
		}
	}
	// The warm found no entry (it wrote one instead): the cache was already lost.
	rt := &warmTransport{warmResp: messageResp(warmModel, usageJSON(0, 200_000, "5m"))}
	f := newWarmFixture(t, rt, anthropicAPI, nil)
	f.serve(t, warmBody(""), warmHeaders)
	f.clock.advance(2 * time.Hour)
	if rt.count() != 2 {
		t.Fatalf("kept warming a lost cache: %d", rt.count())
	}
}

func TestCacheWarmEconomicsFailClosed(t *testing.T) {
	cases := map[string]*warmTransport{
		// 20k tokens of Sonnet 5: (2.50-0.20)*0.02 - 0.20*0.02 = $0.042 < $0.05.
		"below threshold": {realResp: messageResp(warmModel, usageJSON(0, 20_000, "5m"))},
		"nothing cached":  {realResp: messageResp(warmModel, `{"input_tokens":200000,"output_tokens":5,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}`)},
	}
	for name, rt := range cases {
		t.Run(name, func(t *testing.T) {
			f := newWarmFixture(t, rt, anthropicAPI, nil)
			f.serve(t, warmBody(""), warmHeaders)
			f.clock.advance(time.Hour)
			if rt.count() != 1 {
				t.Fatalf("warmed: %d", rt.count())
			}
		})
	}
	t.Run("unknown price", func(t *testing.T) {
		rt := &warmTransport{realResp: messageResp("claude-unpriced-9", usageJSON(0, 200_000, "5m"))}
		f := newWarmFixture(t, rt, anthropicAPI, nil)
		f.serve(t, strings.Replace(warmBody(""), warmModel, "claude-unpriced-9", 1), warmHeaders)
		f.clock.advance(time.Hour)
		if rt.count() != 1 {
			t.Fatalf("warmed an unpriced model: %d", rt.count())
		}
	})
	t.Run("no marker", func(t *testing.T) {
		rt := &warmTransport{}
		f := newWarmFixture(t, rt, anthropicAPI, nil)
		f.serve(t, strings.ReplaceAll(warmBody(""), `,"cache_control":{"type":"ephemeral"}`, ""), warmHeaders)
		f.clock.advance(time.Hour)
		if rt.count() != 1 {
			t.Fatalf("warmed without a declared lifetime: %d", rt.count())
		}
	})
}

func TestCacheWarmScopeFailsClosed(t *testing.T) {
	noSession := map[string]string{"x-api-key": "sk-ant-api-secret-key", "anthropic-version": "2023-06-01"}
	passThrough := map[string]string{"x-cave-transforms": "caveman.pass-through.v1"}
	for k, v := range warmHeaders {
		passThrough[k] = v
	}
	for name, tc := range map[string]struct {
		base    string
		headers map[string]string
	}{
		"custom origin": {"https://upstream.test", warmHeaders},
		"no session":    {anthropicAPI, noSession},
		"pass-through":  {anthropicAPI, passThrough},
	} {
		t.Run(name, func(t *testing.T) {
			rt := &warmTransport{}
			f := newWarmFixture(t, rt, tc.base, nil)
			f.serve(t, warmBody(""), tc.headers)
			f.clock.advance(time.Hour)
			if rt.count() != 1 {
				t.Fatalf("warmed: %d", rt.count())
			}
		})
	}
}

func TestCacheWarmKillSwitch(t *testing.T) {
	rt := &warmTransport{}
	f := newWarmFixture(t, rt, anthropicAPI, nil)
	*f.on = false
	f.serve(t, warmBody(""), warmHeaders)
	*f.on = true
	f.clock.advance(time.Hour)
	if rt.count() != 1 {
		t.Fatalf("armed while switched off: %d", rt.count())
	}
	f.serve(t, warmBody(""), warmHeaders)
	*f.on = false // switched off after arming: the timer must not send
	f.clock.advance(time.Hour)
	if rt.count() != 2 {
		t.Fatalf("warmed after the switch went off: %d", rt.count())
	}

	// No CacheWarm func at all: no warmer.
	if srv := New(Config{}); srv.warmer != nil {
		t.Fatal("warming on without a switch")
	}
}

func TestCacheWarmSideRequestLeavesWarmAlone(t *testing.T) {
	rt := &warmTransport{}
	f := newWarmFixture(t, rt, anthropicAPI, nil)
	f.serve(t, warmBody(""), warmHeaders)
	side := map[string]string{"x-caveman-agent": "title"}
	for k, v := range warmHeaders {
		side[k] = v
	}
	f.clock.advance(100 * time.Second)
	f.serve(t, warmBody(""), side)
	f.clock.advance(170 * time.Second)
	if rt.count() != 3 {
		t.Fatalf("a side request stopped or replaced the session's warm: %d", rt.count())
	}
}

func TestCacheWarmNeverLogsCredentials(t *testing.T) {
	var logs bytes.Buffer
	rt := &warmTransport{warmStatus: http.StatusUnauthorized, warmResp: `{"type":"error"}`}
	f := newWarmFixture(t, rt, anthropicAPI, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	f.serve(t, warmBody(""), warmHeaders)
	f.clock.advance(time.Hour)
	if rt.count() != 2 || !strings.Contains(logs.String(), "cache warm stopped") {
		t.Fatalf("expected one failed warm and its log line: %d\n%s", rt.count(), logs.String())
	}
	if strings.Contains(logs.String(), "sk-ant-api-secret-key") || strings.Contains(logs.String(), "You are an agent") {
		t.Fatalf("warm logged a credential or content:\n%s", logs.String())
	}
}

// Unit level: concurrency with real requests, quiesce, and the LRU bound.

func newUnitWarmer(send func(context.Context, *warmRequest) bool) (*cacheWarmer, *fakeClock) {
	w := newCacheWarmer(func() bool { return true }, send)
	c := &fakeClock{now: time.Now()}
	w.clock = c
	return w, c
}

func TestCacheWarmWaitsForInFlightRealRequest(t *testing.T) {
	sent := 0
	w, c := newUnitWarmer(func(context.Context, *warmRequest) bool { sent++; return true })
	req := &warmRequest{ttl: 5 * time.Minute}
	w.arm("s", "m", req, c.Now())
	end := w.begin("s", "other-model") // a real request on the session, another model
	c.advance(270 * time.Second)
	if sent != 0 {
		t.Fatal("warm sent while a real request of the session was in flight")
	}
	end()
	c.advance(cacheWarmBusyRetry)
	if sent != 1 {
		t.Fatalf("warm not retried after the real request ended: %d", sent)
	}
	// Busy past the deadline: given up.
	end = w.begin("s", "other-model")
	c.advance(10 * time.Minute)
	end()
	c.advance(time.Hour)
	if sent != 1 {
		t.Fatalf("a warm past its deadline was sent: %d", sent)
	}
}

func TestCacheWarmQuiesceAbandonsSlowWarm(t *testing.T) {
	started := make(chan struct{})
	var ctxErr error
	w, c := newUnitWarmer(func(ctx context.Context, _ *warmRequest) bool {
		close(started)
		<-ctx.Done()
		ctxErr = ctx.Err()
		return true
	})
	w.quiesce = 20 * time.Millisecond
	w.arm("s", "m", &warmRequest{ttl: 5 * time.Minute}, c.Now())
	go c.advance(270 * time.Second)
	<-started
	begun := time.Now()
	end := w.begin("s", "m")
	defer end()
	if time.Since(begun) < w.quiesce {
		t.Fatal("real request did not wait for the in-flight warm")
	}
	for i := 0; i < 100; i++ {
		w.mu.Lock()
		done := w.entries[warmKey("s", "m")].done == nil
		w.mu.Unlock()
		if done {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if ctxErr != context.Canceled {
		t.Fatalf("slow warm not abandoned: %v", ctxErr)
	}
	c.advance(time.Hour)
	if w.entries[warmKey("s", "m")].timer != nil {
		t.Fatal("abandoned warm rescheduled itself")
	}
}

func TestCacheWarmLRUBound(t *testing.T) {
	w, c := newUnitWarmer(func(context.Context, *warmRequest) bool { return true })
	for i := 0; i < cacheWarmMaxEntries+10; i++ {
		w.arm("s"+strconv.Itoa(i), "m", &warmRequest{ttl: 5 * time.Minute}, c.Now())
	}
	if len(w.entries) != cacheWarmMaxEntries || w.lru.Len() != cacheWarmMaxEntries {
		t.Fatalf("entries = %d / %d, want %d", len(w.entries), w.lru.Len(), cacheWarmMaxEntries)
	}
	if _, ok := w.entries[warmKey("s0", "m")]; ok {
		t.Fatal("oldest entry not evicted")
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

func TestCacheWarmSavingsExample(t *testing.T) {
	// 150k-token Sonnet 5 prefix, 5m: miss 0.375 - hit 0.03 - warm (0.03 + 10 input tokens).
	usage := providers.UsageObservation{InputTokens: 150_010, OutputTokens: 5, CachedInputTokens: 150_000,
		InputTokensReported: true, OutputTokensReported: true}
	saving, ok := cacheWarmSavings(usage, providers.RequestMetadata{Provider: "anthropic", Model: warmModel}, AuthModePAYG, 5*time.Minute)
	if !ok || saving < 0.3149 || saving > 0.3151 {
		t.Fatalf("saving = %v, %v", saving, ok)
	}
	// Subscription traffic is judged at list prices too.
	if _, ok := cacheWarmSavings(usage, providers.RequestMetadata{Provider: "anthropic", Model: warmModel}, AuthModeSubscription, 5*time.Minute); !ok {
		t.Fatal("subscription traffic not priced at list")
	}
}
