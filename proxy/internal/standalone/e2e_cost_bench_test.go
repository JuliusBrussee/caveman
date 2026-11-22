package standalone

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/engine/tokens"
	"github.com/JuliusBrussee/caveman/mcp/codemode"
	"github.com/JuliusBrussee/caveman/proxy/internal/config"
	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
	"github.com/JuliusBrussee/caveman/shared/platform/catalog"
	"github.com/JuliusBrussee/caveman/shared/platform/cost"
	"github.com/klauspost/compress/zstd"
)

// statefulUpstream faithfully models real provider prompt caching and exact
// catalog billing across every provider supported by caveman-proxy.
//
// Caching mechanics modeled per provider:
//   - Anthropic / Vertex Claude / Bedrock Invoke: explicit cache_control
//     markers (up to 4) or top-level automatic cache_control. Only bytes up to
//     the last marked block participate in prefix caching. Cache hit occurs when
//     a prior request cached the exact same prefix bytes (stripped of
//     cache_control markers, matching Anthropic's content-keyed cache).
//   - Bedrock Converse: explicit {"cachePoint":{"type":"default"}} elements in
//     toolConfig.tools, system, or messages[].content[].
//   - OpenAI Chat / Responses / Codex / Azure OpenAI / OpenAI-Compat:
//     automatic prefix caching for prefixes >= 1024 tokens in 128-token blocks.
//     Routing shard affinity: when prompt_cache_key is present, requests share
//     one deterministic shard; without it, simulated multi-replica routing
//     misses the warm shard on 25% of turns (matching production shard drift).
//   - Gemini / Vertex Gemini: implicit prefix caching for prefixes >= 1024
//     tokens on exact serialized prefix match.
type statefulUpstream struct {
	mu      sync.Mutex
	counter tokens.Counter

	// caches maps provider+model+shard -> list of cached canonical prefix byte slices.
	caches map[string][][]byte

	// stats accumulates exact provider-side token & USD totals per scenario.
	requests       int
	inputTokens    int
	cachedTokens   int
	cacheWriteTok  int
	outputTokens   int
	totalCostUSD   float64
	inputCostUSD   float64
	cacheReadUSD   float64
	cacheWriteUSD  float64
	outputCostUSD  float64
	lastUpstream   []byte
	upstreamBodies [][]byte
	lastHeaders    http.Header
}

func newStatefulUpstream() *statefulUpstream {
	return &statefulUpstream{
		counter: tokens.Default(),
		caches:  map[string][][]byte{},
	}
}

func (u *statefulUpstream) reset() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.caches = map[string][][]byte{}
	u.requests = 0
	u.inputTokens = 0
	u.cachedTokens = 0
	u.cacheWriteTok = 0
	u.outputTokens = 0
	u.totalCostUSD = 0
	u.inputCostUSD = 0
	u.cacheReadUSD = 0
	u.cacheWriteUSD = 0
	u.outputCostUSD = 0
	u.lastUpstream = nil
	u.upstreamBodies = nil
	u.lastHeaders = nil
}

func (u *statefulUpstream) RoundTrip(r *http.Request) (*http.Response, error) {
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	_ = r.Body.Close()

	body := rawBody
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "zstd") {
		dec, derr := zstd.NewReader(bytes.NewReader(rawBody))
		if derr == nil {
			if decoded, rerr := io.ReadAll(dec); rerr == nil {
				body = decoded
			}
			dec.Close()
		}
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	u.requests++
	u.lastUpstream = append([]byte(nil), body...)
	u.upstreamBodies = append(u.upstreamBodies, append([]byte(nil), body...))
	u.lastHeaders = r.Header.Clone()

	path := r.URL.Path
	switch {
	case strings.Contains(path, "/publishers/anthropic/"):
		return u.handleAnthropicLike(r, body, "vertex", modelFromVertexPath(path))
	case strings.Contains(path, "/publishers/google/"):
		return u.handleGemini(r, body, "vertex", modelFromVertexPath(path))
	case strings.Contains(path, "/converse"):
		return u.handleBedrockConverse(r, body, modelFromBedrockPath(path))
	case strings.Contains(path, "/invoke"):
		return u.handleAnthropicLike(r, body, "bedrock", modelFromBedrockPath(path))
	case strings.HasSuffix(path, "/messages"):
		return u.handleAnthropicLike(r, body, "anthropic", "")
	case strings.Contains(path, ":generateContent") || strings.Contains(path, ":streamGenerateContent"):
		return u.handleGemini(r, body, "gemini", modelFromGeminiPath(path))
	case strings.HasSuffix(path, "/responses"):
		return u.handleOpenAIResponses(r, body)
	default:
		return u.handleOpenAIChat(r, body)
	}
}

func (u *statefulUpstream) lookupPrice(provider, model string) cost.Price {
	switch provider {
	case "bedrock":
		if p, _ := catalog.PriceForRegion("bedrock", model, "us-east-1"); p.InputPerMillion > 0 {
			return p
		}
		if p, _ := catalog.Price("bedrock", model); p.InputPerMillion > 0 {
			return p
		}
		if p, _ := catalog.Price("anthropic", "claude-sonnet-4-6"); p.InputPerMillion > 0 {
			return p
		}
	case "vertex":
		if p, _ := catalog.PriceForRegionOrAgnostic("vertex", model, "global"); p.InputPerMillion > 0 {
			return p
		}
		if strings.HasPrefix(model, "gemini-") {
			if p, _ := catalog.Price("gemini", model); p.InputPerMillion > 0 {
				return p
			}
		}
		if p, _ := catalog.Price("anthropic", "claude-sonnet-4-6"); p.InputPerMillion > 0 {
			return p
		}
	case "azure_openai", "openai_compatible":
		if p, _ := catalog.Price("openai", model); p.InputPerMillion > 0 {
			return p
		}
	default:
		if p, _ := catalog.Price(provider, model); p.InputPerMillion > 0 {
			return p
		}
	}
	return cost.Price{InputPerMillion: 3.0, OutputPerMillion: 15.0, CacheReadPerMillion: 0.30, CacheWritePerMillion: 3.75}
}

func (u *statefulUpstream) bill(provider, model string, totalIn, cachedRead, cacheWrite, outTok int) {
	price := cost.ForInputTokens(u.lookupPrice(provider, model), totalIn)
	uncached := totalIn - cachedRead - cacheWrite
	if uncached < 0 {
		uncached = 0
	}
	inUSD := float64(uncached) * price.InputPerMillion / 1e6
	readRate := price.CacheReadPerMillion
	if readRate == 0 {
		readRate = price.InputPerMillion * 0.10
	}
	writeRate := price.CacheWritePerMillion
	if writeRate == 0 {
		writeRate = price.InputPerMillion
	}
	readUSD := float64(cachedRead) * readRate / 1e6
	writeUSD := float64(cacheWrite) * writeRate / 1e6
	outUSD := float64(outTok) * price.OutputPerMillion / 1e6

	u.inputTokens += totalIn
	u.cachedTokens += cachedRead
	u.cacheWriteTok += cacheWrite
	u.outputTokens += outTok
	u.inputCostUSD += inUSD
	u.cacheReadUSD += readUSD
	u.cacheWriteUSD += writeUSD
	u.outputCostUSD += outUSD
	u.totalCostUSD += inUSD + readUSD + writeUSD + outUSD
}

// bestPrefixMatch finds the longest cached byte prefix shared with target,
// returning matched prefix token count (floored to granularity).
func (u *statefulUpstream) bestPrefixMatch(key string, target []byte, minTok, granularity int) int {
	entries := u.caches[key]
	bestBytes := 0
	for _, cached := range entries {
		n := commonBytePrefix(cached, target)
		if n > bestBytes {
			bestBytes = n
		}
	}
	if bestBytes == 0 {
		return 0
	}
	matchedTok := u.counter.Count(target[:bestBytes])
	if matchedTok < minTok {
		return 0
	}
	if granularity > 1 {
		matchedTok = (matchedTok / granularity) * granularity
	}
	return matchedTok
}

func (u *statefulUpstream) storeCachePrefix(key string, prefix []byte) {
	if len(prefix) == 0 {
		return
	}
	cp := append([]byte(nil), prefix...)
	u.caches[key] = append(u.caches[key], cp)
}

func commonBytePrefix(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

// canonicalAnthropicPrefix builds deterministic bytes for each Anthropic
// component (tools, system, messages[]) stripped of cache_control, returning
// total tokens and the exact byte slice covered by the last cache_control
// breakpoint (empty when the request carries no cache_control at all).
func (u *statefulUpstream) canonicalAnthropicPrefix(root map[string]any) (allBytes, cachedBytes []byte) {
	_, autoCache := root["cache_control"]
	var parts [][]byte
	lastMarkedIdx := -1

	if tools, ok := root["tools"]; ok {
		// Anthropic native Tool Search (`defer_loading: true`) excludes deferred tool
		// schemas from the upfront prompt until discovered via tool_search_tool.
		activeTools := tools
		if arr, ok := tools.([]any); ok {
			filtered := make([]any, 0, len(arr))
			for _, item := range arr {
				if m, ok := item.(map[string]any); ok {
					if def, _ := m["defer_loading"].(bool); def {
						continue
					}
				}
				filtered = append(filtered, item)
			}
			activeTools = filtered
		}
		clean, has := stripKeyRecursive(activeTools, "cache_control")
		b, _ := json.Marshal(clean)
		parts = append(parts, append([]byte("tools:"), b...))
		if has {
			lastMarkedIdx = len(parts) - 1
		}
	}
	if sys, ok := root["system"]; ok {
		clean, has := stripKeyRecursive(sys, "cache_control")
		b, _ := json.Marshal(clean)
		parts = append(parts, append([]byte("system:"), b...))
		if has {
			lastMarkedIdx = len(parts) - 1
		}
	}
	if msgs, ok := root["messages"].([]any); ok {
		for _, m := range msgs {
			clean, has := stripKeyRecursive(m, "cache_control")
			b, _ := json.Marshal(clean)
			parts = append(parts, append([]byte("msg:"), b...))
			if has {
				lastMarkedIdx = len(parts) - 1
			}
		}
	}
	if autoCache && len(parts) > 0 {
		lastMarkedIdx = len(parts) - 1
	}
	allBytes = bytes.Join(parts, []byte("\n"))
	if lastMarkedIdx >= 0 {
		cachedBytes = bytes.Join(parts[:lastMarkedIdx+1], []byte("\n"))
	}
	return allBytes, cachedBytes
}

func (u *statefulUpstream) canonicalBedrockConversePrefix(root map[string]any) (allBytes, cachedBytes []byte) {
	var parts [][]byte
	lastMarkedIdx := -1

	if tc, ok := root["toolConfig"]; ok {
		clean, has := stripConverseCachePoints(tc)
		b, _ := json.Marshal(clean)
		parts = append(parts, append([]byte("tools:"), b...))
		if has {
			lastMarkedIdx = len(parts) - 1
		}
	}
	if sys, ok := root["system"]; ok {
		clean, has := stripConverseCachePoints(sys)
		b, _ := json.Marshal(clean)
		parts = append(parts, append([]byte("system:"), b...))
		if has {
			lastMarkedIdx = len(parts) - 1
		}
	}
	if msgs, ok := root["messages"].([]any); ok {
		for _, m := range msgs {
			clean, has := stripConverseCachePoints(m)
			b, _ := json.Marshal(clean)
			parts = append(parts, append([]byte("msg:"), b...))
			if has {
				lastMarkedIdx = len(parts) - 1
			}
		}
	}
	allBytes = bytes.Join(parts, []byte("\n"))
	if lastMarkedIdx >= 0 {
		cachedBytes = bytes.Join(parts[:lastMarkedIdx+1], []byte("\n"))
	}
	return allBytes, cachedBytes
}

func stripKeyRecursive(v any, drop string) (any, bool) {
	switch node := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(node))
		found := false
		for k, val := range node {
			if k == drop {
				found = true
				continue
			}
			c, h := stripKeyRecursive(val, drop)
			out[k] = c
			found = found || h
		}
		return out, found
	case []any:
		out := make([]any, len(node))
		found := false
		for i, val := range node {
			c, h := stripKeyRecursive(val, drop)
			out[i] = c
			found = found || h
		}
		return out, found
	default:
		return v, false
	}
}

func stripConverseCachePoints(v any) (any, bool) {
	switch node := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(node))
		found := false
		for k, val := range node {
			if k == "cachePoint" {
				found = true
				continue
			}
			c, h := stripConverseCachePoints(val)
			out[k] = c
			found = found || h
		}
		return out, found
	case []any:
		out := make([]any, 0, len(node))
		found := false
		for _, val := range node {
			if m, ok := val.(map[string]any); ok {
				if _, isCP := m["cachePoint"]; isCP && len(m) == 1 {
					found = true
					continue
				}
			}
			c, h := stripConverseCachePoints(val)
			out = append(out, c)
			found = found || h
		}
		return out, found
	default:
		return v, false
	}
}

func (u *statefulUpstream) handleAnthropicLike(r *http.Request, body []byte, provider, pathModel string) (*http.Response, error) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return jsonResponse(r, http.StatusBadRequest, `{"type":"error","error":{"type":"invalid_request_error","message":"bad json"}}`), nil
	}
	model, _ := root["model"].(string)
	if pathModel != "" {
		model = pathModel
	}
	if model == "" {
		model = "claude-sonnet-5-5"
	}
	allBytes, cachedBytes := u.canonicalAnthropicPrefix(root)
	totalIn := u.counter.Count(allBytes)
	if totalIn < 1 {
		totalIn = 1
	}
	// Faithfully model Anthropic's server-side cache key: changing TOP-LEVEL
	// output_config.effort invalidates the prompt cache, whereas append-only
	// per-message {"role":"system","content":[],"output_config":{"effort":...}}
	// inside messages[] preserves the cached prefix!
	topEffort := ""
	if oc, ok := root["output_config"].(map[string]any); ok {
		topEffort, _ = oc["effort"].(string)
	}
	cacheKey := provider + ":" + model + ":eff=" + topEffort
	var cacheRead, cacheWrite int
	if len(cachedBytes) > 0 {
		cacheableTok := u.counter.Count(cachedBytes)
		if cacheableTok >= 512 {
			cacheRead = u.bestPrefixMatch(cacheKey, cachedBytes, 512, 1)
			if cacheRead > cacheableTok {
				cacheRead = cacheableTok
			}
			cacheWrite = cacheableTok - cacheRead
			if cacheWrite > 0 {
				u.storeCachePrefix(cacheKey, cachedBytes)
			}
		}
	}
	uncached := totalIn - cacheRead - cacheWrite
	if uncached < 0 {
		uncached = 0
	}
	outTok := 120
	if mt, ok := root["max_tokens"].(float64); ok && mt == 0 {
		outTok = 0
	}
	u.bill(provider, model, totalIn, cacheRead, cacheWrite, outTok)

	respBody := fmt.Sprintf(`{"id":"msg_%d","type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d,"output_tokens":%d}}`,
		u.requests, model, uncached, cacheRead, cacheWrite, outTok)
	return jsonResponse(r, http.StatusOK, respBody), nil
}

func (u *statefulUpstream) handleBedrockConverse(r *http.Request, body []byte, model string) (*http.Response, error) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return jsonResponse(r, http.StatusBadRequest, `{"message":"bad json"}`), nil
	}
	if model == "" {
		model = "global.anthropic.claude-sonnet-4-6"
	}
	allBytes, cachedBytes := u.canonicalBedrockConversePrefix(root)
	totalIn := u.counter.Count(allBytes)
	if totalIn < 1 {
		totalIn = 1
	}
	cacheKey := "bedrock:" + model
	var cacheRead, cacheWrite int
	if len(cachedBytes) > 0 {
		cacheableTok := u.counter.Count(cachedBytes)
		if cacheableTok >= 512 {
			cacheRead = u.bestPrefixMatch(cacheKey, cachedBytes, 512, 1)
			if cacheRead > cacheableTok {
				cacheRead = cacheableTok
			}
			cacheWrite = cacheableTok - cacheRead
			if cacheWrite > 0 {
				u.storeCachePrefix(cacheKey, cachedBytes)
			}
		}
	}
	uncached := totalIn - cacheRead - cacheWrite
	if uncached < 0 {
		uncached = 0
	}
	outTok := 120
	u.bill("bedrock", model, totalIn, cacheRead, cacheWrite, outTok)

	respBody := fmt.Sprintf(`{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{"inputTokens":%d,"outputTokens":%d,"totalTokens":%d,"cacheReadInputTokens":%d,"cacheWriteInputTokens":%d}}`,
		uncached, outTok, totalIn+outTok, cacheRead, cacheWrite)
	return jsonResponse(r, http.StatusOK, respBody), nil
}

func (u *statefulUpstream) canonicalOpenAIPrefix(root map[string]any) []byte {
	var parts [][]byte
	for _, k := range []string{"instructions", "tools", "messages", "input"} {
		if v, ok := root[k]; ok {
			val := v
			if k == "tools" {
				// OpenAI Responses native Tool Search (`defer_loading: true`) excludes
				// deferred function schemas from upfront prompt tokens until loaded.
				if arr, ok := v.([]any); ok {
					filtered := make([]any, 0, len(arr))
					for _, item := range arr {
						if m, ok := item.(map[string]any); ok {
							if def, _ := m["defer_loading"].(bool); def {
								continue
							}
						}
						filtered = append(filtered, item)
					}
					val = filtered
				}
			}
			b, _ := json.Marshal(val)
			parts = append(parts, append([]byte(k+":"), b...))
		}
	}
	return bytes.Join(parts, []byte("\n"))
}

func (u *statefulUpstream) openAICacheCalc(provider, model string, root map[string]any) (totalIn, cacheRead, cacheWrite int) {
	allBytes := u.canonicalOpenAIPrefix(root)
	totalIn = u.counter.Count(allBytes)
	if totalIn < 1 {
		totalIn = 1
	}
	shard := "unkeyed"
	if pck, ok := root["prompt_cache_key"].(string); ok && pck != "" {
		shard = "key:" + pck
	} else if u.requests%4 == 0 {
		// Without prompt_cache_key, multi-replica routing misses the warm shard
		// periodically (simulated 25% shard miss rate).
		shard = fmt.Sprintf("miss-%d", u.requests)
	}
	// Faithfully model OpenAI's server-side cache key: mutating TOP-LEVEL
	// reasoning.effort / reasoning_effort busts the prompt cache, while
	// append-only GPT-6 configuration_update items inside input[] preserve it!
	topEffort := ""
	if rObj, ok := root["reasoning"].(map[string]any); ok {
		topEffort, _ = rObj["effort"].(string)
	} else if re, ok := root["reasoning_effort"].(string); ok {
		topEffort = re
	}
	cacheKey := provider + ":" + model + ":" + shard + ":eff=" + topEffort
	if totalIn >= 1024 {
		cacheRead = u.bestPrefixMatch(cacheKey, allBytes, 1024, 128)
		if cacheRead > totalIn {
			cacheRead = totalIn
		}
		// GPT-5.6 / GPT-6.x bill cache_write_tokens for newly cached prefix blocks.
		p := u.lookupPrice(provider, model)
		if p.CacheWritePerMillion > p.InputPerMillion {
			writtenBlocks := ((totalIn - cacheRead) / 128) * 128
			if writtenBlocks > 0 {
				cacheWrite = writtenBlocks
			}
		}
		u.storeCachePrefix(cacheKey, allBytes)
	}
	return totalIn, cacheRead, cacheWrite
}

func (u *statefulUpstream) handleOpenAIChat(r *http.Request, body []byte) (*http.Response, error) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return jsonResponse(r, http.StatusBadRequest, `{"error":{"message":"bad json"}}`), nil
	}
	model, _ := root["model"].(string)
	if model == "" {
		model = "gpt-6-astra"
	}
	provider := "openai"
	if strings.Contains(r.URL.Path, "/deployments/") {
		provider = "azure_openai"
	}
	totalIn, cacheRead, cacheWrite := u.openAICacheCalc(provider, model, root)
	outTok := 120
	u.bill(provider, model, totalIn, cacheRead, cacheWrite, outTok)

	respBody := fmt.Sprintf(`{"id":"chatcmpl_%d","object":"chat.completion","model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d,"prompt_tokens_details":{"cached_tokens":%d,"cache_write_tokens":%d}}}`,
		u.requests, model, totalIn, outTok, totalIn+outTok, cacheRead, cacheWrite)
	return jsonResponse(r, http.StatusOK, respBody), nil
}

func (u *statefulUpstream) handleOpenAIResponses(r *http.Request, body []byte) (*http.Response, error) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return jsonResponse(r, http.StatusBadRequest, `{"error":{"message":"bad json"}}`), nil
	}
	model, _ := root["model"].(string)
	if model == "" {
		model = "gpt-6.1-sol"
	}
	totalIn, cacheRead, cacheWrite := u.openAICacheCalc("openai", model, root)
	outTok := 120
	u.bill("openai", model, totalIn, cacheRead, cacheWrite, outTok)

	if stream, _ := root["stream"].(bool); stream {
		sse := fmt.Sprintf("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_%d\",\"model\":%q,\"status\":\"completed\",\"usage\":{\"input_tokens\":%d,\"output_tokens\":%d,\"total_tokens\":%d,\"input_tokens_details\":{\"cached_tokens\":%d,\"cache_write_tokens\":%d}}}}\n\n",
			u.requests, model, totalIn, outTok, totalIn+outTok, cacheRead, cacheWrite)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Request:    r,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(sse)),
		}, nil
	}
	respBody := fmt.Sprintf(`{"id":"resp_%d","object":"response","model":%q,"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":%d,"output_tokens":%d,"total_tokens":%d,"input_tokens_details":{"cached_tokens":%d,"cache_write_tokens":%d}}}`,
		u.requests, model, totalIn, outTok, totalIn+outTok, cacheRead, cacheWrite)
	return jsonResponse(r, http.StatusOK, respBody), nil
}

func (u *statefulUpstream) handleGemini(r *http.Request, body []byte, provider, model string) (*http.Response, error) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return jsonResponse(r, http.StatusBadRequest, `{"error":{"message":"bad json"}}`), nil
	}
	if model == "" {
		model = "gemini-2.5-pro"
	}
	var parts [][]byte
	for _, k := range []string{"systemInstruction", "tools", "contents"} {
		if v, ok := root[k]; ok {
			b, _ := json.Marshal(v)
			parts = append(parts, append([]byte(k+":"), b...))
		}
	}
	allBytes := bytes.Join(parts, []byte("\n"))
	totalIn := u.counter.Count(allBytes)
	if totalIn < 1 {
		totalIn = 1
	}
	cacheKey := provider + ":" + model
	var cacheRead int
	if totalIn >= 1024 {
		cacheRead = u.bestPrefixMatch(cacheKey, allBytes, 1024, 1)
		if cacheRead > totalIn {
			cacheRead = totalIn
		}
		u.storeCachePrefix(cacheKey, allBytes)
	}
	outTok := 120
	u.bill(provider, model, totalIn, cacheRead, 0, outTok)

	payload := fmt.Sprintf(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":%d,"candidatesTokenCount":%d,"totalTokenCount":%d,"cachedContentTokenCount":%d}}`,
		totalIn, outTok, totalIn+outTok, cacheRead)
	if strings.Contains(r.URL.Path, ":streamGenerateContent") {
		sse := "data: " + payload + "\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Request:    r,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(sse)),
		}, nil
	}
	return jsonResponse(r, http.StatusOK, payload), nil
}

func jsonResponse(r *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Request:    r,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func modelFromGeminiPath(path string) string {
	idx := strings.Index(path, "/models/")
	if idx < 0 {
		return ""
	}
	rest := path[idx+len("/models/"):]
	if colon := strings.IndexByte(rest, ':'); colon > 0 {
		return rest[:colon]
	}
	return rest
}

func modelFromBedrockPath(path string) string {
	idx := strings.Index(path, "/model/")
	if idx < 0 {
		return ""
	}
	rest := path[idx+len("/model/"):]
	if slash := strings.IndexByte(rest, '/'); slash > 0 {
		return rest[:slash]
	}
	return rest
}

func modelFromVertexPath(path string) string {
	return modelFromGeminiPath(path)
}

// --- Realistic coding-agent tool outputs (modeled on real Claude/Codex sessions) ---

func makeRealisticToolPayloads() []struct {
	name    string
	content string
} {
	// 1. Claude Code Read tool output with Unicode arrow gutter (1→...) — Go file
	var arrowGo strings.Builder
	arrowGo.WriteString("     1→package service\n     2→\n     3→import (\n     4→\t\"context\"\n     5→\t\"fmt\"\n     6→\t\"time\"\n     7→)\n     8→\n")
	for i := 0; i < 18; i++ {
		ln := 9 + i*8
		fmt.Fprintf(&arrowGo, "%6d→func HandleTask%d(ctx context.Context, id string, retry int) error {\n", ln, i)
		fmt.Fprintf(&arrowGo, "%6d→\tstart := time.Now()\n", ln+1)
		fmt.Fprintf(&arrowGo, "%6d→\tif id == \"\" {\n", ln+2)
		fmt.Fprintf(&arrowGo, "%6d→\t\treturn fmt.Errorf(\"empty id for task %d\")\n", ln+3, i)
		fmt.Fprintf(&arrowGo, "%6d→\t}\n", ln+4)
		fmt.Fprintf(&arrowGo, "%6d→\t_ = ctx\n", ln+5)
		fmt.Fprintf(&arrowGo, "%6d→\t_ = time.Since(start) + time.Duration(retry)\n", ln+6)
		fmt.Fprintf(&arrowGo, "%6d→\treturn nil\n", ln+7)
		fmt.Fprintf(&arrowGo, "%6d→}\n", ln+8)
	}

	// 2. Large JSON API payload (60 repetitive records + 2 error records)
	type order struct {
		ID      string `json:"order_id"`
		State   string `json:"state"`
		Region  string `json:"region"`
		Amount  string `json:"amount"`
		Created string `json:"created_at"`
		Note    string `json:"note"`
	}
	orders := make([]order, 0, 60)
	for i := 0; i < 60; i++ {
		st := "fulfilled"
		note := "standard delivery completed on schedule"
		if i == 23 || i == 47 {
			st = "error"
			note = "payment gateway timeout after 30s"
		}
		orders = append(orders, order{
			ID:      fmt.Sprintf("ord-%04d", 1000+i),
			State:   st,
			Region:  "us-west-2",
			Amount:  fmt.Sprintf("%d.00", 20+i),
			Created: fmt.Sprintf("2026-10-08T10:%02d:00Z", i),
			Note:    note,
		})
	}
	ordersJSON, _ := json.MarshalIndent(map[string]any{"orders": orders, "total": len(orders)}, "", "  ")

	// 3. Large `go test -v ./...` plain text output (80 passing tests + 1 failure)
	var goTestOut strings.Builder
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&goTestOut, "=== RUN   TestCacheInvariantCase%02d\n--- PASS: TestCacheInvariantCase%02d (0.01s)\n", i, i)
	}
	goTestOut.WriteString("=== RUN   TestBillingPrecisionOverflow\n    billing_test.go:142: got 10.0001 want 10.0000\n--- FAIL: TestBillingPrecisionOverflow (0.04s)\nFAIL\nFAIL\tgithub.com/example/app/billing\t1.24s\n")

	// 4. Large `git diff` with wide unchanged context
	var diffOut strings.Builder
	diffOut.WriteString("diff --git a/internal/proxy/handler.go b/internal/proxy/handler.go\nindex 1111111..2222222 100644\n--- a/internal/proxy/handler.go\n+++ b/internal/proxy/handler.go\n@@ -10,45 +10,47 @@ func Serve(req Request) Response {\n")
	for i := 0; i < 18; i++ {
		fmt.Fprintf(&diffOut, " \tstep%d := validateField(req, %d)\n", i, i)
	}
	diffOut.WriteString("-\ttimeout := 5 * time.Second\n+\ttimeout := 15 * time.Second\n")
	for i := 18; i < 36; i++ {
		fmt.Fprintf(&diffOut, " \tstep%d := finalizeField(req, %d)\n", i, i)
	}

	// 5. Structured application log (70 repetitive INFO lines + 2 ERROR lines)
	var logOut strings.Builder
	for i := 0; i < 70; i++ {
		if i == 31 || i == 58 {
			fmt.Fprintf(&logOut, "2026-10-08T12:00:%02dZ ERROR worker=%d job=sync-%04d err=connection_reset retries=3\n", i%60, i%4, 2000+i)
		} else {
			fmt.Fprintf(&logOut, "2026-10-08T12:00:%02dZ INFO worker=%d job=sync-%04d status=ok latency_ms=%d\n", i%60, i%4, 2000+i, 12+i%7)
		}
	}

	// 6. Grep / rg search results (55 hits across files)
	var grepOut strings.Builder
	for i := 0; i < 55; i++ {
		fmt.Fprintf(&grepOut, "proxy/internal/gateway/handler_%02d.go:%d: \tstatus := checkPrefixCache(ctx, reqID_%d)\n", i%5, 100+i*3, i)
	}

	// 7. Terminal output with ANSI escape codes & progress bar redraws
	var termOut strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&termOut, "\x1b[32m[build]\x1b[0m compiling module_%02d: 10%%\r\x1b[32m[build]\x1b[0m compiling module_%02d: 50%%\r\x1b[32m[build]\x1b[0m compiling module_%02d: 100%% done\n", i, i, i)
	}
	termOut.WriteString("\x1b[31mERROR:\x1b[0m undefined symbol CachePlan in module_39.go:88\n")

	// 8. `ls -la` / directory listing (60 files)
	var lsOut strings.Builder
	lsOut.WriteString("total 480\n")
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&lsOut, "-rw-r--r--  1 dev  staff  %5d Oct  8 10:%02d handler_part_%02d.go\n", 1024+i*128, i%60, i)
	}

	return []struct {
		name    string
		content string
	}{
		{"read_arrow_go", arrowGo.String()},
		{"json_orders", string(ordersJSON)},
		{"go_test_v", goTestOut.String()},
		{"git_diff", diffOut.String()},
		{"structured_log", logOut.String()},
		{"grep_results", grepOut.String()},
		{"terminal_ansi", termOut.String()},
		{"ls_la", lsOut.String()},
	}
}

func makeVerboseSystemPrompt() string {
	// ~1800 tokens stable system prompt (well above 1024-token prompt cache floor)
	var b strings.Builder
	b.WriteString("You are an expert autonomous software engineering assistant working inside a large Go and TypeScript repository. Follow all repository conventions carefully.\n\n")
	for i := 0; i < 36; i++ {
		fmt.Fprintf(&b, "Guideline %02d: Always preserve byte safety, verify invariants with unit tests, avoid modifying unrelated subsystems, keep error messages actionable, and check edge cases for module subsystem_%02d.\n", i+1, i+1)
	}
	return b.String()
}

func makeAnnotatedToolCatalogAnthropic() []map[string]any {
	tools := make([]map[string]any, 0, 10)
	for i := 0; i < 9; i++ {
		tools = append(tools, map[string]any{
			"name":        fmt.Sprintf("repo_tool_%02d", i),
			"description": fmt.Sprintf("Inspect or search repository artifact %02d safely.", i),
			"input_schema": map[string]any{
				"$schema":    "https://json-schema.org/draft/2020-12/schema",
				"title":      fmt.Sprintf("RepoTool%02dInputParameters", i),
				"deprecated": false,
				"type":       "object",
				"examples": []map[string]any{
					{"path": fmt.Sprintf("pkg/service_%02d/handler.go", i), "limit": 100, "recursive": true},
					{"path": fmt.Sprintf("pkg/service_%02d/handler_test.go", i), "limit": 50, "recursive": false},
				},
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"title":       "TargetFilePath",
						"description": "Relative path inside repository.",
						"examples":    []string{"src/index.ts", "proxy/internal/gateway/proxy.go"},
					},
					"limit": map[string]any{
						"type":       "integer",
						"title":      "MaximumReturnedLines",
						"deprecated": false,
						"examples":   []int{50, 100, 200},
					},
					"recursive": map[string]any{
						"type":     "boolean",
						"title":    "RecursiveDirectoryTraversal",
						"examples": []bool{true, false},
					},
				},
				"required": []string{"path"},
			},
		})
	}
	tools = append(tools, map[string]any{
		"name":        "mcp__caveman__caveman_retrieve",
		"description": "Recover original content for a compressed block using its <<ccr:...>> handle.",
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"handle": map[string]any{"type": "string"},
				"query":  map[string]any{"type": "string"},
			},
			"required": []string{"handle"},
		},
	})
	return tools
}

func makeAnnotatedToolCatalogOpenAI() []map[string]any {
	anth := makeAnnotatedToolCatalogAnthropic()
	out := make([]map[string]any, 0, len(anth))
	for _, t := range anth {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t["name"],
				"description": t["description"],
				"parameters":  t["input_schema"],
			},
		})
	}
	return out
}

func makeAnnotatedToolCatalogResponses() []map[string]any {
	anth := makeAnnotatedToolCatalogAnthropic()
	out := make([]map[string]any, 0, len(anth))
	for _, t := range anth {
		out = append(out, map[string]any{
			"type":        "function",
			"name":        t["name"],
			"description": t["description"],
			"parameters":  t["input_schema"],
		})
	}
	return out
}

func makeAnnotatedToolCatalogGemini() []map[string]any {
	anth := makeAnnotatedToolCatalogAnthropic()
	decls := make([]map[string]any, 0, len(anth))
	for _, t := range anth {
		decls = append(decls, map[string]any{
			"name":        t["name"],
			"description": t["description"],
			"parameters":  t["input_schema"],
		})
	}
	return []map[string]any{{"functionDeclarations": decls}}
}

func makeBedrockConverseTools() map[string]any {
	anth := makeAnnotatedToolCatalogAnthropic()
	specs := make([]map[string]any, 0, len(anth))
	for _, t := range anth {
		specs = append(specs, map[string]any{
			"toolSpec": map[string]any{
				"name":        t["name"],
				"description": t["description"],
				"inputSchema": map[string]any{"json": t["input_schema"]},
			},
		})
	}
	return map[string]any{"tools": specs}
}

type scenarioMetrics struct {
	name          string
	provider      string
	baselineUSD   float64
	optimizedUSD  float64
	savedUSD      float64
	savedPct      float64
	baseInTok     int
	optInTok      int
	optCachedTok  int
	optWriteTok   int
	cavemanBusts  int
	recoveredOK   int
	recoveredFail int
}

type e2eRecordingSink struct {
	mu   sync.Mutex
	rows []gateway.RequestRecord
}

func (s *e2eRecordingSink) Record(r gateway.RequestRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, r)
}

func (s *e2eRecordingSink) cavemanBustCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.rows {
		if r.CacheBustCause == "caveman" {
			n++
		}
	}
	return n
}

func buildBenchServer(t *testing.T, mode string, upstream *statefulUpstream, sink gateway.TelemetrySink) (*gateway.Server, *ccr.Store, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ccrStore, err := ccr.Open(filepath.Join(dir, "ccr.db"))
	if err != nil {
		t.Fatalf("open ccr: %v", err)
	}
	t.Cleanup(func() { _ = ccrStore.Close() })

	cfg := config.Config{
		Label:          "e2e-bench",
		Mode:           mode,
		Listen:         "127.0.0.1:8787",
		BreakpointPlan: "frontier",
		Optimizers: map[string]bool{
			"anthropic-cache-breakpoints": true,
			"openai-prompt-cache-key":     true,
			"bedrock-cache-points":        true,
		},
		Providers: map[string]config.ProviderConfig{
			"anthropic":    {BaseURL: "https://api.anthropic.com"},
			"openai":       {BaseURL: "https://api.openai.com"},
			"gemini":       {BaseURL: "https://generativelanguage.googleapis.com", BillingTier: "paid"},
			"bedrock":      {BaseURL: "https://bedrock-runtime.us-east-1.amazonaws.com", Region: "us-east-1"},
			"azure_openai": {BaseURL: "https://demo-resource.openai.azure.com"},
			"vertex":       {BaseURL: "https://global-aiplatform.googleapis.com"},
		},
		Compat: map[string]config.CompatConfig{
			"openrouter":   {BaseURL: "https://openrouter.ai/api/v1"},
			"anthropic-gw": {BaseURL: "https://gw.example.com", WireDialect: "anthropic"},
		},
	}
	var comp gateway.Compressor
	if mode == "compress" {
		comp = NewEngineCompressor(ccrStore)
	}
	srv := New(cfg, sink, Options{
		HTTPClient:     &http.Client{Transport: upstream},
		Compressor:     comp,
		PrefixCache:    db,
		RecoveryViaMCP: true,
	})
	return srv, ccrStore, db
}

func sendJSON(t *testing.T, srv *gateway.Server, path string, headers map[string]string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST %s returned %d: %s", path, rec.Code, rec.Body.String())
	}
	return rec
}

func sendZstdJSON(t *testing.T, srv *gateway.Server, path string, headers map[string]string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	compressed := enc.EncodeAll(raw, nil)
	_ = enc.Close()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(compressed))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "zstd")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST zstd %s returned %d: %s", path, rec.Code, rec.Body.String())
	}
	return rec
}

// verifyCCRHandles extracts every <<ccr:...>> handle from the last upstream
// body and confirms every single one resolves cleanly in ccrStore.
func verifyCCRHandles(ccrStore *ccr.Store, bodies [][]byte) (okCount, failCount int) {
	if ccrStore == nil {
		return 0, 0
	}
	seen := map[string]bool{}
	for _, b := range bodies {
		s := string(b)
		for {
			idx := strings.Index(s, "<<ccr:")
			if idx < 0 {
				break
			}
			s = s[idx+len("<<ccr:"):]
			end := strings.Index(s, ">>")
			if end < 0 {
				break
			}
			h := s[:end]
			s = s[end+2:]
			if seen[h] {
				continue
			}
			seen[h] = true
			if orig, err := ccrStore.Get(h); err == nil && len(orig) > 0 {
				okCount++
			} else {
				failCount++
			}
		}
	}
	return okCount, failCount
}

// TestE2EProxyCostAcrossAllProviders runs a multi-turn coding-agent conversation
// (with parallel tool calls, diverse real-world tool outputs, system prompt, and
// tool catalog) through the REAL standalone proxy + REAL engine + REAL SQLite
// PrefixCache + REAL CCR store across EVERY supported provider, comparing
// record (baseline) vs compress (optimized) in total multi-turn USD cost.
func TestE2EProxyCostAcrossAllProviders(t *testing.T) {
	payloads := makeRealisticToolPayloads()
	sysPrompt := makeVerboseSystemPrompt()

	type runner func(t *testing.T, srv *gateway.Server)

	scenarios := []struct {
		name     string
		provider string
		run      runner
	}{
		{
			name:     "Anthropic_ClaudeCode_Cached",
			provider: "anthropic",
			run: func(t *testing.T, srv *gateway.Server) {
				tools := makeAnnotatedToolCatalogAnthropic()
				tools[len(tools)-1]["cache_control"] = map[string]any{"type": "ephemeral"}
				sys := []map[string]any{{"type": "text", "text": sysPrompt, "cache_control": map[string]any{"type": "ephemeral"}}}
				msgs := []map[string]any{
					{"role": "user", "content": []map[string]any{{"type": "text", "text": "Investigate the billing timeout, check test failures and diff."}}},
				}
				hdrs := map[string]string{"x-api-key": "sk-ant-api03-bench", "anthropic-version": "2023-06-01", "x-cave-session": "sess-anth-cached"}
				// Turn 0
				sendJSON(t, srv, "/v1/messages", hdrs, map[string]any{
					"model": "claude-opus-5-5", "max_tokens": 1024, "system": sys, "tools": tools, "messages": msgs,
				})
				// 4 multi-turn steps, 2 parallel tool outputs each (8 payloads total)
				for step := 0; step < 4; step++ {
					p1, p2 := payloads[step*2], payloads[step*2+1]
					msgs = append(msgs, map[string]any{
						"role": "assistant",
						"content": []map[string]any{
							{"type": "tool_use", "id": fmt.Sprintf("tu_%d_a", step), "name": "Bash", "input": map[string]any{"cmd": p1.name}},
							{"type": "tool_use", "id": fmt.Sprintf("tu_%d_b", step), "name": "Bash", "input": map[string]any{"cmd": p2.name}},
						},
					})
					// Clear old cache_control markers on earlier messages (Claude Code rolling breakpoint)
					for i := range msgs {
						if arr, ok := msgs[i]["content"].([]map[string]any); ok {
							for j := range arr {
								delete(arr[j], "cache_control")
							}
						}
					}
					msgs = append(msgs, map[string]any{
						"role": "user",
						"content": []map[string]any{
							{"type": "tool_result", "tool_use_id": fmt.Sprintf("tu_%d_a", step), "content": p1.content},
							{"type": "tool_result", "tool_use_id": fmt.Sprintf("tu_%d_b", step), "content": p2.content, "cache_control": map[string]any{"type": "ephemeral"}},
						},
					})
					sendJSON(t, srv, "/v1/messages", hdrs, map[string]any{
						"model": "claude-opus-5-5", "max_tokens": 1024, "system": sys, "tools": tools, "messages": msgs,
					})
				}
			},
		},
		{
			name:     "Anthropic_Unmanaged_PAYG",
			provider: "anthropic",
			run: func(t *testing.T, srv *gateway.Server) {
				tools := makeAnnotatedToolCatalogAnthropic()
				msgs := []map[string]any{
					{"role": "user", "content": []map[string]any{{"type": "text", "text": "Investigate the billing timeout, check test failures and diff."}}},
				}
				hdrs := map[string]string{"x-api-key": "sk-ant-api03-bench", "anthropic-version": "2023-06-01", "x-cave-session": "sess-anth-unmanaged"}
				sendJSON(t, srv, "/v1/messages", hdrs, map[string]any{
					"model": "claude-opus-5-5", "max_tokens": 1024, "system": sysPrompt, "tools": tools, "messages": msgs,
				})
				for step := 0; step < 4; step++ {
					p1, p2 := payloads[step*2], payloads[step*2+1]
					msgs = append(msgs, map[string]any{
						"role": "assistant",
						"content": []map[string]any{
							{"type": "tool_use", "id": fmt.Sprintf("tu_%d_a", step), "name": "Bash", "input": map[string]any{"cmd": p1.name}},
							{"type": "tool_use", "id": fmt.Sprintf("tu_%d_b", step), "name": "Bash", "input": map[string]any{"cmd": p2.name}},
						},
					})
					msgs = append(msgs, map[string]any{
						"role": "user",
						"content": []map[string]any{
							{"type": "tool_result", "tool_use_id": fmt.Sprintf("tu_%d_a", step), "content": p1.content},
							{"type": "tool_result", "tool_use_id": fmt.Sprintf("tu_%d_b", step), "content": p2.content},
						},
					})
					sendJSON(t, srv, "/v1/messages", hdrs, map[string]any{
						"model": "claude-opus-5-5", "max_tokens": 1024, "system": sysPrompt, "tools": tools, "messages": msgs,
					})
				}
			},
		},
		{
			name:     "OpenAI_Chat_ParallelTools",
			provider: "openai",
			run: func(t *testing.T, srv *gateway.Server) {
				tools := makeAnnotatedToolCatalogOpenAI()
				msgs := []map[string]any{
					{"role": "system", "content": sysPrompt},
					{"role": "user", "content": "Investigate the billing timeout, check test failures and diff."},
				}
				hdrs := map[string]string{"Authorization": "Bearer sk-proj-bench", "x-cave-session": "sess-openai-chat"}
				sendJSON(t, srv, "/v1/chat/completions", hdrs, map[string]any{
					"model": "gpt-6-astra", "tools": tools, "messages": msgs,
				})
				for step := 0; step < 4; step++ {
					p1, p2 := payloads[step*2], payloads[step*2+1]
					msgs = append(msgs, map[string]any{
						"role": "assistant",
						"tool_calls": []map[string]any{
							{"id": fmt.Sprintf("call_%d_a", step), "type": "function", "function": map[string]any{"name": "repo_tool_00", "arguments": "{}"}},
							{"id": fmt.Sprintf("call_%d_b", step), "type": "function", "function": map[string]any{"name": "repo_tool_01", "arguments": "{}"}},
						},
					})
					msgs = append(msgs,
						map[string]any{"role": "tool", "tool_call_id": fmt.Sprintf("call_%d_a", step), "content": p1.content},
						map[string]any{"role": "tool", "tool_call_id": fmt.Sprintf("call_%d_b", step), "content": p2.content},
					)
					sendJSON(t, srv, "/v1/chat/completions", hdrs, map[string]any{
						"model": "gpt-6-astra", "tools": tools, "messages": msgs,
					})
				}
			},
		},
		{
			name:     "OpenAI_Responses_ParallelTools",
			provider: "openai",
			run: func(t *testing.T, srv *gateway.Server) {
				tools := makeAnnotatedToolCatalogResponses()
				input := []map[string]any{
					{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "Investigate the billing timeout, check test failures and diff."}}},
				}
				hdrs := map[string]string{"Authorization": "Bearer sk-proj-bench", "x-cave-session": "sess-openai-resp"}
				sendJSON(t, srv, "/v1/responses", hdrs, map[string]any{
					"model": "gpt-6.1-sol", "instructions": sysPrompt, "tools": tools, "input": input,
				})
				for step := 0; step < 4; step++ {
					p1, p2 := payloads[step*2], payloads[step*2+1]
					input = append(input,
						map[string]any{"type": "function_call", "call_id": fmt.Sprintf("fc_%d_a", step), "name": "repo_tool_00", "arguments": "{}"},
						map[string]any{"type": "function_call", "call_id": fmt.Sprintf("fc_%d_b", step), "name": "repo_tool_01", "arguments": "{}"},
						map[string]any{"type": "function_call_output", "call_id": fmt.Sprintf("fc_%d_a", step), "output": p1.content},
						map[string]any{"type": "custom_tool_call_output", "call_id": fmt.Sprintf("fc_%d_b", step), "output": []map[string]any{{"type": "input_text", "text": p2.content}}},
					)
					sendJSON(t, srv, "/v1/responses", hdrs, map[string]any{
						"model": "gpt-6.1-sol", "instructions": sysPrompt, "tools": tools, "input": input,
					})
				}
			},
		},
		{
			name:     "Codex_ChatGPT_Zstd_Responses",
			provider: "openai",
			run: func(t *testing.T, srv *gateway.Server) {
				tools := makeAnnotatedToolCatalogResponses()
				input := []map[string]any{
					{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "Investigate the billing timeout, check test failures and diff."}}},
				}
				hdrs := map[string]string{
					"Authorization":      "Bearer eyJhbGciOiJSUzI1NiJ9.codex-oauth",
					"ChatGPT-Account-ID": "acct-bench-001",
					"x-cave-agent":       "codex",
					"x-cave-session":     "sess-codex-zstd",
				}
				sendZstdJSON(t, srv, "/chatgpt/responses", hdrs, map[string]any{
					"model": "gpt-6.1-sol", "instructions": sysPrompt, "tools": tools, "input": input, "stream": true,
				})
				for step := 0; step < 4; step++ {
					p1, p2 := payloads[step*2], payloads[step*2+1]
					input = append(input,
						map[string]any{"type": "custom_tool_call", "call_id": fmt.Sprintf("cx_%d_a", step), "name": "shell", "input": p1.name},
						map[string]any{"type": "custom_tool_call", "call_id": fmt.Sprintf("cx_%d_b", step), "name": "shell", "input": p2.name},
						map[string]any{"type": "custom_tool_call_output", "call_id": fmt.Sprintf("cx_%d_a", step), "output": []map[string]any{{"type": "input_text", "text": p1.content}}},
						map[string]any{"type": "custom_tool_call_output", "call_id": fmt.Sprintf("cx_%d_b", step), "output": []map[string]any{{"type": "input_text", "text": p2.content}}},
					)
					sendZstdJSON(t, srv, "/chatgpt/responses", hdrs, map[string]any{
						"model": "gpt-6.1-sol", "instructions": sysPrompt, "tools": tools, "input": input, "stream": true,
					})
				}
			},
		},
		{
			name:     "Gemini_StreamGenerateContent",
			provider: "gemini",
			run: func(t *testing.T, srv *gateway.Server) {
				tools := makeAnnotatedToolCatalogGemini()
				contents := []map[string]any{
					{"role": "user", "parts": []map[string]any{{"text": "Investigate the billing timeout, check test failures and diff."}}},
				}
				hdrs := map[string]string{"x-goog-api-key": "AIzaSyBenchKey", "x-cave-session": "sess-gemini-stream"}
				ep := "/gemini/v1beta/models/gemini-2.5-pro:streamGenerateContent"
				sendJSON(t, srv, ep, hdrs, map[string]any{
					"systemInstruction": map[string]any{"parts": []map[string]any{{"text": sysPrompt}}},
					"tools":             tools,
					"contents":          contents,
				})
				for step := 0; step < 4; step++ {
					p1, p2 := payloads[step*2], payloads[step*2+1]
					contents = append(contents,
						map[string]any{"role": "model", "parts": []map[string]any{
							{"functionCall": map[string]any{"name": "repo_tool_00", "args": map[string]any{}}},
							{"functionCall": map[string]any{"name": "repo_tool_01", "args": map[string]any{}}},
						}},
						map[string]any{"role": "user", "parts": []map[string]any{
							// Realistic Gemini MCP/CLI tool responses use keys like stdout/text/output
							{"functionResponse": map[string]any{"name": "repo_tool_00", "response": map[string]any{"stdout": p1.content}}},
							{"functionResponse": map[string]any{"name": "repo_tool_01", "response": map[string]any{"output": p2.content}}},
						}},
					)
					sendJSON(t, srv, ep, hdrs, map[string]any{
						"systemInstruction": map[string]any{"parts": []map[string]any{{"text": sysPrompt}}},
						"tools":             tools,
						"contents":          contents,
					})
				}
			},
		},
		{
			name:     "AzureOpenAI_Chat_ParallelTools",
			provider: "azure_openai",
			run: func(t *testing.T, srv *gateway.Server) {
				tools := makeAnnotatedToolCatalogOpenAI()
				msgs := []map[string]any{
					{"role": "system", "content": sysPrompt},
					{"role": "user", "content": "Investigate the billing timeout, check test failures and diff."},
				}
				hdrs := map[string]string{"api-key": "azure-bench-key", "x-cave-session": "sess-azure-chat"}
				ep := "/azure/openai/deployments/gpt-6-astra/chat/completions?api-version=2024-10-21"
				sendJSON(t, srv, ep, hdrs, map[string]any{
					"model": "gpt-6-astra", "tools": tools, "messages": msgs,
				})
				for step := 0; step < 4; step++ {
					p1, p2 := payloads[step*2], payloads[step*2+1]
					msgs = append(msgs,
						map[string]any{"role": "assistant", "tool_calls": []map[string]any{
							{"id": fmt.Sprintf("az_%d_a", step), "type": "function", "function": map[string]any{"name": "repo_tool_00", "arguments": "{}"}},
							{"id": fmt.Sprintf("az_%d_b", step), "type": "function", "function": map[string]any{"name": "repo_tool_01", "arguments": "{}"}},
						}},
						map[string]any{"role": "tool", "tool_call_id": fmt.Sprintf("az_%d_a", step), "content": p1.content},
						map[string]any{"role": "tool", "tool_call_id": fmt.Sprintf("az_%d_b", step), "content": p2.content},
					)
					sendJSON(t, srv, ep, hdrs, map[string]any{
						"model": "gpt-6-astra", "tools": tools, "messages": msgs,
					})
				}
			},
		},
		{
			name:     "Bedrock_Converse_Claude",
			provider: "bedrock",
			run: func(t *testing.T, srv *gateway.Server) {
				toolConfig := makeBedrockConverseTools()
				sys := []map[string]any{{"text": sysPrompt}}
				msgs := []map[string]any{
					{"role": "user", "content": []map[string]any{{"text": "Investigate the billing timeout, check test failures and diff."}}},
				}
				hdrs := map[string]string{"Authorization": "Bearer bedrock-bench-key", "x-cave-session": "sess-bedrock-converse"}
				ep := "/bedrock/model/global.anthropic.claude-sonnet-4-6/converse"
				sendJSON(t, srv, ep, hdrs, map[string]any{
					"system": sys, "toolConfig": toolConfig, "messages": msgs,
				})
				for step := 0; step < 4; step++ {
					p1, p2 := payloads[step*2], payloads[step*2+1]
					msgs = append(msgs,
						map[string]any{"role": "assistant", "content": []map[string]any{
							{"toolUse": map[string]any{"toolUseId": fmt.Sprintf("br_%d_a", step), "name": "repo_tool_00", "input": map[string]any{}}},
							{"toolUse": map[string]any{"toolUseId": fmt.Sprintf("br_%d_b", step), "name": "repo_tool_01", "input": map[string]any{}}},
						}},
						map[string]any{"role": "user", "content": []map[string]any{
							{"toolResult": map[string]any{"toolUseId": fmt.Sprintf("br_%d_a", step), "content": []map[string]any{{"text": p1.content}}}},
							{"toolResult": map[string]any{"toolUseId": fmt.Sprintf("br_%d_b", step), "content": []map[string]any{{"text": p2.content}}}},
						}},
					)
					sendJSON(t, srv, ep, hdrs, map[string]any{
						"system": sys, "toolConfig": toolConfig, "messages": msgs,
					})
				}
			},
		},
		{
			name:     "Vertex_Claude_RawPredict",
			provider: "vertex",
			run: func(t *testing.T, srv *gateway.Server) {
				tools := makeAnnotatedToolCatalogAnthropic()
				msgs := []map[string]any{
					{"role": "user", "content": []map[string]any{{"type": "text", "text": "Investigate the billing timeout, check test failures and diff."}}},
				}
				hdrs := map[string]string{"x-goog-api-key": "AQ.vertex-express-key", "x-cave-session": "sess-vertex-claude"}
				ep := "/vertex/v1/projects/demo-proj/locations/global/publishers/anthropic/models/claude-sonnet-4-6:rawPredict"
				sendJSON(t, srv, ep, hdrs, map[string]any{
					"anthropic_version": "vertex-2023-10-16", "max_tokens": 1024, "system": sysPrompt, "tools": tools, "messages": msgs,
				})
				for step := 0; step < 4; step++ {
					p1, p2 := payloads[step*2], payloads[step*2+1]
					msgs = append(msgs,
						map[string]any{"role": "assistant", "content": []map[string]any{
							{"type": "tool_use", "id": fmt.Sprintf("vx_%d_a", step), "name": "repo_tool_00", "input": map[string]any{}},
							{"type": "tool_use", "id": fmt.Sprintf("vx_%d_b", step), "name": "repo_tool_01", "input": map[string]any{}},
						}},
						map[string]any{"role": "user", "content": []map[string]any{
							{"type": "tool_result", "tool_use_id": fmt.Sprintf("vx_%d_a", step), "content": p1.content},
							{"type": "tool_result", "tool_use_id": fmt.Sprintf("vx_%d_b", step), "content": p2.content},
						}},
					)
					sendJSON(t, srv, ep, hdrs, map[string]any{
						"anthropic_version": "vertex-2023-10-16", "max_tokens": 1024, "system": sysPrompt, "tools": tools, "messages": msgs,
					})
				}
			},
		},
		{
			name:     "Vertex_Gemini_GenerateContent",
			provider: "vertex",
			run: func(t *testing.T, srv *gateway.Server) {
				tools := makeAnnotatedToolCatalogGemini()
				contents := []map[string]any{
					{"role": "user", "parts": []map[string]any{{"text": "Investigate the billing timeout, check test failures and diff."}}},
				}
				hdrs := map[string]string{"x-goog-api-key": "AQ.vertex-express-key", "x-cave-session": "sess-vertex-gemini"}
				ep := "/vertex/v1/projects/demo-proj/locations/global/publishers/google/models/gemini-2.5-pro:generateContent"
				sendJSON(t, srv, ep, hdrs, map[string]any{
					"systemInstruction": map[string]any{"parts": []map[string]any{{"text": sysPrompt}}},
					"tools":             tools,
					"contents":          contents,
				})
				for step := 0; step < 4; step++ {
					p1, p2 := payloads[step*2], payloads[step*2+1]
					contents = append(contents,
						map[string]any{"role": "model", "parts": []map[string]any{
							{"functionCall": map[string]any{"name": "repo_tool_00", "args": map[string]any{}}},
							{"functionCall": map[string]any{"name": "repo_tool_01", "args": map[string]any{}}},
						}},
						map[string]any{"role": "user", "parts": []map[string]any{
							{"functionResponse": map[string]any{"name": "repo_tool_00", "response": map[string]any{"output": p1.content}}},
							{"functionResponse": map[string]any{"name": "repo_tool_01", "response": map[string]any{"stdout": p2.content}}},
						}},
					)
					sendJSON(t, srv, ep, hdrs, map[string]any{
						"systemInstruction": map[string]any{"parts": []map[string]any{{"text": sysPrompt}}},
						"tools":             tools,
						"contents":          contents,
					})
				}
			},
		},
		{
			name:     "OpenAICompat_AnthropicDialect",
			provider: "openai_compatible",
			run: func(t *testing.T, srv *gateway.Server) {
				tools := makeAnnotatedToolCatalogAnthropic()
				msgs := []map[string]any{
					{"role": "user", "content": []map[string]any{{"type": "text", "text": "Investigate the billing timeout, check test failures and diff."}}},
				}
				hdrs := map[string]string{"x-api-key": "sk-compat-ant-key", "anthropic-version": "2023-06-01", "x-cave-session": "sess-compat-ant"}
				ep := "/compat/anthropic-gw/v1/messages"
				sendJSON(t, srv, ep, hdrs, map[string]any{
					"model": "claude-sonnet-4-6", "max_tokens": 1024, "system": sysPrompt, "tools": tools, "messages": msgs,
				})
				for step := 0; step < 4; step++ {
					p1, p2 := payloads[step*2], payloads[step*2+1]
					msgs = append(msgs,
						map[string]any{"role": "assistant", "content": []map[string]any{
							{"type": "tool_use", "id": fmt.Sprintf("ca_%d_a", step), "name": "repo_tool_00", "input": map[string]any{}},
							{"type": "tool_use", "id": fmt.Sprintf("ca_%d_b", step), "name": "repo_tool_01", "input": map[string]any{}},
						}},
						map[string]any{"role": "user", "content": []map[string]any{
							{"type": "tool_result", "tool_use_id": fmt.Sprintf("ca_%d_a", step), "content": p1.content},
							{"type": "tool_result", "tool_use_id": fmt.Sprintf("ca_%d_b", step), "content": p2.content},
						}},
					)
					sendJSON(t, srv, ep, hdrs, map[string]any{
						"model": "claude-sonnet-4-6", "max_tokens": 1024, "system": sysPrompt, "tools": tools, "messages": msgs,
					})
				}
			},
		},
		{
			name:     "OpenAICompat_OpenAIDialect",
			provider: "openai_compatible",
			run: func(t *testing.T, srv *gateway.Server) {
				tools := makeAnnotatedToolCatalogOpenAI()
				msgs := []map[string]any{
					{"role": "system", "content": sysPrompt},
					{"role": "user", "content": "Investigate the billing timeout, check test failures and diff."},
				}
				hdrs := map[string]string{"Authorization": "Bearer sk-compat-oai-key", "x-cave-session": "sess-compat-oai"}
				ep := "/compat/openrouter/v1/chat/completions"
				sendJSON(t, srv, ep, hdrs, map[string]any{
					"model": "gpt-6-astra", "tools": tools, "messages": msgs,
				})
				for step := 0; step < 4; step++ {
					p1, p2 := payloads[step*2], payloads[step*2+1]
					msgs = append(msgs,
						map[string]any{"role": "assistant", "tool_calls": []map[string]any{
							{"id": fmt.Sprintf("co_%d_a", step), "type": "function", "function": map[string]any{"name": "repo_tool_00", "arguments": "{}"}},
							{"id": fmt.Sprintf("co_%d_b", step), "type": "function", "function": map[string]any{"name": "repo_tool_01", "arguments": "{}"}},
						}},
						map[string]any{"role": "tool", "tool_call_id": fmt.Sprintf("co_%d_a", step), "content": p1.content},
						map[string]any{"role": "tool", "tool_call_id": fmt.Sprintf("co_%d_b", step), "content": p2.content},
					)
					sendJSON(t, srv, ep, hdrs, map[string]any{
						"model": "gpt-6-astra", "tools": tools, "messages": msgs,
					})
				}
			},
		},
	}

	var results []scenarioMetrics
	var totalBaseUSD, totalOptUSD float64

	for _, sc := range scenarios {
		// 1. Baseline: record mode (pure pass-through)
		baseUp := newStatefulUpstream()
		baseSink := &e2eRecordingSink{}
		baseSrv, _, _ := buildBenchServer(t, "record", baseUp, baseSink)
		sc.run(t, baseSrv)

		// 2. Optimized: compress mode
		optUp := newStatefulUpstream()
		optSink := &e2eRecordingSink{}
		optSrv, optCCR, _ := buildBenchServer(t, "compress", optUp, optSink)
		sc.run(t, optSrv)

		recOK, recFail := verifyCCRHandles(optCCR, optUp.upstreamBodies)
		if recFail > 0 {
			t.Errorf("%s: %d CCR recovery handles failed to resolve", sc.name, recFail)
		}
		busts := optSink.cavemanBustCount()
		if busts > 0 {
			t.Errorf("%s: %d caveman-caused cache busts detected", sc.name, busts)
		}

		saved := baseUp.totalCostUSD - optUp.totalCostUSD
		pct := 0.0
		if baseUp.totalCostUSD > 0 {
			pct = saved / baseUp.totalCostUSD * 100
		}
		results = append(results, scenarioMetrics{
			name:          sc.name,
			provider:      sc.provider,
			baselineUSD:   baseUp.totalCostUSD,
			optimizedUSD:  optUp.totalCostUSD,
			savedUSD:      saved,
			savedPct:      pct,
			baseInTok:     baseUp.inputTokens,
			optInTok:      optUp.inputTokens,
			optCachedTok:  optUp.cachedTokens,
			optWriteTok:   optUp.cacheWriteTok,
			cavemanBusts:  busts,
			recoveredOK:   recOK,
			recoveredFail: recFail,
		})
		totalBaseUSD += baseUp.totalCostUSD
		totalOptUSD += optUp.totalCostUSD
	}

	t.Logf("\n%-32s | %9s | %9s | %7s | %8s -> %8s | %8s | %5s | %4s",
		"Scenario", "Base $", "Opt $", "Save %", "BaseTok", "OptTok", "Cached", "Busts", "CCR")
	t.Logf("%s", strings.Repeat("-", 108))
	for _, r := range results {
		t.Logf("%-32s | $%8.4f | $%8.4f | %6.1f%% | %8d -> %8d | %8d | %5d | %4d",
			r.name, r.baselineUSD, r.optimizedUSD, r.savedPct, r.baseInTok, r.optInTok, r.optCachedTok, r.cavemanBusts, r.recoveredOK)
	}
	totPct := (totalBaseUSD - totalOptUSD) / totalBaseUSD * 100
	t.Logf("%s", strings.Repeat("-", 108))
	t.Logf("%-32s | $%8.4f | $%8.4f | %6.1f%%", fmt.Sprintf("TOTAL (all %d scenarios)", len(results)), totalBaseUSD, totalOptUSD, totPct)
}

// TestE2EFullStackCostComparison compares total multi-turn session USD cost
// across four full-stack tiers on a realistic 50-tool MCP workflow (modeled on
// T3 Code / GitHub / Linear MCP servers: list open PRs -> inspect failing PRs
// -> read CI logs for failing checks -> answer):
//
//   - Arm 0 (Raw Baseline):      record mode, 50 raw MCP tool schemas, 4 turns
//   - Arm 1 (Proxy Compress):    compress + cache breakpoints, 50 raw schemas, 4 turns
//   - Arm 2 (+ Schema Strip):    + toolschema_strip: annotations (multi-provider)
//   - Arm 3 (+ CodeMode MCP):    codemode gateway (3 meta-tools collapsing 50
//                                MCP schemas + 1-turn batched MCP pipeline with
//                                field projection, grep, and CCR archival)
func TestE2EFullStackCostComparison(t *testing.T) {
	// Build a realistic 50-tool MCP upstream (PRs, CI logs, issues, threads, preview).
	up := codemode.NewInProcessUpstream("t3")
	prs := make([]map[string]any, 0, 30)
	for i := 1; i <= 30; i++ {
		st := "passing"
		if i == 7 || i == 19 {
			st = "failing"
		}
		prs = append(prs, map[string]any{
			"number":       200 + i,
			"title":        fmt.Sprintf("feat(proxy): adapter improvement #%d", i),
			"author":       fmt.Sprintf("engineer_%02d", i%6),
			"checks_state": st,
			"branch":       fmt.Sprintf("feat/pr-%03d", i),
			"head_sha":     fmt.Sprintf("%040x", i*104729),
			"labels":       []string{"proxy", "performance", "ci"},
			"body":         strings.Repeat("Summary of architectural changes, benchmark methodology, and rollout plan. ", 22),
		})
	}
	rawPRListJSON, _ := json.Marshal(map[string]any{"pull_requests": prs})
	makeCILog := func(prNum int) string {
		var b strings.Builder
		for i := 0; i < 160; i++ {
			fmt.Fprintf(&b, "2026-10-12T10:%02d:%02dZ INFO  [pr-%d] step %d ok: go test ./proxy/providers/pkg_%02d (0.14s)\n", (i/60)%60, i%60, prNum, i, i%12)
		}
		fmt.Fprintf(&b, "2026-10-12T10:03:01Z ERROR [pr-%d] FAIL TestCachePrefixInvariant: digest mismatch at component 2\n", prNum)
		return b.String()
	}

	up.Register(codemode.ToolSpec{
		Name:        "pull_request_list",
		Description: "List pull requests in this thread's repository, recently updated first, including check status, review threads, and branch metadata.",
		InputSchema: map[string]any{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"title":   "PullRequestListParams",
			"type":    "object",
			"properties": map[string]any{
				"state": map[string]any{"type": "string", "title": "State", "enum": []string{"open", "closed", "merged", "all"}, "examples": []string{"open"}, "description": "Filter by PR state."},
				"limit": map[string]any{"type": "integer", "title": "Limit", "examples": []int{30}, "description": "Maximum PRs to return."},
			},
		},
	}, func(_ context.Context, _ map[string]any) (any, error) {
		return map[string]any{"pull_requests": prs}, nil
	})
	up.Register(codemode.ToolSpec{
		Name:        "ci_logs",
		Description: "Read the tail of a PR's failing CI check logs, or one named check's logs.",
		InputSchema: map[string]any{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"title":   "CILogsParams",
			"type":    "object",
			"properties": map[string]any{
				"number":    map[string]any{"type": "integer", "title": "PR Number", "examples": []int{207}, "description": "Pull request number."},
				"tailLines": map[string]any{"type": "integer", "title": "Tail Lines", "examples": []int{200}, "description": "Tail line count."},
			},
			"required": []string{"number"},
		},
	}, func(_ context.Context, args map[string]any) (any, error) {
		n := 207
		if v, ok := args["number"]; ok {
			_, _ = fmt.Sscanf(fmt.Sprint(v), "%d", &n)
		}
		return makeCILog(n), nil
	})
	for i := 0; i < 48; i++ {
		name := fmt.Sprintf("mcp_surface_tool_%02d", i)
		up.Register(codemode.ToolSpec{
			Name: name,
			Description: fmt.Sprintf(
				"Manage workspace resource #%02d across linked environments, threads, previews, and issue trackers with cursor pagination. "+
					"Returns normalized metadata, timestamps, audit trails, and linked references for automated triage workflows. "+
					"Use pagination cursors when iterating over large workspaces to avoid rate limits and partial snapshots.",
				i,
			),
			InputSchema: map[string]any{
				"$schema":    "https://json-schema.org/draft/2020-12/schema",
				"title":      fmt.Sprintf("SurfaceTool%02dInput", i),
				"deprecated": false,
				"type":       "object",
				"properties": map[string]any{
					"environmentId": map[string]any{"type": "string", "title": "Environment ID", "examples": []string{"env_prod_01"}, "description": "Target environment identifier used to route requests across linked local or remote daemon hosts."},
					"threadId":      map[string]any{"type": "string", "title": "Thread ID", "examples": []string{"thr_abc123"}, "description": "Target conversation thread identifier; omit to inherit the active thread context automatically."},
					"cursor":        map[string]any{"type": "string", "title": "Cursor", "examples": []string{"cur_001"}, "description": "Opaque pagination cursor returned by a previous list call when continuing multi-page iteration."},
					"limit":         map[string]any{"type": "integer", "title": "Limit", "examples": []int{50}, "description": "Maximum number of resource entries to return per page before truncating the response."},
				},
			},
		}, func(_ context.Context, _ map[string]any) (any, error) {
			return map[string]any{"ok": true}, nil
		})
	}

	rawSpecs, _ := up.ListTools(context.Background())
	// Convert raw 50-tool MCP catalog to Anthropic and OpenAI wire shapes.
	rawAnthropicTools := make([]map[string]any, 0, len(rawSpecs))
	rawOpenAITools := make([]map[string]any, 0, len(rawSpecs))
	for _, sp := range rawSpecs {
		rawAnthropicTools = append(rawAnthropicTools, map[string]any{
			"name": "mcp__t3__" + sp.Name, "description": sp.Description, "input_schema": sp.InputSchema,
		})
		rawOpenAITools = append(rawOpenAITools, map[string]any{
			"type": "function", "name": "mcp__t3__" + sp.Name, "description": sp.Description, "parameters": sp.InputSchema,
		})
	}

	// Execute the real Code-Mode MCP Gateway once to obtain exact compact tool outputs.
	cmCCR, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmCCR.Close() })
	gw := codemode.NewGateway(cmCCR, nil, nil)
	if err := gw.AddUpstream(context.Background(), up); err != nil {
		t.Fatal(err)
	}
	cmSrv := codemode.NewServer(gw, nil)
	execReqBytes, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{
			"name": "codemode_execute",
			"arguments": map[string]any{
				"steps": []map[string]any{
					{"id": "prs", "op": "call", "tool": "t3.pull_request_list", "args": map[string]any{"state": "open"}},
					{"id": "failing", "op": "filter", "from": "$prs.pull_requests", "where": map[string]any{"field": "checks_state", "equals": "failing"}},
					{"id": "logs", "op": "call", "tool": "t3.ci_logs", "for_each": "$failing", "args": map[string]any{"number": "$item.number"}},
					{"id": "errors", "op": "grep_lines", "from": "$logs", "pattern": "ERROR|FAIL"},
				},
				"return_step": "errors",
			},
		},
	})
	var cmOut bytes.Buffer
	rpcIn := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"codemode_search_tools","arguments":{"query":"pull request failing ci logs","limit":4}}}` + "\n" + string(execReqBytes) + "\n"
	if err := cmSrv.Serve(strings.NewReader(rpcIn), &cmOut); err != nil {
		t.Fatalf("codemode serve: %v", err)
	}
	cmLines := strings.Split(strings.TrimSpace(cmOut.String()), "\n")
	extractRPCText := func(line string) string {
		var r struct {
			Result struct {
				Content []struct{ Text string } `json:"content"`
			} `json:"result"`
		}
		_ = json.Unmarshal([]byte(line), &r)
		if len(r.Result.Content) > 0 {
			return r.Result.Content[0].Text
		}
		return ""
	}
	cmSearchResult := extractRPCText(cmLines[0])
	cmExecResult := extractRPCText(cmLines[1])

	cmAnthropicTools := make([]map[string]any, 0, len(gw.Tools()))
	cmOpenAITools := make([]map[string]any, 0, len(gw.Tools()))
	for _, mt := range gw.Tools() {
		cmAnthropicTools = append(cmAnthropicTools, map[string]any{
			"name": mt.Name, "description": mt.Description, "input_schema": mt.InputSchema,
		})
		cmOpenAITools = append(cmOpenAITools, map[string]any{
			"type": "function", "name": mt.Name, "description": mt.Description, "parameters": mt.InputSchema,
		})
	}

	buildArmServer := func(mode, stripMode string, deferMCP bool, upstream *statefulUpstream) *gateway.Server {
		sink := &e2eRecordingSink{}
		db, err := store.Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		cstore, err := ccr.Open(filepath.Join(t.TempDir(), "ccr.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cstore.Close() })
		opts := map[string]bool{
			"anthropic-cache-breakpoints": true,
			"openai-prompt-cache-key":     true,
		}
		if deferMCP {
			opts["anthropic-defer-mcp-tools"] = true
			opts["openai-defer-mcp-tools"] = true
		}
		cfg := config.Config{
			Label:           "e2e-fullstack",
			Mode:            mode,
			Listen:          "127.0.0.1:8787",
			BreakpointPlan:  "frontier",
			ToolSchemaStrip: stripMode,
			Optimizers:      opts,
			Providers: map[string]config.ProviderConfig{
				"anthropic": {BaseURL: "https://api.anthropic.com"},
				"openai":    {BaseURL: "https://api.openai.com"},
			},
		}
		var comp gateway.Compressor
		if mode == "compress" {
			comp = NewEngineCompressor(cstore)
		}
		return New(cfg, sink, Options{
			HTTPClient:     &http.Client{Transport: upstream},
			Compressor:     comp,
			PrefixCache:    db,
			RecoveryViaMCP: true,
		})
	}

	sysPrompt := strings.Repeat("You are an autonomous software engineering agent debugging CI failures across open pull requests. ", 12)
	log207, log219 := makeCILog(207), makeCILog(219)

	runAnthropicTraditional := func(mode, stripMode string, deferMCP bool) (float64, int, int) {
		u := newStatefulUpstream()
		srv := buildArmServer(mode, stripMode, deferMCP, u)
		hdrs := map[string]string{"x-api-key": "sk-ant-fs-key", "anthropic-version": "2023-06-01", "x-cave-session": fmt.Sprintf("fs-ant-%s-%s-%v", mode, stripMode, deferMCP)}
		msgs := []map[string]any{{"role": "user", "content": []map[string]any{{"type": "text", "text": "Find all open PRs with failing CI and report the root cause."}}}}
		sendJSON(t, srv, "/v1/messages", hdrs, map[string]any{"model": "claude-sonnet-4-6", "max_tokens": 1024, "system": sysPrompt, "tools": rawAnthropicTools, "messages": msgs})
		// Turn 2: list 30 PRs (raw JSON)
		msgs = append(msgs,
			map[string]any{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": "tu_1", "name": "mcp__t3__pull_request_list", "input": map[string]any{"state": "open"}}}},
			map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "tu_1", "content": string(rawPRListJSON)}}},
		)
		sendJSON(t, srv, "/v1/messages", hdrs, map[string]any{"model": "claude-sonnet-4-6", "max_tokens": 1024, "system": sysPrompt, "tools": rawAnthropicTools, "messages": msgs})
		// Turn 3: fetch CI logs for PR 207 & PR 219
		msgs = append(msgs,
			map[string]any{"role": "assistant", "content": []map[string]any{
				{"type": "tool_use", "id": "tu_2a", "name": "mcp__t3__ci_logs", "input": map[string]any{"number": 207}},
				{"type": "tool_use", "id": "tu_2b", "name": "mcp__t3__ci_logs", "input": map[string]any{"number": 219}},
			}},
			map[string]any{"role": "user", "content": []map[string]any{
				{"type": "tool_result", "tool_use_id": "tu_2a", "content": log207},
				{"type": "tool_result", "tool_use_id": "tu_2b", "content": log219},
			}},
		)
		sendJSON(t, srv, "/v1/messages", hdrs, map[string]any{"model": "claude-sonnet-4-6", "max_tokens": 1024, "system": sysPrompt, "tools": rawAnthropicTools, "messages": msgs})
		return u.totalCostUSD, u.inputTokens, len(u.upstreamBodies)
	}

	runAnthropicCodeMode := func() (float64, int, int) {
		u := newStatefulUpstream()
		srv := buildArmServer("compress", "annotations", false, u)
		hdrs := map[string]string{"x-api-key": "sk-ant-fs-key", "anthropic-version": "2023-06-01", "x-cave-session": "fs-ant-codemode"}
		msgs := []map[string]any{{"role": "user", "content": []map[string]any{{"type": "text", "text": "Find all open PRs with failing CI and report the root cause."}}}}
		sendJSON(t, srv, "/v1/messages", hdrs, map[string]any{"model": "claude-sonnet-4-6", "max_tokens": 1024, "system": sysPrompt, "tools": cmAnthropicTools, "messages": msgs})
		// Turn 2: search_tools + execute in compact Code-Mode results (2 turns total to answer!)
		msgs = append(msgs,
			map[string]any{"role": "assistant", "content": []map[string]any{
				{"type": "tool_use", "id": "cm_1", "name": "codemode_search_tools", "input": map[string]any{"query": "pull request failing ci logs"}},
				{"type": "tool_use", "id": "cm_2", "name": "codemode_execute", "input": map[string]any{"steps": []any{}}},
			}},
			map[string]any{"role": "user", "content": []map[string]any{
				{"type": "tool_result", "tool_use_id": "cm_1", "content": cmSearchResult},
				{"type": "tool_result", "tool_use_id": "cm_2", "content": cmExecResult},
			}},
		)
		sendJSON(t, srv, "/v1/messages", hdrs, map[string]any{"model": "claude-sonnet-4-6", "max_tokens": 1024, "system": sysPrompt, "tools": cmAnthropicTools, "messages": msgs})
		return u.totalCostUSD, u.inputTokens, len(u.upstreamBodies)
	}

	runOpenAITraditional := func(mode, stripMode string, deferMCP bool) (float64, int, int) {
		u := newStatefulUpstream()
		srv := buildArmServer(mode, stripMode, deferMCP, u)
		hdrs := map[string]string{"Authorization": "Bearer sk-oai-fs-key", "x-cave-session": fmt.Sprintf("fs-oai-%s-%s-%v", mode, stripMode, deferMCP)}
		input := []map[string]any{{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "Find all open PRs with failing CI and report the root cause."}}}}
		sendJSON(t, srv, "/v1/responses", hdrs, map[string]any{"model": "gpt-6.1-sol", "instructions": sysPrompt, "tools": rawOpenAITools, "input": input})
		input = append(input,
			map[string]any{"type": "function_call", "call_id": "fc_1", "name": "mcp__t3__pull_request_list", "arguments": `{"state":"open"}`},
			map[string]any{"type": "function_call_output", "call_id": "fc_1", "output": string(rawPRListJSON)},
		)
		sendJSON(t, srv, "/v1/responses", hdrs, map[string]any{"model": "gpt-6.1-sol", "instructions": sysPrompt, "tools": rawOpenAITools, "input": input})
		input = append(input,
			map[string]any{"type": "function_call", "call_id": "fc_2a", "name": "mcp__t3__ci_logs", "arguments": `{"number":207}`},
			map[string]any{"type": "function_call", "call_id": "fc_2b", "name": "mcp__t3__ci_logs", "arguments": `{"number":219}`},
			map[string]any{"type": "function_call_output", "call_id": "fc_2a", "output": log207},
			map[string]any{"type": "function_call_output", "call_id": "fc_2b", "output": log219},
		)
		sendJSON(t, srv, "/v1/responses", hdrs, map[string]any{"model": "gpt-6.1-sol", "instructions": sysPrompt, "tools": rawOpenAITools, "input": input})
		return u.totalCostUSD, u.inputTokens, len(u.upstreamBodies)
	}

	runOpenAICodeMode := func() (float64, int, int) {
		u := newStatefulUpstream()
		srv := buildArmServer("compress", "annotations", false, u)
		hdrs := map[string]string{"Authorization": "Bearer sk-oai-fs-key", "x-cave-session": "fs-oai-codemode"}
		input := []map[string]any{{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "Find all open PRs with failing CI and report the root cause."}}}}
		sendJSON(t, srv, "/v1/responses", hdrs, map[string]any{"model": "gpt-6.1-sol", "instructions": sysPrompt, "tools": cmOpenAITools, "input": input})
		input = append(input,
			map[string]any{"type": "function_call", "call_id": "cm_1", "name": "codemode_search_tools", "arguments": `{"query":"pull request failing ci logs"}`},
			map[string]any{"type": "function_call", "call_id": "cm_2", "name": "codemode_execute", "arguments": `{}`},
			map[string]any{"type": "function_call_output", "call_id": "cm_1", "output": cmSearchResult},
			map[string]any{"type": "function_call_output", "call_id": "cm_2", "output": cmExecResult},
		)
		sendJSON(t, srv, "/v1/responses", hdrs, map[string]any{"model": "gpt-6.1-sol", "instructions": sysPrompt, "tools": cmOpenAITools, "input": input})
		return u.totalCostUSD, u.inputTokens, len(u.upstreamBodies)
	}

	a0Cost, a0Tok, a0Turns := runAnthropicTraditional("record", "", false)
	a1Cost, a1Tok, a1Turns := runAnthropicTraditional("compress", "", false)
	a2aCost, a2aTok, a2aTurns := runAnthropicTraditional("compress", "annotations", false)
	a2bCost, a2bTok, a2bTurns := runAnthropicTraditional("compress", "shrink", false)
	a2cCost, a2cTok, a2cTurns := runAnthropicTraditional("compress", "shrink", true)
	a3Cost, a3Tok, a3Turns := runAnthropicCodeMode()

	o0Cost, o0Tok, o0Turns := runOpenAITraditional("record", "", false)
	o1Cost, o1Tok, o1Turns := runOpenAITraditional("compress", "", false)
	o2aCost, o2aTok, o2aTurns := runOpenAITraditional("compress", "annotations", false)
	o2bCost, o2bTok, o2bTurns := runOpenAITraditional("compress", "shrink", false)
	o2cCost, o2cTok, o2cTurns := runOpenAITraditional("compress", "shrink", true)
	o3Cost, o3Tok, o3Turns := runOpenAICodeMode()

	t.Logf("\n=== FULL-STACK 50-TOOL MCP WORKFLOW COST COMPARISON (PR List + Failing CI Logs) ===")
	t.Logf("  Anthropic Claude Sonnet 4.6 (PAYG):")
	t.Logf("    Arm 0  (Raw baseline, 50 MCP tools):     $%.4f | %6d tok | %d turns", a0Cost, a0Tok, a0Turns)
	t.Logf("    Arm 1  (Proxy compress + breakpoints):   $%.4f | %6d tok | %d turns (%.1f%% saved)", a1Cost, a1Tok, a1Turns, (1-a1Cost/a0Cost)*100)
	t.Logf("    Arm 2a (+ toolschema_strip annotations): $%.4f | %6d tok | %d turns (%.1f%% saved)", a2aCost, a2aTok, a2aTurns, (1-a2aCost/a0Cost)*100)
	t.Logf("    Arm 2b (+ toolschema_strip shrink):      $%.4f | %6d tok | %d turns (%.1f%% saved)", a2bCost, a2bTok, a2bTurns, (1-a2bCost/a0Cost)*100)
	t.Logf("    Arm 2c (+ Anthropic defer_loading):      $%.4f | %6d tok | %d turns (%.1f%% saved)", a2cCost, a2cTok, a2cTurns, (1-a2cCost/a0Cost)*100)
	t.Logf("    Arm 3  (+ CodeMode MCP gateway):         $%.4f | %6d tok | %d turns (%.1f%% saved)", a3Cost, a3Tok, a3Turns, (1-a3Cost/a0Cost)*100)
	t.Logf("  OpenAI GPT-6.1-sol (Responses):")
	t.Logf("    Arm 0  (Raw baseline, 50 MCP tools):     $%.4f | %6d tok | %d turns", o0Cost, o0Tok, o0Turns)
	t.Logf("    Arm 1  (Proxy compress + cache key):     $%.4f | %6d tok | %d turns (%.1f%% saved)", o1Cost, o1Tok, o1Turns, (1-o1Cost/o0Cost)*100)
	t.Logf("    Arm 2a (+ toolschema_strip annotations): $%.4f | %6d tok | %d turns (%.1f%% saved)", o2aCost, o2aTok, o2aTurns, (1-o2aCost/o0Cost)*100)
	t.Logf("    Arm 2b (+ toolschema_strip shrink):      $%.4f | %6d tok | %d turns (%.1f%% saved)", o2bCost, o2bTok, o2bTurns, (1-o2bCost/o0Cost)*100)
	t.Logf("    Arm 2c (+ OpenAI defer_loading):         $%.4f | %6d tok | %d turns (%.1f%% saved)", o2cCost, o2cTok, o2cTurns, (1-o2cCost/o0Cost)*100)
	t.Logf("    Arm 3  (+ CodeMode MCP gateway):         $%.4f | %6d tok | %d turns (%.1f%% saved)", o3Cost, o3Tok, o3Turns, (1-o3Cost/o0Cost)*100)

	if a2bCost >= a2aCost || a2cCost >= a2bCost || a3Cost >= a1Cost {
		t.Fatalf("expected monotonic Anthropic savings: a2a=%.4f a2b=%.4f a2c=%.4f a3=%.4f", a2aCost, a2bCost, a2cCost, a3Cost)
	}
	if o2bCost >= o2aCost || o2cCost >= o2bCost || o3Cost >= o1Cost {
		t.Fatalf("expected monotonic OpenAI savings: o2a=%.4f o2b=%.4f o2c=%.4f o3=%.4f", o2aCost, o2bCost, o2cCost, o3Cost)
	}
}

// TestE2EHillClimbRealCorpus replays real sanitized local session transcripts
// and tool outputs when CAVEMAN_BENCH_CORPUS points to a corpus JSON file.
// Skipped automatically in CI when unset so no local files are ever required.
func TestE2EHillClimbRealCorpus(t *testing.T) {
	corpusPath := os.Getenv("CAVEMAN_BENCH_CORPUS")
	if corpusPath == "" {
		t.Skip("set CAVEMAN_BENCH_CORPUS to run against a local session corpus")
	}
	raw, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var corpus struct {
		ToolSamples []struct {
			Source  string `json:"source"`
			Tool    string `json:"tool"`
			Cmd     string `json:"cmd"`
			Bytes   int    `json:"bytes"`
			Content string `json:"content"`
		} `json:"tool_samples"`
		Sessions []struct {
			Source   string `json:"source"`
			Subagent bool   `json:"subagent"`
			Turns    []struct {
				Role        string `json:"role"`
				Text        string `json:"text"`
				Content     string `json:"content"`
				ToolResults []struct {
					ID      string `json:"id"`
					Tool    string `json:"tool"`
					Cmd     string `json:"cmd"`
					Content string `json:"content"`
				} `json:"tool_results"`
			} `json:"turns"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("unmarshal corpus: %v", err)
	}

	dir := t.TempDir()
	ccrStore, err := ccr.Open(filepath.Join(dir, "ccr.db"))
	if err != nil {
		t.Fatalf("open ccr: %v", err)
	}
	defer func() { _ = ccrStore.Close() }()
	eng := engine.New(ccrStore, nil)

	type bucket struct {
		count      int
		compressed int
		beforeTok  int
		afterTok   int
	}
	byType := map[string]*bucket{}
	byFamily := map[string]*bucket{}
	var totBefore, totAfter, totCompressed int

	for _, s := range corpus.ToolSamples {
		data := []byte(s.Content)
		ct := eng.Detect(data)
		res, err := eng.Compress(data, engine.Options{Mode: engine.ModeCompress})
		if err != nil {
			t.Fatalf("compress failed: %v", err)
		}
		if res.RecoveryHandle != "" {
			got, rerr := eng.Retrieve(res.RecoveryHandle)
			if rerr != nil || sha256.Sum256(got) != sha256.Sum256(data) {
				t.Fatalf("CCR roundtrip mismatch for %s/%s", s.Tool, s.Cmd)
			}
		}
		did := res.TokensAfter < res.TokensBefore
		if did {
			totCompressed++
		}
		totBefore += res.TokensBefore
		totAfter += res.TokensAfter

		bt := byType[ct]
		if bt == nil {
			bt = &bucket{}
			byType[ct] = bt
		}
		bt.count++
		if did {
			bt.compressed++
		}
		bt.beforeTok += res.TokensBefore
		bt.afterTok += res.TokensAfter

		fam := s.Tool
		if s.Cmd != "" {
			fam = s.Tool + ":" + s.Cmd
		}
		bf := byFamily[fam]
		if bf == nil {
			bf = &bucket{}
			byFamily[fam] = bf
		}
		bf.count++
		if did {
			bf.compressed++
		}
		bf.beforeTok += res.TokensBefore
		bf.afterTok += res.TokensAfter
	}

	t.Logf("\n=== REAL CORPUS ENGINE BREAKDOWN (%d tool outputs: %d compressed, %d -> %d tokens, %.1f%% saved) ===",
		len(corpus.ToolSamples), totCompressed, totBefore, totAfter,
		float64(totBefore-totAfter)/float64(max(1, totBefore))*100)

	t.Logf("\nBy Detected Type:")
	types := make([]string, 0, len(byType))
	for k := range byType {
		types = append(types, k)
	}
	sort.Slice(types, func(i, j int) bool { return byType[types[i]].beforeTok > byType[types[j]].beforeTok })
	for _, k := range types {
		b := byType[k]
		pct := float64(b.beforeTok-b.afterTok) / float64(max(1, b.beforeTok)) * 100
		t.Logf("  %-15s | n=%3d (hit=%3d) | %7d -> %7d tok (%5.1f%% saved)", k, b.count, b.compressed, b.beforeTok, b.afterTok, pct)
	}

	t.Logf("\nTop 15 Tool Families:")
	fams := make([]string, 0, len(byFamily))
	for k := range byFamily {
		fams = append(fams, k)
	}
	sort.Slice(fams, func(i, j int) bool { return byFamily[fams[i]].beforeTok > byFamily[fams[j]].beforeTok })
	if len(fams) > 15 {
		fams = fams[:15]
	}
	for _, k := range fams {
		b := byFamily[k]
		pct := float64(b.beforeTok-b.afterTok) / float64(max(1, b.beforeTok)) * 100
		t.Logf("  %-26s | n=%3d (hit=%3d) | %7d -> %7d tok (%5.1f%% saved)", k, b.count, b.compressed, b.beforeTok, b.afterTok, pct)
	}

	// Now replay real Claude multi-turn sessions through the FULL proxy across
	// Anthropic (caller-cached), Anthropic (unmanaged), OpenAI Responses, and Gemini!
	sysPrompt := makeVerboseSystemPrompt()
	var claudeBaseUSD, claudeOptUSD float64
	var oaiBaseUSD, oaiOptUSD float64
	var gemBaseUSD, gemOptUSD float64
	replayedSessions := 0

	for si, sess := range corpus.Sessions {
		if sess.Source != "claude" {
			continue
		}
		// Collect tool-result turns
		var steps [][]struct{ id, content string }
		for _, turn := range sess.Turns {
			if len(turn.ToolResults) == 0 {
				continue
			}
			var step []struct{ id, content string }
			for _, tr := range turn.ToolResults {
				if len(tr.Content) > 0 {
					step = append(step, struct{ id, content string }{id: tr.ID, content: tr.Content})
				}
			}
			if len(step) > 0 {
				steps = append(steps, step)
			}
		}
		if len(steps) < 3 {
			continue
		}
		if len(steps) > 12 {
			steps = steps[:12]
		}
		replayedSessions++

		// Replay on Anthropic (caller-cached Claude Code pattern)
		runAnth := func(mode string) (float64, int) {
			up := newStatefulUpstream()
			sk := &e2eRecordingSink{}
			srv, _, _ := buildBenchServer(t, mode, up, sk)
			tools := makeAnnotatedToolCatalogAnthropic()
			tools[len(tools)-1]["cache_control"] = map[string]any{"type": "ephemeral"}
			sys := []map[string]any{{"type": "text", "text": sysPrompt, "cache_control": map[string]any{"type": "ephemeral"}}}
			msgs := []map[string]any{
				{"role": "user", "content": []map[string]any{{"type": "text", "text": "Work on this engineering task."}}},
			}
			hdrs := map[string]string{"x-api-key": "sk-ant-api03-bench", "anthropic-version": "2023-06-01", "x-cave-session": fmt.Sprintf("real-anth-%d", si)}
			sendJSON(t, srv, "/v1/messages", hdrs, map[string]any{"model": "claude-opus-5-5", "max_tokens": 1024, "system": sys, "tools": tools, "messages": msgs})
			for idx, step := range steps {
				uses := make([]map[string]any, 0, len(step))
				results := make([]map[string]any, 0, len(step))
				for j, tr := range step {
					tid := fmt.Sprintf("tu_%d_%d", idx, j)
					uses = append(uses, map[string]any{"type": "tool_use", "id": tid, "name": "Bash", "input": map[string]any{}})
					blk := map[string]any{"type": "tool_result", "tool_use_id": tid, "content": tr.content}
					if j == len(step)-1 {
						blk["cache_control"] = map[string]any{"type": "ephemeral"}
					}
					results = append(results, blk)
				}
				for i := range msgs {
					if arr, ok := msgs[i]["content"].([]map[string]any); ok {
						for j := range arr {
							delete(arr[j], "cache_control")
						}
					}
				}
				msgs = append(msgs,
					map[string]any{"role": "assistant", "content": uses},
					map[string]any{"role": "user", "content": results},
				)
				sendJSON(t, srv, "/v1/messages", hdrs, map[string]any{"model": "claude-opus-5-5", "max_tokens": 1024, "system": sys, "tools": tools, "messages": msgs})
			}
			return up.totalCostUSD, sk.cavemanBustCount()
		}
		bUSD, _ := runAnth("record")
		oUSD, busts := runAnth("compress")
		if busts > 0 {
			t.Errorf("real session %d anthropic had %d caveman cache busts", si, busts)
		}
		claudeBaseUSD += bUSD
		claudeOptUSD += oUSD

		// Replay on OpenAI Responses
		runOAI := func(mode string) (float64, int) {
			up := newStatefulUpstream()
			sk := &e2eRecordingSink{}
			srv, _, _ := buildBenchServer(t, mode, up, sk)
			tools := makeAnnotatedToolCatalogResponses()
			input := []map[string]any{
				{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "Work on this engineering task."}}},
			}
			hdrs := map[string]string{"Authorization": "Bearer sk-proj-bench", "x-cave-session": fmt.Sprintf("real-oai-%d", si)}
			sendJSON(t, srv, "/v1/responses", hdrs, map[string]any{"model": "gpt-6.1-sol", "instructions": sysPrompt, "tools": tools, "input": input})
			for idx, step := range steps {
				for j := range step {
					cid := fmt.Sprintf("fc_%d_%d", idx, j)
					input = append(input,
						map[string]any{"type": "function_call", "call_id": cid, "name": "repo_tool_00", "arguments": "{}"},
					)
				}
				for j, tr := range step {
					cid := fmt.Sprintf("fc_%d_%d", idx, j)
					input = append(input,
						map[string]any{"type": "function_call_output", "call_id": cid, "output": tr.content},
					)
				}
				sendJSON(t, srv, "/v1/responses", hdrs, map[string]any{"model": "gpt-6.1-sol", "instructions": sysPrompt, "tools": tools, "input": input})
			}
			return up.totalCostUSD, sk.cavemanBustCount()
		}
		ob, _ := runOAI("record")
		oo, obusts := runOAI("compress")
		if obusts > 0 {
			t.Errorf("real session %d openai had %d caveman cache busts", si, obusts)
		}
		oaiBaseUSD += ob
		oaiOptUSD += oo

		// Replay on Gemini StreamGenerateContent
		runGem := func(mode string) (float64, int) {
			up := newStatefulUpstream()
			sk := &e2eRecordingSink{}
			srv, _, _ := buildBenchServer(t, mode, up, sk)
			tools := makeAnnotatedToolCatalogGemini()
			contents := []map[string]any{
				{"role": "user", "parts": []map[string]any{{"text": "Work on this engineering task."}}},
			}
			hdrs := map[string]string{"x-goog-api-key": "AIzaSyBenchKey", "x-cave-session": fmt.Sprintf("real-gem-%d", si)}
			ep := "/gemini/v1beta/models/gemini-2.5-pro:streamGenerateContent"
			sendJSON(t, srv, ep, hdrs, map[string]any{"systemInstruction": map[string]any{"parts": []map[string]any{{"text": sysPrompt}}}, "tools": tools, "contents": contents})
			for _, step := range steps {
				calls := make([]map[string]any, 0, len(step))
				resps := make([]map[string]any, 0, len(step))
				for _, tr := range step {
					calls = append(calls, map[string]any{"functionCall": map[string]any{"name": "repo_tool_00", "args": map[string]any{}}})
					resps = append(resps, map[string]any{"functionResponse": map[string]any{"name": "repo_tool_00", "response": map[string]any{"output": tr.content}}})
				}
				contents = append(contents,
					map[string]any{"role": "model", "parts": calls},
					map[string]any{"role": "user", "parts": resps},
				)
				sendJSON(t, srv, ep, hdrs, map[string]any{"systemInstruction": map[string]any{"parts": []map[string]any{{"text": sysPrompt}}}, "tools": tools, "contents": contents})
			}
			return up.totalCostUSD, sk.cavemanBustCount()
		}
		gb, _ := runGem("record")
		go_, gbusts := runGem("compress")
		if gbusts > 0 {
			t.Errorf("real session %d gemini had %d caveman cache busts", si, gbusts)
		}
		gemBaseUSD += gb
		gemOptUSD += go_
	}

	t.Logf("\n=== REAL MULTI-TURN SESSION REPLAY (%d sessions) ===", replayedSessions)
	t.Logf("  Anthropic (Claude Code cached): $%.4f -> $%.4f (%.1f%% saved)", claudeBaseUSD, claudeOptUSD, (claudeBaseUSD-claudeOptUSD)/max(1e-9, claudeBaseUSD)*100)
	t.Logf("  OpenAI Responses (gpt-6.1-sol): $%.4f -> $%.4f (%.1f%% saved)", oaiBaseUSD, oaiOptUSD, (oaiBaseUSD-oaiOptUSD)/max(1e-9, oaiBaseUSD)*100)
	t.Logf("  Gemini Stream (gemini-2.5-pro): $%.4f -> $%.4f (%.1f%% saved)", gemBaseUSD, gemOptUSD, (gemBaseUSD-gemOptUSD)/max(1e-9, gemBaseUSD)*100)
}

// TestE2ENativeCodemodeHarnessOptimizations measures the stacked non-interfering
// optimizations built specifically for Claude Code, Codex CLI, and OpenCode
// (even when those harnesses use their own native codemodes):
//  1. Harness-envelope unwrapping (Claude <system-reminder>, Codex text exec_command, OpenCode <path><content>)
//  2. Live-zone duplicate-read collapsing (0 cache busts, CCR-recoverable)
//  3. Canonical tools[] ordering (prevents MCP reconnect shuffles from busting caches)
//  4. Turn-aware reasoning effort stepping (steps down clean mechanical tool turns, keeps high on errors/prompts)
//
// All figures are local offline tokenizer estimates (`inferred`).
func TestE2ENativeCodemodeHarnessOptimizations(t *testing.T) {
	// Realistic payloads with harness envelopes:
	var goFile strings.Builder
	goFile.WriteString("package engine\n\nimport \"fmt\"\n\n")
	for i := 0; i < 35; i++ {
		fmt.Fprintf(&goFile, "func Handler%02d(id int, name string) string {\n\tout := fmt.Sprintf(\"item-%%d-%%s\", id, name)\n\tfor k := 0; k < 5; k++ {\n\t\tout += \"!\"\n\t}\n\treturn out\n}\n\n", i)
	}
	var numberedGo strings.Builder
	for i, line := range strings.Split(goFile.String(), "\n") {
		fmt.Fprintf(&numberedGo, "%4d→%s\n", i+1, line)
	}
	claudeReadPayload := numberedGo.String() + "\n<system-reminder>\nCheck for edge cases.\n</system-reminder>"

	var testLog strings.Builder
	for i := 1; i <= 45; i++ {
		fmt.Fprintf(&testLog, "=== RUN   TestUnit%02d\n--- PASS: TestUnit%02d (0.01s)\n", i, i)
	}
	testLog.WriteString("=== RUN   TestFinalCheck\n--- PASS: TestFinalCheck (0.01s)\nPASS\nok\tgithub.com/example/app\t0.42s\n")
	codexPassExec := "Chunk ID: 7ae69c\nWall time: 0.4210 seconds\nProcess exited with code 0\nOriginal token count: 1650\nOutput:\n" + testLog.String()

	var jsonRows strings.Builder
	jsonRows.WriteString("[")
	for i := 0; i < 35; i++ {
		if i > 0 {
			jsonRows.WriteByte(',')
		}
		fmt.Fprintf(&jsonRows, `{"id":%d,"service":"billing-%d","region":"us-east-1","status":"healthy","details":"standard verbose service manifest entry %d"}`, i, i, i)
	}
	jsonRows.WriteString("]")
	openCodeReadPayload := "<path>/workspace/services.json</path>\n<type>file</type>\n<content>\n" + jsonRows.String() + "\n</content>"

	sysPrompt := strings.Repeat("You are an expert coding agent with strict safety and verification standards. ", 45)

	buildHarnessServer := func(mode string, up *statefulUpstream, sk *e2eRecordingSink) *gateway.Server {
		dbPath := filepath.Join(t.TempDir(), "bench.db")
		st, err := store.Open(dbPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		ccrStore, err := ccr.OpenMemory()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ccrStore.Close() })

		opts := map[string]bool{
			"anthropic-cache-breakpoints": true,
			"openai-prompt-cache-key":     true,
		}
		if mode == "compress" {
			opts["canonical-tool-order"] = true
			opts["turn-effort-step"] = true
			opts["anthropic-defer-mcp-tools"] = true
			opts["openai-defer-mcp-tools"] = true
		}
		cfg := config.Config{
			Mode:           mode,
			BreakpointPlan: "frontier",
			Optimizers:     opts,
		}
		return New(cfg, sk, Options{
			HTTPClient:     &http.Client{Transport: up},
			Compressor:     NewEngineCompressor(ccrStore),
			PrefixCache:    st,
			RecoveryViaMCP: true,
		})
	}

	// --- 1. Claude Code (Opus 5.5): shuffled MCP tools + repeated <system-reminder> Read + cache-safe per-message turn-effort ---
	runClaude := func(mode string) (float64, int, int, int) {
		up := newStatefulUpstream()
		sk := &e2eRecordingSink{}
		srv := buildHarnessServer(mode, up, sk)
		hdrs := map[string]string{"x-api-key": "sk-ant-bench", "anthropic-version": "2023-06-01", "x-cave-session": "claude-native-opt"}

		toolsOrderA := makeAnnotatedToolCatalogAnthropic()
		toolsOrderB := append([]map[string]any(nil), toolsOrderA...)
		// Reverse order on turn 2 & 4 to simulate MCP server reconnect shuffling!
		for i, j := 0, len(toolsOrderB)-1; i < j; i, j = i+1, j-1 {
			toolsOrderB[i], toolsOrderB[j] = toolsOrderB[j], toolsOrderB[i]
		}

		msgs := []map[string]any{
			{"role": "user", "content": "Inspect handler.go and verify tests."},
		}
		// Turn 1: user prompt (must keep top-level effort=high, no marks)
		sendJSON(t, srv, "/v1/messages", hdrs, map[string]any{"model": "claude-opus-5-5", "max_tokens": 1024, "output_config": map[string]any{"effort": "high"}, "system": sysPrompt, "tools": toolsOrderA, "messages": msgs})

		// Turn 2: first Read (with <system-reminder>, shuffled toolsOrderB) -> clean tool continuation (appends per-message mark("low")!)
		msgs = append(msgs,
			map[string]any{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": "t1", "name": "Read", "input": map[string]any{"path": "handler.go"}}}},
			map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "t1", "content": claudeReadPayload}}},
		)
		sendJSON(t, srv, "/v1/messages", hdrs, map[string]any{"model": "claude-opus-5-5", "max_tokens": 1024, "output_config": map[string]any{"effort": "high"}, "system": sysPrompt, "tools": toolsOrderB, "messages": msgs})

		// Turn 3: duplicate Read of exact same handler.go -> collapses in live zone & replays mark("low")!
		msgs = append(msgs,
			map[string]any{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": "t2", "name": "Read", "input": map[string]any{"path": "handler.go"}}}},
			map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "t2", "content": claudeReadPayload}}},
		)
		sendJSON(t, srv, "/v1/messages", hdrs, map[string]any{"model": "claude-opus-5-5", "max_tokens": 1024, "output_config": map[string]any{"effort": "high"}, "system": sysPrompt, "tools": toolsOrderA, "messages": msgs})

		// Turn 4: failing test tool_result -> replays mark("low") and restores effort="high" via per-message mark("high")!
		msgs = append(msgs,
			map[string]any{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": "t3", "name": "Bash", "input": map[string]any{"cmd": "go test"}}}},
			map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "t3", "is_error": true, "content": "--- FAIL: TestHandler01"}}},
		)
		sendJSON(t, srv, "/v1/messages", hdrs, map[string]any{"model": "claude-opus-5-5", "max_tokens": 1024, "output_config": map[string]any{"effort": "high"}, "system": sysPrompt, "tools": toolsOrderB, "messages": msgs})

		stepped := 0
		if mode == "compress" {
			const lowMark = `{"role":"system","content":[],"output_config":{"effort":"low"}}`
			const highMark = `{"role":"system","content":[],"output_config":{"effort":"high"}}`
			if strings.Contains(string(up.upstreamBodies[1]), lowMark) {
				stepped++
			}
			if strings.Contains(string(up.upstreamBodies[2]), lowMark) {
				stepped++
			}
			if !strings.Contains(string(up.upstreamBodies[3]), highMark) {
				t.Errorf("Claude Opus 5.5 failing test turn should restore effort=high via per-message mark: %s", up.upstreamBodies[3])
			}
			// Top-level output_config.effort MUST remain frozen at "high" on every turn (0 cache bust!).
			for turnIdx, raw := range up.upstreamBodies {
				var doc map[string]any
				_ = json.Unmarshal(raw, &doc)
				oc, _ := doc["output_config"].(map[string]any)
				if oc["effort"] != "high" {
					t.Errorf("turn %d mutated top-level output_config.effort=%v (must stay frozen at high!)", turnIdx+1, oc["effort"])
				}
			}
		}
		return up.totalCostUSD, up.cachedTokens, sk.cavemanBustCount(), stepped
	}
	cBaseUSD, cBaseCached, _, _ := runClaude("record")
	cOptUSD, cOptCached, cBusts, cStepped := runClaude("compress")
	if cBusts > 0 {
		t.Fatalf("Claude optimized run had %d caveman cache busts", cBusts)
	}
	if cStepped != 2 {
		t.Fatalf("expected 2 Claude Opus 5.5 mechanical turns stepped down cache-safely, got %d", cStepped)
	}
	if cOptCached <= cBaseCached {
		t.Fatalf("expected Claude Opus 5.5 cache reads to grow with canonical tools + per-message marks: base=%d opt=%d", cBaseCached, cOptCached)
	}
	if cOptUSD >= cBaseUSD {
		t.Fatalf("expected Claude optimized cost < baseline: base=$%.4f opt=$%.4f", cBaseUSD, cOptUSD)
	}

	// --- 2. Codex CLI (/v1/responses, GPT-6): text exec_command envelope + duplicate read + shuffled tools + cache-safe configuration_update ---
	runCodex := func(mode string) (float64, int, int) {
		up := newStatefulUpstream()
		sk := &e2eRecordingSink{}
		srv := buildHarnessServer(mode, up, sk)
		hdrs := map[string]string{"Authorization": "Bearer sk-openai-bench", "x-cave-session": "codex-native-opt"}

		toolsA := makeAnnotatedToolCatalogResponses()
		toolsB := append([]map[string]any(nil), toolsA...)
		for i, j := 0, len(toolsB)-1; i < j; i, j = i+1, j-1 {
			toolsB[i], toolsB[j] = toolsB[j], toolsB[i]
		}

		input := []map[string]any{
			{"role": "user", "content": "Run tests and inspect handler.go."},
		}
		sendJSON(t, srv, "/v1/responses", hdrs, map[string]any{"model": "gpt-6-sol", "reasoning": map[string]any{"effort": "xhigh"}, "instructions": sysPrompt, "tools": toolsA, "input": input})

		input = append(input,
			map[string]any{"type": "function_call", "call_id": "c1", "name": "exec_command", "arguments": `{"cmd":"go test -v"}`},
			map[string]any{"type": "function_call_output", "call_id": "c1", "output": codexPassExec},
		)
		sendJSON(t, srv, "/v1/responses", hdrs, map[string]any{"model": "gpt-6-sol", "reasoning": map[string]any{"effort": "xhigh"}, "instructions": sysPrompt, "tools": toolsB, "input": input})

		// Duplicate exec_command output in live zone -> collapses!
		input = append(input,
			map[string]any{"type": "function_call", "call_id": "c2", "name": "exec_command", "arguments": `{"cmd":"go test -v"}`},
			map[string]any{"type": "function_call_output", "call_id": "c2", "output": codexPassExec},
		)
		sendJSON(t, srv, "/v1/responses", hdrs, map[string]any{"model": "gpt-6-sol", "reasoning": map[string]any{"effort": "xhigh"}, "instructions": sysPrompt, "tools": toolsA, "input": input})

		// Turn 4: short mechanical user continuation ("continue") -> cache-safe GPT-6 configuration_update("medium")!
		input = append(input,
			map[string]any{"role": "assistant", "content": "Tests passed."},
			map[string]any{"role": "user", "content": "continue"},
		)
		sendJSON(t, srv, "/v1/responses", hdrs, map[string]any{"model": "gpt-6-sol", "reasoning": map[string]any{"effort": "xhigh"}, "instructions": sysPrompt, "tools": toolsB, "input": input})

		// Turn 5: substantive user prompt -> replays update("medium") and restores configuration_update("xhigh")!
		input = append(input,
			map[string]any{"role": "assistant", "content": "Applied."},
			map[string]any{"role": "user", "content": "Now audit the authentication middleware for race conditions."},
		)
		sendJSON(t, srv, "/v1/responses", hdrs, map[string]any{"model": "gpt-6-sol", "reasoning": map[string]any{"effort": "xhigh"}, "instructions": sysPrompt, "tools": toolsA, "input": input})

		stepped := 0
		if mode == "compress" {
			const medUpdate = `{"type":"configuration_update","reasoning":{"effort":"medium"}}`
			const xhighUpdate = `{"type":"configuration_update","reasoning":{"effort":"xhigh"}}`
			if strings.Contains(string(up.upstreamBodies[3]), medUpdate) {
				stepped++
			}
			if !strings.Contains(string(up.upstreamBodies[4]), xhighUpdate) {
				t.Errorf("Codex GPT-6 substantive turn should restore effort=xhigh via configuration_update: %s", up.upstreamBodies[4])
			}
			// Top-level reasoning.effort MUST remain frozen at "xhigh" on every turn!
			for turnIdx, raw := range up.upstreamBodies {
				var doc map[string]any
				_ = json.Unmarshal(raw, &doc)
				rObj, _ := doc["reasoning"].(map[string]any)
				if rObj["effort"] != "xhigh" {
					t.Errorf("Codex turn %d mutated top-level reasoning.effort=%v (must stay frozen at xhigh!)", turnIdx+1, rObj["effort"])
				}
			}
		}
		return up.totalCostUSD, sk.cavemanBustCount(), stepped
	}
	xBaseUSD, _, _ := runCodex("record")
	xOptUSD, xBusts, xStepped := runCodex("compress")
	if xBusts > 0 {
		t.Fatalf("Codex optimized run had %d caveman cache busts", xBusts)
	}
	if xStepped != 1 {
		t.Fatalf("expected 1 Codex GPT-6 mechanical continuation stepped down cache-safely, got %d", xStepped)
	}
	if xOptUSD >= xBaseUSD {
		t.Fatalf("expected Codex optimized cost < baseline: base=$%.4f opt=$%.4f", xBaseUSD, xOptUSD)
	}

	// --- 3. OpenCode (/v1/chat/completions): <path><content> read envelope + duplicate read + canonical tools (0 top-level effort mutation!) ---
	runOpenCode := func(mode string) (float64, int) {
		up := newStatefulUpstream()
		sk := &e2eRecordingSink{}
		srv := buildHarnessServer(mode, up, sk)
		hdrs := map[string]string{"Authorization": "Bearer sk-openai-bench", "x-cave-agent": "opencode", "x-cave-session": "opencode-native-opt"}

		toolsA := makeAnnotatedToolCatalogOpenAI()
		toolsB := append([]map[string]any(nil), toolsA...)
		for i, j := 0, len(toolsB)-1; i < j; i, j = i+1, j-1 {
			toolsB[i], toolsB[j] = toolsB[j], toolsB[i]
		}

		msgs := []map[string]any{
			{"role": "system", "content": sysPrompt},
			{"role": "user", "content": "Inspect services.json"},
		}
		sendJSON(t, srv, "/v1/chat/completions", hdrs, map[string]any{"model": "gpt-5.4", "reasoning_effort": "high", "tools": toolsA, "messages": msgs})

		msgs = append(msgs,
			map[string]any{"role": "assistant", "tool_calls": []map[string]any{{"id": "tc1", "type": "function", "function": map[string]any{"name": "read", "arguments": `{"path":"services.json"}`}}}},
			map[string]any{"role": "tool", "tool_call_id": "tc1", "content": openCodeReadPayload},
		)
		sendJSON(t, srv, "/v1/chat/completions", hdrs, map[string]any{"model": "gpt-5.4", "reasoning_effort": "high", "tools": toolsB, "messages": msgs})

		msgs = append(msgs,
			map[string]any{"role": "assistant", "tool_calls": []map[string]any{{"id": "tc2", "type": "function", "function": map[string]any{"name": "read", "arguments": `{"path":"services.json"}`}}}},
			map[string]any{"role": "tool", "tool_call_id": "tc2", "content": openCodeReadPayload},
		)
		sendJSON(t, srv, "/v1/chat/completions", hdrs, map[string]any{"model": "gpt-5.4", "reasoning_effort": "high", "tools": toolsA, "messages": msgs})

		if mode == "compress" {
			// Chat Completions has no per-message effort mark -> top-level reasoning_effort MUST stay "high" (0 cache bust).
			for turnIdx, raw := range up.upstreamBodies {
				if !strings.Contains(string(raw), `"reasoning_effort":"high"`) {
					t.Errorf("OpenCode turn %d mutated top-level reasoning_effort (cache bust risk): %s", turnIdx+1, raw)
				}
			}
		}
		return up.totalCostUSD, sk.cavemanBustCount()
	}
	ocBaseUSD, _ := runOpenCode("record")
	ocOptUSD, ocBusts := runOpenCode("compress")
	if ocBusts > 0 {
		t.Fatalf("OpenCode optimized run had %d caveman cache busts", ocBusts)
	}
	if ocOptUSD >= ocBaseUSD {
		t.Fatalf("expected OpenCode optimized cost < baseline: base=$%.4f opt=$%.4f", ocBaseUSD, ocOptUSD)
	}

	t.Logf("\n=== NATIVE-CODEMODE HARNESS OPTIMIZATIONS (inferred, 100%% cache-safe, 0 caveman cache busts) ===")
	t.Logf("  Claude Code Opus 5.5 (shuffled MCP + <system-reminder> + dup-read + per-msg turn-effort): $%.4f -> $%.4f (%.1f%% saved; cached_tok %d -> %d; %d/2 turns stepped)",
		cBaseUSD, cOptUSD, (cBaseUSD-cOptUSD)/max(1e-9, cBaseUSD)*100, cBaseCached, cOptCached, cStepped)
	t.Logf("  Codex CLI GPT-6      (shuffled MCP + exec_command env + dup-read + cfg_update effort):    $%.4f -> $%.4f (%.1f%% saved; %d/1 continuation stepped)",
		xBaseUSD, xOptUSD, (xBaseUSD-xOptUSD)/max(1e-9, xBaseUSD)*100, xStepped)
	t.Logf("  OpenCode Chat        (shuffled MCP + <path><content> + dup-read, top-level effort safe):  $%.4f -> $%.4f (%.1f%% saved)",
		ocBaseUSD, ocOptUSD, (ocBaseUSD-ocOptUSD)/max(1e-9, ocBaseUSD)*100)
}
