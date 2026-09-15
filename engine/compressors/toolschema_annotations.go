package compressors

import (
	"encoding/json"

	"github.com/JuliusBrussee/caveman/engine/safety"
)

// toolSchemaAnnotationsType is the forced content type for the annotation strip.
const toolSchemaAnnotationsType = "toolschema-annotations"

// annotationDropKeys are the four JSON-Schema annotation keywords the strip
// removes. Each is documentation-only: none constrains validation, none names a
// tool or a parameter. "description" is deliberately NOT here — truncating
// descriptions is a separate, separately gated decision.
var annotationDropKeys = map[string]bool{
	"$schema":    true,
	"title":      true,
	"examples":   true,
	"deprecated": true,
}

// envelopeSchemaKeys are the tool-definition fields whose value is a JSON Schema.
// They are the ONLY doors from a tool envelope into strippable territory.
var envelopeSchemaKeys = map[string]bool{
	"input_schema": true,
	"inputSchema":  true,
	"parameters":   true,
}

// envelopeNestKeys are tool-definition fields that wrap another tool envelope —
// the OpenAI `{"type":"function","function":{…}}` shape.
var envelopeNestKeys = map[string]bool{"function": true}

// schemaNameMapKeys are schema keywords whose object value is keyed by
// USER-DEFINED names. A member named "title" inside one of these is a property or
// definition name, not metadata; deleting it would change the schema and orphan
// any $ref pointing at it.
var schemaNameMapKeys = map[string]bool{
	"properties":        true,
	"patternProperties": true,
	"$defs":             true,
	"definitions":       true,
	"dependentSchemas":  true,
}

// schemaChildKeys are the JSON-Schema applicator keywords whose value is a schema
// or an array of schemas. Descent is an ALLOWLIST, not a default: a keyword that
// is not listed here (and not a name map) is left completely alone, values
// included. That is what keeps the strip honest around vendor extensions,
// instance data, and non-schema containers — "enum", "const", "default",
// "x-anything" and friends are never walked simply because they are not here.
var schemaChildKeys = map[string]bool{
	"items":                 true,
	"prefixItems":           true,
	"additionalItems":       true,
	"unevaluatedItems":      true,
	"additionalProperties":  true,
	"unevaluatedProperties": true,
	"propertyNames":         true,
	"contains":              true,
	"not":                   true,
	"if":                    true,
	"then":                  true,
	"else":                  true,
	"allOf":                 true,
	"anyOf":                 true,
	"oneOf":                 true,
}

// StripToolSchemaAnnotations removes the annotationDropKeys keywords from a
// serialized tool catalog (the provider `tools` array) and returns ok=false with
// the input unchanged on any parse anomaly.
//
// Scope is deliberately narrow. A tool envelope itself is never edited: every
// field on it — name, description, annotations, _meta, vendor extensions — is
// copied through, and the walk only enters the values of envelopeSchemaKeys.
// Inside a schema it descends only through schemaChildKeys and schemaNameMapKeys.
// MCP's `annotations.title` is a human-readable tool NAME that the model sees and
// may select on, so it is not an annotation in the JSON-Schema sense and is not
// touched.
//
// It is a targeted span deletion, not a re-serialization: every byte outside a
// removed key/value member is copied through untouched, so key order, whitespace,
// number formatting, and string escapes all survive exactly. Two properties the
// callers depend on follow from that:
//
//   - cache_control survives byte-for-byte. An Anthropic breakpoint usually rides
//     the LAST tool; re-serializing the catalog would move or reshape it and bust
//     the very cache this lever exists to preserve.
//   - the transform is deterministic and idempotent — same input bytes always
//     produce the same output bytes, and stripping a stripped catalog is a no-op
//     (ok=true, identical bytes), so the upstream prefix is stable across turns.
//
// Tool order is never changed: elements are only walked, never reordered or
// dropped.
func StripToolSchemaAnnotations(tools []byte) ([]byte, bool) {
	if !json.Valid(tools) {
		return tools, false
	}
	s := &annotationStripper{body: tools}
	start := skipSpace(tools, 0)
	end, ok := s.catalog(start)
	if !ok || skipSpace(tools, end) != len(tools) {
		return tools, false
	}
	if len(s.cuts) == 0 {
		return tools, true
	}
	out := s.apply()
	if out == nil || !json.Valid(out) {
		return tools, false
	}
	return out, true
}

// cutSpan is a half-open byte range to delete from the input.
type cutSpan struct{ start, end int }

// annotationStripper walks the catalog once and records the byte ranges to
// delete. Ranges come out in ascending order and never overlap, so applying them
// is a single copy pass over the untouched bytes.
type annotationStripper struct {
	body []byte
	cuts []cutSpan
}

// catalog walks the top-level value: an array of tool envelopes, or one envelope.
func (s *annotationStripper) catalog(i int) (int, bool) {
	if i >= len(s.body) {
		return 0, false
	}
	switch s.body[i] {
	case '[':
		return s.envelopeArray(i)
	case '{':
		return s.envelope(i)
	default:
		return skipValue(s.body, i)
	}
}

func (s *annotationStripper) envelopeArray(start int) (int, bool) {
	i := skipSpace(s.body, start+1)
	for i < len(s.body) && s.body[i] != ']' {
		end, ok := s.catalog(i)
		if !ok {
			return 0, false
		}
		i = skipSpace(s.body, end)
		if i < len(s.body) && s.body[i] == ',' {
			i = skipSpace(s.body, i+1)
		}
	}
	if i >= len(s.body) || s.body[i] != ']' {
		return 0, false
	}
	return i + 1, true
}

// envelope walks one tool definition. Nothing is ever deleted at this level; only
// the declared schema fields are entered.
func (s *annotationStripper) envelope(start int) (int, bool) {
	return s.walkObject(start, func(key string, valueStart int) (int, bool) {
		switch {
		case envelopeSchemaKeys[key]:
			return s.schema(valueStart)
		case envelopeNestKeys[key]:
			return s.catalog(valueStart)
		default:
			return skipValue(s.body, valueStart)
		}
	}, nil)
}

// schema walks one JSON Schema object, dropping annotation keywords and
// descending only through allowlisted schema positions.
func (s *annotationStripper) schema(start int) (int, bool) {
	if start >= len(s.body) {
		return 0, false
	}
	switch s.body[start] {
	case '{':
		return s.walkObject(start, func(key string, valueStart int) (int, bool) {
			switch {
			case schemaNameMapKeys[key]:
				return s.nameMap(valueStart)
			case schemaChildKeys[key]:
				return s.schema(valueStart)
			default:
				return skipValue(s.body, valueStart)
			}
		}, annotationDropKeys)
	case '[':
		// An array in a schema position holds schemas (allOf/anyOf/prefixItems,
		// or draft-4 tuple `items`).
		i := skipSpace(s.body, start+1)
		for i < len(s.body) && s.body[i] != ']' {
			end, ok := s.schema(i)
			if !ok {
				return 0, false
			}
			i = skipSpace(s.body, end)
			if i < len(s.body) && s.body[i] == ',' {
				i = skipSpace(s.body, i+1)
			}
		}
		if i >= len(s.body) || s.body[i] != ']' {
			return 0, false
		}
		return i + 1, true
	default:
		return skipValue(s.body, start)
	}
}

// nameMap walks an object whose keys are user-defined names: no key is ever
// dropped, and each value is a schema.
func (s *annotationStripper) nameMap(start int) (int, bool) {
	if start >= len(s.body) || s.body[start] != '{' {
		return skipValue(s.body, start)
	}
	return s.walkObject(start, func(_ string, valueStart int) (int, bool) {
		return s.schema(valueStart)
	}, nil)
}

// walkObject iterates one JSON object, handing each member's value to descend and
// recording a deletion for every key in drop (nil drop deletes nothing).
//
// Each deletion takes exactly one separator with it, chosen so the result stays
// valid whatever subset is dropped: a member with a surviving member before it
// swallows the comma on its LEFT (from the previous member's end), and a member
// with none swallows everything up to where the next member starts (or the
// closing brace). Deleting all members leaves "{}" plus the object's leading
// whitespace.
func (s *annotationStripper) walkObject(
	start int,
	descend func(key string, valueStart int) (int, bool),
	drop map[string]bool,
) (int, bool) {
	if start >= len(s.body) || s.body[start] != '{' {
		return 0, false
	}
	i := skipSpace(s.body, start+1)
	keptBefore := false
	prevValueEnd := -1
	for i < len(s.body) && s.body[i] != '}' {
		keyStart := i
		keyEnd, ok := stringEnd(s.body, i)
		if !ok {
			return 0, false
		}
		var key string
		if json.Unmarshal(s.body[keyStart:keyEnd], &key) != nil {
			return 0, false
		}
		i = skipSpace(s.body, keyEnd)
		if i >= len(s.body) || s.body[i] != ':' {
			return 0, false
		}
		valueStart := skipSpace(s.body, i+1)
		dropped := drop[key]
		var valueEnd int
		if dropped {
			valueEnd, ok = skipValue(s.body, valueStart)
		} else {
			valueEnd, ok = descend(key, valueStart)
		}
		if !ok {
			return 0, false
		}
		j := skipSpace(s.body, valueEnd)
		if j >= len(s.body) {
			return 0, false
		}
		var nextStart int
		switch s.body[j] {
		case ',':
			nextStart = skipSpace(s.body, j+1)
		case '}':
			nextStart = j
		default:
			return 0, false
		}
		if dropped {
			if keptBefore {
				s.cuts = append(s.cuts, cutSpan{prevValueEnd, valueEnd})
			} else {
				s.cuts = append(s.cuts, cutSpan{keyStart, nextStart})
			}
		} else {
			keptBefore = true
		}
		prevValueEnd = valueEnd
		i = nextStart
	}
	if i >= len(s.body) || s.body[i] != '}' {
		return 0, false
	}
	return i + 1, true
}

// apply copies everything outside the recorded ranges. A range that is out of
// order or out of bounds returns nil so the caller passes the original through.
func (s *annotationStripper) apply() []byte {
	out := make([]byte, 0, len(s.body))
	last := 0
	for _, cut := range s.cuts {
		if cut.start < last || cut.end > len(s.body) || cut.start > cut.end {
			return nil
		}
		out = append(out, s.body[last:cut.start]...)
		last = cut.end
	}
	return append(out, s.body[last:]...)
}

// skipValue returns the offset just past the JSON value at i without inspecting
// it. The input is pre-validated, so container nesting is tracked by depth and
// strings are skipped whole (a brace inside a string never moves the depth).
func skipValue(body []byte, i int) (int, bool) {
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
	for j < len(body) && !isJSONDelimiter(body[j]) {
		j++
	}
	return j, j > i
}

func stringEnd(body []byte, start int) (int, bool) {
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

func skipSpace(body []byte, i int) int {
	for i < len(body) && isJSONSpace(body[i]) {
		i++
	}
	return i
}

func isJSONSpace(c byte) bool {
	return c == ' ' || c == '\n' || c == '\r' || c == '\t'
}

func isJSONDelimiter(c byte) bool {
	return c == ',' || c == '}' || c == ']' || isJSONSpace(c)
}

// toolSchemaAnnotationsCompressor is the forced-only wrapper around
// StripToolSchemaAnnotations, reached only by forcing the
// "toolschema-annotations" content type; Detect never routes to it, and it is
// deliberately absent from the transform-capability manifest (see
// manifestExcluded) because it is not a compiled-plan transform.
//
// Safety class S4. The strip is semantics-preserving — it removes only annotation
// keywords that constrain nothing and name nothing — but it does change
// model-visible bytes, so it is not byte-safe and must never run on a byte-safe
// path. S4 is the honest class: it is the class defined as altering model-visible
// bytes, and its safety.Info.RequiresCCR forces the original into the recovery
// store before the transform may ship. That recovery requirement is exactly the
// local-wrap promotion clause (local wrap: recovery + CCR), the
// only clause under which this lever may ever default on. Unlike TOON it is not
// lossless-to-model: the dropped annotations are recoverable from CCR, never from
// the emitted bytes.
type toolSchemaAnnotationsCompressor struct{}

// NewToolSchemaAnnotations returns the tool-schema annotation strip.
func NewToolSchemaAnnotations() Compressor { return &toolSchemaAnnotationsCompressor{} }

func (c *toolSchemaAnnotationsCompressor) ContentType() string {
	return toolSchemaAnnotationsType
}

func (c *toolSchemaAnnotationsCompressor) SafetyClass() safety.Class { return safety.S4 }

func (c *toolSchemaAnnotationsCompressor) Compress(input []byte) ([]byte, bool) {
	return StripToolSchemaAnnotations(input)
}
