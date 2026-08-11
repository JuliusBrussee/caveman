package sampling_test

import (
	"math"
	"testing"

	"github.com/JuliusBrussee/caveman/shared/platform/sampling"
)

func TestFractionIsStableAndInRange(t *testing.T) {
	first := sampling.Fraction("org", "project", "model", "request-42")
	if first < 0 || first >= 1 {
		t.Fatalf("Fraction = %v, want [0,1)", first)
	}
	for i := 0; i < 100; i++ {
		if got := sampling.Fraction("org", "project", "model", "request-42"); got != first {
			t.Fatalf("Fraction changed on run %d: got %v want %v", i, got, first)
		}
	}
}

// Length prefixing is the reason two different key lists cannot collide by
// concatenation. Without it these two scopes would share one selection.
func TestFractionDistinguishesConcatenationCollisions(t *testing.T) {
	if sampling.Fraction("ab", "c") == sampling.Fraction("a", "bc") {
		t.Fatal("key list boundaries were not length-prefixed")
	}
	if sampling.Fraction("org", "project") == sampling.Fraction("project", "org") {
		t.Fatal("key order must change the fraction")
	}
}

func TestSampledRateBounds(t *testing.T) {
	keys := []string{"org", "project", "monitor", "request"}
	for _, rate := range []float64{0, -0.5, math.Inf(-1)} {
		if sampling.Sampled(rate, keys...) {
			t.Fatalf("rate %v must select nothing", rate)
		}
	}
	for _, rate := range []float64{1, 1.5, math.Inf(1)} {
		if !sampling.Sampled(rate, keys...) {
			t.Fatalf("rate %v must select everything", rate)
		}
	}
	// Fail closed: an unusable rate selects nothing rather than everything.
	if sampling.Sampled(math.NaN(), keys...) {
		t.Fatal("NaN rate must fail closed")
	}
}

func TestSampledIsScopedToEveryKey(t *testing.T) {
	const rate = 0.5
	base := sampling.Sampled(rate, "org", "project", "monitor", "request")
	differs := false
	for _, keys := range [][]string{
		{"other-org", "project", "monitor", "request"},
		{"org", "other-project", "monitor", "request"},
		{"org", "project", "other-monitor", "request"},
		{"org", "project", "monitor", "other-request"},
	} {
		if sampling.Sampled(rate, keys...) != base {
			differs = true
		}
	}
	if !differs {
		t.Fatal("changing any scope key must be able to change the selection")
	}
}

func TestSampledApproximatesRate(t *testing.T) {
	const (
		total = 10000
		rate  = 0.1
	)
	selected := 0
	for i := 0; i < total; i++ {
		if sampling.Sampled(rate, "org", "project", "monitor", string(rune(i))+":request") {
			selected++
		}
	}
	if selected < 850 || selected > 1150 {
		t.Fatalf("selected=%d/%d, want near %v", selected, total, rate)
	}
}
