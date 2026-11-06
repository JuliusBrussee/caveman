//go:build !windows

package nativeruntime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/JuliusBrussee/caveman/engine/ccr"
)

// budgetGrace is how long a hook-budgeted call may take while still counting as
// "returned at the deadline". hookDeadline is 250ms; the store's own busy
// timeout is 5s, so anything past this grace means the call waited out the
// store instead of the hook budget.
const budgetGrace = 1500 * time.Millisecond

// holdExclusiveWriteLock opens a second connection to the same database file and
// holds a write transaction on it. This stands in for a stale 2.x caveman-mcp
// process keeping the upgraded home's ccr.db write-locked next to the 4.0.1
// runtime (issue #1279): readers still succeed in WAL mode, but every store
// write blocks until the 5s busy timeout reports SQLITE_BUSY.
func holdExclusiveWriteLock(t *testing.T, path string) {
	t.Helper()
	holder, err := sql.Open("sqlite", ccr.SQLiteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	holder.SetMaxOpenConns(1)
	conn, err := holder.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		_ = conn.Close()
		_ = holder.Close()
	})
}

func lockedHomeRuntime(t *testing.T) *Runtime {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ccr.db")
	store, err := ccr.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	holdExclusiveWriteLock(t, path)
	return New(store)
}

// TestHandleReturnsBeforeStoreBusyTimeoutUnderHeldWriteLock is the #1279
// mechanism: a store write blocked by a stale process holding the database must
// surface at the 250ms hook deadline, not at the store's 5s busy timeout.
func TestHandleReturnsBeforeStoreBusyTimeoutUnderHeldWriteLock(t *testing.T) {
	runtime := lockedHomeRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), hookDeadline)
	defer cancel()
	started := time.Now()
	_, err := runtime.Handle(ctx, Request{
		ProtocolVersion: ProtocolVersion,
		Agent:           Agent{ID: "pi"},
		Session:         Session{ID: "contended-session"},
		Event:           Event{Type: "session.start"},
	})
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("write-locked store produced a decision; want a fail-open error")
	}
	t.Logf("Handle error under held write lock after %s: %v", elapsed, err)
	if elapsed > budgetGrace {
		t.Fatalf("Handle returned after %s; hook deadline is %s — the deadline did not bound the blocked store call", elapsed, hookDeadline)
	}
}

// TestQueuedSessionEventReturnsBeforeStoreBusyTimeout covers the pileup: the
// event after a blocked one must fail open at its own deadline instead of
// queueing behind the drain of a 5s store wait.
func TestQueuedSessionEventReturnsBeforeStoreBusyTimeout(t *testing.T) {
	runtime := lockedHomeRuntime(t)
	firstCtx, firstCancel := context.WithTimeout(context.Background(), hookDeadline)
	defer firstCancel()
	firstStart := time.Now()
	if _, err := runtime.Handle(firstCtx, Request{
		ProtocolVersion: ProtocolVersion,
		Agent:           Agent{ID: "pi"},
		Session:         Session{ID: "queued-session"},
		Event:           Event{Type: "session.start"},
	}); err == nil {
		t.Fatal("write-locked store produced a decision; want a fail-open error")
	}
	t.Logf("first event returned after %s", time.Since(firstStart))

	secondCtx, secondCancel := context.WithTimeout(context.Background(), hookDeadline)
	defer secondCancel()
	secondStart := time.Now()
	_, err := runtime.Handle(secondCtx, Request{
		ProtocolVersion: ProtocolVersion,
		Agent:           Agent{ID: "pi"},
		Session:         Session{ID: "queued-session"},
		Event:           Event{Type: "prompt.submit"},
	})
	elapsed := time.Since(secondStart)
	if err == nil {
		t.Fatal("queued event produced a decision while the store stayed write-locked")
	}
	t.Logf("queued event returned after %s: %v", elapsed, err)
	if elapsed > budgetGrace {
		t.Fatalf("queued event returned after %s; it must fail open at the %s hook deadline", elapsed, hookDeadline)
	}
}

// TestServeConnClosesWithinBudgetUnderHeldWriteLock reproduces the reporter's
// observation at the socket layer: the connection closes with zero bytes, and
// after the fix it must do so at the hook deadline rather than seconds later.
func TestServeConnClosesWithinBudgetUnderHeldWriteLock(t *testing.T) {
	runtime := lockedHomeRuntime(t)
	client, server := net.Pipe()
	defer client.Close()
	go serveConn(context.Background(), server, runtime)
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewEncoder(client).Encode(Request{
		ProtocolVersion: ProtocolVersion,
		Agent:           Agent{ID: "pi"},
		Session:         Session{ID: "socket-contended"},
		Event:           Event{Type: "session.start"},
	}); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	var response Response
	decodeErr := json.NewDecoder(client).Decode(&response)
	elapsed := time.Since(started)
	if decodeErr == nil {
		t.Fatalf("write-locked store produced a socket decision: %+v", response)
	}
	if !errors.Is(decodeErr, io.EOF) {
		t.Fatalf("decode error = %v; want EOF from the server closing without a reply", decodeErr)
	}
	t.Logf("empty-reply close after %s", elapsed)
	if elapsed > budgetGrace {
		t.Fatalf("server closed with an empty reply after %s; hook deadline is %s", elapsed, hookDeadline)
	}
}

// The same empty reply must not be silent in the proxy log: the line says the
// hook budget ran out, which is what the reporter could not see (#1279).
func TestServeConnLogsAHookBudgetDrop(t *testing.T) {
	runtime := lockedHomeRuntime(t)
	sink := loggedRuntime(t, runtime)
	if reply := exchange(t, runtime, &Request{
		ProtocolVersion: ProtocolVersion,
		Agent:           Agent{ID: "pi"},
		Session:         Session{ID: "socket-contended"},
		Event:           Event{Type: "session.start"},
	}); len(reply) != 0 {
		t.Fatalf("write-locked store produced a socket decision: %s", reply)
	}
	lines := sink.lines(t)
	if len(lines) != 1 || lines[0]["reason"] != "deadline_exceeded" || lines[0]["event"] != "session.start" {
		t.Fatalf("drop lines = %v; want one naming session.start and deadline_exceeded", lines)
	}
	if _, ok := lines[0]["elapsed_ms"].(float64); !ok {
		t.Fatalf("drop line has no elapsed_ms: %v", lines[0])
	}
}

// TestFreshStoreSocketAnswersDecisionWellWithinBudget is the healthy-home half
// of #1279's differential: a fresh store answers with a real decision far below
// the hook budget, before and after the fix.
func TestFreshStoreSocketAnswersDecisionWellWithinBudget(t *testing.T) {
	store, err := ccr.Open(filepath.Join(t.TempDir(), "ccr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	socketRoot, err := os.MkdirTemp("/tmp", "cave-native-budget-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketRoot)
	path := filepath.Join(socketRoot, "run", "native.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ServeUnix(ctx, path, New(store)) }()
	waitForSocket(t, path, done)

	request := Request{
		ProtocolVersion: ProtocolVersion,
		Agent:           Agent{ID: "pi"},
		Session:         Session{ID: "fresh-home"},
		Event:           Event{Type: "session.start"},
	}
	// One warmup so the measured call is steady state, not first-open cost.
	if _, err := callSocket(path, request); err != nil {
		t.Fatal(err)
	}
	measured := time.Now()
	response, err := callSocket(path, request)
	elapsed := time.Since(measured)
	if err != nil {
		t.Fatal(err)
	}
	if response.Action != "allow" || response.ProtocolVersion != ProtocolVersion || !response.FailOpen || response.DecisionID == "" {
		t.Fatalf("unexpected decision: %+v", response)
	}
	t.Logf("fresh-home session.start decision in %s", elapsed)
	if elapsed > 50*time.Millisecond {
		t.Fatalf("fresh home answered in %s; want well under the %s hook budget", elapsed, hookDeadline)
	}
}

// TestSessionEndBehindSlowEventIsStillArchived pins the one event the deadline
// must not drop: a session.end that arrives while the session stripe is held by
// an event draining a contended store call is finalized once the stripe frees,
// even though its own hook budget died waiting.
func TestSessionEndBehindSlowEventIsStillArchived(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ccr.db")
	store, err := ccr.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	session := Session{ID: "ending-behind-drain"}
	if _, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: ProtocolVersion, Agent: Agent{ID: "claude"}, Session: session,
		Event: Event{Type: "tool.after"},
		Tool:  &Tool{Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`), Output: []byte(strings.Repeat("safe result\n", 300))},
	}); err != nil {
		t.Fatal(err)
	}
	holder, err := sql.Open("sqlite", ccr.SQLiteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	holder.SetMaxOpenConns(1)
	conn, err := holder.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	slowCtx, slowCancel := context.WithTimeout(context.Background(), hookDeadline)
	defer slowCancel()
	if _, err := runtime.Handle(slowCtx, Request{ProtocolVersion: ProtocolVersion, Agent: Agent{ID: "claude"}, Session: session, Event: Event{Type: "prompt.submit"}}); err == nil {
		t.Fatal("write-locked store produced a decision; want a fail-open error")
	}
	endCtx, endCancel := context.WithTimeout(context.Background(), hookDeadline)
	defer endCancel()
	_, _ = runtime.Handle(endCtx, Request{ProtocolVersion: ProtocolVersion, Agent: Agent{ID: "claude"}, Session: session, Event: Event{Type: "session.end"}})
	if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		objects, err := store.ListSessionObjects(session.ID, 100)
		if err != nil {
			t.Fatal(err)
		}
		archived := len(objects) > 0
		for _, object := range objects {
			archived = archived && object.Lifecycle == ccr.LifecycleArchived
		}
		if archived {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("session.end behind a draining event was dropped: %d objects not all archived", len(objects))
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// session.end queued behind an event that was already waiting for the session
// stripe must not run first: the earlier event's capture would land after the
// archive and stay hot forever.
func TestSessionEndDoesNotOvertakeAQueuedEvent(t *testing.T) {
	store, err := ccr.Open(filepath.Join(t.TempDir(), "ccr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	session := Session{ID: "end-keeps-its-place"}
	stripe := runtime.sessionStripe(session.ID)
	stripe <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = runtime.Handle(ctx, Request{
			ProtocolVersion: ProtocolVersion, Agent: Agent{ID: "claude"}, Session: session,
			Event: Event{Type: "tool.after"},
			Tool:  &Tool{Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`), Output: []byte(strings.Repeat("safe result\n", 300))},
		})
	}()
	time.Sleep(30 * time.Millisecond)
	go func() {
		defer wg.Done()
		_, _ = runtime.Handle(ctx, Request{ProtocolVersion: ProtocolVersion, Agent: Agent{ID: "claude"}, Session: session, Event: Event{Type: "session.end"}})
	}()
	time.Sleep(30 * time.Millisecond)
	<-stripe
	wg.Wait()
	objects, err := store.ListSessionObjects(session.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) == 0 {
		t.Fatal("tool.after captured nothing")
	}
	for _, object := range objects {
		if object.Lifecycle != ccr.LifecycleArchived {
			t.Fatalf("object %s type=%s lifecycle=%s written after session.end archived the session", object.ID, object.Type, object.Lifecycle)
		}
	}
}

// subagent.start sends the host nothing; its effect is the state write, so a
// stripe held past the hook deadline delays it instead of dropping it.
func TestStateOnlyEventStillRunsAfterTheDeadline(t *testing.T) {
	store, err := ccr.Open(filepath.Join(t.TempDir(), "ccr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	child := Session{ID: "child-behind-slow-stripe", ParentSessionID: "parent"}
	stripe := runtime.sessionStripe(child.ID)
	stripe <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := runtime.Handle(ctx, Request{ProtocolVersion: ProtocolVersion, Agent: Agent{ID: "codex"}, Session: child, Event: Event{Type: "subagent.start"}}); err == nil {
		t.Fatal("held stripe produced a decision inside the budget")
	}
	<-stripe
	deadline := time.Now().Add(5 * time.Second)
	for {
		objects, err := store.ListSessionObjects(child.ID, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, object := range objects {
			if object.Source == "native:subagent-active" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("subagent.start was dropped at the deadline: %d objects, none native:subagent-active", len(objects))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A host-visible event whose budget runs out while it waits for the stripe
// does no store work afterwards: the host has already failed open.
func TestExpiredWaiterLeavesTheQueueWithoutStoreWork(t *testing.T) {
	store, err := ccr.Open(filepath.Join(t.TempDir(), "ccr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	session := Session{ID: "expired-waiter"}
	stripe := runtime.sessionStripe(session.ID)
	stripe <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := runtime.Handle(ctx, Request{
		ProtocolVersion: ProtocolVersion, Agent: Agent{ID: "claude"}, Session: session,
		Event: Event{Type: "tool.after"},
		Tool:  &Tool{Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`), Output: []byte(strings.Repeat("safe result\n", 300))},
	}); err == nil {
		t.Fatal("held stripe produced a decision inside the budget")
	}
	<-stripe
	time.Sleep(100 * time.Millisecond)
	objects, err := store.ListSessionObjects(session.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 0 {
		t.Fatalf("expired waiter wrote %d objects after its deadline", len(objects))
	}
}
