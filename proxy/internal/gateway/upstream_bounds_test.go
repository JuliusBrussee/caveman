package gateway

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/shared/platform/ssrf"
)

// With no total request deadline (CAVE_GATEWAY_UPSTREAM_TIMEOUT_MS defaults to
// 0) an upstream that connects and then never answers would hang the handler
// forever. The header deadline is the replacement bound; it must not be a body
// deadline, so the streaming suite stays the proof that long responses survive.
func TestDefaultUpstreamClientBoundsResponseHeaders(t *testing.T) {
	t.Setenv("CAVE_GATEWAY_RESPONSE_HEADER_TIMEOUT_MS", "150")
	hang := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-hang:
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	defer close(hang)

	s := New(Config{Adapters: []providers.Adapter{anthropic.New(upstream.URL)}, Auth: stubAuth{}, Creds: stubCreds{key: "sk-test"}})
	transport, ok := s.httpClient.Transport.(*http.Transport)
	if !ok || transport.ResponseHeaderTimeout != 150*time.Millisecond || transport.IdleConnTimeout == 0 {
		t.Fatalf("unbounded default transport: %#v", s.httpClient.Transport)
	}

	done := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-5","messages":[]}`)))
		done <- rec.Code
	}()
	select {
	case code := <-done:
		if code != http.StatusBadGateway {
			t.Fatalf("silent upstream = %d, want 502", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("silent upstream hung the handler: no bound left after the total deadline was removed")
	}
}

// The endpoint pre-flight in the Bedrock and Vertex adapters must be able to see
// that this request leaves through a proxy (#1001). The selector it reads comes
// from the transport that will actually carry the request.
func TestHandlerPublishesUpstreamProxySelectorToAdapters(t *testing.T) {
	// A proxy-only network: nothing in this process can resolve a hostname.
	previousResolver := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("no outbound DNS on this network")
	}}
	t.Cleanup(func() { net.DefaultResolver = previousResolver })

	unresolvable, _ := url.Parse("https://api.anthropic.invalid/v1/messages")
	respond := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	for _, tc := range []struct {
		name       string
		proxy      func(*http.Request) (*url.URL, error)
		wantPassed bool
	}{
		{name: "direct client resolves as before"},
		{name: "proxied client skips resolution", proxy: func(*http.Request) (*url.URL, error) {
			return url.Parse("http://proxy.corp.example:3128")
		}, wantPassed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &preflightAdapter{Adapter: anthropic.New("https://api.anthropic.invalid"), endpoint: unresolvable}
			transport := &http.Transport{Proxy: tc.proxy}
			transport.RegisterProtocol("https", respond)
			s := New(Config{Adapters: []providers.Adapter{recorder}, Auth: stubAuth{}, Creds: stubCreds{key: "sk-test"},
				HTTPClient: &http.Client{Transport: transport}})
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-5","messages":[]}`)))
			if !recorder.seen {
				t.Fatal("adapter never ran the pre-flight")
			}
			if passed := recorder.err == nil; passed != tc.wantPassed {
				t.Fatalf("pre-flight error = %v, want passed=%v", recorder.err, tc.wantPassed)
			}
		})
	}
}

// preflightAdapter runs the same managed-mode pre-flight the Bedrock and Vertex
// adapters run, against a host that does not resolve.
type preflightAdapter struct {
	providers.Adapter
	endpoint *url.URL
	seen     bool
	err      error
}

func (a *preflightAdapter) ResolveUpstreamURL(ctx context.Context, r *http.Request, route providers.RouteContext) (*url.URL, error) {
	a.seen = true
	a.err = providers.ValidateUpstreamEndpoint(ctx, a.endpoint, ssrf.ManagedConfig())
	return a.Adapter.ResolveUpstreamURL(ctx, r, route)
}
