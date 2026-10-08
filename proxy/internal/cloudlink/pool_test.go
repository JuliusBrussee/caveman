package cloudlink

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
)

// addLogin writes a provider login the way `caveman providers add` does, on
// the file store.
func addLogin(t *testing.T, home, id, secret string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, "provider-logins"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "provider-logins", id), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"version": 1, "logins": []any{map[string]any{"id": id, "kind": "api_key", "store": "file"}}})
	if err := os.WriteFile(filepath.Join(home, "provider-logins.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// poolCloud answers every ask with answer and keeps the bodies it got.
type poolCloud struct {
	mu     sync.Mutex
	bodies []map[string]any
	answer func(body map[string]any) (int, string)
}

func (c *poolCloud) handler(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	c.mu.Lock()
	c.bodies = append(c.bodies, body)
	c.mu.Unlock()
	status, answer := c.answer(body)
	w.WriteHeader(status)
	_, _ = io.WriteString(w, answer)
}

func signedIn(t *testing.T, cloud string) string {
	return cloudHome(t, cloud, true, `{"access_token":"`+token(time.Now().Add(time.Hour))+`","gateway_api_key":"cave_project_key"}`)
}

func TestAskSendsThePoolOfWhatThePersonSetUp(t *testing.T) {
	fake := &poolCloud{answer: func(map[string]any) (int, string) { return 200, `{"model":"claude-opus-5-5"}` }}
	cloud := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer cloud.Close()
	home := signedIn(t, cloud.URL)
	// No login: no pool, models says it all.
	newLink(home).Ask(t.Context(), messagesAsk("claude-opus-5-5"))()
	if _, sent := fake.bodies[0]["pool"]; sent {
		t.Fatalf("pool sent without a login: %v", fake.bodies[0]["pool"])
	}
	addLogin(t, home, "openai", "sk-openai")
	newLink(home).Ask(t.Context(), messagesAsk("claude-opus-5-5"))()
	pool, _ := fake.bodies[1]["pool"].([]any)
	var got []string
	for _, item := range pool {
		entry := item.(map[string]any)
		got = append(got, entry["id"].(string)+"|"+entry["model"].(string)+"|"+entry["host"].(string)+"|"+entry["via"].(string))
	}
	want := []string{
		"harness/claude-opus-5-5|claude-opus-5-5|anthropic|local", "harness/claude-sonnet-5-5|claude-sonnet-5-5|anthropic|local",
		"openai/gpt-6.1-sol|gpt-6.1-sol|openai|local", "openai/gpt-6-sol|gpt-6-sol|openai|local",
		"openai/gpt-6-astra|gpt-6-astra|openai|local", "openai/gpt-6-luna|gpt-6-luna|openai|local",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("pool =\n%v\nwant\n%v", got, want)
	}
	if models := fake.bodies[1]["models"].([]any); len(models) != 2 {
		t.Fatalf("models must stay for older Clouds: %v", models)
	}
	raw, _ := json.Marshal(fake.bodies[1])
	if strings.Contains(string(raw), "sk-openai") {
		t.Fatal("a login's secret reached the ask")
	}
}

func TestPoolAnswerLocalLoginBecomesATarget(t *testing.T) {
	fake := &poolCloud{answer: func(map[string]any) (int, string) {
		return 200, `{"pool_id":"openai/gpt-6.1-sol","via":"local","model":"gpt-6.1-sol","effort":"high","decision_id":"d1"}`
	}}
	cloud := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer cloud.Close()
	home := signedIn(t, cloud.URL)
	addLogin(t, home, "openai", "sk-openai")
	answer := newLink(home).Ask(t.Context(), messagesAsk("claude-opus-5-5"))()
	target := answer.Target
	if target == nil || answer.Outcome != "routed" || answer.Model != "" || answer.Effort != "high" || answer.Reject == nil {
		t.Fatalf("answer = %+v", answer)
	}
	if target.Via != "local" || target.Host != "openai" || target.Wire != "responses" || target.URL != "https://api.openai.com/v1/responses" ||
		target.Header.Get("authorization") != "Bearer sk-openai" || target.Translate.Model != "gpt-6.1-sol" {
		t.Fatalf("target = %+v", target)
	}
	// A target that failed: the rest of the ask runs the asked model at the answered effort.
	link := newLink(home)
	link.Ask(t.Context(), messagesAsk("claude-opus-5-5"))().Reject()
	if again := link.Ask(t.Context(), messagesAsk("claude-opus-5-5"))(); again.Target != nil || again.Model != "" || again.Effort != "high" {
		t.Fatalf("after reject = %+v", again)
	}
}

func TestPoolAnswerHarnessEntryAppliesLikeModels(t *testing.T) {
	fake := &poolCloud{answer: func(map[string]any) (int, string) {
		return 200, `{"pool_id":"harness/claude-sonnet-5-5","via":"local","model":"claude-sonnet-5-5"}`
	}}
	cloud := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer cloud.Close()
	home := signedIn(t, cloud.URL)
	addLogin(t, home, "deepseek", "sk-ds")
	answer := newLink(home).Ask(t.Context(), messagesAsk("claude-opus-5-5"))()
	if answer.Target != nil || answer.Model != "claude-sonnet-5-5" || answer.Outcome != "routed" {
		t.Fatalf("answer = %+v", answer)
	}
}

func TestPoolAnswerViaCloudGoesToTheGateway(t *testing.T) {
	fake := &poolCloud{answer: func(map[string]any) (int, string) {
		return 200, `{"pool_id":"cloud:openai:gpt-6.1-sol","via":"cloud","model":"gpt-6.1-sol"}`
	}}
	cloud := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer cloud.Close()
	answer := newLink(signedIn(t, cloud.URL)).Ask(t.Context(), messagesAsk("claude-opus-5-5"))()
	target := answer.Target
	if target == nil || target.Via != "cloud" || target.URL != cloud.URL+"/v1/messages" || target.Wire != "messages" ||
		target.Header.Get("x-caveman-route") != "cloud:openai:gpt-6.1-sol" || target.Header.Get("authorization") != "Bearer cave_project_key" || target.Model != "gpt-6.1-sol" {
		t.Fatalf("answer = %+v target = %+v", answer, target)
	}
	for _, bad := range []string{
		`{"pool_id":"openai/gpt-6.1-sol","via":"local","model":"gpt-6.1-sol"}`, // never sent
		`{"pool_id":"bad id with spaces","via":"cloud","model":"m"}`,
		`{"pool_id":"cloud:OpenAI:gpt-6.1-sol","via":"cloud","model":"m"}`,      // provider not lower case
		`{"pool_id":"cloud:openai:gpt\r\nx-evil: 1","via":"cloud","model":"m"}`, // no header injection
		`{"pool_id":"cloud:openai:gpt-6.1-sol","via":"cloud"}`,                  // no model
		`{"pool_id":"cloud:openai:x","via":"carrier-pigeon"}`,
		`{"model":"gpt-6.1-sol"}`, // outside models, no pool_id
	} {
		fake.answer = func(map[string]any) (int, string) { return 200, bad }
		if answer := newLink(signedIn(t, cloud.URL)).Ask(t.Context(), messagesAsk("claude-opus-5-5"))(); answer.Target != nil || answer.Model != "" || answer.Reason != "answer_outside_pool" {
			t.Errorf("%s: answer = %+v", bad, answer)
		}
	}
}

func TestOlderCloudRefusingPoolIsAskedAgainWithoutIt(t *testing.T) {
	fake := &poolCloud{answer: func(body map[string]any) (int, string) {
		if _, has := body["pool"]; has {
			return 400, `{"error":{"code":"invalid_request","message":"unknown field pool"}}`
		}
		return 200, `{"model":"claude-sonnet-5-5"}`
	}}
	cloud := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer cloud.Close()
	home := signedIn(t, cloud.URL)
	addLogin(t, home, "openai", "sk-openai")
	link := newLink(home)
	if answer := link.Ask(t.Context(), messagesAsk("claude-opus-5-5"))(); answer.Model != "claude-sonnet-5-5" {
		t.Fatalf("answer = %+v", answer)
	}
	if len(fake.bodies) != 2 || fake.bodies[0]["pool"] == nil || fake.bodies[1]["pool"] != nil {
		t.Fatalf("bodies = %d, first pool %v, second pool %v", len(fake.bodies), fake.bodies[0]["pool"], fake.bodies[1]["pool"])
	}
	// From then on this login's asks leave pool out.
	ask := messagesAsk("claude-opus-5-5")
	ask.SessionID = "s2"
	link.Ask(t.Context(), ask)()
	if len(fake.bodies) != 3 || fake.bodies[2]["pool"] != nil {
		t.Fatalf("third ask: %d bodies, pool %v", len(fake.bodies), fake.bodies[len(fake.bodies)-1]["pool"])
	}
}

func TestViaCloudNeedsTheProjectKeyAndHonoursCloudOff(t *testing.T) {
	fake := &poolCloud{answer: func(map[string]any) (int, string) {
		return 200, `{"pool_id":"cloud:openai:gpt-6.1-sol","via":"cloud","model":"gpt-6.1-sol","effort":"high"}`
	}}
	cloud := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer cloud.Close()
	// No project key: the gateway would refuse the login token, so the asked model runs.
	keyless := newLink(cloudHome(t, cloud.URL, true, `{"access_token":"`+token(time.Now().Add(time.Hour))+`"}`))
	if answer := keyless.Ask(t.Context(), messagesAsk("claude-opus-5-5"))(); answer.Target != nil || answer.Reason != "cloud_unavailable" {
		t.Fatalf("keyless answer = %+v", answer)
	}
	// `caveman providers cloud off`: kept on the asked model, no effort from the cloud answer.
	home := signedIn(t, cloud.URL)
	if err := os.WriteFile(filepath.Join(home, "provider-logins.json"), []byte(`{"version":1,"cloud":false,"logins":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if answer := newLink(home).Ask(t.Context(), messagesAsk("claude-opus-5-5"))(); answer.Target != nil || answer.Outcome != "kept" || answer.Reason != "cloud_off" || answer.Effort != "" {
		t.Fatalf("cloud off answer = %+v", answer)
	}
}

// Only a 400 that names pool is retried, once, without it; the ask text goes once otherwise.
func TestOtherBadRequestsAreNeverRetried(t *testing.T) {
	fake := &poolCloud{answer: func(map[string]any) (int, string) {
		return 400, `{"error":{"code":"invalid_request","message":"text too long"}}`
	}}
	cloud := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer cloud.Close()
	home := signedIn(t, cloud.URL)
	addLogin(t, home, "openai", "sk-openai")
	newLink(home).Ask(t.Context(), messagesAsk("claude-opus-5-5"))()
	if len(fake.bodies) != 1 {
		t.Fatalf("asked %d times, want once", len(fake.bodies))
	}
}

// The answered effort was chosen for the target: a failed target's cached
// fallback fits it to the asked model's levels (minimal is low on Claude,
// max is xhigh on OpenAI, none is no effort on Claude).
func TestRejectFitsTheEffortToTheAskedModel(t *testing.T) {
	responses := func(model string) gateway.RouteAsk {
		return gateway.RouteAsk{Provider: "openai", Endpoint: "/v1/responses", Model: model, SessionID: "s1",
			Body: []byte(`{"model":"` + model + `","input":[{"role":"user","content":"fix the bug please"}]}`)}
	}
	thinkingOff := messagesAsk("claude-sonnet-5-5")
	thinkingOff.Body = []byte(`{"model":"claude-sonnet-5-5","thinking":{"type":"between_tools"},"messages":[{"role":"user","content":"fix the bug please"}]}`)
	for _, tc := range []struct {
		ask            gateway.RouteAsk
		answered, want string
	}{
		{messagesAsk("claude-opus-5-5"), "minimal", "low"},
		{messagesAsk("claude-opus-5-5"), "none", ""},
		{responses("gpt-6.1-sol"), "max", "xhigh"}, // not in the catalog: OpenAI's common set
		{responses("gpt-6-sol"), "max", "max"},     // the catalog lists max
		{thinkingOff, "max", "high"},
	} {
		fake := &poolCloud{answer: func(map[string]any) (int, string) {
			return 200, `{"pool_id":"fireworks/kimi-k3","via":"local","model":"kimi-k3","effort":"` + tc.answered + `","decision_id":"d1"}`
		}}
		cloud := httptest.NewServer(http.HandlerFunc(fake.handler))
		home := signedIn(t, cloud.URL)
		addLogin(t, home, "fireworks", "fw-key")
		link := newLink(home)
		first := link.Ask(t.Context(), tc.ask)()
		if first.Target == nil || first.Effort != tc.answered {
			t.Fatalf("%s: answer = %+v", tc.answered, first)
		}
		first.Reject()
		if again := link.Ask(t.Context(), tc.ask)(); again.Target != nil || again.Effort != tc.want {
			t.Errorf("%s on %s: replayed effort %q, want %q", tc.answered, tc.ask.Model, again.Effort, tc.want)
		}
		cloud.Close()
	}
}

// OpenAI Auto offers Cloud its three models alone, in models and in the pool.
func TestOpenAIAutoOffersOnlyItsThreeModels(t *testing.T) {
	fake := &poolCloud{answer: func(map[string]any) (int, string) { return 200, `{"model":"gpt-6-luna"}` }}
	cloud := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer cloud.Close()
	home := signedIn(t, cloud.URL)
	addLogin(t, home, "chatgpt", `{"access_token":"a","refresh_token":"r","expires_at":"2999-01-01T00:00:00Z"}`)
	raw, _ := json.Marshal(map[string]any{"version": 1, "logins": []any{map[string]any{"id": "chatgpt", "kind": "oauth", "store": "file"}}})
	if err := os.WriteFile(filepath.Join(home, "provider-logins.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"gpt-6.1-sol","input":[{"role":"user","content":[{"type":"input_text","text":"` + promptText + `"}]}]}`
	newLink(home).Ask(t.Context(), gateway.RouteAsk{Provider: "openai", Endpoint: "/v1/responses", Model: "gpt-6.1-sol", Agent: "codex",
		SessionID: "s1", InputBytes: len(body), Body: []byte(body), Models: gateway.AutoOpenAIModels})()
	models, _ := json.Marshal(fake.bodies[0]["models"])
	if string(models) != `["gpt-6.1-sol","gpt-6-astra","gpt-6-luna"]` {
		t.Fatalf("models = %s", models)
	}
	var ids []string
	pool, _ := fake.bodies[0]["pool"].([]any)
	for _, item := range pool {
		ids = append(ids, item.(map[string]any)["id"].(string))
	}
	if got := strings.Join(ids, " "); strings.Contains(got, "gpt-6-sol") || !strings.Contains(got, "chatgpt/gpt-6-astra") || !strings.Contains(got, "harness/gpt-6-luna") {
		t.Fatalf("pool = %s", got)
	}
}
