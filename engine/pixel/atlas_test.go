// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import "testing"

func TestAtlasRank(t *testing.T) {
	if got := AtlasRank('A'); got < 0 {
		t.Fatalf("AtlasRank('A') = %d, want present", got)
	}
	if got := AtlasRank(0x21B5); got < 0 {
		t.Fatalf("AtlasRank('↵') = %d, want present", got)
	}
	if got := AtlasRank(0x0009); got != -1 {
		t.Fatalf("AtlasRank(tab) = %d, want -1", got)
	}
}

func TestAtlasDecodedLengths(t *testing.T) {
	loadAtlas()
	if got, want := len(atlasCodepoints), 35501; got != want {
		t.Fatalf("atlas glyph count = %d, want %d", got, want)
	}
	if got, want := len(atlasGrayCodepoints), 35501; got != want {
		t.Fatalf("gray atlas glyph count = %d, want %d", got, want)
	}
}

func TestAtlasGlyphA(t *testing.T) {
	rank := AtlasRank('A')
	if rank < 0 {
		t.Fatal("A missing from atlas")
	}
	if atlasWideFlags[rank] != 0 {
		t.Fatalf("A wide flag = %d, want 0", atlasWideFlags[rank])
	}
	var ink int
	for y := 0; y < AtlasCellH; y++ {
		for x := 0; x < AtlasCellW; x++ {
			ink += int(atlasBit(rank, y, x))
		}
	}
	if ink == 0 {
		t.Fatal("A glyph is empty")
	}
}

func TestAtlasWideCJK(t *testing.T) {
	rank := AtlasRank('\u4E00')
	if rank < 0 {
		t.Fatal("U+4E00 missing from atlas")
	}
	if atlasWideFlags[rank] != 1 {
		t.Fatalf("U+4E00 wide flag = %d, want 1", atlasWideFlags[rank])
	}
	grayRank := AtlasGrayRank('\u4E00')
	if grayRank < 0 {
		t.Fatal("U+4E00 missing from gray atlas")
	}
	if atlasGrayWideFlags[grayRank] != 1 {
		t.Fatalf("gray U+4E00 wide flag = %d, want 1", atlasGrayWideFlags[grayRank])
	}
	_ = atlasGrayByte(grayRank, 0, 0)
}
