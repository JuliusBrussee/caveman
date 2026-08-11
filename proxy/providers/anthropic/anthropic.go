package anthropic

import (
	"context"
	"net/http"

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
	}}
}
