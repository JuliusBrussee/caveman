package gateway

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
	"github.com/JuliusBrussee/caveman/shared/platform/cost"
	"github.com/JuliusBrussee/caveman/shared/platform/id"
)

// Prompt-cache warming. While an agent pauses (the user is away, a long tool
// runs, it waits on subagents) the Anthropic cache entry its last request
// wrote can expire, and the next request pays a full cache write again.
// Replaying that exact request with `max_tokens: 0` shortly before the entry
// expires reads (and so refreshes) the entry and bills no output.
//
// Whether to send each warm is predicted, not fixed: cache_warm_table.go
// holds the measured return-time distribution of each stream class (main or
// subagent, tool call or end of turn, 5m or 1h) and plans the whole warm
// chain by optimal stopping against the catalog price of the model sent.
// Every input fails closed: an unknown lifetime, price or class, a request
// that cannot be replayed at zero output without changing its cache key, or
// an origin other than Anthropic's own API means no warm. The expected saving
// is an estimate and is never booked as a saving.
const (
	cacheWarmOptimizerID = "cache-warm"
	// No warm later than this after the real request that started the chain.
	cacheWarmHorizon = 60 * time.Minute
	// A warm due while a real request of the stream is in flight tries again
	// after this, until its deadline.
	cacheWarmBusyRetry  = 5 * time.Second
	cacheWarmMaxEntries = 1024
	// Held request bodies (with their credentials) stay under this many bytes.
	cacheWarmMaxBytes = 256 << 20
	// A stream not heard from this long after its last request counts as one
	// that never came back.
	cacheWarmGone = 2 * time.Hour
	// Warms in flight at once in the whole process: sibling subagents answered
	// in the same seconds come due together, each a full-context request.
	cacheWarmMaxConcurrent = 2
	// A warm fires up to this much before its planned time, never after it,
	// so those siblings do not all start in the same instant.
	cacheWarmJitter = 5 * time.Second
	// A 529 or 5xx on a warm pauses its credential this long when the answer
	// names no Retry-After.
	cacheWarmOverloadPause = 5 * time.Minute
)

// cacheWarmDelay is Pi's refresh point: 90% of the lifetime, keeping at least
// ten seconds of margin. Zero means no warm.
func cacheWarmDelay(ttl time.Duration) time.Duration {
	if ttl <= 10*time.Second {
		return 0
	}
	return min(ttl*9/10, ttl-10*time.Second)
}

type warmClock interface {
	Now() time.Time
	AfterFunc(time.Duration, func()) interface{ Stop() bool }
}

type systemClock struct{}

// Now is wall time: a laptop that slept must see the hours it slept, which
// the monotonic reading does not count.
func (systemClock) Now() time.Time { return time.Now().Round(0) }
func (systemClock) AfterFunc(d time.Duration, f func()) interface{ Stop() bool } {
	return time.AfterFunc(d, f)
}

// warmRequest is one real request as it left the proxy, rewritten to zero
// output. Memory only: it holds the upstream credential, so it is never
// stored, logged or captured.
type warmRequest struct {
	url      string
	header   http.Header
	body     []byte
	ttl      time.Duration
	adapter  providers.Adapter
	meta     providers.RequestMetadata
	authMode AuthMode
	mode     string
	label    string
	agent    string
	session  string
	basis    string
	// cred names the credential (a hash) for 429 backoff.
	cred string
	// plan[k] says whether warm k is sent (cache_warm_table.go).
	plan []bool
}

// warmEntry is the warming state of one stream: a session, or one subagent
// of it.
type warmEntry struct {
	key   string
	gen   int
	timer interface{ Stop() bool }
	// req is held only while a warm of it may still be sent.
	req *warmRequest
	// class and start of the stream's last real request, until its next one
	// (or its absence) has been counted in the table.
	class     string
	lastStart time.Time
	pending   bool
	elem      *list.Element
}

type cacheWarmer struct {
	mu    sync.Mutex
	clock warmClock
	// enabled is the waste-fixes module switch plus the env kill switch, read
	// again before every warm so switching it off stops warming.
	enabled func() bool
	send    func(ctx context.Context, req *warmRequest) bool
	table   *warmTable
	entries map[string]*warmEntry
	lru     *list.List
	bytes   int
	// real counts the real requests in flight per stream.
	real map[string]int
	// pausedUntil holds 429 and overload backoffs per credential hash.
	pausedUntil map[string]time.Time
	swept       time.Time
	// sending counts the warms in flight (cacheWarmMaxConcurrent).
	sending int
	// jitter is how much earlier than planned a warm fires; tests fix it.
	jitter func() time.Duration
}

func newCacheWarmer(enabled func() bool, send func(context.Context, *warmRequest) bool, table *warmTable) *cacheWarmer {
	return &cacheWarmer{
		clock: systemClock{}, enabled: enabled, send: send, table: table,
		entries: map[string]*warmEntry{}, lru: list.New(), real: map[string]int{}, pausedUntil: map[string]time.Time{},
		jitter: func() time.Duration { return rand.N(cacheWarmJitter) },
	}
}

// begin marks a real request of stream key and stops the stream's warming.
// A session's request leaves its subagents' warms alone: a subagent can still
// be running when its parent talks, and on the measured sessions stopping
// them cost more misses than the warms it spared (proxy/CLAUDE.md has the
// numbers); the table already plans few warms for a subagent that is done.
// It never waits: a warm already in flight finishes in the background. The
// gap since the stream's last request is counted in the table, after the
// lock is released. The returned func ends the request.
func (w *cacheWarmer) begin(key string) func() {
	var seen []warmGap
	w.mu.Lock()
	now := w.clock.Now()
	w.real[key]++
	if e := w.entries[key]; e != nil {
		if e.pending {
			e.pending = false
			seen = append(seen, warmGap{class: e.class, d: max(0, now.Sub(e.lastStart))}) // a clock set back is no gap
		}
		w.stopLocked(e)
	}
	w.mu.Unlock()
	w.table.record(seen)
	var once sync.Once
	return func() {
		once.Do(func() {
			w.mu.Lock()
			if w.real[key]--; w.real[key] <= 0 {
				delete(w.real, key)
			}
			w.mu.Unlock()
		})
	}
}

// stopLocked cancels the entry's timer, invalidates every callback of it and
// lets go of the held request.
func (w *cacheWarmer) stopLocked(e *warmEntry) {
	e.gen++
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
	w.releaseLocked(e)
}

// releaseLocked lets go of the held request (its body and credential
// headers) as soon as no warm of it will be sent.
func (w *cacheWarmer) releaseLocked(e *warmEntry) {
	if e.req != nil {
		w.bytes -= len(e.req.body)
		e.req = nil
	}
}

// backoff pauses every warm on credential cred until until.
func (w *cacheWarmer) backoff(cred string, until time.Time) {
	if cred == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.clock.Now()
	for c, t := range w.pausedUntil {
		if !t.After(now) {
			delete(w.pausedUntil, c)
		}
	}
	if until.After(w.pausedUntil[cred]) {
		w.pausedUntil[cred] = until
	}
}

func (w *cacheWarmer) pausedLocked(cred string, now time.Time) bool {
	return cred != "" && now.Before(w.pausedUntil[cred])
}

// arm records a stream's last request (class, start) for the table and, when
// req is not nil, keeps its cache entry warm by req.plan. startedAt is when
// the real request started: the provider measures the lifetime from there.
func (w *cacheWarmer) arm(key, class string, req *warmRequest, startedAt time.Time) {
	w.mu.Lock()
	now := w.clock.Now()
	seen := w.sweepLocked(now)
	e := w.entries[key]
	if e == nil {
		e = &warmEntry{key: key}
		e.elem = w.lru.PushFront(e)
		w.entries[key] = e
	} else {
		w.lru.MoveToFront(e.elem)
	}
	w.stopLocked(e)
	e.class, e.lastStart, e.pending = class, startedAt, class != ""
	if req != nil {
		e.req = req
		w.bytes += len(req.body)
		if !w.scheduleLocked(e, e.gen, req, 1, startedAt, startedAt.Add(cacheWarmHorizon)) {
			w.releaseLocked(e)
		}
	}
	for w.lru.Len() > cacheWarmMaxEntries || w.bytes > cacheWarmMaxBytes {
		old := w.lru.Back().Value.(*warmEntry)
		if old == e && w.lru.Len() == 1 {
			break
		}
		seen = append(seen, w.dropLocked(old, now)...)
	}
	w.mu.Unlock()
	w.table.record(seen)
}

// dropLocked forgets an entry (evicted, or the proxy is stopping). A stream
// still waiting for its next request was silent this long: the table counts
// it as one that comes back later than its age (warmTable.later), which past
// cacheWarmGone is "never".
func (w *cacheWarmer) dropLocked(e *warmEntry, now time.Time) []warmGap {
	var seen []warmGap
	if e.pending {
		e.pending = false
		seen = []warmGap{{class: e.class, d: now.Sub(e.lastStart), silent: true}}
	}
	w.stopLocked(e)
	w.lru.Remove(e.elem)
	delete(w.entries, e.key)
	return seen
}

// close stops every warm, counts the streams still waiting (dropLocked) and
// saves the table: the gaps seen since the last save, and the streams that
// never came back, would otherwise be lost with the process.
func (w *cacheWarmer) close() {
	var seen []warmGap
	w.mu.Lock()
	now := w.clock.Now()
	for _, e := range w.entries {
		seen = append(seen, w.dropLocked(e, now)...)
	}
	w.mu.Unlock()
	w.table.record(seen)
	w.table.save()
}

// sweepLocked gives up on streams silent for cacheWarmGone, at most once a
// minute: they are returned to be counted as never returning.
func (w *cacheWarmer) sweepLocked(now time.Time) []warmGap {
	if now.Sub(w.swept) < time.Minute {
		return nil
	}
	w.swept = now
	var seen []warmGap
	for _, e := range w.entries {
		if e.pending && now.Sub(e.lastStart) > cacheWarmGone {
			e.pending = false
			seen = append(seen, warmGap{class: e.class, d: -1})
		}
	}
	return seen
}

// scheduleLocked arms warm k of the chain, due `delay` after from, and
// reports whether it did: the chain is over when it did not.
func (w *cacheWarmer) scheduleLocked(e *warmEntry, gen int, req *warmRequest, k int, from, horizon time.Time) bool {
	delay := cacheWarmDelay(req.ttl)
	if delay == 0 || k >= len(req.plan) || !req.plan[k] {
		return false
	}
	due := from.Add(delay)
	// A timer that fires late (sleep, a busy session) keeps half the margin;
	// past it the warm would likely be a full-price write, not a refresh.
	deadline := due.Add((req.ttl - delay) / 2)
	if due.After(horizon) {
		return false
	}
	var fire func()
	fire = func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if e.gen != gen {
			return // stopped, or re-armed with another request
		}
		e.timer = nil
		now := w.clock.Now()
		if now.After(deadline) || (w.enabled != nil && !w.enabled()) || w.pausedLocked(req.cred, now) {
			w.releaseLocked(e)
			return
		}
		if w.real[e.key] > 0 || w.sending >= cacheWarmMaxConcurrent {
			// Never sent while a real request of the stream is in flight, nor
			// past the process-wide cap: it tries again, never past its deadline.
			if now.Add(cacheWarmBusyRetry).After(deadline) {
				w.releaseLocked(e)
				return
			}
			e.timer = w.clock.AfterFunc(cacheWarmBusyRetry, fire)
			return
		}
		again := false
		w.sending++
		func() {
			w.mu.Unlock()
			defer func() {
				w.mu.Lock()
				w.sending--
			}()
			defer func() { _ = recover() }() // a warm never takes the proxy down
			// A warm unanswered when the entry would have expired is lost either way.
			ctx, cancel := context.WithTimeout(context.Background(), req.ttl-delay)
			defer cancel()
			again = w.send(ctx, req)
		}()
		if e.gen == gen && !(again && w.scheduleLocked(e, gen, req, k+1, now, horizon)) {
			w.releaseLocked(e)
		}
	}
	// Earlier only: due and deadline stay where the lifetime puts them.
	e.timer = w.clock.AfterFunc(max(0, due.Sub(w.clock.Now())-w.jitter()), fire)
	return true
}

// cacheControlRE finds every cache_control marker (route.go's cacheControlRE
// is the same shape with the separator). A marker inside a JSON string is
// escaped and never matches.
var cacheControlMarkerRE = regexp.MustCompile(`"cache_control"\s*:\s*(\{[^{}]*\}|null)`)

// warmBody is the zero-output replay of a request that just succeeded, with
// its cache lifetime, or nil when it must not be warmed. body is the exact
// bytes sent upstream. Only top-level fields are read and cache_control
// markers scanned: no full parse on the request path.
func warmBody(body []byte) ([]byte, time.Duration) {
	root, ok := objectRoot(body)
	if !ok {
		return nil, 0
	}
	field := func(name, inner string) string {
		span, ok := jsonsplice.Field(body, root, name)
		if !ok {
			return ""
		}
		if inner == "" {
			return "present"
		}
		v, _ := jsonsplice.StringField(body, span, inner)
		return v
	}
	// Anthropic rejects max_tokens 0 with each of these; budget thinking also
	// renders its budget into the prompt, so no other cap would hit the same key.
	if field("thinking", "type") == "enabled" {
		return nil, 0
	}
	if c := field("tool_choice", "type"); c == "tool" || c == "any" {
		return nil, 0
	}
	if span, ok := jsonsplice.Field(body, root, "output_config"); ok {
		if _, has := jsonsplice.Field(body, span, "format"); has {
			return nil, 0
		}
	}
	ttl := declaredCacheTTL(body)
	if ttl == 0 {
		return nil, 0
	}
	span, ok := jsonsplice.Field(body, root, "max_tokens")
	if !ok {
		return nil, 0
	}
	out, err := jsonsplice.ReplaceRaw(body, span, []byte("0"))
	if err != nil {
		return nil, 0
	}
	if root, ok = objectRoot(out); !ok {
		return nil, 0
	}
	if span, ok := jsonsplice.Field(out, root, "stream"); ok {
		if out, err = jsonsplice.ReplaceRaw(out, span, []byte("false")); err != nil {
			return nil, 0
		}
	}
	return out, ttl
}

// declaredCacheTTL is the one lifetime every cache_control marker declares
// (top-level automatic caching included). Zero when there is no marker, one
// names a lifetime Anthropic does not document, or 5m and 1h markers are
// mixed: such a request has no single lifetime to value a miss or class the
// stream by, so it is not warmed. The simulator applies the same rule to the
// usage it reads (a stream is 1h only when every token written was 1h, 5m
// only when every one was 5m; none of the measured requests mixed them).
func declaredCacheTTL(body []byte) time.Duration {
	ttl := time.Duration(0)
	for _, m := range cacheControlMarkerRE.FindAllSubmatch(body, -1) {
		if string(m[1]) == "null" {
			continue
		}
		var marker struct {
			TTL *string `json:"ttl"`
		}
		if json.Unmarshal(m[1], &marker) != nil {
			return 0
		}
		d := 5 * time.Minute
		switch {
		case marker.TTL == nil || *marker.TTL == "5m":
		case *marker.TTL == "1h":
			d = time.Hour
		default:
			return 0
		}
		if ttl != 0 && d != ttl {
			return 0
		}
		ttl = d
	}
	return ttl
}

// cacheWarmCosts is what one warm costs and what one avoided miss is worth,
// at the catalog list price of the model sent: P = cache read + cache write
// tokens of the last response, T its uncached tail. A warm reads P and pays T
// as input; a miss rewrites P instead of reading it. Subscription traffic is
// valued at list prices as the equivalent of plan usage.
func cacheWarmCosts(usage providers.UsageObservation, meta providers.RequestMetadata, authMode AuthMode, ttl time.Duration) (full, warm float64, ok bool) {
	if !usage.Complete() || usage.ProviderError {
		return 0, 0, false
	}
	prefix := usage.CachedInputTokens + usage.CacheCreationInputTokens
	tail := usage.InputTokens - prefix
	if prefix <= 0 || tail < 0 {
		return 0, 0, false
	}
	price, _ := statsPriceForUsage(meta, usage, string(authMode))
	if price == (cost.Price{}) || !cost.ValidPrice(price) {
		return 0, 0, false
	}
	price = cost.ForInputTokens(price, usage.InputTokens)
	write := price.CacheWritePerMillion
	if ttl == time.Hour {
		write = price.CacheWrite1hPerMillion
	}
	if write <= price.CacheReadPerMillion || price.CacheReadPerMillion <= 0 || (tail > 0 && price.InputPerMillion <= 0) {
		return 0, 0, false
	}
	m := float64(prefix) / 1e6
	full = (write - price.CacheReadPerMillion) * m
	warm = price.CacheReadPerMillion*m + price.InputPerMillion*float64(tail)/1e6
	return full, warm, true
}

// cacheWarmEligible: Anthropic Messages on Anthropic's own API, as the agent
// sent it unencoded and without the request-wide pass-through opt-out.
func cacheWarmEligible(r *http.Request, adapter providers.Adapter, meta providers.RequestMetadata, originKnown func(string, *url.URL) bool, upstream *url.URL) bool {
	if adapter.Name() != "anthropic" || meta.Provider != "anthropic" || !strings.HasSuffix(meta.Endpoint, "/messages") {
		return false
	}
	if strings.TrimSpace(r.Header.Get("x-cave-transforms")) == "caveman.pass-through.v1" {
		return false
	}
	if enc := strings.TrimSpace(r.Header.Get("Content-Encoding")); enc != "" && !strings.EqualFold(enc, "identity") {
		return false
	}
	return originKnown(meta.Provider, upstream)
}

// credentialKey names the upstream credential without holding it.
func credentialKey(h http.Header) string {
	v := h.Get("x-api-key") + "\x00" + h.Get("Authorization")
	if v == "\x00" {
		return ""
	}
	return shortHash(v)
}

func shortHash(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:8])
}

// backoffUntil is how long a 429 pauses warming on its credential: the
// response's Retry-After, else its anthropic-ratelimit-unified-reset (a
// subscription's limit window: epoch seconds, or RFC 3339), when that is
// within eight days; otherwise one hour for a subscription login, whose
// limits reset on rolling windows and not at midnight, and one minute for an
// API key.
func backoffUntil(now time.Time, h http.Header, authMode AuthMode) time.Time {
	var until time.Time
	if s, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After"))); err == nil && s > 0 {
		until = now.Add(time.Duration(s) * time.Second)
	}
	reset := strings.TrimSpace(h.Get("anthropic-ratelimit-unified-reset"))
	at, err := time.Parse(time.RFC3339, reset)
	if s, serr := strconv.ParseInt(reset, 10, 64); serr == nil {
		at, err = time.Unix(s, 0), nil
	}
	if err == nil && until.IsZero() {
		until = at // Retry-After, when the provider sends one, is the shorter and exact wait
	}
	if until.After(now) && until.Before(now.Add(8*24*time.Hour)) {
		return until
	}
	if authMode == AuthModeSubscription || authMode == AuthModeOAuth {
		return now.Add(time.Hour)
	}
	return now.Add(time.Minute)
}

// toolStop reports whether a response asked for a tool, read off the bytes
// as they stream past (JSON or SSE), never buffering them: it keeps the last
// len(needle)-1 bytes, so the needle is found across any number of reads.
// It reads plain bytes only; an encoded response is not classed (armCacheWarm).
type toolStop struct {
	tail []byte
	seen bool
}

var toolStopNeedle = []byte(`"stop_reason":"tool_use"`)

func (t *toolStop) Write(p []byte) (int, error) {
	if t.seen {
		return len(p), nil
	}
	keep := len(toolStopNeedle) - 1
	t.tail = append(t.tail, p[:min(len(p), keep)]...)
	if bytes.Contains(t.tail, toolStopNeedle) || bytes.Contains(p, toolStopNeedle) {
		t.seen, t.tail = true, nil
		return len(p), nil
	}
	switch {
	case len(p) >= keep:
		t.tail = append(t.tail[:0], p[len(p)-keep:]...)
	case len(t.tail) > keep:
		t.tail = append(t.tail[:0], t.tail[len(t.tail)-keep:]...)
	}
	return len(p), nil
}

// warmAnswer is one answered request of a stream, as armCacheWarm needs it.
type warmAnswer struct {
	key      string
	subagent bool
	start    time.Time
	// ok: a 2xx answer read to its end, with no retrieve loop behind it.
	ok       bool
	adapter  providers.Adapter
	meta     providers.RequestMetadata
	authMode AuthMode
	upstream *url.URL
	header   http.Header // as sent upstream
	body     []byte      // as sent upstream
	usage    providers.UsageObservation
	// tool: the answer asked for a tool; unknown when the answer was encoded.
	tool, encoded                      bool
	mode, label, agent, session, basis string
}

// armCacheWarm plans the warm chain of the stream whose request was just
// answered (the exact bytes and headers that were answered, at zero output,
// for the stream's class and the prices of the model sent) and says in one
// log line what it decided and why: the session as a hash, never content or
// a credential.
func (s *Server) armCacheWarm(r *http.Request, a warmAnswer) {
	class, ttl, warms, skip := "", time.Duration(0), 0, ""
	switch {
	case !a.ok:
		skip = "response_error"
	case !s.warmer.enabled():
		skip = "off"
	case !cacheWarmEligible(r, a.adapter, a.meta, s.warmOrigin, a.upstream):
		skip = "not_anthropic_api"
	default:
		var body []byte
		body, ttl = warmBody(a.body)
		wrote5m, wrote1h := a.usage.CacheCreation5mTokens > 0, a.usage.CacheCreation1hTokens > 0
		switch {
		case body == nil:
			skip = "not_replayable"
		case (ttl == time.Hour && wrote5m) || (ttl == 5*time.Minute && wrote1h):
			skip = "lifetime_mismatch"
		default:
			var req *warmRequest
			var plan []bool
			class = warmClassKey(a.subagent, a.tool, ttl)
			full, cost, priced := cacheWarmCosts(a.usage, a.meta, a.authMode, ttl)
			if priced {
				plan = s.warmer.table.plan(class, ttl, cacheWarmHorizon, full, cost)
				if a.encoded {
					// The answer's stop reason was not read: plan for whichever of
					// the two classes warms less, and count the gap for neither.
					other := s.warmer.table.plan(warmClassKey(a.subagent, !a.tool, ttl), ttl, cacheWarmHorizon, full, cost)
					if warmCount(other) < warmCount(plan) {
						plan = other
					}
					class = ""
				}
			}
			switch warms = warmCount(plan); {
			case !priced:
				skip = "unpriced"
			case warms == 0:
				skip = "not_worth_it"
			default:
				req = &warmRequest{
					url: a.upstream.String(), header: a.header.Clone(), body: body, ttl: ttl,
					adapter: a.adapter, meta: a.meta, authMode: a.authMode, mode: a.mode,
					label: a.label, agent: a.agent, session: a.session, basis: a.basis,
					cred: credentialKey(a.header), plan: plan,
				}
			}
			s.warmer.arm(a.key, class, req, a.start)
			if a.encoded {
				class = "unclassed"
			}
		}
	}
	if s.logger != nil {
		id := a.session
		if id == "" {
			id = a.key // the agent's own session header: no session id is stored for it
		}
		level := slog.LevelInfo
		if skip != "" {
			level = slog.LevelDebug // most requests plan nothing; only a planned chain is news
		}
		s.logger.Log(r.Context(), level, "cache warm plan", "session", shortHash(id), "model", a.meta.Model, "class", class,
			"ttl", ttl.String(), "warms", warms, "skip", skip)
	}
}

// warmCount is how many warms a plan sends: the chain stops at its first no.
func warmCount(plan []bool) int {
	n := 0
	for n+1 < len(plan) && plan[n+1] {
		n++
	}
	return n
}

// Close stops cache warming and saves what the warm table learned. Call it
// when the proxy shuts down.
// sendCacheWarm sends one warm and records it. It reports whether warming
// should go on: only a clean answer that read the cache does. Any error, or
// a warm that found the entry gone, stops the stream's warming; a 429, a 529
// or a 5xx also pauses its credential.
func (s *Server) sendCacheWarm(ctx context.Context, req *warmRequest) bool {
	start := time.Now()
	header := req.header.Clone()
	header.Del("Accept-Encoding") // the transport decodes; usage reads plain JSON
	resp, err := s.doUpstream(ctx, func() (*http.Request, error) {
		out, err := http.NewRequestWithContext(ctx, http.MethodPost, req.url, bytes.NewReader(req.body))
		if err != nil {
			return nil, err
		}
		out.Header = header.Clone()
		return out, nil
	})
	status, errCode := http.StatusBadGateway, "cave_upstream_unavailable"
	usage := providers.UsageObservation{CacheStatus: "unknown"}
	var n int64
	if err == nil {
		scanner := req.adapter.NewUsageScanner(resp.Header)
		n, err = io.Copy(scanner, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		status, errCode, usage = resp.StatusCode, "", scanner.Usage()
		switch {
		case err != nil:
			errCode = "cave_upstream_body_read_failed"
		case status >= 400:
			errCode = "provider_" + itoa(int64(status))
		case usage.ProviderError:
			errCode = "provider_error"
		}
		if status == http.StatusTooManyRequests && s.warmer != nil {
			s.warmer.backoff(req.cred, backoffUntil(s.warmer.clock.Now(), resp.Header, req.authMode))
		}
		if status >= 500 && s.warmer != nil {
			// Overloaded (529) or failing: more full-context warms only add load.
			until := s.warmer.clock.Now().Add(cacheWarmOverloadPause)
			if resp.Header.Get("Retry-After") != "" {
				until = backoffUntil(s.warmer.clock.Now(), resp.Header, req.authMode)
			}
			s.warmer.backoff(req.cred, until)
		}
	}
	s.recordCacheWarm(start, req, status, errCode, n, usage)
	if s.logger != nil && errCode != "" {
		s.logger.Info("cache warm stopped", "reason", errCode, "session", shortHash(req.session), "model", req.meta.Model)
	}
	return errCode == "" && usage.CachedInputTokens > 0
}

// recordCacheWarm writes the warm's own row: its real provider usage and list
// price, tagged cache-warm, never a saving, never a user request for the
// harm tripwire, and never sent to Cloud.
func (s *Server) recordCacheWarm(start time.Time, req *warmRequest, status int, errCode string, n int64, usage providers.UsageObservation) {
	if s.sink == nil {
		return
	}
	price := standalonePriceForUsage(req.meta, usage)
	if !providers.ListPriceEligible(req.meta.Provider, string(req.authMode)) || !usage.Complete() {
		price = cost.Price{}
	}
	in, out, cached := costBreakdown(req.meta.Provider, price, usage)
	cacheStatus := usage.CacheStatus
	if cacheStatus == "" {
		cacheStatus = "unknown"
	}
	row := RequestRecord{
		Timestamp:                time.Now().UTC().Format("2006-01-02 15:04:05.000"),
		RequestID:                id.NewUUIDv7(),
		TraceID:                  id.NewUUIDv7(),
		Label:                    labelOrDefault(req.label, "local"),
		SessionID:                req.session,
		SessionCorrelationBasis:  req.basis,
		AgentSlug:                labelOrDefault(req.agent, "unlabeled-agent"),
		Provider:                 req.meta.Provider,
		Model:                    req.meta.Model,
		RouteFrom:                req.meta.Model,
		RouteTo:                  req.meta.Model,
		ProviderOriginKnown:      true,
		Endpoint:                 req.meta.Endpoint,
		StatusCode:               status,
		ErrorCode:                errCode,
		LatencyMS:                time.Since(start).Milliseconds(),
		TTFBMS:                   time.Since(start).Milliseconds(),
		RequestBytes:             len(req.body),
		ResponseBytes:            n,
		InputTokens:              usage.InputTokens,
		OutputTokens:             usage.OutputTokens,
		CachedInputTokens:        usage.CachedInputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		CacheCreation1hTokens:    usage.CacheCreation1hTokens,
		ReasoningTokens:          usage.ReasoningTokens,
		TotalCostUSD:             cost.RoundUSD(in + out + cached),
		Basis:                    "inferred",
		TokenUsageBasis:          standaloneUsageBasis(usage),
		AuthMode:                 string(req.authMode),
		RuntimeMode:              req.mode,
		OptimizationIDs:          []string{cacheWarmOptimizerID},
		CacheStatus:              cacheStatus,
	}
	recordStatsPrice(&row, req.meta, usage)
	s.sink.Record(row)
}
