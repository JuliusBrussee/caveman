// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func decodePNG(t *testing.T, raw []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode PNG: %v", err)
	}
	return img
}

// TestCellAdvanceFourReferenceWidth pins the reference cell-advance-4 geometry:
// width = 2*pad + cols*advance + max(0, glyphW-advance), and a lone glyph is
// drawn at its FULL 5px width at 4px advance (not truncated to 4px) — the glyph
// ink is byte-identical to the 5px-advance render.
func TestCellAdvanceFourReferenceWidth(t *testing.T) {
	adv5 := RenderStyle{}               // CellWBonus 0 → advance 5 (glyph width)
	adv4 := RenderStyle{CellWBonus: -1} // advance 4 → adjacent cells overlap 1px

	// Two columns make the advance difference visible in width.
	img5, err := RenderChunkToPNG("HH", 2, adv5, MaxHeightPx, "")
	if err != nil {
		t.Fatal(err)
	}
	img4, err := RenderChunkToPNG("HH", 2, adv4, MaxHeightPx, "")
	if err != nil {
		t.Fatal(err)
	}
	if img5.Width != 2*PadX+2*AtlasCellW {
		t.Fatalf("advance-5 width = %d, want %d", img5.Width, 2*PadX+2*AtlasCellW)
	}
	wantW4 := 2*PadX + 2*4 + max(0, AtlasCellW-4)
	if img4.Width != wantW4 {
		t.Fatalf("advance-4 width = %d, want %d (reference formula)", img4.Width, wantW4)
	}

	// A lone glyph must be drawn at full width regardless of advance: same cols,
	// same width (the 1px overhang term compensates), byte-identical glyph ink.
	g5, err := RenderChunkToPNG("H", 1, adv5, MaxHeightPx, "")
	if err != nil {
		t.Fatal(err)
	}
	g4, err := RenderChunkToPNG("H", 1, adv4, MaxHeightPx, "")
	if err != nil {
		t.Fatal(err)
	}
	if g5.Width != g4.Width {
		t.Fatalf("lone-glyph widths differ: adv5=%d adv4=%d", g5.Width, g4.Width)
	}
	if !bytes.Equal(g5.PNG, g4.PNG) {
		t.Fatal("lone glyph truncated at advance 4: ink differs from advance 5")
	}
}

// TestPitchSixGeometry pins the reference pitch-6 page geometry: page height is
// 2*pad + (rows-1)*pitch + glyphH (descenders/ascenders both fit), and a
// height-capped page holds 119 rows at pitch 6 on the 728px std canvas.
func TestPitchSixGeometry(t *testing.T) {
	style := RenderStyle{PitchY: 6}
	img, err := RenderChunkToPNG("a\nb\nc", 8, style, MaxHeightPx, "")
	if err != nil {
		t.Fatal(err)
	}
	// 3 rows at pitch 6 with an 8px glyph: 2*4 + 2*6 + 8 = 28.
	if want := 2*PadY + 2*6 + CellH; img.Height != want {
		t.Fatalf("3-row pitch-6 height = %d, want %d", img.Height, want)
	}

	// The std canvas (728px) must hold (728-2*pad-glyphH)/6 + 1 = 119 rows at
	// pitch 6, and the full page must stay within the height cap.
	full, err := RenderChunkToPNG(strings.Repeat("x\n", 300), 8, style, MaxHeightPx, "")
	if err != nil {
		t.Fatal(err)
	}
	wantLines := (MaxHeightPx-2*PadY-CellH)/6 + 1 // 119
	if wantLines != 119 {
		t.Fatalf("pitch-6 lines-per-page derivation = %d, want 119", wantLines)
	}
	if want := 2*PadY + (wantLines-1)*6 + CellH; full.Height != want {
		t.Fatalf("full pitch-6 page height = %d, want %d", full.Height, want)
	}
	if full.Height > MaxHeightPx {
		t.Fatalf("pitch-6 full page %d exceeds height cap %d", full.Height, MaxHeightPx)
	}
}

// TestZebraRowColors verifies each visual row is coloured from the 3-colour
// zebra cycle in order dark-red, dark-blue, black (row % 3).
func TestZebraRowColors(t *testing.T) {
	// Default pitch (8) so rows do not overlap and each band is one pure colour.
	img, err := RenderChunkToPNG("X\nX\nX\nX", 4, RenderStyle{Zebra: true}, MaxHeightPx, "")
	if err != nil {
		t.Fatal(err)
	}
	decoded := decodePNG(t, img.PNG)
	want := []color.RGBA{
		{165, 15, 15, 255}, // row 0: dark red
		{15, 30, 175, 255}, // row 1: dark blue
		{15, 15, 15, 255},  // row 2: black
		{165, 15, 15, 255}, // row 3: cycles back to dark red
	}
	for row := 0; row < 4; row++ {
		got, ok := firstInkColor(decoded, PadY+row*CellH, PadY+row*CellH+CellH)
		if !ok {
			t.Fatalf("row %d: no ink found", row)
		}
		if got != want[row] {
			t.Fatalf("row %d ink = %v, want %v", row, got, want[row])
		}
	}
}

// firstInkColor returns the first non-white pixel colour in rows [y0,y1).
func firstInkColor(img image.Image, y0, y1 int) (color.RGBA, bool) {
	b := img.Bounds()
	for y := y0; y < y1 && y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			r, g, bl, a := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			c := color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(bl >> 8), A: uint8(a >> 8)}
			if !(c.R == 255 && c.G == 255 && c.B == 255) {
				return c, true
			}
		}
	}
	return color.RGBA{}, false
}
