package compressors

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type toonKind int

const (
	toonNull toonKind = iota
	toonBool
	toonNumber
	toonString
	toonObject
	toonArray
)

type toonValue struct {
	kind   toonKind
	b      bool
	s      string
	obj    []toonField
	arr    []toonValue
	parsed bool
}

type toonField struct {
	name  string
	value toonValue
}

// EncodeOptions configures the focused TOON encoder.
type EncodeOptions struct {
	Delimiter byte
	FoldKeys  bool
}

var safeTOONKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

// encodeTOON returns TOON bytes with ok=true only when v is in the supported,
// proven-round-trip subset. Unsupported shapes return ok=false so callers can
// pass the original bytes through unchanged.
func encodeTOON(v any, opt EncodeOptions) ([]byte, bool) {
	if opt.Delimiter == 0 {
		opt.Delimiter = ','
	}
	if opt.Delimiter != ',' && opt.Delimiter != '\t' {
		return nil, false
	}
	tv, ok := asTOONValue(v)
	if !ok {
		return nil, false
	}
	var b strings.Builder
	if !writeTOONValue(&b, tv, "", 0, opt) {
		return nil, false
	}
	return []byte(strings.TrimRight(b.String(), "\n")), true
}

func asTOONValue(v any) (toonValue, bool) {
	switch t := v.(type) {
	case toonValue:
		return t, true
	case nil:
		return toonValue{kind: toonNull}, true
	case bool:
		return toonValue{kind: toonBool, b: t}, true
	case json.Number:
		return toonValue{kind: toonNumber, s: t.String()}, true
	case string:
		return toonValue{kind: toonString, s: t}, true
	case []any:
		arr := make([]toonValue, 0, len(t))
		for _, e := range t {
			tv, ok := asTOONValue(e)
			if !ok {
				return toonValue{}, false
			}
			arr = append(arr, tv)
		}
		return toonValue{kind: toonArray, arr: arr}, true
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		obj := make([]toonField, 0, len(keys))
		for _, k := range keys {
			tv, ok := asTOONValue(t[k])
			if !ok {
				return toonValue{}, false
			}
			obj = append(obj, toonField{name: k, value: tv})
		}
		return toonValue{kind: toonObject, obj: obj}, true
	default:
		return toonValue{}, false
	}
}

func parseJSONTOON(input []byte) (toonValue, bool) {
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	v, err := readJSONTOONValue(dec)
	if err != nil {
		return toonValue{}, false
	}
	if tok, err := dec.Token(); err != io.EOF || tok != nil {
		return toonValue{}, false
	}
	return v, true
}

func readJSONTOONValue(dec *json.Decoder) (toonValue, error) {
	tok, err := dec.Token()
	if err != nil {
		return toonValue{}, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			var fields []toonField
			seen := make(map[string]struct{})
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return toonValue{}, err
				}
				key, ok := keyTok.(string)
				if !ok {
					return toonValue{}, fmt.Errorf("object key is %T", keyTok)
				}
				if _, duplicate := seen[key]; duplicate {
					return toonValue{}, fmt.Errorf("duplicate object key %q", key)
				}
				seen[key] = struct{}{}
				val, err := readJSONTOONValue(dec)
				if err != nil {
					return toonValue{}, err
				}
				fields = append(fields, toonField{name: key, value: val})
			}
			end, err := dec.Token()
			if err != nil {
				return toonValue{}, err
			}
			if d, ok := end.(json.Delim); !ok || d != '}' {
				return toonValue{}, fmt.Errorf("object not closed")
			}
			return toonValue{kind: toonObject, obj: fields}, nil
		case '[':
			var arr []toonValue
			for dec.More() {
				val, err := readJSONTOONValue(dec)
				if err != nil {
					return toonValue{}, err
				}
				arr = append(arr, val)
			}
			end, err := dec.Token()
			if err != nil {
				return toonValue{}, err
			}
			if d, ok := end.(json.Delim); !ok || d != ']' {
				return toonValue{}, fmt.Errorf("array not closed")
			}
			return toonValue{kind: toonArray, arr: arr}, nil
		default:
			return toonValue{}, fmt.Errorf("unexpected delimiter %q", t)
		}
	case nil:
		return toonValue{kind: toonNull}, nil
	case bool:
		return toonValue{kind: toonBool, b: t}, nil
	case json.Number:
		return toonValue{kind: toonNumber, s: t.String()}, nil
	case string:
		return toonValue{kind: toonString, s: t}, nil
	default:
		return toonValue{}, fmt.Errorf("unsupported token %T", tok)
	}
}

func writeTOONValue(b *strings.Builder, v toonValue, name string, indent int, opt EncodeOptions) bool {
	switch v.kind {
	case toonObject:
		if name == "" {
			if len(v.obj) == 0 {
				b.WriteString("{}\n")
				return true
			}
			return writeTOONObject(b, v.obj, indent, opt)
		}
		if !safeTOONKey(name) {
			return false
		}
		if len(v.obj) == 0 {
			writeIndent(b, indent)
			fmt.Fprintf(b, "%s: {}\n", name)
			return true
		}
		writeIndent(b, indent)
		fmt.Fprintf(b, "%s:\n", name)
		return writeTOONObject(b, v.obj, indent+1, opt)
	case toonArray:
		return writeTOONArray(b, name, v.arr, indent, opt)
	default:
		encoded, ok := encodeTOONScalar(v, opt.Delimiter)
		if !ok {
			return false
		}
		writeIndent(b, indent)
		if name == "" {
			fmt.Fprintf(b, "%s\n", encoded)
		} else {
			if !safeTOONKey(name) {
				return false
			}
			fmt.Fprintf(b, "%s: %s\n", name, encoded)
		}
		return true
	}
}

func writeTOONObject(b *strings.Builder, fields []toonField, indent int, opt EncodeOptions) bool {
	ordered := append([]toonField(nil), fields...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].name < ordered[j].name
	})
	for _, f := range ordered {
		if !safeTOONKey(f.name) {
			return false
		}
		if !writeTOONValue(b, f.value, f.name, indent, opt) {
			return false
		}
	}
	return true
}

func writeTOONArray(b *strings.Builder, name string, arr []toonValue, indent int, opt EncodeOptions) bool {
	if name != "" && !safeTOONKey(name) {
		return false
	}
	if len(arr) == 0 {
		writeIndent(b, indent)
		if name == "" {
			fmt.Fprintf(b, "[]\n")
		} else {
			fmt.Fprintf(b, "%s[0]: []\n", name)
		}
		return true
	}
	if scalarArray(arr) {
		cells := make([]string, 0, len(arr))
		for _, e := range arr {
			cell, ok := encodeTOONScalar(e, opt.Delimiter)
			if !ok {
				return false
			}
			cells = append(cells, cell)
		}
		writeIndent(b, indent)
		if name == "" {
			fmt.Fprintf(b, "[%d]: %s\n", len(arr), strings.Join(cells, string(opt.Delimiter)))
		} else {
			fmt.Fprintf(b, "%s[%d]: %s\n", name, len(arr), strings.Join(cells, string(opt.Delimiter)))
		}
		return true
	}
	fields, rows, ok := tabularRows(arr, opt.Delimiter)
	if !ok {
		return false
	}
	for _, f := range fields {
		if !safeTOONKey(f) {
			return false
		}
	}
	writeIndent(b, indent)
	if name == "" {
		fmt.Fprintf(b, "[%d]{%s}:\n", len(rows), strings.Join(fields, ","))
	} else {
		fmt.Fprintf(b, "%s[%d]{%s}:\n", name, len(rows), strings.Join(fields, ","))
	}
	for _, row := range rows {
		writeIndent(b, indent+1)
		fmt.Fprintf(b, "%s\n", strings.Join(row, string(opt.Delimiter)))
	}
	return true
}

func scalarArray(arr []toonValue) bool {
	for _, e := range arr {
		if !isScalar(e) {
			return false
		}
	}
	return true
}

func tabularRows(arr []toonValue, delimiter byte) ([]string, [][]string, bool) {
	if len(arr) == 0 || arr[0].kind != toonObject || len(arr[0].obj) == 0 {
		return nil, nil, false
	}
	fields := make([]string, 0, len(arr[0].obj))
	for _, f := range arr[0].obj {
		if !isScalar(f.value) {
			return nil, nil, false
		}
		fields = append(fields, f.name)
	}
	rows := make([][]string, 0, len(arr))
	for _, item := range arr {
		if item.kind != toonObject || len(item.obj) != len(fields) {
			return nil, nil, false
		}
		row := make([]string, 0, len(fields))
		for i, f := range item.obj {
			if f.name != fields[i] || !isScalar(f.value) {
				return nil, nil, false
			}
			cell, ok := encodeTOONScalar(f.value, delimiter)
			if !ok {
				return nil, nil, false
			}
			row = append(row, cell)
		}
		rows = append(rows, row)
	}
	return fields, rows, true
}

func isScalar(v toonValue) bool {
	return v.kind == toonNull || v.kind == toonBool || v.kind == toonNumber || v.kind == toonString
}

func encodeTOONScalar(v toonValue, delimiter byte) (string, bool) {
	switch v.kind {
	case toonNull:
		return "null", true
	case toonBool:
		if v.b {
			return "true", true
		}
		return "false", true
	case toonNumber:
		if !validJSONNumber(v.s) {
			return "", false
		}
		return v.s, true
	case toonString:
		if needsTOONQuote(v.s, delimiter) {
			out, err := json.Marshal(v.s)
			if err != nil {
				return "", false
			}
			return string(out), true
		}
		return v.s, true
	default:
		return "", false
	}
}

func needsTOONQuote(s string, delimiter byte) bool {
	if s == "" || strings.TrimSpace(s) != s {
		return true
	}
	if strings.ContainsAny(s, "\n\r\t\":") || strings.ContainsRune(s, rune(delimiter)) {
		return true
	}
	switch s[0] {
	case '[', '{', '"', '-':
		return true
	}
	if s[0] >= '0' && s[0] <= '9' {
		return true
	}
	switch s {
	case "true", "false", "null":
		return true
	}
	return validJSONNumber(s)
}

func validJSONNumber(s string) bool {
	var v any
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return false
	}
	if _, ok := v.(json.Number); !ok {
		return false
	}
	return dec.Decode(&v) == io.EOF
}

func safeTOONKey(k string) bool {
	return safeTOONKeyRe.MatchString(k)
}

func writeIndent(b *strings.Builder, indent int) {
	for i := 0; i < indent; i++ {
		b.WriteString("  ")
	}
}

func normalizeDecodedJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalizeDecodedJSON(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalizeDecodedJSON(val)
		}
		return out
	case float64:
		return json.Number(strconv.FormatFloat(t, 'g', -1, 64))
	default:
		return t
	}
}
