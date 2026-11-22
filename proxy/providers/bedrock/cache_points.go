package bedrock

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
	"github.com/JuliusBrussee/caveman/shared/platform/catalog"
)

// maxBedrockCachePoints is Bedrock Anthropic Claude's hard cap on cache
// checkpoints per request (same 4-breakpoint limit as Anthropic direct).
const maxBedrockCachePoints = 4

// anthropicBreakpointPlanner delegates Bedrock InvokeModel Anthropic-wire
// breakpoint planning to the shared Anthropic Messages planner.
var anthropicBreakpointPlanner = anthropic.Adapter{}

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

	cacheEnabled := bedrockCacheOptimizerEnabled(policy)
	deferEnabled := policy.OptimizerEnabled(anthropic.DeferMCPToolsOptimizerID)
	if !cacheEnabled && !deferEnabled {
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

	endpointKind := cachePointEndpoint(meta.Endpoint)
	cur := data
	optimizerIDs := []string{}
	if deferEnabled && (endpointKind == "invoke" || endpointKind == "invoke-with-response-stream") {
		if deferred, ok := anthropic.ApplyDeferMCPTools(cur, root); ok {
			cur = deferred
			optimizerIDs = append(optimizerIDs, anthropic.DeferMCPToolsOptimizerID)
			root = nil
			if json.Unmarshal(cur, &root) != nil {
				return providers.TransformResult{Body: cur, OptimizerIDs: optimizerIDs}, nil
			}
		}
	}

	if !cacheEnabled || containsCacheMarker(root) {
		return providers.TransformResult{Body: cur, OptimizerIDs: optimizerIDs}, nil
	}

	// root above is decoded for decisions only; the marker is spliced into cur.
	rootSpan, ok := jsonsplice.Root(cur)
	if !ok {
		return providers.TransformResult{Body: cur, OptimizerIDs: optimizerIDs}, nil
	}

	var out []byte
	var injected bool
	switch endpointKind {
	case "converse", "converse-stream":
		out, injected = injectConverseCachePointRaw(cur, rootSpan)
	case "invoke", "invoke-with-response-stream":
		out, injected = injectAnthropicCacheControlRaw(cur, rootSpan, root)
	default:
		// Mantle stays out until C4. Unknown/new Bedrock grammars fail closed.
		return providers.TransformResult{Body: cur, OptimizerIDs: optimizerIDs}, nil
	}
	if !injected || !json.Valid(out) {
		return providers.TransformResult{Body: cur, OptimizerIDs: optimizerIDs}, nil
	}
	return providers.TransformResult{Body: out, OptimizerIDs: append(optimizerIDs, CachePointsOptimizerID)}, nil
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
func injectAnthropicCacheControlRaw(data []byte, root jsonsplice.Span, decoded map[string]any) ([]byte, bool) {
	cacheControl := []byte(`{"type":"ephemeral"}`)

	if tools, found := jsonsplice.Field(data, root, "tools"); found {
		if elements, valid := jsonsplice.Elements(data, tools); valid {
			decodedTools, _ := decoded["tools"].([]any)
			for i := len(elements) - 1; i >= 0; i-- {
				if i < len(decodedTools) {
					if m, ok := decodedTools[i].(map[string]any); ok {
						if def, _ := m["defer_loading"].(bool); def {
							continue
						}
					}
				}
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

func isJSONArray(data []byte, span jsonsplice.Span) bool {
	return span.Start < span.End && data[span.Start] == '[' && data[span.End-1] == ']'
}

// PlanCacheBreakpoints places Bedrock prompt-cache checkpoints on Anthropic
// Claude requests (Converse `cachePoint` and InvokeModel `cache_control`)
// within Bedrock's 4-checkpoint limit.
//
// Like Anthropic direct:
//   - PAYG-only and gated on CachePointEligibleModel(meta.Model)
//   - Cold start (0 existing markers): tools tail, system tail, and rolling
//     conversation frontier on the newest message
//   - Managed (>0 existing markers): never moves caller markers; closes the
//     composition dead-zone when only tools/system carry a marker (e.g. from
//     bedrock-cache-points) by placing one rolling frontier checkpoint on the
//     newest message within the 4-checkpoint budget.
func (a Adapter) PlanCacheBreakpoints(body []byte, meta providers.RequestMetadata, payg bool) ([]byte, bool) {
	if !payg || !CachePointEligibleModel(meta.Model) {
		return nil, false
	}
	switch cachePointEndpoint(meta.Endpoint) {
	case "invoke", "invoke-with-response-stream":
		return anthropicBreakpointPlanner.PlanCacheBreakpoints(body, meta, payg)
	case "converse", "converse-stream":
		return planConverseCachePoints(body)
	default:
		return nil, false
	}
}

func planConverseCachePoints(data []byte) ([]byte, bool) {
	var root map[string]any
	if json.Unmarshal(data, &root) != nil {
		return nil, false
	}
	existing := countCacheMarkers(root)
	if existing >= maxBedrockCachePoints {
		return nil, false
	}
	rootSpan, ok := jsonsplice.Root(data)
	if !ok {
		return nil, false
	}

	budget := maxBedrockCachePoints - existing
	var edits []bedrockSpliceEdit
	if existing == 0 {
		if edit, ok := converseToolsTailEdit(data, rootSpan); ok {
			edits = append(edits, edit)
		}
		if edit, ok := converseSystemTailEdit(data, rootSpan); ok {
			edits = append(edits, edit)
		}
		if edit, ok := converseFrontierEdit(data, rootSpan); ok {
			edits = append(edits, edit)
		}
	} else {
		// Composition dead-zone closure: if no message carries a cachePoint yet,
		// place one rolling frontier checkpoint on the newest message without
		// touching caller/optimizer markers on tools or system.
		if messagesCarryCachePoint(root) {
			return nil, false
		}
		if edit, ok := converseFrontierEdit(data, rootSpan); ok {
			edits = append(edits, edit)
		}
	}
	if len(edits) == 0 {
		return nil, false
	}
	if len(edits) > budget {
		edits = edits[:budget]
	}
	out, ok := applyBedrockSpliceEdits(data, edits)
	if !ok || !json.Valid(out) {
		return nil, false
	}
	return out, true
}

type bedrockSpliceEdit struct {
	start int
	end   int
	bytes []byte
}

func appendCachePointEdit(data []byte, arr jsonsplice.Span) (bedrockSpliceEdit, bool) {
	if !isJSONArray(data, arr) {
		return bedrockSpliceEdit{}, false
	}
	elements, valid := jsonsplice.Elements(data, arr)
	if !valid || len(elements) == 0 {
		return bedrockSpliceEdit{}, false
	}
	for _, element := range elements {
		if !isJSONObject(data, element) {
			return bedrockSpliceEdit{}, false
		}
	}
	insertAt := arr.End - 1
	for insertAt > arr.Start+1 && (data[insertAt-1] == ' ' || data[insertAt-1] == '\n' || data[insertAt-1] == '\r' || data[insertAt-1] == '\t') {
		insertAt--
	}
	return bedrockSpliceEdit{
		start: insertAt,
		end:   insertAt,
		bytes: []byte(`,{"cachePoint":{"type":"default"}}`),
	}, true
}

func converseToolsTailEdit(data []byte, root jsonsplice.Span) (bedrockSpliceEdit, bool) {
	toolConfig, exists := jsonsplice.Field(data, root, "toolConfig")
	if !exists || !isJSONObject(data, toolConfig) {
		return bedrockSpliceEdit{}, false
	}
	tools, exists := jsonsplice.Field(data, toolConfig, "tools")
	if !exists {
		return bedrockSpliceEdit{}, false
	}
	return appendCachePointEdit(data, tools)
}

func converseSystemTailEdit(data []byte, root jsonsplice.Span) (bedrockSpliceEdit, bool) {
	system, exists := jsonsplice.Field(data, root, "system")
	if !exists {
		return bedrockSpliceEdit{}, false
	}
	return appendCachePointEdit(data, system)
}

func converseFrontierEdit(data []byte, root jsonsplice.Span) (bedrockSpliceEdit, bool) {
	messages, exists := jsonsplice.Field(data, root, "messages")
	if !exists || !isJSONArray(data, messages) {
		return bedrockSpliceEdit{}, false
	}
	elements, valid := jsonsplice.Elements(data, messages)
	if !valid || len(elements) == 0 {
		return bedrockSpliceEdit{}, false
	}
	for i := len(elements) - 1; i >= 0; i-- {
		if !isJSONObject(data, elements[i]) {
			continue
		}
		content, exists := jsonsplice.Field(data, elements[i], "content")
		if !exists {
			continue
		}
		if edit, ok := appendCachePointEdit(data, content); ok {
			return edit, true
		}
	}
	return bedrockSpliceEdit{}, false
}

func countCacheMarkers(value any) int {
	switch node := value.(type) {
	case map[string]any:
		count := 0
		for key, child := range node {
			if key == "cachePoint" || key == "cache_control" {
				count++
			}
			count += countCacheMarkers(child)
		}
		return count
	case []any:
		count := 0
		for _, child := range node {
			count += countCacheMarkers(child)
		}
		return count
	}
	return 0
}

func messagesCarryCachePoint(root map[string]any) bool {
	msgs, ok := root["messages"]
	if !ok {
		return false
	}
	return countCacheMarkers(msgs) > 0
}

func applyBedrockSpliceEdits(data []byte, edits []bedrockSpliceEdit) ([]byte, bool) {
	sorted := append([]bedrockSpliceEdit(nil), edits...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].start < sorted[j].start })
	total := len(data)
	for _, edit := range sorted {
		total += len(edit.bytes)
	}
	out := make([]byte, 0, total)
	last := 0
	for _, edit := range sorted {
		if edit.start < last || edit.end < edit.start || edit.end > len(data) {
			return nil, false
		}
		out = append(out, data[last:edit.start]...)
		out = append(out, edit.bytes...)
		last = edit.end
	}
	return append(out, data[last:]...), true
}

// CachedPrefixComponents splits a Bedrock Claude request into the components
// Bedrock's prompt cache keys on (system, tools/toolConfig, then each message,
// with cachePoint / cache_control markers stripped) and reports how many
// leading components this request caches. Stripping markers keeps the prefix
// monitor accurate as the rolling frontier cachePoint advances across turns.
func (a Adapter) CachedPrefixComponents(body []byte, meta providers.RequestMetadata) ([][]byte, int, bool) {
	switch cachePointEndpoint(meta.Endpoint) {
	case "invoke", "invoke-with-response-stream":
		return anthropicBreakpointPlanner.CachedPrefixComponents(body, meta)
	case "converse", "converse-stream":
		return converseCachedPrefixComponents(body)
	default:
		return nil, 0, false
	}
}

func converseCachedPrefixComponents(body []byte) ([][]byte, int, bool) {
	root, ok := jsonsplice.Root(body)
	if !ok {
		return nil, 0, false
	}
	messagesSpan, exists := jsonsplice.Field(body, root, "messages")
	if !exists || !isJSONArray(body, messagesSpan) {
		return nil, 0, false
	}
	messages, valid := jsonsplice.Elements(body, messagesSpan)
	if !valid {
		return nil, 0, false
	}
	components := make([][]byte, 0, len(messages))
	cached := 0

	var sysVal []byte
	if sysSpan, found := jsonsplice.Field(body, root, "system"); found {
		stripped, hadMarker := stripConverseCachePointsFromArray(body[sysSpan.Start:sysSpan.End])
		sysVal = stripped
		if hadMarker {
			cached = 2
		}
	}
	components = append(components, bedrockFrameComponent("system", sysVal))

	var toolVal []byte
	if tcSpan, found := jsonsplice.Field(body, root, "toolConfig"); found {
		stripped, hadMarker := stripConverseCachePointsFromObjectArrayField(body[tcSpan.Start:tcSpan.End], "tools")
		toolVal = stripped
		if hadMarker {
			cached = 2
		}
	}
	components = append(components, bedrockFrameComponent("toolConfig", toolVal))

	for _, msgSpan := range messages {
		raw := body[msgSpan.Start:msgSpan.End]
		stripped, hadMarker := stripConverseCachePointsFromObjectArrayField(raw, "content")
		components = append(components, bedrockFrameComponent("message", stripped))
		if hadMarker {
			cached = len(components)
		}
	}
	return components, cached, true
}

func stripConverseCachePointsFromObjectArrayField(obj []byte, field string) ([]byte, bool) {
	if !bytes.Contains(obj, []byte(`"cachePoint"`)) {
		return obj, false
	}
	root, ok := jsonsplice.Root(obj)
	if !ok {
		return obj, false
	}
	arrSpan, found := jsonsplice.Field(obj, root, field)
	if !found || !isJSONArray(obj, arrSpan) {
		return obj, false
	}
	strippedArr, hadMarker := stripConverseCachePointsFromArray(obj[arrSpan.Start:arrSpan.End])
	if !hadMarker {
		return obj, false
	}
	out, err := jsonsplice.ReplaceRaw(obj, arrSpan, strippedArr)
	if err != nil {
		return obj, false
	}
	return out, true
}

func stripConverseCachePointsFromArray(arr []byte) ([]byte, bool) {
	if !bytes.Contains(arr, []byte(`"cachePoint"`)) {
		return arr, false
	}
	span := jsonsplice.Span{Start: 0, End: len(arr)}
	if !isJSONArray(arr, span) {
		return arr, false
	}
	elements, valid := jsonsplice.Elements(arr, span)
	if !valid {
		return arr, false
	}
	var kept [][]byte
	hadMarker := false
	for _, el := range elements {
		if isJSONObject(arr, el) {
			if _, isCP := jsonsplice.Field(arr, el, "cachePoint"); isCP {
				hadMarker = true
				continue
			}
		}
		kept = append(kept, arr[el.Start:el.End])
	}
	if !hadMarker {
		return arr, false
	}
	var out bytes.Buffer
	out.WriteByte('[')
	for i, el := range kept {
		if i > 0 {
			out.WriteByte(',')
		}
		out.Write(el)
	}
	out.WriteByte(']')
	return out.Bytes(), true
}

func bedrockFrameComponent(name string, value []byte) []byte {
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(len(name)))
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(value)))
	return slices.Concat(hdr[:], []byte(name), value)
}
