package config

import (
	"os"
	"path/filepath"
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
	if cfg.Middleware != want {
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
