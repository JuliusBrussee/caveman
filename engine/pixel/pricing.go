// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import "math"

// AnthropicPatchPx is the side length of an Anthropic vision patch. Image input
// tokens are billed per 28×28 patch: ⌈w/28⌉ × ⌈h/28⌉. The older w·h/750 area
// formula is obsolete.
const AnthropicPatchPx = 28

// PixelTier describes an Anthropic vision pricing tier. Anthropic downscales an
// image server-side when EITHER trigger fires: the long edge exceeds MaxPx, or
// the patch-token count exceeds MaxTokens. Both are resize triggers, not just
// billing ceilings — see AnthropicImageTokens.
type PixelTier struct {
	MaxPx     int // server-side downscale trigger on the long edge (px)
	MaxTokens int // patch-token cap; Anthropic's second downscale trigger
}

// StandardPixelTier is the default vision tier (all models). HiResPixelTier is
// unlocked by newer readers — density.go's isHiResModel (Fable 5, Mythos 5,
// Opus 4.8+, Sonnet 5) is the authority on which; unknown models fail closed
// to standard.
var (
	StandardPixelTier = PixelTier{MaxPx: 1568, MaxTokens: 1568}
	HiResPixelTier    = PixelTier{MaxPx: 2576, MaxTokens: 4784}
)

// AnthropicImageTokens returns the Anthropic input-token cost of a w×h image
// under tier, without the safety margin (callers apply ImageCostSafetyMargin).
//
// It models Anthropic's two server-side downscale triggers:
//  1. Long-edge downscale: if the long edge exceeds tier.MaxPx, both dimensions
//     are scaled down proportionally so the long edge lands on MaxPx (the gate
//     must never price resolution that will not survive upload).
//  2. Token-budget clamp: the patch count is capped at tier.MaxTokens. Anthropic
//     enforces this cap by a further proportional resize; we model its effect as
//     an upper-bound clamp, which is the conservative direction for a cost
//     estimate (it never under-prices the image). For every geometry the engine
//     actually emits neither trigger fires, so the two are numerically identical.
func AnthropicImageTokens(w, h int, tier PixelTier) int {
	if w <= 0 || h <= 0 {
		return 0
	}
	longEdge := max(w, h)
	if tier.MaxPx > 0 && longEdge > tier.MaxPx {
		s := float64(tier.MaxPx) / float64(longEdge)
		// Floor each post-downscale dimension at 1px: an extreme aspect ratio can
		// round the short edge to 0, which would zero out its patch count and
		// price a real image at 0 tokens — the non-conservative direction. A 1px
		// dimension still costs one patch row/column, which is the honest floor.
		w = max(1, int(math.Round(float64(w)*s)))
		h = max(1, int(math.Round(float64(h)*s)))
	}
	patchesW := ceilDiv(w, AnthropicPatchPx)
	patchesH := ceilDiv(h, AnthropicPatchPx)
	tokens := patchesW * patchesH
	if tier.MaxTokens > 0 && tokens > tier.MaxTokens {
		tokens = tier.MaxTokens
	}
	return tokens
}

func ceilDiv(a, b int) int {
	if a <= 0 {
		return 0
	}
	return (a + b - 1) / b
}
