package anthropic

import "encoding/json"

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
