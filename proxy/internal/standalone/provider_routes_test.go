package standalone

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/config"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

type countTransformGuard struct{ t *testing.T }

func (g countTransformGuard) CompressSegment([]byte) ([]byte, int, int) {
	g.t.Error("token-count request reached content compression")
	return []byte("changed"), 1000, 1
}

func (g countTransformGuard) StoreOriginal([]byte) (string, error) {
	g.t.Error("token-count request stored a recovery original")
	return "unused", nil
}

func (g countTransformGuard) StripToolSchema([]byte) ([]byte, bool) {
	g.t.Error("token-count request reached schema stripping")
	return []byte(`[]`), true
}

func TestStandaloneTokenCountRoutesPreserveAccountingRequests(t *testing.T) {
	longText := strings.Repeat("a caller's original text to count ", 40)
	openaiBody := `{"model":"gpt-5.5","instructions":"stable prefix","input":[{"role":"user","content":"` + longText + `"}],"tools":[{"type":"function","name":"test","parameters":{"type":"object","title":"count this title"}}]}`
	geminiBody := `{"contents":[{"role":"user","parts":[{"text":"` + longText + `"}]}]}`
	bedrockBody := `{"input":{"converse":{"system":[{"text":"stable prefix"}],"messages":[{"role":"user","content":[{"text":"` + longText + `"}]}]}}}`
	for _, tc := range []struct{ provider, path, body, response string }{
		{"openai", "/openai/v1/responses/input_tokens", openaiBody, `{"object":"response.input_tokens","input_tokens":1234}`},
		{"openai", "/v1/responses/input_tokens", openaiBody, `{"object":"response.input_tokens","input_tokens":1234}`},
		{"gemini", "/gemini/v1/models/gemini-2.5-pro:countTokens", geminiBody, `{"totalTokens":1234}`},
		{"gemini", "/v1/models/gemini-2.5-pro:countTokens", geminiBody, `{"totalTokens":1234}`},
		{"gemini", "/gemini/v1beta/models/gemini-2.5-pro:countTokens", geminiBody, `{"totalTokens":1234}`},
		{"gemini", "/v1beta/models/gemini-2.5-pro:countTokens", geminiBody, `{"totalTokens":1234}`},
		{"bedrock", "/bedrock/model/global.anthropic.claude-sonnet-4-6/count-tokens", bedrockBody, `{"inputTokens":1234}`},
		{"vertex", "/vertex/v1/projects/test/locations/us-central1/publishers/google/models/gemini-2.5-pro:countTokens", geminiBody, `{"totalTokens":1234,"totalBillableCharacters":4567}`},
	} {
		for _, mode := range []string{"record", "active", "compress", "pixel"} {
			t.Run(tc.path+"/"+mode, func(t *testing.T) {
				upstream := &captureUpstreamTransport{response: tc.response}
				spend, err := store.Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer spend.Close()
				sink := &recordingSink{inner: spend}
				srv := New(config.Config{Mode: mode, ToolSchemaStrip: "annotations", Providers: map[string]config.ProviderConfig{
					tc.provider: {BaseURL: "https://upstream.test"},
				}, Optimizers: map[string]bool{"openai-prompt-cache-key": true, "bedrock-cache-points": true, "tool-schema-strip": true}}, sink,
					Options{HTTPClient: &http.Client{Transport: upstream}, Compressor: countTransformGuard{t}, RecoveryViaMCP: true, PrefixCache: spend})
				req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
				req.Header.Set("x-api-key", "sk-test-provider-key")
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("count route blocked: status=%d body=%s", rec.Code, rec.Body.String())
				}
				if string(upstream.body) != tc.body || rec.Body.String() != tc.response {
					t.Fatalf("count request/response bytes changed: upstream=%s response=%s", upstream.body, rec.Body.String())
				}
				wantPath := tc.path
				for _, prefix := range []string{"/openai", "/gemini", "/bedrock", "/vertex"} {
					wantPath = strings.TrimPrefix(wantPath, prefix)
				}
				if upstream.url != "https://upstream.test"+wantPath {
					t.Fatalf("count endpoint changed: %q", upstream.url)
				}
				if sink.last.TotalCostUSD != 0 || sink.last.SavingsUSD != 0 || sink.last.CompressionTokensBefore != 0 || sink.last.InputTokens != 0 || sink.last.OutputTokens != 0 || sink.last.TokenUsageBasis != "unavailable" {
					t.Fatalf("count endpoint claimed generated model usage/spend: %+v", sink.last)
				}
			})
		}
	}
}

func TestStandaloneGeminiStableGenerationRoutes(t *testing.T) {
	for _, prefix := range []string{"/gemini/v1", "/v1"} {
		t.Run(prefix, func(t *testing.T) {
			const body = `{"contents":[{"parts":[{"text":"hello"}]}]}`
			const response = `{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`
			upstream := &captureUpstreamTransport{response: response}
			spend, err := store.Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer spend.Close()
			sink := &recordingSink{inner: spend}
			srv := New(config.Config{Mode: "record", Providers: map[string]config.ProviderConfig{"gemini": {BaseURL: "https://upstream.test"}}}, sink, Options{HTTPClient: &http.Client{Transport: upstream}})
			req := httptest.NewRequest(http.MethodPost, prefix+"/models/gemini-2.5-pro:generateContent", strings.NewReader(body))
			req.Header.Set("x-goog-api-key", "test-google-key")
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || string(upstream.body) != body || rec.Body.String() != response {
				t.Fatalf("stable route failed: status=%d upstream=%s response=%s", rec.Code, upstream.body, rec.Body.String())
			}
			if upstream.url != "https://upstream.test/v1/models/gemini-2.5-pro:generateContent" || sink.last.InputTokens != 10 || sink.last.OutputTokens != 5 {
				t.Fatalf("stable API version or usage changed: url=%s input=%d output=%d", upstream.url, sink.last.InputTokens, sink.last.OutputTokens)
			}
		})
	}
}
