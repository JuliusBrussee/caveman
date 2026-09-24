package config

import (
	"os"
	"strconv"
	"strings"
)

// MiddlewareConfig is the `middleware:` block of caveman.yaml. Every key also
// reads CAVEMAN_MIDDLEWARE_<KEY> (upper case), which wins over the file. Zero or
// absent means the runtime default (docs/technical/middleware-protocol.md §13).
// The schema-bound limits (segment_bytes, page_bytes, max_segments,
// max_manifest_items) can only be lowered.
type MiddlewareConfig struct {
	RetentionSeconds       int64 `yaml:"retention_seconds"`     // default 86400
	MaxRetentionSeconds    int64 `yaml:"max_retention_seconds"` // default 604800; raised to retention if lower
	DeadlineMS             int64 `yaml:"deadline_ms"`           // default 500
	RetrieveDeadlineMS     int64 `yaml:"retrieve_deadline_ms"`  // default 5000
	QueueDepth             int   `yaml:"queue_depth"`           // default 16
	RetrieveQueueDepth     int   `yaml:"retrieve_queue_depth"`  // default 16
	RequestBytes           int   `yaml:"request_bytes"`         // default 2 MiB
	SegmentBytes           int   `yaml:"segment_bytes"`         // default 512 KiB
	PageBytes              int   `yaml:"page_bytes"`            // default 256 KiB
	MaxSegments            int   `yaml:"max_segments"`          // default 256
	MaxManifestItems       int   `yaml:"max_manifest_items"`    // default 4096
	ReceiptBytes           int   `yaml:"receipt_bytes"`         // default 16 KiB
	QuotaRequestsPerMinute int   `yaml:"quota_requests_per_minute"`
	// Storage admission for the whole middleware store, and per principal.
	MaxRows    int64 `yaml:"max_rows"`    // default 1,000,000
	MaxBytes   int64 `yaml:"max_bytes"`   // default 576 MiB
	QuotaRows  int64 `yaml:"quota_rows"`  // per principal; 0 = global limit only
	QuotaBytes int64 `yaml:"quota_bytes"` // per principal; 0 = global limit only
	// EncryptionKeyFile holds base64 32-byte keys, one per line, the first one
	// sealing. EncryptionKey is the same list inline, comma separated, and is
	// read only from CAVEMAN_MIDDLEWARE_ENCRYPTION_KEY: secrets never live in
	// caveman.yaml. Neither set stores originals in plaintext.
	EncryptionKeyFile string `yaml:"encryption_key_file"`
	EncryptionKey     string `yaml:"-" json:"-"`
	// Mode is the middleware's runtime mode: record or compress. Empty follows
	// the proxy's mode (CAVEMAN_MODE), so the proxy can record while the
	// middleware compresses.
	Mode string `yaml:"mode"`
	// Ephemeral declares the store lost on restart (emptyDir, no volume):
	// capabilities then report persistent:false and nothing is compressed.
	Ephemeral bool `yaml:"ephemeral"`
	// DatabaseURL selects the Postgres store shared by every replica instead of
	// the local SQLite file. Read only from CAVEMAN_MIDDLEWARE_DATABASE_URL: it
	// carries a password. DatabaseURLYAML only catches the key in caveman.yaml.
	DatabaseURL     string `yaml:"-" json:"-"`
	DatabaseURLYAML string `yaml:"database_url" json:"-"`
	// TokenMapFile maps token hashes to principals, namespaces and quotas (see
	// package identity). Reloaded on change or SIGHUP.
	TokenMapFile string     `yaml:"token_map_file"`
	OIDC         OIDCConfig `yaml:"oidc"`
}

// OIDCConfig is `middleware.oidc`; CAVEMAN_MIDDLEWARE_OIDC_<KEY> overrides each
// key (ALGORITHMS comma separated). Issuer set turns it on.
type OIDCConfig struct {
	Issuer           string   `yaml:"issuer"`
	Audience         string   `yaml:"audience"`
	JWKSURL          string   `yaml:"jwks_url"`
	Algorithms       []string `yaml:"algorithms"`         // RS256 and/or ES256; default both
	ClockSkewSeconds int64    `yaml:"clock_skew_seconds"` // default 60
	PrincipalClaim   string   `yaml:"principal_claim"`    // default sub
	NamespacesClaim  string   `yaml:"namespaces_claim"`   // optional
}

// withEnv applies CAVEMAN_MIDDLEWARE_* overrides. An unparseable number keeps
// the file value, matching env.Int.
func (m MiddlewareConfig) withEnv() MiddlewareConfig {
	for name, field := range map[string]*int64{
		"RETENTION_SECONDS": &m.RetentionSeconds, "MAX_RETENTION_SECONDS": &m.MaxRetentionSeconds,
		"DEADLINE_MS": &m.DeadlineMS, "RETRIEVE_DEADLINE_MS": &m.RetrieveDeadlineMS,
		"MAX_ROWS": &m.MaxRows, "MAX_BYTES": &m.MaxBytes, "QUOTA_ROWS": &m.QuotaRows, "QUOTA_BYTES": &m.QuotaBytes,
		"OIDC_CLOCK_SKEW_SECONDS": &m.OIDC.ClockSkewSeconds,
	} {
		if v, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("CAVEMAN_MIDDLEWARE_"+name)), 10, 64); err == nil {
			*field = v
		}
	}
	for name, field := range map[string]*int{
		"QUEUE_DEPTH": &m.QueueDepth, "RETRIEVE_QUEUE_DEPTH": &m.RetrieveQueueDepth, "REQUEST_BYTES": &m.RequestBytes,
		"SEGMENT_BYTES": &m.SegmentBytes, "PAGE_BYTES": &m.PageBytes, "MAX_SEGMENTS": &m.MaxSegments,
		"MAX_MANIFEST_ITEMS": &m.MaxManifestItems, "RECEIPT_BYTES": &m.ReceiptBytes, "QUOTA_REQUESTS_PER_MINUTE": &m.QuotaRequestsPerMinute,
	} {
		if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CAVEMAN_MIDDLEWARE_" + name))); err == nil {
			*field = v
		}
	}
	if path := strings.TrimSpace(os.Getenv("CAVEMAN_MIDDLEWARE_ENCRYPTION_KEY_FILE")); path != "" {
		m.EncryptionKeyFile = path
	}
	// Assigned unconditionally: the environment is the only source.
	m.EncryptionKey = strings.TrimSpace(os.Getenv("CAVEMAN_MIDDLEWARE_ENCRYPTION_KEY"))
	m.DatabaseURL = strings.TrimSpace(os.Getenv("CAVEMAN_MIDDLEWARE_DATABASE_URL"))
	for name, field := range map[string]*string{
		"MODE": &m.Mode, "TOKEN_MAP_FILE": &m.TokenMapFile, "OIDC_ISSUER": &m.OIDC.Issuer, "OIDC_AUDIENCE": &m.OIDC.Audience,
		"OIDC_JWKS_URL": &m.OIDC.JWKSURL, "OIDC_PRINCIPAL_CLAIM": &m.OIDC.PrincipalClaim, "OIDC_NAMESPACES_CLAIM": &m.OIDC.NamespacesClaim,
	} {
		if value := strings.TrimSpace(os.Getenv("CAVEMAN_MIDDLEWARE_" + name)); value != "" {
			*field = value
		}
	}
	if value := strings.TrimSpace(os.Getenv("CAVEMAN_MIDDLEWARE_OIDC_ALGORITHMS")); value != "" {
		m.OIDC.Algorithms = strings.Split(strings.ReplaceAll(value, " ", ""), ",")
	}
	if value, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("CAVEMAN_MIDDLEWARE_EPHEMERAL"))); err == nil {
		m.Ephemeral = value
	}
	return m
}
