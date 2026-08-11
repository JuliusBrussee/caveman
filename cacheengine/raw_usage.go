package cacheengine

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// ProviderUsageEvidence binds one provider response's exact usage object to a
// provider-counted total input-token denominator. RawUsage can be passed to
// ObserveRawCacheUsage or cachebench observation records.
type ProviderUsageEvidence struct {
	TotalInputTokens int
	OutputTokens     int
	RawUsage         json.RawMessage
}

// ExtractProviderUsage extracts cache counters and total input tokens from one
// complete, non-streaming provider response. Duplicate keys, missing totals,
// inconsistent cache counters, negative/fractional numbers, and overflow fail
// closed. Anthropic and Bedrock totals include uncached + cache read + cache
// write tokens, matching their provider contracts.
func ExtractProviderUsage(provider string, response []byte) (ProviderUsageEvidence, bool) {
	if !validUniqueJSONObject(response) {
		return ProviderUsageEvidence{}, false
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(response, &root) != nil {
		return ProviderUsageEvidence{}, false
	}
	usageField := "usage"
	if strings.EqualFold(strings.TrimSpace(provider), "gemini") {
		usageField = "usageMetadata"
	}
	raw, ok := root[usageField]
	if !ok || !validUniqueJSONObject(raw) {
		return ProviderUsageEvidence{}, false
	}
	var usage map[string]json.RawMessage
	if json.Unmarshal(raw, &usage) != nil {
		return ProviderUsageEvidence{}, false
	}
	cacheUsage, ok := NormalizeRawCacheUsage(provider, raw)
	if !ok {
		return ProviderUsageEvidence{}, false
	}
	var total, output int
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai":
		input, hasInput, inputOK := rawCounter(usage, "input_tokens")
		prompt, hasPrompt, promptOK := rawCounter(usage, "prompt_tokens")
		if !inputOK || !promptOK || hasInput == hasPrompt {
			return ProviderUsageEvidence{}, false
		}
		if hasInput {
			total = input
		} else {
			total = prompt
		}
		responsesOutput, hasResponsesOutput, responsesOutputOK := rawCounter(usage, "output_tokens")
		chatOutput, hasChatOutput, chatOutputOK := rawCounter(usage, "completion_tokens")
		if !responsesOutputOK || !chatOutputOK || hasResponsesOutput == hasChatOutput {
			return ProviderUsageEvidence{}, false
		}
		if hasResponsesOutput {
			output = responsesOutput
		} else {
			output = chatOutput
		}
	case "anthropic":
		uncached, exists, valid := rawCounter(usage, "input_tokens")
		if !valid || !exists {
			return ProviderUsageEvidence{}, false
		}
		var sumOK bool
		total, sumOK = safeCounterSum(uncached, cacheUsage.CachedInputTokens, cacheUsage.CacheCreationInputTokens)
		if !sumOK {
			return ProviderUsageEvidence{}, false
		}
		output, exists, valid = rawCounter(usage, "output_tokens")
		if !valid || !exists {
			return ProviderUsageEvidence{}, false
		}
	case "bedrock":
		uncached, exists, valid := rawCounter(usage, "inputTokens")
		if !valid || !exists {
			return ProviderUsageEvidence{}, false
		}
		var sumOK bool
		total, sumOK = safeCounterSum(uncached, cacheUsage.CachedInputTokens, cacheUsage.CacheCreationInputTokens)
		if !sumOK {
			return ProviderUsageEvidence{}, false
		}
		output, exists, valid = rawCounter(usage, "outputTokens")
		if !valid || !exists {
			return ProviderUsageEvidence{}, false
		}
	case "gemini":
		var exists, valid bool
		total, exists, valid = rawCounter(usage, "promptTokenCount")
		if !valid || !exists {
			return ProviderUsageEvidence{}, false
		}
		output, exists, valid = rawCounter(usage, "candidatesTokenCount")
		if !valid || !exists {
			return ProviderUsageEvidence{}, false
		}
	default:
		return ProviderUsageEvidence{}, false
	}
	if total <= 0 {
		return ProviderUsageEvidence{}, false
	}
	if cacheUsage.CachedInputTokens > total || cacheUsage.CacheCreationInputTokens > total-cacheUsage.CachedInputTokens {
		return ProviderUsageEvidence{}, false
	}
	return ProviderUsageEvidence{TotalInputTokens: total, OutputTokens: output, RawUsage: append(json.RawMessage(nil), raw...)}, true
}

func rawCounter(root map[string]json.RawMessage, name string) (int, bool, bool) {
	raw, exists := root[name]
	if !exists {
		return 0, false, true
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&number) != nil {
		return 0, true, false
	}
	parsed, err := strconv.ParseInt(string(number), 10, 64)
	if err != nil || parsed < 0 || parsed > int64(math.MaxInt) {
		return 0, true, false
	}
	return int(parsed), true, true
}

func safeCounterSum(values ...int) (int, bool) {
	total := 0
	for _, value := range values {
		if value < 0 || value > math.MaxInt-total {
			return 0, false
		}
		total += value
	}
	return total, true
}

// ObserveRawCacheUsage accepts a provider usage object, not a full response.
// It covers cache counters needed by this module, including OpenAI GPT-5.6
// cache_write_tokens that older shared response normalizers may not expose.
func ObserveRawCacheUsage(result NativeResult, provider string, raw []byte) Observation {
	usage, ok := NormalizeRawCacheUsage(provider, raw)
	if !ok {
		return Observation{Status: ObservationUnavailable, Basis: "unavailable"}
	}
	return Observe(result, usage)
}

// NormalizeRawCacheUsage maps official cache counters into UsageObservation.
// Unknown providers, duplicate keys, negative/fractional
// counters, ambiguous OpenAI shapes, and contradictory Anthropic totals fail
// closed.
func NormalizeRawCacheUsage(provider string, raw []byte) (UsageObservation, bool) {
	if !validUniqueJSONObject(raw) {
		return UsageObservation{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var root map[string]any
	if decoder.Decode(&root) != nil {
		return UsageObservation{}, false
	}
	var read, write int
	var readObserved, writeObserved bool
	var valid bool
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai":
		inputDetails, hasInput, inputValid := optionalObjectField(root, "input_tokens_details")
		promptDetails, hasPrompt, promptValid := optionalObjectField(root, "prompt_tokens_details")
		if !inputValid || !promptValid {
			return UsageObservation{}, false
		}
		if hasInput == hasPrompt {
			return UsageObservation{}, false
		}
		details := inputDetails
		if hasPrompt {
			details = promptDetails
		}
		read, readObserved, valid = optionalCounter(details, "cached_tokens")
		if !valid {
			return UsageObservation{}, false
		}
		write, writeObserved, valid = optionalCounter(details, "cache_write_tokens")
		if !valid {
			return UsageObservation{}, false
		}
	case "anthropic":
		read, readObserved, valid = optionalCounter(root, "cache_read_input_tokens")
		if !valid {
			return UsageObservation{}, false
		}
		write, writeObserved, valid = optionalCounter(root, "cache_creation_input_tokens")
		if !valid {
			return UsageObservation{}, false
		}
		if details, exists, objectValid := optionalObjectField(root, "cache_creation"); !objectValid {
			return UsageObservation{}, false
		} else if exists {
			five, fiveObserved, fiveValid := optionalCounter(details, "ephemeral_5m_input_tokens")
			oneHour, hourObserved, hourValid := optionalCounter(details, "ephemeral_1h_input_tokens")
			if !fiveValid || !hourValid {
				return UsageObservation{}, false
			}
			if fiveObserved || hourObserved {
				detailTotal, sumOK := safeCounterSum(five, oneHour)
				if !sumOK {
					return UsageObservation{}, false
				}
				if writeObserved && detailTotal != write {
					return UsageObservation{}, false
				}
				write, writeObserved = detailTotal, true
			}
		}
	case "bedrock":
		read, readObserved, valid = optionalCounter(root, "cacheReadInputTokens")
		if !valid {
			return UsageObservation{}, false
		}
		write, writeObserved, valid = optionalCounter(root, "cacheWriteInputTokens")
		if !valid {
			return UsageObservation{}, false
		}
	case "gemini":
		read, readObserved, valid = optionalCounter(root, "cachedContentTokenCount")
		if !valid {
			return UsageObservation{}, false
		}
		if !readObserved {
			read, readObserved, valid = optionalCounter(root, "total_cached_tokens")
			if !valid {
				return UsageObservation{}, false
			}
		}
	default:
		return UsageObservation{}, false
	}
	observed := readObserved || writeObserved
	status := "unknown"
	if observed {
		switch {
		case read > 0:
			status = "hit"
		case write > 0:
			status = "write"
		default:
			status = "miss"
		}
	}
	return UsageObservation{
		CachedInputTokens: read, CacheCreationInputTokens: write,
		CacheObserved: observed, CacheStatus: status,
	}, true
}

func optionalObjectField(root map[string]any, name string) (map[string]any, bool, bool) {
	value, exists := root[name]
	if !exists {
		return nil, false, true
	}
	object, ok := value.(map[string]any)
	return object, true, ok
}

func optionalCounter(root map[string]any, name string) (value int, observed, valid bool) {
	raw, exists := root[name]
	if !exists {
		return 0, false, true
	}
	number, ok := raw.(json.Number)
	if !ok {
		return 0, true, false
	}
	parsed, err := strconv.ParseInt(string(number), 10, 64)
	if err != nil || parsed < 0 || int64(int(parsed)) != parsed {
		return 0, true, false
	}
	return int(parsed), true, true
}
