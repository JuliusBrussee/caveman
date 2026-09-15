package openaicompat

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

// Exercise the configured adapter through a real HTTP client. Connection
// nominations are request-specific, so a configuration-time name allowlist
// alone cannot make a header safe to forward to the next hop.
func TestNamedForwardHeadersRespectConnectionNominationsOnWire(t *testing.T) {
	for _, test := range []struct {
		name       string
		mount      string
		header     string
		connection []string
		wantTenant bool
	}{
		{name: "end_to_end", wantTenant: true},
		{name: "one_nominated_header", connection: []string{"X-API-Tenant"}},
		{name: "mixed_case_comma_list", connection: []string{"keep-alive, x-aPi-TeNaNt"}},
		{name: "multiple_connection_fields", connection: []string{"keep-alive", " X-API-Tenant "}},
		{name: "ordinary_native_application_header", mount: "native", header: "OpenAI-Organization", connection: []string{"OpenAI-Organization"}},
		{name: "builtin_opencode_header", mount: "opencode-go", header: "X-OpenCode-Session", connection: []string{"X-OpenCode-Session"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			seen := make(chan http.Header, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen <- r.Header.Clone()
				w.WriteHeader(http.StatusNoContent)
			}))
			defer upstream.Close()
			mount, header := test.mount, test.header
			if mount == "" {
				mount = "tenant"
			}
			if header == "" {
				header = "X-API-Tenant"
			}
			adapter, err := NewNamed(mount, upstream.URL, "X-API-Tenant", "CF-AIG-Authorization")
			if err != nil {
				t.Fatal(err)
			}
			requestPath := "/compat/" + mount + "/v1/chat/completions"
			if mount == "native" {
				adapter = openai.New(upstream.URL)
				requestPath = "/v1/chat/completions"
			}
			inbound := httptest.NewRequest(http.MethodPost, requestPath, nil)
			inbound.Header.Set(header, "hop-private-tenant")
			inbound.Header.Set("CF-AIG-Authorization", "Bearer selected-gateway-key")
			inbound.Header.Set("X-Unrelated", "never-forward")
			inbound.Header["Connection"] = test.connection
			target, err := adapter.ResolveUpstreamURL(context.Background(), inbound, providers.RouteContext{})
			if err != nil {
				t.Fatal(err)
			}
			headers, err := adapter.SanitizeAndMapHeaders(context.Background(), inbound,
				providers.Credential{Mode: "ephemeral_header", Key: "selected-provider-key", Scheme: "bearer"}, target)
			if err != nil {
				t.Fatal(err)
			}
			outbound, err := http.NewRequest(http.MethodPost, target.String(), strings.NewReader(`{"model":"fixture"}`))
			if err != nil {
				t.Fatal(err)
			}
			outbound.Header = headers
			response, err := upstream.Client().Do(outbound)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			got := <-seen
			if test.wantTenant {
				if got.Get(header) != "hop-private-tenant" {
					t.Fatal("configured end-to-end header was lost")
				}
			} else if got.Get(header) != "" {
				t.Fatal("Connection-nominated private header reached the upstream")
			}
			wantGateway := "Bearer selected-gateway-key"
			if mount == "native" {
				wantGateway = ""
			}
			if got.Get("CF-AIG-Authorization") != wantGateway || got.Get("Authorization") != "Bearer selected-provider-key" {
				t.Fatal("unrelated selected credentials changed")
			}
			if got.Get("Connection") != "" || got.Get("X-Unrelated") != "" {
				t.Fatal("unconfigured or hop-by-hop header reached the upstream")
			}
		})
	}
}
