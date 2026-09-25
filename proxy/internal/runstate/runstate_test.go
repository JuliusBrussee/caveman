package runstate

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPortFromListenAcceptsIPv4AndIPv6AndRejectsInvalidPorts(t *testing.T) {
	for listen, want := range map[string]int{
		"127.0.0.1:8787":  8787,
		"[::1]:443":       443,
		"localhost:1":     1,
		"localhost:65535": 65535,
	} {
		got, err := PortFromListen(listen)
		if err != nil || got != want {
			t.Fatalf("PortFromListen(%q) = %d, %v; want %d", listen, got, err, want)
		}
	}
	for _, listen := range []string{"localhost", "localhost:0", "localhost:65536", "localhost:http", ":0"} {
		if _, err := PortFromListen(listen); err == nil {
			t.Fatalf("invalid listen address %q accepted", listen)
		}
	}
}

func TestWriteAndRemoveMatching(t *testing.T) {
	home := t.TempDir()
	state, err := New("127.0.0.1:18787", "compress", "wrap", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(home, state); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(Path(home, state.Port))
	if err != nil {
		t.Fatal(err)
	}
	// POSIX permission bits are synthetic on Windows; NTFS ACLs govern there.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %o, want 600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(Path(home, state.Port)))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %o, want 700", dirInfo.Mode().Perm())
	}
	if err := RemoveMatching(home, state.Port, "successor-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Path(home, state.Port)); err != nil {
		t.Fatalf("mismatched token removed successor state: %v", err)
	}
	if err := RemoveMatching(home, state.Port, state.InstanceToken); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(Path(home, state.Port)); !os.IsNotExist(err) {
		t.Fatalf("matching state still exists: %v", err)
	}
	if err := RemoveMatching(home, state.Port, state.InstanceToken); err != nil {
		t.Fatalf("missing state removal must be idempotent: %v", err)
	}
}

func TestWriteIsAtomicAndReadableContract(t *testing.T) {
	home := t.TempDir()
	state, err := New("127.0.0.1:18788", "record", "start", "dev")
	if err != nil {
		t.Fatal(err)
	}
	state.RecoveryViaMCP = true
	state.CompatForwardHeaders = map[string][]string{"relay": {"X-API-Tenant", "CF-AIG-Authorization"}}
	if err := Write(home, state); err != nil {
		t.Fatal(err)
	}
	got, err := read(home, state.Port)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !reflect.DeepEqual(got, state) {
		t.Fatalf("read state = %+v, want %+v", got, state)
	}
	raw, err := os.ReadFile(Path(home, state.Port))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		t.Fatalf("state file lacks trailing newline: %q", raw)
	}
	matches, err := filepath.Glob(filepath.Join(home, "run", ".runstate-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary state files leaked: %v, %v", matches, err)
	}
}

func TestValidateChecksProcessAndListenerIdentity(t *testing.T) {
	state := State{PID: 42, Listen: "127.0.0.1:8787", InstanceToken: "generation-token"}
	ok := validators{
		alive:      func(int) bool { return true },
		executable: func(int) (string, error) { return "/tmp/caveman-proxy", nil },
		instance:   func(listen, token string) bool { return listen == state.Listen && token == state.InstanceToken },
	}
	if !validate(state, ok) {
		t.Fatal("valid state rejected")
	}
	wrongExe := ok
	wrongExe.executable = func(int) (string, error) { return "/tmp/node", nil }
	if validate(state, wrongExe) {
		t.Fatal("foreign executable accepted")
	}
	exeError := ok
	exeError.executable = func(int) (string, error) { return "", errors.New("identity lookup failed") }
	if validate(state, exeError) {
		t.Fatal("executable lookup failure accepted")
	}
	dead := ok
	dead.alive = func(int) bool { return false }
	if validate(state, dead) {
		t.Fatal("dead pid accepted")
	}
	foreignListener := ok
	foreignListener.instance = func(string, string) bool { return false }
	if validate(state, foreignListener) {
		t.Fatal("live pid and proxy executable authorized a foreign listener")
	}
}

func TestReadRejectsEveryInvalidContractField(t *testing.T) {
	valid := `{"schema":"caveman.proxy.run.v1","pid":1,"port":8787,"listen":"127.0.0.1:8787","mode":"record","owner":"start","instance_token":"token"}`
	tests := map[string]string{
		"malformed JSON": `{`,
		"wrong schema":   strings.Replace(valid, Schema, "future", 1),
		"wrong port":     strings.Replace(valid, `"port":8787`, `"port":8788`, 1),
		"wrong listen":   strings.Replace(valid, `"listen":"127.0.0.1:8787"`, `"listen":"127.0.0.1:8788"`, 1),
		"zero pid":       strings.Replace(valid, `"pid":1`, `"pid":0`, 1),
		"missing token":  strings.Replace(valid, `"instance_token":"token"`, `"instance_token":""`, 1),
		"unknown owner":  strings.Replace(valid, `"owner":"start"`, `"owner":"other"`, 1),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.MkdirAll(filepath.Join(home, "run"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(Path(home, 8787), []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := read(home, 8787); err == nil {
				t.Fatal("invalid run-state contract accepted")
			}
			if err := RemoveMatching(home, 8787, "token"); err == nil {
				t.Fatal("RemoveMatching accepted invalid contract")
			}
		})
	}
}

func TestUnknownSchemaFailsClosed(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "run")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(home, 8787), []byte(`{"schema":"future","pid":1,"port":8787}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := ReadValidated(home, 8787)
	if got.Owner != "unknown" || got.Mode != "" {
		t.Fatalf("got %#v, want owner unknown and no mode", got)
	}
}

func TestNewUsesUTCAnd128BitToken(t *testing.T) {
	state, err := New("127.0.0.1:8787", "record", "invalid", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if state.Owner != "start" {
		t.Fatalf("owner = %q", state.Owner)
	}
	if len(state.InstanceToken) != 32 {
		t.Fatalf("token chars = %d, want 32", len(state.InstanceToken))
	}
	if state.StartedAt.Location() != time.UTC {
		t.Fatalf("started_at location = %v", state.StartedAt.Location())
	}
	if _, err := New("invalid", "record", "start", "dev"); err == nil {
		t.Fatal("New accepted invalid listen address")
	}
}

func TestProcessProbes(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Fatal("current process reported dead")
	}
	if processAlive(1 << 30) {
		t.Fatal("impossible process reported alive")
	}
	executable, err := processExecutable(os.Getpid())
	if err != nil || strings.TrimSpace(executable) == "" {
		t.Fatalf("current executable = %q, %v", executable, err)
	}
}

func TestInstanceProbeRequiresExactLiveTokenWithoutSendingIt(t *testing.T) {
	const token = "private-run-state-generation-token"
	for _, tt := range []struct {
		name    string
		status  int
		headers []string
		want    bool
	}{
		{"matching generation", http.StatusOK, []string{token}, true},
		{"old proxy without identity", http.StatusOK, nil, false},
		{"different generation", http.StatusOK, []string{"other-token"}, false},
		{"ambiguous identity", http.StatusOK, []string{token, "other-token"}, false},
		{"unhealthy matching generation", http.StatusServiceUnavailable, []string{token}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.RequestURI() != "/health/live" {
					t.Errorf("unexpected proof request: %s %s", r.Method, r.URL)
				}
				if strings.Contains(r.URL.String(), token) || strings.Contains(r.Header.Get(InstanceHeader), token) {
					t.Error("probe disclosed the expected identity to the listener")
				}
				for _, value := range tt.headers {
					w.Header().Add(InstanceHeader, value)
				}
				w.WriteHeader(tt.status)
			}))
			defer server.Close()
			listen := strings.TrimPrefix(server.URL, "http://")
			if got := instanceMatches(listen, token); got != tt.want {
				t.Fatalf("instanceMatches = %v, want %v", got, tt.want)
			}
			if instanceMatches(listen, "") {
				t.Fatal("missing expected token accepted")
			}
		})
	}
}

func TestInstanceProbeNeverFollowsRedirectOrUsesEnvironmentProxy(t *testing.T) {
	const token = "generation-token"
	redirectHits, proxyHits := 0, 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectHits++
		w.Header().Set(InstanceHeader, token)
	}))
	defer target.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits++
		w.Header().Set(InstanceHeader, token)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/health/live", http.StatusFound)
	}))
	defer redirect.Close()
	if instanceMatches(strings.TrimPrefix(redirect.URL, "http://"), token) {
		t.Fatal("redirect authorized a different listener")
	}
	if redirectHits != 0 || proxyHits != 0 {
		t.Fatalf("proof escaped its listener: redirect hits=%d, proxy hits=%d", redirectHits, proxyHits)
	}
}

func TestInstanceProbeRejectsClosedListener(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(InstanceHeader, "generation-token")
	}))
	listen := strings.TrimPrefix(server.URL, "http://")
	server.Close()
	if instanceMatches(listen, "generation-token") {
		t.Fatal("closed listener authorized a stale run-state file")
	}
}

func TestCompatUpstreamsRoundTripAndAbsentFieldStaysNil(t *testing.T) {
	home := t.TempDir()
	state, err := New("127.0.0.1:8791", "record", "start", "test")
	if err != nil {
		t.Fatal(err)
	}
	state.CompatUpstreams = map[string]string{
		"opencode-go": "https://opencode.ai/zen/go",
		"myprovider":  "http://127.0.0.1:4000",
	}
	if err := Write(home, state); err != nil {
		t.Fatal(err)
	}
	got, err := read(home, state.Port)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.CompatUpstreams) != 2 ||
		got.CompatUpstreams["opencode-go"] != "https://opencode.ai/zen/go" ||
		got.CompatUpstreams["myprovider"] != "http://127.0.0.1:4000" {
		t.Fatalf("compat upstreams round-trip = %v", got.CompatUpstreams)
	}

	// A file from a proxy that predates the field must still read.
	old, err := New("127.0.0.1:8792", "record", "start", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(home, old); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(Path(home, old.Port))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "compat_upstreams") {
		t.Fatalf("empty compat upstreams must be omitted: %s", raw)
	}
	back, err := read(home, old.Port)
	if err != nil {
		t.Fatalf("run state without compat_upstreams must read: %v", err)
	}
	if back.CompatUpstreams != nil {
		t.Fatalf("absent field = %v, want nil", back.CompatUpstreams)
	}
}

func TestRoutingUpstreamsNeverPublishesURLCredentials(t *testing.T) {
	home := t.TempDir()
	state, err := New("127.0.0.1:8798", "record", "start", "test")
	if err != nil {
		t.Fatal(err)
	}
	state.ProviderUpstreams = map[string]string{"openai": "https://api.openai.com", "anthropic": "https://user:secret@relay.example/tenant"}
	state.CompatUpstreams = map[string]string{"relay": "https://relay.example/tenant?api_key=secret", "plain": "https://relay.example/tenant"}
	if err := Write(home, state); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(Path(home, state.Port))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "api_key") {
		t.Fatalf("credential in run state: %s", raw)
	}
	got, err := read(home, state.Port)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProviderUpstreams["openai"] != "https://api.openai.com" || got.CompatUpstreams["plain"] != "https://relay.example/tenant" {
		t.Fatalf("safe endpoints lost: %s", raw)
	}
	if value, exists := got.ProviderUpstreams["anthropic"]; !exists || value != "" {
		t.Fatalf("unavailable provider must remain explicit: %s", raw)
	}
	if value, exists := got.CompatUpstreams["relay"]; !exists || value != "" {
		t.Fatalf("unavailable mount must remain explicit: %s", raw)
	}
	if state.ProviderUpstreams["anthropic"] != "https://user:secret@relay.example/tenant" {
		t.Fatal("publication changed live configuration")
	}
}
