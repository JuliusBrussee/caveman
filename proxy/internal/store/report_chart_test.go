package store

import "testing"

func TestCachedRateAssumes90PercentCached(t *testing.T) {
	if got := cachedRate(10.0); got < 1.899 || got > 1.901 {
		t.Fatalf("cachedRate(10) = %v, want 1.90 (0.19x list)", got)
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
