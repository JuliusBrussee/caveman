package azureopenai

import (
	"context"
	"net/http"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

// Adapter embeds Base and adds Azure-specific request validation. Legacy paths
// must name a deployment and use the configured dated api-version allowlist;
// current Foundry `/openai/v1` inference paths accept only the documented v1 or
// preview selector (or its documented omitted-v1 default).
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

func (a Adapter) ExtractCompressible(body []byte, meta providers.RequestMetadata) ([][]byte, func([][]byte) ([]byte, error), bool) {
	return openai.ExtractCompressible(body, meta)
}

func (a Adapter) ExtractStabilizable(body []byte, meta providers.RequestMetadata) ([]providers.RewritableBlock, func([][]byte) ([]byte, error), bool) {
	return openai.ExtractStabilizable(body, meta)
}

func (a Adapter) ApplyProviderNativeTransforms(ctx context.Context, body providers.BodyReader, meta providers.RequestMetadata, policy providers.TransformPolicy) (providers.TransformResult, error) {
	return openai.Adapter{Base: a.Base}.ApplyProviderNativeTransforms(ctx, body, meta, policy)
}

func (a Adapter) PlanCacheBreakpoints(body []byte, meta providers.RequestMetadata, payg bool) ([]byte, bool) {
	return openai.Adapter{Base: a.Base}.PlanCacheBreakpoints(body, meta, payg)
}

func (a Adapter) CachedPrefixComponents(body []byte, meta providers.RequestMetadata) ([][]byte, int, bool) {
	return openai.Adapter{Base: a.Base}.CachedPrefixComponents(body, meta)
}

func New(baseURL string) providers.Adapter {
	return Adapter{Base: providers.Base{
		Provider: "azure_openai",
		BaseURL:  baseURL,
		Routes:   []string{"/azure/"},
	}}
}
