package standalone

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/proxy/internal/config"
	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
	"github.com/JuliusBrussee/caveman/proxy/internal/nativeruntime"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
	"github.com/JuliusBrussee/caveman/proxy/providers"
)

type captureUpstreamTransport struct {
	body     []byte
	headers  http.Header
	status   int
	response string
}

func (t *captureUpstreamTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	t.body = append([]byte(nil), body...)
	t.headers = r.Header.Clone()
	status := t.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"u1"}},
		Body:       io.NopCloser(strings.NewReader(t.response)),
		Request:    r,
	}, nil
}

// TestStandaloneBoot_ZeroCloudDeps_InferredRows boots the full standalone server
// — config + BYOK + SQLite — with no Valkey/Postgres/ClickHouse anywhere, proxies
// one request to a loopback upstream, and asserts the persisted spend row is
// labeled `inferred` with a positive cost.
func TestStandaloneBoot_ZeroCloudDeps_InferredRows(t *testing.T) {
	const respBody = `{"id":"resp_stub","model":"gpt-5.5","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":1000,"output_tokens":120,"input_tokens_details":{"cached_tokens":0}}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.Header().Set("x-request-id", "u1")
		_, _ = io.WriteString(w, respBody)
	}))
	defer upstream.Close()

	spend, err := store.Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer spend.Close()

	cfg := config.Config{
		Mode:      "record",
		Providers: map[string]config.ProviderConfig{"openai": {BaseURL: upstream.URL}},
	}
	// Plain client: the upstream stub is on loopback; the SSRF-guarded default
	// (exercised in the SSRF test) would block it.
	srv := New(cfg, spend, Options{HTTPClient: &http.Client{}})

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{"model":"gpt-5.5","input":"hi"}`))
	req.Header.Set("authorization", "Bearer sk-openai-test")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	stats, err := spend.Summary()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if stats.Requests != 1 {
		t.Errorf("requests = %d, want 1 row persisted", stats.Requests)
	}
	if stats.TotalCost <= 0 {
		t.Errorf("total cost = %v, want > 0", stats.TotalCost)
	}
	if stats.Basis != "inferred" {
		t.Errorf("basis = %q, want inferred (standalone never claims verified)", stats.Basis)
	}
}

func TestStandaloneStripsSignedNativeSessionMarkerBeforeProviderAndCorrelatesRow(t *testing.T) {
	const response = `{"id":"resp","model":"gpt-5.5","output":[],"usage":{"input_tokens":10,"output_tokens":1}}`
	upstream := &captureUpstreamTransport{response: response}
	spend, err := store.Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer spend.Close()
	sink := &recordingSink{inner: spend}
	key := bytes.Repeat([]byte{4}, 32)
	marker, err := nativeruntime.SessionMarker(key, "claude:host-77")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Mode: "record"}
	srv := New(cfg, sink, Options{
		HTTPClient:       &http.Client{Transport: upstream},
		SessionMarkerKey: key,
	})
	original := `{"model":"gpt-5.5","input":"Core\n` + marker + `\nkeep exact"}`
	want := `{"model":"gpt-5.5","input":"Core\nkeep exact"}`
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(original))
	req.Header.Set("authorization", "Bearer sk-openai-test")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if string(upstream.body) != want {
		t.Fatalf("provider bytes changed beyond marker removal:\ngot  %s\nwant %s", upstream.body, want)
	}
	if strings.Contains(string(upstream.body), "caveman-session-v1") {
		t.Fatal("session marker reached provider")
	}
	if sink.last.SessionID != "claude:host-77" {
		t.Fatalf("telemetry session id = %q, want signed marker identity", sink.last.SessionID)
	}
	if sink.last.SessionCorrelationBasis != "signed_marker" {
		t.Fatalf("correlation basis = %q, want signed_marker", sink.last.SessionCorrelationBasis)
	}
}

func TestStandaloneUsesOnlyExplicitApproximateSessionFallback(t *testing.T) {
	const response = `{"id":"resp","model":"gpt-5.5","output":[],"usage":{"input_tokens":10,"output_tokens":1}}`
	upstream := &captureUpstreamTransport{response: response}
	spend, err := store.Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer spend.Close()
	sink := &recordingSink{inner: spend}
	srv := New(config.Config{Mode: "record"}, sink, Options{
		HTTPClient: &http.Client{Transport: upstream},
		SessionFallback: func(time.Time, string, string) (string, string) {
			return "claude:recent", "unique_recent_time_model"
		},
	})
	original := `{"model":"gpt-5.5","input":"markerless exact bytes"}`
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(original))
	req.Header.Set("authorization", "Bearer sk-openai-test")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if string(upstream.body) != original {
		t.Fatalf("fallback changed provider bytes: got %s want %s", upstream.body, original)
	}
	if sink.last.SessionID != "claude:recent" || sink.last.SessionCorrelationBasis != "unique_recent_time_model" {
		t.Fatalf("fallback correlation not labeled approximate: %+v", sink.last)
	}
}

func TestStandaloneLeavesAmbiguousFallbackUncorrelated(t *testing.T) {
	upstream := &captureUpstreamTransport{response: `{"id":"resp","model":"gpt-5.5","output":[]}`}
	spend, err := store.Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer spend.Close()
	sink := &recordingSink{inner: spend}
	srv := New(config.Config{Mode: "record"}, sink, Options{
		HTTPClient: &http.Client{Transport: upstream},
		SessionFallback: func(time.Time, string, string) (string, string) {
			return "", ""
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{"model":"gpt-5.5","input":"ambiguous"}`))
	req.Header.Set("authorization", "Bearer sk-openai-test")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if sink.last.SessionID != "" || sink.last.SessionCorrelationBasis != "" {
		t.Fatalf("ambiguous fallback must remain uncorrelated: %+v", sink.last)
	}
}

// TestStandaloneSSRF_BlocksPrivateUpstream proves the SSRF dial guard is always
// on in standalone (not gated on CAVE_ENV=prod): a request whose upstream is a
// private RFC1918 address is blocked at dial time, surfacing as a 502 rather than
// letting the local proxy reach an internal host.
func TestStandaloneSSRF_BlocksPrivateUpstream(t *testing.T) {
	spend, err := store.Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer spend.Close()

	cfg := config.Config{
		Mode:      "record",
		Providers: map[string]config.ProviderConfig{"openai": {BaseURL: "https://10.0.0.1:443"}},
	}
	// Options{} → the default SSRF-guarded standalone client (no plain-client override).
	srv := New(cfg, spend, Options{})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.5","input":"hi"}`))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 — a private-IP upstream must be blocked by the SSRF guard", rec.Code)
	}
}

func TestStandaloneProductionTransportPreservesEncodedResponseWireBytes(t *testing.T) {
	payload := []byte(`{"id":"resp_gzip","model":"gpt-5.5","usage":{"input_tokens":3,"output_tokens":2}}`)
	var encoded bytes.Buffer
	zw := gzip.NewWriter(&encoded)
	if _, err := zw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	wireBody := append([]byte(nil), encoded.Bytes()...)
	acceptEncodings := make(chan string, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptEncodings <- r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("X-Request-Id", "gzip-wire")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(wireBody)
	}))
	defer upstream.Close()

	t.Setenv("CAVE_SSRF_ALLOWLIST", "127.0.0.1")
	spend, err := store.Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer spend.Close()
	srv := New(config.Config{
		Mode:      "record",
		Providers: map[string]config.ProviderConfig{"openai": {BaseURL: upstream.URL}},
	}, spend, Options{HTTPClient: StandaloneHTTPClient(time.Minute)})

	for _, tc := range []struct {
		name           string
		acceptEncoding string
	}{
		{name: "absent"},
		{name: "explicit", acceptEncoding: "gzip"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{"model":"gpt-5.5","input":"wire"}`))
			req.Header.Set("authorization", "Bearer sk-openai-test")
			if tc.acceptEncoding != "" {
				req.Header.Set("Accept-Encoding", tc.acceptEncoding)
			}
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%q", rec.Code, rec.Body.Bytes())
			}
			if got := <-acceptEncodings; got != tc.acceptEncoding {
				t.Fatalf("upstream Accept-Encoding=%q, want %q", got, tc.acceptEncoding)
			}
			if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
				t.Fatalf("Content-Encoding=%q, want gzip", got)
			}
			if !bytes.Equal(rec.Body.Bytes(), wireBody) {
				t.Fatalf("encoded provider bytes changed: got=%x want=%x", rec.Body.Bytes(), wireBody)
			}
		})
	}
}

// TestEngineCompressor_StoreOriginalRoundTrip proves the recovery contract that
// compress mode relies on: the handle StoreOriginal returns resolves, through a
// plain engine over the same CCR store, back to the exact original bytes.
func TestEngineCompressor_StoreOriginalRoundTrip(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatalf("open ccr: %v", err)
	}
	defer store.Close()

	comp := NewEngineCompressor(store)
	original := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"recover me exactly"}]}`)
	handle, err := comp.StoreOriginal(original)
	if err != nil {
		t.Fatalf("store original: %v", err)
	}
	if handle == "" {
		t.Fatal("StoreOriginal returned an empty handle")
	}

	got, err := engine.New(store, nil).Retrieve(handle)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Errorf("retrieved bytes != original:\n got %s\nwant %s", got, original)
	}
}

// TestEngineCompressor_QueryTargetedRetrieve proves the query-targeted recovery
// path copied from Headroom's store.search: with no query the full original content
// block comes back byte-exact; with a query only BM25-relevant sections return, so
// a model needing one detail does not re-ingest the whole block. StoreOriginal is
// called per provider content block, not with an enclosing wire request.
func TestEngineCompressor_QueryTargetedRetrieve(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatalf("open ccr: %v", err)
	}
	defer store.Close()

	comp := &engineCompressor{eng: engine.New(store, nil), store: store}
	original := []byte("Section about kubernetes pod scheduling and node affinity rules.\n\n" +
		"Section about postgres vacuum tuning and autovacuum thresholds.\n\n" +
		"Section about redis eviction policies and maxmemory settings.")
	handle, err := comp.StoreOriginal(original)
	if err != nil {
		t.Fatalf("store original: %v", err)
	}

	full, err := comp.RetrieveOriginal(handle, "")
	if err != nil {
		t.Fatalf("full retrieve: %v", err)
	}
	if !bytes.Equal(full, original) {
		t.Errorf("empty query must return the byte-exact original:\n got %s", full)
	}

	narrowed, err := comp.RetrieveOriginal(handle, "postgres autovacuum tuning")
	if err != nil {
		t.Fatalf("query retrieve: %v", err)
	}
	if !strings.Contains(string(narrowed), "vacuum") {
		t.Errorf("query-targeted retrieve must include the relevant section, got: %s", narrowed)
	}
	if strings.Contains(string(narrowed), "kubernetes") || strings.Contains(string(narrowed), "redis eviction") {
		t.Errorf("query-targeted retrieve must drop irrelevant sections, got: %s", narrowed)
	}
	if len(narrowed) >= len(full) {
		t.Errorf("query-targeted retrieve (%d bytes) must be smaller than full recovery (%d bytes)", len(narrowed), len(full))
	}
}

// TestStandaloneCompressMode_RealEngine_Recoverable boots standalone in compress
// mode with the real engine, sends a request whose message content is a
// compressible JSON payload, and proves the disclosed CCR handle recovers the exact
// original live-zone block through the engine.
func TestStandaloneCompressMode_RealEngine_Recoverable(t *testing.T) {
	t.Setenv("CAVE_ENGINE_TOON", "")
	const respBody = `{"id":"c","object":"chat.completion","model":"gpt-5.5","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1000,"completion_tokens":10}}`
	upstream := &captureUpstreamTransport{response: respBody}

	spend, err := store.Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer spend.Close()

	recovery, err := ccr.OpenMemory()
	if err != nil {
		t.Fatalf("open ccr: %v", err)
	}
	defer recovery.Close()

	cfg := config.Config{
		Mode:      "compress",
		Providers: map[string]config.ProviderConfig{"openai": {BaseURL: "https://upstream.test"}},
	}
	srv := New(cfg, spend, Options{HTTPClient: &http.Client{Transport: upstream}, Compressor: NewEngineCompressor(recovery)})

	// Message content is a JSON document with a long array — the engine's JSON
	// compressor collapses arrays longer than 8, guaranteeing a real reduction.
	items := make([]map[string]any, 80)
	for i := range items {
		items[i] = map[string]any{"id": i + 1, "name": "very repetitive fixture row", "city": "boulder"}
	}
	inner, _ := json.Marshal(map[string]any{"items": items})
	if len(inner) < 512 {
		t.Fatalf("test fixture is %d bytes, want live-zone eligible", len(inner))
	}
	reqMap := map[string]any{"model": "gpt-5.5", "messages": []any{map[string]any{"role": "user", "content": string(inner)}}}
	reqBytes, _ := json.Marshal(reqMap)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(reqBytes))
	req.Header.Set("authorization", "Bearer sk-openai-test")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	handle := rec.Header().Get("x-caveman-recovery-handle")
	if handle == "" {
		t.Fatal("compress mode produced no recovery handle for a compressible JSON payload")
	}
	if string(upstream.body) == string(reqBytes) {
		t.Error("upstream received the original body; compress mode should have shrunk it")
	}

	got, err := engine.New(recovery, nil).Retrieve(handle)
	if err != nil {
		t.Fatalf("retrieve original block: %v", err)
	}
	if !bytes.Equal(got, inner) {
		t.Errorf("recovered block != original:\n got %s\nwant %s", got, inner)
	}
	stats, err := recovery.Summary()
	if err != nil {
		t.Fatalf("recovery stats: %v", err)
	}
	if stats.Totals.TokensBefore <= 0 || stats.Totals.TokensAfter <= 0 || stats.Totals.TokensAfter >= stats.Totals.TokensBefore {
		t.Fatalf("standalone compression erased engine CCR accounting: %+v", stats.Totals)
	}
}

// TestStandaloneCompressMode_RealEngine_QueryAware proves query relevance reaches
// the real JSON compressor through the full proxy path. The planted middle row is
// neither positional, anomalous, nor error-like; only latest-user query relevance
// requires it to survive compression.
func TestStandaloneCompressMode_RealEngine_QueryAware(t *testing.T) {
	t.Setenv("CAVE_ENGINE_TOON", "")
	const respBody = `{"id":"c","object":"chat.completion","model":"gpt-5.5","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1000,"completion_tokens":10}}`
	upstream := &captureUpstreamTransport{response: respBody}

	spend, err := store.Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer spend.Close()
	recovery, err := ccr.OpenMemory()
	if err != nil {
		t.Fatalf("open ccr: %v", err)
	}
	defer recovery.Close()

	items := make([]map[string]any, 80)
	for i := range items {
		status := "host-0000-alpha"
		if i == 25 {
			status = "host-0025-omega"
		}
		items[i] = map[string]any{"id": i, "status": status}
	}
	toolOutput, _ := json.Marshal(map[string]any{"items": items})
	reqBytes, _ := json.Marshal(map[string]any{
		"model": "gpt-5.5",
		"messages": []any{
			map[string]any{"role": "user", "content": "find host-0025-omega"},
			map[string]any{"role": "assistant", "content": "", "tool_calls": []any{
				map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "inventory", "arguments": "{}"}},
			}},
			map[string]any{"role": "tool", "tool_call_id": "c1", "content": string(toolOutput)},
		},
	})
	srv := New(config.Config{
		Mode:      "compress",
		Providers: map[string]config.ProviderConfig{"openai": {BaseURL: "https://upstream.test"}},
	}, spend, Options{
		HTTPClient:     &http.Client{Transport: upstream},
		Compressor:     NewEngineCompressor(recovery),
		RecoveryViaMCP: true,
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(reqBytes))
	req.Header.Set("authorization", "Bearer sk-openai-test")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(upstream.body, []byte("host-0025-omega")) {
		t.Fatalf("query-relevant middle row was dropped by full proxy path: %s", upstream.body)
	}
	if !bytes.Contains(upstream.body, []byte(`__caveman_elided__`)) {
		t.Fatalf("query-aware request did not still compress irrelevant rows: %s", upstream.body)
	}
	if bytes.Equal(upstream.body, reqBytes) {
		t.Fatal("query-aware request passed through unchanged")
	}

	handle := rec.Header().Get("x-caveman-recovery-handle")
	recovered, err := engine.New(recovery, nil).Retrieve(handle)
	if err != nil {
		t.Fatalf("recover query-aware block: %v", err)
	}
	if !bytes.Equal(recovered, toolOutput) {
		t.Fatalf("CCR did not preserve exact original tool output:\n got %s\nwant %s", recovered, toolOutput)
	}
}

// TestStandaloneSubscriptionCompress_NoAccountRequired proves the invariant end to end
// with the real engine: a subscription-authenticated coding-agent request takes
// live-zone compression with NO account signal in the environment at all, while
// `record` mode stays byte-identical pass-through. The persisted row is
// tokens-only — inferred, no dollars.
func TestStandaloneSubscriptionCompress_NoAccountRequired(t *testing.T) {
	t.Setenv("CAVE_ENGINE_TOON", "")
	const respBody = `{"id":"msg","type":"message","model":"claude-sonnet-4-6","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1000,"output_tokens":10}}`

	items := make([]map[string]any, 80)
	for i := range items {
		items[i] = map[string]any{"id": i + 1, "name": "very repetitive fixture row", "city": "boulder"}
	}
	inner, _ := json.Marshal(map[string]any{"items": items})
	reqBytes, _ := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-6",
		"max_tokens": 1024,
		"messages":   []any{map[string]any{"role": "user", "content": string(inner)}},
	})

	run := func(t *testing.T, mode string) ([]byte, gateway.RequestRecord) {
		t.Helper()
		// Deliberately no account signal of any kind in the environment.
		upstream := &captureUpstreamTransport{response: respBody}
		spend, err := store.Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		defer spend.Close()
		recovery, err := ccr.OpenMemory()
		if err != nil {
			t.Fatalf("open ccr: %v", err)
		}
		defer recovery.Close()

		cfg, err := config.Load(filepath.Join(t.TempDir(), "absent.yaml"))
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		cfg.Mode = mode
		cfg.Providers = map[string]config.ProviderConfig{"anthropic": {BaseURL: "https://upstream.test"}}
		sink := &recordingSink{inner: spend}
		// The binary wires exactly these three for compress mode: the engine
		// compressor, the spend store as the durable prefix-replacement cache, and MCP
		// recovery (which `caveman wrap` installs for the agent).
		srv := New(cfg, sink, Options{
			HTTPClient:     &http.Client{Transport: upstream},
			Compressor:     NewEngineCompressor(recovery),
			PrefixCache:    spend,
			RecoveryViaMCP: true,
		})

		req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(reqBytes))
		req.Header.Set("user-agent", "claude-cli/1.0.0")
		req.Header.Set("authorization", "Bearer sk-ant-oat-test")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		return upstream.body, sink.last
	}

	t.Run("record mode stays byte-identical pass-through", func(t *testing.T) {
		got, row := run(t, "record")
		if !bytes.Equal(got, reqBytes) {
			t.Fatalf("record mode must be byte-identical passthrough:\n got %s\nwant %s", got, reqBytes)
		}
		if row.CompressionTokensBefore != 0 {
			t.Fatalf("record row must claim no compression: %+v", row)
		}
	})

	t.Run("no account still compresses the live zone, tokens only", func(t *testing.T) {
		got, row := run(t, "compress")
		if bytes.Equal(got, reqBytes) {
			t.Fatal("an account-less subscription request should have been compressed")
		}
		if !bytes.Contains(got, []byte("<<ccr:")) {
			t.Fatalf("compressed subscription request must disclose a CCR marker: %s", got)
		}
		if row.CompressionTokensBefore <= row.CompressionTokensAfter {
			t.Fatalf("row must record a token reduction: %+v", row)
		}
		if row.CompressionTokenCountBasis != "estimated_engine_o200k" {
			t.Fatalf("compression_token_count_basis = %q, want estimated_engine_o200k", row.CompressionTokenCountBasis)
		}
		if row.Basis != "inferred" {
			t.Fatalf("basis = %q, want inferred", row.Basis)
		}
		if row.TotalCostUSD != 0 || row.SavingsUSD != 0 || row.WouldSaveUSD != nil {
			t.Fatalf("subscription row must carry no dollars: %+v", row)
		}
	})
}

// recordingSink tees the lifecycle row so a test can assert on it while the real
// SQLite store still applies its own persistence-boundary re-zeroing.
type recordingSink struct {
	inner gateway.TelemetrySink
	last  gateway.RequestRecord
}

func (s *recordingSink) Record(rec gateway.RequestRecord) {
	s.last = rec
	s.inner.Record(rec)
}

// TestCreds_PassthroughThenBYOK proves the credential resolver preserves a real
// inbound provider credential and falls back to the operator BYOK env key only
// when the request carries no credential.
func TestCreds_PassthroughThenBYOK(t *testing.T) {
	c := Creds{cfg: config.Config{}}

	rRaw := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	rRaw.Header.Set("x-api-key", "sk-from-agent")
	if got := c.Resolve("anthropic", rRaw).Key; got != "sk-from-agent" {
		t.Errorf("passthrough key = %q, want sk-from-agent", got)
	}

	// OpenAI-style Bearer passthrough.
	rBearer := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rBearer.Header.Set("authorization", "Bearer sk-openai-agent")
	if got := c.Resolve("openai", rBearer).Key; got != "sk-openai-agent" {
		t.Errorf("bearer passthrough key = %q, want sk-openai-agent", got)
	}

	t.Setenv("ANTHROPIC_API_KEY", "sk-byok-env")
	if got := c.Resolve("anthropic", rRaw).Key; got != "sk-from-agent" {
		t.Errorf("credential with env present = %q, want inbound sk-from-agent", got)
	}
	rNoAuth := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	if got := c.Resolve("anthropic", rNoAuth).Key; got != "sk-byok-env" {
		t.Errorf("BYOK fallback key = %q, want sk-byok-env", got)
	}
}

func TestBuildAdapters_RegistersNamedCompatBeforeLegacy(t *testing.T) {
	cfg := config.Config{
		Compat: map[string]config.CompatConfig{
			"openrouter": {BaseURL: "https://openrouter.ai/api"},
			"groq":       {BaseURL: "https://api.groq.com/openai"},
		},
		Providers: map[string]config.ProviderConfig{
			"openai_compatible": {BaseURL: "https://legacy.example.test"},
		},
	}
	adapters := buildAdapters(cfg)

	req := httptest.NewRequest(http.MethodPost, "/compat/openrouter/v1/chat/completions", nil)
	matched := false
	for _, adapter := range adapters {
		if !adapter.MatchRoute(req.Method, req.URL.Path) {
			continue
		}
		matched = true
		upstream, err := adapter.ResolveUpstreamURL(req.Context(), req, providers.RouteContext{})
		if err != nil {
			t.Fatalf("resolve named compat: %v", err)
		}
		want := "https://openrouter.ai/api/v1/chat/completions"
		if got := upstream.String(); got != want {
			t.Fatalf("first matching adapter resolved %q, want named upstream %q", got, want)
		}
		break
	}
	if !matched {
		t.Fatal("no adapter matched named compat route")
	}

	legacyReq := httptest.NewRequest(http.MethodPost, "/compat/openai-compatible/v1/chat/completions", nil)
	matched = false
	for _, adapter := range adapters {
		if !adapter.MatchRoute(legacyReq.Method, legacyReq.URL.Path) {
			continue
		}
		matched = true
		upstream, err := adapter.ResolveUpstreamURL(legacyReq.Context(), legacyReq, providers.RouteContext{})
		if err != nil {
			t.Fatalf("resolve legacy compat: %v", err)
		}
		want := "https://legacy.example.test/v1/chat/completions"
		if got := upstream.String(); got != want {
			t.Fatalf("legacy adapter resolved %q, want %q", got, want)
		}
		break
	}
	if !matched {
		t.Fatal("no adapter matched legacy compat route")
	}
}

func TestBuildAdapters_DefaultCompatBareRoutePreservesConfiguredBase(t *testing.T) {
	cfg := config.Config{Providers: map[string]config.ProviderConfig{
		"openai_compatible": {BaseURL: "http://127.0.0.1:11434/v1?tenant=local"},
	}}
	adapters := buildAdapters(cfg)
	req := httptest.NewRequest(http.MethodPost, "/compat/v1/chat/completions?stream=true", nil)
	for _, adapter := range adapters {
		if adapter.Name() != "openai_compatible" || !adapter.MatchRoute(req.Method, req.URL.Path) {
			continue
		}
		upstream, err := adapter.ResolveUpstreamURL(req.Context(), req, providers.RouteContext{})
		if err != nil {
			t.Fatalf("resolve default compat: %v", err)
		}
		want := "http://127.0.0.1:11434/v1/chat/completions?tenant=local&stream=true"
		if got := upstream.String(); got != want {
			t.Fatalf("default compat upstream = %q, want %q", got, want)
		}
		return
	}
	t.Fatal("default compat adapter was not registered for configured base URL")
}

func TestBuildAdapters_RegistersBedrockFromResolvedRegion(t *testing.T) {
	t.Setenv("CAVE_BEDROCK_REGION", "")
	t.Setenv("AWS_REGION", "eu-west-1")
	t.Setenv("AWS_DEFAULT_REGION", "us-east-2")
	adapters := buildAdapters(config.Config{})

	req := httptest.NewRequest(http.MethodPost, "/bedrock/model/anthropic.claude-3-5-sonnet-20241022-v2:0/converse", nil)
	for _, adapter := range adapters {
		if adapter.Name() != "bedrock" {
			continue
		}
		if !adapter.MatchRoute(req.Method, req.URL.Path) {
			t.Fatal("Bedrock adapter registered but did not match its native route")
		}
		upstream, err := adapter.ResolveUpstreamURL(req.Context(), req, providers.RouteContext{})
		if err != nil {
			t.Fatalf("resolve Bedrock route: %v", err)
		}
		want := "https://bedrock-runtime.eu-west-1.amazonaws.com/model/anthropic.claude-3-5-sonnet-20241022-v2:0/converse"
		if got := upstream.String(); got != want {
			t.Fatalf("Bedrock upstream = %q, want %q", got, want)
		}
		return
	}
	t.Fatal("Bedrock adapter was not registered without a raw base URL")
}

func TestCreds_NamedCompatEnvResolution(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-openrouter")
	t.Setenv("OPENAI_COMPAT_API_KEY", "sk-legacy")
	t.Setenv("OPENAI_API_KEY", "sk-global")
	c := Creds{cfg: config.Config{Compat: map[string]config.CompatConfig{
		"openrouter": {BaseURL: "https://openrouter.ai/api", APIKeyEnv: "OPENROUTER_API_KEY"},
		"ollama":     {BaseURL: "http://localhost:11434", APIKeyEnv: ""},
	}}}

	openrouterReq := httptest.NewRequest(http.MethodPost, "/compat/openrouter/v1/chat/completions", nil)
	if got := c.Resolve("openai_compatible", openrouterReq); got.Key != "sk-openrouter" || got.AuthFallbackEnv != "OPENROUTER_API_KEY" {
		t.Errorf("openrouter credential = %+v, want per-name env key and fallback policy", got)
	}

	ollamaReq := httptest.NewRequest(http.MethodPost, "/compat/ollama/v1/chat/completions", nil)
	if got := c.Resolve("openai_compatible", ollamaReq); got.Key != "" || got.AuthFallbackEnv != "" {
		t.Errorf("ollama credential = %+v, want no auth and no fallback for empty api_key_env", got)
	}

	unknownReq := httptest.NewRequest(http.MethodPost, "/compat/unknown/v1/chat/completions", nil)
	if got := c.Resolve("openai_compatible", unknownReq); got.Key != "sk-legacy" || got.AuthFallbackEnv != "OPENAI_COMPAT_API_KEY" {
		t.Errorf("unknown compat credential = %+v, want legacy OPENAI_COMPAT_API_KEY", got)
	}

	openrouterReq.Header.Set("x-api-key", "sk-inbound")
	if got := c.Resolve("openai_compatible", openrouterReq); got.Key != "sk-inbound" || got.AuthFallbackEnv != "OPENROUTER_API_KEY" {
		t.Errorf("inbound credential = %+v, want passthrough key with named fallback policy", got)
	}
}

// TestStandaloneGeminiGoogleEnvFallbackEndToEnd proves the resolver keeps the
// Gemini fallback policy even when GEMINI_API_KEY is empty, so the gateway can
// use the documented GOOGLE_API_KEY alias without borrowing a cross-provider
// secret. The assertion observes the exact outbound header through the
// listener-free transport seam.
func TestStandaloneGeminiGoogleEnvFallbackEndToEnd(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "google-only-key")
	t.Setenv("OPENAI_API_KEY", "")

	upstream := &captureUpstreamTransport{
		response: `{"candidates":[],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`,
	}
	spend, err := store.Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer spend.Close()

	srv := New(config.Config{
		Mode:      "record",
		Providers: map[string]config.ProviderConfig{"gemini": {BaseURL: "https://upstream.test"}},
	}, spend, Options{HTTPClient: &http.Client{Transport: upstream}})

	req := httptest.NewRequest(http.MethodPost, "/gemini/v1beta/models/gemini-pro:generateContent", strings.NewReader(`{"model":"gemini-pro","contents":[]}`))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := upstream.headers.Get("x-goog-api-key"); got != "google-only-key" {
		t.Fatalf("upstream x-goog-api-key = %q, want GOOGLE_API_KEY value", got)
	}
	if got := upstream.headers.Get("authorization"); got != "" {
		t.Fatalf("upstream authorization = %q, want empty for Gemini API-key mode", got)
	}
}

// TestStandaloneAzureAuthBoundariesEndToEnd keeps the synthetic placeholder
// path distinct from real Entra bearer credentials: the exact sentinel may be
// replaced by AZURE_OPENAI_API_KEY in api-key, while a real bearer remains a
// fail-closed 400 until an explicit Azure bearer contract is wired.
func TestStandaloneAzureAuthBoundariesEndToEnd(t *testing.T) {
	const azurePath = "/azure/openai/deployments/gpt-prod/chat/completions?api-version=2024-10-21"
	const response = `{"id":"azure","model":"gpt-5.5","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	cases := []struct {
		name       string
		inbound    string
		azureKey   string
		openaiKey  string
		wantStatus int
		wantAPIKey string
		wantAuth   string
	}{
		{
			name:       "placeholder bearer becomes Azure api-key",
			inbound:    "Bearer no-key-required",
			azureKey:   "azure-key",
			openaiKey:  "openai-unrelated",
			wantStatus: http.StatusOK,
			wantAPIKey: "azure-key",
		},
		{
			name:       "real bearer fails closed",
			inbound:    "Bearer entra-access-token",
			azureKey:   "azure-key",
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AZURE_OPENAI_API_KEY", tc.azureKey)
			t.Setenv("OPENAI_API_KEY", tc.openaiKey)
			upstream := &captureUpstreamTransport{response: response}
			spend, err := store.Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			defer spend.Close()

			srv := New(config.Config{
				Mode:      "record",
				Providers: map[string]config.ProviderConfig{"azure_openai": {BaseURL: "https://upstream.test"}},
			}, spend, Options{HTTPClient: &http.Client{Transport: upstream}})
			req := httptest.NewRequest(http.MethodPost, azurePath, strings.NewReader(`{"model":"gpt-5.5","messages":[]}`))
			if tc.inbound != "" {
				req.Header.Set("authorization", tc.inbound)
			}
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus == http.StatusOK {
				if got := upstream.headers.Get("api-key"); got != tc.wantAPIKey {
					t.Fatalf("upstream api-key = %q, want %q", got, tc.wantAPIKey)
				}
				if got := upstream.headers.Get("authorization"); got != tc.wantAuth {
					t.Fatalf("upstream authorization = %q, want %q", got, tc.wantAuth)
				}
				if got := upstream.headers.Get("api-key"); got == tc.openaiKey {
					t.Fatal("upstream api-key used unrelated OPENAI_API_KEY")
				}
			} else if upstream.headers != nil {
				t.Fatalf("upstream was called for rejected Azure bearer: %#v", upstream.headers)
			}
		})
	}
}

// TestCreds_BearerSchemeRecorded proves the resolver records the bearer scheme
// only when the key came from an inbound Authorization header, so the Anthropic
// adapter can preserve OAuth tokens as Bearer instead of remapping to x-api-key.
func TestCreds_BearerSchemeRecorded(t *testing.T) {
	c := Creds{cfg: config.Config{}}

	rBearer := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	rBearer.Header.Set("authorization", "Bearer oauth-subscription-token")
	cred := c.Resolve("anthropic", rBearer)
	if cred.Key != "oauth-subscription-token" || cred.Scheme != "bearer" {
		t.Errorf("bearer credential = %+v, want Key=oauth-subscription-token Scheme=bearer", cred)
	}

	// Inbound x-api-key wins over Authorization and carries no bearer scheme.
	rBoth := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	rBoth.Header.Set("x-api-key", "sk-from-agent")
	rBoth.Header.Set("authorization", "Bearer something-else")
	cred = c.Resolve("anthropic", rBoth)
	if cred.Key != "sk-from-agent" || cred.Scheme != "" {
		t.Errorf("x-api-key credential = %+v, want Key=sk-from-agent Scheme=\"\"", cred)
	}

	// BYOK env key is an API key only when no inbound credential is present.
	t.Setenv("ANTHROPIC_API_KEY", "sk-byok-env")
	cred = c.Resolve("anthropic", rBearer)
	if cred.Key != "oauth-subscription-token" || cred.Scheme != "bearer" {
		t.Errorf("bearer credential with env present = %+v, want inbound bearer credential", cred)
	}
	rNoAuth := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	cred = c.Resolve("anthropic", rNoAuth)
	if cred.Key != "sk-byok-env" || cred.Scheme != "" {
		t.Errorf("BYOK fallback credential = %+v, want Key=sk-byok-env Scheme=\"\"", cred)
	}
}

func TestCreds_BedrockInboundThenEnvironment(t *testing.T) {
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "bedrock-env-token")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAENV")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "env-secret")
	c := Creds{cfg: config.Config{}}

	inboundAPIKey := httptest.NewRequest(http.MethodPost, "/bedrock/model/anthropic.claude/converse", nil)
	inboundAPIKey.Header.Set("x-api-key", "bedrock-request-api-key")
	inboundAPIKey.Header.Set("user-agent", "claude-code/2.1.218")
	got := c.Resolve("bedrock", inboundAPIKey)
	if got.Key != "bedrock-request-api-key" || got.Scheme != "" || got.AuthKind != "bedrock_api_key" {
		t.Fatalf("inbound Bedrock x-api-key credential = %+v, want provider-aware API key", got)
	}
	if mode := gateway.ClassifyResolvedAuthMode(inboundAPIKey.Header, got); mode != gateway.AuthModePAYG {
		t.Fatalf("inbound Bedrock x-api-key auth mode = %s, want PAYG before Claude user-agent classification", mode)
	}

	inbound := httptest.NewRequest(http.MethodPost, "/bedrock/model/anthropic.claude/converse", nil)
	inbound.Header.Set("authorization", "Bearer bedrock-request-token")
	got = c.Resolve("bedrock", inbound)
	if got.Key != "bedrock-request-token" || got.Scheme != "bearer" || got.AuthKind != "bedrock_api_key" {
		t.Fatalf("inbound Bedrock credential = %+v, want provider-aware bearer", got)
	}

	noAuth := httptest.NewRequest(http.MethodPost, "/bedrock/model/anthropic.claude/converse", nil)
	got = c.Resolve("bedrock", noAuth)
	if got.Key != "bedrock-env-token" || got.Scheme != "bearer" || got.AuthKind != "bedrock_api_key" {
		t.Fatalf("environment Bedrock credential = %+v, want bearer before IAM", got)
	}

	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")
	t.Setenv("AWS_SESSION_TOKEN", "session-token")
	got = c.Resolve("bedrock", noAuth)
	if got.Key != "AKIAENV:env-secret:session-token" || got.AuthKind != "aws_access_keys" {
		t.Fatalf("IAM Bedrock credential = %+v, want complete access-key tuple", got)
	}

	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	got = c.Resolve("bedrock", noAuth)
	if got.Key != "" || got.AuthKind != "" {
		t.Fatalf("partial IAM credential = %+v, want fail-closed empty credential", got)
	}
}
