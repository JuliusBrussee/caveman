// Package chhttp provides HTTP clients for ClickHouse calls with explicit
// timeouts. The standard library's http.DefaultClient has NO timeout, so a
// stalled or black-holed ClickHouse can hang a caller goroutine forever; under
// load that single slowdown cascades into exhausted goroutines across the
// gateway, worker, and control-api at once.
//
// Two profiles, both overridable via env:
//   - Insert: short (default 5s) so a hung write fails fast and routes to the
//     retry stream (the S25 drain) instead of blocking — CLICKHOUSE_INSERT_TIMEOUT_MS.
//   - Query: longer (default 30s) for reads and DDL/mutations that legitimately
//     take longer — CLICKHOUSE_QUERY_TIMEOUT_MS.
//
// client.Timeout bounds the WHOLE request (dial → response body read), so it
// holds even for callers that pass a context without a deadline. Callers that
// also set a tighter context deadline keep that tighter bound — Timeout is the
// backstop, never a loosening.
//
// TLS is configured here too, because every ClickHouse call in the repo goes
// through these two constructors — see the CLICKHOUSE_TLS_* block below.
package chhttp

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/JuliusBrussee/caveman/shared/platform/env"
)

const (
	defaultInsertTimeoutMS = 5000
	defaultQueryTimeoutMS  = 30000
)

// ReadBodyBounded reads an HTTP response/error body with a hard byte ceiling.
// It deliberately reads one byte beyond the limit so callers can distinguish a
// truncated body from an exact-limit body and fail closed before parsing or
// logging attacker-controlled content.
func ReadBodyBounded(r io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("clickhouse response byte limit must be positive")
	}
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("clickhouse response exceeds %d byte limit", maxBytes)
	}
	return data, nil
}

// The TLS knobs. All three are unset by default, in which case the clients keep
// stock net/http behaviour: system roots, hostname verified against the URL host.
const (
	// serverNameEnv overrides tls.Config.ServerName. This is the managed-
	// ClickHouse cutover case: the private .internal DNS record is dialled while
	// the deployment's certificate carries only *.dtwh SANs, so verification must
	// run against the name the certificate actually holds. Chain AND hostname
	// verification stay fully enforced — only the name being matched changes.
	serverNameEnv = "CLICKHOUSE_TLS_SERVER_NAME"
	// caFileEnv is a path to a PEM bundle APPENDED to the system roots (a private
	// CA is trusted in addition to, never instead of, the public ones).
	caFileEnv = "CLICKHOUSE_TLS_CA_FILE"
	// skipVerifyEnv disables chain and hostname verification. Production refuses
	// it outright; outside production it is honoured with a startup warning.
	skipVerifyEnv = "CLICKHOUSE_TLS_INSECURE_SKIP_VERIFY"
)

// InsertTimeout is the bound for ClickHouse inserts (CLICKHOUSE_INSERT_TIMEOUT_MS, default 5s).
func InsertTimeout() time.Duration {
	return clampMS(env.Int("CLICKHOUSE_INSERT_TIMEOUT_MS", defaultInsertTimeoutMS), defaultInsertTimeoutMS)
}

// QueryTimeout is the bound for ClickHouse queries/DDL (CLICKHOUSE_QUERY_TIMEOUT_MS, default 30s).
func QueryTimeout() time.Duration {
	return clampMS(env.Int("CLICKHOUSE_QUERY_TIMEOUT_MS", defaultQueryTimeoutMS), defaultQueryTimeoutMS)
}

// clampMS converts a millisecond count to a Duration, failing CLOSED to the
// bounded default for any value <= 0 or too large to fit time.Duration. This
// matters because http.Client.Timeout of 0 (or negative) means NO timeout — so
// zero, negative, or overflowed CLICKHOUSE_*_TIMEOUT_MS would silently
// re-introduce the unbounded client this package exists to eliminate. env.Int
// already returns the default for unset/garbage, but accepts these numeric
// values on 64-bit hosts, so the guard lives here.
func clampMS(ms, defMS int) time.Duration {
	maxDurationMilliseconds := int64((time.Duration(1<<63 - 1)) / time.Millisecond)
	if ms <= 0 || int64(ms) > maxDurationMilliseconds {
		ms = defMS
	}
	return time.Duration(ms) * time.Millisecond
}

// production defers to env.IsProduction so this package's TLS refusals arm on
// exactly the same CAVE_ENV values as env.Refuse*ProductionDefaults. Two
// definitions of "prod" would let one gate fire while the other stayed asleep.
func production() bool {
	return env.IsProduction()
}

// tlsClientConfig builds the ClickHouse TLS configuration from the environment,
// or (nil, nil) when no CLICKHOUSE_TLS_* knob is set — in which case callers keep
// the stock transport (system roots, hostname verified against the URL host).
//
// It fails CLOSED: a prod skip-verify request, an unreadable CA file, or a CA
// file with no parseable certificate all return an error rather than quietly
// downgrading to the ambient trust store.
func tlsClientConfig() (*tls.Config, error) {
	serverName := strings.TrimSpace(env.String(serverNameEnv, ""))
	caFile := strings.TrimSpace(env.String(caFileEnv, ""))
	skipVerify := env.Bool(skipVerifyEnv, false)

	if skipVerify && production() {
		return nil, fmt.Errorf("production refuses %s=true: it disables chain and hostname verification, so any in-path party can read or forge telemetry — use %s instead", skipVerifyEnv, serverNameEnv)
	}
	if serverName == "" && caFile == "" && !skipVerify {
		return nil, nil
	}

	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: serverName,
		// #nosec G402 -- refused in production above; non-prod only, and loudly
		// warned about by ValidateProduction at startup.
		InsecureSkipVerify: skipVerify,
	}
	if caFile != "" {
		roots, err := rootsWithCAFile(caFile)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = roots
	}
	return cfg, nil
}

// rootsWithCAFile returns the system pool with the PEM bundle at path appended.
// Appending (rather than replacing) keeps a public managed endpoint verifiable
// while a private CA is trusted for the internal one.
//
// The bundle is parsed block by block instead of via CertPool.AppendCertsFromPEM,
// which reports success as soon as ONE certificate parses and silently drops the
// rest. A secret mount that is truncated mid-bundle, or corrupt after the first
// entry, would then be half-trusted: the endpoints whose issuer survived keep
// verifying and the ones whose issuer was dropped fail later, at the first
// telemetry flush, looking like a network fault. Any unusable certificate block —
// or a trailing PEM header with no complete block behind it — fails the whole
// bundle CLOSED at boot instead.
func rootsWithCAFile(path string) (*x509.CertPool, error) {
	bundle, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", caFileEnv, err)
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("%s: load system certificate pool: %w", caFileEnv, err)
	}
	added := 0
	rest := bundle
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%s (%s): certificate %d is unparseable, so the bundle is incomplete and must not be half-trusted: %w", caFileEnv, path, added+1, err)
		}
		roots.AddCert(cert)
		added++
	}
	if bytes.Contains(rest, []byte("-----BEGIN")) {
		return nil, fmt.Errorf("%s (%s): trailing PEM block is truncated after %d certificate(s), so the bundle is incomplete and must not be half-trusted", caFileEnv, path, added)
	}
	if added == 0 {
		return nil, fmt.Errorf("%s (%s) contains no valid PEM certificate", caFileEnv, path)
	}
	return roots, nil
}

// errTransport fails every request with the configuration error that produced
// it. A client constructor cannot return an error, and falling back to the
// default transport would silently trade a rejected TLS configuration for
// unpinned verification — so the client is built, and refuses to send.
type errTransport struct{ err error }

func (t errTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, t.err }

// queryTransport is the shared connection pool behind ClickHouse query clients.
//
// http.DefaultTransport keeps only DefaultMaxIdleConnsPerHost (2) idle
// connections per host, so the 3rd+ concurrent ClickHouse read dials a fresh
// connection and throws it away on completion — paying a TCP + TLS handshake per
// query. Report handlers fan several queries out at once and production reads
// ClickHouse over a public TLS endpoint, so that handshake was landing on the
// hot path of every dashboard load.
//
// Cloned from DefaultTransport so proxy, TLS, and HTTP/2 behaviour stay stock;
// only the idle-pool sizing changes. MaxConnsPerHost stays 0 (unlimited), so a
// burst is never blocked waiting for a pool slot — the cap only governs how many
// warm connections are RETAINED between calls.
//
// Inserts deliberately keep the implicit http.DefaultTransport whenever no
// CLICKHOUSE_TLS_* knob is set: that path is one sequential POST per flush
// (nothing to pool for), and resolving the transport at request time is what
// lets the gateway's round-trip tests intercept telemetry writes by swapping
// http.DefaultTransport.
//
// The pool is memoised so every query client in a process shares one set of warm
// connections. That fixes the environment at first use, which is correct for a
// service (env is process-wide and read at startup); tests exercise
// newQueryTransport directly instead.
var queryTransport = sync.OnceValues(newQueryTransport)

func newQueryTransport() (*http.Transport, error) {
	cfg, err := tlsClientConfig()
	if err != nil {
		return nil, err
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 128
	t.MaxIdleConnsPerHost = 32
	t.IdleConnTimeout = 90 * time.Second
	applyTLS(t, cfg)
	return t, nil
}

// applyTLS folds the configured overrides into the cloned transport's own TLS
// config instead of replacing it: http.Transport.Clone() carries the ALPN list
// that keeps HTTP/2 available, and swapping the struct would silently drop it.
func applyTLS(t *http.Transport, cfg *tls.Config) {
	if cfg == nil {
		return
	}
	if t.TLSClientConfig == nil {
		t.TLSClientConfig = &tls.Config{}
	}
	target := t.TLSClientConfig
	if target.MinVersion < cfg.MinVersion {
		target.MinVersion = cfg.MinVersion
	}
	target.ServerName = cfg.ServerName
	if cfg.RootCAs != nil {
		target.RootCAs = cfg.RootCAs
	}
	target.InsecureSkipVerify = cfg.InsecureSkipVerify
}

// newInsertTransport returns nil (meaning http.DefaultTransport) unless TLS is
// configured, so the interception the gateway's round-trip tests rely on keeps
// working in every deployment that does not need a TLS override.
func newInsertTransport() (http.RoundTripper, error) {
	cfg, err := tlsClientConfig()
	if err != nil || cfg == nil {
		return nil, err
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	applyTLS(t, cfg)
	return t, nil
}

// NewInsertClient returns an *http.Client bounded for ClickHouse inserts.
func NewInsertClient() *http.Client {
	transport, err := newInsertTransport()
	if err != nil {
		return &http.Client{Timeout: InsertTimeout(), Transport: errTransport{err: err}}
	}
	return &http.Client{Timeout: InsertTimeout(), Transport: transport}
}

// NewQueryClient returns an *http.Client bounded for ClickHouse queries/DDL.
func NewQueryClient() *http.Client {
	transport, err := queryTransport()
	if err != nil {
		return &http.Client{Timeout: QueryTimeout(), Transport: errTransport{err: err}}
	}
	return &http.Client{Timeout: QueryTimeout(), Transport: transport}
}

// ValidateProduction is the startup gate for the ClickHouse transport: call it
// from every service main alongside env.Refuse*ProductionDefaults, as early as
// main allows.
//
// It refuses, in production, a non-HTTPS CLICKHOUSE_URL (the ClickHouse password
// travels as HTTP Basic, so plaintext ships it in the clear) and any attempt to
// disable certificate verification. It also resolves the TLS configuration
// eagerly so an unreadable or unparseable CA bundle fails the process at boot
// instead of at the first telemetry flush. Outside production it warns — loudly
// and once — when verification has been switched off.
//
// It is NOT guaranteed to run before every client is constructed: control-api
// builds a package-level query client at init (internal/httpapi/chclients.go), so
// that one exists before main calls anything. That ordering is safe rather than
// lucky — a client built from a rejected configuration carries errTransport and
// refuses to send, so the failure is the refusal either way; ValidateProduction's
// job is to turn it into a loud non-zero exit at boot instead of a runtime error.
func ValidateProduction(logger *slog.Logger) error {
	if production() {
		raw := strings.TrimSpace(env.String("CLICKHOUSE_URL", ""))
		parsed, err := url.Parse(raw)
		if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Hostname() == "" {
			return fmt.Errorf("production requires an https:// CLICKHOUSE_URL (TLS only); ClickHouse credentials travel as HTTP Basic and a plaintext endpoint ships them in the clear")
		}
	}
	cfg, err := tlsClientConfig()
	if err != nil {
		return err
	}
	if cfg != nil && cfg.InsecureSkipVerify && logger != nil {
		logger.Warn("ClickHouse TLS certificate verification is DISABLED",
			"variable", skipVerifyEnv,
			"env", env.String("CAVE_ENV", "local"),
			"impact", "telemetry and aggregate queries can be read or forged by any in-path party; never set this in production")
	}
	return nil
}
