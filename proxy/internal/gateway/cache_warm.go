package gateway

import (
	"bytes"
	"container/list"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
	"github.com/JuliusBrussee/caveman/shared/platform/cost"
	"github.com/JuliusBrussee/caveman/shared/platform/id"
)

// Prompt-cache warming, after Pi's cache-warmer (streaming mode) and the SDK
// port of it. While an agent pauses (the user is away, a long tool runs, it
// waits on subagents) the Anthropic cache entry its last request wrote can
// expire, and the next request pays a full cache write again. Replaying that
// exact request with `max_tokens: 0` shortly before the entry expires reads
// (and so refreshes) the entry and bills no output.
//
// Every input fails closed: an unknown lifetime, an unknown price, a request
// that cannot be replayed at zero output without changing its cache key, or an
// origin other than Anthropic's own API means no warm. The expected saving is
// a catalog estimate and is never booked as a saving.
const (
	cacheWarmOptimizerID = "cache-warm"
	// Pi's threshold: warm only when the avoided miss beats the warm by this much.
	cacheWarmMinSavingsUSD = 0.05
	// Pi: warming never continues past this long after the real request that
	// started it.
	cacheWarmHorizon = 60 * time.Minute
	// A warm due while a real request of the session is in flight waits this
	// long and tries again, until its deadline.
	cacheWarmBusyRetry  = 5 * time.Second
	cacheWarmMaxEntries = 1024
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

func (systemClock) Now() time.Time { return time.Now() }
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
}

// warmEntry is the warming state of one (session, model).
type warmEntry struct {
	key, session string
	gen          int
	timer        interface{ Stop() bool }
	cancel       context.CancelFunc // the warm in flight
	done         chan struct{}      // closed once it settled
	elem         *list.Element
}

type cacheWarmer struct {
	mu      sync.Mutex
	clock   warmClock
	quiesce time.Duration
	// enabled is the waste-fixes module switch plus the env kill switch, read
	// again before every warm so switching it off stops warming.
	enabled func() bool
	send    func(ctx context.Context, req *warmRequest) bool
	entries map[string]*warmEntry
	lru     *list.List
	// real counts the real requests in flight per session (any model).
	real map[string]int
}

func newCacheWarmer(enabled func() bool, send func(context.Context, *warmRequest) bool) *cacheWarmer {
	return &cacheWarmer{
		clock: systemClock{}, quiesce: 5 * time.Second, enabled: enabled, send: send,
		entries: map[string]*warmEntry{}, lru: list.New(), real: map[string]int{},
	}
}

func warmKey(session, model string) string { return session + "\x00" + model }

// begin marks a real request of session on model. It stops that entry's timer
// and waits up to the quiesce grace for a warm in flight, then abandons it, so
// a real request never races its own warm. The returned func ends the request.
func (w *cacheWarmer) begin(session, model string) func() {
	w.mu.Lock()
	w.real[session]++
	var done chan struct{}
	var cancel context.CancelFunc
	if e := w.entries[warmKey(session, model)]; e != nil {
		w.stopLocked(e)
		done, cancel = e.done, e.cancel
	}
	w.mu.Unlock()
	if done != nil {
		grace := time.NewTimer(w.quiesce)
		select {
		case <-done:
		case <-grace.C:
		}
		grace.Stop()
		cancel()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			w.mu.Lock()
			if w.real[session]--; w.real[session] <= 0 {
				delete(w.real, session)
			}
			w.mu.Unlock()
		})
	}
}

// stopLocked cancels the entry's timer and invalidates every callback of it.
func (w *cacheWarmer) stopLocked(e *warmEntry) {
	e.gen++
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
}

// arm keeps req's cache entry warm. startedAt is when the real request started:
// the provider measures the lifetime from there.
func (w *cacheWarmer) arm(session, model string, req *warmRequest, startedAt time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	key := warmKey(session, model)
	e := w.entries[key]
	if e == nil {
		e = &warmEntry{key: key, session: session}
		e.elem = w.lru.PushFront(e)
		w.entries[key] = e
		for w.lru.Len() > cacheWarmMaxEntries {
			old := w.lru.Back().Value.(*warmEntry)
			w.stopLocked(old)
			if old.cancel != nil {
				old.cancel()
			}
			w.lru.Remove(old.elem)
			delete(w.entries, old.key)
		}
	} else {
		w.lru.MoveToFront(e.elem)
	}
	w.stopLocked(e)
	w.scheduleLocked(e, e.gen, req, startedAt, startedAt.Add(cacheWarmHorizon))
}

func (w *cacheWarmer) scheduleLocked(e *warmEntry, gen int, req *warmRequest, from, horizon time.Time) {
	delay := cacheWarmDelay(req.ttl)
	if delay == 0 {
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
		if e.gen != gen || now.After(deadline) || (w.enabled != nil && !w.enabled()) {
			w.mu.Unlock()
			return
		}
		if w.real[e.session] > 0 {
			// Never concurrent with a real request of the session.
			if now.Add(cacheWarmBusyRetry).After(deadline) {
				w.mu.Unlock()
				return
			}
			e.timer = w.clock.AfterFunc(cacheWarmBusyRetry, fire)
			w.mu.Unlock()
			return
		}
		e.timer = nil
		// A warm unanswered when the entry would have expired is lost either way.
		ctx, cancel := context.WithTimeout(context.Background(), req.ttl-delay)
		done := make(chan struct{})
		e.cancel, e.done = cancel, done
		w.mu.Unlock()

		again := w.send(ctx, req)
		cancel()

		w.mu.Lock()
		if e.done == done {
			e.cancel, e.done = nil, nil
		}
		if again && e.gen == gen {
			w.scheduleLocked(e, gen, req, now, horizon)
		}
		w.mu.Unlock()
		close(done)
	}
	e.timer = w.clock.AfterFunc(max(0, due.Sub(w.clock.Now())), fire)
}

// warmPlan is the zero-output replay of a request that just succeeded, or nil
// when it must not be warmed. body is the exact bytes sent upstream.
func warmPlan(body []byte, usage providers.UsageObservation, meta providers.RequestMetadata, authMode AuthMode) ([]byte, time.Duration) {
	root, ok := objectRoot(body)
	if !ok {
		return nil, 0
	}
	var top struct {
		Thinking *struct {
			Type string `json:"type"`
		} `json:"thinking"`
		ToolChoice *struct {
			Type string `json:"type"`
		} `json:"tool_choice"`
		OutputConfig map[string]json.RawMessage `json:"output_config"`
	}
	if json.Unmarshal(body, &top) != nil {
		return nil, 0
	}
	// Anthropic rejects max_tokens 0 with each of these; budget thinking also
	// renders its budget into the prompt, so no other cap would hit the same key.
	if top.Thinking != nil && top.Thinking.Type == "enabled" {
		return nil, 0
	}
	if top.ToolChoice != nil && (top.ToolChoice.Type == "tool" || top.ToolChoice.Type == "any") {
		return nil, 0
	}
	if _, ok := top.OutputConfig["format"]; ok {
		return nil, 0
	}
	ttl := declaredCacheTTL(body)
	if ttl == 0 {
		return nil, 0
	}
	// The response must agree: a 5-minute write under all-1h markers means the
	// lifetime is not what the request says.
	if ttl == time.Hour && usage.CacheCreation5mTokens > 0 {
		return nil, 0
	}
	if !cacheWarmPays(usage, meta, authMode, ttl) {
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

// declaredCacheTTL is the shortest lifetime any cache_control marker of the
// request declares (top-level automatic caching included): warming on the
// shortest one keeps every entry warm. Zero when there is no marker or a
// marker names a lifetime Anthropic does not document.
func declaredCacheTTL(body []byte) time.Duration {
	if !bytes.Contains(body, []byte(`"cache_control"`)) {
		return 0
	}
	var doc any
	if json.Unmarshal(body, &doc) != nil {
		return 0
	}
	ttl, bad := time.Duration(0), false
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, child := range v {
				if k != "cache_control" {
					walk(child)
					continue
				}
				marker, _ := child.(map[string]any)
				if marker == nil {
					continue // cache_control: null marks nothing
				}
				var d time.Duration
				switch marker["ttl"] {
				case nil, "5m":
					d = 5 * time.Minute
				case "1h":
					d = time.Hour
				default:
					bad = true
				}
				if ttl == 0 || d < ttl {
					ttl = d
				}
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(doc)
	if bad {
		return 0
	}
	return ttl
}

// cacheWarmPays is Pi's economics on the shared catalog for the model actually
// sent: the cached prefix P (cache read + cache write of the last response)
// and the uncached tail T. Without a warm the next request rewrites P; with
// one it reads P, and the warm itself reads P and pays T as input. Warm only
// when (write - read) * P - (read * P + input * T) >= $0.05. Subscription
// traffic is judged at list prices as the proxy for plan usage.
func cacheWarmPays(usage providers.UsageObservation, meta providers.RequestMetadata, authMode AuthMode, ttl time.Duration) bool {
	_, ok := cacheWarmSavings(usage, meta, authMode, ttl)
	return ok
}

func cacheWarmSavings(usage providers.UsageObservation, meta providers.RequestMetadata, authMode AuthMode, ttl time.Duration) (float64, bool) {
	if !usage.Complete() || usage.ProviderError {
		return 0, false
	}
	prefix := usage.CachedInputTokens + usage.CacheCreationInputTokens
	tail := usage.InputTokens - prefix
	if prefix <= 0 || tail < 0 {
		return 0, false
	}
	price, _ := statsPriceForUsage(meta, usage, string(authMode))
	if price == (cost.Price{}) || !cost.ValidPrice(price) {
		return 0, false
	}
	price = cost.ForInputTokens(price, usage.InputTokens)
	write := price.CacheWritePerMillion
	if ttl == time.Hour {
		write = price.CacheWrite1hPerMillion
	}
	if write <= 0 || price.CacheReadPerMillion <= 0 || (tail > 0 && price.InputPerMillion <= 0) {
		return 0, false
	}
	miss := cost.EstimateUSD(cost.Price{CacheWritePerMillion: write}, cost.Usage{CacheCreationTokens: prefix})
	hit := cost.EstimateUSD(cost.Price{CacheReadPerMillion: price.CacheReadPerMillion}, cost.Usage{CachedInputTokens: prefix})
	warm := hit + cost.EstimateUSD(cost.Price{InputPerMillion: price.InputPerMillion}, cost.Usage{InputTokens: tail})
	saving := miss - hit - warm
	return saving, saving >= cacheWarmMinSavingsUSD
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

// sendCacheWarm sends one warm and records it. It reports whether warming
// should go on: only a clean answer that read the cache does. Any error, 429
// included, or a warm that found the entry gone stops the session's warming.
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
