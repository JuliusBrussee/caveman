package store

import (
	"strings"
	"testing"
)

// TestSpendPricesDisjointBuckets proves the headline money number is arithmetic
// on provider-counted, DISJOINT buckets: a cache read is not billed at the
// fresh-input rate, and the four components sum to the total.
func TestSpendPricesDisjointBuckets(t *testing.T) {
	acc := spendAccumulator{}
	acc.observe(turnEvent{
		BillingUsagePresent: true, ProviderKey: "anthropic", Model: "claude-opus-5",
		InputFreshTokens: 1_000_000, CacheReadInputTokens: 2_000_000,
		CacheCreationInputTokens: 500_000, OutputTokens: 100_000,
	})
	spend := buildLearnSpend(acc, 30)
	if spend == nil {
		t.Fatal("expected a priced window")
	}
	if spend.Basis != spendBasis {
		t.Fatalf("basis = %q, want %q", spend.Basis, spendBasis)
	}
	byKey := map[string]LearnSpendComponent{}
	var sum float64
	for _, component := range spend.Components {
		byKey[component.Key] = component
		sum += component.USD
	}
	if len(byKey) != 4 {
		t.Fatalf("expected four disjoint components, got %d: %+v", len(byKey), spend.Components)
	}
	if byKey["cache_read"].Tokens != 2_000_000 {
		t.Fatalf("cache read tokens = %d", byKey["cache_read"].Tokens)
	}
	// A cache read must cost strictly less per token than fresh input, or the
	// whole effective-rate mechanic is meaningless.
	freshPerTok := byKey["fresh_input"].USD / float64(byKey["fresh_input"].Tokens)
	readPerTok := byKey["cache_read"].USD / float64(byKey["cache_read"].Tokens)
	if !(readPerTok < freshPerTok) {
		t.Fatalf("cache read (%v/tok) must be cheaper than fresh input (%v/tok)", readPerTok, freshPerTok)
	}
	if diff := sum - spend.USD; diff > 0.01 || diff < -0.01 {
		t.Fatalf("components %v do not sum to headline %v", sum, spend.USD)
	}
	if spend.EffectiveInputMultiplier <= 0 || spend.EffectiveInputMultiplier >= 1 {
		t.Fatalf("mixed cache traffic must land strictly under 1.0, got %v", spend.EffectiveInputMultiplier)
	}
	if spend.CatalogVersion == "" {
		t.Fatal("a priced figure must name the catalog version it used")
	}
}

// TestSpendNeverBorrowsAnotherModelsPrice is the fail-closed guard: an unknown
// model contributes zero dollars and is disclosed, never priced at a sibling's
// rate. A plausible wrong number is worse than a missing one.
func TestSpendNeverBorrowsAnotherModelsPrice(t *testing.T) {
	acc := spendAccumulator{}
	acc.observe(turnEvent{
		BillingUsagePresent: true, ProviderKey: "anthropic", Model: "claude-imaginary-9",
		InputFreshTokens: 5_000_000, OutputTokens: 1_000_000,
	})
	spend := buildLearnSpend(acc, 30)
	if spend == nil {
		t.Fatal("expected an unpriced disclosure block, not nil")
	}
	if spend.USD != 0 {
		t.Fatalf("unknown model must contribute no spend, got %v", spend.USD)
	}
	if len(spend.Unpriced) != 1 || spend.Unpriced[0].Tokens != 6_000_000 {
		t.Fatalf("unknown model must be disclosed with its tokens: %+v", spend.Unpriced)
	}
	joined := strings.Join(spend.Caveats, " ")
	if !strings.Contains(joined, "floor") {
		t.Fatalf("an excluded model must make the total a disclosed floor: %v", spend.Caveats)
	}
}

// TestSpendResolvesDatedModelAlias allows exactly one alias step — the trailing
// snapshot date, which names the same model — and nothing looser.
func TestSpendResolvesDatedModelAlias(t *testing.T) {
	if _, _, ok := resolveModelPrice("anthropic", "claude-haiku-4-5-20251001"); !ok {
		t.Fatal("an exact dated catalog row must resolve")
	}
	if _, _, ok := resolveModelPrice("anthropic", "claude-opus-5-20260401"); !ok {
		t.Fatal("a dated id must fall back to its undated catalog row")
	}
	if _, _, ok := resolveModelPrice("anthropic", "claude-opus"); ok {
		t.Fatal("a family prefix must NOT resolve to a sibling model's price")
	}
	if _, _, ok := resolveModelPrice("anthropic", ""); ok {
		t.Fatal("an empty model must never price")
	}
}

// TestSpendAbsentWithoutBillingUsage keeps a dollar figure off a machine whose
// transcripts carry no usage blocks: no card at all beats a $0.00 card.
func TestSpendAbsentWithoutBillingUsage(t *testing.T) {
	acc := spendAccumulator{}
	acc.observe(turnEvent{ProviderKey: "anthropic", Model: "claude-opus-5", ContextTotal: 100_000})
	if spend := buildLearnSpend(acc, 30); spend != nil {
		t.Fatalf("turns without billing usage must not produce spend: %+v", spend)
	}
}

// TestCodexBillingUsageNormalizesInclusiveInput pins the semantic difference
// that would otherwise silently overcharge every OpenAI user: input_tokens
// already CONTAINS the cached share.
func TestCodexBillingUsageNormalizesInclusiveInput(t *testing.T) {
	fresh, cached, out, ok := codexBillingUsage(map[string]any{
		"input_tokens": float64(10_000), "cached_input_tokens": float64(8_000), "output_tokens": float64(500),
	})
	if !ok || fresh != 2_000 || cached != 8_000 || out != 500 {
		t.Fatalf("fresh=%d cached=%d out=%d ok=%v; want 2000/8000/500", fresh, cached, out, ok)
	}
	// Incoherent input (cached larger than the inclusive total) must be refused
	// rather than produce a negative fresh bucket.
	if _, _, _, ok := codexBillingUsage(map[string]any{
		"input_tokens": float64(100), "cached_input_tokens": float64(900),
	}); ok {
		t.Fatal("cached > input must be refused")
	}
}

// TestClaudeBillingUsageKeepsBucketsDisjoint pins the opposite convention:
// Anthropic's input_tokens is already uncached-only.
func TestClaudeBillingUsageKeepsBucketsDisjoint(t *testing.T) {
	fresh, out, ok := claudeBillingUsage(map[string]any{
		"message": map[string]any{"usage": map[string]any{
			"input_tokens": float64(120), "cache_read_input_tokens": float64(9_000),
			"cache_creation_input_tokens": float64(300), "output_tokens": float64(45),
		}},
	})
	if !ok || fresh != 120 || out != 45 {
		t.Fatalf("fresh=%d out=%d ok=%v; want 120/45", fresh, out, ok)
	}
	if _, _, ok := claudeBillingUsage(map[string]any{"message": map[string]any{}}); ok {
		t.Fatal("a turn with no usage block must state nothing")
	}
}

// TestPriceInputTokensUsesMeasuredRate proves sinks are priced at what the user
// ACTUALLY pays after caching, not at list. A heavily-cached user's findings
// must not be inflated by an order of magnitude.
func TestPriceInputTokensUsesMeasuredRate(t *testing.T) {
	cached := spendAccumulator{}
	cached.observe(turnEvent{
		BillingUsagePresent: true, ProviderKey: "anthropic", Model: "claude-opus-5",
		InputFreshTokens: 10_000, CacheReadInputTokens: 990_000,
	})
	cold := spendAccumulator{}
	cold.observe(turnEvent{
		BillingUsagePresent: true, ProviderKey: "anthropic", Model: "claude-opus-5",
		InputFreshTokens: 1_000_000,
	})
	cachedSpend, coldSpend := buildLearnSpend(cached, 30), buildLearnSpend(cold, 30)
	if cachedSpend == nil || coldSpend == nil {
		t.Fatal("both windows must price")
	}
	cheap := cachedSpend.priceInputTokens(1_000_000)
	dear := coldSpend.priceInputTokens(1_000_000)
	if !(cheap < dear/2) {
		t.Fatalf("a well-cached user's sink must cost far less: cheap=%v dear=%v", cheap, dear)
	}
	if (&LearnSpend{}).priceInputTokens(1_000) != 0 {
		t.Fatal("an unpriced window must price nothing")
	}
}
