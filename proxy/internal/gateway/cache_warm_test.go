package gateway

import (
	"bytes"
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
	"github.com/JuliusBrussee/caveman/shared/platform/catalog"
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
		if next.at.After(c.now) { // a late timer fires at the current time
			c.now = next.at
		}
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
	// realStatuses answers the real requests in order (0 or missing: 200).
	realStatuses []int
	reals        int
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
	} else {
		t.mu.Lock()
		if t.reals < len(t.realStatuses) && t.realStatuses[t.reals] != 0 {
			status, resp = t.realStatuses[t.reals], `{"type":"error","error":{"type":"invalid_request_error","message":"no"}}`
		}
		t.reals++
		t.mu.Unlock()
		if t.sse && status == http.StatusOK {
			ct = "text/event-stream"
		}
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

// realUsageTail is what Anthropic's API sends with every usage object today.
// Fixtures carry it by default: a response without these fields was priced
// while every real one was not, and the warm tests passed on a dead feature.
const realUsageTail = `,"service_tier":"standard","inference_geo":"not_available","iterations":[{"type":"message","input_tokens":10,"output_tokens":5}]`

// usageJSON is an Anthropic usage object for a 200k-token cached prefix.
func usageJSON(read, write int, ttl string) string {
	creation := `"cache_creation":{"ephemeral_5m_input_tokens":` + strconv.Itoa(write) + `,"ephemeral_1h_input_tokens":0}`
	if ttl == "1h" {
		creation = `"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":` + strconv.Itoa(write) + `}`
	}
	return `{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":` + strconv.Itoa(read) +
		`,"cache_creation_input_tokens":` + strconv.Itoa(write) + `,` + creation + realUsageTail + `}`
}

func messageResp(model, usage string) string {
	return `{"id":"msg","type":"message","model":"` + model + `","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":` + usage + `}`
}

const warmModel = "claude-sonnet-5" // catalog: input 2.00, read 0.20, write 2.50 / 1h 4.00 per M

func reqBody(extra string) string {
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
	clock := &fakeClock{now: time.Now().Round(0)}
	srv.warmer.clock = clock
	srv.warmer.jitter = func() time.Duration { return 0 } // tests pin the planned times
	// Tests plan against a known return time, not the shipped measurements.
	srv.warmer.table = returnsAt(30 * time.Minute)
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
	body := reqBody(`"stream":false,"thinking":{"type":"adaptive"},`)
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
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":" +
		"{\"input_tokens\":10,\"cache_creation_input_tokens\":200000,\"cache_read_input_tokens\":0,\"output_tokens\":5" + realUsageTail + "}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	rt := &warmTransport{realResp: sse, sse: true}
	f := newWarmFixture(t, rt, anthropicAPI, nil)
	f.serve(t, reqBody(`"stream":true,`), warmHeaders)
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
			f.serve(t, reqBody(extra), warmHeaders)
			f.clock.advance(2 * time.Hour)
			if rt.count() != 1 {
				t.Fatalf("warmed a request max_tokens 0 cannot replay: %d requests", rt.count())
			}
		})
	}
	// Effort alone is replayable: it stays in the bytes, so the key is the same.
	rt := &warmTransport{}
	f := newWarmFixture(t, rt, anthropicAPI, nil)
	f.serve(t, reqBody(`"output_config":{"effort":"high"},"tool_choice":{"type":"auto"},`), warmHeaders)
	f.clock.advance(270 * time.Second)
	if rt.count() != 2 || !strings.Contains(rt.body(1), `"output_config":{"effort":"high"}`) {
		t.Fatalf("effort request not warmed as sent: %d", rt.count())
	}
}

func TestCacheWarmOneHourLifetime(t *testing.T) {
	rt := &warmTransport{realResp: messageResp(warmModel, usageJSON(0, 200_000, "1h")),
		warmResp: messageResp(warmModel, usageJSON(200_000, 0, "1h"))}
	f := newWarmFixture(t, rt, anthropicAPI, nil)
	f.srv.warmer.table = returnsAt(100 * time.Minute)
	body := strings.ReplaceAll(reqBody(""), `{"type":"ephemeral"}`, `{"type":"ephemeral","ttl":"1h"}`)
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
	f.srv.warmer.table = returnsAt(100 * time.Minute)
	f.serve(t, body, warmHeaders)
	f.clock.advance(2 * time.Hour)
	if rt.count() != 1 {
		t.Fatalf("warmed a contradicting lifetime: %d", rt.count())
	}
}

func TestCacheWarmChainStopsWhereThePredictionDoes(t *testing.T) {
	rt := &warmTransport{}
	f := newWarmFixture(t, rt, anthropicAPI, nil)
	f.serve(t, reqBody(""), warmHeaders)
	f.clock.advance(3 * time.Hour)
	// Expected back at 30 min: warms at 270 s steps until the cache reaches
	// past it (the 6th keeps it to 32 min), then none.
	if got := rt.count() - 1; got != 6 {
		t.Fatalf("warms = %d, want 6", got)
	}
	// Expected back only after the horizon: the chain cannot reach it.
	rt = &warmTransport{}
	f = newWarmFixture(t, rt, anthropicAPI, nil)
	f.srv.warmer.table = returnsAt(70 * time.Minute)
	f.serve(t, reqBody(""), warmHeaders)
	f.clock.advance(3 * time.Hour)
	if rt.count() != 1 {
		t.Fatalf("warmed toward a return past the horizon: %d", rt.count())
	}
}

func TestCacheWarmNewRequestRearms(t *testing.T) {
	rt := &warmTransport{}
	f := newWarmFixture(t, rt, anthropicAPI, nil)
	f.serve(t, reqBody(""), warmHeaders)
	f.clock.advance(200 * time.Second)
	f.serve(t, reqBody(""), warmHeaders)
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
		f.serve(t, reqBody(""), warmHeaders)
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
	f.serve(t, reqBody(""), warmHeaders)
	f.clock.advance(2 * time.Hour)
	if rt.count() != 2 {
		t.Fatalf("kept warming a lost cache: %d", rt.count())
	}
}

func TestCacheWarmEconomicsFailClosed(t *testing.T) {
	cases := map[string]*warmTransport{
		"nothing cached": {realResp: messageResp(warmModel, `{"input_tokens":200000,"output_tokens":5,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}`)},
	}
	for name, rt := range cases {
		t.Run(name, func(t *testing.T) {
			f := newWarmFixture(t, rt, anthropicAPI, nil)
			f.serve(t, reqBody(""), warmHeaders)
			f.clock.advance(time.Hour)
			if rt.count() != 1 {
				t.Fatalf("warmed: %d", rt.count())
			}
		})
	}
	t.Run("unknown price", func(t *testing.T) {
		rt := &warmTransport{realResp: messageResp("claude-unpriced-9", usageJSON(0, 200_000, "5m"))}
		f := newWarmFixture(t, rt, anthropicAPI, nil)
		f.serve(t, strings.Replace(reqBody(""), warmModel, "claude-unpriced-9", 1), warmHeaders)
		f.clock.advance(time.Hour)
		if rt.count() != 1 {
			t.Fatalf("warmed an unpriced model: %d", rt.count())
		}
	})
	t.Run("no marker", func(t *testing.T) {
		rt := &warmTransport{}
		f := newWarmFixture(t, rt, anthropicAPI, nil)
		f.serve(t, strings.ReplaceAll(reqBody(""), `,"cache_control":{"type":"ephemeral"}`, ""), warmHeaders)
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
			f.serve(t, reqBody(""), tc.headers)
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
	f.serve(t, reqBody(""), warmHeaders)
	*f.on = true
	f.clock.advance(time.Hour)
	if rt.count() != 1 {
		t.Fatalf("armed while switched off: %d", rt.count())
	}
	f.serve(t, reqBody(""), warmHeaders)
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
	f.serve(t, reqBody(""), warmHeaders)
	side := map[string]string{"x-caveman-agent": "title"}
	for k, v := range warmHeaders {
		side[k] = v
	}
	f.clock.advance(100 * time.Second)
	f.serve(t, reqBody(""), side)
	f.clock.advance(170 * time.Second)
	if rt.count() != 3 {
		t.Fatalf("a side request stopped or replaced the session's warm: %d", rt.count())
	}
}

func TestCacheWarmNeverLogsCredentials(t *testing.T) {
	var logs bytes.Buffer
	rt := &warmTransport{warmStatus: http.StatusUnauthorized, warmResp: `{"type":"error"}`}
	f := newWarmFixture(t, rt, anthropicAPI, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	f.serve(t, reqBody(""), warmHeaders)
	f.clock.advance(time.Hour)
	if rt.count() != 2 || !strings.Contains(logs.String(), "cache warm stopped") {
		t.Fatalf("expected one failed warm and its log line: %d\n%s", rt.count(), logs.String())
	}
	if strings.Contains(logs.String(), "sk-ant-api-secret-key") || strings.Contains(logs.String(), "You are an agent") {
		t.Fatalf("warm logged a credential or content:\n%s", logs.String())
	}
}

// The usage object exactly as api.anthropic.com sent it on live subscription
// traffic: priced at the catalog rate, and a stream answered with it is warmed.
func TestCacheWarmPricesTheLiveUsageShape(t *testing.T) {
	const live = `{"input_tokens":2,"cache_creation_input_tokens":34890,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":34890,"ephemeral_1h_input_tokens":0},"output_tokens":4,"service_tier":"standard","inference_geo":"not_available","iterations":[{"type":"message","input_tokens":2,"output_tokens":4}]}`
	meta := providers.RequestMetadata{Provider: "anthropic", Model: "claude-sonnet-5-5"}
	scanner := anthropic.New(anthropicAPI).NewUsageScanner(http.Header{})
	_, _ = scanner.Write([]byte(messageResp(meta.Model, live)))
	usage := scanner.Usage()
	want, _ := catalog.Price("anthropic", meta.Model)
	if got := standalonePriceForUsage(meta, usage); got != want || want.CacheReadPerMillion <= 0 {
		t.Fatalf("price = %+v, want the catalog's %+v (usage %+v)", got, want, usage)
	}
	if _, _, ok := cacheWarmCosts(usage, meta, AuthModeSubscription, 5*time.Minute); !ok {
		t.Fatalf("live usage shape not costed: %+v", usage)
	}
	sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"model\":\"" + warmModel +
		"\",\"content\":[],\"usage\":" + live + "}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":" + live + "}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	for name, rt := range map[string]*warmTransport{
		"json": {realResp: messageResp(warmModel, live)},
		"sse":  {realResp: sse, sse: true},
	} {
		t.Run(name, func(t *testing.T) {
			f := newWarmFixture(t, rt, anthropicAPI, nil)
			extra := ""
			if rt.sse {
				extra = `"stream":true,`
			}
			f.serve(t, reqBody(extra), warmHeaders)
			f.clock.advance(270 * time.Second)
			if rows := f.warmRows(); rt.count() != 2 || len(rows) != 1 || rows[0].TotalCostUSD <= 0 {
				t.Fatalf("requests = %d, warm rows = %+v", rt.count(), rows)
			}
		})
	}
}
