package nativeruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/JuliusBrussee/caveman/shared/platform/redact"
)

// Call sends one bounded host event to the shared runtime. Client adapters use
// same platform endpoint and 250 ms deadline as server; any transport or schema
// failure returns no decision so host remains fail-open.
func Call(ctx context.Context, home string, request Request) (*Response, error) {
	callCtx, cancel := context.WithTimeout(ctx, hookDeadline)
	defer cancel()
	conn, err := dialNativeRuntime(callCtx, home)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(hookDeadline))
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return nil, err
	}
	limited := &io.LimitedReader{R: conn, N: maxRequestBytes + 1}
	var response Response
	if err := json.NewDecoder(limited).Decode(&response); err != nil {
		return nil, err
	}
	if limited.N <= 0 || !validResponse(response) {
		return nil, errors.New("native runtime: invalid response")
	}
	return &response, nil
}

func validResponse(response Response) bool {
	if response.ProtocolVersion != ProtocolVersion || !response.FailOpen {
		return false
	}
	if response.PolicyMode != "" && response.PolicyMode != "record" && response.PolicyMode != "safe" && response.PolicyMode != "max" {
		return false
	}
	if response.Profile != "" {
		switch response.Profile {
		case "record-only", "core", "core-lean-build", "ledger", "ccr-masking", "cache-aware", "full-safe", "full-max":
		default:
			return false
		}
	}
	switch response.Action {
	case "allow", "observe", "block", "ask", "defer":
	default:
		return false
	}
	switch response.Visibility {
	case "silent", "advisory", "user":
	default:
		return false
	}
	if len(response.Context) > 64*1024 || len(response.Message) > 4096 || len(response.OutputReplacement) > 2*1024*1024 || len(response.RecoveryRef) > 1024 || len(response.DecisionID) > 256 {
		return false
	}
	return true
}

const (
	maxRequestBytes = 2 * 1024 * 1024
	hookDeadline    = 250 * time.Millisecond
)

func serveConn(ctx context.Context, conn net.Conn, runtime *Runtime) {
	defer conn.Close()
	started := time.Now()
	_ = conn.SetDeadline(started.Add(hookDeadline))
	hookCtx, cancel := context.WithTimeout(ctx, hookDeadline)
	defer cancel()
	limited := &io.LimitedReader{R: conn, N: maxRequestBytes + 1}
	var request Request
	if err := json.NewDecoder(limited).Decode(&request); err != nil || limited.N <= 0 {
		// A connection that closes before sending a byte is a liveness probe
		// (a second runtime's stale-socket check), not a request.
		if limited.N <= 0 {
			runtime.logDrop(ctx, request, "request_too_large", nil, started)
		} else if limited.N <= maxRequestBytes {
			runtime.logDrop(ctx, request, "request_unreadable", err, started)
		}
		return
	}
	response, err := runtime.Handle(hookCtx, request)
	if err != nil {
		// Closing without a decision is fail-open at every thin host adapter.
		// Never synthesize success: callers use a real decision_id to prove the
		// session reached shared runtime before emitting a correlation marker.
		reason := "handler_error"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "deadline_exceeded"
		}
		runtime.logDrop(ctx, request, reason, err, started)
		return
	}
	if err := json.NewEncoder(conn).Encode(response); err != nil {
		runtime.logDrop(ctx, request, "reply_write_failed", err, started)
	}
}

// dropLogWindow bounds drop lines to one per reason per window: a wedged store
// drops every request, and the hosts fire one per tool call.
const dropLogWindow = 30 * time.Second

// dropLog rate-limits the WARN line serveConn writes for a request it closed
// without a decision. The host fails open on that empty reply by design, which
// left a runtime that answered nothing indistinguishable from one that never
// had to (issue #1279).
type dropLog struct {
	mu         sync.Mutex
	now        func() time.Time
	total      int64
	last       map[string]time.Time
	suppressed map[string]int64
}

// logDrop records one dropped request. Session ids stay out of the line, as
// they stay out of every other runtime export; the agent and event say enough
// to find the host. A shutdown (the serve context ending) is not a drop.
func (r *Runtime) logDrop(ctx context.Context, request Request, reason string, err error, started time.Time) {
	if r == nil || r.Logger == nil || ctx.Err() != nil {
		return
	}
	d := &r.drops
	d.mu.Lock()
	now := time.Now
	if d.now != nil {
		now = d.now
	}
	at := now()
	d.total++
	total := d.total
	if d.last == nil {
		d.last, d.suppressed = map[string]time.Time{}, map[string]int64{}
	}
	if last, seen := d.last[reason]; seen && at.Sub(last) < dropLogWindow {
		d.suppressed[reason]++
		d.mu.Unlock()
		return
	}
	suppressed := d.suppressed[reason]
	d.last[reason], d.suppressed[reason] = at, 0
	d.mu.Unlock()
	attrs := []any{
		"event", request.Event.Type,
		"agent", request.Agent.ID,
		"reason", reason,
		"elapsed_ms", time.Since(started).Milliseconds(),
		"deadline_ms", hookDeadline.Milliseconds(),
		"suppressed", suppressed,
		"dropped_total", total,
	}
	if err != nil {
		attrs = append(attrs, "error", redact.Error(err))
	}
	r.Logger.Warn("native runtime request dropped; host failed open", attrs...)
}
