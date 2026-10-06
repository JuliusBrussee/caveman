package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
)

func TestSetModelSwapsOnlyTheTopLevelModel(t *testing.T) {
	body := []byte(`{ "messages": [{"role":"user","content":"model"}], "model" : "claude-opus-5-5", "metadata": {"model": "x"} }`)
	out, ok := setModel(body, "claude-sonnet-5-5")
	if !ok {
		t.Fatal("setModel refused a body with a top-level model")
	}
	want := `{ "messages": [{"role":"user","content":"model"}], "model" : "claude-sonnet-5-5", "metadata": {"model": "x"} }`
	if string(out) != want {
		t.Fatalf("setModel changed more than the model value:\n got %s\nwant %s", out, want)
	}
	for _, refused := range []string{`{"messages":[],"metadata":{"model":"x"}}`, `{"model":7}`, `[{"model":"a"}]`, `not json`} {
		if out, ok := setModel([]byte(refused), "m"); ok || string(out) != refused {
			t.Errorf("setModel(%s) = %s, %v; want the body unchanged and false", refused, out, ok)
		}
	}
}

type fakeCloud struct {
	mu       sync.Mutex
	answer   RouteAnswer
	asks     []RouteAsk
	observed []RequestRecord
}

func (f *fakeCloud) Ask(_ context.Context, ask RouteAsk) func() RouteAnswer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asks = append(f.asks, ask)
	return func() RouteAnswer { return f.answer }
}

func (f *fakeCloud) Observe(rec RequestRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed = append(f.observed, rec)
}

// routeServer is an Anthropic proxy whose upstream records each model it was
// sent and answers rejectModel with a 400.
func routeServer(t *testing.T, cloud CloudLink, rejectModel string) (*Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	models := []string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(raw, &req)
		mu.Lock()
		models = append(models, req.Model)
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		if req.Model == rejectModel {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"no"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"m","type":"message","model":"`+req.Model+`","content":[],"usage":{"input_tokens":10,"output_tokens":2}}`)
	}))
	t.Cleanup(upstream.Close)
	return New(Config{
		Adapters:   []providers.Adapter{anthropic.New(upstream.URL)},
		Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		Creds:      stubCreds{key: "sk-byok"},
		Sink:       &captureSink{},
		HTTPClient: &http.Client{},
		Cloud:      cloud,
	}), &models
}

func sendMessages(t *testing.T, srv *Server, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-opus-5-5","max_tokens":5,"messages":[{"role":"user","content":"fix the bug"}]}`))
	req.Header.Set("x-api-key", "sk-ant-api-key")
	req.Header.Set("x-cave-agent", "claude")
	for name, value := range header {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	return rec
}

func TestRouteStageMovesTheModelAndRecordsBoth(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Model: "claude-sonnet-5-5", Outcome: "routed", Reason: "ranked", DecisionID: "0b9f6e4e-3b1a-4c7e-9a4e-1d2c3b4a5f60"}}
	srv, models := routeServer(t, cloud, "")
	rec := sendMessages(t, srv, nil)
	if got := *models; len(got) != 1 || got[0] != "claude-sonnet-5-5" {
		t.Fatalf("upstream models = %v, want [claude-sonnet-5-5]", got)
	}
	if rec.Header().Get("x-caveman-routed-from") != "claude-opus-5-5" {
		t.Errorf("x-caveman-routed-from = %q", rec.Header().Get("x-caveman-routed-from"))
	}
	if len(cloud.asks) != 1 || cloud.asks[0].Model != "claude-opus-5-5" || cloud.asks[0].Agent != "claude" {
		t.Fatalf("asks = %+v", cloud.asks)
	}
	row := cloud.observed[0]
	if row.RouteFrom != "claude-opus-5-5" || row.RouteTo != "claude-sonnet-5-5" || row.Model != "claude-sonnet-5-5" || row.RouteOutcome != "routed" {
		t.Errorf("row route = from %q to %q model %q outcome %q", row.RouteFrom, row.RouteTo, row.Model, row.RouteOutcome)
	}
}

func TestRouteStageFailsOpenToTheAskedModel(t *testing.T) {
	for _, answer := range []RouteAnswer{
		{Outcome: "degraded", Reason: "timeout"},
		{Outcome: "paused", Reason: "allowance"},
		{Outcome: "off"},
		{Model: "claude-opus-5-5", Outcome: "kept"},
	} {
		cloud := &fakeCloud{answer: answer}
		srv, models := routeServer(t, cloud, "")
		rec := sendMessages(t, srv, nil)
		if got := *models; len(got) != 1 || got[0] != "claude-opus-5-5" {
			t.Errorf("%s: upstream models = %v, want the asked model", answer.Outcome, got)
		}
		if rec.Header().Get("x-caveman-routed-from") != "" {
			t.Errorf("%s: routed-from header set", answer.Outcome)
		}
		if cloud.observed[0].RouteOutcome != answer.Outcome {
			t.Errorf("%s: recorded outcome %q", answer.Outcome, cloud.observed[0].RouteOutcome)
		}
	}
}

// A provider that rejects the routed model gets the original request, on the
// model the agent asked for, through the existing original-bytes retry.
func TestRouteStageRejectedModelRetriesTheAskedOne(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Model: "claude-sonnet-5-5", Outcome: "routed"}}
	srv, models := routeServer(t, cloud, "claude-sonnet-5-5")
	rec := sendMessages(t, srv, nil)
	if got := *models; len(got) != 2 || got[0] != "claude-sonnet-5-5" || got[1] != "claude-opus-5-5" {
		t.Fatalf("upstream models = %v, want the routed one then the asked one", got)
	}
	if rec.Header().Get("x-caveman-routed-from") != "" {
		t.Error("routed-from header survived the retry on the asked model")
	}
	if row := cloud.observed[0]; row.Model != "claude-opus-5-5" || row.RouteOutcome != "degraded" {
		t.Errorf("row model %q outcome %q, want the asked model and degraded", row.Model, row.RouteOutcome)
	}
}

// The request-wide opt-out and subscription traffic never ask.
func TestRouteStageSkipsPassThroughAndSubscription(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Model: "claude-sonnet-5-5", Outcome: "routed"}}
	srv, models := routeServer(t, cloud, "")
	sendMessages(t, srv, map[string]string{"x-cave-transforms": "caveman.pass-through.v1"})
	sendMessages(t, srv, map[string]string{"x-api-key": "", "authorization": "Bearer sk-ant-oat01-subscription", "user-agent": "claude-cli/2.1.0"})
	if len(cloud.asks) != 0 {
		t.Fatalf("asks = %d, want none", len(cloud.asks))
	}
	for _, model := range *models {
		if model != "claude-opus-5-5" {
			t.Fatalf("upstream models = %v, want only the asked model", *models)
		}
	}
	if len(cloud.observed) != 2 || cloud.observed[0].RouteOutcome != "" {
		t.Errorf("observed = %d rows, first outcome %q", len(cloud.observed), cloud.observed[0].RouteOutcome)
	}
}
