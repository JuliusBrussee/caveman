// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.
//
// The 2-layer red/blue overlay renderer for the `max` density level. Two full
// pages of text are drawn into ONE image as red and blue ink layers and
// subtractively composited, porting the overlay operation from pxcore.py.

package pixel

import (
	"math"
	"strings"
)

// overlayLayerColors are the two ink colours for a max-level overlay page, taken
// verbatim from pxcore.py overlay(): layer 1 dark red, layer 2 dark blue, both
// drawn in place (dx=dy=0, configured geometry). White stays white; where red and
// blue ink overlap the subtractive multiply drives the pixel dark.
var overlayLayerColors = [2][3]uint8{
	{215, 15, 15}, // layer 1 (red)
	{15, 15, 215}, // layer 2 (blue)
}

// compositeTwoLayers is the subtractive multiply from pxcore.py overlay(). Each
// layer is inked over white using its coverage alpha (region*(1-a)+color*a),
// then the layers multiply: out = (L1/255)·(L2/255)·255, rounded once at the
// end. cov1/cov2 hold per-pixel ink coverage (0..255) for the red/blue layers.
func compositeTwoLayers(cov1, cov2 []uint8, c1, c2 [3]uint8) []uint8 {
	n := len(cov1)
	rgb := make([]uint8, n*3)
	for i := 0; i < n; i++ {
		a1 := float64(cov1[i]) / 255
		a2 := float64(cov2[i]) / 255
		for ch := 0; ch < 3; ch++ {
			l1 := 255*(1-a1) + float64(c1[ch])*a1
			l2 := 255*(1-a2) + float64(c2[ch])*a2
			v := math.Round(l1 * l2 / 255)
			if v < 0 {
				v = 0
			} else if v > 255 {
				v = 255
			}
			rgb[i*3+ch] = uint8(v)
		}
	}
	return rgb
}

// blitLayerCoverage stamps one glyph's grayscale coverage (max-blend) into a
// coverage framebuffer. It mirrors blitGlyphGray but is bounds-guarded on both
// axes so a stray wide glyph at the page edge can never panic the request path.
func blitLayerCoverage(cov []uint8, fbW, fbH, x, y int, cp rune) int {
	rank := AtlasGrayRank(cp)
	if rank < 0 {
		return 0
	}
	wide := atlasGrayWideFlags[rank] == 1
	srcW := AtlasGrayCellW
	if wide {
		srcW = 2 * AtlasGrayCellW
	}
	srcOff := int(atlasGrayOffsets[rank])
	for gy := 0; gy < AtlasGrayCellH; gy++ {
		py := y + gy
		if py < 0 || py >= fbH {
			continue
		}
		srcRow := srcOff + gy*srcW
		for gx := 0; gx < srcW; gx++ {
			px := x + gx
			if px < 0 || px >= fbW {
				continue
			}
			coverage := atlasGrayPixels[srcRow+gx]
			if coverage > 0 {
				idx := py*fbW + px
				if coverage > cov[idx] {
					cov[idx] = coverage
				}
			}
		}
	}
	if wide {
		return 2
	}
	return 1
}

// renderLayerCoverage rasterises one layer's wrapped lines into a coverage
// framebuffer of the shared geometry. Advance-based columns match WrapLines and
// the single-layer renderer; dropped glyphs are counted, not drawn.
func renderLayerCoverage(lines []string, width, height, cols, cellW, pitch int) ([]uint8, int, map[rune]int) {
	cov := make([]uint8, width*height)
	dropped := make(map[rune]int)
	droppedChars := 0
	for row, line := range lines {
		baseY := PadY + row*pitch
		col := 0
		for _, r := range line {
			if col >= cols {
				break
			}
			baseX := PadX + col*cellW
			advance := blitLayerCoverage(cov, width, height, baseX, baseY, r)
			if advance == 0 {
				droppedChars++
				dropped[r]++
				col++
			} else {
				col += advance
			}
		}
	}
	return cov, droppedChars, dropped
}

// renderTwoLayerChunk draws one overlay page: redLines in the red layer and
// blueLines in the blue layer, sharing identical geometry. When blueLines is
// empty the page holds a single layer, so it renders exactly like a 1-layer page
// in mono BLACK ink (never red) and carries no 2-layer reader burden. Both
// layers' rendered/dropped chars are counted.
func renderTwoLayerChunk(redLines, blueLines []string, cols int, style RenderStyle, maxHeightPx int) (RenderedImage, error) {
	if cols < 1 {
		cols = 1
	}
	if maxHeightPx == 0 {
		maxHeightPx = MaxHeightPx
	}
	if len(blueLines) == 0 {
		// Single-layer fallback: a page with only a red layer is drawn as an
		// ordinary mono-black page (not red) so no red/blue instruction is owed.
		mono := RenderStyle{
			AA:         style.AA,
			CellWBonus: style.CellWBonus,
			CellHBonus: style.CellHBonus,
			PitchY:     style.PitchY,
		}
		img, err := RenderChunkToPNG(strings.Join(redLines, "\n"), cols, mono, maxHeightPx, "")
		if err != nil {
			return RenderedImage{}, err
		}
		img.Layers = 1
		return img, nil
	}

	atlasW := AtlasGrayCellW
	glyphH := AtlasGrayCellH
	pitch := glyphH + max(0, style.CellHBonus+defaultCellHBonus)
	if style.PitchY > 0 {
		pitch = style.PitchY
	}
	cellW := max(1, atlasW+style.CellWBonus+defaultCellWBonus)
	nMax := max(len(redLines), len(blueLines))
	width := 2*PadX + cols*cellW + max(0, atlasW-cellW)
	height := 2 * PadY
	if nMax > 0 {
		height += (nMax-1)*pitch + glyphH
	}

	cov1, drop1, dc1 := renderLayerCoverage(redLines, width, height, cols, cellW, pitch)
	cov2, drop2, dc2 := renderLayerCoverage(blueLines, width, height, cols, cellW, pitch)
	pngBytes, err := encodeRGBPNG(compositeTwoLayers(cov1, cov2, overlayLayerColors[0], overlayLayerColors[1]), width, height)
	if err != nil {
		return RenderedImage{}, err
	}

	// Reading order red-then-blue is the page's contiguous text, so its char
	// count equals runeLen(join(red,\n) + "\n" + join(blue,\n)) — both layers.
	charsRendered := runeLen(strings.Join(redLines, "\n"))
	if len(blueLines) > 0 {
		charsRendered += 1 + runeLen(strings.Join(blueLines, "\n"))
	}
	mergeDropped(dc1, dc2)
	return RenderedImage{
		PNG:               pngBytes,
		Width:             width,
		Height:            height,
		CharsRendered:     charsRendered,
		DroppedChars:      drop1 + drop2,
		DroppedCodepoints: dc1,
		Layers:            2,
	}, nil
}

// RenderTextToTwoLayerPNGs splits wrapped text into max-level overlay pages. Each
// image holds up to 2×N lines: the first N in the red layer, the next N in the
// blue layer, where N is the per-layer row capacity of the canvas. Reading order
// is red top-to-bottom, then blue top-to-bottom, then the next image. A trailing
// page with ≤N lines renders single-layer black (see renderTwoLayerChunk).
func RenderTextToTwoLayerPNGs(text string, cols, maxCharsPerImage int, style RenderStyle, maxHeightPx int) ([]RenderedImage, error) {
	if cols < 1 {
		cols = DefaultCols
	}
	if maxHeightPx == 0 {
		maxHeightPx = MaxHeightPx
	}
	if maxCharsPerImage <= 0 {
		maxCharsPerImage = ReadableCharsPerImage
	}
	markerScale := max(1, style.MarkerScale)
	glyphH := AtlasGrayCellH
	pitch := glyphH + max(0, style.CellHBonus+defaultCellHBonus)
	if style.PitchY > 0 {
		pitch = style.PitchY
	}
	lines := WrapLines(text, cols, markerScale)
	// Per-layer rows that fit the canvas; each image stacks two such layers.
	linesPerLayer := max(1, (maxHeightPx-2*PadY-glyphH)/pitch+1)
	linesPerImage := 2 * linesPerLayer
	images := make([]RenderedImage, 0)
	for _, page := range splitWrappedLinesIntoReadablePages(lines, linesPerImage, maxCharsPerImage) {
		n := min(len(page), linesPerLayer)
		img, err := renderTwoLayerChunk(page[:n], page[n:], cols, style, maxHeightPx)
		if err != nil {
			return nil, err
		}
		images = append(images, img)
	}
	return images, nil
}
