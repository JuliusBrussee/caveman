package cloudlink

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
)

type localAuth struct{}

func (localAuth) Authenticate(context.Context, *http.Request) (gateway.RequestContext, error) {
	return gateway.RequestContext{Label: "local", RuntimeMode: "compress"}, nil
}

type byok struct{}

func (byok) Resolve(string, *http.Request) providers.Credential {
	return providers.Credential{Mode: "passthrough"}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type nullSink struct{}

func (nullSink) Record(gateway.RequestRecord) {}

// The whole path in one process: the CLI's signed-in state on disk, the proxy
// with the link, a Cloud that routes Auto, and a provider that records the model.
func TestSignedInProxyRoutesAndReports(t *testing.T) {
	var mu sync.Mutex
	var asked, upstreamModel string
	var events []map[string]any
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/v1/route":
			asked = string(raw)
			_, _ = io.WriteString(w, `{"model":"claude-opus-5-5","reason":"ranked","decision_id":"0b9f6e4e-3b1a-4c7e-9a4e-1d2c3b4a5f60"}`)
		case "/api/v1/auth/me":
			_, _ = io.WriteString(w, `{"data":{"level":"decisions"}}`)
		case "/api/v1/runtime/events":
			var batch struct {
				Events []map[string]any `json:"events"`
			}
			_ = json.Unmarshal(raw, &batch)
			events = append(events, batch.Events...)
		}
	}))
	defer cloud.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &req)
		mu.Lock()
		upstreamModel = req.Model
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{"id":"m","type":"message","model":"`+req.Model+`","content":[],"usage":{"input_tokens":10,"output_tokens":2}}`)
	}))
	defer provider.Close()

	link := newLink(cloudHome(t, cloud.URL, true, `{"access_token":"`+token(time.Now().Add(time.Hour))+`","gateway_api_key":"cave_project_key"}`))
	link.events.every = time.Hour
	target, _ := url.Parse(provider.URL)
	srv := gateway.New(gateway.Config{
		// The provider's own origin (the route stage skips any other), served by the stub.
		Adapters: []providers.Adapter{anthropic.New("https://api.anthropic.com")},
		Auth:     localAuth{},
		Creds:    byok{},
		Sink:     nullSink{},
		HTTPClient: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
			return http.DefaultTransport.RoundTrip(r)
		})},
		Cloud: link,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"caveman-auto","max_tokens":5,"messages":[{"role":"user","content":"`+promptText+`"}]}`))
	req.Header.Set("x-api-key", "sk-ant-api03-test")
	req.Header.Set("x-cave-agent", "claude")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	link.flush()

	mu.Lock()
	defer mu.Unlock()
	if upstreamModel != "claude-opus-5-5" || !strings.Contains(rec.Body.String(), `"model":"caveman-auto"`) {
		t.Errorf("provider got model %q, want the routed claude-opus-5-5; the agent read %s", upstreamModel, rec.Body.String())
	}
	if !strings.Contains(asked, `"ask":{"text":"`+promptText+`"}`) || !strings.Contains(asked, `"signals":{"agent":"claude",`) {
		t.Errorf("ask = %s", asked)
	}
	if len(events) != 1 {
		t.Fatalf("events = %v", events)
	}
	route, _ := events[0]["route"].(map[string]any)
	if events[0]["model_requested"] != "caveman-auto" || events[0]["model_used"] != "claude-opus-5-5" || route["outcome"] != "routed" {
		t.Errorf("event = %v", events[0])
	}
	if raw, _ := json.Marshal(events); strings.Contains(string(raw), promptText) {
		t.Error("an event carried prompt text")
	}
}
