package openaicompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func TestNew_DefaultRouteStripsMountAndPreservesBasePathAndQuery(t *testing.T) {
	adapter := New("http://127.0.0.1:11434/v1?tenant=local")
	req := httptest.NewRequest(http.MethodPost, "/compat/v1/chat/completions?stream=true", nil)

	upstream, err := adapter.ResolveUpstreamURL(req.Context(), req, providers.RouteContext{})
	if err != nil {
		t.Fatalf("ResolveUpstreamURL: %v", err)
	}
	if got, want := upstream.String(), "http://127.0.0.1:11434/v1/chat/completions?tenant=local&stream=true"; got != want {
		t.Fatalf("upstream = %q, want %q", got, want)
	}
}

func TestNew_DefaultRouteDoesNotConfuseSimilarPrefixes(t *testing.T) {
	adapter := New("https://compat.example.test/api")
	for _, path := range []string{"/compatibility/v1/chat/completions", "/compat/stubish/v1/chat/completions"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		if adapter.MatchRoute(req.Method, req.URL.Path) {
			// /compatibility is outside the mount; /compat/stubish is inside the
			// broad default mount and must be treated as a normal provider path.
			if path == "/compatibility/v1/chat/completions" {
				t.Fatalf("default adapter matched non-mount prefix %q", path)
			}
			upstream, err := adapter.ResolveUpstreamURL(req.Context(), req, providers.RouteContext{})
			if err != nil {
				t.Fatalf("ResolveUpstreamURL(%q): %v", path, err)
			}
			if got, want := upstream.Path, "/api/stubish/v1/chat/completions"; got != want {
				t.Fatalf("upstream path for %q = %q, want %q", path, got, want)
			}
			continue
		}
		if path != "/compatibility/v1/chat/completions" {
			t.Fatalf("default adapter did not match valid broad mount path %q", path)
		}
	}
}

func TestNew_DefaultRouteRespectsPerRequestBaseURL(t *testing.T) {
	adapter := New("https://default.example.test")
	req := httptest.NewRequest(http.MethodPost, "/compat/v1/responses?stream=true", nil)
	upstream, err := adapter.ResolveUpstreamURL(req.Context(), req, providers.RouteContext{
		BaseURL: "https://tenant.example.test/api/v1?tenant=42",
	})
	if err != nil {
		t.Fatalf("ResolveUpstreamURL: %v", err)
	}
	if got, want := upstream.String(), "https://tenant.example.test/api/v1/responses?tenant=42&stream=true"; got != want {
		t.Fatalf("upstream = %q, want %q", got, want)
	}
}

func TestCompatRoutesRejectAmbiguousEncodedAndDotSegments(t *testing.T) {
	adapter, err := NewNamed("groq", "https://api.groq.com/openai")
	if err != nil {
		t.Fatal(err)
	}
	defaultAdapter := New("https://ollama.example/v1")
	for _, rawPath := range []string{
		"/compat/groq%2Fv1/chat/completions",
		"/compat/groq%5Cv1/chat/completions",
		"/compat/v1/%2e%2e/admin",
		"/compat/v1/../admin",
		"/compat/v1/./admin",
		"/compat//v1/chat/completions",
	} {
		t.Run(rawPath, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, rawPath, nil)
			if err := ValidateRequestPath(req.URL); err == nil {
				t.Fatalf("ValidateRequestPath(%q) succeeded, want rejection (path=%q raw=%q)", rawPath, req.URL.Path, req.URL.RawPath)
			}
			if _, err := adapter.ResolveUpstreamURL(req.Context(), req, providers.RouteContext{}); err == nil {
				t.Fatalf("named resolver accepted ambiguous path %q (decoded=%q raw=%q)", rawPath, req.URL.Path, req.URL.RawPath)
			}
			if _, err := defaultAdapter.ResolveUpstreamURL(req.Context(), req, providers.RouteContext{}); err == nil {
				t.Fatalf("default resolver accepted ambiguous path %q (decoded=%q raw=%q)", rawPath, req.URL.Path, req.URL.RawPath)
			}
			if !strings.Contains(strings.ToLower(req.URL.RawPath), "%2f") && (adapter.MatchRoute(req.Method, req.URL.Path) || defaultAdapter.MatchRoute(req.Method, req.URL.Path)) {
				t.Fatalf("ambiguous path %q claimed a compat adapter (decoded=%q raw=%q)", rawPath, req.URL.Path, req.URL.RawPath)
			}
		})
	}
}

func TestCompatRoutesPreserveConfiguredAndRequestQueries(t *testing.T) {
	cases := []struct {
		name    string
		adapter providers.Adapter
		path    string
		want    string
	}{
		{
			name:    "named",
			adapter: mustNamed(t, "groq", "https://api.groq.com/openai?tenant=groq"),
			path:    "/compat/groq/v1/chat/completions?stream=true",
			want:    "https://api.groq.com/openai/v1/chat/completions?tenant=groq&stream=true",
		},
		{
			name:    "reserved legacy",
			adapter: New("https://legacy.example.test/v1?tenant=legacy"),
			path:    "/compat/openai-compatible/v1/chat/completions?stream=true",
			want:    "https://legacy.example.test/v1/chat/completions?tenant=legacy&stream=true",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			upstream, err := tc.adapter.ResolveUpstreamURL(req.Context(), req, providers.RouteContext{})
			if err != nil {
				t.Fatalf("ResolveUpstreamURL: %v", err)
			}
			if got := upstream.String(); got != tc.want {
				t.Fatalf("upstream = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCompatBaseURLValidationIsStrict(t *testing.T) {
	for _, raw := range []string{
		"ftp://api.example.test/v1",
		"https://user:pass@api.example.test/v1",
		"https://api.example.test/v1#fragment",
		"https://api.example.test/v1/../admin",
		"https://api.example.test/v1/%2e%2e/admin",
		"https://api.example.test/v1/%2Fadmin",
		"https://api.example.test/v1/%5Cadmin",
		"https:///missing-host",
	} {
		t.Run(raw, func(t *testing.T) {
			if err := ValidateBaseURL(raw); err == nil {
				t.Fatalf("ValidateBaseURL(%q) succeeded, want rejection", raw)
			}
		})
	}
}

func mustNamed(t *testing.T, name, baseURL string) providers.Adapter {
	t.Helper()
	adapter, err := NewNamed(name, baseURL)
	if err != nil {
		t.Fatalf("NewNamed: %v", err)
	}
	return adapter
}

func TestNewNamed_RouteAndResolve(t *testing.T) {
	adapter, err := NewNamed("groq", "https://api.groq.com/openai")
	if err != nil {
		t.Fatalf("NewNamed: %v", err)
	}
	if got := adapter.Name(); got != "openai_compatible" {
		t.Fatalf("provider = %q, want openai_compatible", got)
	}
	if !adapter.MatchRoute(http.MethodPost, "/compat/groq/v1/chat/completions") {
		t.Fatal("named adapter did not match its /compat/groq/ route")
	}
	if adapter.MatchRoute(http.MethodPost, "/compat/groqish/v1/chat/completions") {
		t.Fatal("named adapter matched a sibling prefix")
	}

	req := httptest.NewRequest(http.MethodPost, "/compat/groq/v1/chat/completions?stream=true", nil)
	upstream, err := adapter.ResolveUpstreamURL(req.Context(), req, providers.RouteContext{})
	if err != nil {
		t.Fatalf("ResolveUpstreamURL: %v", err)
	}
	want := "https://api.groq.com/openai/v1/chat/completions?stream=true"
	if got := upstream.String(); got != want {
		t.Fatalf("upstream = %q, want %q", got, want)
	}
}

func TestNewNamed_RouteContextBaseURLDoesNotOverridePerNameBase(t *testing.T) {
	adapter, err := NewNamed("groq", "https://api.groq.com/openai")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/compat/groq/v1/chat/completions", nil)
	upstream, err := adapter.ResolveUpstreamURL(req.Context(), req, providers.RouteContext{
		BaseURL: "https://project-override.example.test/custom",
	})
	if err != nil {
		t.Fatalf("ResolveUpstreamURL: %v", err)
	}
	want := "https://api.groq.com/openai/v1/chat/completions"
	if got := upstream.String(); got != want {
		t.Fatalf("named upstream = %q, want static per-name base %q", got, want)
	}
}

func TestNewNamed_RejectsInvalidAndReservedNames(t *testing.T) {
	cases := []string{
		"",
		"Groq",
		"-groq",
		"bad/name",
		"bad name",
		strings.Repeat("a", 65),
		"stub",
		"openai-compatible",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewNamed(name, "https://api.example.test"); err == nil {
				t.Fatalf("NewNamed(%q) succeeded, want error", name)
			}
		})
	}
}

// TestNewNamed_AnthropicMessagesPathUsesAPIKeyHeader shows that a named mount
// maps the credential by wire protocol. OpenCode Go serves both protocols from
// one upstream and rejects a Bearer header on /v1/messages.
func TestNewNamed_AnthropicMessagesPathUsesAPIKeyHeader(t *testing.T) {
	adapter := mustNamed(t, "opencode-go", "https://opencode.ai/zen/go")
	apiKey := providers.Credential{Mode: "ephemeral_header", Key: "sk-test"}
	realBearer := providers.Credential{Mode: "ephemeral_header", Key: "sk-oauth", Scheme: "bearer"}
	placeholder := providers.Credential{Mode: "ephemeral_header", Key: "no-key-required", Scheme: "bearer"}

	cases := []struct {
		name        string
		path        string
		credential  providers.Credential
		inbound     map[string]string
		wantAuth    string
		wantAPIKey  string
		wantVersion string
	}{
		{name: "messages", path: "/compat/opencode-go/v1/messages", credential: apiKey, wantAPIKey: "sk-test", wantVersion: "2023-06-01"},
		{name: "messages keeps a real bearer", path: "/compat/opencode-go/v1/messages", credential: realBearer, wantAuth: "Bearer sk-oauth", wantVersion: "2023-06-01"},
		{name: "messages remaps the placeholder", path: "/compat/opencode-go/v1/messages", credential: placeholder, wantAPIKey: "no-key-required", wantVersion: "2023-06-01"},
		{name: "messages sub-path", path: "/compat/opencode-go/v1/messages/count_tokens", credential: apiKey, wantAPIKey: "sk-test", wantVersion: "2023-06-01"},
		{name: "messages keeps inbound version", path: "/compat/opencode-go/v1/messages", credential: apiKey, inbound: map[string]string{"anthropic-version": "2024-01-01"}, wantAPIKey: "sk-test", wantVersion: "2024-01-01"},
		{name: "chat completions", path: "/compat/opencode-go/v1/chat/completions", credential: apiKey, wantAuth: "Bearer sk-test"},
		{name: "responses", path: "/compat/opencode-go/v1/responses", credential: apiKey, wantAuth: "Bearer sk-test"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			for name, value := range tc.inbound {
				req.Header.Set(name, value)
			}
			out, err := adapter.SanitizeAndMapHeaders(context.Background(), req, tc.credential, nil)
			if err != nil {
				t.Fatalf("SanitizeAndMapHeaders: %v", err)
			}
			if got := out.Get("authorization"); got != tc.wantAuth {
				t.Errorf("authorization = %q, want %q", got, tc.wantAuth)
			}
			if got := out.Get("x-api-key"); got != tc.wantAPIKey {
				t.Errorf("x-api-key = %q, want %q", got, tc.wantAPIKey)
			}
			if got := out.Get("anthropic-version"); got != tc.wantVersion {
				t.Errorf("anthropic-version = %q, want %q", got, tc.wantVersion)
			}
		})
	}

	req := httptest.NewRequest(http.MethodPost, "/compat/opencode-go/v1/messages", nil)
	out, err := adapter.SanitizeAndMapHeaders(context.Background(), req, providers.Credential{Mode: "ephemeral_header"}, nil)
	if err != nil {
		t.Fatalf("SanitizeAndMapHeaders without key: %v", err)
	}
	if out.Get("authorization") != "" || out.Get("x-api-key") != "" {
		t.Fatalf("empty credential produced auth headers: %v", out)
	}
}

// TestNewNamed_ForwardsOpenCodeSessionHeaders pins the session headers that
// OpenCode reads for attribution. The shared allowlist of the base adapter
// drops every x-opencode-* header, and OpenCode can reject a request without
// x-opencode-session. The forward covers the three Pi paths and stops at the
// opencode-go mount.
func TestNewNamed_ForwardsOpenCodeSessionHeaders(t *testing.T) {
	adapter := mustNamed(t, "opencode-go", "https://opencode.ai/zen/go")
	credential := providers.Credential{Mode: "ephemeral_header", Key: "sk-test"}
	inbound := map[string]string{
		"x-opencode-session": "ses_123",
		"x-opencode-client":  "pi",
		"x-opencode-project": "prj_456",
		"x-opencode-request": "req_789",
	}
	paths := []string{
		"/compat/opencode-go/v1/messages",
		"/compat/opencode-go/v1/chat/completions",
		"/compat/opencode-go/v1/responses",
	}
	for _, path := range paths {
		t.Run("forwards "+path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, nil)
			for name, value := range inbound {
				req.Header.Set(name, value)
			}
			out, err := adapter.SanitizeAndMapHeaders(context.Background(), req, credential, nil)
			if err != nil {
				t.Fatalf("SanitizeAndMapHeaders: %v", err)
			}
			for name, want := range inbound {
				if got := out.Get(name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
		})
		t.Run("omits "+path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, nil)
			out, err := adapter.SanitizeAndMapHeaders(context.Background(), req, credential, nil)
			if err != nil {
				t.Fatalf("SanitizeAndMapHeaders: %v", err)
			}
			for name := range inbound {
				if got := out.Get(name); got != "" {
					t.Errorf("%s = %q, want no header", name, got)
				}
			}
		})
	}

	// The messages path keeps the credential mapping of the wire protocol.
	req := httptest.NewRequest(http.MethodPost, "/compat/opencode-go/v1/messages", nil)
	for name, value := range inbound {
		req.Header.Set(name, value)
	}
	out, err := adapter.SanitizeAndMapHeaders(context.Background(), req, credential, nil)
	if err != nil {
		t.Fatalf("SanitizeAndMapHeaders: %v", err)
	}
	if got := out.Get("x-api-key"); got != "sk-test" {
		t.Errorf("x-api-key = %q, want %q", got, "sk-test")
	}
	if got := out.Get("authorization"); got != "" {
		t.Errorf("authorization = %q, want no header", got)
	}
	if got := out.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want %q", got, "2023-06-01")
	}

	// Another named mount gets none of the headers.
	other := mustNamed(t, "other", "https://api.example.test")
	otherReq := httptest.NewRequest(http.MethodPost, "/compat/other/v1/chat/completions", nil)
	for name, value := range inbound {
		otherReq.Header.Set(name, value)
	}
	otherOut, err := other.SanitizeAndMapHeaders(context.Background(), otherReq, credential, nil)
	if err != nil {
		t.Fatalf("SanitizeAndMapHeaders on the other mount: %v", err)
	}
	for name := range inbound {
		if got := otherOut.Get(name); got != "" {
			t.Errorf("other mount %s = %q, want no header", name, got)
		}
	}
}
