package cost

import "math"

type Price struct {
	InputPerMillion               float64 `json:"input_per_million" yaml:"input_per_million"`
	OutputPerMillion              float64 `json:"output_per_million" yaml:"output_per_million"`
	CacheReadPerMillion           float64 `json:"cache_read_input_per_million" yaml:"cache_read_input_per_million"`
	CacheWritePerMillion          float64 `json:"cache_write_input_per_million" yaml:"cache_write_input_per_million"`
	CacheWrite1hPerMillion        float64 `json:"cache_write_1h_input_per_million" yaml:"cache_write_1h_input_per_million"`
	ReasoningPerMillion           float64 `json:"reasoning_output_per_million" yaml:"reasoning_output_per_million"`
	BatchDiscountFraction         float64 `json:"batch_discount_fraction" yaml:"batch_discount_fraction"`
	CacheStoragePerMillionHour    float64 `json:"cache_storage_per_million_tokens_hour" yaml:"cache_storage_per_million_tokens_hour"`
	LongContextThresholdTokens    int     `json:"long_context_threshold_tokens" yaml:"long_context_threshold_tokens"`
	LongContextThresholdInclusive bool    `json:"long_context_threshold_inclusive" yaml:"long_context_threshold_inclusive"`
	LongContextInputMultiplier    float64 `json:"long_context_input_multiplier" yaml:"long_context_input_multiplier"`
	LongContextOutputMultiplier   float64 `json:"long_context_output_multiplier" yaml:"long_context_output_multiplier"`
}

type Usage struct {
	InputTokens           int `json:"input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	CacheCreationTokens   int `json:"cache_creation_input_tokens"`
	CacheCreation1hTokens int `json:"cache_creation_1h_input_tokens"`
	ReasoningTokens       int `json:"reasoning_tokens"`
}

// EstimateUSD prices already-classified billable token buckets. Token counts
// must be disjoint: InputTokens is uncached input; CachedInputTokens is cache
// read; CacheCreationTokens is default/5m cache write;
// CacheCreation1hTokens is 1h cache write; OutputTokens and ReasoningTokens are
// additive only when the caller's provider exposes them as disjoint buckets.
//
// Invalid inputs fail closed per component. Negative token counts, negative or
// non-finite rates, arithmetic overflow, NaN, and infinity never become spend or
// savings; they contribute zero rather than a plausible-but-wrong number.
func EstimateUSD(price Price, usage Usage) float64 {
	total := tokenCost(usage.InputTokens, price.InputPerMillion) +
		tokenCost(usage.OutputTokens, price.OutputPerMillion) +
		tokenCost(usage.CachedInputTokens, price.CacheReadPerMillion) +
		tokenCost(usage.CacheCreationTokens, price.CacheWritePerMillion) +
		tokenCost(usage.CacheCreation1hTokens, price.CacheWrite1hPerMillion) +
		tokenCost(usage.ReasoningTokens, price.ReasoningPerMillion)
	if !finiteNonNegative(total) {
		return 0
	}
	rounded := RoundUSD(total)
	if !finiteNonNegative(rounded) {
		return 0
	}
	return rounded
}

// RoundUSD matches the Decimal128(10) storage scale used by ClickHouse. Six
// decimal places zeroed legitimate sub-micro costs (for example one GPT-5.4
// nano input token), materially understating spend at high request volume.
func RoundUSD(value float64) float64 {
	const scale = 10_000_000_000.0
	if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > math.MaxFloat64/scale {
		return 0
	}
	return math.Round(value*scale) / scale
}

// ForInputTokens applies a catalog-declared long-context surcharge to a copy of
// price. The provider-reported total input count determines the threshold; no
// local tokenizer estimate participates.
func ForInputTokens(price Price, totalInputTokens int) Price {
	if totalInputTokens < 0 || price.LongContextThresholdTokens <= 0 {
		return price
	}
	longContext := totalInputTokens > price.LongContextThresholdTokens ||
		(price.LongContextThresholdInclusive && totalInputTokens == price.LongContextThresholdTokens)
	if !longContext {
		return price
	}
	inputMultiplier := positiveFiniteOrOne(price.LongContextInputMultiplier)
	outputMultiplier := positiveFiniteOrOne(price.LongContextOutputMultiplier)
	price.InputPerMillion = safeRateProduct(price.InputPerMillion, inputMultiplier)
	price.CacheReadPerMillion = safeRateProduct(price.CacheReadPerMillion, inputMultiplier)
	price.CacheWritePerMillion = safeRateProduct(price.CacheWritePerMillion, inputMultiplier)
	price.CacheWrite1hPerMillion = safeRateProduct(price.CacheWrite1hPerMillion, inputMultiplier)
	price.OutputPerMillion = safeRateProduct(price.OutputPerMillion, outputMultiplier)
	price.ReasoningPerMillion = safeRateProduct(price.ReasoningPerMillion, outputMultiplier)
	return price
}

// ValidPrice rejects catalog rows whose numeric fields could create negative or
// non-finite billing. Zero rates remain valid for non-applicable token classes.
func ValidPrice(price Price) bool {
	for _, rate := range []float64{
		price.InputPerMillion,
		price.OutputPerMillion,
		price.CacheReadPerMillion,
		price.CacheWritePerMillion,
		price.CacheWrite1hPerMillion,
		price.ReasoningPerMillion,
		price.BatchDiscountFraction,
		price.CacheStoragePerMillionHour,
	} {
		if !finiteNonNegative(rate) {
			return false
		}
	}
	if price.LongContextThresholdTokens < 0 {
		return false
	}
	if price.BatchDiscountFraction > 1 {
		return false
	}
	if price.LongContextThresholdTokens > 0 {
		if !positiveFinite(price.LongContextInputMultiplier) || !positiveFinite(price.LongContextOutputMultiplier) {
			return false
		}
	}
	return true
}

func tokenCost(tokens int, rate float64) float64 {
	if tokens <= 0 || !finiteNonNegative(rate) || rate == 0 {
		return 0
	}
	value := float64(tokens) / 1_000_000 * rate
	if !finiteNonNegative(value) {
		return 0
	}
	return value
}

func safeRateProduct(rate, multiplier float64) float64 {
	if !finiteNonNegative(rate) || !positiveFinite(multiplier) || rate == 0 {
		return 0
	}
	value := rate * multiplier
	if !finiteNonNegative(value) {
		return 0
	}
	return value
}

func positiveFiniteOrOne(v float64) float64 {
	if !positiveFinite(v) {
		return 1
	}
	return v
}

func positiveFinite(v float64) bool {
	return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0)
}

func finiteNonNegative(v float64) bool {
	return v >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0)
}
