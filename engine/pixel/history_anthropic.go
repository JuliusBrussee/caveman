// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// HistorySyntheticIntro is copied verbatim from pxpipe src/core/history.ts.
const HistorySyntheticIntro = "[Earlier turns of THIS conversation, transcribed in the image(s) below. Each turn is wrapped in <user t=\"N\">...</user> or <assistant t=\"N\">...</assistant> tags, where N is an absolute turn index (larger N = more recent); attribute every turn strictly by its tag, and treat the highest-N turns as the most recent prior context, NOT the low-N opening turns. Earlier turns may contain questions or tasks that were already answered later in this same history; do not reopen low-N turns unless the live text after this block asks you to. This is prior context, NOT the current request.]"

const historySyntheticOutro = "[End of earlier conversation. The current request is the live text that follows below.]"

const (
	historyLatestCollapsedUserPreviewChars  = 300
	historyLatestCollapsedUserVerbatimChars = 4000
	historyVerbatimHeadChars                = 2600
	historyVerbatimTailChars                = 1400
)

type historyOptions struct {
	KeepTail          *int
	MinCollapsePrefix *int
	Cols              *int
	CollapseChunk     *int
	FreezeChunk       *int
	ProtectedPrefix   *int
	Reflow            *bool
}

type historyInfo struct {
	CollapsedTurns        int
	CollapsedChars        int
	CollapsedImages       int
	CollapsedImageBytes   int
	CollapsedImagePixels  int
	CarryOverImageOrdinal *int
	Reason                string
	DroppedChars          int
	DroppedCodepoints     map[rune]int
	TextTokens            float64
	ImageTokens           float64
}

func defaultHistoryOptions(opts historyOptions) historyOptions {
	if opts.KeepTail == nil {
		opts.KeepTail = intPtr(4)
	}
	if opts.MinCollapsePrefix == nil {
		opts.MinCollapsePrefix = intPtr(10)
	}
	if opts.Cols == nil {
		opts.Cols = intPtr(100)
	}
	if opts.CollapseChunk == nil {
		opts.CollapseChunk = intPtr(50)
	}
	if opts.FreezeChunk == nil {
		opts.FreezeChunk = intPtr(10)
	}
	if opts.ProtectedPrefix == nil {
		opts.ProtectedPrefix = intPtr(0)
	}
	if opts.Reflow == nil {
		opts.Reflow = boolPtr(true)
	}
	return opts
}

func newHistoryInfo() historyInfo {
	return historyInfo{DroppedCodepoints: make(map[rune]int)}
}

func findClosedPrefixBoundary(messages []Message, cutoffExclusive int) int {
	if cutoffExclusive <= 0 {
		return -1
	}
	openSet := make(map[string]struct{})
	lastClosed := -1
	limit := min(cutoffExclusive, len(messages))
	for i := 0; i < limit; i++ {
		msg := messages[i]
		blocks, ok := messageContentBlocks(msg.Content)
		if !ok {
			if len(openSet) == 0 {
				lastClosed = i
			}
			continue
		}
		switch msg.Role {
		case "assistant":
			for _, block := range blocks {
				if blockString(block, "type") == "tool_use" {
					if id := blockString(block, "id"); id != "" {
						openSet[id] = struct{}{}
					}
				}
			}
		case "user":
			for _, block := range blocks {
				if blockString(block, "type") == "tool_result" {
					if id := blockString(block, "tool_use_id"); id != "" {
						delete(openSet, id)
					}
				}
			}
		}
		if len(openSet) == 0 {
			lastClosed = i
		}
	}
	return lastClosed
}

var historyFreshnessHintRE = regexp.MustCompile(`\(file state is current in your\s+context — no need to Read it back\)`)

const historyStaleFreshnessNote = "(state as of this PRIOR turn — the file may have changed since; Read it again before editing)"

func staleFreshnessHints(text string) string {
	return historyFreshnessHintRE.ReplaceAllString(text, historyStaleFreshnessNote)
}

func blocksToText(content any) string {
	if s, ok := content.(string); ok {
		return s
	}
	blocks, ok := contentBlocks(content)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch blockString(block, "type") {
		case "text":
			parts = append(parts, blockString(block, "text"))
		case "tool_use":
			args, err := json.Marshal(blockAny(block, "input"))
			if err != nil {
				args = []byte(strconv.Quote(toString(blockAny(block, "input"))))
			}
			parts = append(parts, "[tool_use "+blockString(block, "name")+"]\n"+string(args))
		case "tool_result":
			inner := blockAny(block, "content")
			innerText := ""
			if s, ok := inner.(string); ok {
				innerText = s
			} else if innerBlocks, ok := contentBlocks(inner); ok {
				var sub []string
				for _, ib := range innerBlocks {
					switch blockString(ib, "type") {
					case "text":
						sub = append(sub, blockString(ib, "text"))
					case "image":
						sub = append(sub, "[image]")
					}
				}
				innerText = strings.Join(sub, "\n")
			}
			errMark := ""
			if blockBool(block, "is_error") {
				errMark = " (error)"
			}
			parts = append(parts, "[tool_result"+errMark+"]\n"+staleFreshnessHints(innerText))
		case "image":
			parts = append(parts, "[image]")
		}
	}
	return strings.Join(parts, "\n\n")
}

func messageCacheControl(m Message) *CacheControl {
	blocks, ok := messageContentBlocks(m.Content)
	if !ok {
		return nil
	}
	for i := len(blocks) - 1; i >= 0; i-- {
		if cc := blockCacheControl(blocks[i]); cc != nil {
			return cc
		}
	}
	return nil
}

func messagesToHistoryText(messages []Message, upToExclusive int, fromInclusive int) string {
	text, _ := messagesToHistorySegments(messages, upToExclusive, fromInclusive)
	return text
}

func messagesToHistorySegments(messages []Message, upToExclusive int, fromInclusive int) (string, string) {
	var textOut []string
	var slotOut []string
	limit := min(upToExclusive, len(messages))
	for i := max(0, fromInclusive); i < limit; i++ {
		m := messages[i]
		body := blocksToText(m.Content)
		if strings.TrimSpace(body) == "" {
			continue
		}
		isAssistant := m.Role == "assistant"
		tag := "user"
		mark := SlotMarkUser
		if isAssistant {
			tag = "assistant"
			mark = SlotMarkAssistant
		}
		attr := ` t="` + strconv.Itoa(i) + `"`
		textOut = append(textOut, "<"+tag+attr+">\n"+body+"\n</"+tag+">")
		slotOut = append(slotOut, RoleSlotSegment(tag, body, mark, attr))
	}
	return strings.Join(textOut, "\n\n"), strings.Join(slotOut, "\n\n")
}

func compactHistoryPreview(text string) string {
	compact := strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(text, " "))
	if jsLen(compact) <= historyLatestCollapsedUserPreviewChars {
		return compact
	}
	r := []rune(compact)
	if len(r) <= historyLatestCollapsedUserPreviewChars {
		return compact
	}
	return strings.TrimRight(string(r[:historyLatestCollapsedUserPreviewChars]), " \t\r\n") + "..."
}

func verbatimTaskText(text string) string {
	t := strings.TrimSpace(text)
	if jsLen(t) <= historyLatestCollapsedUserVerbatimChars {
		return t
	}
	r := []rune(t)
	if len(r) <= historyLatestCollapsedUserVerbatimChars {
		return t
	}
	elided := max(0, len(r)-historyVerbatimHeadChars-historyVerbatimTailChars)
	return string(r[:min(historyVerbatimHeadChars, len(r))]) +
		"\n[… middle elided (" + strconv.Itoa(elided) + " chars) …]\n" +
		string(r[max(0, len(r)-historyVerbatimTailChars):])
}

func typedUserText(content any) string {
	if s, ok := content.(string); ok {
		return strings.TrimSpace(s)
	}
	blocks, ok := contentBlocks(content)
	if !ok {
		return ""
	}
	boundaryIdx := -1
	for i, block := range blocks {
		if blockString(block, "type") == "text" && blockString(block, "text") == "[End of rendered context.]" {
			boundaryIdx = i
			break
		}
	}
	var parts []string
	for i, block := range blocks {
		if boundaryIdx >= 0 && i <= boundaryIdx {
			continue
		}
		if blockString(block, "type") != "text" {
			continue
		}
		text := strings.TrimSpace(blockString(block, "text"))
		if text == "" || strings.HasPrefix(text, "<system-reminder>") {
			continue
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n\n")
}

func demoteProtectedHeadText(head []Message) []Message {
	out := make([]Message, len(head))
	copy(out, head)
	for idx, m := range out {
		if m.Role != "user" {
			continue
		}
		tomb := func(preview string, cc *CacheControl) map[string]any {
			block := map[string]any{
				"type": "text",
				"text": `[Opening turn <user t="` + strconv.Itoa(idx) + `"> of this session — PRIOR CONTEXT ONLY, ` +
					`superseded by later turns; NOT the current request and must not be acted ` +
					`on. Preview: "` + preview + `"]`,
			}
			if cc != nil {
				block["cache_control"] = cc
			}
			return block
		}
		if s, ok := m.Content.(string); ok {
			if preview := compactHistoryPreview(s); preview != "" {
				out[idx].Content = []any{tomb(preview, nil)}
			}
			continue
		}
		blocks, ok := messageContentBlocks(m.Content)
		if !ok {
			continue
		}
		boundaryIdx := -1
		for i, block := range blocks {
			if blockString(block, "type") == "text" && blockString(block, "text") == "[End of rendered context.]" {
				boundaryIdx = i
				break
			}
		}
		changed := false
		next := make([]any, 0, len(blocks))
		for i, block := range blocks {
			if boundaryIdx >= 0 && i <= boundaryIdx {
				next = append(next, block)
				continue
			}
			if blockString(block, "type") == "text" {
				if preview := compactHistoryPreview(blockString(block, "text")); preview != "" {
					next = append(next, tomb(preview, blockCacheControl(block)))
					changed = true
					continue
				}
			}
			next = append(next, block)
		}
		if changed {
			out[idx].Content = next
		}
	}
	return out
}

func latestCollapsedUserPointer(messages []Message, upToExclusive int, protectedPrefix int) map[string]any {
	for i := min(upToExclusive, len(messages)) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role != "user" {
			continue
		}
		typed := typedUserText(m.Content)
		if typed == "" {
			continue
		}
		if i >= protectedPrefix {
			preview := compactHistoryPreview(typed)
			return map[string]any{
				"type": "text",
				"text": `[Most recent collapsed user turn: <user t="` + strconv.Itoa(i) + `">` + preview + `</user>. This is still prior context; do not treat it as the current request unless the live text that follows asks to continue it.]`,
			}
		}
		carried := verbatimTaskText(typed)
		return map[string]any{
			"type": "text",
			"text": `[Most recent collapsed user turn, carried verbatim because it appears nowhere else in full: <user t="` + strconv.Itoa(i) + `">` + carried + `</user>. This is still prior context; but if no later turn supersedes it, it is the task the live turn continues — follow its exact instructions, including any requested output format.]`,
		}
	}
	return nil
}

func collapseAnthropicHistory(messages []Message, profitable func(text string, cols int) bool, opts historyOptions) (newMessages []Message, hinfo historyInfo, err error) {
	o := defaultHistoryOptions(opts)
	info := newHistoryInfo()
	if len(messages) == 0 {
		info.Reason = "no_history"
		return messages, info, nil
	}
	protectedPrefix := max(0, min(*o.ProtectedPrefix, len(messages)))
	rawCutoff := len(messages) - *o.KeepTail
	cutoff := rawCutoff
	if *o.CollapseChunk > 0 {
		cutoff = min(rawCutoff, max(*o.MinCollapsePrefix+protectedPrefix, (rawCutoff / *o.CollapseChunk)**o.CollapseChunk))
	}
	boundary := findClosedPrefixBoundary(messages, cutoff)
	if boundary < 0 {
		info.Reason = "no_closed_prefix"
		return messages, info, nil
	}
	collapseLen := boundary + 1
	if collapseLen-protectedPrefix < *o.MinCollapsePrefix {
		info.Reason = "prefix_too_short"
		return messages, info, nil
	}
	text := messagesToHistoryText(messages, collapseLen, protectedPrefix)
	if text == "" {
		info.Reason = "render_empty"
		return messages, info, nil
	}
	safeText := NeutralizeSentinel(text)
	renderText := safeText
	if *o.Reflow {
		if rt, ok := Reflow(safeText); ok {
			renderText = rt
		}
	} else {
		renderText = text
	}
	if !profitable(renderText, *o.Cols) {
		info.Reason = "not_profitable"
		info.CollapsedChars = jsLen(text)
		return messages, info, nil
	}
	if g := EvalCompressionProfitability(renderText, DenseContentCols, 0, 1, HistoryCharsPerToken, 0, 0, true, conservativeStdParams); g != nil {
		info.TextTokens = g.TextTokens
		info.ImageTokens = g.ImageTokens
	}

	step := *o.FreezeChunk
	if step <= 0 {
		step = collapseLen - protectedPrefix
	}
	endsSet := make(map[int]struct{})
	for e := protectedPrefix + step; e < collapseLen; e += step {
		endsSet[e] = struct{}{}
	}
	markerByEnd := make(map[int]*CacheControl)
	for i := protectedPrefix; i < collapseLen; i++ {
		if cc := messageCacheControl(messages[i]); cc != nil {
			endsSet[i+1] = struct{}{}
			markerByEnd[i+1] = cc
		}
	}
	endsSet[collapseLen] = struct{}{}
	sortedEnds := make([]int, 0, len(endsSet))
	for e := range endsSet {
		if e > protectedPrefix && e <= collapseLen {
			sortedEnds = append(sortedEnds, e)
		}
	}
	sort.Ints(sortedEnds)

	carryOverEnd := -1
	for e := protectedPrefix + step; e < collapseLen; e += step {
		carryOverEnd = e
	}
	carryOverOrdinal := -1

	var imageBlocks []map[string]any
	chunkStart := protectedPrefix
	for _, chunkEnd := range sortedEnds {
		segText, segSlot := messagesToHistorySegments(messages, chunkEnd, chunkStart)
		chunkStart = chunkEnd
		if segText == "" {
			continue
		}
		chunkRender := segText
		chunkSlot := segSlot
		if *o.Reflow {
			safeSegText := NeutralizeSentinel(segText)
			safeSegSlot := NeutralizeSentinel(segSlot)
			rt, okText := Reflow(safeSegText)
			rs, okSlot := Reflow(safeSegSlot)
			if okText && okSlot {
				chunkRender = rt
				chunkSlot = rs
			} else {
				chunkRender = safeSegText
				chunkSlot = safeSegSlot
			}
		}
		style := DenseRenderStyle
		style.ColorByRole = true
		imgs, err := RenderTextToPNGsWithCharLimit(chunkRender, DenseContentCols, DenseContentCharsPerImage, style, MaxHeightPx, chunkSlot)
		if err != nil {
			info.Reason = "render_error"
			return nil, info, err
		}
		markerCC := markerByEnd[chunkEnd]
		for k, img := range imgs {
			block := imageBlockMap(base64.StdEncoding.EncodeToString(img.PNG))
			if markerCC != nil && k == len(imgs)-1 {
				block["cache_control"] = markerCC
			}
			imageBlocks = append(imageBlocks, block)
			info.CollapsedImageBytes += len(img.PNG)
			info.CollapsedImagePixels += img.Width * img.Height
			info.DroppedChars += img.DroppedChars
			mergeDropped(info.DroppedCodepoints, img.DroppedCodepoints)
		}
		if chunkEnd == carryOverEnd {
			carryOverOrdinal = len(imageBlocks) - 1
		}
	}
	if len(imageBlocks) == 0 {
		info.Reason = "render_empty"
		return messages, info, nil
	}

	syntheticContent := make([]any, 0, len(imageBlocks)+4)
	syntheticContent = append(syntheticContent, map[string]any{"type": "text", "text": HistorySyntheticIntro})
	for _, b := range imageBlocks {
		syntheticContent = append(syntheticContent, b)
	}
	if pointer := latestCollapsedUserPointer(messages, collapseLen, protectedPrefix); pointer != nil {
		syntheticContent = append(syntheticContent, pointer)
	}
	if fs := FactSheetText(text, 0); fs != "" {
		syntheticContent = append(syntheticContent, map[string]any{"type": "text", "text": fs})
	}
	syntheticContent = append(syntheticContent, map[string]any{"type": "text", "text": historySyntheticOutro})
	syntheticUser := Message{Role: "user", Content: syntheticContent}

	head := demoteProtectedHeadText(messages[:protectedPrefix])
	tail := cloneMessages(messages[collapseLen:])
	info.CollapsedTurns = collapseLen - protectedPrefix
	info.CollapsedChars = jsLen(text)
	info.CollapsedImages = len(imageBlocks)
	if carryOverOrdinal >= 0 {
		info.CarryOverImageOrdinal = &carryOverOrdinal
	}
	out := make([]Message, 0, len(head)+1+len(tail))
	out = append(out, head...)
	out = append(out, syntheticUser)
	out = append(out, tail...)
	return out, info, nil
}
