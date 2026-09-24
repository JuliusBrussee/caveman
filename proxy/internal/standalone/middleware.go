package standalone

import (
	"cmp"
	"log/slog"
	"time"

	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/proxy/internal/config"
	"github.com/JuliusBrussee/caveman/proxy/internal/identity"
	"github.com/JuliusBrussee/caveman/proxy/internal/middleware"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

// NewIdentity builds the middleware routes' identity resolver from config: the
// shared token (single_operator), the token map, OIDC and, when the TLS
// listener verifies client certificates, mTLS.
func NewIdentity(cfg config.Config, logger *slog.Logger) (*identity.Resolver, error) {
	o := cfg.Middleware.OIDC
	return identity.New(identity.Config{
		Token: cfg.AuthToken, TokenMapFile: cfg.Middleware.TokenMapFile, MTLS: cfg.TLS.ClientCAFile != "", Logger: logger,
		OIDC: identity.OIDC{Issuer: o.Issuer, Audience: o.Audience, JWKSURL: o.JWKSURL, Algorithms: o.Algorithms,
			ClockSkew: time.Duration(o.ClockSkewSeconds) * time.Second, PrincipalClaim: o.PrincipalClaim, NamespacesClaim: o.NamespacesClaim},
	})
}

// NewMiddleware builds the framework middleware runtime over state (SQLite or
// Postgres), resolving callers through ids. Its mode is middleware.mode, else the
// proxy's mode. recovery (CCR) is only read, for grants an older runtime issued;
// it may be nil.
func NewMiddleware(cfg config.Config, state store.MiddlewareStore, recovery *ccr.Store, build string, logger *slog.Logger, ids *identity.Resolver) (*middleware.Runtime, error) {
	m := cfg.Middleware
	mode := cmp.Or(m.Mode, cfg.Mode)
	if mode == "pixel" {
		mode = "compress"
	}
	trust := "single_operator"
	if ids.MultiPrincipal() {
		trust = "resolver"
	}
	// A configured but unreadable key fails closed: never store plaintext the
	// operator asked to have encrypted.
	keys, err := middleware.LoadKeyring(m.EncryptionKey, m.EncryptionKeyFile)
	if err != nil {
		return nil, err
	}
	return middleware.New(middleware.Config{
		Store: state, Recovery: recovery, Build: build, Mode: mode, TrustMode: trust, Keys: keys, Logger: logger, Ephemeral: m.Ephemeral,
		Retention: time.Duration(m.RetentionSeconds) * time.Second, MaxRetention: time.Duration(m.MaxRetentionSeconds) * time.Second,
		Limits: middleware.Limits{DeadlineMS: m.DeadlineMS, RetrieveDeadlineMS: m.RetrieveDeadlineMS, QueueDepth: m.QueueDepth,
			RetrieveQueueDepth: m.RetrieveQueueDepth, RequestBytes: m.RequestBytes, SegmentBytes: m.SegmentBytes, PageBytes: m.PageBytes,
			MaxSegments: m.MaxSegments, MaxManifestItems: m.MaxManifestItems, ReceiptBytes: m.ReceiptBytes, QuotaRequestsPerMinute: m.QuotaRequestsPerMinute},
		Capacity: store.MiddlewareLimits{Rows: m.MaxRows, Bytes: m.MaxBytes, PrincipalRows: m.QuotaRows, PrincipalBytes: m.QuotaBytes},
		Identify: ids.Identify,
	})
}
