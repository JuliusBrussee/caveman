package cloudlink

import (
	"encoding/json"
	"fmt"
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
	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

func TestRequestFromEachShape(t *testing.T) {
	many := make([]string, 130)
	for i := range many {
		many[i] = fmt.Sprintf(`{"name":"tool_%d","input_schema":{}}`, i)
	}
	cases := []struct {
		name, endpoint, body string
		want                 routeRequest
	}{
		{
			name:     "anthropic messages",
			endpoint: "/v1/messages",
			body:     `{"model":"claude-opus-5-5","thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"},"tools":[{"name":"Read","input_schema":{}},{"type":"web_search_20250305","name":"web_search"}],"messages":[]}`,
			want:     routeRequest{Endpoint: "messages", ToolNames: []string{"Read", "web_search"}, Effort: "xhigh", Thinking: "adaptive"},
		},
		{
			name:     "anthropic, thinking type unknown and no effort",
			endpoint: "/v1/messages",
			body:     `{"thinking":{"type":"sometimes"},"messages":[]}`,
			want:     routeRequest{Endpoint: "messages"},
		},
		{
			name:     "openai chat",
			endpoint: "/v1/chat/completions",
			body:     `{"reasoning_effort":"low","tools":[{"type":"function","function":{"name":"shell","parameters":{}}}],"messages":[]}`,
			want:     routeRequest{Endpoint: "chat", ToolNames: []string{"shell"}, Effort: "low"},
		},
		{
			name:     "openai responses",
			endpoint: "/v1/responses",
			body:     `{"reasoning":{"effort":"medium","summary":"auto"},"tools":[{"type":"function","name":"apply_patch"},{"type":"web_search"}],"input":"x"}`,
			want:     routeRequest{Endpoint: "responses", ToolNames: []string{"apply_patch"}, Effort: "medium"},
		},
		{
			name:     "names capped and cut",
			endpoint: "/v1/messages",
			body:     `{"tools":[{"name":"` + strings.Repeat("n", 80) + `"},` + strings.Join(many, ",") + `],"messages":[]}`,
		},
	}
	for _, c := range cases {
		root, _ := jsonsplice.Root([]byte(c.body))
		got := requestFor(gateway.RouteAsk{Endpoint: c.endpoint, Body: []byte(c.body)}, root)
		if c.name == "names capped and cut" {
			if len(got.ToolNames) != 128 || got.ToolNames[0] != strings.Repeat("n", 64) || got.ToolNames[127] != "tool_126" {
				t.Errorf("%s: %d names, first %q, last %q", c.name, len(got.ToolNames), got.ToolNames[0], got.ToolNames[len(got.ToolNames)-1])
			}
			continue
		}
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
	labels := map[string]string{"x-claude-code-request-class": "main"}
	if got := requestFor(gateway.RouteAsk{Endpoint: "/v1/messages", Body: []byte(`{}`), Labels: labels, PerMessageOff: true}, jsonsplice.Span{Start: 0, End: 2}); got.Labels["x-claude-code-request-class"] != "main" || !got.PerMessageOff {
		t.Errorf("labels and latch = %+v", got)
	}
}

// cloudRecorder answers every ask with answer and keeps the bodies it got.
type cloudRecorder struct {
	mu     sync.Mutex
	bodies []map[string]any
	answer func(n int) string
}

func (c *cloudRecorder) server(t *testing.T) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		c.mu.Lock()
		c.bodies = append(c.bodies, body)
		n := len(c.bodies)
		c.mu.Unlock()
		_, _ = io.WriteString(w, c.answer(n))
	}))
	t.Cleanup(server.Close)
	return server
}

func askFor(session, parent, text string) gateway.RouteAsk {
	body := `{"model":"claude-opus-5-5","messages":[{"role":"user","content":` + fmt.Sprintf("%q", text) + `}]}`
	return gateway.RouteAsk{Provider: "anthropic", Endpoint: "/v1/messages", Model: "claude-opus-5-5", Agent: "claude", SessionID: session, ParentSessionID: parent, Body: []byte(body)}
}

// Cloud's state goes back per session; a child sends its parent's as
// parent_state; last goes as reported, its counts bounded.
func TestStateRoundTripsPerSessionAndChild(t *testing.T) {
	cloud := &cloudRecorder{answer: func(n int) string {
		return fmt.Sprintf(`{"model":"claude-opus-5-5","reason":"ranked","effort":"low","effort_mode":"message","state":"st-%d"}`, n)
	}}
	link := newLink(cloudHome(t, cloud.server(t).URL, true, `{"access_token":"`+token(time.Now().Add(time.Hour))+`"}`))
	if answer := link.Ask(t.Context(), askFor("s1", "", "first"))(); answer.Effort != "low" || answer.EffortMode != "message" || answer.Outcome != "kept" || answer.Model != "" {
		t.Fatalf("answer = %+v", answer)
	}
	second := askFor("s1", "", "second")
	second.Last = &gateway.RouteLast{Model: strings.Repeat("m", 200), Effort: "low", AgeS: -5, InputTokens: 52000, CacheReadTokens: 50000, CacheWriteTokens: 1200, Compacted: true}
	link.Ask(t.Context(), second)()
	link.Ask(t.Context(), askFor("s1#a1", "s1", "child task"))()
	link.Ask(t.Context(), askFor("s1#a1", "s1", "child second"))()
	link.Ask(t.Context(), askFor("s2", "", "other"))()
	got := cloud.bodies
	if _, ok := got[0]["state"]; ok || got[0]["last"] != nil {
		t.Errorf("a first ask carried state or last: %v", got[0])
	}
	if got[1]["state"] != "st-1" {
		t.Errorf("second ask state = %v, want st-1", got[1]["state"])
	}
	wantLast := map[string]any{"model": strings.Repeat("m", 128), "effort": "low", "age_s": float64(0), "input_tokens": float64(52000), "cache_read_tokens": float64(50000), "cache_write_tokens": float64(1200), "compacted": true}
	if fmt.Sprint(got[1]["last"]) != fmt.Sprint(wantLast) {
		t.Errorf("last = %v\nwant %v", got[1]["last"], wantLast)
	}
	if _, ok := got[2]["state"]; ok || got[2]["parent_state"] != "st-2" {
		t.Errorf("child first ask: state %v parent_state %v, want none and st-2", got[2]["state"], got[2]["parent_state"])
	}
	if got[3]["state"] != "st-3" || got[3]["parent_state"] != "st-2" {
		t.Errorf("child second ask: state %v parent_state %v", got[3]["state"], got[3]["parent_state"])
	}
	if _, ok := got[4]["state"]; ok {
		t.Errorf("another session got s1's state: %v", got[4]["state"])
	}
}

// An oversized or empty state is not kept; a new login forgets every state.
func TestStateBoundsAndLogin(t *testing.T) {
	state := ""
	cloud := &cloudRecorder{answer: func(int) string {
		return `{"model":"claude-opus-5-5","state":"` + state + `"}`
	}}
	url := cloud.server(t).URL
	home := cloudHome(t, url, true, `{"access_token":"`+token(time.Now().Add(time.Hour))+`"}`)
	link := newLink(home)
	state = strings.Repeat("x", stateMax+1)
	link.Ask(t.Context(), askFor("s1", "", "one"))()
	state = "kept"
	link.Ask(t.Context(), askFor("s1", "", "two"))()
	link.Ask(t.Context(), askFor("s1", "", "three"))()
	if _, ok := cloud.bodies[1]["state"]; ok || cloud.bodies[2]["state"] != "kept" {
		t.Fatalf("states sent: %v, %v", cloud.bodies[1]["state"], cloud.bodies[2]["state"])
	}
	// A new login rewrites the credentials with another session token.
	if err := os.WriteFile(filepath.Join(home, "credentials"), []byte(`{"access_token":"`+token(time.Now().Add(2*time.Hour))+`x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link.Ask(t.Context(), askFor("s1", "", "four"))()
	if _, ok := cloud.bodies[3]["state"]; ok {
		t.Errorf("a new login sent the old login's state: %v", cloud.bodies[3]["state"])
	}
}

// A compaction or side request is asked every time and carries no ask text;
// a turn's tool loop asks once.
func TestPerRequestAsksEveryTime(t *testing.T) {
	cloud := &cloudRecorder{answer: func(int) string { return `{"model":"claude-opus-5-5"}` }}
	link := newLink(cloudHome(t, cloud.server(t).URL, true, `{"access_token":"`+token(time.Now().Add(time.Hour))+`"}`))
	side := askFor("s1", "", "summarize the conversation")
	side.PerRequest = true
	side.Labels = map[string]string{"x-claude-code-compaction": "1"}
	link.Ask(t.Context(), side)()
	link.Ask(t.Context(), side)()
	link.Ask(t.Context(), askFor("s1", "", "fix it"))()
	link.Ask(t.Context(), askFor("s1", "", "fix it"))()
	if len(cloud.bodies) != 3 {
		t.Fatalf("cloud asked %d times, want two side requests and one turn", len(cloud.bodies))
	}
	for _, body := range cloud.bodies[:2] {
		request, _ := body["request"].(map[string]any)
		if _, ok := body["ask"]; ok || fmt.Sprint(request["labels"]) != "map[x-claude-code-compaction:1]" {
			t.Errorf("side request body = %v", body)
		}
	}
	if ask, _ := cloud.bodies[2]["ask"].(map[string]any); ask["text"] != "fix it" {
		t.Errorf("turn body = %v", cloud.bodies[2])
	}
}

// An effort or mode the runtime does not know how to splice is left out.
func TestAnswerEffortIsCheckedNotGuessed(t *testing.T) {
	for answer, want := range map[string][2]string{
		`{"model":"claude-sonnet-5-5","effort":"high","effort_mode":"top"}`:      {"high", "top"},
		`{"model":"claude-opus-5-5","effort":"max"}`:                             {"max", ""},
		`{"model":"claude-opus-5-5","effort":"High\"}","effort_mode":"message"}`: {"", ""},
		`{"model":"claude-opus-5-5","effort":"low","effort_mode":"sometimes"}`:   {"", ""},
	} {
		cloud := &cloudRecorder{answer: func(int) string { return answer }}
		link := newLink(cloudHome(t, cloud.server(t).URL, true, `{"access_token":"`+token(time.Now().Add(time.Hour))+`"}`))
		got := link.Ask(t.Context(), askFor("s1", "", "go"))()
		if got.Effort != want[0] || got.EffortMode != want[1] {
			t.Errorf("%s: effort %q mode %q", answer, got.Effort, got.EffortMode)
		}
	}
}

// The link's share of the per-request work on a 5 MB body (it runs while
// compression does, inside the 800 ms budget): parse, ask text, declared request.
func BenchmarkAskParts5MB(b *testing.B) {
	result := `{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"` + strings.Repeat("lorem ipsum dolor sit amet ", 400) + `"}]},{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"Bash","input":{}}]},`
	body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"go"},{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"Bash","input":{}}]},` + strings.Repeat(result, 5<<20/len(result)) + `{"role":"user","content":"next"}],"tools":[{"name":"Bash"}],"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`)
	ask := gateway.RouteAsk{Endpoint: "/v1/messages", Body: body}
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		root, _ := jsonsplice.Root(body)
		_ = askTextFor(ask.Endpoint, body, root)
		_ = requestFor(ask, root)
	}
}

// The ask's fields are route-ask-v1's: every field sent is in the schema and
// every required one is sent, for the body and its request and last objects.
func TestAskMatchesTheRouteAskSchema(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "packages", "shared", "contracts", "schemas", "route-ask-v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	type object struct {
		Required   []string          `json:"required"`
		Properties map[string]object `json:"properties"`
	}
	var schema object
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	full := routeAsk{
		Models: pools["anthropic"], Signals: signals{Agent: "claude"}, Ask: &askText{Text: "go", PrevText: "p", ReplyTail: "r", Turn: 1},
		Request: routeRequest{Endpoint: "messages", Labels: map[string]string{"x-claude-code-agent-id": "a"}, ToolNames: []string{"Read"}},
		Last:    &gateway.RouteLast{Model: "m"}, State: "s", ParentState: "p",
	}
	var body map[string]any
	_ = json.Unmarshal(askBody(full), &body)
	check := func(name string, got map[string]any, spec object) {
		for field := range got {
			if _, ok := spec.Properties[field]; !ok {
				t.Errorf("%s: %q is not a route-ask-v1 field", name, field)
			}
		}
		for _, field := range spec.Required {
			if _, ok := got[field]; !ok {
				t.Errorf("%s: required %q missing", name, field)
			}
		}
	}
	check("body", body, schema)
	for _, part := range []string{"ask", "request", "last"} {
		got, _ := body[part].(map[string]any)
		check(part, got, schema.Properties[part])
	}
}
