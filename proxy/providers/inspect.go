package providers

import (
	"bytes"
	"encoding/json"
	"strconv"
	"unicode"
)

// inspectObject returns the map json.Unmarshal(data, &decoded) gives
// InspectRequest and whether that call succeeds, without decoding the
// conversation. One pass validates data exactly as encoding/json does and
// finds the top-level members. Every member is then decoded as before except
// messages and input, which InspectRequest reads only for their length.
//
// The nested pricing walk (requestPricingUnsupportedReason) can match only a
// key or string that spells audio, cachedcontent or cached_content in some
// case. A request that may spell one (spelled with a \u escape or a non-ASCII
// letter included) is decoded whole, as before, so the walk sees all of it.
func inspectObject(data []byte) (map[string]any, bool) {
	s := requestScan{data: data}
	if !s.value() || s.space() != len(data) || s.overflow {
		return nil, false
	}
	if s.spelled || spellsPricingKey(data) {
		var decoded map[string]any
		return decoded, json.Unmarshal(data, &decoded) == nil
	}
	switch data[skipSpace(data, 0)] {
	case 'n':
		return nil, true // null decodes to a nil map
	case '{':
	default:
		return nil, false // not an object: a type error
	}
	decoded := make(map[string]any, len(s.members))
	for _, m := range s.members {
		key := string(m.key[1 : len(m.key)-1])
		if !plainASCII(m.key) && json.Unmarshal(m.key, &key) != nil {
			return nil, false
		}
		if (key == "messages" || key == "input") && m.elements >= 0 {
			decoded[key] = make([]any, m.elements) // only the length is read
			continue
		}
		var value any
		if json.Unmarshal(data[m.start:m.end], &value) != nil {
			return nil, false
		}
		decoded[key] = value
	}
	return decoded, true
}

// spellsPricingKey reports whether data holds audio, cachedcontent or
// cached_content in any ASCII case. It visits each d, the letter all three
// share.
func spellsPricingKey(data []byte) bool {
	for _, letter := range []byte{'d', 'D'} {
		for i := 0; ; {
			j := bytes.IndexByte(data[i:], letter)
			if j < 0 {
				break
			}
			d := i + j
			if d >= 2 && foldPrefix(data[d-2:], "audio") ||
				d >= 5 && foldPrefix(data[d-5:], "cached") && (foldPrefix(data[d+1:], "content") || foldPrefix(data[d+1:], "_content")) {
				return true
			}
			i = d + 1
		}
	}
	return false
}

// plainASCII reports whether a quoted key reads as written: no escape, and no
// byte a decode could replace (encoding/json turns invalid UTF-8 into U+FFFD).
func plainASCII(quoted []byte) bool {
	for _, c := range quoted {
		if c == '\\' || c >= 0x80 {
			return false
		}
	}
	return true
}

func foldPrefix(data []byte, word string) bool {
	if len(data) < len(word) {
		return false
	}
	for i := 0; i < len(word); i++ {
		c := data[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != word[i] {
			return false
		}
	}
	return true
}

// encoding/json refuses nesting deeper than this.
const maxJSONDepth = 10000

// requestScan is a JSON validator that accepts exactly what json.Valid
// accepts. String content, nearly all of an agent request, is checked with a
// table lookup per byte rather than a state machine call. Along the way it
// notes what inspectObject needs: the top-level members, numbers a decode
// into any refuses, and letters a \u escape or a non-ASCII rune could add.
type requestScan struct {
	data     []byte
	pos      int
	depth    int
	members  []scanMember
	overflow bool // a number outside float64 range (json.Unmarshal into any fails)
	spelled  bool // a \u escape or U+0130 / U+212A lowercases to an ASCII letter
}

type scanMember struct {
	key        []byte // the quoted key as written
	start, end int    // the value
	elements   int    // array length, -1 for any other value
}

// plainStringByte marks the string bytes needing no further look: not a
// quote, backslash or control byte, and not the lead byte of U+0130 (C4 B0)
// or U+212A (E2 84 AA).
var plainStringByte = func() (plain [256]bool) {
	for c := 0x20; c < 256; c++ {
		plain[c] = c != '"' && c != '\\' && c != 0xC4 && c != 0xE2
	}
	return plain
}()

func skipSpace(data []byte, i int) int {
	for i < len(data) && (data[i] == ' ' || data[i] == '\t' || data[i] == '\n' || data[i] == '\r') {
		i++
	}
	return i
}

func (s *requestScan) space() int {
	s.pos = skipSpace(s.data, s.pos)
	return s.pos
}

func (s *requestScan) at(c byte) bool { return s.pos < len(s.data) && s.data[s.pos] == c }

func (s *requestScan) value() bool {
	if s.space() >= len(s.data) {
		return false
	}
	switch c := s.data[s.pos]; {
	case c == '{':
		return s.object()
	case c == '[':
		_, ok := s.array()
		return ok
	case c == '"':
		return s.str()
	case c == 't':
		return s.literal("true")
	case c == 'f':
		return s.literal("false")
	case c == 'n':
		return s.literal("null")
	case c == '-' || '0' <= c && c <= '9':
		return s.number()
	}
	return false
}

func (s *requestScan) object() bool {
	s.pos++
	if s.depth++; s.depth > maxJSONDepth {
		return false
	}
	top := s.depth == 1
	if s.space(); s.at('}') {
		s.pos++
		s.depth--
		return true
	}
	for {
		if s.space(); !s.at('"') {
			return false
		}
		keyStart := s.pos
		if !s.str() {
			return false
		}
		keyEnd := s.pos
		if s.space(); !s.at(':') {
			return false
		}
		s.pos++
		start, elements := s.space(), -1
		if top && s.at('[') {
			n, ok := s.array()
			if !ok {
				return false
			}
			elements = n
		} else if !s.value() {
			return false
		}
		if top {
			s.members = append(s.members, scanMember{key: s.data[keyStart:keyEnd], start: start, end: s.pos, elements: elements})
		}
		s.space()
		switch {
		case s.at(','):
			s.pos++
		case s.at('}'):
			s.pos++
			s.depth--
			return true
		default:
			return false
		}
	}
}

func (s *requestScan) array() (int, bool) {
	s.pos++
	if s.depth++; s.depth > maxJSONDepth {
		return 0, false
	}
	if s.space(); s.at(']') {
		s.pos++
		s.depth--
		return 0, true
	}
	for n := 1; ; n++ {
		if !s.value() {
			return 0, false
		}
		s.space()
		switch {
		case s.at(','):
			s.pos++
		case s.at(']'):
			s.pos++
			s.depth--
			return n, true
		default:
			return 0, false
		}
	}
}

func (s *requestScan) str() bool {
	data := s.data
	for i := s.pos + 1; ; {
		for i < len(data) && plainStringByte[data[i]] {
			i++
		}
		if i >= len(data) {
			return false
		}
		switch c := data[i]; {
		case c == '"':
			s.pos = i + 1
			return true
		case c == '\\':
			if i+1 >= len(data) {
				return false
			}
			switch data[i+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				i += 2
			case 'u':
				if i+6 > len(data) {
					return false
				}
				var r rune
				for _, h := range data[i+2 : i+6] {
					switch {
					case '0' <= h && h <= '9':
						h -= '0'
					case 'a' <= h && h <= 'f':
						h -= 'a' - 10
					case 'A' <= h && h <= 'F':
						h -= 'A' - 10
					default:
						return false
					}
					r = r<<4 | rune(h)
				}
				if lower := unicode.ToLower(r); lower == '_' || 'a' <= lower && lower <= 'z' {
					s.spelled = true
				}
				i += 6
			default:
				return false
			}
		case c < 0x20:
			return false
		default: // 0xC4 or 0xE2: U+0130 and U+212A lowercase to i and k
			if c == 0xC4 && bytes.HasPrefix(data[i+1:], []byte{0xB0}) || c == 0xE2 && bytes.HasPrefix(data[i+1:], []byte{0x84, 0xAA}) {
				s.spelled = true
			}
			i++
		}
	}
}

func (s *requestScan) literal(word string) bool {
	if !bytes.HasPrefix(s.data[s.pos:], []byte(word)) {
		return false
	}
	s.pos += len(word)
	return true
}

func (s *requestScan) number() bool {
	data, start := s.data, s.pos
	i := start
	digits := func() bool {
		from := i
		for i < len(data) && '0' <= data[i] && data[i] <= '9' {
			i++
		}
		return i > from
	}
	if data[i] == '-' {
		i++
	}
	switch {
	case i < len(data) && data[i] == '0':
		i++
	case !digits():
		return false
	}
	if i < len(data) && data[i] == '.' {
		i++
		if !digits() {
			return false
		}
	}
	exponent := i < len(data) && (data[i] == 'e' || data[i] == 'E')
	if exponent {
		i++
		if i < len(data) && (data[i] == '+' || data[i] == '-') {
			i++
		}
		if !digits() {
			return false
		}
	}
	s.pos = i
	// Without an exponent only a literal over 308 digits leaves float64 range.
	if exponent || i-start > 300 {
		if _, err := strconv.ParseFloat(string(data[start:i]), 64); err != nil {
			s.overflow = true
		}
	}
	return true
}
