package catalog_test

import (
	"testing"

	"github.com/JuliusBrussee/caveman/shared/platform/catalog"
)

func TestContextWindowTokensRequiresExactCatalogMatch(t *testing.T) {
	tests := []struct {
		provider string
		model    string
		want     int
		ok       bool
	}{
		{provider: "openai", model: "gpt-5.6", want: 1_050_000, ok: true},
		{provider: "anthropic", model: "claude-sonnet-4-6", want: 1_000_000, ok: true},
		{provider: "google", model: "gemini-2.5-pro", want: 1_048_576, ok: true},
		{provider: "openai", model: "GPT-5.6", ok: false},
		{provider: "openai", model: "gpt-unknown", ok: false},
	}
	for _, test := range tests {
		got, ok := catalog.ContextWindowTokens(test.provider, test.model)
		if got != test.want || ok != test.ok {
			t.Errorf("ContextWindowTokens(%q, %q) = %d, %v; want %d, %v",
				test.provider, test.model, got, ok, test.want, test.ok)
		}
	}
}
