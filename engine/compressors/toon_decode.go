package compressors

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type toonLine struct {
	indent int
	text   string
}

var (
	toonTabularLineRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_.-]*)?\[(\d+)\]\{([^}]*)\}:$`)
	toonArrayLineRe   = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_.-]*)?\[(\d+)\]:(?: (.*))?$`)
	toonKeyLineRe     = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_.-]*):(?: (.*))?$`)
)

// DecodeTOON parses the supported TOON subset back into the JSON data model.
func DecodeTOON(in []byte) (any, bool) {
	return decodeTOON(in)
}

// DecodeJSONForTOONGrader parses JSON with the same json.Number-preserving
// path used by the TOON compressor, then converts it to comparable JSON values.
func DecodeJSONForTOONGrader(in []byte) (any, bool) {
	return canonicalJSONValue(in)
}

func decodeTOON(in []byte) (any, bool) {
	lines, ok := scanTOONLines(string(in))
	if !ok || len(lines) == 0 {
		return nil, false
	}
	if lines[0].indent != 0 {
		return nil, false
	}
	v, next, ok := parseTOONRoot(lines, 0)
	if !ok || next != len(lines) {
		return nil, false
	}
	return v, true
}

func scanTOONLines(s string) ([]toonLine, bool) {
	s = strings.TrimRight(s, "\n")
	if strings.TrimSpace(s) == "" {
		return nil, false
	}
	raw := strings.Split(s, "\n")
	lines := make([]toonLine, 0, len(raw))
	for _, line := range raw {
		if strings.TrimSpace(line) == "" {
			return nil, false
		}
		spaces := len(line) - len(strings.TrimLeft(line, " "))
		if spaces%2 != 0 {
			return nil, false
		}
		lines = append(lines, toonLine{indent: spaces / 2, text: line[spaces:]})
	}
	return lines, true
}

func parseTOONRoot(lines []toonLine, i int) (any, int, bool) {
	text := lines[i].text
	if text == "{}" {
		return map[string]any{}, i + 1, true
	}
	if text == "[]" {
		return []any{}, i + 1, true
	}
	if strings.HasPrefix(text, `"`) {
		v, ok := parseTOONScalar(text)
		return v, i + 1, ok
	}
	if strings.HasPrefix(text, "[") {
		return parseTOONArray(lines, i, 0)
	}
	if !strings.Contains(text, ":") {
		v, ok := parseTOONScalar(text)
		return v, i + 1, ok
	}
	return parseTOONObject(lines, i, 0)
}

func parseTOONObject(lines []toonLine, i, indent int) (map[string]any, int, bool) {
	out := map[string]any{}
	for i < len(lines) {
		if lines[i].indent < indent {
			break
		}
		if lines[i].indent > indent {
			return nil, i, false
		}
		text := lines[i].text
		if m := toonTabularLineRe.FindStringSubmatch(text); m != nil && m[1] != "" {
			if _, exists := out[m[1]]; exists {
				return nil, i, false
			}
			v, next, ok := parseTOONArray(lines, i, indent)
			if !ok {
				return nil, i, false
			}
			out[m[1]] = v
			i = next
			continue
		}
		if m := toonArrayLineRe.FindStringSubmatch(text); m != nil && m[1] != "" {
			if _, exists := out[m[1]]; exists {
				return nil, i, false
			}
			v, next, ok := parseTOONArray(lines, i, indent)
			if !ok {
				return nil, i, false
			}
			out[m[1]] = v
			i = next
			continue
		}
		m := toonKeyLineRe.FindStringSubmatch(text)
		if m == nil || m[1] == "" {
			return nil, i, false
		}
		key := m[1]
		rest := m[2]
		if _, exists := out[key]; exists {
			return nil, i, false
		}
		if rest == "{}" {
			out[key] = map[string]any{}
			i++
			continue
		}
		if rest == "" {
			if i+1 >= len(lines) || lines[i+1].indent != indent+1 {
				return nil, i, false
			}
			child, next, ok := parseTOONObject(lines, i+1, indent+1)
			if !ok {
				return nil, i, false
			}
			out[key] = child
			i = next
			continue
		}
		val, ok := parseTOONScalar(rest)
		if !ok {
			return nil, i, false
		}
		out[key] = val
		i++
	}
	return out, i, true
}

func parseTOONArray(lines []toonLine, i, indent int) ([]any, int, bool) {
	text := lines[i].text
	if m := toonTabularLineRe.FindStringSubmatch(text); m != nil {
		n, err := strconv.Atoi(m[2])
		if err != nil {
			return nil, i, false
		}
		fields := splitFields(m[3])
		if len(fields) == 0 {
			return nil, i, false
		}
		// Exactly n physical rows must follow. Refuse impossible claims before
		// allocating capacity, so a tiny untrusted input cannot request gigabytes.
		if n > len(lines)-i-1 {
			return nil, i, false
		}
		out := make([]any, 0, n)
		i++
		for row := 0; row < n; row++ {
			if i >= len(lines) || lines[i].indent != indent+1 {
				return nil, i, false
			}
			cells, ok := splitTOONCells(lines[i].text, ',')
			if !ok || len(cells) != len(fields) {
				return nil, i, false
			}
			obj := make(map[string]any, len(fields))
			for c, field := range fields {
				val, ok := parseTOONScalar(cells[c])
				if !ok {
					return nil, i, false
				}
				obj[field] = val
			}
			out = append(out, obj)
			i++
		}
		return out, i, true
	}
	m := toonArrayLineRe.FindStringSubmatch(text)
	if m == nil {
		return nil, i, false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return nil, i, false
	}
	rest := m[3]
	if n == 0 {
		if rest != "[]" {
			return nil, i, false
		}
		return []any{}, i + 1, true
	}
	cells, ok := splitTOONCells(rest, ',')
	if !ok || len(cells) != n {
		return nil, i, false
	}
	out := make([]any, 0, n)
	for _, cell := range cells {
		val, ok := parseTOONScalar(cell)
		if !ok {
			return nil, i, false
		}
		out = append(out, val)
	}
	return out, i + 1, true
}

func splitFields(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	seen := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		if !safeTOONKey(p) {
			return nil
		}
		if _, duplicate := seen[p]; duplicate {
			return nil
		}
		seen[p] = struct{}{}
	}
	return parts
}

func splitTOONCells(s string, delimiter byte) ([]string, bool) {
	var out []string
	var b strings.Builder
	inQuote := false
	escaped := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			b.WriteByte(ch)
			escaped = false
			continue
		}
		if inQuote && ch == '\\' {
			b.WriteByte(ch)
			escaped = true
			continue
		}
		if ch == '"' {
			b.WriteByte(ch)
			inQuote = !inQuote
			continue
		}
		if !inQuote && ch == delimiter {
			out = append(out, b.String())
			b.Reset()
			continue
		}
		b.WriteByte(ch)
	}
	if inQuote || escaped {
		return nil, false
	}
	out = append(out, b.String())
	return out, true
}

func parseTOONScalar(s string) (any, bool) {
	if s == "" {
		return nil, false
	}
	if strings.HasPrefix(s, `"`) {
		var out string
		if err := json.Unmarshal([]byte(s), &out); err != nil {
			return nil, false
		}
		return out, true
	}
	switch s {
	case "true":
		return true, true
	case "false":
		return false, true
	case "null":
		return nil, true
	}
	if validJSONNumber(s) {
		return json.Number(s), true
	}
	if strings.ContainsAny(s, "\n\r\t") {
		return nil, false
	}
	if strings.TrimSpace(s) != s {
		return nil, false
	}
	return s, true
}

func canonicalJSONValue(input []byte) (any, bool) {
	tv, ok := parseJSONTOON(input)
	if !ok {
		return nil, false
	}
	return toonValueToAny(tv), true
}

func toonValueToAny(v toonValue) any {
	switch v.kind {
	case toonNull:
		return nil
	case toonBool:
		return v.b
	case toonNumber:
		return json.Number(v.s)
	case toonString:
		return v.s
	case toonArray:
		out := make([]any, len(v.arr))
		for i, e := range v.arr {
			out[i] = toonValueToAny(e)
		}
		return out
	case toonObject:
		out := make(map[string]any, len(v.obj))
		for _, f := range v.obj {
			out[f.name] = toonValueToAny(f.value)
		}
		return out
	default:
		panic(fmt.Sprintf("unknown toon kind %d", v.kind))
	}
}
