package cacheengine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

// Optimize applies provider-native cache metadata or returns copied original
// bytes on every unsupported or unsafe path. It makes no network call.
func (e *Engine) Optimize(ctx context.Context, request NativeRequest) (NativeResult, error) {
	if e == nil || e.guard == nil || e.prefixSafety == nil || e.resolveProfile == nil {
		return NativeResult{}, errors.New("cacheengine: nil engine")
	}
	if e.configErr != nil {
		return NativeResult{}, e.configErr
	}
	if ctx == nil {
		return NativeResult{}, errors.New("cacheengine: nil context")
	}
	if err := ctx.Err(); err != nil {
		return NativeResult{}, err
	}
	if len(request.Body) > e.maxRequestBytes {
		return NativeResult{}, errors.New("cacheengine: request exceeds configured byte limit")
	}
	original := append([]byte(nil), request.Body...)
	unsupported := Profile{ID: "unsupported", Mode: ModeUnsupported, Attribution: AttributionNone}
	result := NativeResult{
		Body:     original,
		Decision: DecisionPassThrough,
		Reason:   ReasonUnsupported,
		Profile:  unsupported,
		Plan: Plan{
			Decision: DecisionPassThrough, Reason: ReasonUnsupported,
			ProfileID: unsupported.ID, Mode: unsupported.Mode, Attribution: unsupported.Attribution,
			EconomicsBasis: "unavailable", KeyShardCount: 1,
		},
		ClaimBasis:         "none",
		VerifiedSavingsUSD: 0,
	}
	if !validNativeIdentity(request) {
		result.Reason = ReasonMalformedRequest
		return result, nil
	}
	if strings.EqualFold(strings.TrimSpace(request.RuntimeMode), "record") {
		result.Reason = ReasonRecordMode
		return result, nil
	}
	if request.AuthMode != "" && !strings.EqualFold(strings.TrimSpace(request.AuthMode), "payg") {
		result.Reason = ReasonNonPAYG
		return result, nil
	}
	if len(request.Body) == 0 {
		result.Reason = ReasonMalformedRequest
		return result, nil
	}
	providerName := strings.ToLower(strings.TrimSpace(request.Provider))
	customDriver := e.drivers[providerName]
	if customDriver == nil && (providerName == "anthropic" || providerName == "openai" || providerName == "bedrock" || providerName == "gemini") && (request.Model == "" || request.Endpoint == "") {
		result.Reason = ReasonMalformedRequest
		return result, nil
	}
	if customDriver == nil && !builtinEndpointSupported(providerName, request.Endpoint) {
		return result, nil
	}
	if customDriver == nil {
		valid, callerManaged := inspectUniqueJSONObject(request.Body, providerName)
		if !valid {
			result.Reason = ReasonMalformedRequest
			return result, nil
		}
		if !nativeBodyModelMatches(providerName, request.Model, request.Body) {
			result.Reason = ReasonProfileMismatch
			return result, nil
		}
		if callerManaged {
			result.Reason = ReasonCallerManaged
			return result, nil
		}
	} else if len(request.StableSegments) == 0 {
		result.Reason = ReasonNoStablePrefix
		return result, nil
	}

	profile := request.Profile
	var ok bool
	if profile.ID != "" && customDriver == nil {
		result.Profile = normalizedProfile(profile)
		result.Reason = ReasonProfileMismatch
		return result, nil
	}
	if profile.ID == "" {
		profile, ok = e.resolveProfile(cloneNativeRequest(request))
		if !ok {
			return result, nil
		}
	}
	profile = normalizedProfile(profile)
	if strings.TrimSpace(profile.Provider) == "" || !strings.EqualFold(strings.TrimSpace(profile.Provider), providerName) {
		result.Profile = profile
		result.Reason = ReasonProfileMismatch
		return result, nil
	}
	if customDriver == nil && !builtinProfileCompatible(providerName, profile) {
		result.Profile = profile
		result.Reason = ReasonProfileMismatch
		return result, nil
	}
	result.Profile = profile
	segments := request.StableSegments
	if len(segments) == 0 {
		prefix, found := nativeStablePrefix(request)
		if !found {
			result.Reason = ReasonNoStablePrefix
			return result, nil
		}
		segments = []Segment{{
			Name: "native-prefix", Content: prefix, Tokens: request.PrefixTokens,
			Stable: true, Cacheable: true, ExpectedCalls: request.ExpectedCalls,
		}}
	}
	plan, err := e.Plan(PlanRequest{
		Scope:                     request.Scope,
		Epoch:                     request.Epoch,
		PartitionKey:              request.PartitionKey,
		ExpectedRequestsPerMinute: request.ExpectedRequestsPerMinute,
		ExpectedCalls:             request.ExpectedCalls,
		Profile:                   profile,
		Segments:                  segments,
	})
	if err != nil {
		return NativeResult{}, err
	}
	result.Plan = plan
	result.Decision = plan.Decision
	result.Reason = plan.Reason
	if plan.Decision != DecisionApply {
		return result, nil
	}

	var body []byte
	var optimizerIDs []string
	if customDriver != nil {
		transformed := customDriver.Apply(ctx, cloneNativeRequest(request), plan)
		body, optimizerIDs = append([]byte(nil), transformed.Body...), append([]string(nil), transformed.OptimizerIDs...)
	} else {
		switch providerName {
		case "anthropic":
			body, optimizerIDs = applyAnthropic(ctx, request, profile)
		case "openai":
			body, optimizerIDs = applyOpenAI(request.Body, request.Endpoint, plan.RoutingKey, profile.Mode == ModeExplicit)
		case "bedrock":
			body, optimizerIDs = applyBedrock(ctx, request, profile)
		default:
			result.Decision = DecisionPassThrough
			result.Reason = ReasonTransformUnavailable
			return result, nil
		}
	}
	if len(body) == 0 || len(body) > e.maxRequestBytes || !validOptimizerIDs(optimizerIDs) || bytes.Equal(body, request.Body) {
		result.Decision = DecisionPassThrough
		result.Reason = ReasonTransformUnavailable
		return result, nil
	}
	result.Body = append([]byte(nil), body...)
	result.OptimizerIDs = append([]string(nil), optimizerIDs...)
	result.Applied = true
	result.Decision = DecisionApply
	result.Reason = ReasonApplied
	result.ClaimBasis = "inferred"
	if providerName == "openai" && profile.Mode == ModeExplicit && !containsStringValue(optimizerIDs, OpenAIExplicitOptimizerID) {
		result.Profile.Attribution = AttributionAffinity
		result.Plan.Attribution = AttributionAffinity
		result.Reason = ReasonAffinityFallback
	}
	return result, nil
}

func builtinEndpointSupported(provider, endpoint string) bool {
	switch provider {
	case "anthropic":
		return endpoint == "/v1/messages"
	case "openai":
		return endpoint == "/v1/chat/completions" || endpoint == "/v1/responses"
	case "bedrock":
		return endpoint == "converse" || endpoint == "converse-stream" || endpoint == "invoke" || endpoint == "invoke-with-response-stream"
	case "gemini":
		return endpoint == "generateContent"
	default:
		return true
	}
}

func nativeBodyModelMatches(provider, model string, body []byte) bool {
	if provider != "openai" && provider != "anthropic" {
		return true
	}
	root, ok := jsonsplice.Root(body)
	if !ok {
		return false
	}
	bodyModel, ok := jsonsplice.StringField(body, root, "model")
	return ok && bodyModel == model
}

func cloneNativeRequest(request NativeRequest) NativeRequest {
	cloned := request
	cloned.Body = append([]byte(nil), request.Body...)
	cloned.StableSegments = append([]Segment(nil), request.StableSegments...)
	for index := range cloned.StableSegments {
		cloned.StableSegments[index].Content = append([]byte(nil), request.StableSegments[index].Content...)
	}
	return cloned
}

func validOptimizerIDs(values []string) bool {
	if len(values) == 0 || len(values) > 64 {
		return false
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if !validIdentity(value, 256, false) || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func builtinProfileCompatible(provider string, profile Profile) bool {
	switch provider {
	case "anthropic":
		return profile.Mode == ModeExplicit && profile.Attribution == AttributionCausal && profile.MaxBreakpoints == 4 && profile.TTL == 5*time.Minute && profile.Rolling && !profile.RoutingKey && profile.OptimizerID == AnthropicStableOptimizerID
	case "openai":
		switch profile.Mode {
		case ModeExplicit:
			return profile.Attribution == AttributionCausal && profile.MaxBreakpoints == 4 && profile.TTL == 30*time.Minute && profile.Rolling && profile.RoutingKey && profile.OptimizerID == OpenAIExplicitOptimizerID
		case ModeAffinity:
			return profile.Attribution == AttributionAffinity && profile.MaxBreakpoints == 1 && profile.TTL == 5*time.Minute && profile.Rolling && profile.RoutingKey && profile.OptimizerID == OpenAIKeyOptimizerID
		default:
			return false
		}
	case "bedrock":
		return profile.Mode == ModeExplicit && profile.Attribution == AttributionCausal && profile.MaxBreakpoints == 4 && profile.TTL == 5*time.Minute && profile.Rolling && !profile.RoutingKey && profile.OptimizerID == BedrockCacheOptimizerID
	case "gemini":
		return profile.Mode == ModeImplicit && profile.Attribution == AttributionOrganic && profile.MaxBreakpoints == 1 && profile.TTL == 0 && profile.Rolling && !profile.RoutingKey && profile.OptimizerID == ""
	default:
		return false
	}
}

func applyAnthropic(ctx context.Context, request NativeRequest, profile Profile) ([]byte, []string) {
	_ = ctx
	body, stable := applyAnthropicStable(request.Body)
	var ids []string
	if stable {
		ids = append(ids, profile.OptimizerID)
	}
	if withRolling, ok := appendTopLevelField(body, "cache_control", []byte(`{"type":"ephemeral"}`)); ok {
		body = withRolling
		ids = appendUnique(ids, AnthropicRollingOptimizerID)
	}
	return body, ids
}

func applyBedrock(ctx context.Context, request NativeRequest, profile Profile) ([]byte, []string) {
	_ = ctx
	body, stable := applyBedrockStable(request.Body, request.Endpoint)
	var ids []string
	if stable {
		ids = append(ids, profile.OptimizerID)
	}
	if profile.Rolling {
		if rolling, ok := appendBedrockRolling(body, request.Endpoint); ok {
			body = rolling
			ids = appendUnique(ids, BedrockRollingOptimizerID)
		}
	}
	return body, ids
}

func appendBedrockRolling(body []byte, endpoint string) ([]byte, bool) {
	var root map[string]any
	if json.Unmarshal(body, &root) != nil {
		return body, false
	}
	messages, ok := root["messages"].([]any)
	if !ok || len(messages) == 0 {
		return body, false
	}
	latest, ok := messages[len(messages)-1].(map[string]any)
	if !ok {
		return body, false
	}
	switch endpoint {
	case "converse", "converse-stream":
		content, ok := latest["content"].([]any)
		if !ok || len(content) == 0 {
			return body, false
		}
		latest["content"] = append(content, map[string]any{"cachePoint": map[string]any{"type": "default"}})
	case "invoke", "invoke-with-response-stream":
		switch content := latest["content"].(type) {
		case string:
			if content == "" {
				return body, false
			}
			latest["content"] = []any{map[string]any{
				"type": "text", "text": content,
				"cache_control": map[string]any{"type": "ephemeral"},
			}}
		case []any:
			if len(content) == 0 {
				return body, false
			}
			block, ok := content[len(content)-1].(map[string]any)
			if !ok {
				return body, false
			}
			block["cache_control"] = map[string]any{"type": "ephemeral"}
		default:
			return body, false
		}
	default:
		return body, false
	}
	out, err := json.Marshal(root)
	return out, err == nil
}

func nativeStablePrefix(request NativeRequest) ([]byte, bool) {
	root, ok := jsonsplice.Root(request.Body)
	if !ok {
		return nil, false
	}
	var fieldNames []string
	var sequence string
	switch strings.ToLower(strings.TrimSpace(request.Provider)) {
	case "anthropic":
		fieldNames, sequence = []string{"tools", "system"}, "messages"
	case "openai":
		fieldNames = []string{"tools", "instructions"}
		if strings.Contains(strings.ToLower(request.Endpoint), "responses") {
			sequence = "input"
		} else {
			sequence = "messages"
		}
	case "bedrock":
		fieldNames, sequence = []string{"toolConfig", "system"}, "messages"
	case "gemini":
		fieldNames, sequence = []string{"systemInstruction", "tools"}, "contents"
	default:
		return nil, false
	}
	prefix := appendFrame(nil, "provider", []byte(strings.ToLower(request.Provider)))
	prefix = appendFrame(prefix, "model", []byte(request.Model))
	found := false
	for _, name := range fieldNames {
		if span, exists := jsonsplice.Field(request.Body, root, name); exists {
			prefix = appendFrame(prefix, name, request.Body[span.Start:span.End])
			found = true
		}
	}
	if sequence != "" {
		if span, exists := jsonsplice.Field(request.Body, root, sequence); exists {
			if elements, valid := jsonsplice.Elements(request.Body, span); valid && len(elements) > 0 {
				leadingStable := false
				for index, element := range elements {
					role, _ := jsonsplice.StringField(request.Body, element, "role")
					if role != "system" && role != "developer" {
						break
					}
					prefix = appendFrame(prefix, sequence+"["+strconv.Itoa(index)+"]", request.Body[element.Start:element.End])
					leadingStable = true
					found = true
				}
				if !found && !leadingStable {
					prefix = appendFrame(prefix, sequence+"[0]", request.Body[elements[0].Start:elements[0].End])
					found = true
				}
			}
		}
	}
	return prefix, found
}

func cacheMarkerAt(provider string, path []string, key string) bool {
	switch provider {
	case "anthropic":
		return key == "cache_control" && (len(path) == 0 || pathMatches(path, "tools", "*") || pathMatches(path, "system", "*") || pathMatches(path, "messages", "*", "content", "*"))
	case "openai":
		return len(path) == 0 && (key == "prompt_cache_key" || key == "prompt_cache_options") || key == "prompt_cache_breakpoint" && (pathMatches(path, "messages", "*", "content", "*") || pathMatches(path, "input", "*", "content", "*"))
	case "bedrock":
		cachePath := pathMatches(path, "system", "*") || pathMatches(path, "messages", "*", "content", "*")
		return key == "cachePoint" && (cachePath || pathMatches(path, "toolConfig", "tools", "*")) || key == "cache_control" && (cachePath || pathMatches(path, "tools", "*"))
	case "gemini":
		return len(path) == 0 && (key == "cachedContent" || key == "cached_content")
	default:
		return false
	}
}

func pathMatches(path []string, expected ...string) bool {
	if len(path) != len(expected) {
		return false
	}
	for index := range path {
		if path[index] != expected[index] {
			return false
		}
	}
	return true
}

func appendTopLevelField(body []byte, name string, value []byte) ([]byte, bool) {
	root, ok := jsonsplice.Root(body)
	if !ok {
		return body, false
	}
	if _, exists := jsonsplice.Field(body, root, name); exists {
		return body, false
	}
	out, err := jsonsplice.AppendObjectFields(body, root, jsonsplice.FieldInsertion{Name: name, Value: value})
	return out, err == nil
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func containsStringValue(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func validUniqueJSONObject(body []byte) bool {
	valid, _ := inspectUniqueJSONObject(body, "")
	return valid
}

func inspectUniqueJSONObject(body []byte, provider string) (bool, bool) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	found, err := inspectUniqueJSONValue(decoder, true, 0, provider, nil)
	if err != nil {
		return false, false
	}
	_, err = decoder.Token()
	return errors.Is(err, io.EOF), found
}

func inspectUniqueJSONValue(decoder *json.Decoder, root bool, depth int, provider string, path []string) (bool, error) {
	if depth > 512 {
		return false, errors.New("cacheengine: JSON nesting limit exceeded")
	}
	token, err := decoder.Token()
	if err != nil {
		return false, err
	}
	delim, composite := token.(json.Delim)
	if !composite {
		if root {
			return false, errors.New("cacheengine: request root must be object")
		}
		return false, nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		found := false
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return false, err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return false, errors.New("cacheengine: duplicate or invalid object key")
			}
			seen[key] = true
			matched := cacheMarkerAt(provider, path, key)
			path = append(path, key)
			childFound, err := inspectUniqueJSONValue(decoder, false, depth+1, provider, path)
			path = path[:len(path)-1]
			if err != nil {
				return false, err
			}
			found = found || matched || childFound
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return false, errors.New("cacheengine: invalid object close")
		}
		return found, nil
	case '[':
		if root {
			return false, errors.New("cacheengine: request root must be object")
		}
		found := false
		path = append(path, "*")
		for decoder.More() {
			childFound, err := inspectUniqueJSONValue(decoder, false, depth+1, provider, path)
			if err != nil {
				return false, err
			}
			found = found || childFound
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return false, errors.New("cacheengine: invalid array close")
		}
		return found, nil
	default:
		return false, errors.New("cacheengine: unexpected delimiter")
	}
}
