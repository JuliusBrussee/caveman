package store

import "testing"

func TestCostChartBlendsEachModelsCatalogCachePrice(t *testing.T) {
	// Fable 5.1: $10 in, $0.25 cache read -> 0.1*10 + 0.9*0.25 = 1.225, not
	// the 1.90 a flat tenth-of-list cache price gave.
	if got := cachedRate(10, 0.25); got < 1.2249 || got > 1.2251 {
		t.Fatalf("cachedRate(10, 0.25) = %v, want 1.225", got)
	}
	families := costFamilies([]Sink{{TokensPerDayRate: 1_000_000}})
	rates := map[string]string{}
	for _, f := range families {
		for _, row := range f.Rows {
			rates[row.Label] = row.Rate
		}
	}
	for label, want := range map[string]string{
		"Fable 5.1":    "$1.23 per 1M input",  // 1.225
		"Opus 5.5":     "$0.58 per 1M input",  // 0.4 + 0.9*0.20
		"Sonnet 5":     "$0.38 per 1M input",  // 0.2 + 0.9*0.20
		"GPT-5.6 Luna": "$0.038 per 1M input", // 0.1*0.20 + 0.9*0.02
	} {
		if rates[label] != want {
			t.Errorf("%s rate = %q, want %q (all rates: %v)", label, rates[label], want, rates)
		}
	}
	if len(rates) != len(costModels) {
		t.Errorf("drew %d of %d chart models; every chart model needs a catalog row: %v", len(rates), len(costModels), rates)
	}
}

func TestTopSinksCapsAt20(t *testing.T) {
	sinks := make([]Sink, 25)
	if got := len(topSinks(sinks)); got != 20 {
		t.Fatalf("topSinks(25) = %d sinks, want 20", got)
	}
	if got := len(topSinks(sinks[:7])); got != 7 {
		t.Fatalf("topSinks(7) = %d sinks, want 7", got)
	}
}
