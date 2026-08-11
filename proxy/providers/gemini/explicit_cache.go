package gemini

import (
	"context"
	"io"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/shared/platform/optimizers"
)

// OptimizerID is retained only as the historical join key for the retired
// runtime scaffold. It is not a current policy flag or optimization label.
const OptimizerID = optimizers.GeminiExplicitCacheOptimizerID

// Adapter embeds the shared Base and preserves the retired Gemini explicit-cache
// entry point as an unconditional byte-identical pass-through.
type Adapter struct {
	providers.Base
}

// ApplyProviderNativeTransforms is an unconditional byte-safe pass-through.
//
// Why it cannot transform: legacy generateContent explicit caching requires a
// real cachedContents resource created before inference. Its creation charge,
// token-hour storage, TTL, deletion, credential project, model, and (on Vertex)
// location all belong to one lifecycle. This gateway has no such lifecycle or
// complete accounting contract. Gemini Interactions is a separate API and
// supports implicit caching only.
//
// The old policy/practice/experiment surfaces are retired. Keep this method so
// old callers and persisted configuration remain harmless: it makes no change
// and emits no optimizer id under every policy and runtime mode.
func (a Adapter) ApplyProviderNativeTransforms(ctx context.Context, body providers.BodyReader, meta providers.RequestMetadata, policy providers.TransformPolicy) (providers.TransformResult, error) {
	data, _ := io.ReadAll(body)
	// Historical no-op: never emit OptimizerID or mutate model-visible bytes.
	return providers.TransformResult{Body: data, OptimizerIDs: []string{}}, nil
}
