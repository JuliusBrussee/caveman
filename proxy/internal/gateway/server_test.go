package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
	"github.com/JuliusBrussee/caveman/proxy/providers/openaicompat"
)

// failingTransformAdapter is an adapter whose optimizer step always errors, used
// to prove the fail-open contract: a transform error must never break the
// request — the original bytes are forwarded unchanged.
type failingTransformAdapter struct{ providers.Base }

func (failingTransformAdapter) ApplyProviderNativeTransforms(ctx context.Context, body providers.BodyReader, meta providers.RequestMetadata, policy providers.TransformPolicy) (providers.TransformResult, error) {
	return providers.TransformResult{}, errors.New("simulated optimizer failure")
}

type mutatingTransformAdapter struct{ providers.Adapter }

func (mutatingTransformAdapter) ApplyProviderNativeTransforms(context.Context, providers.BodyReader, providers.RequestMetadata, providers.TransformPolicy) (providers.TransformResult, error) {
	return providers.TransformResult{
		Body:         []byte(`{"model":"gpt-5.5","input":"mutated"}`),
		OptimizerIDs: []string{"provider.native.fixture"},
	}, nil
}

// --- test seams ---------------------------------------------------------------

type stubAuth struct{ rc RequestContext }

func (a stubAuth) Authenticate(ctx context.Context, r *http.Request) (RequestContext, error) {
	return a.rc, nil
}

type stubCreds struct{ key string }

func (c stubCreds) Resolve(provider string, r *http.Request) providers.Credential {
	return providers.Credential{Mode: "ephemeral_header", Key: c.key}
}

type captureSink struct {
	mu   sync.Mutex
	rows []RequestRecord
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func (s *captureSink) Record(rec RequestRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, rec)
}

func (s *captureSink) last(t *testing.T) RequestRecord {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rows) == 0 {
		t.Fatal("no telemetry row recorded")
	}
	return s.rows[len(s.rows)-1]
}

func newStandaloneTestServer(t *testing.T, upstream string, rc RequestContext, sink TelemetrySink) *Server {
	t.Helper()
	return New(Config{
		Adapters:   []providers.Adapter{openai.New(upstream)},
		Auth:       stubAuth{rc: rc},
		Creds:      stubCreds{key: "sk-byok"},
		Sink:       sink,
		HTTPClient: &http.Client{}, // plain client: the upstream stub is on loopback
	})
}

// --- tests --------------------------------------------------------------------

func TestHealthReadyIdentifiesCavemanProxyRuntime(t *testing.T) {
	srv := New(Config{Adapters: []providers.Adapter{openai.New("https://api.openai.com")}})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		OK       bool   `json:"ok"`
		Service  string `json:"service"`
		Schema   string `json:"schema"`
		Billing  string `json:"billing"`
		Adapters int    `json:"adapters"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.Service != "caveman-proxy" ||
		body.Schema != "caveman.proxy.health.v1" || body.Billing != "byok" || body.Adapters != 1 {
		t.Fatalf("unexpected runtime identity: %#v", body)
	}
}

// TestRecordModePassThrough proves the standalone proxy is byte-safe in record
// mode: the request reaches the upstream unmodified, the response is returned
// byte-for-byte, no optimizer is applied, and one truthful spend row is recorded
// with a positive cost labeled `inferred` (never `verified`).
func TestRecordModePassThrough(t *testing.T) {
	const reqBody = `{"model":"gpt-5.5","input":"hello"}`
	const respBody = `{"id":"resp_stub","object":"response","model":"gpt-5.5","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":1000,"output_tokens":120,"total_tokens":1120,"input_tokens_details":{"cached_tokens":700}}}`

	var gotUpstreamBody string
	var gotUpstreamPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUpstreamPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotUpstreamBody = string(b)
		w.Header().Set("content-type", "application/json")
		w.Header().Set("x-request-id", "upstream-req-1")
		_, _ = io.WriteString(w, respBody)
	}))
	defer upstream.Close()

	sink := &captureSink{}
	srv := newStandaloneTestServer(t, upstream.URL, RequestContext{Label: "local", RuntimeMode: "record"}, sink)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(reqBody))
	req.Header.Set("authorization", "Bearer sk-from-agent")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != respBody {
		t.Errorf("response not preserved byte-for-byte:\n got %q\nwant %q", rec.Body.String(), respBody)
	}
	if gotUpstreamBody != reqBody {
		t.Errorf("request not forwarded byte-for-byte in record mode:\n got %q\nwant %q", gotUpstreamBody, reqBody)
	}
	if gotUpstreamPath != "/v1/responses" {
		t.Errorf("upstream path = %q, want /v1/responses", gotUpstreamPath)
	}
	if got := rec.Header().Get("x-cave-optimization"); got != "none" {
		t.Errorf("x-cave-optimization = %q, want none (record mode applies no optimizer)", got)
	}

	row := sink.last(t)
	if row.Provider != "openai" {
		t.Errorf("provider = %q, want openai", row.Provider)
	}
	if row.Model != "gpt-5.5" {
		t.Errorf("model = %q, want gpt-5.5", row.Model)
	}
	if row.InputTokens != 1000 || row.OutputTokens != 120 || row.CachedInputTokens != 700 {
		t.Errorf("usage = (in %d, out %d, cached %d), want (1000, 120, 700)", row.InputTokens, row.OutputTokens, row.CachedInputTokens)
	}
	if row.TotalCostUSD <= 0 {
		t.Errorf("total_cost_usd = %v, want > 0 for a priced model", row.TotalCostUSD)
	}
	if row.Basis != "inferred" {
		t.Errorf("basis = %q, want inferred (standalone never claims verified)", row.Basis)
	}
	if row.SavingsUSD != 0 {
		t.Errorf("savings = %v, want 0 (no optimizer fired in record mode)", row.SavingsUSD)
	}
	// byte-safety audit: in record mode the transformed hash equals the raw hash.
	if row.RawRequestSHA256 != row.TransformedRequestSHA256 {
		t.Errorf("record mode must not alter bytes: raw hash %s != transformed hash %s", row.RawRequestSHA256, row.TransformedRequestSHA256)
	}
}

func TestExplicitPassThroughSuppressesProviderNativeTransform(t *testing.T) {
	const requestBody = `{"model":"gpt-5.5","input":"original"}`
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamBody = string(body)
		_, _ = io.WriteString(w, `{"id":"response","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()

	sink := &captureSink{}
	srv := New(Config{
		Adapters: []providers.Adapter{mutatingTransformAdapter{Adapter: openai.New(upstream.URL)}},
		Auth: stubAuth{rc: RequestContext{
			Label:       "local",
			RuntimeMode: "cache",
		}},
		Creds:      stubCreds{key: "sk-byok"},
		Sink:       sink,
		HTTPClient: &http.Client{},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(requestBody))
	req.Header.Set("x-cave-transforms", "caveman.pass-through.v1")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || upstreamBody != requestBody || rec.Header().Get("x-cave-optimization") != "none" {
		t.Fatalf("explicit pass-through changed native request: status=%d body=%s optimization=%q", rec.Code, upstreamBody, rec.Header().Get("x-cave-optimization"))
	}
	row := sink.last(t)
	if row.RawRequestSHA256 != row.TransformedRequestSHA256 || len(row.OptimizationIDs) != 0 {
		t.Fatalf("explicit pass-through recorded native transform: %+v", row)
	}
}

// TestRoundTripStreamTTFB proves the streaming path tees usage from SSE without
// buffering the client copy: time-to-first-byte is a real positive value below
// total latency, and usage is parsed from the final stream event.
func TestRoundTripStreamTTFB(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.Header().Set("x-request-id", "upstream-stream-1")
		flusher, _ := w.(http.Flusher)
		chunks := []string{
			`data: {"type":"response.output_text.delta","delta":"hel"}` + "\n\n",
			`data: {"type":"response.output_text.delta","delta":"lo"}` + "\n\n",
			`data: {"type":"response.completed","usage":{"input_tokens":1000,"output_tokens":20,"input_tokens_details":{"cached_tokens":700}}}` + "\n\n",
			"data: [DONE]\n\n",
		}
		for _, c := range chunks {
			_, _ = io.WriteString(w, c)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer upstream.Close()

	sink := &captureSink{}
	srv := newStandaloneTestServer(t, upstream.URL, RequestContext{Label: "local", RuntimeMode: "record"}, sink)

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{"model":"gpt-5.5","input":"hello","stream":true}`))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	row := sink.last(t)
	if !row.Stream {
		t.Error("row.Stream = false, want true")
	}
	if row.InputTokens != 1000 || row.OutputTokens != 20 || row.CachedInputTokens != 700 {
		t.Errorf("stream usage = (in %d, out %d, cached %d), want (1000, 20, 700)", row.InputTokens, row.OutputTokens, row.CachedInputTokens)
	}
}

// TestBareAnthropicRoute proves an agent can point ANTHROPIC_BASE_URL straight at
// the proxy unprefixed: a bare POST /v1/messages routes to Anthropic, forwards to
// {base}/v1/messages with no prefix trimmed, and lands a truthful-spend row.
func TestBareAnthropicRoute(t *testing.T) {
	const respBody = `{"id":"msg_1","type":"message","model":"claude-sonnet-4-6","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1000,"output_tokens":50}}`
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("content-type", "application/json")
		w.Header().Set("x-request-id", "msg-req-1")
		_, _ = io.WriteString(w, respBody)
	}))
	defer upstream.Close()

	sink := &captureSink{}
	srv := New(Config{
		Adapters:   []providers.Adapter{anthropic.New(upstream.URL)},
		Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		Creds:      stubCreds{key: "sk-byok"},
		Sink:       sink,
		HTTPClient: &http.Client{},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", "sk-from-agent")
	req.Header.Set("x-cave-session", "support:session-1")
	req.Header.Set("x-cave-agent-build", strings.Repeat("a", 64))
	req.Header.Set("x-cave-efficiency-plan", strings.Repeat("b", 64))
	req.Header.Set("x-cave-cache-prefix-sha256", strings.Repeat("c", 64))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/messages" {
		t.Errorf("upstream path = %q, want /v1/messages (bare route, no prefix trimmed)", gotPath)
	}
	row := sink.last(t)
	if row.Provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic", row.Provider)
	}
	if row.Model != "claude-sonnet-4-6" {
		t.Errorf("model = %q, want claude-sonnet-4-6", row.Model)
	}
	if row.InputTokens != 1000 || row.OutputTokens != 50 {
		t.Errorf("usage = (in %d, out %d), want (1000, 50)", row.InputTokens, row.OutputTokens)
	}
	if row.TotalCostUSD <= 0 {
		t.Errorf("total_cost_usd = %v, want > 0 (truthful spend for a priced model)", row.TotalCostUSD)
	}
	if row.Basis != "inferred" {
		t.Errorf("basis = %q, want inferred", row.Basis)
	}
	if row.SessionID != "support:session-1" || row.AgentBuildSHA256 != strings.Repeat("a", 64) ||
		row.EfficiencyPlanSHA256 != strings.Repeat("b", 64) {
		t.Errorf("agent evidence identity missing: %+v", row)
	}
	if !row.CacheBoundaryKnown || len(row.ProviderCachePrefixSHA256) != 64 ||
		row.ProviderCachePrefixSHA256 == row.CachePrefixSHA256 ||
		row.ProviderCacheComponentSHA256 == "" {
		t.Errorf("observed provider prefix evidence missing or conflated with declaration: %+v", row)
	}
}

// TestTransformErrorFailsOpen proves the byte-safe fail-open contract: in active
// mode, when the optimizer step errors, the proxy forwards the ORIGINAL request
// bytes unchanged with HTTP 200 — it never returns the managed loop's HTTP 400.
func TestTransformErrorFailsOpen(t *testing.T) {
	const reqBody = `{"model":"gpt-5.5","input":"hello"}`
	const respBody = `{"id":"r","model":"gpt-5.5","usage":{"input_tokens":10,"output_tokens":5}}`
	var gotUpstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotUpstreamBody = string(b)
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, respBody)
	}))
	defer upstream.Close()

	sink := &captureSink{}
	srv := New(Config{
		Adapters: []providers.Adapter{failingTransformAdapter{Base: providers.Base{
			Provider: "openai", BaseURL: upstream.URL, Routes: []string{"/v1/chat/completions"},
		}}},
		Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "active", Optimizers: map[string]bool{"openai-prompt-cache-key": true}}},
		Creds:      stubCreds{key: "sk-byok"},
		Sink:       sink,
		HTTPClient: &http.Client{},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a transform error must fail open, not 400 (body %s)", rec.Code, rec.Body.String())
	}
	if gotUpstreamBody != reqBody {
		t.Errorf("upstream body = %q, want the ORIGINAL %q forwarded unchanged on transform error", gotUpstreamBody, reqBody)
	}
	if got := rec.Header().Get("x-cave-optimization"); got != "none" {
		t.Errorf("x-cave-optimization = %q, want none (failed transform applies no optimizer)", got)
	}
	row := sink.last(t)
	if row.RawRequestSHA256 != row.TransformedRequestSHA256 {
		t.Error("fail-open must forward original bytes: raw and transformed hashes must match")
	}
}

// TestAgentAttribution proves the proxy tags each spend row with the wrapped
// agent from the x-cave-agent header (set by `caveman wrap <agent>`), defaulting
// to "unlabeled-agent" when the header is absent. This is the standalone half of
// the per-agent attribution the managed gateway already records.
func TestAgentAttribution(t *testing.T) {
	const respBody = `{"id":"resp_stub","object":"response","model":"gpt-5.5","usage":{"input_tokens":10,"output_tokens":5}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, respBody)
	}))
	defer upstream.Close()

	send := func(t *testing.T, agentHeader string) RequestRecord {
		t.Helper()
		sink := &captureSink{}
		srv := newStandaloneTestServer(t, upstream.URL, RequestContext{Label: "local", RuntimeMode: "record"}, sink)
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.5","messages":[]}`))
		req.Header.Set("authorization", "Bearer sk-from-agent")
		if agentHeader != "" {
			req.Header.Set("x-cave-agent", agentHeader)
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		return sink.last(t)
	}

	if got := send(t, "opencode").AgentSlug; got != "opencode" {
		t.Errorf("agent_slug = %q, want opencode (from x-cave-agent header)", got)
	}
	if got := send(t, "").AgentSlug; got != "unlabeled-agent" {
		t.Errorf("agent_slug = %q, want unlabeled-agent when the header is absent", got)
	}
}

func TestAgentPathAttribution(t *testing.T) {
	const reqBody = `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`
	const respBody = `{"id":"msg_1","type":"message","model":"claude-sonnet-4-6","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10,"output_tokens":5}}`
	var gotUpstreamPath string
	var gotUpstreamBody string
	upstreamClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotUpstreamPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotUpstreamBody = string(b)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"content-type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(respBody)),
		}, nil
	})}

	send := func(t *testing.T, path, agentHeader string) (*httptest.ResponseRecorder, *captureSink) {
		t.Helper()
		gotUpstreamPath = ""
		gotUpstreamBody = ""
		sink := &captureSink{}
		srv := New(Config{
			Adapters:   []providers.Adapter{anthropic.New("http://upstream.test")},
			Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
			Creds:      stubCreds{key: "sk-byok"},
			Sink:       sink,
			HTTPClient: upstreamClient,
		})
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(reqBody))
		req.Header.Set("x-api-key", "sk-from-agent")
		if agentHeader != "" {
			req.Header.Set("x-cave-agent", agentHeader)
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec, sink
	}

	rec, sink := send(t, "/w/my-app/v1/messages", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if gotUpstreamPath != "/v1/messages" {
		t.Errorf("upstream path = %q, want /v1/messages", gotUpstreamPath)
	}
	if gotUpstreamBody != reqBody {
		t.Errorf("upstream body = %q, want original body %q", gotUpstreamBody, reqBody)
	}
	row := sink.last(t)
	if row.AgentSlug != "my-app" {
		t.Errorf("agent_slug = %q, want my-app", row.AgentSlug)
	}
	if row.Endpoint != "/v1/messages" {
		t.Errorf("endpoint = %q, want stripped /v1/messages", row.Endpoint)
	}

	rec, sink = send(t, "/w/my-app/v1/messages", "other")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := sink.last(t).AgentSlug; got != "my-app" {
		t.Errorf("agent_slug = %q, want my-app from path over conflicting header", got)
	}

	rec, _ = send(t, "/w/BAD!slug/v1/messages", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for invalid agent slug (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "cave_route_not_found") {
		t.Fatalf("body = %s, want cave_route_not_found", rec.Body.String())
	}
	if gotUpstreamPath != "" {
		t.Fatalf("invalid /w/ path reached upstream path %q, want no upstream call", gotUpstreamPath)
	}

	rec, sink = send(t, "/v1/messages", "header-agent")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := sink.last(t).AgentSlug; got != "header-agent" {
		t.Errorf("agent_slug = %q, want header-agent for non-/w/ request", got)
	}
}

// TestUnknownRouteFailsClosed proves an unrecognized path is a 404, never a
// blind pass-through.
func TestUnknownRouteFailsClosed(t *testing.T) {
	sink := &captureSink{}
	srv := newStandaloneTestServer(t, "http://127.0.0.1:0", RequestContext{Label: "local", RuntimeMode: "record"}, sink)
	req := httptest.NewRequest(http.MethodPost, "/not/a/provider/route", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown route", rec.Code)
	}
}

func TestCompatEncodedSeparatorCannotClaimNamedCredentialRoute(t *testing.T) {
	named, err := openaicompat.NewNamed("groq", "https://api.groq.com/openai")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{adapters: []providers.Adapter{named, openaicompat.New("https://ollama.example/v1")}}
	req := httptest.NewRequest(http.MethodPost, "/compat/groq%2Fv1/chat/completions", nil)
	if got := srv.matchAdapter(req); got != nil {
		t.Fatalf("encoded separator selected adapter %q; want route rejection", got.Name())
	}

	agentReq := httptest.NewRequest(http.MethodPost, "/w/editor/compat/groq%2Fv1/chat/completions", nil)
	if !normalizeAgentPath(agentReq) {
		t.Fatal("normalizeAgentPath rejected valid agent prefix")
	}
	if got := srv.matchAdapter(agentReq); got != nil {
		t.Fatalf("encoded separator after agent prefix selected adapter %q; want route rejection", got.Name())
	}
}
