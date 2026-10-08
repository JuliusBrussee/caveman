package providers

import (
	"reflect"
	"testing"
)

func usageStreamCorpus() []string {
	sse := func(event, data string) string {
		if event == "" {
			return "data: " + data + "\n\n"
		}
		return "event: " + event + "\ndata: " + data + "\n\n"
	}
	delta := sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`)
	return []string{
		sse("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":4,"output_tokens":1}}}`) + delta +
			sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`) + sse("message_stop", `{"type":"message_stop"}`),
		sse("message_start", `{"message":{"usage":{"input_tokens":10,"output_tokens":1}}}`) + sse("message_delta", `{"usage":{"output_tokens":5}}`) + sse("message_stop", `{}`),
		sse("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`) + delta,
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"refusal"}}`),
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"REFUSAL"},"usage":{"output_tokens":0,"iterations":[{}]}}`),
		sse("error", `{"type":"error","error":{"type":"overloaded_error"}}`),
		sse("", `{"type":"error"}`), sse("", `{"error":"boom"}`), sse("", `{"error":{}}`), sse("", `{"error":""}`),
		sse("", `{"\u0075sage":{"input_tokens":7,"output_tokens":2}}`),
		sse("", `{"type":"m\u0065ssage_start"}`),
		sse("", `{"choices":[{"delta":{"content":"usage"}}],"usage":null,"service_tier":"default"}`) +
			sse("", `{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":3}}}`) + "data: [DONE]\n\n",
		sse("response.created", `{"type":"response.created","response":{"id":"r","usage":null}}`) +
			sse("response.output_text.delta", `{"type":"response.output_text.delta","delta":"the response usage"}`) +
			sse("response.completed", `{"type":"response.completed","response":{"service_tier":"flex","usage":{"input_tokens":10,"output_tokens":5}}}`),
		sse("", `{"candidates":[{"content":{"parts":[{"text":"a"}]}}],"usageMetadata":{"promptTokenCount":10}}`) +
			sse("", `{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3,"thoughtsTokenCount":1}}`),
		sse("", `{"candidates":[{"content":{"parts":[{"text":"a"}]}}]}`) + sse("", `{"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"promptTokenCount":4}}`),
		sse("", `{"usage":{"input_tokens":3,"output_tokens":1,"server_tool_use":{"web_search_requests":2}}}`),
		sse("", `{"usage":{"input_tokens":3,"output_tokens":1},"web_search_requests":1,"search_queries":"x","grounding_queries":2}`),
		sse("", `{"trafficType":"ON_DEMAND","usage":{"input_tokens":3,"output_tokens":1}}`) + sse("", `{"traffic_type":""}`),
		sse("", `{"inference_geo":"us","usage":{"input_tokens":3,"output_tokens":1}}`),
		// Each name below is the only one its line spells.
		sse("", `{"type":"x","delta":{"stop_reason":"refusal"}}`), sse("", `{"stop_reason":"refusal"}`),
		sse("", `{"service_tier":"flex"}`), sse("", `{"serviceTier":""}`), sse("", `{"trafficType":"PROVISIONED_THROUGHPUT"}`),
		sse("", `{"traffic_type":7}`), sse("", `{"inference_geo":"eu"}`), sse("", `{"web_search_requests":2}`),
		sse("", `{"search_queries":"bad"}`), sse("", `{"grounding_queries":1}`), sse("", `{"error":{"code":1}}`),
		sse("", `{"type":"error"}`) + sse("", `{"type":"message_start"}`) + sse("", `{"type":"message_delta"}`) + sse("", `{"type":"message_stop"}`),
		sse("", `{"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1}}`) + sse("", `{"candidates":[{"finishReason":"STOP"}]}`),
		sse("", `{"usageMetadata":{"promptTokenCount":3}}`) + sse("", `{"promptFeedback":{"blockReason":"SAFETY"}}`),
		sse("message_start", `{"x":1}`) + sse("message_delta", `{"x":2}`) + sse("message_stop", `{"x":3}`) + sse("error", `{"x":4}`),
		sse("", `{"usage":{"input_tokens":5,"output_tokens":3}}`) + sse("", `{"type":"message_start"}`),
		sse("", `{"usage":{"input_tokens":5,"output_tokens":3}}`) + sse("message_start", `{"x":1}`),
		sse("", `{"usage":{"input_tokens":5,"output_tokens":3}}`) + sse("message_delta", `{"x":1}`),
		sse("", `{"usage":{"input_tokens":5,"output_tokens":3}}`) + sse("message_stop", `{"x":1}`),
		sse("", `{"serviceTier":{"type":"priority"},"usage":{"input_tokens":3,"output_tokens":1}}`),
		"data: not json\n\n" + sse("", `[1,2]`) + sse("", `{"usage":{"input_tokens":1,"output_tokens":1}} trailing`) + ": comment\n\n",
		`{"usage":{"input_tokens":3,"output_tokens":1}}` + "\n" + `{"usage":{"input_tokens":5,"output_tokens":2}}` + "\n",
		`{"id":"msg","usage":{"input_tokens":3,"output_tokens":1}}`,
		`[{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1}}]`,
	}
}

func checkUsageLineFilter(t *testing.T, data []byte) {
	t.Helper()
	for _, provider := range []string{"anthropic", "openai", "gemini", "vertex", "bedrock"} {
		var got, want UsageObservation
		parseUsageBytes(provider, data, &got, mayCarryUsage)
		parseUsageBytes(provider, data, &want, func(string, string) bool { return true })
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s usage differs for %q:\n got %+v\nwant %+v", provider, truncateForLog(data), got, want)
		}
	}
}

// TestUsageLineFilterMatchesDecodingEveryLine: skipping the lines
// mayCarryUsage rules out leaves every observed value as it was.
func TestUsageLineFilterMatchesDecodingEveryLine(t *testing.T) {
	for _, stream := range usageStreamCorpus() {
		checkUsageLineFilter(t, []byte(stream))
	}
}

func FuzzUsageLineFilter(f *testing.F) {
	for _, stream := range usageStreamCorpus() {
		f.Add([]byte(stream))
	}
	f.Fuzz(func(t *testing.T, data []byte) { checkUsageLineFilter(t, data) })
}

func truncateForLog(data []byte) string {
	if len(data) > 200 {
		return string(data[:200]) + "…"
	}
	return string(data)
}
