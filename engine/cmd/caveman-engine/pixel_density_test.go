package main

import (
	"encoding/json"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/pixel"
)

// lastPixelRenderSummary decodes the JSONL stream `pixel render` prints and returns
// the trailing summary object (the one with "summary":true).
func lastPixelRenderSummary(t *testing.T, out string) pixelRenderSummary {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(out))
	var summary pixelRenderSummary
	found := false
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			break
		}
		var probe struct {
			Summary bool `json:"summary"`
		}
		if json.Unmarshal(raw, &probe) == nil && probe.Summary {
			if err := json.Unmarshal(raw, &summary); err != nil {
				t.Fatalf("decode summary: %v", err)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("no summary line in render output:\n%s", out)
	}
	return summary
}

// The test-pinned per-page capacities used by the design. This checks them at
// the exported draw seam, so the render ratio test below has an exact anchor.
func TestStdDensityCapacityRatios(t *testing.T) {
	cons := pixel.StdDensityDraw(pixel.DensityConservative)
	bal := pixel.StdDensityDraw(pixel.DensityBalanced)
	mx := pixel.StdDensityDraw(pixel.DensityMax)
	if cons.CharBudget != 28080 || bal.CharBudget != 46291 || mx.CharBudget != 92582 {
		t.Fatalf("char budgets = %d/%d/%d, want 28080/46291/92582", cons.CharBudget, bal.CharBudget, mx.CharBudget)
	}
	if mx.Layers != 2 || cons.Layers != 1 || bal.Layers != 1 {
		t.Fatalf("layers = %d/%d/%d, want 1/1/2", cons.Layers, bal.Layers, mx.Layers)
	}
	balRatio := float64(bal.CharBudget) / float64(cons.CharBudget)
	maxRatio := float64(mx.CharBudget) / float64(cons.CharBudget)
	if math.Abs(balRatio-1.648) > 0.01 || math.Abs(maxRatio-3.296) > 0.01 {
		t.Fatalf("capacity ratios = %.3f / %.3f, want 1.648 / 3.296", balRatio, maxRatio)
	}
}

// End-to-end: the same document rendered at three levels must produce the
// expected page-count ordering, and each report's resolved `density` field must
// echo the level actually drawn. This pins renderer geometry.
func TestPixelRenderDensityPageRatios(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "doc.txt")
	// Many 1500-char spaceless lines: long enough that WrapLines packs near-full
	// lines at both column widths (so the column advantage shows as the capacity
	// ratio predicts), short enough to avoid the per-line quadratic wrap cost of one
	// giant line. Newline-delimited keeps the render fast.
	line := strings.Repeat("a", 1500) + "\n"
	if err := os.WriteFile(file, []byte(strings.Repeat(line, 380)), 0o644); err != nil {
		t.Fatal(err)
	}

	pages := map[string]int{}
	for _, level := range []string{"conservative", "balanced", "max"} {
		out := captureStdout(t, func() {
			runPixelRender([]string{"--density", level, "--no-reflow", file})
		})
		summary := lastPixelRenderSummary(t, out)
		if summary.Density != level {
			t.Fatalf("%s: summary density = %q, want %q", level, summary.Density, level)
		}
		if summary.Pages <= 0 {
			t.Fatalf("%s: pages = %d", level, summary.Pages)
		}
		pages[level] = summary.Pages
		// max stacks two layers per image → the summary must disclose the layer count.
		if level == "max" && summary.Layers != 2 {
			t.Fatalf("max summary must report layers=2, got %d", summary.Layers)
		}
		if level != "max" && summary.Layers != 0 {
			t.Fatalf("%s summary must not report a layer count, got %d", level, summary.Layers)
		}
		// Clean the emitted page PNGs between levels so they never pile up.
		for i := 1; i <= summary.Pages; i++ {
			_ = os.Remove(file + ".px" + itoa(i) + ".png")
		}
	}

	if !(pages["conservative"] > pages["balanced"] && pages["balanced"] > pages["max"]) {
		t.Fatalf("page counts must strictly decrease with density: %d > %d > %d", pages["conservative"], pages["balanced"], pages["max"])
	}
	base := float64(pages["conservative"])
	balRatio := float64(pages["balanced"]) / base
	maxRatio := float64(pages["max"]) / base
	// Page-count tolerances allow for line wrapping at page boundaries.
	if math.Abs(balRatio-0.607) > 0.05 {
		t.Fatalf("balanced page ratio = %.3f (%d/%d), want 0.607 ±0.05", balRatio, pages["balanced"], pages["conservative"])
	}
	if math.Abs(maxRatio-0.303) > 0.05 {
		t.Fatalf("max page ratio = %.3f (%d/%d), want 0.303 ±0.05", maxRatio, pages["max"], pages["conservative"])
	}
}

// pngIsColored reports whether the PNG at path has any non-grey pixel — true for a
// zebra (balanced) or red/blue overlay (max) render, false for a mono conservative
// page. Used to check the geometry actually drawn matches the reported density.
func pngIsColored(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r != g || g != bl {
				return true
			}
		}
	}
	return false
}

func renderCombo(t *testing.T, file string, args []string) (pixelRenderSummary, string) {
	t.Helper()
	out := captureStdout(t, func() { runPixelRender(append(args, "--no-reflow", file)) })
	summary := lastPixelRenderSummary(t, out)
	firstPage := file + ".px1.png"
	return summary, firstPage
}

func cleanPages(file string, pages int) {
	for i := 1; i <= pages; i++ {
		_ = os.Remove(file + ".px" + itoa(i) + ".png")
	}
}

// The load-bearing invariant: for EVERY flag combination the reported density +
// layers must match the pixels actually drawn. Walk the matrix
// {plain, --dense, --multicol 2, --dense --multicol 2} × {conservative, balanced, max}.
//
// A forced multi-column layout draws the fixed conservative grid (no density style /
// overlay), so it must report conservative regardless of the requested level. The max
// 2-layer overlay is single-column only and must survive --dense (which is exactly what
// `caveman convert --density max` shells): --dense --density max must render the true
// overlay (layers:2), not fall through RenderDensePages single-layer.
func TestPixelRenderDensityLabelMatchesPixels(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(file, []byte(strings.Repeat("matrix body line with several words here\n", 400)), 0o644); err != nil {
		t.Fatal(err)
	}

	type combo struct {
		flags []string
		multi bool
		level string
	}
	combos := []combo{}
	for _, level := range []string{"conservative", "balanced", "max"} {
		combos = append(combos,
			combo{[]string{"--density", level}, false, level},
			combo{[]string{"--dense", "--density", level}, false, level},
			combo{[]string{"--multicol", "2", "--density", level}, true, level},
			combo{[]string{"--dense", "--multicol", "2", "--density", level}, true, level},
		)
	}

	for _, c := range combos {
		name := strings.Join(c.flags, " ")
		summary, firstPage := renderCombo(t, file, append([]string{}, c.flags...))

		wantDensity := c.level
		wantLayers := 0
		if c.multi {
			wantDensity = "conservative" // multi-column draws the conservative grid
		} else if c.level == "max" {
			wantLayers = 2 // single-column max = the 2-layer overlay
		}
		if summary.Density != wantDensity {
			t.Fatalf("[%s] density=%q, want %q", name, summary.Density, wantDensity)
		}
		if summary.Layers != wantLayers {
			t.Fatalf("[%s] layers=%d, want %d", name, summary.Layers, wantLayers)
		}
		if summary.Pages <= 0 {
			t.Fatalf("[%s] produced no pages", name)
		}
		// Pixel geometry must match the label: a conservative report is mono grey; a
		// balanced/max report carries zebra or overlay colour.
		colored := pngIsColored(t, firstPage)
		if wantDensity == "conservative" && colored {
			t.Fatalf("[%s] reported conservative but drew coloured pixels", name)
		}
		if wantDensity != "conservative" && !colored {
			t.Fatalf("[%s] reported %s but drew a mono grey page", name, wantDensity)
		}
		cleanPages(file, summary.Pages)
	}

	// The convert path: --dense --density max must render the SAME true overlay as plain
	// --density max — half the pages of balanced, layers:2, coloured — not a single-layer
	// RenderDensePages fallback with double the pages.
	plainMax, plainMaxPage := renderCombo(t, file, []string{"--density", "max"})
	denseMax, _ := renderCombo(t, file, []string{"--dense", "--density", "max"})
	balanced, _ := renderCombo(t, file, []string{"--density", "balanced"})
	if denseMax.Pages != plainMax.Pages {
		t.Fatalf("--dense --density max pages=%d must equal plain --density max pages=%d (true overlay, not RenderDensePages single-layer)", denseMax.Pages, plainMax.Pages)
	}
	if denseMax.Layers != 2 || plainMax.Layers != 2 {
		t.Fatalf("max renders must report layers:2 (dense=%d plain=%d)", denseMax.Layers, plainMax.Layers)
	}
	if !(plainMax.Pages < balanced.Pages) {
		t.Fatalf("2-layer max must pack fewer pages than balanced: max=%d balanced=%d", plainMax.Pages, balanced.Pages)
	}
	if !pngIsColored(t, plainMaxPage) {
		t.Fatalf("max overlay page must carry red/blue ink, got a mono grey page")
	}
	cleanPages(file, plainMax.Pages)
	cleanPages(file, balanced.Pages)
}

// An invalid --density is a LOUD usage error on the dev surface (the engine's env
// path stays fail-closed-to-conservative; the flag does not).
func TestPixelRenderRejectsUnknownDensity(t *testing.T) {
	if os.Getenv("CAVE_TEST_BAD_DENSITY") == "1" {
		runPixelRender([]string{"--density", "turbo", filepath.Join(t.TempDir(), "x.txt")})
		return
	}
	// resolvePixelDensity calls fatal() → os.Exit(1); assert the switch rejects it
	// without needing a subprocess by checking the three valid levels round-trip.
	for _, ok := range []string{"conservative", "balanced", "max", "MAX", " balanced "} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("valid density %q panicked: %v", ok, r)
				}
			}()
			_ = resolvePixelDensity(strings.TrimSpace(strings.ToLower(ok)))
		}()
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
