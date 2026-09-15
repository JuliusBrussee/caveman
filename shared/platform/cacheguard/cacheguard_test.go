package cacheguard_test

import (
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/JuliusBrussee/caveman/shared/platform/cacheguard"
)

func TestInspectFreezesFirstPrefixAndRejectsDrift(t *testing.T) {
	guard := cacheguard.New()
	first, err := guard.Inspect(cacheguard.Input{
		EpochID:       "session-1:epoch-1",
		Prefix:        []byte(`{"system":"stable","tools":[]}`),
		BoundaryKnown: true,
		AdapterKnown:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Decision != cacheguard.DecisionTransformLiveOnly || first.PrefixSHA256 == "" {
		t.Fatalf("first inspection = %#v", first)
	}

	same, err := guard.Inspect(cacheguard.Input{
		EpochID:       "session-1:epoch-1",
		Prefix:        []byte(`{"system":"stable","tools":[]}`),
		BoundaryKnown: true,
		AdapterKnown:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if same.Decision != cacheguard.DecisionTransformLiveOnly {
		t.Fatalf("same prefix decision = %s", same.Decision)
	}

	drift, err := guard.Inspect(cacheguard.Input{
		EpochID:       "session-1:epoch-1",
		Prefix:        []byte(`{"system":"changed","tools":[]}`),
		BoundaryKnown: true,
		AdapterKnown:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if drift.Decision != cacheguard.DecisionPassThrough ||
		!contains(drift.Warnings, cacheguard.WarningPrefixDrift) {
		t.Fatalf("drift inspection = %#v", drift)
	}
}

func TestInspectFailsClosedOnUnknownBoundaryOrAdapter(t *testing.T) {
	result, err := cacheguard.New().Inspect(cacheguard.Input{
		EpochID: "unknown",
		Prefix:  []byte(`{"system":"stable"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != cacheguard.DecisionPassThrough ||
		!contains(result.Warnings, cacheguard.WarningMissingBoundary) ||
		!contains(result.Warnings, cacheguard.WarningAdapterAmbiguity) {
		t.Fatalf("unknown inspection = %#v", result)
	}
}

func TestDetectVolatileReturnsKindsAndOffsetsNotValues(t *testing.T) {
	prefix := []byte(`{"run_id":"secret-run","when":"2026-07-31T10:15:30Z","nonce":"private","id":"019fb52b-d42a-7322-a379-7515e85f131d"}`)
	matches := cacheguard.DetectVolatile(prefix)
	if len(matches) != 4 {
		t.Fatalf("matches = %#v", matches)
	}
	for _, match := range matches {
		if strings.Contains(match.Kind, "secret") || match.Offset < 0 {
			t.Fatalf("volatile match leaked value: %#v", match)
		}
	}
}

func TestConcurrentFirstObservationCannotReplaceEpoch(t *testing.T) {
	guard := cacheguard.New()
	prefixes := [][]byte{[]byte("alpha"), []byte("beta")}
	var wg sync.WaitGroup
	results := make(chan cacheguard.Result, len(prefixes))
	for _, prefix := range prefixes {
		wg.Add(1)
		go func(value []byte) {
			defer wg.Done()
			result, err := guard.Inspect(cacheguard.Input{
				EpochID:       "shared",
				Prefix:        value,
				BoundaryKnown: true,
				AdapterKnown:  true,
			})
			if err != nil {
				t.Error(err)
				return
			}
			results <- result
		}(prefix)
	}
	wg.Wait()
	close(results)

	var transformed, passed int
	for result := range results {
		switch result.Decision {
		case cacheguard.DecisionTransformLiveOnly:
			transformed++
		case cacheguard.DecisionPassThrough:
			passed++
		}
	}
	if transformed != 1 || passed != 1 {
		t.Fatalf("transform/pass-through = %d/%d", transformed, passed)
	}
}

func TestInspectContentBlindDigestRejectsDrift(t *testing.T) {
	guard := cacheguard.New()
	firstDigest := strings.Repeat("a", 64)
	secondDigest := strings.Repeat("b", 64)
	first, err := guard.Inspect(cacheguard.Input{
		EpochID:       "agent:session",
		PrefixSHA256:  firstDigest,
		BoundaryKnown: true,
		AdapterKnown:  true,
	})
	if err != nil || first.PrefixSHA256 != firstDigest ||
		first.Decision != cacheguard.DecisionTransformLiveOnly {
		t.Fatalf("first digest inspection = %#v, %v", first, err)
	}
	drift, err := guard.Inspect(cacheguard.Input{
		EpochID:       "agent:session",
		PrefixSHA256:  secondDigest,
		BoundaryKnown: true,
		AdapterKnown:  true,
	})
	if err != nil || drift.Decision != cacheguard.DecisionPassThrough ||
		!contains(drift.Warnings, cacheguard.WarningPrefixDrift) {
		t.Fatalf("digest drift inspection = %#v, %v", drift, err)
	}
}

// TestEpochsMapIsBounded proves the Guard evicts oldest epochs so its epochs map
// cannot grow without bound over a long-running proxy's lifetime. It exercises far
// more distinct epochs than the cap and confirms the most-recent epoch is retained
// (its digest still trips drift) while an early one has been evicted (treated as a
// fresh first observation, no drift warning).
func TestEpochsMapIsBounded(t *testing.T) {
	guard := cacheguard.New()
	const total = 20000 // > defaultEpochCap
	digest := strings.Repeat("a", 64)
	for i := 0; i < total; i++ {
		id := "epoch-" + strconv.Itoa(i)
		if _, err := guard.Inspect(cacheguard.Input{
			EpochID: id, PrefixSHA256: digest, BoundaryKnown: true, AdapterKnown: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// The earliest epoch was evicted: re-inspecting it is a fresh first observation
	// (transform-live-only), NOT a drift warning.
	early, err := guard.Inspect(cacheguard.Input{
		EpochID: "epoch-0", PrefixSHA256: strings.Repeat("b", 64), BoundaryKnown: true, AdapterKnown: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if early.Decision != cacheguard.DecisionTransformLiveOnly {
		t.Fatalf("evicted epoch must be a fresh observation, got %#v", early)
	}
	// The most-recent epoch is still retained: a changed digest trips drift.
	recent, err := guard.Inspect(cacheguard.Input{
		EpochID: "epoch-" + strconv.Itoa(total-1), PrefixSHA256: strings.Repeat("c", 64), BoundaryKnown: true, AdapterKnown: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(recent.Warnings, cacheguard.WarningPrefixDrift) {
		t.Fatalf("recent epoch must still be tracked for drift, got %#v", recent)
	}
}

func contains(values []cacheguard.Warning, wanted cacheguard.Warning) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
