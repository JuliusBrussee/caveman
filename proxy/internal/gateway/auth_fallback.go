package gateway

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/shared/platform/env"
)

const placeholderToken = "no-key-required"

func (s *Server) applyUpstreamAuthFallback(provider string, credential providers.Credential, headers http.Header) {
	key, ok := fallbackKey(provider, credential, headers)
	if !ok || key == "" {
		return
	}
	switch provider {
	case "anthropic":
		headers.Del("authorization")
		headers.Set("x-api-key", key)
	case "gemini":
		headers.Del("authorization")
		headers.Set("x-goog-api-key", key)
	case "azure_openai":
		// Azure API-key mode is deliberately separate from bearer/Entra mode:
		// the standalone BYOK env key is sent only in the provider's api-key
		// header. Clear any placeholder auth left by the inbound request so a
		// key from another provider can never cross the boundary.
		headers.Del("authorization")
		headers.Del("api-key")
		headers.Set("api-key", key)
	case "openai", "openai_compatible":
		headers.Set("authorization", "Bearer "+key)
	default:
		return
	}
	logger := s.logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info("placeholder auth replaced from env", "provider", provider)
}

func fallbackKey(provider string, credential providers.Credential, headers http.Header) (string, bool) {
	// The resolver is the only component allowed to select a fallback env. An
	// empty value means explicit no-auth (not "use the provider default").
	envName := strings.TrimSpace(credential.AuthFallbackEnv)
	if envName == "" {
		return "", false
	}
	switch provider {
	case "anthropic":
		if hasUsableAuthorization(headers) || hasUsableProviderKey(headers.Get("x-api-key")) {
			return "", false
		}
		if envName != "ANTHROPIC_API_KEY" {
			return "", false
		}
		return firstEnv(envName), true
	case "gemini":
		if hasUsableAuthorization(headers) || hasUsableProviderKey(headers.Get("x-goog-api-key")) {
			return "", false
		}
		if envName != "GEMINI_API_KEY" && envName != "GOOGLE_API_KEY" {
			return "", false
		}
		if envName == "GEMINI_API_KEY" {
			return firstEnv("GEMINI_API_KEY", "GOOGLE_API_KEY"), true
		}
		return firstEnv(envName), true
	case "azure_openai":
		if hasUsableAuthorization(headers) || hasUsableProviderKey(headers.Get("api-key")) {
			return "", false
		}
		if envName != "AZURE_OPENAI_API_KEY" {
			return "", false
		}
		return firstEnv(envName), true
	case "openai", "openai_compatible":
		if hasUsableAuthorization(headers) {
			return "", false
		}
		if provider == "openai" && envName != "OPENAI_API_KEY" {
			return "", false
		}
		return firstEnv(envName), true
	default:
		return "", false
	}
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(env.String(name, "")); value != "" {
			return value
		}
	}
	return ""
}

func hasUsableAuthorization(headers http.Header) bool {
	value := strings.TrimSpace(headers.Get("authorization"))
	return value != "" && !isPlaceholderAuthorization(value)
}

func hasUsableProviderKey(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !isPlaceholderProviderKey(value)
}

func isPlaceholderProviderKey(value string) bool {
	return strings.TrimSpace(value) == placeholderToken
}

func isPlaceholderAuthorization(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != len("Bearer "+placeholderToken) {
		return false
	}
	return strings.EqualFold(value[:len("Bearer")], "Bearer") && value[len("Bearer"):] == " "+placeholderToken
}
