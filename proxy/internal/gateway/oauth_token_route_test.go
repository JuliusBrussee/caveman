package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

const (
	refreshGrant  = `{"grant_type":"refresh_token","refresh_token":"rt-test","client_id":"client-test"}`
	refreshAnswer = `{"access_token":"at-new","expires_in":28800,"refresh_token":"rt-rotated","token_type":"Bearer"}`
)

// tokenUpstream stubs the provider token endpoint and records what reached it.
func tokenUpstream(t *testing.T, status int, answer string) (*httptest.Server, *[]*http.Request, *[]string) {
	t.Helper()
	var reqs []*http.Request
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		reqs = append(reqs, r.Clone(r.Context()))
		bodies = append(bodies, string(b))
		w.Header().Set("content-type", "application/json")
		w.Header().Set("request-id", "req_upstream")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs, &bodies
}

func newTokenTestServer(upstream string, auth Authenticator, sink TelemetrySink) *Server {
	return New(Config{
		Adapters:   []providers.Adapter{anthropic.New(upstream), openai.New(upstream)},
		Auth:       auth,
		Creds:      stubCreds{key: "sk-operator-byok"},
		Sink:       sink,
		HTTPClient: &http.Client{},
	})
}

// TestOAuthTokenRouteRelaysRefresh is the Claude Code "User OAuth refresh
// failed (HTTP 404) ... Proxy path is not recognized." report. Signed in with a
// Claude Platform profile, Claude Code refreshes at
// {ANTHROPIC_BASE_URL}/v1/oauth/token, which caveman wrap points at
// /w/claude on this proxy. The grant must reach the provider unchanged, the
// answer must come back byte-for-byte, the operator's key must not ride along,
// and no spend row may be written.
func TestOAuthTokenRouteRelaysRefresh(t *testing.T) {
	for _, path := range []string{"/w/claude/v1/oauth/token", "/v1/oauth/token", "/anthropic/v1/oauth/token"} {
		t.Run(path, func(t *testing.T) {
			upstream, reqs, bodies := tokenUpstream(t, http.StatusOK, refreshAnswer)
			sink := &captureSink{}
			srv := newTokenTestServer(upstream.URL, stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}}, sink)

			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(refreshGrant))
			req.Header.Set("content-type", "application/json")
			req.Header.Set("anthropic-beta", "oauth-2025-04-20")
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK || rec.Body.String() != refreshAnswer {
				t.Fatalf("got %d %s, want 200 and the upstream answer byte-for-byte", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("request-id"); got != "req_upstream" {
				t.Errorf("request-id = %q, want the provider's", got)
			}
			if len(*reqs) != 1 {
				t.Fatalf("upstream reached %d times, want 1", len(*reqs))
			}
			got := (*reqs)[0]
			if got.Method != http.MethodPost || got.URL.Path != "/v1/oauth/token" {
				t.Errorf("upstream %s %s, want POST /v1/oauth/token", got.Method, got.URL.Path)
			}
			if (*bodies)[0] != refreshGrant {
				t.Errorf("upstream body = %s, want the grant unchanged", (*bodies)[0])
			}
			if got.Header.Get("anthropic-beta") != "oauth-2025-04-20" || got.Header.Get("content-type") != "application/json" {
				t.Errorf("upstream headers lost anthropic-beta/content-type: %v", got.Header)
			}
			if a, k := got.Header.Get("authorization"), got.Header.Get("x-api-key"); a != "" || k != "" {
				t.Errorf("upstream credential authorization=%q x-api-key=%q, want none on a token grant", a, k)
			}
			if len(sink.rows) != 0 {
				t.Errorf("token refresh recorded %d spend rows, want 0", len(sink.rows))
			}
		})
	}
}

// TestOAuthTokenRouteRelaysRejection keeps the provider's refusal intact:
// Claude Code reads "invalid_grant" from the body to clear a dead refresh
// token, so a rewritten error would leave it retrying forever.
func TestOAuthTokenRouteRelaysRejection(t *testing.T) {
	const rejection = `{"error":"invalid_grant","error_description":"refresh token revoked"}`
	upstream, _, _ := tokenUpstream(t, http.StatusBadRequest, rejection)
	srv := newTokenTestServer(upstream.URL, stubAuth{}, &captureSink{})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/w/claude/v1/oauth/token", strings.NewReader(refreshGrant)))
	if rec.Code != http.StatusBadRequest || rec.Body.String() != rejection {
		t.Fatalf("got %d %s, want 400 %s", rec.Code, rec.Body.String(), rejection)
	}
}

// TestOAuthTokenRouteStaysFailClosed proves the token mount is one exact POST
// route, not a new pass-through, and that it sits behind the authenticator.
func TestOAuthTokenRouteStaysFailClosed(t *testing.T) {
	upstream, reqs, _ := tokenUpstream(t, http.StatusOK, refreshAnswer)
	for _, tc := range []struct {
		name   string
		method string
		path   string
		auth   Authenticator
		want   int
	}{
		{"get", http.MethodGet, "/v1/oauth/token", stubAuth{}, http.StatusNotFound},
		{"subtree", http.MethodPost, "/v1/oauth/token/revoke", stubAuth{}, http.StatusNotFound},
		{"prefix collision", http.MethodPost, "/v1/oauth/tokens", stubAuth{}, http.StatusNotFound},
		{"encoded separator", http.MethodPost, "/v1/oauth%2Ftoken", stubAuth{}, http.StatusNotFound},
		{"other oauth path", http.MethodPost, "/v1/oauth/authorize", stubAuth{}, http.StatusNotFound},
		{"auth before route", http.MethodPost, "/w/claude/v1/oauth/token", rejectingAuth{}, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			*reqs = nil
			srv := newTokenTestServer(upstream.URL, tc.auth, &captureSink{})
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader(refreshGrant)))
			if rec.Code != tc.want || len(*reqs) != 0 {
				t.Fatalf("status = %d, upstream reached %d times; want %d and none", rec.Code, len(*reqs), tc.want)
			}
		})
	}
}

func TestOAuthTokenRouteRejectsOversizedBody(t *testing.T) {
	upstream, reqs, _ := tokenUpstream(t, http.StatusOK, refreshAnswer)
	srv := newTokenTestServer(upstream.URL, stubAuth{}, &captureSink{})
	rec := httptest.NewRecorder()
	body := strings.NewReader(strings.Repeat("x", oauthTokenMaxBytes+1))
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/oauth/token", body))
	if rec.Code != http.StatusRequestEntityTooLarge || len(*reqs) != 0 {
		t.Fatalf("status = %d, upstream reached %d times; want 413 and none", rec.Code, len(*reqs))
	}
}
