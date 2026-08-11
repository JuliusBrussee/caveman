package cacheengine

import (
	"testing"
)

func TestObserveRequiresProviderCacheTelemetry(t *testing.T) {
	result := NativeResult{
		Applied:      true,
		Decision:     DecisionApply,
		OptimizerIDs: []string{AnthropicStableOptimizerID},
		Profile:      Profile{Attribution: AttributionCausal},
		ClaimBasis:   "inferred",
	}
	unknown := Observe(result, UsageObservation{})
	if unknown.Status != ObservationUnavailable || unknown.ProviderConfirmed || unknown.VerifiedSavingsUSD != 0 {
		t.Fatalf("unknown = %#v", unknown)
	}
	hit := Observe(result, UsageObservation{
		CachedInputTokens: 800, CacheObserved: true, CacheStatus: "hit",
	})
	if hit.Status != ObservationHit || !hit.ProviderConfirmed || hit.CachedInputTokens != 800 {
		t.Fatalf("hit = %#v", hit)
	}
	if hit.Basis != "provider_observed" || hit.VerifiedSavingsUSD != 0 {
		t.Fatalf("observation crossed standalone verified boundary: %#v", hit)
	}
}

func TestObserveRejectsOrganicHitAttribution(t *testing.T) {
	result := NativeResult{
		Applied:    false,
		Decision:   DecisionObserveOnly,
		Profile:    Profile{Attribution: AttributionOrganic},
		ClaimBasis: "inferred",
	}
	observed := Observe(result, UsageObservation{
		CachedInputTokens: 900, CacheObserved: true, CacheStatus: "hit",
	})
	if observed.Status != ObservationHit || observed.AttributedToEngine {
		t.Fatalf("organic = %#v", observed)
	}
}

func TestObserveRejectsNegativeDirectCounters(t *testing.T) {
	observed := Observe(NativeResult{}, UsageObservation{CacheObserved: true, CachedInputTokens: -1})
	if observed.Status != ObservationUnavailable || observed.ProviderConfirmed {
		t.Fatalf("negative counters accepted: %#v", observed)
	}
}
