package anthropic

import (
	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

// AutomaticPromptCacheOptimizerID is an experimental, default-off direct
// Anthropic Messages transform. Applying its marker is observable; whether the
// provider creates or reads a cache entry must be measured from provider usage
// and does not establish causal or verified savings.
const AutomaticPromptCacheOptimizerID = "anthropic-automatic-prompt-cache"

func automaticPromptCacheMessagesEndpoint(meta providers.RequestMetadata) bool {
	if meta.Provider != "anthropic" {
		return false
	}
	switch meta.Endpoint {
	case "/v1/messages", "/anthropic/v1/messages":
		return true
	default:
		return false
	}
}

func injectAutomaticPromptCacheRaw(data []byte) ([]byte, bool) {
	root, ok := jsonsplice.Root(data)
	if !ok {
		return nil, false
	}
	out, err := jsonsplice.AppendObjectFields(data, root,
		jsonsplice.FieldInsertion{Name: "cache_control", Value: []byte(`{"type":"ephemeral"}`)},
	)
	return out, err == nil
}
