package vertex

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

const stubBase = "https://us-central1-aiplatform.googleapis.com"
const geminiModel = "gemini-2.5-pro"
const claudeModel = "claude-sonnet-4-6"

func newAdapter(t *testing.T) Adapter {
	t.Helper()
	return New(stubBase).(Adapter)
}

// predictPath builds a native Vertex prediction path under the /vertex prefix.
func predictPath(publisher, model, method string) string {
	return "/vertex/v1/projects/demo-proj/locations/global/publishers/" + publisher + "/models/" + model + ":" + method
}

// --- Routing / allowlist -------------------------------------------------

func TestMatchRoute_VertexPredict(t *testing.T) {
	a := newAdapter(t)
	if !a.MatchRoute(http.MethodPost, predictPath("google", geminiModel, "generateContent")) {
		t.Error("vertex gemini route should match")
	}
	if !a.MatchRoute(http.MethodPost, predictPath("anthropic", claudeModel, "rawPredict")) {
		t.Error("vertex anthropic route should match")
	}
	if a.MatchRoute(http.MethodGet, predictPath("google", geminiModel, "generateContent")) {
		t.Error("only POST should match")
	}
	if a.MatchRoute(http.MethodPost, "/openai/v1/responses") {
		t.Error("non-vertex route must not match")
	}
}

func resolve(t *testing.T, a Adapter, path string) (string, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, path, nil)
	if err != nil {
		t.Fatalf("bad url: %v", err)
	}
	u, err := a.ResolveUpstreamURL(context.Background(), req, providers.RouteContext{})
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func TestResolveUpstreamURL_StripsVertexPrefixAndKeepsQuery(t *testing.T) {
	a := newAdapter(t)
	path := predictPath("google", geminiModel, "streamGenerateContent") + "?alt=sse"
	got, err := resolve(t, a, path)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := stubBase + "/v1/projects/demo-proj/locations/global/publishers/google/models/" + geminiModel + ":streamGenerateContent?alt=sse"
	if got != want {
		t.Errorf("upstream url = %q, want %q", got, want)
	}
}

func TestResolveUpstreamURL_ClaudeRawPredict(t *testing.T) {
	a := newAdapter(t)
	got, err := resolve(t, a, predictPath("anthropic", claudeModel, "rawPredict"))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := stubBase + "/v1/projects/demo-proj/locations/global/publishers/anthropic/models/" + claudeModel + ":rawPredict"
	if got != want {
		t.Errorf("upstream url = %q, want %q", got, want)
	}
}

func TestResolveUpstreamURL_RejectsUnknownPublisher(t *testing.T) {
	a := newAdapter(t)
	if _, err := resolve(t, a, predictPath("meta", "llama-3.1-405b", "rawPredict")); err == nil {
		t.Error("unlisted publisher should be rejected")
	}
}

func TestResolveUpstreamURL_RejectsUnknownModel(t *testing.T) {
	a := newAdapter(t)
	// google is allowed, but a non-gemini model is not on the model allowlist.
	if _, err := resolve(t, a, predictPath("google", "text-embedding-004", "predict")); err == nil {
		t.Error("unlisted model should be rejected")
	}
}

func TestResolveUpstreamURL_RejectsUnknownMethod(t *testing.T) {
	a := newAdapter(t)
	if _, err := resolve(t, a, predictPath("google", geminiModel, "predict")); err == nil {
		t.Error("unlisted Vertex method should be rejected")
	}
}

func TestResolveUpstreamURL_RejectsMalformedPath(t *testing.T) {
	a := newAdapter(t)
	// No :method suffix on the model segment.
	if _, err := resolve(t, a, "/vertex/v1/projects/p/locations/global/publishers/google/models/"+geminiModel); err == nil {
		t.Error("path without a method should be rejected")
	}
}

func TestParsePredictPath_VersionedModelWithAtDate(t *testing.T) {
	publisher, model, method := parsePredictPath(predictPath("anthropic", "claude-sonnet-4-5@20250929", "streamRawPredict"))
	if publisher != "anthropic" {
		t.Errorf("publisher = %q, want anthropic", publisher)
	}
	if model != "claude-sonnet-4-5@20250929" {
		t.Errorf("model = %q, want claude-sonnet-4-5@20250929", model)
	}
	if method != "streamRawPredict" {
		t.Errorf("method = %q, want streamRawPredict", method)
	}
}

func TestInspectRequest_ModelFromPathAndStream(t *testing.T) {
	a := newAdapter(t)

	// Gemini generateContent: model from path, not streaming.
	hg := http.Header{}
	hg.Set("x-cave-route-path", predictPath("google", geminiModel, "generateContent"))
	meta, err := a.InspectRequest(context.Background(), strings.NewReader(`{"contents":[]}`), hg)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if meta.Provider != "vertex" {
		t.Errorf("provider = %q, want vertex", meta.Provider)
	}
	if meta.Model != geminiModel {
		t.Errorf("model = %q, want %q", meta.Model, geminiModel)
	}
	if meta.Stream {
		t.Error("generateContent should not set Stream")
	}

	// Claude streamRawPredict: streaming by method suffix.
	hc := http.Header{}
	hc.Set("x-cave-route-path", predictPath("anthropic", claudeModel, "streamRawPredict"))
	meta, err = a.InspectRequest(context.Background(), strings.NewReader(`{"anthropic_version":"vertex-2023-10-16","messages":[]}`), hc)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if meta.Model != claudeModel {
		t.Errorf("model = %q, want %q", meta.Model, claudeModel)
	}
	if !meta.Stream {
		t.Error("streamRawPredict path should set Stream=true")
	}
}

// --- Header mapping: bearer pass-through + NO client-key leak -------------

func TestSanitizeAndMapHeaders_SetsBearerAndDropsInboundAuth(t *testing.T) {
	a := newAdapter(t)
	req, _ := http.NewRequest(http.MethodPost, predictPath("google", geminiModel, "generateContent"), nil)
	req.Header.Set("content-type", "application/json")
	// Inbound client headers that MUST NOT leak upstream.
	req.Header.Set("authorization", "Bearer CLIENT-CAVE-KEY")
	req.Header.Set("x-cave-api-key", "cave_live_clientclient_secret")

	const token = "ya29.vertex-access-token"
	out, err := a.SanitizeAndMapHeaders(context.Background(), req, providers.Credential{Key: token}, nil)
	if err != nil {
		t.Fatalf("sanitize: %v", err)
	}

	if out.Get("Authorization") != "Bearer "+token {
		t.Errorf("Authorization = %q, want Bearer %s", out.Get("Authorization"), token)
	}
	// SECURITY: the client's caveman key must never reach upstream.
	for name, vals := range out {
		for _, v := range vals {
			if strings.Contains(v, "CLIENT-CAVE-KEY") || strings.Contains(v, "cave_live_clientclient_secret") {
				t.Errorf("client key leaked into header %s = %q", name, v)
			}
		}
	}
	if out.Get("x-cave-api-key") != "" {
		t.Error("x-cave-api-key forwarded upstream")
	}
}

// --- Host validation (defense-in-depth) ----------------------------------

func TestIsVertexHost(t *testing.T) {
	cases := map[string]bool{
		"aiplatform.googleapis.com":               true,
		"us-central1-aiplatform.googleapis.com":   true,
		"global-aiplatform.googleapis.com":        true,
		"aiplatform.us.rep.googleapis.com":        true,
		"aiplatform.eu.rep.googleapis.com":        true,
		"evil.example.com":                        false,
		"aiplatform.googleapis.com.evil.com":      false,
		"bedrock-runtime.us-east-1.amazonaws.com": false,
	}
	for host, want := range cases {
		if got := isVertexHost(host); got != want {
			t.Errorf("isVertexHost(%q) = %v, want %v", host, got, want)
		}
	}
}
