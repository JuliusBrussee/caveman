package providers

// CompressionCoverageVersion changes whenever a registered request surface is
// added, removed, or changes compression eligibility.
const CompressionCoverageVersion = "2026-08-10.1"

type CompressionRouteCoverage struct {
	Provider string `json:"provider"`
	Route    string `json:"route"`
	Grammar  string `json:"grammar"`
	Fields   string `json:"fields"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
}

// CompressionRouteMatrix is the checked-in truth table for every route family
// registered by the managed gateway. "supported" means both schema-aware
// byte-splice extraction and a usable server-recovery grammar exist;
// "unsupported" is an intentional byte-identical opt-out.
func CompressionRouteMatrix() []CompressionRouteCoverage {
	return []CompressionRouteCoverage{
		{Provider: "openai", Route: "/v1/chat/completions", Grammar: "openai-chat", Fields: "latest user content and tool output", Status: "supported"},
		{Provider: "openai", Route: "/v1/responses", Grammar: "openai-responses", Fields: "latest user input and tool output", Status: "supported"},
		{Provider: "openai", Route: "/v1/embeddings", Grammar: "openai-embeddings", Status: "unsupported", Reason: "embeddings are not model-visible prompt text"},
		{Provider: "openai", Route: "/openai/v1/chat/completions", Grammar: "openai-chat", Fields: "latest user content and tool output", Status: "supported"},
		{Provider: "openai", Route: "/openai/v1/responses", Grammar: "openai-responses", Fields: "latest user input and tool output", Status: "supported"},
		{Provider: "openai", Route: "/openai/v1/embeddings", Grammar: "openai-embeddings", Status: "unsupported", Reason: "embeddings are not model-visible prompt text"},
		{Provider: "anthropic", Route: "/v1/messages", Grammar: "anthropic-messages", Fields: "cache-cold user text and tool_result output", Status: "supported"},
		{Provider: "anthropic", Route: "/v1/messages/count_tokens", Grammar: "anthropic-count", Status: "unsupported", Reason: "accounting endpoint must preserve request bytes"},
		{Provider: "anthropic", Route: "/anthropic/v1/messages", Grammar: "anthropic-messages", Fields: "cache-cold user text and tool_result output", Status: "supported"},
		{Provider: "anthropic", Route: "/anthropic/v1/messages/count_tokens", Grammar: "anthropic-count", Status: "unsupported", Reason: "accounting endpoint must preserve request bytes"},
		{Provider: "azure_openai", Route: "/azure/", Grammar: "azure-openai-dynamic-prefix", Fields: "validated deployment chat/completions uses OpenAI chat grammar", Status: "supported"},
		{Provider: "openai_compatible", Route: "/compat/", Grammar: "openai-compatible-dynamic-prefix", Fields: "OpenAI chat/completions grammar only", Status: "supported"},
		{Provider: "openai_compatible", Route: "/compat/{name}/v1/chat/completions", Grammar: "openai-chat", Fields: "latest user content and tool output", Status: "supported"},
		{Provider: "openai_compatible", Route: "/compat/{name}/v1/responses", Grammar: "openai-responses", Fields: "latest user input and tool output", Status: "supported"},
		{Provider: "gemini", Route: "/gemini/v1beta/models/", Grammar: "gemini-legacy-dynamic-prefix", Status: "unsupported", Reason: "server recovery grammar is unavailable for Gemini content routes"},
		{Provider: "gemini", Route: "/gemini/v1beta/models/{model}:generateContent", Grammar: "gemini-content", Fields: "latest user text and function response", Status: "supported"},
		{Provider: "gemini", Route: "/gemini/v1beta/models/{model}:streamGenerateContent", Grammar: "gemini-content", Status: "unsupported", Reason: "server recovery grammar is unavailable for Gemini"},
		{Provider: "gemini", Route: "/gemini/v1beta/models/{model}:countTokens", Grammar: "gemini-count", Status: "unsupported", Reason: "accounting endpoint must preserve request bytes"},
		{Provider: "gemini", Route: "/v1beta/models/{model}:generateContent", Grammar: "gemini-content", Fields: "latest user text and function response", Status: "supported"},
		{Provider: "gemini", Route: "/v1beta/models/{model}:streamGenerateContent", Grammar: "gemini-content", Status: "unsupported", Reason: "server recovery grammar is unavailable for Gemini"},
		{Provider: "gemini", Route: "/v1beta/models/{model}:countTokens", Grammar: "gemini-count", Status: "unsupported", Reason: "accounting endpoint must preserve request bytes"},
		{Provider: "bedrock", Route: "/bedrock/model/", Grammar: "bedrock-runtime-dynamic-prefix", Status: "unsupported", Reason: "signed provider-specific body grammar is not safely splice-covered"},
		{Provider: "bedrock", Route: "/bedrock/anthropic/", Grammar: "bedrock-mantle-anthropic", Status: "unsupported", Reason: "Mantle is an opt-in passthrough surface without Bedrock-specific compression proof"},
		{Provider: "vertex", Route: "/vertex/", Grammar: "vertex-predict-dynamic-prefix", Status: "unsupported", Reason: "publisher-specific request grammar is not safely splice-covered"},
	}
}
