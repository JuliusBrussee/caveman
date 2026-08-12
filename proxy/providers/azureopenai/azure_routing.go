package azureopenai

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/shared/platform/env"
)

// defaultAPIVersions is the built-in api-version allowlist. Operators override it
// with CAVE_AZURE_API_VERSION_ALLOWLIST (comma-separated). Pinning the allowed
// api-versions prevents clients from selecting unverified/deprecated versions.
var defaultAPIVersions = []string{
	"2024-02-01",
	"2024-06-01",
	"2024-08-01-preview",
	"2024-10-21",
	"2025-01-01-preview",
	"2025-03-01-preview",
	"2025-04-01-preview",
	"preview",
	"latest",
}

// ResolveUpstreamURL validates either the legacy deployment route or the
// current Foundry Models v1 inference route before resolving upstream.
func (a Adapter) ResolveUpstreamURL(ctx context.Context, req *http.Request, route providers.RouteContext) (*url.URL, error) {
	if err := validateAzureRequest(req.URL); err != nil {
		return nil, err
	}
	return a.Base.ResolveUpstreamURL(ctx, req, route)
}

func validateAzureRequest(u *url.URL) error {
	if foundryV1InferenceRoute(u.Path) {
		versions := u.Query()["api-version"]
		if len(versions) > 1 {
			return fmt.Errorf("azure request has duplicate api-version values")
		}
		if len(versions) == 1 && versions[0] != "v1" && versions[0] != "preview" {
			return fmt.Errorf("azure Foundry api-version %q is not supported", versions[0])
		}
		return nil
	}
	if !legacyChatCompletionsRoute(u.Path) {
		return fmt.Errorf("azure legacy inference path %q is not supported", u.Path)
	}
	versions := u.Query()["api-version"]
	if len(versions) > 1 {
		return fmt.Errorf("azure request has duplicate api-version values")
	}
	version := ""
	if len(versions) == 1 {
		version = versions[0]
	}
	if version == "" {
		return fmt.Errorf("azure request missing api-version")
	}
	if !apiVersionAllowed(version) {
		return fmt.Errorf("azure api-version %q is not on the allowlist", version)
	}
	return nil
}

func foundryV1InferenceRoute(path string) bool {
	path = strings.TrimPrefix(path, "/azure")
	switch path {
	case "/openai/v1/chat/completions", "/openai/v1/responses":
		return true
	default:
		return false
	}
}

func legacyChatCompletionsRoute(path string) bool {
	path = strings.TrimPrefix(path, "/azure")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 5 || parts[0] != "openai" || parts[1] != "deployments" ||
		parts[3] != "chat" || parts[4] != "completions" {
		return false
	}
	deployment := parts[2]
	if deployment == "" || deployment == "." || deployment == ".." || len(deployment) > 128 {
		return false
	}
	for _, char := range deployment {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && !strings.ContainsRune("._-", char) {
			return false
		}
	}
	return true
}

func apiVersionAllowed(version string) bool {
	for _, v := range allowlist() {
		if v == version {
			return true
		}
	}
	return false
}

func allowlist() []string {
	if raw := env.String("CAVE_AZURE_API_VERSION_ALLOWLIST", ""); raw != "" {
		out := []string{}
		for _, v := range strings.Split(raw, ",") {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	return defaultAPIVersions
}
