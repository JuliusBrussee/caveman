package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/url"
	"strings"
	"sync"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/shared/platform/catalog"
	"github.com/JuliusBrussee/caveman/shared/platform/cost"
	"github.com/tiktoken-go/tokenizer"
)

const requestTokenBasis = "estimated_request_json_o200k_v1"

// Bound post-response accounting work independently of the 32 MiB proxy upload
// limit. Larger requests still flow normally and retain provider usage/pricing.
const requestAccountingMaxBytes = 4 << 20

var requestTokenCodec = sync.OnceValues(func() (tokenizer.Codec, error) {
	return tokenizer.Get(tokenizer.O200kBase)
})

// requestAccounting records request-local counterfactual evidence only after a
// successful response. JSON keys and string escapes are normalized on both sides; string data,
// recovery markers, injected tools and cached replacements are counted alike.
// No prompt content is retained in the record. Multimodal payloads are excluded:
// base64/image URLs are not a useful estimate of model-visible image/audio tokens.
func requestAccounting(row *RequestRecord, meta providers.RequestMetadata, usage providers.UsageObservation, original, accepted []byte, retrieved bool) {
	price := recordStatsPrice(row, meta, usage)
	row.RequestMeasurementStatus = "failed"
	if row.StatusCode < 200 || row.StatusCode >= 300 || row.ErrorCode != "" || usage.ProviderError {
		return
	}
	if retrieved || len(usage.CallObservations) > 0 {
		row.RequestMeasurementStatus = "recovery_multiple_calls"
		return
	}
	if len(original) > requestAccountingMaxBytes || len(accepted) > requestAccountingMaxBytes {
		row.RequestMeasurementStatus = "request_payload_too_large"
		return
	}
	row.RequestMeasurementStatus = "unsupported_payload"
	beforeJSON, ok := compactTextRequest(original)
	if !ok {
		return
	}
	afterJSON, ok := compactTextRequest(accepted)
	if !ok {
		return
	}
	codec, err := requestTokenCodec()
	if err != nil {
		row.RequestMeasurementStatus = "tokenizer_unavailable"
		return
	}
	before, err := codec.Count(string(beforeJSON))
	if err != nil {
		row.RequestMeasurementStatus = "tokenizer_unavailable"
		return
	}
	after := before
	if !bytes.Equal(beforeJSON, afterJSON) {
		after, err = codec.Count(string(afterJSON))
		if err != nil {
			row.RequestMeasurementStatus = "tokenizer_unavailable"
			return
		}
	}
	row.RequestTokensBefore = before
	row.RequestTokensAfter = after
	row.RequestTokenBasis = requestTokenBasis
	row.RequestMeasurementStatus = "measured"
	row.RequestSavingsBasis = "unpriced"
	if !validStatsUsage(usage) {
		row.RequestSavingsBasis = "usage_incomplete"
		return
	}
	if !row.PricingKnown {
		return
	}
	effective := cost.ForInputTokens(price, usage.InputTokens)
	delta := before - after
	// A local BPE delta cannot prove the provider's counterfactual threshold.
	// Do not price a potential tier crossing or a physically impossible baseline.
	baselineInput := int64(usage.InputTokens) + int64(delta)
	if baselineInput < 0 || baselineInput > int64(math.MaxInt) ||
		cost.ForInputTokens(price, int(baselineInput)) != effective {
		row.RequestSavingsBasis = "counterfactual_tier_unknown"
		return
	}
	value, ok := weightedInputDeltaUSD(price, delta, usage)
	if !ok {
		row.RequestSavingsBasis = "usage_incomplete"
		return
	}
	row.RequestEstimatedInputDeltaUSD = &value
	row.RequestSavingsBasis = "estimated_tokens_x_observed_cache_mix"
}

// recordStatsPrice snapshots known provider-counted spend even when request
// payloads cannot be token-estimated (for example image input).
func recordStatsPrice(row *RequestRecord, meta providers.RequestMetadata, usage providers.UsageObservation) cost.Price {
	if !validStatsSpendUsage(usage) {
		return cost.Price{}
	}
	price, pricingMeta := statsPriceForUsage(meta, usage, row.AuthMode)
	if price == (cost.Price{}) || !cost.ValidPrice(price) {
		return cost.Price{}
	}
	version := statsCatalogVersion(pricingMeta)
	if strings.HasPrefix(version, "unpriced:") || version == "" {
		return cost.Price{}
	}
	effective := cost.ForInputTokens(price, usage.InputTokens)
	if !statsInputRatesKnown(effective, usage) ||
		(usage.OutputTokens > usage.ReasoningTokens && effective.OutputPerMillion <= 0) ||
		(usage.ReasoningTokens > 0 && effective.ReasoningPerMillion <= 0 && effective.OutputPerMillion <= 0) {
		return cost.Price{}
	}
	row.PricingKnown = true
	row.PricingProvider = pricingMeta.Provider
	row.PricingModel = pricingMeta.Model
	row.PricingCatalogVersion = version
	row.PriceInputPerMillion = effective.InputPerMillion
	row.PriceOutputPerMillion = effective.OutputPerMillion
	row.PriceCacheReadPerMillion = effective.CacheReadPerMillion
	row.PriceCacheWritePerMillion = effective.CacheWritePerMillion
	row.PriceCacheWrite1hPerMillion = effective.CacheWrite1hPerMillion
	row.PriceReasoningPerMillion = effective.ReasoningPerMillion
	if row.PriceReasoningPerMillion == 0 {
		row.PriceReasoningPerMillion = effective.OutputPerMillion
	}
	return price
}

func compactTextRequest(body []byte) ([]byte, bool) {
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil || value == nil || containsNonTextInput(value) {
		return nil, false
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, false
	}
	// A reassembler can replace \u0061 with a, reorder object keys, or change
	// HTML escaping without changing provider-visible content. Canonicalize both
	// sides so those serialization changes cannot mint token savings. UseNumber
	// preserves large integer fields rather than rounding them through float64.
	var normalized bytes.Buffer
	encoder := json.NewEncoder(&normalized)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(value) != nil {
		return nil, false
	}
	return bytes.TrimSuffix(normalized.Bytes(), []byte{'\n'}), true
}

func containsNonTextInput(value any) bool {
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			if containsNonTextInput(item) {
				return true
			}
		}
	case map[string]any:
		if kind, ok := v["type"].(string); ok {
			switch strings.ToLower(kind) {
			case "image", "image_url", "input_image", "output_image", "input_audio", "audio", "file", "input_file", "video", "document":
				return true
			}
		}
		for key, item := range v {
			switch key {
			case "inlineData", "inline_data", "fileData", "file_data", "input_audio", "image_url":
				if media, structured := item.(map[string]any); structured {
					for _, field := range []string{"data", "mimeType", "mime_type", "fileUri", "file_uri", "url"} {
						if _, present := media[field].(string); present {
							return true
						}
					}
				} else if _, urlString := item.(string); key == "image_url" && urlString {
					return true
				}
			case "image", "document", "video", "audio":
				// Bedrock Converse uses union keys instead of type discriminators.
				// Require the media source shape: properties.image in a tool schema
				// is ordinary text metadata, not an image input.
				if media, structured := item.(map[string]any); structured {
					if source, ok := media["source"].(map[string]any); ok {
						if _, bytesPresent := source["bytes"]; bytesPresent {
							return true
						}
						if _, s3Present := source["s3Location"]; s3Present {
							return true
						}
						if _, formatPresent := media["format"].(string); formatPresent {
							return true
						}
					}
				}
			}
			if containsNonTextInput(item) {
				return true
			}
		}
	}
	return false
}

// statsPricingOriginKnown binds a published-price snapshot to the resolved
// provider origin. An operator's custom base URL may use the same wire protocol
// and model name while charging entirely different rates.
func statsPricingOriginKnown(provider string, upstream *url.URL) bool {
	if upstream == nil || upstream.Scheme != "https" || upstream.User != nil || (upstream.Port() != "" && upstream.Port() != "443") {
		return false
	}
	host := strings.ToLower(upstream.Hostname())
	switch provider {
	case "openai":
		switch host {
		case "api.openai.com", "us.api.openai.com", "eu.api.openai.com", "au.api.openai.com", "ca.api.openai.com", "jp.api.openai.com", "in.api.openai.com", "sg.api.openai.com", "kr.api.openai.com", "gb.api.openai.com", "ae.api.openai.com":
			return true
		}
	case "anthropic":
		return host == "api.anthropic.com"
	case "gemini":
		return host == "generativelanguage.googleapis.com"
	case "bedrock":
		parts := strings.Split(host, ".")
		return len(parts) == 4 && parts[1] != "" &&
			(((parts[0] == "bedrock-runtime" || parts[0] == "bedrock-runtime-fips") && parts[2] == "amazonaws" && parts[3] == "com") ||
				(parts[0] == "bedrock-mantle" && parts[2] == "api" && parts[3] == "aws"))
	case "vertex":
		if host == "aiplatform.googleapis.com" || host == "aiplatform.us.rep.googleapis.com" || host == "aiplatform.eu.rep.googleapis.com" {
			return true
		}
		region, ok := strings.CutSuffix(host, "-aiplatform.googleapis.com")
		return ok && region != "" && !strings.Contains(region, ".")
	}
	return false
}

func validStatsUsage(usage providers.UsageObservation) bool {
	return !usage.ProviderError && validStatsSpendUsage(usage)
}

// A provider error prevents counterfactual savings, but complete observed
// billing buckets can still carry a known published-price spend estimate.
func validStatsSpendUsage(usage providers.UsageObservation) bool {
	return usage.Complete() && len(usage.CallObservations) == 0 &&
		usage.InputTokens >= 0 && usage.OutputTokens >= 0 && usage.CachedInputTokens >= 0 &&
		usage.CacheCreationInputTokens >= 0 && usage.CacheCreation1hTokens >= 0 &&
		usage.CachedInputTokens <= usage.InputTokens &&
		usage.CacheCreationInputTokens <= usage.InputTokens-usage.CachedInputTokens &&
		usage.CacheCreation1hTokens <= usage.CacheCreationInputTokens &&
		usage.ReasoningTokens >= 0 && usage.ReasoningTokens <= usage.OutputTokens
}

// weightedInputDeltaUSD prices a signed local delta using observed disjoint
// input/cache-read/5m-write/1h-write proportions. Cache mix of the removed tokens
// is unobserved, so this is explicitly an estimate, never invoice savings. The
// observed tier is applied; crossing into another counterfactual tier is refused.
func weightedInputDeltaUSD(price cost.Price, delta int, usage providers.UsageObservation) (float64, bool) {
	if !validStatsUsage(usage) || !cost.ValidPrice(price) || price == (cost.Price{}) {
		return 0, false
	}
	baselineInput := int64(usage.InputTokens) + int64(delta)
	effective := cost.ForInputTokens(price, usage.InputTokens)
	if !statsInputRatesKnown(effective, usage) {
		return 0, false
	}
	if delta == 0 {
		return 0, true
	}
	if usage.InputTokens == 0 {
		return 0, false
	}
	if baselineInput < 0 || baselineInput > int64(math.MaxInt) || cost.ForInputTokens(price, int(baselineInput)) != effective {
		return 0, false
	}
	fresh := usage.InputTokens - usage.CachedInputTokens - usage.CacheCreationInputTokens
	write5m := usage.CacheCreationInputTokens - usage.CacheCreation1hTokens
	inputCostPerMillion := float64(fresh)*effective.InputPerMillion +
		float64(usage.CachedInputTokens)*effective.CacheReadPerMillion +
		float64(write5m)*effective.CacheWritePerMillion +
		float64(usage.CacheCreation1hTokens)*effective.CacheWrite1hPerMillion
	value := float64(delta) * (inputCostPerMillion / float64(usage.InputTokens)) / 1_000_000
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return cost.RoundUSD(value), true
}

func statsInputRatesKnown(price cost.Price, usage providers.UsageObservation) bool {
	// Null catalog rates decode as zero for non-applicable classes. A provider
	// reporting that class does not make it free; the request becomes unpriced.
	return (usage.InputTokens-usage.CachedInputTokens-usage.CacheCreationInputTokens == 0 || price.InputPerMillion > 0) &&
		(usage.CachedInputTokens == 0 || price.CacheReadPerMillion > 0) &&
		(usage.CacheCreationInputTokens-usage.CacheCreation1hTokens == 0 || price.CacheWritePerMillion > 0) &&
		(usage.CacheCreation1hTokens == 0 || price.CacheWrite1hPerMillion > 0)
}

func statsPriceForUsage(meta providers.RequestMetadata, usage providers.UsageObservation, authMode string) (cost.Price, providers.RequestMetadata) {
	if authMode != string(AuthModePAYG) && authMode != string(AuthModeOAuth) && authMode != string(AuthModeSubscription) {
		return cost.Price{}, meta
	}
	if meta.Provider == "chatgpt-subscription" {
		// Explicit same-model API equivalent. Never substitute a sibling model.
		meta.Provider = "openai"
	}
	if authMode != string(AuthModePAYG) && meta.Provider == "gemini" {
		// The equivalent is explicitly the paid API, not the subscription bill.
		meta.BillingTier = "paid"
	}
	return standalonePriceForUsage(meta, usage), meta
}

func statsCatalogVersion(meta providers.RequestMetadata) string {
	var version string
	switch meta.Provider {
	case "bedrock":
		_, version = catalog.PriceForRegion(meta.Provider, meta.Model, meta.Region)
	case "vertex":
		_, version = catalog.PriceForRegionOrAgnostic(meta.Provider, meta.Model, meta.Region)
	default:
		_, version = catalog.Price(meta.Provider, meta.Model)
	}
	return version
}
