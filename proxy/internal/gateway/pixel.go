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
//
// It has rewriteRequest's shape. Decisions on record are followed: a render an
// earlier turn was cached with is re-sent, and a text that went out as text
// stays text. A NEW render is remembered only once the request carrying it is
// known to go out, its original stored and the request spliced. If either
// fails, the new renders go out as text and record that, and the earlier
// renders still go: dropping them would bust the prefix they were cached in.
func (s *Server) pixelRequest(body []byte, meta providers.RequestMetadata, transform *providers.TransformResult, requestID string) *compressionOutcome {
	if s.compressor == nil || !pixel.Allowed(meta.Model) {
		return nil
	}

	d := &pixelDecider{s: s, opts: pixel.DefaultTransformOptions(meta.Model)}
	collectPixelLiveZone(meta, body, d)
	if d.maxImages > 0 {
		// The renders earlier turns were cached with always go; a new one
		// that would pass the cap goes out as text.
		images := d.clientImages
		for _, rep := range d.reps {
			if !rep.fresh {
				images += rep.imageCount
			}
		}
		d.dropFresh(func(rep pixelReplacement) bool {
			if images+rep.imageCount > d.maxImages {
				return true
			}
			images += rep.imageCount
			return false
		})
	}
	if len(d.reps) == 0 {
		return nil
	}
	handle, err := s.compressor.StoreOriginal(body)
	if err != nil || handle == "" {
		if s.logger != nil {
			s.logger.Warn("pixel recovery store failed; nothing new renders this turn", "error", redact.Error(err), "request_id", requestID)
		}
		handle = ""
		d.dropFresh(everyRender)
	}
	out, info, err := applyPixelReplacements(body, d.reps)
	if err != nil && d.dropFresh(everyRender) {
		out, info, err = applyPixelReplacements(body, d.reps)
	}
	if err == nil && d.remember() {
		out, info, err = applyPixelReplacements(body, d.reps)
	}
	if err != nil || len(out) == 0 {
		// ponytail: the memo rows spliced when first sent, so only a
		// non-deterministic splice gets here; that turn goes out as text.
		return nil
	}

	transform.Body = out
	transform.OptimizerIDs = append(transform.OptimizerIDs, pixelOptimizerID)
	// A turn that only re-sends earlier renderings earned nothing new: like a
	// substitution-only compress turn, it books an honest zero.
	ratio := 0.0
	if before := info.TextTokensEstimate; before > 0 {
		ratio = float64(before-info.ImageTokensEstimate) / float64(before)
	}
	return &compressionOutcome{
		handle:      handle,
		before:      info.TextTokensEstimate,
		after:       info.ImageTokensEstimate,
		ratio:       ratio,
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
	// fresh marks a render made by this request, not yet remembered; family,
	// key, parts and wrap are what remembering or dropping it needs.
	fresh  bool
	family pixelFamily
	key    []byte
	parts  []byte
	wrap   func([]byte) []byte
}

// PixelHandle marks a PrefixCache row holding rendered image parts.
const PixelHandle = "pixel"

// anthropicManyImages is the most images a pixel request carries to Anthropic.
// Past 20 images in one request the API rejects any image over 2000 px on a
// side, and the renders are wider (claude-fable-5's are 2573 px).
// ponytail: counts every image as oversized; render narrower past the cap if
// long conversations need more renders.
const anthropicManyImages = 20

// pixelFamily is one provider's image wire shape: render turns text into the
// comma-joined image parts that provider reads, each opening with image.
type pixelFamily struct {
	scope  string
	image  []byte
	render func(text string, opts pixel.TransformOptions) (parts []byte, before, after, imageBytes int, ok bool)
}

var (
	anthropicPixel       = pixelFamily{scope: "pixel:anthropic:", image: []byte(`"type":"image"`), render: anthropicImageBlocks}
	openAIChatPixel      = pixelFamily{scope: "pixel:openai-chat:", image: []byte(`"type":"image_url"`), render: openAIImageParts}
	openAIResponsesPixel = pixelFamily{scope: "pixel:openai-responses:", image: []byte(`"type":"input_image"`), render: openAIResponsesImageParts}
)

func bracketed(parts []byte) []byte { return append(append([]byte("["), parts...), ']') }

func asIs(parts []byte) []byte { return parts }

// keepMarker shapes the images that replace a whole Anthropic block. A
// cache_control on that block moves onto the last image: without it the
// provider writes no entry at the client's breakpoint, and the next turn has
// none to read.
func keepMarker(body []byte, block gatewayJSONSpan) func([]byte) []byte {
	marker, ok := gatewayFindObjectField(body, block, "cache_control")
	if !ok {
		return asIs
	}
	value := body[marker.start:marker.end]
	return func(parts []byte) []byte {
		if len(parts) == 0 {
			return parts
		}
		out := append([]byte(nil), parts[:len(parts)-1]...) // reopen the last image
		return append(append(append(out, `,"cache_control":`...), value...), '}')
	}
}

// pixelDecider makes pixel rendering first-decision-wins across turns, the way
// compressRequest does for text. Pixel used to render only the live message
// and forget it, so the next turn re-sent that message as text and busted the
// prefix the provider had just cached with the images in it. Now a text the
// live turn rendered is re-sent as the same image bytes on every later turn,
// and one that went out as text stays text. Without a PrefixCache (embedders)
// it renders the live message only, as before.
type pixelDecider struct {
	s    *Server
	opts pixel.TransformOptions
	reps []pixelReplacement
	// maxImages caps the images in the request, 0 for no cap, and
	// clientImages counts the ones the client sent.
	maxImages, clientImages int
}

// add decides the JSON string at text. replace is the span its rendering
// replaces and wrap shapes the image parts for that span; only a live text may
// be rendered for the first time, and that render waits in d.reps, fresh, for
// pixelRequest to remember or drop it.
func (d *pixelDecider) add(body []byte, text, replace gatewayJSONSpan, family pixelFamily, live bool, minChars int, wrap func([]byte) []byte) {
	render := func() (pixelReplacement, bool) {
		value, ok := gatewayDecodeJSONString(body[text.start:text.end])
		if !ok || len(value) < minChars {
			return pixelReplacement{}, false
		}
		parts, before, after, imageBytes, ok := family.render(value, d.opts)
		if !ok {
			return pixelReplacement{}, false
		}
		return pixelReplacement{span: replace, raw: wrap(parts), before: before, after: after, imageCount: bytes.Count(parts, family.image), imageBytes: imageBytes,
			fresh: true, family: family, parts: parts, wrap: wrap}, true
	}
	cache := d.s.prefixCache
	if cache == nil {
		if !live {
			return
		}
		if rep, ok := render(); ok {
			d.reps = append(d.reps, rep)
		}
		return
	}
	scope := d.scope(family)
	key := body[text.start:text.end]
	if stored, handle, hit := cache.LookupReplacement(scope, key); hit {
		if handle != RawDecisionHandle {
			d.reps = append(d.reps, pixelReplacement{span: replace, raw: wrap(stored), imageCount: bytes.Count(stored, family.image)})
		}
		return
	}
	// Text while the store could not record it; a stored row outranks that
	// (see rewriteRequest), so it is recorded below like any text decision.
	if live && !d.s.unpersistedRaw.has(scope, key) {
		if rep, ok := render(); ok {
			rep.key = key
			d.reps = append(d.reps, rep)
			return
		}
	}
	d.keepText(family, key, replace, wrap)
}

// scope is the PrefixCache scope of family's decisions. The model is in it:
// geometry is resolved per reader model, and a model switch starts a new
// provider cache anyway.
func (d *pixelDecider) scope(family pixelFamily) string { return family.scope + d.opts.Model }

// keepText records that the text at key goes out as text. If another request
// already decided it (a concurrent fork, a lookup that failed), the store
// answers with that decision and it is followed instead.
func (d *pixelDecider) keepText(family pixelFamily, key []byte, replace gatewayJSONSpan, wrap func([]byte) []byte) {
	if d.s.prefixCache == nil {
		return
	}
	stored, err := d.s.prefixCache.RememberReplacement(d.scope(family), key, nil, RawDecisionHandle)
	if err != nil {
		d.s.unpersistedRaw.add(d.scope(family), key)
	} else if len(stored) > 0 {
		d.reps = append(d.reps, pixelReplacement{span: replace, raw: wrap(stored), imageCount: bytes.Count(stored, family.image)})
	}
}

// dropFresh sends the new renders drop picks (in request order) as text,
// recording that, and reports whether it dropped any.
func (d *pixelDecider) dropFresh(drop func(pixelReplacement) bool) bool {
	var dropped []pixelReplacement
	kept := make([]pixelReplacement, 0, len(d.reps))
	for _, rep := range d.reps {
		if rep.fresh && drop(rep) {
			dropped = append(dropped, rep)
		} else {
			kept = append(kept, rep)
		}
	}
	d.reps = kept
	for _, rep := range dropped {
		d.keepText(rep.family, rep.key, rep.span, rep.wrap)
	}
	return len(dropped) > 0
}

func everyRender(pixelReplacement) bool { return true }

// remember records this request's new renders once it is known to go out. A
// render the store cannot take does not go out at all, since the next turn
// could not re-send it, and one another request decided first gives way to
// that decision. It reports whether any render changed.
func (d *pixelDecider) remember() bool {
	cache := d.s.prefixCache
	if cache == nil {
		return false
	}
	changed := false
	kept := make([]pixelReplacement, 0, len(d.reps))
	for _, rep := range d.reps {
		if !rep.fresh {
			kept = append(kept, rep)
			continue
		}
		stored, err := cache.RememberReplacement(d.scope(rep.family), rep.key, rep.parts, PixelHandle)
		switch {
		case err != nil:
			d.s.unpersistedRaw.add(d.scope(rep.family), rep.key)
			changed = true
		case len(stored) == 0:
			changed = true // text came first
		case !bytes.Equal(stored, rep.parts):
			kept = append(kept, pixelReplacement{span: rep.span, raw: rep.wrap(stored), imageCount: bytes.Count(stored, rep.family.image)})
			changed = true
		default:
			kept = append(kept, rep) // rendered here: books its saving
		}
	}
	d.reps = kept
	return changed
}

// collectPixelLiveZone fills d.reps with the request's pixel decisions.
func collectPixelLiveZone(meta providers.RequestMetadata, body []byte, d *pixelDecider) {
	// A token count must measure the caller's exact prompt: neither a new
	// rendering nor an earlier one may alter it.
	if strings.Contains(meta.Endpoint, "count_tokens") || strings.HasSuffix(meta.Endpoint, "/input_tokens") {
		return
	}
	switch meta.Provider {
	case "anthropic":
		collectAnthropicPixelLiveZone(body, d)
	case "openai", "azure_openai", "openai_compatible":
		collectOpenAIPixelLiveZone(body, d)
	}
}

func collectAnthropicPixelLiveZone(body []byte, d *pixelDecider) {
	root, ok := gatewayRootObjectSpan(body)
	if !ok {
		return
	}
	messagesSpan, ok := gatewayFindObjectField(body, root, "messages")
	if !ok || messagesSpan.start >= len(body) || body[messagesSpan.start] != '[' {
		return
	}
	messageSpans, ok := gatewayArrayElements(body, messagesSpan)
	if !ok {
		return
	}
	d.maxImages = anthropicManyImages
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
	for i, msg := range messageSpans {
		if gatewayObjectStringField(body, msg, "role") == "user" {
			collectAnthropicPixelCandidates(body, msg, d, i == target)
		}
	}
}

func collectAnthropicPixelCandidates(body []byte, msg gatewayJSONSpan, d *pixelDecider, live bool) {
	content, ok := gatewayFindObjectField(body, msg, "content")
	if !ok {
		return
	}
	switch {
	case gatewayIsJSONString(body, content):
		d.add(body, content, content, anthropicPixel, live, d.opts.MinCompressChars, bracketed)
	case content.start < content.end && body[content.start] == '[':
		collectAnthropicPixelBlocks(body, content, d, live)
	}
}

func collectAnthropicPixelBlocks(body []byte, blocksSpan gatewayJSONSpan, d *pixelDecider, live bool) {
	blocks, ok := gatewayArrayElements(body, blocksSpan)
	if !ok {
		return
	}
	for _, block := range blocks {
		if block.start >= block.end || body[block.start] != '{' {
			continue
		}
		switch gatewayObjectStringField(body, block, "type") {
		case "image":
			d.clientImages++
		case "text":
			textSpan, ok := gatewayFindObjectField(body, block, "text")
			if !ok || !gatewayIsJSONString(body, textSpan) {
				continue
			}
			d.add(body, textSpan, block, anthropicPixel, live, d.opts.MinCompressChars, keepMarker(body, block))
		case "tool_result":
			content, ok := gatewayFindObjectField(body, block, "content")
			if !ok {
				continue
			}
			switch {
			case gatewayIsJSONString(body, content):
				d.add(body, content, content, anthropicPixel, live, d.opts.MinToolResultChars, bracketed)
			case content.start < content.end && body[content.start] == '[':
				collectAnthropicPixelBlocks(body, content, d, live)
			}
		}
	}
}

func collectOpenAIPixelLiveZone(body []byte, d *pixelDecider) {
	root, ok := gatewayRootObjectSpan(body)
	if !ok {
		return
	}
	if messagesSpan, ok := gatewayFindObjectField(body, root, "messages"); ok {
		collectOpenAIChatPixelLiveZone(body, messagesSpan, d)
	} else if inputSpan, ok := gatewayFindObjectField(body, root, "input"); ok {
		collectOpenAIResponsesPixelLiveZone(body, inputSpan, d)
	}
}

func collectOpenAIChatPixelLiveZone(body []byte, messagesSpan gatewayJSONSpan, d *pixelDecider) {
	if messagesSpan.start >= len(body) || body[messagesSpan.start] != '[' {
		return
	}
	messageSpans, ok := gatewayArrayElements(body, messagesSpan)
	if !ok {
		return
	}
	target := -1
	for i := len(messageSpans) - 1; i >= 0; i-- {
		if gatewayObjectStringField(body, messageSpans[i], "role") == "user" {
			target = i
			break
		}
	}
	if target < 0 {
		return
	}
	for i, msg := range messageSpans {
		if gatewayObjectStringField(body, msg, "role") != "user" {
			continue
		}
		content, ok := gatewayFindObjectField(body, msg, "content")
		if !ok {
			continue
		}
		live := i == target
		switch {
		case gatewayIsJSONString(body, content):
			d.add(body, content, content, openAIChatPixel, live, d.opts.MinCompressChars, bracketed)
		case content.start < content.end && body[content.start] == '[':
			parts, ok := gatewayArrayElements(body, content)
			if !ok {
				continue
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
				d.add(body, textSpan, part, openAIChatPixel, live, d.opts.MinCompressChars, asIs)
			}
		}
	}
}

func collectOpenAIResponsesPixelLiveZone(body []byte, inputSpan gatewayJSONSpan, d *pixelDecider) {
	if gatewayIsJSONString(body, inputSpan) {
		// A bare string input is one user turn; the next turn sends it back as a
		// message's content string, which shares this decision.
		d.add(body, inputSpan, inputSpan, openAIResponsesPixel, true, d.opts.MinCompressChars, func(parts []byte) []byte {
			return append(append([]byte(`[{"type":"message","role":"user","content":[`), parts...), `]}]`...)
		})
		return
	}
	if inputSpan.start >= inputSpan.end || body[inputSpan.start] != '[' {
		return
	}
	items, ok := gatewayArrayElements(body, inputSpan)
	if !ok {
		return
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
	for i, item := range items {
		if item.start >= item.end || body[item.start] != '{' {
			continue
		}
		if gatewayObjectStringField(body, item, "type") == "function_call_output" {
			if recoveredCalls[gatewayObjectStringField(body, item, "call_id")] {
				continue
			}
			if output, found := gatewayFindObjectField(body, item, "output"); found && gatewayIsJSONString(body, output) {
				d.add(body, output, output, openAIResponsesPixel, i == latestTool, d.opts.MinToolResultChars, bracketed)
			}
			continue
		}
		if gatewayObjectStringField(body, item, "role") == "user" {
			collectOpenAIResponsesUserPixelCandidates(body, item, d, i == latestUser)
		}
	}
}

func collectOpenAIResponsesUserPixelCandidates(body []byte, item gatewayJSONSpan, d *pixelDecider, live bool) {
	content, ok := gatewayFindObjectField(body, item, "content")
	if !ok {
		return
	}
	if gatewayIsJSONString(body, content) {
		d.add(body, content, content, openAIResponsesPixel, live, d.opts.MinCompressChars, bracketed)
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
		d.add(body, textSpan, part, openAIResponsesPixel, live, d.opts.MinCompressChars, asIs)
	}
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

func openAIResponsesImageParts(text string, opts pixel.TransformOptions) ([]byte, int, int, int, bool) {
	images, before, after, imageBytes, ok := renderLiveZonePNGs(text, opts)
	if !ok {
		return nil, 0, 0, 0, false
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
	return bytes.Join(rawMessagesToBytes(parts), []byte(",")), before, after, imageBytes, true
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
