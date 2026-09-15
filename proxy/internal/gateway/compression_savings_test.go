package gateway

import (
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/shared/platform/cost"
)

// TestCompressionSavingsUSD_ZeroWithoutOptimizer proves the no-fake-savings gate:
// a token reduction is worth nothing unless the caveman-compression id is present.
func TestCompressionSavingsUSD_ZeroWithoutOptimizer(t *testing.T) {
	price := cost.Price{InputPerMillion: 3.0}
	if got := compressionSavingsUSD(price, 1000, 600, nil, providers.UsageObservation{InputTokens: 1000, InputTokensReported: true, OutputTokensReported: true}); got != 0 {
		t.Errorf("savings without optimizer id = %v, want 0", got)
	}
	if got := compressionSavingsUSD(price, 1000, 600, []string{"output-brevity"}, providers.UsageObservation{InputTokens: 1000, InputTokensReported: true, OutputTokensReported: true}); got != 0 {
		t.Errorf("savings with an unrelated optimizer = %v, want 0", got)
	}
}

// TestCompressionSavingsUSD_NonzeroWithOptimizer proves the removed tokens are
// valued at the model's input rate once the optimizer id is present (uncached traffic).
func TestCompressionSavingsUSD_NonzeroWithOptimizer(t *testing.T) {
	price := cost.Price{InputPerMillion: 3.0}
	got := compressionSavingsUSD(price, 1000, 600, []string{compressionOptimizerID}, providers.UsageObservation{InputTokens: 1000, InputTokensReported: true, OutputTokensReported: true})
	want := cost.RoundUSD(cost.EstimateUSD(cost.Price{InputPerMillion: 3.0}, cost.Usage{InputTokens: 400}))
	if got != want {
		t.Errorf("savings = %v, want %v", got, want)
	}
	if got <= 0 {
		t.Errorf("savings = %v, want > 0", got)
	}
}

// TestCompressionSavingsUSD_PreservesRegressions keeps overhead visible as a
// negative inferred delta rather than silently clipping it.
func TestCompressionSavingsUSD_PreservesRegressions(t *testing.T) {
	price := cost.Price{InputPerMillion: 3.0}
	if got := compressionSavingsUSD(price, 600, 600, []string{compressionOptimizerID}, providers.UsageObservation{InputTokens: 1000, InputTokensReported: true, OutputTokensReported: true}); got != 0 {
		t.Errorf("savings when equal = %v, want 0", got)
	}
	if got := compressionSavingsUSD(price, 600, 800, []string{compressionOptimizerID}, providers.UsageObservation{InputTokens: 1000, InputTokensReported: true, OutputTokensReported: true}); got != -0.0006 {
		t.Errorf("savings when larger = %v, want -0.0006", got)
	}
}

// TestCompressionSavingsUSD_CacheHeavyUsesWeightedRate avoids a discontinuous
// majority-cache heuristic while accounting for the small uncached share.
func TestCompressionSavingsUSD_CacheHeavyUsesWeightedRate(t *testing.T) {
	price := cost.Price{InputPerMillion: 3.0, CacheReadPerMillion: 0.3}
	// A request served almost entirely from cache: 144k cache reads, ~100 fresh input.
	usage := providers.UsageObservation{InputTokens: 144100, CachedInputTokens: 144000, InputTokensReported: true, OutputTokensReported: true}
	got := compressionSavingsUSD(price, 1000, 600, []string{compressionOptimizerID}, usage)
	want := cost.RoundUSD(400 * (100*3.0 + 144000*0.3) / 144100 / 1e6)
	if got != want {
		t.Errorf("cache-heavy savings = %v, want weighted rate %v", got, want)
	}
	full := cost.RoundUSD(cost.EstimateUSD(cost.Price{InputPerMillion: 3.0}, cost.Usage{InputTokens: 400}))
	if got >= full {
		t.Errorf("cache-heavy savings %v must be below the full-input-rate figure %v", got, full)
	}
}
