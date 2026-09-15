package openaicompat

import (
	"net/http"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

// The fixtures below are captured from a real Anthropic-compatible upstream
// (Z.AI GLM, issue #1026) whose usage block follows ANTHROPIC cache semantics:
// input_tokens EXCLUDES cached tokens, cache telemetry rides the final usage
// block, message_start always reports zeros, and nonstandard extra fields
// (server_tool_use, service_tier) appear alongside the counters.

// zaiWarmStream is a complete SSE response on a cache-warm request: the final
// usage block reports 36 fresh input tokens plus a 1280-token cache read.
const zaiWarmStream = `event: message_start
data: {"type": "message_start", "message": {"id": "msg_warm", "type": "message", "role": "assistant", "model": "glm-5.3", "content": [], "stop_reason": null, "stop_sequence": null, "usage": {"input_tokens": 0, "output_tokens": 0}}}

event: content_block_start
data: {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}

event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "ok"}}

event: content_block_stop
data: {"type": "content_block_stop", "index": 0}

event: message_delta
data: {"type": "message_delta", "delta": {"stop_reason": "end_turn", "stop_sequence": null}, "usage": {"input_tokens": 36, "output_tokens": 4, "cache_read_input_tokens": 1280, "server_tool_use": {"web_search_requests": 0}, "service_tier": "standard"}}

event: message_stop
data: {"type": "message_stop"}
`

// zaiColdStream is the same request one turn earlier, before the prompt cache is
// warm: the full prompt is fresh input and the cache read is zero.
const zaiColdStream = `event: message_start
data: {"type": "message_start", "message": {"id": "msg_cold", "type": "message", "role": "assistant", "model": "glm-5.3", "content": [], "stop_reason": null, "stop_sequence": null, "usage": {"input_tokens": 0, "output_tokens": 0}}}

event: message_delta
data: {"type": "message_delta", "delta": {"stop_reason": "max_tokens", "stop_sequence": null}, "usage": {"input_tokens": 1316, "output_tokens": 16, "cache_read_input_tokens": 0, "server_tool_use": {"web_search_requests": 0}, "service_tier": "standard"}}

event: message_stop
data: {"type": "message_stop"}
`

// zaiNonStreamWarm is a non-streaming response body with the same warm usage.
const zaiNonStreamWarm = `{"id": "msg_ns", "type": "message", "role": "assistant", "model": "glm-5.3", "content": [{"type": "text", "text": "ok"}], "stop_reason": "end_turn", "stop_sequence": null, "usage": {"input_tokens": 36, "output_tokens": 4, "cache_read_input_tokens": 1280, "server_tool_use": {"web_search_requests": 0}, "service_tier": "standard"}}`

func scanUsage(t *testing.T, adapter providers.Adapter, body string) providers.UsageObservation {
	t.Helper()
	scanner := adapter.NewUsageScanner(http.Header{})
	if _, err := scanner.Write([]byte(body)); err != nil {
		t.Fatalf("scanner write: %v", err)
	}
	return scanner.Usage()
}

// TestNamedMountAnthropicWireDialectStreamWarmCache verifies the three captured
// deviations parse as provider-complete: a missing cache_creation_input_tokens
// counts as zero, unknown extra fields are ignored, input_tokens comes from the
// final usage block despite message_start zeros, and — the core of the dialect —
// input_tokens EXCLUDES the cache read, so effective input is 36+1280 and the
// payload is NOT a cache-total-over-input contradiction.
func TestNamedMountAnthropicWireDialectStreamWarmCache(t *testing.T) {
	adapter, err := NewNamedWithWireDialect("zai", "https://api.example.test/api/anthropic", "anthropic")
	if err != nil {
		t.Fatalf("NewNamedWithWireDialect: %v", err)
	}
	usage := scanUsage(t, adapter, zaiWarmStream)
	if usage.Malformed || !usage.Complete() {
		t.Fatalf("warm stream usage = %+v, want complete (issue #1026: malformed on cache-warm rows)", usage)
	}
	if usage.InputTokens != 1316 {
		t.Errorf("InputTokens = %d, want 36 fresh + 1280 cached = 1316", usage.InputTokens)
	}
	if usage.OutputTokens != 4 {
		t.Errorf("OutputTokens = %d, want 4", usage.OutputTokens)
	}
	if usage.CachedInputTokens != 1280 {
		t.Errorf("CachedInputTokens = %d, want 1280", usage.CachedInputTokens)
	}
	if usage.CacheCreationInputTokens != 0 {
		t.Errorf("CacheCreationInputTokens = %d, want 0 (field absent counts as zero)", usage.CacheCreationInputTokens)
	}
	if usage.CacheStatus != "hit" {
		t.Errorf("CacheStatus = %q, want hit", usage.CacheStatus)
	}
}

func TestNamedMountAnthropicWireDialectStreamColdCache(t *testing.T) {
	adapter, err := NewNamedWithWireDialect("zai", "https://api.example.test/api/anthropic", "anthropic")
	if err != nil {
		t.Fatalf("NewNamedWithWireDialect: %v", err)
	}
	usage := scanUsage(t, adapter, zaiColdStream)
	if usage.Malformed || !usage.Complete() {
		t.Fatalf("cold stream usage = %+v, want complete", usage)
	}
	if usage.InputTokens != 1316 {
		t.Errorf("InputTokens = %d, want 1316", usage.InputTokens)
	}
	if usage.CachedInputTokens != 0 || usage.CacheStatus != "miss" {
		t.Errorf("cached = %d, status = %q, want 0/miss", usage.CachedInputTokens, usage.CacheStatus)
	}
}

func TestNamedMountAnthropicWireDialectNonStream(t *testing.T) {
	adapter, err := NewNamedWithWireDialect("zai", "https://api.example.test/api/anthropic", "anthropic")
	if err != nil {
		t.Fatalf("NewNamedWithWireDialect: %v", err)
	}
	usage, _, err := adapter.ParseUsage(t.Context(), http.Header{}, strings.NewReader(zaiNonStreamWarm))
	if err != nil {
		t.Fatalf("ParseUsage: %v", err)
	}
	if usage.Malformed || !usage.Complete() {
		t.Fatalf("non-stream usage = %+v, want complete", usage)
	}
	if usage.InputTokens != 1316 || usage.CachedInputTokens != 1280 || usage.CacheStatus != "hit" {
		t.Errorf("usage = input %d, cached %d, status %q; want 1316/1280/hit", usage.InputTokens, usage.CachedInputTokens, usage.CacheStatus)
	}
}

// TestNamedMountDefaultWireDialectUnchanged pins the default: without the
// opt-in dialect the shared OpenAI-shape parser keeps its existing verdict for
// this payload (cache total exceeds the inclusive input total → malformed), so
// first-party and default-mount behavior is untouched by the dialect option.
func TestNamedMountDefaultWireDialectUnchanged(t *testing.T) {
	adapter, err := NewNamed("zai", "https://api.example.test/api/anthropic")
	if err != nil {
		t.Fatalf("NewNamed: %v", err)
	}
	usage := scanUsage(t, adapter, zaiWarmStream)
	if !usage.Malformed {
		t.Fatalf("default dialect usage = %+v, want malformed verdict preserved without opt-in", usage)
	}
}

func TestNewNamedWithWireDialectRejectsUnknownDialect(t *testing.T) {
	if _, err := NewNamedWithWireDialect("zai", "https://api.example.test", "openai-ish"); err == nil {
		t.Fatal("unknown usage dialect accepted, want error")
	}
}
