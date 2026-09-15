// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"math"
	"testing"
)

// TestAnthropicImageTokensWorkedExamples asserts the raw patch-token math
// (⌈w/28⌉ × ⌈h/28⌉, capped) against Anthropic's own worked examples, with no
// safety margin applied so the geometry is pinned exactly.
func TestAnthropicImageTokensWorkedExamples(t *testing.T) {
	cases := []struct {
		name string
		w, h int
		tier PixelTier
		want int
	}{
		// 1092/28 = 39 exactly → 39×39 = 1521, under the 1568 cap.
		{"square standard", 1092, 1092, StandardPixelTier, 1521},
		// 56×26 = 1456, the engine's default full page — under the cap.
		{"engine full page", 1568, 728, StandardPixelTier, 1456},
		// Long edge 2576 > 1568 → downscale to 1568×886 → 56×32 = 1792 → cap 1568.
		{"oversize standard downscale+cap", 2576, 1456, StandardPixelTier, 1568},
		// 92×52 = 4784, exactly the high-res no-resize ceiling.
		{"hires full canvas", 2576, 1456, HiResPixelTier, 4784},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AnthropicImageTokens(tc.w, tc.h, tc.tier); got != tc.want {
				t.Fatalf("AnthropicImageTokens(%d, %d) = %d, want %d", tc.w, tc.h, got, tc.want)
			}
		})
	}
}

// TestAnthropicImageTokensEdges pins the degenerate inputs and the cap boundary.
func TestAnthropicImageTokensEdges(t *testing.T) {
	if got := AnthropicImageTokens(0, 728, StandardPixelTier); got != 0 {
		t.Fatalf("zero width = %d, want 0", got)
	}
	if got := AnthropicImageTokens(1568, -1, StandardPixelTier); got != 0 {
		t.Fatalf("negative height = %d, want 0", got)
	}
	// Standard cap must never be exceeded even at the tier's max canvas.
	if got := AnthropicImageTokens(1568, 1568, StandardPixelTier); got != StandardPixelTier.MaxTokens {
		t.Fatalf("standard max canvas = %d, want cap %d", got, StandardPixelTier.MaxTokens)
	}
	// Extreme aspect ratio: a 15680×4 strip downscales the short edge to
	// round(4 * 1568/15680) = round(0.4) = 0. Without a 1px floor that would
	// zero out patchesH and price the image at 0 tokens — a non-conservative
	// "free" image. The short edge must floor at 1px → 56×1 = 56 patches.
	if got := AnthropicImageTokens(15680, 4, StandardPixelTier); got != 56 {
		t.Fatalf("extreme aspect ratio = %d, want 56 (short edge floored at 1px)", got)
	}
}

// TestImageCostSafetyMarginApplication asserts the 1.10 margin is applied on
// top of the raw patch tokens, tested separately from the raw geometry.
func TestImageCostSafetyMarginApplication(t *testing.T) {
	raw := AnthropicImageTokens(1568, 728, StandardPixelTier) // 1456
	if raw != 1456 {
		t.Fatalf("raw = %d, want 1456", raw)
	}
	withMargin := int(math.Ceil(float64(raw) * ImageCostSafetyMargin))
	if withMargin != 1602 { // ceil(1456 * 1.10) = ceil(1601.6)
		t.Fatalf("withMargin = %d, want 1602", withMargin)
	}
}
