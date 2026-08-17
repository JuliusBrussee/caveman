package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/shared/platform/env"
	"github.com/JuliusBrussee/caveman/shared/platform/redact"
)

const retrieveToolName = "caveman_retrieve"
const maxRetrieves = 3
const defaultMaxRetrieveResponseBytes = 8 << 20

var retrieveToolSchema = json.RawMessage(`{"type":"object","properties":{"handle":{"type":"string","description":"recovery handle from a <<ccr:...>> marker in compressed content"},"query":{"type":"string","description":"optional: describe the detail you need; the proxy returns only the elided sections most relevant to it instead of the entire original block"}},"required":["handle"]}`)

type retrieveTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

const retrieveToolDescription = "Recover original content for a compressed block. Use the handle shown in a <<ccr:...>> marker inside the content. Pass query only when a narrower recovery is enough."

// serverRetrieveSupported is the positive allowlist for wire grammars fully
// implemented by inject/parse/append/strip below. Unsupported routes must keep
// original bytes: sending compressed content with an unusable recovery tool is
// a correctness failure, not a graceful degradation.
func serverRetrieveSupported(provider, routePath string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	routePath = strings.ToLower(strings.TrimSpace(routePath))
	switch provider {
	case "openai", "azure_openai", "openai_compatible":
		return strings.HasSuffix(routePath, "/chat/completions") || strings.HasSuffix(routePath, "/responses")
	case "anthropic":
		return strings.HasSuffix(routePath, "/messages")
	case "gemini", "vertex":
		return strings.HasSuffix(routePath, ":generatecontent")
	default:
		return false
	}
}

func injectRetrieveTool(provider, routePath string, body []byte) ([]byte, bool) {
	if hasRetrieveTool(body) {
		return body, false
	}
	tool := retrieveTool{
		Name:        retrieveToolName,
		Description: retrieveToolDescription,
		InputSchema: retrieveToolSchema,
		Parameters:  retrieveToolSchema,
	}
	toolBytes, err := json.Marshal(providerToolSchema(provider, routePath, tool))
	if err != nil || !json.Valid(toolBytes) {
		return body, false
	}
	root, ok := gatewayRootObjectSpan(body)
	if !ok {
		return body, false
	}
	toolsSpan, hasTools := gatewayFindObjectField(body, root, "tools")
	if hasTools {
		if toolsSpan.start >= len(body) || body[toolsSpan.start] != '[' {
			return body, false
		}
		elements, ok := gatewayArrayElements(body, toolsSpan)
		if !ok {
			return body, false
		}
		insertAt := toolsSpan.end - 1
		prefix := []byte(nil)
		if len(elements) > 0 {
			prefix = []byte(",")
		}
		out := make([]byte, 0, len(body)+len(prefix)+len(toolBytes))
		out = append(out, body[:insertAt]...)
		out = append(out, prefix...)
		out = append(out, toolBytes...)
		out = append(out, body[insertAt:]...)
		return out, json.Valid(out)
	}

	insertAt := root.end - 1
	prefix := []byte(`,"tools":[`)
	if gatewayObjectEmpty(body, root) {
		prefix = []byte(`"tools":[`)
	}
	out := make([]byte, 0, len(body)+len(prefix)+len(toolBytes)+1)
	out = append(out, body[:insertAt]...)
	out = append(out, prefix...)
	out = append(out, toolBytes...)
	out = append(out, ']')
	out = append(out, body[insertAt:]...)
	if !json.Valid(out) {
		return body, false
	}
	return out, true
}

func hasRetrieveTool(body []byte) bool {
	var root map[string]any
	if json.Unmarshal(body, &root) != nil {
		return false
	}
	tools, _ := root["tools"].([]any)
	return toolNameInList(tools, retrieveToolName)
}

type gatewayJSONSpan struct {
	start int
	end   int
}

func gatewayRootObjectSpan(body []byte) (gatewayJSONSpan, bool) {
	start := gatewaySkipJSONSpace(body, 0)
	if start >= len(body) || body[start] != '{' {
		return gatewayJSONSpan{}, false
	}
	end, ok := gatewayScanJSONValue(body, start)
	if !ok {
		return gatewayJSONSpan{}, false
	}
	if gatewaySkipJSONSpace(body, end) != len(body) {
		return gatewayJSONSpan{}, false
	}
	return gatewayJSONSpan{start: start, end: end}, true
}

func gatewayObjectEmpty(body []byte, obj gatewayJSONSpan) bool {
	i := gatewaySkipJSONSpace(body, obj.start+1)
	return i < obj.end && body[i] == '}'
}

func gatewayFindObjectField(body []byte, obj gatewayJSONSpan, field string) (gatewayJSONSpan, bool) {
	if obj.start < 0 || obj.end > len(body) || obj.start >= obj.end || body[obj.start] != '{' {
		return gatewayJSONSpan{}, false
	}
	i := gatewaySkipJSONSpace(body, obj.start+1)
	for i < obj.end {
		if body[i] == '}' {
			return gatewayJSONSpan{}, false
		}
		if body[i] != '"' {
			return gatewayJSONSpan{}, false
		}
		keyStart := i
		keyEnd, ok := gatewayScanJSONString(body, keyStart)
		if !ok {
			return gatewayJSONSpan{}, false
		}
		var key string
		if json.Unmarshal(body[keyStart:keyEnd], &key) != nil {
			return gatewayJSONSpan{}, false
		}
		i = gatewaySkipJSONSpace(body, keyEnd)
		if i >= obj.end || body[i] != ':' {
			return gatewayJSONSpan{}, false
		}
		valueStart := gatewaySkipJSONSpace(body, i+1)
		valueEnd, ok := gatewayScanJSONValue(body, valueStart)
		if !ok {
			return gatewayJSONSpan{}, false
		}
		if key == field {
			return gatewayJSONSpan{start: valueStart, end: valueEnd}, true
		}
		i = gatewaySkipJSONSpace(body, valueEnd)
		if i < obj.end && body[i] == ',' {
			i = gatewaySkipJSONSpace(body, i+1)
			continue
		}
	}
	return gatewayJSONSpan{}, false
}

func gatewayArrayElements(body []byte, arr gatewayJSONSpan) ([]gatewayJSONSpan, bool) {
	if arr.start < 0 || arr.end > len(body) || arr.start >= arr.end || body[arr.start] != '[' {
		return nil, false
	}
	var out []gatewayJSONSpan
	i := gatewaySkipJSONSpace(body, arr.start+1)
	for i < arr.end {
		if body[i] == ']' {
			return out, true
		}
		valueEnd, ok := gatewayScanJSONValue(body, i)
		if !ok {
			return nil, false
		}
		out = append(out, gatewayJSONSpan{start: i, end: valueEnd})
		i = gatewaySkipJSONSpace(body, valueEnd)
		if i < arr.end && body[i] == ',' {
			i = gatewaySkipJSONSpace(body, i+1)
			continue
		}
	}
	return nil, false
}

func gatewayScanJSONValue(body []byte, start int) (int, bool) {
	i := gatewaySkipJSONSpace(body, start)
	if i >= len(body) {
		return 0, false
	}
	switch body[i] {
	case '"':
		return gatewayScanJSONString(body, i)
	case '{', '[':
		depth := 0
		for j := i; j < len(body); j++ {
			switch body[j] {
			case '"':
				end, ok := gatewayScanJSONString(body, j)
				if !ok {
					return 0, false
				}
				j = end - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return j + 1, true
				}
				if depth < 0 {
					return 0, false
				}
			}
		}
		return 0, false
	default:
		j := i
		for j < len(body) {
			switch body[j] {
			case ',', '}', ']', ' ', '\n', '\r', '\t':
				if j == i {
					return 0, false
				}
				return j, true
			default:
				j++
			}
		}
		return j, j > i
	}
}

func gatewayScanJSONString(body []byte, start int) (int, bool) {
	if start >= len(body) || body[start] != '"' {
		return 0, false
	}
	for i := start + 1; i < len(body); i++ {
		switch body[i] {
		case '\\':
			i++
		case '"':
			return i + 1, true
		}
	}
	return 0, false
}

func gatewaySkipJSONSpace(body []byte, i int) int {
	for i < len(body) {
		switch body[i] {
		case ' ', '\n', '\r', '\t':
			i++
		default:
			return i
		}
	}
	return i
}

func providerToolSchema(provider, routePath string, tool retrieveTool) any {
	if providerUsesGeminiTools(provider, routePath) {
		return map[string]any{
			"functionDeclarations": []any{map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  tool.Parameters,
			}},
		}
	}
	if providerUsesResponsesTools(provider, routePath) {
		return map[string]any{
			"type":        "function",
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  tool.Parameters,
		}
	}
	if providerUsesOpenAITools(provider) {
		return map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  tool.Parameters,
			},
		}
	}
	return map[string]any{
		"name":         tool.Name,
		"description":  tool.Description,
		"input_schema": tool.InputSchema,
	}
}

func providerUsesResponsesTools(provider, routePath string) bool {
	return providerUsesOpenAITools(provider) && strings.HasSuffix(strings.ToLower(strings.TrimSpace(routePath)), "/responses")
}

func providerUsesGeminiTools(provider, routePath string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	return (provider == "gemini" || provider == "vertex") && strings.HasSuffix(strings.ToLower(strings.TrimSpace(routePath)), ":generatecontent")
}

func providerUsesOpenAITools(provider string) bool {
	switch provider {
	case "openai", "azure_openai", "openai_compatible":
		return true
	default:
		return false
	}
}

func toolNameInList(tools []any, name string) bool {
	for _, t := range tools {
		tm, _ := t.(map[string]any)
		if tm == nil {
			continue
		}
		if n, _ := tm["name"].(string); n == name {
			return true
		}
		if fn, _ := tm["function"].(map[string]any); fn != nil {
			if n, _ := fn["name"].(string); n == name {
				return true
			}
		}
		declarations, _ := tm["functionDeclarations"].([]any)
		for _, declaration := range declarations {
			declarationMap, _ := declaration.(map[string]any)
			if n, _ := declarationMap["name"].(string); n == name {
				return true
			}
		}
	}
	return false
}

func parseRetrieveCall(provider, routePath string, respBody []byte) (callID, handle, query string, ok bool) {
	var root map[string]any
	if json.Unmarshal(respBody, &root) != nil {
		return "", "", "", false
	}
	if providerUsesGeminiTools(provider, routePath) {
		candidates, _ := root["candidates"].([]any)
		var functionCalls []map[string]any
		for _, candidate := range candidates {
			candidateMap, _ := candidate.(map[string]any)
			content, _ := candidateMap["content"].(map[string]any)
			parts, _ := content["parts"].([]any)
			for _, part := range parts {
				partMap, _ := part.(map[string]any)
				if call, _ := partMap["functionCall"].(map[string]any); call != nil {
					functionCalls = append(functionCalls, call)
				}
			}
		}
		if len(functionCalls) != 1 {
			return "", "", "", false
		}
		call := functionCalls[0]
		if name, _ := call["name"].(string); name != retrieveToolName {
			return "", "", "", false
		}
		id, _ := call["id"].(string)
		args, _ := call["args"].(map[string]any)
		handle, _ = args["handle"].(string)
		if handle == "" {
			handle, _ = args["recovery_handle"].(string)
		}
		query, _ = args["query"].(string)
		return id, handle, query, true
	}
	if providerUsesResponsesTools(provider, routePath) {
		output, _ := root["output"].([]any)
		var functionCalls []map[string]any
		for _, item := range output {
			itemMap, _ := item.(map[string]any)
			if typ, _ := itemMap["type"].(string); typ == "function_call" {
				functionCalls = append(functionCalls, itemMap)
			}
		}
		if len(functionCalls) != 1 {
			return "", "", "", false
		}
		call := functionCalls[0]
		if name, _ := call["name"].(string); name != retrieveToolName {
			return "", "", "", false
		}
		id, _ := call["call_id"].(string)
		argStr, _ := call["arguments"].(string)
		handle, query = retrieveArgs([]byte(argStr))
		return id, handle, query, id != ""
	}
	if providerUsesOpenAITools(provider) {
		choices, _ := root["choices"].([]any)
		if len(choices) == 0 {
			return "", "", "", false
		}
		first, _ := choices[0].(map[string]any)
		msg, _ := first["message"].(map[string]any)
		calls, _ := msg["tool_calls"].([]any)
		if len(calls) != 1 {
			return "", "", "", false
		}
		c, _ := calls[0].(map[string]any)
		fn, _ := c["function"].(map[string]any)
		if name, _ := fn["name"].(string); name != retrieveToolName {
			return "", "", "", false
		}
		id, _ := c["id"].(string)
		// OpenAI tool-call arguments arrive as a JSON-encoded string.
		argStr, _ := fn["arguments"].(string)
		handle, query = retrieveArgs([]byte(argStr))
		return id, handle, query, id != ""
	}
	content, _ := root["content"].([]any)
	var toolUses []map[string]any
	for _, blk := range content {
		bm, _ := blk.(map[string]any)
		if t, _ := bm["type"].(string); t == "tool_use" {
			toolUses = append(toolUses, bm)
		}
	}
	if len(toolUses) != 1 {
		return "", "", "", false
	}
	if name, _ := toolUses[0]["name"].(string); name != retrieveToolName {
		return "", "", "", false
	}
	id, _ := toolUses[0]["id"].(string)
	// Anthropic tool_use carries arguments as a parsed object under "input".
	if input, _ := toolUses[0]["input"].(map[string]any); input != nil {
		handle, _ = input["handle"].(string)
		if handle == "" {
			handle, _ = input["recovery_handle"].(string)
		}
		query, _ = input["query"].(string)
	}
	return id, handle, query, id != ""
}

// retrieveArgs pulls handle/query out of an OpenAI tool-call's JSON-string
// arguments. It is best-effort: missing fields yield empty strings.
func retrieveArgs(argBytes []byte) (handle, query string) {
	if len(argBytes) == 0 {
		return "", ""
	}
	var args map[string]any
	if json.Unmarshal(argBytes, &args) != nil {
		return "", ""
	}
	handle, _ = args["handle"].(string)
	if handle == "" {
		handle, _ = args["recovery_handle"].(string)
	}
	q, _ := args["query"].(string)
	return handle, q
}

func appendRetrieveResult(provider, routePath string, reqBody, respBody []byte, callID, recovered string) ([]byte, bool) {
	var req map[string]any
	if json.Unmarshal(reqBody, &req) != nil {
		return nil, false
	}
	var resp map[string]any
	if json.Unmarshal(respBody, &resp) != nil {
		return nil, false
	}
	if providerUsesGeminiTools(provider, routePath) {
		contents, _ := req["contents"].([]any)
		if contents == nil {
			return nil, false
		}
		candidates, _ := resp["candidates"].([]any)
		if len(candidates) == 0 {
			return nil, false
		}
		candidate, _ := candidates[0].(map[string]any)
		modelContent, _ := candidate["content"].(map[string]any)
		if modelContent == nil {
			return nil, false
		}
		contents = append(contents, modelContent)
		functionResponse := map[string]any{
			"name":     retrieveToolName,
			"response": map[string]any{"output": recovered},
		}
		if callID != "" {
			functionResponse["id"] = callID
		}
		contents = append(contents, map[string]any{
			"role":  "user",
			"parts": []any{map[string]any{"functionResponse": functionResponse}},
		})
		req["contents"] = contents
	} else if providerUsesResponsesTools(provider, routePath) {
		var input []any
		switch existing := req["input"].(type) {
		case []any:
			input = append(input, existing...)
		case string:
			input = append(input, map[string]any{"role": "user", "content": existing})
		default:
			return nil, false
		}
		output, _ := resp["output"].([]any)
		if len(output) == 0 {
			return nil, false
		}
		input = append(input, output...)
		input = append(input, map[string]any{
			"type":    "function_call_output",
			"call_id": callID,
			"output":  recovered,
		})
		req["input"] = input
	} else {
		msgs, _ := req["messages"].([]any)
		if msgs == nil {
			return nil, false
		}
		if providerUsesOpenAITools(provider) {
			choices, _ := resp["choices"].([]any)
			if len(choices) == 0 {
				return nil, false
			}
			first, _ := choices[0].(map[string]any)
			assistant := first["message"]
			if am, ok := assistant.(map[string]any); ok && am["content"] == nil {
				am["content"] = ""
			}
			msgs = append(msgs, assistant)
			msgs = append(msgs, map[string]any{"role": "tool", "tool_call_id": callID, "content": recovered})
		} else {
			msgs = append(msgs, map[string]any{"role": "assistant", "content": resp["content"]})
			msgs = append(msgs, map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": callID, "content": recovered},
			}})
		}
		req["messages"] = msgs
	}
	out, err := json.Marshal(req)
	if err != nil || !json.Valid(out) {
		return nil, false
	}
	return out, true
}

func addUsage(a, b providers.UsageObservation) providers.UsageObservation {
	aCount, bCount := a.ObservationCount, b.ObservationCount
	for _, pair := range []struct {
		dst *int
		add int
	}{
		{&a.InputTokens, b.InputTokens}, {&a.OutputTokens, b.OutputTokens},
		{&a.CachedInputTokens, b.CachedInputTokens}, {&a.CacheCreationInputTokens, b.CacheCreationInputTokens},
		{&a.CacheCreation5mTokens, b.CacheCreation5mTokens}, {&a.CacheCreation1hTokens, b.CacheCreation1hTokens},
		{&a.ReasoningTokens, b.ReasoningTokens},
	} {
		if sum, ok := checkedUsageAdd(*pair.dst, pair.add); ok {
			*pair.dst = sum
		} else {
			*pair.dst = 0
			a.Malformed = true
		}
	}
	if aCount == 0 {
		a.InputTokensReported = b.InputTokensReported
		a.OutputTokensReported = b.OutputTokensReported
	} else if bCount > 0 {
		a.InputTokensReported = a.InputTokensReported && b.InputTokensReported
		a.OutputTokensReported = a.OutputTokensReported && b.OutputTokensReported
	}
	if count, ok := checkedUsageAdd(aCount, bCount); ok {
		a.ObservationCount = count
	} else {
		a.ObservationCount = 0
		a.Malformed = true
	}
	a.Malformed = a.Malformed || b.Malformed
	a.CacheObserved = a.CacheObserved || b.CacheObserved
	a.CacheStatus = aggregateCacheStatus(a)
	if a.PricingUnsupportedReason == "" {
		a.PricingUnsupportedReason = b.PricingUnsupportedReason
	}
	mergeUsageQualifier(&a.ServiceTier, b.ServiceTier, "mixed_service_tier", &a.PricingUnsupportedReason)
	mergeUsageQualifier(&a.InferenceGeo, b.InferenceGeo, "mixed_inference_geo", &a.PricingUnsupportedReason)
	return a
}

func checkedUsageAdd(a, b int) (int, bool) {
	if a < 0 || b < 0 || a > int(^uint(0)>>1)-b {
		return 0, false
	}
	return a + b, true
}

func aggregateCacheStatus(usage providers.UsageObservation) string {
	if !usage.CacheObserved || usage.Malformed {
		return "unknown"
	}
	if usage.CachedInputTokens > 0 {
		return "hit"
	}
	if usage.CacheCreationInputTokens > 0 {
		return "write"
	}
	return "miss"
}

func mergeUsageQualifier(current *string, next, reason string, unsupported *string) {
	if *current == "" {
		*current = next
		return
	}
	if next != "" && next != *current && *unsupported == "" {
		*unsupported = reason
	}
}

func bufferedBody(resp *http.Response, body []byte) *http.Response {
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp
}

func stripRetrieveCall(provider, routePath string, respBody []byte) ([]byte, bool) {
	var root map[string]any
	if json.Unmarshal(respBody, &root) != nil {
		return respBody, false
	}
	changed := false
	if providerUsesGeminiTools(provider, routePath) {
		candidates, _ := root["candidates"].([]any)
		for _, candidate := range candidates {
			candidateMap, _ := candidate.(map[string]any)
			content, _ := candidateMap["content"].(map[string]any)
			parts, _ := content["parts"].([]any)
			kept := make([]any, 0, len(parts))
			for _, part := range parts {
				partMap, _ := part.(map[string]any)
				call, _ := partMap["functionCall"].(map[string]any)
				if name, _ := call["name"].(string); name == retrieveToolName {
					changed = true
					continue
				}
				kept = append(kept, part)
			}
			if len(kept) != len(parts) {
				if len(kept) == 0 {
					kept = append(kept, map[string]any{"text": ""})
				}
				content["parts"] = kept
			}
		}
	} else if providerUsesResponsesTools(provider, routePath) {
		output, _ := root["output"].([]any)
		kept := make([]any, 0, len(output))
		for _, item := range output {
			itemMap, _ := item.(map[string]any)
			if typ, _ := itemMap["type"].(string); typ == "function_call" {
				if name, _ := itemMap["name"].(string); name == retrieveToolName {
					changed = true
					continue
				}
			}
			kept = append(kept, item)
		}
		if changed {
			root["output"] = kept
		}
	} else if providerUsesOpenAITools(provider) {
		choices, _ := root["choices"].([]any)
		for _, ch := range choices {
			chm, _ := ch.(map[string]any)
			msg, _ := chm["message"].(map[string]any)
			if msg == nil {
				continue
			}
			calls, _ := msg["tool_calls"].([]any)
			if len(calls) == 0 {
				continue
			}
			kept := make([]any, 0, len(calls))
			for _, c := range calls {
				cm, _ := c.(map[string]any)
				fn, _ := cm["function"].(map[string]any)
				if name, _ := fn["name"].(string); name == retrieveToolName {
					changed = true
					continue
				}
				kept = append(kept, c)
			}
			if len(kept) == len(calls) {
				continue
			}
			if len(kept) == 0 {
				delete(msg, "tool_calls")
				if msg["content"] == nil {
					msg["content"] = ""
				}
				if _, ok := chm["finish_reason"]; ok {
					chm["finish_reason"] = "stop"
				}
			} else {
				msg["tool_calls"] = kept
			}
		}
	} else {
		content, _ := root["content"].([]any)
		kept := make([]any, 0, len(content))
		for _, blk := range content {
			bm, _ := blk.(map[string]any)
			if t, _ := bm["type"].(string); t == "tool_use" {
				if name, _ := bm["name"].(string); name == retrieveToolName {
					changed = true
					continue
				}
			}
			kept = append(kept, blk)
		}
		if changed {
			root["content"] = kept
			if sr, _ := root["stop_reason"].(string); sr == "tool_use" {
				root["stop_reason"] = "end_turn"
			}
		}
	}
	if !changed {
		return respBody, false
	}
	out, err := json.Marshal(root)
	if err != nil || !json.Valid(out) {
		return respBody, false
	}
	return out, true
}

func (s *Server) forwardCleaned(provider, routePath string, cur *http.Response, respBody []byte, requestID string) *http.Response {
	if cleaned, ok := stripRetrieveCall(provider, routePath, respBody); ok {
		if s.logger != nil {
			s.logger.Warn("retrieve loop ended with unresolved caveman_retrieve call; stripped it from client response", "request_id", requestID)
		}
		cur.Header.Del("Content-Length")
		return bufferedBody(cur, cleaned)
	}
	return bufferedBody(cur, respBody)
}

func retrieveFailureResponse(kind, message string) *http.Response {
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"type":    kind,
			"message": message,
		},
	})
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Status:     "502 Bad Gateway",
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	resp.Header.Set("Content-Type", "application/json")
	return resp
}

func retrieveResponseTooLarge() *http.Response {
	return retrieveFailureResponse("cave_retrieve_response_too_large", "Upstream response exceeded the local retrieve limit.")
}

func retrieveResponseReadFailed() *http.Response {
	return retrieveFailureResponse("cave_retrieve_response_read_failed", "Upstream response could not be read during local retrieval.")
}

func maxRetrieveResponseBytes() int64 {
	return int64(env.Int("CAVE_MAX_RETRIEVE_RESPONSE_BYTES", defaultMaxRetrieveResponseBytes))
}

func readRetrieveResponseBody(resp *http.Response) ([]byte, bool, error) {
	maxBytes := maxRetrieveResponseBytes()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return body, false, err
	}
	tooLarge := int64(len(body)) > maxBytes
	if tooLarge {
		// Inspection is bounded, but a large valid provider response must still
		// reach caller byte-for-byte. Prepend consumed prefix and retain original
		// closer; caller owns restored body from here.
		resp.Body = &prependReadCloser{
			Reader: io.MultiReader(bytes.NewReader(body), resp.Body),
			Closer: resp.Body,
		}
	}
	return body, tooLarge, nil
}

type prependReadCloser struct {
	io.Reader
	io.Closer
}

func allowedRetrieveHandles(handles []string) map[string]struct{} {
	allowed := make(map[string]struct{}, len(handles))
	for _, handle := range handles {
		if handle != "" {
			allowed[handle] = struct{}{}
		}
	}
	return allowed
}

func replayOriginalResponse(ctx context.Context, upstream *http.Client, reqURL *url.URL, headers http.Header, originalBody []byte, oversized *http.Response) (*http.Response, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL.String(), bytes.NewReader(originalBody))
	if err != nil {
		_ = oversized.Body.Close()
		return retrieveResponseTooLarge(), false
	}
	req.Header = headers.Clone()
	req.Header.Del("Content-Length")
	resp, err := upstream.Do(req)
	if err != nil {
		_ = oversized.Body.Close()
		return retrieveResponseTooLarge(), false
	}
	_ = oversized.Body.Close()
	return resp, true
}

func (s *Server) runRetrieveLoop(ctx context.Context, upstream *http.Client, reqURL *url.URL, headers http.Header, reqBody, originalBody []byte, handles []string, resp *http.Response, adapter providers.Adapter, provider, routePath, requestID string) (*http.Response, []providers.UsageObservation, bool, bool) {
	var calls []providers.UsageObservation
	retrieved := false
	allowedHandles := allowedRetrieveHandles(handles)
	retriever, ok := s.compressor.(Retriever)
	if !ok {
		respBody, tooLarge, err := readRetrieveResponseBody(resp)
		if tooLarge {
			if s.logger != nil {
				s.logger.Warn("retrieve loop bypassed inspection for large upstream response", "request_id", requestID, "max_bytes", maxRetrieveResponseBytes())
			}
			replayed, ok := replayOriginalResponse(ctx, upstream, reqURL, headers, originalBody, resp)
			return replayed, calls, false, ok
		}
		_ = resp.Body.Close()
		if err != nil {
			return retrieveResponseReadFailed(), calls, retrieved, false
		}
		_, _, _, retrieved = parseRetrieveCall(provider, routePath, respBody)
		return s.forwardCleaned(provider, routePath, resp, respBody, requestID), calls, retrieved, false
	}
	cur := resp
	curReq := reqBody
	for i := 0; i < maxRetrieves; i++ {
		respBody, tooLarge, err := readRetrieveResponseBody(cur)
		if tooLarge {
			if s.logger != nil {
				s.logger.Warn("retrieve loop bypassed inspection for large upstream response", "request_id", requestID, "max_bytes", maxRetrieveResponseBytes())
			}
			replayed, ok := replayOriginalResponse(ctx, upstream, reqURL, headers, originalBody, cur)
			return replayed, calls, retrieved, ok
		}
		_ = cur.Body.Close()
		if err != nil {
			return retrieveResponseReadFailed(), calls, retrieved, false
		}
		callID, handle, query, ok := parseRetrieveCall(provider, routePath, respBody)
		if !ok {
			return s.forwardCleaned(provider, routePath, cur, respBody, requestID), calls, retrieved, false
		}
		retrieved = true
		if _, allowed := allowedHandles[handle]; !allowed {
			if s.logger != nil {
				s.logger.Warn("ccr retrieve handle was not created for current request; stripping call", "request_id", requestID)
			}
			return s.forwardCleaned(provider, routePath, cur, respBody, requestID), calls, retrieved, false
		}
		// Intercepted response is a real provider call. Capture its usage before
		// any recovery/build/continuation step can fail.
		sc := adapter.NewUsageScanner(cur.Header)
		_, _ = sc.Write(respBody)
		calls = append(calls, sc.Usage())
		recovered, err := retriever.RetrieveOriginal(handle, query)
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("ccr retrieve failed; forwarding cleaned response", "error", redact.Error(err), "request_id", requestID)
			}
			return s.forwardCleaned(provider, routePath, cur, respBody, requestID), calls, retrieved, false
		}
		nextReqBody, ok := appendRetrieveResult(provider, routePath, curReq, respBody, callID, string(recovered))
		if !ok {
			return s.forwardCleaned(provider, routePath, cur, respBody, requestID), calls, retrieved, false
		}
		nextReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL.String(), bytes.NewReader(nextReqBody))
		if err != nil {
			return s.forwardCleaned(provider, routePath, cur, respBody, requestID), calls, retrieved, false
		}
		nextReq.Header = headers.Clone()
		nextResp, err := upstream.Do(nextReq)
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("ccr retrieve continuation upstream failed", "error", redact.Error(err), "request_id", requestID)
			}
			return s.forwardCleaned(provider, routePath, cur, respBody, requestID), calls, retrieved, false
		}
		cur = nextResp
		curReq = nextReqBody
	}
	respBody, tooLarge, err := readRetrieveResponseBody(cur)
	if tooLarge {
		if s.logger != nil {
			s.logger.Warn("retrieve loop bypassed final inspection for large upstream response", "request_id", requestID, "max_bytes", maxRetrieveResponseBytes())
		}
		replayed, ok := replayOriginalResponse(ctx, upstream, reqURL, headers, originalBody, cur)
		return replayed, calls, retrieved, ok
	}
	_ = cur.Body.Close()
	if err != nil {
		return retrieveResponseReadFailed(), calls, retrieved, false
	}
	return s.forwardCleaned(provider, routePath, cur, respBody, requestID), calls, retrieved, false
}
