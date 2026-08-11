package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/JuliusBrussee/caveman/engine/pixel"
	"github.com/JuliusBrussee/caveman/proxy/providers"
	anthropicprovider "github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/shared/platform/redact"
)

const pixelOptimizerID = "pixel-render"

// pixelRequest applies S4 text-to-PNG compression to provider wire formats. It
// gates by measured model allowlist, stores the original before publishing lossy
// bytes, and reports inferred estimates only.
func (s *Server) pixelRequest(adapter providers.Adapter, body []byte, meta providers.RequestMetadata, transform *providers.TransformResult, requestID string) *compressionOutcome {
	if s.compressor == nil || !pixel.Allowed(meta.Model) {
		return nil
	}

	opts := pixel.DefaultTransformOptions(meta.Model)
	out, info, err := transformPixelLiveZone(adapter, meta, body, opts)
	if err != nil || info.ImageCount == 0 || len(out) == 0 {
		return nil
	}
	before := info.TextTokensEstimate
	if before <= 0 {
		return nil
	}

	handle, err := s.compressor.StoreOriginal(body)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("pixel recovery store failed; forwarding original bytes unchanged", "error", redact.Error(err), "request_id", requestID)
		}
		return nil
	}

	transform.Body = out
	transform.OptimizerIDs = append(transform.OptimizerIDs, pixelOptimizerID)
	return &compressionOutcome{
		handle:      handle,
		before:      before,
		after:       info.ImageTokensEstimate,
		ratio:       float64(before-info.ImageTokensEstimate) / float64(before),
		bookSavings: false,
	}
}

type pixelReplacement struct {
	span       gatewayJSONSpan
	raw        []byte
	before     int
	after      int
	imageCount int
	imageBytes int
}

func transformPixelLiveZone(adapter providers.Adapter, meta providers.RequestMetadata, body []byte, opts pixel.TransformOptions) ([]byte, pixel.TransformInfo, error) {
	provider := meta.Provider
	if adapter != nil {
		// Pass the full metadata through: adapters gate on Endpoint too (e.g.
		// Anthropic count_tokens opts out of any content transform).
		if segments, _, ok := adapter.ExtractCompressible(body, meta); !ok || len(segments) == 0 {
			return nil, pixel.TransformInfo{Reason: "no_live_zone"}, nil
		}
	}
	switch provider {
	case "anthropic":
		return transformAnthropicPixelLiveZone(body, opts)
	case "openai", "azure_openai", "openai_compatible":
		return transformOpenAIPixelLiveZone(body, opts)
	default:
		return nil, pixel.TransformInfo{Reason: "unsupported_live_zone_pixel"}, nil
	}
}

func transformAnthropicPixelLiveZone(body []byte, opts pixel.TransformOptions) ([]byte, pixel.TransformInfo, error) {
	root, ok := gatewayRootObjectSpan(body)
	if !ok {
		return nil, pixel.TransformInfo{Reason: "parse_error"}, nil
	}
	messagesSpan, ok := gatewayFindObjectField(body, root, "messages")
	if !ok || messagesSpan.start >= len(body) || body[messagesSpan.start] != '[' {
		return nil, pixel.TransformInfo{Reason: "no_messages"}, nil
	}
	messageSpans, ok := gatewayArrayElements(body, messagesSpan)
	if !ok {
		return nil, pixel.TransformInfo{Reason: "parse_error"}, nil
	}
	rawMessages := make([]json.RawMessage, 0, len(messageSpans))
	for _, span := range messageSpans {
		rawMessages = append(rawMessages, append(json.RawMessage(nil), body[span.start:span.end]...))
	}
	floor := anthropicprovider.ComputeFrozenCount(rawMessages)
	target := -1
	for i := len(messageSpans) - 1; i >= floor; i-- {
		if gatewayObjectStringField(body, messageSpans[i], "role") == "user" {
			target = i
			break
		}
	}
	if target < 0 {
		return nil, pixel.TransformInfo{Reason: "no_live_user"}, nil
	}
	var reps []pixelReplacement
	collectAnthropicPixelCandidates(body, messageSpans[target], opts, &reps)
	return applyPixelReplacements(body, reps)
}

func collectAnthropicPixelCandidates(body []byte, msg gatewayJSONSpan, opts pixel.TransformOptions, reps *[]pixelReplacement) {
	content, ok := gatewayFindObjectField(body, msg, "content")
	if !ok {
		return
	}
	switch {
	case gatewayIsJSONString(body, content):
		if rep, ok := renderAnthropicPixelValue(body, content, opts, opts.MinCompressChars); ok {
			*reps = append(*reps, rep)
		}
	case content.start < content.end && body[content.start] == '[':
		collectAnthropicPixelBlocks(body, content, opts, reps)
	}
}

func collectAnthropicPixelBlocks(body []byte, blocksSpan gatewayJSONSpan, opts pixel.TransformOptions, reps *[]pixelReplacement) {
	blocks, ok := gatewayArrayElements(body, blocksSpan)
	if !ok {
		return
	}
	for _, block := range blocks {
		if block.start >= block.end || body[block.start] != '{' {
			continue
		}
		switch gatewayObjectStringField(body, block, "type") {
		case "text":
			textSpan, ok := gatewayFindObjectField(body, block, "text")
			if !ok || !gatewayIsJSONString(body, textSpan) {
				continue
			}
			if rep, ok := renderAnthropicPixelBlock(body, block, textSpan, opts, opts.MinCompressChars); ok {
				*reps = append(*reps, rep)
			}
		case "tool_result":
			content, ok := gatewayFindObjectField(body, block, "content")
			if !ok {
				continue
			}
			switch {
			case gatewayIsJSONString(body, content):
				if rep, ok := renderAnthropicPixelValue(body, content, opts, opts.MinToolResultChars); ok {
					*reps = append(*reps, rep)
				}
			case content.start < content.end && body[content.start] == '[':
				collectAnthropicPixelBlocks(body, content, opts, reps)
			}
		}
	}
}

func transformOpenAIPixelLiveZone(body []byte, opts pixel.TransformOptions) ([]byte, pixel.TransformInfo, error) {
	root, ok := gatewayRootObjectSpan(body)
	if !ok {
		return nil, pixel.TransformInfo{Reason: "parse_error"}, nil
	}
	messagesSpan, ok := gatewayFindObjectField(body, root, "messages")
	if ok {
		return transformOpenAIChatPixelLiveZone(body, messagesSpan, opts)
	}
	inputSpan, ok := gatewayFindObjectField(body, root, "input")
	if ok {
		return transformOpenAIResponsesPixelLiveZone(body, inputSpan, opts)
	}
	return nil, pixel.TransformInfo{Reason: "no_messages_or_input"}, nil
}

func transformOpenAIChatPixelLiveZone(body []byte, messagesSpan gatewayJSONSpan, opts pixel.TransformOptions) ([]byte, pixel.TransformInfo, error) {
	if messagesSpan.start >= len(body) || body[messagesSpan.start] != '[' {
		return nil, pixel.TransformInfo{Reason: "messages_not_array"}, nil
	}
	messageSpans, ok := gatewayArrayElements(body, messagesSpan)
	if !ok {
		return nil, pixel.TransformInfo{Reason: "parse_error"}, nil
	}
	target := -1
	for i := len(messageSpans) - 1; i >= 0; i-- {
		if gatewayObjectStringField(body, messageSpans[i], "role") == "user" {
			target = i
			break
		}
	}
	if target < 0 {
		return nil, pixel.TransformInfo{Reason: "no_live_user"}, nil
	}
	var reps []pixelReplacement
	content, ok := gatewayFindObjectField(body, messageSpans[target], "content")
	if !ok {
		return nil, pixel.TransformInfo{Reason: "no_content"}, nil
	}
	switch {
	case gatewayIsJSONString(body, content):
		if rep, ok := renderOpenAIPixelValue(body, content, opts); ok {
			reps = append(reps, rep)
		}
	case content.start < content.end && body[content.start] == '[':
		parts, ok := gatewayArrayElements(body, content)
		if !ok {
			return nil, pixel.TransformInfo{Reason: "parse_error"}, nil
		}
		for _, part := range parts {
			if part.start >= part.end || body[part.start] != '{' {
				continue
			}
			if typ := gatewayObjectStringField(body, part, "type"); typ != "text" && typ != "input_text" {
				continue
			}
			textSpan, ok := gatewayFindObjectField(body, part, "text")
			if !ok || !gatewayIsJSONString(body, textSpan) {
				continue
			}
			if rep, ok := renderOpenAIPixelPart(body, part, textSpan, opts); ok {
				reps = append(reps, rep)
			}
		}
	}
	return applyPixelReplacements(body, reps)
}

func transformOpenAIResponsesPixelLiveZone(body []byte, inputSpan gatewayJSONSpan, opts pixel.TransformOptions) ([]byte, pixel.TransformInfo, error) {
	if gatewayIsJSONString(body, inputSpan) {
		text, ok := gatewayDecodeJSONString(body[inputSpan.start:inputSpan.end])
		if !ok || len(text) < opts.MinCompressChars {
			return nil, pixel.TransformInfo{Reason: "below_min_chars"}, nil
		}
		parts, before, after, imageBytes, imageCount, ok := openAIResponsesImageParts(text, opts)
		if !ok {
			return nil, pixel.TransformInfo{Reason: "not_profitable"}, nil
		}
		raw := make([]byte, 0, len(parts)+64)
		raw = append(raw, `[{"type":"message","role":"user","content":[`...)
		raw = append(raw, parts...)
		raw = append(raw, `]}]`...)
		return applyPixelReplacements(body, []pixelReplacement{{span: inputSpan, raw: raw, before: before, after: after, imageCount: imageCount, imageBytes: imageBytes}})
	}
	if inputSpan.start >= inputSpan.end || body[inputSpan.start] != '[' {
		return nil, pixel.TransformInfo{Reason: "input_not_string_or_array"}, nil
	}
	items, ok := gatewayArrayElements(body, inputSpan)
	if !ok {
		return nil, pixel.TransformInfo{Reason: "parse_error"}, nil
	}
	latestUser, latestTool := -1, -1
	recoveredCalls := map[string]bool{}
	for i, item := range items {
		if item.start >= item.end || body[item.start] != '{' {
			continue
		}
		typ := gatewayObjectStringField(body, item, "type")
		switch {
		case typ == "function_call_output":
			latestTool = i
		case typ == "function_call":
			if providers.IsRecoveryToolName(gatewayObjectStringField(body, item, "name")) {
				if callID := gatewayObjectStringField(body, item, "call_id"); callID != "" {
					recoveredCalls[callID] = true
				}
			}
		case gatewayObjectStringField(body, item, "role") == "user":
			latestUser = i
		}
	}
	var reps []pixelReplacement
	if latestUser >= 0 {
		collectOpenAIResponsesUserPixelCandidates(body, items[latestUser], opts, &reps)
	}
	if latestTool >= 0 {
		item := items[latestTool]
		if callID := gatewayObjectStringField(body, item, "call_id"); !recoveredCalls[callID] {
			if output, found := gatewayFindObjectField(body, item, "output"); found && gatewayIsJSONString(body, output) {
				if rep, ok := renderOpenAIResponsesPixelValue(body, output, opts, opts.MinToolResultChars); ok {
					reps = append(reps, rep)
				}
			}
		}
	}
	return applyPixelReplacements(body, reps)
}

func collectOpenAIResponsesUserPixelCandidates(body []byte, item gatewayJSONSpan, opts pixel.TransformOptions, reps *[]pixelReplacement) {
	content, ok := gatewayFindObjectField(body, item, "content")
	if !ok {
		return
	}
	if gatewayIsJSONString(body, content) {
		if rep, ok := renderOpenAIResponsesPixelValue(body, content, opts, opts.MinCompressChars); ok {
			*reps = append(*reps, rep)
		}
		return
	}
	if content.start >= content.end || body[content.start] != '[' {
		return
	}
	parts, ok := gatewayArrayElements(body, content)
	if !ok {
		return
	}
	for _, part := range parts {
		typ := gatewayObjectStringField(body, part, "type")
		if typ != "input_text" && typ != "text" {
			continue
		}
		textSpan, found := gatewayFindObjectField(body, part, "text")
		if !found || !gatewayIsJSONString(body, textSpan) {
			continue
		}
		if rep, ok := renderOpenAIResponsesPixelPart(body, part, textSpan, opts); ok {
			*reps = append(*reps, rep)
		}
	}
}

func renderOpenAIResponsesPixelValue(body []byte, valueSpan gatewayJSONSpan, opts pixel.TransformOptions, minChars int) (pixelReplacement, bool) {
	text, ok := gatewayDecodeJSONString(body[valueSpan.start:valueSpan.end])
	if !ok || len(text) < minChars {
		return pixelReplacement{}, false
	}
	parts, before, after, imageBytes, imageCount, ok := openAIResponsesImageParts(text, opts)
	if !ok {
		return pixelReplacement{}, false
	}
	return pixelReplacement{span: valueSpan, raw: append(append([]byte("["), parts...), ']'), before: before, after: after, imageCount: imageCount, imageBytes: imageBytes}, true
}

func renderOpenAIResponsesPixelPart(body []byte, partSpan, textSpan gatewayJSONSpan, opts pixel.TransformOptions) (pixelReplacement, bool) {
	text, ok := gatewayDecodeJSONString(body[textSpan.start:textSpan.end])
	if !ok || len(text) < opts.MinCompressChars {
		return pixelReplacement{}, false
	}
	parts, before, after, imageBytes, imageCount, ok := openAIResponsesImageParts(text, opts)
	if !ok {
		return pixelReplacement{}, false
	}
	return pixelReplacement{span: partSpan, raw: parts, before: before, after: after, imageCount: imageCount, imageBytes: imageBytes}, true
}

func renderAnthropicPixelValue(body []byte, valueSpan gatewayJSONSpan, opts pixel.TransformOptions, minChars int) (pixelReplacement, bool) {
	text, ok := gatewayDecodeJSONString(body[valueSpan.start:valueSpan.end])
	if !ok || len(text) < minChars {
		return pixelReplacement{}, false
	}
	blocks, before, after, imageBytes, ok := anthropicImageBlocks(text, opts)
	if !ok {
		return pixelReplacement{}, false
	}
	return pixelReplacement{span: valueSpan, raw: append(append([]byte("["), blocks...), ']'), before: before, after: after, imageCount: bytes.Count(blocks, []byte(`"type":"image"`)), imageBytes: imageBytes}, true
}

func renderAnthropicPixelBlock(body []byte, blockSpan, textSpan gatewayJSONSpan, opts pixel.TransformOptions, minChars int) (pixelReplacement, bool) {
	text, ok := gatewayDecodeJSONString(body[textSpan.start:textSpan.end])
	if !ok || len(text) < minChars {
		return pixelReplacement{}, false
	}
	blocks, before, after, imageBytes, ok := anthropicImageBlocks(text, opts)
	if !ok {
		return pixelReplacement{}, false
	}
	return pixelReplacement{span: blockSpan, raw: blocks, before: before, after: after, imageCount: bytes.Count(blocks, []byte(`"type":"image"`)), imageBytes: imageBytes}, true
}

func renderOpenAIPixelValue(body []byte, valueSpan gatewayJSONSpan, opts pixel.TransformOptions) (pixelReplacement, bool) {
	text, ok := gatewayDecodeJSONString(body[valueSpan.start:valueSpan.end])
	if !ok || len(text) < opts.MinCompressChars {
		return pixelReplacement{}, false
	}
	parts, before, after, imageBytes, ok := openAIImageParts(text, opts)
	if !ok {
		return pixelReplacement{}, false
	}
	return pixelReplacement{span: valueSpan, raw: append(append([]byte("["), parts...), ']'), before: before, after: after, imageCount: bytes.Count(parts, []byte(`"type":"image_url"`)), imageBytes: imageBytes}, true
}

func renderOpenAIPixelPart(body []byte, partSpan, textSpan gatewayJSONSpan, opts pixel.TransformOptions) (pixelReplacement, bool) {
	text, ok := gatewayDecodeJSONString(body[textSpan.start:textSpan.end])
	if !ok || len(text) < opts.MinCompressChars {
		return pixelReplacement{}, false
	}
	parts, before, after, imageBytes, ok := openAIImageParts(text, opts)
	if !ok {
		return pixelReplacement{}, false
	}
	return pixelReplacement{span: partSpan, raw: parts, before: before, after: after, imageCount: bytes.Count(parts, []byte(`"type":"image_url"`)), imageBytes: imageBytes}, true
}

func anthropicImageBlocks(text string, opts pixel.TransformOptions) ([]byte, int, int, int, bool) {
	images, before, after, imageBytes, ok := renderLiveZonePNGs(text, opts)
	if !ok {
		return nil, 0, 0, 0, false
	}
	blocks := make([]json.RawMessage, 0, len(images))
	for _, img := range images {
		b, _ := json.Marshal(map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": "image/png",
				"data":       base64.StdEncoding.EncodeToString(img.PNG),
			},
		})
		blocks = append(blocks, b)
	}
	return bytes.Join(rawMessagesToBytes(blocks), []byte(",")), before, after, imageBytes, true
}

func openAIImageParts(text string, opts pixel.TransformOptions) ([]byte, int, int, int, bool) {
	images, before, after, imageBytes, ok := renderLiveZonePNGs(text, opts)
	if !ok {
		return nil, 0, 0, 0, false
	}
	parts := make([]json.RawMessage, 0, len(images))
	for _, img := range images {
		b, _ := json.Marshal(map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(img.PNG),
			},
		})
		parts = append(parts, b)
	}
	return bytes.Join(rawMessagesToBytes(parts), []byte(",")), before, after, imageBytes, true
}

func openAIResponsesImageParts(text string, opts pixel.TransformOptions) ([]byte, int, int, int, int, bool) {
	images, before, after, imageBytes, ok := renderLiveZonePNGs(text, opts)
	if !ok {
		return nil, 0, 0, 0, 0, false
	}
	parts := make([]json.RawMessage, 0, len(images))
	for _, img := range images {
		b, _ := json.Marshal(map[string]any{
			"type":      "input_image",
			"image_url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(img.PNG),
			"detail":    "high",
		})
		parts = append(parts, b)
	}
	return bytes.Join(rawMessagesToBytes(parts), []byte(",")), before, after, imageBytes, len(images), true
}

func renderLiveZonePNGs(text string, opts pixel.TransformOptions) ([]pixel.RenderedImage, int, int, int, bool) {
	renderText := pixel.MinifyForRender(text)
	if opts.Reflow {
		if reflowed, ok := pixel.Reflow(renderText); ok {
			renderText = reflowed
		}
	}
	// Resolve density from the request's reader model + CAVE_PIXEL_DENSITY, exactly
	// like the transforms do (default balanced; a falsey env or an unrecognised model
	// fail closed to conservative standard-tier geometry). Without this the density
	// env would be dead ink on the proxy's own live-zone render.
	draw := pixel.ResolveDensityDraw(opts.Model, pixel.DensityFromEnv())
	// A non-mono ink scheme needs its reader note in-image so the model reads the
	// colour convention (zebra) or the two-layer overlay order correctly.
	if note := pixel.DensityInkNote(draw.Zebra, draw.Layers); note != "" {
		renderText = strings.TrimSpace(note) + "\n" + renderText
	}
	cols := pixel.MeasureContentCols(renderText, draw.Cols, 1)
	var images []pixel.RenderedImage
	var err error
	if draw.Layers == 2 {
		images, err = pixel.RenderTextToTwoLayerPNGs(renderText, cols, draw.CharBudget, draw.Style, draw.CanvasH)
	} else {
		images, err = pixel.RenderTextToPNGsWithCharLimit(renderText, cols, draw.CharBudget, draw.Style, draw.CanvasH, "")
	}
	if err != nil || len(images) == 0 {
		return nil, 0, 0, 0, false
	}
	before := max(1, int(float64(len(text))/opts.CharsPerToken))
	// Price each emitted image honestly by its actual pixels under the resolved tier
	// (hi-res canvases cost far more than the old flat 100/image would credit) — the
	// inferred savings must never over-count. Both sides stay `inferred`.
	after := 0
	var imageBytes int
	for _, img := range images {
		after += int(math.Ceil(float64(pixel.AnthropicImageTokens(img.Width, img.Height, draw.Tier)) * pixel.ImageCostSafetyMargin))
		imageBytes += len(img.PNG)
	}
	if after >= before {
		return nil, 0, 0, 0, false
	}
	return images, before, after, imageBytes, true
}

func rawMessagesToBytes(raw []json.RawMessage) [][]byte {
	out := make([][]byte, len(raw))
	for i := range raw {
		out[i] = raw[i]
	}
	return out
}

func applyPixelReplacements(body []byte, reps []pixelReplacement) ([]byte, pixel.TransformInfo, error) {
	sort.Slice(reps, func(i, j int) bool { return reps[i].span.start < reps[j].span.start })
	if len(reps) == 0 {
		return nil, pixel.TransformInfo{Reason: "no_profitable_live_blocks"}, nil
	}
	var out []byte
	last := 0
	info := pixel.TransformInfo{Compressed: true}
	for _, rep := range reps {
		if rep.span.start < last || rep.span.end > len(body) {
			return nil, info, fmt.Errorf("pixel live-zone splice overlap")
		}
		out = append(out, body[last:rep.span.start]...)
		out = append(out, rep.raw...)
		last = rep.span.end
		info.TextTokensEstimate += rep.before
		info.ImageTokensEstimate += rep.after
		info.ImageCount += rep.imageCount
		info.ImageBytes += rep.imageBytes
	}
	out = append(out, body[last:]...)
	if !json.Valid(out) {
		return nil, info, fmt.Errorf("pixel live-zone output invalid JSON")
	}
	return out, info, nil
}

func gatewayObjectStringField(body []byte, obj gatewayJSONSpan, field string) string {
	span, ok := gatewayFindObjectField(body, obj, field)
	if !ok || !gatewayIsJSONString(body, span) {
		return ""
	}
	value, ok := gatewayDecodeJSONString(body[span.start:span.end])
	if !ok {
		return ""
	}
	return value
}

func gatewayIsJSONString(body []byte, span gatewayJSONSpan) bool {
	return span.start < span.end && span.start >= 0 && span.end <= len(body) && body[span.start] == '"'
}

func gatewayDecodeJSONString(raw []byte) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

func transformPixelBody(provider string, body []byte, opts pixel.TransformOptions) ([]byte, pixel.TransformInfo, error) {
	switch provider {
	case "anthropic", "bedrock":
		return pixel.TransformAnthropic(body, opts)
	case "openai", "azure_openai", "openai_compatible":
		return pixel.TransformOpenAI(body, opts)
	case "gemini":
		return pixel.TransformGemini(body, opts)
	case "vertex":
		shape := sniffVertexPixelShape(body)
		switch shape {
		case "gemini":
			return pixel.TransformGemini(body, opts)
		case "anthropic":
			return pixel.TransformAnthropic(body, opts)
		default:
			return nil, pixel.TransformInfo{}, nil
		}
	default:
		return nil, pixel.TransformInfo{}, nil
	}
}

func sniffVertexPixelShape(body []byte) string {
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return ""
	}
	if _, ok := root["contents"]; ok {
		return "gemini"
	}
	if _, ok := root["messages"]; ok {
		return "anthropic"
	}
	return ""
}
