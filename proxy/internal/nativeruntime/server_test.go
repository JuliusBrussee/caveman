//go:build !windows

package nativeruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/engine/ccr"
)

func TestUnixServerConcurrentSessionsAndPermissions(t *testing.T) {
	store, err := ccr.Open(filepath.Join(t.TempDir(), "ccr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	socketRoot, err := os.MkdirTemp("/tmp", "cave-native-socket-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketRoot)
	path := filepath.Join(socketRoot, "run", "native.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ServeUnix(ctx, path, runtime) }()
	waitForSocket(t, path, done)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket permissions = %o, want 600", info.Mode().Perm())
	}

	var wg sync.WaitGroup
	for _, session := range []string{"s1", "s2", "s3", "s4"} {
		session := session
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, err := callSocket(path, Request{
				ProtocolVersion: 1,
				Agent:           Agent{ID: "codex"},
				Session:         Session{ID: session},
				Event:           Event{Type: "prompt.submit"},
			})
			if err != nil || response.Action != "allow" {
				t.Errorf("session %s: response=%+v err=%v", session, response, err)
			}
		}()
	}
	wg.Wait()
	for _, session := range []string{"s1", "s2", "s3", "s4"} {
		objects, err := store.ListSessionObjects(session, 10)
		if err != nil || len(objects) != 2 || !containsType(objects, ccr.ObjectTaskContract) || !containsType(objects, ccr.ObjectTaskDecision) {
			t.Fatalf("session %s isolation failed: objects=%+v err=%v", session, objects, err)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop after cancellation")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("socket was not removed on shutdown: %v", err)
	}
}

func TestServeConnRejectsOversizedRequestWithoutDecision(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	client, server := net.Pipe()
	defer client.Close()
	go serveConn(context.Background(), server, runtime)
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	request := Request{
		ProtocolVersion: 1, Agent: Agent{ID: "claude"}, Session: Session{ID: "oversized"}, Event: Event{Type: "tool.after"},
		Tool: &Tool{Name: "read_file", Input: json.RawMessage(`{"path":"src/large.go"}`), Output: make([]byte, maxRequestBytes)},
	}
	_ = json.NewEncoder(client).Encode(request)
	var response Response
	if err := json.NewDecoder(client).Decode(&response); err == nil {
		t.Fatalf("oversized request produced a decision: %+v", response)
	}
	objects, err := store.ListSessionObjects("oversized", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 0 {
		t.Fatalf("oversized request reached runtime: %+v", objects)
	}
}

func TestServeConnRuntimeErrorReturnsNoSyntheticDecision(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client, server := net.Pipe()
	defer client.Close()
	go serveConn(context.Background(), server, New(store))
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	if err := json.NewEncoder(client).Encode(Request{
		ProtocolVersion: 99,
		Agent:           Agent{ID: "claude"},
		Session:         Session{ID: "invalid-protocol"},
		Event:           Event{Type: "session.start"},
	}); err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.NewDecoder(client).Decode(&response); err == nil {
		t.Fatalf("runtime error produced synthetic success: %+v", response)
	}
	objects, err := store.ListSessionObjects("invalid-protocol", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 0 {
		t.Fatalf("invalid request reached persistence: %+v", objects)
	}
}

func callSocket(path string, request Request) (Response, error) {
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return Response{}, err
	}
	var response Response
	err = json.NewDecoder(bufio.NewReader(conn)).Decode(&response)
	return response, err
}

func waitForSocket(t *testing.T, path string, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("server exited before socket appeared: %v", err)
		default:
		}
		if info, err := os.Stat(path); err == nil {
			// Listen publishes the socket before ServeUnix applies its restrictive
			// mode. The parent directory is already 0700, but readiness means the
			// final socket contract is visible too.
			if info.Mode().Perm() == 0o600 {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket did not appear: %s", path)
}
