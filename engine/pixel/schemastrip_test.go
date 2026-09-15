// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestStripSchemaDescriptionsCases(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "read",
			in:   `{"type":"object","properties":{"file_path":{"type":"string","description":"The absolute path to the file to read"},"offset":{"type":"integer","description":"Line number to start at","minimum":0,"maximum":9007199254740991},"limit":{"type":"integer","description":"Number of lines","exclusiveMinimum":0}},"required":["file_path"],"additionalProperties":false}`,
			want: `{"type":"object","properties":{"file_path":{"type":"string"},"offset":{"type":"integer","minimum":0,"maximum":9007199254740991},"limit":{"type":"integer","exclusiveMinimum":0}},"required":["file_path"],"additionalProperties":false}`,
		},
		{
			name: "description property name",
			in:   `{"type":"object","properties":{"description":{"type":"string","description":"A short description"},"prompt":{"type":"string","description":"The task"},"title":{"type":"string","description":"Property name collides"}},"required":["description","prompt"],"additionalProperties":false}`,
			want: `{"type":"object","properties":{"description":{"type":"string"},"prompt":{"type":"string"},"title":{"type":"string"}},"required":["description","prompt"],"additionalProperties":false}`,
		},
		{
			name: "bash",
			in:   `{"type":"object","properties":{"command":{"type":"string","description":"The command to execute"},"timeout":{"type":"number","description":"Optional timeout","maximum":600000},"run_in_background":{"type":"boolean","description":"Run async","default":false}},"required":["command"]}`,
			want: `{"type":"object","properties":{"command":{"type":"string"},"timeout":{"type":"number","maximum":600000},"run_in_background":{"type":"boolean"}},"required":["command"]}`,
		},
		{
			name: "edit",
			in:   `{"type":"object","properties":{"file_path":{"type":"string","description":"absolute path"},"old_string":{"type":"string","description":"text to replace"},"new_string":{"type":"string","description":"replacement"},"replace_all":{"type":"boolean","description":"Replace every occurrence","default":false}},"required":["file_path","old_string","new_string"]}`,
			want: `{"type":"object","properties":{"file_path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"},"replace_all":{"type":"boolean"}},"required":["file_path","old_string","new_string"]}`,
		},
		{
			name: "enum",
			in:   `{"type":"object","properties":{"status":{"type":"string","description":"Job status","enum":["pending","in_progress","completed","failed"]}},"required":["status"]}`,
			want: `{"type":"object","properties":{"status":{"type":"string","enum":["pending","in_progress","completed","failed"]}},"required":["status"]}`,
		},
		{
			name: "composition",
			in:   `{"type":"object","properties":{"identifier":{"description":"either an id or a name","oneOf":[{"type":"string","description":"name lookup","minLength":1},{"type":"integer","description":"numeric id","minimum":1}]},"filter":{"anyOf":[{"type":"string","description":"plain text"},{"type":"null"}]},"combo":{"allOf":[{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]},{"type":"object","properties":{"b":{"type":"number"}}}]}},"required":["identifier"]}`,
			want: `{"type":"object","properties":{"identifier":{"oneOf":[{"type":"string","minLength":1},{"type":"integer","minimum":1}]},"filter":{"anyOf":[{"type":"string"},{"type":"null"}]},"combo":{"allOf":[{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]},{"type":"object","properties":{"b":{"type":"number"}}}]}},"required":["identifier"]}`,
		},
		{
			name: "refs",
			in:   `{"type":"object","$defs":{"Loc":{"type":"object","description":"A 2D location","properties":{"lat":{"type":"number","description":"latitude"},"lng":{"type":"number","description":"longitude"}},"required":["lat","lng"]}},"properties":{"here":{"$ref":"#/$defs/Loc"},"there":{"$ref":"#/$defs/Loc"}},"required":["here"]}`,
			want: `{"type":"object","$defs":{"Loc":{"type":"object","properties":{"lat":{"type":"number"},"lng":{"type":"number"}},"required":["lat","lng"]}},"properties":{"here":{"$ref":"#/$defs/Loc"},"there":{"$ref":"#/$defs/Loc"}},"required":["here"]}`,
		},
		{
			name: "format",
			in:   `{"type":"object","properties":{"when":{"type":"string","format":"date-time"},"who":{"type":"string","format":"uri"},"freeform":{"type":"string","format":"a-very-long-format-string-that-is-prose"}}}`,
			want: `{"type":"object","properties":{"when":{"type":"string","format":"date-time"},"who":{"type":"string","format":"uri"},"freeform":{"type":"string"}}}`,
		},
		{
			name: "constraints",
			in:   `{"type":"object","properties":{"email":{"type":"string","description":"email address","pattern":"^[^@]+@[^@]+$","minLength":3,"maxLength":254},"tags":{"type":"array","description":"list of tags","uniqueItems":true,"minItems":0,"maxItems":10,"items":{"type":"string","minLength":1}}}}`,
			want: `{"type":"object","properties":{"email":{"type":"string","pattern":"^[^@]+@[^@]+$","minLength":3,"maxLength":254},"tags":{"type":"array","uniqueItems":true,"minItems":0,"maxItems":10,"items":{"type":"string","minLength":1}}}}`,
		},
		{
			name: "boolean additionalProperties false",
			in:   `{"type":"object","properties":{"a":{"type":"string"}},"additionalProperties":false}`,
			want: `{"type":"object","properties":{"a":{"type":"string"}},"additionalProperties":false}`,
		},
		{
			name: "root oneOf",
			in:   `{"oneOf":[{"type":"object","properties":{"name":{"type":"string","description":"display name"}},"required":["name"]},{"type":"object","properties":{"id":{"type":"integer","description":"numeric id"}},"required":["id"]}]}`,
			want: `{"oneOf":[{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]},{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := StripSchemaDescriptions(mustJSON(tc.in))
			if !changed && tc.in != tc.want {
				t.Fatal("changed=false for changed schema")
			}
			want := mustJSON(tc.want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %#v\nwant %#v", got, want)
			}
		})
	}
}

func TestStripSchemaDepthCapAndStructure(t *testing.T) {
	deep := map[string]any{"type": "string", "description": "leaf"}
	cur := deep
	for i := 0; i < 25; i++ {
		cur = map[string]any{
			"type":        "object",
			"description": "level",
			"properties":  map[string]any{"next": cur},
		}
	}
	gotAny, _ := StripSchemaDescriptions(cur)
	node := gotAny.(map[string]any)
	for i := 0; i < 20; i++ {
		props := node["properties"].(map[string]any)
		node = props["next"].(map[string]any)
	}
	seenDescriptionBelowCap := false
	for {
		if _, ok := node["description"].(string); ok {
			seenDescriptionBelowCap = true
			break
		}
		props, ok := node["properties"].(map[string]any)
		if !ok {
			break
		}
		next, ok := props["next"].(map[string]any)
		if !ok {
			break
		}
		node = next
	}
	if !seenDescriptionBelowCap {
		t.Fatal("deep nodes below cap lost description")
	}
	if !SchemaHasStructure(map[string]any{"properties": map[string]any{}}) {
		t.Fatal("schema structure not detected")
	}
	if SchemaHasStructure(map[string]any{"description": "only prose"}) {
		t.Fatal("annotation-only schema reported structure")
	}
}

func mustJSON(s string) map[string]any {
	var out map[string]any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		panic(err)
	}
	return out
}
