package anthropic

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// FrozenPrefixComponents returns exact JSON value bytes that Anthropic treats as
// frozen context: top-level system/tools plus messages below cache floor.
// Length-prefixed framing prevents concatenation ambiguity. Live tail stays out,
// so its growth cannot rotate observed prefix digest.
func (a Adapter) FrozenPrefixComponents(body []byte, meta providers.RequestMetadata) ([][]byte, bool) {
	if strings.Contains(meta.Endpoint, "count_tokens") {
		return nil, false
	}
	root, ok := rootObjectSpan(body)
	if !ok {
		return nil, false
	}
	messagesValue, ok := findObjectField(body, root, "messages")
	if !ok || messagesValue.start >= len(body) || body[messagesValue.start] != '[' {
		return nil, false
	}
	messages, ok := arrayElements(body, messagesValue)
	if !ok {
		return nil, false
	}
	rawMessages := make([]json.RawMessage, len(messages))
	for i, span := range messages {
		rawMessages[i] = append(json.RawMessage(nil), body[span.start:span.end]...)
	}
	floor := ComputeFrozenCount(rawMessages)
	if floor < 0 || floor > len(messages) {
		return nil, false
	}
	out := [][]byte{[]byte("cave.anthropic.frozen-prefix.v1")}
	for _, name := range []string{"system", "tools"} {
		if span, exists := findObjectField(body, root, name); exists {
			out = append(out, frozenField(name, body[span.start:span.end]))
		} else {
			out = append(out, frozenField(name, nil))
		}
	}
	for i := 0; i < floor; i++ {
		out = append(out, frozenField("message", body[messages[i].start:messages[i].end]))
	}
	return out, true
}

func frozenField(name string, value []byte) []byte {
	var length [8]byte
	binary.BigEndian.PutUint32(length[:4], uint32(len(name)))
	binary.BigEndian.PutUint32(length[4:], uint32(len(value)))
	out := append([]byte(nil), length[:]...)
	out = append(out, name...)
	out = append(out, value...)
	return out
}

// ExtractCompressible returns only the Anthropic live zone. For conversations
// that carry cache_control markers (Claude Code, caching SDKs) the live zone is
// the provider-uncacheable tail: every user or trailing injected system message
// at/after the frozen floor — content past the last breakpoint is never cached
// upstream, so compressing it can never bust a prefix. Without markers there is
// no way to know what the provider cached, so only the just-arrived latest user
// message qualifies. It never collects the top-level system field, tools,
// frozen messages, assistant messages, or non-text blocks, and reassembly
// byte-splices changed string values into the original request bytes.
func (a Adapter) ExtractCompressible(body []byte, meta providers.RequestMetadata) ([][]byte, func([][]byte) ([]byte, error), bool) {
	messageSpans, live, ok := messageZones(body, meta)
	if !ok || len(live) == 0 {
		return nil, nil, false
	}
	recovered := recoveryToolUseIDs(body, messageSpans)
	var candidates []spliceCandidate
	for i, span := range messageSpans {
		if live[i] {
			candidates = append(candidates, collectAnthropicCandidates(body, span, recovered)...)
		}
	}
	if len(candidates) == 0 {
		return nil, nil, false
	}
	segments := make([][]byte, len(candidates))
	for i, c := range candidates {
		segments[i] = append([]byte(nil), c.original...)
	}
	reassemble := func(reps [][]byte) ([]byte, error) {
		return spliceStringReplacements(body, candidates, reps)
	}
	return segments, reassemble, true
}

// ExtractStabilizable returns every content block the proxy may rewrite, each
// tagged with the cache zone it sits in: the live zone (Live, eligible for new
// compression) plus the frozen user/system blocks at or below the cache_control
// floor (eligible ONLY for byte-identical substitution of a replacement the proxy
// already emitted for those exact bytes).
//
// Frozen blocks have to be exposed because compression is not a one-turn event: a
// message compressed while it WAS the live zone arrives again on the next turn as
// the client's original bytes, now below the floor. Forwarding those originals
// would flip the upstream prefix back to a form the provider never cached and miss
// the cache entry the previous turn paid to create — so the caller substitutes the
// stored replacement and the prefix stays byte-stable for the whole conversation.
// Assistant blocks are never collected: the proxy never compresses them, so a
// substitution could never exist for them.
func (a Adapter) ExtractStabilizable(body []byte, meta providers.RequestMetadata) ([]providers.RewritableBlock, func([][]byte) ([]byte, error), bool) {
	messageSpans, live, ok := messageZones(body, meta)
	if !ok {
		return nil, nil, false
	}
	recovered := recoveryToolUseIDs(body, messageSpans)
	var candidates []spliceCandidate
	var blocks []providers.RewritableBlock
	for i, span := range messageSpans {
		if !live[i] {
			switch messageRole(body, span) {
			case "user", "system":
			default:
				continue
			}
		}
		for _, c := range collectAnthropicCandidates(body, span, recovered) {
			candidates = append(candidates, c)
			blocks = append(blocks, providers.RewritableBlock{
				Content: append([]byte(nil), c.original...),
				Live:    live[i],
				Kind:    c.kind,
			})
		}
	}
	if len(blocks) == 0 {
		return nil, nil, false
	}
	reassemble := func(reps [][]byte) ([]byte, error) {
		return spliceStringReplacements(body, candidates, reps)
	}
	return blocks, reassemble, true
}

// messageZones parses a request into its message spans plus the set of message
// indexes that form the live zone. It is the single definition of "live" shared by
// ExtractCompressible (live blocks only) and ExtractStabilizable (live + frozen),
// so the two can never disagree about where the cache floor sits.
func messageZones(body []byte, meta providers.RequestMetadata) ([]jsonSpan, map[int]bool, bool) {
	if strings.Contains(meta.Endpoint, "count_tokens") {
		return nil, nil, false
	}
	root, ok := rootObjectSpan(body)
	if !ok {
		return nil, nil, false
	}
	messagesSpan, ok := findObjectField(body, root, "messages")
	if !ok || messagesSpan.start >= len(body) || body[messagesSpan.start] != '[' {
		return nil, nil, false
	}
	messageSpans, ok := arrayElements(body, messagesSpan)
	if !ok || len(messageSpans) == 0 {
		return nil, nil, false
	}

	rawMessages := make([]json.RawMessage, 0, len(messageSpans))
	for _, span := range messageSpans {
		rawMessages = append(rawMessages, append(json.RawMessage(nil), body[span.start:span.end]...))
	}
	floor := ComputeFrozenCount(rawMessages)
	live := make(map[int]bool)
	if messagesHaveContentCacheControl(rawMessages) {
		for i := floor; i < len(messageSpans); i++ {
			switch messageRole(body, messageSpans[i]) {
			case "user", "system":
				live[i] = true
			}
		}
	} else {
		for i := len(messageSpans) - 1; i >= floor; i-- {
			if messageRole(body, messageSpans[i]) == "user" {
				live[i] = true
				break
			}
		}
	}
	return messageSpans, live, true
}

func messageRole(body []byte, msg jsonSpan) string {
	role, ok := objectStringField(body, msg, "role")
	if !ok {
		return ""
	}
	return role
}

func objectStringField(body []byte, obj jsonSpan, field string) (string, bool) {
	value, ok := findObjectField(body, obj, field)
	if !ok || !isJSONString(body, value) {
		return "", false
	}
	return decodeJSONString(body[value.start:value.end])
}

// recoveryToolUseIDs collects the tool_use ids of every caveman recovery call in
// the conversation, so the tool_result answering one can be left alone. See
// providers.IsRecoveryToolName for why compressing recovered bytes strands the
// agent with no path back to its own data.
func recoveryToolUseIDs(body []byte, messageSpans []jsonSpan) map[string]bool {
	ids := make(map[string]bool)
	for _, msg := range messageSpans {
		if messageRole(body, msg) != "assistant" {
			continue
		}
		content, ok := findObjectField(body, msg, "content")
		if !ok || content.start >= content.end || body[content.start] != '[' {
			continue
		}
		blocks, ok := arrayElements(body, content)
		if !ok {
			continue
		}
		for _, block := range blocks {
			if block.start >= block.end || body[block.start] != '{' {
				continue
			}
			if typ, _ := objectStringField(body, block, "type"); typ != "tool_use" {
				continue
			}
			name, _ := objectStringField(body, block, "name")
			if !providers.IsRecoveryToolName(name) {
				continue
			}
			if id, ok := objectStringField(body, block, "id"); ok && id != "" {
				ids[id] = true
			}
		}
	}
	return ids
}

func collectAnthropicCandidates(body []byte, msg jsonSpan, recovered map[string]bool) []spliceCandidate {
	content, ok := findObjectField(body, msg, "content")
	if !ok {
		return nil
	}
	var out []spliceCandidate
	switch {
	case isJSONString(body, content):
		collectStringCandidate(body, content, &out, false, "history")
	case content.start < content.end && body[content.start] == '[':
		collectAnthropicBlocks(body, content, &out, false, recovered)
	}
	return out
}

func collectAnthropicBlocks(body []byte, blocksSpan jsonSpan, out *[]spliceCandidate, inToolResult bool, recovered map[string]bool) {
	blocks, ok := arrayElements(body, blocksSpan)
	if !ok {
		return
	}
	for _, block := range blocks {
		if block.start >= block.end || body[block.start] != '{' {
			continue
		}
		typ, ok := objectStringField(body, block, "type")
		if !ok {
			continue
		}
		switch typ {
		case "text":
			if textSpan, ok := findObjectField(body, block, "text"); ok && isJSONString(body, textSpan) {
				kind := "history"
				if inToolResult {
					kind = "tool_result"
				}
				collectStringCandidate(body, textSpan, out, inToolResult, kind)
			}
		case "tool_result":
			// Recovered bytes are never a compression candidate — not on the turn they
			// arrive, and not as frozen history afterwards, because collecting them at
			// all would let the prefix cache substitute the elision back in.
			if id, ok := objectStringField(body, block, "tool_use_id"); ok && recovered[id] {
				continue
			}
			content, ok := findObjectField(body, block, "content")
			if !ok {
				continue
			}
			switch {
			case isJSONString(body, content):
				collectStringCandidate(body, content, out, true, "tool_result")
			case content.start < content.end && body[content.start] == '[':
				collectAnthropicBlocks(body, content, out, true, recovered)
			}
		}
	}
}

func collectStringCandidate(
	body []byte,
	span jsonSpan,
	out *[]spliceCandidate,
	allowForcedTOON bool,
	kind string,
) {
	value, ok := decodeJSONString(body[span.start:span.end])
	if !ok {
		return
	}
	original := []byte(value)
	if len(original) < minCompressBlockBytes && !(allowForcedTOON && forcedTOONCandidate(original)) {
		return
	}
	*out = append(*out, spliceCandidate{jsonSpan: span, original: original, kind: kind})
}

func forcedTOONCandidate(original []byte) bool {
	if !strings.EqualFold(os.Getenv("CAVE_ENGINE_TOON"), "best-of") {
		return false
	}
	trimmed := bytes.TrimSpace(original)
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
}
