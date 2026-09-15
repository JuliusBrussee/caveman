package compressors_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/compressors"
	"github.com/JuliusBrussee/caveman/engine/safety"
)

func TestStripToolSchemaAnnotations(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{
			name: "drops the four annotation keywords inside a schema",
			in:   `[{"name":"Read","input_schema":{"$schema":"https://json-schema.org/draft/2020-12/schema","title":"Read args","deprecated":false,"examples":[{"path":"a.go"}],"type":"object"}}]`,
			want: `[{"name":"Read","input_schema":{"type":"object"}}]`,
			ok:   true,
		},
		{
			name: "keeps description untouched",
			in:   `[{"name":"Read","description":"Read a file. Returns its contents.","input_schema":{"description":"args","title":"x"}}]`,
			want: `[{"name":"Read","description":"Read a file. Returns its contents.","input_schema":{"description":"args"}}]`,
			ok:   true,
		},
		{
			name: "envelope fields are never edited",
			in:   `[{"name":"Read","title":"Read a file","deprecated":true,"examples":["read a.go"],"input_schema":{"title":"drop"}}]`,
			want: `[{"name":"Read","title":"Read a file","deprecated":true,"examples":["read a.go"],"input_schema":{}}]`,
			ok:   true,
		},
		{
			name: "MCP annotations.title is a tool name, not metadata",
			in:   `[{"name":"Read","annotations":{"title":"Read a file","readOnlyHint":true},"inputSchema":{"title":"drop","type":"object"}}]`,
			want: `[{"name":"Read","annotations":{"title":"Read a file","readOnlyHint":true},"inputSchema":{"type":"object"}}]`,
			ok:   true,
		},
		{
			name: "object-valued vendor extension on the envelope is untouched",
			in:   `[{"name":"Read","x-vendor":{"title":"vendor label","examples":[1],"nested":{"deprecated":true}},"_meta":{"title":"meta"},"input_schema":{"title":"drop"}}]`,
			want: `[{"name":"Read","x-vendor":{"title":"vendor label","examples":[1],"nested":{"deprecated":true}},"_meta":{"title":"meta"},"input_schema":{}}]`,
			ok:   true,
		},
		{
			name: "object-valued unknown keyword INSIDE a schema is untouched",
			in:   `[{"name":"Read","input_schema":{"type":"object","x-ui":{"title":"Widget","examples":[1]},"$comment":"kept","title":"drop"}}]`,
			want: `[{"name":"Read","input_schema":{"type":"object","x-ui":{"title":"Widget","examples":[1]},"$comment":"kept"}}]`,
			ok:   true,
		},
		{
			name: "openai function envelope is entered, its siblings are not",
			in:   `[{"type":"function","title":"envelope","function":{"name":"read","description":"d","strict":true,"parameters":{"title":"drop","type":"object"}}}]`,
			want: `[{"type":"function","title":"envelope","function":{"name":"read","description":"d","strict":true,"parameters":{"type":"object"}}}]`,
			ok:   true,
		},
		{
			name: "property named title survives",
			in:   `[{"name":"Write","input_schema":{"type":"object","title":"Write args","properties":{"title":{"type":"string","title":"The title"},"examples":{"type":"array","examples":[1]}},"required":["title"]}}]`,
			want: `[{"name":"Write","input_schema":{"type":"object","properties":{"title":{"type":"string"},"examples":{"type":"array"}},"required":["title"]}}]`,
			ok:   true,
		},
		{
			name: "definition named title survives with its $ref",
			in:   `[{"name":"T","input_schema":{"$defs":{"title":{"type":"string","title":"x"}},"properties":{"a":{"$ref":"#/$defs/title"}}}}]`,
			want: `[{"name":"T","input_schema":{"$defs":{"title":{"type":"string"}},"properties":{"a":{"$ref":"#/$defs/title"}}}}]`,
			ok:   true,
		},
		{
			name: "enum, const and default instance data are never walked",
			in:   `[{"name":"T","input_schema":{"properties":{"mode":{"enum":[{"title":"keep"},"deprecated"],"title":"Mode"},"a":{"const":{"title":"c"}},"b":{"default":{"examples":[1]}}}}}]`,
			want: `[{"name":"T","input_schema":{"properties":{"mode":{"enum":[{"title":"keep"},"deprecated"]},"a":{"const":{"title":"c"}},"b":{"default":{"examples":[1]}}}}}]`,
			ok:   true,
		},
		{
			name: "applicator keywords are descended",
			in:   `[{"name":"T","input_schema":{"anyOf":[{"title":"a","type":"string"},{"title":"b"}],"items":{"title":"i","type":"number"},"not":{"examples":[1]}}}]`,
			want: `[{"name":"T","input_schema":{"anyOf":[{"type":"string"},{}],"items":{"type":"number"},"not":{}}}]`,
			ok:   true,
		},
		{
			name: "cache_control on the last tool is preserved byte-for-byte",
			in:   `[{"name":"A","input_schema":{"title":"a"}},{"name":"B","input_schema":{"title":"b"},"cache_control":{"type":"ephemeral"}}]`,
			want: `[{"name":"A","input_schema":{}},{"name":"B","input_schema":{},"cache_control":{"type":"ephemeral"}}]`,
			ok:   true,
		},
		{
			name: "first member dropped",
			in:   `[{"name":"A","input_schema":{"title":"a","type":"object"}}]`,
			want: `[{"name":"A","input_schema":{"type":"object"}}]`,
			ok:   true,
		},
		{
			name: "every member dropped leaves an empty object",
			in:   `[{"name":"A","input_schema":{"title":"a","examples":[1]}}]`,
			want: `[{"name":"A","input_schema":{}}]`,
			ok:   true,
		},
		{
			// A catalog may spell "title" with a Unicode escape; a raw byte search
			// would miss it and leave the annotation on the wire.
			name: "escaped key spelling is still recognized",
			in:   "[{\"name\":\"A\",\"input_schema\":{\"\\u0074itle\":\"a\",\"type\":\"object\"}}]",
			want: `[{"name":"A","input_schema":{"type":"object"}}]`,
			ok:   true,
		},
		{
			name: "nothing to strip returns the input unchanged",
			in:   `[{"name":"A","description":"d","input_schema":{"type":"object"}}]`,
			want: `[{"name":"A","description":"d","input_schema":{"type":"object"}}]`,
			ok:   true,
		},
		{
			name: "malformed JSON passes through",
			in:   `[{"name":"A",`,
			want: `[{"name":"A",`,
			ok:   false,
		},
		{
			name: "trailing garbage passes through",
			in:   `[{"name":"A"}] {`,
			want: `[{"name":"A"}] {`,
			ok:   false,
		},
		{
			name: "empty input passes through",
			in:   ``,
			want: ``,
			ok:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, ok := compressors.StripToolSchemaAnnotations([]byte(tc.in))
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if string(out) != tc.want {
				t.Fatalf("out = %s\nwant = %s", out, tc.want)
			}
			if !tc.ok {
				return
			}
			if !json.Valid(out) {
				t.Fatalf("output is not valid JSON: %s", out)
			}
			again, ok := compressors.StripToolSchemaAnnotations(out)
			if !ok || !bytes.Equal(again, out) {
				t.Fatalf("not idempotent: ok=%v second=%s first=%s", ok, again, out)
			}
		})
	}
}

// TestStripToolSchemaAnnotationsPreservesOrderAndSpacing pins the minimal-splice
// property: only the dropped members move, and every other byte — element order,
// indentation, number spelling, string escapes — is copied through untouched.
func TestStripToolSchemaAnnotationsPreservesOrderAndSpacing(t *testing.T) {
	in := "[\n  { \"name\" : \"First\" ,\n    \"input_schema\" : { \"type\" : \"object\" ,\n      \"title\" : \"drop me\" ,\n      \"maxItems\" : 1.50 } } ,\n" +
		"  { \"name\" : \"Second\" , \"description\" : \"tab\\there\" }\n]"
	want := "[\n  { \"name\" : \"First\" ,\n    \"input_schema\" : { \"type\" : \"object\" ,\n      \"maxItems\" : 1.50 } } ,\n" +
		"  { \"name\" : \"Second\" , \"description\" : \"tab\\there\" }\n]"
	out, ok := compressors.StripToolSchemaAnnotations([]byte(in))
	if !ok {
		t.Fatal("strip declined a well-formed catalog")
	}
	if string(out) != want {
		t.Fatalf("out = %q\nwant = %q", out, want)
	}
	var tools []map[string]any
	if err := json.Unmarshal(out, &tools); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools {
		names = append(names, tool["name"].(string))
	}
	if strings.Join(names, ",") != "First,Second" {
		t.Fatalf("tool order changed: %v", names)
	}
}

// TestStripToolSchemaAnnotationsIsDeterministic pins the cache-stability property
// the proxy relies on: the same catalog bytes always produce the same output
// bytes, so a stripped prefix stays byte-identical on every later turn.
func TestStripToolSchemaAnnotationsIsDeterministic(t *testing.T) {
	in := []byte(`[{"name":"A","input_schema":{"title":"a","properties":{"p":{"title":"t","type":"string"}}}},{"name":"B","input_schema":{"examples":[1,2]},"cache_control":{"type":"ephemeral"}}]`)
	first, ok := compressors.StripToolSchemaAnnotations(in)
	if !ok {
		t.Fatal("strip declined a well-formed catalog")
	}
	for i := 0; i < 8; i++ {
		next, ok := compressors.StripToolSchemaAnnotations(in)
		if !ok || !bytes.Equal(next, first) {
			t.Fatalf("run %d diverged: ok=%v out=%s", i, ok, next)
		}
	}
}

func TestToolSchemaAnnotationsCompressorIsForcedOnlyAndS4(t *testing.T) {
	c, ok := compressors.Default().For("toolschema-annotations")
	if !ok {
		t.Fatal("toolschema-annotations is not registered")
	}
	if c.ContentType() != "toolschema-annotations" {
		t.Fatalf("content type = %q", c.ContentType())
	}
	if c.SafetyClass() != safety.S4 {
		t.Fatalf("safety class = %v, want S4", c.SafetyClass())
	}
	info, found := safety.Lookup(c.SafetyClass())
	if !found || info.ByteSafe || !info.RequiresCCR {
		t.Fatalf("class contract broken: %+v found=%v", info, found)
	}
	out, ok := c.Compress([]byte(`[{"name":"A","input_schema":{"title":"a","type":"object"}}]`))
	if !ok || string(out) != `[{"name":"A","input_schema":{"type":"object"}}]` {
		t.Fatalf("Compress = %s ok=%v", out, ok)
	}
	if _, bad := c.Compress([]byte(`{`)); bad {
		t.Fatal("malformed input must report !ok")
	}
}
