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
