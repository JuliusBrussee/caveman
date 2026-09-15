package anthropic

import (
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/internal/counttokens"
)

var _ providers.TokenCounter = Adapter{}

func (a Adapter) CountTokensRequest(original []byte, meta providers.RequestMetadata) (string, []byte, bool) {
	if meta.Endpoint != "/v1/messages" && meta.Endpoint != "/anthropic/v1/messages" {
		return "", nil, false
	}
	body, ok := counttokens.ProjectAnthropic(original)
	if !ok {
		return "", nil, false
	}
	if strings.HasPrefix(meta.Endpoint, "/anthropic/") {
		return "/anthropic/v1/messages/count_tokens", body, true
	}
	return "/v1/messages/count_tokens", body, true
}

func (a Adapter) ParseCountTokens(response []byte) (int, bool) {
	return counttokens.ParseNonNegativeInt(response, "input_tokens")
}
