package gateway

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
)

func TestOAuthTokenRoutePreservesGrantAndProviderAnswer(t *testing.T) {
	for _, path := range []string{"/v1/oauth/token", "/anthropic/v1/oauth/token", "/w/claude/v1/oauth/token"} {
		for _, grant := range []string{
			`{"grant_type":"refresh_token","refresh_token":"fixture-refresh","client_id":"fixture-client"}`,
			`{"grant_type":"urn:ietf:params:oauth:grant-type:jwt-bearer","assertion":"fixture-jwt","scope":"org:fixture"}`,
		} {
			for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusTooManyRequests} {
				t.Run(path+"/"+http.StatusText(status)+"/"+grant[:20], func(t *testing.T) {
					const answer = `{"provider":"answer must remain unchanged"}`
					sink := &captureSink{}
					calls := 0
					srv := New(Config{
						Adapters: []providers.Adapter{anthropic.New("https://api.anthropic.com")},
						Auth:     stubAuth{rc: RequestContext{RuntimeMode: "compress", Label: "trial:test"}},
						Creds:    stubCreds{key: "operator-key-must-not-leak"}, Sink: sink,
						HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
							calls++
							body, err := io.ReadAll(r.Body)
							if err != nil || string(body) != grant || r.Method != http.MethodPost || r.URL.String() != "https://api.anthropic.com/v1/oauth/token" {
								t.Fatalf("grant changed: method=%s url=%s body=%q error=%v", r.Method, r.URL, body, err)
							}
							for _, name := range []string{"Authorization", "X-Api-Key", "X-Cave-Api-Key", "X-Cave-Agent"} {
								if got := r.Header.Get(name); got != "" {
									t.Errorf("token grant forwarded %s=%q", name, got)
								}
							}
							if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Anthropic-Beta") != "oauth-2025-04-20" {
								t.Errorf("grant headers changed: %v", r.Header)
							}
							return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}, "Request-Id": {"fixture-id"}}, Body: io.NopCloser(strings.NewReader(answer))}, nil
						})},
					})
					req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(grant))
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Anthropic-Beta", "oauth-2025-04-20")
					req.Header.Set("Authorization", "Bearer client-key-must-not-leak")
					req.Header.Set("X-Api-Key", "client-api-key-must-not-leak")
					rec := httptest.NewRecorder()
					srv.Handler().ServeHTTP(rec, req)
					if rec.Code != status || rec.Body.String() != answer || rec.Header().Get("Request-Id") != "fixture-id" || calls != 1 {
						t.Fatalf("token response changed: status=%d body=%s headers=%v calls=%d", rec.Code, rec.Body, rec.Header(), calls)
					}
					if len(sink.rows) != 0 {
						t.Fatalf("token exchange created %d inference rows", len(sink.rows))
					}
				})
			}
		}
	}
}

func TestOAuthTokenRouteRemainsExactAndAuthenticated(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		auth         Authenticator
		want         int
	}{
		{http.MethodGet, "/v1/oauth/token", stubAuth{}, http.StatusNotFound},
		{http.MethodPost, "/v1/oauth/token/revoke", stubAuth{}, http.StatusNotFound},
		{http.MethodPost, "/v1/oauth/tokens", stubAuth{}, http.StatusNotFound},
		{http.MethodPost, "/v1/oauth%2Ftoken", stubAuth{}, http.StatusNotFound},
		{http.MethodPost, "/openai/v1/oauth/token", stubAuth{}, http.StatusNotFound},
		{http.MethodPost, "/v1/oauth/token", rejectingAuth{}, http.StatusUnauthorized},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			srv := New(Config{
				Adapters: []providers.Adapter{anthropic.New("https://api.anthropic.com")}, Auth: tc.auth, Creds: stubCreds{},
				HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					t.Fatal("invalid or unauthenticated token route reached provider")
					return nil, nil
				})},
			})
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`)))
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

func TestOAuthTokenRouteRejectsOversizedOrIncompleteGrant(t *testing.T) {
	for _, tc := range []struct {
		name string
		body io.Reader
		want int
	}{
		{"oversized", strings.NewReader(strings.Repeat("x", oauthTokenMaxBytes+1)), http.StatusRequestEntityTooLarge},
		{"incomplete", errReadCloser{}, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := New(Config{
				Adapters: []providers.Adapter{anthropic.New("https://api.anthropic.com")}, Auth: stubAuth{}, Creds: stubCreds{},
				HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					t.Fatal("incomplete or oversized grant reached provider")
					return nil, nil
				})},
			})
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/oauth/token", tc.body))
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d", rec.Code, tc.want)
			}
		})
	}
}

func TestOAuthTokenRouteAbortsIncompleteProviderResponse(t *testing.T) {
	srv := New(Config{
		Adapters: []providers.Adapter{anthropic.New("https://api.anthropic.com")}, Auth: stubAuth{}, Creds: stubCreds{},
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: errReadCloser{}}, nil
		})},
	})
	defer func() {
		if err, ok := recover().(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
			t.Fatalf("incomplete token response did not abort HTTP framing: %v", err)
		}
		if got := srv.inflight.Load(); got != 0 {
			t.Errorf("interrupted response leaked %d inflight requests", got)
		}
	}()
	srv.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/oauth/token", strings.NewReader(`{}`)))
}
