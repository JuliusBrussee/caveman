// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"os"
	"regexp"
	"strconv"
	"strings"
)

// DensityLevel selects how aggressively the pixel renderer packs text into a
// page. Unknown/invalid values fail closed to conservative.
type DensityLevel string

const (
	// DensityConservative is fallback geometry: 5px cell advance, 8px line pitch,
	// mono ink, one layer.
	DensityConservative DensityLevel = "conservative"
	// DensityBalanced is the default: 4px advance, 6px pitch, zebra ink, one
	// layer. Rows interpenetrate; colour marks row membership.
	DensityBalanced DensityLevel = "balanced"
	// DensityMax is balanced geometry plus a second overlay layer. The overlay
	// renderer lands in a later phase; here Layers:2 is carried but unconsumed.
	DensityMax DensityLevel = "max"
)

// Hi-res-tier canvas: the maximum no-resize rectangle for the high-res vision
// tier (2576×1456 = 92×52 patches = 4784 tokens = HiResPixelTier.MaxTokens).
// The standard-tier canvas is MaxWidthPx×MaxHeightPx (1568×728) from render.go.
const (
	HiResMaxWidthPx  = 2576
	HiResMaxHeightPx = 1456
)

// renderParams is the resolved per-reader-model, per-level render profile. It is
// the single source of truth for both what the renderer draws and what the gate
// prices, so profitability math prices exactly what will render.
type renderParams struct {
	Tier    PixelTier // canvas/pricing tier (std 1568×728 vs hi-res 2576×1456)
	CellAdv int       // horizontal cell advance in px (5 = today, 4 = squeeze)
	PitchY  int       // vertical line pitch in px (8 = today, 6 = tight)
	Zebra   bool      // ink scheme: false = mono black, true = 3-colour zebra
	Layers  int       // 1 or 2; 2 is max only (overlay renderer lands later)
}

var (
	// Hi-res-tier reader families matched by prefix, mirroring applicability.go.
	hiResPrefixBases = []string{"claude-fable-5", "claude-mythos-5", "claude-sonnet-5"}
	// Opus qualifies for the hi-res tier from 4.8 onward (claude-opus-4-8+).
	opusVersionRE = regexp.MustCompile(`^claude-opus-(\d+)(?:[-.](\d+))?`)
)

// DensityFromEnv reads CAVE_PIXEL_DENSITY lazily (like CAVE_PIXEL_MODELS in
// applicability.go — no init-time caching). Unset/empty defaults to balanced;
// falsey values map to conservative; unknown values fail closed to conservative.
func DensityFromEnv() DensityLevel {
	raw, ok := os.LookupEnv("CAVE_PIXEL_DENSITY")
	if !ok || strings.TrimSpace(raw) == "" {
		return DensityBalanced
	}
	trimmed := strings.TrimSpace(raw)
	if falseyModelList(trimmed) {
		return DensityConservative
	}
	return normalizeLevel(DensityLevel(strings.ToLower(trimmed)))
}

func normalizeLevel(l DensityLevel) DensityLevel {
	switch l {
	case DensityConservative, DensityBalanced, DensityMax:
		return l
	default:
		return DensityConservative
	}
}

// ResolveDensity maps a reader model and requested level to a render profile.
// Fail-closed rules: an unrecognised model resolves to conservative geometry on
// the standard tier regardless of the requested level; GPT-5.6 gets balanced
// geometry but never the hi-res Anthropic canvas (GPT sizing stays governed by
// gpt_profiles.go). Hard floors are enforced so no config can reach the reader
// stall envelope.
func ResolveDensity(model string, level DensityLevel) renderParams {
	level = normalizeLevel(level)
	hires := isHiResModel(model)
	// Only models we recognise as density-capable get anything but the
	// conservative floor. GPT-5.6 is recognised but pinned to the std canvas.
	if !hires && !isGpt56(model) {
		return applyDensityFloors(levelParams(DensityConservative, StandardPixelTier))
	}
	tier := StandardPixelTier
	if hires {
		tier = HiResPixelTier
	}
	return applyDensityFloors(levelParams(level, tier))
}

func levelParams(level DensityLevel, tier PixelTier) renderParams {
	switch level {
	case DensityBalanced:
		return renderParams{Tier: tier, CellAdv: 4, PitchY: 6, Zebra: true, Layers: 1}
	case DensityMax:
		return renderParams{Tier: tier, CellAdv: 4, PitchY: 6, Zebra: true, Layers: 2}
	default: // conservative
		return renderParams{Tier: tier, CellAdv: 5, PitchY: 8, Zebra: false, Layers: 1}
	}
}

// applyDensityFloors clamps a profile away from the reader stall envelope:
// zebra pitch floors at 5 (colour buys that rung), mono pitch floors at 6,
// layers are held in [1,2]. Background tints are not an option at all.
func applyDensityFloors(rp renderParams) renderParams {
	pitchFloor := 6
	if rp.Zebra {
		pitchFloor = 5
	}
	rp.PitchY = max(pitchFloor, rp.PitchY)
	rp.Layers = min(2, max(1, rp.Layers))
	return rp
}

func isHiResModel(model string) bool {
	if model == "" {
		return false
	}
	stripped := variantTagRE.ReplaceAllString(model, "")
	for _, cand := range modelCandidates(stripped) {
		c := strings.ToLower(cand)
		for _, base := range hiResPrefixBases {
			if c == base || strings.HasPrefix(c, base+"-") {
				return true
			}
		}
		if m := opusVersionRE.FindStringSubmatch(c); m != nil {
			major, _ := strconv.Atoi(m[1])
			minor := 0
			if m[2] != "" {
				minor, _ = strconv.Atoi(m[2])
			}
			if major > 4 || (major == 4 && minor >= 8) {
				return true
			}
		}
	}
	return false
}

func isGpt56(model string) bool {
	if model == "" {
		return false
	}
	stripped := strings.ToLower(variantTagRE.ReplaceAllString(model, ""))
	for _, cand := range modelCandidates(stripped) {
		if gpt56RE.MatchString(cand) {
			return true
		}
	}
	return false
}

// --- Page geometry derived from a render profile ---------------------------
//
// A page is drawn as: rows advance by PitchY with an 8px-tall glyph, so the
// last row still gets its full glyph height; columns advance by CellAdv with a
// 5px-wide glyph, so a narrow advance overlaps the neighbour. These helpers are
// the single geometry the renderer and the gate both consult.

// canvas returns the maximum no-resize canvas for the profile's tier.
func (rp renderParams) canvas() (widthPx, heightPx int) {
	if rp.Tier == HiResPixelTier {
		return HiResMaxWidthPx, HiResMaxHeightPx
	}
	return MaxWidthPx, MaxHeightPx
}

// linesPerPage is how many text rows fit in a canvas of the given height at this
// pitch: (H - 2*pad - glyphH)/pitch + 1.
func (rp renderParams) linesPerPage(canvasHeightPx int) int {
	return max(1, (canvasHeightPx-2*PadY-CellH)/rp.PitchY+1)
}

// pageHeightPx is the pixel height of a page holding `lines` rows:
// 2*pad + (lines-1)*pitch + glyphH.
func (rp renderParams) pageHeightPx(lines int) int {
	if lines <= 0 {
		return 2 * PadY
	}
	return 2*PadY + (lines-1)*rp.PitchY + CellH
}

// colsPerPage is how many columns fit in a canvas of the given width at this
// cell advance, accounting for the glyph overhang of the final column.
func (rp renderParams) colsPerPage(canvasWidthPx int) int {
	return max(1, (canvasWidthPx-2*PadX-max(0, AtlasCellW-rp.CellAdv))/rp.CellAdv)
}

// pageWidthPx is the pixel width of a page `cols` columns wide:
// 2*pad + cols*advance + max(0, glyphW-advance).
func (rp renderParams) pageWidthPx(cols int) int {
	return 2*PadX + cols*rp.CellAdv + max(0, AtlasCellW-rp.CellAdv)
}

// charsPerPage is the character capacity of a full canvas page at this profile.
func (rp renderParams) charsPerPage(canvasWidthPx, canvasHeightPx int) int {
	return rp.colsPerPage(canvasWidthPx) * rp.linesPerPage(canvasHeightPx)
}

// pageCols is the column count that fills the profile's canvas width — 312 on
// the std canvas at cell-advance 5, 513 on the hi-res canvas, etc.
func (rp renderParams) pageCols() int {
	w, _ := rp.canvas()
	return rp.colsPerPage(w)
}

// pageRows is the row count that fills the profile's canvas height.
func (rp renderParams) pageRows() int {
	_, h := rp.canvas()
	return rp.linesPerPage(h)
}

// layers is the effective layer count. Exactly 2 is the max overlay; ANY other
// value fails closed to a single conservative layer (the brief's fail-closed
// direction — an unexpected layer count never renders a phantom overlay).
func (rp renderParams) layers() int {
	if rp.Layers == 2 {
		return 2
	}
	return 1
}

// imageRowGeometry returns the per-layer pixel rows, column count and layer count
// for one emitted image at this profile — the shared basis of imageLineCapacity
// and the gate's per-image pricing. Multi-column images draw conservative std
// geometry with a single layer; single-column images fill the tier canvas and,
// at max, stack two layers into the same pixels.
func (rp renderParams) imageRowGeometry(cols, numCols, maxCharsPerImage int) (rowsPerLayer, n, layers int) {
	n = max(1, numCols)
	if maxCharsPerImage <= 0 {
		maxCharsPerImage = ReadableCharsPerImage
	}
	layers = 1
	var hardLinesPerImg int
	if n > 1 {
		hardLinesPerImg = LinesPerImage
	} else {
		_, canvasH := rp.canvas()
		hardLinesPerImg = rp.linesPerPage(canvasH)
		layers = rp.layers()
	}
	readableLinesPerCol := max(1, maxCharsPerImage/max(1, cols))
	rowsPerLayer = min(hardLinesPerImg, readableLinesPerCol)
	return rowsPerLayer, n, layers
}

// imageLineCapacity is the honest number of text lines one emitted image holds at
// this profile: per-layer rows × columns × layers. The gate, the splitter and the
// truncator all size images through this one seam so they can never disagree on
// capacity — at max a single image holds 2× the lines of a balanced page.
func (rp renderParams) imageLineCapacity(cols, numCols, maxCharsPerImage int) int {
	rowsPerLayer, n, layers := rp.imageRowGeometry(cols, numCols, maxCharsPerImage)
	return rowsPerLayer * n * layers
}

// charBudget is the per-image character capacity of a full canvas page at this
// profile (rows × cols × layers). Page capacity — not the fixed std budget — is
// the cap used by the renderer and cost gate. For std-conservative it
// equals the legacy ReadableCharsPerImage
// (312 × 90 × 1 = 28080). A max page holds two layers, so its budget is doubled
// (identical pixels, twice the text) — the single seam through which the gate,
// splitter and transforms agree on 2-layer capacity.
func (rp renderParams) charBudget() int {
	return rp.pageCols() * rp.pageRows() * rp.layers()
}

// renderStyle builds the RenderStyle that draws this profile. Cell advance is
// expressed through CellWBonus (advance − glyph width); pitch and zebra are
// carried directly. The overlay layer (Layers:2) is not consumed here.
func (rp renderParams) renderStyle() RenderStyle {
	return RenderStyle{
		AA:         true,
		CellWBonus: rp.CellAdv - AtlasGrayCellW,
		PitchY:     rp.PitchY,
		Zebra:      rp.Zebra,
	}
}

// conservativeStdParams is the standard-tier conservative profile: today's exact
// geometry. Gate callers with no density context price against it, which
// reproduces the pre-density pricing byte-for-byte.
var conservativeStdParams = renderParams{Tier: StandardPixelTier, CellAdv: 5, PitchY: 8, Zebra: false, Layers: 1}

// isConservativeGeometry reports whether the profile draws today's geometry
// (5px advance, 8px pitch, mono). When true the transform keeps its original
// per-site RenderStyle so conservative output stays byte-for-byte identical;
// only non-conservative levels swap in renderStyle().
func (rp renderParams) isConservativeGeometry() bool {
	return rp.CellAdv == 5 && rp.PitchY == 8 && !rp.Zebra
}

// DensityDraw is the exported, cross-package view of a resolved render profile:
// everything a caller OUTSIDE the pixel package (the engine CLI, the proxy live
// zone) needs to render at a density without touching the unexported renderParams.
// Style is the RenderStyle to draw (the empty value for conservative, so
// conservative output stays byte-for-byte identical); Cols is the single-column
// page width; CharBudget is the per-image character capacity (doubled for the
// 2-layer max page); Layers is 1 or 2 (2 = max overlay); CanvasH is the tier canvas
// height to pass as maxHeightPx; Tier prices the emitted images; Zebra reports the
// ink scheme so the caller can prepend the reader note.
type DensityDraw struct {
	Style      RenderStyle
	Cols       int
	CharBudget int
	Layers     int
	CanvasH    int
	Tier       PixelTier
	Zebra      bool
}

func drawFromParams(rp renderParams) DensityDraw {
	style := RenderStyle{}
	if !rp.isConservativeGeometry() {
		style = rp.renderStyle()
	}
	_, canvasH := rp.canvas()
	return DensityDraw{
		Style:      style,
		Cols:       rp.pageCols(),
		CharBudget: rp.charBudget(),
		Layers:     rp.layers(),
		CanvasH:    canvasH,
		Tier:       rp.Tier,
		Zebra:      rp.Zebra,
	}
}

// StdDensityDraw resolves a density level to its STANDARD-tier geometry, model-
// agnostic: the dev-tool render (`caveman-engine pixel render`) draws exactly the
// requested level with no reader-model gating. An unknown level normalises to
// conservative (the same fail-closed direction as the env path).
func StdDensityDraw(level DensityLevel) DensityDraw {
	return drawFromParams(applyDensityFloors(levelParams(normalizeLevel(level), StandardPixelTier)))
}

// ResolveDensityDraw resolves a reader model + level to its drawable geometry,
// applying ResolveDensity's fail-closed model gating (an unrecognised model falls
// to the conservative standard-tier floor). This is the seam the proxy live zone
// uses so CAVE_PIXEL_DENSITY drives its own render exactly as the transforms do.
func ResolveDensityDraw(model string, level DensityLevel) DensityDraw {
	return drawFromParams(ResolveDensity(model, level))
}

// DensityInkNote returns the reader instruction a caller must prepend before
// rendered content so the model reads the ink convention correctly: the two-layer
// overlay note when the page stacks two layers, the zebra note when zebra ink is on
// and single-layer, otherwise the empty string (mono black needs no note). It is
// the single exported wording shared by the transforms and the proxy live zone.
func DensityInkNote(zebra bool, layers int) string {
	if layers == 2 {
		return twoLayerReaderNote()
	}
	return zebraReaderNote(zebra)
}
