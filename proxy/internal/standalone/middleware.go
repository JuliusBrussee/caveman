package standalone

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/proxy/internal/config"
	"github.com/JuliusBrussee/caveman/proxy/internal/middleware"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

// NewMiddleware shares the listener's operator authority and record-mode gate.
// A shared bearer means shared authority, not independent tenant identities.
// Embedders needing separate principals use middleware.Config.Principal instead.
// recovery (CCR) is only read, for grants an older runtime issued; it may be nil.
func NewMiddleware(cfg config.Config, state *store.Store, recovery *ccr.Store, build string, logger *slog.Logger) (*middleware.Runtime, error) {
	auth := Auth{token: cfg.AuthToken}
	mode := cfg.Mode
	if mode == "pixel" {
		mode = "compress"
	}
	m := cfg.Middleware
	// A configured but unreadable key fails closed: never store plaintext the
	// operator asked to have encrypted.
	keys, err := middleware.LoadKeyring(m.EncryptionKey, m.EncryptionKeyFile)
	if err != nil {
		return nil, err
	}
	return middleware.New(middleware.Config{
		Store: state, Recovery: recovery, Build: build, Mode: mode, TrustMode: "single_operator", Keys: keys, Logger: logger,
		Retention: time.Duration(m.RetentionSeconds) * time.Second, MaxRetention: time.Duration(m.MaxRetentionSeconds) * time.Second,
		Limits: middleware.Limits{DeadlineMS: m.DeadlineMS, RetrieveDeadlineMS: m.RetrieveDeadlineMS, QueueDepth: m.QueueDepth,
			RetrieveQueueDepth: m.RetrieveQueueDepth, RequestBytes: m.RequestBytes, SegmentBytes: m.SegmentBytes, PageBytes: m.PageBytes,
			MaxSegments: m.MaxSegments, MaxManifestItems: m.MaxManifestItems, ReceiptBytes: m.ReceiptBytes, QuotaRequestsPerMinute: m.QuotaRequestsPerMinute},
		Capacity: store.MiddlewareLimits{Rows: m.MaxRows, Bytes: m.MaxBytes, PrincipalRows: m.QuotaRows, PrincipalBytes: m.QuotaBytes},
		Principal: func(r *http.Request) (string, error) {
			if _, err := auth.Authenticate(r.Context(), r); err != nil {
				return "", err
			}
			return "single_operator", nil
		},
	})
}
