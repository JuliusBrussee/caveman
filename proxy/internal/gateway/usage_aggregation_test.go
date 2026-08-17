package gateway

import (
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func TestAddUsageOverflowFailsClosed(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	got := addUsage(
		providers.UsageObservation{InputTokens: maxInt, InputTokensReported: true, OutputTokensReported: true, ObservationCount: 1},
		providers.UsageObservation{InputTokens: 1, InputTokensReported: true, OutputTokensReported: true, ObservationCount: 1},
	)
	if !got.Malformed || got.Complete() || got.InputTokens != 0 {
		t.Fatalf("overflow aggregate = %+v, want malformed honest zero", got)
	}
}
