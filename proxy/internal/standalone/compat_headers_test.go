package standalone

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/config"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

func TestNamedCompatExplicitHeadersSurviveOnlyTheirConfiguredMount(t *testing.T) {
	observed := make(chan http.Header, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"fixture","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer upstream.Close()
	for _, mode := range []string{"record", "compress"} {
		t.Run(mode, func(t *testing.T) {
			spend, err := store.Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer spend.Close()
			srv := New(config.Config{Mode: mode, Compat: map[string]config.CompatConfig{
				"tenant":   {BaseURL: upstream.URL, ForwardHeaders: []string{"X-API-Tenant", "CF-AIG-Authorization"}},
				"ordinary": {BaseURL: upstream.URL},
			}}, spend, Options{HTTPClient: &http.Client{}})
			for _, mount := range []string{"tenant", "ordinary"} {
				for _, operation := range []string{"chat/completions", "messages"} {
					req := httptest.NewRequest(http.MethodPost, "/compat/"+mount+"/v1/"+operation, strings.NewReader(`{"model":"fixture","messages":[{"role":"user","content":"hello"}]}`))
					req.Header.Set("Authorization", "Bearer provider-key")
					req.Header.Set("Content-Type", "application/json")
					req.Header["X-Api-Tenant"] = []string{"tenant-a", "tenant-b"}
					req.Header.Set("CF-AIG-Authorization", "Bearer gateway-key")
					req.Header.Set("X-Unrelated-Secret", "never-forward")
					for _, name := range []string{"session_id", "x-session-id", "x-client-request-id", "x-session-affinity"} {
						req.Header.Set(name, "session-fixture")
					}
					rec := httptest.NewRecorder()
					srv.Handler().ServeHTTP(rec, req)
					if rec.Code != 200 {
						t.Fatalf("%s/%s: status %d: %s", mount, operation, rec.Code, rec.Body.String())
					}
					captured := <-observed
					if mount == "tenant" {
						if !reflect.DeepEqual(captured.Values("X-API-Tenant"), []string{"tenant-a", "tenant-b"}) || captured.Get("CF-AIG-Authorization") != "Bearer gateway-key" {
							t.Fatalf("configured headers lost: %v", captured)
						}
					} else if captured.Get("X-API-Tenant") != "" || captured.Get("CF-AIG-Authorization") != "" {
						t.Fatalf("another mount inherited custom headers: %v", captured)
					}
					if captured.Get("X-Unrelated-Secret") != "" {
						t.Fatal("undeclared header forwarded")
					}
					if captured.Get("Authorization") != "Bearer provider-key" {
						t.Fatal("standard provider credential changed")
					}
					for _, name := range []string{"session_id", "x-session-id", "x-client-request-id", "x-session-affinity"} {
						if captured.Get(name) != "session-fixture" {
							t.Fatalf("SDK session header %s lost", name)
						}
					}
				}
			}
		})
	}
}
