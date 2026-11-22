package providers

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

// TurnEffortOptimizerID is the opt-in optimizer ID for cache-safe turn-aware
// reasoning / thinking effort stepping (`CAVEMAN_TURN_EFFORT=1` /
// `optimizers.turn-effort-step`).
//
// Cache-safety contract: it NEVER mutates top-level `output_config.effort` or
// `reasoning.effort` / `reasoning_effort` mid-conversation (which invalidates
// upstream prompt caches). Instead, the gateway drives stateful append-only
// per-message effort marks on models that natively support them:
//   - Anthropic Claude 5+ (Opus 5.5, Sonnet 5.5, Haiku 5.5, Opus 5, Fable 5/5.1,
//     Mythos 5/5.1): `{"role":"system","content":[],"output_config":{"effort":...}}`
//     with `anthropic-beta: mid-conversation-output-config-2026-07-01`.
//   - OpenAI Responses GPT-6+: `{"type":"configuration_update","reasoning":{"effort":...}}`
//     placed before mechanical continuation user turns (never mid-tool-loop).
const TurnEffortOptimizerID = "turn-effort-step"

// IsMechanicalUserContinuation reports whether a Responses API request ends
// with a new, unanswered short mechanical continuation user prompt (e.g.
// "continue", "proceed", "go ahead", "yes", "ok", "keep going", "next") on an
// established conversation (>2 items). Substantive human prompts, questions,
// code/error pastes, or first turns always return false.
func IsMechanicalUserContinuation(body []byte) bool {
	root, ok := jsonsplice.Root(body)
	if !ok {
		return false
	}
	inSpan, ok := jsonsplice.Field(body, root, "input")
	if !ok || inSpan.Start >= inSpan.End || body[inSpan.Start] != '[' {
		return false
	}
	items, ok := jsonsplice.Elements(body, inSpan)
	if !ok || len(items) <= 2 {
		return false
	}
	last := items[len(items)-1]
	role, ok := jsonsplice.StringField(body, last, "role")
	if !ok || role != "user" {
		return false
	}
	text, ok := extractResponsesUserText(body, last)
	if !ok {
		return false
	}
	return isShortMechanicalAck(text)
}

func extractResponsesUserText(body []byte, item jsonsplice.Span) (string, bool) {
	contentSpan, ok := jsonsplice.Field(body, item, "content")
	if !ok || contentSpan.Start >= contentSpan.End {
		return "", false
	}
	if body[contentSpan.Start] == '"' {
		var s string
		if json.Unmarshal(body[contentSpan.Start:contentSpan.End], &s) != nil {
			return "", false
		}
		return s, true
	}
	if body[contentSpan.Start] != '[' {
		return "", false
	}
	parts, ok := jsonsplice.Elements(body, contentSpan)
	if !ok || len(parts) != 1 {
		return "", false
	}
	typ, _ := jsonsplice.StringField(body, parts[0], "type")
	if typ != "input_text" && typ != "text" {
		return "", false
	}
	txt, ok := jsonsplice.StringField(body, parts[0], "text")
	return txt, ok
}

func isShortMechanicalAck(raw string) bool {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimRight(s, ".!")
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 40 || strings.ContainsAny(s, "?\n\r`{}[]()<>") {
		return false
	}
	switch s {
	case "continue", "please continue", "go ahead", "proceed", "keep going",
		"next", "next step", "do it", "do that", "apply it", "apply that",
		"yes", "yep", "yeah", "ok", "okay", "sure", "sounds good", "looks good",
		"lgtm", "ship it", "go for it", "carry on":
		return true
	default:
		return false
	}
}

// StepDownEffort maps a high reasoning effort level down one notch for clean
// mechanical tool-result continuations. Already-low / minimal / none levels
// return "" (unchanged). Unset ("") maps to "low".
func StepDownEffort(current string) string {
	switch strings.ToLower(strings.TrimSpace(current)) {
	case "":
		return "low"
	case "max", "xhigh":
		return "medium"
	case "high":
		return "low"
	default:
		// medium, low, minimal, none, or unknown values stay untouched.
		return ""
	}
}

// ShouldStepDownTurnEffort inspects the latest turn of an Anthropic Messages or
// OpenAI Chat/Responses request and returns true ONLY when:
//  1. The conversation has prior turns (>2 items/messages — never first-turn planning).
//  2. The latest turn consists strictly of tool outputs (no new human text prompt).
//  3. None of the latest tool outputs carry `is_error: true` or failure/error markers.
func ShouldStepDownTurnEffort(body []byte, provider, endpoint string) bool {
	root, ok := jsonsplice.Root(body)
	if !ok {
		return false
	}
	ep := strings.ToLower(endpoint)
	switch {
	case provider == "anthropic" || strings.HasSuffix(ep, "/messages") ||
		(provider == "vertex" && strings.Contains(ep, "/publishers/anthropic/")):
		return isCleanAnthropicToolContinuation(body, root)
	case ep == "/responses" || strings.HasSuffix(ep, "/responses"):
		return isCleanResponsesToolContinuation(body, root)
	case strings.Contains(ep, "/chat/completions"):
		return isCleanChatToolContinuation(body, root)
	default:
		return false
	}
}

func isCleanAnthropicToolContinuation(body []byte, root jsonsplice.Span) bool {
	msgsSpan, ok := jsonsplice.Field(body, root, "messages")
	if !ok {
		return false
	}
	msgs, ok := jsonsplice.Elements(body, msgsSpan)
	if !ok || len(msgs) <= 2 {
		return false
	}
	// Claude Code 2.1.220+ may append a trailing harness `role: "system"` message
	// right after the newest user tool_result message. Step back over trailing
	// system messages to locate the newest user turn.
	idx := len(msgs) - 1
	for idx > 1 {
		r, _ := jsonsplice.StringField(body, msgs[idx], "role")
		if r == "system" {
			idx--
			continue
		}
		break
	}
	last := msgs[idx]
	role, ok := jsonsplice.StringField(body, last, "role")
	if !ok || role != "user" {
		return false
	}
	contentSpan, ok := jsonsplice.Field(body, last, "content")
	if !ok || contentSpan.Start >= contentSpan.End || body[contentSpan.Start] != '[' {
		return false
	}
	blocks, ok := jsonsplice.Elements(body, contentSpan)
	if !ok || len(blocks) == 0 {
		return false
	}
	sawToolResult := false
	for _, blk := range blocks {
		typ, ok := jsonsplice.StringField(body, blk, "type")
		if !ok {
			return false
		}
		if typ == "text" {
			// Claude Code frequently injects a trailing harness `<system-reminder>`
			// text block alongside tool_result blocks. Treat pure system-reminder
			// text blocks as harness metadata, never as a human prompt; any other
			// text block means a human spoke -> keep full effort.
			if isPureSystemReminderBlock(body, blk) {
				continue
			}
			return false
		}
		if typ != "tool_result" {
			// An image or other non-tool block alongside tool_result -> keep full effort.
			return false
		}
		sawToolResult = true
		if isErrSpan, found := jsonsplice.Field(body, blk, "is_error"); found {
			if bytes.Equal(bytes.TrimSpace(body[isErrSpan.Start:isErrSpan.End]), []byte("true")) {
				return false
			}
		}
		if inner, found := jsonsplice.Field(body, blk, "content"); found {
			if payloadHasErrorSignal(body[inner.Start:inner.End]) {
				return false
			}
		}
	}
	return sawToolResult
}

func isPureSystemReminderBlock(body []byte, blk jsonsplice.Span) bool {
	text, ok := jsonsplice.StringField(body, blk, "text")
	if !ok {
		return false
	}
	rest := strings.TrimSpace(text)
	if rest == "" {
		return false
	}
	// Verify the ENTIRE text consists strictly of one or more
	// `<system-reminder>...</system-reminder>` blocks with no human text sandwiched between.
	const (
		openTag  = "<system-reminder>"
		closeTag = "</system-reminder>"
	)
	for len(rest) > 0 {
		if !strings.HasPrefix(rest, openTag) {
			return false
		}
		end := strings.Index(rest[len(openTag):], closeTag)
		if end < 0 {
			return false
		}
		rest = strings.TrimSpace(rest[len(openTag)+end+len(closeTag):])
	}
	return true
}

func isCleanResponsesToolContinuation(body []byte, root jsonsplice.Span) bool {
	inputSpan, ok := jsonsplice.Field(body, root, "input")
	if !ok || inputSpan.Start >= inputSpan.End || body[inputSpan.Start] != '[' {
		return false
	}
	items, ok := jsonsplice.Elements(body, inputSpan)
	if !ok || len(items) <= 2 {
		return false
	}
	// Walk backwards over the contiguous trailing tool-output batch.
	sawToolOutput := false
	for i := len(items) - 1; i >= 0; i-- {
		typ, _ := jsonsplice.StringField(body, items[i], "type")
		if typ == "function_call_output" || typ == "custom_tool_call_output" ||
			typ == "local_shell_call_output" || typ == "shell_call_output" || typ == "apply_patch_call_output" {
			sawToolOutput = true
			if status, ok := jsonsplice.StringField(body, items[i], "status"); ok && strings.EqualFold(status, "failed") {
				return false
			}
			if outSpan, found := jsonsplice.Field(body, items[i], "output"); found {
				if payloadHasErrorSignal(body[outSpan.Start:outSpan.End]) {
					return false
				}
			}
			continue
		}
		// Immediately before the trailing tool-output batch must be a model tool call
		// or assistant/reasoning item (never a user message).
		if !sawToolOutput {
			return false
		}
		if typ == "message" {
			role, _ := jsonsplice.StringField(body, items[i], "role")
			return role == "assistant"
		}
		return typ == "function_call" || typ == "custom_tool_call" || typ == "local_shell_call" ||
			typ == "shell_call" || typ == "apply_patch_call" || typ == "reasoning"
	}
	return false
}

func isCleanChatToolContinuation(body []byte, root jsonsplice.Span) bool {
	msgsSpan, ok := jsonsplice.Field(body, root, "messages")
	if !ok {
		return false
	}
	msgs, ok := jsonsplice.Elements(body, msgsSpan)
	if !ok || len(msgs) <= 2 {
		return false
	}
	sawTool := false
	for i := len(msgs) - 1; i >= 0; i-- {
		role, _ := jsonsplice.StringField(body, msgs[i], "role")
		if role == "tool" {
			sawTool = true
			if contentSpan, found := jsonsplice.Field(body, msgs[i], "content"); found {
				if payloadHasErrorSignal(body[contentSpan.Start:contentSpan.End]) {
					return false
				}
			}
			continue
		}
		if !sawTool {
			return false
		}
		return role == "assistant"
	}
	return false
}

var turnEffortErrorSignals = [][]byte{
	[]byte("--- FAIL:"),
	[]byte("\nFAIL\t"),
	[]byte("Traceback (most recent call last)"),
	[]byte("panic: "),
	[]byte("SyntaxError:"),
	[]byte("TypeError:"),
	[]byte("ReferenceError:"),
	[]byte("AssertionError"),
	[]byte("fatal: "),
	[]byte("FAILURES!"),
	[]byte("test result: FAILED"),
	[]byte("Command timed out"),
	[]byte("apply_patch verification failed"),
	[]byte(`"status":"failed"`),
	[]byte(`"isError":true`),
	[]byte(`"isError": true`),
	[]byte("error[E"),
	[]byte("error TS"),
	[]byte("npm ERR!"),
}

var nonZeroExitPrefixes = [][]byte{
	[]byte("Process exited with code "),
	[]byte("Exit code: "),
	[]byte("exit status "),
	[]byte("exit code "),
	[]byte(`"exit_code":`),
	[]byte(`"exit_code": `),
	[]byte(`"exitCode":`),
	[]byte(`"exitCode": `),
	[]byte(`"returncode":`),
	[]byte(`"returncode": `),
}

func payloadHasErrorSignal(raw []byte) bool {
	// Decode JSON string payloads so escaped newlines (\n), tabs (\t), and
	// quotes (\") match error patterns accurately. For nested JSON arrays/objects
	// (e.g. Anthropic content block arrays), decode individual text strings or
	// fall back to scanning decoded slices.
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		if len(raw) <= 65536 {
			var decoded string
			if json.Unmarshal(raw, &decoded) == nil {
				return scanTextErrorSignals([]byte(decoded))
			}
		} else {
			// Bounded head + tail unescape for giant strings to stay O(1).
			if scanTextErrorSignals(unescapeJSONFragment(raw[1:16384])) {
				return true
			}
			return scanTextErrorSignals(unescapeJSONFragment(raw[len(raw)-16384 : len(raw)-1]))
		}
	}
	if len(raw) > 32768 {
		if scanTextErrorSignals(unescapeJSONFragment(raw[:16384])) {
			return true
		}
		return scanTextErrorSignals(unescapeJSONFragment(raw[len(raw)-16384:]))
	}
	return scanTextErrorSignals(unescapeJSONFragment(raw))
}

func unescapeJSONFragment(b []byte) []byte {
	if !bytes.ContainsRune(b, '\\') {
		return b
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] == '\\' && i+1 < len(b) {
			switch b[i+1] {
			case 'n':
				out = append(out, '\n')
				i++
				continue
			case 't':
				out = append(out, '\t')
				i++
				continue
			case 'r':
				out = append(out, '\r')
				i++
				continue
			case '"':
				out = append(out, '"')
				i++
				continue
			case '\\':
				out = append(out, '\\')
				i++
				continue
			}
		}
		out = append(out, b[i])
	}
	return out
}

func scanTextErrorSignals(b []byte) bool {
	for _, sig := range turnEffortErrorSignals {
		if bytes.Contains(b, sig) {
			return true
		}
	}
	for _, pfx := range nonZeroExitPrefixes {
		if hasNonZeroExitAfterPrefix(b, pfx) {
			return true
		}
	}
	return hasNonNullJSONErrorField(b)
}

// hasNonNullJSONErrorField catches Hermes tool_error JSON payloads like
// `{"error": "File not found: foo.go"}` while ignoring `"error": null` or `"error": ""`.
func hasNonNullJSONErrorField(b []byte) bool {
	search := b
	const needle = `"error":`
	for {
		idx := bytes.Index(search, []byte(needle))
		if idx < 0 {
			return false
		}
		rest := bytes.TrimLeft(search[idx+len(needle):], " \t")
		if len(rest) >= 2 && rest[0] == '"' && rest[1] != '"' {
			return true
		}
		search = search[idx+len(needle):]
	}
}

// hasNonZeroExitAfterPrefix reports whether any occurrence of pfx in b is
// followed by a non-zero integer exit code (1, 2, 101, 127, 128, 137, 139, -1, etc.).
func hasNonZeroExitAfterPrefix(b, pfx []byte) bool {
	search := b
	for {
		idx := bytes.Index(search, pfx)
		if idx < 0 {
			return false
		}
		rest := search[idx+len(pfx):]
		i := 0
		for i < len(rest) && (rest[i] == ' ' || rest[i] == '\t') {
			i++
		}
		if i < len(rest) {
			if rest[i] == '-' && i+1 < len(rest) && rest[i+1] >= '1' && rest[i+1] <= '9' {
				return true
			}
			j := i
			nonZero := false
			for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
				if rest[j] != '0' {
					nonZero = true
				}
				j++
			}
			if j > i && nonZero {
				return true
			}
		}
		search = search[idx+len(pfx):]
	}
}
