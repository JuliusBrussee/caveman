package standalone

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/config"
)

func TestStandaloneVertexExpressCredentialsEndToEnd(t *testing.T) {
	// Captured from @google/genai 1.52.0 with vertexai=true, apiKey, and
	// httpOptions={baseUrl:"<local proxy>/vertex",apiVersion:"v1"}. No ADC or
	// Google account is used by this local HTTP test.
	const path = "/vertex/v1/publishers/google/models/gemini-2.5-flash:generateContent"
	const body = `{"contents":[{"parts":[{"text":"local proof"}],"role":"user"}]}`
	const response = `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}`
	const key = "AIza-fake-express-account-key"
	for _, mode := range []string{"record", "compress", "pixel", "optimize"} {
		for _, tc := range []struct {
			name, query, header, auth, wantQuery, wantKey, wantAuth string
			reject                                                  bool
		}{
			{name: "actual SDK header", header: key, wantKey: key},
			{name: "key query", query: "key=" + key, wantKey: key},
			{name: "encoded dollar key", query: "%24key=AIza%2Dfake%2Dexpress%2Daccount%2Dkey", wantKey: key},
			{name: "preserve other query bytes", query: "alt=sse&key=" + key + "&trace=a%2fb&trace=b+c", wantKey: key, wantQuery: "alt=sse&trace=a%2fb&trace=b+c"},
			{name: "matching repeated identity", header: key, query: "key=" + key + "&$key=" + key, wantKey: key},
			{name: "placeholder displaced by key", auth: "Bearer no-key-required", query: "key=" + key, wantKey: key},
			{name: "OAuth preserved", auth: "Bearer ya29.caller-token", wantAuth: "Bearer ya29.caller-token"},
			{name: "no Google environment fallback"},
			{name: "header query conflict", header: key, query: "key=other-account-key", reject: true},
			{name: "query conflict", query: "key=" + key + "&$key=other-account-key", reject: true},
			{name: "native key and OAuth conflict", header: key, auth: "Bearer ya29.other-account-token", reject: true},
			{name: "query key and OAuth conflict", query: "key=" + key, auth: "Bearer ya29.other-account-token", reject: true},
			{name: "malformed query key", query: "key=bad%ZZ", reject: true},
			{name: "query header injection", query: "key=bad%0D%0AInjected%3Avalue", reject: true},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				t.Setenv("GEMINI_API_KEY", "different-gemini-account")
				t.Setenv("GOOGLE_API_KEY", "different-google-account")
				type captured struct {
					path, body string
					headers    http.Header
				}
				hits := make(chan captured, 1)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					b, _ := io.ReadAll(r.Body)
					hits <- captured{r.URL.RequestURI(), string(b), r.Header.Clone()}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, response)
				}))
				defer upstream.Close()
				srv := New(config.Config{Mode: mode, Providers: map[string]config.ProviderConfig{
					"vertex": {BaseURL: upstream.URL},
				}}, nil, Options{HTTPClient: upstream.Client()})
				target := path
				if tc.query != "" {
					target += "?" + tc.query
				}
				req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("x-goog-api-key", tc.header)
				req.Header.Set("Authorization", tc.auth)
				req.Header.Set("x-goog-user-project", "caller-quota-project")
				req.Header.Set("x-vertex-ai-llm-request-type", "shared")
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, req)
				if tc.reject {
					if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "cave_provider_credentials_conflict") {
						t.Fatalf("conflict status=%d response=%s", rec.Code, rec.Body.String())
					}
					select {
					case <-hits:
						t.Fatal("conflicting credentials reached upstream")
					default:
					}
					for _, secret := range []string{key, "other-account", "different-google", "different-gemini"} {
						if strings.Contains(rec.Body.String(), secret) {
							t.Fatal("error response disclosed credential material")
						}
					}
					return
				}
				if rec.Code != http.StatusOK || rec.Body.String() != response {
					t.Fatalf("response changed: status=%d body=%s", rec.Code, rec.Body.String())
				}
				got := <-hits
				wantPath := strings.TrimPrefix(path, "/vertex")
				if tc.wantQuery != "" {
					wantPath += "?" + tc.wantQuery
				}
				if got.path != wantPath || got.body != body || got.headers.Get("x-goog-api-key") != tc.wantKey || got.headers.Get("Authorization") != tc.wantAuth {
					t.Fatalf("caller route/body/identity changed: %+v", got)
				}
				if got.headers.Get("x-goog-user-project") != "caller-quota-project" || got.headers.Get("x-vertex-ai-llm-request-type") != "shared" {
					t.Fatal("caller quota project or traffic type changed")
				}
			})
		}
	}
}
