// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import "testing"

// TestDensityEnvelopeStaysNoResize asserts that every level × tier full-canvas
// page stays inside its tier's no-resize envelope: long edge ≤ tier.MaxPx AND
// raw patch tokens ≤ tier.MaxTokens. If either is exceeded Anthropic downscales
// server-side and destroys the glyphs, so the gate must never emit such a page.
func TestDensityEnvelopeStaysNoResize(t *testing.T) {
	cases := []struct {
		name  string
		model string
	}{
		{"std", "gpt-5.6"},          // GPT-5.6 → standard tier
		{"hires", "claude-fable-5"}, // Fable 5 → hi-res tier
	}
	for _, tier := range cases {
		for _, level := range []DensityLevel{DensityConservative, DensityBalanced, DensityMax} {
			rp := ResolveDensity(tier.model, level)
			cw, ch := rp.canvas()
			cols := rp.colsPerPage(cw)
			lines := rp.linesPerPage(ch)
			pw := rp.pageWidthPx(cols)
			ph := rp.pageHeightPx(lines)
			longEdge := max(pw, ph)
			// Raw (un-clamped, un-downscaled) patch tokens — the true cost before
			// any server-side resize trigger fires.
			rawTokens := ceilDiv(pw, AnthropicPatchPx) * ceilDiv(ph, AnthropicPatchPx)
			t.Run(tier.name+"/"+string(level), func(t *testing.T) {
				if longEdge > rp.Tier.MaxPx {
					t.Fatalf("page %dx%d long edge %d exceeds tier MaxPx %d", pw, ph, longEdge, rp.Tier.MaxPx)
				}
				if rawTokens > rp.Tier.MaxTokens {
					t.Fatalf("page %dx%d raw tokens %d exceed tier cap %d", pw, ph, rawTokens, rp.Tier.MaxTokens)
				}
			})
		}
	}
}

// TestCharsPerPageBalancedVsConservative pins per-page character capacity on
// the standard canvas. Geometry changes must update this test and its renderer
// contract together.
func TestCharsPerPageBalancedVsConservative(t *testing.T) {
	cons := ResolveDensity("gpt-5.6", DensityConservative) // std, 5/8/mono
	bal := ResolveDensity("gpt-5.6", DensityBalanced)      // std, 4/6/zebra
	consChars := cons.charsPerPage(MaxWidthPx, MaxHeightPx)
	balChars := bal.charsPerPage(MaxWidthPx, MaxHeightPx)
	if consChars != 28080 {
		t.Fatalf("conservative chars/page = %d, want 28080 (312 cols × 90 rows)", consChars)
	}
	if balChars != 46291 {
		t.Fatalf("balanced chars/page = %d, want 46291 (389 cols × 119 rows)", balChars)
	}
	// Pin capacity ratio through integer math so floating-point rounding cannot
	// hide geometry drift.
	if got := balChars * 10000 / consChars; got != 16485 {
		t.Fatalf("balanced/conservative ratio*1e4 = %d, want 16485", got)
	}
}

// TestHiResPageGeometryPatchAligned extends the std-page patch-alignment guard
// (render_test.go TestPageGeometryPatchAligned) to the hi-res tier: a full hi-res
// page lands on a whole 52-patch height, fills the 92-patch width inside the
// 2576px long edge, and costs exactly the tier's 4784-token ceiling.
func TestHiResPageGeometryPatchAligned(t *testing.T) {
	const patch = AnthropicPatchPx
	for _, level := range []DensityLevel{DensityConservative, DensityBalanced} {
		rp := ResolveDensity("claude-fable-5", level) // hi-res tier
		t.Run(string(level), func(t *testing.T) {
			cw, ch := rp.canvas()
			if cw != HiResMaxWidthPx || ch != HiResMaxHeightPx {
				t.Fatalf("hi-res canvas = %dx%d, want %dx%d", cw, ch, HiResMaxWidthPx, HiResMaxHeightPx)
			}
			cols := rp.pageCols() // conservative 513, balanced 641
			rows := rp.pageRows() // conservative 181, balanced 241
			pw := rp.pageWidthPx(cols)
			ph := rp.pageHeightPx(rows)

			// Height is an exact whole number of patches (52 × 28 = 1456), no
			// partial patch row.
			if ph != HiResMaxHeightPx || ph%patch != 0 {
				t.Fatalf("hi-res page height = %d, want %d (= %d patches)", ph, HiResMaxHeightPx, HiResMaxHeightPx/patch)
			}
			// Width fills the 92-patch grid within the tier long edge (2573 ≤ 2576).
			if pw > cw {
				t.Fatalf("hi-res page width %d exceeds tier long edge %d", pw, cw)
			}
			if got := ceilDiv(pw, patch); got != HiResMaxWidthPx/patch {
				t.Fatalf("hi-res page width %d = %d patches, want %d", pw, got, HiResMaxWidthPx/patch)
			}
			// A full hi-res page costs exactly the tier's no-resize token ceiling.
			if got := AnthropicImageTokens(pw, ph, HiResPixelTier); got != 4784 {
				t.Fatalf("hi-res full page = %d tokens, want 4784 (pre-margin)", got)
			}
		})
	}
}

// TestImageTokensForRowsPerLevel asserts the gate prices each level's actual
// geometry: for the same rows the balanced page is cheaper (narrower + shorter),
// balanced with its own char budget packs 119 rows into one image, and multi-
// column pages always price at conservative geometry (density never applies to
// the multi-column renderer).
func TestImageTokensForRowsPerLevel(t *testing.T) {
	cons := conservativeStdParams
	bal := ResolveDensity("gpt-5.6", DensityBalanced)

	// Same 90 rows, 312 cols, single column, default budget.
	if got := imageTokensForRows(90, 312, 1, 0, ReadableCharsPerImage, cons); got != 1602 {
		t.Fatalf("conservative 90-row cost = %v, want 1602 (ceil(1456×1.10))", got)
	}
	if got := imageTokensForRows(90, 312, 1, 0, ReadableCharsPerImage, bal); got != 991 {
		t.Fatalf("balanced 90-row cost = %v, want 991 (900 raw × 1.10 margin, ceil)", got)
	}

	// Balanced with its own per-page budget (312 × 119) fits 119 rows in ONE
	// image at 1257×724 → 45×26 patches = 1170 tokens → ceil(1170×1.10) = 1287.
	balBudget := 312 * bal.linesPerPage(MaxHeightPx) // 312 × 119 = 37128
	if got := imageTokensForRows(119, 312, 1, 0, balBudget, bal); got != 1287 {
		t.Fatalf("balanced full-page 119-row cost = %v, want 1287", got)
	}

	// Multi-column pages ignore density geometry (the multicol renderer uses
	// CellW/CellH), so the price is identical for any profile.
	mcBal := imageTokensForRows(120, 100, 2, 0, ReadableCharsPerImage, bal)
	mcCons := imageTokensForRows(120, 100, 2, 0, ReadableCharsPerImage, cons)
	if mcBal != mcCons {
		t.Fatalf("multicol priced differently by profile: bal=%v cons=%v", mcBal, mcCons)
	}
}
