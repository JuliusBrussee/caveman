package gateway

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/azureopenai"
	"github.com/JuliusBrussee/caveman/proxy/providers/bedrock"
	"github.com/JuliusBrussee/caveman/proxy/providers/gemini"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
	"github.com/JuliusBrussee/caveman/proxy/providers/openaicompat"
	"github.com/JuliusBrussee/caveman/proxy/providers/vertex"
)

// The provider flushes headers, then waits for the test to receive them before
// producing a ping. It then waits again before finishing. This detects both
// header buffering and full-body buffering without a long wall-clock soak.
func TestProviderStreamsFlushBeforeCompletion(t *testing.T) {
	cases := []struct {
		name, path, upstream, mediaType string
		adapter                         func(string) providers.Adapter
	}{
		{"anthropic", "/v1/messages", "", "text/event-stream", anthropic.New},
		{"openai-chat", "/v1/chat/completions", "", "text/event-stream", openai.New},
		{"openai-responses", "/v1/responses", "", "text/event-stream", openai.New},
		{"azure", "/azure/openai/deployments/test/chat/completions?api-version=2024-10-21", "", "text/event-stream", azureopenai.New},
		{"gemini", "/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", "", "text/event-stream", gemini.New},
		{"vertex", "/vertex/v1/projects/test/locations/us-central1/publishers/anthropic/models/claude-sonnet-4-5:streamRawPredict", "", "text/event-stream", vertex.New},
		{"compat", "/compat/v1/chat/completions", "", "text/event-stream", openaicompat.New},
		{"bedrock", "/bedrock/model/anthropic.claude-sonnet-4-5-20250929-v1:0/converse-stream", "https://bedrock-runtime.us-east-1.amazonaws.com", "application/vnd.amazon.eventstream", bedrock.New},
		{"chatgpt", "/chatgpt/responses", "", "text/event-stream", openai.New},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CAVE_GATEWAY_UPSTREAM_TIMEOUT_MS", "")
			allowBody, allowEnd := make(chan struct{}), make(chan struct{})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			const ping = "event: ping\ndata: {\"type\":\"ping\"}\n\n"
			const end = "event: future_provider_event\ndata: {}\n\n"
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.mediaType)
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				select {
				case <-allowBody:
				case <-r.Context().Done():
					return
				}
				_, _ = io.WriteString(w, ping)
				w.(http.Flusher).Flush()
				select {
				case <-allowEnd:
				case <-r.Context().Done():
					return
				}
				_, _ = io.WriteString(w, end)
			}))
			defer upstream.Close()
			base := tc.upstream
			if base == "" {
				base = upstream.URL
			}
			transport := http.DefaultTransport.(*http.Transport).Clone()
			defer transport.CloseIdleConnections()
			s := New(Config{
				Adapters: []providers.Adapter{tc.adapter(base)},
				Auth:     stubAuth{rc: RequestContext{RuntimeMode: "record"}}, Creds: stubCreds{key: "sk-test"},
				ChatGPTUpstream: upstream.URL,
			})
			if s.httpClient.Timeout != 0 {
				t.Fatalf("default request lifetime = %v, want no total deadline", s.httpClient.Timeout)
			}
			// Keep the real adapter's route/signing validation, but send its wire
			// request to a local stub, never a real provider.
			s.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				r.URL.Scheme = "http"
				r.URL.Host = strings.TrimPrefix(upstream.URL, "http://")
				return transport.RoundTrip(r)
			})
			proxy := httptest.NewServer(s.Handler())
			defer proxy.Close()
			// Deliberately no JSON stream flag: wire protocol must win.
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, proxy.URL+tc.path, strings.NewReader(`{"model":"test","messages":[]}`))
			resp, err := proxy.Client().Do(req)
			if err != nil {
				t.Fatalf("headers blocked before body: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				b, _ := io.ReadAll(resp.Body)
				t.Fatalf("status=%d body=%s", resp.StatusCode, b)
			}
			close(allowBody)
			first := make([]byte, len(ping))
			if _, err := io.ReadFull(resp.Body, first); err != nil {
				t.Fatalf("first event blocked until completion: %v", err)
			}
			if string(first) != ping {
				t.Fatalf("first event changed: %q", first)
			}
			close(allowEnd)
			rest, err := io.ReadAll(resp.Body)
			if err != nil || string(rest) != end {
				t.Fatalf("unknown event changed: body=%q error=%v", rest, err)
			}
		})
	}
}

func TestEncodedRequestPassesThroughWithoutTransforms(t *testing.T) {
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	_, _ = io.WriteString(zw, `{"model":"test","messages":[]}`)
	_ = zw.Close()
	for _, mode := range []string{"record", "compress", "pixel", "active"} {
		t.Run(mode, func(t *testing.T) {
			s := New(Config{
				Adapters: []providers.Adapter{mutatingTransformAdapter{Adapter: anthropic.New("https://provider.test")}},
				Auth:     stubAuth{rc: RequestContext{RuntimeMode: mode}}, Creds: stubCreds{key: "sk-test"},
				HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					body, _ := io.ReadAll(r.Body)
					if !bytes.Equal(body, compressed.Bytes()) || r.Header.Get("Content-Encoding") != "gzip" {
						t.Errorf("encoded request corrupted: body_equal=%v encoding=%q", bytes.Equal(body, compressed.Bytes()), r.Header.Get("Content-Encoding"))
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
				})},
			})
			r := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(compressed.Bytes()))
			r.Header.Set("Content-Encoding", "gzip")
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != 200 || w.Header().Get("x-cave-optimization") != "none" {
				t.Fatalf("opaque body was transformed: status=%d headers=%v", w.Code, w.Header())
			}
		})
	}
}

func TestInterruptedStreamsAbortClientFramingWithoutReplay(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/chatgpt/responses"} {
		for _, http2 := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/http2=%v", path, http2), func(t *testing.T) {
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "event: ping\ndata: {}\n\n")
					w.(http.Flusher).Flush()
					panic(http.ErrAbortHandler)
				}))
				defer upstream.Close()
				sink := &captureSink{}
				s := New(Config{Adapters: []providers.Adapter{anthropic.New(upstream.URL)}, Auth: stubAuth{}, Creds: stubCreds{}, Sink: sink, HTTPClient: upstream.Client(), ChatGPTUpstream: upstream.URL})
				proxy := httptest.NewUnstartedServer(s.Handler())
				proxy.EnableHTTP2 = http2
				proxy.StartTLS()
				defer proxy.Close()
				resp, err := proxy.Client().Post(proxy.URL+path, "application/json", strings.NewReader(`{"stream":true}`))
				if err != nil {
					t.Fatal(err)
				}
				_, err = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if err == nil {
					t.Fatal("truncated upstream became a clean client EOF")
				}
				if calls.Load() != 1 {
					t.Fatalf("partial stream replayed %d times", calls.Load())
				}
				if row := sink.last(t); row.ErrorCode != "cave_upstream_body_read_failed" {
					t.Fatalf("interruption recorded as success: %+v", row)
				}
			})
		}
	}
}

func TestClientCancellationReleasesUpstream(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/chatgpt/responses"} {
		t.Run(path, func(t *testing.T) {
			started, stopped := make(chan struct{}), make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				close(started)
				<-r.Context().Done()
				close(stopped)
			}))
			defer upstream.Close()
			s := New(Config{Adapters: []providers.Adapter{anthropic.New(upstream.URL)}, Auth: stubAuth{}, Creds: stubCreds{}, HTTPClient: upstream.Client(), ChatGPTUpstream: upstream.URL})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"stream":true}`)).WithContext(ctx)
			done := make(chan struct{})
			go func() { defer close(done); s.Handler().ServeHTTP(httptest.NewRecorder(), r) }()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("upstream not reached")
			}
			cancel()
			select {
			case <-stopped:
			case <-time.After(2 * time.Second):
				t.Fatal("canceled client left upstream running")
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("canceled proxy handler stuck")
			}
		})
	}
}
