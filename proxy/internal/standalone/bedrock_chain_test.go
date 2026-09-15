package standalone

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/config"
	"github.com/JuliusBrussee/caveman/shared/platform/awscreds"
)

// containerCredentialStub plays the ECS task-role endpoint: the shape a proxy
// sees on Fargate through AWS_CONTAINER_CREDENTIALS_FULL_URI. Loopback http is
// the one plaintext form the chain accepts, so httptest needs no override.
func containerCredentialStub(t *testing.T) (url string, calls *int) {
	t.Helper()
	calls = new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		_ = json.NewEncoder(w).Encode(map[string]string{
			"AccessKeyId":     "ASIACHAIN",
			"SecretAccessKey": "chain-secret",
			"Token":           "chain-session",
			"Expiration":      time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	t.Cleanup(srv.Close)
	return srv.URL, calls
}

// clearAWSEnv leaves only the chain source the test wires; a developer's own
// AWS_* exports would otherwise take precedence and the assertions would pass
// for the wrong reason.
func clearAWSEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_BEARER_TOKEN_BEDROCK",
		"AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"} {
		t.Setenv(name, "")
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
}

func TestStandaloneBedrockSignsWithTaskRoleWhenEnvHasNoKeys(t *testing.T) {
	clearAWSEnv(t)
	endpoint, calls := containerCredentialStub(t)
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", endpoint)

	upstream := &captureUpstreamTransport{response: `{}`}
	srv := New(config.Config{
		Mode:      "record",
		Providers: map[string]config.ProviderConfig{"bedrock": {BaseURL: "https://bedrock-runtime.us-east-1.amazonaws.com"}},
	}, nil, Options{HTTPClient: &http.Client{Transport: upstream}})

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/bedrock/model/anthropic.claude-sonnet-4-6/converse", strings.NewReader(`{"messages":[{"role":"user","content":[{"text":"hi"}]}]}`))
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}
	auth := upstream.headers.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=ASIACHAIN/") || !strings.Contains(auth, "/us-east-1/bedrock/aws4_request") {
		t.Fatalf("upstream was not signed with the task-role principal: %q", auth)
	}
	if got := upstream.headers.Get("X-Amz-Security-Token"); got != "chain-session" {
		t.Errorf("session token = %q, want the role's session token", got)
	}
	for name, values := range upstream.headers {
		for _, value := range values {
			if strings.Contains(value, "chain-secret") {
				t.Errorf("signing secret leaked in %s", name)
			}
		}
	}
	if *calls != 1 {
		t.Errorf("credential endpoint called %d times for two requests; want one fetch, then cache", *calls)
	}
}

func TestStandaloneBedrockReSignsSDKRequestWithTaskRole(t *testing.T) {
	clearAWSEnv(t)
	endpoint, _ := containerCredentialStub(t)
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", endpoint)

	upstream := &captureUpstreamTransport{response: `{}`}
	srv := New(config.Config{
		Mode:      "record",
		Providers: map[string]config.ProviderConfig{"bedrock": {BaseURL: "https://bedrock-runtime.us-east-1.amazonaws.com"}},
	}, nil, Options{HTTPClient: &http.Client{Transport: upstream}})

	// An SDK on the same task signed with the same role; the proxy must re-sign
	// with the identity the chain resolves, exactly as it does for env keys.
	inbound := "AWS4-HMAC-SHA256 Credential=ASIACHAIN/20260907/us-east-1/bedrock/aws4_request, SignedHeaders=host;x-amz-date;x-amz-security-token, Signature=" + strings.Repeat("0", 64)
	req := httptest.NewRequest(http.MethodPost, "/bedrock/model/anthropic.claude-sonnet-4-6/converse", strings.NewReader(`{"messages":[]}`))
	req.Header.Set("Authorization", inbound)
	req.Header.Set("X-Amz-Date", "20260907T000000Z")
	req.Header.Set("X-Amz-Security-Token", "chain-session")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if auth := upstream.headers.Get("Authorization"); !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=ASIACHAIN/") || auth == inbound {
		t.Fatalf("SDK request was not re-signed with the chain principal: %q", auth)
	}
}

func TestCreds_BedrockEnvPairStillWinsOverChain(t *testing.T) {
	clearAWSEnv(t)
	endpoint, calls := containerCredentialStub(t)
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", endpoint)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAENV")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "env-secret")
	c := Creds{cfg: config.Config{}, bedrock: awscreds.New(awscreds.Options{})}

	req := httptest.NewRequest(http.MethodPost, "/bedrock/model/anthropic.claude-sonnet-4-6/converse", nil)
	if got := c.Resolve("bedrock", req); got.Key != "AKIAENV:env-secret" || got.AuthKind != "aws_access_keys" {
		t.Fatalf("credential = %+v, want the configured env pair", got)
	}
	if *calls != 0 {
		t.Fatalf("chain consulted %d times although the env pair was complete", *calls)
	}
}

func TestStandaloneBedrockWithoutAnyCredentialStillFailsClosed(t *testing.T) {
	clearAWSEnv(t)
	upstream := &captureUpstreamTransport{response: `{}`}
	srv := New(config.Config{
		Mode:      "record",
		Providers: map[string]config.ProviderConfig{"bedrock": {BaseURL: "https://bedrock-runtime.us-east-1.amazonaws.com"}},
	}, nil, Options{HTTPClient: &http.Client{Transport: upstream}})
	req := httptest.NewRequest(http.MethodPost, "/bedrock/model/anthropic.claude-sonnet-4-6/converse", strings.NewReader(`{"messages":[]}`))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("status = %d, want a client-side credential error, body = %s", rec.Code, rec.Body.String())
	}
	if upstream.headers != nil {
		t.Fatal("request with no resolvable credential reached upstream")
	}
}

// A typo'd secret variable must not silently switch the signing principal to
// whatever role the host carries. This is the shipped path (chain wired), not
// the hand-built nil-chain Creds the older partial-pair test exercises.
func TestStandaloneBedrockPartialEnvPairDoesNotFallThroughToRole(t *testing.T) {
	clearAWSEnv(t)
	endpoint, calls := containerCredentialStub(t)
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", endpoint)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAENV")

	upstream := &captureUpstreamTransport{response: `{}`}
	srv := New(config.Config{
		Mode:      "record",
		Providers: map[string]config.ProviderConfig{"bedrock": {BaseURL: "https://bedrock-runtime.us-east-1.amazonaws.com"}},
	}, nil, Options{HTTPClient: &http.Client{Transport: upstream}})
	req := httptest.NewRequest(http.MethodPost, "/bedrock/model/anthropic.claude-sonnet-4-6/converse", strings.NewReader(`{"messages":[]}`))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code < 400 || rec.Code >= 500 || upstream.headers != nil {
		t.Fatalf("status = %d upstream-called = %v, want fail-closed with no upstream call (body %s)", rec.Code, upstream.headers != nil, rec.Body.String())
	}
	if *calls != 0 {
		t.Fatalf("container role consulted %d times behind a partial env pair", *calls)
	}
}
