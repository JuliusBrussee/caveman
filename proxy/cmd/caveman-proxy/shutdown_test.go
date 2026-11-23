package main

import (
	"bufio"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Exercise the real process and provider stream: returning from runServe closes
// its stores and main then exits, even if http.Server still has active handlers.
func TestServeShutdown(t *testing.T) {
	if os.Getenv("CAVEMAN_TEST_SERVE_SHUTDOWN") == "1" {
		runServe(slog.New(slog.NewTextHandler(io.Discard, nil)))
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not support sending POSIX signals to another process")
	}
	for _, force := range []bool{false, true} {
		name := "drain"
		if force {
			name = "second-signal"
		}
		t.Run(name, func(t *testing.T) {
			release := make(chan struct{})
			defer close(release)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "event: ping\ndata: {}\n\n")
				w.(http.Flusher).Flush()
				select {
				case <-release:
					_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
				case <-r.Context().Done():
				}
			}))
			defer upstream.Close()
			cmd, addr, stopped := startShutdownProxy(t, upstream.URL)
			client := &http.Client{Timeout: 15 * time.Second}
			req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/messages", strings.NewReader(`{"model":"claude-test","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":true}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("x-api-key", "fixture-key")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			reader := bufio.NewReader(resp.Body)
			line, err := reader.ReadString('\n')
			if err != nil || line != "event: ping\n" {
				t.Fatalf("initial stream = %q, %v (status %d)", line, err, resp.StatusCode)
			}
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			waitForShutdownListener(t, addr)
			if force {
				if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
				select {
				case <-stopped:
				case <-time.After(2 * time.Second):
					t.Fatal("second signal did not force the draining proxy to exit")
				}
				return
			}
			// The previous five-second shutdown deadline cut off accepted streams.
			select {
			case <-stopped:
				t.Fatal("proxy exited before the accepted provider stream completed")
			case <-time.After(5500 * time.Millisecond):
			}
			release <- struct{}{}
			rest, err := io.ReadAll(reader)
			if err != nil || !strings.Contains(string(rest), "event: message_stop\n") {
				t.Fatalf("drained response = %q, %v", rest, err)
			}
			select {
			case <-stopped:
				if !cmd.ProcessState.Success() {
					t.Fatalf("graceful shutdown failed: %v", cmd.ProcessState)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("proxy did not exit after the stream completed")
			}
		})
	}
}

func startShutdownProxy(t *testing.T, upstream string) (*exec.Cmd, string, <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	home := t.TempDir()
	configPath := filepath.Join(home, "caveman.yaml")
	if err := os.WriteFile(configPath, []byte("mode: record\nupstream_proxy: off\nproviders:\n  anthropic:\n    base_url: "+upstream+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestServeShutdown$")
	cmd.Env = append(os.Environ(),
		"CAVEMAN_TEST_SERVE_SHUTDOWN=1", "CAVEMAN_HOME="+home,
		"CAVEMAN_CONFIG="+configPath, "CAVEMAN_LISTEN="+addr,
		"CAVEMAN_DB="+filepath.Join(home, "caveman.db"), "CAVEMAN_CCR_DB="+filepath.Join(home, "ccr.db"),
		"CAVEMAN_MODE=record", "CAVEMAN_AUTH_TOKEN=", "CAVE_SSRF_ALLOWLIST=localhost",
		"CAVE_GATEWAY_UPSTREAM_TIMEOUT_MS=0", "CAVE_UPSTREAM_PROXY=off",
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() { _ = cmd.Wait(); close(stopped) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-stopped })
	client := &http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://" + addr + "/health/ready")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return cmd, addr, stopped
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("isolated shutdown proxy did not become ready")
	return nil, "", nil
}

func waitForShutdownListener(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("proxy did not close its listener during shutdown")
}
