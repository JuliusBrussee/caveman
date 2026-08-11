package compressors

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// a verbose MCP-style tool catalog with long descriptions, annotation metadata,
// an enum, a required list, and — critically — a parameter literally named
// "description" that must survive (it is a property name, not metadata).
const sampleCatalog = `{
  "tools": [
    {
      "name": "search_files",
      "description": "Search the workspace for files. This tool walks the entire tree, honours .gitignore, and returns ranked matches with surrounding context lines so the agent can decide which file to open next.",
      "inputSchema": {
        "type": "object",
        "title": "SearchFilesArgs",
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "properties": {
          "query": {
            "type": "string",
            "description": "The search query string to match against file contents using ranked relevance.",
            "examples": ["needle", "func main"]
          },
          "mode": {
            "type": "string",
            "enum": ["regex", "literal", "glob"],
            "default": "literal",
            "description": "How to interpret the query."
          },
          "description": {
            "type": "string",
            "description": "A free-text note the caller may attach to the search for logging."
          }
        },
        "required": ["query"]
      }
    }
  ]
}`

func TestToolSchemaPreservesSelectionTokens(t *testing.T) {
	c := NewToolSchema()
	out, ok := c.Compress([]byte(sampleCatalog))
	if !ok {
		t.Fatal("expected ok=true on a valid catalog")
	}
	if !json.Valid(out) {
		t.Fatal("output is not valid JSON")
	}
	if len(out) >= len(sampleCatalog) {
		t.Fatalf("expected smaller output, got %d >= %d", len(out), len(sampleCatalog))
	}

	// Selectability invariant: names, enum values, and required must survive.
	for _, must := range []string{
		`"search_files"`,                 // tool name
		`"query"`,                        // parameter name
		`"mode"`,                         // parameter name
		`"regex"`, `"literal"`, `"glob"`, // enum values
		`"required"`,
	} {
		if !bytes.Contains(out, []byte(must)) {
			t.Errorf("selection token %s was dropped", must)
		}
	}

	// Annotation metadata must be dropped.
	for _, gone := range []string{`"examples"`, `"title"`, `"$schema"`, `"func main"`, `"SearchFilesArgs"`} {
		if bytes.Contains(out, []byte(gone)) {
			t.Errorf("metadata %s should have been dropped", gone)
		}
	}

	// Tool description reduction remains intentional; envelope metadata follows
	// a separate path so schema-only keys such as title are not dropped from it.
	if bytes.Contains(out, []byte("honours .gitignore")) {
		t.Error("long tool description tail should have been truncated")
	}
}

func TestToolSchemaPreservesEnvelopeAndMCPAnnotationTitle(t *testing.T) {
	input := []byte(`{"tools":[{"name":"Read","description":"Read one file with all provider-visible selection detail intact.","annotations":{"title":"Read a file","readOnlyHint":true},"_meta":{"title":"vendor title","examples":[1]},"inputSchema":{"title":"ReadArgs","type":"object","properties":{"path":{"type":"string","title":"Path","description":"Target path."}}}}],"title":"catalog title"}`)
	out, ok := NewToolSchema().Compress(input)
	if !ok {
		t.Fatal("expected ok=true")
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got["title"] != "catalog title" {
		t.Fatalf("catalog envelope title changed: %s", out)
	}
	tools := got["tools"].([]any)
	tool := tools[0].(map[string]any)
	annotations := tool["annotations"].(map[string]any)
	if annotations["title"] != "Read a file" || annotations["readOnlyHint"] != true {
		t.Fatalf("MCP annotations changed: %s", out)
	}
	if tool["description"] != "Read one file with all provider-visible selection detail intact." {
		t.Fatalf("tool description changed: %s", out)
	}
	meta := tool["_meta"].(map[string]any)
	if meta["title"] != "vendor title" {
		t.Fatalf("vendor metadata changed: %s", out)
	}
	schema := tool["inputSchema"].(map[string]any)
	if _, present := schema["title"]; present {
		t.Fatalf("schema title was not removed: %s", out)
	}
	path := schema["properties"].(map[string]any)["path"].(map[string]any)
	if _, present := path["title"]; present {
		t.Fatalf("nested schema title was not removed: %s", out)
	}
}

func TestToolSchemaPropertyNamedDescriptionSurvives(t *testing.T) {
	c := NewToolSchema()
	out, ok := c.Compress([]byte(sampleCatalog))
	if !ok {
		t.Fatal("expected ok=true")
	}
	// The parameter literally named "description" lives inside properties; its
	// key must survive even though "description" is also a metadata key name.
	var parsed struct {
		Tools []struct {
			InputSchema struct {
				Properties map[string]any `json:"properties"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	props := parsed.Tools[0].InputSchema.Properties
	if _, present := props["description"]; !present {
		t.Fatal("the user-defined 'description' parameter was wrongly dropped as metadata")
	}
	if _, present := props["query"]; !present {
		t.Fatal("the 'query' parameter was dropped")
	}
}

func TestToolSchemaDependencyPropertyNamesSurvive(t *testing.T) {
	input := []byte(`{"type":"object","dependentSchemas":{"title":{"properties":{"example":{"type":"string"}}},"$comment":{"required":["title"]}},"dependentRequired":{"example":["title"],"$comment":["example"]},"dependencies":{"title":{"properties":{"$comment":{"type":"boolean"}}},"example":["title"]}}`)
	out, ok := NewToolSchema().Compress(input)
	if !ok {
		t.Fatal("expected ok=true")
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	for _, keyword := range []string{"dependentSchemas", "dependentRequired", "dependencies"} {
		entries, ok := got[keyword].(map[string]any)
		if !ok {
			t.Fatalf("%s changed shape: %s", keyword, out)
		}
		for _, property := range []string{"title", "example", "$comment"} {
			if _, expected := map[string]bool{
				"dependentSchemas:title": true, "dependentSchemas:$comment": true,
				"dependentRequired:example": true, "dependentRequired:$comment": true,
				"dependencies:title": true, "dependencies:example": true,
			}[keyword+":"+property]; expected {
				if _, present := entries[property]; !present {
					t.Fatalf("property name %q dropped under %s: %s", property, keyword, out)
				}
			}
		}
	}
}

func TestToolSchemaByteSafeOnMalformed(t *testing.T) {
	c := NewToolSchema()
	if _, ok := c.Compress([]byte(`{not valid json`)); ok {
		t.Fatal("expected ok=false on malformed JSON (caller forwards original)")
	}
	if _, ok := c.Compress([]byte(`{} {}`)); ok {
		t.Fatal("expected ok=false on multiple JSON values")
	}
}

func TestToolSchemaIdempotent(t *testing.T) {
	c := NewToolSchema()
	once, ok := c.Compress([]byte(sampleCatalog))
	if !ok {
		t.Fatal("first compress failed")
	}
	twice, ok := c.Compress(once)
	if !ok {
		t.Fatal("second compress failed")
	}
	if !bytes.Equal(once, twice) {
		t.Errorf("not idempotent:\n once=%s\ntwice=%s", once, twice)
	}
}

// TestToolSchemaDescriptionTruncationStaysUTF8 guards UTF-8 safety: the byte-index
// cut must back off to a rune boundary so no U+FFFD replacement character reaches
// the model.
//
// This test has TEETH where the raw-byte / utf8.Valid checks did not. json.Marshal
// does not emit the raw EF BF BD bytes for an invalid-UTF-8 Go string — it escapes
// the replacement rune to the ASCII sequence �, so `bytes.Contains(out, EFBFBD)`
// is false and `utf8.Valid(out)` is true even on the buggy parent (s[:maxDescLen]).
// The only reliable check is to DECODE the output back to Go strings — where �
// becomes an actual utf8.RuneError rune — and assert no string value contains it.
// The test fails when truncation splits a UTF-8 sequence and passes on capRunes.
func TestToolSchemaDescriptionTruncationStaysUTF8(t *testing.T) {
	c := NewToolSchema()
	// A description long enough to be reduced, whose byte cap falls inside the
	// multi-byte "é": 79 'a', then "é" (bytes 79–80), then filler.
	description := strings.Repeat("a", 79) + "é" + strings.Repeat("b", 200)
	input := []byte(`{"tools":[{"name":"t","inputSchema":{"type":"object","properties":{"p":{"type":"string","description":"` + description + `"}},"$defs":{"d":{"description":"` + description + `"}}}}]}`)
	out, ok := c.Compress(input)
	if !ok {
		t.Fatal("expected ok=true")
	}
	var parsed any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	forEachStringValue(parsed, func(s string) {
		if strings.ContainsRune(s, utf8.RuneError) {
			t.Fatalf("a compressed string value contains U+FFFD (mid-rune cut): %q", s)
		}
		if !utf8.ValidString(s) {
			t.Fatalf("a compressed string value is not valid UTF-8: %q", s)
		}
	})
}

// forEachStringValue walks decoded JSON and invokes fn on every string value,
// including nested $defs/definitions, so the UTF-8 assertion covers the whole
// model-visible catalog rather than one helper's return.
func forEachStringValue(v any, fn func(string)) {
	switch n := v.(type) {
	case string:
		fn(n)
	case map[string]any:
		for _, val := range n {
			forEachStringValue(val, fn)
		}
	case []any:
		for _, e := range n {
			forEachStringValue(e, fn)
		}
	}
}

// TestToolSchemaRetainsMarkerlessConstraints reproduces the round-2 review's
// under-keep cases and asserts each constraint SURVIVES compression — both the
// small-description path (kept whole, no eliding below the small budget) and a
// constraint buried in a genuinely large description (retained by its bound marker
// through eliding). Over-keeping is the safe direction; under-keeping is the bug.
func TestToolSchemaRetainsMarkerlessConstraints(t *testing.T) {
	c := NewToolSchema()
	cases := []struct {
		name string
		desc string
		want string // must appear verbatim in the compressed description
	}{
		{"small_bound_no_marker_word", "Sets the limit. Anything over 100 errors.", "Anything over 100 errors."},
		{"small_above_not_allowed", "Values above 100 are not allowed.", "Values above 100 are not allowed."},
		{"small_invalid", "Negative numbers are invalid.", "Negative numbers are invalid."},
		{
			"buried_in_large_desc",
			"This is a deliberately long lead sentence describing pagination behaviour that the server handles for you automatically without any further round trips or bookkeeping so it easily clears the small budget. Values above 100 are not allowed.",
			"Values above 100 are not allowed.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := []byte(`{"tools":[{"name":"t","inputSchema":{"type":"object","properties":{"p":{"type":"string","description":` + mustJSON(tc.desc) + `}}}}]}`)
			out, ok := c.Compress(input)
			if !ok {
				t.Fatal("expected ok=true")
			}
			if !bytes.Contains(out, []byte(tc.want)) {
				t.Fatalf("constraint %q was dropped; output=%s", tc.want, out)
			}
		})
	}
}

func mustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// TestToolSchemaAbbreviationNotSplit guards abbreviation boundaries: the description reducer
// must not cut inside an abbreviation. "Target path, e.g. /srv/data" must survive
// whole rather than collapsing to "Target path, e." — the old first-period cut.
func TestToolSchemaAbbreviationNotSplit(t *testing.T) {
	c := NewToolSchema()
	input := []byte(`{"tools":[{"name":"t","inputSchema":{"type":"object","properties":{"path":{"type":"string","description":"Target path, e.g. /srv/data."}}}}]}`)
	out, ok := c.Compress(input)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !bytes.Contains(out, []byte("Target path, e.g. /srv/data.")) {
		t.Fatalf("abbreviation was cut; output=%s", out)
	}
	if bytes.Contains(out, []byte(`"Target path, e."`)) {
		t.Fatal("description was cut inside the 'e.g.' abbreviation")
	}
}

// TestToolSchemaKeepsDefault guards default preservation: a `default` value is part of
// argument construction (an agent omits the argument to inherit it), so unlike
// pure annotation metadata it must NOT be dropped.
func TestToolSchemaKeepsDefault(t *testing.T) {
	c := NewToolSchema()
	input := []byte(`{"tools":[{"name":"t","inputSchema":{"type":"object","properties":{"mode":{"type":"string","enum":["a","b"],"default":"a","description":"Mode."}}}}]}`)
	out, ok := c.Compress(input)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !bytes.Contains(out, []byte(`"default":"a"`)) {
		t.Fatalf("default value was dropped; output=%s", out)
	}
}

// TestToolSchemaRefIntegrity guards reference integrity: a $defs / definitions entry whose
// name collides with a schema-metadata key must not be dropped, or every $ref that
// targets it dangles. It also asserts the general property that every $ref in the
// compressed output still resolves to an existing definition.
func TestToolSchemaRefIntegrity(t *testing.T) {
	c := NewToolSchema()
	input := []byte(`{"name":"t","inputSchema":{"type":"object","properties":{"x":{"$ref":"#/$defs/title"},"slash":{"$ref":"#/$defs/a~1b"},"tilde":{"$ref":"#/$defs/til~0de"}},"$defs":{"title":{"type":"string","enum":["a","b"]},"a/b":{"type":"number"},"til~de":{"type":"boolean"},"Widget":{"type":"object"}}}}`)
	out, ok := c.Compress(input)
	if !ok {
		t.Fatal("expected ok=true")
	}
	// The metadata-named definition and its selection surface must survive.
	if !bytes.Contains(out, []byte(`"title":{`)) {
		t.Fatalf("$defs.title (a metadata-named definition) was dropped; output=%s", out)
	}
	if !bytes.Contains(out, []byte(`"a","b"`)) {
		t.Fatalf("the enum on $defs.title was dropped with it; output=%s", out)
	}
	assertRefsResolve(t, out)
}

// assertRefsResolve walks parsed JSON and asserts every internal JSON-Pointer
// $ref resolves from its own schema-resource root. This is the $ref-integrity
// conformance guard the structural selection profile cannot provide (SelectionProfile
// never follows $ref), so the drop-a-definition class cannot pass silently again.
func assertRefsResolve(t *testing.T, out []byte) {
	t.Helper()
	var root any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if unresolved := unresolvedInternalRefs(root); len(unresolved) > 0 {
		t.Fatalf("dangling internal $refs after compression: %v", unresolved)
	}
}

var embeddedToolSchemaKeys = map[string]bool{
	"inputSchema":  true,
	"input_schema": true,
	"parameters":   true,
}

// unresolvedInternalRefs resolves each local JSON Pointer from the schema
// resource that contains it: an embedded tool schema or the nearest subschema
// carrying its own $id. It deliberately does not search sibling schemas; the
// same $defs path in another tool cannot repair a dangling ref here.
func unresolvedInternalRefs(document any) []string {
	var unresolved []string
	var walk func(node, resourceRoot any, keysAreUserDefined bool)
	walk = func(node, resourceRoot any, keysAreUserDefined bool) {
		switch n := node.(type) {
		case map[string]any:
			currentRoot := resourceRoot
			if !keysAreUserDefined {
				if id, ok := n["$id"].(string); ok && id != "" {
					currentRoot = n
				}
				if ref, ok := n["$ref"].(string); ok && (ref == "#" || strings.HasPrefix(ref, "#/")) {
					if !jsonPointerResolves(currentRoot, ref) {
						unresolved = append(unresolved, ref)
					}
				}
			}
			for key, value := range n {
				if key == "$ref" && !keysAreUserDefined {
					continue
				}
				if keysAreUserDefined {
					walk(value, currentRoot, false)
					continue
				}
				if embeddedToolSchemaKeys[key] {
					if _, ok := value.(map[string]any); ok {
						walk(value, value, false)
						continue
					}
				}
				walk(value, currentRoot, userDefinedKeys[key])
			}
		case []any:
			for _, value := range n {
				walk(value, resourceRoot, false)
			}
		}
	}
	walk(document, document, false)
	return unresolved
}

func jsonPointerResolves(root any, ref string) bool {
	if ref == "#" {
		return true
	}
	pointer, err := url.PathUnescape(strings.TrimPrefix(ref, "#"))
	if err != nil || !strings.HasPrefix(pointer, "/") {
		return false
	}
	segments := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	current := root
	for _, encoded := range segments {
		segment, ok := decodeJSONPointerToken(encoded)
		if !ok {
			return false
		}
		switch node := current.(type) {
		case map[string]any:
			var exists bool
			current, exists = node[segment]
			if !exists {
				return false
			}
		case []any:
			if segment == "" || (len(segment) > 1 && segment[0] == '0') {
				return false
			}
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || strconv.Itoa(index) != segment || index >= len(node) {
				return false
			}
			current = node[index]
		default:
			return false
		}
	}
	return true
}

func decodeJSONPointerToken(encoded string) (string, bool) {
	var decoded strings.Builder
	for index := 0; index < len(encoded); index++ {
		if encoded[index] != '~' {
			decoded.WriteByte(encoded[index])
			continue
		}
		if index+1 >= len(encoded) {
			return "", false
		}
		index++
		switch encoded[index] {
		case '0':
			decoded.WriteByte('~')
		case '1':
			decoded.WriteByte('/')
		default:
			return "", false
		}
	}
	return decoded.String(), true
}

// TestRefIntegrityGuardDetectsDrop checks that the $ref-integrity assertion catches:
// it must report a dropped definition as unresolvable and a present one as
// resolvable. Without this, TestToolSchemaRefIntegrity could pass vacuously.
func TestRefIntegrityGuardDetectsDrop(t *testing.T) {
	var root any
	if err := json.Unmarshal([]byte(`{"properties":{"x":{"$ref":"#/$defs/gone"}},"$defs":{"kept":{"type":"string"}}}`), &root); err != nil {
		t.Fatal(err)
	}
	if unresolved := unresolvedInternalRefs(root); len(unresolved) != 1 || unresolved[0] != "#/$defs/gone" {
		t.Fatalf("guard did not report dropped definition: %v", unresolved)
	}
	root = nil
	if err := json.Unmarshal([]byte(`{"properties":{"x":{"$ref":"#/$defs/kept"}},"$defs":{"kept":{"type":"string"}}}`), &root); err != nil {
		t.Fatal(err)
	}
	if unresolved := unresolvedInternalRefs(root); len(unresolved) != 0 {
		t.Fatalf("guard failed to resolve present definition: %v", unresolved)
	}
}

func TestRefIntegrityGuardDoesNotCrossToolSchemaRoots(t *testing.T) {
	var catalog any
	if err := json.Unmarshal([]byte(`{"tools":[{"name":"broken","inputSchema":{"properties":{"x":{"$ref":"#/$defs/shared"}},"$defs":{}}},{"name":"other","inputSchema":{"$defs":{"shared":{"type":"string"}}}}]}`), &catalog); err != nil {
		t.Fatal(err)
	}
	if unresolved := unresolvedInternalRefs(catalog); len(unresolved) != 1 || unresolved[0] != "#/$defs/shared" {
		t.Fatalf("sibling tool schema incorrectly satisfied dangling ref: %v", unresolved)
	}
}

func TestRefIntegrityGuardUsesNearestIDResourceRoot(t *testing.T) {
	var schema any
	if err := json.Unmarshal([]byte(`{"$defs":{"shared":{"type":"string"}},"properties":{"nested":{"$id":"nested.json","properties":{"x":{"$ref":"#/$defs/shared"}},"$defs":{}}}}`), &schema); err != nil {
		t.Fatal(err)
	}
	if unresolved := unresolvedInternalRefs(schema); len(unresolved) != 1 || unresolved[0] != "#/$defs/shared" {
		t.Fatalf("parent resource incorrectly satisfied nested $id ref: %v", unresolved)
	}
}

func TestRefIntegrityGuardRejectsNonCanonicalArrayIndex(t *testing.T) {
	var schema any
	if err := json.Unmarshal([]byte(`{"properties":{"x":{"$ref":"#/items/+0"}},"items":[{"type":"string"}]}`), &schema); err != nil {
		t.Fatal(err)
	}
	if unresolved := unresolvedInternalRefs(schema); len(unresolved) != 1 || unresolved[0] != "#/items/+0" {
		t.Fatalf("non-canonical array index incorrectly resolved: %v", unresolved)
	}
}

// TestToolSchemaPreservesArgumentConstraints is the call-VALIDITY conformance
// gate. A compressed catalog must not turn valid tool calls into invalid ones:
// every description sentence that states an argument constraint (RFC3339 format,
// mutual-exclusion "exactly one", absolute-path, required) survives, while pure
// filler prose is dropped. It pins the exact output against a golden file and
// separately asserts each individual constraint token, so a regression in either
// the split logic or the marker set fails loudly.
func TestToolSchemaPreservesArgumentConstraints(t *testing.T) {
	in, err := os.ReadFile("testdata/toolschema_constraints_catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	c := NewToolSchema()
	out, ok := c.Compress(in)
	if !ok {
		t.Fatal("expected ok=true on the constraints fixture")
	}
	if !utf8.Valid(out) {
		t.Fatal("compressor emitted invalid UTF-8")
	}

	// Golden regression: exact output (pretty-printed for a readable diff).
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, out, "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteByte('\n')
	golden, err := os.ReadFile("testdata/toolschema_constraints_catalog.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pretty.Bytes(), golden) {
		t.Fatalf("output drifted from golden.\n--- got ---\n%s\n--- want ---\n%s", pretty.String(), golden)
	}

	// Every constraint token must be present in the compressed catalog. These are
	// exactly the markers an agent needs to build a valid call; losing any one of
	// them yields an invalid argument and a retry loop that costs more than the
	// shrink saved.
	mustSurvive := []string{
		"must be an absolute path",         // absolute-path rule (large desc, elided)
		"exactly one of body",              // mutual exclusion (large desc, elided)
		"is required",                      // required-field rule
		"Rejected if body",                 // rejection rule
		"must be absolute",                 // second absolute rule
		"format must be RFC3339",           // RFC3339 timestamp constraint
		"are rejected",                     // timezone rejection
		"ISO 8601 duration",                // ISO 8601 constraint
		"Must match the P#DT",              // grammar constraint
		"must not be dropped",              // metadata-named $defs entry overlap
		`"default":"overwrite"`,            // default value kept, not dropped
		"Anything over 100 errors",         // marker-less bound in a SMALL desc (kept whole)
		"Values above 100 are not allowed", // bound constraint retained through ELISION
		"negative numbers are invalid",     // second bound retained through elision
	}
	for _, m := range mustSurvive {
		if !bytes.Contains(out, []byte(m)) {
			t.Errorf("constraint token %q did not survive compression", m)
		}
	}

	// Pure filler prose (only ever dropped from genuinely large descriptions) must go.
	for _, gone := range []string{
		"carries no rule and should be dropped", // write_file top-desc filler
		"paginates for you automatically",       // count lead filler past the cap
	} {
		if bytes.Contains(out, []byte(gone)) {
			t.Errorf("filler %q should have been dropped", gone)
		}
	}

	// $ref integrity across the whole fixture (the ISO8601Duration $ref must resolve).
	assertRefsResolve(t, out)

	// Idempotent: compressing the output again is a no-op.
	twice, ok := c.Compress(out)
	if !ok || !bytes.Equal(out, twice) {
		t.Fatalf("not idempotent on the constraints fixture")
	}
}
