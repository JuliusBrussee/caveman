package middleware

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// outcome is what one request did, for metrics and the audit log. It never
// holds content, raw scope values, handles or credentials: scope and handle are
// truncated hashes.
type outcome struct {
	route, principal, scope, handle string
	mechanism                       string // how principal authenticated
	client                          string // Caveman-Middleware-Client, logged only (§14)
	status                          int
	code, planStatus, reason        string
	requestBytes, responseBytes     int
	deleted                         *DeleteCounts
}

var latencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type histogram struct {
	counts []int64 // per bucket, cumulative at write time
	sum    float64
	n      int64
}

type metrics struct {
	mu           sync.Mutex
	requests     map[[3]string]int64 // route, status, code
	decisions    map[[2]string]int64 // plan status, reason
	latency      map[string]*histogram
	unauthorized int64
	// rebaselines counts optimizes that started a new epoch; plaintextRefused
	// and plaintextAllowed count retrieves of plaintext originals under a key.
	rebaselines, plaintextRefused, plaintextAllowed atomic.Int64
}

var knownRoutes = []string{"capabilities", "optimize", "retrieve", "receipts", "sessions/delete"}

// record counts one finished request and writes its audit line.
func (r *Runtime) record(o *outcome, start time.Time) {
	elapsed := time.Since(start)
	route := o.route
	if !slices.Contains(knownRoutes, route) {
		route = "other"
	}
	code := o.code
	if code == "" {
		code = "ok"
	}
	m := &r.metrics
	m.mu.Lock()
	if m.requests == nil {
		m.requests, m.decisions, m.latency = map[[3]string]int64{}, map[[2]string]int64{}, map[string]*histogram{}
	}
	m.requests[[3]string{route, strconv.Itoa(o.status), code}]++
	if o.planStatus != "" {
		m.decisions[[2]string{o.planStatus, o.reason}]++
	}
	if o.status == 401 {
		m.unauthorized++
	}
	h := m.latency[route]
	if h == nil {
		h = &histogram{counts: make([]int64, len(latencyBuckets))}
		m.latency[route] = h
	}
	for i, bound := range latencyBuckets {
		if elapsed.Seconds() <= bound {
			h.counts[i]++
		}
	}
	h.sum += elapsed.Seconds()
	h.n++
	m.mu.Unlock()
	if r.cfg.Logger == nil {
		return
	}
	attrs := []any{"route", route, "status", o.status, "code", code, "principal", o.principal, "auth", o.mechanism, "scope", o.scope,
		"request_bytes", o.requestBytes, "response_bytes", o.responseBytes, "latency_ms", elapsed.Milliseconds()}
	if o.planStatus != "" {
		attrs = append(attrs, "plan_status", o.planStatus, "reason", o.reason)
	}
	if o.handle != "" {
		attrs = append(attrs, "handle", o.handle)
	}
	if o.client != "" {
		attrs = append(attrs, "client", o.client)
	}
	if o.deleted != nil {
		attrs = append(attrs, "deleted_scopes", o.deleted.Scopes, "deleted_grants", o.deleted.Grants, "deleted_originals", o.deleted.Originals)
	}
	r.cfg.Logger.Info("middleware request", attrs...)
}

// logSafe keeps a client-supplied header to 256 printable product/version
// characters, so it cannot forge log structure.
func logSafe(value string) string {
	value = strings.Map(func(c rune) rune {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune(" ._/+-", c) {
			return c
		}
		return '?'
	}, value)
	return value[:min(len(value), 256)]
}

// WriteMetrics appends the middleware series in Prometheus text format.
func (r *Runtime) WriteMetrics(w io.Writer) {
	m := &r.metrics
	m.mu.Lock()
	requests, decisions, latency, unauthorized := sortedKeys(m.requests), sortedKeys(m.decisions), sortedKeys(m.latency), m.unauthorized
	var b strings.Builder
	series := func(name, kind, help string) { fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind) }
	series("caveman_middleware_requests_total", "counter", "Middleware requests by route, HTTP status and error code (ok on success).")
	for _, k := range requests {
		fmt.Fprintf(&b, "caveman_middleware_requests_total{route=%q,status=%q,code=%q} %d\n", k[0], k[1], k[2], m.requests[k])
	}
	series("caveman_middleware_decisions_total", "counter", "Optimize plans by plan status and reason.")
	for _, k := range decisions {
		fmt.Fprintf(&b, "caveman_middleware_decisions_total{status=%q,reason=%q} %d\n", k[0], k[1], m.decisions[k])
	}
	series("caveman_middleware_request_duration_seconds", "histogram", "Middleware request latency, queue wait included.")
	for _, route := range latency {
		h := m.latency[route]
		for i, bound := range latencyBuckets {
			fmt.Fprintf(&b, "caveman_middleware_request_duration_seconds_bucket{route=%q,le=%q} %d\n", route, strconv.FormatFloat(bound, 'g', -1, 64), h.counts[i])
		}
		fmt.Fprintf(&b, "caveman_middleware_request_duration_seconds_bucket{route=%q,le=\"+Inf\"} %d\n", route, h.n)
		fmt.Fprintf(&b, "caveman_middleware_request_duration_seconds_sum{route=%q} %g\n", route, h.sum)
		fmt.Fprintf(&b, "caveman_middleware_request_duration_seconds_count{route=%q} %d\n", route, h.n)
	}
	m.mu.Unlock()
	series("caveman_middleware_unauthorized_total", "counter", "Middleware requests rejected for a missing or unknown credential.")
	fmt.Fprintf(&b, "caveman_middleware_unauthorized_total %d\n", unauthorized)
	series("caveman_middleware_queue_depth", "gauge", "Requests holding a middleware queue slot.")
	fmt.Fprintf(&b, "caveman_middleware_queue_depth{queue=\"optimize\"} %d\ncaveman_middleware_queue_depth{queue=\"retrieve\"} %d\n", len(r.queue), len(r.retrieveQueue))
	series("caveman_middleware_queue_capacity", "gauge", "Configured middleware queue slots.")
	fmt.Fprintf(&b, "caveman_middleware_queue_capacity{queue=\"optimize\"} %d\ncaveman_middleware_queue_capacity{queue=\"retrieve\"} %d\n", cap(r.queue), cap(r.retrieveQueue))
	series("caveman_middleware_epoch_rebaselines_total", "counter", "Optimizes whose history no longer extended the stored manifest and started a new epoch (trimmed or summarized history, nested agents).")
	fmt.Fprintf(&b, "caveman_middleware_epoch_rebaselines_total %d\n", m.rebaselines.Load())
	series("caveman_middleware_plaintext_originals_total", "counter", "Retrieves of originals stored in plaintext while a key is configured: refused, or allowed by allow_plaintext_originals.")
	fmt.Fprintf(&b, "caveman_middleware_plaintext_originals_total{outcome=\"refused\"} %d\ncaveman_middleware_plaintext_originals_total{outcome=\"allowed\"} %d\n", m.plaintextRefused.Load(), m.plaintextAllowed.Load())
	series("caveman_middleware_expiry_backlog", "gauge", "Expired scopes, plans and receipts the last sweep left unreclaimed; growth means expiry outpaces the sweep.")
	fmt.Fprintf(&b, "caveman_middleware_expiry_backlog %d\n", r.backlog.Load())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if rows, size, err := r.cfg.Store.MiddlewareUsage(ctx); err == nil {
		limits := r.cfg.Capacity
		series("caveman_middleware_store_rows", "gauge", "Rows held by the middleware store.")
		fmt.Fprintf(&b, "caveman_middleware_store_rows %d\n", rows)
		series("caveman_middleware_store_bytes", "gauge", "Payload bytes held by the middleware store, originals included.")
		fmt.Fprintf(&b, "caveman_middleware_store_bytes %d\n", size)
		series("caveman_middleware_store_limit", "gauge", "Admission limits; a per-principal limit of 0 means only the global limit applies.")
		for _, limit := range []struct {
			scope, unit string
			value       int64
		}{{"global", "rows", limits.Rows}, {"global", "bytes", limits.Bytes}, {"principal", "rows", limits.PrincipalRows}, {"principal", "bytes", limits.PrincipalBytes}} {
			fmt.Fprintf(&b, "caveman_middleware_store_limit{scope=%q,unit=%q} %d\n", limit.scope, limit.unit, limit.value)
		}
	}
	_, _ = io.WriteString(w, b.String())
}

func sortedKeys[K comparable, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b K) int { return strings.Compare(fmt.Sprint(a), fmt.Sprint(b)) })
	return keys
}

// Ready reports whether the middleware store accepts writes. Readiness is
// unauthenticated, so the answer is kept for a second: a probe flood costs at
// most one write transaction a second. Concurrent callers wait on the lock for
// the one probe in flight (at most its 2s timeout) and share its answer.
func (r *Runtime) Ready(ctx context.Context) error {
	r.readyMu.Lock()
	defer r.readyMu.Unlock()
	if time.Since(r.readyAt) >= time.Second {
		// Not the caller's cancellation: a client that hangs up would have it
		// cached as every probe's answer.
		probe, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		r.readyErr, r.readyAt = r.cfg.Store.MiddlewareWritable(probe), time.Now()
		cancel()
	}
	return r.readyErr
}

// rateQuota enforces quota_requests_per_minute per principal; a principal's
// own requests_per_minute replaces the runtime-wide limit.
// ponytail: fixed one-minute windows; a sliding window if boundary bursts matter.
type rateQuota struct {
	limit  int
	mu     sync.Mutex
	window int64
	counts map[string]int
}

// allow reports whether principal may send another request now and, if not,
// the whole seconds until its window resets.
func (q *rateQuota) allow(principal string, limit int, now time.Time) (bool, int) {
	if limit <= 0 {
		limit = q.limit
	}
	if limit <= 0 {
		return true, 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if minute := now.Unix() / 60; minute != q.window {
		q.window, q.counts = minute, map[string]int{}
	}
	if q.counts[principal] >= limit {
		return false, int(60 - now.Unix()%60)
	}
	q.counts[principal]++
	return true, 0
}

// perPrincipal bounds how many slots of one queue a single principal holds, so
// one caller's slow bodies cannot fill a queue every principal shares, and
// keeps the last reserve free slots for principals holding none: however many
// slots other principals hold, one with no request in flight still gets in
// unless reserve distinct principals already took the reserve. Requests that
// cannot take a slot wait for one to free. limit 0 is no bound.
type perPrincipal struct {
	limit, depth, reserve int
	mu                    sync.Mutex
	total                 int            // slots held, all principals
	held                  map[string]int // slots held per principal
	freed                 chan struct{}  // closed and replaced when a slot frees
}

// acquire waits for one of principal's slots until ctx ends, and returns its
// release.
// ponytail: every release wakes every waiter; a per-principal wait queue if
// waiter counts ever make that measurable.
func (p *perPrincipal) acquire(ctx context.Context, principal string) (func(), error) {
	if p.limit <= 0 {
		return func() {}, nil
	}
	for {
		p.mu.Lock()
		if p.held == nil {
			p.held, p.freed = map[string]int{}, make(chan struct{})
		}
		n, free := p.held[principal], p.depth-p.total
		if n < p.limit && (free > p.reserve || (n == 0 && free > 0)) {
			p.held[principal], p.total = n+1, p.total+1
			p.mu.Unlock()
			return func() {
				p.mu.Lock()
				if p.held[principal]--; p.held[principal] == 0 {
					delete(p.held, principal)
				}
				p.total--
				close(p.freed)
				p.freed = make(chan struct{})
				p.mu.Unlock()
			}, nil
		}
		freed := p.freed
		p.mu.Unlock()
		select {
		case <-freed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
