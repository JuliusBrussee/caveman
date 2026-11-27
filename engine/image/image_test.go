package image_test

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"testing"

	cvimage "github.com/JuliusBrussee/caveman/engine/image"
)

// genPNGWithDimensions returns a valid PNG whose IHDR declares w×h without
// materializing w*h pixels in memory, so decode-bomb tests exercise the real
// header parser (image.DecodeConfig) without spending seconds compressing a
// multi-hundred-megapixel image.
func genPNGWithDimensions(t *testing.T, w, h uint32) []byte {
	t.Helper()
	b := append([]byte(nil), genPNG(t, 1, 1)...)
	// PNG layout: 8-byte signature, 4-byte length (13), 4-byte "IHDR",
	// 4-byte width, 4-byte height, 5-byte IHDR tail, 4-byte CRC32.
	binary.BigEndian.PutUint32(b[16:20], w)
	binary.BigEndian.PutUint32(b[20:24], h)
	crc := crc32.ChecksumIEEE(b[12:29])
	binary.BigEndian.PutUint32(b[29:33], crc)
	return b
}

func genPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{uint8(x % 256), uint8(y % 256), uint8((x + y) % 256), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestAnalyze(t *testing.T) {
	t.Parallel()
	p, ok := cvimage.Analyze(genPNG(t, 800, 600))
	if !ok || p.Width != 800 || p.Height != 600 || p.Format != "png" {
		t.Fatalf("Analyze = %+v ok=%v", p, ok)
	}
	if _, ok := cvimage.Analyze([]byte("not an image")); ok {
		t.Error("non-image must report ok=false")
	}
}

func TestEstimateTokensRequiresModelAndEffectiveDetail(t *testing.T) {
	t.Parallel()
	if got := cvimage.EstimateTokens("openai", 1024, 1024); got != 0 {
		t.Fatalf("legacy provider-only estimate = %d, want honest zero", got)
	}
	cases := []struct {
		provider, model, detail string
		want                    int
		ok                      bool
	}{
		{"openai", "gpt-4o", "high", 765, true},
		{"openai", "gpt-4o-2024-08-06", "high", 765, true},
		{"openai", "gpt-4o-20garbage", "high", 0, false},
		{"openai", "gpt-4o-mini", "low", 2833, true},
		{"openai", "gpt-5.6", "original", 1024, true},
		{"openai", "gpt-5.6", "auto", 1024, true},
		{"openai", "gpt-5.6", "default", 1024, true},
		{"openai", "gpt-5.6", "low", 256, true},
		{"anthropic", "claude-sonnet-4-6", "default", 1369, true},
		{"anthropic", "claude-opus-4-20250514", "default", 1369, true},
		{"anthropic", "claude-opus-4-8-anything", "default", 0, false},
		{"anthropic", "claude-sonnet-4-9", "default", 0, false},
		{"anthropic", "claude-opus-99-1", "default", 0, false},
		{"anthropic", "claude-fable-5", "default", 1369, true},
		{"google", "gemini-3.6-flash", "default", 1120, true},
		{"gemini", "gemini-3.6-flash", "low", 280, true},
		{"google", "gemini-2.5-pro", "low", 64, true},
		{"google", "gemini-2.5-pro", "default", 0, false},
		{"google", "gemini-3-audio", "default", 0, false},
		{"google", "gemini-3.6-flash-anything", "default", 0, false},
		{"openai", "gpt-4.1-mini", "high", 0, false},
		{"openai", "unknown", "high", 0, false},
		{"unknown", "anything", "default", 0, false},
	}
	for _, c := range cases {
		got, ok := cvimage.EstimateTokensForModel(c.provider, c.model, c.detail, 1024, 1024)
		if got != c.want || ok != c.ok {
			t.Errorf("EstimateTokensForModel(%q,%q,%q) = (%d,%v), want (%d,%v)", c.provider, c.model, c.detail, got, ok, c.want, c.ok)
		}
	}
	if got, ok := cvimage.EstimateTokensForModel("google", "gemini-3.6-flash", "default", 100_000_001, 1); ok || got != 0 {
		t.Fatalf("oversized estimate = (%d,%v), want unavailable", got, ok)
	}
	if got, ok := cvimage.EstimateTokensForModel("anthropic", "claude-sonnet-4-6", "default", 1920, 1080); !ok || got != 1560 {
		t.Fatalf("Claude standard 1920x1080 = (%d,%v), want (1560,true)", got, ok)
	}
	if got, ok := cvimage.EstimateTokensForModel("anthropic", "claude-opus-4-7", "default", 1920, 1080); !ok || got != 2691 {
		t.Fatalf("Claude Opus 4.7 high-res 1920x1080 = (%d,%v), want (2691,true)", got, ok)
	}
	if got, ok := cvimage.EstimateTokensForModel("anthropic", "claude-opus-4-20250514", "default", 1920, 1080); !ok || got != 1560 {
		t.Fatalf("Claude 4.0 dated snapshot 1920x1080 = (%d,%v), want (1560,true)", got, ok)
	}
	if got, ok := cvimage.EstimateTokensForModel("anthropic", "claude-opus-4-1-20250805", "default", 1920, 1080); !ok || got != 1560 {
		t.Fatalf("Claude 4.1 dated snapshot 1920x1080 = (%d,%v), want (1560,true)", got, ok)
	}
	if got, ok := cvimage.EstimateTokensForModel("anthropic", "claude-opus-4-8", "default", 1920, 1080); !ok || got != 2691 {
		t.Fatalf("Claude 4.8 1920x1080 = (%d,%v), want (2691,true)", got, ok)
	}
}

func TestDecideFailsSafeTowardKeepingDetail(t *testing.T) {
	t.Parallel()
	p := cvimage.Props{Width: 1024, Height: 1024}
	keep := []string{
		"",                         // empty → keep
		"read the serial number",   // text/OCR → keep
		"how many people are here", // counting → keep
		"transcribe the sign",      // OCR → keep
		"tell me about this image", // ambiguous (no allow-low signal) → keep
	}
	for _, q := range keep {
		if plan := cvimage.Decide(p, q, "openai", "gpt-4o"); plan.Action != cvimage.ActionPassthrough {
			t.Errorf("query %q should keep full detail, got %s", q, plan.Action)
		}
	}
}

func TestDecideLowDetailAndResize(t *testing.T) {
	t.Parallel()
	p := cvimage.Props{Width: 1024, Height: 1024}
	if plan := cvimage.DecideWithDetail(p, "describe the overall scene", "openai", "gpt-4o", "high"); plan.Action != cvimage.ActionDetailLow {
		t.Errorf("openai gpt-4o describe → detail-low, got %s", plan.Action)
	}
	if plan := cvimage.Decide(p, "what is in the background", "anthropic", "claude-sonnet-4-6"); plan.Action != cvimage.ActionResize {
		t.Errorf("anthropic describe → resize, got %s", plan.Action)
	}
	// GPT-4o-mini uses a much larger base than GPT-4o, but high detail adds
	// per-tile tokens to that same base. Exact model math still permits low.
	big := cvimage.Props{Width: 1600, Height: 1200}
	if plan := cvimage.DecideWithDetail(big, "describe the scene", "openai", "gpt-4o-mini", "high"); plan.Action != cvimage.ActionDetailLow {
		t.Errorf("gpt-4o-mini exact high-to-low comparison should choose detail-low, got %s", plan.Action)
	}
	if plan := cvimage.DecideWithDetail(p, "describe the scene", "openai", "future-model", "high"); plan.Action != cvimage.ActionPassthrough {
		t.Errorf("unknown model must pass through, got %s", plan.Action)
	}
	if plan := cvimage.Decide(p, "describe the scene", "openai", "gpt-4o"); plan.Action != cvimage.ActionPassthrough {
		t.Errorf("ambiguous default detail must pass through, got %s", plan.Action)
	}
}

func TestApplyResizeShrinksAndIsDeterministic(t *testing.T) {
	t.Parallel()
	orig := genPNG(t, 1024, 1024)
	plan := cvimage.Plan{Action: cvimage.ActionResize, ResizeLong: 768, Provider: "anthropic", Model: "claude-sonnet-4-6", BeforeDetail: "default"}
	a, tb, ta, ok := cvimage.Apply(orig, plan)
	if !ok {
		t.Fatal("expected a resize result")
	}
	if ta >= tb {
		t.Errorf("tokens should drop: before=%d after=%d", tb, ta)
	}
	props, _ := cvimage.Analyze(a)
	if props.Width != 768 || props.Height != 768 {
		t.Errorf("resized dims = %dx%d, want 768x768", props.Width, props.Height)
	}
	b, _, _, _ := cvimage.Apply(orig, plan)
	if !bytes.Equal(a, b) {
		t.Error("resize is not deterministic")
	}
}

func TestApplyDetailLowKeepsBytes(t *testing.T) {
	t.Parallel()
	orig := genPNG(t, 1024, 1024)
	plan := cvimage.Plan{Action: cvimage.ActionDetailLow, Provider: "openai", Model: "gpt-4o", BeforeDetail: "high"}
	out, tb, ta, ok := cvimage.Apply(orig, plan)
	if !ok {
		t.Fatal("expected detail-low result")
	}
	if !bytes.Equal(out, orig) {
		t.Error("detail-low must leave the image bytes unchanged (gateway sets the flag)")
	}
	if ta != 85 || tb <= ta {
		t.Errorf("detail-low tokens before=%d after=%d (want after=85, before>after)", tb, ta)
	}
}

func TestApplyPassthrough(t *testing.T) {
	t.Parallel()
	orig := genPNG(t, 100, 100)
	_, _, _, ok := cvimage.Apply(orig, cvimage.Plan{Action: cvimage.ActionPassthrough, Provider: "openai", Model: "gpt-4o", BeforeDetail: "high"})
	if ok {
		t.Error("passthrough must report ok=false")
	}
}

func TestApplyDetailLowRejectedForProviderWithoutLowTier(t *testing.T) {
	t.Parallel()
	// Anthropic has no low-detail tier. Handed a detail-low plan directly, Apply
	// must claim nothing — not report a 100% saving with the bytes unchanged.
	orig := genPNG(t, 1024, 1024)
	if _, _, _, ok := cvimage.Apply(orig, cvimage.Plan{Action: cvimage.ActionDetailLow, Provider: "anthropic", Model: "claude-sonnet-4-6", BeforeDetail: "default"}); ok {
		t.Error("detail-low on a provider without a low tier must report ok=false (no fake saving)")
	}
}

func TestApplyResizePreservesColorOfSemiTransparentPixels(t *testing.T) {
	t.Parallel()
	// A fully-red, half-opaque image must stay red after downscale (the
	// premultiplied-alpha trap darkens it to ~half red otherwise).
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.NRGBA{R: 255, G: 0, B: 0, A: 128})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	out, _, _, ok := cvimage.Apply(buf.Bytes(), cvimage.Plan{Action: cvimage.ActionResize, ResizeLong: 32, Provider: "anthropic", Model: "claude-sonnet-4-6", BeforeDetail: "default"})
	if !ok {
		t.Fatal("expected resize")
	}
	decoded, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	// Read STRAIGHT-alpha channels (NRGBA), not the premultiplied RGBA() values —
	// a half-opaque red is correctly ~128 when premultiplied, so the meaningful
	// check is that the un-premultiplied red is preserved at ~255.
	c := color.NRGBAModel.Convert(decoded.At(0, 0)).(color.NRGBA)
	if c.R < 250 {
		t.Errorf("red channel darkened by premultiplied alpha: straight R=%d (A=%d)", c.R, c.A)
	}
}

func TestAnalyzeRejectsDecodeBomb(t *testing.T) {
	t.Parallel()
	// A header declaring an enormous area must be rejected before any full decode.
	for _, dims := range [][2]uint32{{12000, 12000}, {100000, 100000}} {
		b := genPNGWithDimensions(t, dims[0], dims[1])
		cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
		if err != nil || format != "png" || uint32(cfg.Width) != dims[0] || uint32(cfg.Height) != dims[1] {
			t.Fatalf("fixture %dx%d header did not parse cleanly: cfg=%+v format=%q err=%v", dims[0], dims[1], cfg, format, err)
		}
		if _, ok := cvimage.Analyze(b); ok {
			t.Errorf("Analyze must reject %dx%d image whose pixel area exceeds the decode-bomb cap", dims[0], dims[1])
		}
	}
}

func TestDecideNeverUpscales(t *testing.T) {
	t.Parallel()
	small := cvimage.Props{Width: 100, Height: 100}
	if plan := cvimage.Decide(small, "describe the scene", "anthropic", "claude-sonnet-4-6"); plan.Action == cvimage.ActionResize {
		t.Error("a small image must not be resized (would upscale)")
	}
}

func TestApplyRejectsMissingModelOrDetail(t *testing.T) {
	t.Parallel()
	orig := genPNG(t, 1024, 1024)
	for _, plan := range []cvimage.Plan{
		{Action: cvimage.ActionResize, ResizeLong: 768, Provider: "anthropic", BeforeDetail: "default"},
		{Action: cvimage.ActionDetailLow, Provider: "openai", Model: "gpt-4o"},
		{Action: cvimage.ActionDetailLow, Provider: "openai", Model: "future", BeforeDetail: "high"},
	} {
		if _, _, _, ok := cvimage.Apply(orig, plan); ok {
			t.Fatalf("unsupported plan %+v must fail closed", plan)
		}
	}
}

func TestApplyRejectsMarginalInferredReductionForS4(t *testing.T) {
	t.Parallel()
	orig := genPNG(t, 1024, 1024)
	plan := cvimage.Plan{Action: cvimage.ActionResize, ResizeLong: 1000, Provider: "anthropic", Model: "claude-sonnet-4-6", BeforeDetail: "default"}
	if _, before, after, ok := cvimage.Apply(orig, plan); ok || before != 0 || after != 0 {
		t.Fatalf("marginal S4 reduction must fail closed, got before=%d after=%d ok=%v", before, after, ok)
	}
}
