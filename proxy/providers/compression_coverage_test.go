package providers_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/azureopenai"
	"github.com/JuliusBrussee/caveman/proxy/providers/bedrock"
	"github.com/JuliusBrussee/caveman/proxy/providers/gemini"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
	"github.com/JuliusBrussee/caveman/proxy/providers/openaicompat"
	"github.com/JuliusBrussee/caveman/proxy/providers/vertex"
)

func TestCompressionRouteMatrixCoversEveryRegisteredRoute(t *testing.T) {
	matrix := map[string]bool{}
	for _, row := range providers.CompressionRouteMatrix() {
		if row.Route == "" || row.Grammar == "" || row.Status != "supported" && row.Status != "unsupported" {
			t.Fatalf("invalid compression matrix row: %#v", row)
		}
		if row.Status == "unsupported" && row.Reason == "" {
			t.Fatalf("unsupported row missing reason: %#v", row)
		}
		key := row.Provider + " " + row.Route
		if matrix[key] {
			t.Fatalf("duplicate compression matrix row %q", key)
		}
		matrix[key] = true
	}

	registered := map[string][]string{
		"openai":            openai.New("https://example.com").(openai.Adapter).Routes,
		"anthropic":         anthropic.New("https://example.com").(anthropic.Adapter).Routes,
		"azure_openai":      azureopenai.New("https://example.com").(azureopenai.Adapter).Routes,
		"bedrock":           bedrock.New("https://example.com").(bedrock.Adapter).Routes,
		"vertex":            vertex.New("https://example.com").(vertex.Adapter).Routes,
		"openai_compatible": openaicompat.New("https://example.com").(openaicompat.Adapter).Routes,
		"gemini":            append(gemini.New("https://example.com").(gemini.Adapter).Routes, gemini.CompressionRoutePatterns()...),
	}
	// Read-only metadata mounts are registered request surfaces too, so they
	// need an explicit compression decision for the same reason the POST routes
	// do: a new one must not be able to appear without one (issue #1187).
	registeredMetadata := map[string][]string{
		"openai":            openai.New("https://example.com").(openai.Adapter).MetadataRoutes,
		"anthropic":         anthropic.New("https://example.com").(anthropic.Adapter).MetadataRoutes,
		"azure_openai":      azureopenai.New("https://example.com").(azureopenai.Adapter).MetadataRoutes,
		"bedrock":           bedrock.New("https://example.com").(bedrock.Adapter).MetadataRoutes,
		"vertex":            vertex.New("https://example.com").(vertex.Adapter).MetadataRoutes,
		"openai_compatible": openaicompat.New("https://example.com").(openaicompat.Adapter).MetadataRoutes,
		"gemini":            gemini.New("https://example.com").(gemini.Adapter).MetadataRoutes,
	}
	for provider, routes := range registeredMetadata {
		for _, route := range routes {
			if !matrix[provider+" "+route] {
				t.Errorf("registered metadata route missing compression decision: provider=%q route=%q", provider, route)
			}
		}
	}

	for provider, routes := range registered {
		for _, route := range routes {
			if !matrix[provider+" "+route] {
				t.Errorf("registered route missing compression decision: provider=%q route=%q", provider, route)
			}
		}
	}

	geminiAdapter := gemini.New("https://example.com")
	for _, pattern := range gemini.CompressionRoutePatterns() {
		if !geminiAdapter.MatchRoute(http.MethodPost, strings.Replace(pattern, "{model}", "gemini-test", 1)) {
			t.Errorf("Gemini compression pattern is not registered by MatchRoute: %q", pattern)
		}
	}
	namedCompat, err := openaicompat.NewNamed("matrix-test", "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/compat/matrix-test/v1/chat/completions", "/compat/matrix-test/v1/responses"} {
		if !namedCompat.MatchRoute(http.MethodPost, route) {
			t.Errorf("named compat route is not registered: %q", route)
		}
	}
	if !namedCompat.(providers.MetadataRouter).MatchMetadataRoute(http.MethodGet, "/compat/matrix-test/v1/models") {
		t.Error("named compat metadata route is not registered: /compat/{name}/v1/models")
	}
	for _, family := range []string{"/compat/{name}/v1/chat/completions", "/compat/{name}/v1/responses", "/compat/{name}/v1/models"} {
		if !matrix["openai_compatible "+family] {
			t.Errorf("named compat family missing compression decision: %q", family)
		}
	}
}

func TestCompressionRouteMatrixMatchesRecoveryGrammar(t *testing.T) {
	status := map[string]string{}
	for _, row := range providers.CompressionRouteMatrix() {
		status[row.Provider+" "+row.Route] = row.Status
	}
	for _, key := range []string{
		"openai /v1/responses",
		"openai /openai/v1/responses",
		"openai_compatible /compat/{name}/v1/responses",
		"gemini /gemini/v1beta/models/{model}:generateContent",
		"gemini /v1beta/models/{model}:generateContent",
	} {
		if status[key] != "supported" {
			t.Errorf("%s status = %q, want supported with implemented recovery grammar", key, status[key])
		}
	}
	for _, key := range []string{
		"gemini /gemini/v1beta/models/{model}:streamGenerateContent",
		"gemini /v1beta/models/{model}:streamGenerateContent",
	} {
		if status[key] != "unsupported" {
			t.Errorf("%s status = %q, want unsupported without streaming recovery grammar", key, status[key])
		}
	}
}
