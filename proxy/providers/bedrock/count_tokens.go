package bedrock

import (
	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/internal/counttokens"
)

var _ providers.TokenCounter = Adapter{}

func (a Adapter) CountTokensRequest(original []byte, meta providers.RequestMetadata) (string, []byte, bool) {
	if meta.Endpoint != "mantle_messages" {
		return "", nil, false
	}
	body, ok := counttokens.ProjectAnthropic(original)
	if !ok {
		return "", nil, false
	}
	return "/bedrock/anthropic/v1/messages/count_tokens", body, true
}

func (a Adapter) ParseCountTokens(response []byte) (int, bool) {
	return counttokens.ParseNonNegativeInt(response, "input_tokens")
}
