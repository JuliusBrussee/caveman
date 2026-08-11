package providers_test

import (
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

const toolzoneBody = `{"model":"claude-sonnet-4-6","system":"be brief",` +
	`"tools":[{"name":"Read","title":"Read"},{"name":"Write","cache_control":{"type":"ephemeral"}}],` +
	`"messages":[{"role":"user","content":"hi"}]}`

func TestExtractToolCatalogReturnsExactBytesAndSplicesInPlace(t *testing.T) {
	meta := providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"}
	raw, reassemble, ok := providers.ExtractToolCatalog([]byte(toolzoneBody), meta)
	if !ok {
		t.Fatal("anthropic messages catalog was not extracted")
	}
	want := `[{"name":"Read","title":"Read"},{"name":"Write","cache_control":{"type":"ephemeral"}}]`
	if string(raw) != want {
		t.Fatalf("raw = %s\nwant = %s", raw, want)
	}
	out, err := reassemble([]byte(`[{"name":"Read"}]`))
	if err != nil {
		t.Fatal(err)
	}
	expected := strings.Replace(toolzoneBody, want, `[{"name":"Read"}]`, 1)
	if string(out) != expected {
		t.Fatalf("splice touched bytes outside the catalog:\n got %s\nwant %s", out, expected)
	}
}

func TestExtractToolCatalogFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		body string
		meta providers.RequestMetadata
	}{
		{
			name: "openai is not covered yet",
			body: toolzoneBody,
			meta: providers.RequestMetadata{Provider: "openai", Endpoint: "/v1/chat/completions"},
		},
		{
			name: "gemini is not covered yet",
			body: toolzoneBody,
			meta: providers.RequestMetadata{Provider: "gemini", Endpoint: "/v1beta/models/x:generateContent"},
		},
		{
			name: "count_tokens must measure the body it was given",
			body: toolzoneBody,
			meta: providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages/count_tokens"},
		},
		{
			name: "no tools field",
			body: `{"model":"claude-sonnet-4-6","messages":[]}`,
			meta: providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"},
		},
		{
			name: "tools is not an array",
			body: `{"model":"claude-sonnet-4-6","tools":{"name":"Read"},"messages":[]}`,
			meta: providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"},
		},
		{
			name: "malformed body",
			body: `{"model":"claude-sonnet-4-6","tools":[`,
			meta: providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, ok := providers.ExtractToolCatalog([]byte(tc.body), tc.meta); ok {
				t.Fatal("extraction must fail closed")
			}
		})
	}
}
