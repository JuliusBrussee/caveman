package cacheengine

import (
	"math"
	"strconv"
	"testing"
)

func TestNormalizeRawCacheUsageAcrossBuiltins(t *testing.T) {
	tests := []struct {
		name, provider, raw string
		wantRead, wantWrite int
		wantStatus          string
	}{
		{"openai responses", "openai", `{"input_tokens_details":{"cached_tokens":1920,"cache_write_tokens":0}}`, 1920, 0, "hit"},
		{"openai chat write", "openai", `{"prompt_tokens_details":{"cached_tokens":0,"cache_write_tokens":1200}}`, 0, 1200, "write"},
		{"anthropic details", "anthropic", `{"cache_read_input_tokens":0,"cache_creation_input_tokens":1200,"cache_creation":{"ephemeral_5m_input_tokens":1000,"ephemeral_1h_input_tokens":200}}`, 0, 1200, "write"},
		{"bedrock hit", "bedrock", `{"cacheReadInputTokens":800,"cacheWriteInputTokens":0}`, 800, 0, "hit"},
		{"gemini miss", "gemini", `{"cachedContentTokenCount":0}`, 0, 0, "miss"},
		{"gemini interactions", "gemini", `{"total_cached_tokens":700}`, 700, 0, "hit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			usage, ok := NormalizeRawCacheUsage(test.provider, []byte(test.raw))
			if !ok {
				t.Fatal("normalization failed")
			}
			if usage.CachedInputTokens != test.wantRead || usage.CacheCreationInputTokens != test.wantWrite || usage.CacheStatus != test.wantStatus || !usage.CacheObserved {
				t.Fatalf("usage = %#v", usage)
			}
		})
	}
}

func TestNormalizeRawCacheUsageFailsClosed(t *testing.T) {
	tests := []struct{ provider, raw string }{
		{"openai", `{"input_tokens_details":{"cached_tokens":1},"prompt_tokens_details":{"cached_tokens":1}}`},
		{"openai", `{"input_tokens_details":{"cached_tokens":-1}}`},
		{"openai", `{"input_tokens_details":{"cached_tokens":1.5}}`},
		{"anthropic", `{"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":99}}`},
		{"anthropic", `{"cache_creation_input_tokens":100,"cache_creation":"invalid"}`},
		{"gemini", `{"cachedContentTokenCount":1,"cachedContentTokenCount":2}`},
		{"unknown", `{"cached_tokens":1}`},
	}
	for _, test := range tests {
		if usage, ok := NormalizeRawCacheUsage(test.provider, []byte(test.raw)); ok {
			t.Fatalf("%s/%s normalized as %#v", test.provider, test.raw, usage)
		}
	}
}

func TestObserveRawCacheUsageKeepsVerifiedZero(t *testing.T) {
	result := NativeResult{Applied: true, Profile: Profile{Attribution: AttributionCausal}}
	observed := ObserveRawCacheUsage(result, "openai", []byte(`{"input_tokens_details":{"cached_tokens":1500,"cache_write_tokens":0}}`))
	if observed.Status != ObservationHit || !observed.ProviderConfirmed || !observed.AttributedToEngine || observed.VerifiedSavingsUSD != 0 {
		t.Fatalf("observed = %#v", observed)
	}
	invalid := ObserveRawCacheUsage(result, "openai", []byte(`{"input_tokens_details":{"cached_tokens":-1}}`))
	if invalid.Status != ObservationUnavailable || invalid.ProviderConfirmed {
		t.Fatalf("invalid = %#v", invalid)
	}
}

func TestExtractProviderUsageFromCompleteResponses(t *testing.T) {
	tests := []struct {
		provider, response                         string
		wantTotal, wantOutput, wantRead, wantWrite int
	}{
		{"openai", `{"id":"r1","usage":{"prompt_tokens":2100,"completion_tokens":12,"prompt_tokens_details":{"cached_tokens":1900,"cache_write_tokens":0}}}`, 2100, 12, 1900, 0},
		{"openai", `{"id":"r2","usage":{"input_tokens":2200,"output_tokens":13,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":1800}}}`, 2200, 13, 0, 1800},
		{"anthropic", `{"id":"r3","usage":{"input_tokens":200,"output_tokens":14,"cache_read_input_tokens":1800,"cache_creation_input_tokens":0}}`, 2000, 14, 1800, 0},
		{"bedrock", `{"usage":{"inputTokens":200,"outputTokens":15,"cacheReadInputTokens":0,"cacheWriteInputTokens":1800}}`, 2000, 15, 0, 1800},
		{"gemini", `{"usageMetadata":{"promptTokenCount":2000,"candidatesTokenCount":16,"cachedContentTokenCount":1800}}`, 2000, 16, 1800, 0},
	}
	for _, test := range tests {
		t.Run(test.provider, func(t *testing.T) {
			evidence, ok := ExtractProviderUsage(test.provider, []byte(test.response))
			if !ok || evidence.TotalInputTokens != test.wantTotal || evidence.OutputTokens != test.wantOutput {
				t.Fatalf("evidence = %#v, ok=%v", evidence, ok)
			}
			usage, ok := NormalizeRawCacheUsage(test.provider, evidence.RawUsage)
			if !ok || usage.CachedInputTokens != test.wantRead || usage.CacheCreationInputTokens != test.wantWrite {
				t.Fatalf("usage = %#v, ok=%v", usage, ok)
			}
		})
	}
}

func TestExtractProviderUsageFailsClosed(t *testing.T) {
	for _, test := range []struct{ provider, response string }{
		{"openai", `{"usage":{"prompt_tokens":2,"input_tokens":2,"prompt_tokens_details":{}}}`},
		{"openai", `{"usage":{"prompt_tokens":2,"prompt_tokens_details":{}}}`},
		{"openai", `{"usage":{"prompt_tokens":2,"completion_tokens":1,"output_tokens":1,"prompt_tokens_details":{}}}`},
		{"openai", `{"usage":{"prompt_tokens":2,"prompt_tokens":3,"prompt_tokens_details":{}}}`},
		{"anthropic", `{"usage":{"input_tokens":1,"cache_read_input_tokens":-1}}`},
		{"bedrock", `{"usage":{"cacheReadInputTokens":1}}`},
		{"gemini", `{"usageMetadata":{"promptTokenCount":1.5}}`},
		{"unknown", `{"usage":{"input_tokens":1}}`},
	} {
		if evidence, ok := ExtractProviderUsage(test.provider, []byte(test.response)); ok {
			t.Fatalf("%s extracted %#v", test.provider, evidence)
		}
	}
}

func TestProviderUsageRejectsCounterSumOverflowAndCountersAboveTotal(t *testing.T) {
	maximum := strconv.FormatInt(int64(math.MaxInt), 10)
	overflow := []byte(`{"cache_creation":{"ephemeral_5m_input_tokens":` + maximum + `,"ephemeral_1h_input_tokens":1}}`)
	if _, ok := NormalizeRawCacheUsage("anthropic", overflow); ok {
		t.Fatal("overflowing Anthropic cache creation details accepted")
	}
	openAI := []byte(`{"usage":{"prompt_tokens":10,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":8,"cache_write_tokens":3}}}`)
	if _, ok := ExtractProviderUsage("openai", openAI); ok {
		t.Fatal("OpenAI cache counters above total input accepted")
	}
	gemini := []byte(`{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1,"cachedContentTokenCount":11}}`)
	if _, ok := ExtractProviderUsage("gemini", gemini); ok {
		t.Fatal("Gemini cached input above total input accepted")
	}
}
