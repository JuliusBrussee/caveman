package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMiddlewareConfigYAMLAndEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caveman.yaml")
	yaml := "middleware:\n  retention_seconds: 3600\n  max_retention_seconds: 7200\n  queue_depth: 4\n  max_rows: 500\n  quota_bytes: 1024\n  encryption_key_file: /from/yaml\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAVEMAN_MIDDLEWARE_QUEUE_DEPTH", "32")
	t.Setenv("CAVEMAN_MIDDLEWARE_RETRIEVE_DEADLINE_MS", "9000")
	t.Setenv("CAVEMAN_MIDDLEWARE_MAX_ROWS", "not-a-number")
	t.Setenv("CAVEMAN_MIDDLEWARE_ENCRYPTION_KEY", " inline-key ")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := MiddlewareConfig{RetentionSeconds: 3600, MaxRetentionSeconds: 7200, QueueDepth: 32, RetrieveDeadlineMS: 9000,
		MaxRows: 500, QuotaBytes: 1024, EncryptionKeyFile: "/from/yaml", EncryptionKey: "inline-key"}
	if !reflect.DeepEqual(cfg.Middleware, want) {
		t.Fatalf("middleware config = %+v, want %+v", cfg.Middleware, want)
	}
}

// The encryption key is a secret: caveman.yaml can never supply it.
func TestMiddlewareEncryptionKeyIsEnvOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caveman.yaml")
	if err := os.WriteFile(path, []byte("middleware:\n  encryption_key: from-yaml\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Middleware.EncryptionKey != "" {
		t.Fatal("encryption key read from caveman.yaml")
	}
}

func TestMiddlewareIdentityAndHAKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caveman.yaml")
	yaml := "mode: record\nmiddleware:\n  mode: compress\n  token_map_file: /etc/caveman/tokens.yaml\n  oidc:\n    issuer: https://idp.example\n    audience: caveman\n    jwks_url: https://idp.example/jwks\n    algorithms: [ES256]\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAVEMAN_MIDDLEWARE_EPHEMERAL", "true")
	t.Setenv("CAVEMAN_MIDDLEWARE_DATABASE_URL", " postgres://u:p@db/caveman ")
	t.Setenv("CAVEMAN_MIDDLEWARE_OIDC_ALGORITHMS", "RS256, ES256")
	t.Setenv("CAVEMAN_MIDDLEWARE_OIDC_NAMESPACES_CLAIM", "caveman_namespaces")
	t.Setenv("CAVEMAN_TLS_CERT_FILE", "/tls/tls.crt")
	t.Setenv("CAVEMAN_TLS_KEY_FILE", "/tls/tls.key")
	t.Setenv("CAVEMAN_METRICS_TOKEN", " metrics-secret ")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	m := cfg.Middleware
	if cfg.Mode != "record" || m.Mode != "compress" || !m.Ephemeral || m.DatabaseURL != "postgres://u:p@db/caveman" || m.TokenMapFile != "/etc/caveman/tokens.yaml" {
		t.Fatalf("middleware keys = %+v (proxy mode %q)", m, cfg.Mode)
	}
	if !reflect.DeepEqual(m.OIDC, OIDCConfig{Issuer: "https://idp.example", Audience: "caveman", JWKSURL: "https://idp.example/jwks",
		Algorithms: []string{"RS256", "ES256"}, NamespacesClaim: "caveman_namespaces"}) {
		t.Fatalf("oidc = %+v", m.OIDC)
	}
	if cfg.TLS != (TLSConfig{CertFile: "/tls/tls.crt", KeyFile: "/tls/tls.key"}) || cfg.MetricsToken != "metrics-secret" {
		t.Fatalf("tls = %+v metrics token %q", cfg.TLS, cfg.MetricsToken)
	}
}

// A database URL carries a password: caveman.yaml is refused, not ignored, so a
// replica never silently runs on SQLite the operator did not choose.
func TestMiddlewareDatabaseURLIsEnvOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caveman.yaml")
	if err := os.WriteFile(path, []byte("middleware:\n  database_url: postgres://u:secret@db/x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "CAVEMAN_MIDDLEWARE_DATABASE_URL") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("database_url in yaml: %v", err)
	}
}

// Every identity source makes a non-loopback listener legal; nothing does not.
// TLS files must come as a pair.
func TestListenAuthenticationSourcesAndTLSPairs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.yaml")
	t.Setenv("CAVEMAN_LISTEN", "0.0.0.0:8787")
	if _, err := Load(path); err == nil {
		t.Fatal("non-loopback listen allowed with no inbound authentication")
	}
	for name, value := range map[string]string{
		"CAVEMAN_MIDDLEWARE_TOKEN_MAP_FILE": "/tokens.yaml", "CAVEMAN_MIDDLEWARE_OIDC_ISSUER": "https://idp.example",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, value)
			if _, err := Load(path); err != nil {
				t.Fatalf("%s did not authorize a non-loopback listen: %v", name, err)
			}
		})
	}
	t.Run("client CA", func(t *testing.T) {
		t.Setenv("CAVEMAN_TLS_CERT_FILE", "/c")
		t.Setenv("CAVEMAN_TLS_KEY_FILE", "/k")
		t.Setenv("CAVEMAN_TLS_CLIENT_CA_FILE", "/ca")
		if _, err := Load(path); err != nil {
			t.Fatalf("a client CA did not authorize a non-loopback listen: %v", err)
		}
	})
	t.Run("cert without key", func(t *testing.T) {
		t.Setenv("CAVEMAN_AUTH_TOKEN", "0123456789abcdef0123")
		t.Setenv("CAVEMAN_TLS_CERT_FILE", "/c")
		if _, err := Load(path); err == nil {
			t.Fatal("a certificate without a key was accepted")
		}
	})
}

// The per-principal queue share and the plaintext migration flag read the
// environment like every other key; Configured tells an operator-set
// middleware from the default.
func TestMiddlewareShareAndPlaintextKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caveman.yaml")
	if err := os.WriteFile(path, []byte("mode: record\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Middleware.Configured() {
		t.Fatalf("no middleware key set, yet Configured: %+v", cfg.Middleware)
	}
	t.Setenv("CAVEMAN_MIDDLEWARE_PRINCIPAL_IN_FLIGHT", "3")
	t.Setenv("CAVEMAN_MIDDLEWARE_ALLOW_PLAINTEXT_ORIGINALS", "true")
	if cfg, err = Load(path); err != nil {
		t.Fatal(err)
	}
	if m := cfg.Middleware; m.PrincipalInFlight != 3 || !m.AllowPlaintextOriginals || !m.Configured() {
		t.Fatalf("middleware config = %+v", m)
	}
}
