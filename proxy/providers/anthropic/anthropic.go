package anthropic

import (
	"context"
	"net/http"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// Adapter embeds the shared Base and overrides ApplyProviderNativeTransforms for
// explicit breakpoints and the separate experimental automatic-cache marker.
type Adapter struct {
	providers.Base
}

func (a Adapter) InspectRequest(ctx context.Context, body providers.BodyReader, headers http.Header) (providers.RequestMetadata, error) {
	meta, err := a.Base.InspectRequest(ctx, body, headers)
	if path := headers.Get("x-cave-route-path"); path != "" {
		meta.Endpoint = path
	}
	return meta, err
}

func New(baseURL string) providers.Adapter {
	return Adapter{Base: providers.Base{
		Provider: "anthropic",
		BaseURL:  baseURL,
		// Both the gateway-prefixed routes and the bare Anthropic routes are
		// accepted, mirroring the OpenAI adapter. The bare routes let an agent
		// (e.g. Claude Code) point ANTHROPIC_BASE_URL straight at the standalone
		// proxy unprefixed; ResolveUpstreamURL has no /anthropic prefix to trim, so
		// it forwards to {base}/v1/messages unchanged.
		Routes: []string{"/anthropic/v1/messages", "/anthropic/v1/messages/count_tokens", "/v1/messages", "/v1/messages/count_tokens"},
		// GET /v1/models and /v1/models/{id} are part of the Anthropic wire
		// protocol, so the bare and prefixed mounts carry them for the same
		// reason the inference routes are doubled up.
		MetadataRoutes: []string{"/anthropic/v1/models", "/v1/models"},
	}}
}

// MatchMetadataRequest claims /anthropic/v1/models always, but the bare
// /v1/models spelling only from Anthropic wire-protocol callers: every
// Anthropic SDK and Claude Code send anthropic-version, and a caller that
// omits it (curl, scripts) still sends its key in x-api-key, which OpenAI
// clients never do. The bare spelling is shared with the OpenAI adapter
// (issue #1187).
func (a Adapter) MatchMetadataRequest(r *http.Request) bool {
	return a.MatchMetadataRoute(r.Method, r.URL.Path) &&
		(strings.HasPrefix(r.URL.Path, "/anthropic/") ||
			r.Header.Get("anthropic-version") != "" || r.Header.Get("x-api-key") != "")
}
