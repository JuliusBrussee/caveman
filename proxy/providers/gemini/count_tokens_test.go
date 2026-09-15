package gemini

import (
	"os"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func TestCountTokensRequestGolden(t *testing.T) {
	original := readCountFixture(t, "testdata/count_tokens_input.json")
	want := readCountFixture(t, "testdata/count_tokens_expected.json")

	route, body, ok := Adapter{}.CountTokensRequest(original, providers.RequestMetadata{
		Model:    "gemini-3.6-flash",
		Endpoint: "generateContent",
	})
	if !ok {
		t.Fatal("CountTokensRequest rejected valid GenerateContent request")
	}
	if route != "/v1beta/models/gemini-3.6-flash:countTokens" {
		t.Fatalf("route=%q", route)
	}
	if string(body) != string(want) {
		t.Fatalf("projected body mismatch\n got: %s\nwant: %s", body, want)
	}
}

func TestCountTokensRequestFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		meta providers.RequestMetadata
	}{
		{"malformed", "{", providers.RequestMetadata{Model: "gemini-3.6-flash", Endpoint: "generateContent"}},
		{"missing contents", `{}`, providers.RequestMetadata{Model: "gemini-3.6-flash", Endpoint: "generateContent"}},
		{"missing model", `{"contents":[]}`, providers.RequestMetadata{Endpoint: "generateContent"}},
		{"wrong endpoint", `{"contents":[]}`, providers.RequestMetadata{Model: "gemini-3.6-flash", Endpoint: "countTokens"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, ok := (Adapter{}).CountTokensRequest([]byte(tc.body), tc.meta); ok {
				t.Fatal("CountTokensRequest accepted unprojectable request")
			}
		})
	}
}

func TestParseCountTokens(t *testing.T) {
	if got, ok := (Adapter{}).ParseCountTokens([]byte(`{"totalTokens":456}`)); !ok || got != 456 {
		t.Fatalf("ParseCountTokens=(%d,%v)", got, ok)
	}
	if _, ok := (Adapter{}).ParseCountTokens([]byte(`{"totalTokens":"456"}`)); ok {
		t.Fatal("ParseCountTokens accepted string count")
	}
	for _, body := range []string{
		`{"totalTokens":1,"totalTokens":2}`,
		`{"other":1,"other":2,"totalTokens":3}`,
		`{"totalTokens":1}{}`,
	} {
		if _, ok := (Adapter{}).ParseCountTokens([]byte(body)); ok {
			t.Fatalf("ParseCountTokens accepted ambiguous response %q", body)
		}
	}
}

func readCountFixture(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for len(body) > 0 && (body[len(body)-1] == '\n' || body[len(body)-1] == '\r') {
		body = body[:len(body)-1]
	}
	return body
}
