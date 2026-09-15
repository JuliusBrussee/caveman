// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

var (
	compactSlabBlankRunRE = regexp.MustCompile(`\n{3,}`)
	jsonObjectHeadRE      = regexp.MustCompile(`^\{\s*("|\})`)
	jsonArrayHeadRE       = regexp.MustCompile(`^\[\s*("|\{|\[|-?\d|true\b|false\b|null\b|\])`)
	diffHeadRE            = regexp.MustCompile(`^---\s+\S`)
	logLineRE             = regexp.MustCompile(`^(\[?(DEBUG|INFO|WARN|WARNING|ERROR|TRACE|FATAL)\]?\b|\d{4}-\d{2}-\d{2}[T ]?|\d{2}:\d{2}:\d{2}\b)`)
)

func CountVisualRows(text string, cols int) int {
	cols = max(1, cols)
	rows := 0
	lineLen := 0
	for _, r := range text {
		if r == '\n' {
			rows += max(1, int(math.Ceil(float64(lineLen)/float64(cols))))
			lineLen = 0
			continue
		}
		if r == NLSentinel {
			lineLen++
			rows += max(1, int(math.Ceil(float64(lineLen)/float64(cols))))
			lineLen = 0
			continue
		}
		if r > 0xFFFF {
			lineLen += 2
		} else {
			lineLen++
		}
	}
	rows += max(1, int(math.Ceil(float64(lineLen)/float64(cols))))
	return rows
}

func EstimateImageCount(text string, cols, numCols, maxCharsPerImage int, rp renderParams) int {
	n := max(1, numCols)
	if maxCharsPerImage <= 0 {
		maxCharsPerImage = ReadableCharsPerImage
	}
	// Rows-per-image comes from the shared layer-aware seam, so at max a page
	// holds 2× the lines and this estimate matches the gate and the renderer.
	linesPerImage := rp.imageLineCapacity(cols, n, maxCharsPerImage)
	charBudget := max(1, maxCharsPerImage*n)
	rows := CountVisualRows(text, cols)
	return max(1,
		int(math.Ceil(float64(rows)/float64(linesPerImage))),
		int(math.Ceil(float64(jsLen(text))/float64(charBudget))),
	)
}

func ClassifyContent(text string) string {
	head := text
	if len(head) > 4096 {
		head = head[:4096]
	}
	trimmed := strings.TrimLeft(head, " \t\r\n")
	switch {
	case strings.HasPrefix(trimmed, "{") && jsonObjectHeadRE.MatchString(trimmed):
		return "structured"
	case strings.HasPrefix(trimmed, "[") && jsonArrayHeadRE.MatchString(trimmed):
		return "structured"
	case strings.HasPrefix(trimmed, "---\n") || strings.HasPrefix(trimmed, "---\r\n"):
		return "structured"
	case strings.HasPrefix(trimmed, "diff --git ") || diffHeadRE.MatchString(trimmed):
		return "structured"
	}
	var lines []string
	for _, line := range strings.Split(head, "\n") {
		if len(lines) >= 40 {
			break
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) < 4 {
		return "other"
	}
	hits := 0
	for _, line := range lines {
		if logLineRE.MatchString(line) {
			hits++
		}
	}
	if float64(hits)/float64(len(lines)) >= 0.3 {
		return "log"
	}
	return "other"
}

func TruncateForBudget(text string, maxImages, cols, numCols, maxCharsPerImage int, rp renderParams) (out string, omittedChars int, truncated bool) {
	n := max(1, numCols)
	if maxImages < 1 {
		maxImages = 1
	}
	if maxCharsPerImage <= 0 {
		maxCharsPerImage = DenseContentCharsPerImage
	}
	estImages := EstimateImageCount(text, cols, n, maxCharsPerImage, rp)
	if estImages <= maxImages {
		return text, 0, false
	}
	// Same layer-aware per-image line budget the estimate and the renderer use, so
	// a max tool result gets its full 2-layer capacity instead of half.
	totalRowBudget := max(8, maxImages*rp.imageLineCapacity(cols, n, maxCharsPerImage)-6)
	totalCharBudget := max(128, maxImages*maxCharsPerImage*n-512)
	shape := ClassifyContent(text)
	nlChar := "\n"
	if !strings.Contains(text, "\n") {
		nlChar = string(NLSentinel)
	}
	lines := strings.Split(text, nlChar)
	originalLines := len(lines)
	originalChars := jsLen(text)

	if shape == "structured" {
		rows, chars, cut := 0, 0, 0
		for i, line := range lines {
			r := lineRows(line, cols)
			c := jsLen(line)
			if i > 0 {
				c++
			}
			if rows+r > totalRowBudget || chars+c > totalCharBudget {
				break
			}
			rows += r
			chars += c
			cut = i + 1
		}
		if cut == 0 {
			cut = 1
		}
		head := strings.Join(lines[:cut], nlChar)
		omitted := originalChars - jsLen(head)
		return head + buildPagingMarker(originalChars, originalLines, estImages, cut, 0, originalLines-cut, omitted), omitted, true
	}

	headRowBudget := int(math.Floor(float64(totalRowBudget) * 0.6))
	tailRowBudget := totalRowBudget - headRowBudget
	headCharBudget := int(math.Floor(float64(totalCharBudget) * 0.6))
	tailCharBudget := totalCharBudget - headCharBudget
	headRows, headChars, headCut := 0, 0, 0
	for i, line := range lines {
		r := lineRows(line, cols)
		c := jsLen(line)
		if i > 0 {
			c++
		}
		if headRows+r > headRowBudget || headChars+c > headCharBudget {
			break
		}
		headRows += r
		headChars += c
		headCut = i + 1
	}
	if headCut == 0 {
		headCut = 1
	}
	tailRows, tailChars, tailStart := 0, 0, len(lines)
	for i := len(lines) - 1; i >= headCut; i-- {
		r := lineRows(lines[i], cols)
		c := jsLen(lines[i])
		if i < len(lines)-1 {
			c++
		}
		if tailRows+r > tailRowBudget || tailChars+c > tailCharBudget {
			break
		}
		tailRows += r
		tailChars += c
		tailStart = i
	}
	if tailStart <= headCut || tailStart >= len(lines) {
		head := strings.Join(lines[:headCut], nlChar)
		omitted := originalChars - jsLen(head)
		return head + buildPagingMarker(originalChars, originalLines, estImages, headCut, 0, originalLines-headCut, omitted), omitted, true
	}
	headText := strings.Join(lines[:headCut], nlChar)
	tailText := strings.Join(lines[tailStart:], nlChar)
	shownChars := jsLen(headText) + jsLen(tailText)
	omitted := originalChars - shownChars
	return headText +
		buildPagingMarker(originalChars, originalLines, estImages, headCut, len(lines)-tailStart, originalLines-headCut-(len(lines)-tailStart), omitted) +
		tailText, omitted, true
}

func lineRows(line string, cols int) int {
	return max(1, int(math.Ceil(float64(jsLen(line))/float64(max(1, cols)))))
}

func buildPagingMarker(originalChars, originalLines, originalEstImages, shownHeadLines, shownTailLines, omittedLines, omittedChars int) string {
	tailNote := " Showing first " + strconv.Itoa(shownHeadLines) + " lines (tail elided)."
	if shownTailLines > 0 {
		tailNote = " Showing first " + strconv.Itoa(shownHeadLines) + " lines and last " + strconv.Itoa(shownTailLines) + " lines."
	}
	return "\n\n[ pxpipe paging: omitted " + commaInt(omittedLines) + " lines (" + commaInt(omittedChars) +
		" chars) of content here. Original length: " + commaInt(originalChars) + " chars (" +
		commaInt(originalLines) + " lines, ~" + strconv.Itoa(originalEstImages) + " images)." + tailNote + " ]\n\n"
}

func commaInt(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return "-" + commaInt(-n)
	}
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre == 0 {
		pre = 3
	}
	b.WriteString(s[:pre])
	for i := pre; i < len(s); i += 3 {
		b.WriteByte(',')
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

func CompactSlabWhitespace(text string) string {
	if text == "" {
		return text
	}
	var b strings.Builder
	lineStart := 0
	for i := 0; i <= len(text); i++ {
		if i == len(text) || text[i] == '\n' {
			end := i
			for end > lineStart {
				c := text[end-1]
				if c != ' ' && c != '\t' {
					break
				}
				end--
			}
			b.WriteString(text[lineStart:end])
			if i < len(text) {
				b.WriteByte('\n')
			}
			lineStart = i + 1
		}
	}
	return compactSlabBlankRunRE.ReplaceAllString(b.String(), "\n\n")
}
