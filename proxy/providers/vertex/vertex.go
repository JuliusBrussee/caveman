package vertex

import "github.com/JuliusBrussee/caveman/proxy/providers"

// Adapter is the Google Vertex AI gateway adapter. Like Bedrock, Vertex fronts
// multiple model families behind one cloud endpoint: Google Gemini
// (publishers/google, :generateContent) and Anthropic Claude
// (publishers/anthropic, :rawPredict). This adapter proxies both.
//
// It is deliberately thin. Vertex auth is a plain OAuth2 bearer token
// (Authorization: Bearer <google-access-token>), which the embedded
// providers.Base already emits in its default SanitizeAndMapHeaders case — so,
// unlike Bedrock, there is NO request signing here and NO custom header method.
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
// The ONLY Vertex-specific logic is upstream URL / host resolution (the
// aiplatform host family) plus a thin InspectRequest override for
// streaming-by-method. Both live in routing.go.
//
// Vertex is a byte-safe passthrough: the adapter swaps the base URL and sets the
// bearer header but never alters the model-visible request body, and registers
// no provider-native transform optimizers (the inherited pass-through
// Base.ApplyProviderNativeTransforms is used unchanged).
type Adapter struct {
	providers.Base
}

// Routes are the Vertex prediction paths the gateway proxies, prefixed with
// /vertex/. The native Vertex path carries the project, location, publisher,
// model, and method:
//
//	POST /vertex/v1/projects/{project}/locations/{location}/publishers/google/models/{model}:generateContent
//	POST /vertex/v1/projects/{project}/locations/{location}/publishers/google/models/{model}:streamGenerateContent
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
