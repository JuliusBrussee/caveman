// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

type RenderStyle struct {
	Grid        bool
	GridCols    int
	MarkerScale int
	MarkerRed   bool
	CellHBonus  int
	CellWBonus  int
	AA          bool
	ColorCycle  bool
	ColorByRole bool
	// PitchY overrides the vertical line pitch in px. Zero uses the default
	// (glyph height plus CellHBonus). A value below glyph height makes rows
	// interpenetrate; the drawing path blends dark-ink-wins so both rows render.
	PitchY int
	// Zebra colours each visual row from a 3-colour cycle so row membership
	// survives tight pitch. It reuses the colour-cycle mask path.
	Zebra bool
}

const (
	MaxHeightPx               = 728
	ReadableCharsPerImage     = 28080
	DenseContentCharsPerImage = 28080
	DenseContentCols          = 312
	DefaultCols               = 312
	PadX                      = 4
	PadY                      = 4
	CellW                     = 5
	CellH                     = 8
	NLSentinel                = '↵'
	NLSentinelLiteral         = '⏎'
	GutterCells               = 4
	MaxWidthPx                = 1568
)

const (
	tabWidth             = 4
	defaultCellWBonus    = 0
	defaultCellHBonus    = 0
	gridInk              = 25
	gutterDividerInk     = 64
	gutterDividerInsetPx = 2
	SlotMarkUser         = "\x01"
	SlotMarkAssistant    = "\x02"
	slotNeutral          = "\x03"
)

var DenseRenderStyle = RenderStyle{AA: true}

var trailingRenderWhitespaceRE = regexp.MustCompile(`[ \t]+$`)
var blankRunRenderRE = regexp.MustCompile(`\n{4,}`)

var glyphPalette = [][3]uint8{
	{20, 20, 20},
	{20, 40, 160},
	{150, 20, 20},
	{20, 110, 40},
}

var rolePalette = [][3]uint8{
	{20, 120, 50},
	{30, 70, 180},
}

// zebraPalette cycles per visual row so row membership survives tight pitch.
// Order: dark red, dark blue, black (row % 3) at pitch 6.
var zebraPalette = [][3]uint8{
	{165, 15, 15},
	{15, 30, 175},
	{15, 15, 15},
}

func cellsFor(cp rune, markerScale int) int {
	if cp == NLSentinel && markerScale > 1 {
		return markerScale
	}
	rank := AtlasRank(cp)
	if rank < 0 {
		return 1
	}
	if atlasWideFlags[rank] == 1 {
		return 2
	}
	return 1
}

func jsLen(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func runeLen(s string) int {
	return utf8.RuneCountInString(s)
}

func MinifyForRender(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = trailingRenderWhitespaceRE.ReplaceAllString(line, "")
	}
	return blankRunRenderRE.ReplaceAllString(strings.Join(lines, "\n"), "\n\n\n")
}

func Reflow(text string) (string, bool) {
	if strings.ContainsRune(text, NLSentinel) {
		return "", false
	}
	lines := strings.Split(MinifyForRender(text), "\n")
	for i, line := range lines {
		lines[i] = ExpandTabsInLine(line)
	}
	return strings.Join(lines, string(NLSentinel)), true
}

func Dereflow(s string) string {
	return strings.ReplaceAll(s, string(NLSentinel), "\n")
}

func NeutralizeSentinel(s string) string {
	return strings.ReplaceAll(s, string(NLSentinel), string(NLSentinelLiteral))
}

func ExpandTabsInLine(line string) string {
	if !strings.ContainsRune(line, '\t') {
		return line
	}
	var b strings.Builder
	col := 0
	for _, r := range line {
		if r == '\t' {
			span := tabWidth - (col % tabWidth)
			b.WriteRune('→')
			if span > 1 {
				b.WriteString(strings.Repeat(" ", span-1))
			}
			col += span
			continue
		}
		b.WriteRune(r)
		col += cellsFor(r, 1)
	}
	return b.String()
}

func MeasureLineCols(line string, markerScale int) int {
	if markerScale < 1 {
		markerScale = 1
	}
	w := 0
	for _, r := range line {
		w += cellsFor(r, markerScale)
	}
	return w
}

func MeasureContentCols(text string, maxCols, markerScale int) int {
	if maxCols < 1 {
		maxCols = 1
	}
	if markerScale < 1 {
		markerScale = 1
	}
	widest := 1
	for _, line := range strings.Split(text, "\n") {
		w := MeasureLineCols(ExpandTabsInLine(line), markerScale)
		if w > widest {
			widest = w
		}
		if widest >= maxCols {
			return maxCols
		}
	}
	return min(maxCols, widest)
}

func WrapLines(text string, cols, markerScale int) []string {
	if cols < 1 {
		cols = 1
	}
	if markerScale < 1 {
		markerScale = 1
	}
	var out []string
	for _, rawWithTabs := range strings.Split(MinifyForRender(text), "\n") {
		raw := ExpandTabsInLine(rawWithTabs)
		if raw == "" {
			out = append(out, "")
			continue
		}
		var cur strings.Builder
		curCols := 0
		for _, r := range raw {
			w := cellsFor(r, markerScale)
			if curCols+w > cols {
				out = append(out, cur.String())
				cur.Reset()
				cur.WriteRune(r)
				curCols = w
			} else {
				cur.WriteRune(r)
				curCols += w
			}
		}
		if cur.Len() > 0 {
			out = append(out, cur.String())
		}
	}
	return out
}

func SlotCopyBody(body string) string {
	if !strings.Contains(body, SlotMarkUser) && !strings.Contains(body, SlotMarkAssistant) {
		return body
	}
	body = strings.ReplaceAll(body, SlotMarkUser, slotNeutral)
	return strings.ReplaceAll(body, SlotMarkAssistant, slotNeutral)
}

func RoleSlotSegment(tag, body, mark string, attr string) string {
	open := "<" + tag + attr + ">"
	close := "</" + tag + ">"
	return strings.Repeat(mark, runeLen(open)) + "\n" + SlotCopyBody(body) + "\n" + strings.Repeat(mark, runeLen(close))
}

func splitWrappedLinesIntoReadablePages(lines []string, maxLines, maxChars int) [][]string {
	lineLimit := max(1, maxLines)
	charLimit := max(1, maxChars)
	var pages [][]string
	var cur []string
	curChars := 0
	for _, line := range lines {
		lineChars := jsLen(line)
		if len(cur) > 0 {
			lineChars++
		}
		if len(cur) > 0 && (len(cur) >= lineLimit || curChars+lineChars > charLimit) {
			pages = append(pages, cur)
			cur = nil
			curChars = 0
		}
		cur = append(cur, line)
		curChars += jsLen(line)
		if len(cur) > 1 {
			curChars++
		}
	}
	if len(cur) > 0 {
		pages = append(pages, cur)
	}
	if len(pages) == 0 {
		return [][]string{{}}
	}
	return pages
}

func readableLinesPerColumn(cols int) int {
	return max(1, ReadableCharsPerImage/max(1, cols))
}

func blitGlyph(fb []uint8, fbW, x, y int, cp rune, markerMask []uint8) int {
	rank := AtlasRank(cp)
	if rank < 0 {
		return 0
	}
	wide := atlasWideFlags[rank] == 1
	srcW := AtlasCellW
	if wide {
		srcW = 2 * AtlasCellW
	}
	srcOff := int(atlasOffsets[rank])
	for gy := 0; gy < AtlasCellH; gy++ {
		dstRow := (y+gy)*fbW + x
		bitRowStart := srcOff + gy*srcW
		for gx := 0; gx < srcW; gx++ {
			bitIdx := bitRowStart + gx
			b := atlasPixels[bitIdx>>3]
			bit := (b >> (7 - (bitIdx & 7))) & 1
			if bit != 0 {
				idx := dstRow + gx
				fb[idx] = 255
				if markerMask != nil {
					markerMask[idx] = 1
				}
			}
		}
	}
	if wide {
		return 2
	}
	return 1
}

func blitGlyphGray(fb []uint8, fbW, x, y int, cp rune) int {
	rank := AtlasGrayRank(cp)
	if rank < 0 {
		return 0
	}
	wide := atlasGrayWideFlags[rank] == 1
	srcW := AtlasGrayCellW
	if wide {
		srcW = 2 * AtlasGrayCellW
	}
	srcOff := int(atlasGrayOffsets[rank])
	for gy := 0; gy < AtlasGrayCellH; gy++ {
		dstRow := (y+gy)*fbW + x
		srcRow := srcOff + gy*srcW
		for gx := 0; gx < srcW; gx++ {
			coverage := atlasGrayPixels[srcRow+gx]
			if coverage > 0 {
				idx := dstRow + gx
				if coverage > fb[idx] {
					fb[idx] = coverage
				}
			}
		}
	}
	if wide {
		return 2
	}
	return 1
}

func blitGlyphScaled(fb, markerMask []uint8, fbW, fbH, x, y int, cp rune, scaleX int) int {
	rank := AtlasRank(cp)
	if rank < 0 {
		return 0
	}
	wide := atlasWideFlags[rank] == 1
	srcW := AtlasCellW
	if wide {
		srcW = 2 * AtlasCellW
	}
	srcOff := int(atlasOffsets[rank])
	for gy := 0; gy < AtlasCellH; gy++ {
		py := y + gy
		if py >= fbH {
			break
		}
		bitRowStart := srcOff + gy*srcW
		for gx := 0; gx < srcW; gx++ {
			bitIdx := bitRowStart + gx
			b := atlasPixels[bitIdx>>3]
			if ((b >> (7 - (bitIdx & 7))) & 1) == 0 {
				continue
			}
			for sx := 0; sx < scaleX; sx++ {
				px := x + gx*scaleX + sx
				if px >= fbW {
					break
				}
				idx := py*fbW + px
				fb[idx] = 255
				if markerMask != nil {
					markerMask[idx] = 1
				}
			}
		}
	}
	if wide {
		return 2 * scaleX
	}
	return scaleX
}

func drawGrid(fb []uint8, fbW, fbH, rows, gridCols, cellH, cellW, glyphH int) {
	for row := 0; row < rows; row++ {
		y := PadY + row*cellH + (glyphH - 1)
		if y >= fbH {
			break
		}
		rowStart := y * fbW
		for x := 0; x < fbW; x++ {
			if fb[rowStart+x] == 0 {
				fb[rowStart+x] = gridInk
			}
		}
	}
	if gridCols > 0 {
		for col := gridCols; ; col += gridCols {
			x := PadX + col*cellW
			if x >= fbW-PadX {
				break
			}
			for y := 0; y < fbH; y++ {
				idx := y*fbW + x
				if fb[idx] == 0 {
					fb[idx] = gridInk
				}
			}
		}
	}
}

func slotForMark(r rune) int {
	switch r {
	case 0x0001:
		return 1
	case 0x0002:
		return 2
	default:
		return 0
	}
}

func RenderChunkToPNG(text string, cols int, style RenderStyle, maxHeightPx int, slotText string) (RenderedImage, error) {
	if cols == 0 {
		cols = DefaultCols
	}
	if cols < 1 {
		cols = 1
	}
	if maxHeightPx == 0 {
		maxHeightPx = MaxHeightPx
	}
	useAA := style.AA
	atlasH := AtlasCellH
	atlasW := AtlasCellW
	if useAA {
		atlasH = AtlasGrayCellH
		atlasW = AtlasGrayCellW
	}
	markerScale := max(1, style.MarkerScale)
	// glyphH is the drawn glyph height; pitch is the vertical line advance. When
	// pitch < glyphH rows interpenetrate (the drawing path blends dark-ink-wins).
	glyphH := atlasH
	pitch := atlasH + max(0, style.CellHBonus+defaultCellHBonus)
	if style.PitchY > 0 {
		pitch = style.PitchY
	}
	cellW := max(1, atlasW+style.CellWBonus+defaultCellWBonus)
	lines := WrapLines(text, cols, markerScale)
	var slotLines []string
	if style.ColorByRole {
		slotLines = WrapLines(slotText, cols, markerScale)
	}
	maxLines := max(1, (maxHeightPx-2*PadY-glyphH)/pitch+1)
	fitLines := lines
	if len(fitLines) > maxLines {
		fitLines = fitLines[:maxLines]
	}
	fitSlotLines := slotLines
	if len(fitSlotLines) > maxLines {
		fitSlotLines = fitSlotLines[:maxLines]
	}
	charsRendered := 0
	if len(fitLines) == len(lines) {
		charsRendered = runeLen(text)
	} else {
		for _, line := range fitLines {
			charsRendered += runeLen(line)
		}
		charsRendered += max(0, len(fitLines)-1)
	}
	width := 2*PadX + cols*cellW + max(0, atlasW-cellW)
	// Reference page height: 2*pad + (rows-1)*pitch + glyphH, so the final row
	// keeps its full glyph height even when intermediate rows advance by pitch.
	height := 2 * PadY
	if len(fitLines) > 0 {
		height += (len(fitLines)-1)*pitch + glyphH
	}
	fb := make([]uint8, width*height)
	var markerMask []uint8
	if style.MarkerRed {
		markerMask = make([]uint8, width*height)
	}
	useColorCycle := style.ColorCycle
	useColorByRole := style.ColorByRole
	useZebra := style.Zebra
	var colorMask []uint8
	if useColorCycle || useColorByRole || useZebra {
		colorMask = make([]uint8, width*height)
	}
	dropped := make(map[rune]int)
	droppedChars := 0
	glyphIndex := 0
	for row, line := range fitLines {
		baseY := PadY + row*pitch
		col := 0
		var slotRow []rune
		if fitSlotLines != nil && row < len(fitSlotLines) {
			slotRow = []rune(fitSlotLines[row])
		}
		charIdx := 0
		for _, r := range line {
			if col >= cols {
				break
			}
			baseX := PadX + col*cellW
			isMarker := r == NLSentinel
			colorSlot := 0
			if useColorByRole {
				if charIdx < len(slotRow) {
					colorSlot = slotForMark(slotRow[charIdx])
				}
			} else if useZebra {
				colorSlot = (row % len(zebraPalette)) + 1
			} else {
				colorSlot = (glyphIndex % len(glyphPalette)) + 1
			}
			advance := 0
			if isMarker && markerScale > 1 {
				advance = blitGlyphScaled(fb, markerMask, width, height, baseX, baseY, r, markerScale)
				paintColorMask(colorMask, fb, width, height, baseX, baseY, advance*cellW, atlasH, colorSlot)
			} else if useAA {
				advance = blitGlyphGray(fb, width, baseX, baseY, r)
				if advance > 0 {
					paintColorMask(colorMask, fb, width, height, baseX, baseY, advance*atlasW, atlasH, colorSlot)
				}
			} else {
				var mark []uint8
				if isMarker {
					mark = markerMask
				}
				advance = blitGlyph(fb, width, baseX, baseY, r, mark)
				if advance > 0 {
					paintColorMask(colorMask, fb, width, height, baseX, baseY, advance*atlasW, atlasH, colorSlot)
				}
			}
			glyphIndex++
			charIdx++
			if advance == 0 {
				droppedChars++
				dropped[r]++
				col++
			} else {
				col += advance
			}
		}
	}
	if style.Grid {
		drawGrid(fb, width, height, len(fitLines), max(0, style.GridCols), pitch, cellW, atlasH)
	}
	for i := range fb {
		fb[i] = 255 - fb[i]
	}
	var pngBytes []byte
	var err error
	if colorMask != nil {
		palette := glyphPalette
		if useColorByRole {
			palette = rolePalette
		} else if useZebra {
			palette = zebraPalette
		}
		pngBytes, err = encodeRGBPNG(renderColorMaskRGB(fb, colorMask, palette), width, height)
	} else if markerMask != nil {
		rgb := make([]uint8, width*height*3)
		for i, g := range fb {
			if markerMask[i] == 1 && g < 128 {
				rgb[i*3] = 220
				rgb[i*3+1] = 0
				rgb[i*3+2] = 0
			} else {
				rgb[i*3] = g
				rgb[i*3+1] = g
				rgb[i*3+2] = g
			}
		}
		pngBytes, err = encodeRGBPNG(rgb, width, height)
	} else {
		pngBytes, err = encodeGrayPNG(fb, width, height)
	}
	if err != nil {
		return RenderedImage{}, err
	}
	return RenderedImage{
		PNG:               pngBytes,
		Width:             width,
		Height:            height,
		CharsRendered:     charsRendered,
		DroppedChars:      droppedChars,
		DroppedCodepoints: dropped,
	}, nil
}

func paintColorMask(colorMask, fb []uint8, width, height, baseX, baseY, w, h, slot int) {
	if colorMask == nil {
		return
	}
	for gy := 0; gy < h; gy++ {
		py := baseY + gy
		if py >= height {
			break
		}
		for gx := 0; gx < w; gx++ {
			px := baseX + gx
			if px >= width {
				break
			}
			idx := py*width + px
			if fb[idx] > 0 {
				colorMask[idx] = uint8(slot)
			}
		}
	}
}

func renderColorMaskRGB(fb, colorMask []uint8, palette [][3]uint8) []uint8 {
	rgb := make([]uint8, len(fb)*3)
	for i, g := range fb {
		slot := colorMask[i]
		if slot > 0 {
			coverage := float64(255 - g)
			p := palette[(int(slot)-1)%len(palette)]
			rgb[i*3] = uint8(math.Round(255 - coverage*float64(255-p[0])/255))
			rgb[i*3+1] = uint8(math.Round(255 - coverage*float64(255-p[1])/255))
			rgb[i*3+2] = uint8(math.Round(255 - coverage*float64(255-p[2])/255))
		} else {
			rgb[i*3] = g
			rgb[i*3+1] = g
			rgb[i*3+2] = g
		}
	}
	return rgb
}

func RenderTextToPNGs(text string, cols int, style RenderStyle) ([]RenderedImage, error) {
	return RenderTextToPNGsWithCharLimit(text, cols, ReadableCharsPerImage, style, MaxHeightPx, "")
}

func RenderTextToPNGsWithCharLimit(text string, cols, maxCharsPerImage int, style RenderStyle, maxHeightPx int, slotText string) ([]RenderedImage, error) {
	if cols == 0 {
		cols = DefaultCols
	}
	if cols < 1 {
		cols = 1
	}
	if maxCharsPerImage == 0 {
		maxCharsPerImage = ReadableCharsPerImage
	}
	if maxHeightPx == 0 {
		maxHeightPx = MaxHeightPx
	}
	markerScale := max(1, style.MarkerScale)
	glyphH := AtlasCellH
	pitch := AtlasCellH + max(0, style.CellHBonus+defaultCellHBonus)
	if style.PitchY > 0 {
		pitch = style.PitchY
	}
	lines := WrapLines(text, cols, markerScale)
	var slotLines []string
	if style.ColorByRole {
		slotLines = WrapLines(slotText, cols, markerScale)
	}
	hardLinesPerImg := max(1, (maxHeightPx-2*PadY-glyphH)/pitch+1)
	linesPerImg := min(hardLinesPerImg, max(1, maxCharsPerImage/cols))
	images := make([]RenderedImage, 0)
	slotCursor := 0
	for _, page := range splitWrappedLinesIntoReadablePages(lines, linesPerImg, maxCharsPerImage) {
		chunk := strings.Join(page, "\n")
		slotChunk := ""
		if slotLines != nil {
			end := min(len(slotLines), slotCursor+len(page))
			slotChunk = strings.Join(slotLines[slotCursor:end], "\n")
		}
		slotCursor += len(page)
		img, err := RenderChunkToPNG(chunk, cols, style, maxHeightPx, slotChunk)
		if err != nil {
			return nil, err
		}
		images = append(images, img)
	}
	return images, nil
}

func MultiColWidth(cols, numCols int) int {
	n := max(1, numCols)
	return 2*PadX + n*cols*CellW + (n-1)*GutterCells*CellW
}

func MaxFittingCols(cols int) int {
	n := 1
	for MultiColWidth(cols, n+1) <= MaxWidthPx {
		n++
	}
	return n
}

func renderMultiColChunkFromLines(lines []string, cols, numCols, charsCovered, linesPerCol int) (RenderedImage, error) {
	width := MultiColWidth(cols, numCols)
	rowsPerCol := max(1, linesPerCol)
	usedRows := min(len(lines), rowsPerCol)
	if usedRows < 1 {
		usedRows = 1
	}
	height := 2*PadY + usedRows*CellH
	fb := make([]uint8, width*height)
	dropped := make(map[rune]int)
	droppedChars := 0
	colStride := cols*CellW + GutterCells*CellW
	for c := 0; c < numCols; c++ {
		colBaseX := PadX + c*colStride
		colStart := c * rowsPerCol
		if colStart >= len(lines) {
			break
		}
		colEnd := min(colStart+rowsPerCol, len(lines))
		for r := 0; r < colEnd-colStart; r++ {
			line := lines[colStart+r]
			baseY := PadY + r*CellH
			col := 0
			for _, cp := range line {
				if col >= cols {
					break
				}
				baseX := colBaseX + col*CellW
				advance := blitGlyph(fb, width, baseX, baseY, cp, nil)
				if advance == 0 {
					droppedChars++
					dropped[cp]++
					col++
				} else {
					col += advance
				}
			}
		}
	}
	if numCols >= 2 {
		gutterPxPerSide := GutterCells * CellW
		yStart := gutterDividerInsetPx
		yEnd := height - gutterDividerInsetPx
		for c := 0; c < numCols-1; c++ {
			colEndX := PadX + c*colStride + cols*CellW
			dividerX := colEndX + gutterPxPerSide/2
			for y := yStart; y < yEnd; y++ {
				idx := y*width + dividerX
				if fb[idx] == 0 {
					fb[idx] = gutterDividerInk
				}
			}
		}
	}
	for i := range fb {
		fb[i] = 255 - fb[i]
	}
	pngBytes, err := encodeGrayPNG(fb, width, height)
	if err != nil {
		return RenderedImage{}, err
	}
	return RenderedImage{
		PNG:               pngBytes,
		Width:             width,
		Height:            height,
		CharsRendered:     charsCovered,
		DroppedChars:      droppedChars,
		DroppedCodepoints: dropped,
	}, nil
}

func RenderTextToPNGsMultiCol(text string, cols, numCols int) ([]RenderedImage, error) {
	if cols == 0 {
		cols = DefaultCols
	}
	if numCols <= 1 {
		return RenderTextToPNGs(text, cols, RenderStyle{})
	}
	if MultiColWidth(cols, numCols) > MaxWidthPx {
		numCols = MaxFittingCols(cols)
		if numCols <= 1 {
			return RenderTextToPNGs(text, cols, RenderStyle{})
		}
	}
	lines := WrapLines(text, cols, 1)
	hardLinesPerImg := max(1, (MaxHeightPx-2*PadY)/CellH)
	linesPerImg := min(hardLinesPerImg, readableLinesPerColumn(cols))
	linesPerImage := linesPerImg * numCols
	totalChars := runeLen(text)
	images := make([]RenderedImage, 0)
	coveredChars := 0
	pages := splitWrappedLinesIntoReadablePages(lines, linesPerImage, ReadableCharsPerImage*max(1, numCols))
	for i, page := range pages {
		isLast := i == len(pages)-1
		chars := 0
		if isLast {
			chars = max(0, totalChars-coveredChars)
		} else {
			for _, line := range page {
				chars += runeLen(line)
			}
			chars += max(0, len(page)-1)
		}
		coveredChars += chars
		img, err := renderMultiColChunkFromLines(page, cols, numCols, chars, linesPerImg)
		if err != nil {
			return nil, err
		}
		images = append(images, img)
	}
	return images, nil
}

type DensePagesOptions struct {
	Cols             int
	Shrink           *bool
	MultiCol         int
	Reflow           bool
	MaxCharsPerImage int
	Style            *RenderStyle
	MaxHeightPx      int
}

func RenderDensePages(text string, opts DensePagesOptions) ([]RenderedImage, error) {
	source := text
	if opts.Reflow {
		if packed, ok := Reflow(text); ok {
			source = packed
		}
	}
	maxCols := opts.Cols
	if maxCols == 0 {
		maxCols = DenseContentCols
	}
	maxCols = max(1, maxCols)
	shrink := true
	if opts.Shrink != nil {
		shrink = *opts.Shrink
	}
	cols := maxCols
	if shrink {
		cols = MeasureContentCols(source, maxCols, 1)
	}
	requestedCols := opts.MultiCol
	if requestedCols == 0 {
		requestedCols = max(1, MaxFittingCols(cols))
	} else {
		requestedCols = max(1, requestedCols)
	}
	numCols := requestedCols
	if cols < maxCols {
		numCols = 1
	}
	style := DenseRenderStyle
	if opts.Style != nil {
		style = *opts.Style
	}
	maxChars := opts.MaxCharsPerImage
	if maxChars == 0 {
		maxChars = DenseContentCharsPerImage
	}
	maxHeight := opts.MaxHeightPx
	if maxHeight == 0 {
		maxHeight = MaxHeightPx
	}
	if numCols > 1 {
		return RenderTextToPNGsMultiCol(source, cols, numCols)
	}
	return RenderTextToPNGsWithCharLimit(source, cols, maxChars, style, maxHeight, "")
}
