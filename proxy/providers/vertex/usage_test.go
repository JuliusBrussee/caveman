package vertex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/shared/platform/catalog"
	"github.com/JuliusBrussee/caveman/shared/platform/cost"
)

// Gemini on Vertex returns usage in usageMetadata (camelCase). The shared
// ParseUsageBytes understands this shape, so the Vertex adapter needs no custom
// parser — this test proves the inherited path works end to end.
func TestParseUsage_GeminiUsageMetadata(t *testing.T) {
	a := newAdapter(t)
	body := `{"candidates":[{"content":{"parts":[{"text":"hi"}]}}],` +
		`"usageMetadata":{"promptTokenCount":1200,"candidatesTokenCount":340,"totalTokenCount":1580,"cachedContentTokenCount":600,"thoughtsTokenCount":40}}`
	usage, _, err := a.ParseUsage(context.Background(), http.Header{}, strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if usage.InputTokens != 1200 || usage.OutputTokens != 380 {
		t.Errorf("tokens = %d/%d, want normalized total 1200/380", usage.InputTokens, usage.OutputTokens)
	}
	if usage.CachedInputTokens != 600 {
		t.Errorf("cached = %d, want 600", usage.CachedInputTokens)
	}
	if usage.ReasoningTokens != 40 {
		t.Errorf("reasoning = %d, want 40", usage.ReasoningTokens)
	}
	if usage.CacheStatus != "hit" {
		t.Errorf("cache_status = %q, want hit", usage.CacheStatus)
	}
}

// Claude on Vertex returns the native Anthropic Messages usage (snake_case),
// including cache telemetry. Also understood by the shared parser.
func TestParseUsage_ClaudeMessagesSnakeCaseWithCacheHit(t *testing.T) {
	a := newAdapter(t)
	body := `{"id":"msg_x","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],` +
		`"usage":{"input_tokens":200,"output_tokens":120,"cache_read_input_tokens":900,"cache_creation_input_tokens":0}}`
	usage, _, err := a.ParseUsage(context.Background(), http.Header{}, strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if usage.InputTokens != 1100 || usage.OutputTokens != 120 {
		t.Errorf("tokens = %d/%d, want normalized total 1100/120", usage.InputTokens, usage.OutputTokens)
	}
	if usage.CachedInputTokens != 900 {
		t.Errorf("cached = %d, want 900", usage.CachedInputTokens)
	}
	if usage.CacheStatus != "hit" {
		t.Errorf("cache_status = %q, want hit", usage.CacheStatus)
	}
}

// No cache telemetry -> cache_status stays the honest "unknown".
func TestParseUsage_GeminiNoCacheStaysUnknown(t *testing.T) {
	a := newAdapter(t)
	body := `{"usageMetadata":{"promptTokenCount":500,"candidatesTokenCount":50,"totalTokenCount":550}}`
	usage, _, err := a.ParseUsage(context.Background(), http.Header{}, strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if usage.CacheStatus != "unknown" {
		t.Errorf("cache_status = %q, want unknown (no cache telemetry)", usage.CacheStatus)
	}
}

// Catalog cost for a Vertex model in the catalog must be non-zero (the catalog
// entries were added with this adapter), proving usage+cost wiring.
func TestUsageAndCost_NonZeroForCatalogModels(t *testing.T) {
	for _, model := range []string{geminiModel, claudeModel} {
		price, version := catalog.Price("vertex", model)
		if strings.HasPrefix(version, "unpriced") {
			t.Fatalf("catalog missing vertex model %q (version=%q)", model, version)
		}
		if price.InputPerMillion <= 0 || price.OutputPerMillion <= 0 {
			t.Fatalf("catalog vertex price not set for %q: %+v", model, price)
		}
	}
}

// End-to-end: a Vertex request routed through the adapter against an
// aiplatform-shaped httptest stub records a truthful usage row — tokens and cost
// non-zero, cache_status honest, bearer token forwarded. Exercises
// ResolveUpstreamURL + (inherited) SanitizeAndMapHeaders + the real upstream
// call + ParseUsage together, for both model families.
func TestVertex_EndToEndTruthfulUsage(t *testing.T) {
	var sawAuth string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("content-type", "application/json")
		w.Header().Set("x-request-id", "vertex-stub-1")
		w.WriteHeader(http.StatusOK)
		if strings.Contains(r.URL.Path, "publishers/anthropic") {
			_, _ = w.Write([]byte(`{"id":"msg_x","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],` +
				`"usage":{"input_tokens":1500,"output_tokens":420,"cache_read_input_tokens":600,"cache_creation_input_tokens":0}}`))
			return
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"hi"}]}}],` +
			`"usageMetadata":{"promptTokenCount":1500,"candidatesTokenCount":420,"totalTokenCount":1920,"cachedContentTokenCount":600}}`))
	}))
	defer stub.Close()

	a := New(stub.URL).(Adapter)

	cases := []struct {
		publisher string
		model     string
		method    string
		body      string
	}{
		{"google", geminiModel, "generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`},
		{"anthropic", claudeModel, "rawPredict", `{"anthropic_version":"vertex-2023-10-16","messages":[{"role":"user","content":"hi"}],"max_tokens":100}`},
	}
	for _, tc := range cases {
		path := predictPath(tc.publisher, tc.model, tc.method)
		inbound, _ := http.NewRequest(http.MethodPost, path, strings.NewReader(tc.body))
		inbound.Header.Set("content-type", "application/json")
		inbound.Header.Set("x-cave-route-path", path)

		upstreamURL, err := a.ResolveUpstreamURL(context.Background(), inbound, providers.RouteContext{})
		if err != nil {
			t.Fatalf("%s resolve: %v", tc.publisher, err)
		}
		headers, err := a.SanitizeAndMapHeaders(context.Background(), inbound, providers.Credential{Key: "ya29.vertex-access-token"}, nil)
		if err != nil {
			t.Fatalf("%s sanitize: %v", tc.publisher, err)
		}

		upReq, _ := http.NewRequest(http.MethodPost, upstreamURL.String(), strings.NewReader(tc.body))
		upReq.Header = headers
		resp, err := http.DefaultClient.Do(upReq)
		if err != nil {
			t.Fatalf("%s upstream call: %v", tc.publisher, err)
		}

		if !strings.HasPrefix(sawAuth, "Bearer ") {
			t.Errorf("%s: stub did not receive a Bearer Authorization header: %q", tc.publisher, sawAuth)
		}

		usage, _, err := a.ParseUsage(context.Background(), resp.Header, resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("%s parse usage: %v", tc.publisher, err)
		}
		if usage.InputTokens == 0 || usage.OutputTokens == 0 {
			t.Fatalf("%s: usage tokens are zero: %+v", tc.publisher, usage)
		}
		if usage.CachedInputTokens != 600 {
			t.Errorf("%s: cached = %d, want 600", tc.publisher, usage.CachedInputTokens)
		}
		if usage.CacheStatus != "hit" {
			t.Errorf("%s: cache_status = %q, want hit", tc.publisher, usage.CacheStatus)
		}
		if usage.ProviderRequestID != "vertex-stub-1" {
			t.Errorf("%s: provider request id = %q, want vertex-stub-1", tc.publisher, usage.ProviderRequestID)
		}

		price, _ := catalog.Price("vertex", tc.model)
		total := cost.EstimateUSD(price, cost.Usage{
			InputTokens:       usage.InputTokens - usage.CachedInputTokens,
			OutputTokens:      usage.OutputTokens,
			CachedInputTokens: usage.CachedInputTokens,
		})
		if total <= 0 {
			t.Fatalf("%s: cost = %v, want > 0 (truthful spend)", tc.publisher, total)
		}
	}
}
