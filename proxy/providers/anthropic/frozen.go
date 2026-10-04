package anthropic

import (
	"bytes"
	"encoding/json"
)

// ComputeFrozenCount returns the first mutable message index. It mirrors
// headroom's cache-control floor: the highest message index whose content block
// carries cache_control, plus one. System/tools markers never raise this floor.
// The trailing message is always reserved as the live zone (floor is clamped to
// len(messages)-1): agents like Claude Code place a cache_control marker ON the
// newest message, but that just-arrived turn cannot be in any provider cache
// yet, so freezing it would make the live zone permanently empty. Claude Code
// 2.1.220 appends a small marked system message immediately after a new user
// tool_result; that pair is one first-send cache write, so both messages stay live.
func ComputeFrozenCount(messages []json.RawMessage) int {
	floor := 0
	for i, raw := range messages {
		if messageHasContentCacheControl([]byte(raw)) {
			floor = i + 1
		}
	}
	if max := len(messages) - 1; floor > max {
		if max < 0 {
			return 0
		}
		if max > 0 && messageRoleRaw(messages[max]) == "system" && messageHasToolResult(messages[max-1]) {
			return max - 1
		}
		return max
	}
	return floor
}

func messageRoleRaw(raw []byte) string {
	msg, ok := rootObjectSpan(raw)
	if !ok {
		return ""
	}
	role, ok := objectStringField(raw, msg, "role")
	if !ok {
		return ""
	}
	return role
}

func messageHasToolResult(raw []byte) bool {
	msg, ok := rootObjectSpan(raw)
	if !ok {
		return false
	}
	content, ok := findObjectField(raw, msg, "content")
	if !ok || content.start >= len(raw) || raw[content.start] != '[' {
		return false
	}
	blocks, ok := arrayElements(raw, content)
	if !ok {
		return false
	}
	for _, block := range blocks {
		if block.start >= block.end || raw[block.start] != '{' {
			continue
		}
		if typ, ok := objectStringField(raw, block, "type"); ok && typ == "tool_result" {
			return true
		}
	}
	return false
}

// messagesHaveContentCacheControl reports whether any message carries a
// content-block cache_control marker. Marked conversations get uncacheable-tail
// live-zone semantics; unmarked ones stay latest-user-only.
func messagesHaveContentCacheControl(messages []json.RawMessage) bool {
	for _, raw := range messages {
		if messageHasContentCacheControl([]byte(raw)) {
			return true
		}
	}
	return false
}

func messageHasContentCacheControl(raw []byte) bool {
	msgSpan, ok := rootObjectSpan(raw)
	if !ok {
		return false
	}
	content, ok := findObjectField(raw, msgSpan, "content")
	if !ok || content.start >= len(raw) || raw[content.start] != '[' {
		return false
	}
	blocks, ok := arrayElements(raw, content)
	if !ok {
		return false
	}
	for _, block := range blocks {
		if block.start < block.end && raw[block.start] == '{' {
			if _, ok := findObjectField(raw, block, "cache_control"); ok {
				return true
			}
		}
	}
	return false
}

// stripCacheControl returns value with every cache_control member removed from the
// block objects it may carry: the elements of a system or tools array, or the
// content blocks of a message object. Only the marker goes; every other byte keeps
// its order, so a block hashes the same whether or not the client marked it this
// turn. Values without the key are returned untouched.
func stripCacheControl(value []byte) []byte {
	if !bytes.Contains(value, []byte(`"cache_control"`)) {
		return value
	}
	start := skipJSONSpace(value, 0)
	if start >= len(value) {
		return value
	}
	var blocks []jsonSpan
	var ok bool
	switch value[start] {
	case '[':
		end, scanned := scanJSONValue(value, start)
		if !scanned {
			return value
		}
		blocks, ok = arrayElements(value, jsonSpan{start: start, end: end})
	case '{':
		msg, isObject := rootObjectSpan(value)
		if !isObject {
			return value
		}
		content, found := findObjectField(value, msg, "content")
		if !found || content.start >= len(value) || value[content.start] != '[' {
			return value
		}
		blocks, ok = arrayElements(value, content)
	}
	if !ok {
		return value
	}
	var out []byte
	last, changed := 0, false
	for _, block := range blocks {
		if block.start >= block.end || value[block.start] != '{' {
			continue
		}
		member, found := objectMemberSpan(value, block, "cache_control")
		if !found {
			continue
		}
		out = append(out, value[last:member.start]...)
		last, changed = member.end, true
	}
	if !changed {
		return value
	}
	return append(out, value[last:]...)
}

// objectMemberSpan locates the `"field": value` member of obj together with exactly
// one delimiting comma (the one after it, or for the last member the one before),
// so deleting the span leaves a valid object.
func objectMemberSpan(body []byte, obj jsonSpan, field string) (jsonSpan, bool) {
	if obj.start < 0 || obj.end > len(body) || obj.start >= obj.end || body[obj.start] != '{' {
		return jsonSpan{}, false
	}
	prevComma := -1
	i := skipJSONSpace(body, obj.start+1)
	for i < obj.end && body[i] == '"' {
		keyStart := i
		keyEnd, ok := scanJSONString(body, keyStart)
		if !ok {
			return jsonSpan{}, false
		}
		key, ok := decodeJSONString(body[keyStart:keyEnd])
		if !ok {
			return jsonSpan{}, false
		}
		i = skipJSONSpace(body, keyEnd)
		if i >= obj.end || body[i] != ':' {
			return jsonSpan{}, false
		}
		valueEnd, ok := scanJSONValue(body, i+1)
		if !ok {
			return jsonSpan{}, false
		}
		next := skipJSONSpace(body, valueEnd)
		if key == field {
			if next < obj.end && body[next] == ',' {
				return jsonSpan{start: keyStart, end: next + 1}, true
			}
			if prevComma >= 0 {
				keyStart = prevComma
			}
			return jsonSpan{start: keyStart, end: valueEnd}, true
		}
		if next >= obj.end || body[next] != ',' {
			return jsonSpan{}, false
		}
		prevComma = next
		i = skipJSONSpace(body, next+1)
	}
	return jsonSpan{}, false
}
