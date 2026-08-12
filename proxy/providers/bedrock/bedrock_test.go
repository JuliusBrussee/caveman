package bedrock

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/shared/platform/awssig"
)

const stubBase = "https://bedrock-runtime.us-east-1.amazonaws.com"
const claudeModel = "anthropic.claude-3-5-sonnet-20241022-v2:0"

func newAdapter(t *testing.T) Adapter {
	t.Helper()
	return New(stubBase).(Adapter)
}

func invokePath(model, action string) string {
	return "/bedrock/model/" + model + "/" + action
}

// --- Routing / allowlist -------------------------------------------------

func TestMatchRoute_BedrockInvoke(t *testing.T) {
	a := newAdapter(t)
	if !a.MatchRoute(http.MethodPost, invokePath(claudeModel, "invoke")) {
		t.Error("bedrock invoke route should match")
	}
	if !a.MatchRoute(http.MethodPost, invokePath(claudeModel, "converse")) {
		t.Error("bedrock converse route should match")
	}
	if a.MatchRoute(http.MethodGet, invokePath(claudeModel, "invoke")) {
		t.Error("only POST should match")
	}
	if a.MatchRoute(http.MethodPost, "/openai/v1/responses") {
		t.Error("non-bedrock route must not match")
	}
	if !a.MatchRoute(http.MethodPost, "/bedrock/anthropic/v1/messages") {
		t.Error("official Bedrock Mantle Messages route should match the adapter")
	}
}

func resolve(t *testing.T, a Adapter, path string, header map[string]string) (string, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, path, nil)
	if err != nil {
		t.Fatalf("bad url: %v", err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	u, err := a.ResolveUpstreamURL(context.Background(), req, providers.RouteContext{})
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func TestResolveUpstreamURL_StripsBedrockPrefix(t *testing.T) {
	a := newAdapter(t)
	got, err := resolve(t, a, invokePath(claudeModel, "invoke"), nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := stubBase + "/model/" + claudeModel + "/invoke"
	if got != want {
		t.Errorf("upstream url = %q, want %q", got, want)
	}
}

func TestResolveUpstreamURL_RejectsUnknownModel(t *testing.T) {
	a := newAdapter(t)
	if _, err := resolve(t, a, invokePath("evil.unlisted-model", "invoke"), nil); err == nil {
		t.Error("unlisted model should be rejected")
	}
}

func TestResolveUpstreamURL_RejectsUnknownAction(t *testing.T) {
	a := newAdapter(t)
	if _, err := resolve(t, a, invokePath(claudeModel, "start-async-invoke"), nil); err == nil {
		t.Error("unlisted Bedrock action should be rejected")
	}
}

func TestResolveUpstreamURL_RejectsUnknownRegion(t *testing.T) {
	a := newAdapter(t)
	if _, err := resolve(t, a, invokePath(claudeModel, "invoke"), map[string]string{"x-cave-aws-region": "moon-base-1"}); err == nil {
		t.Error("unlisted region should be rejected")
	}
}

func TestResolveUpstreamURL_RejectsRegionHostMismatch(t *testing.T) {
	a := newAdapter(t)
	if _, err := resolve(t, a, invokePath(claudeModel, "invoke"), map[string]string{"x-cave-aws-region": "eu-west-1"}); err == nil {
		t.Error("a client region must not diverge from the resolved endpoint host")
	}
}

func TestResolveUpstreamURL_RejectsMalformedPath(t *testing.T) {
	a := newAdapter(t)
	if _, err := resolve(t, a, "/bedrock/model/"+claudeModel, nil); err == nil {
		t.Error("path without an action should be rejected")
	}
}

func TestResolveUpstreamURL_MantleIsOptInAndUsesOfficialHost(t *testing.T) {
	a := New(RuntimeBaseURL("eu-west-1")).(Adapter)
	path := "/bedrock/anthropic/v1/messages"
	if _, err := resolve(t, a, path, nil); err == nil {
		t.Fatal("Mantle must stay disabled until the deployment explicitly opts in")
	}

	t.Setenv("CAVE_BEDROCK_MANTLE_ENABLED", "true")
	got, err := resolve(t, a, path, map[string]string{"x-cave-aws-region": "eu-west-1"})
	if err != nil {
		t.Fatalf("resolve Mantle: %v", err)
	}
	want := "https://bedrock-mantle.eu-west-1.api.aws/anthropic/v1/messages"
	if got != want {
		t.Fatalf("Mantle upstream = %q, want %q", got, want)
	}
}

func TestResolveUpstreamURL_RejectsConfiguredEndpointKindMismatch(t *testing.T) {
	t.Setenv("CAVE_BEDROCK_MANTLE_ENABLED", "true")
	a := newAdapter(t)
	runtimeReq, _ := http.NewRequest(http.MethodPost, invokePath(claudeModel, "invoke"), nil)
	if _, err := a.ResolveUpstreamURL(context.Background(), runtimeReq, providers.RouteContext{
		BaseURL:      stubBase,
		EndpointKind: "mantle",
	}); err == nil {
		t.Fatal("runtime path accepted a Mantle-configured provider connection")
	}
	mantleReq, _ := http.NewRequest(http.MethodPost, "/bedrock/anthropic/v1/messages", nil)
	if _, err := a.ResolveUpstreamURL(context.Background(), mantleReq, providers.RouteContext{
		BaseURL:      stubBase,
		EndpointKind: "runtime",
	}); err == nil {
		t.Fatal("Mantle path accepted a Runtime-configured provider connection")
	}
}

func TestBedrockHostAndRegionInventory(t *testing.T) {
	tests := []struct {
		host   string
		region string
		ok     bool
	}{
		{"bedrock-runtime.us-east-1.amazonaws.com", "us-east-1", true},
		{"bedrock-runtime-fips.us-gov-west-1.amazonaws.com", "us-gov-west-1", true},
		{"bedrock-mantle.eu-west-1.api.aws", "eu-west-1", true},
		{"bedrock-runtime.evil.amazonaws.com.example.net", "", false},
		{"bedrock-mantle.us-east-1.amazonaws.com", "", false},
	}
	for _, tc := range tests {
		if got := regionFromHost(tc.host); got != tc.region {
			t.Errorf("regionFromHost(%q) = %q, want %q", tc.host, got, tc.region)
		}
		if got := isBedrockHost(tc.host); got != tc.ok {
			t.Errorf("isBedrockHost(%q) = %v, want %v", tc.host, got, tc.ok)
		}
	}
	for _, region := range []string{"us-west-1", "ca-west-1", "eu-south-2", "ap-southeast-7", "me-central-1", "sa-east-1", "us-gov-east-1"} {
		if !regionAllowed(region) {
			t.Errorf("current Bedrock region %q should be allowed", region)
		}
	}
}

func TestEndpointAndAPIKeyRegionInventoriesStayNarrowerThanRuntime(t *testing.T) {
	for _, region := range []string{"us-east-1", "eu-west-1", "ap-southeast-3", "us-gov-west-1"} {
		if !MantleRegionAllowed(region) {
			t.Errorf("current Mantle region %q should be allowed", region)
		}
	}
	for _, region := range []string{"us-west-1", "ca-west-1", "ap-southeast-7", "us-gov-east-1"} {
		if MantleRegionAllowed(region) {
			t.Errorf("unsupported Mantle region %q must fail closed", region)
		}
	}
	for _, region := range []string{"us-east-1", "ap-northeast-3", "eu-central-2", "us-gov-east-1"} {
		if !APIKeyRegionAllowed(region) {
			t.Errorf("current Bedrock API-key region %q should be allowed", region)
		}
	}
	for _, region := range []string{"us-east-2", "us-west-1", "ca-west-1", "ap-southeast-3"} {
		if APIKeyRegionAllowed(region) {
			t.Errorf("unsupported Bedrock API-key region %q must fail closed", region)
		}
	}
}

func TestModelAllowlist_CurrentProfilesAndARNs(t *testing.T) {
	for _, model := range []string{
		"global.anthropic.claude-sonnet-4-6",
		"us.meta.llama4-maverick-17b-instruct-v1:0",
		"eu.amazon.nova-2-lite-v1:0",
		"jp.anthropic.claude-sonnet-4-6",
		"au.anthropic.claude-sonnet-4-6",
		"arn:aws:bedrock:us-east-1:123456789012:application-inference-profile/abc123",
		"arn:aws:bedrock:us-east-1:123456789012:inference-profile/us.anthropic.claude-sonnet-4-6",
		"arn:aws:bedrock:us-east-1:123456789012:provisioned-model/abc123def456",
		"arn:aws:bedrock:us-east-1:123456789012:custom-model-deployment/abc123def456",
		"arn:aws:bedrock:us-east-1:123456789012:imported-model/abc123def456",
		"arn:aws:bedrock:us-east-1:123456789012:custom-model/vendor.model/abc123def456",
		"arn:aws:bedrock:us-east-1:123456789012:prompt/ABCDEFGHIJ:3",
		"arn:aws:bedrock:us-east-1:123456789012:default-prompt-router/router-v1",
		"arn:aws:sagemaker:us-east-1:123456789012:endpoint/marketplace-model",
	} {
		if !modelAllowed(model) {
			t.Errorf("current model/profile %q should be allowed", model)
		}
	}
	for _, model := range []string{
		"global.evil.unlisted-model",
		"anthropic.claudeevil-v1:0",
		"anthropic.claude-good/../evil",
		"arn:aws:s3:us-east-1:123456789012:bucket/not-bedrock",
		"arn:aws:bedrock:us-east-1:bad-account:application-inference-profile/abc123",
		"arn:aws:bedrock:us-east-1::inference-profile/us.anthropic.claude-sonnet-4-6",
		"arn:aws:bedrock:us-east-1:123456789012:foundation-model/anthropic.claude-sonnet-4-6",
		"arn:aws:sagemaker:us-east-1:bad-account:endpoint/marketplace-model",
		"arn:aws:bedrock:us-east-1:123456789012:provisioned-model/../../secret",
	} {
		if modelAllowed(model) {
			t.Errorf("invalid model/profile %q must be rejected", model)
		}
	}
}

func TestParseModelPath_VersionedModelWithColon(t *testing.T) {
	model, action := parseModelPath(invokePath(claudeModel, "converse"))
	if model != claudeModel {
		t.Errorf("model = %q, want %q", model, claudeModel)
	}
	if action != "converse" {
		t.Errorf("action = %q, want converse", action)
	}
}

func TestInspectRequest_ModelFromPath(t *testing.T) {
	a := newAdapter(t)
	h := http.Header{}
	h.Set("x-cave-route-path", invokePath(claudeModel, "converse-stream"))
	meta, err := a.InspectRequest(context.Background(), strings.NewReader(`{"messages":[]}`), h)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if meta.Provider != "bedrock" {
		t.Errorf("provider = %q, want bedrock", meta.Provider)
	}
	if meta.Model != claudeModel {
		t.Errorf("model = %q, want %q", meta.Model, claudeModel)
	}
	if !meta.Stream {
		t.Error("converse-stream path should set Stream=true")
	}
}

func TestInspectRequest_MantleModelFromBody(t *testing.T) {
	t.Setenv("CAVE_BEDROCK_MANTLE_ENABLED", "true")
	a := newAdapter(t)
	h := http.Header{}
	h.Set("x-cave-route-path", "/bedrock/anthropic/v1/messages")
	meta, err := a.InspectRequest(context.Background(), strings.NewReader(`{"model":"anthropic.claude-opus-4-8","stream":true,"messages":[]}`), h)
	if err != nil {
		t.Fatalf("inspect Mantle: %v", err)
	}
	if meta.Model != "anthropic.claude-opus-4-8" || meta.Endpoint != "mantle_messages" || !meta.Stream {
		t.Fatalf("Mantle metadata = %+v", meta)
	}
}

func TestInspectRequest_MantleRejectsNonAnthropicMessageModel(t *testing.T) {
	t.Setenv("CAVE_BEDROCK_MANTLE_ENABLED", "true")
	a := newAdapter(t)
	h := http.Header{}
	h.Set("x-cave-route-path", "/bedrock/anthropic/v1/messages")
	if _, err := a.InspectRequest(context.Background(), strings.NewReader(`{"model":"mistral.mistral-large-3-675b-instruct","messages":[]}`), h); err == nil {
		t.Fatal("the Anthropic-wire Mantle lane must reject non-Anthropic models")
	}
}

func TestInspectRequest_GuardrailChargeFailsPricingClosed(t *testing.T) {
	a := newAdapter(t)
	h := http.Header{}
	h.Set("x-cave-route-path", invokePath(claudeModel, "converse"))
	h.Set("x-amzn-bedrock-guardrail-identifier", "guardrail-1")
	meta, err := a.InspectRequest(context.Background(), strings.NewReader(`{"messages":[]}`), h)
	if err != nil {
		t.Fatal(err)
	}
	if meta.PricingUnsupportedReason != "unsupported_provider_guardrail_charge" {
		t.Fatalf("pricing reason = %q", meta.PricingUnsupportedReason)
	}
}

// --- SigV4 header construction + NO secret leak --------------------------

const testSecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"

func TestSanitizeAndMapHeaders_BuildsSigV4AndNeverLeaksSecret(t *testing.T) {
	a := newAdapter(t)
	req, _ := http.NewRequest(http.MethodPost, invokePath(claudeModel, "invoke"), nil)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-cave-aws-region", "us-east-1")
	// Inbound client headers that MUST NOT leak upstream.
	req.Header.Set("authorization", "Bearer CLIENT-CAVE-KEY")
	req.Header.Set("x-cave-api-key", "cave_live_clientclient_secret")

	cred := providers.Credential{Key: "AKIAIOSFODNN7EXAMPLE:" + testSecret + ":session-tok", AuthKind: "aws_access_keys"}
	upstream, _ := a.ResolveUpstreamURL(context.Background(), req, providers.RouteContext{})
	out, err := a.SanitizeAndMapHeaders(context.Background(), req, cred, upstream)
	if err != nil {
		t.Fatalf("sanitize: %v", err)
	}

	auth := out.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/") {
		t.Errorf("Authorization not a SigV4 header: %q", auth)
	}
	if !strings.Contains(auth, "/us-east-1/bedrock/aws4_request") {
		t.Errorf("Authorization scope wrong: %q", auth)
	}
	if !strings.Contains(auth, "Signature=") {
		t.Errorf("Authorization missing signature: %q", auth)
	}
	if out.Get("X-Amz-Date") == "" {
		t.Error("X-Amz-Date not set")
	}
	if out.Get("X-Amz-Security-Token") != "session-tok" {
		t.Errorf("session token = %q, want session-tok", out.Get("X-Amz-Security-Token"))
	}
	if out.Get("X-Amz-Content-Sha256") != awssig.HashPayload(nil) {
		t.Errorf("content-sha256 = %q, want SHA-256 of the exact empty body", out.Get("X-Amz-Content-Sha256"))
	}
	// Host signed against the upstream bedrock host, not the inbound path.
	if out.Get("Host") != "bedrock-runtime.us-east-1.amazonaws.com" {
		t.Errorf("Host = %q, want bedrock-runtime.us-east-1.amazonaws.com", out.Get("Host"))
	}

	// SECURITY: the secret access key must NEVER appear in any forwarded header.
	for name, vals := range out {
		for _, v := range vals {
			if strings.Contains(v, testSecret) {
				t.Fatalf("AWS secret leaked into upstream header %s = %q", name, v)
			}
			if v == "Bearer CLIENT-CAVE-KEY" || strings.Contains(v, "cave_live_clientclient_secret") {
				t.Errorf("client caveman key leaked into header %s = %q", name, v)
			}
		}
	}
	if out.Get("x-cave-api-key") != "" {
		t.Error("x-cave-api-key forwarded upstream")
	}
}

func TestSanitizeAndMapHeaders_SignsBoundWirePayloadAndRuntimeMetadata(t *testing.T) {
	a := newAdapter(t)
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	req, _ := http.NewRequest(http.MethodPost, invokePath(claudeModel, "converse"), nil)
	req.Header.Set("x-amzn-bedrock-trace", "ENABLED_FULL")
	req.Header.Set("x-amzn-bedrock-performanceconfig-latency", "optimized")
	req.Header.Set("x-amzn-bedrock-request-metadata", `{"cave_project":"project-safe"}`)
	upstream, err := a.ResolveUpstreamURL(context.Background(), req, providers.RouteContext{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := providers.WithRequestPayloadHash(context.Background(), body)
	out, err := a.SanitizeAndMapHeaders(ctx, req, providers.Credential{
		Key:      "AKIAIOSFODNN7EXAMPLE:" + testSecret,
		AuthKind: "aws_access_keys",
	}, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Get("X-Amz-Content-Sha256"); got != awssig.HashPayload(body) {
		t.Fatalf("payload hash = %q, want exact transformed wire body", got)
	}
	for name, want := range map[string]string{
		"x-amzn-bedrock-trace":                     "ENABLED_FULL",
		"x-amzn-bedrock-performanceconfig-latency": "optimized",
		"x-amzn-bedrock-request-metadata":          `{"cave_project":"project-safe"}`,
	} {
		if got := out.Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	if auth := out.Get("Authorization"); !strings.Contains(strings.ToLower(auth), "x-amzn-bedrock-request-metadata") {
		t.Fatalf("request metadata header was not covered by SigV4: %q", auth)
	}
}

func TestSanitizeAndMapHeaders_IAMRejectsUnknownWirePayload(t *testing.T) {
	a := newAdapter(t)
	req, _ := http.NewRequest(http.MethodPost, invokePath(claudeModel, "invoke"), strings.NewReader(`{"prompt":"hello"}`))
	req.GetBody = nil
	upstream, err := a.ResolveUpstreamURL(context.Background(), req, providers.RouteContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.SanitizeAndMapHeaders(context.Background(), req, providers.Credential{
		Key:      "AKIAIOSFODNN7EXAMPLE:" + testSecret,
		AuthKind: "aws_access_keys",
	}, upstream); err == nil {
		t.Fatal("IAM signing accepted a non-replayable body without an exact bound payload hash")
	}
}

func TestSanitizeAndMapHeaders_BedrockAPIKeyUsesBearer(t *testing.T) {
	a := newAdapter(t)
	req, _ := http.NewRequest(http.MethodPost, invokePath(claudeModel, "converse"), nil)
	upstream, _ := a.ResolveUpstreamURL(context.Background(), req, providers.RouteContext{})
	out, err := a.SanitizeAndMapHeaders(context.Background(), req, providers.Credential{
		Key:      "bedrock-api-key-secret",
		AuthKind: "bedrock_api_key",
	}, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Get("Authorization"); got != "Bearer bedrock-api-key-secret" {
		t.Fatalf("Authorization = %q", got)
	}
	if out.Get("X-Amz-Date") != "" || out.Get("X-Amz-Content-Sha256") != "" {
		t.Fatal("Bedrock API key request must not be SigV4 signed")
	}
}

func TestSanitizeAndMapHeaders_MantleAuthShapes(t *testing.T) {
	t.Setenv("CAVE_BEDROCK_MANTLE_ENABLED", "true")
	a := newAdapter(t)
	req, _ := http.NewRequest(http.MethodPost, "/bedrock/anthropic/v1/messages", nil)
	req.Header.Set("anthropic-version", "2023-06-01")
	upstream, err := a.ResolveUpstreamURL(context.Background(), req, providers.RouteContext{})
	if err != nil {
		t.Fatal(err)
	}

	apiKeyHeaders, err := a.SanitizeAndMapHeaders(context.Background(), req, providers.Credential{
		Key:      "mantle-api-key-secret",
		AuthKind: "bedrock_api_key",
	}, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if got := apiKeyHeaders.Get("x-api-key"); got != "mantle-api-key-secret" {
		t.Fatalf("Mantle x-api-key = %q", got)
	}
	if apiKeyHeaders.Get("Authorization") != "" {
		t.Fatal("Mantle API key must not be remapped to Authorization")
	}
	if got := apiKeyHeaders.Get("anthropic-version"); got != "2023-06-01" {
		t.Fatalf("anthropic-version = %q", got)
	}

	signed, err := a.SanitizeAndMapHeaders(context.Background(), req, providers.Credential{
		Key:      "AKIAIOSFODNN7EXAMPLE:" + testSecret,
		AuthKind: "aws_access_keys",
	}, upstream)
	if err != nil {
		t.Fatal(err)
	}
	if auth := signed.Get("Authorization"); !strings.Contains(auth, "/bedrock-mantle/aws4_request") {
		t.Fatalf("Mantle SigV4 service scope = %q", auth)
	}
}

func TestSanitizeAndMapHeaders_FailsClosedWithoutCredentials(t *testing.T) {
	a := newAdapter(t)
	req, _ := http.NewRequest(http.MethodPost, invokePath(claudeModel, "invoke"), nil)
	if _, err := a.SanitizeAndMapHeaders(context.Background(), req, providers.Credential{Key: ""}, nil); err == nil {
		t.Error("missing AWS credentials must fail closed, not pass through unsigned")
	}
	if _, err := a.SanitizeAndMapHeaders(context.Background(), req, providers.Credential{Key: "AKIAONLY", AuthKind: "aws_access_keys"}, nil); err == nil {
		t.Error("credentials without a secret must fail closed")
	}
	if _, err := a.SanitizeAndMapHeaders(context.Background(), req, providers.Credential{Key: "secret", AuthKind: "unknown_kind"}, nil); err == nil {
		t.Error("unknown explicit Bedrock auth kind must fail closed")
	}
}
