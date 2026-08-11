package gateway

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/azureopenai"
	"github.com/JuliusBrussee/caveman/proxy/providers/gemini"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
	"github.com/JuliusBrussee/caveman/proxy/providers/openaicompat"
)

type passthroughTestCreds struct{}

func (passthroughTestCreds) Resolve(provider string, r *http.Request) providers.Credential {
	fallbackEnv := map[string]string{
		"anthropic":         "ANTHROPIC_API_KEY",
		"gemini":            "GEMINI_API_KEY",
		"azure_openai":      "AZURE_OPENAI_API_KEY",
		"openai":            "OPENAI_API_KEY",
		"openai_compatible": "OPENAI_COMPAT_API_KEY",
	}[provider]
	if provider == "openai_compatible" {
		switch {
		case strings.HasPrefix(r.URL.Path, "/compat/openrouter/"):
			fallbackEnv = "OPENROUTER_API_KEY"
		case strings.HasPrefix(r.URL.Path, "/compat/ollama/"):
			fallbackEnv = ""
		}
	}
	if key := strings.TrimSpace(r.Header.Get("x-api-key")); key != "" {
		return providers.Credential{Mode: "ephemeral_header", Key: key, AuthFallbackEnv: fallbackEnv}
	}
	if raw := strings.TrimSpace(r.Header.Get("authorization")); raw != "" {
		return providers.Credential{Mode: "ephemeral_header", Key: testBearerKey(raw), Scheme: "bearer", AuthFallbackEnv: fallbackEnv}
	}
	return providers.Credential{Mode: "ephemeral_header", AuthFallbackEnv: fallbackEnv}
}

func testBearerKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) > len("Bearer ") && strings.EqualFold(raw[:len("Bearer")], "Bearer") && raw[len("Bearer")] == ' ' {
		return strings.TrimSpace(raw[len("Bearer "):])
	}
	return strings.TrimSpace(strings.TrimPrefix(raw, "Bearer "))
}

func TestUpstreamAuthFallbackHeaders(t *testing.T) {
	const body = `{"model":"gpt-5.5","input":"auth fallback body"}`

	cases := []struct {
		name         string
		newAdapter   func(string) providers.Adapter
		path         string
		headers      map[string]string
		env          map[string]string
		response     string
		wantAuth     string
		wantXAPIKey  string
		wantXGoogKey string
		wantAzureKey string
		wantLogCount int
	}{
		{
			name:         "openai placeholder bearer uses env authorization",
			newAdapter:   openai.New,
			path:         "/v1/responses",
			headers:      map[string]string{"authorization": "Bearer no-key-required"},
			env:          map[string]string{"OPENAI_API_KEY": "sk-env-openai"},
			response:     `{"id":"resp","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}`,
			wantAuth:     "Bearer sk-env-openai",
			wantLogCount: 1,
		},
		{
			name:       "openai placeholder bearer stays untouched without env",
			newAdapter: openai.New,
			path:       "/v1/responses",
			headers:    map[string]string{"authorization": "Bearer no-key-required"},
			response:   `{"id":"resp","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}`,
			wantAuth:   "Bearer no-key-required",
		},
		{
			name:       "openai real authorization is never replaced",
			newAdapter: openai.New,
			path:       "/v1/responses",
			headers:    map[string]string{"authorization": "Bearer sk-real-agent"},
			env:        map[string]string{"OPENAI_API_KEY": "sk-env-openai"},
			response:   `{"id":"resp","model":"gpt-5.5","usage":{"input_tokens":1,"output_tokens":1}}`,
			wantAuth:   "Bearer sk-real-agent",
		},
		{
			name:       "anthropic x-api-key already present is untouched",
			newAdapter: anthropic.New,
			path:       "/v1/messages",
			headers: map[string]string{
				"authorization": "Bearer no-key-required",
				"x-api-key":     "sk-agent-anthropic",
			},
			env:         map[string]string{"ANTHROPIC_API_KEY": "sk-env-anthropic"},
			response:    `{"id":"msg","type":"message","model":"claude-sonnet-4-6","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`,
			wantXAPIKey: "sk-agent-anthropic",
		},
		{
			name:         "gemini placeholder bearer uses gemini env x-goog-api-key",
			newAdapter:   gemini.New,
			path:         "/gemini/v1beta/models/gemini-pro:generateContent",
			headers:      map[string]string{"authorization": "Bearer no-key-required"},
			env:          map[string]string{"GEMINI_API_KEY": "sk-env-gemini"},
			response:     `{"candidates":[],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`,
			wantXGoogKey: "sk-env-gemini",
			wantLogCount: 1,
		},
		{
			name:         "gemini falls back to google env",
			newAdapter:   gemini.New,
			path:         "/gemini/v1beta/models/gemini-pro:generateContent",
			headers:      map[string]string{"authorization": "Bearer no-key-required"},
			env:          map[string]string{"GOOGLE_API_KEY": "sk-env-google"},
			response:     `{"candidates":[],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`,
			wantXGoogKey: "sk-env-google",
			wantLogCount: 1,
		},
		{
			name: "named compat key stays on its configured env",
			newAdapter: func(base string) providers.Adapter {
				adapter, err := openaicompat.NewNamed("openrouter", base)
				if err != nil {
					panic(err)
				}
				return adapter
			},
			path:         "/compat/openrouter/v1/chat/completions",
			env:          map[string]string{"OPENROUTER_API_KEY": "sk-openrouter", "OPENAI_API_KEY": "sk-global"},
			response:     `{"id":"compat","model":"gpt-5.5","usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			wantAuth:     "Bearer sk-openrouter",
			wantLogCount: 1,
		},
		{
			name: "named compat explicit no-auth has no outbound auth",
			newAdapter: func(base string) providers.Adapter {
				adapter, err := openaicompat.NewNamed("ollama", base)
				if err != nil {
					panic(err)
				}
				return adapter
			},
			path:     "/compat/ollama/v1/chat/completions",
			env:      map[string]string{"OPENAI_API_KEY": "sk-global"},
			response: `{"id":"compat","model":"gpt-5.5","usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		},
		{
			name:         "azure API key uses api-key and ignores OpenAI key",
			newAdapter:   azureopenai.New,
			path:         "/azure/openai/deployments/gpt-prod/chat/completions?api-version=2024-10-21",
			env:          map[string]string{"AZURE_OPENAI_API_KEY": "azure-key", "OPENAI_API_KEY": "sk-global"},
			response:     `{"id":"azure","model":"gpt-5.5","usage":{"prompt_tokens":1,"completion_tokens":1}}`,
			wantAzureKey: "azure-key",
			wantLogCount: 1,
		},
		{
			name:       "azure absent key does not use OpenAI key",
			newAdapter: azureopenai.New,
			path:       "/azure/openai/deployments/gpt-prod/chat/completions?api-version=2024-10-21",
			env:        map[string]string{"OPENAI_API_KEY": "sk-global"},
			response:   `{"id":"azure","model":"gpt-5.5","usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearAuthFallbackEnv(t)
			for name, value := range tc.env {
				t.Setenv(name, value)
			}

			var gotBody []byte
			var gotAuth, gotXAPIKey, gotXGoogKey, gotAzureKey string
			upstream := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				gotAuth = r.Header.Get("authorization")
				gotXAPIKey = r.Header.Get("x-api-key")
				gotXGoogKey = r.Header.Get("x-goog-api-key")
				gotAzureKey = r.Header.Get("api-key")
				gotBody, _ = io.ReadAll(r.Body)
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     http.StatusText(http.StatusOK),
					Header:     http.Header{"content-type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(tc.response)),
					Request:    r,
				}, nil
			})

			var logBytes bytes.Buffer
			sink := &captureSink{}
			srv := New(Config{
				Adapters:   []providers.Adapter{tc.newAdapter("https://upstream.test")},
				Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
				Creds:      passthroughTestCreds{},
				Sink:       sink,
				HTTPClient: &http.Client{Transport: upstream},
				Logger:     slog.New(slog.NewTextHandler(&logBytes, &slog.HandlerOptions{Level: slog.LevelInfo})),
			})

			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(body))
			for name, value := range tc.headers {
				req.Header.Set(name, value)
			}
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
			}
			if gotAuth != tc.wantAuth {
				t.Errorf("upstream Authorization = %q, want %q", gotAuth, tc.wantAuth)
			}
			if gotXAPIKey != tc.wantXAPIKey {
				t.Errorf("upstream x-api-key = %q, want %q", gotXAPIKey, tc.wantXAPIKey)
			}
			if gotXGoogKey != tc.wantXGoogKey {
				t.Errorf("upstream x-goog-api-key = %q, want %q", gotXGoogKey, tc.wantXGoogKey)
			}
			if gotAzureKey != tc.wantAzureKey {
				t.Errorf("upstream api-key = %q, want %q", gotAzureKey, tc.wantAzureKey)
			}
			if !bytes.Equal(gotBody, []byte(body)) {
				t.Fatalf("record mode body changed:\n got %s\nwant %s", string(gotBody), body)
			}
			row := sink.last(t)
			if row.RawRequestSHA256 != row.TransformedRequestSHA256 {
				t.Fatalf("record mode hashes differ: raw=%s transformed=%s", row.RawRequestSHA256, row.TransformedRequestSHA256)
			}
			logText := logBytes.String()
			if count := strings.Count(logText, "placeholder auth replaced from env"); count != tc.wantLogCount {
				t.Fatalf("fallback log count = %d, want %d (logs %q)", count, tc.wantLogCount, logText)
			}
			for _, key := range tc.env {
				if key != "" && strings.Contains(logText, key) {
					t.Fatalf("fallback log leaked key value: %q", logText)
				}
			}
		})
	}
}

func TestApplyUpstreamAuthFallbackHeaderOnly(t *testing.T) {
	cases := []struct {
		name         string
		provider     string
		headers      http.Header
		credential   providers.Credential
		env          map[string]string
		wantAuth     string
		wantXAPIKey  string
		wantXGoogKey string
		wantAzureKey string
		wantLogCount int
	}{
		{
			name:         "openai placeholder bearer replaced",
			provider:     "openai",
			headers:      http.Header{"Authorization": []string{"Bearer no-key-required"}},
			credential:   providers.Credential{AuthFallbackEnv: "OPENAI_API_KEY"},
			env:          map[string]string{"OPENAI_API_KEY": "sk-env-openai"},
			wantAuth:     "Bearer sk-env-openai",
			wantLogCount: 1,
		},
		{
			name:       "openai real bearer preserved",
			provider:   "openai",
			headers:    http.Header{"Authorization": []string{"Bearer sk-real-agent"}},
			credential: providers.Credential{AuthFallbackEnv: "OPENAI_API_KEY"},
			env:        map[string]string{"OPENAI_API_KEY": "sk-env-openai"},
			wantAuth:   "Bearer sk-real-agent",
		},
		{
			name:        "anthropic x-api-key preserved",
			provider:    "anthropic",
			headers:     http.Header{"X-Api-Key": []string{"sk-agent-anthropic"}},
			credential:  providers.Credential{AuthFallbackEnv: "ANTHROPIC_API_KEY"},
			env:         map[string]string{"ANTHROPIC_API_KEY": "sk-env-anthropic"},
			wantXAPIKey: "sk-agent-anthropic",
		},
		{
			name:         "gemini placeholder provider key replaced",
			provider:     "gemini",
			headers:      http.Header{"X-Goog-Api-Key": []string{"no-key-required"}},
			credential:   providers.Credential{AuthFallbackEnv: "GEMINI_API_KEY"},
			env:          map[string]string{"GEMINI_API_KEY": "sk-env-gemini"},
			wantXGoogKey: "sk-env-gemini",
			wantLogCount: 1,
		},
		{
			name:         "gemini uses google fallback",
			provider:     "gemini",
			headers:      http.Header{"Authorization": []string{"Bearer no-key-required"}},
			credential:   providers.Credential{AuthFallbackEnv: "GOOGLE_API_KEY"},
			env:          map[string]string{"GOOGLE_API_KEY": "sk-env-google"},
			wantXGoogKey: "sk-env-google",
			wantLogCount: 1,
		},
		{
			name:         "named compat uses only its configured key",
			provider:     "openai_compatible",
			headers:      http.Header{"Authorization": []string{"Bearer no-key-required"}},
			credential:   providers.Credential{AuthFallbackEnv: "OPENROUTER_API_KEY"},
			env:          map[string]string{"OPENROUTER_API_KEY": "sk-openrouter", "OPENAI_API_KEY": "sk-global"},
			wantAuth:     "Bearer sk-openrouter",
			wantLogCount: 1,
		},
		{
			name:       "named compat explicit no-auth ignores global key",
			provider:   "openai_compatible",
			credential: providers.Credential{},
			env:        map[string]string{"OPENAI_API_KEY": "sk-global"},
		},
		{
			name:         "azure API key uses api-key header only",
			provider:     "azure_openai",
			headers:      http.Header{"Authorization": []string{"Bearer no-key-required"}, "api-key": []string{"no-key-required"}},
			credential:   providers.Credential{AuthFallbackEnv: "AZURE_OPENAI_API_KEY"},
			env:          map[string]string{"AZURE_OPENAI_API_KEY": "azure-key", "OPENAI_API_KEY": "sk-global"},
			wantAzureKey: "azure-key",
			wantLogCount: 1,
		},
		{
			name:       "azure ignores unrelated OpenAI key",
			provider:   "azure_openai",
			credential: providers.Credential{AuthFallbackEnv: "AZURE_OPENAI_API_KEY"},
			env:        map[string]string{"OPENAI_API_KEY": "sk-global"},
		},
		{
			name:       "bedrock skipped",
			provider:   "bedrock",
			headers:    http.Header{"Authorization": []string{"Bearer no-key-required"}},
			credential: providers.Credential{AuthFallbackEnv: "OPENAI_API_KEY"},
			env:        map[string]string{"ANTHROPIC_API_KEY": "sk-env-anthropic", "OPENAI_API_KEY": "sk-env-openai"},
			wantAuth:   "Bearer no-key-required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearAuthFallbackEnv(t)
			for name, value := range tc.env {
				t.Setenv(name, value)
			}

			var logBytes bytes.Buffer
			srv := &Server{logger: slog.New(slog.NewTextHandler(&logBytes, &slog.HandlerOptions{Level: slog.LevelInfo}))}
			headers := tc.headers.Clone()
			srv.applyUpstreamAuthFallback(tc.provider, tc.credential, headers)

			if got := headers.Get("authorization"); got != tc.wantAuth {
				t.Errorf("Authorization = %q, want %q", got, tc.wantAuth)
			}
			if got := headers.Get("x-api-key"); got != tc.wantXAPIKey {
				t.Errorf("x-api-key = %q, want %q", got, tc.wantXAPIKey)
			}
			if got := headers.Get("x-goog-api-key"); got != tc.wantXGoogKey {
				t.Errorf("x-goog-api-key = %q, want %q", got, tc.wantXGoogKey)
			}
			if got := headers.Get("api-key"); got != tc.wantAzureKey {
				t.Errorf("api-key = %q, want %q", got, tc.wantAzureKey)
			}
			logText := logBytes.String()
			if count := strings.Count(logText, "placeholder auth replaced from env"); count != tc.wantLogCount {
				t.Fatalf("fallback log count = %d, want %d (logs %q)", count, tc.wantLogCount, logText)
			}
			for _, key := range tc.env {
				if key != "" && strings.Contains(logText, key) {
					t.Fatalf("fallback log leaked key value: %q", logText)
				}
			}
		})
	}
}

func clearAuthFallbackEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "AZURE_OPENAI_API_KEY", "OPENAI_COMPAT_API_KEY", "OPENROUTER_API_KEY"} {
		t.Setenv(name, "")
	}
}
