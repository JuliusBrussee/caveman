package cachebench

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
)

// ModelVisibleEquivalent strips only provider cache metadata and normalizes
// string-vs-single-text-block wire forms before comparing prompt semantics.
func ModelVisibleEquivalent(original, transformed []byte) bool {
	left, ok := decodeJSON(original)
	if !ok {
		return false
	}
	right, ok := decodeJSON(transformed)
	if !ok {
		return false
	}
	left, _ = sanitizeCacheMetadata(left, nil)
	right, _ = sanitizeCacheMetadata(right, nil)
	return reflect.DeepEqual(left, right)
}

func decodeJSON(raw []byte) (any, bool) {
	if !validUniqueJSONObject(raw) {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, false
	}
	return value, true
}

func sanitizeCacheMetadata(value any, path []string) (any, bool) {
	switch node := value.(type) {
	case map[string]any:
		clean := make(map[string]any, len(node))
		removedMetadata := false
		for key, child := range node {
			if legalCacheMetadata(path, key, child) {
				removedMetadata = true
				continue
			}
			sanitized, keep := sanitizeCacheMetadata(child, appendPath(path, key))
			if keep {
				clean[key] = sanitized
			}
		}
		if len(path) == 0 {
			if normalized, ok := normalizeTextBlocks(clean["system"]); ok {
				clean["system"] = normalized
			}
		}
		if pathMatches(path, "messages", "*") || pathMatches(path, "input", "*") {
			if normalized, ok := normalizeTextBlocks(clean["content"]); ok {
				clean["content"] = normalized
			}
		}
		return clean, !removedMetadata || len(clean) > 0
	case []any:
		clean := make([]any, 0, len(node))
		for _, child := range node {
			sanitized, keep := sanitizeCacheMetadata(child, appendPath(path, "*"))
			if keep {
				clean = append(clean, sanitized)
			}
		}
		return clean, true
	default:
		return value, true
	}
}

func appendPath(path []string, element string) []string {
	next := make([]string, len(path)+1)
	copy(next, path)
	next[len(path)] = element
	return next
}

func legalCacheMetadata(path []string, key string, value any) bool {
	switch key {
	case "prompt_cache_key":
		text, ok := value.(string)
		return len(path) == 0 && ok && len(text) == 32 && isLowerHex(text)
	case "prompt_cache_options":
		return len(path) == 0 && exactStringMap(value, "mode", "explicit")
	case "prompt_cache_breakpoint":
		allowed := pathMatches(path, "messages", "*", "content", "*") || pathMatches(path, "input", "*", "content", "*")
		return allowed && exactStringMap(value, "mode", "explicit")
	case "cache_control":
		allowed := len(path) == 0 || pathMatches(path, "tools", "*") || pathMatches(path, "system", "*") || pathMatches(path, "messages", "*", "content", "*")
		return allowed && exactStringMap(value, "type", "ephemeral")
	case "cachePoint":
		allowed := pathMatches(path, "toolConfig", "tools", "*") || pathMatches(path, "system", "*") || pathMatches(path, "messages", "*", "content", "*")
		return allowed && exactStringMap(value, "type", "default")
	default:
		return false
	}
}

func exactStringMap(value any, key, expected string) bool {
	object, ok := value.(map[string]any)
	if !ok || len(object) != 1 {
		return false
	}
	actual, ok := object[key].(string)
	return ok && actual == expected
}

func pathMatches(path []string, pattern ...string) bool {
	if len(path) != len(pattern) {
		return false
	}
	for index := range pattern {
		if path[index] != pattern[index] {
			return false
		}
	}
	return true
}

func isLowerHex(value string) bool {
	return strings.Trim(value, "0123456789abcdef") == ""
}

func normalizeTextBlocks(value any) (string, bool) {
	blocks, ok := value.([]any)
	if !ok || len(blocks) != 1 {
		return "", false
	}
	block, ok := blocks[0].(map[string]any)
	if !ok || len(block) != 2 {
		return "", false
	}
	typeName, _ := block["type"].(string)
	text, textOK := block["text"].(string)
	if !textOK || (typeName != "text" && typeName != "input_text") {
		return "", false
	}
	return text, true
}
