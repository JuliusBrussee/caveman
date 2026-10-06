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

func TestAskSendsTheFeaturesLineAndNoPromptText(t *testing.T) {
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
	want := map[string]any{
		"models": []any{"claude-opus-5-5", "claude-sonnet-5-5"},
		"text":   fmt.Sprintf("routerd features: harness=claude context_tokens=%d tools_declared=1 recent_tool_errors=1 images=true", ask.InputBytes/4),
	}
	if fmt.Sprint(body) != fmt.Sprint(want) {
		t.Fatalf("ask body = %v\nwant %v", body, want)
	}
	if auth != "Bearer cave_project_key" {
		t.Errorf("authorization = %q, want the durable project key", auth)
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
