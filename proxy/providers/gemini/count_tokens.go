package gemini

import (
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/internal/counttokens"
)

var _ providers.TokenCounter = Adapter{}

func (a Adapter) CountTokensRequest(original []byte, meta providers.RequestMetadata) (string, []byte, bool) {
	if meta.Model == "" || strings.Contains(meta.Model, "/") {
		return "", nil, false
	}
	if meta.Endpoint != "generateContent" && meta.Endpoint != "streamGenerateContent" {
		return "", nil, false
	}
	body, ok := counttokens.ProjectGemini(original)
	if !ok {
		return "", nil, false
	}
	return "/v1beta/models/" + meta.Model + ":countTokens", body, true
}

func (a Adapter) ParseCountTokens(response []byte) (int, bool) {
	return counttokens.ParseNonNegativeInt(response, "totalTokens")
}
