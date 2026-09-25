package vertex

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func TestInboundKeyCannotChangeResolvedVertexPrincipal(t *testing.T) {
	adapter := New("https://aiplatform.googleapis.com")
	for _, credential := range []providers.Credential{
		{Mode: "managed", Key: "selected-oauth"},
		{Mode: "managed", Key: "selected-oauth", Scheme: "bearer"},
		{Mode: "managed", Key: "selected-api-key", Scheme: "api_key"},
		{Mode: "ephemeral_header", Key: "selected-oauth", Scheme: "bearer"},
	} {
		req := httptest.NewRequest(http.MethodPost, "/vertex/v1/publishers/google/models/gemini-2.5-flash:generateContent?key=inbound-key", nil)
		req.Header.Set("Authorization", "Bearer caller-token")
		u, err := adapter.ResolveUpstreamURL(req.Context(), req, providers.RouteContext{})
		if err != nil {
			t.Fatal(err)
		}
		headers, err := adapter.SanitizeAndMapHeaders(req.Context(), req, credential, u)
		if err != nil {
			t.Fatal(err)
		}
		if credential.Scheme == "api_key" {
			if headers.Get("x-goog-api-key") != credential.Key || headers.Get("Authorization") != "" {
				t.Fatalf("selected API-key principal changed: %v", headers)
			}
		} else if headers.Get("Authorization") != "Bearer "+credential.Key || headers.Get("x-goog-api-key") != "" {
			t.Fatalf("selected OAuth principal changed: %v", headers)
		}
		if u.RawQuery != "" {
			t.Fatal("inbound query key augmented the selected principal")
		}
	}
}
