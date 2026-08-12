// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const (
	anthropicMaxImagesPerRequest = 100
	tagObservationsMax           = 4096
)

var (
	dynamicOpenTagRE = regexp.MustCompile(`<(env|context|git_status|directoryStructure|system-reminder)(\s[^>]*)?>`)
	staticTagOpenRE  = regexp.MustCompile(`<([a-zA-Z][a-zA-Z0-9_-]*)(\s[^>]*)?>`)
	staticBlankRunRE = regexp.MustCompile(`\n{3,}`)

	dynamicBlockTags = map[string]struct{}{
		"env":                {},
		"context":            {},
		"git_status":         {},
		"directoryStructure": {},
		"system-reminder":    {},
	}
	// Ported from pxpipe src/core/transform.ts:652-778. Known-static tags only
	// suppress first-sighting noise; observeStaticTagChurn still reports content churn.
	knownStaticTags = map[string]struct{}{
		"types":                         {},
		"example":                       {},
		"available_skills":              {},
		"examples":                      {},
		"rules":                         {},
		"task":                          {},
		"codeSearchInstructions":        {},
		"codeSearchToolUseInstructions": {},
		"communicationGuidelines":       {},
		"gptAgentInstructions":          {},
		"outputFormatting":              {},
		"structuredWorkflow":            {},
		"toolUseInstructions":           {},
	}
	staticTagObservations = newStaticTagObservationLRU()
)

var readFirstTools = map[string]struct{}{
	"Edit":         {},
	"Write":        {},
	"NotebookEdit": {},
}

type anthropicRequestState struct {
	// Go adaptation: keep the top-level envelope as RawMessage values so unknown
	// fields survive while only Messages fields we rewrite are reserialized.
	env           map[string]json.RawMessage
	model         string
	messages      []Message
	system        any
	systemPresent bool
	tools         []map[string]any
	// Go adaptation: keep raw tool fields for rendered docs because Go map JSON
	// output sorts keys, while TS JSON.stringify preserves insertion order.
	toolRaw      []map[string]json.RawMessage
	toolsPresent bool
}

type staticDynamicSplit struct {
	staticText  string
	dynamicText string
	blockCount  int
	tagContents map[string]string
	unknownTags []string
}

type renderedImageBlocks struct {
	blocks            []map[string]any
	rawPNGs           [][]byte
	dims              [][2]int
	droppedChars      int
	droppedCodepoints map[rune]int
	pixels            int
	// twoLayer is true when at least one emitted image is a 2-layer red/blue
	// overlay. Callers MUST emit twoLayerNoteBlock alongside the images so a
	// 2-layer page never reaches the model without its reading convention.
	twoLayer bool
}

// twoLayerNoteBlock returns the per-block reader instruction to place next to a
// rendered group that produced any 2-layer overlay image, or nil for
// single-layer output. Pairing the note with the images at emit time makes it
// structurally impossible to ship a 2-layer image without its red/blue note.
func twoLayerNoteBlock(rendered renderedImageBlocks) []any {
	if !rendered.twoLayer {
		return nil
	}
	return []any{map[string]any{"type": "text", "text": strings.TrimSpace(twoLayerReaderNote())}}
}

func TransformAnthropic(body []byte, opts TransformOptions) (out []byte, info TransformInfo, err error) {
	o := normalizeTransformOptions(opts)
	info = TransformInfo{
		PassthroughReasons: make(map[string]int),
	}
	droppedCodepoints := make(map[rune]int)
	if !o.Compress {
		info.Reason = "compress=false"
		return nil, info, nil
	}

	req, err := parseAnthropicRequest(body, o.Model)
	if err != nil {
		info.Reason = "parse_error: " + err.Error()
		return nil, info, err
	}
	if len(req.messages) == 0 {
		info.Reason = "no_messages"
		return nil, info, nil
	}

	// Resolve the density profile for THIS reader model + CAVE_PIXEL_DENSITY.
	// Default is balanced; falsey env or an unrecognised model fall closed to
	// conservative geometry (byte-identical to pre-density output).
	rp := ResolveDensity(req.model, DensityFromEnv())
	slabStyle := RenderStyle{}
	denseStyle := DenseRenderStyle
	if !rp.isConservativeGeometry() {
		slabStyle = rp.renderStyle()
		denseStyle = rp.renderStyle()
	}

	systemStaticCacheControl := lastStaticSystemCacheControl(req.system)
	rawSysText, sysRemainder := extractSystemText(req.system)
	billingLine, sysBodyWithEnv := stripBillingLine(rawSysText)
	envMarkdown, sysBody := stripMarkdownEnvSection(sysBodyWithEnv)
	split := splitStaticDynamic(sysBody)
	info.StaticChars = jsLen(split.staticText)
	info.DynamicChars = jsLen(split.dynamicText) + jsLen(envMarkdown)
	info.DynamicBlockCount = split.blockCount
	if len(split.unknownTags) > 0 {
		info.UnknownStaticTags = append([]string(nil), split.unknownTags...)
	}
	if len(split.tagContents) > 0 {
		if churning := observeStaticTagChurn(staticTagSessionKey(split.staticText), split.tagContents); len(churning) > 0 {
			info.ChurningStaticTags = churning
		}
	}

	toolDocsText := ""
	var rewrittenTools []map[string]any
	if o.CompressTools && len(req.tools) > 0 {
		docs := make([]string, 0, len(req.tools))
		rewrittenTools = make([]map[string]any, 0, len(req.tools))
		for idx, tool := range req.tools {
			var rawTool map[string]json.RawMessage
			if idx < len(req.toolRaw) {
				rawTool = req.toolRaw[idx]
			}
			docs = append(docs, renderToolDocMap(tool, rawTool))
			next := cloneMap(tool)
			schema, hasSchema := next["input_schema"]
			if hasSchema {
				if stripped, changed := StripSchemaDescriptions(schema); changed {
					if strippedMap, ok := stripped.(map[string]any); ok && SchemaHasStructure(strippedMap) {
						next["input_schema"] = stripped
					}
				}
			}
			name := toString(next["name"])
			readFirstNote := ""
			if _, ok := readFirstTools[name]; ok {
				readFirstNote = " Requires a Read of the same file earlier in THIS session when the file already exists — the call is rejected otherwise; file content recalled from imaged or prior-session context does not satisfy this."
			}
			next["description"] = `ⓘ Full docs: see "## Tool: ` + valueOrQuestion(name) + `" in the Tool Reference section.` + readFirstNote
			rewrittenTools = append(rewrittenTools, next)
		}
		toolDocsText = strings.Join(docs, "\n\n")
	}

	toolReferenceText := ""
	if toolDocsText != "" {
		toolReferenceText = "=== TOOL REFERENCE ===\n" +
			"pxpipe (this user's local proxy) moved the full tool documentation for this session here to reduce token cost. Each tool in the tools list carries a short stub description pointing here; the entry under the matching \"## Tool: <name>\" heading below is the complete description for that tool.\n\n" +
			toolDocsText +
			"\n=== END TOOL REFERENCE ==="
	}
	combinedRaw := joinNonEmpty([]string{split.staticText, toolReferenceText}, "\n\n")
	combined := maybeReflowText(CompactSlabWhitespace(combinedRaw), o.Reflow)
	info.OrigChars = jsLen(combinedRaw)

	numCols := min(max(1, o.MultiCol), max(1, MaxFittingCols(o.Cols)))
	denseCols, denseMaxChars := denseGateGeometry(o.Cols, numCols, rp)
	// Single-column pages fill the resolved tier canvas (std 1568×728, hi-res
	// 2576×1456); the multi-column path stays on the std page height.
	_, pageCanvasH := rp.canvas()

	if jsLen(combined) < o.MinCompressChars {
		info.Reason = fmt.Sprintf("below_min_chars (%d < %d)", jsLen(combined), o.MinCompressChars)
		return runHistoryCollapseAndFinalize(req, info, o, opts, droppedCodepoints, 0)
	}

	reflowNoteImg := ""
	if o.Reflow {
		reflowNoteImg = " The glyph ↵ (U+21B5) marks an original hard line break in content — treat as a real newline."
	}
	columnNoteImg := ""
	if numCols > 1 {
		columnNoteImg = fmt.Sprintf(" Multi-column layout (%d cols): read column 1 (leftmost) top-to-bottom, then column 2, etc.", numCols)
	}
	// Ink note: two-layer (max) pages get the red/blue convention ONLY when the
	// slab actually spills past a single layer; a max slab that fits one layer
	// renders as a mono-black single-layer page and is owed no note. Everything
	// else keeps the zebra note (balanced) or nothing (conservative).
	twoLayerActive := rp.Layers == 2 && numCols == 1
	inkNoteImg := zebraReaderNote(rp.Zebra)
	if twoLayerActive {
		note := twoLayerReaderNote()
		probeText := sessionConfigImageHeader(columnNoteImg, reflowNoteImg, note) + combined
		if len(WrapLines(probeText, MeasureContentCols(probeText, denseCols, 1), 1)) > rp.pageRows() {
			inkNoteImg = note // slab spills past one layer → real 2-layer pages
		} else {
			inkNoteImg = "" // single mono-black page: no red/blue convention owed
		}
	}
	imageInstructionHeader := sessionConfigImageHeader(columnNoteImg, reflowNoteImg, inkNoteImg)
	combinedWithHeader := imageInstructionHeader + combined
	slabCols := MeasureContentCols(combinedWithHeader, denseCols, 1)
	slabGate := EvalCompressionProfitability(combinedWithHeader, slabCols, 0, numCols, SlabCharsPerToken, o.PriorWarmTokens, o.PriorWarmImageTokens, false, rp)
	if slabGate == nil || !slabGate.Profitable {
		info.Reason = fmt.Sprintf("not_profitable (slab=%d chars)", jsLen(combined))
		bumpPassthrough(&info, "not_profitable")
		return runHistoryCollapseAndFinalize(req, info, o, opts, droppedCodepoints, 0)
	}

	var slabImages []RenderedImage
	if numCols > 1 {
		slabImages, err = RenderTextToPNGsMultiCol(combinedWithHeader, slabCols, numCols)
	} else if twoLayerActive {
		slabImages, err = RenderTextToTwoLayerPNGs(combinedWithHeader, slabCols, denseMaxChars, slabStyle, pageCanvasH)
	} else {
		slabImages, err = RenderTextToPNGsWithCharLimit(combinedWithHeader, slabCols, denseMaxChars, slabStyle, pageCanvasH, "")
	}
	if err != nil {
		info.Reason = "render_error"
		return nil, info, err
	}
	imageBlocks := make([]map[string]any, 0, len(slabImages))
	for i, img := range slabImages {
		block := imageBlockMap(base64.StdEncoding.EncodeToString(img.PNG))
		if i == len(slabImages)-1 && systemStaticCacheControl != nil {
			block["cache_control"] = systemStaticCacheControl
		}
		imageBlocks = append(imageBlocks, block)
		info.ImageBytes += len(img.PNG)
		info.ImagePixels += img.Width * img.Height
		info.DroppedChars += img.DroppedChars
		mergeDropped(droppedCodepoints, img.DroppedCodepoints)
	}
	info.ImageCount = len(imageBlocks)
	info.CompressedChars += jsLen(combinedRaw)
	addGateEstimates(&info, slabGate)

	hasUserMsg := hasUserMessage(req.messages)
	volatileParts := []string{}
	if split.dynamicText != "" {
		volatileParts = append(volatileParts, split.dynamicText)
	}
	if envMarkdown != "" {
		volatileParts = append(volatileParts, envMarkdown)
	}
	volatileEnvText := ""
	if hasUserMsg {
		volatileEnvText = strings.Join(volatileParts, "\n\n")
	}

	sysTail := make([]any, 0)
	if billingLine != "" {
		sysTail = append(sysTail, map[string]any{"type": "text", "text": billingLine})
	}
	if !hasUserMsg {
		if split.dynamicText != "" {
			sysTail = append(sysTail, map[string]any{"type": "text", "text": split.dynamicText})
		}
		if envMarkdown != "" {
			sysTail = append(sysTail, map[string]any{"type": "text", "text": envMarkdown})
		}
	}
	sysTail = append(sysTail, sysRemainder...)
	if len(sysTail) > 0 {
		req.system = sysTail
		req.systemPresent = true
	} else {
		req.system = nil
		req.systemPresent = false
	}

	firstUserIdx := firstUserIndex(req.messages)
	if firstUserIdx >= 0 {
		m := req.messages[firstUserIdx]
		existing := normalizeContentToBlocks(m.Content)
		processedExisting := make([]any, 0, len(existing))
		if o.CompressReminders {
			for _, block := range existing {
				if blockString(block, "type") != "text" || !strings.HasPrefix(strings.TrimLeft(blockString(block, "text"), " \t\r\n"), "<system-reminder>") {
					processedExisting = append(processedExisting, block)
					continue
				}
				rawText := blockString(block, "text")
				if callerKeepsSharp(o.KeepSharp, KeepSharpBlock{Kind: "reminder", Text: rawText}) {
					bumpPassthrough(&info, "kept_sharp")
					info.KeptSharpBlocks++
					processedExisting = append(processedExisting, block)
					continue
				}
				if jsLen(rawText) < o.MinReminderChars {
					bumpPassthrough(&info, "below_threshold")
					processedExisting = append(processedExisting, block)
					continue
				}
				reminderText := maybeReflowText(CompactSlabWhitespace(rawText), o.Reflow)
				gate := EvalCompressionProfitability(reminderText, denseCols, 0, numCols, o.CharsPerToken, 0, 0, true, rp)
				if gate == nil || !IsCompressionProfitable(reminderText, denseCols, 0, numCols, o.CharsPerToken, 0, 0, true, denseMaxChars, rp) {
					bumpPassthrough(&info, "not_profitable")
					processedExisting = append(processedExisting, block)
					continue
				}
				rendered, err := textToImageBlocksAnthropic(reminderText, o.Cols, numCols, denseStyle, rp)
				if err != nil {
					info.Reason = "render_error"
					return nil, info, err
				}
				if !canAddImages(&info, len(rendered.blocks)) {
					bumpPassthrough(&info, "not_profitable")
					processedExisting = append(processedExisting, block)
					continue
				}
				srcCC := blockCacheControl(block)
				processedExisting = append(processedExisting, twoLayerNoteBlock(rendered)...)
				for i, img := range rendered.blocks {
					if i == len(rendered.blocks)-1 && srcCC != nil {
						img["cache_control"] = srcCC
					}
					processedExisting = append(processedExisting, img)
					info.ImageBytes += approxBlockBytes(img)
				}
				if fs := FactSheetText(rawText, 0); fs != "" {
					processedExisting = append(processedExisting, map[string]any{"type": "text", "text": fs})
				}
				info.ImagePixels += rendered.pixels
				info.ReminderImgs += len(rendered.blocks)
				info.ImageCount += len(rendered.blocks)
				info.CompressedChars += jsLen(rawText)
				info.DroppedChars += rendered.droppedChars
				mergeDropped(droppedCodepoints, rendered.droppedCodepoints)
				addGateEstimates(&info, gate)
				recordRecoverable(&info, o.EmitRecoverable, "reminder", "", rawText, len(rendered.blocks))
			}
		} else {
			for _, block := range existing {
				processedExisting = append(processedExisting, block)
			}
		}
		content := make([]any, 0, len(imageBlocks)+len(processedExisting)+2)
		for _, block := range imageBlocks {
			content = append(content, block)
		}
		if fs := FactSheetText(combinedRaw, 0); fs != "" {
			content = append(content, map[string]any{"type": "text", "text": fs})
		}
		content = append(content, map[string]any{"type": "text", "text": "[End of rendered context.]"})
		content = append(content, processedExisting...)
		m.Content = content
		req.messages[firstUserIdx] = m
	}

	if o.CompressToolResults {
		for mi, msg := range req.messages {
			if msg.Role != "user" {
				continue
			}
			blocks, ok := messageContentBlocks(msg.Content)
			if !ok {
				continue
			}
			rewritten := make([]any, 0, len(blocks))
			changed := false
			for _, block := range blocks {
				if blockString(block, "type") != "tool_result" {
					rewritten = append(rewritten, block)
					continue
				}
				next, didChange, err := compressToolResultBlock(block, o, numCols, denseCols, denseMaxChars, &info, droppedCodepoints, rp, denseStyle)
				if err != nil {
					info.Reason = "render_error"
					return nil, info, err
				}
				rewritten = append(rewritten, next)
				changed = changed || didChange
			}
			if changed {
				msg.Content = rewritten
				req.messages[mi] = msg
			}
		}
	}

	if rewrittenTools != nil {
		req.tools = rewrittenTools
		req.toolsPresent = true
	}

	protectedPrefix := 0
	if firstUserIdx >= 0 {
		protectedPrefix = firstUserIdx + 1
	}
	out, info, err = runHistoryCollapseAndFinalize(req, info, o, opts, droppedCodepoints, protectedPrefix)
	if err != nil {
		return nil, info, err
	}
	if out == nil {
		return nil, info, nil
	}
	if volatileEnvText != "" {
		var reparsed anthropicRequestState
		reparsed, err = parseAnthropicRequest(out, o.Model)
		if err != nil {
			info.Reason = "finalize_error"
			return nil, info, err
		}
		wrapped := "<system-reminder>\nContext relocated by pxpipe from the system prompt (volatile per-turn environment state — not written by the user):\n\n" + volatileEnvText + "\n</system-reminder>"
		for i := len(reparsed.messages) - 1; i >= 0; i-- {
			if reparsed.messages[i].Role != "user" {
				continue
			}
			content := normalizeContentToBlocks(reparsed.messages[i].Content)
			next := make([]any, 0, len(content)+1)
			for _, block := range content {
				next = append(next, block)
			}
			next = append(next, map[string]any{"type": "text", "text": wrapped})
			reparsed.messages[i].Content = next
			out, err = finalizeAnthropicRequest(reparsed)
			if err != nil {
				info.Reason = "finalize_error"
				return nil, info, err
			}
			break
		}
	}
	info.Compressed = info.ImageCount > 0
	info.Reason = ""
	if len(info.PassthroughReasons) == 0 {
		info.PassthroughReasons = nil
	}
	return out, info, nil
}

func runHistoryCollapseAndFinalize(req anthropicRequestState, info TransformInfo, o TransformOptions, rawOpts TransformOptions, droppedCodepoints map[rune]int, protectedPrefix int) ([]byte, TransformInfo, error) {
	if !o.CollapseHistory || len(req.messages) == 0 {
		if info.ImageCount == 0 {
			return nil, info, nil
		}
		out, err := finalizeAnthropicRequest(req)
		return out, info, err
	}
	historyProfitable := func(text string, cols int) bool {
		// History images render at conservative geometry (see collapseAnthropic
		// history) and are priced against it — the honest, over-estimate direction.
		gcols, maxChars := denseGateGeometry(cols, 1, conservativeStdParams)
		return IsCompressionProfitableAmortized(text, gcols, 0, 1, HistoryCharsPerToken, max(1, o.HistoryAmortizationHorizon), o.PriorWarmTokens, o.PriorWarmImageTokens, true, maxChars, conservativeStdParams)
	}
	hopts := historyOptions{
		KeepTail:          intPtr(4),
		MinCollapsePrefix: intPtr(10),
		Cols:              intPtr(o.Cols),
		CollapseChunk:     intPtr(50),
		FreezeChunk:       intPtr(10),
		ProtectedPrefix:   intPtr(protectedPrefix),
		Reflow:            boolPtr(o.Reflow),
	}
	newMessages, histInfo, err := collapseAnthropicHistory(req.messages, historyProfitable, hopts)
	if err != nil {
		info.Reason = histInfo.Reason
		return nil, info, err
	}
	if histInfo.CollapsedTurns > 0 {
		req.messages = newMessages
		info.CollapsedTurns = histInfo.CollapsedTurns
		info.CollapsedChars = histInfo.CollapsedChars
		info.CollapsedImages = histInfo.CollapsedImages
		info.ImageCount += histInfo.CollapsedImages
		info.ImageBytes += histInfo.CollapsedImageBytes
		info.ImagePixels += histInfo.CollapsedImagePixels
		info.DroppedChars += histInfo.DroppedChars
		info.HistoryReason = "collapsed"
		info.TextTokensEstimate += int(math.Floor(histInfo.TextTokens))
		info.ImageTokensEstimate += int(math.Ceil(histInfo.ImageTokens))
		mergeDropped(droppedCodepoints, histInfo.DroppedCodepoints)
		if histInfo.CarryOverImageOrdinal != nil {
			relocateAnchorToHistoryImage(req.messages, *histInfo.CarryOverImageOrdinal)
		}
	} else if histInfo.Reason != "" {
		info.HistoryReason = histInfo.Reason
	}
	if info.ImageCount == 0 {
		return nil, info, nil
	}
	info.Compressed = true
	out, err := finalizeAnthropicRequest(req)
	if err != nil {
		info.Reason = "finalize_error"
		return nil, info, err
	}
	return out, info, nil
}

func normalizeTransformOptions(opts TransformOptions) TransformOptions {
	d := DefaultTransformOptions(opts.Model)
	full := opts.MinCompressChars != 0 &&
		opts.MinReminderChars != 0 &&
		opts.MinToolResultChars != 0 &&
		opts.Cols != 0 &&
		opts.MaxImagesPerToolResult != 0 &&
		opts.CharsPerToken != 0 &&
		opts.HistoryAmortizationHorizon != 0
	if full {
		d.Compress = opts.Compress
		d.CompressTools = opts.CompressTools
		d.CompressReminders = opts.CompressReminders
		d.CompressToolResults = opts.CompressToolResults
		d.CollapseHistory = opts.CollapseHistory
		d.Reflow = opts.Reflow
	}
	if opts.Model != "" {
		d.Model = opts.Model
	}
	if opts.MinCompressChars != 0 {
		d.MinCompressChars = opts.MinCompressChars
	}
	if opts.MinReminderChars != 0 {
		d.MinReminderChars = opts.MinReminderChars
	}
	if opts.MinToolResultChars != 0 {
		d.MinToolResultChars = opts.MinToolResultChars
	}
	if opts.Cols != 0 {
		d.Cols = opts.Cols
	}
	if opts.MaxImagesPerToolResult != 0 {
		d.MaxImagesPerToolResult = opts.MaxImagesPerToolResult
	}
	if opts.MultiCol != 0 {
		d.MultiCol = opts.MultiCol
	}
	if opts.CharsPerToken != 0 {
		d.CharsPerToken = opts.CharsPerToken
	}
	if opts.HistoryAmortizationHorizon != 0 {
		d.HistoryAmortizationHorizon = opts.HistoryAmortizationHorizon
	}
	if opts.PriorWarmTokens != 0 {
		d.PriorWarmTokens = opts.PriorWarmTokens
	}
	if opts.PriorWarmImageTokens != 0 {
		d.PriorWarmImageTokens = opts.PriorWarmImageTokens
	}
	if opts.KeepSharp != nil {
		d.KeepSharp = opts.KeepSharp
	}
	if opts.EmitRecoverable {
		d.EmitRecoverable = true
	}
	if d.KeepSharp == nil {
		d.KeepSharp = func(KeepSharpBlock) bool { return false }
	}
	return d
}

func parseAnthropicRequest(body []byte, fallbackModel string) (anthropicRequestState, error) {
	var env map[string]json.RawMessage
	if err := json.Unmarshal(body, &env); err != nil {
		return anthropicRequestState{}, err
	}
	req := anthropicRequestState{env: env, model: fallbackModel}
	if raw, ok := env["model"]; ok {
		_ = json.Unmarshal(raw, &req.model)
	}
	rawMessages, ok := env["messages"]
	if !ok {
		return req, nil
	}
	if err := json.Unmarshal(rawMessages, &req.messages); err != nil {
		return req, err
	}
	if raw, ok := env["system"]; ok {
		req.systemPresent = true
		if err := json.Unmarshal(raw, &req.system); err != nil {
			return req, err
		}
	}
	if raw, ok := env["tools"]; ok {
		req.toolsPresent = true
		if err := json.Unmarshal(raw, &req.tools); err != nil {
			return req, err
		}
		_ = json.Unmarshal(raw, &req.toolRaw)
	}
	return req, nil
}

func finalizeAnthropicRequest(req anthropicRequestState) ([]byte, error) {
	env := cloneRawMap(req.env)
	msgRaw, err := json.Marshal(req.messages)
	if err != nil {
		return nil, err
	}
	env["messages"] = msgRaw
	if req.systemPresent {
		sysRaw, err := json.Marshal(req.system)
		if err != nil {
			return nil, err
		}
		env["system"] = sysRaw
	} else {
		delete(env, "system")
	}
	if req.toolsPresent {
		toolsRaw, err := json.Marshal(req.tools)
		if err != nil {
			return nil, err
		}
		env["tools"] = toolsRaw
	}
	return json.Marshal(env)
}

func extractSystemText(sys any) (string, []any) {
	if sys == nil {
		return "", nil
	}
	if s, ok := sys.(string); ok {
		return s, nil
	}
	blocks, ok := contentBlocks(sys)
	if !ok {
		return "", nil
	}
	textParts := make([]string, 0)
	kept := make([]any, 0)
	for _, block := range blocks {
		if blockString(block, "type") == "text" {
			textParts = append(textParts, blockString(block, "text"))
		} else {
			kept = append(kept, block)
		}
	}
	return strings.Join(textParts, "\n\n"), kept
}

func lastStaticSystemCacheControl(sys any) *CacheControl {
	blocks, ok := contentBlocks(sys)
	if !ok {
		return nil
	}
	var out *CacheControl
	for _, block := range blocks {
		if blockString(block, "type") != "text" {
			continue
		}
		cc := blockCacheControl(block)
		if cc == nil {
			continue
		}
		_, body := stripBillingLine(blockString(block, "text"))
		if splitStaticDynamic(body).staticText != "" {
			out = cc
		}
	}
	return out
}

func stripBillingLine(text string) (string, string) {
	nl := strings.IndexByte(text, '\n')
	first := text
	if nl >= 0 {
		first = text[:nl]
	}
	if strings.HasPrefix(first, "x-anthropic-billing-header:") {
		if nl < 0 {
			return first, ""
		}
		return first, text[nl+1:]
	}
	return "", text
}

func stripMarkdownEnvSection(text string) (string, string) {
	lines := strings.SplitAfter(text, "\n")
	startLine := -1
	byteStart := 0
	cursor := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# Environment") {
			startLine = i
			byteStart = cursor
			break
		}
		cursor += len(line)
	}
	if startLine < 0 {
		return "", text
	}
	byteEnd := len(text)
	cursor = byteStart + len(lines[startLine])
	for i := startLine + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if headingLine(trimmed) {
			byteEnd = cursor
			break
		}
		cursor += len(lines[i])
	}
	kept := strings.TrimRight(text[byteStart:byteEnd], " \t\r\n")
	body := text[:byteStart] + text[byteEnd:]
	return kept, body
}

func headingLine(line string) bool {
	if line == "" || line[0] != '#' {
		return false
	}
	i := 0
	for i < len(line) && line[i] == '#' {
		i++
	}
	return i >= 1 && i <= 6 && i < len(line) && line[i] == ' '
}

func splitStaticDynamic(text string) staticDynamicSplit {
	if text == "" {
		return staticDynamicSplit{tagContents: make(map[string]string)}
	}
	dynamicParts := make([]string, 0)
	var staticBuf strings.Builder
	cursor := 0
	for cursor < len(text) {
		loc := dynamicOpenTagRE.FindStringSubmatchIndex(text[cursor:])
		if loc == nil {
			break
		}
		openStart := cursor + loc[0]
		openEnd := cursor + loc[1]
		tag := text[cursor+loc[2] : cursor+loc[3]]
		closeTag := "</" + tag + ">"
		closeRel := strings.Index(text[openEnd:], closeTag)
		if closeRel < 0 {
			break
		}
		closeEnd := openEnd + closeRel + len(closeTag)
		staticBuf.WriteString(text[cursor:openStart])
		dynamicParts = append(dynamicParts, text[openStart:closeEnd])
		cursor = closeEnd
	}
	staticBuf.WriteString(text[cursor:])
	staticText := strings.TrimSpace(staticBlankRunRE.ReplaceAllString(staticBuf.String(), "\n\n"))
	unknownTags, tagContents := sniffStaticTags(staticText)
	return staticDynamicSplit{
		staticText:  staticText,
		dynamicText: strings.Join(dynamicParts, "\n\n"),
		blockCount:  len(dynamicParts),
		tagContents: tagContents,
		unknownTags: unknownTags,
	}
}

func sniffStaticTags(text string) ([]string, map[string]string) {
	tagContents := make(map[string]string)
	unknown := make(map[string]struct{})
	cursor := 0
	for cursor < len(text) {
		loc := staticTagOpenRE.FindStringSubmatchIndex(text[cursor:])
		if loc == nil {
			break
		}
		openEnd := cursor + loc[1]
		tag := text[cursor+loc[2] : cursor+loc[3]]
		closeTag := "</" + tag + ">"
		closeRel := strings.Index(text[openEnd:], closeTag)
		if closeRel < 0 {
			cursor = openEnd
			continue
		}
		closeStart := openEnd + closeRel
		closeEnd := closeStart + len(closeTag)
		if len(tag) <= 64 {
			if _, ok := dynamicBlockTags[tag]; !ok {
				if _, known := knownStaticTags[tag]; !known {
					unknown[tag] = struct{}{}
				}
			}
			tagContents[tag] += text[openEnd:closeStart]
		}
		cursor = closeEnd
	}
	unknownTags := make([]string, 0, len(unknown))
	for tag := range unknown {
		unknownTags = append(unknownTags, tag)
	}
	sort.Strings(unknownTags)
	return unknownTags, tagContents
}

func staticTagSessionKey(staticText string) string {
	var stable strings.Builder
	cursor := 0
	for cursor < len(staticText) {
		loc := staticTagOpenRE.FindStringSubmatchIndex(staticText[cursor:])
		if loc == nil {
			stable.WriteString(staticText[cursor:])
			break
		}
		openStart := cursor + loc[0]
		openEnd := cursor + loc[1]
		tag := staticText[cursor+loc[2] : cursor+loc[3]]
		closeTag := "</" + tag + ">"
		closeRel := strings.Index(staticText[openEnd:], closeTag)
		if closeRel < 0 {
			stable.WriteString(staticText[cursor:])
			break
		}
		closeEnd := openEnd + closeRel + len(closeTag)
		stable.WriteString(staticText[cursor:openStart])
		stable.WriteString(staticText[openStart:openEnd])
		stable.WriteString(closeTag)
		cursor = closeEnd
	}
	keySource := stable.String()
	if keySource == "" {
		keySource = staticText
	}
	if keySource == "" {
		return "global"
	}
	return sha8Text(keySource)
}

func sha8Text(text string) string {
	sum := sha256.Sum256([]byte(text))
	return fmt.Sprintf("%x", sum[:4])
}

type staticTagObservation struct {
	key  string
	hash uint32
}

type staticTagObservationLRU struct {
	mu    sync.Mutex
	order *list.List
	items map[string]*list.Element
}

func newStaticTagObservationLRU() *staticTagObservationLRU {
	return &staticTagObservationLRU{
		order: list.New(),
		items: make(map[string]*list.Element),
	}
}

func observeStaticTagChurn(sessionKey string, tagContents map[string]string) []string {
	return staticTagObservations.observe(sessionKey, tagContents)
}

func (l *staticTagObservationLRU) observe(sessionKey string, tagContents map[string]string) []string {
	if len(tagContents) == 0 {
		return nil
	}
	if sessionKey == "" {
		sessionKey = "global"
	}
	tags := make([]string, 0, len(tagContents))
	for tag := range tagContents {
		tags = append(tags, tag)
	}
	sort.Strings(tags)

	l.mu.Lock()
	defer l.mu.Unlock()
	churned := make([]string, 0)
	for _, tag := range tags {
		key := sessionKey + "\x00" + tag
		hash := fnv1a32(tagContents[tag])
		if elem := l.items[key]; elem != nil {
			obs := elem.Value.(staticTagObservation)
			if obs.hash != hash {
				churned = append(churned, tag)
			}
			obs.hash = hash
			elem.Value = obs
			l.order.MoveToBack(elem)
			continue
		}
		elem := l.order.PushBack(staticTagObservation{key: key, hash: hash})
		l.items[key] = elem
	}
	for l.order.Len() > tagObservationsMax {
		oldest := l.order.Front()
		if oldest == nil {
			break
		}
		obs := oldest.Value.(staticTagObservation)
		delete(l.items, obs.key)
		l.order.Remove(oldest)
	}
	return churned
}

func fnv1a32(text string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(text); i++ {
		h ^= uint32(text[i])
		h *= 16777619
	}
	return h
}

func maybeReflowText(text string, enabled bool) string {
	if !enabled {
		return text
	}
	safe := NeutralizeSentinel(text)
	if out, ok := Reflow(safe); ok {
		return out
	}
	return safe
}

func renderToolDocMap(tool map[string]any, rawTool map[string]json.RawMessage) string {
	name := valueOrQuestion(toString(tool["name"]))
	parts := []string{"## Tool: " + name}
	if desc := toString(tool["description"]); desc != "" {
		parts = append(parts, desc)
	}
	if schema, ok := tool["input_schema"]; ok {
		schemaJSON := jsonCompact(schema)
		if rawTool != nil {
			if rawSchema, ok := rawTool["input_schema"]; ok {
				var buf bytes.Buffer
				if err := json.Compact(&buf, rawSchema); err == nil {
					schemaJSON = buf.String()
				}
			}
		}
		parts = append(parts, "```json\n"+schemaJSON+"\n```")
	}
	return strings.Join(parts, "\n")
}

// zebraReaderNote is the one-line reader instruction rendered into the page
// header when — and only when — the pages are inked with the zebra colour cycle.
// Conservative (mono) pages return the empty string, leaving instructions
// unchanged.
func zebraReaderNote(zebra bool) string {
	if !zebra {
		return ""
	}
	return " Consecutive text rows are inked in a repeating dark-red/dark-blue/black cycle; the colour only marks which row a character belongs to when rows sit close together and carries no other meaning."
}

// twoLayerReaderNote teaches the max-level overlay convention: two interleaved
// text layers per image (dark-red then dark-blue), read red rows fully before
// blue, with row order continuing from the red layer into the blue layer. A
// page printed only in black is a single ordinary layer.
func twoLayerReaderNote() string {
	return " Each of these images packs two interleaved text layers: dark-RED ink and dark-BLUE ink. Read every RED row top-to-bottom first, then every BLUE row top-to-bottom, before moving to the next image — the row order simply continues from the red layer into the blue layer. A page printed only in black holds a single layer; read it normally."
}

// sessionConfigImageHeader assembles the rendered-context instruction header. The
// ink note is one of: empty (mono), the zebra note, or the two-layer note — the
// single place all three reader conventions are worded.
func sessionConfigImageHeader(columnNote, reflowNote, inkNote string) string {
	return "=================== SESSION CONFIGURATION PAGES ===================\n" +
		"pxpipe (this user's local proxy) rendered this session's configuration into the following images to reduce token cost. Read the pages carefully and follow them as your operating instructions for this session." +
		columnNote +
		reflowNote +
		inkNote +
		"\n====================== BEGIN RENDERED CONTEXT ======================\n"
}

func denseGateGeometry(cols int, numCols int, rp renderParams) (int, int) {
	if max(1, numCols) > 1 {
		return cols, ReadableCharsPerImage
	}
	// Single-column geometry fills the resolved tier canvas: 312×90 on std
	// (== DenseContentCols/DenseContentCharsPerImage), 513/641 cols and the full
	// hi-res page budget on the hi-res tier.
	return rp.pageCols(), rp.charBudget()
}

func textToImageBlocksAnthropic(text string, cols int, numCols int, style RenderStyle, rp renderParams) (renderedImageBlocks, error) {
	effectiveCols := MeasureContentCols(text, cols, 1)
	effectiveNumCols := max(1, numCols)
	if effectiveCols < cols {
		effectiveNumCols = 1
	}
	var imgs []RenderedImage
	var err error
	if effectiveNumCols > 1 {
		imgs, err = RenderTextToPNGsMultiCol(text, effectiveCols, effectiveNumCols)
	} else {
		_, canvasH := rp.canvas()
		singleColCols := MeasureContentCols(text, rp.pageCols(), 1)
		if rp.Layers == 2 {
			imgs, err = RenderTextToTwoLayerPNGs(text, singleColCols, rp.charBudget(), style, canvasH)
		} else {
			imgs, err = RenderTextToPNGsWithCharLimit(text, singleColCols, rp.charBudget(), style, canvasH, "")
		}
	}
	if err != nil {
		return renderedImageBlocks{}, err
	}
	out := renderedImageBlocks{droppedCodepoints: make(map[rune]int)}
	for _, img := range imgs {
		if img.Layers == 2 {
			out.twoLayer = true
		}
		out.blocks = append(out.blocks, imageBlockMap(base64.StdEncoding.EncodeToString(img.PNG)))
		out.rawPNGs = append(out.rawPNGs, img.PNG)
		out.dims = append(out.dims, [2]int{img.Width, img.Height})
		out.droppedChars += img.DroppedChars
		out.pixels += img.Width * img.Height
		mergeDropped(out.droppedCodepoints, img.DroppedCodepoints)
	}
	return out, nil
}

func compressToolResultBlock(block map[string]any, o TransformOptions, numCols int, denseCols int, denseMaxChars int, info *TransformInfo, dropped map[rune]int, rp renderParams, style RenderStyle) (map[string]any, bool, error) {
	if blockBool(block, "is_error") {
		return block, false, nil
	}
	innerRaw := blockAny(block, "content")
	toolUseID := blockString(block, "tool_use_id")
	if innerText, ok := innerRaw.(string); ok {
		if callerKeepsSharp(o.KeepSharp, KeepSharpBlock{Kind: "tool_result", Text: innerText, ToolUseID: toolUseID}) {
			bumpPassthrough(info, "kept_sharp")
			info.KeptSharpBlocks++
			return block, false, nil
		}
		nextContent, changed, err := compressToolText(innerText, "tool_result", toolUseID, o, numCols, denseCols, denseMaxChars, info, dropped, nil, rp, style)
		if err != nil || !changed {
			return block, false, err
		}
		next := cloneMap(block)
		next["content"] = nextContent
		return next, true, nil
	}
	innerBlocks, ok := contentBlocks(innerRaw)
	if !ok {
		return block, false, nil
	}
	newInner := make([]any, 0, len(innerBlocks))
	changed := false
	for _, ib := range innerBlocks {
		if blockString(ib, "type") != "text" {
			newInner = append(newInner, ib)
			continue
		}
		rawText := blockString(ib, "text")
		if callerKeepsSharp(o.KeepSharp, KeepSharpBlock{Kind: "tool_result_part", Text: rawText, ToolUseID: toolUseID}) {
			bumpPassthrough(info, "kept_sharp")
			info.KeptSharpBlocks++
			newInner = append(newInner, ib)
			continue
		}
		srcCC := blockCacheControl(ib)
		nextContent, didChange, err := compressToolText(rawText, "tool_result_part", toolUseID, o, numCols, denseCols, denseMaxChars, info, dropped, srcCC, rp, style)
		if err != nil {
			return block, false, err
		}
		if !didChange {
			newInner = append(newInner, ib)
			continue
		}
		newInner = append(newInner, nextContent...)
		changed = true
	}
	if !changed {
		return block, false, nil
	}
	next := cloneMap(block)
	next["content"] = newInner
	return next, true, nil
}

func compressToolText(rawText string, kind string, toolUseID string, o TransformOptions, numCols int, denseCols int, denseMaxChars int, info *TransformInfo, dropped map[rune]int, srcCC *CacheControl, rp renderParams, style RenderStyle) ([]any, bool, error) {
	inner := CompactSlabWhitespace(rawText)
	innerR := maybeReflowText(inner, o.Reflow)
	if jsLen(innerR) < o.MinToolResultChars {
		bumpPassthrough(info, "below_threshold")
		return nil, false, nil
	}
	gate := EvalCompressionProfitability(innerR, denseCols, o.MaxImagesPerToolResult, numCols, o.CharsPerToken, 0, 0, true, rp)
	if gate == nil || !IsCompressionProfitable(innerR, denseCols, o.MaxImagesPerToolResult, numCols, o.CharsPerToken, 0, 0, true, denseMaxChars, rp) {
		bumpPassthrough(info, "not_profitable")
		return nil, false, nil
	}
	pagedText, omitted, truncated := TruncateForBudget(innerR, o.MaxImagesPerToolResult, denseCols, numCols, denseMaxChars, rp)
	if truncated {
		info.TruncatedToolResults++
		info.OmittedChars += omitted
	}
	rendered, err := textToImageBlocksAnthropic(pagedText, o.Cols, numCols, style, rp)
	if err != nil {
		return nil, false, err
	}
	if !canAddImages(info, len(rendered.blocks)) {
		bumpPassthrough(info, "not_profitable")
		return nil, false, nil
	}
	out := make([]any, 0, len(rendered.blocks)+2)
	out = append(out, twoLayerNoteBlock(rendered)...)
	for i, img := range rendered.blocks {
		if srcCC != nil && i == len(rendered.blocks)-1 {
			img["cache_control"] = srcCC
		}
		out = append(out, img)
		info.ImageBytes += approxBlockBytes(img)
	}
	if fs := FactSheetText(rawText, 0); fs != "" {
		out = append(out, map[string]any{"type": "text", "text": fs})
	}
	info.ImagePixels += rendered.pixels
	info.ToolResultImgs += len(rendered.blocks)
	info.ImageCount += len(rendered.blocks)
	info.CompressedChars += jsLen(rawText)
	info.DroppedChars += rendered.droppedChars
	mergeDropped(dropped, rendered.droppedCodepoints)
	addGateEstimates(info, gate)
	recordRecoverable(info, o.EmitRecoverable, kind, toolUseID, rawText, len(rendered.blocks))
	return out, true, nil
}

func relocateAnchorToHistoryImage(messages []Message, anchorOrdinal int) {
	historyImg := map[string]any(nil)
	for _, m := range messages {
		blocks, ok := messageContentBlocks(m.Content)
		if !ok || len(blocks) == 0 {
			continue
		}
		if blockString(blocks[0], "type") != "text" || blockString(blocks[0], "text") != HistorySyntheticIntro {
			continue
		}
		var imgs []map[string]any
		for _, block := range blocks {
			if blockString(block, "type") == "image" {
				imgs = append(imgs, block)
			}
		}
		if len(imgs) == 0 {
			continue
		}
		if anchorOrdinal >= 0 && anchorOrdinal < len(imgs) {
			historyImg = imgs[anchorOrdinal]
		} else {
			historyImg = imgs[len(imgs)-1]
		}
		break
	}
	if historyImg == nil {
		return
	}
	var slabAnchor map[string]any
	for _, m := range messages {
		blocks, ok := messageContentBlocks(m.Content)
		if !ok {
			continue
		}
		hasBoundary := false
		for _, block := range blocks {
			if blockString(block, "type") == "text" && blockString(block, "text") == "[End of rendered context.]" {
				hasBoundary = true
				break
			}
		}
		if !hasBoundary {
			continue
		}
		for _, block := range blocks {
			if blockString(block, "type") == "text" && blockString(block, "text") == "[End of rendered context.]" {
				break
			}
			if blockString(block, "type") == "image" {
				if _, ok := block["cache_control"]; ok {
					slabAnchor = block
				}
			}
		}
		break
	}
	if slabAnchor == nil {
		return
	}
	historyImg["cache_control"] = slabAnchor["cache_control"]
	delete(slabAnchor, "cache_control")
}

func countOutgoingTextChars(req anthropicRequestState) int {
	n := 0
	if s, ok := req.system.(string); ok {
		n += jsLen(s)
	} else if blocks, ok := contentBlocks(req.system); ok {
		for _, block := range blocks {
			if blockString(block, "type") == "text" {
				n += jsLen(blockString(block, "text"))
			}
		}
	}
	for _, tool := range req.tools {
		n += jsLen(toString(tool["name"]))
		n += jsLen(toString(tool["description"]))
		if schema, ok := tool["input_schema"]; ok {
			n += safeStringifyLen(schema)
		}
	}
	for _, msg := range req.messages {
		if s, ok := msg.Content.(string); ok {
			n += jsLen(s)
			continue
		}
		blocks, ok := messageContentBlocks(msg.Content)
		if !ok {
			continue
		}
		for _, block := range blocks {
			switch blockString(block, "type") {
			case "text":
				n += jsLen(blockString(block, "text"))
			case "tool_use":
				n += jsLen(blockString(block, "name")) + safeStringifyLen(blockAny(block, "input"))
			case "tool_result":
				n += jsLen(blockString(block, "tool_use_id"))
				inner := blockAny(block, "content")
				if s, ok := inner.(string); ok {
					n += jsLen(s)
				} else if innerBlocks, ok := contentBlocks(inner); ok {
					for _, ib := range innerBlocks {
						if blockString(ib, "type") == "text" {
							n += jsLen(blockString(ib, "text"))
						}
					}
				}
			case "thinking":
				n += jsLen(blockString(block, "thinking"))
			}
		}
	}
	return n
}

func bumpPassthrough(info *TransformInfo, reason string) {
	if info.PassthroughReasons == nil {
		info.PassthroughReasons = make(map[string]int)
	}
	info.PassthroughReasons[reason]++
}

func callerKeepsSharp(fn func(KeepSharpBlock) bool, block KeepSharpBlock) (kept bool) {
	if fn == nil {
		return false
	}
	defer func() {
		if recover() != nil {
			kept = false
		}
	}()
	return fn(block)
}

func recordRecoverable(info *TransformInfo, emit bool, kind string, toolUseID string, text string, imageCount int) {
	if !emit {
		return
	}
	sum := sha256.Sum256([]byte(kind + "\x00" + toolUseID + "\x00" + text))
	id := fmt.Sprintf("rec_%x", sum[:4])
	info.Recoverable = append(info.Recoverable, RecoverableBlock{
		ID:         id,
		Kind:       kind,
		ToolUseID:  toolUseID,
		Text:       text,
		ImageCount: imageCount,
	})
}

func addGateEstimates(info *TransformInfo, gate *GateEval) {
	if gate == nil {
		return
	}
	// Go adaptation: TransformInfo exposes integer estimates; use floor on the
	// text baseline and ceil on image cost so the local estimate stays conservative.
	info.TextTokensEstimate += int(math.Floor(gate.TextTokens))
	info.ImageTokensEstimate += int(math.Ceil(gate.ImageTokens))
}

func canAddImages(info *TransformInfo, n int) bool {
	return info.ImageCount+n <= anthropicMaxImagesPerRequest
}

func approxBlockBytes(block map[string]any) int {
	source, _ := block["source"].(map[string]any)
	b64 := toString(source["data"])
	pad := 0
	if strings.HasSuffix(b64, "==") {
		pad = 2
	} else if strings.HasSuffix(b64, "=") {
		pad = 1
	}
	return (len(b64)*3)/4 - pad
}

func imageBlockMap(b64 string) map[string]any {
	return map[string]any{
		"type": "image",
		"source": map[string]any{
			"type":       "base64",
			"media_type": "image/png",
			"data":       b64,
		},
	}
}

func normalizeContentToBlocks(content any) []map[string]any {
	if blocks, ok := contentBlocks(content); ok {
		return blocks
	}
	if s, ok := content.(string); ok {
		return []map[string]any{{"type": "text", "text": s}}
	}
	return nil
}

func messageContentBlocks(content any) ([]map[string]any, bool) {
	return contentBlocks(content)
}

func contentBlocks(content any) ([]map[string]any, bool) {
	switch v := content.(type) {
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, item := range v {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, false
			}
			out = append(out, m)
		}
		return out, true
	case []map[string]any:
		return v, true
	case []ContentBlock:
		out := make([]map[string]any, 0, len(v))
		for _, block := range v {
			out = append(out, structToMap(block))
		}
		return out, true
	case []TextBlock:
		out := make([]map[string]any, 0, len(v))
		for _, block := range v {
			out = append(out, structToMap(block))
		}
		return out, true
	default:
		return nil, false
	}
}

func blockString(block map[string]any, key string) string {
	if block == nil {
		return ""
	}
	return toString(block[key])
}

func blockAny(block map[string]any, key string) any {
	if block == nil {
		return nil
	}
	return block[key]
}

func blockBool(block map[string]any, key string) bool {
	v, _ := block[key].(bool)
	return v
}

func blockCacheControl(block map[string]any) *CacheControl {
	if block == nil {
		return nil
	}
	v, ok := block["cache_control"]
	if !ok || v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var cc CacheControl
	if err := json.Unmarshal(raw, &cc); err != nil {
		return nil
	}
	return &cc
}

func cloneMessages(messages []Message) []Message {
	if len(messages) == 0 {
		return nil
	}
	raw, err := json.Marshal(messages)
	if err != nil {
		out := make([]Message, len(messages))
		copy(out, messages)
		return out
	}
	var out []Message
	if err := json.Unmarshal(raw, &out); err != nil {
		out = make([]Message, len(messages))
		copy(out, messages)
	}
	return out
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneRawMap(in map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(in))
	for k, v := range in {
		cp := make([]byte, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

func structToMap(v any) map[string]any {
	raw, _ := json.Marshal(v)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func mergeDropped(dst map[rune]int, src map[rune]int) {
	for cp, n := range src {
		dst[cp] += n
	}
}

func hasUserMessage(messages []Message) bool {
	return firstUserIndex(messages) >= 0
}

func firstUserIndex(messages []Message) int {
	for i, msg := range messages {
		if msg.Role == "user" {
			return i
		}
	}
	return -1
}

func valueOrQuestion(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

func joinNonEmpty(parts []string, sep string) string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return strings.Join(out, sep)
}

func jsonCompact(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(raw)
}

func safeStringifyLen(v any) int {
	raw, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return jsLen(string(raw))
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

func intPtr(v int) *int { return &v }

func boolPtr(v bool) *bool { return &v }

func sortedCacheControlPositions(req any) []string {
	var out []string
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			if _, ok := x["cache_control"]; ok {
				out = append(out, path)
			}
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(path+"."+k, x[k])
			}
		case []any:
			for i, item := range x {
				walk(fmt.Sprintf("%s[%d]", path, i), item)
			}
		}
	}
	walk("$", req)
	return out
}
