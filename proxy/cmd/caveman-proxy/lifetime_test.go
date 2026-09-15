package main

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Use the real serve loop in a subprocess, with isolated persistence. The old
// timeout is accelerated; no heartbeat or hook event keeps the process alive.
func TestServeDoesNotExpire(t *testing.T) {
	if os.Getenv("CAVEMAN_TEST_SERVE_LIFETIME") == "1" {
		runServe(slog.New(slog.NewTextHandler(io.Discard, nil)))
		return
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	home := t.TempDir()
	var output bytes.Buffer
	cmd := exec.Command(os.Args[0], "-test.run=^TestServeDoesNotExpire$")
	cmd.Env = append(os.Environ(),
		"CAVEMAN_TEST_SERVE_LIFETIME=1", "CAVEMAN_HOME="+home,
		"CAVEMAN_CONFIG="+filepath.Join(home, "missing.yaml"), "CAVEMAN_LISTEN="+addr,
		"CAVEMAN_PROXY_OWNER=wrap", "CAVEMAN_NATIVE_IDLE_TIMEOUT=100ms", "CAVEMAN_MODE=record",
	)
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })
	client := &http.Client{Timeout: 200 * time.Millisecond}
	ready := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://" + addr + "/health/ready")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatal("isolated proxy did not become ready")
	}
	time.Sleep(500 * time.Millisecond)
	resp, err := client.Get("http://" + addr + "/health/ready")
	if err != nil {
		t.Fatalf("proxy expired without a heartbeat: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status after idle = %d", resp.StatusCode)
	}
}
