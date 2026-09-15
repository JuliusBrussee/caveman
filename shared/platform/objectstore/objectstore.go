// Package objectstore is a thin blob store over S3-compatible backends (MinIO
// locally, S3 in AWS). It stores opaque bytes only — all artifact metadata
// (sha256, compression, envelope encryption metadata, size, expiry) lives in the
// Postgres `artifacts` table, so the object store never needs user-metadata.
//
// Bodies written here are already compressed and envelope-encrypted by the
// caller; the store treats them as opaque. The bucket is private (no public
// ACL) per the deployment's bucket policy.
package objectstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/shared/platform/env"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ErrNotFound is returned by Get/Delete when the key does not exist.
var ErrNotFound = errors.New("objectstore: object not found")

// ErrTooLarge is returned when a remote object exceeds the configured in-memory
// read ceiling. Artifact/capture payloads are bounded on write; enforcing a
// read ceiling too prevents a corrupted or attacker-written bucket object from
// exhausting a service process.
var ErrTooLarge = errors.New("objectstore: object exceeds read limit")

// ErrPagingUnsupported is returned when a caller that must bound listing
// memory is given a backend without a cursor-capable implementation.
var ErrPagingUnsupported = errors.New("objectstore: paged listing unsupported")

// Store is the blob backend contract.
type Store interface {
	// Put writes body at key, overwriting any existing object.
	Put(ctx context.Context, key string, body []byte, contentType string) error
	// Get returns the bytes at key, or ErrNotFound.
	Get(ctx context.Context, key string) ([]byte, error)
	// Delete removes key (no error if already absent).
	Delete(ctx context.Context, key string) error
	// Exists reports whether key is present.
	Exists(ctx context.Context, key string) (bool, error)
	// List returns all keys under prefix (used by retention + ZDR assertions).
	List(ctx context.Context, prefix string) ([]string, error)
}

// ObjectPage is one bounded, lexicographically ordered page of keys. NextToken
// is safe to persist as a cursor; it is empty when the prefix is exhausted.
type ObjectPage struct {
	Keys      []string
	NextToken string
	Truncated bool
}

// PagedLister is implemented by production backends and bounded test stores.
// A retention sweep must use this interface rather than materializing an
// unbounded Store.List result.
type PagedLister interface {
	ListPage(ctx context.Context, prefix, startAfter string, maxKeys int) (ObjectPage, error)
}

// ListPage dispatches to a cursor-capable backend. It intentionally does not
// fall back to Store.List: callers use it specifically to preserve a memory
// bound when a tenant prefix is large.
func ListPage(ctx context.Context, store Store, prefix, startAfter string, maxKeys int) (ObjectPage, error) {
	if maxKeys <= 0 {
		return ObjectPage{}, fmt.Errorf("objectstore: page size must be positive")
	}
	lister, ok := store.(PagedLister)
	if !ok {
		return ObjectPage{}, ErrPagingUnsupported
	}
	return lister.ListPage(ctx, prefix, startAfter, maxKeys)
}

// VersionPurger is implemented by version-aware backends. Explicit privacy
// deletion uses it to remove current objects, noncurrent versions, and delete
// markers immediately instead of waiting for lifecycle expiration.
type VersionPurger interface {
	DeleteAllVersions(ctx context.Context, key string) error
	DeletePrefixAllVersions(ctx context.Context, prefix string) error
}

// PurgeObject removes every version when supported, falling back to ordinary
// deletion only for non-versioned test/self-hosted backends.
func PurgeObject(ctx context.Context, store Store, key string) error {
	if purger, ok := store.(VersionPurger); ok {
		return purger.DeleteAllVersions(ctx, key)
	}
	return store.Delete(ctx, key)
}

// PurgePrefix removes every object version under a tenant prefix. Production S3
// backends must implement VersionPurger; fallback supports non-versioned stores.
func PurgePrefix(ctx context.Context, store Store, prefix string) error {
	if purger, ok := store.(VersionPurger); ok {
		return purger.DeletePrefixAllVersions(ctx, prefix)
	}
	keys, err := store.List(ctx, prefix)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if err := store.Delete(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

// minioStore is the S3/MinIO-backed Store.
type minioStore struct {
	client *minio.Client
	bucket string
}

// Config configures FromEnv / New.
type Config struct {
	Endpoint  string // host:port, no scheme
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string
	UseSSL    bool
}

// FromEnv builds a Store from the S3_* environment variables. It returns
// (nil, nil) when S3_ENDPOINT is unset outside production. Production requires
// object storage because retention, replay, and artifact offload must not
// silently disable themselves after launch.
//
// Credential vars accept both the repo's names (S3_ACCESS_KEY/S3_SECRET_KEY)
// and the AWS-conventional names (S3_ACCESS_KEY_ID/S3_SECRET_ACCESS_KEY). TLS is
// inferred from the endpoint scheme (https) unless S3_USE_SSL forces it.
func FromEnv() (Store, error) {
	endpoint := env.String("S3_ENDPOINT", "")
	if endpoint == "" {
		if env.IsProduction() {
			return nil, errors.New("objectstore: S3_ENDPOINT is required in production")
		}
		return nil, nil
	}
	production := env.IsProduction()
	bucketFallback := "caveman-local"
	if production {
		bucketFallback = ""
	}
	access := env.String("S3_ACCESS_KEY", env.String("S3_ACCESS_KEY_ID", ""))
	secret := env.String("S3_SECRET_KEY", env.String("S3_SECRET_ACCESS_KEY", ""))
	useSSL := strings.HasPrefix(endpoint, "https://") || env.Bool("S3_USE_SSL", false)
	cfg := Config{
		Endpoint:  endpoint,
		Bucket:    env.String("S3_BUCKET", bucketFallback),
		AccessKey: access,
		SecretKey: secret,
		Region:    env.String("S3_REGION", "us-east-1"),
		UseSSL:    useSSL,
	}
	if err := validateConfig(cfg, production); err != nil {
		return nil, err
	}
	store, err := New(cfg)
	if err != nil {
		return nil, err
	}
	if production {
		timeout := time.Duration(env.Int("CAVE_OBJECTSTORE_PROBE_TIMEOUT_MS", 10000)) * time.Millisecond
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := Probe(ctx, store); err != nil {
			return nil, fmt.Errorf("objectstore: production read/write/delete probe failed: %w", err)
		}
	}
	return store, nil
}

func validateConfig(cfg Config, production bool) error {
	if strings.TrimSpace(cfg.Endpoint) == "" || strings.TrimSpace(cfg.Bucket) == "" {
		return errors.New("objectstore: endpoint and bucket are required")
	}
	if strings.TrimSpace(cfg.AccessKey) == "" || strings.TrimSpace(cfg.SecretKey) == "" {
		return errors.New("objectstore: access key and secret key are required")
	}
	if !production {
		return nil
	}
	if !cfg.UseSSL || strings.HasPrefix(strings.ToLower(strings.TrimSpace(cfg.Endpoint)), "http://") {
		return errors.New("objectstore: production requires TLS")
	}
	raw := strings.TrimSpace(cfg.Endpoint)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("objectstore: production endpoint must be an HTTPS origin without credentials, path, query, or fragment")
	}
	return nil
}

// New constructs a MinIO/S3-backed Store. FromEnv adds a live production probe.
func New(cfg Config) (Store, error) {
	endpoint := strings.TrimPrefix(strings.TrimPrefix(cfg.Endpoint, "https://"), "http://")
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("objectstore: minio client: %w", err)
	}
	return &minioStore{client: client, bucket: cfg.Bucket}, nil
}

// Probe proves the operations production retention needs instead of accepting a
// syntactically valid but unusable bucket configuration. The random probe body
// contains no tenant data and every version is removed before success returns.
func Probe(ctx context.Context, store Store) error {
	if store == nil {
		return errors.New("store is nil")
	}
	probe := make([]byte, 32)
	if _, err := rand.Read(probe); err != nil {
		return fmt.Errorf("generate probe: %w", err)
	}
	key := "_cave_health/" + base64.RawURLEncoding.EncodeToString(probe)
	if err := store.Put(ctx, key, probe, "application/octet-stream"); err != nil {
		return err
	}
	cleaned := false
	defer func() {
		if !cleaned {
			// Keep health checks within their caller deadline. A failed probe may
			// leave one random, tenant-free canary for lifecycle cleanup; readiness
			// must never hang on an unbounded background delete.
			_ = PurgeObject(ctx, store, key)
		}
	}()
	got, err := store.Get(ctx, key)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, probe) {
		return errors.New("probe bytes did not round-trip")
	}
	if err := PurgeObject(ctx, store, key); err != nil {
		return err
	}
	cleaned = true
	exists, err := store.Exists(ctx, key)
	if err != nil {
		return err
	}
	if exists {
		return errors.New("probe object still exists after purge")
	}
	return nil
}

func (m *minioStore) Put(ctx context.Context, key string, body []byte, contentType string) error {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	_, err := m.client.PutObject(ctx, m.bucket, key, bytes.NewReader(body), int64(len(body)),
		minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("objectstore: put %q: %w", key, err)
	}
	return nil
}

func (m *minioStore) Get(ctx context.Context, key string) ([]byte, error) {
	obj, err := m.client.GetObject(ctx, m.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("objectstore: get %q: %w", key, err)
	}
	defer obj.Close()
	maxBytes := int64(env.Int("CAVE_OBJECTSTORE_MAX_READ_BYTES", 64<<20))
	if maxBytes <= 0 {
		maxBytes = 64 << 20
	}
	body, err := io.ReadAll(io.LimitReader(obj, maxBytes+1))
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("objectstore: read %q: %w", key, err)
	}
	if int64(len(body)) > maxBytes {
		return nil, ErrTooLarge
	}
	return body, nil
}

func (m *minioStore) Delete(ctx context.Context, key string) error {
	if err := m.client.RemoveObject(ctx, m.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("objectstore: delete %q: %w", key, err)
	}
	return nil
}

func (m *minioStore) DeleteAllVersions(ctx context.Context, key string) error {
	return m.deleteVersions(ctx, key, true)
}

func (m *minioStore) DeletePrefixAllVersions(ctx context.Context, prefix string) error {
	return m.deleteVersions(ctx, prefix, false)
}

func (m *minioStore) deleteVersions(ctx context.Context, keyOrPrefix string, exact bool) error {
	found := false
	for obj := range m.client.ListObjects(ctx, m.bucket, minio.ListObjectsOptions{
		Prefix: keyOrPrefix, Recursive: true, WithVersions: true,
	}) {
		if obj.Err != nil {
			return fmt.Errorf("objectstore: list versions %q: %w", keyOrPrefix, obj.Err)
		}
		if exact && obj.Key != keyOrPrefix {
			continue
		}
		found = true
		if err := m.client.RemoveObject(ctx, m.bucket, obj.Key, minio.RemoveObjectOptions{VersionID: obj.VersionID}); err != nil {
			return fmt.Errorf("objectstore: delete version %q (%q): %w", obj.Key, obj.VersionID, err)
		}
	}
	if exact && !found {
		// Non-versioned/S3-compatible backends may omit version listings. Ordinary
		// deletion remains idempotent and covers that deployment shape.
		return m.Delete(ctx, keyOrPrefix)
	}
	return nil
}

func (m *minioStore) Exists(ctx context.Context, key string) (bool, error) {
	_, err := m.client.StatObject(ctx, m.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return false, nil
		}
		return false, fmt.Errorf("objectstore: stat %q: %w", key, err)
	}
	return true, nil
}

func (m *minioStore) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	for obj := range m.client.ListObjects(ctx, m.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("objectstore: list %q: %w", prefix, obj.Err)
		}
		keys = append(keys, obj.Key)
	}
	return keys, nil
}

func (m *minioStore) ListPage(ctx context.Context, prefix, startAfter string, maxKeys int) (ObjectPage, error) {
	if maxKeys <= 0 {
		return ObjectPage{}, fmt.Errorf("objectstore: page size must be positive")
	}
	// Request one extra key so the caller can tell whether another page exists,
	// while cancelling the producer as soon as the bounded result is known.
	pageCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	keys := make([]string, 0, maxKeys)
	var extra string
	for obj := range m.client.ListObjects(pageCtx, m.bucket, minio.ListObjectsOptions{
		Prefix: prefix, Recursive: true, StartAfter: startAfter, MaxKeys: maxKeys + 1,
	}) {
		if obj.Err != nil {
			return ObjectPage{}, fmt.Errorf("objectstore: list page %q: %w", prefix, obj.Err)
		}
		if len(keys) < maxKeys {
			keys = append(keys, obj.Key)
		} else {
			extra = obj.Key
			break
		}
	}
	if extra == "" {
		return ObjectPage{Keys: keys}, nil
	}
	return ObjectPage{Keys: keys, NextToken: keys[len(keys)-1], Truncated: true}, nil
}
