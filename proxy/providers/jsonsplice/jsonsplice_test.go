package jsonsplice_test

import (
	"bytes"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

func TestRootAcceptsOnlyCompleteObjects(t *testing.T) {
	tests := []struct {
		name string
		body string
		ok   bool
	}{
		{"object with whitespace", " \n {\"a\":1}\t", true},
		{"nested object", `{"a":[{"b":"brace } [ remains text"}]}`, true},
		{"array", `[]`, false},
		{"scalar", `1`, false},
		{"trailing value", `{} []`, false},
		{"malformed", `{"a":`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			span, ok := jsonsplice.Root([]byte(tc.body))
			if ok != tc.ok {
				t.Fatalf("Root(%q) ok=%v, want %v", tc.body, ok, tc.ok)
			}
			if ok && string([]byte(tc.body)[span.Start:span.End]) != stringsTrimSpace(tc.body) {
				t.Fatalf("root span = %q", []byte(tc.body)[span.Start:span.End])
			}
		})
	}
}

func TestFieldElementsAndStringsNavigateWithoutReencoding(t *testing.T) {
	body := []byte(`{"escaped\u005fkey":"line\nvalue","items":[1,{"name":"second"},["nested"]],"flag":true}`)
	root, ok := jsonsplice.Root(body)
	if !ok {
		t.Fatal("root not found")
	}
	if got, ok := jsonsplice.StringField(body, root, "escaped_key"); !ok || got != "line\nvalue" {
		t.Fatalf("decoded string field = %q, %v", got, ok)
	}
	items, ok := jsonsplice.Field(body, root, "items")
	if !ok {
		t.Fatal("items not found")
	}
	elements, ok := jsonsplice.Elements(body, items)
	if !ok || len(elements) != 3 {
		t.Fatalf("elements = %+v, %v", elements, ok)
	}
	if string(body[elements[0].Start:elements[0].End]) != "1" ||
		string(body[elements[1].Start:elements[1].End]) != `{"name":"second"}` ||
		string(body[elements[2].Start:elements[2].End]) != `["nested"]` {
		t.Fatalf("element spans changed bytes: %q %q %q",
			body[elements[0].Start:elements[0].End],
			body[elements[1].Start:elements[1].End],
			body[elements[2].Start:elements[2].End])
	}
	secondName, ok := jsonsplice.StringField(body, elements[1], "name")
	if !ok || secondName != "second" {
		t.Fatalf("nested string field = %q, %v", secondName, ok)
	}
	if _, ok := jsonsplice.Field(body, root, "missing"); ok {
		t.Fatal("missing field reported present")
	}
	if _, ok := jsonsplice.StringField(body, root, "flag"); ok {
		t.Fatal("non-string field decoded as string")
	}
	if empty, ok := jsonsplice.Elements([]byte(`[]`), jsonsplice.Span{Start: 0, End: 2}); !ok || len(empty) != 0 {
		t.Fatalf("empty array = %+v, %v", empty, ok)
	}
}

func TestNavigationRejectsInvalidSpansAndMalformedContainers(t *testing.T) {
	body := []byte(`{"a":1}`)
	invalid := []jsonsplice.Span{
		{Start: -1, End: 2},
		{Start: 0, End: len(body) + 1},
		{Start: 2, End: 2},
		{Start: 1, End: len(body)},
	}
	for _, span := range invalid {
		if _, ok := jsonsplice.Field(body, span, "a"); ok {
			t.Errorf("Field accepted invalid span %+v", span)
		}
		if _, ok := jsonsplice.Elements(body, span); ok {
			t.Errorf("Elements accepted invalid span %+v", span)
		}
		if _, ok := jsonsplice.String(body, span); ok {
			t.Errorf("String accepted invalid span %+v", span)
		}
	}
	if _, ok := jsonsplice.Field([]byte(`{"a" 1}`), jsonsplice.Span{Start: 0, End: 7}, "a"); ok {
		t.Fatal("field parser accepted missing colon")
	}
	if _, ok := jsonsplice.Elements([]byte(`[{"a":1}`), jsonsplice.Span{Start: 0, End: 8}); ok {
		t.Fatal("elements parser accepted unterminated value")
	}
}

func TestReplaceQuotesChangedStringsAndPreservesEverythingElse(t *testing.T) {
	body := []byte(`{"a":"old","keep":"\u003c","b":"same"}`)
	root, _ := jsonsplice.Root(body)
	a, _ := jsonsplice.Field(body, root, "a")
	b, _ := jsonsplice.Field(body, root, "b")
	candidates := []jsonsplice.Candidate{
		{Span: a, Original: []byte("old")},
		{Span: b, Original: []byte("same")},
	}
	got, err := jsonsplice.Replace(body, candidates, [][]byte{[]byte("<new>\n"), []byte("same")})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":"<new>\n","keep":"\u003c","b":"same"}` {
		t.Fatalf("Replace output = %s", got)
	}

	unchanged, err := jsonsplice.Replace(body, candidates, [][]byte{nil, []byte("same")})
	if err != nil {
		t.Fatal(err)
	}
	if len(unchanged) > 0 && &unchanged[0] != &body[0] {
		t.Fatal("no-op replacement allocated or rewrote body")
	}
}

func TestReplaceRejectsCountAndRangeErrors(t *testing.T) {
	body := []byte(`{"a":"x","b":"y"}`)
	root, _ := jsonsplice.Root(body)
	a, _ := jsonsplice.Field(body, root, "a")
	b, _ := jsonsplice.Field(body, root, "b")
	if _, err := jsonsplice.Replace(body, []jsonsplice.Candidate{{Span: a}}, nil); err == nil {
		t.Fatal("mismatched replacement count accepted")
	}
	for _, candidates := range [][]jsonsplice.Candidate{
		{{Span: jsonsplice.Span{Start: -1, End: 2}}},
		{{Span: jsonsplice.Span{Start: 0, End: len(body) + 1}}},
		{{Span: jsonsplice.Span{Start: 2, End: 2}}},
		{{Span: b}, {Span: a}},
	} {
		replacements := make([][]byte, len(candidates))
		for i := range replacements {
			replacements[i] = []byte("changed")
		}
		if _, err := jsonsplice.Replace(body, candidates, replacements); err == nil {
			t.Fatalf("invalid candidates accepted: %+v", candidates)
		}
	}
}

func TestAppendObjectFieldsPreservesOriginalBytes(t *testing.T) {
	body := []byte("{\n  \"model\" : \"gpt-5.6\", \"messages\": [ {\"role\":\"user\",\"content\":\"<>&\"} ] \n}")
	root, ok := jsonsplice.Root(body)
	if !ok {
		t.Fatal("root not found")
	}
	got, err := jsonsplice.AppendObjectFields(body, root,
		jsonsplice.FieldInsertion{Name: "prompt_cache_key", Value: []byte(`"abc"`)},
		jsonsplice.FieldInsertion{Name: "reasoning_effort", Value: []byte(`"low"`)},
	)
	if err != nil {
		t.Fatal(err)
	}
	inserted := []byte(`,"prompt_cache_key":"abc","reasoning_effort":"low"`)
	want := bytes.Replace(body, []byte(" \n}"), append(inserted, ' ', '\n', '}'), 1)
	if !bytes.Equal(got, want) {
		t.Fatalf("raw insertion changed original bytes:\n got %s\nwant %s", got, want)
	}
}

func TestAppendObjectFieldsHandlesEmptyObject(t *testing.T) {
	body := []byte(`{  }`)
	root, ok := jsonsplice.Root(body)
	if !ok {
		t.Fatal("root not found")
	}
	got, err := jsonsplice.AppendObjectFields(body, root,
		jsonsplice.FieldInsertion{Name: "cache_control", Value: []byte(`{"type":"ephemeral"}`)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"cache_control":{"type":"ephemeral"}  }` {
		t.Fatalf("unexpected insertion: %s", got)
	}
}

func TestAppendObjectFieldsValidatesInputsAndNoOpPreservesBody(t *testing.T) {
	body := []byte(`{"a":1}`)
	root, _ := jsonsplice.Root(body)
	got, err := jsonsplice.AppendObjectFields(body, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 0 && &got[0] != &body[0] {
		t.Fatal("empty insertion allocated or rewrote body")
	}

	tests := []struct {
		name   string
		span   jsonsplice.Span
		fields []jsonsplice.FieldInsertion
	}{
		{"invalid object range", jsonsplice.Span{Start: 1, End: len(body)}, []jsonsplice.FieldInsertion{{Name: "b", Value: []byte("2")}}},
		{"empty name", root, []jsonsplice.FieldInsertion{{Value: []byte("2")}}},
		{"invalid JSON value", root, []jsonsplice.FieldInsertion{{Name: "b", Value: []byte(`{`)}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := jsonsplice.AppendObjectFields(body, tc.span, tc.fields...); err == nil {
				t.Fatal("invalid insertion accepted")
			}
		})
	}
}

func TestReplaceRawPreservesOutsideSpan(t *testing.T) {
	body := []byte(`{"system":"You \u003c exact","messages":[]}`)
	root, ok := jsonsplice.Root(body)
	if !ok {
		t.Fatal("root not found")
	}
	system, ok := jsonsplice.Field(body, root, "system")
	if !ok {
		t.Fatal("system not found")
	}
	replacement := append([]byte(`[{"type":"text","text":`), body[system.Start:system.End]...)
	replacement = append(replacement, []byte(`,"cache_control":{"type":"ephemeral"}}]`)...)
	got, err := jsonsplice.ReplaceRaw(body, system, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte(`"text":"You \u003c exact"`)) {
		t.Fatalf("original string spelling was not preserved: %s", got)
	}
	if !bytes.HasSuffix(got, []byte(`,"messages":[]}`)) {
		t.Fatalf("bytes after replaced span changed: %s", got)
	}
}

func TestReplaceRawRejectsInvalidRangeAndReplacement(t *testing.T) {
	body := []byte(`{"a":1}`)
	for _, span := range []jsonsplice.Span{
		{Start: -1, End: 2},
		{Start: 0, End: len(body) + 1},
		{Start: 2, End: 2},
	} {
		if _, err := jsonsplice.ReplaceRaw(body, span, []byte("null")); err == nil {
			t.Fatalf("invalid span accepted: %+v", span)
		}
	}
	if _, err := jsonsplice.ReplaceRaw(body, jsonsplice.Span{Start: 5, End: 6}, []byte(`{`)); err == nil {
		t.Fatal("invalid JSON replacement accepted")
	}
}

func stringsTrimSpace(value string) string {
	return string(bytes.TrimSpace([]byte(value)))
}
