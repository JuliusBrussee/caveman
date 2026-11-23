package gateway

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/pixel"
)

// A text part can carry cache breakpoints or provider metadata. Replacing the
// whole part must not silently discard fields the renderer does not understand.
func TestPixelPreservesOpaqueContentPartMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, prefix, suffix, field string
		family                      pixelFamily
		collect                     func([]byte, *pixelDecider)
	}{
		{"openai responses breakpoint", `{"input":[{"role":"user","content":[`, `]}]}`, `"prompt_cache_breakpoint":{"mode":"explicit","ttl":"12h"}`, openAIResponsesPixel, collectOpenAIPixelLiveZone},
		{"openai chat metadata", `{"messages":[{"role":"user","content":[`, `]}]}`, `"provider_metadata":{"signed":"opaque"}`, openAIChatPixel, collectOpenAIPixelLiveZone},
		{"anthropic citations", `{"messages":[{"role":"user","content":[`, `]}]}`, `"citations":[{"type":"char_location","start_char_index":0,"end_char_index":5}]`, anthropicPixel, collectAnthropicPixelLiveZone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := `"a text block"`
			partType := "text"
			if tc.family.scope == openAIResponsesPixel.scope {
				partType = "input_text"
			}
			part := []byte(`{"type":"` + partType + `","text":` + original + `,` + tc.field + `}`)
			body := []byte(tc.prefix + string(part) + tc.suffix)
			cache := newTestPrefixCache()
			if _, err := cache.RememberReplacement(tc.family.scope+"gpt-5.6", []byte(original), []byte(`{"type":"input_image","image_url":"data:image/png;base64,AA=="}`), PixelHandle); err != nil {
				t.Fatal(err)
			}
			d := &pixelDecider{s: New(Config{PrefixCache: cache}), opts: pixel.DefaultTransformOptions("gpt-5.6")}
			tc.collect(body, d)
			out, _, err := applyPixelReplacements(body, d.reps)
			if err != nil {
				t.Fatal(err)
			}
			if len(out) == 0 {
				out = body
			}
			if !json.Valid(out) || !bytes.Contains(out, part) {
				t.Fatalf("pixel transformation discarded opaque part metadata: %s", out)
			}
		})
	}
}
