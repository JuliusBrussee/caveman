package gateway

import (
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/pixel"
)

// The proxy live zone must CONSUME density, not ignore it: with CAVE_PIXEL_DENSITY
// unset (default balanced) a density-capable reader renders denser (more rows per
// page → fewer pages) than a reader that fails closed to conservative, and an
// explicit conservative env override forces the fail-closed geometry back. This
// mirrors the engine's default-balanced page-ratio e2e on the proxy's own render
// path. All numbers are inferred; the proxy books no verified savings here.
func TestLiveZoneConsumesDensity(t *testing.T) {
	// Newline-delimited so the per-line wrap cost stays cheap; wide enough that both
	// densities fill columns and the row-pitch advantage shows as fewer pages.
	text := strings.Repeat(strings.Repeat("x", 200)+"\n", 1500)

	// Default env (unset → balanced) + a std-tier density-capable reader (gpt-5.6).
	balOpts := pixel.DefaultTransformOptions("gpt-5.6")
	balImgs, before, after, _, ok := renderLiveZonePNGs(text, balOpts)
	if !ok || len(balImgs) == 0 {
		t.Fatalf("balanced live-zone render failed: ok=%v pages=%d", ok, len(balImgs))
	}

	// An unrecognised reader model must fail closed to conservative standard-tier.
	consOpts := pixel.DefaultTransformOptions("mystery-reader-9")
	consImgs, _, _, _, okC := renderLiveZonePNGs(text, consOpts)
	if !okC || len(consImgs) == 0 {
		t.Fatalf("conservative fail-closed render failed: ok=%v pages=%d", okC, len(consImgs))
	}

	if !(len(balImgs) < len(consImgs)) {
		t.Fatalf("default balanced must pack denser than fail-closed conservative: balanced=%d conservative=%d", len(balImgs), len(consImgs))
	}

	// Honest per-image pricing: the after estimate must reflect real image pixels
	// (~1.5k tokens for a full standard page), never the old flat 100/image that
	// would over-count savings — and it must still be a profitable win.
	if after <= 0 || after >= before {
		t.Fatalf("after must be a positive honest estimate below before: before=%d after=%d", before, after)
	}
	if after <= len(balImgs)*100 {
		t.Fatalf("after (%d) collapsed to the old flat 100/image undercount for %d pages", after, len(balImgs))
	}

	// An explicit conservative env override wins over the default, forcing the same
	// geometry (and page count) as the fail-closed reader.
	t.Setenv("CAVE_PIXEL_DENSITY", "conservative")
	envImgs, _, _, _, okE := renderLiveZonePNGs(text, balOpts)
	if !okE || len(envImgs) != len(consImgs) {
		t.Fatalf("CAVE_PIXEL_DENSITY=conservative must match fail-closed page count: env=%d conservative=%d", len(envImgs), len(consImgs))
	}
}

// The density profile the live zone renders at must be tier-correct: a hi-res reader
// draws the hi-res canvas at balanced, an unrecognised reader fails closed to the
// conservative standard tier regardless of the requested level.
func TestResolveDensityDrawTiers(t *testing.T) {
	hi := pixel.ResolveDensityDraw("claude-fable-5", pixel.DensityBalanced)
	if hi.Tier != pixel.HiResPixelTier || hi.CanvasH != pixel.HiResMaxHeightPx {
		t.Fatalf("hi-res reader must draw the hi-res canvas: tier=%+v canvasH=%d", hi.Tier, hi.CanvasH)
	}
	if !hi.Zebra || hi.Layers != 1 {
		t.Fatalf("balanced must be single-layer zebra ink: zebra=%v layers=%d", hi.Zebra, hi.Layers)
	}

	closed := pixel.ResolveDensityDraw("mystery-reader-9", pixel.DensityMax)
	if closed.Tier != pixel.StandardPixelTier || closed.CanvasH != pixel.MaxHeightPx {
		t.Fatalf("unknown reader must fail closed to the standard tier: tier=%+v canvasH=%d", closed.Tier, closed.CanvasH)
	}
	if closed.Zebra || closed.Layers != 1 || (closed.Style != pixel.RenderStyle{}) {
		t.Fatalf("fail-closed profile must be mono single-layer conservative geometry: %+v", closed)
	}
}
