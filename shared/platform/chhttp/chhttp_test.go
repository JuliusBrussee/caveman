package chhttp

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadBodyBoundedRejectsOverLimit(t *testing.T) {
	if _, err := ReadBodyBounded(strings.NewReader("12345"), 4); err == nil {
		t.Fatal("ReadBodyBounded accepted a body over its hard limit")
	}
	got, err := ReadBodyBounded(strings.NewReader("1234"), 4)
	if err != nil || string(got) != "1234" {
		t.Fatalf("ReadBodyBounded exact limit = %q, %v", got, err)
	}
}

func TestDefaultTimeouts(t *testing.T) {
	if got := InsertTimeout(); got != 5*time.Second {
		t.Fatalf("default InsertTimeout = %v, want 5s", got)
	}
	if got := QueryTimeout(); got != 30*time.Second {
		t.Fatalf("default QueryTimeout = %v, want 30s", got)
	}
}

func TestTimeoutsFromEnv(t *testing.T) {
	t.Setenv("CLICKHOUSE_INSERT_TIMEOUT_MS", "750")
	t.Setenv("CLICKHOUSE_QUERY_TIMEOUT_MS", "12000")
	if got := InsertTimeout(); got != 750*time.Millisecond {
		t.Fatalf("InsertTimeout = %v, want 750ms", got)
	}
	if got := QueryTimeout(); got != 12*time.Second {
		t.Fatalf("QueryTimeout = %v, want 12s", got)
	}
}

// TestZeroOrNegativeEnvFailsClosed is the honesty guard: a misconfigured
// CLICKHOUSE_*_TIMEOUT_MS=0 or negative must NOT yield Timeout:0 (no timeout =
// the unbounded client this package exists to kill). It must clamp to the
// bounded default.
func TestZeroOrNegativeEnvFailsClosed(t *testing.T) {
	for _, v := range []string{"0", "-1", "-5000"} {
		t.Run("insert="+v, func(t *testing.T) {
			t.Setenv("CLICKHOUSE_INSERT_TIMEOUT_MS", v)
			if got := InsertTimeout(); got != 5*time.Second {
				t.Fatalf("InsertTimeout with env=%q = %v, want clamped to 5s (not an unbounded 0)", v, got)
			}
			if c := NewInsertClient(); c.Timeout <= 0 {
				t.Fatalf("insert client Timeout=%v with env=%q — unbounded client re-introduced", c.Timeout, v)
			}
		})
		t.Run("query="+v, func(t *testing.T) {
			t.Setenv("CLICKHOUSE_QUERY_TIMEOUT_MS", v)
			if got := QueryTimeout(); got != 30*time.Second {
				t.Fatalf("QueryTimeout with env=%q = %v, want clamped to 30s (not an unbounded 0)", v, got)
			}
			if c := NewQueryClient(); c.Timeout <= 0 {
				t.Fatalf("query client Timeout=%v with env=%q — unbounded client re-introduced", c.Timeout, v)
			}
		})
	}
}

func TestOverflowingTimeoutEnvFailsClosed(t *testing.T) {
	// Parses as int on 64-bit hosts, then overflows when multiplied by a
	// millisecond unless clampMS checks the bound before conversion. On 32-bit
	// hosts env.Int rejects it and returns the same safe default.
	t.Setenv("CLICKHOUSE_INSERT_TIMEOUT_MS", "9223372036854775807")
	t.Setenv("CLICKHOUSE_QUERY_TIMEOUT_MS", "9223372036854775807")
	if got := InsertTimeout(); got != 5*time.Second {
		t.Fatalf("overflowing InsertTimeout = %v, want 5s", got)
	}
	if got := QueryTimeout(); got != 30*time.Second {
		t.Fatalf("overflowing QueryTimeout = %v, want 30s", got)
	}
	if c := NewInsertClient(); c.Timeout <= 0 {
		t.Fatalf("overflowing insert timeout produced unbounded client: %v", c.Timeout)
	}
	if c := NewQueryClient(); c.Timeout <= 0 {
		t.Fatalf("overflowing query timeout produced unbounded client: %v", c.Timeout)
	}
}

// The query client must keep enough warm connections for a report handler's
// concurrent fan-out. http.DefaultTransport's 2-per-host idle cap meant the 3rd
// concurrent query paid a fresh TCP + TLS handshake against the production
// ClickHouse endpoint on every dashboard load.
func TestQueryClientPoolsConnectionsForFanOut(t *testing.T) {
	c := NewQueryClient()
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("query client transport = %T, want *http.Transport with an explicit idle pool", c.Transport)
	}
	if tr.MaxIdleConnsPerHost <= http.DefaultMaxIdleConnsPerHost {
		t.Errorf("MaxIdleConnsPerHost = %d, want > %d so concurrent report queries reuse connections",
			tr.MaxIdleConnsPerHost, http.DefaultMaxIdleConnsPerHost)
	}
}

// Inserts stay on the implicit http.DefaultTransport: the gateway's round-trip
// tests intercept telemetry writes by swapping it, which only works while the
// client resolves the transport at request time.
func TestInsertClientUsesDefaultTransport(t *testing.T) {
	if tr := NewInsertClient().Transport; tr != nil {
		t.Errorf("insert client transport = %T, want nil (http.DefaultTransport)", tr)
	}
}

func TestClientsCarryTimeout(t *testing.T) {
	if c := NewInsertClient(); c.Timeout != InsertTimeout() {
		t.Fatalf("insert client timeout = %v, want %v", c.Timeout, InsertTimeout())
	}
	if c := NewQueryClient(); c.Timeout != QueryTimeout() {
		t.Fatalf("query client timeout = %v, want %v", c.Timeout, QueryTimeout())
	}
}

// ── TLS ───────────────────────────────────────────────────────────────────────
//
// The query pool is memoised (one shared set of warm connections per process),
// so TLS assertions below drive newQueryTransport / NewInsertClient, which read
// the environment on every call.

// selfSignedCert returns a certificate valid ONLY for dnsName (no IP SANs) plus
// the path to its PEM, so a client dialling 127.0.0.1 can only verify it via a
// ServerName override — exactly the managed-ClickHouse `*.dtwh` situation.
func selfSignedCert(t *testing.T, dnsName string) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: dnsName},
		DNSNames:              []string{dnsName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("build key pair: %v", err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, certPEM, 0o600); err != nil {
		t.Fatalf("write CA file: %v", err)
	}
	return pair, path
}

func startTLSServer(t *testing.T, cert tls.Certificate) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("Ok.\n"))
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// The private-TLS cutover: the endpoint is dialled by one name and its
// certificate carries another. Verification must succeed ONLY with the override
// — without it the handshake must still fail, proving the override moves the
// name being checked rather than switching checking off.
func TestServerNameOverrideIsRequiredAndSufficient(t *testing.T) {
	cert, caFile := selfSignedCert(t, "ch.dtwh")
	srv := startTLSServer(t, cert)

	t.Run("without override", func(t *testing.T) {
		t.Setenv("CLICKHOUSE_TLS_CA_FILE", caFile)
		resp, err := NewInsertClient().Get(srv.URL)
		if err == nil {
			resp.Body.Close()
			t.Fatal("handshake against 127.0.0.1 succeeded with a ch.dtwh-only certificate — hostname verification is not being enforced")
		}
	})

	t.Run("with override", func(t *testing.T) {
		t.Setenv("CLICKHOUSE_TLS_CA_FILE", caFile)
		t.Setenv("CLICKHOUSE_TLS_SERVER_NAME", "ch.dtwh")
		resp, err := NewInsertClient().Get(srv.URL)
		if err != nil {
			t.Fatalf("request with ServerName override failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	})
}

// Both client profiles must carry the same transport configuration — a TLS
// override that only reached inserts would leave every read unverifiable.
func TestBothClientProfilesCarryTLSConfig(t *testing.T) {
	t.Setenv("CLICKHOUSE_TLS_SERVER_NAME", "ch.dtwh")

	insert, err := newInsertTransport()
	if err != nil {
		t.Fatalf("newInsertTransport: %v", err)
	}
	it, ok := insert.(*http.Transport)
	if !ok {
		t.Fatalf("insert transport = %T, want *http.Transport once TLS is configured", insert)
	}
	if it.TLSClientConfig == nil || it.TLSClientConfig.ServerName != "ch.dtwh" {
		t.Errorf("insert transport TLS config = %+v, want ServerName=ch.dtwh", it.TLSClientConfig)
	}

	qt, err := newQueryTransport()
	if err != nil {
		t.Fatalf("newQueryTransport: %v", err)
	}
	if qt.TLSClientConfig == nil || qt.TLSClientConfig.ServerName != "ch.dtwh" {
		t.Errorf("query transport TLS config = %+v, want ServerName=ch.dtwh", qt.TLSClientConfig)
	}
	// The idle pool must survive the TLS wiring.
	if qt.MaxIdleConnsPerHost <= http.DefaultMaxIdleConnsPerHost {
		t.Errorf("MaxIdleConnsPerHost = %d, want the tuned pool preserved", qt.MaxIdleConnsPerHost)
	}
}

// A CA bundle is APPENDED to the system roots, never substituted for them: a
// private CA must not make a publicly-issued endpoint unverifiable.
func TestCAFileAppendsToSystemRoots(t *testing.T) {
	_, caFile := selfSignedCert(t, "ch.dtwh")
	t.Setenv("CLICKHOUSE_TLS_CA_FILE", caFile)

	cfg, err := tlsClientConfig()
	if err != nil {
		t.Fatalf("tlsClientConfig: %v", err)
	}
	if cfg == nil || cfg.RootCAs == nil {
		t.Fatal("CA file did not produce a root pool")
	}
	bundle, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatalf("read CA file: %v", err)
	}
	expected, err := x509.SystemCertPool()
	if err != nil {
		t.Skipf("system certificate pool unavailable: %v", err)
	}
	if !expected.AppendCertsFromPEM(bundle) {
		t.Fatal("test bundle did not parse")
	}
	if !cfg.RootCAs.Equal(expected) {
		t.Error("root pool is not the system roots plus the bundle — a private CA must be added to public trust, not replace it")
	}
}

// An unreadable or unparseable CA bundle must fail CLOSED — never silently fall
// back to ambient trust — both at startup and at client construction.
func TestCAFileFailsClosed(t *testing.T) {
	garbage := filepath.Join(t.TempDir(), "garbage.pem")
	if err := os.WriteFile(garbage, []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	cases := map[string]string{
		"missing":     filepath.Join(t.TempDir(), "absent.pem"),
		"unparseable": garbage,
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CLICKHOUSE_TLS_CA_FILE", path)
			if _, err := tlsClientConfig(); err == nil {
				t.Fatal("tlsClientConfig accepted an unusable CA bundle")
			}
			if err := ValidateProduction(nil); err == nil {
				t.Fatal("startup accepted an unusable CA bundle")
			}
			if _, err := newQueryTransport(); err == nil {
				t.Fatal("query transport built with an unusable CA bundle")
			}
			// The constructor cannot return an error, so the client it returns
			// must refuse to send rather than fall back to system roots.
			if _, err := NewInsertClient().Get("https://clickhouse.invalid/ping"); err == nil {
				t.Fatal("insert client sent a request despite an unusable CA bundle")
			}
		})
	}
}

// A rejected TLS configuration must keep failing when the endpoint is REACHABLE.
// The earlier fail-closed test dials an unresolvable host, so a client that had
// quietly fallen back to http.DefaultTransport would still error — for the wrong
// reason — and look correct. Here the server answers, so the only thing standing
// between the request and a 200 is errTransport: the request must fail with the
// CONFIGURATION error, never with a transport-level TLS complaint and never with
// a response.
func TestRejectedTLSConfigRefusesAgainstAReachableServer(t *testing.T) {
	cert, _ := selfSignedCert(t, "ch.dtwh")
	garbage := filepath.Join(t.TempDir(), "garbage.pem")
	if err := os.WriteFile(garbage, []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatalf("write garbage: %v", err)
	}

	// Insert clients only: the query pool is memoised for the whole process, so a
	// NewQueryClient here would hand back whatever transport an earlier test fixed.
	// newQueryTransport's own refusal is asserted directly in TestCAFileFailsClosed.
	assertRefused := func(t *testing.T, url string) {
		t.Helper()
		resp, err := NewInsertClient().Get(url)
		if err == nil {
			status := resp.StatusCode
			resp.Body.Close()
			t.Fatalf("insert client reached a live server (status %d) with a rejected TLS configuration", status)
		}
		if !strings.Contains(err.Error(), caFileEnv) {
			t.Fatalf("insert client error = %v, want the %s configuration failure — a transport-level error means the rejected config was replaced by ambient trust", err, caFileEnv)
		}
	}

	t.Run("tls server", func(t *testing.T) {
		srv := startTLSServer(t, cert)
		t.Setenv("CLICKHOUSE_TLS_CA_FILE", garbage)
		assertRefused(t, srv.URL)
	})

	// The unambiguous half: over plaintext HTTP a fallback transport has nothing
	// to verify, so anything but a refusal here is a 200.
	t.Run("plaintext server", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("Ok.\n"))
		}))
		t.Cleanup(srv.Close)
		t.Setenv("CLICKHOUSE_TLS_CA_FILE", garbage)
		assertRefused(t, srv.URL)
	})
}

// AppendCertsFromPEM reports success as soon as ONE certificate parses, so a
// truncated or corrupt secret mount would be half-trusted: the issuers that
// survived keep verifying, the dropped ones fail much later at a telemetry flush.
// Every unusable bundle must fail CLOSED at boot instead.
func TestPartiallyUnusableCABundleFailsClosed(t *testing.T) {
	_, goodFile := selfSignedCert(t, "ch.dtwh")
	good, err := os.ReadFile(goodFile)
	if err != nil {
		t.Fatalf("read good bundle: %v", err)
	}
	corrupt := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not DER")})
	truncated := good[:len(good)/2]

	for name, bundle := range map[string][]byte{
		"corrupt second certificate": append(append([]byte(nil), good...), corrupt...),
		"corrupt first certificate":  append(append([]byte(nil), corrupt...), good...),
		"truncated tail":             append(append([]byte(nil), good...), truncated...),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ca.pem")
			if err := os.WriteFile(path, bundle, 0o600); err != nil {
				t.Fatalf("write bundle: %v", err)
			}
			t.Setenv("CLICKHOUSE_TLS_CA_FILE", path)
			if _, err := tlsClientConfig(); err == nil {
				t.Fatal("a partially unusable CA bundle was accepted — the trust store is silently incomplete")
			}
			if err := ValidateProduction(nil); err == nil {
				t.Fatal("startup accepted a partially unusable CA bundle")
			}
		})
	}
}

// Skip-verify is refused in production. Shipping it would disable chain and
// hostname verification for every telemetry row and aggregate query.
func TestSkipVerifyRefusedInProduction(t *testing.T) {
	t.Setenv("CAVE_ENV", "prod")
	t.Setenv("CLICKHOUSE_URL", "https://clickhouse.internal:8443")
	t.Setenv("CLICKHOUSE_TLS_INSECURE_SKIP_VERIFY", "true")

	if _, err := tlsClientConfig(); err == nil || !strings.Contains(err.Error(), "CLICKHOUSE_TLS_INSECURE_SKIP_VERIFY") {
		t.Fatalf("tlsClientConfig error = %v, want a production refusal naming the variable", err)
	}
	if err := ValidateProduction(nil); err == nil {
		t.Fatal("startup accepted CLICKHOUSE_TLS_INSECURE_SKIP_VERIFY=true in production")
	}
	if _, err := newQueryTransport(); err == nil {
		t.Fatal("query transport built with skip-verify in production")
	}
	if _, err := NewInsertClient().Get("https://clickhouse.invalid/ping"); err == nil {
		t.Fatal("insert client sent a request with skip-verify in production")
	}
}

// Outside production it is honoured, but never silently: startup logs a warning.
func TestSkipVerifyOutsideProductionWarns(t *testing.T) {
	t.Setenv("CAVE_ENV", "local")
	t.Setenv("CLICKHOUSE_TLS_INSECURE_SKIP_VERIFY", "true")

	cfg, err := tlsClientConfig()
	if err != nil {
		t.Fatalf("tlsClientConfig: %v", err)
	}
	if cfg == nil || !cfg.InsecureSkipVerify {
		t.Fatalf("TLS config = %+v, want InsecureSkipVerify honoured outside production", cfg)
	}
	var logged bytes.Buffer
	if err := ValidateProduction(slog.New(slog.NewJSONHandler(&logged, nil))); err != nil {
		t.Fatalf("ValidateProduction: %v", err)
	}
	if !strings.Contains(logged.String(), "CLICKHOUSE_TLS_INSECURE_SKIP_VERIFY") || !strings.Contains(logged.String(), "WARN") {
		t.Fatalf("startup log = %q, want a warning naming the variable", logged.String())
	}
}

// Unset knobs must leave the stock transport alone — no TLS config, and inserts
// keep resolving http.DefaultTransport at request time.
func TestNoTLSEnvLeavesTransportsStock(t *testing.T) {
	cfg, err := tlsClientConfig()
	if err != nil {
		t.Fatalf("tlsClientConfig: %v", err)
	}
	if cfg != nil {
		t.Errorf("TLS config = %+v, want nil with no CLICKHOUSE_TLS_* set", cfg)
	}
	tr, err := newInsertTransport()
	if err != nil || tr != nil {
		t.Errorf("insert transport = (%v, %v), want (nil, nil)", tr, err)
	}
	qt, err := newQueryTransport()
	if err != nil {
		t.Fatalf("newQueryTransport: %v", err)
	}
	if tc := qt.TLSClientConfig; tc != nil && (tc.ServerName != "" || tc.RootCAs != nil || tc.InsecureSkipVerify) {
		t.Errorf("query transport TLS config = %+v, want the untouched clone of http.DefaultTransport's", tc)
	}
}

// The clone of http.DefaultTransport carries the ALPN list that keeps HTTP/2
// available; folding the overrides in must not drop it.
func TestTLSOverridePreservesALPN(t *testing.T) {
	t.Setenv("CLICKHOUSE_TLS_SERVER_NAME", "ch.dtwh")
	stock := http.DefaultTransport.(*http.Transport).Clone()
	qt, err := newQueryTransport()
	if err != nil {
		t.Fatalf("newQueryTransport: %v", err)
	}
	if stock.TLSClientConfig == nil {
		t.Skip("stock transport has no TLS config to preserve")
	}
	if got, want := strings.Join(qt.TLSClientConfig.NextProtos, ","), strings.Join(stock.TLSClientConfig.NextProtos, ","); got != want {
		t.Errorf("NextProtos = %q, want %q", got, want)
	}
}

// Production refuses a plaintext ClickHouse endpoint: the credentials travel as
// HTTP Basic, so http:// ships them in the clear. Non-prod is untouched.
func TestClickHouseURLSchemeValidation(t *testing.T) {
	cases := []struct {
		name    string
		caveEnv string
		url     string
		wantErr bool
	}{
		{name: "prod https", caveEnv: "prod", url: "https://ch.dtwh:8443", wantErr: false},
		{name: "prod http", caveEnv: "prod", url: "http://ch.internal:8123", wantErr: true},
		{name: "prod empty", caveEnv: "prod", url: "", wantErr: true},
		{name: "prod hostless", caveEnv: "prod", url: "https:///ping", wantErr: true},
		{name: "prod garbage", caveEnv: "prod", url: "://nonsense", wantErr: true},
		{name: "local http", caveEnv: "local", url: "http://clickhouse:8123", wantErr: false},
		{name: "local empty", caveEnv: "local", url: "", wantErr: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CAVE_ENV", tc.caveEnv)
			t.Setenv("CLICKHOUSE_URL", tc.url)
			err := ValidateProduction(nil)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateProduction accepted CLICKHOUSE_URL=%q under CAVE_ENV=%s", tc.url, tc.caveEnv)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateProduction rejected CLICKHOUSE_URL=%q under CAVE_ENV=%s: %v", tc.url, tc.caveEnv, err)
			}
		})
	}
}
