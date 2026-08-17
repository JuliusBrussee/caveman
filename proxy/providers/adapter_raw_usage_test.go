package providers

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// decodeRaw unmarshals a[b] usage.RawUsage for comparison, tolerating that
// re-marshaling a parsed map can reorder keys (values are what matter).
func decodeRaw(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	if len(raw) == 0 {
		t.Fatal("RawUsage was not captured")
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("RawUsage is not valid JSON: %v (%s)", err, raw)
	}
	return m
}

// C8c: raw usage persistence. The gateway already normalizes provider usage
// into typed token fields (InputTokens, OutputTokens, ...); RawUsage exists
// so a later catalog correction can re-price a request from what the provider
// actually said, without re-deriving through this parser's own normalization.
func TestParseUsageBytes_RawUsageNonStream(t *testing.T) {
	body := `{"id":"resp_stub","object":"response","model":"gpt-5.6","usage":{"input_tokens":2006,"output_tokens":300,"total_tokens":2306,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":1920}}}`
	var usage UsageObservation
	ParseUsageBytes("openai", []byte(body), &usage)

	got := decodeRaw(t, usage.RawUsage)
	want := map[string]any{
		"input_tokens":  float64(2006),
		"output_tokens": float64(300),
		"total_tokens":  float64(2306),
		"input_tokens_details": map[string]any{
			"cached_tokens":      float64(0),
			"cache_write_tokens": float64(1920),
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RawUsage = %#v, want %#v", got, want)
	}
	if usage.CacheCreationInputTokens != 1920 || usage.CachedInputTokens != 0 || cacheStatusFor(usage) != "write" {
		t.Errorf("typed cache usage = %+v, want a 1,920-token write", usage)
	}
}

// A streamed OpenAI chat-completions response with stream_options.include_usage
// emits one final chunk carrying usage on an otherwise-empty choices array.
// RawUsage must capture that terminal chunk, not an earlier partial.
func TestParseUsageBytes_RawUsageStreamCapturesTerminalChunk(t *testing.T) {
	stream := "" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":42,\"completion_tokens\":8,\"total_tokens\":50}}\n\n" +
		"data: [DONE]\n\n"
	var usage UsageObservation
	ParseUsageBytes("openai", []byte(stream), &usage)

	got := decodeRaw(t, usage.RawUsage)
	want := map[string]any{
		"prompt_tokens":     float64(42),
		"completion_tokens": float64(8),
		"total_tokens":      float64(50),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RawUsage = %#v, want %#v", got, want)
	}
	if usage.InputTokens != 42 || usage.OutputTokens != 8 {
		t.Errorf("normalized totals = in=%d out=%d, want 42/8", usage.InputTokens, usage.OutputTokens)
	}
}

// No usage-bearing chunk at all (e.g. a provider error body): RawUsage stays
// empty rather than capturing something unrelated.
func TestParseUsageBytes_RawUsageAbsentWhenNoUsageReported(t *testing.T) {
	var usage UsageObservation
	ParseUsageBytes("openai", []byte(`{"error":{"message":"bad request","type":"invalid_request_error"}}`), &usage)
	if len(usage.RawUsage) != 0 {
		t.Errorf("expected no RawUsage captured, got %s", usage.RawUsage)
	}
}

// C8 review finding 4: RawUsage must MERGE across chunks, not keep only the
// last one. Anthropic is the concrete failure case this guards: message_start
// carries input_tokens + the cache fields, message_delta carries ONLY the
// final output_tokens. Anthropic is the one provider whose cache delta mints
// verified_savings, so losing input/cache data here would defeat the whole
// point of RawUsage (re-pricing from what the provider actually said).
func TestParseUsageBytes_RawUsageMergesAnthropicSplitAcrossChunks(t *testing.T) {
	stream := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":1000,"cache_read_input_tokens":500,"cache_creation_input_tokens":200,"output_tokens":1}}}`, "",
		`event: message_delta`,
		`data: {"type":"message_delta","usage":{"output_tokens":250}}`, "",
	}, "\n")
	var usage UsageObservation
	ParseUsageBytes("anthropic", []byte(stream), &usage)

	got := decodeRaw(t, usage.RawUsage)
	want := map[string]any{
		"input_tokens":                float64(1000),
		"cache_read_input_tokens":     float64(500),
		"cache_creation_input_tokens": float64(200),
		// message_delta's output_tokens (250, the final cumulative total) must
		// win over message_start's provisional 1 — the larger of two numbers
		// wins, the same max rule the typed counters use.
		"output_tokens": float64(250),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RawUsage = %#v, want %#v (merged across message_start + message_delta)", got, want)
	}
}

// CW 15: the merge rule its own doc claimed (max, matching the typed counters)
// and the rule it implemented (last-non-null-wins) diverge on a non-monotonic
// stream. RawUsage exists so a later catalog correction can re-price a request
// from what the provider said; if it disagreed with the typed counters the
// request was originally priced from, re-pricing would silently produce a
// different number. The two must agree on every monotonic field.
func TestParseUsageBytes_RawUsageAgreesWithTypedCountersOnNonMonotonicStream(t *testing.T) {
	// The final chunk under-reports: 3 output tokens after an earlier chunk
	// already reported 250. The typed counter takes the max; so must RawUsage.
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":1000,"output_tokens":250}}}`,
		`data: {"type":"message_delta","usage":{"input_tokens":40,"output_tokens":3}}`,
	}, "\n")
	var usage UsageObservation
	ParseUsageBytes("anthropic", []byte(stream), &usage)

	if usage.InputTokens != 1000 || usage.OutputTokens != 250 {
		t.Fatalf("typed counters = in=%d out=%d, want 1000/250 (max rule)", usage.InputTokens, usage.OutputTokens)
	}
	got := decodeRaw(t, usage.RawUsage)
	want := map[string]any{"input_tokens": float64(1000), "output_tokens": float64(250)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RawUsage = %#v, want %#v — RawUsage and the typed counters must not disagree about the same request", got, want)
	}
}

// CW 6: when the parser zeroes a typed counter because the stream ended without
// its terminal usage event, the raw blob still carried the provisional count
// from the first chunk. Re-pricing from it would resurrect exactly the number
// the parser had just refused to stand behind, so the blob is stamped.
func TestParseUsageBytes_TruncatedStreamStampsRawUsageIncomplete(t *testing.T) {
	cases := map[string]struct{ provider, body string }{
		// Anthropic: message_start with no terminal message_delta usage block.
		"anthropic stream": {"anthropic", strings.Join([]string{
			`data: {"type":"message_start","message":{"usage":{"input_tokens":1000,"cache_read_input_tokens":500,"output_tokens":7}}}`,
			`data: {"type":"content_block_delta","delta":{"text":"partial"}}`,
		}, "\n")},
		// Gemini SSE: usageMetadata with no candidate carrying a finishReason.
		"gemini stream": {"gemini", strings.Join([]string{
			`data: {"candidates":[{"content":{"parts":[{"text":"partial"}]}}],"usageMetadata":{"promptTokenCount":900,"candidatesTokenCount":12,"totalTokenCount":912}}`,
		}, "\n")},
		// Gemini JSON array (non-SSE streamed body), same missing finishReason.
		"gemini array": {"gemini", `[{"candidates":[{"content":{"parts":[{"text":"partial"}]}}],"usageMetadata":{"promptTokenCount":900,"candidatesTokenCount":12,"totalTokenCount":912}}]`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var usage UsageObservation
			ParseUsageBytes(tc.provider, []byte(tc.body), &usage)

			if usage.OutputTokensReported || usage.OutputTokens != 0 {
				t.Fatalf("truncated stream reported output: reported=%v tokens=%d", usage.OutputTokensReported, usage.OutputTokens)
			}
			got := decodeRaw(t, usage.RawUsage)
			if flag, ok := got["raw_usage_incomplete"].(bool); !ok || !flag {
				t.Fatalf("RawUsage = %#v, want raw_usage_incomplete:true — an unlabelled partial blob lets re-pricing resurrect a count the parser refused", got)
			}
			// The provider's own values stay for audit; only the label is added.
			if len(got) < 2 {
				t.Errorf("RawUsage = %#v, want the provider's reported fields preserved beside the label", got)
			}
		})
	}
}

// A complete stream must NOT be stamped: the label has to mean something.
func TestParseUsageBytes_CompleteStreamIsNotStampedIncomplete(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`,
		`data: {"type":"message_delta","usage":{"output_tokens":25}}`,
	}, "\n")
	var usage UsageObservation
	ParseUsageBytes("anthropic", []byte(stream), &usage)
	if _, stamped := decodeRaw(t, usage.RawUsage)["raw_usage_incomplete"]; stamped {
		t.Errorf("complete stream was stamped incomplete: %s", usage.RawUsage)
	}
}

// A later chunk's explicit JSON null for a field must never erase an earlier
// chunk's real value for that SAME field — only a non-null value may
// overwrite.
func TestParseUsageBytes_RawUsageNullDoesNotEraseEarlierValue(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`,
		`data: {"type":"message_delta","usage":{"output_tokens":null}}`,
	}, "\n")
	var usage UsageObservation
	ParseUsageBytes("anthropic", []byte(stream), &usage)

	got := decodeRaw(t, usage.RawUsage)
	want := map[string]any{
		"input_tokens":  float64(10),
		"output_tokens": float64(1),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RawUsage = %#v, want %#v (null must not erase the earlier good output_tokens)", got, want)
	}
}
