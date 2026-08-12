package compressors

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestTOONCompressorTabularArrayRoundTrips(t *testing.T) {
	c := NewTOON()
	in := []byte(`{"hikes":[{"id":1,"name":"Blue Lake","distanceKm":7.5,"sunny":true},{"id":2,"name":"Ridge","distanceKm":9.2,"sunny":false},{"id":3,"name":"Loop","distanceKm":5.1,"sunny":true}],"friends":["ana","luis","sam"],"meta":{"season":"spring_2025"}}`)

	out, ok := c.Compress(in)
	if !ok {
		t.Fatal("expected TOON compression")
	}
	if !bytes.Contains(out, []byte("hikes[3]{id,name,distanceKm,sunny}:")) {
		t.Fatalf("expected tabular header in:\n%s", out)
	}
	if len(out) >= len(in) {
		t.Fatalf("expected fewer bytes than JSON, got %d >= %d:\n%s", len(out), len(in), out)
	}
	assertTOONRoundTrip(t, in, out)
}

func TestTOONCompressorUnsupportedShapesPassThrough(t *testing.T) {
	c := NewTOON()
	cases := [][]byte{
		[]byte(`{"rows":[{"id":1,"name":"a"},{"id":2,"label":"b"}]}`),
		[]byte(`{"rows":[{"id":1,"meta":{"nested":true}},{"id":2,"meta":{"nested":false}}]}`),
		[]byte(`{"bad":[1,2,`),
	}
	for _, in := range cases {
		if out, ok := c.Compress(in); ok {
			t.Fatalf("unsupported input compressed to %s", out)
		}
	}
}

func TestTOONStringsNeedingQuotesRoundTrip(t *testing.T) {
	c := NewTOON()
	in := []byte(`{"rows":[{"id":1,"value":"a,b"},{"id":2,"value":"true"},{"id":3,"value":"123"},{"id":4,"value":""},{"id":5,"value":" leading"},{"id":6,"value":"line\nbreak"},{"id":7,"value":"a\"b"}]}`)

	out, ok := c.Compress(in)
	if !ok {
		t.Fatal("expected compression")
	}
	for _, want := range []string{`"a,b"`, `"true"`, `"123"`, `""`, `" leading"`, `"line\nbreak"`, `"a\"b"`} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("output missing quoted %s:\n%s", want, out)
		}
	}
	assertTOONRoundTrip(t, in, out)
}

func TestTOONNumbersPreserveLexicalForm(t *testing.T) {
	c := NewTOON()
	in := []byte(`{"rows":[{"id":1,"n":7.50},{"id":2,"n":1e9},{"id":3,"n":123456789012345678901234567890}]}`)

	out, ok := c.Compress(in)
	if !ok {
		t.Fatal("expected compression")
	}
	for _, want := range []string{"7.50", "1e9", "123456789012345678901234567890"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Fatalf("output missing number %s:\n%s", want, out)
		}
	}
	assertTOONRoundTrip(t, in, out)
}

func TestTOONDeterministic(t *testing.T) {
	c := NewTOON()
	in := []byte(`{"rows":[{"id":1,"name":"a"},{"id":2,"name":"b"}],"tags":["x","y"]}`)
	first, ok := c.Compress(in)
	if !ok {
		t.Fatal("expected compression")
	}
	second, ok := c.Compress(in)
	if !ok {
		t.Fatal("expected compression")
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("TOON output not deterministic:\nfirst=%s\nsecond=%s", first, second)
	}
}

func TestTOONSortsNonTabularObjectKeys(t *testing.T) {
	a, ok := NewTOON().Compress([]byte(`{"b":1,"a":2,"nested":{"z":3,"c":4}}`))
	if !ok {
		t.Fatal("expected compression")
	}
	b, ok := NewTOON().Compress([]byte(`{"a":2,"b":1,"nested":{"c":4,"z":3}}`))
	if !ok {
		t.Fatal("expected compression")
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("semantic object key order should not affect TOON:\na=%s\nb=%s", a, b)
	}
	if !strings.HasPrefix(string(a), "a: 2\nb: 1\nnested:\n  c: 4\n  z: 3") {
		t.Fatalf("expected sorted keys, got:\n%s", a)
	}
}

func TestTOONRootScalarAndScalarArrayRoundTrip(t *testing.T) {
	for _, in := range [][]byte{
		[]byte(`["ana","luis","sam"]`),
		[]byte(`"plain"`),
		[]byte(`42`),
		[]byte(`{}`),
		[]byte(`[]`),
	} {
		v, ok := parseJSONTOON(in)
		if !ok {
			t.Fatalf("parse json: %s", in)
		}
		out, ok := encodeTOON(v, EncodeOptions{})
		if !ok {
			t.Fatalf("encode TOON: %s", in)
		}
		if string(in) == `[]` && string(out) != "[]" {
			t.Fatalf("root empty array encoded as %q, want []", out)
		}
		assertTOONRoundTrip(t, in, out)
	}
}

func assertTOONRoundTrip(t *testing.T, input, toon []byte) {
	t.Helper()
	want, ok := canonicalJSONValue(input)
	if !ok {
		t.Fatalf("canonical input failed: %s", input)
	}
	got, ok := DecodeTOON(toon)
	if !ok {
		t.Fatalf("decode TOON failed:\n%s", toon)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip mismatch\nwant=%#v\ngot=%#v\ntoon:\n%s", want, got, toon)
	}
}
