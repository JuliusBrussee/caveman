package image

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// EstimateTokens retains its signature only; returning zero is a deliberate
// semantic break from the former provider-wide estimate. Current providers use
// model- and media-detail-specific regimes, so callers must migrate to
// EstimateTokensForModel. Honest zero prevents stale math from authorizing a
// lossy transform.
func EstimateTokens(provider string, w, h int) int {
	return 0
}

// EstimateTokensForModel returns documented input-image tokens only when
// provider, model family, and effective detail/media-resolution form a closed
// supported tuple. ok=false means unavailable, never zero-cost. Counts remain
// inferred until provider usage confirms them.
func EstimateTokensForModel(provider, model, detail string, w, h int) (tokens int, ok bool) {
	if w <= 0 || h <= 0 || int64(w) > int64(maxPixels)/int64(h) {
		return 0, false
	}
	provider = normalizeProvider(provider)
	model = normalizeModel(model)
	detail = strings.ToLower(strings.TrimSpace(detail))
	if model == "" || detail == "" {
		return 0, false
	}

	switch provider {
	case "openai":
		return estimateOpenAI(model, detail, w, h)
	case "anthropic":
		return estimateAnthropic(model, detail, w, h)
	case "google":
		return estimateGemini(model, detail)
	default:
		return 0, false
	}
}

func normalizeProvider(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	if p == "gemini" {
		return "google"
	}
	return p
}

func normalizeModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	if i := strings.IndexByte(model, '@'); i >= 0 {
		model = model[:i]
	}
	if i := strings.IndexByte(model, ':'); i >= 0 {
		model = model[:i]
	}
	if i := strings.Index(model, "claude-"); i > 0 {
		model = model[i:]
	}
	return strings.TrimSpace(model)
}

func aliasOrDateSnapshot(model, base string) bool {
	if model == base {
		return true
	}
	suffix := strings.TrimPrefix(model, base+"-")
	if suffix == model {
		return false
	}
	_, err := time.Parse("2006-01-02", suffix)
	return err == nil
}

func estimateOpenAI(model, detail string, w, h int) (int, bool) {
	switch {
	case aliasOrDateSnapshot(model, "gpt-4o-mini"):
		switch detail {
		case "low":
			return 2833, true
		case "high":
			return openAITileTokens(w, h, 2833, 5667), true
		default:
			return 0, false
		}
	case aliasOrDateSnapshot(model, "gpt-4o"), aliasOrDateSnapshot(model, "gpt-4.1"), aliasOrDateSnapshot(model, "gpt-4.5"):
		switch detail {
		case "low":
			return 85, true
		case "high":
			return openAITileTokens(w, h, 85, 170), true
		default:
			return 0, false
		}
	case aliasOrDateSnapshot(model, "gpt-5.6"), aliasOrDateSnapshot(model, "gpt-5.6-sol"), aliasOrDateSnapshot(model, "gpt-5.6-terra"), aliasOrDateSnapshot(model, "gpt-5.6-luna"):
		switch detail {
		case "low":
			return 16 * 16, true // provider renders low detail at 512×512, 32px patches
		case "default", "auto", "original":
			return ceilDiv(w, 32) * ceilDiv(h, 32), true
		default:
			// GPT-5.6 high has finite resizing limits not published here; refusing
			// is safer than treating it as original.
			return 0, false
		}
	default:
		return 0, false
	}
}

func estimateAnthropic(model, detail string, w, h int) (int, bool) {
	if detail != "default" {
		return 0, false
	}
	family, major, minor, known := anthropicVersion(model)
	if !known {
		return 0, false
	}
	maxPx, maxTokens := 1568, 1568
	switch {
	case major == 3:
		// Current reference keeps Claude 3.x on the standard profile.
	case major == 4 && minor <= 6:
		// Current reference keeps Claude <=4.6 on the standard profile.
	case major == 4 && family == "opus" && (minor == 7 || minor == 8):
		maxPx, maxTokens = 2576, 4784
	case major == 5 && minor == 0 && (family == "fable" || family == "mythos" || family == "sonnet"):
		maxPx, maxTokens = 2576, 4784
	default:
		// Never infer a future model generation's vision contract.
		return 0, false
	}
	return anthropicPatchTokens(w, h, maxPx, maxTokens), true
}

func anthropicVersion(model string) (family string, major, minor int, ok bool) {
	parts := strings.Split(model, "-")
	if len(parts) < 3 || parts[0] != "claude" {
		return "", 0, 0, false
	}
	// Provider snapshots use an optional YYYYMMDD suffix, and Bedrock may add
	// -vN after it. Strip only those exact suffixes; arbitrary suffixes are not
	// members of a documented model tier.
	if len(parts) >= 2 && isAnthropicRevision(parts[len(parts)-1]) {
		if len(parts) < 3 || !isYYYYMMDD(parts[len(parts)-2]) {
			return "", 0, 0, false
		}
		parts = parts[:len(parts)-2]
	} else if isYYYYMMDD(parts[len(parts)-1]) {
		parts = parts[:len(parts)-1]
	}
	parseVersion := func(s string, allowZero bool) (int, bool) {
		if len(s) < 1 || len(s) > 2 {
			return 0, false
		}
		v, err := strconv.Atoi(s)
		return v, err == nil && (v > 0 || allowZero)
	}
	switch len(parts) {
	case 3: // claude-{family}-{major} or claude-{major}-{family}
		if isAnthropicFamily(parts[1]) {
			family = parts[1]
			major, ok = parseVersion(parts[2], false)
		} else if isAnthropicFamily(parts[2]) {
			family = parts[2]
			major, ok = parseVersion(parts[1], false)
		}
	case 4: // claude-{family}-{major}-{minor} or claude-{major}-{minor}-{family}
		if isAnthropicFamily(parts[1]) {
			family = parts[1]
			major, ok = parseVersion(parts[2], false)
			if ok {
				minor, ok = parseVersion(parts[3], true)
			}
		} else if isAnthropicFamily(parts[3]) {
			family = parts[3]
			major, ok = parseVersion(parts[1], false)
			if ok {
				minor, ok = parseVersion(parts[2], true)
			}
		}
	}
	return family, major, minor, ok
}

func isAnthropicFamily(s string) bool {
	switch s {
	case "sonnet", "opus", "haiku", "fable", "mythos":
		return true
	default:
		return false
	}
}

func isYYYYMMDD(s string) bool {
	if len(s) != 8 {
		return false
	}
	_, err := time.Parse("20060102", s)
	return err == nil
}

func isAnthropicRevision(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	_, err := strconv.Atoi(s[1:])
	return err == nil
}

func estimateGemini(model, detail string) (int, bool) {
	switch {
	case isGemini3ImageModel(model):
		switch detail {
		case "low":
			return 280, true
		case "medium":
			return 560, true
		case "default", "high":
			return 1120, true
		case "ultra_high", "ultra-high":
			return 2240, true
		default:
			return 0, false
		}
	case isGemini25ImageModel(model):
		switch detail {
		case "low":
			return 64, true
		case "medium":
			return 256, true
		default:
			// High/default may add pan-and-scan crops; dimensions alone cannot
			// reproduce provider tokenization.
			return 0, false
		}
	default:
		return 0, false
	}
}

func isGemini3ImageModel(model string) bool {
	switch model {
	case "gemini-3-pro", "gemini-3-pro-preview",
		"gemini-3-flash", "gemini-3-flash-preview",
		"gemini-3.1-pro-preview", "gemini-3.1-flash-lite", "gemini-3.1-flash-lite-preview",
		"gemini-3.5-flash", "gemini-3.5-flash-lite", "gemini-3.6-flash":
		return true
	default:
		return false
	}
}

func isGemini25ImageModel(model string) bool {
	switch model {
	case "gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite":
		return true
	default:
		return false
	}
}

// openAITileTokens scales to fit 2048², then short side to 768px, then bills
// provider-specific base + per-tile tokens.
func openAITileTokens(w, h, base, perTile int) int {
	if w > 2048 || h > 2048 {
		w, h = fitWithin(w, h, 2048)
	}
	short := w
	if h < short {
		short = h
	}
	if short > 768 {
		w, h = scaleShortSideTo(w, h, 768)
	}
	tiles := ceilDiv(w, 512) * ceilDiv(h, 512)
	return base + perTile*tiles
}

func anthropicPatchTokens(w, h, maxPx, maxTokens int) int {
	fits := func(fw, fh int) bool {
		return ceilDiv(fw, 28)*28 <= maxPx && ceilDiv(fh, 28)*28 <= maxPx && ceilDiv(fw, 28)*ceilDiv(fh, 28) <= maxTokens
	}
	if !fits(w, h) {
		swapped := h > w
		if swapped {
			w, h = h, w
		}
		aspect := float64(w) / float64(h)
		lo, hi := 1, w
		for lo+1 < hi {
			mid := lo + (hi-lo)/2
			mh := max(1, int(math.Round(float64(mid)/aspect)))
			if fits(mid, mh) {
				lo = mid
			} else {
				hi = mid
			}
		}
		w, h = lo, max(1, int(math.Round(float64(lo)/aspect)))
		if swapped {
			w, h = h, w
		}
	}
	return ceilDiv(w, 28) * ceilDiv(h, 28)
}

func fitWithin(w, h, max int) (int, int) {
	long := w
	if h > long {
		long = h
	}
	if long <= max {
		return w, h
	}
	nw := w * max / long
	nh := h * max / long
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	return nw, nh
}

func scaleShortSideTo(w, h, target int) (int, int) {
	short := w
	if h < short {
		short = h
	}
	if short <= target {
		return w, h
	}
	nw := w * target / short
	nh := h * target / short
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	return nw, nh
}

func ceilDiv(a, b int) int {
	if b <= 0 {
		return 0
	}
	return (a + b - 1) / b
}
