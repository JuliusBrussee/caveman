package objectstore

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// runStoreContract exercises the Store interface against any implementation.
func runStoreContract(t *testing.T, s Store) {
	ctx := context.Background()
	key := "artifacts/org/proj/abc"
	body := []byte("opaque encrypted bytes")

	if ok, _ := s.Exists(ctx, key); ok {
		t.Fatal("key should not exist yet")
	}
	if _, err := s.Get(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := s.Put(ctx, key, body, "application/octet-stream"); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := s.Get(ctx, key)
	if err != nil || string(got) != string(body) {
		t.Fatalf("get mismatch: %q %v", got, err)
	}
	keys, err := s.List(ctx, "artifacts/org/")
	if err != nil || len(keys) != 1 {
		t.Fatalf("list: %v %v", keys, err)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if ok, _ := s.Exists(ctx, key); ok {
		t.Fatal("key should be gone after delete")
	}
}

func TestMemoryStoreContract(t *testing.T) {
	runStoreContract(t, NewMemory())
}

func TestMemoryVersionPurgeContracts(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	for _, key := range []string{"capture/org-a/one", "capture/org-a/two", "capture/org-b/keep"} {
		if err := store.Put(ctx, key, []byte(key), ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DeleteAllVersions(ctx, "capture/org-a/one"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := store.Exists(ctx, "capture/org-a/one"); ok {
		t.Fatal("DeleteAllVersions left current object")
	}
	if err := store.DeletePrefixAllVersions(ctx, "capture/org-a/"); err != nil {
		t.Fatal(err)
	}
	keys, err := store.List(ctx, "capture/")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != "capture/org-b/keep" {
		t.Fatalf("prefix version purge left keys %v", keys)
	}
}

func TestPurgePrefixUsesVersionPurgerAndFallback(t *testing.T) {
	ctx := context.Background()
	versioned := &prefixTrackingStore{Memory: NewMemory()}
	if err := PurgePrefix(ctx, versioned, "capture/org-a/"); err != nil {
		t.Fatal(err)
	}
	if versioned.prefix != "capture/org-a/" {
		t.Fatalf("version-aware prefix = %q", versioned.prefix)
	}

	memory := NewMemory()
	for _, key := range []string{"capture/org-a/one", "capture/org-a/two", "capture/org-b/keep"} {
		if err := memory.Put(ctx, key, []byte(key), ""); err != nil {
			t.Fatal(err)
		}
	}
	fallback := &plainStore{memory: memory}
	if err := PurgePrefix(ctx, fallback, "capture/org-a/"); err != nil {
		t.Fatal(err)
	}
	keys, err := fallback.List(ctx, "capture/")
	if err != nil || len(keys) != 1 || keys[0] != "capture/org-b/keep" {
		t.Fatalf("fallback prefix purge keys=%v err=%v", keys, err)
	}
}

func TestPurgeFallbackSurfacesListAndDeleteErrors(t *testing.T) {
	sentinel := errors.New("backend unavailable")
	listFailure := &faultStore{listErr: sentinel}
	if err := PurgePrefix(context.Background(), listFailure, "capture/org/"); !errors.Is(err, sentinel) {
		t.Fatalf("list error = %v", err)
	}
	deleteFailure := &faultStore{keys: []string{"capture/org/one"}, deleteErr: sentinel}
	if err := PurgePrefix(context.Background(), deleteFailure, "capture/org/"); !errors.Is(err, sentinel) {
		t.Fatalf("delete error = %v", err)
	}
	if err := PurgeObject(context.Background(), deleteFailure, "capture/org/one"); !errors.Is(err, sentinel) {
		t.Fatalf("object delete error = %v", err)
	}
}

func TestProbeProvesReadWriteAndVersionPurge(t *testing.T) {
	store := &probeTrackingStore{Memory: NewMemory()}
	if err := Probe(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if store.versionPurges != 1 || store.Len() != 0 {
		t.Fatalf("version purges=%d remaining=%d", store.versionPurges, store.Len())
	}
}

func TestProbeCleanupHonorsCallerDeadline(t *testing.T) {
	store := &deadlineCleanupStore{Memory: NewMemory()}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := Probe(ctx, store); err == nil {
		t.Fatal("probe unexpectedly succeeded")
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("probe cleanup ignored caller deadline: %s", elapsed)
	}
}

type deadlineCleanupStore struct{ *Memory }

func (s *deadlineCleanupStore) Get(context.Context, string) ([]byte, error) {
	return nil, errors.New("probe read failed")
}

func (s *deadlineCleanupStore) Delete(_ context.Context, _ string) error {
	return errors.New("ordinary delete should not be used")
}

func (s *deadlineCleanupStore) DeleteAllVersions(ctx context.Context, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

type probeTrackingStore struct {
	*Memory
	versionPurges int
}

func (s *probeTrackingStore) DeleteAllVersions(ctx context.Context, key string) error {
	s.versionPurges++
	return s.Memory.Delete(ctx, key)
}

type prefixTrackingStore struct {
	*Memory
	prefix string
}

func (s *prefixTrackingStore) DeletePrefixAllVersions(ctx context.Context, prefix string) error {
	s.prefix = prefix
	return s.Memory.DeletePrefixAllVersions(ctx, prefix)
}

type plainStore struct {
	memory *Memory
}

func (s *plainStore) Put(ctx context.Context, key string, body []byte, contentType string) error {
	return s.memory.Put(ctx, key, body, contentType)
}
func (s *plainStore) Get(ctx context.Context, key string) ([]byte, error) {
	return s.memory.Get(ctx, key)
}
func (s *plainStore) Delete(ctx context.Context, key string) error {
	return s.memory.Delete(ctx, key)
}
func (s *plainStore) Exists(ctx context.Context, key string) (bool, error) {
	return s.memory.Exists(ctx, key)
}
func (s *plainStore) List(ctx context.Context, prefix string) ([]string, error) {
	return s.memory.List(ctx, prefix)
}

type faultStore struct {
	keys      []string
	listErr   error
	deleteErr error
}

func (s *faultStore) Put(context.Context, string, []byte, string) error { return nil }
func (s *faultStore) Get(context.Context, string) ([]byte, error)       { return nil, ErrNotFound }
func (s *faultStore) Delete(context.Context, string) error              { return s.deleteErr }
func (s *faultStore) Exists(context.Context, string) (bool, error)      { return false, nil }
func (s *faultStore) List(context.Context, string) ([]string, error) {
	return append([]string(nil), s.keys...), s.listErr
}

func TestValidateConfigProductionRequiresTLSAndCredentials(t *testing.T) {
	valid := Config{
		Endpoint:  "https://s3.fr-par.scw.cloud",
		Bucket:    "caveman-prod",
		AccessKey: "access-key",
		SecretKey: "secret-key",
		UseSSL:    true,
	}
	if err := validateConfig(valid, true); err != nil {
		t.Fatalf("valid production object-store config rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "plaintext endpoint", mutate: func(c *Config) { c.Endpoint = "http://s3.example.com"; c.UseSSL = false }},
		{name: "TLS flag disabled", mutate: func(c *Config) { c.Endpoint = "s3.example.com"; c.UseSSL = false }},
		{name: "credentials in URL", mutate: func(c *Config) { c.Endpoint = "https://user:pass@s3.example.com" }},
		{name: "endpoint path", mutate: func(c *Config) { c.Endpoint = "https://s3.example.com/private" }},
		{name: "missing bucket", mutate: func(c *Config) { c.Bucket = "" }},
		{name: "missing access key", mutate: func(c *Config) { c.AccessKey = "" }},
		{name: "missing secret key", mutate: func(c *Config) { c.SecretKey = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid
			tc.mutate(&cfg)
			if err := validateConfig(cfg, true); err == nil {
				t.Fatal("unsafe production object-store config accepted")
			}
		})
	}
}

func TestValidateConfigLocalAllowsExplicitPlaintextMinIO(t *testing.T) {
	cfg := Config{Endpoint: "http://minio:9000", Bucket: "local", AccessKey: "minio", SecretKey: "minio", UseSSL: false}
	if err := validateConfig(cfg, false); err != nil {
		t.Fatalf("local MinIO config rejected: %v", err)
	}
}

func TestFromEnvProductionDoesNotDefaultBucket(t *testing.T) {
	t.Setenv("CAVE_ENV", "prod")
	t.Setenv("S3_ENDPOINT", "https://s3.fr-par.scw.cloud")
	t.Setenv("S3_BUCKET", "")
	t.Setenv("S3_ACCESS_KEY", "access-key")
	t.Setenv("S3_SECRET_KEY", "secret-key")
	if _, err := FromEnv(); err == nil {
		t.Fatal("production object store accepted missing S3_BUCKET")
	}
}

func TestFromEnvProductionRequiresObjectStore(t *testing.T) {
	t.Setenv("CAVE_ENV", "prod")
	t.Setenv("S3_ENDPOINT", "")
	if store, err := FromEnv(); err == nil || store != nil {
		t.Fatalf("production object store accepted missing endpoint: store=%v err=%v", store, err)
	}
}

func TestFromEnvLocalDisabledAndConfigured(t *testing.T) {
	t.Setenv("CAVE_ENV", "local")
	t.Setenv("S3_ENDPOINT", "")
	if store, err := FromEnv(); err != nil || store != nil {
		t.Fatalf("disabled local object store = %v, %v", store, err)
	}

	t.Setenv("S3_ENDPOINT", "http://127.0.0.1:9000")
	t.Setenv("S3_BUCKET", "")
	t.Setenv("S3_ACCESS_KEY", "")
	t.Setenv("S3_SECRET_KEY", "")
	t.Setenv("S3_ACCESS_KEY_ID", "fallback-access")
	t.Setenv("S3_SECRET_ACCESS_KEY", "fallback-secret")
	store, err := FromEnv()
	if err != nil || store == nil {
		t.Fatalf("configured local object store = %v, %v", store, err)
	}
}

func TestNewConstructsClientWithoutNetwork(t *testing.T) {
	store, err := New(Config{
		Endpoint:  "https://s3.example.test",
		Bucket:    "bucket",
		AccessKey: "access",
		SecretKey: "secret",
		Region:    "fr-par",
		UseSSL:    true,
	})
	if err != nil || store == nil {
		t.Fatalf("New = %v, %v", store, err)
	}
}

// TestLiveMinioContract runs the same contract against a real MinIO when
// S3_ENDPOINT is set (skipped otherwise).
func TestLiveMinioContract(t *testing.T) {
	if os.Getenv("S3_ENDPOINT") == "" {
		t.Skip("S3_ENDPOINT not set; skipping live MinIO test")
	}
	s, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if s == nil {
		t.Fatal("S3_ENDPOINT is set but object store is not configured")
	}
	if _, err := s.List(context.Background(), "__liveprobe__/"); err != nil {
		t.Fatalf("configured object store is unreachable: %v", err)
	}
	runStoreContract(t, s)

	ctx := context.Background()
	versionedKey := "capture/live-version-contract/payload"
	if err := s.Put(ctx, versionedKey, []byte("v1"), "application/octet-stream"); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, versionedKey, []byte("v2"), "application/octet-stream"); err != nil {
		t.Fatal(err)
	}
	if err := PurgeObject(ctx, s, versionedKey); err != nil {
		t.Fatalf("version purge: %v", err)
	}
	if exists, err := s.Exists(ctx, versionedKey); err != nil || exists {
		t.Fatalf("version purge left current object: exists=%t err=%v", exists, err)
	}
	if err := Probe(ctx, s); err != nil {
		t.Fatalf("read/write/version-purge probe: %v", err)
	}
}
