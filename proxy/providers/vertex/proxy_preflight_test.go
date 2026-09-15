package vertex

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// #1001: same shape as the Bedrock case — a proxy-only network has no outbound
// DNS, and resolving the aiplatform host here failed the request before the
// upstream proxy was ever consulted.
func TestResolveUpstreamURL_ProductionPreflightSkipsDNSWhenProxied(t *testing.T) {
	t.Setenv("CAVE_ENV", "prod")
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("no outbound DNS on this network")
	}}
	t.Cleanup(func() { net.DefaultResolver = previous })

	a := newAdapter(t)
	req, err := http.NewRequest(http.MethodPost, predictPath("google", geminiModel, "generateContent"), nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := a.ResolveUpstreamURL(context.Background(), req, providers.RouteContext{}); err == nil {
		t.Fatal("unproxied production pre-flight must still resolve and fail without DNS")
	}

	proxied := providers.WithUpstreamProxy(context.Background(), func(*http.Request) (*url.URL, error) {
		return url.Parse("http://proxy.corp.example:3128")
	})
	got, err := a.ResolveUpstreamURL(proxied, req, providers.RouteContext{})
	if err != nil {
		t.Fatalf("proxied production pre-flight resolved DNS: %v", err)
	}
	if want := stubBase + predictPath("google", geminiModel, "generateContent")[len("/vertex"):]; got.String() != want {
		t.Fatalf("upstream url = %q, want %q", got, want)
	}
}

// The Anthropic SDK counts tokens on Vertex through a fixed pseudo-model
// segment: .../publishers/anthropic/models/count-tokens:rawPredict. Rejecting
// it as an unknown model failed every count_tokens call from a Vertex-routed
// Claude client. :countTokens is publisher-agnostic on aiplatform.
func TestAccountingRoutesResolveForBothPublishers(t *testing.T) {
	adapter := New("https://us-central1-aiplatform.googleapis.com")
	for _, path := range []string{
		"/vertex/v1/projects/p/locations/us-central1/publishers/anthropic/models/count-tokens:rawPredict",
		"/vertex/v1/projects/p/locations/us-central1/publishers/anthropic/models/claude-sonnet-4-5:countTokens",
		"/vertex/v1/projects/p/locations/us-central1/publishers/google/models/gemini-2.5-pro:countTokens",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		if _, err := adapter.ResolveUpstreamURL(req.Context(), req, providers.RouteContext{}); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
	denied := httptest.NewRequest(http.MethodPost,
		"/vertex/v1/projects/p/locations/us-central1/publishers/anthropic/models/count-tokens:streamRawPredict", nil)
	if _, err := adapter.ResolveUpstreamURL(denied.Context(), denied, providers.RouteContext{}); err == nil {
		t.Error("the accounting pseudo-model is not a general inference route")
	}
}
