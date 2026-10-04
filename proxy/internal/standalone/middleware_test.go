package standalone

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/config"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

// A non-loopback listener that only a middleware identity source makes legal
// has no shared token: the provider routes refuse everything rather than
// accept everything, while loopback without a token keeps its behavior.
func TestProviderRoutesFailClosedOffLoopbackWithoutSharedToken(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-env")
	for listen, want := range map[string]int{"0.0.0.0:8787": http.StatusUnauthorized, "10.0.0.5:8787": http.StatusUnauthorized, "127.0.0.1:8787": http.StatusOK, "": http.StatusOK} {
		upstream := &captureUpstreamTransport{response: anthropicStubResponse}
		cfg := config.Config{Mode: "record", Listen: listen, Middleware: config.MiddlewareConfig{TokenMapFile: "/tokens.yaml"},
			Providers: map[string]config.ProviderConfig{"anthropic": {BaseURL: "https://upstream.test"}}}
		handler := New(cfg, nil, Options{HTTPClient: &http.Client{Transport: upstream}}).Handler()
		if rec := postMessages(t, handler, map[string]string{"x-cave-api-key": "team-a-token-0123456789"}); rec.Code != want {
			t.Errorf("listen %q: provider route %d, want %d", listen, rec.Code, want)
		}
		if want != http.StatusOK && upstream.url != "" {
			t.Errorf("listen %q: a refused request reached the provider", listen)
		}
	}
}

func TestMetricsTokenFromConfigGatesMetrics(t *testing.T) {
	handler := New(config.Config{Mode: "record", MetricsToken: "metrics-token-0123456789"}, nil, Options{}).Handler()
	if rec := get(t, handler, "/metrics"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("/metrics without the token: %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer metrics-token-0123456789")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics with the token: %d", rec.Code)
	}
}

// A11: the middleware compresses while the proxy records, and a token map
// makes the runtime a multi-principal resolver.
func TestNewMiddlewareModeAndTrustFollowConfig(t *testing.T) {
	dir := t.TempDir()
	state, err := store.Open(filepath.Join(dir, "state.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	sum := sha256.Sum256([]byte("team-a-token-0123456789"))
	tokens := filepath.Join(dir, "tokens.yaml")
	if err := os.WriteFile(tokens, []byte("principals:\n  - {name: team-a, namespaces: [\"*\"], token_sha256: ["+hex.EncodeToString(sum[:])+"]}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                string
		cfg                 config.Config
		token, mode, trust  string
		unauthenticatedCode int
	}{
		{"follows proxy mode", config.Config{Mode: "record", AuthToken: authToken}, authToken, "record", "single_operator", 401},
		{"own mode", config.Config{Mode: "record", AuthToken: authToken, Middleware: config.MiddlewareConfig{Mode: "compress"}}, authToken, "compress", "single_operator", 401},
		{"token map", config.Config{Mode: "pixel", Middleware: config.MiddlewareConfig{TokenMapFile: tokens}}, "team-a-token-0123456789", "compress", "resolver", 401},
		{"open loopback", config.Config{Mode: "record"}, "", "record", "single_operator", 200},
	} {
		ids, err := NewIdentity(tc.cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		runtime, err := NewMiddleware(tc.cfg, state, nil, "test", nil, ids)
		if err != nil {
			t.Fatal(err)
		}
		capabilities := func(token string) (int, map[string]any) {
			req := httptest.NewRequest(http.MethodGet, "/caveman/v1/middleware/capabilities", nil)
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			rec := httptest.NewRecorder()
			runtime.ServeHTTP(rec, req)
			var doc map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &doc)
			return rec.Code, doc
		}
		code, doc := capabilities(tc.token)
		if code != 200 || doc["mode"] != tc.mode || doc["trust_mode"] != tc.trust {
			t.Errorf("%s: %d mode=%v trust=%v", tc.name, code, doc["mode"], doc["trust_mode"])
		}
		if code, _ := capabilities(""); code != tc.unauthenticatedCode {
			t.Errorf("%s: no credential got %d", tc.name, code)
		}
	}
}
