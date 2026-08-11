package compressors

import (
	"reflect"
	"testing"
)

func TestDecodeTOONFailsClosedOnImpossibleCountsAndDuplicateKeys(t *testing.T) {
	for _, input := range []string{
		"rows[1000000000]{id}:\n  1",
		"rows[1]{id,id}:\n  1,2",
		"name: first\nname: second",
	} {
		if value, ok := DecodeTOON([]byte(input)); ok {
			t.Fatalf("accepted malformed TOON %q as %#v", input, value)
		}
	}
}

func TestParseJSONTOONRejectsDuplicateKeys(t *testing.T) {
	for _, input := range []string{
		`{"id":0,"id":""}`,
		`{"rows":[{"id":0,"id":""}]}`,
		`{"outer":{"id":0,"id":""}}`,
	} {
		if value, ok := parseJSONTOON([]byte(input)); ok {
			t.Fatalf("accepted ambiguous JSON %q as %#v", input, value)
		}
	}
}

func FuzzTOONRoundTrip(f *testing.F) {
	for _, seed := range []string{
		`{"rows":[{"id":1,"name":"ana"},{"id":2,"name":"luis"}]}`,
		`{"values":["a,b","true","123",""," leading"]}`,
		`[{"id":1,"ok":true},{"id":2,"ok":false}]`,
		`{"nested":{"tags":["x","y"],"empty":{}}}`,
		`42`,
		`"hello"`,
		`":"`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		input := []byte(raw)
		v, ok := parseJSONTOON(input)
		if !ok {
			return
		}
		out, ok := encodeTOON(v, EncodeOptions{})
		if !ok {
			return
		}
		got, ok := DecodeTOON(out)
		if !ok {
			t.Fatalf("decode failed for TOON:\n%s", out)
		}
		want, ok := canonicalJSONValue(input)
		if !ok {
			t.Fatalf("canonical failed for JSON: %s", input)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round-trip mismatch\nwant=%#v\ngot=%#v\ntoon:\n%s", want, got, out)
		}
	})
}

func FuzzTOONDecode(f *testing.F) {
	for _, seed := range []string{
		"rows[2]{id,name}:\n  1,ana\n  2,sam",
		"rows[1000000000]{id}:\n  1",
		"name: first\nname: second",
		"values[3]: true,null,42",
		"\"root scalar\"",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(_ *testing.T, raw string) {
		_, _ = DecodeTOON([]byte(raw))
	})
}
