// Package providerorigin contains the canonical-origin allowlist used by both
// the control plane and gateway money paths. A configured endpoint may still be
// routed when it is customer-owned, but only an official provider origin can
// carry catalog-priced or verified-savings provenance.
package providerorigin

import (
	"net/url"
	"strings"
)

// Trusted reports whether raw is an HTTPS origin owned by the named provider.
// It intentionally rejects openai-compatible endpoints: their protocol is
// useful for routing, but no provider identity or list-price contract is proven.
func Trusted(provider, raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai":
		return host == "api.openai.com" || (strings.HasSuffix(host, ".api.openai.com") && strings.Count(host, ".") == 3)
	case "anthropic":
		return host == "api.anthropic.com"
	case "gemini":
		return host == "generativelanguage.googleapis.com"
	case "azure_openai":
		return strings.HasSuffix(host, ".openai.azure.com") || strings.HasSuffix(host, ".cognitiveservices.azure.com")
	case "bedrock":
		return isAWSBedrockHost(host)
	case "vertex":
		return host == "aiplatform.googleapis.com" ||
			(strings.HasSuffix(host, "-aiplatform.googleapis.com") && strings.Count(host, ".") == 2) ||
			(strings.HasPrefix(host, "aiplatform.") && strings.HasSuffix(host, ".rep.googleapis.com"))
	default:
		return false
	}
}

func isAWSBedrockHost(host string) bool {
	parts := strings.Split(host, ".")
	if len(parts) != 4 || parts[3] != "com" || parts[2] != "amazonaws" {
		return false
	}
	return parts[0] == "bedrock-runtime" || parts[0] == "bedrock-runtime-fips" || parts[0] == "bedrock" || parts[0] == "bedrock-fips" || parts[0] == "bedrock-mantle"
}
