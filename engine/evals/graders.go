// Package evals is the engine's local eval harness: it replays a fixture set
// through the engine behind quality graders and reports the realized ratio and
// a pass/fail verdict, so no ratio claim ships unproven. It mirrors
// the canonical fail-closed grader contract from public/evals — an unknown
// grader type never silently passes.
package evals

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/JuliusBrussee/caveman/engine/compressors"
)

// Subject is what a grader judges: one fixture compressed by the engine.
type Subject struct {
	Input         []byte
	Output        []byte
	Ratio         float64
	ContentType   string
	Method        string
	PassedThrough bool
}

// Grader is one quality check from a fixture manifest.
type Grader struct {
	Type      string         `yaml:"type" json:"type"`
	Min       float64        `yaml:"min" json:"min,omitempty"`
	Max       float64        `yaml:"max" json:"max,omitempty"`
	Value     string         `yaml:"value" json:"value,omitempty"`
	Reference any            `yaml:"reference" json:"reference,omitempty"`
	Options   map[string]any `yaml:"options" json:"options,omitempty"`
}

// Verdict is a grader outcome (mirrors public/evals GradeResult).
type Verdict struct {
	Passed bool
	Reason string
}

func pass(reason string) Verdict { return Verdict{Passed: true, Reason: reason} }
func fail(reason string) Verdict { return Verdict{Passed: false, Reason: reason} }

// Grade runs one grader against a subject. The default branch fails closed: an
// unknown or misspelled grader type is never treated as a pass.
func Grade(g Grader, s Subject) Verdict {
	switch g.Type {
	case "exact_match":
		want := graderString(g)
		if strings.TrimSpace(string(s.Output)) == strings.TrimSpace(want) {
			return pass("output exactly matches expected value")
		}
		return fail(fmt.Sprintf("output %q != expected %q", strings.TrimSpace(string(s.Output)), strings.TrimSpace(want)))
	case "ratio_threshold":
		if s.Ratio < g.Min {
			return fail(fmt.Sprintf("ratio %.4f below min %.4f", s.Ratio, g.Min))
		}
		if g.Max > 0 && s.Ratio > g.Max {
			return fail(fmt.Sprintf("ratio %.4f above max %.4f", s.Ratio, g.Max))
		}
		return pass(fmt.Sprintf("ratio %.4f within [%.4f,%.4f]", s.Ratio, g.Min, g.Max))
	case "valid_json":
		if json.Valid(s.Output) {
			return pass("output is valid JSON")
		}
		return fail("output is not valid JSON")
	case "json_or_toon_round_trip":
		if json.Valid(s.Output) {
			return pass("output is valid JSON")
		}
		if s.Method != "toon" {
			return fail("output is not valid JSON and method is not TOON")
		}
		return gradeTOONRoundTrip(s)
	case "json_schema":
		var candidate any
		if err := json.Unmarshal(s.Output, &candidate); err != nil {
			return fail("output is not JSON: " + err.Error())
		}
		schema, ok := graderSchema(g)
		if !ok {
			return fail("json_schema requires options.schema or reference schema")
		}
		if reason := validateJSONSchema(candidate, schema, "$"); reason != "" {
			return fail(reason)
		}
		return pass("output satisfies JSON schema")
	case "tool_sequence":
		want := graderStringList(g.Options["tools"])
		if len(want) == 0 {
			want = graderStringList(g.Options["sequence"])
		}
		if len(want) == 0 {
			want = graderReferenceStringList(g.Reference)
		}
		if len(want) == 0 {
			return fail("tool_sequence requires a non-empty tools sequence")
		}
		got := extractToolSequence(s.Output)
		if reflect.DeepEqual(got, want) {
			return pass("tool call sequence matches")
		}
		return fail(fmt.Sprintf("tool sequence %v != expected %v", got, want))
	case "contains":
		if bytes.Contains(s.Output, []byte(g.Value)) {
			return pass(fmt.Sprintf("output contains %q", g.Value))
		}
		return fail(fmt.Sprintf("output missing %q", g.Value))
	case "not_contains":
		if !bytes.Contains(s.Output, []byte(g.Value)) {
			return pass(fmt.Sprintf("output omits %q", g.Value))
		}
		return fail(fmt.Sprintf("output still contains %q", g.Value))
	case "byte_identical":
		if bytes.Equal(s.Input, s.Output) {
			return pass("output is byte-identical to input")
		}
		return fail("output differs from input")
	case "compressed":
		if !s.PassedThrough {
			return pass("payload was compressed (not passed through)")
		}
		return fail("payload passed through unchanged")
	case "toon_round_trip":
		if s.PassedThrough {
			return fail("TOON round-trip requires a compressed TOON output")
		}
		return gradeTOONRoundTrip(s)
	case "recall_keys":
		// Checks that must-keep tokens survived compression. Fail-closed: an empty
		// must_contain list is a configuration error, not a pass.
		musts := graderStringList(g.Options["must_contain"])
		if len(musts) == 0 {
			return fail("recall_keys requires a non-empty options.must_contain list")
		}
		minRecall := 1.0
		switch v := g.Options["min_recall"].(type) {
		case float64:
			minRecall = v
		case int:
			minRecall = float64(v)
		}
		kept := 0
		var missing []string
		for _, m := range musts {
			if bytes.Contains(s.Output, []byte(m)) {
				kept++
			} else {
				missing = append(missing, m)
			}
		}
		recall := float64(kept) / float64(len(musts))
		if recall >= minRecall {
			return pass(fmt.Sprintf("recall %.2f ≥ %.2f (%d/%d kept)", recall, minRecall, kept, len(musts)))
		}
		return fail(fmt.Sprintf("recall %.2f < %.2f, missing %v", recall, minRecall, missing))
	default:
		// Fail closed: an unknown grader type is a gate failure, never a pass.
		return fail("unknown grader type: " + strings.TrimSpace(g.Type))
	}
}

func gradeTOONRoundTrip(s Subject) Verdict {
	want, ok := compressors.DecodeJSONForTOONGrader(s.Input)
	if !ok {
		return fail("input is not valid JSON for TOON round-trip")
	}
	got, ok := compressors.DecodeTOON(s.Output)
	if !ok {
		return fail("output is not decodable TOON")
	}
	if reflect.DeepEqual(got, want) {
		return pass("TOON decodes to the original JSON value")
	}
	return fail("TOON output does not round-trip to the original JSON value")
}

// graderStringList coerces a YAML-decoded option sequence into a string slice.
// Scalar entries that are not strings (e.g. an unquoted numeric error code like
// 503) are stringified rather than silently dropped — dropping them would make
// the recall_keys gate fail OPEN (claim full recall while a must-keep token was
// never checked). Nested collections are skipped: a must-keep entry is a scalar.
func graderStringList(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		if ss, ok := v.([]string); ok {
			return append([]string(nil), ss...)
		}
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		switch t := e.(type) {
		case string:
			out = append(out, t)
		case nil, []any, map[string]any:
			// skip: not a scalar token
		default:
			out = append(out, fmt.Sprint(t))
		}
	}
	return out
}

func graderString(g Grader) string {
	if g.Value != "" {
		return g.Value
	}
	if s, ok := g.Reference.(string); ok {
		return s
	}
	return fmt.Sprint(g.Reference)
}

func graderSchema(g Grader) (map[string]any, bool) {
	if g.Options != nil {
		if schema, ok := mapAny(g.Options["schema"]); ok {
			return schema, true
		}
	}
	if schema, ok := mapAny(g.Reference); ok {
		return schema, true
	}
	if raw, ok := g.Reference.(string); ok && strings.TrimSpace(raw) != "" {
		var schema map[string]any
		if json.Unmarshal([]byte(raw), &schema) == nil {
			return schema, true
		}
	}
	return nil, false
}

func mapAny(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, v := range m {
			ks, ok := k.(string)
			if !ok {
				return nil, false
			}
			out[ks] = v
		}
		return out, true
	default:
		return nil, false
	}
}

func validateJSONSchema(v any, schema map[string]any, path string) string {
	if enum, ok := schema["enum"].([]any); ok {
		for _, allowed := range enum {
			if reflect.DeepEqual(v, allowed) {
				return ""
			}
		}
		return fmt.Sprintf("%s not in enum %v", path, enum)
	}
	if c, ok := schema["const"]; ok && !reflect.DeepEqual(v, c) {
		return fmt.Sprintf("%s != const %v", path, c)
	}
	if types := schemaTypeSet(schema["type"]); len(types) > 0 && !valueMatchesAnyType(v, types) {
		return fmt.Sprintf("%s has wrong type, want %v", path, types)
	}
	switch x := v.(type) {
	case map[string]any:
		if req := graderStringList(schema["required"]); len(req) > 0 {
			for _, key := range req {
				if _, ok := x[key]; !ok {
					return fmt.Sprintf("%s missing required property %q", path, key)
				}
			}
		}
		if props, ok := mapAny(schema["properties"]); ok {
			for key, rawSchema := range props {
				child, ok := x[key]
				if !ok {
					continue
				}
				childSchema, ok := mapAny(rawSchema)
				if !ok {
					return fmt.Sprintf("%s.properties.%s is not a schema", path, key)
				}
				if reason := validateJSONSchema(child, childSchema, path+"."+key); reason != "" {
					return reason
				}
			}
		}
		if allow, ok := schema["additionalProperties"].(bool); ok && !allow {
			props, _ := mapAny(schema["properties"])
			for key := range x {
				if _, ok := props[key]; !ok {
					return fmt.Sprintf("%s has additional property %q", path, key)
				}
			}
		}
	case []any:
		if min, ok := number(schema["minItems"]); ok && len(x) < int(min) {
			return fmt.Sprintf("%s has %d items, below minItems %.0f", path, len(x), min)
		}
		if max, ok := number(schema["maxItems"]); ok && len(x) > int(max) {
			return fmt.Sprintf("%s has %d items, above maxItems %.0f", path, len(x), max)
		}
		if itemSchema, ok := mapAny(schema["items"]); ok {
			for i, child := range x {
				if reason := validateJSONSchema(child, itemSchema, fmt.Sprintf("%s[%d]", path, i)); reason != "" {
					return reason
				}
			}
		}
	case string:
		if min, ok := number(schema["minLength"]); ok && len(x) < int(min) {
			return fmt.Sprintf("%s length below minLength %.0f", path, min)
		}
		if max, ok := number(schema["maxLength"]); ok && len(x) > int(max) {
			return fmt.Sprintf("%s length above maxLength %.0f", path, max)
		}
		if pattern, ok := schema["pattern"].(string); ok {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return fmt.Sprintf("%s has invalid pattern %q", path, pattern)
			}
			if !re.MatchString(x) {
				return fmt.Sprintf("%s does not match pattern %q", path, pattern)
			}
		}
	case float64:
		if min, ok := number(schema["minimum"]); ok && x < min {
			return fmt.Sprintf("%s %.4f below minimum %.4f", path, x, min)
		}
		if max, ok := number(schema["maximum"]); ok && x > max {
			return fmt.Sprintf("%s %.4f above maximum %.4f", path, x, max)
		}
	}
	return ""
}

func schemaTypeSet(v any) map[string]bool {
	out := map[string]bool{}
	switch x := v.(type) {
	case string:
		out[x] = true
	case []any:
		for _, e := range x {
			if s, ok := e.(string); ok {
				out[s] = true
			}
		}
	}
	return out
}

func valueMatchesAnyType(v any, types map[string]bool) bool {
	for typ := range types {
		switch typ {
		case "object":
			if _, ok := v.(map[string]any); ok {
				return true
			}
		case "array":
			if _, ok := v.([]any); ok {
				return true
			}
		case "string":
			if _, ok := v.(string); ok {
				return true
			}
		case "number":
			if _, ok := v.(float64); ok {
				return true
			}
		case "integer":
			if n, ok := v.(float64); ok && n == float64(int64(n)) {
				return true
			}
		case "boolean":
			if _, ok := v.(bool); ok {
				return true
			}
		case "null":
			if v == nil {
				return true
			}
		}
	}
	return false
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	default:
		return 0, false
	}
}

func graderReferenceStringList(v any) []string {
	if out := graderStringList(v); len(out) > 0 {
		return out
	}
	if s, ok := v.(string); ok {
		return splitToolNames(s)
	}
	return nil
}

func extractToolSequence(raw []byte) []string {
	var v any
	if json.Unmarshal(raw, &v) == nil {
		if out := toolNamesFromValue(v); len(out) > 0 {
			return out
		}
	}
	return splitToolNames(string(raw))
}

func toolNamesFromValue(v any) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			out = append(out, toolNamesFromValue(e)...)
		}
		return out
	case map[string]any:
		for _, key := range []string{"tool_calls", "calls", "steps", "tools"} {
			if calls, ok := x[key]; ok {
				return toolNamesFromValue(calls)
			}
		}
		for _, key := range []string{"name", "tool", "tool_name"} {
			if name, ok := x[key].(string); ok && strings.TrimSpace(name) != "" {
				return []string{strings.TrimSpace(name)}
			}
		}
		if fn, ok := mapAny(x["function"]); ok {
			if name, ok := fn["name"].(string); ok && strings.TrimSpace(name) != "" {
				return []string{strings.TrimSpace(name)}
			}
		}
	case string:
		return splitToolNames(x)
	}
	return nil
}

func splitToolNames(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\t' || r == '>' || r == '|'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(strings.Trim(f, "-"))
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}
