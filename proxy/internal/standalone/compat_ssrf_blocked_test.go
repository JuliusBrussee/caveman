package standalone

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/config"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

// #1440: a compat mount whose base_url is a loopback relay (Headroom on
// 127.0.0.1) is refused by the standalone SSRF dial guard unless the operator
// allow-lists it. That refusal is correct and stays. What was wrong is how it
// surfaced: the same 502 cave_upstream_unavailable as a provider outage, and no
// log line at the default level, so the escape hatch (CAVE_SSRF_ALLOWLIST)
// was invisible. The refusal must carry its own error code and one WARN line
// that names the blocked host and the setting.
func TestCompatLoopbackUpstreamBlockedBySSRFGuardSaysSo(t *testing.T) {
	t.Setenv("CAVE_SSRF_ALLOWLIST", "")
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg","type":"message","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()

	spend, err := store.Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer spend.Close()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	// Options without an HTTPClient: the real SSRF-guarded standalone client.
	srv := New(config.Config{Mode: "record", Compat: map[string]config.CompatConfig{
		"legohr": {BaseURL: upstream.URL, WireDialect: "anthropic"},
	}}, spend, Options{Logger: logger})

	req := httptest.NewRequest(http.MethodPost, "/compat/legohr/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "sk-relay")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if hits.Load() != 0 {
		t.Fatalf("the guard must still refuse the loopback upstream; it was reached %d times", hits.Load())
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %s)", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	if envelope.Error.Code != "cave_upstream_blocked" {
		t.Fatalf("error code = %q, want cave_upstream_blocked so a guard refusal is not read as an outage (body %s)", envelope.Error.Code, rec.Body.String())
	}
	line := ""
	for _, candidate := range strings.Split(logs.String(), "\n") {
		if strings.Contains(candidate, "level=WARN") && strings.Contains(candidate, "SSRF") {
			line = candidate
		}
	}
	if line == "" {
		t.Fatalf("no WARN line for the blocked upstream; logs:\n%s", logs.String())
	}
	for _, want := range []string{"127.0.0.1", "CAVE_SSRF_ALLOWLIST"} {
		if !strings.Contains(line, want) {
			t.Fatalf("WARN line does not name %s: %s", want, line)
		}
	}
	if strings.Contains(line, "sk-relay") {
		t.Fatalf("WARN line leaked the credential: %s", line)
	}
}
