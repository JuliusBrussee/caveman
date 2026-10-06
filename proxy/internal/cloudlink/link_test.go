package cloudlink

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
)

const secretPrompt = "PROMPT-TEXT-THAT-MUST-STAY-LOCAL"

// cloudHome writes the CLI state a signed-in user with routing on leaves.
func cloudHome(t *testing.T, cloud string, routing bool, credentials string) string {
	t.Helper()
	home := t.TempDir()
	doc := map[string]any{"baseURL": cloud, "gatewayUrl": cloud, "tokenStore": "file", "deviceId": "device-1", "modules": map[string]any{"routing": routing}}
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(home, "cloud.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "credentials"), []byte(credentials), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func token(exp time.Time) string {
	payload, _ := json.Marshal(map[string]any{"uid": "u1", "exp": exp.Unix()})
	return base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func newLink(home string) *Link {
	link := New(home, nil)
	link.keychain = func() string { return "" }
	return link
}

func messagesAsk(model string) gateway.RouteAsk {
	body := fmt.Sprintf(`{"model":%q,"tools":[{"name":"bash"}],"messages":[{"role":"user","content":%q},{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"bash","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","is_error":true,"content":"boom"},{"type":"image","source":{}}]}]}`, model, secretPrompt)
	return gateway.RouteAsk{Provider: "anthropic", Endpoint: "/v1/messages", Model: model, Agent: "claude", SessionID: "s1", ToolsCount: 1, InputBytes: len(body), Body: []byte(body)}
}

func TestAskSendsOnlyModelsAndSignals(t *testing.T) {
	var got []byte
	var auth string
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		auth = r.Header.Get("authorization")
		_, _ = io.WriteString(w, `{"model":"claude-sonnet-5-5","reason":"ranked","decision_id":"0b9f6e4e-3b1a-4c7e-9a4e-1d2c3b4a5f60"}`)
	}))
	defer cloud.Close()
	link := newLink(cloudHome(t, cloud.URL, true, `{"access_token":"`+token(time.Now().Add(time.Hour))+`","gateway_api_key":"cave_project_key"}`))
	ask := messagesAsk("claude-opus-5-5")
	answer := link.Ask(t.Context(), ask)()
	if answer.Model != "claude-sonnet-5-5" || answer.Outcome != "routed" || answer.DecisionID == "" {
		t.Fatalf("answer = %+v", answer)
	}
	if strings.Contains(string(got), secretPrompt) || strings.Contains(string(got), "fix") {
		t.Fatalf("the ask carried prompt text: %s", got)
	}
	var body map[string]any
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatal(err)
	}
	if _, hasText := body["text"]; hasText {
		t.Fatalf("the ask has a text field: %s", got)
	}
	want := map[string]any{
		"models":  []any{"claude-opus-5-5", "claude-sonnet-5-5"},
		"signals": map[string]any{"agent": "claude", "context_tokens": float64(ask.InputBytes / 4), "tools_declared": float64(1), "tool_errors": float64(1), "images": true},
	}
	if fmt.Sprint(body) != fmt.Sprint(want) {
		t.Fatalf("ask body = %v\nwant %v", body, want)
	}
	if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") == "cave_project_key" {
		t.Errorf("authorization = %q, want the fresh session token", auth)
	}
	// A lapsed session token falls back to the durable project key.
	stale := newLink(cloudHome(t, cloud.URL, true, `{"access_token":"`+token(time.Now().Add(-time.Minute))+`","gateway_api_key":"cave_project_key"}`))
	stale.Ask(t.Context(), ask)()
	if auth != "Bearer cave_project_key" {
		t.Errorf("authorization = %q, want the project key once the session token lapsed", auth)
	}
}

func TestAskFailsOpen(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"cloud 500": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		"timeout":   func(w http.ResponseWriter, r *http.Request) { time.Sleep(1500 * time.Millisecond) },
		"cloud 401": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
		"allowance": func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"model":"claude-opus-5-5","reason":"allowance","decision_id":"0b9f6e4e-3b1a-4c7e-9a4e-1d2c3b4a5f60"}`)
		},
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			var hits atomic.Int32
			cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); handler(w, r) }))
			defer cloud.Close()
			link := newLink(cloudHome(t, cloud.URL, true, `{"access_token":"`+token(time.Now().Add(time.Hour))+`"}`))
			started := time.Now()
			answer := link.Ask(t.Context(), messagesAsk("claude-opus-5-5"))()
			if waited := time.Since(started); waited > routeBudget+300*time.Millisecond {
				t.Errorf("the request waited %v, beyond the %v budget", waited, routeBudget)
			}
			if answer.Model != "" || (answer.Outcome != "degraded" && answer.Outcome != "paused") {
				t.Fatalf("answer = %+v, want the asked model kept", answer)
			}
			// A new ask right after does not wait on Cloud again.
			next := messagesAsk("claude-opus-5-5")
			next.SessionID = "s2"
			if again := link.Ask(t.Context(), next)(); again.Model != "" {
				t.Fatalf("second answer = %+v", again)
			}
			if hits.Load() != 1 {
				t.Errorf("cloud asked %d times, want once before the pause", hits.Load())
			}
		})
	}
}

func TestAskOnlyWhenRoutingIsOnAndSignedIn(t *testing.T) {
	var hits atomic.Int32
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer cloud.Close()
	for name, home := range map[string]string{
		"routing off": cloudHome(t, cloud.URL, false, `{"access_token":"`+token(time.Now().Add(time.Hour))+`"}`),
		"signed out":  cloudHome(t, cloud.URL, true, ""),
		"no state":    t.TempDir(),
	} {
		if answer := newLink(home).Ask(t.Context(), messagesAsk("claude-opus-5-5"))(); answer.Outcome != "off" || answer.Model != "" {
			t.Errorf("%s: answer = %+v, want off", name, answer)
		}
	}
	home := cloudHome(t, cloud.URL, true, `{"access_token":"`+token(time.Now().Add(time.Hour))+`"}`)
	if answer := newLink(home).Ask(t.Context(), messagesAsk("claude-haiku-4-5"))(); answer.Outcome != "off" {
		t.Errorf("a model outside the pool: answer = %+v, want off", answer)
	}
	if hits.Load() != 0 {
		t.Errorf("cloud asked %d times, want never", hits.Load())
	}
}

// Every tool-loop turn of one ask reuses one decision, so the model never
// switches halfway through a turn.
func TestOneDecisionPerAsk(t *testing.T) {
	var hits atomic.Int32
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"model":"claude-sonnet-5-5","reason":"ranked"}`)
	}))
	defer cloud.Close()
	link := newLink(cloudHome(t, cloud.URL, true, `{"access_token":"`+token(time.Now().Add(time.Hour))+`"}`))
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if answer := link.Ask(t.Context(), messagesAsk("claude-opus-5-5"))(); answer.Model != "claude-sonnet-5-5" {
				t.Errorf("answer = %+v", answer)
			}
		}()
	}
	wg.Wait()
	if hits.Load() != 1 {
		t.Errorf("cloud asked %d times for one ask, want once", hits.Load())
	}
}

func record(i int) gateway.RequestRecord {
	return gateway.RequestRecord{
		Timestamp: time.Now().UTC().Format("2006-01-02 15:04:05.000"), AgentSlug: "claude", Provider: "anthropic",
		Model: "claude-sonnet-5-5", RouteFrom: "claude-opus-5-5", RouteTo: "claude-sonnet-5-5", AuthMode: "payg", RuntimeMode: "compress",
		InputTokens: 100 + i, OutputTokens: 10, TokenUsageBasis: "provider_complete", CompressionTokensBefore: 50, CompressionTokensAfter: 20,
		RouteOutcome: "routed", RouteReason: "ranked", RouteDecisionID: "0b9f6e4e-3b1a-4c7e-9a4e-1d2c3b4a5f60",
	}
}

func TestEventsFollowTheDataLevel(t *testing.T) {
	var mu sync.Mutex
	var batches [][]map[string]any
	level := "usage"
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			_, _ = fmt.Fprintf(w, `{"user":{"email":"a@b.c"},"data":{"level":%q}}`, level)
		case "/api/v1/runtime/events":
			var body struct {
				SchemaVersion int              `json:"schema_version"`
				Events        []map[string]any `json:"events"`
			}
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			if body.SchemaVersion != 1 || strings.Contains(string(raw), secretPrompt) {
				t.Errorf("bad batch: %s", raw)
			}
			mu.Lock()
			batches = append(batches, body.Events)
			mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer cloud.Close()
	link := newLink(cloudHome(t, cloud.URL, true, `{"access_token":"`+token(time.Now().Add(time.Hour))+`"}`))
	link.events.every = time.Hour
	for i := range 600 {
		link.Observe(record(i))
	}
	link.flush()
	mu.Lock()
	defer mu.Unlock()
	total := 0
	for _, batch := range batches {
		if len(batch) > eventBatchMax {
			t.Fatalf("batch of %d events, over %d", len(batch), eventBatchMax)
		}
		total += len(batch)
	}
	if total != 600 {
		t.Fatalf("sent %d events, want 600", total)
	}
	event := batches[0][0]
	if event["level"] != "usage" || event["model_used"] != "claude-sonnet-5-5" || event["tokens"] == nil || event["kept_out_of_context"] == nil {
		t.Errorf("usage event = %v", event)
	}
	if _, ok := event["route"]; ok {
		t.Errorf("a usage-level event carried the route decision: %v", event)
	}
}

func TestEventsDropOnFailureWithoutBlocking(t *testing.T) {
	var posts atomic.Int32
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runtime/events" {
			posts.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, `{"data":{}}`)
	}))
	defer cloud.Close()
	link := newLink(cloudHome(t, cloud.URL, true, `{"access_token":"`+token(time.Now().Add(time.Hour))+`"}`))
	link.events.every = time.Hour
	started := time.Now()
	for i := range 3 * eventQueueMax {
		link.Observe(record(i))
	}
	if took := time.Since(started); took > time.Second {
		t.Fatalf("Observe blocked: %v for %d events", took, 3*eventQueueMax)
	}
	link.flush()
	if n := len(link.events.ch); n != 0 {
		t.Fatalf("%d events kept after a failed send, want all dropped", n)
	}
	if posts.Load() == 0 {
		t.Fatal("nothing was sent")
	}
}

func TestEventsNeverSentAtLevelOffOrSignedOut(t *testing.T) {
	var posts atomic.Int32
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runtime/events" {
			posts.Add(1)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"level":"off"}}`)
	}))
	defer cloud.Close()
	link := newLink(cloudHome(t, cloud.URL, true, `{"access_token":"`+token(time.Now().Add(time.Hour))+`"}`))
	link.events.every = time.Hour
	link.Observe(record(1))
	link.flush()
	signedOut := newLink(cloudHome(t, cloud.URL, true, ""))
	signedOut.Observe(record(2))
	if posts.Load() != 0 {
		t.Fatalf("%d batches sent at level off or signed out", posts.Load())
	}
}

// Every event, at every level, uses only the fields runtime/v1 defines, with
// its required ones and its patterns and enums (the schema in the contracts
// package is the source).
func TestEventsMatchTheRuntimeV1Schema(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "packages", "shared", "contracts", "schemas", "runtime-event-v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs struct {
			Event struct {
				Required   []string                   `json:"required"`
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"event"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	for _, level := range []string{"counts", "usage", "decisions"} {
		encoded, _ := json.Marshal(eventFor(record(1), strings.Repeat("ab", 32), time.Now()).atLevel(level))
		var event map[string]any
		_ = json.Unmarshal(encoded, &event)
		for _, field := range schema.Defs.Event.Required {
			if _, ok := event[field]; !ok {
				t.Errorf("%s: required %q missing", level, field)
			}
		}
		for field, value := range event {
			spec, ok := schema.Defs.Event.Properties[field]
			if !ok {
				t.Errorf("%s: %q is not a runtime/v1 field", level, field)
				continue
			}
			var rule struct {
				Pattern string `json:"pattern"`
				Enum    []any  `json:"enum"`
			}
			_ = json.Unmarshal(spec, &rule)
			if text, isText := value.(string); isText && rule.Pattern != "" && !regexp.MustCompile(rule.Pattern).MatchString(text) {
				t.Errorf("%s: %s=%q does not match %s", level, field, text, rule.Pattern)
			}
			if len(rule.Enum) > 0 && !slices.Contains(rule.Enum, value) {
				t.Errorf("%s: %s=%v is not one of %v", level, field, value, rule.Enum)
			}
		}
	}
}

// A key minted before it could route answers 403: the request keeps its model,
// status learns why from route-state.json, and a new login lifts the pause.
func TestRefusedKeyFailsOpenUntilANewLogin(t *testing.T) {
	var hits atomic.Int32
	refuse := atomic.Bool{}
	refuse.Store(true)
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if refuse.Load() {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"code":"cave_scope_missing"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"model":"claude-sonnet-5-5","reason":"ranked"}`)
	}))
	defer cloud.Close()
	home := cloudHome(t, cloud.URL, true, `{"access_token":"`+token(time.Now().Add(-time.Hour))+`","gateway_api_key":"cave_old_key"}`)
	link := newLink(home)
	if answer := link.Ask(t.Context(), messagesAsk("claude-opus-5-5"))(); answer.Model != "" || answer.Outcome != "degraded" || answer.Reason != "cloud_403" {
		t.Fatalf("answer = %+v, want the asked model kept and degraded", answer)
	}
	var state map[string]string
	raw, err := os.ReadFile(filepath.Join(home, "route-state.json"))
	if err != nil || json.Unmarshal(raw, &state) != nil || state["outcome"] != "degraded" || state["reason"] != "cloud_403" || state["until"] == "" {
		t.Fatalf("route-state.json = %s (%v)", raw, err)
	}
	next := messagesAsk("claude-opus-5-5")
	next.SessionID = "s2"
	link.Ask(t.Context(), next)()
	if hits.Load() != 1 {
		t.Fatalf("cloud asked %d times during the pause, want once", hits.Load())
	}
	// `caveman login` writes a new credential: the pause lifts and the record goes.
	refuse.Store(false)
	time.Sleep(10 * time.Millisecond) // a distinct mtime for the rewritten file
	if err := os.WriteFile(filepath.Join(home, "credentials"), []byte(`{"access_token":"`+token(time.Now().Add(time.Hour))+`","gateway_api_key":"cave_new_key"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	next.SessionID = "s3"
	if answer := link.Ask(t.Context(), next)(); answer.Model != "claude-sonnet-5-5" {
		t.Fatalf("after a new login: answer = %+v", answer)
	}
	if _, err := os.Stat(filepath.Join(home, "route-state.json")); !os.IsNotExist(err) {
		t.Errorf("route-state.json kept after routing works again: %v", err)
	}
}

// A billing limit pauses like the allowance and keeps Cloud's notice for status.
func TestBillingLimitPausesWithCloudsNotice(t *testing.T) {
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"model":"claude-opus-5-5","reason":"billing_limit","notice":"Routing hit your $20 limit · raise it: caveman billing"}`)
	}))
	defer cloud.Close()
	home := cloudHome(t, cloud.URL, true, `{"access_token":"`+token(time.Now().Add(time.Hour))+`"}`)
	if answer := newLink(home).Ask(t.Context(), messagesAsk("claude-opus-5-5"))(); answer.Model != "" || answer.Outcome != "paused" || answer.Reason != "billing_limit" {
		t.Fatalf("answer = %+v", answer)
	}
	raw, _ := os.ReadFile(filepath.Join(home, "route-state.json"))
	if !strings.Contains(string(raw), `"reason":"billing_limit"`) || !strings.Contains(string(raw), "raise it: caveman billing") {
		t.Errorf("route-state.json = %s", raw)
	}
}

// A Responses chain continued by previous_response_id is never routed: its
// follow-ups carry no human text, so a decision could not hold for the turn.
func TestStatefulResponsesChainsAreNotRouted(t *testing.T) {
	var hits atomic.Int32
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer cloud.Close()
	link := newLink(cloudHome(t, cloud.URL, true, `{"access_token":"`+token(time.Now().Add(time.Hour))+`"}`))
	body := `{"model":"gpt-6-sol","previous_response_id":"resp_1","input":[{"type":"function_call_output","call_id":"c","output":"ok"}]}`
	answer := link.Ask(t.Context(), gateway.RouteAsk{Provider: "openai", Endpoint: "/v1/responses", Model: "gpt-6-sol", Body: []byte(body)})()
	if answer.Outcome != "off" || answer.Reason != "stateful_chain" || hits.Load() != 0 {
		t.Fatalf("answer = %+v, cloud hits %d", answer, hits.Load())
	}
}

// Events recorded under one login are never sent with another's credential,
// and the new login's own data level decides.
func TestEventsStayWithTheLoginThatRecordedThem(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			level := "usage"
			if strings.HasSuffix(r.Header.Get("authorization"), "org-b") {
				level = "off"
			}
			_, _ = fmt.Fprintf(w, `{"data":{"level":%q}}`, level)
		case "/api/v1/runtime/events":
			mu.Lock()
			sent = append(sent, r.Header.Get("authorization"))
			mu.Unlock()
		}
	}))
	defer cloud.Close()
	home := cloudHome(t, cloud.URL, true, `{"access_token":"org-a"}`)
	link := newLink(home)
	link.events.every = time.Hour
	link.Observe(record(1))
	link.flush() // org A, level usage: sent
	link.Observe(record(2))
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(home, "credentials"), []byte(`{"access_token":"org-b"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link.flush() // org A's queued event must not go out as org B, and B is level off
	link.Observe(record(3))
	link.flush()
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 || sent[0] != "Bearer org-a" {
		t.Fatalf("batches sent with %v, want one, as org A", sent)
	}
}

// Events flow while signed in with routing off, and say the route stage was
// off. The level is /me's: decisions only when /me says so, counts when /me
// names none, and nothing at all under the CLI telemetry opt-out.
func TestEventsFollowMeAndTheOptOut(t *testing.T) {
	var mu sync.Mutex
	var events []map[string]any
	me := `{"data":{"level":"decisions"}}`
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/me" {
			mu.Lock()
			_, _ = io.WriteString(w, me)
			mu.Unlock()
			return
		}
		var batch struct {
			Events []map[string]any `json:"events"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &batch)
		mu.Lock()
		events = append(events, batch.Events...)
		mu.Unlock()
	}))
	defer cloud.Close()
	send := func(home string) []map[string]any {
		t.Helper()
		mu.Lock()
		events = nil
		mu.Unlock()
		link := newLink(home)
		link.events.every = time.Hour
		rec := record(1)
		rec.RouteOutcome, rec.RouteReason, rec.RouteDecisionID = "", "", ""
		rec.ProviderOriginKnown, rec.PricingKnown, rec.Model, rec.RouteFrom = false, false, "/models/llama-3.gguf", "/models/llama-3.gguf"
		link.Observe(rec)
		link.flush()
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), events...)
	}
	signedIn := `{"access_token":"` + token(time.Now().Add(time.Hour)) + `"}`

	got := send(cloudHome(t, cloud.URL, false, signedIn))
	if len(got) != 1 || got[0]["level"] != "decisions" || got[0]["model_used"] != "custom" {
		t.Fatalf("routing off, level decisions: %v", got)
	}
	if route, _ := got[0]["route"].(map[string]any); route["outcome"] != "off" {
		t.Errorf("route = %v, want outcome off", got[0]["route"])
	}

	mu.Lock()
	me = `{"user":{"email":"a@b.c"}}`
	mu.Unlock()
	if got := send(cloudHome(t, cloud.URL, false, signedIn)); len(got) != 1 || got[0]["level"] != "counts" || got[0]["route"] != nil {
		t.Fatalf("/me without a level: %v, want counts", got)
	}

	optedOut := cloudHome(t, cloud.URL, true, signedIn)
	raw, _ := os.ReadFile(filepath.Join(optedOut, "cloud.json"))
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	doc["telemetry"] = map[string]any{"enabled": false, "decidedAt": "2026-10-05T00:00:00Z", "promptVersion": 2}
	raw, _ = json.Marshal(doc)
	_ = os.WriteFile(filepath.Join(optedOut, "cloud.json"), raw, 0o600)
	if got := send(optedOut); len(got) != 0 {
		t.Fatalf("telemetry off still sent %v", got)
	}
	t.Setenv("DO_NOT_TRACK", "1")
	if got := send(cloudHome(t, cloud.URL, true, signedIn)); len(got) != 0 {
		t.Fatalf("DO_NOT_TRACK still sent %v", got)
	}
}

// A 200 that is not JSON pauses like any other bad answer.
func TestUnreadableAnswerPauses(t *testing.T) {
	var hits atomic.Int32
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "<html>gateway</html>")
	}))
	defer cloud.Close()
	link := newLink(cloudHome(t, cloud.URL, true, `{"access_token":"`+token(time.Now().Add(time.Hour))+`"}`))
	if answer := link.Ask(t.Context(), messagesAsk("claude-opus-5-5"))(); answer.Model != "" || answer.Reason != "answer_unreadable" {
		t.Fatalf("answer = %+v", answer)
	}
	next := messagesAsk("claude-opus-5-5")
	next.SessionID = "s2"
	link.Ask(t.Context(), next)()
	if hits.Load() != 1 {
		t.Errorf("cloud asked %d times, want once before the pause", hits.Load())
	}
}

// A limit pause asks /me at most every 15 minutes and lifts once routing is no
// longer limited (a card added, a limit raised).
func TestLimitPauseLiftsWhenMeSaysSo(t *testing.T) {
	var routeHits atomic.Int32
	var limited atomic.Bool
	limited.Store(true)
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			state := "ok"
			if limited.Load() {
				state = "limited"
			}
			_, _ = fmt.Fprintf(w, `{"plan":"free","products":[{"id":"routing","state":%q}]}`, state)
		case "/v1/route":
			routeHits.Add(1)
			if limited.Load() {
				_, _ = io.WriteString(w, `{"model":"claude-opus-5-5","reason":"allowance"}`)
				return
			}
			_, _ = io.WriteString(w, `{"model":"claude-sonnet-5-5","reason":"ranked"}`)
		}
	}))
	defer cloud.Close()
	link := newLink(cloudHome(t, cloud.URL, true, `{"access_token":"`+token(time.Now().Add(48*time.Hour))+`"}`))
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	link.now = func() time.Time { return time.Unix(0, clock.Load()) }
	ask := func(session string) gateway.RouteAnswer {
		next := messagesAsk("claude-opus-5-5")
		next.SessionID = session
		return link.Ask(t.Context(), next)()
	}
	if answer := ask("s1"); answer.Outcome != "paused" {
		t.Fatalf("answer = %+v", answer)
	}
	limited.Store(false) // a card is added
	if answer := ask("s2"); answer.Outcome != "paused" || routeHits.Load() != 1 {
		t.Fatalf("within 15 minutes: answer = %+v, route asks %d", answer, routeHits.Load())
	}
	clock.Add(int64(16 * time.Minute))
	ask("s3") // starts the /me check in the background and stays paused
	deadline := time.Now().Add(2 * time.Second)
	for {
		if answer := ask(fmt.Sprintf("s4-%d", time.Now().UnixNano())); answer.Model == "claude-sonnet-5-5" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the pause never lifted after /me stopped marking routing limited")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Once the provider refuses the routed model, the rest of the ask keeps the
// asked one rather than failing over on every turn.
func TestRejectedModelKeepsTheAskOnTheAskedModel(t *testing.T) {
	var hits atomic.Int32
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"model":"claude-sonnet-5-5","reason":"ranked"}`)
	}))
	defer cloud.Close()
	link := newLink(cloudHome(t, cloud.URL, true, `{"access_token":"`+token(time.Now().Add(time.Hour))+`"}`))
	first := link.Ask(t.Context(), messagesAsk("claude-opus-5-5"))()
	if first.Model != "claude-sonnet-5-5" || first.Reject == nil {
		t.Fatalf("first = %+v", first)
	}
	first.Reject()
	if next := link.Ask(t.Context(), messagesAsk("claude-opus-5-5"))(); next.Model != "" || next.Reason != "provider_rejected_routed_model" || hits.Load() != 1 {
		t.Fatalf("next = %+v, cloud hits %d", next, hits.Load())
	}
}

// A login rewrites cloud.json; the secret cached from the keychain is dropped
// at once, so no ask goes out with the old login's credential.
func TestKeychainSecretIsDroppedWhenTheLoginChanges(t *testing.T) {
	home := t.TempDir()
	write := func(org string) {
		raw, _ := json.Marshal(map[string]any{"baseURL": "https://api.example.test", "tokenStore": "keychain", "organizationId": org, "modules": map[string]any{"routing": true}})
		if err := os.WriteFile(filepath.Join(home, "cloud.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var secret atomic.Value
	secret.Store(`{"access_token":"login-a"}`)
	write("org-a")
	link := New(home, nil)
	link.keychain = func() string { return secret.Load().(string) }
	waitFor := func(want string) {
		t.Helper()
		for deadline := time.Now().Add(2 * time.Second); link.settings().access != want; time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("credential = %q, want %q", link.settings().access, want)
			}
		}
	}
	waitFor("login-a")
	secret.Store(`{"access_token":"login-b"}`)
	time.Sleep(10 * time.Millisecond)
	write("org-b-longer")
	if got := link.settings().access; got == "login-a" {
		t.Fatal("the old login's secret is still in use after cloud.json changed")
	}
	waitFor("login-b")
}
