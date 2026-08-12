package anthropic

import (
	"os"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func TestCountTokensRequestGolden(t *testing.T) {
	original := readCountFixture(t, "testdata/count_tokens_input.json")
	want := readCountFixture(t, "testdata/count_tokens_expected.json")

	route, body, ok := Adapter{}.CountTokensRequest(original, providers.RequestMetadata{Endpoint: "/anthropic/v1/messages"})
	if !ok {
		t.Fatal("CountTokensRequest rejected valid Messages request")
	}
	if route != "/anthropic/v1/messages/count_tokens" {
		t.Fatalf("route=%q", route)
	}
	if string(body) != string(want) {
		t.Fatalf("projected body mismatch\n got: %s\nwant: %s", body, want)
	}
}

func TestCountTokensRequestFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		endpoint string
	}{
		{"malformed", "{", "/v1/messages"},
		{"missing model", `{"messages":[]}`, "/v1/messages"},
		{"missing messages", `{"model":"claude-sonnet-4-6"}`, "/v1/messages"},
		{"wrong endpoint", `{"model":"claude-sonnet-4-6","messages":[]}`, "/v1/messages/count_tokens"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, ok := (Adapter{}).CountTokensRequest([]byte(tc.body), providers.RequestMetadata{Endpoint: tc.endpoint}); ok {
				t.Fatal("CountTokensRequest accepted unprojectable request")
			}
		})
	}
}

func TestParseCountTokens(t *testing.T) {
	for _, tc := range []struct {
		body string
		want int
		ok   bool
	}{
		{`{"input_tokens":123}`, 123, true},
		{`{"input_tokens":0}`, 0, true},
		{`{"input_tokens":-1}`, 0, false},
		{`{"input_tokens":1.5}`, 0, false},
		{`{}`, 0, false},
		{`{"input_tokens":1,"input_tokens":2}`, 0, false},
		{`{"other":1,"other":2,"input_tokens":3}`, 0, false},
		{`{"input_tokens":1}{}`, 0, false},
	} {
		got, ok := (Adapter{}).ParseCountTokens([]byte(tc.body))
		if got != tc.want || ok != tc.ok {
			t.Fatalf("ParseCountTokens(%s)=(%d,%v), want (%d,%v)", tc.body, got, ok, tc.want, tc.ok)
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
