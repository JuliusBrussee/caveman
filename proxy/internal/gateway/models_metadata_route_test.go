package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
	"github.com/JuliusBrussee/caveman/proxy/providers/openaicompat"
)

// modelsUpstream stubs a provider's read-only model catalog and records what
// path/method/headers the proxy forwarded.
func modelsUpstream(t *testing.T, body string) (*httptest.Server, *http.Request, *string) {
	t.Helper()
	var gotReq http.Request
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = *r.Clone(r.Context())
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &gotReq, &gotBody
}

// rejectingAuth fails every request, so the metadata mount can be checked for
// the auth-before-route ordering the rest of the gateway relies on.
type rejectingAuth struct{}

func (rejectingAuth) Authenticate(context.Context, *http.Request) (RequestContext, error) {
	return RequestContext{}, errors.New("unauthorized")
}

const modelsCatalog = `{"object":"list","data":[{"id":"Main","object":"model","context_length":1000000}]}`

// TestModelsMetadataRouteForwardsUnchanged is issue #1187: every
// OpenAI-compatible client reads model metadata from GET /v1/models, and Hermes
// sizes its context window from the context_length it finds there. The gateway
// route allowlist is POST-only and lists no models route, so the request 404s
// with cave_route_not_found and Hermes silently falls back to its 256k default.
//
// The catalog read must reach the upstream unchanged on the bare mount, the
// provider-prefixed mount and the /w/<agent> attribution mount, and the
// upstream's own JSON (context_length included) must come back byte-for-byte.
func TestModelsMetadataRouteForwardsUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name         string
		path         string
		wantUpstream string
	}{
		{"bare", "/v1/models", "/v1/models"},
		{"bare single model", "/v1/models/Main", "/v1/models/Main"},
		{"provider prefixed", "/openai/v1/models", "/v1/models"},
		{"agent mount", "/w/hermes/v1/models", "/v1/models"},
		{"agent mount single model", "/w/hermes/v1/models/Main", "/v1/models/Main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream, gotReq, gotBody := modelsUpstream(t, modelsCatalog)

			sink := &captureSink{}
			srv := newStandaloneTestServer(t, upstream.URL, RequestContext{Label: "local", RuntimeMode: "compress"}, sink)

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("authorization", "Bearer sk-from-agent")
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
			}
			if rec.Body.String() != modelsCatalog {
				t.Errorf("response body = %s, want the upstream catalog byte-for-byte %s", rec.Body.String(), modelsCatalog)
			}
			if gotReq.URL.Path != tc.wantUpstream {
				t.Errorf("upstream path = %q, want %q", gotReq.URL.Path, tc.wantUpstream)
			}
			if gotReq.Method != http.MethodGet {
				t.Errorf("upstream method = %q, want GET", gotReq.Method)
			}
			if *gotBody != "" {
				t.Errorf("upstream body = %q, want empty for a metadata read", *gotBody)
			}
			if got := gotReq.Header.Get("authorization"); got != "Bearer sk-byok" {
				t.Errorf("upstream authorization = %q, want the resolved credential", got)
			}
			// A catalog read runs no inference: it must not invent a spend row.
			if len(sink.rows) != 0 {
				t.Errorf("metadata read recorded %d spend rows, want 0", len(sink.rows))
			}
		})
	}
}

// TestAnthropicModelsMetadataRoute covers the sibling adapter: the Anthropic
// wire protocol defines GET /v1/models too, and it shared the same gap.
func TestAnthropicModelsMetadataRoute(t *testing.T) {
	upstream, gotReq, _ := modelsUpstream(t, modelsCatalog)
	srv := New(Config{
		Adapters:   []providers.Adapter{anthropic.New(upstream.URL)},
		Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}},
		Creds:      stubCreds{key: "sk-byok"},
		Sink:       &captureSink{},
		HTTPClient: &http.Client{},
	})

	for _, path := range []string{"/v1/models", "/anthropic/v1/models", "/w/hermes/v1/models"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 (body %s)", path, rec.Code, rec.Body.String())
		}
		if gotReq.URL.Path != "/v1/models" {
			t.Errorf("%s: upstream path = %q, want /v1/models", path, gotReq.URL.Path)
		}
	}
}

// TestMetadataRouteStaysFailClosed proves the new read-only surface did not
// turn the closed route allowlist into a prefix pass-through.
func TestMetadataRouteStaysFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		// POST is an inference verb; it must not reach a metadata mount.
		{"post to models", http.MethodPost, "/v1/models"},
		{"delete a model", http.MethodDelete, "/v1/models/Main"},
		// One trailing id segment only — never an arbitrary subtree.
		{"deep subtree", http.MethodGet, "/v1/models/Main/versions/3"},
		// A GET must not open up the inference routes themselves.
		{"get an inference route", http.MethodGet, "/v1/chat/completions"},
		{"get an unknown route", http.MethodGet, "/v1/not/a/route"},
		{"models prefix collision", http.MethodGet, "/v1/models-secret"},
		// Gemini's inference routes live under /v1/models/{model}:method. A
		// method call is not a catalog read, so an OpenAI metadata route must
		// not swallow one when both adapters are mounted on the same proxy.
		{"gemini method path", http.MethodGet, "/v1/models/gemini-pro:generateContent"},
		{"gemini count path", http.MethodGet, "/v1/models/gemini-pro:countTokens"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reached bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				_, _ = io.WriteString(w, `{}`)
			}))
			defer upstream.Close()

			srv := newStandaloneTestServer(t, upstream.URL, RequestContext{Label: "local", RuntimeMode: "compress"}, &captureSink{})
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "cave_route_not_found") {
				t.Errorf("body = %s, want cave_route_not_found", rec.Body.String())
			}
			if reached {
				t.Error("request reached the upstream; the allowlist must fail closed")
			}
		})
	}
}

// TestMetadataRouteAuthenticatesFirst keeps the route oracle shut: an
// unauthenticated metadata read answers 401 like every other mount, so the
// 401/404 split cannot be used to enumerate which providers this proxy serves.
func TestMetadataRouteAuthenticatesFirst(t *testing.T) {
	upstream, _, _ := modelsUpstream(t, modelsCatalog)
	srv := New(Config{
		Adapters:   []providers.Adapter{openai.New(upstream.URL)},
		Auth:       rejectingAuth{},
		Creds:      stubCreds{key: "sk-byok"},
		Sink:       &captureSink{},
		HTTPClient: &http.Client{},
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for an unauthenticated metadata read (body %s)", rec.Code, rec.Body.String())
	}
}

// TestCompatModelsMetadataRoute covers the OpenAI-compatible mounts, which had
// the same gap: Groq, Ollama, vLLM and friends all serve GET /v1/models, and a
// named mount is how caveman reaches them.
func TestCompatModelsMetadataRoute(t *testing.T) {
	upstream, gotReq, _ := modelsUpstream(t, modelsCatalog)

	named, err := openaicompat.NewNamed("groq", upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Config{
		Adapters:   []providers.Adapter{named, openaicompat.New(upstream.URL)},
		Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}},
		Creds:      stubCreds{key: "sk-byok"},
		Sink:       &captureSink{},
		HTTPClient: &http.Client{},
	})

	for _, tc := range []struct{ path, wantUpstream string }{
		{"/compat/groq/v1/models", "/v1/models"},
		{"/compat/groq/v1/models/llama-4", "/v1/models/llama-4"},
		{"/compat/v1/models", "/v1/models"},
		{"/w/hermes/compat/groq/v1/models", "/v1/models"},
	} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 (body %s)", tc.path, rec.Code, rec.Body.String())
		}
		if gotReq.URL.Path != tc.wantUpstream {
			t.Errorf("%s: upstream path = %q, want %q", tc.path, gotReq.URL.Path, tc.wantUpstream)
		}
	}

	// The compat path validation still runs first: an encoded separator must not
	// let one mount claim another mount's credentials through the new surface.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/compat/groq%2Fv1/models", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("encoded separator status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestModelsMetadataRoutePreservesQueryAndDoublePrefix covers the two cases
// #1188 asserted and TestModelsMetadataRouteForwardsUnchanged does not.
//
// Pagination query: OpenAI's catalog read takes query parameters, and clients
// that page the list send them. The metadata passthrough builds its upstream
// request from ResolveUpstreamURL rather than cloning the inbound one, so query
// preservation is a property of that seam, not something the mount gets for
// free — nothing else here pins it.
//
// Double prefix: /w/<agent> attribution and the /openai provider prefix compose,
// and the agent-mount cases above only ever pair /w/<agent> with a bare path.
func TestModelsMetadataRoutePreservesQueryAndDoublePrefix(t *testing.T) {
	for _, tc := range []struct {
		name         string
		path         string
		wantUpstream string
		wantQuery    string
	}{
		{"query preserved", "/v1/models?limit=1", "/v1/models", "limit=1"},
		{"query preserved on agent mount", "/w/hermes/v1/models?limit=1", "/v1/models", "limit=1"},
		{"double prefix", "/w/hermes/openai/v1/models", "/v1/models", ""},
		{"double prefix single model", "/w/hermes/openai/v1/models/Main", "/v1/models/Main", ""},
		{"double prefix with query", "/w/hermes/openai/v1/models?limit=1", "/v1/models", "limit=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream, gotReq, gotBody := modelsUpstream(t, modelsCatalog)

			sink := &captureSink{}
			srv := newStandaloneTestServer(t, upstream.URL, RequestContext{Label: "local", RuntimeMode: "compress"}, sink)

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("authorization", "Bearer sk-from-agent")
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
			}
			if rec.Body.String() != modelsCatalog {
				t.Errorf("response body = %s, want the upstream catalog byte-for-byte", rec.Body.String())
			}
			if gotReq.URL.Path != tc.wantUpstream {
				t.Errorf("upstream path = %q, want %q", gotReq.URL.Path, tc.wantUpstream)
			}
			if gotReq.URL.RawQuery != tc.wantQuery {
				t.Errorf("upstream query = %q, want %q", gotReq.URL.RawQuery, tc.wantQuery)
			}
			if *gotBody != "" {
				t.Errorf("upstream body = %q, want empty for a metadata read", *gotBody)
			}
			if len(sink.rows) != 0 {
				t.Errorf("metadata read recorded %d spend rows, want 0", len(sink.rows))
			}
		})
	}
}
