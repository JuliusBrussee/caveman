package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

func TestExtractCompressionQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		endpoint string
		body     string
		want     string
	}{
		{
			name:     "openai chat skips latest tool output",
			provider: "openai",
			endpoint: "/v1/chat/completions",
			body:     `{"messages":[{"role":"user","content":"find quarantined host omega"},{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"inventory","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"[{\"host\":\"alpha\"},{\"host\":\"quarantined-host-omega\"}]"}]}`,
			want:     "find quarantined host omega",
		},
		{
			name:     "openai responses skips function output",
			provider: "openai",
			endpoint: "/v1/responses",
			body:     `{"input":[{"role":"user","content":[{"type":"input_text","text":"which deployment failed"}]},{"type":"function_call_output","call_id":"c1","output":"[{\"deployment\":\"api\"},{\"deployment\":\"worker\"}]"}]}`,
			want:     "which deployment failed",
		},
		{
			name:     "anthropic skips tool result block",
			provider: "anthropic",
			endpoint: "/v1/messages",
			body:     `{"messages":[{"role":"user","content":[{"type":"text","text":"find fatal build step"}]},{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"build","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"step 67 FATAL linker error"}]}]}`,
			want:     "find fatal build step",
		},
		{
			name:     "gemini skips function response",
			provider: "gemini",
			endpoint: "generateContent",
			body:     `{"contents":[{"role":"user","parts":[{"text":"which shard is unhealthy"}]},{"role":"model","parts":[{"functionCall":{"name":"status","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"status","response":{"output":"[{\"shard\":\"a\"},{\"shard\":\"b\"}]"}}}]}]}`,
			want:     "which shard is unhealthy",
		},
		{
			name:     "malformed request has no query",
			provider: "openai",
			endpoint: "/v1/chat/completions",
			body:     `{`,
			want:     "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := extractCompressionQuery(tt.provider, tt.endpoint, []byte(tt.body)); got != tt.want {
				t.Fatalf("query = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractCompressionQueryBoundsWorkWithoutBreakingUTF8(t *testing.T) {
	query := strings.Repeat("a", maxCompressionQueryBytes-1) + "€" + strings.Repeat("z", 32)
	body := `{"messages":[{"role":"user","content":` + quoteJSON(query) + `}]}`
	got := extractCompressionQuery("openai", "/v1/chat/completions", []byte(body))
	if len(got) > maxCompressionQueryBytes {
		t.Fatalf("query bytes = %d, want <= %d", len(got), maxCompressionQueryBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("query cap split a UTF-8 code point")
	}
}

type queryCaptureCompressor struct {
	stubCompressor
	compressQueries []string
	estimateQueries []string
}

func (c *queryCaptureCompressor) CompressSegmentQuery(segment []byte, query string) ([]byte, int, int) {
	c.compressQueries = append(c.compressQueries, query)
	return c.stubCompressor.CompressSegment(segment)
}

func (c *queryCaptureCompressor) EstimateSegmentQuery(segment []byte, query string) (int, int) {
	c.estimateQueries = append(c.estimateQueries, query)
	return 100, 40
}

func (c *queryCaptureCompressor) EstimateSegment(segment []byte) (int, int) {
	return 100, 40
}

func TestCompressModePassesLatestUserQueryToEveryLiveSegment(t *testing.T) {
	var gotUpstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotUpstreamBody = string(body)
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, chatRespBody)
	}))
	defer upstream.Close()

	query := "find quarantined host omega"
	toolOutput := strings.Repeat(`[{"host":"alpha","status":"ok"},{"host":"quarantined-host-omega","status":"blocked"}]`, 12)
	body := `{"model":"gpt-5.5","messages":[{"role":"user","content":"` + query + `"},{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"inventory","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":` + quoteJSON(toolOutput) + `}]}`
	comp := &queryCaptureCompressor{stubCompressor: stubCompressor{
		out:       []byte("X"),
		before:    100,
		after:     40,
		handle:    "ccr_query",
		recovered: []byte(toolOutput),
	}}
	sink := &captureSink{}
	srv := New(Config{
		Adapters:       []providers.Adapter{openai.New(upstream.URL)},
		Auth:           stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}},
		Creds:          stubCreds{key: "sk-byok"},
		Sink:           sink,
		Compressor:     comp,
		RecoveryViaMCP: true,
		HTTPClient:     &http.Client{},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if len(comp.compressQueries) == 0 {
		t.Fatal("query-aware compressor was not used")
	}
	for i, got := range comp.compressQueries {
		if got != query {
			t.Fatalf("compress query[%d] = %q, want %q", i, got, query)
		}
	}
	if strings.Contains(gotUpstreamBody, "quarantined-host-omega") {
		t.Fatalf("upstream still received uncompressed tool output: %s", gotUpstreamBody)
	}
}

func TestObserveEstimateUsesSameLatestUserQuery(t *testing.T) {
	var gotUpstreamBody string
	upstream := echoUpstream(t, &gotUpstreamBody)
	defer upstream.Close()

	query := "find quarantined host omega"
	toolOutput := strings.Repeat(`[{"host":"alpha"},{"host":"quarantined-host-omega"}]`, 16)
	body := `{"model":"gpt-5.5","messages":[{"role":"user","content":"` + query + `"},{"role":"tool","tool_call_id":"c1","content":` + quoteJSON(toolOutput) + `}]}`
	comp := &queryCaptureCompressor{}
	sink := &captureSink{}
	srv := New(Config{
		Adapters:        []providers.Adapter{openai.New(upstream.URL)},
		Auth:            stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		Creds:           stubCreds{key: "sk-byok"},
		Sink:            sink,
		Compressor:      comp,
		ObserveEstimate: true,
		HTTPClient:      &http.Client{},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if gotUpstreamBody != body {
		t.Fatal("observe estimate changed forwarded request bytes")
	}
	if len(comp.estimateQueries) == 0 {
		t.Fatal("query-aware estimator was not used")
	}
	for i, got := range comp.estimateQueries {
		if got != query {
			t.Fatalf("estimate query[%d] = %q, want %q", i, got, query)
		}
	}
}

func quoteJSON(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return `"` + replacer.Replace(value) + `"`
}
