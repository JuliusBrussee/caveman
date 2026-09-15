// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"image/color"
	"math"
	"strings"
	"testing"
)

// maxOverlayStyle is the resolved max geometry (cell advance 4, pitch 6) that the
// overlay renderer draws at.
var maxOverlayStyle = RenderStyle{AA: true, PitchY: 6, CellWBonus: -1}

// TestCompositeTwoLayersReferenceNumbers pins the subtractive multiply against
// hand-computed pixel values, including the canonical full red+blue overlap from
// pxcore.py overlay(): (215,15,15)·(15,15,215)/255 → (13,1,13).
func TestCompositeTwoLayersReferenceNumbers(t *testing.T) {
	red := overlayLayerColors[0]  // (215,15,15)
	blue := overlayLayerColors[1] // (15,15,215)
	cases := []struct {
		name   string
		c1, c2 uint8
		want   [3]uint8
	}{
		{"full-overlap-goes-dark", 255, 255, [3]uint8{13, 1, 13}},
		{"white-stays-white", 0, 0, [3]uint8{255, 255, 255}},
		{"red-only", 255, 0, [3]uint8{215, 15, 15}},
		{"blue-only", 0, 255, [3]uint8{15, 15, 215}},
		// Half red coverage over full blue (a1=128/255, a2=1):
		//   R: l1=255-40·a1=234.92, l2=15 → 234.92·15/255 = 13.82 → 14
		//   G: l1=255-240·a1=134.53, l2=15 → 134.53·15/255 =  7.91 →  8
		//   B: l1=134.53, l2=215 → 134.53·215/255 = 113.42 → 113
		{"half-red-over-full-blue", 128, 255, [3]uint8{14, 8, 113}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := compositeTwoLayers([]uint8{tc.c1}, []uint8{tc.c2}, red, blue)
			if [3]uint8{got[0], got[1], got[2]} != tc.want {
				t.Fatalf("cov(%d,%d) = %v, want %v", tc.c1, tc.c2, got[:3], tc.want)
			}
		})
	}
}

// composePixelRef reproduces the reference composite for one pixel — used to
// verify the rendered overlap against the formula independently of encoding.
func composePixelRef(c1, c2 uint8, col1, col2 [3]uint8) color.RGBA {
	a1 := float64(c1) / 255
	a2 := float64(c2) / 255
	var out [3]uint8
	for ch := 0; ch < 3; ch++ {
		l1 := 255*(1-a1) + float64(col1[ch])*a1
		l2 := 255*(1-a2) + float64(col2[ch])*a2
		v := math.Round(l1 * l2 / 255)
		if v < 0 {
			v = 0
		} else if v > 255 {
			v = 255
		}
		out[ch] = uint8(v)
	}
	return color.RGBA{R: out[0], G: out[1], B: out[2], A: 255}
}

// TestTwoLayerOverlapMatchesReferenceComposite renders the SAME glyph in both
// layers at the same cell (so red and blue coverage are identical at every
// pixel) and checks each decoded pixel equals the subtractive multiply computed
// from the gray atlas coverage.
func TestTwoLayerOverlapMatchesReferenceComposite(t *testing.T) {
	img, err := renderTwoLayerChunk([]string{"M"}, []string{"M"}, 1, maxOverlayStyle, MaxHeightPx)
	if err != nil {
		t.Fatal(err)
	}
	if img.Layers != 2 {
		t.Fatalf("Layers = %d, want 2", img.Layers)
	}
	decoded := decodePNG(t, img.PNG)
	rank := AtlasGrayRank('M')
	if rank < 0 {
		t.Fatal("no gray glyph for 'M'")
	}
	red := overlayLayerColors[0]
	blue := overlayLayerColors[1]
	inked := false
	for row := 0; row < AtlasGrayCellH; row++ {
		for col := 0; col < AtlasGrayCellW; col++ {
			cov := atlasGrayByte(rank, row, col)
			want := composePixelRef(cov, cov, red, blue)
			got := rgba8(decoded, PadX+col, PadY+row)
			if got != want {
				t.Fatalf("pixel (%d,%d) cov=%d got=%v want=%v", col, row, cov, got, want)
			}
			if cov > 0 {
				inked = true
			}
		}
	}
	if !inked {
		t.Fatal("no inked pixel exercised — glyph render produced no coverage")
	}
	// A padding pixel far from any glyph must stay pure white.
	if got := rgba8(decoded, 0, 0); got != (color.RGBA{255, 255, 255, 255}) {
		t.Fatalf("corner pixel = %v, want white", got)
	}
}

// TestTwoLayerSplitter covers page-by-page halving across layers: exact 2N,
// odd (shorter blue), single-line (black not red), empty, and spill into a
// second image whose tail is a single-layer black page.
func TestTwoLayerSplitter(t *testing.T) {
	// maxHeightPx 28 → linesPerLayer = (28-8-8)/6 + 1 = 3; each image holds ≤6.
	const maxH = 28
	line := "aaa"
	body := func(n int) string {
		s := ""
		for i := 0; i < n; i++ {
			if i > 0 {
				s += "\n"
			}
			s += line
		}
		return s
	}
	render := func(n int) []RenderedImage {
		imgs, err := RenderTextToTwoLayerPNGs(body(n), 3, 1<<20, maxOverlayStyle, maxH)
		if err != nil {
			t.Fatalf("render %d lines: %v", n, err)
		}
		return imgs
	}

	// Exact 2N (6 lines): one 2-layer image, red 3 + blue 3, chars count BOTH
	// layers = runeLen("aaa"×6 joined) = 6*3 + 5 = 23.
	if imgs := render(6); len(imgs) != 1 || imgs[0].Layers != 2 {
		t.Fatalf("6 lines: got %d imgs layers=%v, want 1 image Layers 2", len(imgs), layersOf(imgs))
	} else if imgs[0].CharsRendered != 23 {
		t.Fatalf("6-line CharsRendered = %d, want 23 (both layers)", imgs[0].CharsRendered)
	}

	// Odd (5 lines): one 2-layer image, red 3 + blue 2 (shorter blue). Chars =
	// runeLen of all 5 lines = 5*3 + 4 = 19 (proves the blue layer is counted).
	if imgs := render(5); len(imgs) != 1 || imgs[0].Layers != 2 {
		t.Fatalf("5 lines: got %d imgs layers=%v, want 1 image Layers 2", len(imgs), layersOf(imgs))
	} else if imgs[0].CharsRendered != 19 {
		t.Fatalf("5-line CharsRendered = %d, want 19 (both layers)", imgs[0].CharsRendered)
	}

	// Single line (blue empty): one single-layer image, mono BLACK ink, not red.
	if imgs := render(1); len(imgs) != 1 || imgs[0].Layers != 1 {
		t.Fatalf("1 line: got %d imgs layers=%v, want 1 image Layers 1", len(imgs), layersOf(imgs))
	} else if hasColorPixel(decodePNG(t, imgs[0].PNG)) {
		t.Fatal("single-line max page has coloured ink — must render mono black, not red")
	}

	// Empty text: must not panic, one (blank, single-layer) image.
	if imgs, err := RenderTextToTwoLayerPNGs("", 3, 1<<20, maxOverlayStyle, maxH); err != nil {
		t.Fatalf("empty text: %v", err)
	} else if len(imgs) != 1 {
		t.Fatalf("empty text produced %d images, want 1", len(imgs))
	}

	// Spill (7 lines): image 0 full 2-layer (6 lines), image 1 the single-line
	// tail as a mono-black single-layer page.
	imgs := render(7)
	if len(imgs) != 2 {
		t.Fatalf("7 lines: %d images, want 2", len(imgs))
	}
	if imgs[0].Layers != 2 || imgs[1].Layers != 1 {
		t.Fatalf("7-line layers = %v, want [2 1]", layersOf(imgs))
	}
	if hasColorPixel(decodePNG(t, imgs[1].PNG)) {
		t.Fatal("7-line tail page is coloured — single-layer tail must be mono black")
	}
}

func layersOf(imgs []RenderedImage) []int {
	out := make([]int, len(imgs))
	for i, img := range imgs {
		out[i] = img.Layers
	}
	return out
}

// TestTwoLayerGateParityAndCapacity asserts the two invariants of the max gate:
// a max image costs the SAME tokens as a balanced page (identical pixels) while
// holding double the characters, so for equal text volume max is cheaper.
func TestTwoLayerGateParityAndCapacity(t *testing.T) {
	bal := ResolveDensity("gpt-5.6", DensityBalanced) // std, 1 layer
	mx := ResolveDensity("gpt-5.6", DensityMax)       // std, 2 layers, same geometry

	// Capacity doubling: a max image holds two layers of text.
	if mx.charBudget() != 2*bal.charBudget() {
		t.Fatalf("max charBudget = %d, want 2× balanced = %d", mx.charBudget(), 2*bal.charBudget())
	}

	cols := bal.pageCols()
	rows := bal.pageRows()
	if cols != mx.pageCols() || rows != mx.pageRows() {
		t.Fatalf("balanced/max geometry diverged: bal %dx%d vs max %dx%d", cols, rows, mx.pageCols(), mx.pageRows())
	}

	// Gate parity: one full max image (2×rows lines) prices identically to one
	// full balanced page (rows lines) — same pixels, same tokens.
	balPage := imageTokensForRows(rows, cols, 1, 0, bal.charBudget(), bal)
	maxImage := imageTokensForRows(2*rows, cols, 1, 0, mx.charBudget(), mx)
	if maxImage != balPage {
		t.Fatalf("max full-image cost %v != balanced full-page cost %v (parity broken)", maxImage, balPage)
	}

	// For the SAME row volume, max needs fewer/cheaper images (the capacity win).
	volume := 3 * rows
	if maxTok, balTok := imageTokensForRows(volume, cols, 1, 0, mx.charBudget(), mx), imageTokensForRows(volume, cols, 1, 0, bal.charBudget(), bal); !(maxTok < balTok) {
		t.Fatalf("max tokens %v not cheaper than balanced %v for %d rows", maxTok, balTok, volume)
	}
}

// TestTwoLayerCapacitySeam pins that the layer-aware capacity flows through the
// SAME seam into EstimateImageCount and TruncateForBudget: a tool result sized
// just under 2×N lines fits ONE max image with zero omitted chars, while the
// identical content needs two balanced images (and truncates at maxImages=1).
func TestTwoLayerCapacitySeam(t *testing.T) {
	bal := ResolveDensity("gpt-5.6", DensityBalanced) // std, 1 layer, N rows/image
	mx := ResolveDensity("gpt-5.6", DensityMax)       // std, 2 layers, 2N rows/image
	cols := bal.pageCols()
	N := bal.pageRows()

	if got := bal.imageLineCapacity(cols, 1, bal.charBudget()); got != N {
		t.Fatalf("balanced imageLineCapacity = %d, want %d", got, N)
	}
	if got := mx.imageLineCapacity(cols, 1, mx.charBudget()); got != 2*N {
		t.Fatalf("max imageLineCapacity = %d, want %d (2×N)", got, 2*N)
	}

	// (2N-1) full-width lines: one visual row each.
	lines := make([]string, 2*N-1)
	for i := range lines {
		lines[i] = strings.Repeat("x", cols)
	}
	text := strings.Join(lines, "\n")

	if got := EstimateImageCount(text, cols, 1, mx.charBudget(), mx); got != 1 {
		t.Fatalf("max EstimateImageCount(2N-1 lines) = %d, want 1", got)
	}
	if got := EstimateImageCount(text, cols, 1, bal.charBudget(), bal); got != 2 {
		t.Fatalf("balanced EstimateImageCount(2N-1 lines) = %d, want 2", got)
	}

	// maxImages=1: max keeps the whole payload; balanced must truncate.
	if out, omitted, trunc := TruncateForBudget(text, 1, cols, 1, mx.charBudget(), mx); trunc || omitted != 0 || out != text {
		t.Fatalf("max truncated a one-image payload: trunc=%v omitted=%d", trunc, omitted)
	}
	if _, omitted, trunc := TruncateForBudget(text, 1, cols, 1, bal.charBudget(), bal); !trunc || omitted <= 0 {
		t.Fatalf("balanced did not truncate a two-image payload at maxImages=1: trunc=%v omitted=%d", trunc, omitted)
	}
}
