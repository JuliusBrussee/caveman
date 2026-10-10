package gateway

import (
	"bytes"
	"context"
	"log/slog"
	"math"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
)

// The plans .blocks/cache-warm-sim.py prints for the shipped table
// (--table cache_warm_table.json --plan-cases <the keys>, one digit per warm
// point): the proxy and the simulator that measured the policy plan alike,
// prices included. Regenerate these with the table.
func TestCacheWarmPlanMatchesTheSimulator(t *testing.T) {
	table := newWarmTable("")
	for name, want := range map[string]string{
		"main/end/5m:300000:10:claude-opus-5":          "1111000000000",
		"main/end/5m:20000:2000:claude-haiku-5-5":      "1100000000000",
		"main/tool/5m:250000:10:claude-sonnet-5-5":     "1111100011100",
		"main/end/1h:300000:10:claude-opus-5":          "1",
		"main/tool/1h:250000:10:claude-sonnet-5-5":     "1",
		"subagent/tool/5m:160000:10:claude-sonnet-5-5": "1100000000000",
		"subagent/end/5m:240000:10:claude-sonnet-5-5":  "1000000000000",
		"subagent/end/5m:30000:500:claude-haiku-5-5":   "0000000000000",
	} {
		part := strings.Split(name, ":")
		class, prefix, tail, model := part[0], atoi(t, part[1]), atoi(t, part[2]), part[3]
		ttl := 5 * time.Minute
		if strings.HasSuffix(class, "/1h") {
			ttl = time.Hour
		}
		usage := providers.UsageObservation{InputTokens: prefix + tail, CachedInputTokens: prefix, OutputTokens: 1,
			InputTokensReported: true, OutputTokensReported: true}
		full, warm, ok := cacheWarmCosts(usage, providers.RequestMetadata{Provider: "anthropic", Model: model}, AuthModePAYG, ttl)
		if !ok {
			t.Fatalf("%s: not priced", name)
		}
		got := ""
		for _, send := range table.plan(class, ttl, cacheWarmHorizon, full, warm)[1:] {
			got += map[bool]string{true: "1", false: "0"}[send]
		}
		if got != want {
			t.Errorf("%s: plan %s, the simulator's %s", name, got, want)
		}
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// Half the streams never come back, half come back at 20 min and pay the
// whole miss: realized (measured over the streams that return) is 1, and the
// chance of never returning is the table's last bin, applied once. Four warms
// at 0.10 buy 0.5 x 1.00; at 0.13 they do not. A realized that also averaged
// in the never-returning half (0.5) would refuse the first chain.
func TestCacheWarmPlanCountsNeverReturningOnce(t *testing.T) {
	table := returnsAt(20 * time.Minute)
	p := table.prior["all"].P
	p[40], p[240] = 0.5, 0.5
	if plan := table.plan("main/end/5m", 5*time.Minute, cacheWarmHorizon, 1.0, 0.10); warmCount(plan) != 4 {
		t.Fatalf("plan at 0.10 = %v", plan[:7])
	}
	if plan := table.plan("main/end/5m", 5*time.Minute, cacheWarmHorizon, 1.0, 0.13); warmCount(plan) != 0 {
		t.Fatalf("plan at 0.13 = %v", plan[:7])
	}
	// The shipped realized shares are of returning streams: none is dragged
	// down by its class's never-returning share (subagent/end: over half).
	shipped := newWarmTable("")
	for class, c := range shipped.prior {
		if c.Realized < 0.6 || c.Realized > 1 {
			t.Errorf("%s: realized %v", class, c.Realized)
		}
	}
}

func TestCacheWarmToolStopAcrossAnyReads(t *testing.T) {
	stream := `data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null}}`
	for _, size := range []int{1, 2, 5, 7, 23, 24, 1000} {
		var s toolStop
		for i := 0; i < len(stream); i += size {
			_, _ = s.Write([]byte(stream[i:min(len(stream), i+size)]))
		}
		if !s.seen {
			t.Fatalf("tool_use not seen in reads of %d bytes", size)
		}
		var e toolStop
		end := strings.Replace(stream, "tool_use", "end_turn", 1)
		for i := 0; i < len(end); i += size {
			_, _ = e.Write([]byte(end[i:min(len(end), i+size)]))
		}
		if e.seen || len(e.tail) >= len(toolStopNeedle) {
			t.Fatalf("reads of %d bytes: seen=%v, tail of %d bytes", size, e.seen, len(e.tail))
		}
	}
}

func TestCacheWarmCorruptGapsFileStartsFromDefaults(t *testing.T) {
	good := `[` + strings.Repeat("1,", 240) + `1]`
	want, _, _ := newWarmTable("").probs("main/end/5m")
	for name, raw := range map[string]string{
		"garbage":        `{"classes":`,
		"negative":       `{"bin_seconds":30,"classes":{"main/end/5m":` + strings.Replace(good, "1,", "-5,", 1) + `}}`,
		"absurd":         `{"bin_seconds":30,"classes":{"main/end/5m":` + strings.Replace(good, "1,", "1e300,", 1) + `}}`,
		"short":          `{"bin_seconds":30,"classes":{"main/end/5m":[1,2,3]}}`,
		"other bins":     `{"bin_seconds":60,"classes":{"main/end/5m":` + good + `}}`,
		"unknown class":  `{"bin_seconds":30,"classes":{"../../etc":` + good + `}}`,
		"one bad class":  `{"bin_seconds":30,"classes":{"main/end/5m":` + good + `,"main/tool/5m":[null]}}`,
		"null and zeros": `{"bin_seconds":30,"classes":{"main/end/5m":null}}`,
	} {
		path := filepath.Join(t.TempDir(), "gaps.json")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		table := newWarmTable(path)
		got, _, ok := table.probs("main/end/5m")
		if !ok || len(table.counts) != 0 {
			t.Fatalf("%s: counts kept: %v", name, table.counts)
		}
		for i := range got {
			if math.IsNaN(got[i]) || got[i] != want[i] {
				t.Fatalf("%s: bin %d = %v, default %v", name, i, got[i], want[i])
			}
		}
		table.observe("main/end/5m", time.Minute) // and it still learns
		if after, _, _ := table.probs("main/end/5m"); !(after[2] > want[2]) {
			t.Fatalf("%s: stopped learning", name)
		}
	}
	// A symlink is neither read nor written through.
	dir := t.TempDir()
	target, link := filepath.Join(dir, "target"), filepath.Join(dir, "gaps.json")
	saved := `{"bin_seconds":30,"classes":{"main/end/5m":` + good + `}}`
	if err := os.WriteFile(target, []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skip("no symlinks here")
	}
	table := newWarmTable(link)
	table.observe("main/end/5m", time.Minute)
	table.save()
	if raw, _ := os.ReadFile(target); len(table.counts["main/end/5m"]) == 0 || table.counts["main/end/5m"][0] != 0 || string(raw) != saved {
		t.Fatalf("symlinked gaps file was read or written: %v", table.counts)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the symlink was replaced")
	}
}

// A stream evicted (or alive at shutdown) while still waiting is counted as
// coming back later than it was seen silent, never dropped: the returns that
// came first are all counted, so dropping the rest reads as "always returns".
func TestCacheWarmEvictedPendingStreamIsCounted(t *testing.T) {
	w, c := newUnitWarmer(func(context.Context, *warmRequest) bool { return true })
	w.arm("first", "main/end/5m", nil, c.Now())
	c.advance(10 * time.Minute)
	w.arm("second", "main/tool/5m", nil, c.Now().Add(-3*time.Hour)) // silent past the horizon already
	for i := 0; i < cacheWarmMaxEntries; i++ {
		w.arm("s"+itoa(int64(i)), "", nil, c.Now())
	}
	if w.entries["first"] != nil || w.entries["second"] != nil {
		t.Fatal("not evicted")
	}
	// Silent for 10 min under a table that returns at 30 min: one stream in the 30 min bin.
	got := w.table.counts["main/end/5m"]
	if got == nil || got[60] != 1 || sum(got) != 1 {
		t.Fatalf("evicted pending stream: %v", got)
	}
	if gone := w.table.counts["main/tool/5m"]; gone == nil || gone[240] != 1 || sum(gone) != 1 {
		t.Fatalf("stream silent past the horizon: %v", gone)
	}
	// One that was not waiting (its gap already counted) adds nothing.
	if n := len(w.table.counts); n != 2 {
		t.Fatalf("classes counted: %d", n)
	}
	// Seen silent past every return the table knows: it never came back.
	late := returnsAt(5 * time.Minute)
	late.record([]warmGap{{class: "main/end/5m", d: 10 * time.Minute, silent: true}})
	if got := late.counts["main/end/5m"]; got[240] != 1 || sum(got) != 1 {
		t.Fatalf("silent past the last known return: %v", got)
	}
	// Part of the mass: silent for 10 min when half return at 5 and half at 30.
	half := returnsAt(30 * time.Minute)
	half.prior["all"].P[10], half.prior["all"].P[60] = 0.5, 0.5
	half.record([]warmGap{{class: "main/end/5m", d: 10 * time.Minute, silent: true}})
	if got := half.counts["main/end/5m"]; got[60] != 1 || got[10] != 0 {
		t.Fatalf("mass put before the age it was seen silent at: %v", got[:61])
	}
}

func sum(v []float64) float64 {
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s
}

func TestCacheWarmShutdownSavesWhatItLearned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gaps.json")
	sent := 0
	w, c := newUnitWarmer(func(context.Context, *warmRequest) bool { sent++; return true })
	w.table = newWarmTable(path)
	body := []byte("0123456789")
	for _, key := range []string{"a", "b", "c"} {
		w.arm(key, "main/end/5m", &warmRequest{ttl: 5 * time.Minute, body: body, plan: []bool{false, true, true}}, c.Now())
	}
	c.advance(time.Minute)
	w.begin("a")() // one return, far from the 20 that trigger a save
	if _, err := os.Stat(path); err == nil {
		t.Fatal("saved before shutdown; the test proves nothing")
	}
	w.close()
	if len(w.entries) != 0 || w.lru.Len() != 0 || w.bytes != 0 {
		t.Fatalf("held after close: %d entries, %d bytes", len(w.entries), w.bytes)
	}
	info, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("gaps not saved 0600 at shutdown: %v", err)
	}
	got := newWarmTable(path).counts["main/end/5m"]
	// a came back at 1 min; b and c were silent for 1 min and are spread later.
	if got == nil || got[2] < 1 || math.Abs(sum(got)-3) > 1e-9 || got[0]+got[1] != 0 {
		t.Fatalf("reloaded counts: sum %v, first bins %v", sum(got), got[:4])
	}
	c.advance(time.Hour)
	if sent != 0 {
		t.Fatalf("warmed after close: %d", sent)
	}
	(&Server{}).Close() // no warmer: nothing to do
}

// The held request (body and credential headers) goes as soon as the chain
// is over, not when the stream is next heard from or evicted.
func TestCacheWarmReleasesTheRequestWhenTheChainEnds(t *testing.T) {
	body := []byte("0123456789")
	for name, tc := range map[string]struct {
		again bool
		plan  []bool
		after time.Duration
	}{
		"plan done":    {true, []bool{false, true, true, false, true}, 540 * time.Second},
		"plan end":     {true, []bool{false, true}, 270 * time.Second},
		"warm failed":  {false, []bool{false, true, true, true}, 270 * time.Second},
		"never starts": {true, []bool{false, false, true}, 0},
	} {
		w, c := newUnitWarmer(func(context.Context, *warmRequest) bool { return tc.again })
		w.arm("s", "main/end/5m", &warmRequest{ttl: 5 * time.Minute, body: body, plan: tc.plan}, c.Now())
		if tc.after > 0 && (w.entries["s"].req == nil || w.bytes != len(body)) {
			t.Fatalf("%s: not held while a warm is due", name)
		}
		c.advance(tc.after)
		if e := w.entries["s"]; e.req != nil || e.timer != nil || w.bytes != 0 || !e.pending {
			t.Fatalf("%s: req held=%v timer=%v bytes=%d pending=%v", name, e.req != nil, e.timer != nil, w.bytes, e.pending)
		}
	}
	// Past its deadline, switched off or paused: let go as well.
	w, c := newUnitWarmer(func(context.Context, *warmRequest) bool { return true })
	w.arm("s", "main/end/5m", &warmRequest{ttl: 5 * time.Minute, body: body, cred: "k", plan: []bool{false, true, true}}, c.Now())
	w.backoff("k", c.Now().Add(time.Hour))
	c.advance(270 * time.Second)
	if w.entries["s"].req != nil || w.bytes != 0 {
		t.Fatal("a paused chain kept its request")
	}
}

// The gaps file is written with the warmer unlocked: a slow disk never holds
// up the other streams' requests.
func TestCacheWarmSavesOutsideTheWarmerLock(t *testing.T) {
	w, c := newUnitWarmer(func(context.Context, *warmRequest) bool { return true })
	w.table = newWarmTable(filepath.Join(t.TempDir(), "gaps.json"))
	w.arm("s", "main/end/5m", nil, c.Now())
	w.table.pending = 19
	w.table.saveMu.Lock() // the write is stuck
	done := make(chan struct{})
	go func() { w.begin("s")(); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		w.table.mu.Lock()
		counted := w.table.pending == 0
		w.table.mu.Unlock()
		if counted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the gap was never counted")
		}
		time.Sleep(time.Millisecond)
	}
	if !w.mu.TryLock() {
		t.Fatal("the warmer is locked while the gaps file is written")
	}
	w.mu.Unlock()
	if _, _, ok := w.table.probs("main/end/5m"); !ok { // the table too
		t.Fatal("table unreadable during a write")
	}
	w.table.saveMu.Unlock()
	<-done
}

func TestCacheWarmMixedLifetimesAreNotWarmed(t *testing.T) {
	mixed := `{"model":"m","max_tokens":10,"system":[{"type":"text","text":"a","cache_control":{"type":"ephemeral","ttl":"1h"}}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"b","cache_control":{"type":"ephemeral"}}]}]}`
	if ttl := declaredCacheTTL([]byte(mixed)); ttl != 0 {
		t.Fatalf("mixed 1h and 5m markers given one lifetime: %v", ttl)
	}
	if body, _ := warmBody([]byte(mixed)); body != nil {
		t.Fatal("mixed lifetimes warmed")
	}
	if ttl := declaredCacheTTL([]byte(strings.Replace(mixed, `{"type":"ephemeral"}`, `{"type":"ephemeral","ttl":"1h"}`, 1))); ttl != time.Hour {
		t.Fatalf("all-1h markers: %v", ttl)
	}
	// 5m markers answered with 1h writes (or the reverse): no single lifetime either.
	rt := &warmTransport{realResp: messageResp(warmModel, usageJSON(0, 200_000, "1h"))}
	f := newWarmFixture(t, rt, anthropicAPI, nil)
	f.serve(t, reqBody(""), warmHeaders)
	f.clock.advance(time.Hour)
	if rt.count() != 1 {
		t.Fatalf("warmed a request whose writes had another lifetime than its markers: %d", rt.count())
	}
}

func warmAnswerFor(model string, tool, encoded bool) warmAnswer {
	upstream, _ := url.Parse(anthropicAPI + "/v1/messages")
	return warmAnswer{
		key: "sess-secret-id", start: time.Now(), ok: true, adapter: anthropic.New(anthropicAPI),
		meta:     providers.RequestMetadata{Provider: "anthropic", Model: model, Endpoint: "/v1/messages"},
		authMode: AuthModePAYG, upstream: upstream, header: map[string][]string{"X-Api-Key": {"sk-ant-api-secret-key"}},
		body: []byte(reqBody("")), tool: tool, encoded: encoded, session: "sess-secret-id",
		usage: providers.UsageObservation{InputTokens: 200_010, OutputTokens: 5, CacheCreationInputTokens: 200_000,
			CacheCreation5mTokens: 200_000, InputTokensReported: true, OutputTokensReported: true},
	}
}

// One line per answered request says what was planned (info) or why nothing was (debug),
// with the session as a hash and no content or credential.
func TestCacheWarmLogsThePlanOrTheSkip(t *testing.T) {
	var logs bytes.Buffer
	f := newWarmFixture(t, &warmTransport{}, anthropicAPI, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	line := func(a warmAnswer) string {
		logs.Reset()
		f.srv.armCacheWarm(r, a)
		out := logs.String()
		if strings.Count(out, "cache warm plan") != 1 || !strings.Contains(out, "session="+shortHash("sess-secret-id")) {
			t.Fatalf("log: %s", out)
		}
		if strings.Contains(out, "sess-secret-id") || strings.Contains(out, "sk-ant") || strings.Contains(out, "You are an agent") {
			t.Fatalf("logged a session id, credential or content: %s", out)
		}
		return out
	}
	// The fixture's table returns at 30 min: 6 warms keep a 5m cache that long.
	if out := line(warmAnswerFor(warmModel, true, false)); !strings.Contains(out, "model="+warmModel) ||
		!strings.Contains(out, "class=main/tool/5m") || !strings.Contains(out, "ttl=5m0s") || !strings.Contains(out, "warms=6") || !strings.Contains(out, `skip=""`) {
		t.Fatalf("planned: %s", out)
	}
	failed := warmAnswerFor(warmModel, false, false)
	failed.ok = false
	off := warmAnswerFor(warmModel, false, false)
	bedrock := warmAnswerFor(warmModel, false, false)
	bedrock.upstream, _ = url.Parse("https://bedrock-runtime.us-east-1.amazonaws.com/v1/messages")
	thinking := warmAnswerFor(warmModel, false, false)
	thinking.body = []byte(reqBody(`"thinking":{"type":"enabled","budget_tokens":1024},`))
	oneHour := warmAnswerFor(warmModel, false, false)
	oneHour.usage.CacheCreation5mTokens, oneHour.usage.CacheCreation1hTokens = 0, 200_000
	tiny := warmAnswerFor(warmModel, false, false)
	tiny.usage.InputTokens, tiny.usage.CacheCreationInputTokens, tiny.usage.CacheCreation5mTokens = 200_010, 10, 10
	for want, a := range map[string]warmAnswer{
		"response_error": failed, "not_anthropic_api": bedrock, "not_replayable": thinking, "lifetime_mismatch": oneHour,
		"unpriced": warmAnswerFor("claude-unpriced-9", false, false), "not_worth_it": tiny,
	} {
		if out := line(a); !strings.Contains(out, "skip="+want) || !strings.Contains(out, "warms=0") {
			t.Fatalf("%s: %s", want, out)
		}
	}
	*f.on = false
	if out := line(off); !strings.Contains(out, "skip=off") {
		t.Fatalf("off: %s", out)
	}
}

// An encoded answer's stop reason is not read: the stream is planned as
// whichever class warms less, and its next gap teaches neither class.
func TestCacheWarmEncodedAnswerIsNotClassed(t *testing.T) {
	f := newWarmFixture(t, &warmTransport{}, anthropicAPI, nil)
	table := returnsAt(20 * time.Minute) // tool turns come back, finished turns never
	never := make([]float64, 241)
	never[240] = 1
	table.prior["main/end"] = warmClass{N: 1, Realized: 1, P: never}
	f.srv.warmer.table = table
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	f.srv.armCacheWarm(r, warmAnswerFor(warmModel, true, false))
	if e := f.srv.warmer.entries["sess-secret-id"]; e.req == nil || e.class != "main/tool/5m" || !e.pending {
		t.Fatalf("a plain tool answer: %+v", e)
	}
	for _, tool := range []bool{true, false} { // whatever the scan of the encoded bytes said
		f.srv.armCacheWarm(r, warmAnswerFor(warmModel, tool, true))
		if e := f.srv.warmer.entries["sess-secret-id"]; e.req != nil || e.class != "" || e.pending {
			t.Fatalf("an encoded answer was classed or warmed as the eager class: %+v", e)
		}
	}
}
