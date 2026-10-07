package translate

// A small JSON reader and writer for the request bodies. A coding agent
// resends its whole conversation every turn, so a body is mostly history the
// translation copies as it came: the reader finds values as sub-slices of the
// body (no copy, no decode), and the writer appends those raw bytes, JSON
// strings included, around the few values it builds. Only small values (types,
// ids, names) are ever decoded. encoding/json runs about ten times slower per
// byte, and a 100k-token body is about 400 KB.
//
// The reader checks structure (brackets, string ends, separators), not every
// escape: a body it accepts that a strict parser would refuse goes upstream as
// the caller wrote it, and the upstream refuses it.

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"unicode/utf8"
)

var errBadJSON = errors.New("translate: body is not valid JSON")

// kv is one object member: the key as written between its quotes, the value
// as written (sub-slices of the body).
type kv struct {
	key []byte
	val []byte
}

func isSpace(c byte) bool { return c == ' ' || c == '\n' || c == '\r' || c == '\t' }

func skipSpace(b []byte, i int) int {
	for i < len(b) && isSpace(b[i]) {
		i++
	}
	return i
}

// stringEnd is the index after the string token starting at b[i] ('"').
func stringEnd(b []byte, i int) (int, bool) {
	i++
	for {
		at := bytes.IndexByte(b[i:], '"')
		if at < 0 {
			return 0, false
		}
		i += at
		// An escaped quote has an odd run of backslashes before it.
		run := 0
		for j := i - 1; j >= 0 && b[j] == '\\'; j-- {
			run++
		}
		i++
		if run%2 == 0 {
			return i, true
		}
	}
}

// valueEnd is the index after the value starting at b[i] (no leading space).
func valueEnd(b []byte, i int) (int, bool) {
	if i >= len(b) {
		return 0, false
	}
	switch c := b[i]; {
	case c == '"':
		return stringEnd(b, i)
	case c == '{' || c == '[':
		depth := 0
		for j := i; j < len(b); j++ {
			switch b[j] {
			case '"':
				end, ok := stringEnd(b, j)
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
		for j < len(b) && b[j] != ',' && b[j] != '}' && b[j] != ']' && b[j] != ':' && !isSpace(b[j]) {
			j++
		}
		if j == i {
			return 0, false
		}
		switch word := b[i:j]; {
		case string(word) == "true" || string(word) == "false" || string(word) == "null":
		case c == '-' || c >= '0' && c <= '9':
		default:
			return 0, false
		}
		return j, true
	}
}

// trimValue is b without surrounding space, false when b is not exactly one
// value.
func trimValue(b []byte) ([]byte, bool) {
	start := skipSpace(b, 0)
	end, ok := valueEnd(b, start)
	if !ok || skipSpace(b, end) != len(b) {
		return nil, false
	}
	return b[start:end], true
}

// objectKVs reads the members of the object b (one trimmed value) into dst.
func objectKVs(b []byte, dst []kv) ([]kv, bool) {
	if len(b) < 2 || b[0] != '{' {
		return dst, false
	}
	dst, end, ok := objectAt(b, 0, dst)
	return dst, ok && end == len(b)
}

// arrayItems reads the elements of the array b (one trimmed value) into dst.
func arrayItems(b []byte, dst [][]byte) ([][]byte, bool) {
	if len(b) < 2 || b[0] != '[' || b[len(b)-1] != ']' {
		return dst, false
	}
	i := skipSpace(b, 1)
	if b[i] == ']' {
		return dst, i == len(b)-1
	}
	for {
		end, ok := valueEnd(b, i)
		if !ok {
			return dst, false
		}
		dst = append(dst, b[i:end])
		i = skipSpace(b, end)
		if i >= len(b) {
			return dst, false
		}
		if b[i] == ']' {
			return dst, i == len(b)-1
		}
		if b[i] != ',' {
			return dst, false
		}
		i = skipSpace(b, i+1)
	}
}

// obj is one object's members with lookup by key (the last of duplicates,
// as encoding/json takes it).
type obj []kv

func parseObj(b []byte) (obj, bool) {
	kvs, ok := objectKVs(b, nil)
	return obj(kvs), ok
}

func (o obj) get(key string) []byte {
	for i := len(o) - 1; i >= 0; i-- {
		if string(o[i].key) == key {
			return o[i].val
		}
	}
	return nil
}

func (o obj) has(key string) bool { return o.get(key) != nil }

// str is the value of key decoded as a string ("" when absent or not one).
func (o obj) str(key string) string { return jstr(o.get(key)) }

// jstr decodes a JSON string token; "" for anything else.
func jstr(raw []byte) string {
	if len(raw) < 2 || raw[0] != '"' {
		return ""
	}
	inner := raw[1 : len(raw)-1]
	if bytes.IndexByte(inner, '\\') < 0 && utf8.Valid(inner) {
		return string(inner)
	}
	var out string
	_ = json.Unmarshal(raw, &out)
	return out
}

// isStr reports a JSON string token.
func isStr(raw []byte) bool { return len(raw) >= 2 && raw[0] == '"' }

// isNull reports an absent or null value.
func isNull(raw []byte) bool { return len(raw) == 0 || string(raw) == "null" }

// items reads an array value (nil for anything else).
func items(raw []byte) [][]byte {
	out, ok := arrayItems(raw, nil)
	if !ok {
		return nil
	}
	return out
}

// --- writing ------------------------------------------------------------------

// appendString appends s as a JSON string. Invalid UTF-8 becomes U+FFFD, as
// encoding/json writes it.
func appendString[T ~string | ~[]byte](dst []byte, s T) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c >= 0x20 && c != '"' && c != '\\' && c < utf8.RuneSelf {
			i++
			continue
		}
		if c >= utf8.RuneSelf {
			var buf [utf8.UTFMax]byte
			n := copy(buf[:], s[i:min(i+utf8.UTFMax, len(s))])
			if r, size := utf8.DecodeRune(buf[:n]); r != utf8.RuneError || size != 1 {
				i += size
				continue
			}
			dst = append(dst, s[start:i]...)
			dst = append(dst, `\ufffd`...)
			i++
			start = i
			continue
		}
		dst = append(dst, s[start:i]...)
		switch c {
		case '"', '\\':
			dst = append(dst, '\\', c)
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\r':
			dst = append(dst, '\\', 'r')
		case '\t':
			dst = append(dst, '\\', 't')
		default:
			dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
		}
		i++
		start = i
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

const hexDigits = "0123456789abcdef"

// appendKey appends `"key":` (key needs no escaping), after a comma unless
// dst ends an opening bracket.
func appendKey(dst []byte, key string) []byte {
	if n := len(dst); n > 0 && dst[n-1] != '{' && dst[n-1] != '[' {
		dst = append(dst, ',')
	}
	dst = append(dst, '"')
	dst = append(dst, key...)
	return append(dst, '"', ':')
}

// appendComma appends a comma unless dst ends an opening bracket.
func appendComma(dst []byte) []byte {
	if n := len(dst); n > 0 && dst[n-1] != '{' && dst[n-1] != '[' {
		dst = append(dst, ',')
	}
	return dst
}

// appendJoined appends one JSON string holding prefix, then the string
// tokens (raw JSON string tokens) joined by sep (already escaped, e.g.
// `\n`), without decoding any of them: their escaped contents are
// concatenated as they are.
func appendJoined(dst []byte, prefix string, tokens [][]byte, sep string) []byte {
	dst = appendString(dst, prefix)
	dst = dst[:len(dst)-1] // the prefix, escaped, still open
	for i, token := range tokens {
		if i > 0 {
			dst = append(dst, sep...)
		}
		dst = append(dst, token[1:len(token)-1]...)
	}
	return append(dst, '"')
}

// inner is a JSON string token without its quotes (still escaped).
func inner(token []byte) []byte {
	if !isStr(token) {
		return nil
	}
	return token[1 : len(token)-1]
}

// appendInner appends escaped string contents as one JSON string.
func appendInner(dst []byte, prefix string, contents []byte) []byte {
	dst = appendString(dst, prefix)
	dst = dst[:len(dst)-1] // the prefix, escaped, still open
	dst = append(dst, contents...)
	return append(dst, '"')
}

// appendDataURI appends "data:<media>;base64,<data>" from escaped contents,
// the base64 copied once.
func appendDataURI(dst, media, data []byte) []byte {
	dst = append(dst, `"data:`...)
	dst = append(dst, media...)
	dst = append(dst, ";base64,"...)
	dst = append(dst, data...)
	return append(dst, '"')
}

// dataURI splits the string token "data:<media>;base64,<data>" into the
// escaped contents of media and data (sub-slices of token).
func dataURI(token []byte) (media, data []byte, ok bool) {
	rest, found := bytes.CutPrefix(inner(token), []byte("data:"))
	if !found {
		return nil, nil, false
	}
	media, data, found = bytes.Cut(rest, []byte(";base64,"))
	return media, data, found && len(media) > 0
}

// rawObject renders a top-level body: members sorted by key, values copied as
// they are (each already valid JSON).
func rawObject(fields map[string]json.RawMessage) []byte {
	keys := make([]string, 0, len(fields))
	size := 2
	for key, value := range fields {
		keys = append(keys, key)
		size += len(key) + len(value) + 4
	}
	slices.Sort(keys)
	out := make([]byte, 0, size)
	out = append(out, '{')
	for _, key := range keys {
		out = appendComma(out)
		out = appendString(out, key)
		out = append(out, ':')
		out = append(out, fields[key]...)
	}
	return append(out, '}')
}

// topFields reads a body's top-level members as raw values (sub-slices of
// body: nothing is copied).
func topFields(body []byte) (map[string]json.RawMessage, error) {
	root, ok := trimValue(body)
	if !ok {
		return nil, errBadJSON
	}
	kvs, ok := objectKVs(root, make([]kv, 0, 24))
	if !ok {
		return nil, errBadJSON
	}
	out := make(map[string]json.RawMessage, len(kvs)+4)
	for _, member := range kvs {
		key := string(member.key)
		if bytes.IndexByte(member.key, '\\') >= 0 {
			key = jstr(append(append([]byte{'"'}, member.key...), '"'))
		}
		out[key] = member.val
	}
	return out, nil
}

// eachItem calls fn with each element of the array arr: its raw bytes and, for
// an object, its members, read in the same pass. False when arr is not a
// well-formed array. o's backing array is reused for the next element: fn
// copies it to keep it.
func eachItem(arr []byte, fn func(raw []byte, o obj)) bool {
	if len(arr) < 2 || arr[0] != '[' || arr[len(arr)-1] != ']' {
		return false
	}
	i := skipSpace(arr, 1)
	if arr[i] == ']' {
		return i == len(arr)-1
	}
	var reuse []kv
	for {
		var end int
		var members obj
		if arr[i] == '{' {
			kvs, objectEnd, ok := objectAt(arr, i, reuse[:0])
			if !ok {
				return false
			}
			reuse, members, end = kvs, obj(kvs), objectEnd
		} else {
			valueEnd, ok := valueEnd(arr, i)
			if !ok {
				return false
			}
			end = valueEnd
		}
		fn(arr[i:end], members)
		i = skipSpace(arr, end)
		if i >= len(arr) {
			return false
		}
		if arr[i] == ']' {
			return i == len(arr)-1
		}
		if arr[i] != ',' {
			return false
		}
		i = skipSpace(arr, i+1)
	}
}

// objectAt reads the object starting at b[i] into dst and returns its end.
func objectAt(b []byte, i int, dst []kv) ([]kv, int, bool) {
	i = skipSpace(b, i+1)
	if i < len(b) && b[i] == '}' {
		return dst, i + 1, true
	}
	for i < len(b) {
		if b[i] != '"' {
			return dst, 0, false
		}
		keyEnd, ok := stringEnd(b, i)
		if !ok {
			return dst, 0, false
		}
		key := b[i+1 : keyEnd-1]
		i = skipSpace(b, keyEnd)
		if i >= len(b) || b[i] != ':' {
			return dst, 0, false
		}
		i = skipSpace(b, i+1)
		end, ok := valueEnd(b, i)
		if !ok {
			return dst, 0, false
		}
		dst = append(dst, kv{key: key, val: b[i:end]})
		i = skipSpace(b, end)
		if i >= len(b) {
			return dst, 0, false
		}
		if b[i] == '}' {
			return dst, i + 1, true
		}
		if b[i] != ',' {
			return dst, 0, false
		}
		i = skipSpace(b, i+1)
	}
	return dst, 0, false
}

// openElem starts one more element of a JSON array being built in arr: '['
// before the first, a comma before every other (the caller closes it).
func openElem(arr []byte) []byte {
	if len(arr) == 0 {
		return append(arr, '[')
	}
	return append(arr, ',')
}

// tok is raw when it is a string token, else "" (a missing id or name).
func tok(raw []byte) []byte {
	if isStr(raw) {
		return raw
	}
	return []byte(`""`)
}

func slicesContains(values []string, value string) bool { return slices.Contains(values, value) }

func effortRank(level string) int { return slices.Index(effortOrder, level) }
