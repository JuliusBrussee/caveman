package gateway

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
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
// of it (whose parent is the session's key).
type warmEntry struct {
	key, parent string
	gen         int
	timer       interface{ Stop() bool }
	req         *warmRequest
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
	// pausedUntil holds 429 backoffs per credential hash.
	pausedUntil map[string]time.Time
	swept       time.Time
}

func newCacheWarmer(enabled func() bool, send func(context.Context, *warmRequest) bool, table *warmTable) *cacheWarmer {
	return &cacheWarmer{
		clock: systemClock{}, enabled: enabled, send: send, table: table,
		entries: map[string]*warmEntry{}, lru: list.New(), real: map[string]int{}, pausedUntil: map[string]time.Time{},
	}
}

// begin marks a real request of stream key (parent: the session a subagent
// belongs to). It stops the stream's warming and, for a session, every
// subagent of it: a parent that is talking again has its children's results.
// It never waits: a warm already in flight finishes in the background. The
// gap since the stream's last request is counted in the table. The returned
// func ends the request.
func (w *cacheWarmer) begin(key string) func() {
	w.mu.Lock()
	now := w.clock.Now()
	w.real[key]++
	if e := w.entries[key]; e != nil {
		if e.pending {
			e.pending = false
			w.table.observe(e.class, now.Sub(e.lastStart))
		}
		w.stopLocked(e)
	}
	for _, e := range w.entries {
		if e.parent == key {
			w.stopLocked(e)
		}
	}
	w.mu.Unlock()
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
func (w *cacheWarmer) arm(key, parent, class string, req *warmRequest, startedAt time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sweepLocked()
	e := w.entries[key]
	if e == nil {
		e = &warmEntry{key: key}
		e.elem = w.lru.PushFront(e)
		w.entries[key] = e
	} else {
		w.lru.MoveToFront(e.elem)
	}
	w.stopLocked(e)
	e.parent, e.class, e.lastStart, e.pending = parent, class, startedAt, class != ""
	if req != nil {
		e.req = req
		w.bytes += len(req.body)
		w.scheduleLocked(e, e.gen, req, 1, startedAt, startedAt.Add(cacheWarmHorizon))
	}
	for w.lru.Len() > cacheWarmMaxEntries || w.bytes > cacheWarmMaxBytes {
		old := w.lru.Back().Value.(*warmEntry)
		if old == e && w.lru.Len() == 1 {
			break
		}
		w.evictLocked(old)
	}
}

func (w *cacheWarmer) evictLocked(e *warmEntry) {
	w.stopLocked(e)
	w.lru.Remove(e.elem)
	delete(w.entries, e.key)
}

// sweepLocked counts streams silent for cacheWarmGone as never returning, at
// most once a minute.
func (w *cacheWarmer) sweepLocked() {
	now := w.clock.Now()
	if now.Sub(w.swept) < time.Minute {
		return
	}
	w.swept = now
	for _, e := range w.entries {
		if e.pending && now.Sub(e.lastStart) > cacheWarmGone {
			e.pending = false
			w.table.observe(e.class, -1)
		}
	}
}

// scheduleLocked arms warm k of the chain, due `delay` after from.
func (w *cacheWarmer) scheduleLocked(e *warmEntry, gen int, req *warmRequest, k int, from, horizon time.Time) {
	delay := cacheWarmDelay(req.ttl)
	if delay == 0 || k >= len(req.plan) || !req.plan[k] {
		return
	}
	due := from.Add(delay)
	// A timer that fires late (sleep, a busy session) keeps half the margin;
	// past it the warm would likely be a full-price write, not a refresh.
	deadline := due.Add((req.ttl - delay) / 2)
	if due.After(horizon) {
		return
	}
	var fire func()
	fire = func() {
		w.mu.Lock()
		now := w.clock.Now()
		if e.gen != gen || now.After(deadline) || (w.enabled != nil && !w.enabled()) || w.pausedLocked(req.cred, now) {
			w.mu.Unlock()
			return
		}
		if w.real[e.key] > 0 {
			// Never sent while a real request of the stream is in flight.
			if now.Add(cacheWarmBusyRetry).After(deadline) {
				w.mu.Unlock()
				return
			}
			e.timer = w.clock.AfterFunc(cacheWarmBusyRetry, fire)
			w.mu.Unlock()
			return
		}
		e.timer = nil
		w.mu.Unlock()

		// A warm unanswered when the entry would have expired is lost either way.
		ctx, cancel := context.WithTimeout(context.Background(), req.ttl-delay)
		again := w.send(ctx, req)
		cancel()

		w.mu.Lock()
		if again && e.gen == gen {
			w.scheduleLocked(e, gen, req, k+1, now, horizon)
		}
		w.mu.Unlock()
	}
	e.timer = w.clock.AfterFunc(max(0, due.Sub(w.clock.Now())), fire)
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

// declaredCacheTTL is the shortest lifetime any cache_control marker declares
// (top-level automatic caching included): warming on the shortest one keeps
// every entry warm. Zero when there is no marker or one names a lifetime
// Anthropic does not document.
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
		if ttl == 0 || d < ttl {
			ttl = d
		}
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
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:8])
}

// backoffUntil is how long a 429 pauses warming on its credential: a
// subscription login for the rest of the local day (its usage limit is the
// day's budget), an API key for Retry-After (default one minute).
func backoffUntil(now time.Time, h http.Header, authMode AuthMode) time.Time {
	if authMode == AuthModeSubscription || authMode == AuthModeOAuth {
		y, m, d := now.Date()
		return time.Date(y, m, d+1, 0, 0, 0, 0, now.Location())
	}
	if s, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After"))); err == nil && s > 0 {
		return now.Add(time.Duration(s) * time.Second)
	}
	return now.Add(time.Minute)
}

// toolStop reports whether a response asked for a tool, read off the bytes
// as they stream past (JSON or SSE), never buffering them.
type toolStop struct {
	tail []byte
	seen bool
}

var toolStopNeedle = []byte(`"stop_reason":"tool_use"`)

func (t *toolStop) Write(p []byte) (int, error) {
	if t.seen {
		return len(p), nil
	}
	head := p
	if len(head) > len(toolStopNeedle) {
		head = head[:len(toolStopNeedle)]
	}
	if bytes.Contains(append(t.tail, head...), toolStopNeedle) || bytes.Contains(p, toolStopNeedle) {
		t.seen = true
		return len(p), nil
	}
	keep := p
	if len(keep) > len(toolStopNeedle) {
		keep = keep[len(keep)-len(toolStopNeedle):]
	}
	t.tail = append(t.tail[:0], keep...)
	return len(p), nil
}

// sendCacheWarm sends one warm and records it. It reports whether warming
// should go on: only a clean answer that read the cache does. Any error, or
// a warm that found the entry gone, stops the stream's warming; a 429 also
// pauses its credential.
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
	}
	s.recordCacheWarm(start, req, status, errCode, n, usage)
	if s.logger != nil && errCode != "" {
		s.logger.Info("cache warm stopped", "reason", errCode, "session_id", req.session, "model", req.meta.Model)
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
