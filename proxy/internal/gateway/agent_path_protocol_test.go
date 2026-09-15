package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/gemini"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

// TestAgentPathAttributionAcrossWireProtocols keeps /w/<slug> attribution a
// proxy contract, independent of the CLI's compiled agent registry. An
// intentionally unregistered slug must survive every native coding-agent wire
// protocol while the prefix itself is removed before adapter routing.
func TestAgentPathAttributionAcrossWireProtocols(t *testing.T) {
	const agentSlug = "kilo-canary"

	tests := []struct {
		name         string
		path         string
		body         string
		responseBody string
		provider     string
		adapter      func(string) providers.Adapter
	}{
		{
			name:     "openai chat completions",
			path:     "/v1/chat/completions",
			body:     `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`,
			provider: "openai",
			adapter:  openai.New,
			responseBody: `{"id":"chatcmpl_1","object":"chat.completion","model":"gpt-4o-mini",` +
				`"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],` +
				`"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		},
		{
			name:     "openai responses",
			path:     "/v1/responses",
			body:     `{"model":"gpt-5.5","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`,
			provider: "openai",
			adapter:  openai.New,
			responseBody: `{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.5",` +
				`"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],` +
				`"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`,
		},
		{
			name:     "anthropic messages",
			path:     "/v1/messages",
			body:     `{"model":"claude-sonnet-4-6","max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`,
			provider: "anthropic",
			adapter:  anthropic.New,
			responseBody: `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6",` +
				`"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn",` +
				`"usage":{"input_tokens":10,"output_tokens":5}}`,
		},
		{
			name:     "gemini generate content",
			path:     "/v1beta/models/gemini-2.5-flash:generateContent",
			body:     `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			provider: "gemini",
			adapter:  gemini.New,
			responseBody: `{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}],` +
				`"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},` +
				`"modelVersion":"gemini-2.5-flash"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var upstreamPath string
			var upstreamBody string
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				upstreamPath = r.URL.Path
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("read upstream request body: %v", err)
				}
				upstreamBody = string(body)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"content-type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(tc.responseBody)),
				}, nil
			})}
			sink := &captureSink{}
			srv := New(Config{
				Adapters:   []providers.Adapter{tc.adapter("http://upstream.test")},
				Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
				Creds:      stubCreds{key: "sk-byok"},
				Sink:       sink,
				HTTPClient: client,
			})

			req := httptest.NewRequest(http.MethodPost, "/w/"+agentSlug+tc.path, strings.NewReader(tc.body))
			req.Header.Set("x-cave-agent", "conflicting-header")
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
			}
			if upstreamPath != tc.path {
				t.Errorf("upstream path = %q, want stripped path %q", upstreamPath, tc.path)
			}
			if upstreamBody != tc.body {
				t.Errorf("upstream body = %q, want original body %q", upstreamBody, tc.body)
			}
			row := sink.last(t)
			if row.AgentSlug != agentSlug {
				t.Errorf("agent_slug = %q, want path slug %q", row.AgentSlug, agentSlug)
			}
			if row.Provider != tc.provider {
				t.Errorf("provider = %q, want %q", row.Provider, tc.provider)
			}
			if row.Endpoint != tc.path {
				t.Errorf("endpoint = %q, want stripped endpoint %q", row.Endpoint, tc.path)
			}
		})
	}
}
