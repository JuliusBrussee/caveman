package nativeruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/engine/ccr"
)

// syncBuffer is a log sink the server goroutine and the test can share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) lines(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, entry)
	}
	return out
}

func loggedRuntime(t *testing.T, runtime *Runtime) *syncBuffer {
	t.Helper()
	sink := &syncBuffer{}
	runtime.Logger = slog.New(slog.NewJSONHandler(sink, nil))
	return sink
}

// exchange runs one connection through serveConn to completion: the request
// (nil sends nothing), then whatever the server answers before it closes.
func exchange(t *testing.T, runtime *Runtime, request *Request) []byte {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		serveConn(context.Background(), server, runtime)
		close(done)
	}()
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	if request != nil {
		if err := json.NewEncoder(client).Encode(request); err != nil {
			t.Fatal(err)
		}
	} else {
		_ = client.Close()
	}
	var reply bytes.Buffer
	if request != nil {
		_, _ = reply.ReadFrom(client)
		_ = client.Close()
	}
	<-done
	return reply.Bytes()
}

func memoryRuntime(t *testing.T) *Runtime {
	t.Helper()
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return New(store)
}

// Issue #1279: a native request the runtime cannot answer closes with an empty
// reply, which the host takes as fail-open, and nothing anywhere said so. The
// reporter could not tell a slow store from a broken one. Each drop must leave
// a WARN line naming the event and why, without the session id.
func TestDroppedNativeRequestIsLoggedWithItsReason(t *testing.T) {
	runtime := memoryRuntime(t)
	sink := loggedRuntime(t, runtime)
	if reply := exchange(t, runtime, &Request{
		ProtocolVersion: 99,
		Agent:           Agent{ID: "pi"},
		Session:         Session{ID: "secret-session-id"},
		Event:           Event{Type: "tool.after"},
	}); len(reply) != 0 {
		t.Fatalf("a rejected request got a reply: %s", reply)
	}
	lines := sink.lines(t)
	if len(lines) != 1 {
		t.Fatalf("logged %d lines for one dropped request, want 1: %v", len(lines), lines)
	}
	line := lines[0]
	if line["level"] != "WARN" || line["event"] != "tool.after" || line["agent"] != "pi" || line["reason"] != "handler_error" {
		t.Fatalf("drop line = %v; want WARN naming event tool.after, agent pi, reason handler_error", line)
	}
	if !strings.Contains(line["error"].(string), "protocol version") {
		t.Fatalf("drop line error = %v; want the handler's reason", line["error"])
	}
	if strings.Contains(sink.buf.String(), "secret-session-id") {
		t.Fatalf("drop line leaked the session id: %s", sink.buf.String())
	}
}

// A burst of drops for one reason is one line per window, and the next line
// counts the ones it stood in for.
func TestDroppedNativeRequestLogIsRateLimited(t *testing.T) {
	runtime := memoryRuntime(t)
	sink := loggedRuntime(t, runtime)
	now := time.Unix(1_800_000_000, 0)
	runtime.drops.now = func() time.Time { return now }
	bad := &Request{ProtocolVersion: 99, Agent: Agent{ID: "pi"}, Session: Session{ID: "s"}, Event: Event{Type: "session.start"}}
	for i := 0; i < 5; i++ {
		exchange(t, runtime, bad)
	}
	if lines := sink.lines(t); len(lines) != 1 {
		t.Fatalf("5 drops inside one window logged %d lines, want 1", len(lines))
	}
	now = now.Add(dropLogWindow)
	exchange(t, runtime, bad)
	lines := sink.lines(t)
	if len(lines) != 2 {
		t.Fatalf("a drop after the window logged %d lines in total, want 2", len(lines))
	}
	if lines[1]["suppressed"] != 4.0 || lines[1]["dropped_total"] != 6.0 {
		t.Fatalf("second line = %v; want suppressed 4 and dropped_total 6", lines[1])
	}
}

// A connection that sends nothing (a liveness probe, the stale-socket check of
// a second runtime) is not a dropped request.
func TestEmptyNativeConnectionIsNotLoggedAsADrop(t *testing.T) {
	runtime := memoryRuntime(t)
	sink := loggedRuntime(t, runtime)
	exchange(t, runtime, nil)
	if lines := sink.lines(t); len(lines) != 0 {
		t.Fatalf("an empty connection logged %v", lines)
	}
	if reply := exchange(t, runtime, &Request{ProtocolVersion: ProtocolVersion, Agent: Agent{ID: "pi"}, Session: Session{ID: "ok"}, Event: Event{Type: "session.start"}}); len(reply) == 0 {
		t.Fatal("a healthy request got no reply")
	}
	if lines := sink.lines(t); len(lines) != 0 {
		t.Fatalf("an answered request logged %v", lines)
	}
}
