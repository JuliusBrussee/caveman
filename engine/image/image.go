// Package image is the engine's deterministic, offline image-token reducer. It
// does NOT implement the byte-stream compressors.Compressor interface — image
// blocks live inside multimodal message arrays and the decision needs provider
// context (which detail knob exists) and query context (what the user asked).
// No production gateway path calls Decide or Apply today. They are an offline,
// opt-in primitive; any future wire path must store the original for recovery.
//
// Everything here is deterministic and pure-stdlib (no model weights, no
// network): supported token math is bound to provider+model+effective detail, the router
// is a keyword + dimension heuristic, and resize is an integer area-average box
// filter re-encoded as PNG (deterministic; token cost is dimension-based, so the
// PNG-vs-JPEG byte size does not affect token estimates). Counts here are local;
// provider usage is authoritative. The router fails safe toward keeping full
// detail: a miss costs reduction, never quality.
package image

import (
	"bytes"
	"image"
	"image/color"
	_ "image/gif"  // register GIF decoder
	_ "image/jpeg" // register JPEG decoder
	"image/png"
	"regexp"
	"strings"
)

// Props is the cheap, decode-light analysis of an image (dimensions + format).
type Props struct {
	Width  int
	Height int
	Format string
}

// maxPixels bounds the declared image area we will handle. A header can claim
// enormous dimensions (a decode bomb); rejecting them here keeps the downstream
// full image.Decode in Apply from attempting a huge allocation.
const maxPixels = 100_000_000 // 100 megapixels

// Analyze reads only the image header for dimensions and format. It returns
// ok=false on anything it cannot decode or that declares an unreasonable pixel
// area, so callers pass the image through unchanged.
func Analyze(b []byte) (Props, bool) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return Props{}, false
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return Props{}, false // decode-bomb guard
	}
	return Props{Width: cfg.Width, Height: cfg.Height, Format: format}, true
}

// Action is the chosen image transform.
type Action string

const (
	// ActionPassthrough keeps the image untouched (the fail-safe default).
	ActionPassthrough Action = "passthrough"
	// ActionDetailLow asks the gateway to set the provider's low-detail flag
	// (OpenAI detail:"low" / Gemini media_resolution:"low"). The bytes are
	// unchanged; the provider downsamples server-side.
	ActionDetailLow Action = "detail-low"
	// ActionResize downscales the pixels to ResizeLong on the long edge — the
	// only lever for providers without a detail flag (e.g. Anthropic).
	ActionResize Action = "resize"
)

// Plan is the deterministic decision for one image. SafetyClass is always S4
// when Action != passthrough (model-visible content changes; CCR must hold the
// original), expressed by the gateway, not here.
type Plan struct {
	Action       Action
	ResizeLong   int // long-edge target px when Action == ActionResize
	Provider     string
	Model        string
	BeforeDetail string // effective detail/media resolution before this transform
}

var (
	// Queries that need full detail: text/OCR, fine detail, counting, exactness.
	keepHighRe = regexp.MustCompile(`(?i)\b(read|reads?|text|ocr|transcrib\w*|serial|number|numbers|code|sign|signage|label|labels|caption|chart|charts|axis|axes|table|tables|diagram|legend|fine print|count|counting|how many|exact\w*|digit\w*|spelling|verbatim)\b`)
	// Queries safe for a low-detail thumbnail: gist, scene, description.
	allowLowRe = regexp.MustCompile(`(?i)\b(describe|description|what is this|what's in|whats in|is there|are there|overall|scene|setting|mood|colou?r|colou?rs|background|roughly|gist|summar\w*|general(ly)?|vibe)\b`)
)

// Decide chooses a transform for an image given the sibling query text, the
// provider, and the model. It is fail-safe by construction: an empty or
// ambiguous query, any text/precision signal, or an unknown situation yields
// ActionPassthrough — a router miss keeps full detail.
func Decide(p Props, query, provider, model string) Plan {
	return DecideWithDetail(p, query, provider, model, "default")
}

// DecideWithDetail is Decide with explicit effective provider detail/media
// resolution. Ambiguous or unsupported model-detail tuples pass through.
func DecideWithDetail(p Props, query, provider, model, beforeDetail string) Plan {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.ToLower(strings.TrimSpace(model))
	beforeDetail = strings.ToLower(strings.TrimSpace(beforeDetail))
	plan := Plan{Action: ActionPassthrough, Provider: provider, Model: model, BeforeDetail: beforeDetail}

	q := strings.TrimSpace(query)
	// Keep full detail unless the query is explicitly a low-detail-safe ask AND
	// carries no precision signal.
	if q == "" || keepHighRe.MatchString(q) || !allowLowRe.MatchString(q) {
		return plan
	}
	before, supported := EstimateTokensForModel(provider, model, beforeDetail, p.Width, p.Height)
	if !supported || before <= 0 {
		return plan
	}

	longEdge := p.Width
	if p.Height > longEdge {
		longEdge = p.Height
	}

	switch provider {
	case "openai":
		low, ok := EstimateTokensForModel(provider, model, "low", p.Width, p.Height)
		if ok && meaningfulTokenReduction(before, low) {
			return Plan{Action: ActionDetailLow, Provider: provider, Model: model, BeforeDetail: beforeDetail}
		}
		return plan
	case "google", "gemini":
		low, ok := EstimateTokensForModel(provider, model, "low", p.Width, p.Height)
		if ok && meaningfulTokenReduction(before, low) {
			return Plan{Action: ActionDetailLow, Provider: provider, Model: model, BeforeDetail: beforeDetail}
		}
		return plan
	case "anthropic":
		if longEdge > 768 {
			afterW, afterH := scaleToLong(p.Width, p.Height, 768)
			after, ok := EstimateTokensForModel(provider, model, beforeDetail, afterW, afterH)
			if ok && meaningfulTokenReduction(before, after) {
				return Plan{Action: ActionResize, ResizeLong: 768, Provider: provider, Model: model, BeforeDetail: beforeDetail}
			}
		}
		return plan
	default:
		return plan
	}
}

func meaningfulTokenReduction(before, after int) bool {
	// S4 changes model-visible input. A marginal inferred delta cannot justify
	// that quality risk; require at least 25% before any future caller can act.
	return before > 0 && after > 0 && int64(after)*4 <= int64(before)*3
}

// Apply executes a plan. For ActionResize it returns the downscaled image (PNG,
// deterministic). For ActionDetailLow it returns the original bytes unchanged
// (the gateway sets the provider flag) with the low-detail token estimate. It
// returns ok=false when nothing useful happened (passthrough, decode failure, or
// a resize that would not actually reduce tokens), so the caller claims nothing.
func Apply(orig []byte, plan Plan) (out []byte, tokensBefore, tokensAfter int, ok bool) {
	props, okp := Analyze(orig)
	if !okp {
		return nil, 0, 0, false
	}
	tokensBefore, ok = EstimateTokensForModel(plan.Provider, plan.Model, plan.BeforeDetail, props.Width, props.Height)
	if !ok || tokensBefore <= 0 {
		return nil, 0, 0, false
	}

	switch plan.Action {
	case ActionResize:
		img, _, err := image.Decode(bytes.NewReader(orig))
		if err != nil {
			return nil, 0, 0, false
		}
		nw, nh := scaleToLong(props.Width, props.Height, plan.ResizeLong)
		if nw >= props.Width && nh >= props.Height {
			return nil, 0, 0, false // not actually smaller
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, downscale(img, nw, nh)); err != nil {
			return nil, 0, 0, false
		}
		tokensAfter, ok = EstimateTokensForModel(plan.Provider, plan.Model, plan.BeforeDetail, nw, nh)
		if !ok || !meaningfulTokenReduction(tokensBefore, tokensAfter) {
			return nil, 0, 0, false
		}
		return buf.Bytes(), tokensBefore, tokensAfter, true
	case ActionDetailLow:
		low, supported := EstimateTokensForModel(plan.Provider, plan.Model, "low", props.Width, props.Height)
		if !supported || low <= 0 {
			// The provider has no low-detail tier (e.g. Anthropic) — a detail-low
			// plan is not applicable. Return failure rather than report a reduction
			// with the bytes unchanged. (Decide never emits this plan
			// for such providers; Apply must not trust a plan blindly.)
			return nil, 0, 0, false
		}
		tokensAfter = low
		if !meaningfulTokenReduction(tokensBefore, tokensAfter) {
			return nil, 0, 0, false
		}
		return orig, tokensBefore, tokensAfter, true // bytes unchanged; gateway sets the flag
	default:
		return orig, tokensBefore, tokensBefore, false
	}
}

func scaleToLong(w, h, long int) (int, int) {
	if w >= h {
		if w <= long {
			return w, h
		}
		nh := h * long / w
		if nh < 1 {
			nh = 1
		}
		return long, nh
	}
	if h <= long {
		return w, h
	}
	nw := w * long / h
	if nw < 1 {
		nw = 1
	}
	return nw, long
}

// downscale is a deterministic integer area-average (box) filter. Each
// destination pixel averages the source pixels in its mapped region. Pure
// arithmetic in a fixed traversal order → byte-identical output for the same
// input on every platform.
func downscale(src image.Image, nw, nh int) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		sy0 := b.Min.Y + y*sh/nh
		sy1 := b.Min.Y + (y+1)*sh/nh
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for x := 0; x < nw; x++ {
			sx0 := b.Min.X + x*sw/nw
			sx1 := b.Min.X + (x+1)*sw/nw
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var r, g, bl, a, cnt uint64
			for yy := sy0; yy < sy1; yy++ {
				for xx := sx0; xx < sx1; xx++ {
					// Convert to straight (non-premultiplied) alpha before averaging.
					// RGBA() is alpha-premultiplied, which darkens semi-transparent
					// pixels when written back as straight-alpha NRGBA.
					c := color.NRGBA64Model.Convert(src.At(xx, yy)).(color.NRGBA64)
					r += uint64(c.R)
					g += uint64(c.G)
					bl += uint64(c.B)
					a += uint64(c.A)
					cnt++
				}
			}
			if cnt == 0 {
				cnt = 1
			}
			// NRGBA64 channels are 16-bit straight-alpha; shift back to 8-bit.
			dst.Set(x, y, color.NRGBA{
				R: uint8((r / cnt) >> 8),
				G: uint8((g / cnt) >> 8),
				B: uint8((bl / cnt) >> 8),
				A: uint8((a / cnt) >> 8),
			})
		}
	}
	return dst
}
