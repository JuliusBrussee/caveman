package standalone

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/config"
	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

type protocolSink chan gateway.RequestRecord

func (s protocolSink) Record(record gateway.RequestRecord) { s <- record }

// These fixtures follow the provider SDK's native wire, including raw AWS
// EventStream payloads (the event name is in a binary header, not a JSON wrapper).
// Sources: anthropic-sdk-python/src/anthropic/_streaming.py;
// openai-python/src/openai/_streaming.py; python-genai/google/genai/_api_client.py;
// botocore/eventstream.py and botocore/parsers.py::EventStreamJSONParser.
func TestStandaloneNativeStreamsAndProviderErrors(t *testing.T) {
	const anthropicStart = `{"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`
	const anthropicEnd = `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}`
	const anthropicError = `{"type":"error","error":{"type":"overloaded_error","message":"provider unavailable"}}`
	const chatStart = `{"id":"chat_test","object":"chat.completion.chunk","model":"gpt-5.5","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}],"usage":null}`
	const chatEnd = `{"id":"chat_test","object":"chat.completion.chunk","model":"gpt-5.5","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
	const openaiError = `{"error":{"message":"provider unavailable","type":"server_error","code":"server_error"}}`
	const geminiStart = `{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]}}]}`
	const geminiEnd = `{"candidates":[{"content":{"role":"model","parts":[{"text":"world"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`
	const googleError = `{"error":{"code":503,"message":"provider unavailable","status":"UNAVAILABLE"}}`
	sse := func(event, body string) string {
		if event != "" {
			return "event: " + event + "\ndata: " + body + "\n\n"
		}
		return "data: " + body + "\n\n"
	}
	chunk := func(body string) string {
		return string(nativeEventFrame("event", "chunk", `{"bytes":"`+base64.StdEncoding.EncodeToString([]byte(body))+`"}`))
	}
	for _, tc := range []struct {
		name, provider, path, body, media, first, final, streamError string
	}{
		{"Anthropic SSE", "anthropic", "/anthropic/v1/messages", `{"model":"claude-sonnet-4-6","max_tokens":16,"stream":true,"messages":[]}`, "text/event-stream", sse("message_start", anthropicStart), sse("message_delta", anthropicEnd) + sse("message_stop", `{"type":"message_stop"}`), sse("error", anthropicError)},
		{"Anthropic SSE event name without JSON type", "anthropic", "/anthropic/v1/messages", `{"model":"claude-sonnet-4-6","max_tokens":16,"stream":true,"messages":[]}`, "text/event-stream", sse("message_start", strings.Replace(anthropicStart, `"type":"message_start",`, "", 1)), sse("message_delta", strings.Replace(anthropicEnd, `"type":"message_delta",`, "", 1)) + sse("message_stop", `{}`), sse("error", `{"message":"provider unavailable"}`)},
		{"OpenAI Chat SSE", "openai", "/openai/v1/chat/completions", `{"model":"gpt-5.5","stream":true,"messages":[]}`, "text/event-stream", sse("", chatStart), sse("", chatEnd) + "data: [DONE]\n\n", sse("error", openaiError)},
		{"OpenAI Responses SSE", "openai", "/openai/v1/responses", `{"model":"gpt-5.5","stream":true,"input":"hello"}`, "text/event-stream", sse("response.created", `{"type":"response.created","response":{"id":"resp_test","status":"in_progress","usage":null}}`), sse("response.completed", `{"type":"response.completed","response":{"id":"resp_test","status":"completed","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`), sse("error", openaiError)},
		{"Gemini stable SSE", "gemini", "/gemini/v1/models/gemini-2.5-pro:streamGenerateContent?alt=sse", `{"contents":[]}`, "text/event-stream", sse("", geminiStart), sse("", geminiEnd), sse("", googleError)},
		{"Gemini beta JSON array", "gemini", "/gemini/v1beta/models/gemini-2.5-pro:streamGenerateContent", `{"contents":[]}`, "application/json", "[" + geminiStart + ",", geminiEnd + "]", googleError + "]"},
		{"Azure SSE", "azure_openai", "/azure/openai/deployments/test/chat/completions?api-version=2024-10-21", `{"model":"gpt-5.5","stream":true,"messages":[]}`, "text/event-stream", sse("", `{"choices":[],"prompt_annotations":[]}`) + sse("", chatStart), sse("", chatEnd) + "data: [DONE]\n\n", sse("error", openaiError)},
		{"Vertex Google SSE", "vertex", "/vertex/v1/projects/test/locations/us-central1/publishers/google/models/gemini-2.5-pro:streamGenerateContent?alt=sse", `{"contents":[]}`, "text/event-stream", sse("", geminiStart), sse("", geminiEnd), sse("", googleError)},
		{"Vertex Anthropic SSE", "vertex", "/vertex/v1/projects/test/locations/us-central1/publishers/anthropic/models/claude-sonnet-4-6:streamRawPredict", `{"anthropic_version":"vertex-2023-10-16","stream":true,"messages":[]}`, "text/event-stream", sse("message_start", anthropicStart), sse("message_delta", anthropicEnd) + sse("message_stop", `{"type":"message_stop"}`), sse("error", anthropicError)},
		{"Bedrock Converse EventStream", "bedrock", "/bedrock/model/global.anthropic.claude-sonnet-4-6/converse-stream", `{"messages":[]}`, "application/vnd.amazon.eventstream", string(nativeEventFrame("event", "messageStart", `{"role":"assistant"}`)), string(nativeEventFrame("event", "messageStop", `{"stopReason":"end_turn"}`)) + string(nativeEventFrame("event", "metadata", `{"usage":{"inputTokens":10,"outputTokens":5,"totalTokens":15},"metrics":{"latencyMs":10}}`)), string(nativeEventFrame("exception", "modelStreamErrorException", `{"message":"provider unavailable","originalStatusCode":503}`))},
		{"Bedrock Invoke EventStream", "bedrock", "/bedrock/model/global.anthropic.claude-sonnet-4-6/invoke-with-response-stream", `{"anthropic_version":"bedrock-2023-05-31","messages":[]}`, "application/vnd.amazon.eventstream", chunk(anthropicStart), chunk(anthropicEnd) + chunk(`{"type":"message_stop"}`), string(nativeEventFrame("exception", "modelStreamErrorException", `{"message":"provider unavailable","originalStatusCode":503}`))},
		{"Bedrock Mantle SSE", "bedrock", "/bedrock/anthropic/v1/messages", `{"model":"anthropic.claude-sonnet-4-6","stream":true,"messages":[]}`, "text/event-stream", sse("message_start", anthropicStart), sse("message_delta", anthropicEnd) + sse("message_stop", `{"type":"message_stop"}`), sse("error", anthropicError)},
		{"Bedrock Mantle SSE event name without JSON type", "bedrock", "/bedrock/anthropic/v1/messages", `{"model":"anthropic.claude-sonnet-4-6","stream":true,"messages":[]}`, "text/event-stream", sse("message_start", strings.Replace(anthropicStart, `"type":"message_start",`, "", 1)), sse("message_delta", strings.Replace(anthropicEnd, `"type":"message_delta",`, "", 1)) + sse("message_stop", `{}`), sse("error", `{"message":"provider unavailable"}`)},
		{"Named compatible Chat SSE", "openai_compatible", "/compat/protocol-test/v1/chat/completions", `{"model":"gpt-5.5","stream":true,"messages":[]}`, "text/event-stream", sse("", chatStart), sse("", chatEnd) + "data: [DONE]\n\n", sse("error", openaiError)},
	} {
		for _, scenario := range []string{"success", "http_error", "stream_error"} {
			t.Run(tc.name+"/"+scenario, func(t *testing.T) {
				t.Setenv("CAVE_BEDROCK_MANTLE_ENABLED", "true")
				release := make(chan struct{})
				defer func() {
					select {
					case <-release:
					default:
						close(release)
					}
				}()
				wantStatus, wantBody := http.StatusOK, tc.first+tc.final
				if scenario == "stream_error" {
					wantBody = tc.first + tc.streamError
				} else if scenario == "http_error" {
					wantStatus, wantBody = http.StatusTooManyRequests, `{"error":{"message":"provider rate limit","type":"rate_limit_error","code":429}}`
				}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					if string(body) != tc.body {
						t.Error("native request body changed")
					}
					w.Header().Set("Content-Type", tc.media)
					w.Header().Set("X-Request-Id", "native-request-id")
					w.Header().Set("Retry-After", "17")
					w.WriteHeader(wantStatus)
					if scenario == "http_error" {
						_, _ = io.WriteString(w, wantBody)
						return
					}
					_, _ = io.WriteString(w, tc.first)
					w.(http.Flusher).Flush()
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					_, _ = io.WriteString(w, strings.TrimPrefix(wantBody, tc.first))
				}))
				defer upstream.Close()
				sink := make(protocolSink, 1)
				cfg := config.Config{Mode: "record", Providers: map[string]config.ProviderConfig{tc.provider: {BaseURL: upstream.URL}}}
				if tc.provider == "openai_compatible" {
					cfg.Compat = map[string]config.CompatConfig{"protocol-test": {BaseURL: upstream.URL}}
				}
				srv := httptest.NewServer(New(cfg, sink, Options{HTTPClient: &http.Client{}}).Handler())
				defer srv.Close()
				req, err := http.NewRequest(http.MethodPost, srv.URL+tc.path, strings.NewReader(tc.body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("x-api-key", "sk-protocol-test-key")
				req.Header.Set("Accept-Encoding", "identity")
				client := &http.Client{Timeout: 5 * time.Second}
				resp, err := client.Do(req)
				if err != nil {
					t.Fatalf("response did not start before upstream completion: %v", err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != wantStatus || resp.Header.Get("Retry-After") != "17" || resp.Header.Get("X-Request-Id") != "native-request-id" {
					t.Fatalf("provider status/headers changed: status=%d headers=%v", resp.StatusCode, resp.Header)
				}
				var first []byte
				if scenario != "http_error" {
					first = make([]byte, len(tc.first))
					if _, err := io.ReadFull(resp.Body, first); err != nil {
						t.Fatalf("first event was buffered until completion: %v", err)
					}
					close(release)
				}
				rest, err := io.ReadAll(resp.Body)
				if err != nil || string(append(first, rest...)) != wantBody {
					t.Fatalf("native response bytes changed: err=%v", err)
				}
				select {
				case record := <-sink:
					switch scenario {
					case "success":
						if record.InputTokens != 10 || record.OutputTokens != 5 || record.TokenUsageBasis != "provider_complete" || record.ErrorCode != "" {
							t.Fatalf("native final usage not observed: %+v", record)
						}
					case "http_error":
						if record.StatusCode != 429 || record.ErrorCode != "provider_429" || record.TotalCostUSD != 0 {
							t.Fatalf("provider HTTP error misrecorded: %+v", record)
						}
					case "stream_error":
						if record.StatusCode != 200 || record.ErrorCode != "provider_stream_error" || record.TokenUsageBasis == "provider_complete" || record.TotalCostUSD != 0 {
							t.Fatalf("provider stream error misrecorded: %+v", record)
						}
					}
				case <-time.After(5 * time.Second):
					t.Fatal("missing native request record")
				}
			})
		}
	}
}

func TestStandaloneStreamErrorKeepsBilledUsageWithoutClaimingSavings(t *testing.T) {
	const usageEvents = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":1000,\"output_tokens\":1}}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5}}\n\n"
	var records []gateway.RequestRecord
	for _, failed := range []bool{false, true} {
		response := usageEvents + "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
		if failed {
			response = usageEvents + "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"provider error after billed output\"}}\n\n"
		}
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, response)
		}))
		sink := make(protocolSink, 1)
		transport := statsStreamRoundTripper(func(r *http.Request) (*http.Response, error) {
			// Route the fixture to loopback while preserving its logical provider
			// origin so the report can capture the official catalog price.
			clone := r.Clone(r.Context())
			clone.URL.Scheme = "http"
			clone.URL.Host = strings.TrimPrefix(upstream.URL, "http://")
			response, err := http.DefaultTransport.RoundTrip(clone)
			if response != nil {
				response.Request = r
			}
			return response, err
		})
		srv := New(config.Config{Mode: "active", Providers: map[string]config.ProviderConfig{"anthropic": {BaseURL: "https://api.anthropic.com"}},
			Optimizers: map[string]bool{"anthropic-cache-breakpoints": true}}, sink, Options{HTTPClient: &http.Client{Transport: transport}})
		req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-6","max_tokens":16,"stream":true,"system":"a stable prefix","messages":[{"role":"user","content":"hello"}]}`))
		req.Header.Set("x-api-key", "sk-test-key")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		upstream.Close()
		if rec.Code != 200 || rec.Body.String() != response {
			t.Fatal("provider stream was modified")
		}
		records = append(records, <-sink)
	}
	success, failed := records[0], records[1]
	if success.SavingsUSD <= 0 {
		t.Fatalf("successful baseline did not exercise cache-savings accounting: %+v", success)
	}
	if failed.ErrorCode != "provider_stream_error" || failed.SavingsUSD != 0 || failed.TokenUsageBasis != "provider_complete" ||
		failed.InputTokens != 1010 || failed.OutputTokens != 5 || failed.TotalCostUSD <= 0 || failed.TotalCostUSD != success.TotalCostUSD {
		t.Fatalf("failed stream lost billed usage or claimed savings: %+v", failed)
	}
	ledger, err := store.Open(filepath.Join(t.TempDir(), "stats.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	ledger.Record(success)
	ledger.Record(failed)
	report, err := ledger.BuildStatsReport(store.StatsReportOptions{Days: 0, Now: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if report.Totals.CompleteUsageRequests != 2 || report.Totals.APISpendRequests != 2 || report.Totals.APISpendUSD == nil ||
		*report.Totals.APISpendUSD != success.TotalCostUSD+failed.TotalCostUSD {
		t.Fatalf("stats omitted complete billed stream-error usage: %+v", report.Totals)
	}
	if failed.RequestEstimatedInputDeltaUSD != nil || report.Totals.APISavingsRequests != 1 {
		t.Fatalf("stats credited failed-stream savings: failed=%+v totals=%+v", failed, report.Totals)
	}
}

type statsStreamRoundTripper func(*http.Request) (*http.Response, error)

func (f statsStreamRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// Smithy EventStream string headers have a one-byte name length, type 7,
// and a two-byte value length. Both CRCs cover the native wire bytes.
func nativeEventFrame(messageType, eventType, payload string) []byte {
	var headers bytes.Buffer
	eventHeader := ":event-type"
	if messageType == "exception" {
		eventHeader = ":exception-type"
	}
	for _, pair := range [][2]string{{":message-type", messageType}, {eventHeader, eventType}, {":content-type", "application/json"}} {
		headers.WriteByte(byte(len(pair[0])))
		headers.WriteString(pair[0])
		headers.WriteByte(7)
		_ = binary.Write(&headers, binary.BigEndian, uint16(len(pair[1])))
		headers.WriteString(pair[1])
	}
	frame := make([]byte, 12, 16+headers.Len()+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(16+headers.Len()+len(payload)))
	binary.BigEndian.PutUint32(frame[4:8], uint32(headers.Len()))
	binary.BigEndian.PutUint32(frame[8:12], crc32.ChecksumIEEE(frame[:8]))
	frame = append(frame, headers.Bytes()...)
	frame = append(frame, []byte(payload)...)
	return binary.BigEndian.AppendUint32(frame, crc32.ChecksumIEEE(frame))
}
