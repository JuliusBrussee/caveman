// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import "math"

const (
	ImageCostSafetyMargin = 1.10
	CacheCreateRate       = 1.25
	CacheReadRate         = 0.10
	CharsPerToken         = 4.0
	SlabCharsPerToken     = 2.0
	HistoryCharsPerToken  = 2.0
)

var LinesPerImage = max(1, (MaxHeightPx-2*PadY)/CellH)

type GateEval struct {
	ImageTokens, TextTokens     float64
	BurnImageSide, BurnTextSide float64
	Profitable                  bool
}

func singleColWidthPx(cols int) int {
	return 2*PadX + cols*CellW
}

func multiColWidthPx(cols, numCols int) int {
	n := max(1, numCols)
	if n == 1 {
		return singleColWidthPx(cols)
	}
	return MultiColWidth(cols, n)
}

// heightForRows is the reference page height for a page holding `rows` rows at
// the given pitch and glyph height: 2*pad + (rows-1)*pitch + glyphH.
func heightForRows(rows, pitch, glyphH int) int {
	if rows <= 0 {
		return 2 * PadY
	}
	return 2*PadY + (rows-1)*pitch + glyphH
}

func imageTokensForRows(visualRows, cols, numCols, imageCountCap, maxCharsPerImage int, rp renderParams) float64 {
	if visualRows <= 0 {
		return 0
	}
	n := max(1, numCols)
	if maxCharsPerImage <= 0 {
		maxCharsPerImage = ReadableCharsPerImage
	}
	// The multi-column renderer draws at conservative geometry (CellW/CellH, std
	// tier) and never honours density levers, so price it that way. Single-column
	// pages price the profile's actual geometry — cell-advance width, pitch, tier
	// canvas height — so the gate prices exactly the page that will render.
	var widthPx, pitch, glyphH int
	tier := rp.Tier
	if n > 1 {
		widthPx = multiColWidthPx(cols, n)
		pitch, glyphH = CellH, CellH
		tier = StandardPixelTier
	} else {
		widthPx = rp.pageWidthPx(cols)
		pitch, glyphH = rp.PitchY, CellH
	}
	// Rows-per-image, columns and layers all flow from the one shared seam so the
	// gate, the splitter and the truncator agree. A 2-layer (max) image stacks
	// `layers` text layers into the SAME pixel height, so it holds `layers×` the
	// lines per image while costing the same per-image tokens as a single-layer
	// page — the gate parity that makes max profitable exactly where balanced is.
	rowsPerImage, _, layers := rp.imageRowGeometry(cols, n, maxCharsPerImage)
	linesPerImage := rowsPerImage * n * layers
	imagesNeeded := int(math.Ceil(float64(visualRows) / float64(linesPerImage)))
	if imageCountCap > 0 && imagesNeeded > imageCountCap {
		imagesNeeded = imageCountCap
	}
	fullImages := max(0, imagesNeeded-1)
	linesInLast := visualRows - fullImages*linesPerImage
	rowsInLast := min(max(1, linesInLast), rowsPerImage)
	fullImageHeight := heightForRows(rowsPerImage, pitch, glyphH)
	lastImageHeight := heightForRows(rowsInLast, pitch, glyphH)
	// Each rendered image is a separate Anthropic image block, priced (and
	// capped) independently on its own patch grid; sum the per-image tokens,
	// then apply the conservative safety margin once.
	perFull := AnthropicImageTokens(widthPx, fullImageHeight, tier)
	perLast := AnthropicImageTokens(widthPx, lastImageHeight, tier)
	totalTokens := fullImages*perFull + perLast
	return math.Ceil(float64(totalTokens) * ImageCostSafetyMargin)
}

func imageTokensCost(text string, cols, numCols, imageCountCap int, shrinkWidth bool, maxCharsPerImage int, rp renderParams) float64 {
	effectiveCols := cols
	if shrinkWidth {
		effectiveCols = MeasureContentCols(text, cols, 1)
	}
	rows := CountVisualRows(text, effectiveCols)
	return imageTokensForRows(rows, effectiveCols, numCols, imageCountCap, maxCharsPerImage, rp)
}

func EvalCompressionProfitability(text string, cols, imageCountCap, numCols int, charsPerToken, priorWarmTokens, priorWarmImageTokens float64, shrinkWidth bool, rp renderParams) *GateEval {
	if text == "" {
		return nil
	}
	cpt := charsPerToken
	if !isFinitePositive(cpt) {
		cpt = CharsPerToken
	}
	// Price against the profile's full-page character budget so single-column
	// pages fill the tier's canvas; the multi-column renderer stays on the fixed
	// readable cap. For std-conservative both are 28080, preserving old pricing.
	budget := rp.charBudget()
	if max(1, numCols) > 1 {
		budget = ReadableCharsPerImage
	}
	imageTokens := imageTokensCost(text, cols, max(1, numCols), imageCountCap, shrinkWidth, budget, rp)
	textTokens := float64(jsLen(text)) / cpt
	burnImageSide := 0.0
	if isFinitePositive(priorWarmTokens) {
		burnImageSide = priorWarmTokens * (CacheCreateRate - CacheReadRate)
	}
	burnTextSide := 0.0
	if isFinitePositive(priorWarmImageTokens) {
		burnTextSide = priorWarmImageTokens * (CacheCreateRate - CacheReadRate)
	}
	return &GateEval{
		ImageTokens:   imageTokens,
		TextTokens:    textTokens,
		BurnImageSide: burnImageSide,
		BurnTextSide:  burnTextSide,
		Profitable:    imageTokens+burnImageSide < textTokens+burnTextSide,
	}
}

func IsCompressionProfitable(text string, cols, imageCountCap, numCols int, cpt, priorWarm, priorWarmImage float64, shrinkWidth bool, maxCharsPerImage int, rp renderParams) bool {
	if text == "" {
		return false
	}
	if cols == 0 {
		cols = DefaultCols
	}
	if maxCharsPerImage == 0 {
		maxCharsPerImage = ReadableCharsPerImage
	}
	if !isFinitePositive(cpt) {
		cpt = CharsPerToken
	}
	imageTokens := imageTokensCost(text, cols, max(1, numCols), imageCountCap, shrinkWidth, maxCharsPerImage, rp)
	textTokens := float64(jsLen(text)) / cpt
	burnImageSide := 0.0
	if isFinitePositive(priorWarm) {
		burnImageSide = priorWarm * (CacheCreateRate - CacheReadRate)
	}
	burnTextSide := 0.0
	if isFinitePositive(priorWarmImage) {
		burnTextSide = priorWarmImage * (CacheCreateRate - CacheReadRate)
	}
	return imageTokens+burnImageSide < textTokens+burnTextSide
}

func IsCompressionProfitableAmortized(text string, cols, imageCountCap, numCols int, charsPerToken float64, horizon int, priorWarmTokens, priorWarmImageTokens float64, shrinkWidth bool, maxCharsPerImage int, rp renderParams) bool {
	if horizon <= 1 {
		return IsCompressionProfitable(text, cols, imageCountCap, numCols, charsPerToken, priorWarmTokens, priorWarmImageTokens, shrinkWidth, maxCharsPerImage, rp)
	}
	if text == "" {
		return false
	}
	if maxCharsPerImage == 0 {
		maxCharsPerImage = ReadableCharsPerImage
	}
	cpt := charsPerToken
	if !isFinitePositive(cpt) {
		cpt = CharsPerToken
	}
	n := max(1, numCols)
	imageTokens := imageTokensCost(text, cols, n, imageCountCap, shrinkWidth, maxCharsPerImage, rp)
	textTokens := float64(jsLen(text)) / cpt
	N := max(2, horizon)
	imageLifetime := imageTokens * (CacheCreateRate + CacheReadRate*float64(N-1))
	textLifetime := textTokens * CacheReadRate * float64(N)
	burnImageSide := 0.0
	if isFinitePositive(priorWarmTokens) {
		burnImageSide = priorWarmTokens * (CacheCreateRate - CacheReadRate)
	}
	burnTextSide := 0.0
	if isFinitePositive(priorWarmImageTokens) {
		burnTextSide = priorWarmImageTokens * (CacheCreateRate - CacheReadRate)
	}
	return imageLifetime+burnImageSide < textLifetime+burnTextSide
}

func isFinitePositive(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v > 0
}

func MaxCharsPerImage(cols int) int {
	return min(cols*LinesPerImage, ReadableCharsPerImage)
}
