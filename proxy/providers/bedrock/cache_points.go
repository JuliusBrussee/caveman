package bedrock

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/shared/platform/catalog"
)

// CachePointsOptimizerID is deliberately separate from the Anthropic-direct
// cache attribution set. C1 changes only the Bedrock wire body; C2/C3 own the
// distinct accounting method and rollout evidence.
const CachePointsOptimizerID = "bedrock-cache-points"

var bedrockCacheOptimizerIDs = map[string]bool{
	CachePointsOptimizerID: true,
}

// ApplyProviderNativeTransforms adds one Bedrock-native cache marker to the
// largest stable Anthropic Claude prefix on Bedrock Runtime. Caller-managed
// markers, models without the catalog's prompt_cache capability, unsupported
// vendors/surfaces, non-PAYG auth, malformed JSON, and bodies without a stable
// tools/system prefix pass through byte-identically.
func (a Adapter) ApplyProviderNativeTransforms(
	ctx context.Context,
	body providers.BodyReader,
	meta providers.RequestMetadata,
	policy providers.TransformPolicy,
) (providers.TransformResult, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return providers.TransformResult{Body: data, OptimizerIDs: []string{}}, nil
	}
	passthrough := providers.TransformResult{Body: data, OptimizerIDs: []string{}}

	if !bedrockCacheOptimizerEnabled(policy) {
		return passthrough, nil
	}
	if policy.RuntimeMode == "record" {
		return passthrough, nil
	}
	if policy.AuthMode != "" && policy.AuthMode != "payg" {
		return passthrough, nil
	}
	if !CachePointEligibleModel(meta.Model) {
		return passthrough, nil
	}

	var root map[string]any
	if json.Unmarshal(data, &root) != nil {
		return passthrough, nil
	}
	if containsCacheMarker(root) {
		return passthrough, nil
	}

	var injected bool
	switch meta.Endpoint {
	case "converse", "converse-stream":
		injected = injectConverseCachePoint(root)
	case "invoke", "invoke-with-response-stream":
		injected = injectAnthropicCacheControl(root)
	default:
		// Mantle stays out until C4. Unknown/new Bedrock grammars fail closed.
		return passthrough, nil
	}
	if !injected {
		return passthrough, nil
	}

	out, err := json.Marshal(root)
	if err != nil {
		return passthrough, nil
	}
	return providers.TransformResult{Body: out, OptimizerIDs: []string{CachePointsOptimizerID}}, nil
}

// cachePointEligibleModels is computed once from the catalog: the Bedrock
// Anthropic Claude model ids whose catalog row asserts the prompt_cache
// capability (any region — cache support is a model property; regional pricing
// gaps are separately handled by the accounting layer's honest zero). The
// Anthropic-Claude check strips at most one inference-profile routing scope
// (StripInferenceProfileScope) before matching, the same normalization the
// model allowlist applies: a global./us./eu. profile id still routes to a
// Claude model whose Converse/InvokeModel body grammar is exactly what the
// injection functions handle. Keying on the EXACT catalog model id (scope
// included) is what keeps the population honest — a profile id with no
// catalog row of its own (e.g. us.anthropic.* today) is not eligible, because
// AWS prices geographic profiles differently from global ones and no grounded
// rate exists to verify a delta against.
var cachePointEligibleModels = sync.OnceValue(func() map[string]bool {
	eligible := map[string]bool{}
	for _, entry := range catalog.List() {
		if entry.Provider != "bedrock" || !strings.HasPrefix(StripInferenceProfileScope(entry.Model), "anthropic.claude-") {
			continue
		}
		if enabled, ok := entry.Capabilities["prompt_cache"].(bool); ok && enabled {
			eligible[entry.Model] = true
		}
	}
	return eligible
})

// CachePointEligibleModel reports whether the cache-point transform may run
// for a model: an Anthropic Claude id — bare or behind one inference-profile
// scope (the injection grammars are Anthropic-shaped either way) — whose
// catalog row asserts prompt_cache. Gating on the bare name prefix alone
// admitted legacy Claude models with no documented cache support — appending
// a cachePoint there risks converting working traffic into upstream
// rejections, and the gateway's fail-open covers transform errors, not an
// upstream rejecting successfully-transformed bytes.
// This predicate IS the population the catalog completeness test prices
// (TestBedrockClaudeCacheRowsAreFullyPriced calls it), so the transform's
// population and the test's population are the same set by construction. An
// unloadable catalog or an absent model yields false — fail closed to
// byte-identical passthrough.
func CachePointEligibleModel(model string) bool {
	return cachePointEligibleModels()[model]
}

func bedrockCacheOptimizerEnabled(policy providers.TransformPolicy) bool {
	for id := range bedrockCacheOptimizerIDs {
		if policy.OptimizerEnabled(id) {
			return true
		}
	}
	return false
}

func containsCacheMarker(value any) bool {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if key == "cachePoint" || key == "cache_control" || containsCacheMarker(child) {
				return true
			}
		}
	case []any:
		for _, child := range node {
			if containsCacheMarker(child) {
				return true
			}
		}
	}
	return false
}

func injectConverseCachePoint(root map[string]any) bool {
	if rawToolConfig, exists := root["toolConfig"]; exists {
		toolConfig, ok := rawToolConfig.(map[string]any)
		if !ok {
			return false
		}
		rawTools, exists := toolConfig["tools"]
		if !exists {
			return false
		}
		tools, ok := rawTools.([]any)
		if !ok || len(tools) == 0 {
			return false
		}
		for _, tool := range tools {
			if _, ok := tool.(map[string]any); !ok {
				return false
			}
		}
		toolConfig["tools"] = append(tools, defaultCachePoint())
		return true
	}

	rawSystem, exists := root["system"]
	if !exists {
		return false
	}
	system, ok := rawSystem.([]any)
	if !ok || len(system) == 0 {
		return false
	}
	for _, block := range system {
		if _, ok := block.(map[string]any); !ok {
			return false
		}
	}
	root["system"] = append(system, defaultCachePoint())
	return true
}

func injectAnthropicCacheControl(root map[string]any) bool {
	if tools, ok := root["tools"].([]any); ok && len(tools) > 0 {
		for index := len(tools) - 1; index >= 0; index-- {
			if tool, ok := tools[index].(map[string]any); ok {
				tool["cache_control"] = ephemeralCacheControl()
				return true
			}
		}
	}

	switch system := root["system"].(type) {
	case string:
		if system == "" {
			return false
		}
		root["system"] = []any{map[string]any{
			"type":          "text",
			"text":          system,
			"cache_control": ephemeralCacheControl(),
		}}
		return true
	case []any:
		for index := len(system) - 1; index >= 0; index-- {
			if block, ok := system[index].(map[string]any); ok {
				block["cache_control"] = ephemeralCacheControl()
				return true
			}
		}
	}
	return false
}

func defaultCachePoint() map[string]any {
	return map[string]any{"cachePoint": map[string]any{"type": "default"}}
}

func ephemeralCacheControl() map[string]any {
	return map[string]any{"type": "ephemeral"}
}
