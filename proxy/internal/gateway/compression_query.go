package gateway

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// maxCompressionQueryBytes bounds relevance-scoring work. Query context never
// leaves the request path or enters telemetry; the cap only prevents one enormous
// user message from turning BM25 tokenization into unbounded hot-path work.
const maxCompressionQueryBytes = 16 << 10

// extractCompressionQuery returns the latest human text from a provider request.
// Tool results are deliberately skipped: they are the payload being compressed,
// not the question that decides which rows matter. Malformed or unknown shapes
// return empty, preserving the historical query-agnostic compressor behavior.
func extractCompressionQuery(provider, endpoint string, body []byte) string {
	var root map[string]any
	if json.Unmarshal(body, &root) != nil {
		return ""
	}

	switch provider {
	case "openai", "azure_openai", "openai_compatible":
		if strings.Contains(strings.ToLower(endpoint), "responses") {
			return latestOpenAIResponsesQuery(root["input"])
		}
		return latestMessageQuery(root["messages"], map[string]bool{
			"text": true, "input_text": true,
		})
	case "anthropic":
		return latestMessageQuery(root["messages"], map[string]bool{"text": true})
	case "gemini":
		return latestGeminiQuery(root["contents"])
	default:
		return ""
	}
}

func latestOpenAIResponsesQuery(input any) string {
	if text, ok := input.(string); ok {
		return normalizeCompressionQuery(text)
	}
	return latestMessageQuery(input, map[string]bool{
		"text": true, "input_text": true,
	})
}

func latestMessageQuery(raw any, allowedBlockTypes map[string]bool) string {
	messages, _ := raw.([]any)
	for i := len(messages) - 1; i >= 0; i-- {
		message, _ := messages[i].(map[string]any)
		if role, _ := message["role"].(string); role != "user" {
			continue
		}
		if query := compressionText(message["content"], allowedBlockTypes); query != "" {
			return query
		}
	}
	return ""
}

func compressionText(raw any, allowedBlockTypes map[string]bool) string {
	if text, ok := raw.(string); ok {
		return normalizeCompressionQuery(text)
	}
	blocks, _ := raw.([]any)
	for _, rawBlock := range blocks {
		block, _ := rawBlock.(map[string]any)
		blockType, _ := block["type"].(string)
		if !allowedBlockTypes[blockType] {
			continue
		}
		text, _ := block["text"].(string)
		if query := normalizeCompressionQuery(text); query != "" {
			return query
		}
	}
	return ""
}

func latestGeminiQuery(raw any) string {
	contents, _ := raw.([]any)
	for i := len(contents) - 1; i >= 0; i-- {
		content, _ := contents[i].(map[string]any)
		if role, _ := content["role"].(string); role != "user" {
			continue
		}
		parts, _ := content["parts"].([]any)
		for _, rawPart := range parts {
			part, _ := rawPart.(map[string]any)
			text, _ := part["text"].(string)
			if query := normalizeCompressionQuery(text); query != "" {
				return query
			}
		}
	}
	return ""
}

func normalizeCompressionQuery(query string) string {
	query = strings.TrimSpace(query)
	if len(query) <= maxCompressionQueryBytes {
		return query
	}
	end := maxCompressionQueryBytes
	for end > 0 && !utf8.ValidString(query[:end]) {
		end--
	}
	return query[:end]
}
