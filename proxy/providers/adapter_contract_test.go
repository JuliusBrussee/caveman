package providers

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestMatchRoute(t *testing.T) {
	b := Base{Provider: "openai", Routes: []string{"/v1/responses", "/openai/v1/responses"}}
	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodPost, "/v1/responses", true},
		{http.MethodPost, "/openai/v1/responses", true},
		{http.MethodPost, "/v1/responses/extra", false}, // exact route stays closed
		{http.MethodPost, "/compat/acme/v1/responses", false},
		{http.MethodGet, "/v1/responses", false}, // only POST
		{http.MethodPost, "/v1/chat", false},     // not a registered route
	}
	for _, c := range cases {
		if got := b.MatchRoute(c.method, c.path); got != c.want {
			t.Errorf("MatchRoute(%s,%s) = %v, want %v", c.method, c.path, got, c.want)
		}
	}
	prefix := Base{Provider: "openai_compatible", Routes: []string{"/compat/"}}
	if !prefix.MatchRoute(http.MethodPost, "/compat/acme/v1/responses") {
		t.Error("explicit subtree route did not match")
	}
}

func TestInspectRequestFailsPricingClosedForNonTokenCharges(t *testing.T) {
	tests := []struct {
		name, provider, body, reason string
	}{
		{"openai hosted search", "openai", `{"model":"gpt-5.5","tools":[{"type":"web_search_preview"}]}`, "unsupported_provider_tool_charge"},
		{"openai background", "openai", `{"model":"gpt-5.5","background":true}`, "unsupported_background_response"},
		{"gemini grounding", "gemini", `{"contents":[],"tools":[{"googleSearch":{}}]}`, "unsupported_provider_tool_charge"},
		{"gemini snake grounding", "gemini", `{"contents":[],"tools":[{"google_maps":{}}]}`, "unsupported_provider_tool_charge"},
		{"gemini code execution has no separate fee", "gemini", `{"contents":[],"tools":[{"codeExecution":{}}]}`, ""},
		{"gemini explicit cache storage", "gemini", `{"cachedContent":"cachedContents/abc","contents":[]}`, "unsupported_cache_storage_charge"},
		{"bedrock optimized latency", "bedrock", `{"performanceConfig":{"latency":"optimized"}}`, "unsupported_performance_tier"},
		{"bedrock explicit standard latency", "bedrock", `{"performanceConfig":{"latency":"standard"}}`, ""},
		{"bedrock guardrail body", "bedrock", `{"guardrailConfig":{"guardrailIdentifier":"g","guardrailVersion":"1"}}`, "unsupported_provider_guardrail_charge"},
		{"bedrock nonstandard service tier object", "bedrock", `{"serviceTier":{"type":"flex"}}`, ""},
		{"anthropic fast mode", "anthropic", `{"speed":"fast"}`, "unsupported_speed_tier"},
		{"anthropic web fetch is token only", "anthropic", `{"tools":[{"type":"web_fetch_20260318","name":"web_fetch"}]}`, ""},
		{"anthropic web search has per-call fee", "anthropic", `{"tools":[{"type":"web_search_20260318","name":"web_search"}]}`, "unsupported_provider_tool_charge"},
		{"anthropic fallback", "anthropic", `{"fallbacks":["claude-haiku-4-5"]}`, "unsupported_model_fallback_pricing"},
		{"nested audio", "gemini", `{"contents":[{"parts":[{"inlineData":{"mimeType":"audio/wav","data":"AA=="}}]}]}`, "unsupported_audio_tokens"},
		{"custom function stays token priced", "openai", `{"tools":[{"type":"function","function":{"name":"lookup"}}]}`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := Base{Provider: tc.provider}
			meta, err := base.InspectRequest(context.Background(), strings.NewReader(tc.body), http.Header{})
			if err != nil {
				t.Fatal(err)
			}
			if meta.PricingUnsupportedReason != tc.reason {
				t.Fatalf("reason = %q, want %q", meta.PricingUnsupportedReason, tc.reason)
			}
			if tc.name == "bedrock nonstandard service tier object" && meta.ServiceTier != "flex" {
				t.Fatalf("service tier = %q, want flex", meta.ServiceTier)
			}
		})
	}
}

func TestListPriceEligibilityAndResolvedOpenAIRegion(t *testing.T) {
	for _, tc := range []struct {
		provider, mode string
		want           bool
	}{
		{"openai", "api_key", true},
		{"vertex", "oauth", true},
		{"openai", "oauth", false},
		{"vertex", "subscription", false},
		{"vertex", "unknown", false},
	} {
		if got := ListPriceEligible(tc.provider, tc.mode); got != tc.want {
			t.Errorf("ListPriceEligible(%q,%q) = %v, want %v", tc.provider, tc.mode, got, tc.want)
		}
	}
	u, _ := url.Parse("https://eu.api.openai.com/v1/responses")
	meta := ApplyResolvedPricingRoute(RequestMetadata{Provider: "openai"}, u)
	if meta.Region != "eu" {
		t.Fatalf("resolved OpenAI region = %q, want eu", meta.Region)
	}
	for _, raw := range []string{
		"https://bedrock-runtime.eu-west-1.amazonaws.com/model/x/converse",
		"https://bedrock-runtime-fips.us-gov-west-1.amazonaws.com/model/x/converse",
		"https://bedrock-mantle.ap-southeast-2.api.aws/anthropic/v1/messages",
	} {
		bedrockURL, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		want := strings.Split(bedrockURL.Hostname(), ".")[1]
		resolved := ApplyResolvedPricingRoute(RequestMetadata{Provider: "bedrock"}, bedrockURL)
		if resolved.Region != want {
			t.Errorf("%s: resolved Bedrock region = %q, want %q", raw, resolved.Region, want)
		}
	}
}

// Header sanitization is the security-critical contract: the inbound client's
// Caveman key / authorization must NOT be forwarded upstream; only the
// explicitly-resolved upstream credential is mapped, per provider.
func TestSanitizeAndMapHeaders_NoKeyLeakAndCorrectMapping(t *testing.T) {
	cases := []struct {
		provider   string
		credHeader string // the header the upstream credential should land in
	}{
		{"openai", "authorization"},
		{"anthropic", "x-api-key"},
		{"gemini", "x-goog-api-key"},
		{"azure_openai", "api-key"},
		{"openai_compatible", "authorization"},
	}
	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			b := Base{Provider: c.provider}
			req, _ := http.NewRequest(http.MethodPost, "/x", nil)
			// Inbound client headers that MUST NOT leak upstream:
			req.Header.Set("authorization", "Bearer CLIENT-CAVE-KEY")
			req.Header.Set("x-cave-api-key", "cave_live_clientclient_secret")
			req.Header.Set("x-cave-upstream-key", "should-not-be-copied-verbatim")
			req.Header.Set("content-type", "application/json")
			req.Header.Set("x-internal-secret", "nope")

			out, err := b.SanitizeAndMapHeaders(req.Context(), req, Credential{Key: "UPSTREAM-PROVIDER-KEY"}, nil)
			if err != nil {
				t.Fatalf("sanitize: %v", err)
			}

			// The upstream credential is mapped to the provider's header.
			got := out.Get(c.credHeader)
			switch c.credHeader {
			case "authorization":
				if got != "Bearer UPSTREAM-PROVIDER-KEY" {
					t.Errorf("%s = %q, want Bearer UPSTREAM-PROVIDER-KEY", c.credHeader, got)
				}
			default:
				if got != "UPSTREAM-PROVIDER-KEY" {
					t.Errorf("%s = %q, want UPSTREAM-PROVIDER-KEY", c.credHeader, got)
				}
			}

			// The client's Caveman key must NOT appear anywhere upstream.
			for name, vals := range out {
				for _, v := range vals {
					if v == "Bearer CLIENT-CAVE-KEY" || v == "cave_live_clientclient_secret" {
						t.Errorf("client key leaked into upstream header %s = %q", name, v)
					}
				}
			}
			// x-cave-* and arbitrary inbound secrets are not forwarded.
			if out.Get("x-cave-api-key") != "" {
				t.Error("x-cave-api-key forwarded upstream")
			}
			if out.Get("x-cave-upstream-key") != "" {
				t.Error("x-cave-upstream-key forwarded upstream")
			}
			if out.Get("x-internal-secret") != "" {
				t.Error("non-allowlisted x-internal-secret forwarded upstream")
			}
			// Allowlisted passthrough still works.
			if out.Get("content-type") != "application/json" {
				t.Error("content-type should pass through")
			}
		})
	}
}

func TestSanitizeAndMapHeaders_AzureRejectsBearerUntilAuthContractExists(t *testing.T) {
	b := Base{Provider: "azure_openai"}
	req, _ := http.NewRequest(http.MethodPost, "/x", nil)
	if _, err := b.SanitizeAndMapHeaders(req.Context(), req, Credential{Key: "access-token", Scheme: "bearer"}, nil); err == nil {
		t.Fatal("Azure bearer credential should fail closed")
	}
	if _, err := b.SanitizeAndMapHeaders(req.Context(), req, Credential{Key: "Bearer access-token"}, nil); err == nil {
		t.Fatal("Azure bearer-shaped credential should fail closed")
	}
	if _, err := b.SanitizeAndMapHeaders(req.Context(), req, Credential{Key: "eyJheader.payload.signature"}, nil); err == nil {
		t.Fatal("Azure JWT credential should fail closed")
	}
}

func TestSanitizeAndMapHeaders_AzurePlaceholderBearerDefersToFallback(t *testing.T) {
	b := Base{Provider: "azure_openai"}
	req, _ := http.NewRequest(http.MethodPost, "/x", nil)

	out, err := b.SanitizeAndMapHeaders(req.Context(), req, Credential{Key: "no-key-required", Scheme: "bearer"}, nil)
	if err != nil {
		t.Fatalf("Azure placeholder bearer should reach the gateway fallback seam: %v", err)
	}
	if got := out.Get("authorization"); got != "" {
		t.Errorf("placeholder authorization = %q, want empty", got)
	}
	if got := out.Get("api-key"); got != "" {
		t.Errorf("placeholder api-key = %q, want empty before fallback", got)
	}
}

func TestSanitizeAndMapHeaders_TracePropagationIsOptIn(t *testing.T) {
	b := Base{Provider: "openai"}
	req, _ := http.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	req.Header.Set("tracestate", "vendor=value")

	t.Setenv("CAVE_PROPAGATE_TRACE_HEADERS_UPSTREAM", "false")
	out, err := b.SanitizeAndMapHeaders(req.Context(), req, Credential{}, nil)
	if err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	if out.Get("traceparent") != "" || out.Get("tracestate") != "" {
		t.Fatal("trace context propagated without explicit opt-in")
	}

	t.Setenv("CAVE_PROPAGATE_TRACE_HEADERS_UPSTREAM", "true")
	out, err = b.SanitizeAndMapHeaders(req.Context(), req, Credential{}, nil)
	if err != nil {
		t.Fatalf("sanitize opt-in: %v", err)
	}
	if out.Get("traceparent") != req.Header.Get("traceparent") || out.Get("tracestate") != "vendor=value" {
		t.Fatal("explicit trace propagation did not preserve validated context")
	}
}

func TestMapProviderError(t *testing.T) {
	b := Base{Provider: "openai"}
	pe := b.MapProviderError(http.StatusTooManyRequests, http.Header{}, []byte(`{"error":"rate"}`))
	if pe.Code != "provider_429" {
		t.Errorf("error code = %q, want provider_429", pe.Code)
	}
	if pe.Message == "" {
		t.Error("error message should carry the provider body")
	}
}

// TestSanitizeAndMapHeaders_AnthropicBearerSchemePreserved proves scheme
// preservation for Anthropic: a key that arrived as `Authorization: Bearer`
// (Claude Pro/Max OAuth, ANTHROPIC_AUTH_TOKEN setups) goes upstream as a
// Bearer, never remapped to x-api-key — OAuth tokens 401 under x-api-key.
func TestSanitizeAndMapHeaders_AnthropicBearerSchemePreserved(t *testing.T) {
	b := Base{Provider: "anthropic"}
	req, _ := http.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")

	out, err := b.SanitizeAndMapHeaders(req.Context(), req, Credential{Key: "OAUTH-TOKEN", Scheme: "bearer"}, nil)
	if err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	if got := out.Get("authorization"); got != "Bearer OAUTH-TOKEN" {
		t.Errorf("authorization = %q, want Bearer OAUTH-TOKEN", got)
	}
	if got := out.Get("x-api-key"); got != "" {
		t.Errorf("x-api-key = %q, want empty (bearer scheme must not be remapped)", got)
	}
	// The OAuth beta header the agent sent must survive the sanitize pass.
	if got := out.Get("anthropic-beta"); got != "oauth-2025-04-20" {
		t.Errorf("anthropic-beta = %q, want oauth-2025-04-20", got)
	}

	// Default scheme (BYOK / inbound x-api-key) keeps the pre-existing mapping.
	out, err = b.SanitizeAndMapHeaders(req.Context(), req, Credential{Key: "API-KEY"}, nil)
	if err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	if got := out.Get("x-api-key"); got != "API-KEY" {
		t.Errorf("x-api-key = %q, want API-KEY", got)
	}
	if got := out.Get("authorization"); got != "" {
		t.Errorf("authorization = %q, want empty for default scheme", got)
	}
}

func TestSanitizeAndMapHeaders_GeminiBearerRequiresQuotaProject(t *testing.T) {
	b := Base{Provider: "gemini"}
	req, _ := http.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:generateContent", nil)
	req.Header.Set("x-goog-user-project", "my-quota-project")

	out, err := b.SanitizeAndMapHeaders(req.Context(), req, Credential{Key: "OAUTH-TOKEN", Scheme: "bearer"}, nil)
	if err != nil {
		t.Fatalf("sanitize Gemini OAuth: %v", err)
	}
	if got := out.Get("authorization"); got != "Bearer OAUTH-TOKEN" {
		t.Fatalf("authorization=%q", got)
	}
	if got := out.Get("x-goog-user-project"); got != "my-quota-project" {
		t.Fatalf("x-goog-user-project=%q", got)
	}

	missing, _ := http.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:generateContent", nil)
	if _, err := b.SanitizeAndMapHeaders(missing.Context(), missing, Credential{Key: "OAUTH-TOKEN", Scheme: "bearer"}, nil); err == nil {
		t.Fatal("Gemini OAuth without x-goog-user-project should fail closed")
	}
}

func TestSanitizeAndMapHeaders_GeminiAPIKeyDropsQuotaProject(t *testing.T) {
	b := Base{Provider: "gemini"}
	req, _ := http.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:generateContent", nil)
	req.Header.Set("x-goog-user-project", "attacker-project")

	out, err := b.SanitizeAndMapHeaders(req.Context(), req, Credential{Key: "GEMINI-API-KEY"}, nil)
	if err != nil {
		t.Fatalf("sanitize Gemini API key: %v", err)
	}
	if got := out.Get("x-goog-api-key"); got != "GEMINI-API-KEY" {
		t.Fatalf("x-goog-api-key=%q", got)
	}
	if got := out.Get("x-goog-user-project"); got != "" {
		t.Fatalf("API-key request forwarded caller quota project %q", got)
	}
}

func TestSanitizeAndMapHeaders_GeminiPlaceholderDefersToFallback(t *testing.T) {
	b := Base{Provider: "gemini"}
	req, _ := http.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:generateContent", nil)

	out, err := b.SanitizeAndMapHeaders(req.Context(), req, Credential{Key: "no-key-required", Scheme: "bearer"}, nil)
	if err != nil {
		t.Fatalf("Gemini placeholder should reach env fallback: %v", err)
	}
	if out.Get("authorization") != "" || out.Get("x-goog-api-key") != "" || out.Get("x-goog-user-project") != "" {
		t.Fatalf("placeholder leaked upstream auth headers: %#v", out)
	}
}
