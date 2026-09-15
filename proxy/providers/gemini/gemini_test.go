package gemini

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func TestQueryCredentialsDoNotOverrideResolvedPrincipal(t *testing.T) {
	a := New("https://upstream.test")
	for _, credential := range []providers.Credential{
		{Mode: "managed", Key: "resolved-key"},
		{Mode: "ephemeral_header", Key: "resolved-key"},
		{Mode: "managed", Scheme: "bearer", Key: "resolved-oauth"},
		{Mode: "ephemeral_header", Scheme: "bearer", Key: "resolved-oauth"},
		{Mode: "ephemeral_header", Scheme: "bearer", Key: "caller-oauth"},
	} {
		t.Run(credential.Mode+"/"+credential.Scheme+"/"+credential.Key, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/gemini/v1beta/models/gemini-2.5-pro:generateContent?key=caller-query-key", nil)
			req.Header.Set("Authorization", "Bearer caller-oauth")
			req.Header.Set("x-goog-user-project", "caller-project")
			u, err := a.ResolveUpstreamURL(req.Context(), req, providers.RouteContext{})
			if err != nil {
				t.Fatal(err)
			}
			got, err := a.SanitizeAndMapHeaders(req.Context(), req, credential, u)
			if err != nil {
				t.Fatal(err)
			}
			if credential.Scheme == "bearer" {
				// The caller's own OAuth credential keeps BOTH of its inputs: the
				// bearer it authenticated with and the URL key it also supplied.
				// A separately resolved bearer must not gain that key.
				wantKey := ""
				if credential.Key == "caller-oauth" {
					wantKey = "caller-query-key"
				}
				if got.Get("Authorization") != "Bearer "+credential.Key || got.Get("x-goog-api-key") != wantKey {
					t.Fatalf("query key changed resolved OAuth principal: %v", got)
				}
			} else if got.Get("x-goog-api-key") != credential.Key || got.Get("Authorization") != "" {
				t.Fatalf("query key changed resolved API-key principal: %v", got)
			}
			if u.RawQuery != "" {
				t.Fatal("query credential leaked into upstream URL")
			}
		})
	}
}

func TestGeminiRouteMetadata(t *testing.T) {
	a := New("http://upstream").(Adapter)
	tests := []struct {
		name   string
		path   string
		model  string
		stream bool
	}{
		{name: "prefixed generate", path: "/gemini/v1beta/models/gemini-2.5-pro:generateContent", model: "gemini-2.5-pro"},
		{name: "bare generate", path: "/v1beta/models/gemini-2.5-pro:generateContent", model: "gemini-2.5-pro"},
		{name: "prefixed stream", path: "/gemini/v1beta/models/gemini-2.5-flash:streamGenerateContent", model: "gemini-2.5-flash", stream: true},
		{name: "bare stream", path: "/v1beta/models/gemini-2.5-flash:streamGenerateContent", model: "gemini-2.5-flash", stream: true},
		{name: "prefixed count tokens", path: "/gemini/v1beta/models/gemini-1.5-pro:countTokens", model: "gemini-1.5-pro"},
		{name: "bare count tokens", path: "/v1beta/models/gemini-1.5-pro:countTokens", model: "gemini-1.5-pro"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !a.MatchRoute(http.MethodPost, tt.path) {
				t.Fatalf("route %q did not match", tt.path)
			}
			if a.MatchRoute(http.MethodGet, tt.path) {
				t.Fatalf("GET route %q matched", tt.path)
			}
			h := http.Header{}
			h.Set("x-cave-route-path", tt.path)
			meta, err := a.InspectRequest(context.Background(), strings.NewReader(`{"contents":[]}`), h)
			if err != nil {
				t.Fatalf("inspect: %v", err)
			}
			if meta.Provider != "gemini" {
				t.Errorf("provider = %q, want gemini", meta.Provider)
			}
			if meta.Model != tt.model {
				t.Errorf("model = %q, want %q", meta.Model, tt.model)
			}
			if meta.Stream != tt.stream {
				t.Errorf("stream = %v, want %v", meta.Stream, tt.stream)
			}
		})
	}

	if a.MatchRoute(http.MethodPost, "/v1beta/models/gemini-2.5-pro:embedContent") {
		t.Error("unknown bare Gemini method matched")
	}
	if a.MatchRoute(http.MethodPost, "/v1beta/models/gemini-2.5-pro:generateContent/extra") {
		t.Error("non-exact bare Gemini route matched")
	}
}

func TestPrefixedGeminiRejectsUnsupportedMethods(t *testing.T) {
	a := New("http://upstream").(Adapter)
	for _, path := range []string{
		"/gemini/v1beta/models/text-embedding-004:embedContent",
		"/gemini/v1beta/models/gemini-2.5-flash:unsupportedMethod",
		"/gemini/v1beta/models/gemini-2.5-flash:generateContent/extra",
	} {
		if a.MatchRoute(http.MethodPost, path) {
			t.Fatalf("unsupported prefixed route %q matched", path)
		}
	}
}

func TestBareGeminiEmbedContentDoesNotMatch(t *testing.T) {
	a := New("http://upstream").(Adapter)
	path := "/v1beta/models/text-embedding-004:embedContent"

	if a.MatchRoute(http.MethodPost, path) {
		t.Fatalf("bare embedContent route %q matched", path)
	}
}

func TestBareGeminiVerifiedRoutesMatchMetadata(t *testing.T) {
	a := New("http://upstream").(Adapter)
	tests := []struct {
		name   string
		path   string
		model  string
		stream bool
	}{
		{name: "generateContent", path: "/v1beta/models/gemini-2.5-pro:generateContent", model: "gemini-2.5-pro"},
		{name: "streamGenerateContent", path: "/v1beta/models/gemini-2.5-flash:streamGenerateContent", model: "gemini-2.5-flash", stream: true},
		{name: "countTokens", path: "/v1beta/models/gemini-1.5-pro:countTokens", model: "gemini-1.5-pro"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !a.MatchRoute(http.MethodPost, tt.path) {
				t.Fatalf("bare route %q did not match", tt.path)
			}
			if a.MatchRoute(http.MethodGet, tt.path) {
				t.Fatalf("GET bare route %q matched", tt.path)
			}

			h := http.Header{}
			h.Set("x-cave-route-path", tt.path)
			meta, err := a.InspectRequest(context.Background(), strings.NewReader(`{"contents":[]}`), h)
			if err != nil {
				t.Fatalf("inspect: %v", err)
			}
			if meta.Provider != "gemini" {
				t.Errorf("provider = %q, want gemini", meta.Provider)
			}
			if meta.Model != tt.model {
				t.Errorf("model = %q, want %q", meta.Model, tt.model)
			}
			if meta.Stream != tt.stream {
				t.Errorf("stream = %v, want %v", meta.Stream, tt.stream)
			}
		})
	}
}
