package cacheengine

import (
	"strings"
	"sync"
	"time"

	"github.com/JuliusBrussee/caveman/shared/platform/catalog"
	"github.com/JuliusBrussee/caveman/shared/platform/cost"
)

func defaultProfile(request NativeRequest) (Profile, bool) {
	provider := strings.ToLower(strings.TrimSpace(request.Provider))
	switch provider {
	case "anthropic":
		if !catalogCacheCapability(provider, request.Model, "prompt_cache") {
			return Profile{}, false
		}
		write, read := cacheMultipliers(request, 1.25, 0.10)
		return Profile{
			ID: "cave-cache-anthropic-v1", Provider: provider, Mode: ModeExplicit,
			Attribution: AttributionCausal, MinPrefixTokens: anthropicMinimum(request.Model), MaxBreakpoints: 4,
			EconomicsKnown: true, WriteMultiplier: write, ReadMultiplier: read, TTL: 5 * time.Minute,
			Rolling: true, OptimizerID: AnthropicStableOptimizerID,
		}, true
	case "openai":
		if openAIExplicitModel(request.Model) {
			write, read := cacheMultipliers(request, 1.25, 0.10)
			return Profile{
				ID: "cave-cache-openai-explicit-v1", Provider: provider, Mode: ModeExplicit,
				Attribution: AttributionCausal, MinPrefixTokens: 1024, MaxBreakpoints: 4,
				EconomicsKnown: true, WriteMultiplier: write, ReadMultiplier: read, TTL: 30 * time.Minute,
				Rolling: true, RoutingKey: true, MaxRPMPerKey: 15, OptimizerID: OpenAIExplicitOptimizerID,
			}, true
		}
		if !catalogCacheCapability(provider, request.Model, "prompt_cache_key") {
			return Profile{}, false
		}
		write, read := cacheMultipliers(request, 1, 0.10)
		return Profile{
			ID: "cave-cache-openai-affinity-v1", Provider: provider, Mode: ModeAffinity,
			Attribution: AttributionAffinity, MinPrefixTokens: 2048, MaxBreakpoints: 1,
			EconomicsKnown: true, WriteMultiplier: write, ReadMultiplier: read, TTL: 5 * time.Minute,
			Rolling: true, RoutingKey: true, MaxRPMPerKey: 15,
			OptimizerID: OpenAIKeyOptimizerID,
		}, true
	case "bedrock":
		if !bedrockCachePointEligibleModel(request.Model) || !bedrockCachePointEndpointEligible(request.Model, request.Endpoint) {
			return Profile{}, false
		}
		write, read := cacheMultipliers(request, 1.25, 0.10)
		return Profile{
			ID: "cave-cache-bedrock-v1", Provider: provider, Mode: ModeExplicit,
			Attribution: AttributionCausal, MinPrefixTokens: bedrockMinimum(request.Model), MaxBreakpoints: 4,
			EconomicsKnown: true, WriteMultiplier: write, ReadMultiplier: read, TTL: 5 * time.Minute,
			Rolling: true, OptimizerID: BedrockCacheOptimizerID,
		}, true
	case "gemini":
		if !catalogCacheCapability(provider, request.Model, "explicit_cache") {
			return Profile{}, false
		}
		return Profile{
			ID: "cave-cache-gemini-implicit-v1", Provider: provider, Mode: ModeImplicit,
			Attribution: AttributionOrganic, MinPrefixTokens: geminiMinimum(request.Model), MaxBreakpoints: 1,
			EconomicsKnown: false, Rolling: true,
		}, true
	default:
		return Profile{}, false
	}
}

var cacheCapabilities = sync.OnceValue(func() map[string]bool {
	capabilities := map[string]bool{}
	for _, entry := range catalog.List() {
		for _, capability := range []string{"prompt_cache", "prompt_cache_key", "explicit_cache"} {
			if enabled, ok := entry.Capabilities[capability].(bool); ok && enabled {
				capabilities[entry.Provider+"\x00"+entry.Model+"\x00"+capability] = true
			}
		}
	}
	return capabilities
})

func catalogCacheCapability(provider, model, capability string) bool {
	return cacheCapabilities()[provider+"\x00"+model+"\x00"+capability]
}

var bedrockCachePointModels = sync.OnceValue(func() map[string]bool {
	eligible := map[string]bool{}
	for _, entry := range catalog.List() {
		if entry.Provider != "bedrock" || !strings.HasPrefix(stripBedrockInferenceScope(entry.Model), "anthropic.claude-") {
			continue
		}
		if enabled, ok := entry.Capabilities["prompt_cache"].(bool); ok && enabled {
			eligible[entry.Model] = true
		}
	}
	return eligible
})

func bedrockCachePointEligibleModel(model string) bool {
	return bedrockCachePointModels()[model]
}

var bedrockCachePointEndpoints = sync.OnceValue(func() map[string]bool {
	eligible := map[string]bool{}
	for _, entry := range catalog.List() {
		if entry.Provider != "bedrock" {
			continue
		}
		promptCache, _ := entry.Capabilities["prompt_cache"].(bool)
		converse, _ := entry.Capabilities["converse"].(bool)
		invoke, _ := entry.Capabilities["invoke_model"].(bool)
		if !promptCache {
			continue
		}
		if converse {
			eligible[entry.Model+"\x00converse"] = true
		}
		if invoke {
			eligible[entry.Model+"\x00invoke"] = true
		}
	}
	return eligible
})

func bedrockCachePointEndpointEligible(model, endpoint string) bool {
	surface := ""
	switch endpoint {
	case "converse", "converse-stream":
		surface = "converse"
	case "invoke", "invoke-with-response-stream":
		surface = "invoke"
	default:
		return false
	}
	return bedrockCachePointEndpoints()[model+"\x00"+surface]
}

func stripBedrockInferenceScope(model string) string {
	for _, scope := range []string{"global.", "us.", "eu.", "apac.", "jp.", "au."} {
		if strings.HasPrefix(model, scope) {
			return strings.TrimPrefix(model, scope)
		}
	}
	return model
}

func cacheMultipliers(request NativeRequest, fallbackWrite, fallbackRead float64) (float64, float64) {
	var price cost.Price
	var version string
	if request.Region != "" {
		price, version = catalog.PriceForRegion(request.Provider, request.Model, request.Region)
	} else {
		price, version = catalog.Price(request.Provider, request.Model)
	}
	if strings.HasPrefix(version, "unpriced:") || price.InputPerMillion <= 0 {
		return fallbackWrite, fallbackRead
	}
	write := fallbackWrite
	read := fallbackRead
	if price.CacheWritePerMillion > 0 {
		write = price.CacheWritePerMillion / price.InputPerMillion
	} else if request.Provider == "openai" {
		write = 1
	}
	if price.CacheReadPerMillion > 0 {
		read = price.CacheReadPerMillion / price.InputPerMillion
	}
	return write, read
}

// openAIExplicitModel stays narrow because older models reject explicit cache
// fields. Current official contract names GPT-5.6 family; future families need
// profile data or an explicit caller override rather than a guessed request.
func openAIExplicitModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return model == "gpt-5.6" || strings.HasPrefix(model, "gpt-5.6-")
}

func anthropicMinimum(model string) int {
	model = strings.ToLower(model)
	switch {
	case strings.Contains(model, "haiku-4-5"), strings.Contains(model, "opus-4-5"), strings.Contains(model, "opus-4-6"):
		return 4096
	case strings.Contains(model, "fable-5"), strings.Contains(model, "mythos-5"):
		return 512
	case strings.Contains(model, "opus-4-8"), strings.Contains(model, "sonnet-5"), strings.Contains(model, "sonnet-4-6"), strings.Contains(model, "opus-5"):
		return 1024
	default:
		return 1024
	}
}

func bedrockMinimum(model string) int {
	model = strings.ToLower(model)
	switch {
	case strings.Contains(model, "opus-4-5"), strings.Contains(model, "opus-4-6"), strings.Contains(model, "sonnet-4-5"), strings.Contains(model, "haiku-4-5"):
		return 4096
	case strings.Contains(model, "claude-3-5-haiku"):
		return 2048
	default:
		return 1024
	}
}

func geminiMinimum(model string) int {
	model = strings.ToLower(model)
	switch {
	case strings.Contains(model, "gemini-2.5"):
		return 2048
	case strings.Contains(model, "gemini-3"):
		return 4096
	default:
		return 0
	}
}
