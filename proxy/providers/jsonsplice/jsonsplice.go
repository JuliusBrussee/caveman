package jsonsplice

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type Span struct{ Start, End int }

type Candidate struct {
	Span
	Original []byte
}

type FieldInsertion struct {
	Name  string
	Value []byte
}

func Root(body []byte) (Span, bool) {
	if !json.Valid(body) {
		return Span{}, false
	}
	start := space(body, 0)
	end, ok := value(body, start)
	return Span{start, end}, ok && start < len(body) && body[start] == '{' && space(body, end) == len(body)
}

func Field(body []byte, object Span, name string) (Span, bool) {
	if object.Start < 0 || object.End > len(body) || object.Start >= object.End ||
		body[object.Start] != '{' || body[object.End-1] != '}' {
		return Span{}, false
	}
	for i := space(body, object.Start+1); i < object.End && body[i] != '}'; {
		keyEnd, ok := stringEnd(body, i)
		if !ok {
			return Span{}, false
		}
		var key string
		if json.Unmarshal(body[i:keyEnd], &key) != nil {
			return Span{}, false
		}
		i = space(body, keyEnd)
		if i >= object.End || body[i] != ':' {
			return Span{}, false
		}
		start := space(body, i+1)
		end, ok := value(body, start)
		if !ok {
			return Span{}, false
		}
		if key == name {
			return Span{start, end}, true
		}
		i = space(body, end)
		if i < object.End && body[i] == ',' {
			i = space(body, i+1)
		}
	}
	return Span{}, false
}

func Elements(body []byte, array Span) ([]Span, bool) {
	if array.Start < 0 || array.End > len(body) || array.Start >= array.End ||
		body[array.Start] != '[' || body[array.End-1] != ']' {
		return nil, false
	}
	var out []Span
	for i := space(body, array.Start+1); i < array.End && body[i] != ']'; {
		end, ok := value(body, i)
		if !ok {
			return nil, false
		}
		out = append(out, Span{i, end})
		i = space(body, end)
		if i < array.End && body[i] == ',' {
			i = space(body, i+1)
		}
	}
	return out, true
}

func String(body []byte, span Span) (string, bool) {
	if span.Start < 0 || span.End > len(body) || span.Start >= span.End || body[span.Start] != '"' {
		return "", false
	}
	var out string
	if json.Unmarshal(body[span.Start:span.End], &out) != nil {
		return "", false
	}
	return out, true
}

func StringField(body []byte, object Span, name string) (string, bool) {
	span, ok := Field(body, object, name)
	if !ok {
		return "", false
	}
	return String(body, span)
}

func Replace(body []byte, candidates []Candidate, replacements [][]byte) ([]byte, error) {
	if len(candidates) != len(replacements) {
		return nil, fmt.Errorf("json splice: %d replacements for %d candidates", len(replacements), len(candidates))
	}
	var out []byte
	last := 0
	for i, candidate := range candidates {
		if candidate.Start < last || candidate.End > len(body) || candidate.Start >= candidate.End {
			return nil, fmt.Errorf("json splice: invalid range")
		}
		replacement := replacements[i]
		if replacement == nil || bytes.Equal(replacement, candidate.Original) {
			continue
		}
		quoted, err := quote(string(replacement))
		if err != nil {
			return nil, err
		}
		if out == nil {
			out = make([]byte, 0, len(body)-len(candidate.Original)+len(replacement))
		}
		out = append(out, body[last:candidate.Start]...)
		out = append(out, quoted...)
		last = candidate.End
	}
	if out == nil {
		return body, nil
	}
	return append(out, body[last:]...), nil
}

// ReplaceRaw replaces one JSON value while preserving every byte outside span.
// replacement must itself be valid JSON. Unlike Replace, it does not quote the
// replacement and is suitable for provider-envelope edits.
func ReplaceRaw(body []byte, span Span, replacement []byte) ([]byte, error) {
	if span.Start < 0 || span.End > len(body) || span.Start >= span.End {
		return nil, fmt.Errorf("json splice: invalid range")
	}
	if !json.Valid(replacement) {
		return nil, fmt.Errorf("json splice: replacement is not valid JSON")
	}
	out := make([]byte, 0, len(body)-(span.End-span.Start)+len(replacement))
	out = append(out, body[:span.Start]...)
	out = append(out, replacement...)
	out = append(out, body[span.End:]...)
	return out, nil
}

// AppendObjectFields inserts fields immediately before an object's closing
// brace. Existing bytes, whitespace, key order, and escapes remain untouched.
// Callers must reject existing decoded keys before calling this function.
func AppendObjectFields(body []byte, object Span, fields ...FieldInsertion) ([]byte, error) {
	if object.Start < 0 || object.End > len(body) || object.Start >= object.End ||
		body[object.Start] != '{' || body[object.End-1] != '}' {
		return nil, fmt.Errorf("json splice: invalid object range")
	}
	if len(fields) == 0 {
		return body, nil
	}

	insertAt := object.End - 1
	for insertAt > object.Start+1 && bytes.ContainsRune([]byte(" \n\r\t"), rune(body[insertAt-1])) {
		insertAt--
	}
	hasFields := insertAt > object.Start+1

	var addition bytes.Buffer
	if hasFields {
		addition.WriteByte(',')
	}
	for i, field := range fields {
		if field.Name == "" || !json.Valid(field.Value) {
			return nil, fmt.Errorf("json splice: invalid field insertion")
		}
		if i > 0 {
			addition.WriteByte(',')
		}
		name, err := json.Marshal(field.Name)
		if err != nil {
			return nil, err
		}
		addition.Write(name)
		addition.WriteByte(':')
		addition.Write(field.Value)
	}

	out := make([]byte, 0, len(body)+addition.Len())
	out = append(out, body[:insertAt]...)
	out = append(out, addition.Bytes()...)
	out = append(out, body[insertAt:]...)
	return out, nil
}

func value(body []byte, start int) (int, bool) {
	i := space(body, start)
	if i >= len(body) {
		return 0, false
	}
	if body[i] == '"' {
		return stringEnd(body, i)
	}
	if body[i] == '{' || body[i] == '[' {
		depth := 0
		for j := i; j < len(body); j++ {
			switch body[j] {
			case '"':
				end, ok := stringEnd(body, j)
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
	}
	j := i
	for j < len(body) && !bytes.ContainsRune([]byte(",}] \n\r\t"), rune(body[j])) {
		j++
	}
	return j, j > i
}

func stringEnd(body []byte, start int) (int, bool) {
	if start >= len(body) || body[start] != '"' {
		return 0, false
	}
	for i := start + 1; i < len(body); i++ {
		if body[i] == '\\' {
			i++
		} else if body[i] == '"' {
			return i + 1, true
		}
	}
	return 0, false
}

func space(body []byte, i int) int {
	for i < len(body) && bytes.ContainsRune([]byte(" \n\r\t"), rune(body[i])) {
		i++
	}
	return i
}

func quote(value string) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
