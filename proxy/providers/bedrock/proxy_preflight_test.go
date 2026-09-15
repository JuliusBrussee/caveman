package bedrock

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// #1001: on a proxy-only corporate network the process has no outbound DNS, so
// the production pre-flight's resolve fails before the upstream proxy is ever
// consulted and Bedrock is unusable. With a proxy selected for the destination
// the pre-flight must complete without resolving anything.
func TestResolveUpstreamURL_ProductionPreflightSkipsDNSWhenProxied(t *testing.T) {
	t.Setenv("CAVE_ENV", "prod")
	withoutDNS(t)
	a := newAdapter(t)
	req, err := http.NewRequest(http.MethodPost, invokePath(claudeModel, "invoke"), nil)
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
	if want := stubBase + "/model/" + claudeModel + "/invoke"; got.String() != want {
		t.Fatalf("upstream url = %q, want %q", got, want)
	}
}

// withoutDNS makes every hostname lookup in this process fail, which is what a
// proxy-only network looks like from inside the proxy binary.
func withoutDNS(t *testing.T) {
	t.Helper()
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("no outbound DNS on this network")
	}}
	t.Cleanup(func() { net.DefaultResolver = previous })
}
