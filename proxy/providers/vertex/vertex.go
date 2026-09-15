package vertex

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// Adapter is the Google Vertex AI gateway adapter. Like Bedrock, Vertex fronts
// multiple model families behind one cloud endpoint: Google Gemini
// (publishers/google, :generateContent) and Anthropic Claude
// (publishers/anthropic, :rawPredict). This adapter proxies both.
//
// Vertex auth uses an OAuth2 bearer token or an Express-mode API key. The
// caller's selected scheme is preserved; there is no request signing here.
// The caller (an SDK/CLI using Application Default Credentials or
// `gcloud auth print-access-token`) supplies a fresh access token per request;
// the gateway forwards it and holds no GCP secret.
//
// Usage parsing is likewise inherited: the shared providers.ParseUsageBytes
// already understands both shapes Vertex returns — Gemini camelCase
// (usageMetadata.promptTokenCount/candidatesTokenCount/cachedContentTokenCount/
// thoughtsTokenCount) and Claude snake_case (usage.input_tokens/
// cache_read_input_tokens/…). So there is no usage.go either.
//
// Vertex-specific routing validates the aiplatform host family and inspects
// streaming-by-method. API-key query parameters normalize to the native header.
//
// Vertex is a byte-safe passthrough: the adapter swaps the base URL and sets the
// credential header but never alters the model-visible request body, and registers
// no provider-native transform optimizers (the inherited pass-through
// Base.ApplyProviderNativeTransforms is used unchanged).
type Adapter struct {
	providers.Base
}

func (a Adapter) SanitizeAndMapHeaders(ctx context.Context, req *http.Request, credential providers.Credential, upstream *url.URL) (http.Header, error) {
	key, err := providers.GoogleRequestAPIKey(req)
	if err != nil {
		return nil, err
	}
	if credential.Mode == "ephemeral_header" && credential.Scheme == "api_key" && key == credential.Key {
		auth := strings.TrimSpace(req.Header.Get("Authorization"))
		if auth != "" && !strings.EqualFold(auth, "Bearer no-key-required") {
			// The Google SDK selects either an API key or OAuth credentials.
			// Do not silently choose between two explicit caller identities.
			return nil, providers.ErrGoogleRequestCredentials
		}
	}
	if credential.Mode == "ephemeral_header" && credential.Scheme == "api_key" && key != "" && key != credential.Key {
		// Both are caller-supplied and they name different Express keys. A managed
		// credential stays authoritative (it is the gateway's own principal, and
		// the caller's Authorization is a Caveman credential, not a Google one).
		return nil, providers.ErrGoogleRequestCredentials
	}
	out, err := a.Base.SanitizeAndMapHeaders(ctx, req, credential, upstream)
	if err == nil && credential.Scheme == "api_key" {
		out.Del("Authorization")
		if credential.Key != "" {
			out.Set("x-goog-api-key", credential.Key)
		}
	}
	return out, err
}

// Routes are the Vertex prediction paths the gateway proxies, prefixed with
// /vertex/. The native Vertex path carries the project, location, publisher,
// model, and method:
//
//	POST /vertex/v1/projects/{project}/locations/{location}/publishers/google/models/{model}:generateContent
//	POST /vertex/v1/projects/{project}/locations/{location}/publishers/google/models/{model}:streamGenerateContent
//	POST /vertex/v1/projects/{project}/locations/{location}/publishers/google/models/{model}:countTokens
//	POST /vertex/v1/projects/{project}/locations/{location}/publishers/anthropic/models/{model}:rawPredict
//	POST /vertex/v1/projects/{project}/locations/{location}/publishers/anthropic/models/{model}:streamRawPredict
var Routes = []string{"/vertex/"}

// New constructs the Vertex adapter. baseURL is the aiplatform endpoint; in
// production it is a Vertex host (e.g. https://us-central1-aiplatform.googleapis.com
// or the global https://aiplatform.googleapis.com); locally it points at the
// provider-stub.
func New(baseURL string) providers.Adapter {
	return Adapter{Base: providers.Base{
		Provider: "vertex",
		BaseURL:  baseURL,
		Routes:   Routes,
	}}
}
