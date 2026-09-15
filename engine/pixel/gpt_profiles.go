// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"encoding/json"
	"math"
	"os"
	"regexp"
	"strings"
	"sync"
)

const (
	GptMaxHeightPx      = 1932
	DefaultGptStripCols = 152
)

type GptVisionCost struct {
	Regime     string  `json:"regime"`
	Base       float64 `json:"base,omitempty"`
	PerTile    float64 `json:"perTile,omitempty"`
	Multiplier float64 `json:"multiplier,omitempty"`
	PatchCap   int     `json:"patchCap,omitempty"`
}

type GptProfile struct {
	Vision      GptVisionCost `json:"vision"`
	StripCols   int           `json:"stripCols"`
	MaxHeightPx int           `json:"maxHeightPx"`
}

var (
	// pxpipe src/core/gpt-model-profiles.ts:67-68 includes o4-mini in the patch-billed mini family.
	gptMiniNanoPatchRE = regexp.MustCompile(`^(?:(?:gpt-5(?:\.\d+)?|gpt-4\.1)-(?:mini|nano)|o4-mini)`)
	gptNanoRE          = regexp.MustCompile(`nano`)
	gpt56RE            = regexp.MustCompile(`^gpt-5\.6`)
	gpt5DotRE          = regexp.MustCompile(`^gpt-5\.\d`)
	gpt5RE             = regexp.MustCompile(`^gpt-5`)
	gptReasoningRE     = regexp.MustCompile(`^o[13]`)

	gptEnvMu  sync.Mutex
	gptEnvRaw string
	gptEnvMap map[string]GptProfile
)

var defaultGptProfile = GptProfile{
	Vision:      GptVisionCost{Regime: "tile", Base: 85, PerTile: 170},
	StripCols:   DefaultGptStripCols,
	MaxHeightPx: GptMaxHeightPx,
}

func ResolveGptProfile(model string) GptProfile {
	m := strings.ToLower(model)
	env := gptEnvProfiles()
	bestLen := -1
	var best GptProfile
	for prefix, profile := range env {
		if strings.HasPrefix(m, prefix) && len(prefix) > bestLen {
			best = profile
			bestLen = len(prefix)
		}
	}
	if bestLen >= 0 {
		return best
	}
	return resolveBuiltinGptProfile(m)
}

func resolveBuiltinGptProfile(m string) GptProfile {
	switch {
	case gptMiniNanoPatchRE.MatchString(m) && gptNanoRE.MatchString(m):
		return GptProfile{Vision: GptVisionCost{Regime: "patch", Multiplier: 2.46, PatchCap: 1536}, StripCols: DefaultGptStripCols, MaxHeightPx: GptMaxHeightPx}
	case gptMiniNanoPatchRE.MatchString(m):
		return GptProfile{Vision: GptVisionCost{Regime: "patch", Multiplier: 1.62, PatchCap: 1536}, StripCols: DefaultGptStripCols, MaxHeightPx: GptMaxHeightPx}
	case gpt56RE.MatchString(m):
		return GptProfile{Vision: GptVisionCost{Regime: "patch", Multiplier: 1, PatchCap: 10000}, StripCols: DefaultGptStripCols, MaxHeightPx: GptMaxHeightPx}
	case gpt5DotRE.MatchString(m):
		return GptProfile{Vision: GptVisionCost{Regime: "patch", Multiplier: 1, PatchCap: 10000}, StripCols: DefaultGptStripCols, MaxHeightPx: GptMaxHeightPx}
	case gpt5RE.MatchString(m):
		return GptProfile{Vision: GptVisionCost{Regime: "tile", Base: 70, PerTile: 140}, StripCols: DefaultGptStripCols, MaxHeightPx: GptMaxHeightPx}
	case gptReasoningRE.MatchString(m):
		return GptProfile{Vision: GptVisionCost{Regime: "tile", Base: 75, PerTile: 150}, StripCols: DefaultGptStripCols, MaxHeightPx: GptMaxHeightPx}
	default:
		return defaultGptProfile
	}
}

type gptProfileEnvPartial struct {
	Vision      *GptVisionCost `json:"vision"`
	StripCols   *float64       `json:"stripCols"`
	MaxHeightPx *float64       `json:"maxHeightPx"`
}

func gptEnvProfiles() map[string]GptProfile {
	// Caveman rename from pxpipe's PXPIPE_GPT_PROFILES; JSON schema and
	// longest-prefix semantics stay identical.
	raw := os.Getenv("CAVE_PIXEL_GPT_PROFILES")
	gptEnvMu.Lock()
	defer gptEnvMu.Unlock()
	if raw == gptEnvRaw && gptEnvMap != nil {
		return gptEnvMap
	}
	gptEnvRaw = raw
	gptEnvMap = parseGptEnvProfiles(raw)
	return gptEnvMap
}

func parseGptEnvProfiles(raw string) map[string]GptProfile {
	out := make(map[string]GptProfile)
	if strings.TrimSpace(raw) == "" {
		return out
	}
	var obj map[string]gptProfileEnvPartial
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return out
	}
	for k, partial := range obj {
		key := strings.ToLower(k)
		base := resolveBuiltinGptProfile(key)
		profile := base
		if partial.Vision != nil && validGptVisionCost(*partial.Vision) {
			profile.Vision = *partial.Vision
		}
		if partial.StripCols != nil && validPositiveIntFloat(*partial.StripCols) {
			profile.StripCols = int(math.Floor(*partial.StripCols))
		}
		if partial.MaxHeightPx != nil && validPositiveIntFloat(*partial.MaxHeightPx) {
			profile.MaxHeightPx = int(math.Floor(*partial.MaxHeightPx))
		}
		out[key] = profile
	}
	return out
}

func validGptVisionCost(v GptVisionCost) bool {
	switch v.Regime {
	case "tile":
		// Token prices cannot be negative or all-zero. Check a conservative
		// 16-tile envelope too, so later float-to-int conversion cannot overflow.
		return v.Base >= 0 && v.PerTile >= 0 && (v.Base > 0 || v.PerTile > 0) &&
			safeIntFloat(v.Base+16*v.PerTile)
	case "patch":
		return v.Multiplier > 0 && v.PatchCap > 0 &&
			safeIntFloat(v.Multiplier*float64(v.PatchCap))
	default:
		return false
	}
}

func finiteFloat(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func safeIntFloat(v float64) bool {
	// Strict inequality avoids float64 rounding MaxInt up to 2^63 on 64-bit
	// platforms, which would convert to a negative int.
	return finiteFloat(v) && v >= 0 && v < float64(int(^uint(0)>>1))
}

func validPositiveIntFloat(v float64) bool {
	return v >= 1 && safeIntFloat(v)
}

func OpenAIVisionTokens(model string, w, h int) int {
	cost := ResolveGptProfile(model).Vision
	if cost.Regime == "patch" {
		patches := int(math.Ceil(float64(w)/32) * math.Ceil(float64(h)/32))
		if cost.PatchCap > 0 && patches > cost.PatchCap {
			patches = cost.PatchCap
		}
		return int(math.Ceil(float64(patches) * cost.Multiplier))
	}
	W, H := float64(w), float64(h)
	if max(W, H) > 2048 {
		r := 2048 / max(W, H)
		W = math.Floor(W * r)
		H = math.Floor(H * r)
	}
	if min(W, H) > 768 {
		r := 768 / min(W, H)
		W = math.Floor(W * r)
		H = math.Floor(H * r)
	}
	tiles := math.Ceil(W/512) * math.Ceil(H/512)
	return int(math.Ceil(cost.Base + cost.PerTile*tiles))
}
