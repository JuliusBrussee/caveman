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

func TestExtractToolCatalogMultiProvider(t *testing.T) {
	for _, tc := range []struct {
		name string
		meta providers.RequestMetadata
		body string
	}{
		{
			name: "openai chat",
			meta: providers.RequestMetadata{Provider: "openai", Endpoint: "/v1/chat/completions"},
			body: `{"model":"gpt-5.6","tools":[{"type":"function","function":{"name":"Read","parameters":{"type":"object","title":"Read"}}}],"messages":[]}`,
		},
		{
			name: "openai responses",
			meta: providers.RequestMetadata{Provider: "openai", Endpoint: "/v1/responses"},
			body: `{"model":"gpt-5.6","tools":[{"type":"function","name":"Read","parameters":{"type":"object","title":"Read"}}],"input":[]}`,
		},
		{
			name: "gemini generateContent",
			meta: providers.RequestMetadata{Provider: "gemini", Endpoint: "/v1beta/models/gemini-2.5-pro:generateContent"},
			body: `{"tools":[{"functionDeclarations":[{"name":"Read","parameters":{"type":"object","title":"Read"}}]}],"contents":[]}`,
		},
		{
			name: "azure openai",
			meta: providers.RequestMetadata{Provider: "azure_openai", Endpoint: "/azure/openai/deployments/gpt/chat/completions"},
			body: `{"model":"gpt-5.6","tools":[{"type":"function","function":{"name":"Read","parameters":{"type":"object"}}}],"messages":[]}`,
		},
		{
			name: "vertex claude",
			meta: providers.RequestMetadata{Provider: "vertex", Endpoint: "/vertex/v1/projects/p/locations/global/publishers/anthropic/models/claude-sonnet-4-6:rawPredict"},
			body: toolzoneBody,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, reassemble, ok := providers.ExtractToolCatalog([]byte(tc.body), tc.meta)
			if !ok || len(raw) == 0 {
				t.Fatalf("expected catalog extraction to succeed for %s", tc.name)
			}
			out, err := reassemble(raw)
			if err != nil || string(out) != tc.body {
				t.Fatalf("identity splice altered body: err=%v", err)
			}
		})
	}
}

func TestExtractToolCatalogFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		body string
		meta providers.RequestMetadata
	}{
		{
			name: "bedrock is excluded",
			body: toolzoneBody,
			meta: providers.RequestMetadata{Provider: "bedrock", Endpoint: "/bedrock/model/anthropic.claude-sonnet-4-6/converse"},
		},
		{
			name: "count_tokens must measure the body it was given",
			body: toolzoneBody,
			meta: providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages/count_tokens"},
		},
		{
			name: "gemini countTokens must measure the body it was given",
			body: toolzoneBody,
			meta: providers.RequestMetadata{Provider: "gemini", Endpoint: "/v1beta/models/gemini-2.5-pro:countTokens"},
		},
		{
			name: "openai input_tokens must measure the body it was given",
			body: toolzoneBody,
			meta: providers.RequestMetadata{Provider: "openai", Endpoint: "/v1/responses/input_tokens"},
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
