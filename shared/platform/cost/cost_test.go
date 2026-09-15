package cost_test

import (
	"math"
	"testing"

	"github.com/JuliusBrussee/caveman/shared/platform/cost"
)

func TestEstimateUSDFailsClosedOnInvalidInputs(t *testing.T) {
	tests := []struct {
		name  string
		price cost.Price
		usage cost.Usage
	}{
		{"negative tokens", cost.Price{InputPerMillion: 10}, cost.Usage{InputTokens: -1}},
		{"negative rate", cost.Price{InputPerMillion: -10}, cost.Usage{InputTokens: 1_000_000}},
		{"nan rate", cost.Price{InputPerMillion: math.NaN()}, cost.Usage{InputTokens: 1_000_000}},
		{"infinite rate", cost.Price{InputPerMillion: math.Inf(1)}, cost.Usage{InputTokens: 1_000_000}},
		{"overflow", cost.Price{InputPerMillion: math.MaxFloat64}, cost.Usage{InputTokens: int(^uint(0) >> 1)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cost.EstimateUSD(tc.price, tc.usage); got != 0 {
				t.Fatalf("EstimateUSD = %v, want honest zero", got)
			}
		})
	}
}

func TestEstimateUSDPreservesSubMicroCosts(t *testing.T) {
	got := cost.EstimateUSD(cost.Price{InputPerMillion: 0.20}, cost.Usage{InputTokens: 1})
	if got != 0.0000002 {
		t.Fatalf("one nano-priced token = %.10f, want 0.0000002000", got)
	}
	if total := cost.RoundUSD(got * 1_000_000); total != 0.2 {
		t.Fatalf("one million one-token requests = %.10f, want 0.2", total)
	}
}

func TestEstimateUSDPricesCacheWriteTTLsSeparately(t *testing.T) {
	price := cost.Price{CacheWritePerMillion: 3.75, CacheWrite1hPerMillion: 6}
	usage := cost.Usage{CacheCreationTokens: 1_000_000, CacheCreation1hTokens: 1_000_000}
	if got := cost.EstimateUSD(price, usage); got != 9.75 {
		t.Fatalf("EstimateUSD = %v, want 9.75", got)
	}
}

func TestForInputTokensAppliesProviderReportedLongContextTier(t *testing.T) {
	price := cost.Price{
		InputPerMillion: 5, OutputPerMillion: 30, CacheReadPerMillion: .5,
		LongContextThresholdTokens: 272_000, LongContextInputMultiplier: 2, LongContextOutputMultiplier: 1.5,
	}
	if got := cost.ForInputTokens(price, 272_000); got.InputPerMillion != 5 || got.OutputPerMillion != 30 {
		t.Fatalf("at threshold changed price: %+v", got)
	}
	got := cost.ForInputTokens(price, 272_001)
	if got.InputPerMillion != 10 || got.CacheReadPerMillion != 1 || got.OutputPerMillion != 45 {
		t.Fatalf("long-context price = %+v, want 10/1/45", got)
	}
}

func TestForInputTokensHonorsInclusiveThreshold(t *testing.T) {
	price := cost.Price{
		InputPerMillion: 3, OutputPerMillion: 15,
		LongContextThresholdTokens: 200_000, LongContextThresholdInclusive: true,
		LongContextInputMultiplier: 2, LongContextOutputMultiplier: 1.5,
	}
	got := cost.ForInputTokens(price, 200_000)
	if got.InputPerMillion != 6 || got.OutputPerMillion != 22.5 {
		t.Fatalf("inclusive boundary price = %+v", got)
	}
}
