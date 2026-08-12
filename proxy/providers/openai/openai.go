package openai

import (
	"context"
	"net/http"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func New(baseURL string) providers.Adapter {
	return Adapter{Base: providers.Base{Provider: "openai", BaseURL: baseURL, Routes: []string{"/v1/chat/completions", "/v1/responses", "/v1/embeddings", "/openai/v1/chat/completions", "/openai/v1/responses", "/openai/v1/embeddings"}}}
}

func (a Adapter) InspectRequest(ctx context.Context, body providers.BodyReader, headers http.Header) (providers.RequestMetadata, error) {
	meta, err := a.Base.InspectRequest(ctx, body, headers)
	if path := headers.Get("x-cave-route-path"); path != "" {
		meta.Endpoint = path
	}
	return meta, err
}
