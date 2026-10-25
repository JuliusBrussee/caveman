package compressors

import (
	"encoding/json"
	"github.com/JuliusBrussee/caveman/shared/toolpolicy"
	"reflect"
	"strings"
	"testing"
)

func TestPreservedToolCatalogKeepsFullEnvelopeAndSchema(t *testing.T) {
	description := strings.Repeat("Use targeted semantic exploration. More detailed selection guidance follows. ", 20)
	protected := map[string]any{"name": "tokensave_context", "description": description, "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string", "description": description, "default": "Parser.parse", "maxLength": 42}}, "required": []any{"query"}}, "annotations": map[string]any{"title": "Code context"}}
	ordinary := map[string]any{"name": "Read", "description": description, "inputSchema": map[string]any{"type": "object"}}
	for _, wrap := range []string{"mcp", "openai", "gemini"} {
		t.Run(wrap, func(t *testing.T) {
			var input any = []any{protected, ordinary}
			if wrap == "openai" {
				input = []any{map[string]any{"type": "function", "function": protected}, map[string]any{"type": "function", "function": ordinary}}
			}
			if wrap == "gemini" {
				input = map[string]any{"tools": []any{map[string]any{"functionDeclarations": []any{protected, ordinary}}}}
			}
			raw, _ := json.Marshal(input)
			out, ok := NewToolSchemaWithPreservedTools(toolpolicy.Policy{"tokensave_*"}).Compress(raw)
			if !ok {
				t.Fatal("compress failed")
			}
			var decoded any
			if err := json.Unmarshal(out, &decoded); err != nil {
				t.Fatal(err)
			}
			var find func(any) map[string]any
			find = func(v any) map[string]any {
				switch x := v.(type) {
				case map[string]any:
					if x["name"] == "tokensave_context" {
						return x
					}
					for _, child := range x {
						if m := find(child); m != nil {
							return m
						}
					}
				case []any:
					for _, child := range x {
						if m := find(child); m != nil {
							return m
						}
					}
				}
				return nil
			}
			// Compare decoded values to avoid number representation differences.
			encoded, _ := json.Marshal(protected)
			var want map[string]any
			_ = json.Unmarshal(encoded, &want)
			if !reflect.DeepEqual(find(decoded), want) {
				t.Fatalf("protected envelope changed: %s", out)
			}
			if wrap != "gemini" && len(out) >= len(raw) {
				t.Fatal("unrelated catalog entry did not shrink")
			}
		})
	}
}
