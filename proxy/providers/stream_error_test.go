package providers

import (
	"net/http"
	"strings"
	"testing"
)

func TestProviderErrorEnvelopeDoesNotInspectModelContent(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"OpenAI error", `{"error":{"type":"server_error","message":"private provider detail"}}`, true},
		{"OpenAI failed response", `{"type":"response.failed","response":{"error":{"code":"server_error"}}}`, true},
		{"Anthropic error event", "event: error\ndata: {\"message\":\"private detail\"}\n\n", true},
		{"Google array error", `[{"candidates":[]},{"error":{"code":503}}]`, true},
		{"null error", `{"error":null}`, false},
		{"empty error", `{"error":{}}`, false},
		{"tool payload", `{"choices":[{"message":{"tool_calls":[{"function":{"arguments":"{\"error\":{\"message\":\"example\"}}"}}]}}]}`, false},
		{"ordinary content", `{"candidates":[{"content":{"parts":[{"text":"event: error"}]}}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var usage UsageObservation
			ParseUsageBytes("openai", []byte(tc.body), &usage)
			if usage.ProviderError != tc.want {
				t.Fatalf("ProviderError=%v, want %v", usage.ProviderError, tc.want)
			}
			if strings.Contains(string(usage.RawUsage), "private") {
				t.Fatal("provider error message entered raw usage")
			}
		})
	}
}

func TestUsageScannerRetainsObservedErrorFromBoundedTail(t *testing.T) {
	scanner := (Base{Provider: "openai"}).NewUsageScanner(http.Header{})
	scanner.SetLimit(256)
	_, _ = scanner.Write([]byte(strings.Repeat("data: {\"content\":\"some streamed output\"}\n\n", 100)))
	_, _ = scanner.Write([]byte("event: error\ndata: {\"error\":{\"code\":\"server_error\"}}\n\n"))
	got := scanner.Usage()
	if !got.ProviderError || got.Complete() || got.PricingUnsupportedReason != "response_scan_limit_exceeded" {
		t.Fatalf("observed error was lost while discarding incomplete usage: %+v", got)
	}
}

func TestAnthropicEventNameDefinesIncompleteUsageWithoutJSONType(t *testing.T) {
	// The Anthropic SDK fills a missing JSON type from the SSE event name.
	// Without doing the same, message_start's provisional output looks final.
	data := "event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n" +
		"event: error\ndata: {\"message\":\"provider unavailable\"}\n\n"
	var usage UsageObservation
	ParseUsageBytes("anthropic", []byte(data), &usage)
	if !usage.ProviderError || usage.Complete() || usage.OutputTokensReported || usage.OutputTokens != 0 {
		t.Fatalf("SSE event name failed to identify provisional usage: %+v", usage)
	}
}
