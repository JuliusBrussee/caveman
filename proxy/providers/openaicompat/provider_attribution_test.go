package openaicompat

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
)

func TestCanonicalProviderAttributionIsMountScoped(t *testing.T) {
	groups := map[string][]string{
		"openrouter":  {"HTTP-Referer", "X-OpenRouter-Title", "X-OpenRouter-Categories"},
		"nvidia":      {"X-Billing-Invoke-Origin"},
		"opencode":    {"X-OpenCode-Session", "X-OpenCode-Client"},
		"opencode-go": {"X-OpenCode-Session", "X-OpenCode-Client"},
		"other":       {},
	}
	for mount, wanted := range groups {
		t.Run(mount, func(t *testing.T) {
			seen := make(chan http.Header, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen <- r.Header.Clone()
				w.WriteHeader(http.StatusNoContent)
			}))
			defer upstream.Close()
			adapter, err := NewNamed(mount, upstream.URL)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/compat/"+mount+"/v1/responses", nil)
			all := make(map[string]bool)
			for _, names := range groups {
				for _, name := range names {
					all[name] = true
					request.Header.Set(name, "fixture")
				}
			}
			for _, name := range wanted {
				delete(all, name)
			}
			target, err := adapter.ResolveUpstreamURL(request.Context(), request, providers.RouteContext{})
			if err != nil {
				t.Fatal(err)
			}
			headers, err := adapter.SanitizeAndMapHeaders(request.Context(), request, providers.Credential{}, target)
			if err != nil {
				t.Fatal(err)
			}
			out, err := http.NewRequest(http.MethodPost, target.String(), nil)
			if err != nil {
				t.Fatal(err)
			}
			out.Header = headers
			response, err := upstream.Client().Do(out)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			got := <-seen
			for _, name := range wanted {
				if got.Get(name) != "fixture" {
					t.Errorf("%s lost its %s", mount, name)
				}
			}
			for name := range all {
				if got.Get(name) != "" {
					t.Errorf("%s received another provider's %s", mount, name)
				}
			}
		})
	}
}
