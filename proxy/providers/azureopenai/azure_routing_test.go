package azureopenai

import (
	"context"
	"net/http"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func resolve(t *testing.T, rawurl string) error {
	t.Helper()
	a := New("https://example.openai.azure.com").(Adapter)
	req, err := http.NewRequest(http.MethodPost, rawurl, nil)
	if err != nil {
		t.Fatalf("bad test url: %v", err)
	}
	_, err = a.ResolveUpstreamURL(context.Background(), req, providers.RouteContext{})
	return err
}

func TestAzure_ValidRequestAllowed(t *testing.T) {
	if err := resolve(t, "/azure/openai/deployments/gpt-prod/chat/completions?api-version=2024-10-21"); err != nil {
		t.Errorf("valid azure request rejected: %v", err)
	}
}

func TestAzure_FoundryV1InferenceRoutesAllowed(t *testing.T) {
	for _, path := range []string{
		"/azure/openai/v1/chat/completions",
		"/azure/openai/v1/chat/completions?api-version=v1",
		"/azure/openai/v1/responses?api-version=preview",
	} {
		if err := resolve(t, path); err != nil {
			t.Errorf("current Foundry route %q rejected: %v", path, err)
		}
	}
}

func TestAzure_FoundryV1RejectsUnknownVersionAndRoute(t *testing.T) {
	for _, path := range []string{
		"/azure/openai/v1/responses?api-version=latest",
		"/azure/openai/v1/embeddings?api-version=v1",
		"/azure/openai/v1/responses/resp_123/cancel?api-version=v1",
	} {
		if err := resolve(t, path); err == nil {
			t.Errorf("unsupported Foundry route %q should be rejected", path)
		}
	}
}

func TestAzure_FoundryV1ResolvesNativeUpstreamPath(t *testing.T) {
	a := New("https://example.openai.azure.com").(Adapter)
	req, err := http.NewRequest(http.MethodPost, "/azure/openai/v1/responses?api-version=v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.ResolveUpstreamURL(context.Background(), req, providers.RouteContext{})
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "https://example.openai.azure.com/openai/v1/responses?api-version=v1" {
		t.Fatalf("upstream URL=%q", got.String())
	}
}

func TestAzure_DisallowedApiVersionRejected(t *testing.T) {
	if err := resolve(t, "/azure/openai/deployments/gpt-prod/chat/completions?api-version=1999-01-01"); err == nil {
		t.Error("api-version not on allowlist should be rejected")
	}
}

func TestAzure_MissingApiVersionRejected(t *testing.T) {
	if err := resolve(t, "/azure/openai/deployments/gpt-prod/chat/completions"); err == nil {
		t.Error("missing api-version should be rejected")
	}
}

func TestAzure_MissingDeploymentRejected(t *testing.T) {
	if err := resolve(t, "/azure/openai/chat/completions?api-version=2024-10-21"); err == nil {
		t.Error("missing deployment should be rejected")
	}
}

func TestAzure_LegacyRouteRejectsNonChatActions(t *testing.T) {
	for _, path := range []string{
		"/azure/openai/deployments/gpt-prod/images/generations?api-version=2024-10-21",
		"/azure/openai/deployments/gpt-prod/audio/speech?api-version=2024-10-21",
		"/azure/openai/deployments/gpt-prod/embeddings?api-version=2024-10-21",
		"/azure/openai/deployments/gpt-prod/chat/completions/extra?api-version=2024-10-21",
		"/azure/prefix/openai/deployments/gpt-prod/chat/completions?api-version=2024-10-21",
	} {
		if err := resolve(t, path); err == nil {
			t.Errorf("unsupported Azure legacy route %q should be rejected", path)
		}
	}
}

func TestAzure_EnvAllowlistOverride(t *testing.T) {
	t.Setenv("CAVE_AZURE_API_VERSION_ALLOWLIST", "2030-01-01, 2030-06-01")
	if err := resolve(t, "/azure/openai/deployments/d/chat/completions?api-version=2030-01-01"); err != nil {
		t.Errorf("env-allowlisted version should pass: %v", err)
	}
	// A version that is in the built-in default but not the override must now fail.
	if err := resolve(t, "/azure/openai/deployments/d/chat/completions?api-version=2024-10-21"); err == nil {
		t.Error("env override should replace the default allowlist")
	}
}
