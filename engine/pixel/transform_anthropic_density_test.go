// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"encoding/base64"
	"image"
	"strings"
	"testing"
)

// TestZebraReaderNote pins the one-line reader instruction: emitted only for
// zebra (balanced/max) pages, silent for conservative.
func TestZebraReaderNote(t *testing.T) {
	if got := zebraReaderNote(false); got != "" {
		t.Fatalf("mono note = %q, want empty", got)
	}
	got := zebraReaderNote(true)
	if got == "" || !strings.Contains(got, "dark-red/dark-blue/black") {
		t.Fatalf("zebra note missing colour explanation: %q", got)
	}
}

// TestTransformAnthropicHiResBalancedGeometry is the end-to-end check that a
// hi-res reader (Fable 5) renders REAL hi-res pages by default: the slab page
// fills the 2576px-class hi-res canvas (92-patch width = 2573px) with coloured
// zebra ink, and the same request pinned off stays hi-res but mono (conservative
// geometry on the hi-res canvas).
func TestTransformAnthropicHiResBalancedGeometry(t *testing.T) {
	body := mustJSONBytes(t, map[string]any{
		"model":    "claude-fable-5",
		"system":   strings.Repeat("static slab ", 12000),
		"messages": []any{map[string]any{"role": "user", "content": "go"}},
	})

	// Default env → hi-res balanced. Full-width page: cell-adv 4 fills the hi-res
	// canvas → 2*4 + 641*4 + 1 = 2573px (92 patches wide, ≤ 2576 tier long edge).
	out, info, err := TransformAnthropic(body, TransformOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if out == nil || !info.Compressed {
		t.Fatalf("no transform output, info=%+v", info)
	}
	img := firstDensityImage(t, out)
	if img.Bounds().Dx() != 2573 {
		t.Fatalf("hi-res balanced page width = %d, want 2573 (fills hi-res canvas)", img.Bounds().Dx())
	}
	if img.Bounds().Dy() > HiResMaxHeightPx {
		t.Fatalf("hi-res page height %d exceeds canvas %d", img.Bounds().Dy(), HiResMaxHeightPx)
	}
	if !hasColorPixel(img) {
		t.Fatal("hi-res balanced page has no coloured ink — zebra not rendered")
	}
	// The zebra reader instruction is rendered into this page's header (it gates
	// on the same rp.Zebra as the colour ink); its text is pinned by
	// TestZebraReaderNote.

	// Pinned off → hi-res CONSERVATIVE (cell-adv 5 also fills the canvas → 2*4 +
	// 513*5 = 2573px), mono ink.
	t.Setenv("CAVE_PIXEL_DENSITY", "off")
	outC, _, err := TransformAnthropic(body, TransformOptions{})
	if err != nil {
		t.Fatal(err)
	}
	imgC := firstDensityImage(t, outC)
	if imgC.Bounds().Dx() != 2573 {
		t.Fatalf("hi-res conservative page width = %d, want 2573", imgC.Bounds().Dx())
	}
	if hasColorPixel(imgC) {
		t.Fatal("hi-res conservative page has coloured ink — should be mono")
	}
}

func firstDensityImage(t *testing.T, out []byte) image.Image {
	t.Helper()
	datas := collectImageData(userBlocksFromBody(t, out))
	if len(datas) == 0 {
		t.Fatal("no image blocks in transform output")
	}
	raw, err := base64.StdEncoding.DecodeString(datas[0])
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	return decodePNG(t, raw)
}

func hasColorPixel(img image.Image) bool {
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
