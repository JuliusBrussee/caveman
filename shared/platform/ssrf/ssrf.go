// Package ssrf provides SSRF (Server-Side Request Forgery) protection for
// outbound HTTP connections. It blocks link-local, loopback, private, and
// cloud-metadata addresses at both pre-flight (URL validation) and dial time
// (DialContext wrapper) to defend against DNS rebinding.
package ssrf

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// loopbackPrefixes lists the loopback ranges. They are always blocked in
// managed mode; in self-hosted mode an explicit allowlist entry (the hostname,
// the IP literal, or "localhost") opts them back in — a single operator
// pointing their own proxy at a local model server (Ollama, LM Studio, a test
// stub) is the machine's owner, so there is no confused-deputy boundary to
// defend. Managed multi-tenant mode keeps the absolute block.
var loopbackPrefixes = func() []netip.Prefix {
	raw := []string{
		"127.0.0.0/8",
		"::1/128",
	}
	out := make([]netip.Prefix, 0, len(raw))
	for _, s := range raw {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			panic("ssrf: invalid built-in prefix: " + s)
		}
		out = append(out, p.Masked())
	}
	return out
}()

// blockedPrefixes lists CIDR ranges that are always blocked regardless of
// mode. Built once at init time using net/netip for allocation-free checks.
var blockedPrefixes = func() []netip.Prefix {
	raw := []string{
		// Link-local unicast (includes AWS/GCP/Azure metadata 169.254.169.254)
		"169.254.0.0/16",
		"fe80::/10",
		// Unique-local IPv6 (fc00::/7 covers both fc00::/8 and fd00::/8)
		"fc00::/7",
		// Unspecified
		"0.0.0.0/8",
		"::/128",
		// IPv4 multicast
		"224.0.0.0/4",
		// IPv6 multicast
		"ff00::/8",
		// Documentation / test ranges (not routable)
		"192.0.2.0/24",
		"198.51.100.0/24",
		"203.0.113.0/24",
		"2001:db8::/32",
		// Shared/special-use ranges must not become internal-network pivots.
		"100.64.0.0/10",  // carrier-grade NAT
		"192.0.0.0/24",   // IETF protocol assignments
		"240.0.0.0/4",    // reserved/broadcast
		"100::/64",       // IPv6 discard-only
		"64:ff9b::/96",   // NAT64 well-known prefix
		"64:ff9b:1::/48", // NAT64 local-use prefix
		"2001::/32",      // Teredo
		"2002::/16",      // 6to4
		// IPv4-mapped IPv6 range — blocks ::ffff:127.0.0.1 style bypasses
		"::ffff:0:0/96",
	}
	out := make([]netip.Prefix, 0, len(raw))
	for _, s := range raw {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			panic("ssrf: invalid built-in prefix: " + s)
		}
		out = append(out, p.Masked())
	}
	return out
}()

// selfHostedSyntheticPrefixes contains non-routable ranges used by local TUN
// clients as synthetic DNS answers. Managed deployments keep these blocked;
// self-hosted clients must be able to dial them so mihomo/Clash-style fake-IP
// routing can translate the connection back to the intended provider host.
var selfHostedSyntheticPrefixes = func() []netip.Prefix {
	raw := []string{
		"198.18.0.0/15",       // RFC 2544 benchmarking; common fake-IP IPv4 pool
		"fdfe:dcba:9876::/64", // common mihomo fake-IP IPv6 pool
	}
	out := make([]netip.Prefix, 0, len(raw))
	for _, s := range raw {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			panic("ssrf: invalid built-in prefix: " + s)
		}
		out = append(out, p.Masked())
	}
	return out
}()

// privateRFC1918 lists the RFC1918 private unicast prefixes.  These are
// conditionally blocked: always blocked in managed mode, blockable via
// AllowList in self-hosted mode.
var privateRFC1918 = func() []netip.Prefix {
	raw := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
	}
	out := make([]netip.Prefix, 0, len(raw))
	for _, s := range raw {
		p, _ := netip.ParsePrefix(s)
		out = append(out, p.Masked())
	}
	return out
}()

// Config controls SSRF guard behaviour.
type Config struct {
	// ManagedMode mirrors CAVE_ENV=="prod". When true:
	//   - RFC1918 private addresses are always blocked (AllowList is ignored).
	//   - Upstream port must be 443.
	//   - HTTP (non-TLS) is rejected.
	ManagedMode bool

	// AllowList is an explicit set of hosts (bare hostname or host:port) whose
	// resolved IPs are permitted even if they fall in a normally-blocked range.
	// Used in self-hosted deployments where the provider endpoint lives on an
	// internal network. Loopback destinations additionally accept the entry
	// "localhost" as covering 127.0.0.0/8 and ::1 (the transport dials the
	// resolved IP, so the hostname alone would never match at dial time).
	// Ignored when ManagedMode is true.
	AllowList []string

	// ConnectTimeout bounds the TCP connect phase. Zero selects 3 seconds in
	// managed mode and 30 seconds in self-hosted mode.
	ConnectTimeout time.Duration
}

// ManagedConfig returns a Config with ManagedMode enabled and no allowlist.
func ManagedConfig() Config { return Config{ManagedMode: true} }

// SelfHostedConfig returns a Config with ManagedMode disabled and the given
// allowlist.
func SelfHostedConfig(allowList ...string) Config {
	return Config{ManagedMode: false, AllowList: allowList}
}

// ValidateURL resolves raw to a URL, validates the scheme/port constraints,
// and checks every IP the hostname resolves to against the SSRF block lists.
// It is a pre-flight check only — see NewDialContext for dial-time enforcement.
//
// Errors are safe to return to callers; they contain the blocked IP but never
// the original credential material.
func ValidateURL(ctx context.Context, raw string, cfg Config) error {
	u, err := url.Parse(raw)
	if err != nil {
		// net/url.Error includes the raw URL (and may therefore include
		// credentials or query secrets). Keep this error field-only and stable.
		return errors.New("ssrf: invalid URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && !cfg.ManagedMode) {
		return fmt.Errorf("ssrf: scheme %q not permitted (managed mode requires https)", u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("ssrf: credentials embedded in URL are forbidden")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("ssrf: URL must contain a host")
	}
	port := u.Port()
	if cfg.ManagedMode && port != "" && port != "443" {
		return errors.New("ssrf: managed mode requires port 443")
	}
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return validateHostPort(ctx, host, port, cfg)
}

// ValidateHost resolves host (bare hostname or IP literal) and checks all
// resolved addresses.  Use when you have a host/port pair rather than a full
// URL.
func ValidateHost(ctx context.Context, host string, cfg Config) error {
	return validateHostPort(ctx, host, "", cfg)
}

func validateHostPort(ctx context.Context, host, port string, cfg Config) error {
	if err := validateHostInput(host); err != nil {
		return err
	}

	// If host is an IP literal, check it directly without a DNS round-trip.
	if addr, err := netip.ParseAddr(host); err == nil {
		return checkAddr(addr, host, port, cfg)
	}

	// "localhost" is explicitly blocked regardless of what DNS says — unless a
	// self-hosted operator allowlisted it (resolution still runs, so every
	// resolved address is range-checked below like any other).
	if strings.EqualFold(host, "localhost") && !(!cfg.ManagedMode && isInAllowList(host, port, cfg.AllowList)) {
		return fmt.Errorf("ssrf: host %q is blocked (loopback)", host)
	}

	// Resolve ALL addresses the hostname currently maps to.  A hostname that
	// returns even one blocked address is rejected (defense-in-depth against
	// split-horizon / DNS rebinding scenarios where the pre-flight check and
	// the dial see different answers).
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("ssrf: DNS resolution failed for %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("ssrf: host %q resolved to no addresses", host)
	}

	for _, ia := range addrs {
		a, ok := netip.AddrFromSlice(ia.IP)
		if !ok {
			return fmt.Errorf("ssrf: could not parse resolved IP %v for host %q", ia.IP, host)
		}
		a = a.Unmap() // normalise ::ffff:x.x.x.x → x.x.x.x
		if err := checkAddr(a, host, port, cfg); err != nil {
			return err
		}
	}
	return nil
}

// validateHostInput rejects URL/userinfo-shaped values before they reach DNS
// or an error formatter. IP literals (including zoned IPv6) are handled by
// netip.ParseAddr and may contain colons or a zone identifier.
func validateHostInput(host string) error {
	if host == "" {
		return errors.New("ssrf: invalid host")
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return nil
	}
	if strings.ContainsAny(host, "/?#@\\:%") {
		return errors.New("ssrf: invalid host")
	}
	return nil
}

// checkAddr returns an error if addr is in any blocked range.
//
// host is the original hostname (or IP literal) used for allowlist matching.
// At dial time host will itself be an IP literal; the allowlist check must
// therefore accept both the hostname form and the resolved IP string.
func checkAddr(addr netip.Addr, host, port string, cfg Config) error {
	// Strip any IPv6 zone identifier (e.g. fe80::1%eth0) before range checks:
	// netip.Prefix.Contains returns false for ANY zoned address, so without this
	// a zoned literal like "fe80::1%eth0" or "::1%lo0" would evade every blocked
	// prefix and defeat the loopback/link-local guard.
	addr = addr.WithZone("").Unmap()

	for _, p := range loopbackPrefixes {
		if p.Contains(addr) {
			// Managed mode blocks loopback absolutely — allowing tenants to
			// route through 127.x or ::1 would trivially reach local-only
			// services. Self-hosted mode opts back in only via an explicit
			// allowlist entry: the original host, the IP literal, or
			// "localhost" (dial time only ever sees the resolved IP).
			if !cfg.ManagedMode &&
				(isInAllowList(host, port, cfg.AllowList) || isInAllowList(addr.String(), port, cfg.AllowList) || isInAllowList("localhost", port, cfg.AllowList)) {
				return nil
			}
			// A fail-closed guard that does not name its own escape hatch reads
			// as "unsupported" rather than "not opted in" — #841 concluded the
			// proxy simply could not reach a local relay, when self-hosted mode
			// has allowed exactly that all along. Managed mode is deliberately
			// silent: the allowlist is a no-op there by contract, so advertising
			// it would send the operator after a setting that cannot help.
			if !cfg.ManagedMode {
				return fmt.Errorf("ssrf: destination %s (for host %q) is in blocked range %s; add %s to the SSRF allowlist (CAVE_SSRF_ALLOWLIST) to permit it", addr, host, p, allowListSuggestion(addr, port))
			}
			return fmt.Errorf("ssrf: destination %s (for host %q) is in blocked range %s", addr, host, p)
		}
	}

	for _, p := range selfHostedSyntheticPrefixes {
		if p.Contains(addr) {
			if !cfg.ManagedMode {
				return nil
			}
			return fmt.Errorf("ssrf: destination %s (for host %q) is in blocked range %s", addr, host, p)
		}
	}

	for _, p := range blockedPrefixes {
		if p.Contains(addr) {
			// These ranges (link-local/metadata, ULA outside the narrow local-TUN
			// exception, multicast, unspecified, documentation) are absolutely
			// blocked — no allowlist escape in any mode.
			return fmt.Errorf("ssrf: destination %s (for host %q) is in blocked range %s", addr, host, p)
		}
	}

	if inRFC1918(addr) {
		if cfg.ManagedMode {
			return fmt.Errorf("ssrf: destination %s (for host %q) is a private address blocked in managed mode", addr, host)
		}
		// In self-hosted mode, RFC1918 is blocked unless the original hostname
		// OR the resolved IP literal appears in the allowlist.
		if !isInAllowList(host, port, cfg.AllowList) && !isInAllowList(addr.String(), port, cfg.AllowList) {
			return fmt.Errorf("ssrf: destination %s (for host %q) is a private address; add %s to the SSRF allowlist (CAVE_SSRF_ALLOWLIST) to permit it", addr, host, allowListSuggestion(addr, port))
		}
	}
	return nil
}

// allowListSuggestion renders the allowlist entry to advise for a blocked
// destination. It must be an entry isInAllowList would actually accept at BOTH
// stages: ValidateHost pre-flights with an empty port, so JoinHostPort would
// emit a trailing-colon token like "127.0.0.1:" that matches only the
// port-less stage — an operator following that advice literally relaxes the
// pre-flight guard, is blocked again at dial time by a second message naming a
// different token, and leaves a stale weakening entry behind. The bare address
// form is the one entry that matches every stage.
func allowListSuggestion(addr netip.Addr, port string) string {
	if port == "" {
		return addr.String()
	}
	return net.JoinHostPort(addr.String(), port)
}

func inRFC1918(addr netip.Addr) bool {
	for _, p := range privateRFC1918 {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func isInAllowList(host, port string, list []string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	for _, entry := range list {
		raw := strings.TrimSpace(entry)
		entryHost, entryPort, err := net.SplitHostPort(raw)
		if err == nil {
			if strings.ToLower(strings.Trim(entryHost, "[]")) == h && entryPort == port {
				return true
			}
			continue
		}
		if strings.ToLower(strings.Trim(raw, "[]")) == h {
			return true
		}
	}
	return false
}

// DialContext returns a DialContext function suitable for use in
// http.Transport.DialContext.  It re-validates the resolved IP at the moment
// the kernel actually establishes the connection, closing the TOCTOU window
// between ValidateURL and the actual dial.
//
// This is the primary defence against DNS rebinding: even if a pre-flight
// ValidateURL passed, a rebind that returns a blocked IP by the time the
// transport dials will be caught here.
func DialContext(cfg Config) func(ctx context.Context, network, addr string) (net.Conn, error) {
	connectTimeout := cfg.ConnectTimeout
	if connectTimeout <= 0 {
		connectTimeout = 30 * time.Second
		if cfg.ManagedMode {
			connectTimeout = 3 * time.Second
		}
	}
	dialer := &net.Dialer{
		Timeout:   connectTimeout,
		KeepAlive: 30 * time.Second,
	}
	return dialContextWith(cfg, net.DefaultResolver.LookupNetIP, dialer.DialContext)
}

type lookupNetIPFunc func(context.Context, string, string) ([]netip.Addr, error)
type rawDialContextFunc func(context.Context, string, string) (net.Conn, error)

// dialContextWith resolves each hostname exactly once, validates every returned
// address, then dials a validated IP literal. net.Transport passes hostnames to
// DialContext; validating the hostname and handing it to net.Dialer would cause
// a second DNS lookup and reopen the DNS-rebinding window.
func dialContextWith(cfg Config, lookup lookupNetIPFunc, dial rawDialContextFunc) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, errors.New("ssrf: invalid dial address")
		}
		if err := validateHostInput(host); err != nil {
			return nil, err
		}
		if cfg.ManagedMode && port != "443" {
			return nil, errors.New("ssrf: managed mode requires port 443")
		}

		if parsed, parseErr := netip.ParseAddr(host); parseErr == nil {
			parsed = parsed.WithZone("").Unmap()
			if err := checkAddr(parsed, host, port, cfg); err != nil {
				return nil, err
			}
			return dial(ctx, network, net.JoinHostPort(parsed.String(), port))
		}

		if strings.EqualFold(host, "localhost") && !(!cfg.ManagedMode && isInAllowList(host, port, cfg.AllowList)) {
			return nil, fmt.Errorf("ssrf: host %q is blocked (loopback)", host)
		}
		addrs, err := lookup(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("ssrf: DNS resolution failed for %q: %w", host, err)
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("ssrf: host %q resolved to no addresses", host)
		}
		checked := make([]netip.Addr, 0, len(addrs))
		for _, resolved := range addrs {
			resolved = resolved.WithZone("").Unmap()
			if err := checkAddr(resolved, host, port, cfg); err != nil {
				return nil, err
			}
			checked = append(checked, resolved)
		}

		var lastErr error
		for _, resolved := range checked {
			conn, err := dial(ctx, network, net.JoinHostPort(resolved.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, fmt.Errorf("ssrf: dial validated addresses for %q: %w", host, lastErr)
	}
}

// NewHTTPClient returns an *http.Client whose Transport enforces the SSRF
// policy at dial time.  The caller may set additional fields (Timeout, etc.)
// on the returned client.
//
// Use this to create the gateway's upstream client so all outbound provider
// requests are guarded even against DNS-rebinding attacks.
func NewHTTPClient(cfg Config) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	// SSRF enforcement observes the address passed to DialContext. Go's
	// default transport may instead dial an HTTP(S)_PROXY address and leave the
	// proxy to connect to the request destination, which would move the guarded
	// boundary away from the host this client was built to protect. This package
	// has no destination-aware proxy contract, so protected clients are direct
	// by construction; callers that need a proxy must provide a separate,
	// explicitly validated client.
	t.Proxy = nil
	// Do not inherit alternate dial hooks from a process-mutated default
	// transport. DialTLS* can bypass DialContext entirely for HTTPS, and the
	// deprecated Dial hook otherwise competes with the guarded hook.
	t.Dial = nil
	t.DialTLSContext = nil
	t.DialTLS = nil
	t.DialContext = DialContext(cfg)
	return &http.Client{
		Transport: t,
		// Provider and webhook clients must not carry credentials across redirects.
		// Callers that intentionally implement redirects (for example OIDC) must
		// validate every hop explicitly and use their own bounded policy.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
