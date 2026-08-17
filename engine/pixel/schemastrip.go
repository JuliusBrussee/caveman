// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

const schemaStripMaxDepth = 20
const formatMaxLen = 32

var schemaStripKeys = map[string]struct{}{
	"description": {},
	"title":       {},
	"examples":    {},
	"default":     {},
	"$schema":     {},
	"$id":         {},
	"$comment":    {},
}

var schemaCompositionKeys = map[string]struct{}{
	"oneOf": {},
	"anyOf": {},
	"allOf": {},
}

var schemaNamedSubschemaKeys = map[string]struct{}{
	"properties":        {},
	"patternProperties": {},
	"definitions":       {},
	"$defs":             {},
}

var schemaSingleSubschemaKeys = map[string]struct{}{
	"items":                 {},
	"additionalProperties":  {},
	"not":                   {},
	"contains":              {},
	"propertyNames":         {},
	"unevaluatedItems":      {},
	"unevaluatedProperties": {},
	"if":                    {},
	"then":                  {},
	"else":                  {},
}

var schemaVerbatimKeys = map[string]struct{}{
	"required":         {},
	"enum":             {},
	"const":            {},
	"type":             {},
	"$ref":             {},
	"minimum":          {},
	"maximum":          {},
	"exclusiveMinimum": {},
	"exclusiveMaximum": {},
	"minLength":        {},
	"maxLength":        {},
	"minItems":         {},
	"maxItems":         {},
	"minProperties":    {},
	"maxProperties":    {},
	"multipleOf":       {},
	"uniqueItems":      {},
	"pattern":          {},
}

func StripSchemaDescriptions(schema any) (any, bool) {
	return stripSchemaDescriptions(schema, 0)
}

func stripSchemaDescriptions(node any, depth int) (any, bool) {
	if depth > schemaStripMaxDepth {
		return node, false
	}
	switch v := node.(type) {
	case []any:
		return node, false
	case map[string]any:
		out := make(map[string]any, len(v))
		changed := false
		for k, val := range v {
			if _, ok := schemaStripKeys[k]; ok {
				changed = true
				continue
			}
			if k == "format" {
				if s, ok := val.(string); ok && len(s) > formatMaxLen {
					changed = true
					continue
				}
			}
			if _, ok := schemaVerbatimKeys[k]; ok {
				out[k] = val
				continue
			}
			if _, ok := schemaNamedSubschemaKeys[k]; ok {
				if m, ok := val.(map[string]any); ok {
					nested := make(map[string]any, len(m))
					for pk, pv := range m {
						stripped, subChanged := stripSchemaDescriptions(pv, depth+1)
						if subChanged {
							changed = true
						}
						nested[pk] = stripped
					}
					out[k] = nested
					continue
				}
			}
			if _, ok := schemaCompositionKeys[k]; ok {
				if arr, ok := val.([]any); ok {
					next := make([]any, len(arr))
					for i, sub := range arr {
						stripped, subChanged := stripSchemaDescriptions(sub, depth+1)
						if subChanged {
							changed = true
						}
						next[i] = stripped
					}
					out[k] = next
					continue
				}
			}
			if _, ok := schemaSingleSubschemaKeys[k]; ok {
				if _, ok := val.(bool); ok {
					out[k] = val
				} else {
					stripped, subChanged := stripSchemaDescriptions(val, depth+1)
					if subChanged {
						changed = true
					}
					out[k] = stripped
				}
				continue
			}
			if _, ok := val.(map[string]any); ok {
				stripped, subChanged := stripSchemaDescriptions(val, depth+1)
				if subChanged {
					changed = true
				}
				out[k] = stripped
			} else {
				out[k] = val
			}
		}
		return out, changed
	default:
		return node, false
	}
}

var SchemaStructuralKeys = []string{"properties", "patternProperties", "oneOf", "anyOf", "allOf", "items", "$ref", "enum", "const"}

func SchemaHasStructure(schema map[string]any) bool {
	for _, key := range SchemaStructuralKeys {
		if _, ok := schema[key]; ok {
			return true
		}
	}
	return false
}
