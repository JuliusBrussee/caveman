package cost_test

import (
	"math"
	"testing"

	"github.com/JuliusBrussee/caveman/shared/platform/cost"
)

func TestEstimateUSDPricesEveryDisjointBucket(t *testing.T) {
	price := cost.Price{
		InputPerMillion:        1,
		OutputPerMillion:       2,
		CacheReadPerMillion:    3,
		CacheWritePerMillion:   4,
		CacheWrite1hPerMillion: 5,
		ReasoningPerMillion:    6,
	}
	usage := cost.Usage{
		InputTokens:           1_000_000,
		OutputTokens:          1_000_000,
		CachedInputTokens:     1_000_000,
		CacheCreationTokens:   1_000_000,
		CacheCreation1hTokens: 1_000_000,
		ReasoningTokens:       1_000_000,
	}
	if got := cost.EstimateUSD(price, usage); got != 21 {
		t.Fatalf("EstimateUSD() = %v, want 21", got)
	}
	if got := cost.EstimateUSD(cost.Price{}, usage); got != 0 {
		t.Fatalf("unpriced usage = %v, want honest zero", got)
	}
	maxInt := int(^uint(0) >> 1)
	overflowPrice := cost.Price{
		InputPerMillion: 1e295, OutputPerMillion: 1e295, CacheReadPerMillion: 1e295,
		CacheWritePerMillion: 1e295, CacheWrite1hPerMillion: 1e295, ReasoningPerMillion: 1e295,
	}
	overflowUsage := cost.Usage{
		InputTokens: maxInt, OutputTokens: maxInt, CachedInputTokens: maxInt,
		CacheCreationTokens: maxInt, CacheCreation1hTokens: maxInt, ReasoningTokens: maxInt,
	}
	if got := cost.EstimateUSD(overflowPrice, overflowUsage); got != 0 {
		t.Fatalf("component-sum overflow = %v, want honest zero", got)
	}
}

func TestRoundUSDBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		value float64
		want  float64
	}{
		{name: "positive half up", value: 0.00000000005, want: 0.0000000001},
		{name: "negative", value: -1.23456789014, want: -1.2345678901},
		{name: "nan", value: math.NaN()},
		{name: "positive infinity", value: math.Inf(1)},
		{name: "negative infinity", value: math.Inf(-1)},
		{name: "scale overflow", value: math.MaxFloat64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cost.RoundUSD(tt.value); got != tt.want {
				t.Fatalf("RoundUSD(%v) = %.12f, want %.12f", tt.value, got, tt.want)
			}
		})
	}
}

func TestForInputTokensCoversAllRatesAndSafeFallbacks(t *testing.T) {
	price := cost.Price{
		InputPerMillion: 1, OutputPerMillion: 2,
		CacheReadPerMillion: 3, CacheWritePerMillion: 4, CacheWrite1hPerMillion: 5,
		ReasoningPerMillion:        6,
		LongContextThresholdTokens: 10, LongContextInputMultiplier: 2, LongContextOutputMultiplier: 3,
	}
	got := cost.ForInputTokens(price, 11)
	if got.InputPerMillion != 2 || got.CacheReadPerMillion != 6 ||
		got.CacheWritePerMillion != 8 || got.CacheWrite1hPerMillion != 10 ||
		got.OutputPerMillion != 6 || got.ReasoningPerMillion != 18 {
		t.Fatalf("long-context rates = %+v", got)
	}
	if got := cost.ForInputTokens(price, -1); got != price {
		t.Fatalf("negative provider count changed price: %+v", got)
	}
	noTier := price
	noTier.LongContextThresholdTokens = 0
	if got := cost.ForInputTokens(noTier, 100); got != noTier {
		t.Fatalf("disabled tier changed price: %+v", got)
	}

	invalid := price
	invalid.InputPerMillion = math.Inf(1)
	invalid.OutputPerMillion = -1
	invalid.LongContextInputMultiplier = math.NaN()
	invalid.LongContextOutputMultiplier = 0
	got = cost.ForInputTokens(invalid, 11)
	if got.InputPerMillion != 0 || got.OutputPerMillion != 0 ||
		got.CacheReadPerMillion != invalid.CacheReadPerMillion ||
		got.ReasoningPerMillion != invalid.ReasoningPerMillion {
		t.Fatalf("invalid rate/multiplier fallback = %+v", got)
	}
	overflow := price
	overflow.InputPerMillion = 1e308
	overflow.LongContextInputMultiplier = 2
	if got := cost.ForInputTokens(overflow, 11); got.InputPerMillion != 0 {
		t.Fatalf("rate product overflow = %v, want honest zero", got.InputPerMillion)
	}
}

func TestValidPriceAcceptsCatalogShapeAndRejectsUnsafeFields(t *testing.T) {
	valid := cost.Price{
		InputPerMillion: 1, OutputPerMillion: 2,
		CacheReadPerMillion: 0.1, CacheWritePerMillion: 1.5, CacheWrite1hPerMillion: 2,
		ReasoningPerMillion: 3, BatchDiscountFraction: 0.5,
		CacheStoragePerMillionHour: 0.01,
		LongContextThresholdTokens: 100, LongContextInputMultiplier: 2, LongContextOutputMultiplier: 1.5,
	}
	if !cost.ValidPrice(valid) {
		t.Fatal("valid catalog price rejected")
	}
	zero := cost.Price{}
	if !cost.ValidPrice(zero) {
		t.Fatal("zero non-applicable rates rejected")
	}
	mutations := []struct {
		name string
		fn   func(*cost.Price)
	}{
		{name: "negative input", fn: func(p *cost.Price) { p.InputPerMillion = -1 }},
		{name: "nan output", fn: func(p *cost.Price) { p.OutputPerMillion = math.NaN() }},
		{name: "infinite cache read", fn: func(p *cost.Price) { p.CacheReadPerMillion = math.Inf(1) }},
		{name: "negative cache write", fn: func(p *cost.Price) { p.CacheWritePerMillion = -1 }},
		{name: "negative 1h cache write", fn: func(p *cost.Price) { p.CacheWrite1hPerMillion = -1 }},
		{name: "negative reasoning", fn: func(p *cost.Price) { p.ReasoningPerMillion = -1 }},
		{name: "negative batch discount", fn: func(p *cost.Price) { p.BatchDiscountFraction = -1 }},
		{name: "batch discount over one", fn: func(p *cost.Price) { p.BatchDiscountFraction = 1.01 }},
		{name: "negative storage", fn: func(p *cost.Price) { p.CacheStoragePerMillionHour = -1 }},
		{name: "negative threshold", fn: func(p *cost.Price) { p.LongContextThresholdTokens = -1 }},
		{name: "zero input multiplier", fn: func(p *cost.Price) { p.LongContextInputMultiplier = 0 }},
		{name: "nan output multiplier", fn: func(p *cost.Price) { p.LongContextOutputMultiplier = math.NaN() }},
	}
	for _, tt := range mutations {
		t.Run(tt.name, func(t *testing.T) {
			candidate := valid
			tt.fn(&candidate)
			if cost.ValidPrice(candidate) {
				t.Fatalf("unsafe price accepted: %+v", candidate)
			}
		})
	}
}
