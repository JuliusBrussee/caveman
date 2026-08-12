package cacheengine

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

func applyOpenAI(body []byte, endpoint, routingKey string, explicit bool) ([]byte, []string) {
	if routingKey == "" {
		return body, nil
	}
	marked := body
	breakpoint := false
	if explicit {
		marked, breakpoint = markOpenAIBreakpoints(body, endpoint)
	}
	root, ok := jsonsplice.Root(marked)
	if !ok {
		return body, nil
	}
	insertions := []jsonsplice.FieldInsertion{{Name: "prompt_cache_key", Value: quotedJSON(routingKey)}}
	ids := []string{OpenAIKeyOptimizerID}
	if breakpoint {
		insertions = append(insertions, jsonsplice.FieldInsertion{Name: "prompt_cache_options", Value: []byte(`{"mode":"explicit"}`)})
		ids = append(ids, OpenAIExplicitOptimizerID)
	}
	out, err := jsonsplice.AppendObjectFields(marked, root, insertions...)
	if err != nil {
		return body, nil
	}
	return out, ids
}

// markOpenAIBreakpoints keeps one stable anchor plus the latest three cacheable
// message blocks marked. GPT-5.6 explicit mode does not fall back to unmarked
// prefixes: retaining prior rolling markers lets request N+1 read the prefix
// written by request N while adding at most one new rolling write.
func markOpenAIBreakpoints(body []byte, endpoint string) ([]byte, bool) {
	root, ok := jsonsplice.Root(body)
	if !ok {
		return body, false
	}
	sequenceName := "messages"
	stringBlockType := "text"
	supported := map[string]bool{"text": true, "image_url": true, "input_audio": true, "file": true, "refusal": true}
	if strings.Contains(strings.ToLower(endpoint), "responses") {
		sequenceName = "input"
		stringBlockType = "input_text"
		supported = map[string]bool{"input_text": true, "input_image": true, "input_file": true}
	}
	sequence, ok := jsonsplice.Field(body, root, sequenceName)
	if !ok {
		return body, false
	}
	items, ok := jsonsplice.Elements(body, sequence)
	if !ok || len(items) == 0 {
		return body, false
	}
	markable := make([]int, 0, len(items))
	stable := -1
	leadingStable := true
	for index, item := range items {
		role, _ := jsonsplice.StringField(body, item, "role")
		if role != "system" && role != "developer" {
			leadingStable = false
		}
		if !openAICacheableRole(role, sequenceName) || !openAIItemMarkable(body, item, supported) {
			continue
		}
		markable = append(markable, index)
		if leadingStable {
			stable = index
		}
	}
	if len(markable) == 0 {
		return body, false
	}
	selected := make(map[int]bool, 4)
	if stable >= 0 {
		selected[stable] = true
	}
	for index := len(markable) - 1; index >= 0 && len(selected) < 4; index-- {
		selected[markable[index]] = true
	}
	indices := make([]int, 0, len(selected))
	for index := range selected {
		indices = append(indices, index)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(indices)))
	marked := body
	count := 0
	for _, index := range indices {
		var changed bool
		// Descending targets keep every earlier original span valid: each prior
		// insertion/replacement occurred strictly after the next target.
		marked, changed = markOpenAIItem(marked, items[index], stringBlockType, supported)
		if !changed {
			return body, false
		}
		count++
	}
	return marked, count > 0
}

func openAICacheableRole(role, sequenceName string) bool {
	if sequenceName == "input" {
		return role == "system" || role == "developer" || role == "user" || role == "assistant"
	}
	return role == "system" || role == "developer" || role == "user" || role == "assistant" || role == "tool"
}

func openAIItemMarkable(body []byte, item jsonsplice.Span, supported map[string]bool) bool {
	content, ok := jsonsplice.Field(body, item, "content")
	if !ok {
		return false
	}
	if text, stringContent := jsonsplice.String(body, content); stringContent {
		return text != ""
	}
	blocks, ok := jsonsplice.Elements(body, content)
	if !ok {
		return false
	}
	for _, block := range blocks {
		blockType, _ := jsonsplice.StringField(body, block, "type")
		if supported[blockType] {
			return true
		}
	}
	return false
}

func markOpenAIItem(body []byte, item jsonsplice.Span, stringBlockType string, supported map[string]bool) ([]byte, bool) {
	content, ok := jsonsplice.Field(body, item, "content")
	if !ok {
		return body, false
	}
	if text, stringContent := jsonsplice.String(body, content); stringContent {
		if text == "" {
			return body, false
		}
		replacement := []byte(`[{"type":"` + stringBlockType + `","text":` + string(quotedJSON(text)) + `,"prompt_cache_breakpoint":{"mode":"explicit"}}]`)
		out, err := jsonsplice.ReplaceRaw(body, content, replacement)
		return out, err == nil
	}
	blocks, ok := jsonsplice.Elements(body, content)
	if !ok {
		return body, false
	}
	for index := len(blocks) - 1; index >= 0; index-- {
		blockType, _ := jsonsplice.StringField(body, blocks[index], "type")
		if !supported[blockType] {
			continue
		}
		if _, exists := jsonsplice.Field(body, blocks[index], "prompt_cache_breakpoint"); exists {
			return body, false
		}
		out, err := jsonsplice.AppendObjectFields(body, blocks[index], jsonsplice.FieldInsertion{
			Name: "prompt_cache_breakpoint", Value: []byte(`{"mode":"explicit"}`),
		})
		return out, err == nil
	}
	return body, false
}

func quotedJSON(value string) []byte {
	out, _ := json.Marshal(value)
	return out
}
