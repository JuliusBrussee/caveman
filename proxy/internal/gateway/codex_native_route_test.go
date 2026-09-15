package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

// TestCodexNativeRouteNeedsV1 pins the proxy half of the contract the CLI writes
// into ~/.codex/config.toml as `base_url`. Codex's OpenAI-Responses client
// appends "/responses" onto base_url itself, so what arrives here is
// `<base_url path> + "/responses"`. The openai adapter's Routes are a closed,
// exact allowlist holding "/v1/responses" and never "/responses", so a base_url
// written without the "/v1" 404s with cave_route_not_found before a single
// request reaches OpenAI (#1045).
//
// This is the drift guard: the two spellings live in different languages and
// different repositories' worth of code, and nothing else fails when they stop
// agreeing — the user just gets a 404 the first time they run `codex exec`.
func TestCodexNativeRouteNeedsV1(t *testing.T) {
	adapter := openai.New("https://api.openai.com")

	for _, tc := range []struct {
		name      string
		basePath  string
		wantMatch bool
	}{
		// What `caveman enable codex` wrote before #1045.
		{name: "without /v1", basePath: "/w/codex", wantMatch: false},
		// What it writes now.
		{name: "with /v1", basePath: "/w/codex/v1", wantMatch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.basePath+"/responses", nil)
			if !normalizeAgentPath(req) {
				t.Fatalf("normalizeAgentPath rejected %q", req.URL.Path)
			}
			if got := req.Header.Get("x-cave-agent"); got != "codex" {
				t.Fatalf("agent attribution = %q, want codex", got)
			}
			if got := adapter.MatchRoute(http.MethodPost, req.URL.Path); got != tc.wantMatch {
				t.Fatalf("MatchRoute(%q) = %v, want %v", req.URL.Path, got, tc.wantMatch)
			}
		})
	}
}
