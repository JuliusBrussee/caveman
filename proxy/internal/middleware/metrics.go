package middleware

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// outcome is what one request did, for metrics and the audit log. It never
// holds content, raw scope values, handles or credentials: scope and handle are
// truncated hashes.
type outcome struct {
	route, principal, scope, handle string
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
	attrs := []any{"route", route, "status", o.status, "code", code, "principal", o.principal, "scope", o.scope,
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

// Ready reports whether the middleware store accepts writes.
func (r *Runtime) Ready(ctx context.Context) error { return r.cfg.Store.MiddlewareWritable(ctx) }

// rateQuota enforces quota_requests_per_minute per principal.
// ponytail: fixed one-minute windows; a sliding window if boundary bursts matter.
type rateQuota struct {
	limit  int
	mu     sync.Mutex
	window int64
	counts map[string]int
}

// allow reports whether principal may send another request now and, if not,
// the whole seconds until its window resets.
func (q *rateQuota) allow(principal string, now time.Time) (bool, int) {
	if q.limit <= 0 {
		return true, 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if minute := now.Unix() / 60; minute != q.window {
		q.window, q.counts = minute, map[string]int{}
	}
	if q.counts[principal] >= q.limit {
		return false, int(60 - now.Unix()%60)
	}
	q.counts[principal]++
	return true, 0
}
