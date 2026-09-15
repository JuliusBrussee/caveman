// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"path/filepath"
	"testing"
)

// TestTransformOpenAIDefaultBalancedGeometry checks the standalone OpenAI path
// renders balanced by default for gpt-5.6 (a density-capable reader on the std
// canvas, never hi-res): the first strip carries the cell-advance-4 width and
// coloured zebra ink, and the same request pinned off stays conservative + mono.
func TestTransformOpenAIDefaultBalancedGeometry(t *testing.T) {
	input := readFileTest(t, filepath.Join("testdata/transform_openai", "chat-input.json"))

	// Default env → balanced. Strip width = 2*4 + 152*4 + 1 = 617 (cell-adv 4 on
	// the 152-col GPT strip); conservative is 2*4 + 152*5 = 768.
	out, info, err := TransformOpenAI(input, openAITestOptions("gpt-5.6"))
	if err != nil {
		t.Fatalf("TransformOpenAI: %v", err)
	}
	if info.ImageCount == 0 {
		t.Fatalf("no images rendered, info=%+v", info)
	}
	var got any
	unmarshalTest(t, out, &got)
	uris := collectDataImageStrings(got)
	if len(uris) == 0 {
		t.Fatal("no data-image parts in output")
	}
	first := decodeDataImageTest(t, uris[0])
	if first.Bounds().Dx() != 617 {
		t.Fatalf("balanced strip width = %d, want 617 (cell-adv 4)", first.Bounds().Dx())
	}
	if !hasColorPixel(first) {
		t.Fatal("balanced strip has no coloured ink — zebra not rendered")
	}

	// Pinned off → conservative mono strip at width 768.
	t.Setenv("CAVE_PIXEL_DENSITY", "off")
	outC, _, err := TransformOpenAI(input, openAITestOptions("gpt-5.6"))
	if err != nil {
		t.Fatalf("TransformOpenAI (off): %v", err)
	}
	var gotC any
	unmarshalTest(t, outC, &gotC)
	firstC := decodeDataImageTest(t, collectDataImageStrings(gotC)[0])
	if firstC.Bounds().Dx() != 768 {
		t.Fatalf("conservative strip width = %d, want 768 (cell-adv 5)", firstC.Bounds().Dx())
	}
	if hasColorPixel(firstC) {
		t.Fatal("conservative strip has coloured ink — should be mono")
	}
}
