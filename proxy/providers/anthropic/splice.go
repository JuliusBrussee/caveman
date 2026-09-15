package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const minCompressBlockBytes = 512

type jsonSpan struct {
	start int
	end   int
}

type spliceCandidate struct {
	jsonSpan
	original []byte
	kind     string
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
	out := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	return out, nil
}

func spliceStringReplacements(body []byte, candidates []spliceCandidate, reps [][]byte) ([]byte, error) {
	if len(reps) != len(candidates) {
		return nil, fmt.Errorf("anthropic compress: %d replacements for %d segments", len(reps), len(candidates))
	}
	var out []byte
	last := 0
	changed := false
	for i, c := range candidates {
		if c.start < last || c.end > len(body) || c.start >= c.end {
			return nil, fmt.Errorf("anthropic compress: invalid splice range")
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
