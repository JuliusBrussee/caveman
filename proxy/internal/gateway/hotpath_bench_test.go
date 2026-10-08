package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
)

// Request sizes of the hot-path benchmarks: roughly 2k, 50k and 200k
// tokens of an agent conversation.
var benchSizes = []struct {
	name  string
	bytes int
}{{"2k", 9_000}, {"50k", 180_000}, {"200k", 730_000}}

var benchBodies sync.Map

// benchAnthropicBody is a deterministic agent-shaped Messages request of
// about size bytes: system, tools, alternating turns and a cache breakpoint
// on the newest user block.
func benchAnthropicBody(size int, stream bool) []byte {
	key := fmt.Sprint(size, stream)
	if body, ok := benchBodies.Load(key); ok {
		return body.([]byte)
	}
	body := buildBenchAnthropicBody(size, stream)
	benchBodies.Store(key, body)
	return body
}

func buildBenchAnthropicBody(size int, stream bool) []byte {
	words := strings.Fields("the gateway reads the request and routes it to the provider while the cache keeps a stable prefix for every turn")
	text := func(i, n int) string {
		var b strings.Builder
		for j := 0; b.Len() < n; j++ {
			b.WriteString(words[(i+j)%len(words)])
			b.WriteByte(' ')
		}
		return b.String()
	}
	type block struct {
		Type         string          `json:"type"`
		Text         string          `json:"text,omitempty"`
		CacheControl json.RawMessage `json:"cache_control,omitempty"`
	}
	type message struct {
		Role    string  `json:"role"`
		Content []block `json:"content"`
	}
	tools := make([]map[string]any, 8)
	for i := range tools {
		tools[i] = map[string]any{"name": "tool_" + strconv.Itoa(i), "description": text(i, 400),
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}
	}
	req := map[string]any{
		"model": "claude-sonnet-4-5", "max_tokens": 32000, "stream": stream,
		"system": []block{{Type: "text", Text: text(0, 2_000)}},
		"tools":  tools,
	}
	var messages []message
	for i := 0; ; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, message{Role: role, Content: []block{{Type: "text", Text: text(i, 1_500)}}})
		req["messages"] = messages
		if len(messages)*1_550+20_000 < size {
			continue
		}
		if body, _ := json.Marshal(req); len(body) >= size && role == "user" {
			last := &messages[len(messages)-1].Content[0]
			last.CacheControl = json.RawMessage(`{"type":"ephemeral"}`)
			body, _ = json.Marshal(req)
			return body
		}
	}
}

const benchAnthropicResponse = `{"id":"msg_bench","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":1200,"cache_read_input_tokens":48000,"cache_creation_input_tokens":0,"output_tokens":60}}`

// benchAnthropicStream is a canned Messages stream: start, n text deltas,
// the terminal usage delta and stop.
func benchAnthropicStream(n int) string {
	var b strings.Builder
	b.WriteString("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_bench\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-5\",\"content\":[],\"usage\":{\"input_tokens\":1200,\"cache_read_input_tokens\":48000,\"cache_creation_input_tokens\":0,\"output_tokens\":1}}}\n\n")
	b.WriteString("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"word%d \"}}\n\n", i)
	}
	b.WriteString("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
	b.WriteString("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":60}}\n\n")
	b.WriteString("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	return b.String()
}

func benchUpstream(stream bool) http.RoundTripper {
	payload, contentType := benchAnthropicResponse, "application/json"
	if stream {
		payload, contentType = benchAnthropicStream(200), "text/event-stream"
	}
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		_, _ = io.Copy(io.Discard, r.Body)
		return &http.Response{StatusCode: http.StatusOK, Request: r,
			Header: http.Header{"Content-Type": []string{contentType}},
			Body:   io.NopCloser(strings.NewReader(payload))}, nil
	})
}

// rowSignal tells the benchmark each time a row reaches the sink.
type rowSignal chan struct{}

func (s rowSignal) Record(RequestRecord) { s <- struct{}{} }

// BenchmarkProxyHandler is one record-mode Messages request through the
// whole handler against an in-process upstream, wired as the binary ships
// (rows finished off the request path): the time until the handler returns,
// which is when the client's connection is free again. Like one agent, the
// next request starts once the previous row is written; that wait is not
// timed.
func BenchmarkProxyHandler(b *testing.B) {
	for _, size := range benchSizes {
		for _, stream := range []bool{false, true} {
			body := benchAnthropicBody(size.bytes, stream)
			b.Run(fmt.Sprintf("%s/stream=%v", size.name, stream), func(b *testing.B) {
				rows := make(rowSignal, 1)
				srv := New(Config{
					Adapters:    []providers.Adapter{anthropic.New("https://api.anthropic.com")},
					Auth:        stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
					Creds:       stubCreds{key: "sk-ant-bench"},
					Sink:        rows,
					HTTPClient:  &http.Client{Transport: benchUpstream(stream)},
					AsyncRecord: true,
				})
				defer srv.Close()
				handler := srv.Handler()
				b.SetBytes(int64(len(body)))
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
					req.Header.Set("x-api-key", "sk-ant-bench")
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, req)
					if rec.Code != http.StatusOK {
						b.Fatalf("status %d: %s", rec.Code, rec.Body.String())
					}
					b.StopTimer()
					<-rows
					b.StartTimer()
				}
				b.StopTimer()
			})
		}
	}
}

func benchPricedUsage() providers.UsageObservation {
	var usage providers.UsageObservation
	providers.ParseUsageBytes("anthropic", []byte(benchAnthropicResponse), &usage)
	return usage
}

// BenchmarkRequestAccounting is the request token count record() runs after
// the response: a pass-through request, so both sides are the same bytes.
func BenchmarkRequestAccounting(b *testing.B) {
	meta := providers.RequestMetadata{Provider: "anthropic", Model: "claude-sonnet-4-5"}
	usage := benchPricedUsage()
	for _, size := range benchSizes {
		body := benchAnthropicBody(size.bytes, false)
		b.Run(size.name, func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				row := RequestRecord{StatusCode: http.StatusOK, AuthMode: string(AuthModePAYG)}
				requestAccounting(&row, meta, usage, body, body, false)
				if row.RequestMeasurementStatus != "measured" {
					b.Fatalf("status %q", row.RequestMeasurementStatus)
				}
			}
		})
	}
}

// BenchmarkProviderPrefixEvidence is the frozen-prefix digest every request
// to an adapter with prefix evidence pays.
func BenchmarkProviderPrefixEvidence(b *testing.B) {
	adapter := anthropic.New("https://api.anthropic.com")
	meta := providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"}
	for _, size := range benchSizes {
		body := benchAnthropicBody(size.bytes, false)
		b.Run(size.name, func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, _, ok := providerPrefixEvidence(adapter, body, meta); !ok {
					b.Fatal("no prefix evidence")
				}
			}
		})
	}
}

// BenchmarkInspectRequest is the request metadata read every request starts
// with.
func BenchmarkInspectRequest(b *testing.B) {
	adapter := anthropic.New("https://api.anthropic.com")
	headers := http.Header{"X-Cave-Route-Path": []string{"/v1/messages"}}
	for _, size := range benchSizes {
		body := benchAnthropicBody(size.bytes, false)
		b.Run(size.name, func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if meta, err := adapter.InspectRequest(context.Background(), bytes.NewReader(body), headers); err != nil || meta.Model == "" {
					b.Fatalf("inspect: %v %+v", err, meta)
				}
			}
		})
	}
}

// BenchmarkParseUsageStream is the usage read of a finished Messages stream.
func BenchmarkParseUsageStream(b *testing.B) {
	for _, n := range []int{200, 2000} {
		stream := []byte(benchAnthropicStream(n))
		b.Run(strconv.Itoa(n)+"deltas", func(b *testing.B) {
			b.SetBytes(int64(len(stream)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var usage providers.UsageObservation
				providers.ParseUsageBytes("anthropic", stream, &usage)
				if !usage.Complete() {
					b.Fatal("usage incomplete")
				}
			}
		})
	}
}
