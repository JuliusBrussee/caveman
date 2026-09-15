package gateway

import (
	"net/http"
	"strings"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

type AuthMode string

const (
	AuthModePAYG         AuthMode = "payg"
	AuthModeOAuth        AuthMode = "oauth"
	AuthModeSubscription AuthMode = "subscription"
	AuthModeUnknown      AuthMode = "unknown"
)

var subscriptionUserAgentPrefixes = []string{
	"claude-cli/",
	"claude-code/",
	"codex-cli/",
	"cursor/",
	"claude-vscode/",
	"github-copilot/",
	"anthropic-cli/",
	"antigravity/",
}

// ClassifyAuthMode classifies the caller from header shape only. It is pure and
// must never log header values: token shapes are enough for policy.
func ClassifyAuthMode(h http.Header) AuthMode {
	if mode, ok := classifyHeaderAuthMode(h); ok {
		return mode
	}
	return AuthModeUnknown
}

// ClassifyResolvedAuthMode includes the credential resolver's final BYOK/config
// key. Header-only classification labeled a configured subscription/OAuth token
// as PAYG whenever the client omitted Authorization, creating phantom spend.
func ClassifyResolvedAuthMode(h http.Header, credential providers.Credential) AuthMode {
	if mode, explicit := classifyCredentialAuthKind(credential); explicit {
		return mode
	}
	if mode, ok := classifyHeaderAuthMode(h); ok {
		return mode
	}
	return classifyCredential(credential)
}

func classifyHeaderAuthMode(h http.Header) (AuthMode, bool) {
	ua := strings.ToLower(h.Get("user-agent"))
	for _, prefix := range subscriptionUserAgentPrefixes {
		if strings.Contains(ua, prefix) {
			return AuthModeSubscription, true
		}
	}

	auth := strings.TrimSpace(h.Get("authorization"))
	if auth != "" {
		scheme, value, ok := strings.Cut(auth, " ")
		if !ok || !strings.EqualFold(scheme, "bearer") {
			return AuthModeOAuth, true
		}
		return classifyCredential(providers.Credential{Key: strings.TrimSpace(value), Scheme: "bearer"}), true
	}

	if strings.TrimSpace(h.Get("x-api-key")) != "" || strings.TrimSpace(h.Get("x-goog-api-key")) != "" {
		return AuthModePAYG, true
	}
	return AuthModeUnknown, false
}

func classifyCredential(credential providers.Credential) AuthMode {
	if mode, explicit := classifyCredentialAuthKind(credential); explicit {
		return mode
	}
	token := strings.TrimSpace(credential.Key)
	lower := strings.ToLower(token)
	switch {
	case token == "":
		return AuthModeUnknown
	case strings.HasPrefix(lower, "sk-ant-oat"), strings.HasPrefix(lower, "sk-ant-sid"), strings.HasPrefix(lower, "sess-"):
		return AuthModeSubscription
	case strings.HasPrefix(lower, "bearer "), strings.HasPrefix(lower, "ya29."), strings.Count(token, ".") >= 2:
		return AuthModeOAuth
	case strings.HasPrefix(token, "sk-ant-api"), strings.HasPrefix(token, "sk-"), strings.HasPrefix(token, "AIza"), strings.HasPrefix(token, "AKIA"), strings.HasPrefix(token, "ASIA"):
		return AuthModePAYG
	case strings.EqualFold(credential.Scheme, "bearer"):
		return AuthModeOAuth
	default:
		return AuthModeUnknown
	}
}

// classifyCredentialAuthKind trusts only resolver-stamped credential metadata.
// Bedrock API keys travel over Bearer but are paid AWS credentials, not OAuth;
// IAM access keys are likewise PAYG. An unknown explicit kind fails closed rather
// than falling back to a token-shape guess.
func classifyCredentialAuthKind(credential providers.Credential) (AuthMode, bool) {
	switch strings.ToLower(strings.TrimSpace(credential.AuthKind)) {
	case "":
		return AuthModeUnknown, false
	case "bedrock_api_key", "aws_access_keys":
		return AuthModePAYG, true
	default:
		return AuthModeUnknown, true
	}
}
