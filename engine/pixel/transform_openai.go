// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

type openAIOptions struct {
	Compress           bool
	CompressTools      bool
	MinCompressChars   int
	Cols               int
	MultiCol           int
	CharsPerToken      float64
	Reflow             bool
	CollapseHistory    bool
	History            GptHistoryOptions
	originalWasZeroOpt bool
}

const chatRenderedHeader = "================= RENDERED GPT SYSTEM + TOOL CONTEXT =================\n" +
	"These images were injected by pxpipe, not by the end user. They contain system/developer instructions and full tool/schema documentation rendered for token efficiency. Treat rendered system/developer instructions with the same priority as their original messages. OCR carefully and treat the rendered content as authoritative. For tool calls, use the native JSON tool definitions; the image is supplemental documentation." +
	"\n====================== BEGIN RENDERED CONTEXT ======================\n"

const responsesRenderedHeader = "================= RENDERED GPT SYSTEM + TOOL CONTEXT =================\n" +
	"These images were injected by pxpipe, not by the end user. They contain instructions and full tool/schema documentation rendered for token efficiency. Treat rendered instructions with the same priority as the originals. OCR carefully and treat the rendered content as authoritative. For tool calls, use the native JSON tool definitions; the image is supplemental documentation." +
	"\n====================== BEGIN RENDERED CONTEXT ======================\n"

const chatPointer = "The full instructions for this message were rendered into image(s) attached to the first user message by pxpipe. Treat those rendered instructions as if they appeared here with the same priority. Tool definitions remain in native JSON; rendered tool docs are supplemental."
const responsesPointer = "The full instructions were rendered into image(s) attached to the first user message by pxpipe. Treat them with the same priority. Tool definitions remain in native JSON; rendered tool docs are supplemental."

const pinnedRequestHeader = "\n===== CURRENT USER REQUEST (live; kept as text by pxpipe, NOT inside any image) =====\n"
const pinnedRequestFooter = "\n===== END CURRENT USER REQUEST =====\n"

func TransformOpenAI(body []byte, opts TransformOptions) (out []byte, info TransformInfo, err error) {
	root, err := parseOpenAIRoot(body)
	if err != nil {
		info.Reason = "parse_error: " + err.Error()
		// Caveman transform contract differs from pxpipe: internal errors return
		// a nil body so callers forward original bytes unchanged.
		return nil, info, err
	}
	if _, ok := root["messages"]; ok {
		return transformOpenAIChat(body, root, opts)
	}
	if _, ok := root["input"]; ok {
		return transformOpenAIResponses(body, root, opts)
	}
	info.Reason = "parse_error: neither messages nor input present"
	return body, info, nil
}

func transformOpenAIChat(body []byte, root map[string]json.RawMessage, opts TransformOptions) ([]byte, TransformInfo, error) {
	info := openAIEmptyInfo()
	model := openAIModel(root, opts)
	o := resolveOpenAIOptions(opts, model)
	if !o.Compress {
		info.Reason = "compress=false"
		return body, info, nil
	}
	var messages []any
	if err := json.Unmarshal(root["messages"], &messages); err != nil {
		info.Reason = "parse_error: messages must be an array"
		return nil, info, err
	}
	firstUserIdx := firstRoleIndex(messages, "user")
	if firstUserIdx < 0 {
		info.Reason = "no_user_message"
		return body, info, nil
	}

	var tools []any
	var haveTools bool
	if raw, ok := root["tools"]; ok {
		haveTools = true
		if err := json.Unmarshal(raw, &tools); err != nil {
			info.Reason = "parse_error: tools must be an array"
			return nil, info, err
		}
	}

	var authorityDocs, systemTexts []string
	for _, item := range messages {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "system" && role != "developer" {
			continue
		}
		text := openAIChatContentText(msg["content"])
		if text == "" {
			continue
		}
		authorityDocs = append(authorityDocs, "## "+strings.ToUpper(role)+" MESSAGE\n"+text)
		systemTexts = append(systemTexts, text)
		info.StaticChars += len(text)
	}
	rewrittenTools, toolDocs, toolsChanged := tools, "", false
	if o.CompressTools && haveTools {
		rewrittenTools, toolDocs, toolsChanged = rewriteChatToolsForGpt(tools)
	}

	combinedRaw := strings.Join(nonEmptyStrings(append(authorityDocs, toolDocs)), "\n\n")
	info.OrigChars = len(combinedRaw)
	if combinedRaw == "" {
		info.Reason = "no_static_context"
		return body, info, nil
	}
	combined := maybeReflowOpenAI(CompactSlabWhitespace(combinedRaw), o.Reflow)
	if len(combined) < o.MinCompressChars {
		info.Reason = fmt.Sprintf("below_min_chars (%d < %d)", len(combined), o.MinCompressChars)
		return body, info, nil
	}

	profile := ResolveGptProfile(model)
	rp := ResolveDensity(model, DensityFromEnv())
	gptStyle := RenderStyle{}
	if !rp.isConservativeGeometry() {
		gptStyle = rp.renderStyle()
	}
	header := withOpenAIZebraNote(withOpenAIReflowNote(chatRenderedHeader, o.Reflow), rp.Zebra)
	renderedText := header + combined
	cols := min(MeasureContentCols(renderedText, o.Cols, 1), profile.StripCols)
	gate := evalOpenAIGate(model, renderedText, cols, o.CharsPerToken, rp)
	if !gate.Profitable {
		info.Reason = fmt.Sprintf("not_profitable (slab=%d chars)", len(combined))
		info.PassthroughReasons = map[string]int{"not_profitable": 1}
		return body, info, nil
	}
	images, err := renderOpenAIText(renderedText, cols, profile.MaxHeightPx, gptStyle)
	if err != nil {
		info.Reason = "render_error: " + err.Error()
		return nil, info, err
	}
	if len(images) == 0 {
		info.Reason = "render_empty"
		return body, info, nil
	}
	foldOpenAIImages(&info, model, images)
	info.TextTokensEstimate += gptBaselineImagedTokens(systemTexts, tools, rewrittenTools, haveTools)
	info.CompressedChars = len(combinedRaw)
	if info.TextTokensEstimate == 0 {
		info.TextTokensEstimate = gptTextTokens(combinedRaw)
	}

	imageParts := make([]any, 0, len(images)+2)
	for _, img := range images {
		imageParts = append(imageParts, openAIChatImagePart(img))
	}
	if fact := FactSheetText(combinedRaw, 0); fact != "" {
		imageParts = append(imageParts, map[string]any{"type": "text", "text": fact})
	}
	imageParts = append(imageParts, map[string]any{"type": "text", "text": "[End of rendered GPT system/tool context.]"})
	slabUserMsg := map[string]any{"role": "user", "content": imageParts}
	messages = insertAny(messages, firstUserIdx, slabUserMsg)
	for _, item := range messages {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "system" && role != "developer" {
			continue
		}
		if openAIChatContentText(msg["content"]) != "" {
			setOpenAIChatTextContent(msg, chatPointer)
		}
	}

	if o.CollapseHistory {
		turns := OpenAIChatMessagesToTurns(messages)
		profitable := func(text string, cols int) bool {
			// History renders at conservative geometry (RenderStyle{}); price it so.
			return evalOpenAIGate(model, text, cols, o.CharsPerToken, conservativeStdParams).Profitable
		}
		historyOpts := o.History
		historyOpts.Cols = ResolveGptProfile(model).StripCols
		historyOpts.MaxHeightPx = ResolveGptProfile(model).MaxHeightPx
		historyOpts.Reflow = o.Reflow
		plan, err := PlanGptCollapse(turns, firstUserIdx+1, profitable, historyOpts)
		if err != nil {
			info.Reason = "history_render_error: " + err.Error()
			return nil, info, err
		}
		foldOpenAIHistory(&info, model, plan)
		if len(plan.Images)+len(plan.ImagesAfter) > 0 {
			synthetic, guard := chatHistorySynthetic(plan)
			messages = replaceRangeWith(messages, plan.Start, plan.EndExclusive, synthetic, guard)
		}
	}

	setRawJSON(root, "messages", messages)
	if haveTools && toolsChanged {
		setRawJSON(root, "tools", rewrittenTools)
	}
	out, err := json.Marshal(root)
	if err != nil {
		info.Reason = "marshal_error: " + err.Error()
		return nil, info, err
	}
	info.Compressed = true
	return out, info, nil
}

func transformOpenAIResponses(body []byte, root map[string]json.RawMessage, opts TransformOptions) ([]byte, TransformInfo, error) {
	info := openAIEmptyInfo()
	model := openAIModel(root, opts)
	o := resolveOpenAIOptions(opts, model)
	if !o.Compress {
		info.Reason = "compress=false"
		return body, info, nil
	}

	inputWasString := false
	var originalInput string
	var inputItems []any
	var inputString string
	if err := json.Unmarshal(root["input"], &inputString); err == nil {
		inputWasString = true
		originalInput = inputString
		inputItems = []any{}
	} else if err := json.Unmarshal(root["input"], &inputItems); err != nil {
		info.Reason = "parse_error: input must be a string or array"
		return nil, info, err
	}
	firstUserIdx := -1
	if !inputWasString {
		firstUserIdx = firstRoleIndex(inputItems, "user")
		if firstUserIdx < 0 {
			info.Reason = "no_user_message"
			return body, info, nil
		}
	}

	var tools []any
	var haveTools bool
	if raw, ok := root["tools"]; ok {
		haveTools = true
		if err := json.Unmarshal(raw, &tools); err != nil {
			info.Reason = "parse_error: tools must be an array"
			return nil, info, err
		}
	}

	var authorityDocs, systemTexts []string
	if raw, ok := root["instructions"]; ok {
		var instructions string
		if json.Unmarshal(raw, &instructions) == nil && instructions != "" {
			authorityDocs = append(authorityDocs, "## INSTRUCTIONS\n"+instructions)
			systemTexts = append(systemTexts, instructions)
			info.StaticChars += len(instructions)
		}
	}
	for _, item := range inputItems {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "system" && role != "developer" {
			continue
		}
		text := responsesStaticContentText(msg["content"])
		if text == "" {
			continue
		}
		authorityDocs = append(authorityDocs, "## "+strings.ToUpper(role)+" MESSAGE\n"+text)
		systemTexts = append(systemTexts, text)
		info.StaticChars += len(text)
	}
	rewrittenTools, toolDocs, toolsChanged := tools, "", false
	if o.CompressTools && haveTools {
		rewrittenTools, toolDocs, toolsChanged = rewriteFlatToolsForGpt(tools)
	}

	combinedRaw := strings.Join(nonEmptyStrings(append(authorityDocs, toolDocs)), "\n\n")
	info.OrigChars = len(combinedRaw)
	if combinedRaw == "" {
		info.Reason = "no_static_context"
		return body, info, nil
	}
	combined := maybeReflowOpenAI(CompactSlabWhitespace(combinedRaw), o.Reflow)
	if len(combined) < o.MinCompressChars {
		info.Reason = fmt.Sprintf("below_min_chars (%d < %d)", len(combined), o.MinCompressChars)
		return body, info, nil
	}

	profile := ResolveGptProfile(model)
	rp := ResolveDensity(model, DensityFromEnv())
	gptStyle := RenderStyle{}
	if !rp.isConservativeGeometry() {
		gptStyle = rp.renderStyle()
	}
	header := withOpenAIZebraNote(withOpenAIReflowNote(responsesRenderedHeader, o.Reflow), rp.Zebra)
	renderedText := header + combined
	cols := min(MeasureContentCols(renderedText, o.Cols, 1), profile.StripCols)
	gate := evalOpenAIGate(model, renderedText, cols, o.CharsPerToken, rp)
	if !gate.Profitable {
		info.Reason = fmt.Sprintf("not_profitable (slab=%d chars)", len(combined))
		info.PassthroughReasons = map[string]int{"not_profitable": 1}
		return body, info, nil
	}
	images, err := renderOpenAIText(renderedText, cols, profile.MaxHeightPx, gptStyle)
	if err != nil {
		info.Reason = "render_error: " + err.Error()
		return nil, info, err
	}
	if len(images) == 0 {
		info.Reason = "render_empty"
		return body, info, nil
	}
	foldOpenAIImages(&info, model, images)
	info.TextTokensEstimate += gptBaselineImagedTokens(systemTexts, tools, rewrittenTools, haveTools)
	info.CompressedChars = len(combinedRaw)
	if info.TextTokensEstimate == 0 {
		info.TextTokensEstimate = gptTextTokens(combinedRaw)
	}

	imageParts := make([]any, 0, len(images)+2)
	for _, img := range images {
		imageParts = append(imageParts, openAIResponsesImagePart(img))
	}
	if fact := FactSheetText(combinedRaw, 0); fact != "" {
		imageParts = append(imageParts, map[string]any{"type": "input_text", "text": fact})
	}
	imageParts = append(imageParts, map[string]any{"type": "input_text", "text": "[End of rendered GPT system/tool context.]"})
	if inputWasString {
		content := append([]any{}, imageParts...)
		content = append(content, map[string]any{"type": "input_text", "text": originalInput})
		root["input"] = mustRawJSON([]any{map[string]any{"role": "user", "content": content}})
	} else {
		slabUserItem := map[string]any{"role": "user", "content": imageParts}
		inputItems = insertAny(inputItems, firstUserIdx, slabUserItem)
		setRawJSON(root, "input", inputItems)
	}

	if raw, ok := root["instructions"]; ok {
		var instructions string
		if json.Unmarshal(raw, &instructions) == nil && instructions != "" {
			root["instructions"] = mustRawJSON(responsesPointer)
		}
	}
	if !inputWasString {
		for _, item := range inputItems {
			msg, ok := item.(map[string]any)
			if !ok {
				continue
			}
			role, _ := msg["role"].(string)
			if role != "system" && role != "developer" {
				continue
			}
			if responsesStaticContentText(msg["content"]) != "" {
				setResponsesTextContent(msg, responsesPointer)
			}
		}
		setRawJSON(root, "input", inputItems)
	}

	if o.CollapseHistory && !inputWasString {
		turns := OpenAIResponsesItemsToTurns(inputItems)
		profitable := func(text string, cols int) bool {
			// History renders at conservative geometry (RenderStyle{}); price it so.
			return evalOpenAIGate(model, text, cols, o.CharsPerToken, conservativeStdParams).Profitable
		}
		historyOpts := o.History
		historyOpts.Cols = ResolveGptProfile(model).StripCols
		historyOpts.MaxHeightPx = ResolveGptProfile(model).MaxHeightPx
		historyOpts.Reflow = o.Reflow
		plan, err := PlanGptCollapse(turns, firstUserIdx+1, profitable, historyOpts)
		if err != nil {
			info.Reason = "history_render_error: " + err.Error()
			return nil, info, err
		}
		foldOpenAIHistory(&info, model, plan)
		if len(plan.Images)+len(plan.ImagesAfter) > 0 {
			synthetic, guard := responsesHistorySynthetic(plan)
			inputItems = replaceRangeWith(inputItems, plan.Start, plan.EndExclusive, synthetic, guard)
			setRawJSON(root, "input", inputItems)
		}
	}

	if haveTools && toolsChanged {
		setRawJSON(root, "tools", rewrittenTools)
	}
	out, err := json.Marshal(root)
	if err != nil {
		info.Reason = "marshal_error: " + err.Error()
		return nil, info, err
	}
	info.Compressed = true
	return out, info, nil
}

type openAIGateResult struct {
	ImageTokens float64
	TextTokens  float64
	Profitable  bool
}

func evalOpenAIGate(model, renderedText string, cols int, charsPerToken float64, rp renderParams) openAIGateResult {
	if charsPerToken <= 0 || math.IsNaN(charsPerToken) || math.IsInf(charsPerToken, 0) {
		charsPerToken = 4
	}
	imageTokens := float64(estimateOpenAIImageTokens(model, renderedText, cols, ResolveGptProfile(model).MaxHeightPx, rp))
	textTokens := float64(jsLen(renderedText)) / charsPerToken
	return openAIGateResult{ImageTokens: imageTokens, TextTokens: textTokens, Profitable: imageTokens < textTokens}
}

func estimateOpenAIImageTokens(model, text string, cols, maxHeightPx int, rp renderParams) int {
	cols = max(1, cols)
	if maxHeightPx <= 0 {
		maxHeightPx = GptMaxHeightPx
	}
	lines := WrapLines(text, cols, 1)
	linesPerImg := min(rp.linesPerPage(maxHeightPx), readableLinesPerColumn(cols))
	pages := splitWrappedLinesIntoReadablePages(lines, linesPerImg, ReadableCharsPerImage)
	stripW := rp.pageWidthPx(cols)
	total := 0
	for _, page := range pages {
		height := heightForRows(max(1, len(page)), rp.PitchY, CellH)
		total += OpenAIVisionTokens(model, stripW, height)
	}
	return total
}

func renderOpenAIText(text string, cols, maxHeightPx int, style RenderStyle) ([]RenderedImage, error) {
	return RenderTextToPNGsWithCharLimit(text, cols, ReadableCharsPerImage, style, maxHeightPx, "")
}

func foldOpenAIImages(info *TransformInfo, model string, images []RenderedImage) {
	info.ImageCount += len(images)
	for _, img := range images {
		info.ImageBytes += len(img.PNG)
		info.ImagePixels += img.Width * img.Height
		info.DroppedChars += img.DroppedChars
		info.ImageTokensEstimate += OpenAIVisionTokens(model, img.Width, img.Height)
	}
}

func foldOpenAIHistory(info *TransformInfo, model string, plan GptCollapsePlan) {
	all := append(append([]RenderedImage{}, plan.Images...), plan.ImagesAfter...)
	if len(all) == 0 {
		if plan.Reason != "" {
			info.HistoryReason = plan.Reason
		}
		if plan.CollapsedChars > 0 {
			info.CollapsedChars = plan.CollapsedChars
		}
		return
	}
	foldOpenAIImages(info, model, all)
	info.TextTokensEstimate += gptTextTokens(plan.Text)
	info.CollapsedTurns = plan.CollapsedTurns
	info.CollapsedChars = plan.CollapsedChars
	info.CollapsedImages = len(all)
	info.HistoryReason = "collapsed"
}

func gptTextTokens(text string) int {
	return gptCountTokens(text)
}

func gptImageTokens(model string, images []RenderedImage) int {
	total := 0
	for _, img := range images {
		total += OpenAIVisionTokens(model, img.Width, img.Height)
	}
	return total
}

func gptBaselineImagedTokens(systemTexts []string, originalTools, strippedTools []any, haveTools bool) int {
	total := 0
	for _, text := range systemTexts {
		total += gptTextTokens(text)
	}
	if haveTools && len(originalTools) > 0 {
		orig := gptTextTokens(string(openAIMustJSON(originalTools)))
		stripped := 0
		if len(strippedTools) > 0 {
			stripped = gptTextTokens(string(openAIMustJSON(strippedTools)))
		}
		total += max(0, orig-stripped)
	}
	return total
}

func openAIChatImagePart(img RenderedImage) map[string]any {
	return map[string]any{
		"type": "image_url",
		"image_url": map[string]any{
			"url":    "data:image/png;base64," + base64.StdEncoding.EncodeToString(img.PNG),
			"detail": "high",
		},
	}
}

func openAIResponsesImagePart(img RenderedImage) map[string]any {
	return map[string]any{
		"type":      "input_image",
		"image_url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(img.PNG),
		"detail":    "high",
	}
}

func chatHistorySynthetic(plan GptCollapsePlan) (map[string]any, map[string]any) {
	content := []any{map[string]any{"type": "text", "text": HistoryTranscriptIntro}}
	for _, img := range plan.Images {
		content = append(content, openAIChatImagePart(img))
	}
	if plan.PinText != nil {
		content = append(content, map[string]any{"type": "text", "text": pinnedRequestBlock(*plan.PinText)})
		for _, img := range plan.ImagesAfter {
			content = append(content, openAIChatImagePart(img))
		}
	}
	if fact := FactSheetText(plan.Text, 0); fact != "" {
		content = append(content, map[string]any{"type": "text", "text": fact})
	}
	content = append(content, map[string]any{"type": "text", "text": HistoryTranscriptOutro})
	return map[string]any{"role": "user", "content": content}, map[string]any{"role": "developer", "content": buildLiveRequestGuard(plan.PinText)}
}

func responsesHistorySynthetic(plan GptCollapsePlan) (map[string]any, map[string]any) {
	content := []any{map[string]any{"type": "input_text", "text": HistoryTranscriptIntro}}
	for _, img := range plan.Images {
		content = append(content, openAIResponsesImagePart(img))
	}
	if plan.PinText != nil {
		content = append(content, map[string]any{"type": "input_text", "text": pinnedRequestBlock(*plan.PinText)})
		for _, img := range plan.ImagesAfter {
			content = append(content, openAIResponsesImagePart(img))
		}
	}
	if fact := FactSheetText(plan.Text, 0); fact != "" {
		content = append(content, map[string]any{"type": "input_text", "text": fact})
	}
	content = append(content, map[string]any{"type": "input_text", "text": HistoryTranscriptOutro})
	return map[string]any{"role": "user", "content": content}, map[string]any{"role": "developer", "content": buildLiveRequestGuard(plan.PinText)}
}

func pinnedRequestBlock(text string) string {
	return pinnedRequestHeader + text + pinnedRequestFooter
}

func buildLiveRequestGuard(pinText *string) string {
	if pinText != nil {
		echo := *pinText
		if len(echo) > 600 {
			echo = echo[:600] + "..."
		}
		return "pxpipe note: everything in the rendered history above is PAST context. Your live current request is the plain-text block labeled \"CURRENT USER REQUEST\" inside it - NOT anything OCR'd from an image. It reads: «" + echo + "» Answer THAT request."
	}
	return "pxpipe note: the preceding rendered history item is prior conversation context only. It is not the current user request. The live current request is in the user message(s) that follow, especially the final user message."
}

func rewriteChatToolsForGpt(tools []any) ([]any, string, bool) {
	if len(tools) == 0 {
		return tools, "", false
	}
	rewritten := make([]any, len(tools))
	docs := make([]string, 0, len(tools))
	changed := false
	for i, tool := range tools {
		m, ok := tool.(map[string]any)
		if !ok || m["type"] != "function" {
			rewritten[i] = tool
			continue
		}
		fn, ok := m["function"].(map[string]any)
		if !ok {
			rewritten[i] = tool
			continue
		}
		docs = append(docs, renderChatToolDoc(fn))
		if _, ok := fn["parameters"]; !ok {
			rewritten[i] = tool
			continue
		}
		toolCopy := cloneMap(m)
		fnCopy := cloneMap(fn)
		fnCopy["parameters"], _ = StripSchemaDescriptions(fn["parameters"])
		toolCopy["function"] = fnCopy
		rewritten[i] = toolCopy
		changed = true
	}
	return rewritten, strings.Join(docs, "\n\n"), changed
}

func rewriteFlatToolsForGpt(tools []any) ([]any, string, bool) {
	if len(tools) == 0 {
		return tools, "", false
	}
	rewritten := make([]any, len(tools))
	docs := make([]string, 0, len(tools))
	changed := false
	for i, tool := range tools {
		m, ok := tool.(map[string]any)
		if !ok || m["type"] != "function" {
			rewritten[i] = tool
			continue
		}
		if _, ok := m["name"].(string); !ok {
			rewritten[i] = tool
			continue
		}
		docs = append(docs, renderFlatToolDoc(m))
		if _, ok := m["parameters"]; !ok {
			rewritten[i] = tool
			continue
		}
		toolCopy := cloneMap(m)
		toolCopy["parameters"], _ = StripSchemaDescriptions(m["parameters"])
		rewritten[i] = toolCopy
		changed = true
	}
	return rewritten, strings.Join(docs, "\n\n"), changed
}

func renderChatToolDoc(fn map[string]any) string {
	parts := []string{"## Tool: " + stringOr(fn["name"], "?")}
	if desc, _ := fn["description"].(string); desc != "" {
		parts = append(parts, desc)
	}
	if params, ok := fn["parameters"]; ok {
		parts = append(parts, "```json\n"+string(openAIMustJSON(params))+"\n```")
	}
	return strings.Join(parts, "\n")
}

func renderFlatToolDoc(tool map[string]any) string {
	parts := []string{"## Tool: " + stringOr(tool["name"], "?")}
	if desc, _ := tool["description"].(string); desc != "" {
		parts = append(parts, desc)
	}
	if params, ok := tool["parameters"]; ok {
		parts = append(parts, "```json\n"+string(openAIMustJSON(params))+"\n```")
	}
	return strings.Join(parts, "\n")
}

func openAIEmptyInfo() TransformInfo {
	return TransformInfo{PassthroughReasons: map[string]int{}}
}

func resolveOpenAIOptions(opts TransformOptions, model string) openAIOptions {
	if openAIOptionsZero(opts) {
		opts = DefaultTransformOptions(model)
		opts.Cols = DefaultGptStripCols
	}
	if opts.Cols == 0 {
		opts.Cols = DefaultGptStripCols
	}
	if opts.MinCompressChars == 0 {
		opts.MinCompressChars = 2000
	}
	if opts.CharsPerToken == 0 {
		opts.CharsPerToken = 4
	}
	history := DefaultGptHistoryOptions()
	return openAIOptions{
		Compress:         opts.Compress,
		CompressTools:    opts.CompressTools,
		MinCompressChars: opts.MinCompressChars,
		Cols:             min(opts.Cols, ResolveGptProfile(model).StripCols),
		MultiCol:         1,
		CharsPerToken:    opts.CharsPerToken,
		Reflow:           opts.Reflow,
		CollapseHistory:  opts.CollapseHistory,
		History:          history,
	}
}

func parseOpenAIRoot(body []byte) (map[string]json.RawMessage, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, err
	}
	if root == nil {
		return nil, errors.New("body must be a JSON object")
	}
	return root, nil
}

func openAIModel(root map[string]json.RawMessage, opts TransformOptions) string {
	if raw, ok := root["model"]; ok {
		var model string
		if json.Unmarshal(raw, &model) == nil && model != "" {
			return model
		}
	}
	return opts.Model
}

func openAIChatContentText(content any) string {
	if s, ok := content.(string); ok {
		return s
	}
	arr, ok := content.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(arr))
	for _, p := range arr {
		m, ok := p.(map[string]any)
		if !ok || m["type"] != "text" {
			continue
		}
		if txt, ok := m["text"].(string); ok {
			parts = append(parts, txt)
		}
	}
	return strings.Join(parts, "\n\n")
}

func responsesStaticContentText(content any) string {
	if s, ok := content.(string); ok {
		return s
	}
	return responsesContentToHistoryText(content)
}

func setOpenAIChatTextContent(msg map[string]any, text string) {
	if arr, ok := msg["content"].([]any); ok {
		kept := make([]any, 0, len(arr)+1)
		kept = append(kept, map[string]any{"type": "text", "text": text})
		for _, p := range arr {
			m, ok := p.(map[string]any)
			if ok && m["type"] == "text" {
				continue
			}
			kept = append(kept, p)
		}
		msg["content"] = kept
		return
	}
	msg["content"] = text
}

func setResponsesTextContent(msg map[string]any, text string) {
	if arr, ok := msg["content"].([]any); ok {
		kept := make([]any, 0, len(arr)+1)
		kept = append(kept, map[string]any{"type": "input_text", "text": text})
		for _, p := range arr {
			m, ok := p.(map[string]any)
			if ok {
				typ, _ := m["type"].(string)
				if typ == "input_text" || typ == "text" || typ == "output_text" || typ == "summary_text" {
					continue
				}
			}
			kept = append(kept, p)
		}
		msg["content"] = kept
		return
	}
	msg["content"] = text
}

func firstRoleIndex(items []any, role string) int {
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if r, _ := m["role"].(string); r == role {
			return i
		}
	}
	return -1
}

func maybeReflowOpenAI(text string, enabled bool) string {
	if !enabled {
		return text
	}
	if packed, ok := Reflow(text); ok {
		return packed
	}
	return text
}

func withOpenAIReflowNote(header string, enabled bool) string {
	if !enabled {
		return header
	}
	note := " The glyph ↵ (U+21B5) marks an original hard line break in content; treat it as a real newline."
	return strings.Replace(header, "\n====", note+"\n====", 1)
}

func withOpenAIZebraNote(header string, zebra bool) string {
	note := zebraReaderNote(zebra)
	if note == "" {
		return header
	}
	return strings.Replace(header, "\n====", note+"\n====", 1)
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func insertAny(items []any, idx int, value any) []any {
	out := make([]any, 0, len(items)+1)
	out = append(out, items[:idx]...)
	out = append(out, value)
	out = append(out, items[idx:]...)
	return out
}

func replaceRangeWith(items []any, start, end int, values ...any) []any {
	out := make([]any, 0, len(items)-(end-start)+len(values))
	out = append(out, items[:start]...)
	out = append(out, values...)
	out = append(out, items[end:]...)
	return out
}

func setRawJSON(root map[string]json.RawMessage, key string, value any) {
	root[key] = mustRawJSON(value)
}

func mustRawJSON(value any) json.RawMessage {
	return json.RawMessage(openAIMustJSON(value))
}

func openAIMustJSON(value any) []byte {
	b, err := json.Marshal(value)
	if err != nil {
		return []byte("null")
	}
	return b
}

func sha8Bytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:4])
}

func openAIOptionsZero(opts TransformOptions) bool {
	return opts.Model == "" &&
		!opts.Compress &&
		!opts.CompressTools &&
		!opts.CompressReminders &&
		!opts.CompressToolResults &&
		opts.MinCompressChars == 0 &&
		opts.MinReminderChars == 0 &&
		opts.MinToolResultChars == 0 &&
		opts.Cols == 0 &&
		opts.MaxImagesPerToolResult == 0 &&
		opts.MultiCol == 0 &&
		opts.CharsPerToken == 0 &&
		opts.HistoryAmortizationHorizon == 0 &&
		opts.PriorWarmTokens == 0 &&
		opts.PriorWarmImageTokens == 0 &&
		!opts.CollapseHistory &&
		!opts.Reflow &&
		opts.KeepSharp == nil &&
		!opts.EmitRecoverable
}
