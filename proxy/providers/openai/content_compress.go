package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

const minCompressBlockBytes = 512

type jsonSpan struct {
	start int
	end   int
}

type spliceCandidate struct {
	jsonSpan
	original []byte
}

// zoneCandidate is one splice candidate plus the provider-cache zone it sits in.
// Live candidates belong to the just-arrived turn and may be newly compressed;
// frozen candidates are the same block types in EARLIER messages, exposed only so a
// replacement this proxy already emitted for those exact bytes can be substituted
// back byte-identically.
type zoneCandidate struct {
	spliceCandidate
	live bool
	kind string
}

// ExtractCompressible returns only OpenAI's live zone: the latest tool message
// plus the latest user message. It never collects system/developer/assistant
// content, and reassembly byte-splices changed string values into the original
// request bytes.
func (a Adapter) ExtractCompressible(body []byte, meta providers.RequestMetadata) ([][]byte, func([][]byte) ([]byte, error), bool) {
	return ExtractCompressible(body, meta)
}

// ExtractCompressible is exported so Azure OpenAI and OpenAI-compatible
// adapters can reuse the identical wire grammar without changing provider
// identity, routing, or header dispatch.
func ExtractCompressible(body []byte, meta providers.RequestMetadata) ([][]byte, func([][]byte) ([]byte, error), bool) {
	zones, ok := rewritableZones(body, meta, true)
	if !ok {
		return nil, nil, false
	}
	candidates := make([]spliceCandidate, 0, len(zones))
	for _, zone := range zones {
		if zone.live {
			candidates = append(candidates, zone.spliceCandidate)
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
// compression) plus the same block types in earlier messages (frozen — eligible
// ONLY for byte-identical substitution of a replacement the proxy already emitted
// for those exact bytes).
//
// Frozen blocks have to be exposed because compression is not a one-turn event: a
// message compressed while it WAS the live zone arrives again on the next turn as
// the client's original bytes. OpenAI caches long prompt prefixes automatically, so
// forwarding those originals silently misses the cache entry the previous turn paid
// to create — the caller substitutes the stored replacement instead and the prefix
// stays byte-stable for the whole conversation. Assistant/system/developer blocks
// are never collected: the proxy never compresses them, so a substitution could
// never exist for them.
//
// Exported for the same reason ExtractCompressible is: Azure OpenAI and
// OpenAI-compatible adapters reuse the identical wire grammar.
func (a Adapter) ExtractStabilizable(body []byte, meta providers.RequestMetadata) ([]providers.RewritableBlock, func([][]byte) ([]byte, error), bool) {
	return ExtractStabilizable(body, meta)
}

func ExtractStabilizable(body []byte, meta providers.RequestMetadata) ([]providers.RewritableBlock, func([][]byte) ([]byte, error), bool) {
	zones, ok := rewritableZones(body, meta, false)
	if !ok || len(zones) == 0 {
		return nil, nil, false
	}
	candidates := make([]spliceCandidate, len(zones))
	blocks := make([]providers.RewritableBlock, len(zones))
	for i, zone := range zones {
		candidates[i] = zone.spliceCandidate
		blocks[i] = providers.RewritableBlock{
			Content: append([]byte(nil), zone.original...),
			Live:    zone.live,
			Kind:    zone.kind,
		}
	}
	reassemble := func(reps [][]byte) ([]byte, error) {
		return spliceStringReplacements(body, candidates, reps)
	}
	return blocks, reassemble, true
}

// rewritableZones is the single definition of OpenAI's rewrite zones, shared by
// ExtractCompressible (live only) and ExtractStabilizable (live + frozen), so the
// two can never disagree about which message is the live one. Candidates come back
// in document order — messages, then items, then content parts are all walked in
// order — which is the ascending, non-overlapping ordering the splicer requires.
//
// liveOnly is a pure cost switch for ExtractCompressible, which throws every frozen
// candidate away: it skips COLLECTING them, so a long conversation never pays to
// decode history the caller has already decided to discard. The live-zone RULE is
// unchanged — which message is live is still decided by the same full scan of roles
// — so the live candidates, their order, and the ok result are byte-for-byte what
// the full walk produces. Collecting a frozen candidate can never change ok
// (per-message parse problems are skipped, not failed), which is what makes the
// skip safe.
func rewritableZones(body []byte, meta providers.RequestMetadata, liveOnly bool) ([]zoneCandidate, bool) {
	root, ok := rootObjectSpan(body)
	if !ok {
		return nil, false
	}
	if strings.Contains(meta.Endpoint, "embeddings") {
		return nil, false
	}
	if strings.Contains(meta.Endpoint, "responses") {
		return responsesZones(body, root, liveOnly)
	}
	return chatZones(body, root, liveOnly)
}

func chatZones(body []byte, root jsonSpan, liveOnly bool) ([]zoneCandidate, bool) {
	messagesSpan, ok := findObjectField(body, root, "messages")
	if !ok || messagesSpan.start >= len(body) || body[messagesSpan.start] != '[' {
		return nil, false
	}
	messageSpans, ok := arrayElements(body, messagesSpan)
	if !ok || len(messageSpans) == 0 {
		return nil, false
	}
	live := latestOpenAITargets(body, messageSpans)
	recovered := chatRecoveryCallIDs(body, messageSpans)
	var out []zoneCandidate
	for i, msg := range messageSpans {
		if liveOnly && !live[i] {
			continue
		}
		role := messageRole(body, msg)
		if role != "user" && role != "tool" {
			continue
		}
		// Recovered bytes are never a compression candidate — see
		// providers.IsRecoveryToolName.
		if role == "tool" {
			if id, ok := objectStringField(body, msg, "tool_call_id"); ok && recovered[id] {
				continue
			}
		}
		for _, c := range collectOpenAICandidates(body, msg, role == "tool") {
			kind := "history"
			if role == "tool" {
				kind = "tool_result"
			}
			out = append(out, zoneCandidate{spliceCandidate: c, live: live[i], kind: kind})
		}
	}
	return out, true
}

func responsesZones(body []byte, root jsonSpan, liveOnly bool) ([]zoneCandidate, bool) {
	input, ok := findObjectField(body, root, "input")
	if !ok {
		return nil, false
	}
	if isJSONString(body, input) {
		// A bare string input carries no conversation history, so it is all live.
		var candidates []spliceCandidate
		collectStringCandidate(body, input, &candidates, false)
		return liveZones(candidates, "history"), true
	}
	if input.start >= input.end || body[input.start] != '[' {
		return nil, false
	}
	items, ok := arrayElements(body, input)
	if !ok {
		return nil, false
	}
	latestUser, latestTool := -1, -1
	recovered := make(map[string]bool)
	for i, item := range items {
		if item.start >= item.end || body[item.start] != '{' {
			continue
		}
		typ, _ := objectStringField(body, item, "type")
		role, _ := objectStringField(body, item, "role")
		switch {
		case typ == "function_call_output":
			latestTool = i
		case typ == "function_call":
			name, _ := objectStringField(body, item, "name")
			if providers.IsRecoveryToolName(name) {
				if id, ok := objectStringField(body, item, "call_id"); ok && id != "" {
					recovered[id] = true
				}
			}
		case role == "user":
			latestUser = i
		}
	}
	var out []zoneCandidate
	for i, item := range items {
		live := i == latestTool || i == latestUser
		if liveOnly && !live {
			continue
		}
		if item.start >= item.end || body[item.start] != '{' {
			continue
		}
		typ, _ := objectStringField(body, item, "type")
		role, _ := objectStringField(body, item, "role")
		var candidates []spliceCandidate
		switch {
		case typ == "function_call_output":
			// Recovered bytes are never a compression candidate — see
			// providers.IsRecoveryToolName.
			if id, ok := objectStringField(body, item, "call_id"); ok && recovered[id] {
				continue
			}
			output, found := findObjectField(body, item, "output")
			if !found || !isJSONString(body, output) {
				continue
			}
			collectStringCandidate(body, output, &candidates, true)
		case role == "user":
			candidates = collectResponsesContent(body, item)
		default:
			continue
		}
		for _, c := range candidates {
			kind := "history"
			if typ == "function_call_output" {
				kind = "tool_result"
			}
			out = append(out, zoneCandidate{spliceCandidate: c, live: live, kind: kind})
		}
	}
	return out, true
}

// chatRecoveryCallIDs collects the tool_call ids of every caveman recovery call
// in the chat grammar, so the tool message answering one can be left alone.
func chatRecoveryCallIDs(body []byte, messages []jsonSpan) map[string]bool {
	ids := make(map[string]bool)
	for _, msg := range messages {
		if messageRole(body, msg) != "assistant" {
			continue
		}
		calls, ok := findObjectField(body, msg, "tool_calls")
		if !ok || calls.start >= calls.end || body[calls.start] != '[' {
			continue
		}
		items, ok := arrayElements(body, calls)
		if !ok {
			continue
		}
		for _, call := range items {
			if call.start >= call.end || body[call.start] != '{' {
				continue
			}
			fn, ok := findObjectField(body, call, "function")
			if !ok || fn.start >= fn.end || body[fn.start] != '{' {
				continue
			}
			name, _ := objectStringField(body, fn, "name")
			if !providers.IsRecoveryToolName(name) {
				continue
			}
			if id, ok := objectStringField(body, call, "id"); ok && id != "" {
				ids[id] = true
			}
		}
	}
	return ids
}

func liveZones(candidates []spliceCandidate, kind string) []zoneCandidate {
	out := make([]zoneCandidate, len(candidates))
	for i, c := range candidates {
		out[i] = zoneCandidate{spliceCandidate: c, live: true, kind: kind}
	}
	return out
}

func collectResponsesContent(body []byte, item jsonSpan) []spliceCandidate {
	content, ok := findObjectField(body, item, "content")
	if !ok {
		return nil
	}
	var candidates []spliceCandidate
	if isJSONString(body, content) {
		collectStringCandidate(body, content, &candidates, false)
		return candidates
	}
	if content.start >= content.end || body[content.start] != '[' {
		return nil
	}
	parts, ok := arrayElements(body, content)
	if !ok {
		return nil
	}
	for _, part := range parts {
		typ, _ := objectStringField(body, part, "type")
		if typ != "input_text" && typ != "text" {
			continue
		}
		if text, found := findObjectField(body, part, "text"); found && isJSONString(body, text) {
			collectStringCandidate(body, text, &candidates, false)
		}
	}
	return candidates
}

// latestOpenAITargets is the live-zone rule for the chat grammar: the latest tool
// message and the latest user message. Everything before them is history the
// provider may already have cached automatically.
func latestOpenAITargets(body []byte, messages []jsonSpan) map[int]bool {
	latestUser := -1
	latestTool := -1
	for i, msg := range messages {
		switch messageRole(body, msg) {
		case "user":
			latestUser = i
		case "tool":
			latestTool = i
		}
	}
	targets := map[int]bool{}
	if latestTool >= 0 {
		targets[latestTool] = true
	}
	if latestUser >= 0 {
		targets[latestUser] = true
	}
	return targets
}

func collectOpenAICandidates(body []byte, msg jsonSpan, allowForcedTOON bool) []spliceCandidate {
	content, ok := findObjectField(body, msg, "content")
	if !ok {
		return nil
	}
	var out []spliceCandidate
	switch {
	case isJSONString(body, content):
		collectStringCandidate(body, content, &out, allowForcedTOON)
	case content.start < content.end && body[content.start] == '[':
		parts, ok := arrayElements(body, content)
		if !ok {
			return nil
		}
		for _, part := range parts {
			if part.start >= part.end || body[part.start] != '{' {
				continue
			}
			typ, ok := objectStringField(body, part, "type")
			if !ok {
				continue
			}
			switch typ {
			case "text", "input_text", "output_text":
				if textSpan, ok := findObjectField(body, part, "text"); ok && isJSONString(body, textSpan) {
					collectStringCandidate(body, textSpan, &out, allowForcedTOON)
				}
			}
		}
	}
	return out
}

func collectStringCandidate(body []byte, span jsonSpan, out *[]spliceCandidate, allowForcedTOON bool) {
	value, ok := decodeJSONString(body[span.start:span.end])
	if !ok {
		return
	}
	original := []byte(value)
	if len(original) < minCompressBlockBytes && !(allowForcedTOON && forcedTOONCandidate(original)) {
		return
	}
	*out = append(*out, spliceCandidate{jsonSpan: span, original: original})
}

func forcedTOONCandidate(original []byte) bool {
	if !strings.EqualFold(os.Getenv("CAVE_ENGINE_TOON"), "best-of") {
		return false
	}
	trimmed := bytes.TrimSpace(original)
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
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

func rootObjectSpan(body []byte) (jsonSpan, bool) {
	start := skipJSONSpace(body, 0)
	if start >= len(body) || body[start] != '{' {
		return jsonSpan{}, false
	}
	end, ok := scanJSONValue(body, start)
	if !ok {
		return jsonSpan{}, false
	}
	if skipJSONSpace(body, end) != len(body) {
		return jsonSpan{}, false
	}
	return jsonSpan{start: start, end: end}, true
}

func findObjectField(body []byte, obj jsonSpan, field string) (jsonSpan, bool) {
	if obj.start < 0 || obj.end > len(body) || obj.start >= obj.end || body[obj.start] != '{' {
		return jsonSpan{}, false
	}
	i := skipJSONSpace(body, obj.start+1)
	for i < obj.end {
		if body[i] == '}' {
			return jsonSpan{}, false
		}
		if body[i] != '"' {
			return jsonSpan{}, false
		}
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
		valueStart := skipJSONSpace(body, i+1)
		valueEnd, ok := scanJSONValue(body, valueStart)
		if !ok {
			return jsonSpan{}, false
		}
		if key == field {
			return jsonSpan{start: valueStart, end: valueEnd}, true
		}
		i = skipJSONSpace(body, valueEnd)
		if i < obj.end && body[i] == ',' {
			i = skipJSONSpace(body, i+1)
			continue
		}
	}
	return jsonSpan{}, false
}

func arrayElements(body []byte, arr jsonSpan) ([]jsonSpan, bool) {
	if arr.start < 0 || arr.end > len(body) || arr.start >= arr.end || body[arr.start] != '[' {
		return nil, false
	}
	var out []jsonSpan
	i := skipJSONSpace(body, arr.start+1)
	for i < arr.end {
		if body[i] == ']' {
			return out, true
		}
		valueEnd, ok := scanJSONValue(body, i)
		if !ok {
			return nil, false
		}
		out = append(out, jsonSpan{start: i, end: valueEnd})
		i = skipJSONSpace(body, valueEnd)
		if i < arr.end && body[i] == ',' {
			i = skipJSONSpace(body, i+1)
			continue
		}
	}
	return nil, false
}

func scanJSONValue(body []byte, start int) (int, bool) {
	i := skipJSONSpace(body, start)
	if i >= len(body) {
		return 0, false
	}
	switch body[i] {
	case '"':
		return scanJSONString(body, i)
	case '{', '[':
		depth := 0
		for j := i; j < len(body); j++ {
			switch body[j] {
			case '"':
				end, ok := scanJSONString(body, j)
				if !ok {
					return 0, false
				}
				j = end - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return j + 1, true
				}
				if depth < 0 {
					return 0, false
				}
			}
		}
		return 0, false
	default:
		j := i
		for j < len(body) {
			switch body[j] {
			case ',', '}', ']', ' ', '\n', '\r', '\t':
				if j == i {
					return 0, false
				}
				return j, true
			default:
				j++
			}
		}
		return j, j > i
	}
}

func scanJSONString(body []byte, start int) (int, bool) {
	if start >= len(body) || body[start] != '"' {
		return 0, false
	}
	for i := start + 1; i < len(body); i++ {
		switch body[i] {
		case '\\':
			i++
		case '"':
			return i + 1, true
		}
	}
	return 0, false
}

func skipJSONSpace(body []byte, i int) int {
	for i < len(body) {
		switch body[i] {
		case ' ', '\n', '\r', '\t':
			i++
		default:
			return i
		}
	}
	return i
}

func decodeJSONString(raw []byte) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

func isJSONString(body []byte, span jsonSpan) bool {
	return span.start < span.end && span.start >= 0 && span.end <= len(body) && body[span.start] == '"'
}

func quoteJSONStringNoHTML(s string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func spliceStringReplacements(body []byte, candidates []spliceCandidate, reps [][]byte) ([]byte, error) {
	if len(reps) != len(candidates) {
		return nil, fmt.Errorf("openai compress: %d replacements for %d segments", len(reps), len(candidates))
	}
	var out []byte
	last := 0
	changed := false
	for i, c := range candidates {
		if c.start < last || c.end > len(body) || c.start >= c.end {
			return nil, fmt.Errorf("openai compress: invalid splice range")
		}
		rep := reps[i]
		if rep == nil || bytes.Equal(rep, c.original) {
			continue
		}
		quoted, err := quoteJSONStringNoHTML(string(rep))
		if err != nil {
			return nil, err
		}
		if !changed {
			out = make([]byte, 0, len(body)-len(c.original)+len(rep))
		}
		out = append(out, body[last:c.start]...)
		out = append(out, quoted...)
		last = c.end
		changed = true
	}
	if !changed {
		return body, nil
	}
	out = append(out, body[last:]...)
	return out, nil
}
