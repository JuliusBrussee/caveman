// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// intendedGlyphs holds the caveman-local glyph surgery applied on top of the
// pxpipe 5x8 atlas (both the 1-bit and grayscale planes carry identical
// 0/255 coverage for these ASCII glyphs). Mirrors the intent of the upstream
// (which the sandbox has no network to fetch byte-for-byte):
//   - backtick and apostrophe are made unambiguous — backtick is a top-left
//     stroke leaning down-right (a grave accent), apostrophe a top-right stroke
//     leaning down-left. Readers were confusing the two.
//   - K gains real diagonal arms so it can no longer read as H (they previously
//     differed by a single pixel).
//
// TestGlyphAtlasSurgery asserts the embedded atlas matches these; run
// TestRegenGlyphAtlas with REGEN_GLYPHS=1 to re-derive the .bin.gz assets.
var intendedGlyphs = map[rune][8]string{
	'`': {
		"#....",
		".#...",
		".....",
		".....",
		".....",
		".....",
		".....",
		".....",
	},
	'\'': {
		"...#.",
		"..#..",
		".#...",
		".....",
		".....",
		".....",
		".....",
		".....",
	},
	'K': {
		".....",
		"#..#.",
		"#.#..",
		"##...",
		"##...",
		"#.#..",
		"#..#.",
		".....",
	},
}

// glyphH is the unchanged H, kept here so the tests can prove K stays distinct
// from it without depending on the embedded bytes it is being compared against.
var glyphH = [8]string{
	".....",
	"#..#.",
	"#..#.",
	"####.",
	"#..#.",
	"#..#.",
	"#..#.",
	".....",
}

func glyphBits(cp rune) [8]string {
	rank := AtlasRank(cp)
	var out [8]string
	for row := 0; row < AtlasCellH; row++ {
		line := make([]byte, AtlasCellW)
		for col := 0; col < AtlasCellW; col++ {
			if atlasBit(rank, row, col) == 1 {
				line[col] = '#'
			} else {
				line[col] = '.'
			}
		}
		out[row] = string(line)
	}
	return out
}

func symDiff(a, b [8]string) int {
	n := 0
	for row := 0; row < AtlasCellH; row++ {
		for col := 0; col < AtlasCellW; col++ {
			if (a[row][col] == '#') != (b[row][col] == '#') {
				n++
			}
		}
	}
	return n
}

func TestGlyphAtlasSurgery(t *testing.T) {
	// Each edited glyph must match the intended bitmap in BOTH atlas planes.
	for cp, want := range intendedGlyphs {
		rank := AtlasRank(cp)
		grank := AtlasGrayRank(cp)
		if rank < 0 || grank < 0 {
			t.Fatalf("%q missing from atlas (rank=%d gray=%d)", string(cp), rank, grank)
		}
		for row := 0; row < AtlasCellH; row++ {
			for col := 0; col < AtlasCellW; col++ {
				on := want[row][col] == '#'
				if (atlasBit(rank, row, col) == 1) != on {
					t.Fatalf("%q 1-bit (%d,%d) = %d, want %v", string(cp), row, col, atlasBit(rank, row, col), on)
				}
				gray := atlasGrayByte(grank, row, col)
				if on && gray != 255 {
					t.Fatalf("%q gray (%d,%d) = %d, want 255", string(cp), row, col, gray)
				}
				if !on && gray != 0 {
					t.Fatalf("%q gray (%d,%d) = %d, want 0", string(cp), row, col, gray)
				}
			}
		}
	}

	// H must be untouched, so K vs H distinctness is measured against the real H.
	if got := glyphBits('H'); got != glyphH {
		t.Fatalf("H changed unexpectedly: %v", got)
	}

	// Distinctness: the whole point of the surgery.
	if d := symDiff(intendedGlyphs['`'], intendedGlyphs['\'']); d < 4 {
		t.Fatalf("backtick vs apostrophe differ by only %d pixels", d)
	}
	if d := symDiff(intendedGlyphs['K'], glyphH); d < 4 {
		t.Fatalf("K vs H differ by only %d pixels", d)
	}
	// K must carry diagonal ink in the interior columns (1..3) that H lacks —
	// that is what makes it read as a K and not an H.
	interiorK := 0
	for row := 1; row < 7; row++ {
		for col := 1; col < 4; col++ {
			if intendedGlyphs['K'][row][col] == '#' && glyphH[row][col] != '#' {
				interiorK++
			}
		}
	}
	if interiorK < 2 {
		t.Fatalf("K has only %d diagonal pixels absent in H", interiorK)
	}
}

// TestRegenGlyphAtlas rewrites atlas-pixels.bin.gz and atlasgray-pixels.bin.gz
// with intendedGlyphs applied. Glyph dimensions are unchanged, so the offset,
// codepoint and wide-flag tables stay valid — only the two pixel planes move.
func TestRegenGlyphAtlas(t *testing.T) {
	if os.Getenv("REGEN_GLYPHS") == "" {
		t.Skip("set REGEN_GLYPHS=1 to regenerate the glyph atlas")
	}
	loadAtlas()
	pix := append([]uint8(nil), atlasPixels...)
	gpix := append([]uint8(nil), atlasGrayPixels...)

	for cp, want := range intendedGlyphs {
		rank := AtlasRank(cp)
		grank := AtlasGrayRank(cp)
		if atlasWideFlags[rank] != 0 || atlasGrayWideFlags[grank] != 0 {
			t.Fatalf("%q is wide; surgery assumes 5x8", string(cp))
		}
		bitBase := int(atlasOffsets[rank])
		grayBase := int(atlasGrayOffsets[grank])
		for row := 0; row < AtlasCellH; row++ {
			for col := 0; col < AtlasCellW; col++ {
				on := want[row][col] == '#'
				bitIdx := bitBase + row*AtlasCellW + col
				mask := byte(1) << (7 - (bitIdx & 7))
				if on {
					pix[bitIdx>>3] |= mask
				} else {
					pix[bitIdx>>3] &^= mask
				}
				gidx := grayBase + row*AtlasGrayCellW + col
				if on {
					gpix[gidx] = 255
				} else {
					gpix[gidx] = 0
				}
			}
		}
	}

	writeGzip(t, filepath.Join("assets", "atlas-pixels.bin.gz"), pix)
	writeGzip(t, filepath.Join("assets", "atlasgray-pixels.bin.gz"), gpix)
}

func writeGzip(t *testing.T, path string, raw []byte) {
	t.Helper()
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}
