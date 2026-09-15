package providers

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"net/http"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// These payloads mirror what cmd/provider-stub and the real providers emit, so
// the assertions double as a contract for the usage parser.

func TestParseUsageBytes_NonStream(t *testing.T) {
	cases := []struct {
		name                                    string
		provider                                string
		body                                    string
		in, out, cached, cacheCreate, reasoning int
	}{
		{
			name:     "openai responses",
			provider: "openai",
			body:     `{"id":"resp_stub","object":"response","model":"gpt-5.5","usage":{"input_tokens":1000,"output_tokens":120,"total_tokens":1120,"input_tokens_details":{"cached_tokens":700}}}`,
			in:       1000, out: 120, cached: 700,
		},
		{
			name:     "openai responses cache write",
			provider: "openai",
			body:     `{"id":"resp_stub","object":"response","model":"gpt-5.6","usage":{"input_tokens":2006,"output_tokens":300,"total_tokens":2306,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":1920}}}`,
			in:       2006, out: 300, cacheCreate: 1920,
		},
		{
			name:     "openai chat completions",
			provider: "openai",
			body:     `{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":42,"completion_tokens":8,"total_tokens":50}}`,
			in:       42, out: 8,
		},
		{
			name:     "openai chat completions cache write",
			provider: "openai",
			body:     `{"choices":[],"usage":{"prompt_tokens":2006,"completion_tokens":300,"total_tokens":2306,"prompt_tokens_details":{"cached_tokens":0,"cache_write_tokens":1920}}}`,
			in:       2006, out: 300, cacheCreate: 1920,
		},
		{
			name:     "openai reasoning",
			provider: "openai",
			body:     `{"usage":{"input_tokens":500,"output_tokens":300,"output_tokens_details":{"reasoning_tokens":250}}}`,
			in:       500, out: 300, reasoning: 250,
		},
		{
			name:     "openai embeddings",
			provider: "openai",
			body:     `{"object":"list","data":[],"usage":{"prompt_tokens":12,"total_tokens":12}}`,
			in:       12, out: 0,
		},
		{
			name:     "anthropic messages",
			provider: "anthropic",
			body:     `{"id":"msg_stub","type":"message","usage":{"input_tokens":1100,"output_tokens":120,"cache_creation_input_tokens":400,"cache_read_input_tokens":800}}`,
			// Normalized input is total effective input; cache fields are subsets.
			in: 2300, out: 120, cached: 800, cacheCreate: 400,
		},
		{
			name:     "gemini generateContent",
			provider: "gemini",
			body:     `{"candidates":[],"usageMetadata":{"promptTokenCount":900,"candidatesTokenCount":100,"totalTokenCount":1000}}`,
			in:       900, out: 100,
		},
		{
			name:     "gemini cached + thoughts",
			provider: "gemini",
			body:     `{"usageMetadata":{"promptTokenCount":900,"candidatesTokenCount":100,"cachedContentTokenCount":300,"thoughtsTokenCount":40}}`,
			// Output is normalized to the billed total; reasoning is a subset.
			in: 900, out: 140, cached: 300, reasoning: 40,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var u UsageObservation
			ParseUsageBytes(tc.provider, []byte(tc.body), &u)
			assertUsage(t, u, tc.in, tc.out, tc.cached, tc.cacheCreate, tc.reasoning)
		})
	}
}

func TestParseUsageBytes_Stream(t *testing.T) {
	openaiSSE := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"stub "}`, "",
		`data: {"type":"response.output_text.delta","delta":"openai stream"}`, "",
		`data: {"type":"response.completed","usage":{"input_tokens":1000,"output_tokens":20,"input_tokens_details":{"cached_tokens":700}}}`, "",
		"data: [DONE]", "",
	}, "\n")

	openaiNestedSSE := strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"r","usage":{"input_tokens":640,"output_tokens":33}}}`, "",
		"data: [DONE]", "",
	}, "\n")

	anthropicStubSSE := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_stream","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-5","usage":{"input_tokens":1000,"output_tokens":1}}}`, "",
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"stub anthropic stream"}}`, "",
		`event: message_stop`,
		`data: {"type":"message_stop"}`, "",
	}, "\n")

	// Real Anthropic streams report final output (cumulative) in message_delta and
	// cache fields in message_start.
	anthropicRealSSE := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":1000,"cache_read_input_tokens":500,"cache_creation_input_tokens":200,"output_tokens":1}}}`, "",
		`event: message_delta`,
		`data: {"type":"message_delta","usage":{"output_tokens":250}}`, "",
	}, "\n")

	geminiJSONL := strings.Join([]string{
		`{"candidates":[{"content":{"parts":[{"text":"stub "}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"gemini stream"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":900,"candidatesTokenCount":100}}`,
	}, "\n")

	cases := []struct {
		name                                    string
		provider, body                          string
		in, out, cached, cacheCreate, reasoning int
	}{
		{"openai responses sse", "openai", openaiSSE, 1000, 20, 700, 0, 0},
		{"openai nested response.usage", "openai", openaiNestedSSE, 640, 33, 0, 0, 0},
		{"anthropic stub sse without final usage", "anthropic", anthropicStubSSE, 1000, 0, 0, 0, 0},
		{"anthropic real sse cumulative", "anthropic", anthropicRealSSE, 1700, 250, 500, 200, 0},
		{"gemini jsonl stream", "gemini", geminiJSONL, 900, 100, 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var u UsageObservation
			ParseUsageBytes(tc.provider, []byte(tc.body), &u)
			assertUsage(t, u, tc.in, tc.out, tc.cached, tc.cacheCreate, tc.reasoning)
		})
	}
}

func TestParseUsageBytes_GeminiStreamingJSONArray(t *testing.T) {
	body := `[{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1,"totalTokenCount":11}},{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":2,"totalTokenCount":17}}]`
	var got UsageObservation
	ParseUsageBytes("gemini", []byte(body), &got)
	if !got.Complete() || got.InputTokens != 10 || got.OutputTokens != 7 || got.ReasoningTokens != 2 {
		t.Fatalf("array stream usage = %+v", got)
	}
}

func TestParseUsageBytes_GeminiNestedToolFinishReasonIsNotTerminal(t *testing.T) {
	body := strings.Join([]string{
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"x","args":{"finishReason":"STOP"}}}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1,"totalTokenCount":11}}`,
		`{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"totalTokenCount":12}}`,
	}, "\n")
	var got UsageObservation
	ParseUsageBytes("gemini", []byte(body), &got)
	if got.Complete() || got.OutputTokensReported {
		t.Fatalf("nested tool argument forged terminal usage: %+v", got)
	}
}

func TestParseUsageBytes_ExplicitNullCountersAreMalformed(t *testing.T) {
	t.Run("anthropic terminal null", func(t *testing.T) {
		body := strings.Join([]string{
			`data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`,
			`data: {"type":"message_delta","usage":{"output_tokens":null}}`,
		}, "\n")
		var got UsageObservation
		ParseUsageBytes("anthropic", []byte(body), &got)
		if !got.Malformed || got.Complete() {
			t.Fatalf("null terminal counter must fail closed: %+v", got)
		}
	})
	t.Run("gemini prompt null", func(t *testing.T) {
		body := `{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":null,"candidatesTokenCount":2}}`
		var got UsageObservation
		ParseUsageBytes("gemini", []byte(body), &got)
		if !got.Malformed || got.Complete() {
			t.Fatalf("null usage counter must fail closed: %+v", got)
		}
	})
}

func TestUsagePricingQualifiersAndTerminalProof(t *testing.T) {
	var bedrock UsageObservation
	ParseUsageBytes("bedrock", []byte(`{"serviceTier":{"type":"priority"},"usage":{"input_tokens":10,"output_tokens":2}}`), &bedrock)
	if bedrock.ServiceTier != "priority" || !bedrock.Complete() {
		t.Fatalf("bedrock qualifier = %+v, want complete priority tier", bedrock)
	}

	var vertex UsageObservation
	ParseUsageBytes("vertex", []byte(`{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"totalTokenCount":12,"trafficType":"PROVISIONED_THROUGHPUT"}}`), &vertex)
	if vertex.ServiceTier != "provisioned_throughput" || !vertex.Complete() {
		t.Fatalf("vertex traffic qualifier = %+v", vertex)
	}

	var nestedOpenAI UsageObservation
	ParseUsageBytes("openai", []byte(`data: {"type":"response.completed","response":{"service_tier":"priority","usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}`+"\n\n"), &nestedOpenAI)
	if nestedOpenAI.ServiceTier != "priority" || !nestedOpenAI.Complete() {
		t.Fatalf("nested OpenAI qualifier = %+v", nestedOpenAI)
	}

	var truncatedClaude UsageObservation
	ParseUsageBytes("anthropic", []byte("event: message_start\n"+`data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`+"\n\n"), &truncatedClaude)
	if !truncatedClaude.InputTokensReported || truncatedClaude.OutputTokensReported || truncatedClaude.Complete() {
		t.Fatalf("truncated Anthropic stream = %+v, want provider_partial", truncatedClaude)
	}

	var truncatedGemini UsageObservation
	ParseUsageBytes("gemini", []byte(`{"candidates":[]}`+"\n"+`{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"totalTokenCount":12}}`+"\n"), &truncatedGemini)
	if !truncatedGemini.InputTokensReported || truncatedGemini.OutputTokensReported || truncatedGemini.Complete() {
		t.Fatalf("truncated Gemini stream = %+v, want provider_partial", truncatedGemini)
	}
}

func TestProviderNoChargeAndFilteredOutputSemantics(t *testing.T) {
	var refusal UsageObservation
	ParseUsageBytes("anthropic", []byte(`{"stop_reason":"refusal","usage":{"input_tokens":10,"output_tokens":0}}`), &refusal)
	if refusal.PricingUnsupportedReason != "provider_no_charge_refusal" || !refusal.Complete() {
		t.Fatalf("pre-output refusal = %+v", refusal)
	}

	var midstreamRefusal UsageObservation
	ParseUsageBytes("anthropic", []byte(`{"stop_reason":"refusal","usage":{"input_tokens":10,"output_tokens":4}}`), &midstreamRefusal)
	if midstreamRefusal.PricingUnsupportedReason != "" || !midstreamRefusal.Complete() {
		t.Fatalf("billed mid-stream refusal = %+v", midstreamRefusal)
	}

	var streamedRefusal UsageObservation
	ParseUsageBytes("anthropic", []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":0}}}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"refusal"},"usage":{"output_tokens":0}}`,
	}, "\n")), &streamedRefusal)
	if streamedRefusal.PricingUnsupportedReason != "provider_no_charge_refusal" || !streamedRefusal.Complete() {
		t.Fatalf("streamed pre-output refusal = %+v", streamedRefusal)
	}

	var filtered UsageObservation
	ParseUsageBytes("gemini", []byte(`{"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"promptTokenCount":10,"totalTokenCount":10}}`), &filtered)
	if !filtered.Complete() || filtered.InputTokens != 10 || filtered.OutputTokens != 0 {
		t.Fatalf("filtered Gemini response = %+v, want complete 10/0", filtered)
	}

	for _, tc := range []struct {
		name, provider, body string
	}{
		{
			name:     "gemini jsonl",
			provider: "gemini",
			body: strings.Join([]string{
				`{"promptFeedback":{"blockReason":"SAFETY"}}`,
				`{"usageMetadata":{"promptTokenCount":10,"totalTokenCount":10}}`,
			}, "\n"),
		},
		{
			name:     "vertex sse",
			provider: "vertex",
			body: strings.Join([]string{
				`data: {"promptFeedback":{"blockReason":"PROHIBITED_CONTENT"}}`,
				`data: {"usageMetadata":{"promptTokenCount":12,"totalTokenCount":12}}`,
			}, "\n\n"),
		},
		{
			name:     "gemini json array",
			provider: "gemini",
			body:     `[{"promptFeedback":{"blockReason":"BLOCKLIST"}},{"usageMetadata":{"promptTokenCount":14,"totalTokenCount":14}}]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got UsageObservation
			ParseUsageBytes(tc.provider, []byte(tc.body), &got)
			if !got.Complete() || !got.InputTokensReported || !got.OutputTokensReported || got.OutputTokens != 0 {
				t.Fatalf("streamed filtered response = %+v, want complete provider-reported input and zero output", got)
			}
		})
	}
}

func TestParseUsageBytes_NormalizesProviderSemantics(t *testing.T) {
	tests := []struct {
		name, provider, body string
		wantIn, wantOut      int
	}{
		{
			name: "openai input and output already include cache and reasoning", provider: "openai",
			body:   `{"usage":{"input_tokens":1000,"output_tokens":300,"input_tokens_details":{"cached_tokens":700},"output_tokens_details":{"reasoning_tokens":250}}}`,
			wantIn: 1000, wantOut: 300,
		},
		{
			name: "anthropic input excludes cache", provider: "anthropic",
			body:   `{"usage":{"input_tokens":100,"output_tokens":30,"cache_read_input_tokens":700,"cache_creation_input_tokens":200}}`,
			wantIn: 1000, wantOut: 30,
		},
		{
			name: "gemini output excludes thoughts and input includes tool results", provider: "gemini",
			body:   `{"usageMetadata":{"promptTokenCount":100,"toolUsePromptTokenCount":20,"candidatesTokenCount":30,"thoughtsTokenCount":40,"totalTokenCount":190}}`,
			wantIn: 120, wantOut: 70,
		},
		{
			name: "vertex claude uses anthropic input semantics", provider: "vertex",
			body:   `{"usage":{"input_tokens":100,"output_tokens":30,"cache_read_input_tokens":700,"cache_creation_input_tokens":200}}`,
			wantIn: 1000, wantOut: 30,
		},
		{
			name: "vertex gemini stays inclusive", provider: "vertex",
			body:   `{"usageMetadata":{"promptTokenCount":1000,"candidatesTokenCount":30,"cachedContentTokenCount":700,"thoughtsTokenCount":20,"totalTokenCount":1050}}`,
			wantIn: 1000, wantOut: 50,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got UsageObservation
			ParseUsageBytes(tc.provider, []byte(tc.body), &got)
			if got.Malformed {
				t.Fatalf("usage unexpectedly malformed: %+v", got)
			}
			if got.InputTokens != tc.wantIn || got.OutputTokens != tc.wantOut {
				t.Fatalf("normalized usage = %d/%d, want %d/%d", got.InputTokens, got.OutputTokens, tc.wantIn, tc.wantOut)
			}
			if !got.InputTokensReported || !got.OutputTokensReported {
				t.Fatalf("reported flags = input:%v output:%v, want both true", got.InputTokensReported, got.OutputTokensReported)
			}
		})
	}
}

func TestParseUsageBytes_MalformedCountersFailClosed(t *testing.T) {
	tests := []struct{ name, body string }{
		{"negative", `{"usage":{"input_tokens":-1,"output_tokens":2}}`},
		{"fractional", `{"usage":{"input_tokens":1.5,"output_tokens":2}}`},
		{"overflow", `{"usage":{"input_tokens":9223372036854775808,"output_tokens":2}}`},
		{"conflicting aliases", `{"usage":{"input_tokens":10,"prompt_tokens":11,"output_tokens":2}}`},
		{"conflicting cache write aliases", `{"usage":{"input_tokens":10,"output_tokens":2,"cache_creation_input_tokens":3,"input_tokens_details":{"cache_write_tokens":4}}}`},
		{"cache exceeds total", `{"usage":{"input_tokens":10,"output_tokens":2,"input_tokens_details":{"cached_tokens":11}}}`},
		{"cache write exceeds total", `{"usage":{"input_tokens":10,"output_tokens":2,"input_tokens_details":{"cache_write_tokens":11}}}`},
		{"reasoning exceeds output", `{"usage":{"input_tokens":10,"output_tokens":2,"output_tokens_details":{"reasoning_tokens":3}}}`},
		{"contradictory total", `{"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":99}}`},
		{"malformed explicit cache", `{"usage":{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":"bad"}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got UsageObservation
			ParseUsageBytes("openai", []byte(tc.body), &got)
			if !got.Malformed {
				t.Fatalf("usage = %+v, want Malformed", got)
			}
			if status := cacheStatusFor(got); status != "unknown" {
				t.Fatalf("cache status = %q, want unknown for malformed usage", status)
			}
		})
	}
}

func TestParseUsageBytes_MissingAndPartialUsage(t *testing.T) {
	var missing UsageObservation
	ParseUsageBytes("openai", []byte(`{"id":"resp_no_usage"}`), &missing)
	if missing.InputTokensReported || missing.OutputTokensReported || missing.Malformed {
		t.Fatalf("missing usage = %+v, want clean unavailable observation", missing)
	}

	var partial UsageObservation
	ParseUsageBytes("openai", []byte(`{"usage":{"input_tokens":10}}`), &partial)
	if !partial.InputTokensReported || partial.OutputTokensReported || partial.Malformed {
		t.Fatalf("partial usage = %+v, want input-only provider observation", partial)
	}

	var zero UsageObservation
	ParseUsageBytes("openai", []byte(`{"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`), &zero)
	if !zero.InputTokensReported || !zero.OutputTokensReported || zero.Malformed {
		t.Fatalf("provider-reported zero = %+v, want complete valid observation", zero)
	}
}

func TestParseUsageBytes_AnthropicCacheCreationTTLBreakdown(t *testing.T) {
	body := `{"usage":{"input_tokens":100,"output_tokens":20,"cache_creation_input_tokens":300,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":200,"ephemeral_1h_input_tokens":100}}}`
	var got UsageObservation
	ParseUsageBytes("anthropic", []byte(body), &got)
	if got.Malformed {
		t.Fatalf("usage unexpectedly malformed: %+v", got)
	}
	if got.InputTokens != 400 || got.CacheCreationInputTokens != 300 || got.CacheCreation5mTokens != 200 || got.CacheCreation1hTokens != 100 {
		t.Fatalf("TTL usage = %+v, want total=400 create=300 5m=200 1h=100", got)
	}
}

func TestUsageScanner_TeeAndStatus(t *testing.T) {
	b := Base{Provider: "anthropic"}
	sc := b.NewUsageScanner(http.Header{"X-Request-Id": []string{"req-123"}})
	// Simulate the proxy copy loop writing chunks through the tee.
	for _, chunk := range []string{
		`event: message_start` + "\n" + `data: {"type":"message_start","message":{"usage":{"input_tokens":80,"cache_read_input_tokens":20,"output_tokens":1}}}` + "\n\n",
		`event: message_delta` + "\n" + `data: {"type":"message_delta","usage":{"output_tokens":12}}` + "\n\n",
	} {
		if _, err := sc.Write([]byte(chunk)); err != nil {
			t.Fatalf("scanner write: %v", err)
		}
	}
	u := sc.Usage()
	assertUsage(t, u, 100, 12, 20, 0, 0)
	if u.ProviderRequestID != "req-123" {
		t.Errorf("provider request id = %q, want req-123", u.ProviderRequestID)
	}
	if u.CacheStatus != "hit" {
		t.Errorf("cache status = %q, want hit (cached tokens present)", u.CacheStatus)
	}
}

func TestCacheStatus(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		// No cache telemetry at all -> must not claim a definitive miss.
		{"no cache fields", `{"usage":{"input_tokens":10,"output_tokens":5}}`, "unknown"},
		{"embeddings no cache", `{"object":"list","usage":{"prompt_tokens":12}}`, "unknown"},
		// Cache field present but zero read -> genuine miss.
		{"explicit zero read", `{"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":0}}`, "miss"},
		// Cache read present -> hit.
		{"cache read hit", `{"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":7}}`, "hit"},
		// Only cache creation -> write, not miss.
		{"cache write only", `{"usage":{"input_tokens":410,"output_tokens":5,"cache_creation_input_tokens":400,"cache_read_input_tokens":0}}`, "write"},
		// OpenAI chat/Azure cached reads live under prompt_tokens_details.
		{"prompt_tokens_details cached", `{"usage":{"prompt_tokens":50,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":20}}}`, "hit"},
	}
	b := Base{Provider: "openai"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := b.NewUsageScanner(http.Header{})
			_, _ = sc.Write([]byte(tc.body))
			if got := sc.Usage().CacheStatus; got != tc.want {
				t.Errorf("cache status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUsageScanner_Truncation(t *testing.T) {
	// A scanner whose byte limit is exceeded must report unknown, not a confident zero.
	sc := &UsageScanner{provider: "openai", limit: 8}
	_, _ = sc.Write([]byte(`{"usage":{"input_tokens":10,"output_tokens":5}}`))
	u := sc.Usage()
	if u.CacheStatus != "unknown" || u.InputTokens != 0 {
		t.Errorf("truncated scanner = %+v, want zero usage and unknown status", u)
	}
}

func TestUsageScanner_DecodesSupportedSideCopyWithoutChangingWireBytes(t *testing.T) {
	encodings := []string{"gzip", "deflate", "zstd"}
	payloads := []struct {
		name string
		body string
	}{
		{"json", `{"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`},
		{"sse", "data: {\"type\":\"response.completed\",\"usage\":{\"input_tokens\":12,\"output_tokens\":7,\"total_tokens\":19}}\n\ndata: [DONE]\n\n"},
	}
	for _, encoding := range encodings {
		for _, payload := range payloads {
			t.Run(encoding+"/"+payload.name, func(t *testing.T) {
				wire := compressUsagePayload(t, encoding, []byte(payload.body))
				original := append([]byte(nil), wire...)
				sc := (Base{Provider: "openai"}).NewUsageScanner(http.Header{"Content-Encoding": []string{encoding}})
				_, _ = sc.Write(wire)
				got := sc.Usage()
				if !got.Complete() {
					t.Fatalf("%s usage incomplete: %+v", encoding, got)
				}
				if payload.name == "json" && (got.InputTokens != 10 || got.OutputTokens != 5) {
					t.Fatalf("%s JSON usage = %+v", encoding, got)
				}
				if payload.name == "sse" && (got.InputTokens != 12 || got.OutputTokens != 7) {
					t.Fatalf("%s SSE usage = %+v", encoding, got)
				}
				if !bytes.Equal(wire, original) {
					t.Fatal("accounting scan mutated wire bytes")
				}
			})
		}
	}
}

func TestUsageScanner_CorruptSupportedEncodingIsExplicitlyUnpriced(t *testing.T) {
	for _, encoding := range []string{"gzip", "deflate", "zstd"} {
		t.Run(encoding, func(t *testing.T) {
			sc := (Base{Provider: "openai"}).NewUsageScanner(http.Header{"Content-Encoding": []string{encoding}})
			_, _ = sc.Write([]byte("not-a-valid-" + encoding + "-stream"))
			got := sc.Usage()
			want := "content_decode_failed/" + encoding
			if got.PricingUnsupportedReason != want || got.Complete() {
				t.Fatalf("corrupt %s usage = %+v, want reason %q", encoding, got, want)
			}
		})
	}
}

func TestUsageScanner_DecodedSideCopyHonorsScanLimit(t *testing.T) {
	const limit = 128
	wire := compressUsagePayload(t, "gzip", []byte(strings.Repeat("x", 4096)))
	if len(wire) >= limit {
		t.Fatalf("test requires compressed bytes below the scan limit, got %d", len(wire))
	}
	sc := (Base{Provider: "openai"}).NewUsageScanner(http.Header{"Content-Encoding": []string{"gzip"}})
	sc.SetLimit(limit)
	_, _ = sc.Write(wire)
	got := sc.Usage()
	if got.PricingUnsupportedReason != "response_scan_limit_exceeded" || got.Complete() {
		t.Fatalf("oversized decoded usage = %+v", got)
	}
}

func TestUsageScanner_StackedEncodingIsExplicitlyUnpriced(t *testing.T) {
	wire := compressUsagePayload(t, "gzip", []byte(`{"usage":{"input_tokens":10,"output_tokens":5}}`))
	sc := (Base{Provider: "openai"}).NewUsageScanner(http.Header{"Content-Encoding": []string{"gzip, br"}})
	_, _ = sc.Write(wire)
	got := sc.Usage()
	if got.PricingUnsupportedReason != "unsupported_content_encoding/stacked" || got.Complete() {
		t.Fatalf("stacked encoding usage = %+v", got)
	}
}

func compressUsagePayload(t *testing.T, encoding string, body []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	var (
		writer interface {
			Write([]byte) (int, error)
			Close() error
		}
		err error
	)
	switch encoding {
	case "gzip":
		writer = gzip.NewWriter(&compressed)
	case "deflate":
		writer = zlib.NewWriter(&compressed)
	case "zstd":
		writer, err = zstd.NewWriter(&compressed)
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unsupported test encoding %q", encoding)
	}
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), compressed.Bytes()...)
}

func TestUsageScanner_UnsupportedEncodingIsExplicitlyUnpriced(t *testing.T) {
	sc := (Base{Provider: "openai"}).NewUsageScanner(http.Header{"Content-Encoding": []string{"br"}})
	_, _ = sc.Write([]byte("compressed"))
	got := sc.Usage()
	if got.PricingUnsupportedReason != "unsupported_content_encoding/br" || got.Complete() {
		t.Fatalf("unsupported encoding usage = %+v", got)
	}
}

func TestUsageScanner_RecoversCompleteTerminalUsageFromBoundedTail(t *testing.T) {
	sc := &UsageScanner{provider: "openai", limit: 256}
	_, _ = sc.Write([]byte(strings.Repeat("x", 1024)))
	_, _ = sc.Write([]byte("\ndata: {\"type\":\"response.completed\",\"usage\":{\"input_tokens\":20,\"output_tokens\":8,\"total_tokens\":28}}\n\n"))
	got := sc.Usage()
	if !got.Complete() || got.InputTokens != 20 || got.OutputTokens != 8 {
		t.Fatalf("tail usage = %+v, want complete 20/8", got)
	}
}

// A single SSE/JSONL line larger than the old 4MB cap must not silently drop usage.
func TestParseUsageBytes_LargeSingleLine(t *testing.T) {
	pad := strings.Repeat("x", 5*1024*1024)
	body := `data: {"type":"response.completed","usage":{"input_tokens":1000,"output_tokens":50},"padding":"` + pad + `"}` + "\n\n"
	var u UsageObservation
	ParseUsageBytes("openai", []byte(body), &u)
	if u.InputTokens != 1000 || u.OutputTokens != 50 {
		t.Errorf("large single line dropped usage: got input=%d output=%d, want 1000/50", u.InputTokens, u.OutputTokens)
	}
}

func assertUsage(t *testing.T, u UsageObservation, in, out, cached, cacheCreate, reasoning int) {
	t.Helper()
	if u.InputTokens != in {
		t.Errorf("input = %d, want %d", u.InputTokens, in)
	}
	if u.OutputTokens != out {
		t.Errorf("output = %d, want %d", u.OutputTokens, out)
	}
	if u.CachedInputTokens != cached {
		t.Errorf("cached = %d, want %d", u.CachedInputTokens, cached)
	}
	if u.CacheCreationInputTokens != cacheCreate {
		t.Errorf("cache_creation = %d, want %d", u.CacheCreationInputTokens, cacheCreate)
	}
	if u.ReasoningTokens != reasoning {
		t.Errorf("reasoning = %d, want %d", u.ReasoningTokens, reasoning)
	}
}
