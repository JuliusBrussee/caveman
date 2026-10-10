package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/runstate"
)

// A config that does not load says nothing about the listener. Reporting the
// state of DefaultListen instead would answer for some other port, and doing it
// silently hides the very error the operator is debugging (a typo'd
// CAVE_UPSTREAM_PROXY is now a load failure).
func TestRunStatus_UnloadableConfigIsUnknownAndLogged(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, "caveman.yaml")
	if err := os.WriteFile(config, []byte("listen: 127.0.0.1:8787\nupstream_proxy: ftp://proxy.corp.example:21\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAVEMAN_HOME", home)
	t.Setenv("CAVEMAN_CONFIG", config)

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	out := captureStdout(t, func() { runStatus(logger, nil) })

	var state map[string]any
	if err := json.Unmarshal([]byte(out), &state); err != nil {
		t.Fatalf("status output %q: %v", out, err)
	}
	if state["owner"] != "unknown" {
		t.Fatalf("status = %v, want owner unknown", state)
	}
	if !strings.Contains(logs.String(), "cannot load caveman.yaml") {
		t.Fatalf("config error was swallowed: %q", logs.String())
	}
}

func TestInstanceIdentityIsPublishedOnlyOnHealth(t *testing.T) {
	const token = "local-instance-token"
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(runstate.InstanceHeader) != "" {
			t.Error("response identity was injected into the provider request")
		}
		w.WriteHeader(http.StatusOK)
	})
	handler := withInstanceIdentity(next, token, "127.0.0.1:8787")
	for _, tt := range []struct {
		method, path string
		wantIdentity bool
	}{
		{http.MethodGet, "/health/live", true},
		{http.MethodGet, "/health/ready", false},
		{http.MethodPost, "/health/live", false},
		{http.MethodPost, "/v1/messages", false},
		{http.MethodPost, "/w/pi/openai/v1/chat/completions", false},
	} {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(tt.method, tt.path, nil))
			want := ""
			if tt.wantIdentity {
				want = token
				if response.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("identity response may be cached across listener generations")
				}
			}
			if got := response.Header().Get(runstate.InstanceHeader); got != want {
				t.Fatalf("identity header = %q, want %q", got, want)
			}
		})
	}
}

// Windows `caveman stop` asks over the listener because no signal reaches a
// detached proxy there gracefully. Only the run-state file's shutdown token
// stops it, only on a loopback listener, and never from a browser page or a
// name that resolves to loopback (DNS rebinding).
func TestShutdownNeedsThisGenerationsTokenOnLoopback(t *testing.T) {
	const token = "local-shutdown-token"
	for _, tt := range []struct {
		name, header, host string
		browser            map[string]string
		loopback           bool
		wantStatus         int
		wantStop           bool
	}{
		{"matching token", token, "127.0.0.1:8787", nil, true, http.StatusAccepted, true},
		{"localhost", token, "localhost:8787", nil, true, http.StatusAccepted, true},
		{"ipv6 loopback", token, "[::1]:8787", nil, true, http.StatusAccepted, true},
		{"wrong token", "other", "127.0.0.1:8787", nil, true, http.StatusForbidden, false},
		{"no token", "", "127.0.0.1:8787", nil, true, http.StatusForbidden, false},
		{"rebound host", token, "attacker.example:8787", nil, true, http.StatusForbidden, false},
		{"host without port", token, "127.0.0.1", nil, true, http.StatusForbidden, false},
		{"origin", token, "127.0.0.1:8787", map[string]string{"Origin": "http://127.0.0.1:8787"}, true, http.StatusForbidden, false},
		{"fetch metadata", token, "127.0.0.1:8787", map[string]string{"Sec-Fetch-Site": "same-origin"}, true, http.StatusForbidden, false},
		{"shared listener", token, "127.0.0.1:8787", nil, false, http.StatusTeapot, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stopped := false
			handler := withShutdown(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusTeapot)
			}), token, tt.loopback, func() { stopped = true })
			request := httptest.NewRequest(http.MethodPost, "/caveman/shutdown", nil)
			request.Host = tt.host
			if tt.header != "" {
				request.Header.Set(runstate.ShutdownHeader, tt.header)
			}
			for name, value := range tt.browser {
				request.Header.Set(name, value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tt.wantStatus || stopped != tt.wantStop {
				t.Fatalf("status %d stopped %v, want %d %v", response.Code, stopped, tt.wantStatus, tt.wantStop)
			}
		})
	}
	response := httptest.NewRecorder()
	withShutdown(http.NotFoundHandler(), token, true, func() { t.Fatal("GET stopped the proxy") }).
		ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/caveman/shutdown", nil))
}

// /health/live hands its caller the instance token on loopback, even when the
// gate behind it answers 401 and whatever Host the caller names. That token
// must not stop the proxy, and the one that does never leaves the run state.
func TestShutdownTokenIsNotTheIdentityHealthPublishes(t *testing.T) {
	state, err := runstate.New("127.0.0.1:8787", "record", "start", "test")
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	handler := withRunState(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gated", http.StatusUnauthorized)
	}), state, true, func() { stopped = true })
	health := httptest.NewRecorder()
	probe := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	probe.Host = "attacker.example"
	handler.ServeHTTP(health, probe)
	leaked := health.Header().Get(runstate.InstanceHeader)
	if leaked != state.InstanceToken {
		t.Fatalf("fixture: health published %q, want the instance token", leaked)
	}
	for name, values := range health.Header() {
		for _, value := range values {
			if strings.Contains(value, state.ShutdownToken) {
				t.Fatalf("health header %s carries the shutdown token", name)
			}
		}
	}
	for _, header := range []string{runstate.ShutdownHeader, runstate.InstanceHeader} {
		request := httptest.NewRequest(http.MethodPost, "/caveman/shutdown", nil)
		request.Host = "127.0.0.1:8787"
		request.Header.Set(header, leaked)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || stopped {
			t.Fatalf("the published identity in %s: status %d stopped %v, want 403", header, response.Code, stopped)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/caveman/shutdown", nil)
	request.Host = "127.0.0.1:8787"
	request.Header.Set(runstate.ShutdownHeader, state.ShutdownToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || !stopped {
		t.Fatalf("the run state's shutdown token: status %d stopped %v, want 202", response.Code, stopped)
	}
}

func TestRunStatusRequiresThisListenerGeneration(t *testing.T) {
	for _, matching := range []bool{false, true} {
		t.Run(strconv.FormatBool(matching), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CAVEMAN_HOME", home)
			const listenerToken = "live-listener-token"
			server := httptest.NewServer(withInstanceIdentity(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}), listenerToken, "127.0.0.1:0"))
			defer server.Close()
			state, err := runstate.New(strings.TrimPrefix(server.URL, "http://"), "record", "start", "test")
			if err != nil {
				t.Fatal(err)
			}
			if matching {
				state.InstanceToken = listenerToken
			}
			state.ProviderUpstreams = map[string]string{"openai": "https://api.openai.com"}
			if err := runstate.Write(home, state); err != nil {
				t.Fatal(err)
			}
			// The test process is named caveman-proxy.test, so it passes the real
			// executable/liveness checks. Only the listener token distinguishes
			// this generation from a stale file naming the same live process.
			out := captureStdout(t, func() {
				runStatus(slog.New(slog.NewTextHandler(io.Discard, nil)), []string{"--port", strconv.Itoa(state.Port)})
			})
			var got runstate.PublicState
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatal(err)
			}
			if matching {
				if got.Owner != "start" || got.InstanceToken != listenerToken || got.ProviderUpstreams["openai"] != "https://api.openai.com" {
					t.Fatalf("matching listener did not publish its route: %s", out)
				}
			} else if got.Owner != "unknown" || got.InstanceToken != "" || len(got.ProviderUpstreams) != 0 {
				t.Fatalf("stale listener identity authorized routing: %s", out)
			}
		})
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = f
	defer func() {
		os.Stdout = previous
		_ = f.Close()
	}()
	fn()
	os.Stdout = previous
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestCaptureStdoutHandlesLargeOutput(t *testing.T) {
	want := strings.Repeat("output", 64*1024)
	got := captureStdout(t, func() {
		if _, err := io.WriteString(os.Stdout, want); err != nil {
			t.Fatal(err)
		}
	})
	if got != want {
		t.Fatalf("captured %d bytes, want %d", len(got), len(want))
	}
}

// The identity header exists so the local CLI can match a run-state file it can
// already read. On a shared listener it only hands unauthenticated /health/live
// callers a value that correlates restarts and tells instances apart behind a
// load balancer, so a non-loopback bind publishes nothing.
func TestInstanceIdentityIsLoopbackOnly(t *testing.T) {
	for _, tt := range []struct {
		listen   string
		loopback bool
	}{
		{"127.0.0.1:8787", true},
		{"localhost:8787", true},
		{"[::1]:8787", true},
		{"0.0.0.0:8787", false},
		{"10.0.0.5:8787", false},
		{"[::]:8787", false},
		{"not-an-address", false},
	} {
		t.Run(tt.listen, func(t *testing.T) {
			if got := loopbackListen(tt.listen); got != tt.loopback {
				t.Fatalf("loopbackListen(%q) = %v, want %v", tt.listen, got, tt.loopback)
			}
			handler := withInstanceIdentity(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}), "listener-token", tt.listen)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health/live", nil))
			want := ""
			if tt.loopback {
				want = "listener-token"
			}
			if got := response.Header().Get(runstate.InstanceHeader); got != want {
				t.Fatalf("identity header = %q, want %q", got, want)
			}
		})
	}
}

// A wildcard bind (CAVEMAN_LISTEN=0.0.0.0:<port>) is still the user's own
// runtime: the CLI and status reach it through loopback, so a loopback caller
// gets the identity there. A caller from the network never does.
func TestInstanceIdentityOnAWildcardBindIsForLoopbackCallersOnly(t *testing.T) {
	for _, tt := range []struct {
		listen, remote string
		want           bool
	}{
		{"0.0.0.0:8787", "127.0.0.1:50000", true},
		{"[::]:8787", "[::1]:50000", true},
		{":8787", "127.0.0.1:50000", true},
		{"0.0.0.0:8787", "192.168.1.20:50000", false},
		{"[::]:8787", "[fe80::1]:50000", false},
		{"10.0.0.5:8787", "127.0.0.1:50000", false},
	} {
		t.Run(tt.listen+" from "+tt.remote, func(t *testing.T) {
			handler := withInstanceIdentity(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}), "listener-token", tt.listen)
			request := httptest.NewRequest(http.MethodGet, "/health/live", nil)
			request.RemoteAddr = tt.remote
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if got := response.Header().Get(runstate.InstanceHeader) == "listener-token"; got != tt.want {
				t.Fatalf("identity published = %v, want %v", got, tt.want)
			}
		})
	}

	// End to end: status validates a wildcard-bound runtime through loopback.
	home := t.TempDir()
	t.Setenv("CAVEMAN_HOME", home)
	server := httptest.NewUnstartedServer(nil)
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	state, err := runstate.New("0.0.0.0:"+port, "record", "start", "test")
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = withRunState(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), state, loopbackListen(state.Listen), func() {})
	server.Start()
	defer server.Close()
	if err := runstate.Write(home, state); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		runStatus(slog.New(slog.NewTextHandler(io.Discard, nil)), []string{"--port", port})
	})
	var got runstate.PublicState
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Owner != "start" || got.InstanceToken != state.InstanceToken {
		t.Fatalf("a wildcard-bound runtime reads as foreign: %s", out)
	}
}

// The keepalive beacon from older CLIs carries no credential and changes
// nothing, so it is answered in front of the inbound token gate. A 401 here
// would make an old CLI log a failure for a no-op.
func TestKeepaliveIsAnsweredBeforeTheTokenGate(t *testing.T) {
	beacons := 0
	gated := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gated", http.StatusUnauthorized)
	})
	handler := withKeepalive(gated, func() { beacons++ })

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/caveman/keepalive", nil))
	if response.Code != http.StatusNoContent || beacons != 1 {
		t.Fatalf("keepalive status = %d, beacons = %d, want 204 and 1", response.Code, beacons)
	}
	// Everything else still reaches the gated handler.
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("inference status = %d, want the gate to answer it", response.Code)
	}
	// A GET is not the beacon.
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/caveman/keepalive", nil))
	if response.Code != http.StatusUnauthorized || beacons != 1 {
		t.Fatalf("GET keepalive status = %d, beacons = %d, want the gate to answer it", response.Code, beacons)
	}
}
