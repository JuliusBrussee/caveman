package bedrock

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/shared/platform/env"
)

// ParseUsage extracts token usage from a Bedrock response. Bedrock reports usage
// in two shapes depending on the operation:
//
//   - Converse / ConverseStream: camelCase {"usage":{"inputTokens","outputTokens",
//     "cacheReadInputTokens","cacheWriteInputTokens"}}
//   - InvokeModel (Anthropic models): native Anthropic snake_case
//     {"usage":{"input_tokens","output_tokens","cache_read_input_tokens",
//     "cache_creation_input_tokens"}}
//
// Both are handled. The cache_status is honest: hit/write are only asserted when
// Bedrock actually reports cache telemetry, otherwise it stays "unknown".
func (a Adapter) ParseUsage(ctx context.Context, responseHeaders http.Header, streamOrBody io.Reader) (providers.UsageObservation, io.Reader, error) {
	data, err := io.ReadAll(streamOrBody)
	usage := newUsage(responseHeaders)
	if err != nil {
		return usage, bytes.NewReader(data), err
	}
	parseBedrockUsage(data, &usage)
	usage.CacheStatus = cacheStatusFor(usage)
	return usage, bytes.NewReader(data), nil
}

// NewUsageScanner returns a Bedrock-aware usage scanner so streamed
// (converse-stream / invoke-with-response-stream) responses are parsed with the
// same camelCase rules as non-streamed ones.
func (a Adapter) NewUsageScanner(responseHeaders http.Header) *providers.UsageScanner {
	scanner := providers.NewExternalUsageScanner(responseHeaders, parseBedrockUsageScanner)
	scanner.SetLimit(env.Int("CAVE_USAGE_SCAN_MAX_BYTES", 16*1024*1024))
	scanner.SetRequestID(bedrockRequestID(responseHeaders))
	return scanner
}

// bedrockRequestID prefers the AWS x-amzn-requestid header, falling back to the
// generic x-request-id.
func bedrockRequestID(h http.Header) string {
	if id := h.Get("x-amzn-requestid"); id != "" {
		return id
	}
	return h.Get("x-request-id")
}

// parseBedrockUsageScanner adapts parseBedrockUsage to the scanner callback
// signature, also resolving cache_status.
func parseBedrockUsageScanner(data []byte, usage *providers.UsageObservation) {
	parseBedrockUsage(data, usage)
	usage.CacheStatus = cacheStatusFor(*usage)
}

func newUsage(h http.Header) providers.UsageObservation {
	return providers.UsageObservation{CacheStatus: "unknown", ProviderRequestID: bedrockRequestID(h), ObservationCount: 1}
}

// parseBedrockUsage merges usage from a whole-body JSON document, SSE/JSONL,
// or the binary application/vnd.amazon.eventstream framing used by Bedrock's
// real ConverseStream/InvokeModelWithResponseStream APIs. Counters are merged
// with a max rule so cumulative stream totals resolve correctly.
func parseBedrockUsage(data []byte, usage *providers.UsageObservation) {
	if payloads, ok := bedrockEventPayloads(data); ok {
		streamed, terminalUsage := false, false
		for _, payload := range payloads {
			parseBedrockPayload(payload, usage)
			isStream, isTerminal := bedrockPayloadCompletion(payload)
			streamed = streamed || isStream
			terminalUsage = terminalUsage || isTerminal
		}
		if streamed && !terminalUsage {
			// A frame-boundary truncation can leave a perfectly valid message_start
			// frame carrying provisional input/output usage. Without the terminal
			// metadata/message_delta usage, the bill is incomplete.
			usage.OutputTokens = 0
			usage.OutputTokensReported = false
			usage.ReasoningTokens = 0
			// The stamp cannot come from ParseUsageBytes: mergeBedrockUsage hands
			// it one synthesized {"usage":{...}} document with none of the
			// message_start/message_delta markers its own truncation detection
			// keys on. Truncation is detected HERE, so it must be labelled here,
			// or RawUsage keeps the provisional output_tokens with nothing saying
			// the parser refused it.
			providers.MarkRawUsageIncomplete(usage)
		}
		return
	}
	parseBedrockText(data, usage)
}

func bedrockPayloadCompletion(payload []byte) (streamed, terminalUsage bool) {
	var obj map[string]any
	if decodeBedrockObject(payload, &obj) != nil {
		return false, false
	}
	if encoded, ok := obj["bytes"].(string); ok {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return true, false
		}
		return bedrockPayloadCompletion(decoded)
	}
	if typ, _ := obj["type"].(string); typ != "" {
		switch typ {
		case "message_start", "message_delta", "message_stop", "content_block_start", "content_block_delta", "content_block_stop":
			streamed = true
		}
		if typ == "message_delta" {
			if raw, ok := obj["usage"].(map[string]any); ok {
				_, terminalUsage = raw["output_tokens"]
			}
		}
	}
	if _, ok := obj["messageStart"]; ok {
		streamed = true
	}
	if _, ok := obj["messageStop"]; ok {
		streamed = true
	}
	if metadata, ok := obj["metadata"].(map[string]any); ok {
		streamed = true
		if raw, ok := metadata["usage"].(map[string]any); ok {
			_, input := raw["inputTokens"]
			_, output := raw["outputTokens"]
			terminalUsage = input && output
		}
	}
	return streamed, terminalUsage
}

func parseBedrockText(data []byte, usage *providers.UsageObservation) {
	var root map[string]any
	if decodeBedrockObject(data, &root) == nil {
		mergeBedrockUsage(root, usage)
		return
	}
	streamed, terminalUsage := false, false
	scanner := bufio.NewScanner(bytes.NewReader(data))
	maxLine := len(data) + 1
	if maxLine < 64*1024 {
		maxLine = 64 * 1024
	}
	scanner.Buffer(make([]byte, 0, 64*1024), maxLine)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "event:") || strings.HasPrefix(line, ":") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if line == "" || line == "[DONE]" {
			continue
		}
		var obj map[string]any
		if decodeBedrockObject([]byte(line), &obj) == nil {
			mergeBedrockUsage(obj, usage)
			isStream, isTerminal := bedrockPayloadCompletion([]byte(line))
			streamed = streamed || isStream
			terminalUsage = terminalUsage || isTerminal
		}
	}
	if streamed && !terminalUsage {
		// Mantle uses Anthropic SSE rather than AWS EventStream. A truncated SSE
		// can still contain provisional message_start usage; never promote that
		// partial prefix into complete billable usage.
		usage.OutputTokens = 0
		usage.OutputTokensReported = false
		usage.ReasoningTokens = 0
		// Same reason as the EventStream site above: this parser detects the
		// truncation, so this parser owns labelling the raw blob.
		providers.MarkRawUsageIncomplete(usage)
	}
}

// parseBedrockPayload handles both ConverseStream JSON events and InvokeModel's
// chunk wrapper ({"bytes":"<base64 provider-native chunk>"}).
func parseBedrockPayload(payload []byte, usage *providers.UsageObservation) {
	var wrapper map[string]any
	if decodeBedrockObject(payload, &wrapper) == nil {
		if encoded, ok := wrapper["bytes"].(string); ok {
			if decoded, err := base64.StdEncoding.DecodeString(encoded); err == nil {
				parseBedrockUsage(decoded, usage)
				return
			}
		}
		mergeBedrockUsage(wrapper, usage)
		return
	}
	parseBedrockText(payload, usage)
}

// bedrockEventPayloads decodes AWS event-stream frames and validates both CRCs.
// Invalid/truncated frames return ok=false so no partial binary payload can be
// mistaken for provider usage and priced.
func bedrockEventPayloads(data []byte) (payloads [][]byte, ok bool) {
	if len(data) < 16 {
		return nil, false
	}
	remaining := data
	for len(remaining) > 0 {
		if len(remaining) < 16 {
			return nil, false
		}
		total := int(binary.BigEndian.Uint32(remaining[0:4]))
		headers := int(binary.BigEndian.Uint32(remaining[4:8]))
		if total < 16 || total > len(remaining) || headers < 0 || 12+headers > total-4 {
			return nil, false
		}
		if got, want := crc32.ChecksumIEEE(remaining[:8]), binary.BigEndian.Uint32(remaining[8:12]); got != want {
			return nil, false
		}
		frame := remaining[:total]
		if got, want := crc32.ChecksumIEEE(frame[:total-4]), binary.BigEndian.Uint32(frame[total-4:]); got != want {
			return nil, false
		}
		payloads = append(payloads, append([]byte(nil), frame[12+headers:total-4]...))
		remaining = remaining[total:]
	}
	return payloads, len(payloads) > 0
}

// mergeBedrockUsage reads usage from the Bedrock shapes (camelCase converse,
// snake_case anthropic-invoke, nested metadata/message.usage) and merges the
// maximum value seen for each field.
func mergeBedrockUsage(obj map[string]any, usage *providers.UsageObservation) {
	mergeBedrockServiceTier(obj, usage)
	for _, u := range bedrockUsageObjects(obj) {
		validateBedrockRawTotal(u, usage)
		normalized := make(map[string]any, len(u)+1)
		for key, value := range u {
			normalized[key] = value
		}
		copyBedrockAliases(normalized, u, usage, "input_tokens", "inputTokens")
		copyBedrockAliases(normalized, u, usage, "output_tokens", "outputTokens")
		copyBedrockAliases(normalized, u, usage, "cache_read_input_tokens", "cacheReadInputTokens", "cacheReadInputTokenCount")
		copyBedrockAliases(normalized, u, usage, "cache_creation_input_tokens", "cacheWriteInputTokens", "cacheWriteInputTokenCount")
		normalizeBedrockCacheDetails(normalized, u, usage)
		// Bedrock Converse totalTokens is cache-inclusive, unlike the raw
		// Anthropic Invoke total_tokens shape. Validate it above against the
		// provider-specific raw counters, then remove it before the shared
		// Anthropic normalizer adds cache subsets to effective input.
		delete(normalized, "totalTokens")
		delete(normalized, "total_tokens")
		raw, err := json.Marshal(map[string]any{"usage": normalized})
		if err != nil {
			usage.Malformed = true
			continue
		}
		// Both Bedrock Converse and Anthropic Invoke report their base input
		// counter exclusive of cache read/write tokens. The shared Anthropic
		// normalizer adds those subsets exactly once.
		providers.ParseUsageBytes("anthropic", raw, usage)
	}
}

// normalizeBedrockCacheDetails maps Bedrock's TTL-aware cacheDetails array to
// the provider-neutral Anthropic cache_creation shape. The shared normalizer
// then validates that the 5m+1h detail sum equals cacheWriteInputTokens and
// preserves the breakdown needed for honest tiered pricing.
func normalizeBedrockCacheDetails(dst, src map[string]any, usage *providers.UsageObservation) {
	raw, present := src["cacheDetails"]
	if !present {
		return
	}
	details, ok := raw.([]any)
	if !ok {
		usage.Malformed = true
		return
	}
	byTTL := map[string]int{"5m": 0, "1h": 0}
	for _, rawDetail := range details {
		detail, ok := rawDetail.(map[string]any)
		if !ok {
			usage.Malformed = true
			return
		}
		ttl, ok := detail["ttl"].(string)
		if !ok || (ttl != "5m" && ttl != "1h") {
			usage.Malformed = true
			return
		}
		tokens, valid, tokenPresent := bedrockCounter(detail, "inputTokens", "input_tokens")
		if !tokenPresent || !valid || byTTL[ttl] > math.MaxInt-tokens {
			usage.Malformed = true
			return
		}
		byTTL[ttl] += tokens
	}
	cacheCreation := map[string]any{
		"ephemeral_5m_input_tokens": byTTL["5m"],
		"ephemeral_1h_input_tokens": byTTL["1h"],
	}
	if existing, exists := dst["cache_creation"]; exists {
		existingMap, ok := existing.(map[string]any)
		if !ok {
			usage.Malformed = true
		} else {
			for key, value := range cacheCreation {
				current, valid, currentPresent := bedrockCounter(existingMap, key)
				if !currentPresent || !valid || current != value {
					usage.Malformed = true
				}
			}
		}
	}
	dst["cache_creation"] = cacheCreation
}

func mergeBedrockServiceTier(obj map[string]any, usage *providers.UsageObservation) {
	for _, key := range []string{"serviceTier", "service_tier"} {
		raw, present := obj[key]
		if !present {
			continue
		}
		tier := ""
		switch value := raw.(type) {
		case string:
			tier = strings.TrimSpace(value)
		case map[string]any:
			if typed, ok := value["type"].(string); ok {
				tier = strings.TrimSpace(typed)
			}
		}
		if tier == "" {
			usage.PricingUnsupportedReason = "unsupported_service_tier_shape"
		} else {
			usage.ServiceTier = tier
		}
		return
	}
}

func validateBedrockRawTotal(u map[string]any, usage *providers.UsageObservation) {
	kind := bedrockUsageKindFor(u)
	if kind == bedrockUsageMixed {
		// Runtime Converse/ConverseStream and Mantle use different total
		// semantics. A partially converted object must not be allowed to pick
		// whichever relation happens to make its total pass.
		usage.Malformed = true
		return
	}
	total, totalOK, totalPresent := bedrockCounter(u, "totalTokens", "total_tokens")
	if !totalPresent {
		return
	}
	input, inputOK, inputPresent := bedrockCounter(u, "inputTokens", "input_tokens", "prompt_tokens")
	output, outputOK, outputPresent := bedrockCounter(u, "outputTokens", "output_tokens", "completion_tokens")
	cacheRead, cacheReadOK, cacheReadPresent := bedrockCounter(u, "cacheReadInputTokens", "cacheReadInputTokenCount", "cache_read_input_tokens", "cached_input_tokens")
	cacheWrite, cacheWriteOK, cacheWritePresent := bedrockCounter(u, "cacheWriteInputTokens", "cacheWriteInputTokenCount", "cache_creation_input_tokens")
	if !totalOK || (inputPresent && !inputOK) || (outputPresent && !outputOK) ||
		(cacheReadPresent && !cacheReadOK) || (cacheWritePresent && !cacheWriteOK) {
		usage.Malformed = true
		return
	}
	if inputOK && outputOK {
		// Converse/ConverseStream use camelCase counters and define totalTokens
		// over total input (base + cache read + cache write) plus output. Mantle's
		// Anthropic snake_case usage keeps its native total_tokens relation, so do
		// not silently reinterpret that separate provider shape.
		expectedInput := input
		if kind == bedrockUsageConverse {
			var ok bool
			expectedInput, ok = bedrockCheckedAdd(expectedInput, optionalBedrockCounter(cacheRead, cacheReadOK))
			if ok {
				expectedInput, ok = bedrockCheckedAdd(expectedInput, optionalBedrockCounter(cacheWrite, cacheWriteOK))
			}
			if !ok {
				usage.Malformed = true
				return
			}
		}
		expectedTotal, ok := bedrockCheckedAdd(expectedInput, output)
		if !ok || total != expectedTotal {
			usage.Malformed = true
		}
	}
}

type bedrockUsageKind uint8

const (
	bedrockUsageUnknown bedrockUsageKind = iota
	bedrockUsageConverse
	bedrockUsageMantle
	bedrockUsageMixed
)

// bedrockUsageKindFor separates the camelCase Runtime Converse contract from
// Mantle's Anthropic-compatible snake_case contract. Exact duplicate aliases
// are tolerated only when every populated logical field has both spellings;
// a partial/mixed object is rejected rather than choosing a total relation by
// whichever key happened to be present.
func bedrockUsageKindFor(u map[string]any) bedrockUsageKind {
	type aliasGroup struct {
		camel []string
		snake []string
	}
	groups := []aliasGroup{
		{camel: []string{"inputTokens"}, snake: []string{"input_tokens", "prompt_tokens"}},
		{camel: []string{"outputTokens"}, snake: []string{"output_tokens", "completion_tokens"}},
		{camel: []string{"totalTokens"}, snake: []string{"total_tokens"}},
		{camel: []string{"cacheReadInputTokens", "cacheReadInputTokenCount"}, snake: []string{"cache_read_input_tokens", "cached_input_tokens"}},
		{camel: []string{"cacheWriteInputTokens", "cacheWriteInputTokenCount"}, snake: []string{"cache_creation_input_tokens"}},
		{camel: []string{"cacheDetails"}, snake: []string{"cache_creation"}},
	}
	camel, snake := false, false
	for _, group := range groups {
		hasCamel := bedrockAnyKey(u, group.camel...)
		hasSnake := bedrockAnyKey(u, group.snake...)
		camel = camel || hasCamel
		snake = snake || hasSnake
	}
	if camel && snake {
		for _, group := range groups {
			if bedrockAnyKey(u, group.camel...) != bedrockAnyKey(u, group.snake...) {
				return bedrockUsageMixed
			}
		}
	}
	if camel {
		return bedrockUsageConverse
	}
	if snake {
		return bedrockUsageMantle
	}
	return bedrockUsageUnknown
}

func bedrockAnyKey(m map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := m[key]; ok {
			return true
		}
	}
	return false
}

func optionalBedrockCounter(value int, ok bool) int {
	if !ok {
		return 0
	}
	return value
}

func bedrockCheckedAdd(a, b int) (int, bool) {
	if a > math.MaxInt-b {
		return 0, false
	}
	return a + b, true
}

func bedrockCounter(m map[string]any, keys ...string) (value int, ok, present bool) {
	for _, key := range keys {
		raw, exists := m[key]
		if !exists {
			continue
		}
		present = true
		parsed, valid := bedrockNonNegativeInt(raw)
		if !valid {
			return 0, false, true
		}
		if ok && parsed != value {
			return 0, false, true
		}
		value, ok = parsed, true
	}
	return value, ok, present
}

func bedrockNonNegativeInt(v any) (int, bool) {
	var n64 int64
	switch n := v.(type) {
	case json.Number:
		parsed, err := strconv.ParseInt(n.String(), 10, 64)
		if err != nil {
			return 0, false
		}
		n64 = parsed
	case int:
		if n < 0 {
			return 0, false
		}
		return n, true
	case int64:
		n64 = n
	case float64:
		if n < 0 || math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n > float64(math.MaxInt64) {
			return 0, false
		}
		n64 = int64(n)
	default:
		return 0, false
	}
	if n64 < 0 || uint64(n64) > uint64(^uint(0)>>1) {
		return 0, false
	}
	return int(n64), true
}

func copyBedrockAliases(dst, src map[string]any, usage *providers.UsageObservation, snake string, camel ...string) {
	keys := append([]string{}, camel...)
	keys = append(keys, snake)
	value, valid, present := bedrockCounter(src, keys...)
	if !present {
		return
	}
	if !valid {
		usage.Malformed = true
		return
	}
	dst[snake] = value
}

// decodeBedrockObject preserves integer lexemes as json.Number. Decoding into
// float64 first loses precision above 2^53 and could turn an invalid/overflowed
// provider counter into a plausible billable integer.
func decodeBedrockObject(data []byte, dst *map[string]any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

// bedrockUsageObjects returns every usage-bearing sub-object reachable from obj.
// Bedrock places usage at the top level ("usage") for both Converse and
// InvokeModel; streamed converse emits it under "metadata".
func bedrockUsageObjects(obj map[string]any) []map[string]any {
	var out []map[string]any
	add := func(v any) {
		if m, ok := v.(map[string]any); ok {
			out = append(out, m)
		}
	}
	add(obj["usage"])
	if meta, ok := obj["metadata"].(map[string]any); ok {
		add(meta["usage"])
	}
	if msg, ok := obj["message"].(map[string]any); ok {
		add(msg["usage"])
	}
	return out
}

// cacheStatusFor maps observed cache usage to an honest cache_status, only
// asserting hit/write when Bedrock actually reported cache telemetry.
func cacheStatusFor(usage providers.UsageObservation) string {
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

// MapProviderError maps a Bedrock error response to a ProviderError, preferring
// the AWS x-amzn-errortype header (e.g. ThrottlingException,
// ValidationException) over a bare status code so the error code is meaningful.
func (a Adapter) MapProviderError(status int, headers http.Header, body []byte) providers.ProviderError {
	if t := headers.Get("x-amzn-errortype"); t != "" {
		// x-amzn-errortype can be "Type:url"; keep only the type.
		if idx := strings.IndexByte(t, ':'); idx > 0 {
			t = t[:idx]
		}
		return providers.ProviderError{Code: "bedrock_" + t, Message: string(body)}
	}
	return a.Base.MapProviderError(status, headers, body)
}
