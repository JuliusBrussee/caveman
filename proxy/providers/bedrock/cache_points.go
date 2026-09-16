package bedrock

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
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

	// root above is decoded for decisions only; the marker is spliced into data.
	rootSpan, ok := jsonsplice.Root(data)
	if !ok {
		return passthrough, nil
	}

	var out []byte
	var injected bool
	switch cachePointEndpoint(meta.Endpoint) {
	case "converse", "converse-stream":
		out, injected = injectConverseCachePointRaw(data, rootSpan)
	case "invoke", "invoke-with-response-stream":
		out, injected = injectAnthropicCacheControlRaw(data, rootSpan)
	default:
		// Mantle stays out until C4. Unknown/new Bedrock grammars fail closed.
		return passthrough, nil
	}
	if !injected || !json.Valid(out) {
		return passthrough, nil
	}
	return providers.TransformResult{Body: out, OptimizerIDs: []string{CachePointsOptimizerID}}, nil
}

// Gateway telemetry keeps full request path in metadata. Direct engine callers
// use normalized action. Accept both without broadening supported grammars.
func cachePointEndpoint(endpoint string) string {
	if modelID, action := parseModelPath(endpoint); modelID != "" {
		return action
	}
	return endpoint
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

// injectConverseCachePointRaw appends one cachePoint marker to the end of the
// Converse tools array (if toolConfig is present) or the system array,
// splicing it into the original byte slice so every other byte is untouched.
func injectConverseCachePointRaw(data []byte, root jsonsplice.Span) ([]byte, bool) {
	cachePoint := []byte(`{"cachePoint":{"type":"default"}}`)

	if toolConfig, exists := jsonsplice.Field(data, root, "toolConfig"); exists {
		if !isJSONObject(data, toolConfig) {
			return nil, false
		}
		tools, exists := jsonsplice.Field(data, toolConfig, "tools")
		if !exists {
			return nil, false
		}
		elements, valid := jsonsplice.Elements(data, tools)
		if !valid || len(elements) == 0 {
			return nil, false
		}
		for _, element := range elements {
			if !isJSONObject(data, element) {
				return nil, false
			}
		}
		out, err := jsonsplice.AppendArrayElements(data, tools, cachePoint)
		return out, err == nil
	}

	system, exists := jsonsplice.Field(data, root, "system")
	if !exists {
		return nil, false
	}
	elements, valid := jsonsplice.Elements(data, system)
	if !valid || len(elements) == 0 {
		return nil, false
	}
	for _, element := range elements {
		if !isJSONObject(data, element) {
			return nil, false
		}
	}
	out, err := jsonsplice.AppendArrayElements(data, system, cachePoint)
	return out, err == nil
}

// injectAnthropicCacheControlRaw places one ephemeral cache_control marker on
// the last object-shaped tool, or system block, or wraps a bare string system
// prompt into the block form the marker needs. Mirrors cache_breakpoints.go.
func injectAnthropicCacheControlRaw(data []byte, root jsonsplice.Span) ([]byte, bool) {
	cacheControl := []byte(`{"type":"ephemeral"}`)

	if tools, found := jsonsplice.Field(data, root, "tools"); found {
		if elements, valid := jsonsplice.Elements(data, tools); valid {
			for i := len(elements) - 1; i >= 0; i-- {
				element := elements[i]
				if isJSONObject(data, element) {
					out, err := jsonsplice.AppendObjectFields(data, element,
						jsonsplice.FieldInsertion{Name: "cache_control", Value: cacheControl},
					)
					return out, err == nil
				}
			}
		}
	}

	system, found := jsonsplice.Field(data, root, "system")
	if !found || system.Start >= system.End {
		return nil, false
	}
	switch data[system.Start] {
	case '"':
		text, ok := jsonsplice.String(data, system)
		if !ok || text == "" {
			return nil, false
		}
		replacement := make([]byte, 0, system.End-system.Start+72)
		replacement = append(replacement, []byte(`[{"type":"text","text":`)...)
		replacement = append(replacement, data[system.Start:system.End]...)
		replacement = append(replacement, []byte(`,"cache_control":{"type":"ephemeral"}}]`)...)
		out, err := jsonsplice.ReplaceRaw(data, system, replacement)
		return out, err == nil
	case '[':
		elements, valid := jsonsplice.Elements(data, system)
		if !valid {
			return nil, false
		}
		for i := len(elements) - 1; i >= 0; i-- {
			element := elements[i]
			if isJSONObject(data, element) {
				out, err := jsonsplice.AppendObjectFields(data, element,
					jsonsplice.FieldInsertion{Name: "cache_control", Value: cacheControl},
				)
				return out, err == nil
			}
		}
	}
	return nil, false
}

func isJSONObject(data []byte, span jsonsplice.Span) bool {
	return span.Start < span.End && data[span.Start] == '{'
}
