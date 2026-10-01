package postgresconfig

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWithOrgRejectsEmptyScopeBeforeOpeningTransaction(t *testing.T) {
	err := WithOrg(context.Background(), nil, "  ", nil)
	if err == nil || !strings.Contains(err.Error(), "organization scope is required") {
		t.Fatalf("empty scope error = %v", err)
	}
}

func TestProductionRequiresVerifyFullAndUsesSystemRootsWithoutCA(t *testing.T) {
	t.Setenv("CAVE_ENV", "prod")
	t.Setenv(caEnvironment, "")
	if _, err := ParsePoolConfig("postgres://user:pass@db.example:5432/cave?sslmode=require"); err == nil || !strings.Contains(err.Error(), "verify-full") {
		t.Fatalf("sslmode=require error = %v", err)
	}
	config, err := ParsePoolConfig("postgres://user:pass@db.example:5432/cave?sslmode=verify-full")
	if err != nil {
		t.Fatal(err)
	}
	if tls := config.ConnConfig.TLSConfig; tls == nil || tls.InsecureSkipVerify || tls.RootCAs != nil || len(config.ConnConfig.Fallbacks) != 0 {
		t.Fatal("verify-full without a CA must verify against the system roots with no fallback")
	}
}

// A remote host without an explicit sslmode would get pgx's prefer: TLS that
// checks no certificate, then plaintext. Only loopback keeps that default.
func TestRemoteHostRequiresExplicitSSLMode(t *testing.T) {
	t.Setenv("CAVE_ENV", "local")
	t.Setenv(caEnvironment, "")
	t.Setenv("PGSSLMODE", "")
	for _, refused := range []string{
		"postgres://user:pass@db.example:5432/cave",
		"postgres://user:pass@db.example:5432/cave?sslmode=prefer",
		"postgres://user:pass@db.example:5432/cave?sslmode=allow",
		"host=10.0.0.5 user=u dbname=cave",
		"host=/tmp,db.example user=u dbname=cave",
	} {
		if _, err := ParsePoolConfig(refused); err == nil || !strings.Contains(err.Error(), "verify-full") {
			t.Fatalf("ParsePoolConfig(%q) error = %v", refused, err)
		}
	}
	for _, accepted := range []string{
		"postgres://user:pass@db.example:5432/cave?sslmode=verify-full",
		"postgres://user:pass@db.example:5432/cave?sslmode=disable",
		"postgres://user:pass@db.example:5432/cave?sslmode=require",
		"host=10.0.0.5 user=u dbname=cave sslmode=disable",
		"postgres://user:pass@localhost:5432/cave",
		"postgres://user:pass@127.0.0.1:5432/cave",
		"postgres://user:pass@[::1]:5432/cave",
		"host=/var/run/postgresql user=u dbname=cave",
		// A socket attempt runs no TLS, whatever the mode: it does not make
		// the TCP host's explicit require a prefer.
		"host=/tmp,db.example user=u dbname=cave sslmode=require",
	} {
		if _, err := ParsePoolConfig(accepted); err != nil {
			t.Fatalf("ParsePoolConfig(%q) error = %v", accepted, err)
		}
	}
	t.Setenv("PGSSLMODE", "verify-full")
	if _, err := ParsePoolConfig("postgres://user:pass@db.example:5432/cave"); err != nil {
		t.Fatalf("PGSSLMODE=verify-full error = %v", err)
	}
}

// The mode is pgx's reading of the string, not a second parse of it: a quoted
// value that contains "sslmode=", a repeated key (pgx keeps the last) and
// PGSSLMODE behind the string cannot pass a mode pgx does not run.
func TestSSLModeIsTheOnePgxRuns(t *testing.T) {
	t.Setenv(caEnvironment, "")
	t.Setenv("PGSSLMODE", "")
	for env, cases := range map[string]struct{ refused, accepted []string }{
		"local": {
			refused: []string{
				"application_name='x sslmode=verify-full' host=db.example user=u dbname=cave",
				"host=db.example user=u dbname=cave sslmode=verify-full sslmode=prefer",
				"postgres://user:pass@db.example:5432/cave?sslmode=verify-full&sslmode=allow",
			},
			accepted: []string{
				"host=db.example user=u dbname=cave sslmode=prefer sslmode=verify-full",
				"postgres://user:pass@db.example:5432/cave?sslmode=prefer&sslmode=require",
			},
		},
		"prod": {
			refused: []string{
				"postgres://user:pass@db.example:5432/cave?sslmode=verify-full&sslmode=disable",
				"postgres://user:pass@db.example:5432/cave?application_name=sslmode%3Dverify-full",
			},
			accepted: []string{"postgres://user:pass@db.example:5432/cave?sslmode=prefer&sslmode=verify-full"},
		},
	} {
		t.Setenv("CAVE_ENV", env)
		for _, refused := range cases.refused {
			if _, err := ParsePoolConfig(refused); err == nil || !strings.Contains(err.Error(), "verify-full") {
				t.Errorf("%s: ParsePoolConfig(%q) error = %v", env, refused, err)
			}
		}
		for _, accepted := range cases.accepted {
			if _, err := ParsePoolConfig(accepted); err != nil {
				t.Errorf("%s: ParsePoolConfig(%q) error = %v", env, accepted, err)
			}
		}
	}
	t.Setenv("CAVE_ENV", "prod")
	t.Setenv("PGSSLMODE", "verify-full")
	if _, err := ParsePoolConfig("postgres://user:pass@db.example:5432/cave?sslmode=require"); err == nil {
		t.Error("PGSSLMODE=verify-full overrode the string's sslmode=require")
	}
	if _, err := ParsePoolConfig("postgres://user:pass@db.example:5432/cave"); err != nil {
		t.Errorf("production with PGSSLMODE=verify-full: %v", err)
	}
}

func TestProductionPinsProvidedCAAndServerName(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	cert, err := x509.ParseCertificate(server.Certificate().Raw)
	if err != nil {
		t.Fatal(err)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	t.Setenv("CAVE_ENV", "prod")
	t.Setenv(caEnvironment, string(ca))
	config, err := ParsePoolConfig("postgres://user:pass@db.internal:5432/cave?sslmode=verify-full")
	if err != nil {
		t.Fatal(err)
	}
	if config.ConnConfig.TLSConfig == nil || config.ConnConfig.TLSConfig.InsecureSkipVerify {
		t.Fatal("production TLS verification is disabled")
	}
	if config.ConnConfig.TLSConfig.ServerName != "db.internal" {
		t.Fatalf("server name = %q", config.ConnConfig.TLSConfig.ServerName)
	}
}

func TestProductionReadsCAFromPathWithoutPuttingPEMInEnvironment(t *testing.T) {
	ca := []byte("-----BEGIN CERTIFICATE-----\nfile-backed-ca\n-----END CERTIFICATE-----\n")
	path := filepath.Join(t.TempDir(), "postgres-ca.pem")
	if err := os.WriteFile(path, ca, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAVE_ENV", "prod")
	t.Setenv(caEnvironment, "")
	t.Setenv(caFileEnvironment, path)
	got, err := caPEMFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if got != strings.TrimSpace(string(ca)) {
		t.Fatalf("file-backed CA = %q, want file contents", got)
	}
}

func TestRejectsDirectAndFileCATogether(t *testing.T) {
	path := filepath.Join(t.TempDir(), "postgres-ca.pem")
	if err := os.WriteFile(path, []byte("certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAVE_ENV", "local")
	t.Setenv(caEnvironment, "direct")
	t.Setenv(caFileEnvironment, path)
	if _, err := ParsePoolConfig("postgres://user:pass@localhost:5432/cave?sslmode=disable"); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("both CA sources error = %v", err)
	}
}

func TestLocalPlaintextRemainsAvailable(t *testing.T) {
	t.Setenv("CAVE_ENV", "local")
	t.Setenv(caEnvironment, "")
	if _, err := ParsePoolConfig("postgres://user:pass@localhost:5432/cave?sslmode=disable"); err != nil {
		t.Fatal(err)
	}
}

func TestProductionRejectsMalformedAndNonPostgresURLs(t *testing.T) {
	t.Setenv("CAVE_ENV", "prod")
	t.Setenv(caEnvironment, "")
	for _, databaseURL := range []string{
		"://broken",
		"https://db.example/cave?sslmode=verify-full",
		"postgres:///cave?sslmode=verify-full",
	} {
		if _, err := ParsePoolConfig(databaseURL); err == nil || !strings.Contains(err.Error(), "must be a Postgres URL") {
			t.Fatalf("ParsePoolConfig(%q) error = %v", databaseURL, err)
		}
	}
}

func TestConfiguredCARejectsInvalidPEMAndPlaintextTLS(t *testing.T) {
	t.Setenv("CAVE_ENV", "local")
	t.Setenv(caEnvironment, "not a certificate")
	if _, err := ParsePoolConfig("postgres://user:pass@localhost:5432/cave?sslmode=verify-full"); err == nil || !strings.Contains(err.Error(), "contains no valid certificate") {
		t.Fatalf("invalid CA error = %v", err)
	}

	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	cert, err := x509.ParseCertificate(server.Certificate().Raw)
	if err != nil {
		t.Fatal(err)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	t.Setenv(caEnvironment, string(ca))
	if _, err := ParsePoolConfig("postgres://user:pass@localhost:5432/cave?sslmode=disable"); err == nil || !strings.Contains(err.Error(), "TLS is disabled") {
		t.Fatalf("plaintext with CA error = %v", err)
	}
}

func TestPoolConstructorsReturnParseErrorsWithoutDialing(t *testing.T) {
	t.Setenv("CAVE_ENV", "local")
	t.Setenv(caEnvironment, "")
	const invalid = "postgres://%zz"
	if _, err := NewPool(context.Background(), invalid); err == nil || !strings.Contains(err.Error(), "parse DATABASE_URL") {
		t.Fatalf("NewPool() error = %v", err)
	}
}
