// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

type goldenManifest struct {
	Entries []goldenEntry `json:"entries"`
}

type goldenEntry struct {
	ID      string         `json:"id"`
	Text    string         `json:"text"`
	Mode    string         `json:"mode"`
	Options map[string]any `json:"options"`
	Images  []goldenImage  `json:"images"`
}

type goldenImage struct {
	File          string `json:"file"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	CharsRendered int    `json:"charsRendered"`
	DroppedChars  int    `json:"droppedChars"`
}

func TestGoldenFixtures(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "golden", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest goldenManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Entries) != 18 {
		t.Fatalf("golden entries = %d, want 18", len(manifest.Entries))
	}
	for _, entry := range manifest.Entries {
		t.Run(entry.ID, func(t *testing.T) {
			got, err := renderGoldenEntry(entry)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(entry.Images) {
				t.Fatalf("image count = %d, want %d", len(got), len(entry.Images))
			}
			for i, img := range got {
				wantMeta := entry.Images[i]
				if img.Width != wantMeta.Width || img.Height != wantMeta.Height {
					t.Fatalf("image %d dimensions = %dx%d, want %dx%d", i, img.Width, img.Height, wantMeta.Width, wantMeta.Height)
				}
				if img.CharsRendered != wantMeta.CharsRendered || img.DroppedChars != wantMeta.DroppedChars {
					t.Fatalf("image %d meta chars=%d drops=%d, want chars=%d drops=%d", i, img.CharsRendered, img.DroppedChars, wantMeta.CharsRendered, wantMeta.DroppedChars)
				}
				wantPNG, err := os.ReadFile(filepath.Join("testdata", "golden", wantMeta.File))
				if err != nil {
					t.Fatal(err)
				}
				compareDecodedPNG(t, img.PNG, wantPNG)
			}
		})
	}
}

func renderGoldenEntry(entry goldenEntry) ([]RenderedImage, error) {
	cols := intOption(entry.Options, "cols", DefaultCols)
	switch entry.Mode {
	case "single":
		return RenderTextToPNGs(entry.Text, cols, RenderStyle{})
	case "multicol2":
		return RenderTextToPNGsMultiCol(entry.Text, cols, intOption(entry.Options, "multiCol", 2))
	case "dense":
		return RenderDensePages(entry.Text, DensePagesOptions{
			Cols:   cols,
			Reflow: boolOption(entry.Options, "reflow"),
		})
	case "density":
		// gpt-5.6 pins the standard tier so the golden stays small; the level
		// drives cell advance, pitch and zebra ink through the resolved profile.
		level := DensityConservative
		if strOption(entry.Options, "density") == "balanced" {
			level = DensityBalanced
		}
		return RenderTextToPNGs(entry.Text, cols, ResolveDensity("gpt-5.6", level).renderStyle())
	case "twolayer":
		// A small max-level red/blue overlay page. maxHeightPx is shrunk so a
		// handful of lines already spills into two layers.
		rp := ResolveDensity("gpt-5.6", DensityMax)
		maxH := intOption(entry.Options, "maxHeightPx", MaxHeightPx)
		return RenderTextToTwoLayerPNGs(entry.Text, cols, rp.charBudget(), rp.renderStyle(), maxH)
	default:
		return nil, fmt.Errorf("unknown golden mode %q", entry.Mode)
	}
}

func intOption(opts map[string]any, key string, fallback int) int {
	if v, ok := opts[key]; ok {
		if f, ok := v.(float64); ok {
			return int(f)
		}
	}
	return fallback
}

func strOption(opts map[string]any, key string) string {
	if v, ok := opts[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func boolOption(opts map[string]any, key string) bool {
	if v, ok := opts[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func compareDecodedPNG(t *testing.T, gotPNG, wantPNG []byte) {
	t.Helper()
	got, err := png.Decode(bytes.NewReader(gotPNG))
	if err != nil {
		t.Fatalf("decode got PNG: %v", err)
	}
	want, err := png.Decode(bytes.NewReader(wantPNG))
	if err != nil {
		t.Fatalf("decode want PNG: %v", err)
	}
	if got.Bounds().Dx() != want.Bounds().Dx() || got.Bounds().Dy() != want.Bounds().Dy() {
		t.Fatalf("decoded bounds = %v, want %v", got.Bounds(), want.Bounds())
	}
	var diffs []string
	for y := 0; y < got.Bounds().Dy(); y++ {
		for x := 0; x < got.Bounds().Dx(); x++ {
			g := rgba8(got, x, y)
			w := rgba8(want, x, y)
			if g != w {
				if len(diffs) < 10 {
					diffs = append(diffs, fmt.Sprintf("(%d,%d) got=%v want=%v", x, y, g, w))
				}
			}
		}
	}
	if len(diffs) > 0 {
		t.Fatalf("decoded pixels differ: %v", diffs)
	}
}

func rgba8(img image.Image, x, y int) color.RGBA {
	r, g, b, a := img.At(img.Bounds().Min.X+x, img.Bounds().Min.Y+y).RGBA()
	return color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}
}
