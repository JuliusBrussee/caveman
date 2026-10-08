package gateway

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/openaicompat"
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

// autoModelRE is a request body's first "model" pair.
var autoModelRE = regexp.MustCompile(`"model"\s*:\s*"([^"]+)"`)

// auto turns a route-stage test request into an Auto one that runs on the
// model it named: that model is Auto's fallback for the test, so the route
// stage asks about it and the bytes upstream are the ones the test expects.
func auto(t testing.TB, body string) string {
	t.Helper()
	match := autoModelRE.FindStringSubmatch(body)
	if match == nil || match[1] == AutoModel {
		return body
	}
	provider := "openai"
	if strings.HasPrefix(match[1], "claude-") {
		provider = "anthropic"
	}
	if was := autoFallback[provider]; was != match[1] {
		autoFallback[provider] = match[1]
		t.Cleanup(func() { autoFallback[provider] = was })
	}
	return strings.Replace(body, match[0], `"model":"`+AutoModel+`"`, 1)
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
		// The provider's own origin (routing skips any other), served by the stub.
		Adapters:   []providers.Adapter{anthropic.New("https://api.anthropic.com")},
		Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		Creds:      stubCreds{key: "sk-byok"},
		Sink:       &captureSink{},
		HTTPClient: &http.Client{Transport: toStub(upstream.URL)},
		Cloud:      cloud,
	}), &models
}

// toStub sends every upstream request to the stub server instead.
func toStub(stub string) http.RoundTripper {
	target, _ := url.Parse(stub)
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		return http.DefaultTransport.RoundTrip(r)
	})
}

func sendMessages(t *testing.T, srv *Server, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(auto(t, `{"model":"claude-opus-5-5","max_tokens":5,"messages":[{"role":"user","content":"fix the bug"}]}`)))
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
	if rec.Header().Get("x-caveman-routed-from") != AutoModel {
		t.Errorf("x-caveman-routed-from = %q", rec.Header().Get("x-caveman-routed-from"))
	}
	if len(cloud.asks) != 1 || cloud.asks[0].Model != "claude-opus-5-5" || cloud.asks[0].Agent != "claude" {
		t.Fatalf("asks = %+v", cloud.asks)
	}
	row := cloud.observed[0]
	if row.RouteFrom != AutoModel || row.RouteTo != "claude-sonnet-5-5" || row.Model != "claude-sonnet-5-5" || row.RouteOutcome != "routed" {
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

// bearerCreds resolves the agent's own bearer, as the standalone resolver does.
type bearerCreds struct{}

func (bearerCreds) Resolve(_ string, r *http.Request) providers.Credential {
	return providers.Credential{Mode: "ephemeral_header", Key: strings.TrimPrefix(r.Header.Get("authorization"), "Bearer "), Scheme: "bearer"}
}

// The request-wide opt-out never asks.
func TestRouteStageSkipsPassThrough(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Model: "claude-sonnet-5-5", Outcome: "routed"}}
	srv, models := routeServer(t, cloud, "")
	sendMessages(t, srv, map[string]string{"x-cave-transforms": "caveman.pass-through.v1"})
	if len(cloud.asks) != 0 || len(*models) != 1 || (*models)[0] != "claude-opus-5-5" {
		t.Fatalf("asks = %d, upstream models = %v; want none and the asked model", len(cloud.asks), *models)
	}
	if len(cloud.observed) != 1 || cloud.observed[0].RouteOutcome != "" {
		t.Errorf("observed = %+v", cloud.observed)
	}
}

// Auto on a subscription routes too: the routed model goes to the provider's
// own API on the subscription's own token, and a model the plan refuses
// replays on the asked one.
func TestRouteStageRoutesSubscriptions(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Model: "claude-sonnet-5-5", Outcome: "routed"}}
	var mu sync.Mutex
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		seen = append(seen, req.Model+" "+r.Header.Get("authorization"))
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		if req.Model == "claude-sonnet-5-5" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"permission_error","message":"not on this plan"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"m","type":"message","model":"`+req.Model+`","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()
	srv := New(Config{
		Adapters: []providers.Adapter{anthropic.New("https://api.anthropic.com")}, Auth: stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		Creds: bearerCreds{}, Sink: &captureSink{}, HTTPClient: &http.Client{Transport: toStub(upstream.URL)}, Cloud: cloud,
	})
	rec := sendMessages(t, srv, map[string]string{"x-api-key": "", "authorization": "Bearer sk-ant-oat01-subscription", "user-agent": "claude-cli/2.1.0"})
	if len(cloud.asks) != 1 {
		t.Fatalf("asks = %d, want one", len(cloud.asks))
	}
	want := []string{"claude-sonnet-5-5 Bearer sk-ant-oat01-subscription", "claude-opus-5-5 Bearer sk-ant-oat01-subscription"}
	if strings.Join(seen, "|") != strings.Join(want, "|") {
		t.Fatalf("upstream saw %q, want %q", seen, want)
	}
	if !strings.Contains(rec.Body.String(), `"model":"caveman-auto"`) || cloud.observed[0].RouteOutcome != "degraded" {
		t.Errorf("agent read %s, outcome %q", rec.Body.String(), cloud.observed[0].RouteOutcome)
	}
}

// A custom upstream origin (a proxy, Azure, a local server) is never routed.
func TestRouteStageSkipsCustomOrigins(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Model: "claude-sonnet-5-5", Outcome: "routed"}}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{"id":"m","type":"message","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()
	srv := New(Config{
		Adapters: []providers.Adapter{anthropic.New(upstream.URL)}, Auth: stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		Creds: stubCreds{key: "sk-byok"}, Sink: &captureSink{}, HTTPClient: &http.Client{}, Cloud: cloud,
	})
	sendMessages(t, srv, nil)
	if len(cloud.asks) != 0 || cloud.observed[0].RouteReason != "custom_provider_origin" || cloud.observed[0].ProviderOriginKnown {
		t.Fatalf("asks %d, row %+v", len(cloud.asks), cloud.observed[0])
	}
}

// The provider refusing the routed model tells the link, once.
func TestRouteStageReportsARejectedModel(t *testing.T) {
	rejected := 0
	cloud := &fakeCloud{answer: RouteAnswer{Model: "claude-sonnet-5-5", Outcome: "routed", Reject: func() { rejected++ }}}
	srv, _ := routeServer(t, cloud, "claude-sonnet-5-5")
	sendMessages(t, srv, nil)
	if rejected != 1 {
		t.Fatalf("Reject called %d times, want once", rejected)
	}
}

// A model the agent named itself is never asked about and goes as sent.
func TestRouteStageNeverAsksAboutANamedModel(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Model: "claude-sonnet-5-5", Effort: "low", Outcome: "routed"}}
	srv, log := effortServer(t, cloud, nil)
	body := convo("high", uA)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("x-api-key", "sk-ant-api-key")
	req.Header.Set("x-claude-code-session-id", "sess-1")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	sent, _ := log.last()
	if len(cloud.asks) != 0 || string(sent) != body || !strings.Contains(rec.Body.String(), "claude-opus-5-5") {
		t.Fatalf("asks %d, sent %s, agent read %s", len(cloud.asks), sent, rec.Body.String())
	}
	if row := cloud.observed[0]; row.RouteOutcome != "off" || row.RouteReason != "named_model" || row.Model != "claude-opus-5-5" {
		t.Errorf("row outcome %q model %q, want no route stage on the asked model", row.RouteOutcome, row.Model)
	}
}

// Auto that Cloud cannot route runs on the provider's fallback model, the ask
// names that model, and the agent reads Auto, in JSON and in a stream.
func TestAutoFallsBackAndShowsAuto(t *testing.T) {
	for _, stream := range []bool{false, true} {
		cloud := &fakeCloud{answer: RouteAnswer{Outcome: "degraded", Reason: "timeout"}}
		srv, log := streamServer(t, cloud)
		body := strings.Replace(convo("high", uA), `"model":"claude-opus-5-5"`, `"model":"caveman-auto"`, 1)
		if stream {
			body = strings.Replace(body, `"max_tokens":5`, `"max_tokens":5,"stream":true`, 1)
		}
		rec := post(t, srv, body, nil)
		sent, _ := log.last()
		if !bytes.Contains(sent, []byte(`"model":"claude-sonnet-5-5"`)) || bytes.Contains(sent, []byte(AutoModel)) {
			t.Fatalf("stream=%v: upstream got %s", stream, sent)
		}
		if len(cloud.asks) != 1 || cloud.asks[0].Model != "claude-sonnet-5-5" {
			t.Fatalf("stream=%v: asks %+v", stream, cloud.asks)
		}
		if got := rec.Body.String(); !strings.Contains(got, `"model":"caveman-auto"`) || strings.Contains(got, "claude-sonnet-5-5") {
			t.Errorf("stream=%v: the agent read %s", stream, got)
		}
		if row := cloud.observed[0]; row.RouteOutcome != "degraded" || row.Model != "claude-sonnet-5-5" {
			t.Errorf("stream=%v: row outcome %q model %q", stream, row.RouteOutcome, row.Model)
		}
	}
}

// count_tokens naming Auto counts on the fallback model; with no Cloud link
// at all Auto still never reaches the provider.
func TestAutoCountTokensAndNoCloudLink(t *testing.T) {
	for _, cloud := range []CloudLink{&fakeCloud{}, nil} {
		srv, log := effortServer(t, cloud, nil)
		for _, path := range []string{"/v1/messages/count_tokens", "/v1/messages"} {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"caveman-auto","messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set("x-api-key", "sk-ant-api-key")
			srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
			if sent, _ := log.last(); string(sent) != `{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":"hi"}]}` {
				t.Errorf("cloud %v %s: upstream got %s", cloud != nil, path, sent)
			}
		}
		if fake, ok := cloud.(*fakeCloud); ok && len(fake.asks) != 1 {
			t.Errorf("asks = %d, want one (Messages only)", len(fake.asks))
		}
	}
}

// Auto where no provider serves it (another provider, an unreadable model
// field) is a clean 400; an encoded body naming Auto is decoded and runs.
func TestAutoElsewhereIsRefusedAndEncodedAutoRuns(t *testing.T) {
	srv, log := effortServer(t, &fakeCloud{}, nil)
	compat, err := openaicompat.NewNamed("deepseek", "https://api.deepseek.com")
	if err != nil {
		t.Fatal(err)
	}
	refused := New(Config{
		Adapters: []providers.Adapter{compat}, Auth: stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		Creds: stubCreds{key: "sk"}, Sink: &captureSink{}, HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("Auto reached a provider that cannot serve it")
			return nil, nil
		})},
	})
	req := httptest.NewRequest(http.MethodPost, "/compat/deepseek/v1/chat/completions", strings.NewReader(`{"model":"caveman-auto","messages":[]}`))
	rec := httptest.NewRecorder()
	refused.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "cave_auto_unavailable") {
		t.Fatalf("other provider: %d %s", rec.Code, rec.Body.String())
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(`{"model":"caveman-auto","messages":[{"role":"user","content":"hi"}]}`))
	_ = zw.Close()
	req = httptest.NewRequest(http.MethodPost, "/v1/messages", &gz)
	req.Header.Set("x-api-key", "sk-ant-api-key")
	req.Header.Set("content-encoding", "gzip")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	sent, header := log.last()
	if rec.Code != http.StatusOK || string(sent) != `{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":"hi"}]}` || header.Get("content-encoding") != "" {
		t.Fatalf("gzip Auto: %d, upstream got %q (%q)", rec.Code, sent, header.Get("content-encoding"))
	}
}

// A refusal the asked model's own bytes get too (an expired login, a prompt
// too long) was not the route stage's doing: the ask keeps its decision.
func TestRouteStageKeepsTheDecisionWhenTheRetryFailsToo(t *testing.T) {
	for _, answer := range []RouteAnswer{{Model: "claude-sonnet-5-5", Outcome: "routed"}, {Effort: "low", Outcome: "kept"}} {
		rejected, calls := 0, 0
		answer.Reject = func() { rejected++ }
		srv := New(Config{
			Adapters: []providers.Adapter{anthropic.New("https://api.anthropic.com")}, Auth: stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
			Creds: stubCreds{key: "sk-byok"}, Sink: &captureSink{}, Cloud: &fakeCloud{answer: answer},
			HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusRequestEntityTooLarge, Request: r, Header: http.Header{"Content-Type": {"application/json"}},
					Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"request_too_large","message":"prompt is too long"}}`))}, nil
			})},
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(auto(t, `{"model":"claude-opus-5-5","max_tokens":5,"messages":[{"role":"user","content":"fix the bug"}]}`)))
		req.Header.Set("x-api-key", "sk-ant-api-key")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge || calls != 2 || rejected != 0 {
			t.Errorf("%s: agent read %d after %d upstream requests, rejected %d; want the 413 and the decision kept", answer.Outcome, rec.Code, calls, rejected)
		}
	}
}

// A transient Cloud failure runs the model the session's previous request
// went to (its cache holds the prefix) and records the failure as it was; a
// deliberate state, a limit or a provider's refusal runs the fallback.
func TestCloudFailureKeepsTheSessionsModel(t *testing.T) {
	session := map[string]string{"x-claude-code-session-id": "sess-1"}
	for _, tc := range []struct {
		answer RouteAnswer
		want   string
	}{
		{RouteAnswer{Outcome: "degraded", Reason: "timeout"}, "claude-sonnet-5-5"},
		{RouteAnswer{Outcome: "degraded", Reason: "cloud_unreachable"}, "claude-sonnet-5-5"},
		{RouteAnswer{Outcome: "degraded", Reason: "cloud_503"}, "claude-sonnet-5-5"},
		{RouteAnswer{Outcome: "degraded", Reason: "answer_unreadable"}, "claude-sonnet-5-5"},
		{RouteAnswer{Outcome: "off"}, "claude-opus-5-5"},
		{RouteAnswer{Outcome: "degraded", Reason: "login_expired"}, "claude-opus-5-5"},
		{RouteAnswer{Outcome: "degraded", Reason: "cloud_401"}, "claude-opus-5-5"},
		{RouteAnswer{Outcome: "degraded", Reason: "cloud_403"}, "claude-opus-5-5"},
		{RouteAnswer{Outcome: "paused", Reason: "allowance"}, "claude-opus-5-5"},
		{RouteAnswer{Outcome: "paused", Reason: "billing_limit"}, "claude-opus-5-5"},
		{RouteAnswer{Outcome: "degraded", Reason: "provider_rejected_routed_model"}, "claude-opus-5-5"},
	} {
		cloud := &fakeCloud{answer: RouteAnswer{Model: "claude-sonnet-5-5", Outcome: "routed"}}
		srv, models := routeServer(t, cloud, "")
		sendMessages(t, srv, session)
		cloud.mu.Lock()
		cloud.answer = tc.answer
		cloud.mu.Unlock()
		rec := sendMessages(t, srv, session)
		if got := *models; len(got) != 2 || got[1] != tc.want {
			t.Errorf("%s %s: upstream models = %v, want %s second", tc.answer.Outcome, tc.answer.Reason, got, tc.want)
		}
		if row := cloud.observed[1]; row.RouteOutcome != tc.answer.Outcome || row.RouteReason != tc.answer.Reason || row.Model != tc.want || !strings.Contains(rec.Body.String(), `"model":"caveman-auto"`) {
			t.Errorf("%s %s: row outcome %q reason %q model %q, agent read %s", tc.answer.Outcome, tc.answer.Reason, row.RouteOutcome, row.RouteReason, row.Model, rec.Body.String())
		}
	}
	// A session with no previous request runs the fallback.
	srv, models := routeServer(t, &fakeCloud{answer: RouteAnswer{Outcome: "degraded", Reason: "timeout"}}, "")
	sendMessages(t, srv, session)
	if got := *models; len(got) != 1 || got[0] != "claude-opus-5-5" {
		t.Errorf("first request of a session: upstream models = %v", got)
	}
}

// The Auto id with Claude Code's [1m] suffix is Auto: it is asked about, the
// fallback model goes upstream and the agent reads the id it sent.
func TestAutoTakesThe1MSuffix(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "degraded", Reason: "timeout"}}
	srv, log := effortServer(t, cloud, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"caveman-auto[1m]","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", "sk-ant-api-key")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if sent, _ := log.last(); string(sent) != `{"model":"claude-sonnet-5-5","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}` {
		t.Fatalf("upstream got %s", sent)
	}
	if len(cloud.asks) != 1 || cloud.asks[0].Model != "claude-sonnet-5-5" || !strings.Contains(rec.Body.String(), `"model":"caveman-auto[1m]"`) {
		t.Errorf("asks %+v, agent read %s", cloud.asks, rec.Body.String())
	}
	if row := cloud.observed[0]; row.RouteFrom != AutoModel || row.Model != "claude-sonnet-5-5" {
		t.Errorf("row from %q model %q", row.RouteFrom, row.Model)
	}
}

// The original-bytes retry of an Auto request asks for an identity answer too:
// a compressed one would reach the agent naming the real model.
func TestAutoRetryAnswerNamesAuto(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("content-type", "application/json")
		if req.Model == "claude-sonnet-5-5" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"no"}}`)
			return
		}
		// As a provider answers an agent that takes gzip (the Go transport
		// decodes only what it asked for itself).
		w.Header().Set("content-encoding", "gzip")
		zw := gzip.NewWriter(w)
		_, _ = io.WriteString(zw, `{"id":"m","type":"message","model":"`+req.Model+`","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
		_ = zw.Close()
	}))
	defer upstream.Close()
	srv := New(Config{
		Adapters: []providers.Adapter{anthropic.New("https://api.anthropic.com")}, Auth: stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		Creds: stubCreds{key: "sk-byok"}, Sink: &captureSink{}, HTTPClient: &http.Client{Transport: toStub(upstream.URL)},
		Cloud: &fakeCloud{answer: RouteAnswer{Model: "claude-sonnet-5-5", Outcome: "routed"}},
	})
	rec := sendMessages(t, srv, map[string]string{"accept-encoding": "gzip"})
	if rec.Header().Get("content-encoding") != "" || !strings.Contains(rec.Body.String(), `"model":"caveman-auto"`) {
		t.Fatalf("agent read (%q) %q", rec.Header().Get("content-encoding"), rec.Body.String())
	}
}

// rejectingCloud is a fakeCloud whose Reject does what the link's does: the
// rest of the ask runs the asked model.
func rejectingCloud(answer RouteAnswer) *fakeCloud {
	cloud := &fakeCloud{}
	answer.Reject = func() {
		cloud.mu.Lock()
		defer cloud.mu.Unlock()
		cloud.answer = RouteAnswer{Outcome: "degraded", Reason: "provider_rejected_routed_model"}
	}
	cloud.answer = answer
	return cloud
}

// heldServer is an Anthropic proxy whose upstream answers each model with
// the status set for it (200 when none) and records the models it was sent.
func heldServer(t *testing.T, cloud CloudLink) (*Server, *[]string, map[string]int) {
	t.Helper()
	var mu sync.Mutex
	models, statuses := []string{}, map[string]int{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		models = append(models, req.Model)
		status := statuses[req.Model]
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		if status != 0 {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"overloaded_error","message":"no"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"m","type":"message","model":"`+req.Model+`","content":[],"usage":{"input_tokens":10,"output_tokens":2}}`)
	}))
	t.Cleanup(upstream.Close)
	return New(Config{
		Adapters: []providers.Adapter{anthropic.New("https://api.anthropic.com")}, Auth: stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		Creds: stubCreds{key: "sk-byok"}, Sink: &captureSink{}, HTTPClient: &http.Client{Transport: toStub(upstream.URL)}, Cloud: cloud,
	}), &models, statuses
}

func sendAuto(t *testing.T, srv *Server, header map[string]string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(auto(t, `{"model":"claude-opus-5-5","max_tokens":5,"messages":[{"role":"user","content":"fix the bug"}]}`)))
	req.Header.Set("x-api-key", "sk-ant-api-key")
	for name, value := range header {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec.Code
}

// A 429 without Retry-After on the routed model and on the retry: the
// decision is rejected, so the agent's own retries send one request each.
func TestRateLimitedRetryDoesNotDoubleTheAgentsRetries(t *testing.T) {
	srv, models, statuses := heldServer(t, rejectingCloud(RouteAnswer{Model: "claude-sonnet-5-5", Outcome: "routed"}))
	statuses["claude-sonnet-5-5"], statuses["claude-opus-5-5"] = http.StatusTooManyRequests, http.StatusTooManyRequests
	for attempt, want := range []int{2, 3, 4} {
		if code := sendAuto(t, srv, nil); code != http.StatusTooManyRequests || len(*models) != want {
			t.Fatalf("attempt %d: status %d, %d upstream sends so far (%v), want %d", attempt+1, code, len(*models), *models, want)
		}
	}
}

// A held model that does not serve is not held again: the next attempt runs
// the fallback. One that is refused costs the replay once.
func TestHeldModelThatFailsGivesWayToTheFallback(t *testing.T) {
	session := map[string]string{"x-claude-code-session-id": "sess-1"}
	for _, status := range []int{529, http.StatusServiceUnavailable, http.StatusBadRequest} {
		cloud := &fakeCloud{answer: RouteAnswer{Model: "claude-sonnet-5-5", Outcome: "routed"}}
		srv, models, statuses := heldServer(t, cloud)
		sendAuto(t, srv, session)
		cloud.mu.Lock()
		cloud.answer = RouteAnswer{Outcome: "degraded", Reason: "timeout"}
		cloud.mu.Unlock()
		statuses["claude-sonnet-5-5"] = status
		sendAuto(t, srv, session)
		sendAuto(t, srv, session)
		want := "claude-sonnet-5-5 claude-sonnet-5-5 claude-opus-5-5"
		if status == http.StatusBadRequest {
			want = "claude-sonnet-5-5 claude-sonnet-5-5 claude-opus-5-5 claude-opus-5-5" // refused: replayed once
		}
		if got := strings.Join(*models, " "); got != want {
			t.Errorf("held model answering %d: upstream models %q, want %q", status, got, want)
		}
	}
}

// What heldModel refuses: another provider's model under the same session
// key, and a session whose last request was a compaction (no cache to keep).
func TestHeldModelGuards(t *testing.T) {
	timeout := RouteAnswer{Outcome: "degraded", Reason: "timeout"}
	for name, tc := range map[string]struct {
		provider string
		last     *RouteLast
		want     string
	}{
		"same provider":           {"anthropic", &RouteLast{sent: "claude-opus-5-5", provider: "anthropic"}, "claude-opus-5-5"},
		"another provider":        {"anthropic", &RouteLast{sent: "gpt-6-astra", provider: "openai"}, ""},
		"after compaction":        {"anthropic", &RouteLast{sent: "claude-opus-5-5", provider: "anthropic", Compacted: true}, ""},
		"openai":                  {"openai", &RouteLast{sent: "gpt-6-astra", provider: "openai"}, "gpt-6-astra"},
		"openai, anthropic model": {"openai", &RouteLast{sent: "claude-opus-5-5", provider: "anthropic"}, ""},
	} {
		if got := heldModel(timeout, tc.provider, tc.last); got != tc.want {
			t.Errorf("%s: held %q, want %q", name, got, tc.want)
		}
	}
}

// A model the agent named itself that ends in [1m] is not Auto: it goes
// upstream once as sent and Cloud is never asked.
func TestNamedModelWithThe1MSuffixIsNotAuto(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Model: "claude-sonnet-5-5", Outcome: "routed"}}
	srv, log := effortServer(t, cloud, nil)
	body := `{"model":"claude-opus-5-5[1m]","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("x-api-key", "sk-ant-api-key")
	srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
	if sent, _ := log.last(); len(log.bodies) != 1 || string(sent) != body || len(cloud.asks) != 0 {
		t.Fatalf("%d upstream requests, last %s, %d asks", len(log.bodies), sent, len(cloud.asks))
	}
}
