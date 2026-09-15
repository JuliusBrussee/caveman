// Built on the caveman pixel primitives ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"

	engineimage "github.com/JuliusBrussee/caveman/engine/image"
)

const (
	// Same banner as the Anthropic history collapse — one source of truth for the
	// wording the model is told to attribute turns by.
	geminiHistoryIntro = HistorySyntheticIntro

	geminiHistoryKeepTail          = 4
	geminiHistoryMinCollapsePrefix = 10
	geminiHistoryCols              = DenseContentCols
	geminiSystemPointer            = "[caveman] Rendered session configuration images are attached at the start of the first user content. Read them before answering."
)

var (
	geminiTagOpenRE  = regexp.MustCompile(`<([a-zA-Z][a-zA-Z0-9_-]*)(\s[^>]*)?>`)
	geminiBlankRunRE = regexp.MustCompile(`\n{3,}`)
)

type geminiGenerateContent struct {
	Contents          []geminiContent    `json:"contents,omitempty"`
	SystemInstruction *geminiInstruction `json:"systemInstruction,omitempty"`
	Tools             []json.RawMessage  `json:"tools,omitempty"`
	extra             map[string]json.RawMessage
}

type geminiInstruction struct {
	Parts []geminiPart `json:"parts,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts,omitempty"`
}

type geminiPart struct {
	Text             *string                    `json:"text,omitempty"`
	InlineData       *geminiInlineData          `json:"inline_data,omitempty"`
	FunctionResponse *geminiFunctionResponse    `json:"functionResponse,omitempty"`
	FunctionCall     *geminiFunctionCall        `json:"functionCall,omitempty"`
	Extra            map[string]json.RawMessage `json:"-"`
}

type geminiInlineData struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"`
}

type geminiFunctionResponse struct {
	Name     string                     `json:"name,omitempty"`
	Response json.RawMessage            `json:"response,omitempty"`
	Extra    map[string]json.RawMessage `json:"-"`
}

type geminiFunctionCall struct {
	Name  string                     `json:"name,omitempty"`
	Args  json.RawMessage            `json:"args,omitempty"`
	Extra map[string]json.RawMessage `json:"-"`
}

type geminiSplitText struct {
	staticText        string
	dynamicText       string
	dynamicBlockCount int
}

type geminiRenderResult struct {
	parts             []geminiPart
	imageCount        int
	imageBytes        int
	imagePixels       int
	imageTokens       int
	droppedChars      int
	droppedCodepoints map[rune]int
}

type geminiGateEval struct {
	imageTokens   float64
	textTokens    float64
	burnImageSide float64
	burnTextSide  float64
	profitable    bool
}

// TransformGemini rewrites a Gemini generateContent body (contents[].parts[], systemInstruction).
// err != nil OR info.ImageCount == 0 means the caller MUST forward the original bytes.
func TransformGemini(body []byte, opts TransformOptions) (out []byte, info TransformInfo, err error) {
	info = TransformInfo{
		OrigChars:          0,
		CompressedChars:    0,
		ImageCount:         0,
		ImageBytes:         0,
		ImagePixels:        0,
		PassthroughReasons: make(map[string]int),
	}
	o := mergeGeminiOptions(opts)
	if !o.Compress {
		info.Reason = "compress=false"
		return body, info, nil
	}

	req, err := parseGeminiGenerateContent(body)
	if err != nil {
		info.Reason = "parse_error: " + err.Error()
		return nil, info, err
	}
	mediaResolution, supported := geminiGlobalMediaResolution(req.extra)
	if !supported || (mediaResolution != "default" && mediaResolution != "high") {
		info.Reason = "unsupported_media_resolution"
		return body, info, nil
	}
	o.geminiMediaResolution = mediaResolution
	if _, supported := engineimage.EstimateTokensForModel("gemini", o.Model, mediaResolution, 1, 1); !supported {
		info.Reason = "unsupported_image_token_profile"
		return body, info, nil
	}

	changed := false
	if o.CollapseHistory && len(req.Contents) > 0 {
		ok, err := collapseGeminiHistory(&req, &info, o)
		if err != nil {
			info.Reason = "history_transform_error: " + err.Error()
			return nil, info, err
		}
		changed = changed || ok
	}

	if req.SystemInstruction != nil {
		ok, err := transformGeminiSystem(&req, req.SystemInstruction, &info, o)
		if err != nil {
			info.Reason = "system_transform_error: " + err.Error()
			return nil, info, err
		}
		changed = changed || ok
	}

	if o.CompressToolResults {
		ok, err := transformGeminiContents(&req, &info, o)
		if err != nil {
			info.Reason = "contents_transform_error: " + err.Error()
			return nil, info, err
		}
		changed = changed || ok
	}

	if !changed || info.ImageCount == 0 {
		if info.Reason == "" {
			info.Reason = "no_profitable_blocks"
		}
		if len(info.PassthroughReasons) == 0 {
			info.PassthroughReasons = nil
		}
		return body, info, nil
	}

	out, err = marshalGeminiGenerateContent(req)
	if err != nil {
		info.Reason = "marshal_error: " + err.Error()
		return nil, info, err
	}
	info.Compressed = true
	if info.Reason == "" || info.Reason == "no_profitable_blocks" {
		info.Reason = "applied"
	}
	if len(info.PassthroughReasons) == 0 {
		info.PassthroughReasons = nil
	}
	return out, info, nil
}

func mergeGeminiOptions(opts TransformOptions) TransformOptions {
	def := DefaultTransformOptions(opts.Model)
	if opts.Model != "" {
		def.Model = opts.Model
	}
	boolInitialized := opts.Compress || opts.CompressTools || opts.CompressReminders || opts.CompressToolResults
	if boolInitialized {
		def.Compress = opts.Compress
		def.CompressTools = opts.CompressTools
		def.CompressReminders = opts.CompressReminders
		def.CompressToolResults = opts.CompressToolResults
		def.CollapseHistory = opts.CollapseHistory
		def.Reflow = opts.Reflow
	}
	if opts.MinCompressChars > 0 {
		def.MinCompressChars = opts.MinCompressChars
	}
	if opts.MinReminderChars > 0 {
		def.MinReminderChars = opts.MinReminderChars
	}
	if opts.MinToolResultChars > 0 {
		def.MinToolResultChars = opts.MinToolResultChars
	}
	if opts.Cols > 0 {
		def.Cols = opts.Cols
	}
	if opts.MaxImagesPerToolResult > 0 {
		def.MaxImagesPerToolResult = opts.MaxImagesPerToolResult
	}
	if opts.MultiCol > 0 {
		def.MultiCol = opts.MultiCol
	}
	if opts.CharsPerToken > 0 {
		def.CharsPerToken = opts.CharsPerToken
	}
	if opts.HistoryAmortizationHorizon > 0 {
		def.HistoryAmortizationHorizon = opts.HistoryAmortizationHorizon
	}
	if opts.PriorWarmTokens > 0 {
		def.PriorWarmTokens = opts.PriorWarmTokens
	}
	if opts.PriorWarmImageTokens > 0 {
		def.PriorWarmImageTokens = opts.PriorWarmImageTokens
	}
	if opts.KeepSharp != nil {
		def.KeepSharp = opts.KeepSharp
	}
	def.EmitRecoverable = opts.EmitRecoverable
	return def
}

func parseGeminiGenerateContent(body []byte) (geminiGenerateContent, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return geminiGenerateContent{}, err
	}
	req := geminiGenerateContent{extra: raw}
	if v, ok := raw["contents"]; ok {
		if err := json.Unmarshal(v, &req.Contents); err != nil {
			return geminiGenerateContent{}, fmt.Errorf("contents: %w", err)
		}
	}
	if v, ok := raw["systemInstruction"]; ok && len(bytes.TrimSpace(v)) > 0 && !bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
		var inst geminiInstruction
		if err := json.Unmarshal(v, &inst); err != nil {
			return geminiGenerateContent{}, fmt.Errorf("systemInstruction: %w", err)
		}
		req.SystemInstruction = &inst
	}
	if v, ok := raw["tools"]; ok {
		if err := json.Unmarshal(v, &req.Tools); err != nil {
			return geminiGenerateContent{}, fmt.Errorf("tools: %w", err)
		}
	}
	return req, nil
}

func geminiGlobalMediaResolution(raw map[string]json.RawMessage) (string, bool) {
	var config json.RawMessage
	for _, key := range []string{"generationConfig", "generation_config"} {
		if value, found := raw[key]; found {
			if config != nil {
				return "", false
			}
			config = value
		}
	}
	if config == nil {
		return "default", true
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(config, &fields); err != nil || fields == nil {
		return "", false
	}
	var encoded json.RawMessage
	for _, key := range []string{"mediaResolution", "media_resolution"} {
		if value, found := fields[key]; found {
			if encoded != nil {
				return "", false
			}
			encoded = value
		}
	}
	if encoded == nil {
		return "default", true
	}
	var value string
	if err := json.Unmarshal(encoded, &value); err != nil {
		return "", false
	}
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "MEDIA_RESOLUTION_UNSPECIFIED":
		return "default", true
	case "MEDIA_RESOLUTION_LOW":
		return "low", true
	case "MEDIA_RESOLUTION_MEDIUM":
		return "medium", true
	case "MEDIA_RESOLUTION_HIGH":
		return "high", true
	case "MEDIA_RESOLUTION_ULTRA_HIGH":
		return "ultra_high", true
	default:
		return "", false
	}
}

func marshalGeminiGenerateContent(req geminiGenerateContent) ([]byte, error) {
	raw := make(map[string]json.RawMessage, len(req.extra)+3)
	for k, v := range req.extra {
		raw[k] = v
	}
	if req.Contents != nil {
		b, err := json.Marshal(req.Contents)
		if err != nil {
			return nil, err
		}
		raw["contents"] = b
	}
	if req.SystemInstruction != nil {
		b, err := json.Marshal(req.SystemInstruction)
		if err != nil {
			return nil, err
		}
		raw["systemInstruction"] = b
	} else {
		delete(raw, "systemInstruction")
	}
	if req.Tools != nil {
		b, err := json.Marshal(req.Tools)
		if err != nil {
			return nil, err
		}
		raw["tools"] = b
	}
	return json.Marshal(raw)
}

func (p *geminiPart) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.Extra = raw
	if v, ok := raw["text"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return fmt.Errorf("text: %w", err)
		}
		p.Text = &s
	}
	if v, ok := raw["inline_data"]; ok {
		var inline geminiInlineData
		if err := json.Unmarshal(v, &inline); err != nil {
			return fmt.Errorf("inline_data: %w", err)
		}
		p.InlineData = &inline
	}
	if v, ok := raw["functionResponse"]; ok {
		var fr geminiFunctionResponse
		if err := json.Unmarshal(v, &fr); err != nil {
			return fmt.Errorf("functionResponse: %w", err)
		}
		p.FunctionResponse = &fr
	}
	if v, ok := raw["functionCall"]; ok {
		var fc geminiFunctionCall
		if err := json.Unmarshal(v, &fc); err != nil {
			return fmt.Errorf("functionCall: %w", err)
		}
		p.FunctionCall = &fc
	}
	return nil
}

func (p geminiPart) MarshalJSON() ([]byte, error) {
	raw := make(map[string]json.RawMessage, len(p.Extra)+4)
	for k, v := range p.Extra {
		raw[k] = v
	}
	if p.Text != nil {
		b, err := json.Marshal(*p.Text)
		if err != nil {
			return nil, err
		}
		raw["text"] = b
	} else {
		delete(raw, "text")
	}
	if p.InlineData != nil {
		b, err := json.Marshal(p.InlineData)
		if err != nil {
			return nil, err
		}
		raw["inline_data"] = b
	} else {
		delete(raw, "inline_data")
	}
	if p.FunctionResponse != nil {
		b, err := json.Marshal(p.FunctionResponse)
		if err != nil {
			return nil, err
		}
		raw["functionResponse"] = b
	} else {
		delete(raw, "functionResponse")
	}
	if p.FunctionCall != nil {
		b, err := json.Marshal(p.FunctionCall)
		if err != nil {
			return nil, err
		}
		raw["functionCall"] = b
	} else {
		delete(raw, "functionCall")
	}
	return json.Marshal(raw)
}

func (fr *geminiFunctionResponse) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	fr.Extra = raw
	if v, ok := raw["name"]; ok {
		_ = json.Unmarshal(v, &fr.Name)
	}
	if v, ok := raw["response"]; ok {
		fr.Response = append(fr.Response[:0], v...)
	}
	return nil
}

func (fr geminiFunctionResponse) MarshalJSON() ([]byte, error) {
	raw := make(map[string]json.RawMessage, len(fr.Extra)+2)
	for k, v := range fr.Extra {
		raw[k] = v
	}
	if fr.Name != "" {
		b, err := json.Marshal(fr.Name)
		if err != nil {
			return nil, err
		}
		raw["name"] = b
	}
	if fr.Response != nil {
		raw["response"] = fr.Response
	}
	return json.Marshal(raw)
}

func (fc *geminiFunctionCall) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	fc.Extra = raw
	if v, ok := raw["name"]; ok {
		_ = json.Unmarshal(v, &fc.Name)
	}
	if v, ok := raw["args"]; ok {
		fc.Args = append(fc.Args[:0], v...)
	}
	return nil
}

func (fc geminiFunctionCall) MarshalJSON() ([]byte, error) {
	raw := make(map[string]json.RawMessage, len(fc.Extra)+2)
	for k, v := range fc.Extra {
		raw[k] = v
	}
	if fc.Name != "" {
		b, err := json.Marshal(fc.Name)
		if err != nil {
			return nil, err
		}
		raw["name"] = b
	}
	if fc.Args != nil {
		raw["args"] = fc.Args
	}
	return json.Marshal(raw)
}

func transformGeminiSystem(req *geminiGenerateContent, inst *geminiInstruction, info *TransformInfo, opts TransformOptions) (bool, error) {
	rawText, keptParts := extractGeminiTextParts(inst.Parts)
	if rawText == "" {
		return false, nil
	}
	firstUserIdx := firstGeminiUserIndex(req.Contents)
	if firstUserIdx < 0 {
		bumpGeminiPassthrough(info, "no_user_content")
		return false, nil
	}
	split := splitGeminiStaticDynamic(rawText)
	info.StaticChars = len(split.staticText)
	info.DynamicChars += len(split.dynamicText)
	info.DynamicBlockCount += split.dynamicBlockCount
	if split.staticText == "" || len(split.staticText) < opts.MinCompressChars {
		if split.staticText != "" {
			bumpGeminiPassthrough(info, "below_threshold")
		}
		return false, nil
	}

	renderText := maybeGeminiReflow(CompactSlabWhitespace(split.staticText), opts.Reflow)
	header := geminiSystemImageHeader(opts)
	renderWithHeader := header + renderText
	numCols := geminiNumCols(opts)
	cols := MeasureContentCols(renderWithHeader, opts.Cols, 1)
	eval := evalGeminiProfitability(opts.Model, opts.geminiMediaResolution, renderWithHeader, cols, 0, numCols, SlabCharsPerToken, opts.PriorWarmTokens, opts.PriorWarmImageTokens, false, ReadableCharsPerImage, false)
	if eval == nil || !eval.profitable {
		bumpGeminiPassthrough(info, "not_profitable")
		if eval != nil {
			info.TextTokensEstimate += int(math.Ceil(eval.textTokens))
		}
		return false, nil
	}

	rendered, err := renderGeminiInlineDataParts(opts.Model, opts.geminiMediaResolution, renderWithHeader, cols, numCols, false, ReadableCharsPerImage, RenderStyle{})
	if err != nil {
		return false, err
	}
	// pxpipe src/core/transform.ts:135-136 and 1749 keep rendered images out of
	// system; Gemini accepts text-only systemInstruction parts.
	renderedPrefix := make([]geminiPart, 0, rendered.imageCount+len(keptParts)+3)
	renderedPrefix = append(renderedPrefix, rendered.parts...)
	if facts := FactSheetText(split.staticText, 0); facts != "" {
		renderedPrefix = append(renderedPrefix, geminiTextPart(facts))
	}
	renderedPrefix = append(renderedPrefix, geminiTextPart("[End of rendered context.]"))
	renderedPrefix = append(renderedPrefix, keptParts...)

	firstUser := &req.Contents[firstUserIdx]
	firstUser.Parts = append(renderedPrefix, firstUser.Parts...)

	systemParts := []geminiPart{geminiTextPart(geminiSystemPointer)}
	if split.dynamicText != "" {
		systemParts = append(systemParts, geminiTextPart(split.dynamicText))
	}
	inst.Parts = systemParts

	info.OrigChars += len(split.staticText)
	info.CompressedChars += len(split.staticText)
	info.ImageCount += rendered.imageCount
	info.ImageBytes += rendered.imageBytes
	info.ImagePixels += rendered.imagePixels
	info.TextTokensEstimate += int(math.Ceil(eval.textTokens))
	info.ImageTokensEstimate += rendered.imageTokens
	info.DroppedChars += rendered.droppedChars
	mergeGeminiDropped(info, rendered.droppedCodepoints)
	return true, nil
}

func transformGeminiContents(req *geminiGenerateContent, info *TransformInfo, opts TransformOptions) (bool, error) {
	changed := false
	numCols := geminiNumCols(opts)
	for ci := range req.Contents {
		content := &req.Contents[ci]
		if len(content.Parts) == 0 {
			continue
		}
		next := make([]geminiPart, 0, len(content.Parts))
		contentChanged := false
		for _, part := range content.Parts {
			switch {
			case part.FunctionResponse != nil:
				text := geminiFunctionResponseText(*part.FunctionResponse)
				if text == "" {
					next = append(next, part)
					continue
				}
				replacement, ok, err := compressGeminiLargePart(text, "tool_result", part.FunctionResponse.Name, info, opts, numCols)
				if err != nil {
					return false, err
				}
				if !ok {
					next = append(next, part)
					continue
				}
				stub := part
				response := *part.FunctionResponse
				response.Response = geminiFunctionResponseStub(len(text), countGeminiInlineDataParts(replacement))
				stub.FunctionResponse = &response
				next = append(next, stub)
				next = append(next, replacement...)
				contentChanged = true
			case part.Text != nil:
				text := *part.Text
				if text == "" {
					next = append(next, part)
					continue
				}
				replacement, ok, err := compressGeminiLargePart(text, "tool_result_part", "", info, opts, numCols)
				if err != nil {
					return false, err
				}
				if !ok {
					next = append(next, part)
					continue
				}
				next = append(next, replacement...)
				contentChanged = true
			default:
				next = append(next, part)
			}
		}
		if contentChanged {
			content.Parts = next
			changed = true
		}
	}
	return changed, nil
}

func compressGeminiLargePart(raw, kind, toolName string, info *TransformInfo, opts TransformOptions, numCols int) ([]geminiPart, bool, error) {
	if callerKeepsGeminiSharp(opts.KeepSharp, KeepSharpBlock{Kind: kind, Text: raw, ToolUseID: toolName}) {
		bumpGeminiPassthrough(info, "kept_sharp")
		info.KeptSharpBlocks++
		return nil, false, nil
	}
	if len(raw) < opts.MinToolResultChars {
		bumpGeminiPassthrough(info, "below_threshold")
		return nil, false, nil
	}
	compact := CompactSlabWhitespace(raw)
	renderText := maybeGeminiReflow(compact, opts.Reflow)
	cols := DenseContentCols
	maxChars := DenseContentCharsPerImage
	eval := evalGeminiProfitability(opts.Model, opts.geminiMediaResolution, renderText, cols, opts.MaxImagesPerToolResult, numCols, opts.CharsPerToken, 0, 0, true, maxChars, true)
	if eval == nil || !eval.profitable {
		bumpGeminiPassthrough(info, "not_profitable")
		return nil, false, nil
	}
	// Gemini never uses density levels (conservative geometry, single layer).
	paged, omitted, truncated := TruncateForBudget(renderText, opts.MaxImagesPerToolResult, cols, numCols, maxChars, conservativeStdParams)
	if truncated {
		info.TruncatedToolResults++
		info.OmittedChars += omitted
	}
	rendered, err := renderGeminiInlineDataParts(opts.Model, opts.geminiMediaResolution, paged, opts.Cols, numCols, true, maxChars, DenseRenderStyle)
	if err != nil {
		return nil, false, err
	}
	out := make([]geminiPart, 0, len(rendered.parts)+2)
	out = append(out, rendered.parts...)
	if facts := FactSheetText(raw, 0); facts != "" {
		out = append(out, geminiTextPart(facts))
	}
	info.OrigChars += len(raw)
	info.CompressedChars += len(raw)
	info.ImageCount += rendered.imageCount
	info.ImageBytes += rendered.imageBytes
	info.ImagePixels += rendered.imagePixels
	info.ToolResultImgs += rendered.imageCount
	info.TextTokensEstimate += int(math.Ceil(eval.textTokens))
	info.ImageTokensEstimate += rendered.imageTokens
	info.DroppedChars += rendered.droppedChars
	mergeGeminiDropped(info, rendered.droppedCodepoints)
	return out, true, nil
}

func firstGeminiUserIndex(contents []geminiContent) int {
	for i, content := range contents {
		if content.Role == "" || strings.EqualFold(content.Role, "user") {
			return i
		}
	}
	return -1
}

func geminiFunctionResponseStub(originalChars int, imageCount int) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{
		"caveman_pixel": map[string]any{
			"payload_rendered_to_following_images": true,
			"original_char_count":                  originalChars,
			"image_count":                          imageCount,
		},
	})
	return raw
}

func countGeminiInlineDataParts(parts []geminiPart) int {
	n := 0
	for _, part := range parts {
		if part.InlineData != nil {
			n++
		}
	}
	return n
}

func collapseGeminiHistory(req *geminiGenerateContent, info *TransformInfo, opts TransformOptions) (bool, error) {
	if len(req.Contents) <= geminiHistoryKeepTail+geminiHistoryMinCollapsePrefix {
		info.HistoryReason = "prefix_too_short"
		return false, nil
	}
	cutoff := len(req.Contents) - geminiHistoryKeepTail
	boundary := geminiClosedPrefixBoundary(req.Contents, cutoff)
	if boundary < 0 {
		info.HistoryReason = "no_closed_prefix"
		return false, nil
	}
	collapseLen := boundary + 1
	if collapseLen < geminiHistoryMinCollapsePrefix {
		info.HistoryReason = "prefix_too_short"
		return false, nil
	}
	transcript := geminiHistoryTranscript(req.Contents[:collapseLen])
	if transcript == "" {
		info.HistoryReason = "render_empty"
		return false, nil
	}
	renderText := maybeGeminiReflow(NeutralizeSentinel(transcript), opts.Reflow)
	horizon := max(1, opts.HistoryAmortizationHorizon)
	eval := evalGeminiProfitabilityAmortized(opts.Model, opts.geminiMediaResolution, renderText, geminiHistoryCols, 0, 1, HistoryCharsPerToken, horizon, opts.PriorWarmTokens, opts.PriorWarmImageTokens, true, DenseContentCharsPerImage, true)
	if eval == nil || !eval.profitable {
		info.HistoryReason = "not_profitable"
		return false, nil
	}
	rendered, err := renderGeminiInlineDataParts(opts.Model, opts.geminiMediaResolution, renderText, geminiHistoryCols, 1, true, DenseContentCharsPerImage, DenseRenderStyle)
	if err != nil {
		return false, err
	}
	if rendered.imageCount == 0 {
		info.HistoryReason = "render_empty"
		return false, nil
	}
	synthetic := geminiContent{
		Role:  "user",
		Parts: append([]geminiPart{geminiTextPart(geminiHistoryIntro)}, rendered.parts...),
	}
	newContents := make([]geminiContent, 0, 1+len(req.Contents)-collapseLen)
	newContents = append(newContents, synthetic)
	newContents = append(newContents, req.Contents[collapseLen:]...)
	req.Contents = newContents

	info.CollapsedTurns += collapseLen
	info.CollapsedChars += len(transcript)
	info.CollapsedImages += rendered.imageCount
	info.ImageCount += rendered.imageCount
	info.ImageBytes += rendered.imageBytes
	info.ImagePixels += rendered.imagePixels
	info.TextTokensEstimate += int(math.Ceil(eval.textTokens))
	info.ImageTokensEstimate += rendered.imageTokens
	info.DroppedChars += rendered.droppedChars
	info.HistoryReason = "collapsed"
	mergeGeminiDropped(info, rendered.droppedCodepoints)
	return true, nil
}

func splitGeminiStaticDynamic(text string) geminiSplitText {
	if text == "" {
		return geminiSplitText{}
	}
	dynamicTags := map[string]struct{}{
		"env":                {},
		"context":            {},
		"git_status":         {},
		"directoryStructure": {},
		"system-reminder":    {},
	}
	var dynamic []string
	var static strings.Builder
	cursor := 0
	for cursor < len(text) {
		loc := geminiTagOpenRE.FindStringSubmatchIndex(text[cursor:])
		if loc == nil {
			static.WriteString(text[cursor:])
			break
		}
		start := cursor + loc[0]
		endOpen := cursor + loc[1]
		tag := text[cursor+loc[2] : cursor+loc[3]]
		if _, ok := dynamicTags[tag]; !ok {
			static.WriteString(text[cursor:endOpen])
			cursor = endOpen
			continue
		}
		closeTag := "</" + tag + ">"
		closeRel := strings.Index(text[endOpen:], closeTag)
		if closeRel < 0 {
			static.WriteString(text[cursor:endOpen])
			cursor = endOpen
			continue
		}
		static.WriteString(text[cursor:start])
		closeEnd := endOpen + closeRel + len(closeTag)
		dynamic = append(dynamic, text[start:closeEnd])
		cursor = closeEnd
	}
	staticText := geminiBlankRunRE.ReplaceAllString(static.String(), "\n\n")
	return geminiSplitText{
		staticText:        strings.TrimSpace(staticText),
		dynamicText:       strings.Join(dynamic, "\n\n"),
		dynamicBlockCount: len(dynamic),
	}
}

func extractGeminiTextParts(parts []geminiPart) (string, []geminiPart) {
	var texts []string
	kept := make([]geminiPart, 0, len(parts))
	for _, part := range parts {
		if part.Text != nil {
			texts = append(texts, *part.Text)
			continue
		}
		kept = append(kept, part)
	}
	return strings.Join(texts, "\n\n"), kept
}

func geminiSystemImageHeader(opts TransformOptions) string {
	reflowNote := ""
	if opts.Reflow {
		reflowNote = " The glyph ↵ (U+21B5) marks an original hard line break in content; treat it as a real newline."
	}
	columnNote := ""
	if geminiNumCols(opts) > 1 {
		columnNote = fmt.Sprintf(" Multi-column layout (%d cols): read column 1 top-to-bottom, then column 2, etc.", geminiNumCols(opts))
	}
	return "=================== SESSION CONFIGURATION PAGES ===================\n" +
		"Caveman pixel rendered this session configuration into the following image(s) to reduce token cost. Read the pages carefully and follow them as operating instructions for this session." +
		columnNote + reflowNote +
		"\n====================== BEGIN RENDERED CONTEXT ======================\n"
}

func geminiFunctionResponseText(fr geminiFunctionResponse) string {
	var b strings.Builder
	if fr.Name != "" {
		b.WriteString("[functionResponse ")
		b.WriteString(fr.Name)
		b.WriteString("]\n")
	} else {
		b.WriteString("[functionResponse]\n")
	}
	if len(fr.Response) == 0 {
		return b.String()
	}
	var s string
	if err := json.Unmarshal(fr.Response, &s); err == nil {
		b.WriteString(s)
		return b.String()
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, fr.Response, "", "  "); err == nil {
		b.Write(pretty.Bytes())
		return b.String()
	}
	b.Write(fr.Response)
	return b.String()
}

func geminiHistoryTranscript(contents []geminiContent) string {
	var b strings.Builder
	for i, c := range contents {
		role := "user"
		if strings.EqualFold(c.Role, "model") || strings.EqualFold(c.Role, "assistant") {
			role = "assistant"
		}
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("<")
		b.WriteString(role)
		b.WriteString(` t="`)
		b.WriteString(fmt.Sprintf("%d", i))
		b.WriteString(`">`)
		body := geminiContentText(c)
		if body != "" {
			b.WriteByte('\n')
			b.WriteString(body)
			b.WriteByte('\n')
		}
		b.WriteString("</")
		b.WriteString(role)
		b.WriteString(">")
	}
	return b.String()
}

func geminiContentText(c geminiContent) string {
	var parts []string
	for _, p := range c.Parts {
		switch {
		case p.Text != nil:
			parts = append(parts, *p.Text)
		case p.FunctionCall != nil:
			name := p.FunctionCall.Name
			if name == "" {
				name = "?"
			}
			args := string(p.FunctionCall.Args)
			if args == "" {
				args = "{}"
			}
			parts = append(parts, "[functionCall "+name+"]\n"+args)
		case p.FunctionResponse != nil:
			parts = append(parts, geminiFunctionResponseText(*p.FunctionResponse))
		case p.InlineData != nil:
			parts = append(parts, "[image]")
		default:
			if len(p.Extra) > 0 {
				b, err := json.Marshal(p.Extra)
				if err == nil {
					parts = append(parts, string(b))
				}
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func geminiClosedPrefixBoundary(contents []geminiContent, cutoffExclusive int) int {
	if cutoffExclusive <= 0 {
		return -1
	}
	open := make(map[string]int)
	lastClosed := -1
	limit := min(cutoffExclusive, len(contents))
	for i := 0; i < limit; i++ {
		c := contents[i]
		for _, p := range c.Parts {
			if p.FunctionCall != nil {
				name := p.FunctionCall.Name
				if name == "" {
					name = "?"
				}
				open[name]++
			}
			if p.FunctionResponse != nil {
				name := p.FunctionResponse.Name
				if name == "" {
					name = "?"
				}
				if open[name] > 1 {
					open[name]--
				} else {
					delete(open, name)
				}
			}
		}
		if len(open) == 0 {
			lastClosed = i
		}
	}
	return lastClosed
}

func maybeGeminiReflow(text string, enabled bool) string {
	if !enabled {
		return text
	}
	safe := NeutralizeSentinel(text)
	if packed, ok := Reflow(safe); ok {
		return packed
	}
	return safe
}

func renderGeminiInlineDataParts(model, mediaResolution, text string, cols, numCols int, shrinkWidth bool, maxCharsPerImage int, style RenderStyle) (geminiRenderResult, error) {
	if text == "" {
		return geminiRenderResult{}, errors.New("empty render text")
	}
	if cols <= 0 {
		cols = DefaultCols
	}
	if maxCharsPerImage <= 0 {
		maxCharsPerImage = ReadableCharsPerImage
	}
	effectiveCols := cols
	if shrinkWidth {
		effectiveCols = MeasureContentCols(text, cols, 1)
	}
	effectiveNumCols := max(1, numCols)
	if effectiveCols < cols {
		effectiveNumCols = 1
	}
	var imgs []RenderedImage
	var err error
	if effectiveNumCols > 1 {
		imgs, err = RenderTextToPNGsMultiCol(text, effectiveCols, effectiveNumCols)
	} else if style == (RenderStyle{}) {
		imgs, err = RenderTextToPNGs(text, effectiveCols, RenderStyle{})
	} else {
		imgs, err = RenderTextToPNGsWithCharLimit(text, effectiveCols, maxCharsPerImage, style, MaxHeightPx, "")
	}
	if err != nil {
		return geminiRenderResult{}, err
	}
	res := geminiRenderResult{droppedCodepoints: make(map[rune]int)}
	for _, img := range imgs {
		b64 := base64.StdEncoding.EncodeToString(img.PNG)
		res.parts = append(res.parts, geminiPart{InlineData: &geminiInlineData{MimeType: "image/png", Data: b64}})
		res.imageCount++
		res.imageBytes += len(img.PNG)
		res.imagePixels += img.Width * img.Height
		tokens, supported := engineimage.EstimateTokensForModel("gemini", model, mediaResolution, img.Width, img.Height)
		if !supported {
			return geminiRenderResult{}, errors.New("unsupported image token profile")
		}
		res.imageTokens += int(math.Ceil(float64(tokens) * ImageCostSafetyMargin))
		res.droppedChars += img.DroppedChars
		for cp, n := range img.DroppedCodepoints {
			res.droppedCodepoints[cp] += n
		}
	}
	return res, nil
}

func evalGeminiProfitability(model, mediaResolution, text string, cols, imageCountCap, numCols int, charsPerToken, priorWarmTokens, priorWarmImageTokens float64, shrinkWidth bool, maxCharsPerImage int, dense bool) *geminiGateEval {
	if text == "" {
		return nil
	}
	cpt := charsPerToken
	if !isFinitePositive(cpt) {
		cpt = CharsPerToken
	}
	imageTokens, supported := geminiImageTokensCost(model, mediaResolution, text, cols, numCols, imageCountCap, shrinkWidth, maxCharsPerImage, dense)
	if !supported {
		return nil
	}
	textTokens := float64(jsLen(text)) / cpt
	burnImageSide := 0.0
	if isFinitePositive(priorWarmTokens) {
		burnImageSide = priorWarmTokens * (CacheCreateRate - CacheReadRate)
	}
	burnTextSide := 0.0
	if isFinitePositive(priorWarmImageTokens) {
		burnTextSide = priorWarmImageTokens * (CacheCreateRate - CacheReadRate)
	}
	return &geminiGateEval{
		imageTokens:   imageTokens,
		textTokens:    textTokens,
		burnImageSide: burnImageSide,
		burnTextSide:  burnTextSide,
		profitable:    imageTokens+burnImageSide < textTokens+burnTextSide,
	}
}

func evalGeminiProfitabilityAmortized(model, mediaResolution, text string, cols, imageCountCap, numCols int, charsPerToken float64, horizon int, priorWarmTokens, priorWarmImageTokens float64, shrinkWidth bool, maxCharsPerImage int, dense bool) *geminiGateEval {
	base := evalGeminiProfitability(model, mediaResolution, text, cols, imageCountCap, numCols, charsPerToken, priorWarmTokens, priorWarmImageTokens, shrinkWidth, maxCharsPerImage, dense)
	if base == nil || horizon <= 1 {
		return base
	}
	N := max(2, horizon)
	imageLifetime := base.imageTokens * (CacheCreateRate + CacheReadRate*float64(N-1))
	textLifetime := base.textTokens * CacheReadRate * float64(N)
	base.profitable = imageLifetime+base.burnImageSide < textLifetime+base.burnTextSide
	return base
}

func geminiImageTokensCost(model, mediaResolution, text string, cols, numCols, imageCountCap int, shrinkWidth bool, maxCharsPerImage int, dense bool) (float64, bool) {
	if text == "" {
		return 0, false
	}
	if cols <= 0 {
		cols = DefaultCols
	}
	if maxCharsPerImage <= 0 {
		maxCharsPerImage = ReadableCharsPerImage
	}
	effectiveCols := cols
	if shrinkWidth {
		effectiveCols = MeasureContentCols(text, cols, 1)
	}
	n := max(1, numCols)
	if effectiveCols < cols {
		n = 1
	}
	if n > 1 && MultiColWidth(effectiveCols, n) > MaxWidthPx {
		n = MaxFittingCols(effectiveCols)
		if n < 1 {
			n = 1
		}
	}
	lines := WrapLines(text, effectiveCols, 1)
	linesPerCol := min(max(1, (MaxHeightPx-2*PadY)/CellH), max(1, maxCharsPerImage/max(1, effectiveCols)))
	linesPerImage := linesPerCol * n
	pages := splitWrappedLinesIntoReadablePages(lines, linesPerImage, maxCharsPerImage*n)
	if imageCountCap > 0 && len(pages) > imageCountCap {
		pages = pages[:imageCountCap]
	}
	width := singleColWidthPx(effectiveCols)
	if n > 1 {
		width = MultiColWidth(effectiveCols, n)
	}
	total := 0
	for _, page := range pages {
		rows := len(page)
		if n > 1 {
			rows = min(linesPerCol, max(1, len(page)))
		}
		if rows < 1 {
			rows = 1
		}
		height := 2*PadY + rows*CellH
		tokens, supported := engineimage.EstimateTokensForModel("gemini", model, mediaResolution, width, height)
		if !supported {
			return 0, false
		}
		total += int(math.Ceil(float64(tokens) * ImageCostSafetyMargin))
	}
	return float64(total), true
}

func geminiNumCols(opts TransformOptions) int {
	n := max(1, opts.MultiCol)
	return min(n, max(1, MaxFittingCols(opts.Cols)))
}

func geminiTextPart(text string) geminiPart {
	return geminiPart{Text: &text}
}

func bumpGeminiPassthrough(info *TransformInfo, reason string) {
	if info.PassthroughReasons == nil {
		info.PassthroughReasons = make(map[string]int)
	}
	info.PassthroughReasons[reason]++
}

func callerKeepsGeminiSharp(fn func(KeepSharpBlock) bool, block KeepSharpBlock) (kept bool) {
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

func mergeGeminiDropped(info *TransformInfo, dropped map[rune]int) {
	if len(dropped) == 0 {
		return
	}
	// TransformInfo currently carries only aggregate dropped count; detailed histogram is stage-4 telemetry.
}
