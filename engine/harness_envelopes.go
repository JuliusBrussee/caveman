package engine

import (
	"bytes"
	"encoding/json"
	"unicode"
)

var codexTextHeaderPrefixes = [][]byte{
	[]byte("Chunk ID: "),
	[]byte("Wall time: "),
	[]byte("Process exited with code "),
	[]byte("Exit code: "),
	[]byte("Process running with session ID "),
	[]byte("Command timed out"),
	[]byte("Original token count: "),
	[]byte("Total output lines: "),
}

// unwrapCodexTextExec separates Codex CLI's standard text `exec_command` /
// `shell_command` envelope:
//
//	Chunk ID: 7ae69c
//	Wall time: 0.1234 seconds
//	Process exited with code 0
//	Original token count: 18480
//	Output:
//	<command output>
//
// Left in place, the metadata header lines prevent Detect from recognizing JSON,
// JUnit XML, diffs, listings, or source code cleanly. It requires at least one
// recognized Codex metadata line followed by an `Output:` delimiter within the
// first 8 lines, plus non-whitespace content after `Output:`. rewrap restores
// the exact header bytes verbatim.
func unwrapCodexTextExec(input []byte) ([]byte, fileWrapper) {
	pos := 0
	seenMeta := false
	for lineNum := 0; lineNum < 8 && pos < len(input); lineNum++ {
		nl := bytes.IndexByte(input[pos:], '\n')
		var lineEnd, nextPos int
		if nl < 0 {
			lineEnd = len(input)
			nextPos = len(input)
		} else {
			lineEnd = pos + nl
			nextPos = lineEnd + 1
		}
		trimmed := bytes.TrimRight(input[pos:lineEnd], " \t\r")
		if bytes.Equal(trimmed, []byte("Output:")) {
			if !seenMeta || nextPos >= len(input) {
				return input, fileWrapper{}
			}
			inner := input[nextPos:]
			if len(bytes.TrimSpace(inner)) == 0 {
				return input, fileWrapper{}
			}
			return inner, fileWrapper{
				prefix:  input[:nextPos],
				present: true,
			}
		}
		matched := false
		for _, pfx := range codexTextHeaderPrefixes {
			if bytes.HasPrefix(trimmed, pfx) {
				matched = true
				seenMeta = true
				break
			}
		}
		if !matched && len(trimmed) > 0 {
			// A non-metadata line before Output: means this is not a Codex exec header.
			return input, fileWrapper{}
		}
		pos = nextPos
	}
	return input, fileWrapper{}
}

// unwrapClaudeSystemReminder separates a trailing Claude Code
// `<system-reminder>...</system-reminder>` block appended to `Read`, `Bash`,
// `Grep`, or `WebFetch` tool outputs. Left in place, the trailing tag breaks
// JSON validation, line-numbered listing detection, and `<file>…</file>`
// matching. rewrap appends the exact original `<system-reminder>` suffix back
// byte-for-byte so no host instruction is ever dropped.
func unwrapClaudeSystemReminder(input []byte) ([]byte, fileWrapper) {
	const (
		openTag  = "<system-reminder>"
		closeTag = "</system-reminder>"
	)
	rightTrimmed := bytes.TrimRightFunc(input, unicode.IsSpace)
	if !bytes.HasSuffix(rightTrimmed, []byte(closeTag)) {
		return input, fileWrapper{}
	}
	closeStart := len(rightTrimmed) - len(closeTag)
	openIdx := bytes.LastIndex(rightTrimmed[:closeStart], []byte(openTag))
	if openIdx <= 0 {
		// Either no opening tag, or the entire payload IS a system-reminder
		// (nothing before it to compress).
		return input, fileWrapper{}
	}
	// Reject malformed nested closing tags inside the trailing block.
	if bytes.Contains(rightTrimmed[openIdx+len(openTag):closeStart], []byte(closeTag)) {
		return input, fileWrapper{}
	}
	// Pull preceding blank lines / newlines into the suffix so the inner body
	// ends cleanly on its own last content byte (important for listing/JSON).
	cut := openIdx
	for cut > 0 && (input[cut-1] == '\n' || input[cut-1] == '\r') {
		cut--
	}
	inner := input[:cut]
	if len(bytes.TrimSpace(inner)) == 0 {
		return input, fileWrapper{}
	}
	return inner, fileWrapper{
		suffix:  input[cut:],
		present: true,
	}
}

// unwrapOpenCodeRead separates OpenCode's read-tool XML-style wrapper:
//
//	<path>/repo/src/app.ts</path>
//	<type>file</type>
//	<content>
//	1: export function ...
//	</content>
//
// Conservative: requires trimmed input to start with `<path>`, end with
// `</content>`, contain exactly one `<content>` and one `</content>`, and carry
// non-whitespace inner content. rewrap restores the exact envelope bytes.
func unwrapOpenCodeRead(input []byte) ([]byte, fileWrapper) {
	const (
		pathOpen    = "<path>"
		contentOpen = "<content>"
		contentEnd  = "</content>"
	)
	leftTrimmed := bytes.TrimLeftFunc(input, unicode.IsSpace)
	start := len(input) - len(leftTrimmed)
	end := start + len(bytes.TrimRightFunc(leftTrimmed, unicode.IsSpace))
	trimmed := input[start:end]
	if !bytes.HasPrefix(trimmed, []byte(pathOpen)) || !bytes.HasSuffix(trimmed, []byte(contentEnd)) {
		return input, fileWrapper{}
	}
	if bytes.Count(trimmed, []byte(contentOpen)) != 1 || bytes.Count(trimmed, []byte(contentEnd)) != 1 {
		return input, fileWrapper{}
	}
	openIdx := bytes.Index(trimmed, []byte(contentOpen))
	closeIdx := len(trimmed) - len(contentEnd)
	innerStart := openIdx + len(contentOpen)
	if innerStart+1 < closeIdx && trimmed[innerStart] == '\r' && trimmed[innerStart+1] == '\n' {
		innerStart += 2
	} else if innerStart < closeIdx && trimmed[innerStart] == '\n' {
		innerStart++
	}
	if closeIdx >= innerStart+2 && trimmed[closeIdx-2] == '\r' && trimmed[closeIdx-1] == '\n' {
		closeIdx -= 2
	} else if closeIdx > innerStart && trimmed[closeIdx-1] == '\n' {
		closeIdx--
	}
	if innerStart >= closeIdx {
		return input, fileWrapper{}
	}
	// Pull OpenCode's trailing read-tool footer lines (`(End of file - total N lines)`,
	// `(File has more lines. Use 'offset'...)`, `(Output capped...)`) into suffix
	// so line-numbered listing / JSON / code detection succeeds on real OpenCode reads.
	closeIdx = pullOpenCodeTrailingFooter(trimmed, innerStart, closeIdx)
	inner := trimmed[innerStart:closeIdx]
	if len(bytes.TrimSpace(inner)) == 0 {
		return input, fileWrapper{}
	}
	return inner, fileWrapper{
		prefix:  input[:start+innerStart],
		suffix:  input[start+closeIdx:],
		present: true,
	}
}

func pullOpenCodeTrailingFooter(buf []byte, innerStart, closeIdx int) int {
	cur := closeIdx
	for range 3 {
		// Find last non-empty line in buf[innerStart:cur].
		end := cur
		for end > innerStart && (buf[end-1] == '\n' || buf[end-1] == '\r' || buf[end-1] == ' ' || buf[end-1] == '\t') {
			end--
		}
		if end <= innerStart {
			break
		}
		lineStart := bytes.LastIndexByte(buf[innerStart:end], '\n')
		if lineStart < 0 {
			lineStart = innerStart
		} else {
			lineStart = innerStart + lineStart + 1
		}
		line := bytes.TrimSpace(buf[lineStart:end])
		if !isOpenCodeReadFooterLine(line) {
			break
		}
		// Include preceding newlines/blank lines before the footer into suffix.
		cut := lineStart
		for cut > innerStart && (buf[cut-1] == '\n' || buf[cut-1] == '\r') {
			cut--
		}
		if cut <= innerStart {
			break
		}
		cur = cut
	}
	return cur
}

func isOpenCodeReadFooterLine(line []byte) bool {
	return bytes.HasPrefix(line, []byte("(End of file - ")) ||
		bytes.HasPrefix(line, []byte("(File has more lines")) ||
		bytes.HasPrefix(line, []byte("(Output capped")) ||
		bytes.HasPrefix(line, []byte("(Showing lines "))
}

// DedupComponents describes the semantically-safe identity of a tool result for
// live-zone duplicate-read collapsing:
//   - Key: canonical bytes to hash (preserving Codex exit codes and OpenCode
//     `<path>` identity while stripping volatile `Chunk ID` / `Wall time` and
//     trailing `<system-reminder>` blocks).
//   - InnerLen: byte length of the real payload (excluding `<system-reminder>`
//     and volatile metadata headers) so tiny outputs with large reminders never
//     cross `minDedupBlockBytes`.
//   - TrailingReminder: exact original trailing `<system-reminder>` suffix to
//     re-attach verbatim to a duplicate stub so live harness instructions are
//     never dropped.
//   - SkipDedup: true when the block is itself a standalone `<system-reminder>`
//     (must never be collapsed).
type DedupComponents struct {
	Key              []byte
	InnerLen         int
	TrailingReminder []byte
	SkipDedup        bool
}

// ExtractDedupComponents computes the envelope-aware duplicate-read identity
// for a tool result block.
func ExtractDedupComponents(input []byte) DedupComponents {
	trimmed := bytes.TrimSpace(input)
	if bytes.HasPrefix(trimmed, []byte("<system-reminder>")) && bytes.HasSuffix(trimmed, []byte("</system-reminder>")) {
		return DedupComponents{SkipDedup: true}
	}

	body := input
	var trailingReminder []byte
	for range 3 {
		inner, w := unwrapClaudeSystemReminder(body)
		if !w.present {
			break
		}
		body = inner
		trailingReminder = append(append([]byte(nil), w.suffix...), trailingReminder...)
	}

	var semanticMeta bytes.Buffer
	if inner, w := unwrapCodexExec(body); w.present {
		// Preserve JSON exit_code in semantic identity while stripping volatile chunk_id/wall_time.
		if ec := extractJSONExitCode(w.prefix); ec != "" {
			semanticMeta.WriteString("codex_json_exit=")
			semanticMeta.WriteString(ec)
			semanticMeta.WriteByte('\n')
		}
		body = inner
	} else if inner, hw := unwrapHermesToolJSON(body); hw.present {
		// Preserve Hermes semantic fields (exit_code, returncode, error, path) while
		// ignoring volatile duration / duration_ms / wall_time fields.
		if meta := extractHermesSemanticMeta(body); meta != "" {
			semanticMeta.WriteString(meta)
		}
		body = inner
	} else if inner, w := unwrapCodexTextExec(body); w.present {
		if ec := extractCodexTextExitStatus(w.prefix); ec != "" {
			semanticMeta.WriteString("codex_text_status=")
			semanticMeta.WriteString(ec)
			semanticMeta.WriteByte('\n')
		}
		body = inner
	} else if inner, w := unwrapOpenCodeRead(body); w.present {
		if p := extractXMLTagValue(w.prefix, "<path>", "</path>"); p != "" {
			semanticMeta.WriteString("opencode_path=")
			semanticMeta.WriteString(p)
			semanticMeta.WriteByte('\n')
		}
		if t := extractXMLTagValue(w.prefix, "<type>", "</type>"); t != "" {
			semanticMeta.WriteString("opencode_type=")
			semanticMeta.WriteString(t)
			semanticMeta.WriteByte('\n')
		}
		body = inner
	}

	innerLen := len(body)
	var key []byte
	if semanticMeta.Len() > 0 {
		key = make([]byte, 0, semanticMeta.Len()+len(body))
		key = append(key, semanticMeta.Bytes()...)
		key = append(key, body...)
	} else {
		key = body
	}
	return DedupComponents{
		Key:              key,
		InnerLen:         innerLen,
		TrailingReminder: trailingReminder,
	}
}

// CanonicalDedupKey returns the inner semantic payload for duplicate-read
// comparison (strips volatile Codex Chunk ID / Wall time headers while
// preserving exit codes and OpenCode paths).
func CanonicalDedupKey(input []byte) []byte {
	return ExtractDedupComponents(input).Key
}

func extractCodexTextExitStatus(header []byte) string {
	for _, line := range bytes.Split(header, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if bytes.HasPrefix(trimmed, []byte("Process exited with code ")) ||
			bytes.HasPrefix(trimmed, []byte("Exit code: ")) ||
			bytes.HasPrefix(trimmed, []byte("Command timed out")) {
			return string(trimmed)
		}
	}
	return ""
}

func extractJSONExitCode(prefix []byte) string {
	idx := bytes.Index(prefix, []byte(`"exit_code":`))
	if idx < 0 {
		return ""
	}
	rest := bytes.TrimLeft(prefix[idx+len(`"exit_code":`):], " \t")
	j := 0
	if j < len(rest) && rest[j] == '-' {
		j++
	}
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	if j == 0 {
		return ""
	}
	return string(rest[:j])
}

func extractXMLTagValue(buf []byte, openTag, closeTag string) string {
	o := bytes.Index(buf, []byte(openTag))
	if o < 0 {
		return ""
	}
	rest := buf[o+len(openTag):]
	c := bytes.Index(rest, []byte(closeTag))
	if c < 0 {
		return ""
	}
	return string(bytes.TrimSpace(rest[:c]))
}

// hermesToolJSONWrapper unwraps Hermes Agent's JSON tool-result dicts
// (`terminal`: {"output":"...", "exit_code":0, "error":null},
// `execute_code`: {"output":"...", "returncode":0, "duration":0.42},
// `read_file`: {"content":"...", "path":"...", "total_lines":...}) so Detect
// classifies the inner test report, diff, log, or source file accurately and
// rewrap re-encodes the compressed string back into the exact JSON envelope.
type hermesToolJSONWrapper struct {
	prefix  []byte
	suffix  []byte
	present bool
}

func (w hermesToolJSONWrapper) rewrap(out []byte) []byte {
	if !w.present {
		return out
	}
	var encoded bytes.Buffer
	enc := json.NewEncoder(&encoded)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(string(out))
	quoted := bytes.TrimSuffix(encoded.Bytes(), []byte("\n"))
	var b bytes.Buffer
	b.Grow(len(w.prefix) + len(quoted) + len(w.suffix))
	b.Write(w.prefix)
	b.Write(quoted)
	b.Write(w.suffix)
	return b.Bytes()
}

func unwrapHermesToolJSON(input []byte) ([]byte, hermesToolJSONWrapper) {
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) < 20 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return input, hermesToolJSONWrapper{}
	}
	// Quick discriminator: only unwrap known Hermes tool-result envelopes
	// carrying one of Hermes's metadata keys alongside a large string payload.
	hasHermesMeta := bytes.Contains(trimmed, []byte(`"exit_code"`)) ||
		bytes.Contains(trimmed, []byte(`"returncode"`)) ||
		bytes.Contains(trimmed, []byte(`"total_lines"`)) ||
		bytes.Contains(trimmed, []byte(`"file_path"`)) ||
		(bytes.Contains(trimmed, []byte(`"output"`)) && bytes.Contains(trimmed, []byte(`"error"`))) ||
		(bytes.Contains(trimmed, []byte(`"content"`)) && bytes.Contains(trimmed, []byte(`"path"`)))
	if !hasHermesMeta || !json.Valid(trimmed) {
		return input, hermesToolJSONWrapper{}
	}
	// Locate the exact byte span of top-level `"output"` or `"content"` JSON string.
	startOffset := len(input) - len(bytes.TrimLeftFunc(input, unicode.IsSpace))
	for _, field := range []string{"output", "content"} {
		valStart, valEnd, ok := findTopLevelJSONStringSpan(trimmed, field)
		if !ok || valEnd-valStart < 256 {
			continue
		}
		var inner string
		if json.Unmarshal(trimmed[valStart:valEnd], &inner) != nil || len(bytes.TrimSpace([]byte(inner))) == 0 {
			continue
		}
		absStart := startOffset + valStart
		absEnd := startOffset + valEnd
		return []byte(inner), hermesToolJSONWrapper{
			prefix:  input[:absStart],
			suffix:  input[absEnd:],
			present: true,
		}
	}
	return input, hermesToolJSONWrapper{}
}

func extractHermesSemanticMeta(body []byte) string {
	var m map[string]any
	if json.Unmarshal(bytes.TrimSpace(body), &m) != nil {
		return ""
	}
	var out bytes.Buffer
	for _, k := range []string{"exit_code", "returncode", "error", "path", "file_path"} {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		if s, isStr := v.(string); isStr && s == "" {
			continue
		}
		out.WriteString("hermes_")
		out.WriteString(k)
		out.WriteByte('=')
		if enc, err := json.Marshal(v); err == nil {
			out.Write(enc)
		}
		out.WriteByte('\n')
	}
	return out.String()
}

// findTopLevelJSONStringSpan scans a valid JSON object for a top-level string
// field named targetKey and returns its [start, end) byte span (including quotes).
func findTopLevelJSONStringSpan(obj []byte, targetKey string) (int, int, bool) {
	skipWS := func(i int) int {
		for i < len(obj) && (obj[i] == ' ' || obj[i] == '\t' || obj[i] == '\n' || obj[i] == '\r') {
			i++
		}
		return i
	}
	scanStr := func(i int) (int, bool) {
		if i >= len(obj) || obj[i] != '"' {
			return 0, false
		}
		for j := i + 1; j < len(obj); j++ {
			if obj[j] == '\\' {
				j++
				continue
			}
			if obj[j] == '"' {
				return j + 1, true
			}
		}
		return 0, false
	}
	scanVal := func(i int) (int, bool) {
		i = skipWS(i)
		if i >= len(obj) {
			return 0, false
		}
		switch obj[i] {
		case '"':
			return scanStr(i)
		case '{', '[':
			depth := 0
			for j := i; j < len(obj); j++ {
				switch obj[j] {
				case '"':
					end, ok := scanStr(j)
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
				}
			}
			return 0, false
		default:
			j := i
			for j < len(obj) && obj[j] != ',' && obj[j] != '}' && obj[j] != ']' && obj[j] != ' ' && obj[j] != '\t' && obj[j] != '\n' && obj[j] != '\r' {
				j++
			}
			return j, j > i
		}
	}

	i := skipWS(0)
	if i >= len(obj) || obj[i] != '{' {
		return 0, 0, false
	}
	i = skipWS(i + 1)
	for i < len(obj) && obj[i] != '}' {
		kEnd, ok := scanStr(i)
		if !ok {
			return 0, 0, false
		}
		var key string
		if json.Unmarshal(obj[i:kEnd], &key) != nil {
			return 0, 0, false
		}
		i = skipWS(kEnd)
		if i >= len(obj) || obj[i] != ':' {
			return 0, 0, false
		}
		vStart := skipWS(i + 1)
		vEnd, ok := scanVal(vStart)
		if !ok {
			return 0, 0, false
		}
		if key == targetKey && vStart < vEnd && obj[vStart] == '"' {
			return vStart, vEnd, true
		}
		i = skipWS(vEnd)
		if i < len(obj) && obj[i] == ',' {
			i = skipWS(i + 1)
		}
	}
	return 0, 0, false
}
