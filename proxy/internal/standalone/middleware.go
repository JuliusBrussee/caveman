package standalone

import (
	"net/http"

	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/proxy/internal/config"
	"github.com/JuliusBrussee/caveman/proxy/internal/middleware"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

// NewMiddleware shares the listener's operator authority and record-mode gate.
// A shared bearer means shared authority, not independent tenant identities.
// Embedders needing separate principals use middleware.Config.Principal instead.
func NewMiddleware(cfg config.Config, state *store.Store, recovery *ccr.Store, build string) (*middleware.Runtime, error) {
	auth := Auth{token: cfg.AuthToken}
	mode := cfg.Mode
	if mode == "pixel" {
		mode = "compress"
	}
	return middleware.New(middleware.Config{
		Store: state, Recovery: recovery, Build: build, Mode: mode, TrustMode: "single_operator",
		Principal: func(r *http.Request) (string, error) {
			if _, err := auth.Authenticate(r.Context(), r); err != nil {
				return "", err
			}
			return "single_operator", nil
		},
	})
}
