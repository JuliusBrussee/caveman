package standalone

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/config"
)

// authToken is a realistic 32-byte operator secret: long enough to pass
// config.validateAuthToken, distinctive enough that any header carrying it
// upstream is unmistakable in the assertions below.
const authToken = "cave_tok_0123456789abcdef012345"

// anthropicStubResponse is the minimum Messages shape the usage parser accepts,
// so these tests fail on headers and status only — never on metering.
const anthropicStubResponse = `{"id":"msg_stub","type":"message","model":"claude-sonnet-4-6","content":[],"usage":{"input_tokens":10,"output_tokens":1}}`

// newTokenGatedServer builds a standalone server whose inbound gate is the shared
// token, pointed at a capturing upstream. A nil sink is deliberate: these tests
// assert on the forwarded request, and the gateway skips recording without one.
func newTokenGatedServer(t *testing.T, token string) (http.Handler, *captureUpstreamTransport) {
	t.Helper()
	handler, upstream, _ := newLoggingTokenGatedServer(t, token)
	return handler, upstream
}

// newLoggingTokenGatedServer is the same server with its warnings captured, for
// the tests that assert on what a rejection discloses.
func newLoggingTokenGatedServer(t *testing.T, token string) (http.Handler, *captureUpstreamTransport, *bytes.Buffer) {
	t.Helper()
	upstream := &captureUpstreamTransport{response: anthropicStubResponse}
	cfg := config.Config{
		Mode:      "record",
		AuthToken: token,
		Providers: map[string]config.ProviderConfig{"anthropic": {BaseURL: "https://upstream.test"}},
	}
	logs := &bytes.Buffer{}
	srv := New(cfg, nil, Options{
		HTTPClient: &http.Client{Transport: upstream},
		Logger:     slog.New(slog.NewTextHandler(logs, nil)),
	})
	return srv.Handler(), upstream, logs
}

// get issues an unauthenticated GET, for the probe routes that stay open.
func get(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func postMessages(t *testing.T, handler http.Handler, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// assertUpstreamNeverSawToken is the whole point of the header deletion: the
// operator's shared secret is not a provider credential and must not reach one.
func assertUpstreamNeverSawToken(t *testing.T, upstream *captureUpstreamTransport) {
	t.Helper()
	for name, values := range upstream.headers {
		for _, value := range values {
			if strings.Contains(value, authToken) {
				t.Fatalf("upstream header %s carried the inbound token: %q", name, value)
			}
		}
	}
}

func TestAuthToken_RejectsRequestWithoutToken(t *testing.T) {
	handler, _ := newTokenGatedServer(t, authToken)
	rec := postMessages(t, handler, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "cave_unauthorized") {
		t.Fatalf("body = %s, want cave_unauthorized", rec.Body.String())
	}
}

func TestAuthToken_AcceptsCaveAPIKeyAndStripsIt(t *testing.T) {
	handler, upstream := newTokenGatedServer(t, authToken)
	rec := postMessages(t, handler, map[string]string{"x-cave-api-key": authToken, "x-api-key": "sk-ant-client"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := upstream.headers.Get("x-cave-api-key"); got != "" {
		t.Fatalf("upstream saw x-cave-api-key = %q, want it consumed at the proxy", got)
	}
	assertUpstreamNeverSawToken(t, upstream)
}

func TestAuthToken_AcceptsBearerAndFallsBackToOperatorKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-env")
	handler, upstream := newTokenGatedServer(t, authToken)
	rec := postMessages(t, handler, map[string]string{"Authorization": "Bearer " + authToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	// The bearer held the shared token, so it was consumed; the request reaches
	// the provider on the operator's BYOK key, not on the inbound secret.
	if got := upstream.headers.Get("x-api-key"); got != "sk-ant-env" {
		t.Fatalf("upstream x-api-key = %q, want the BYOK env key", got)
	}
	assertUpstreamNeverSawToken(t, upstream)
}

func TestAuthToken_InboundProviderKeyStillWinsOverBYOK(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-env")
	handler, upstream := newTokenGatedServer(t, authToken)
	rec := postMessages(t, handler, map[string]string{"Authorization": "Bearer " + authToken, "x-api-key": "sk-ant-client"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := upstream.headers.Get("x-api-key"); got != "sk-ant-client" {
		t.Fatalf("upstream x-api-key = %q, want the caller's own key", got)
	}
	assertUpstreamNeverSawToken(t, upstream)
}

func TestAuthToken_RejectsWrongTokenInEitherHeader(t *testing.T) {
	for name, headers := range map[string]map[string]string{
		"x-cave-api-key": {"x-cave-api-key": authToken + "-wrong"},
		"bearer":         {"Authorization": "Bearer " + authToken + "-wrong"},
	} {
		t.Run(name, func(t *testing.T) {
			handler, _ := newTokenGatedServer(t, authToken)
			rec := postMessages(t, handler, headers)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "cave_unauthorized") {
				t.Fatalf("body = %s, want cave_unauthorized", rec.Body.String())
			}
		})
	}
}

// TestAuthToken_HealthStaysUnauthenticated: a load balancer in front of a
// VPC-bound proxy probes readiness without holding the operator's secret.
func TestAuthToken_HealthStaysUnauthenticated(t *testing.T) {
	handler, _ := newTokenGatedServer(t, authToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestAuthToken_EmptyTokenKeepsLoopbackBehavior(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-env")
	handler, upstream := newTokenGatedServer(t, "")
	rec := postMessages(t, handler, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := upstream.headers.Get("x-api-key"); got != "sk-ant-env" {
		t.Fatalf("upstream x-api-key = %q, want the BYOK env key", got)
	}
}

// TestAuthToken_PreservesForeignBearer is the credential-safety case: an
// Authorization header that is NOT the shared token is a real provider OAuth
// credential and must survive the gate untouched.
func TestAuthToken_PreservesForeignBearer(t *testing.T) {
	const oauth = "sk-ant-oat01-operator-oauth-token"
	handler, upstream := newTokenGatedServer(t, authToken)
	rec := postMessages(t, handler, map[string]string{
		"x-cave-api-key": authToken,
		"Authorization":  "Bearer " + oauth,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := upstream.headers.Get("Authorization"); got != "Bearer "+oauth {
		t.Fatalf("upstream Authorization = %q, want the caller's OAuth bearer preserved", got)
	}
	assertUpstreamNeverSawToken(t, upstream)
}

// A client that hedges and sends the token in BOTH headers must still have it
// consumed from Authorization: an early return on the x-cave-api-key match
// left the bearer in place, and every adapter forwards Authorization.
func TestAuthToken_ConsumesTokenFromBothHeaders(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-env")
	handler, upstream := newTokenGatedServer(t, authToken)
	rec := postMessages(t, handler, map[string]string{"x-cave-api-key": authToken, "Authorization": "Bearer " + authToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := upstream.headers.Get("Authorization"); got != "" {
		t.Fatalf("upstream Authorization = %q, want the token-bearing header consumed", got)
	}
	if got := upstream.headers.Get("x-api-key"); got != "sk-ant-env" {
		t.Fatalf("upstream x-api-key = %q, want the operator key once both token headers are consumed", got)
	}
	assertUpstreamNeverSawToken(t, upstream)
}

// A rejected token used to be completely silent: no log line, no metric, so a
// brute-force run against the gate looked exactly like an idle proxy. The line
// must name the path and the remote HOST and nothing else — not the presented
// secret, not the header, not even the source port.
func TestAuthToken_RejectionIsCountedAndLogged(t *testing.T) {
	handler, _, logs := newLoggingTokenGatedServer(t, authToken)
	rec := postMessages(t, handler, map[string]string{"x-cave-api-key": authToken + "-wrong"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
	}
	logged := logs.String()
	if !strings.Contains(logged, "inbound token rejected") {
		t.Fatalf("log = %q, want the rejection warning", logged)
	}
	if !strings.Contains(logged, "path=/v1/messages") {
		t.Fatalf("log = %q, want the rejected path", logged)
	}
	// httptest.NewRequest uses 192.0.2.1:1234 as RemoteAddr.
	if !strings.Contains(logged, "remote=192.0.2.1") || strings.Contains(logged, "192.0.2.1:1234") {
		t.Fatalf("log = %q, want the remote host without its port", logged)
	}
	for _, secret := range []string{authToken, "x-cave-api-key", "Authorization"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("log = %q, must not disclose %q", logged, secret)
		}
	}
	metrics := get(t, handler, "/metrics").Body.String()
	if !strings.Contains(metrics, "cave_proxy_unauthorized_total 1") {
		t.Fatalf("metrics = %q, want cave_proxy_unauthorized_total 1", metrics)
	}
}

// Route matching used to run before the gate, so an unauthenticated caller could
// enumerate the proxy's providers and compat mounts by telling 401 from 404.
func TestAuthToken_UnknownRouteIsNotARouteOracle(t *testing.T) {
	handler, _ := newTokenGatedServer(t, authToken)
	for _, path := range []string{"/v1/messages", "/openai/v1/chat/completions", "/definitely/not/a/route"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s status = %d, want 401 for every path without a token (body %s)", path, rec.Code, rec.Body.String())
		}
	}
	// With the token presented, an unknown path is still the 404 it always was:
	// moving the gate earlier must not turn fail-closed routing into a 401.
	req := httptest.NewRequest(http.MethodPost, "/definitely/not/a/route", strings.NewReader(`{}`))
	req.Header.Set("x-cave-api-key", authToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("authenticated unknown path status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

// The probe routes a load balancer needs stay open; /chatgpt/ is inference and
// does not.
func TestAuthToken_ProbeRoutesStayOpenAndChatGPTIsGated(t *testing.T) {
	handler, _ := newTokenGatedServer(t, authToken)
	for _, path := range []string{"/metrics", "/health/live", "/health/ready"} {
		if rec := get(t, handler, path); rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200 without a token (body %s)", path, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/chatgpt/responses", strings.NewReader(`{}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("/chatgpt/ status = %d, want 401 without a token (body %s)", rec.Code, rec.Body.String())
	}
}
