// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/JuliusBrussee/caveman/engine/tokens"
)

const (
	GptHistoryCols      = 152
	GptHistoryMaxImages = 16
)

const HistoryTranscriptIntro = "[Earlier turns of THIS conversation, transcribed in the image(s) below. Each turn is wrapped in <user t=\"N\">...</user> or <assistant t=\"N\">...</assistant> tags, where N is an absolute turn index (larger N = more recent); attribute every turn strictly by its tag, and treat the highest-N turns as the most recent prior context, NOT the low-N opening turns. Earlier turns may contain questions or tasks that were already answered later in this same history; do not reopen low-N turns unless the live text after this block asks you to. This is prior context, NOT the current request.]"
const HistoryTranscriptOutro = "[End of earlier conversation. The current request is the live text that follows below.]"

type GptHistoryOptions struct {
	KeepTail          int
	MinCollapsePrefix int
	MinCollapseTokens int
	Cols              int
	CollapseChunk     int
	FreezeChunk       int
	SectionTokens     int
	MaxHeightPx       int
	MaxImages         int
	Reflow            bool
}

func DefaultGptHistoryOptions() GptHistoryOptions {
	return GptHistoryOptions{
		KeepTail:          6,
		MinCollapsePrefix: 10,
		MinCollapseTokens: 2000,
		Cols:              GptHistoryCols,
		CollapseChunk:     10,
		FreezeChunk:       10,
		SectionTokens:     2000,
		MaxHeightPx:       GptMaxHeightPx,
		MaxImages:         GptHistoryMaxImages,
		Reflow:            true,
	}
}

type GptHistoryTurn struct {
	Text     string
	OpenIDs  []string
	CloseIDs []string
	Opaque   bool
	UserText *string
}

type GptCollapsePlan struct {
	Images            []RenderedImage
	ImagesAfter       []RenderedImage
	PinText           *string
	Text              string
	Start             int
	EndExclusive      int
	CollapsedTurns    int
	CollapsedChars    int
	Reason            string
	DroppedChars      int
	DroppedCodepoints map[rune]int
}

type GptProfitableFunc func(text string, cols int) bool

func PlanGptCollapse(turns []GptHistoryTurn, protectedPrefix int, isProfitable GptProfitableFunc, opts GptHistoryOptions) (GptCollapsePlan, error) {
	o := normalizeGptHistoryOptions(opts)
	base := GptCollapsePlan{DroppedCodepoints: map[rune]int{}}
	pp := min(max(0, protectedPrefix), len(turns))
	rawCutoff := len(turns) - o.KeepTail
	if rawCutoff-pp < o.MinCollapsePrefix {
		base.Reason = "prefix_too_short"
		return base, nil
	}
	cutoff := rawCutoff
	if o.CollapseChunk > 0 {
		cutoff = min(rawCutoff, max(pp+o.MinCollapsePrefix, pp+((rawCutoff-pp)/o.CollapseChunk)*o.CollapseChunk))
	}
	boundary := findGptClosedBoundary(turns, cutoff, pp)
	if boundary < pp {
		base.Reason = "no_closed_prefix"
		return base, nil
	}
	if boundary+1-pp < o.MinCollapsePrefix {
		base.Reason = "prefix_too_short"
		return base, nil
	}
	rawEnd := boundary + 1
	pinIdx := -1
	for i := len(turns) - 1; i >= pp; i-- {
		if turns[i].UserText != nil {
			pinIdx = i
			break
		}
	}
	if pinIdx >= rawEnd {
		pinIdx = -1
	}
	if pinIdx >= 0 && !gptClosedPrefix(turns, pp, pinIdx) {
		pinIdx = -1
	}
	text := joinGptTurns(turns, pp, rawEnd, pinIdx)
	if text == "" || gptCountTokens(text) < o.MinCollapseTokens {
		base.Reason = "below_min_tokens"
		base.CollapsedChars = len(text)
		return base, nil
	}
	renderText := text
	if o.Reflow {
		safeText := NeutralizeSentinel(text)
		if packed, ok := Reflow(safeText); ok {
			renderText = packed
		} else {
			renderText = safeText
		}
	}
	if isProfitable != nil && !isProfitable(renderText, o.Cols) {
		base.Reason = "not_profitable"
		base.CollapsedChars = len(text)
		return base, nil
	}

	sections := make([][2]int, 0)
	if o.FreezeChunk <= 0 {
		if pinIdx > pp {
			sections = append(sections, [2]int{pp, pinIdx})
		}
		afterStart := pp
		if pinIdx >= pp {
			afterStart = pinIdx + 1
		}
		if afterStart < rawEnd {
			sections = append(sections, [2]int{afterStart, rawEnd})
		}
	} else {
		secStart := pp
		acc := 0
		open := make(map[string]struct{})
		for i := pp; i < rawEnd; i++ {
			if i == pinIdx {
				if secStart < i {
					prev := -1
					if len(sections) > 0 && sections[len(sections)-1][1] == secStart {
						prev = len(sections) - 1
					}
					if acc < o.SectionTokens && prev >= 0 {
						sections[prev][1] = i
					} else {
						sections = append(sections, [2]int{secStart, i})
					}
				}
				secStart = i + 1
				acc = 0
				continue
			}
			acc += gptCountTokens(turns[i].Text)
			for _, id := range turns[i].OpenIDs {
				open[id] = struct{}{}
			}
			for _, id := range turns[i].CloseIDs {
				delete(open, id)
			}
			if acc >= o.SectionTokens && len(open) == 0 {
				sections = append(sections, [2]int{secStart, i + 1})
				secStart = i + 1
				acc = 0
			}
		}
	}
	if len(sections) == 0 {
		base.Reason = "below_min_tokens"
		base.CollapsedChars = len(text)
		return base, nil
	}

	type renderedSection struct {
		start, end int
		images     []RenderedImage
	}
	maxImages := max(0, o.MaxImages)
	var rendered []renderedSection
	imgCount := 0
	collapseEnd := pp
	for _, sec := range sections {
		sectionText := joinGptTurns(turns, sec[0], sec[1], -1)
		if sectionText == "" {
			continue
		}
		sectionRender := sectionText
		if o.Reflow {
			safeSection := NeutralizeSentinel(sectionText)
			if packed, ok := Reflow(safeSection); ok {
				sectionRender = packed
			} else {
				sectionRender = safeSection
			}
		}
		images, err := RenderTextToPNGsWithCharLimit(sectionRender, o.Cols, ReadableCharsPerImage, RenderStyle{}, o.MaxHeightPx, "")
		if err != nil {
			return base, err
		}
		if imgCount+len(images) > maxImages {
			break
		}
		rendered = append(rendered, renderedSection{start: sec[0], end: sec[1], images: images})
		imgCount += len(images)
		collapseEnd = sec[1]
	}
	pinConsumed := pinIdx >= pp && collapseEnd > pinIdx
	var before, after []RenderedImage
	for _, r := range rendered {
		if pinConsumed && r.start >= pinIdx+1 {
			after = append(after, r.images...)
		} else {
			before = append(before, r.images...)
		}
	}
	if len(before) == 0 && len(after) == 0 {
		base.Reason = "too_many_images"
		base.CollapsedChars = len(text)
		return base, nil
	}
	collapsedText := joinGptTurns(turns, pp, collapseEnd, -1)
	if pinConsumed {
		collapsedText = joinGptTurns(turns, pp, collapseEnd, pinIdx)
	}
	dropped := make(map[rune]int)
	droppedChars := 0
	for _, img := range append(append([]RenderedImage{}, before...), after...) {
		droppedChars += img.DroppedChars
		for cp, n := range img.DroppedCodepoints {
			dropped[cp] += n
		}
	}
	plan := GptCollapsePlan{
		Images:            before,
		ImagesAfter:       after,
		Text:              collapsedText,
		Start:             pp,
		EndExclusive:      collapseEnd,
		CollapsedTurns:    collapseEnd - pp,
		CollapsedChars:    len(collapsedText),
		DroppedChars:      droppedChars,
		DroppedCodepoints: dropped,
	}
	if pinConsumed {
		plan.PinText = turns[pinIdx].UserText
		plan.CollapsedTurns--
	}
	return plan, nil
}

func normalizeGptHistoryOptions(opts GptHistoryOptions) GptHistoryOptions {
	def := DefaultGptHistoryOptions()
	if opts == (GptHistoryOptions{}) {
		return def
	}
	if opts.KeepTail == 0 {
		opts.KeepTail = def.KeepTail
	}
	if opts.MinCollapsePrefix == 0 {
		opts.MinCollapsePrefix = def.MinCollapsePrefix
	}
	if opts.Cols == 0 {
		opts.Cols = def.Cols
	}
	if opts.SectionTokens == 0 {
		opts.SectionTokens = def.SectionTokens
	}
	if opts.MaxHeightPx == 0 {
		opts.MaxHeightPx = def.MaxHeightPx
	}
	return opts
}

func findGptClosedBoundary(turns []GptHistoryTurn, cutoffExclusive, from int) int {
	open := make(map[string]struct{})
	lastClosed := from - 1
	limit := min(cutoffExclusive, len(turns))
	for i := from; i < limit; i++ {
		t := turns[i]
		if t.Opaque {
			break
		}
		for _, id := range t.OpenIDs {
			open[id] = struct{}{}
		}
		for _, id := range t.CloseIDs {
			delete(open, id)
		}
		if len(open) == 0 {
			lastClosed = i
		}
	}
	return lastClosed
}

func gptClosedPrefix(turns []GptHistoryTurn, from, toExclusive int) bool {
	open := make(map[string]struct{})
	for i := from; i < toExclusive; i++ {
		t := turns[i]
		if t.Opaque {
			return false
		}
		for _, id := range t.OpenIDs {
			open[id] = struct{}{}
		}
		for _, id := range t.CloseIDs {
			delete(open, id)
		}
	}
	return len(open) == 0
}

func joinGptTurns(turns []GptHistoryTurn, from, toExclusive, skip int) string {
	parts := make([]string, 0, max(0, toExclusive-from))
	for i := from; i < toExclusive; i++ {
		if i == skip {
			continue
		}
		if s := turns[i].Text; s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n")
}

// pxpipe uses gpt-tokenizer/o200k_base here. Caveman uses engine/tokens, whose
// default counter uses the same offline encoding and whose fallback undercounts
// rather than inventing a reduction when tokenizer init fails.
func gptCountTokens(text string) int {
	if text == "" {
		return 0
	}
	return tokens.Default().Count([]byte(text))
}

func safeJSONText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func OpenAIResponsesItemsToTurns(items []any) []GptHistoryTurn {
	turns := make([]GptHistoryTurn, len(items))
	for i, item := range items {
		turns[i] = responsesItemToGptTurn(item, i)
	}
	return turns
}

func responsesItemToGptTurn(item any, idx int) GptHistoryTurn {
	o, ok := item.(map[string]any)
	if !ok {
		return GptHistoryTurn{Opaque: true}
	}
	if typ, _ := o["type"].(string); typ != "" {
		switch typ {
		case "reasoning":
			return GptHistoryTurn{}
		case "function_call":
			callID := firstString(o["call_id"], o["id"])
			name := stringOr(o["name"], "tool")
			args := safeJSONText(o["arguments"])
			turn := GptHistoryTurn{Text: "[tool_use " + name + "]\n" + args}
			if callID != "" {
				turn.OpenIDs = []string{callID}
			}
			return turn
		case "function_call_output":
			callID, _ := o["call_id"].(string)
			out := safeJSONText(o["output"])
			turn := GptHistoryTurn{Text: "[tool_result]\n" + out}
			if callID != "" {
				turn.CloseIDs = []string{callID}
			}
			return turn
		}
	}
	role, _ := o["role"].(string)
	if role == "" {
		return GptHistoryTurn{Opaque: true}
	}
	body := responsesContentToHistoryText(o["content"])
	if strings.TrimSpace(body) == "" {
		return GptHistoryTurn{}
	}
	tag := role
	if role != "assistant" && role != "user" {
		tag = role
	}
	text := "<" + tag + " t=\"" + strconvItoa(idx) + "\">\n" + body + "\n</" + tag + ">"
	turn := GptHistoryTurn{Text: text}
	if role == "user" {
		userText := body
		turn.UserText = &userText
	}
	return turn
}

func OpenAIChatMessagesToTurns(messages []any) []GptHistoryTurn {
	turns := make([]GptHistoryTurn, len(messages))
	for i, msg := range messages {
		turns[i] = chatMessageToGptTurn(msg, i)
	}
	return turns
}

func chatMessageToGptTurn(msg any, idx int) GptHistoryTurn {
	o, ok := msg.(map[string]any)
	if !ok {
		return GptHistoryTurn{}
	}
	role, _ := o["role"].(string)
	body := chatContentToHistoryText(o["content"])
	if role == "tool" {
		id, _ := o["tool_call_id"].(string)
		turn := GptHistoryTurn{Text: "[tool_result]\n" + body}
		if id != "" {
			turn.CloseIDs = []string{id}
		}
		return turn
	}
	if role == "assistant" {
		parts := make([]string, 0, 2)
		if strings.TrimSpace(body) != "" {
			parts = append(parts, body)
		}
		var openIDs []string
		if calls, ok := o["tool_calls"].([]any); ok {
			for _, call := range calls {
				c, ok := call.(map[string]any)
				if !ok {
					continue
				}
				if id, _ := c["id"].(string); id != "" {
					openIDs = append(openIDs, id)
				}
				fn, _ := c["function"].(map[string]any)
				name := "tool"
				if fn != nil {
					name = stringOr(fn["name"], "tool")
				}
				args := ""
				if fn != nil {
					args = safeJSONText(fn["arguments"])
				}
				parts = append(parts, "[tool_use "+name+"]\n"+args)
			}
		}
		text := strings.Join(parts, "\n")
		if strings.TrimSpace(text) == "" {
			return GptHistoryTurn{OpenIDs: openIDs}
		}
		return GptHistoryTurn{
			Text:    "<assistant t=\"" + strconvItoa(idx) + "\">\n" + text + "\n</assistant>",
			OpenIDs: openIDs,
		}
	}
	if strings.TrimSpace(body) == "" {
		return GptHistoryTurn{}
	}
	tag := role
	if tag == "" {
		tag = "user"
	}
	turn := GptHistoryTurn{Text: "<" + tag + " t=\"" + strconvItoa(idx) + "\">\n" + body + "\n</" + tag + ">"}
	if role == "user" {
		userText := body
		turn.UserText = &userText
	}
	return turn
}

func firstString(values ...any) string {
	for _, v := range values {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func stringOr(v any, fallback string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return fallback
}

func responsesContentToHistoryText(content any) string {
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
		if !ok {
			continue
		}
		typ, _ := m["type"].(string)
		switch typ {
		case "input_text", "output_text", "text", "summary_text":
			if txt, ok := m["text"].(string); ok {
				parts = append(parts, txt)
			}
		case "input_image", "image", "output_image":
			parts = append(parts, "[image]")
		case "refusal":
			if txt, ok := m["refusal"].(string); ok {
				parts = append(parts, txt)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func chatContentToHistoryText(content any) string {
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
		if !ok {
			continue
		}
		typ, _ := m["type"].(string)
		switch typ {
		case "text":
			if txt, ok := m["text"].(string); ok {
				parts = append(parts, txt)
			}
		case "image_url", "input_image", "image":
			parts = append(parts, "[image]")
		}
	}
	return strings.Join(parts, "\n")
}
