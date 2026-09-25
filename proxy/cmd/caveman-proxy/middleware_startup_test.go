package main

import (
	"bytes"
	"database/sql"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startServe runs the real serve loop in a subprocess with an isolated home and
// the given environment, returning its address, output and exit channel.
func startServe(t *testing.T, home string, env ...string) (string, *bytes.Buffer, chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	var output bytes.Buffer
	cmd := exec.Command(os.Args[0], "-test.run=^TestMiddlewareStartupServe$")
	cmd.Env = append(append(os.Environ(), "CAVEMAN_TEST_SERVE_MIDDLEWARE=1", "CAVEMAN_HOME="+home,
		"CAVEMAN_CONFIG="+filepath.Join(home, "missing.yaml"), "CAVEMAN_LISTEN="+addr, "CAVEMAN_MODE=record"), env...)
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })
	return addr, &output, done
}

func TestMiddlewareStartupServe(t *testing.T) {
	if os.Getenv("CAVEMAN_TEST_SERVE_MIDDLEWARE") != "1" {
		t.Skip("subprocess entry point")
	}
	runServe(slog.New(slog.NewTextHandler(os.Stderr, nil)))
}

// GO-5: a middleware the operator configured that cannot start stops the
// process, as a shared Postgres store always did.
func TestServeExitsWhenConfiguredMiddlewareCannotStart(t *testing.T) {
	_, output, done := startServe(t, t.TempDir(), "CAVEMAN_MIDDLEWARE_ENCRYPTION_KEY=not-a-key")
	select {
	case err := <-done:
		if code := exitCode(err); code != 1 || !strings.Contains(output.String(), "framework middleware unavailable") {
			t.Fatalf("exit %d:\n%s", code, output)
		}
		done <- err // for the cleanup
	case <-time.After(10 * time.Second):
		t.Fatalf("the proxy kept serving without its configured middleware:\n%s", output)
	}
}

// GO-5: the default local middleware failing leaves inference up but the
// replica unready, never a ready probe in front of 503s.
func TestServeIsNotReadyWhenDefaultMiddlewareCannotStart(t *testing.T) {
	home := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(home, "caveman.db")))
	if err != nil {
		t.Fatal(err)
	}
	// A middleware table no runtime could have written: InitMiddleware fails.
	if _, err := db.Exec(`CREATE TABLE middleware_usage (unexpected INTEGER)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	addr, output, _ := startServe(t, home)
	client := &http.Client{Timeout: time.Second}
	get := func(path string) (int, string) {
		resp, err := client.Get("http://" + addr + path)
		if err != nil {
			return 0, ""
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if status, _ := get("/health/live"); status == 200 {
			break
		}
	}
	if status, _ := get("/health/live"); status != 200 {
		t.Fatalf("proxy did not start:\n%s", output)
	}
	if status, body := get("/health/ready"); status != 503 || !strings.Contains(body, `"middleware":"degraded"`) {
		t.Fatalf("ready = %d %s with the middleware down:\n%s", status, body, output)
	}
}

func exitCode(err error) int {
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode()
	}
	if err == nil {
		return 0
	}
	return -1
}
