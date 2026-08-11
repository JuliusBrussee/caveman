// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// toolResultHasTwoLayerNote reports whether a tool_result block carries a text
// block teaching the red/blue reading convention.
func toolResultHasTwoLayerNote(tr map[string]any) bool {
	content, ok := tr["content"].([]any)
	if !ok {
		return false
	}
	for _, b := range content {
		bm, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if bm["type"] == "text" && strings.Contains(fmt.Sprint(bm["text"]), "dark-RED") {
			return true
		}
	}
	return false
}

// TestTwoLayerReaderNote pins the max-level reader instruction: two red/blue
// layers, red-before-blue, row order continues red→blue, black = single layer.
func TestTwoLayerReaderNote(t *testing.T) {
	note := twoLayerReaderNote()
	for _, want := range []string{"dark-RED", "dark-BLUE", "RED row", "BLUE row", "black"} {
		if !strings.Contains(note, want) {
			t.Fatalf("two-layer note missing %q: %s", want, note)
		}
	}
	// Keep it terse: at most 3 sentences (plus the black-page clarifier makes 3).
	if got := strings.Count(note, ". "); got > 3 {
		t.Fatalf("two-layer note has %d sentence breaks, want ≤3: %s", got, note)
	}
}

// TestTransformAnthropicMaxSmallSlabBlack asserts the fail-soft path: a max slab
// that fits one layer renders as a single mono-BLACK page (never red, never a
// phantom second layer).
func TestTransformAnthropicMaxSmallSlabBlack(t *testing.T) {
	t.Setenv("CAVE_PIXEL_DENSITY", "max")
	body := mustJSONBytes(t, map[string]any{
		"model":    "gpt-5.6",                           // std tier: one layer ≈ 46k chars
		"system":   strings.Repeat("static slab ", 700), // ~8.4k chars → one layer
		"messages": []any{map[string]any{"role": "user", "content": "go"}},
	})
	out, info, err := TransformAnthropic(body, TransformOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if out == nil || !info.Compressed {
		t.Fatalf("no transform output, info=%+v", info)
	}
	if hasColorPixel(firstDensityImage(t, out)) {
		t.Fatal("small max slab rendered with colour — must be single-layer mono black")
	}
}

// TestTransformAnthropicMaxLargeSlabTwoLayer is the end-to-end check that a max
// slab exceeding one layer renders the red/blue overlay AND that its doubled
// capacity produces fewer images than balanced for the same input.
func TestTransformAnthropicMaxLargeSlabTwoLayer(t *testing.T) {
	big := strings.Repeat("token ", 30000) // ~180k chars → >119 std rows → 2 layers
	makeBody := func() []byte {
		return mustJSONBytes(t, map[string]any{
			"model":    "gpt-5.6",
			"system":   big,
			"messages": []any{map[string]any{"role": "user", "content": "go"}},
		})
	}

	t.Setenv("CAVE_PIXEL_DENSITY", "max")
	outMax, infoMax, err := TransformAnthropic(makeBody(), TransformOptions{})
	if err != nil || outMax == nil || !infoMax.Compressed {
		t.Fatalf("max transform failed: err=%v info=%+v", err, infoMax)
	}
	if !hasColorPixel(firstDensityImage(t, outMax)) {
		t.Fatal("large max slab has no colour — 2-layer overlay not rendered")
	}
	maxImgs := len(collectImageData(userBlocksFromBody(t, outMax)))

	t.Setenv("CAVE_PIXEL_DENSITY", "balanced")
	outBal, infoBal, err := TransformAnthropic(makeBody(), TransformOptions{})
	if err != nil || outBal == nil || !infoBal.Compressed {
		t.Fatalf("balanced transform failed: err=%v info=%+v", err, infoBal)
	}
	balImgs := len(collectImageData(userBlocksFromBody(t, outBal)))

	if !(maxImgs < balImgs) {
		t.Fatalf("max used %d images, balanced %d — 2-layer packing did not reduce image count", maxImgs, balImgs)
	}
}

// TestTransformAnthropicMaxToolResultEmitsNote checks that a 2-layer tool-result image
// never reaches the model without its red/blue note — even when the slab itself
// is a single-layer black page (so the note cannot ride on the slab header).
func TestTransformAnthropicMaxToolResultEmitsNote(t *testing.T) {
	t.Setenv("CAVE_PIXEL_DENSITY", "max")
	big := strings.Repeat("alpha bravo charlie delta echo foxtrot ", 2000) // ~78k chars → >N std rows → 2 layers
	body := mustJSONBytes(t, map[string]any{
		"model":  "gpt-5.6",
		"system": strings.Repeat("static slab ", 300), // small slab → single black page, no slab note
		"messages": []any{map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "big_result", "content": big},
			},
		}},
	})
	out, info, err := TransformAnthropic(body, TransformOptions{CharsPerToken: 2})
	if err != nil {
		t.Fatal(err)
	}
	if out == nil || info.ToolResultImgs == 0 {
		t.Fatalf("tool result was not imaged, info=%+v", info)
	}
	tr := findToolResult(t, userBlocksFromBody(t, out), "big_result")
	if !toolResultHasTwoLayerNote(tr) {
		t.Fatal("2-layer tool result emitted WITHOUT its red/blue reader note")
	}
	// The imaged tool result must actually be a coloured 2-layer overlay.
	imgs := collectImageDataFromAny(tr)
	if len(imgs) == 0 {
		t.Fatal("no image in tool result")
	}
	raw, err := base64.StdEncoding.DecodeString(imgs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !hasColorPixel(decodePNG(t, raw)) {
		t.Fatal("tool-result image is not coloured — 2-layer overlay not rendered")
	}
}

// TestTransformAnthropicMaxSmallToolResultBlackNoNote is the fail-soft companion:
// a tool result that fits one layer renders mono black and gets NO 2-layer note.
func TestTransformAnthropicMaxSmallToolResultBlackNoNote(t *testing.T) {
	t.Setenv("CAVE_PIXEL_DENSITY", "max")
	small := strings.Repeat("alpha bravo charlie delta ", 400) // ~10k chars → <N std rows → single black page
	body := mustJSONBytes(t, map[string]any{
		"model":  "gpt-5.6",
		"system": strings.Repeat("static slab ", 300),
		"messages": []any{map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "small_result", "content": small},
			},
		}},
	})
	out, info, err := TransformAnthropic(body, TransformOptions{CharsPerToken: 2})
	if err != nil {
		t.Fatal(err)
	}
	if out == nil || info.ToolResultImgs == 0 {
		t.Fatalf("tool result was not imaged, info=%+v", info)
	}
	tr := findToolResult(t, userBlocksFromBody(t, out), "small_result")
	if toolResultHasTwoLayerNote(tr) {
		t.Fatal("single-layer tool result got a phantom 2-layer note")
	}
	imgs := collectImageDataFromAny(tr)
	raw, err := base64.StdEncoding.DecodeString(imgs[0])
	if err != nil {
		t.Fatal(err)
	}
	if hasColorPixel(decodePNG(t, raw)) {
		t.Fatal("single-layer tool result is coloured — must be mono black")
	}
}
