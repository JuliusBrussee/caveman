package gateway

import (
	"net/http"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func TestClassifyAuthModeTable(t *testing.T) {
	cases := []struct {
		name string
		h    http.Header
		want AuthMode
	}{
		{"ua subscription wins over payg token", http.Header{"User-Agent": {"codex-cli/1.0"}, "Authorization": {"Bearer sk-ant-api123"}}, AuthModeSubscription},
		{"claude code ua", http.Header{"User-Agent": {"Claude-Code/2.0"}}, AuthModeSubscription},
		{"anthropic oat token is subscription credential", http.Header{"Authorization": {"Bearer sk-ant-oat-abc"}}, AuthModeSubscription},
		{"anthropic payg", http.Header{"Authorization": {"Bearer sk-ant-api-abc"}}, AuthModePAYG},
		{"generic sk payg", http.Header{"Authorization": {"Bearer sk-project"}}, AuthModePAYG},
		{"jwt oauth", http.Header{"Authorization": {"Bearer aaa.bbb.ccc"}}, AuthModeOAuth},
		{"non bearer oauth", http.Header{"Authorization": {"Basic abc"}}, AuthModeOAuth},
		{"x api key payg", http.Header{"X-Api-Key": {"sk-agent"}}, AuthModePAYG},
		{"google api key payg", http.Header{"X-Goog-Api-Key": {"AIza"}}, AuthModePAYG},
		{"bearer unknown is oauth", http.Header{"Authorization": {"Bearer opaque-token"}}, AuthModeOAuth},
		{"missing credential is unknown", http.Header{}, AuthModeUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyAuthMode(tc.h); got != tc.want {
				t.Fatalf("ClassifyAuthMode = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestClassifyResolvedAuthModeUsesConfiguredCredential(t *testing.T) {
	if got := ClassifyResolvedAuthMode(http.Header{}, providers.Credential{Key: "sk-ant-oat-configured"}); got != AuthModeSubscription {
		t.Fatalf("configured subscription = %s", got)
	}
	if got := ClassifyResolvedAuthMode(http.Header{}, providers.Credential{Key: "sk-ant-api-configured"}); got != AuthModePAYG {
		t.Fatalf("configured PAYG = %s", got)
	}
	if got := ClassifyResolvedAuthMode(http.Header{}, providers.Credential{Key: "opaque", Scheme: "bearer"}); got != AuthModeOAuth {
		t.Fatalf("configured bearer = %s", got)
	}
}

func TestClassifyResolvedAuthModeBedrockCredentialsArePAYG(t *testing.T) {
	for _, tc := range []struct {
		name       string
		header     http.Header
		credential providers.Credential
	}{
		{
			name:       "opaque API key transported as bearer",
			header:     http.Header{"Authorization": {"Bearer opaque-bedrock-token"}},
			credential: providers.Credential{Key: "opaque-bedrock-token", Scheme: "bearer", AuthKind: "bedrock_api_key"},
		},
		{
			name:       "Claude Code user agent still bills AWS",
			header:     http.Header{"User-Agent": {"claude-cli/2.0"}},
			credential: providers.Credential{Key: "opaque-bedrock-token", Scheme: "bearer", AuthKind: "bedrock_api_key"},
		},
		{
			name:       "IAM access keys",
			header:     http.Header{},
			credential: providers.Credential{Key: "AKIAEXAMPLE:secret", AuthKind: "aws_access_keys"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyResolvedAuthMode(tc.header, tc.credential); got != AuthModePAYG {
				t.Fatalf("ClassifyResolvedAuthMode = %s, want PAYG for explicit Bedrock credential", got)
			}
		})
	}
}
