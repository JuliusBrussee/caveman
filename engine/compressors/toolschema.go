package compressors

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/JuliusBrussee/caveman/engine/safety"
)

// maxDescLen caps the non-constraint *lead* sentence of a kept description at this
// many bytes. A short lead keeps a selection hint; the rest of the prose is
// recoverable via CCR. Constraint-bearing sentences are never subject to this cap
// (see compressDescription) — dropping them would produce invalid tool calls.
const maxDescLen = 80

// schemaMetaDrop is the set of JSON-Schema annotation keys that never affect tool
// selection and carry the most bytes: examples, titles, comments, and the dialect
// marker. They are dropped outright. NOTE: these are only dropped where they are
// schema *metadata* — never where they are user-defined names (see
// compressSchema's userDefinedKeys handling for properties / $defs / definitions /
// patternProperties). `default` is deliberately NOT in this set: a default value
// is part of argument construction (an agent that omits it relies on it), so it is
// kept — see keepSchemaKeys.
var schemaMetaDrop = map[string]bool{
	"examples": true,
	"example":  true,
	"$comment": true,
	"title":    true,
	"$schema":  true,
}

// keepSchemaKeys is the set of schema keys that carry argument-construction meaning
// and are kept verbatim (no recursion, no truncation): enum and required are the
// selection surface; default is what an agent omits an argument to inherit — losing
// it silently changes the call. `const` pins a single valid value, same hazard.
var keepSchemaKeys = map[string]bool{
	"enum":     true,
	"required": true,
	"default":  true,
	"const":    true,
}

// userDefinedKeys are schema keys whose *child map keys* are user-controlled names
// (property names, $defs/definition names, patternProperties regexes, dependency
// property names), not schema vocabulary. Their keys must all survive verbatim —
// a definition or dependency named "title" is not metadata. Only schema values
// under these keys are compressed; string-array dependency values pass through.
var userDefinedKeys = map[string]bool{
	"properties":        true,
	"$defs":             true,
	"definitions":       true,
	"patternProperties": true,
	"dependentSchemas":  true,
	"dependentRequired": true,
	"dependencies":      true,
}

// toolSchemaCompressor compresses an MCP/OpenAI tool-definition catalog (or any
// JSON Schema) by dropping annotation metadata and reducing free-text descriptions
// to their selection lead plus every constraint-bearing sentence, while preserving
// every selection-relevant token verbatim — tool and parameter names, types, enums,
// required, defaults, and $ref targets.
//
// Two structural boundaries it upholds:
//   - selection surface: names/params/enums/required survive byte-for-byte. This
//     does not guarantee same-tool behavior because descriptions are model-visible
//     and reduced lossily; behavioral equivalence requires model evals.
//   - argument validity: a description sentence stating a constraint (must / required
//     / exactly one / rejected / format / ISO / RFC / absolute) is retained in full,
//     reducing the risk of invalid arguments. It does not *guarantee* argument
//     validity — only that recognised constraint sentences present in the source
//     are not dropped.
//
// It is S4 (lossy): dropped descriptive prose and metadata are recoverable through
// CCR. On any parse problem it reports !ok and the caller forwards bytes unchanged.
type toolSchemaCompressor struct{}

// NewToolSchema returns the tool-schema / tool-catalog compressor. It is selected
// by forcing the "toolschema" content type (engine.Options.Type); Detect never
// routes to it, so the proxy's generic JSON path is unaffected.
func NewToolSchema() Compressor { return &toolSchemaCompressor{} }

func (c *toolSchemaCompressor) ContentType() string       { return "toolschema" }
func (c *toolSchemaCompressor) SafetyClass() safety.Class { return safety.S4 }

func (c *toolSchemaCompressor) Compress(input []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false // malformed JSON → pass-through
	}
	if dec.More() {
		return nil, false // more than one value → not a single catalog payload
	}
	out, err := json.Marshal(c.compressDocument(v))
	if err != nil {
		return nil, false
	}
	return out, true
}

// compressDocument distinguishes provider tool envelopes from raw JSON Schema.
// Envelope fields are model-visible tool metadata, not JSON-Schema vocabulary:
// MCP annotations.title, vendor metadata, and cache hints must survive. Tool
// descriptions take the compressor's explicit description path; only declared
// schema-valued fields cross into compressSchema.
func (c *toolSchemaCompressor) compressDocument(v any) any {
	switch document := v.(type) {
	case []any:
		out := make([]any, len(document))
		for i, tool := range document {
			out[i] = c.compressToolEnvelope(tool)
		}
		return out
	case map[string]any:
		if tools, ok := document["tools"]; ok {
			out := cloneJSONMap(document)
			out["tools"] = c.compressToolEnvelope(tools)
			return out
		}
		if isToolEnvelope(document) {
			return c.compressToolEnvelope(document)
		}
		return c.compressSchema(document, false)
	default:
		return v
	}
}

func (c *toolSchemaCompressor) compressToolEnvelope(v any) any {
	switch envelope := v.(type) {
	case []any:
		out := make([]any, len(envelope))
		for i, tool := range envelope {
			out[i] = c.compressToolEnvelope(tool)
		}
		return out
	case map[string]any:
		out := cloneJSONMap(envelope)
		for key, value := range envelope {
			switch {
			case envelopeSchemaKeys[key]:
				out[key] = c.compressSchema(value, false)
			case envelopeNestKeys[key]:
				out[key] = c.compressToolEnvelope(value)
			case key == "description":
				// Tool descriptions are intentionally reduced by this lossy
				// compressor, but no generic schema metadata rule applies to
				// their surrounding envelope.
				if description, ok := value.(string); ok {
					out[key] = compressDescription(description)
				}
			}
		}
		return out
	default:
		return v
	}
}

func isToolEnvelope(value map[string]any) bool {
	if _, ok := value["name"]; ok {
		return true
	}
	for key := range value {
		if envelopeSchemaKeys[key] || envelopeNestKeys[key] {
			return true
		}
	}
	return false
}

func cloneJSONMap(value map[string]any) map[string]any {
	clone := make(map[string]any, len(value))
	for key, item := range value {
		clone[key] = item
	}
	return clone
}

// compressSchema walks a parsed JSON Schema / tool catalog. inUserKeys is true when
// the map is the value of a properties / $defs / definitions / patternProperties
// object, whose keys are user-defined names that must all survive verbatim — only
// their schema values are compressed. Everywhere else the map is a schema object
// whose annotation keys may be dropped, and whose descriptions are reduced.
func (c *toolSchemaCompressor) compressSchema(v any, inUserKeys bool) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, val := range t {
			if inUserKeys {
				// Keys here are user-defined names — keep every one; compress its
				// schema value as a normal schema object.
				m[k] = c.compressSchema(val, false)
				continue
			}
			switch {
			case schemaMetaDrop[k]:
				// Drop annotation metadata outright (recoverable via CCR).
			case k == "description":
				if s, ok := val.(string); ok {
					m[k] = compressDescription(s)
				} else {
					m[k] = c.compressSchema(val, false)
				}
			case keepSchemaKeys[k]:
				// Argument-construction meaning — keep verbatim, no recursion.
				m[k] = val
			case userDefinedKeys[k]:
				m[k] = c.compressSchema(val, true)
			default:
				m[k] = c.compressSchema(val, false)
			}
		}
		return m
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = c.compressSchema(e, false)
		}
		return out
	default:
		return v
	}
}

// constraintRe matches an argument-construction constraint marker as a whole word
// or phrase. Matching is deliberately biased to OVER-keep: retaining an extra
// sentence only costs a little ratio, while dropping a constraint sentence produces
// an invalid tool call and a retry loop that costs more than the shrink saved. The
// set is broad on purpose — bound words (over/above/max/range/…), prohibition words
// (cannot/invalid/not allowed/…), and format anchors (iso/rfc/absolute) — so a
// constraint phrased without the word "must" is still retained. iso/rfc allow a
// trailing digit run so "RFC3339" and "ISO8601" match.
var constraintRe = regexp.MustCompile(`(?i)\b(?:` +
	`must|must not|cannot|can't|shall|` +
	`require\w*|reject\w*|disallow\w*|forbidden|` +
	`invalid|not allowed|not permitted|` +
	`exactly one|only one of|one of|at least|at most|` +
	`mutually exclusive|only|unique|case[- ]?sensitive|` +
	`max|min|maximum|minimum|range|between|` +
	`greater than|less than|more than|fewer than|no more than|no less than|` +
	`over|above|below|under|beyond|exceed\w*|` +
	`format\w*|iso[- ]?\d*|rfc[- ]?\d*|absolute` +
	`)\b`)

// hasConstraintMarker reports whether a sentence states an argument constraint.
func hasConstraintMarker(s string) bool { return constraintRe.MatchString(s) }

// smallDescBudget is the size below which a description is kept WHOLE, no sentence
// eliding at all. Below it, dropping a sentence saves too few bytes to justify any
// risk of under-keeping a constraint the marker set does not recognise — so short
// descriptions (the common case) pass through verbatim and only genuinely large
// ones are reduced.
const smallDescBudget = maxDescLen * 2

// compressDescription reduces a free-text description. A description that already
// fits smallDescBudget is kept whole — eliding it would save a handful of bytes
// while risking the loss of a constraint stated without a marker word. Only a
// genuinely large description is reduced: to its selection lead (the first
// non-constraint sentence, capped at maxDescLen on a rune boundary) plus every
// constraint-bearing sentence, in original order. Deterministic and idempotent:
// re-compressing its own output yields the same string.
func compressDescription(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= smallDescBudget {
		return s
	}
	sentences := splitSentences(s)
	var kept []string
	leadTaken := false
	for _, sent := range sentences {
		switch {
		case hasConstraintMarker(sent):
			kept = append(kept, sent) // never dropped, never capped
		case !leadTaken:
			kept = append(kept, capRunes(sent, maxDescLen))
			leadTaken = true
		default:
			// A non-constraint, non-lead sentence: drop (recoverable via CCR).
		}
	}
	return strings.Join(kept, " ")
}

// splitSentences breaks prose into sentences on a period that is followed by
// whitespace and an uppercase letter and does not end an abbreviation. This keeps
// "e.g.", "i.e.", "Node.js", "v1.2" and decimals ("3.14") intact: the last two are
// excluded because their period is not followed by whitespace, and the first two by
// the abbreviation guard. Sentences are returned with their trailing period.
func splitSentences(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '.' {
			continue
		}
		j := i + 1
		if j >= len(s) || !isSpaceByte(s[j]) {
			continue // period not followed by whitespace → not a boundary
		}
		for j < len(s) && isSpaceByte(s[j]) {
			j++
		}
		if j >= len(s) {
			continue
		}
		r, _ := utf8.DecodeRuneInString(s[j:])
		if !unicode.IsUpper(r) {
			continue // next word not capitalised → not a sentence start
		}
		if endsWithAbbrev(s, i) {
			continue
		}
		if seg := strings.TrimSpace(s[start : i+1]); seg != "" {
			out = append(out, seg)
		}
		start = j
		i = j - 1 // resume scanning at the next sentence's first char
	}
	if seg := strings.TrimSpace(s[start:]); seg != "" {
		out = append(out, seg)
	}
	return out
}

// endsWithAbbrev reports whether the period at dotIdx terminates an abbreviation
// rather than a sentence: a single-letter initial ("A."), a token with an internal
// dot ("e.g", "i.e", "U.S"), or a known abbreviation word ("etc", "vs", "no").
func endsWithAbbrev(s string, dotIdx int) bool {
	k := dotIdx - 1
	for k >= 0 && (isLetterByte(s[k]) || s[k] == '.') {
		k--
	}
	word := strings.ToLower(strings.Trim(s[k+1:dotIdx], "."))
	if word == "" {
		return false
	}
	if len(word) == 1 { // an initial: "A. Name"
		return true
	}
	if strings.Contains(word, ".") { // "e.g", "i.e", "u.s"
		return true
	}
	return abbrevSet[word]
}

// abbrevSet is a conservative set of English/Latin abbreviations whose trailing
// period must not be read as a sentence boundary. Treating an ambiguous token as an
// abbreviation only over-joins (keeps more together), which is the safe direction.
var abbrevSet = map[string]bool{
	"e.g": true, "i.e": true, "eg": true, "ie": true, "etc": true,
	"vs": true, "cf": true, "al": true, "viz": true, "resp": true,
	"approx": true, "no": true, "fig": true, "figs": true, "dr": true,
	"mr": true, "mrs": true, "ms": true, "prof": true, "sr": true,
	"jr": true, "st": true, "inc": true, "ltd": true, "corp": true,
	"co": true, "dept": true, "est": true,
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f'
}
func isLetterByte(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }

// capRunes truncates s to at most maxBytes bytes without splitting a UTF-8 rune,
// so the compressor never emits a U+FFFD replacement character into the
// model-visible catalog. It backs off to the last word boundary when
// that still leaves a substantial lead, rather than emitting a mangled half-word.
// It is idempotent for already-short strings.
func capRunes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := s[:maxBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	if sp := strings.LastIndexByte(cut, ' '); sp > maxBytes/2 {
		cut = cut[:sp]
	}
	return strings.TrimRight(cut, " ")
}
