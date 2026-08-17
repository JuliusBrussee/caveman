package bedrock

import "github.com/JuliusBrussee/caveman/proxy/providers"

// Adapter is the AWS Bedrock gateway adapter. It embeds the shared Base and adds
// Bedrock API-key/IAM authentication, a region/model allowlist, and
// Bedrock-shaped usage parsing.
//
// Bedrock signs the exact post-transform upstream request and parses
// Bedrock-shaped usage. Its only provider-native transform is the opt-in
// bedrock-cache-points marker for Anthropic Claude on Bedrock Runtime; every
// unsupported vendor and surface remains byte-identical pass-through.
type Adapter struct {
	providers.Base
}

// Routes include the broad native Runtime surface and an explicit Anthropic-wire
// Mantle surface. ResolveUpstreamURL keeps Mantle opt-in and closes each subtree
// to its exact supported actions.
//
//	POST /bedrock/model/{modelId}/invoke
//	POST /bedrock/model/{modelId}/invoke-with-response-stream
//	POST /bedrock/model/{modelId}/converse
//	POST /bedrock/model/{modelId}/converse-stream
//	POST /bedrock/anthropic/v1/messages
//	POST /bedrock/anthropic/v1/messages/count_tokens
var Routes = []string{"/bedrock/model/", "/bedrock/anthropic/"}

// New constructs the Bedrock adapter. baseURL is the bedrock-runtime endpoint;
// in production it is the regional endpoint
// (https://bedrock-runtime.<region>.amazonaws.com); locally it points at the
// provider-stub.
func New(baseURL string) providers.Adapter {
	return Adapter{Base: providers.Base{
		Provider: "bedrock",
		BaseURL:  baseURL,
		Routes:   Routes,
	}}
}
