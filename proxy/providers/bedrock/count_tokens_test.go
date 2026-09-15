package bedrock

import (
	"os"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func TestCountTokensRequestGolden(t *testing.T) {
	original := readCountFixture(t, "testdata/count_tokens_input.json")
	want := readCountFixture(t, "testdata/count_tokens_expected.json")

	route, body, ok := Adapter{}.CountTokensRequest(original, providers.RequestMetadata{Endpoint: "mantle_messages"})
	if !ok {
		t.Fatal("CountTokensRequest rejected valid Mantle Messages request")
	}
	if route != "/bedrock/anthropic/v1/messages/count_tokens" {
		t.Fatalf("route=%q", route)
	}
	if string(body) != string(want) {
		t.Fatalf("projected body mismatch\n got: %s\nwant: %s", body, want)
	}
}

func TestCountTokensRequestFailsClosed(t *testing.T) {
	for _, endpoint := range []string{"invoke", "converse", "mantle_count_tokens", ""} {
		if _, _, ok := (Adapter{}).CountTokensRequest(
			[]byte(`{"model":"anthropic.claude-opus-4-8","messages":[]}`),
			providers.RequestMetadata{Endpoint: endpoint},
		); ok {
			t.Fatalf("CountTokensRequest accepted endpoint %q", endpoint)
		}
	}
}

func TestParseCountTokens(t *testing.T) {
	if got, ok := (Adapter{}).ParseCountTokens([]byte(`{"input_tokens":789}`)); !ok || got != 789 {
		t.Fatalf("ParseCountTokens=(%d,%v)", got, ok)
	}
	if _, ok := (Adapter{}).ParseCountTokens([]byte(`{"input_tokens":null}`)); ok {
		t.Fatal("ParseCountTokens accepted null count")
	}
	for _, body := range []string{
		`{"input_tokens":1,"input_tokens":2}`,
		`{"other":1,"other":2,"input_tokens":3}`,
		`{"input_tokens":1}{}`,
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
