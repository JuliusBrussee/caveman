package ssrf

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"testing"
)

// IPv6 zone identifiers must not defeat the block lists: a zoned link-local or
// loopback literal has to be rejected just like its un-zoned form.
func TestValidateHost_ZonedAddressesBlocked(t *testing.T) {
	ctx := context.Background()
	blocked := []string{
		"fe80::1%eth0", // link-local with zone
		"::1%lo0",      // loopback with zone
		"fe80::1",      // link-local, no zone (control)
		"::1",          // loopback, no zone (control)
	}
	for _, h := range blocked {
		if err := ValidateHost(ctx, h, ManagedConfig()); err == nil {
			t.Errorf("ValidateHost(%q) = nil, want blocked", h)
		}
	}
}

func TestDialContext_HostPortAllowlistMatchesAtConnectTime(t *testing.T) {
	lookup := func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.0.0.1")}, nil
	}
	dialed := 0
	dial := func(context.Context, string, string) (net.Conn, error) {
		dialed++
		left, right := net.Pipe()
		_ = right.Close()
		return left, nil
	}
	guard := dialContextWith(SelfHostedConfig("model.internal:8443"), lookup, dial)
	conn, err := guard(context.Background(), "tcp", "model.internal:8443")
	if err != nil {
		t.Fatalf("exact host:port allowlist rejected: %v", err)
	}
	_ = conn.Close()
	if dialed != 1 {
		t.Fatalf("dial count = %d, want 1", dialed)
	}
	if _, err := guard(context.Background(), "tcp", "model.internal:9443"); err == nil {
		t.Fatal("host:port allowlist applied to another port")
	}
}

func TestDialContext_ResolveOnceThenDialValidatedIP(t *testing.T) {
	lookups := 0
	lookup := func(context.Context, string, string) ([]netip.Addr, error) {
		lookups++
		if lookups == 1 {
			return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
		}
		// A second resolution would model a DNS rebind to loopback.
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	var dialed string
	dial := func(_ context.Context, _, addr string) (net.Conn, error) {
		dialed = addr
		left, right := net.Pipe()
		_ = right.Close()
		return left, nil
	}
	conn, err := dialContextWith(ManagedConfig(), lookup, dial)(t.Context(), "tcp", "provider.example:443")
	if err != nil {
		t.Fatalf("guarded dial: %v", err)
	}
	_ = conn.Close()
	if lookups != 1 {
		t.Fatalf("DNS lookups = %d, want exactly 1", lookups)
	}
	if dialed != "1.1.1.1:443" {
		t.Fatalf("dialed %q, want validated IP literal", dialed)
	}
}

func TestDialContext_SelfHostedFakeIPReachesTUN(t *testing.T) {
	lookup := func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{
			netip.MustParseAddr("198.18.0.44"),
			netip.MustParseAddr("fdfe:dcba:9876::2c"),
		}, nil
	}
	var dialed string
	dial := func(_ context.Context, _, addr string) (net.Conn, error) {
		dialed = addr
		left, right := net.Pipe()
		_ = right.Close()
		return left, nil
	}
	conn, err := dialContextWith(SelfHostedConfig(), lookup, dial)(t.Context(), "tcp", "api.anthropic.com:443")
	if err != nil {
		t.Fatalf("self-hosted fake-IP dial: %v", err)
	}
	_ = conn.Close()
	if dialed != "198.18.0.44:443" {
		t.Fatalf("dialed %q, want fake-IP address", dialed)
	}

	ipv6Only := func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("fdfe:dcba:9876::2c")}, nil
	}
	dialed = ""
	conn, err = dialContextWith(SelfHostedConfig(), ipv6Only, dial)(t.Context(), "tcp", "api.anthropic.com:443")
	if err != nil {
		t.Fatalf("self-hosted IPv6 fake-IP dial: %v", err)
	}
	_ = conn.Close()
	if dialed != "[fdfe:dcba:9876::2c]:443" {
		t.Fatalf("dialed %q, want IPv6 fake-IP address", dialed)
	}

	dialed = ""
	_, err = dialContextWith(ManagedConfig(), lookup, dial)(t.Context(), "tcp", "api.anthropic.com:443")
	if err == nil {
		t.Fatal("managed guard accepted fake-IP destination")
	}
	if dialed != "" {
		t.Fatalf("managed guard dialed blocked fake-IP address %q", dialed)
	}
}

func TestDialContext_AllowlistedHostnameCannotReachMetadata(t *testing.T) {
	lookup := func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("169.254.169.254")}, nil
	}
	dialCalled := false
	dial := func(context.Context, string, string) (net.Conn, error) {
		dialCalled = true
		return nil, errors.New("must not dial")
	}
	guard := dialContextWith(SelfHostedConfig("model.internal"), lookup, dial)
	if _, err := guard(t.Context(), "tcp", "model.internal:80"); err == nil {
		t.Fatal("allowlisted hostname reached absolute-block metadata range")
	}
	if dialCalled {
		t.Fatal("dial called for metadata address")
	}
}

func TestManagedDialRejectsNonTLSPortBeforeNetwork(t *testing.T) {
	lookupCalled := false
	lookup := func(context.Context, string, string) ([]netip.Addr, error) {
		lookupCalled = true
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}
	guard := dialContextWith(ManagedConfig(), lookup, nil)
	if _, err := guard(t.Context(), "tcp", "provider.example:80"); err == nil {
		t.Fatal("managed dial accepted port 80")
	}
	if lookupCalled {
		t.Fatal("managed non-443 port reached DNS")
	}
}

func TestNewHTTPClientRejectsRedirects(t *testing.T) {
	client := NewHTTPClient(ManagedConfig())
	next, _ := http.NewRequest(http.MethodPost, "http://1.1.1.1:80/steal", nil)
	prev, _ := http.NewRequest(http.MethodPost, "https://provider.example/v1", nil)
	if err := client.CheckRedirect(next, []*http.Request{prev}); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect error = %v, want http.ErrUseLastResponse", err)
	}
}

func TestValidateURL_ZonedLiteralBlocked(t *testing.T) {
	ctx := context.Background()
	for _, u := range []string{
		"https://[fe80::1%25eth0]/v1/x",
		"https://[::1%25lo0]/v1/x",
	} {
		if err := ValidateURL(ctx, u, ManagedConfig()); err == nil {
			t.Errorf("ValidateURL(%q) = nil, want blocked", u)
		}
	}
}
