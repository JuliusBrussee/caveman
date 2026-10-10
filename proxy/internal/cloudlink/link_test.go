package cloudlink

import (
	"cmp"
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
	"unicode/utf8"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
	"github.com/JuliusBrussee/caveman/proxy/providers/jsonsplice"
)

// promptText goes out in the route ask and never in an event.
const promptText = "PROMPT-TEXT-ONLY-THE-ROUTE-ASK-CARRIES"

// cloudHome writes the CLI state a signed-in user with routing on leaves.
func cloudHome(t *testing.T, cloud string, routing bool, credentials string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home) // the old ~/.caveman-cloud file is read too: never the developer's
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
	body := fmt.Sprintf(`{"model":%q,"tools":[{"name":"bash"}],"messages":[{"role":"user","content":%q},{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"bash","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","is_error":true,"content":"boom"},{"type":"image","source":{}}]}]}`, model, promptText)
	return gateway.RouteAsk{Provider: "anthropic", Endpoint: "/v1/messages", Model: model, Agent: "claude", SessionID: "s1", ToolsCount: 1, InputBytes: len(body), Body: []byte(body)}
}

func TestAskSendsModelsSignalsAndText(t *testing.T) {
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
	var body map[string]any
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatal(err)
	}
	// A first turn: the tool-result message after it is no human turn, and the
	// empty optional fields are left out (no labels, no last, no state yet).
	want := map[string]any{
		"models":  []any{"claude-opus-5-5", "claude-sonnet-5-5"},
		"signals": map[string]any{"agent": "claude", "context_tokens": float64(ask.InputBytes / 4), "tools_declared": float64(1), "tool_errors": float64(1), "images": true},
		"ask":     map[string]any{"text": promptText},
		"request": map[string]any{"endpoint": "messages", "tool_names": []any{"bash"}, "effort": "", "thinking": "", "per_message_off": false},
	}
	if fmt.Sprint(body) != fmt.Sprint(want) {
		t.Fatalf("ask body = %v\nwant %v", body, want)
	}
	// The gateway authenticates the project key only, never the login token.
	if auth != "Bearer cave_project_key" {
		t.Errorf("authorization = %q, want the project key", auth)
	}
	// A login that issued no key still asks with its session token.
	keyless := newLink(cloudHome(t, cloud.URL, true, `{"access_token":"`+token(time.Now().Add(time.Hour))+`"}`))
	keyless.Ask(t.Context(), ask)()
	if !strings.HasPrefix(auth, "Bearer ") || auth == "Bearer cave_project_key" {
		t.Errorf("authorization = %q, want the session token without a key", auth)
	}
}

func TestAskTextFromEachShape(t *testing.T) {
	cases := []struct {
		name, endpoint, body string
		want                 askText
	}{
		{
			name:     "claude code messages",
			endpoint: "/v1/messages",
			body: `{"model":"claude-opus-5-5","system":[{"type":"text","text":"You are Claude Code"}],"messages":[
				{"role":"user","content":[{"type":"text","text":"<system-reminder>ctx</system-reminder>"},{"type":"text","text":"fix the login bug"}]},
				{"role":"assistant","content":[{"type":"thinking","thinking":"hmm","signature":"s"},{"type":"text","text":"Looking."},{"type":"tool_use","id":"t1","name":"Read","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"package a"}]},
				{"role":"assistant","content":[{"type":"text","text":"Fixed: the check used < not <=."}]},
				{"role":"user","content":[{"type":"text","text":"<system-reminder>note</system-reminder>"},{"type":"text","text":"now add a test"}]},
				{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Bash","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"ok"}]}]}`,
			want: askText{
				Text:      "<system-reminder>note</system-reminder>\nnow add a test",
				PrevText:  "<system-reminder>ctx</system-reminder>\nfix the login bug",
				ReplyTail: "Fixed: the check used < not <=.",
				Turn:      1,
			},
		},
		{
			name:     "openai chat",
			endpoint: "/v1/chat/completions",
			body: `{"model":"gpt-6-sol","messages":[
				{"role":"system","content":"be brief"},
				{"role":"user","content":"first"},
				{"role":"assistant","content":"answer one"},
				{"role":"user","content":[{"type":"text","text":"second"},{"type":"image_url","image_url":{"url":"data:"}},{"type":"text","text":"more"}]},
				{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
				{"role":"tool","tool_call_id":"c1","content":"result"}]}`,
			want: askText{Text: "second\nmore", PrevText: "first", ReplyTail: "answer one", Turn: 1},
		},
		{
			name:     "openai responses",
			endpoint: "/v1/responses",
			body: `{"model":"gpt-6-sol","input":[
				{"role":"developer","content":"rules"},
				{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]},
				{"type":"reasoning","summary":[]},
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer one"}]},
				{"type":"message","role":"user","content":[{"type":"input_text","text":"second"},{"type":"input_text","text":"more"}]},
				{"type":"function_call","call_id":"c1","name":"shell","arguments":"{}"},
				{"type":"function_call_output","call_id":"c1","output":"ok"}]}`,
			want: askText{Text: "second\nmore", PrevText: "first", ReplyTail: "answer one", Turn: 1},
		},
		{
			name:     "responses input string",
			endpoint: "/v1/responses",
			body:     `{"model":"gpt-6-sol","input":"just this"}`,
			want:     askText{Text: "just this"},
		},
		{
			// An interrupt note rides with the interrupted call's tool result,
			// so it belongs to the tool loop: the latest human turn stays B.
			// Blank messages count as no human turn either.
			name:     "interrupted tool call",
			endpoint: "/v1/messages",
			body: `{"messages":[
				{"role":"user","content":"A"},
				{"role":"assistant","content":[{"type":"text","text":"R"}]},
				{"role":"user","content":[{"type":"text","text":"  "}]},
				{"role":"user","content":"B"},
				{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"Bash","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"x"},{"type":"text","text":"[Request interrupted by user for tool use]"}]}]}`,
			want: askText{Text: "B", PrevText: "A", ReplyTail: "R", Turn: 1},
		},
		{
			// Accepted: a redirect typed after an interrupt rides with the tool
			// result, so it is not sent; the next human turn decides again.
			name:     "interrupt then type",
			endpoint: "/v1/messages",
			body: `{"messages":[
				{"role":"user","content":"fix the bug"},
				{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"Bash","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"rejected"},{"type":"text","text":"[Request interrupted by user for tool use]"},{"type":"text","text":"do it in Go instead"}]}]}`,
			want: askText{Text: "fix the bug"},
		},
		{
			// codex-rs's opening order (no captured Codex body in this repo):
			// permissions, AGENTS.md, environment, then the prompt. One group,
			// one human turn: the prompt.
			name:     "codex opening input",
			endpoint: "/v1/responses",
			body: `{"model":"gpt-6-sol","input":[
				{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>workspace-write</permissions instructions>"}]},
				{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for /repo\n\n<INSTRUCTIONS>\nrun make test\n</INSTRUCTIONS>"}]},
				{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/repo</cwd>\n</environment_context>"}]},
				{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the flaky test"}]}]}`,
			want: askText{Text: "fix the flaky test"},
		},
		{
			// Only the group's last user message counts: an image-only prompt
			// leaves the ask empty rather than sending the environment block.
			name:     "codex image-only prompt",
			endpoint: "/v1/responses",
			body: `{"model":"gpt-6-sol","input":[
				{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>workspace-write</permissions instructions>"}]},
				{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for /repo"}]},
				{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/repo</cwd>\n</environment_context>"}]},
				{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:"}]}]}`,
		},
		{
			name:     "image-only earlier turn",
			endpoint: "/v1/messages",
			body: `{"messages":[
				{"role":"user","content":[{"type":"image","source":{}}]},
				{"role":"assistant","content":"A cat."},
				{"role":"user","content":"crop it"}]}`,
			want: askText{Text: "crop it", ReplyTail: "A cat.", Turn: 1},
		},
		{
			name:     "codex second turn",
			endpoint: "/v1/responses",
			body: `{"model":"gpt-6-sol","input":[
				{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>workspace-write</permissions instructions>"}]},
				{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for /repo"}]},
				{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/repo</cwd>\n</environment_context>"}]},
				{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the flaky test"}]},
				{"type":"reasoning","summary":[]},
				{"type":"function_call","call_id":"c1","name":"shell","arguments":"{}"},
				{"type":"function_call_output","call_id":"c1","output":"ok"},
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Fixed: the test raced the clock."}]},
				{"type":"message","role":"user","content":[{"type":"input_text","text":"now run the suite"}]}]}`,
			want: askText{Text: "now run the suite", PrevText: "fix the flaky test", ReplyTail: "Fixed: the test raced the clock.", Turn: 1},
		},
		{
			name:     "legacy chat function result",
			endpoint: "/v1/chat/completions",
			body: `{"messages":[
				{"role":"user","content":"ask"},
				{"role":"assistant","content":null,"function_call":{"name":"f","arguments":"{}"}},
				{"role":"function","name":"f","content":"result"},
				{"role":"user","content":"reminder"}]}`,
			want: askText{Text: "ask"},
		},
		{
			name:     "responses mcp approval response",
			endpoint: "/v1/responses",
			body: `{"input":[
				{"role":"user","content":"ask"},
				{"type":"mcp_approval_request","id":"a1","name":"t","arguments":"{}","server_label":"s"},
				{"type":"mcp_approval_response","approval_request_id":"a1","approve":true},
				{"role":"user","content":"reminder"}]}`,
			want: askText{Text: "ask"},
		},
		{
			name:     "responses tool search output",
			endpoint: "/v1/responses",
			body: `{"input":[
				{"role":"user","content":"ask"},
				{"type":"tool_search_call","id":"s1"},
				{"type":"tool_search_output","call_id":"s1"},
				{"role":"user","content":"reminder"}]}`,
			want: askText{Text: "ask"},
		},
		{
			name:     "responses user message next to a tool output",
			endpoint: "/v1/responses",
			body: `{"input":[
				{"role":"user","content":"ask"},
				{"type":"function_call","call_id":"c1","name":"shell","arguments":"{}"},
				{"type":"function_call_output","call_id":"c1","output":"ok"},
				{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>x</environment_context>"}]}]}`,
			want: askText{Text: "ask"},
		},
		{
			name:     "chat user message after tool results",
			endpoint: "/v1/chat/completions",
			body: `{"messages":[
				{"role":"user","content":"ask"},
				{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
				{"role":"tool","tool_call_id":"c1","content":"result"},
				{"role":"user","content":"reminder"}]}`,
			want: askText{Text: "ask"},
		},
		{
			name:     "tool results only",
			endpoint: "/v1/messages",
			body:     `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"x"}]}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, ok := jsonsplice.Root([]byte(tc.body))
			if !ok {
				t.Fatal("bad fixture")
			}
			if got := askTextFor(tc.endpoint, []byte(tc.body), root); got != tc.want {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// Claude Code puts its system-reminders next to tool results: that message is
// part of the tool loop, so the loop keeps the ask's text and its decision.
func TestToolLoopKeepsTheAsk(t *testing.T) {
	var hits atomic.Int32
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"model":"claude-sonnet-5-5","reason":"ranked"}`)
	}))
	defer cloud.Close()
	link := newLink(cloudHome(t, cloud.URL, true, `{"access_token":"`+token(time.Now().Add(time.Hour))+`"}`))
	ask := func(messages string) gateway.RouteAsk {
		body := `{"model":"claude-opus-5-5","messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>ctx</system-reminder>"},{"type":"text","text":"fix the login bug"}]}` + messages + `]}`
		return gateway.RouteAsk{Provider: "anthropic", Endpoint: "/v1/messages", Model: "claude-opus-5-5", Agent: "claude", SessionID: "s1", Body: []byte(body)}
	}
	first := ask("")
	loop := ask(`,{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"package a"},{"type":"text","text":"<system-reminder>The file changed.</system-reminder>"}]}`)
	root, _ := jsonsplice.Root(loop.Body)
	if got := askTextFor(loop.Endpoint, loop.Body, root); got.Text != "<system-reminder>ctx</system-reminder>\nfix the login bug" || got.Turn != 0 {
		t.Fatalf("ask text in the loop = %+v", got)
	}
	for _, request := range []gateway.RouteAsk{first, loop} {
		if answer := link.Ask(t.Context(), request)(); answer.Model != "claude-sonnet-5-5" {
			t.Fatalf("answer = %+v", answer)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("cloud asked %d times for one ask and its tool loop, want once", hits.Load())
	}
}

// Each field keeps its end within its bound, cut on a rune boundary: Claude
// Code puts its reminders first and the person's words last.
func TestAskTextKeepsTheEnds(t *testing.T) {
	long := "a" + strings.Repeat("é", 100_000) // a cut at an even offset lands mid-rune
	body, _ := json.Marshal(map[string]any{"messages": []map[string]any{
		{"role": "user", "content": long + "PRV"},
		{"role": "assistant", "content": long + "END"},
		{"role": "user", "content": long + "ASK"},
	}})
	root, _ := jsonsplice.Root(body)
	got := askTextFor("/v1/messages", body, root)
	for name, cut := range map[string]struct {
		text, of string
		max      int
	}{"text": {got.Text, long + "ASK", askTextMax}, "prev_text": {got.PrevText, long + "PRV", askSideMax}, "reply_tail": {got.ReplyTail, long + "END", askSideMax}} {
		if !strings.HasSuffix(cut.of, cut.text) || !utf8.ValidString(cut.text) || len(cut.text) > cut.max || len(cut.text) < cut.max-3 {
			t.Errorf("%s: %d bytes, valid %v, kept the end %v", name, len(cut.text), utf8.ValidString(cut.text), strings.HasSuffix(cut.of, cut.text))
		}
	}
	// A 21 KiB reminder before the first ask: prev_text still ends with it.
	reminder := "<system-reminder>" + strings.Repeat("memory line\n", 1800) + "</system-reminder>"
	body, _ = json.Marshal(map[string]any{"messages": []map[string]any{
		{"role": "user", "content": []map[string]any{{"type": "text", "text": reminder}, {"type": "text", "text": "FIRST-ASK"}}},
		{"role": "assistant", "content": "done"},
		{"role": "user", "content": "now?"},
	}})
	root, _ = jsonsplice.Root(body)
	if got := askTextFor("/v1/messages", body, root); !strings.HasSuffix(got.PrevText, "\nFIRST-ASK") || len(got.PrevText) > askSideMax {
		t.Errorf("prev_text ends %q (%d bytes)", got.PrevText[max(len(got.PrevText)-20, 0):], len(got.PrevText))
	}
}

// Escaping can make the JSON six times the text; the body still fits 256 KiB
// and text keeps the longest end that fits. A text whose kept end is blank is
// not sent: Cloud refuses it.
func TestAskBodyFitsTheContract(t *testing.T) {
	side := strings.Repeat("\x02", askSideMax) // "\u0002" in JSON
	text := strings.Repeat("\x01", askTextMax)
	raw := askBody(routeAsk{Models: pools["anthropic"], Signals: signals{Agent: "claude"}, Ask: &askText{Text: text, PrevText: side, ReplyTail: side, Turn: 3}})
	var got routeAsk
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(raw) > askBodyMax || len(raw) <= askBodyMax-6 {
		t.Errorf("body is %d bytes, want the most that fits %d", len(raw), askBodyMax)
	}
	if got.Ask.Text == "" || !strings.HasSuffix(text, got.Ask.Text) || got.Ask.PrevText != side || got.Ask.ReplyTail != side || got.Ask.Turn != 3 {
		t.Errorf("ask = text %d bytes, prev %d, reply %d, turn %d", len(got.Ask.Text), len(got.Ask.PrevText), len(got.Ask.ReplyTail), got.Ask.Turn)
	}
	blank := "ask" + strings.Repeat("\n", askTextMax-3)
	if raw := askBody(routeAsk{Models: pools["anthropic"], Signals: signals{Agent: "claude"}, Ask: &askText{Text: blank, PrevText: side, ReplyTail: side}}); raw != nil {
		t.Errorf("a blank kept end was sent: %d bytes", len(raw))
	}
	// An ordinary ask goes whole, its <tags> and && unescaped.
	small := routeAsk{Models: pools["openai"], Signals: signals{Agent: "codex"}, Ask: &askText{Text: "<system-reminder>a && b</system-reminder>"}}
	if raw := string(askBody(small)); !strings.Contains(raw, `"ask":{"text":"<system-reminder>a && b</system-reminder>"},`) {
		t.Errorf("askBody = %s", raw)
	}
}

// A latest turn without text keeps the model and asks nothing.
func TestImageOnlyAskKeepsTheModel(t *testing.T) {
	var hits atomic.Int32
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer cloud.Close()
	link := newLink(cloudHome(t, cloud.URL, true, `{"access_token":"`+token(time.Now().Add(time.Hour))+`"}`))
	body := `{"model":"gpt-6-sol","input":[{"role":"user","content":"<environment_context>x</environment_context>"},{"role":"user","content":[{"type":"input_image","image_url":"data:"}]}]}`
	answer := link.Ask(t.Context(), gateway.RouteAsk{Provider: "openai", Endpoint: "/v1/responses", Model: "gpt-6-sol", SessionID: "s1", Body: []byte(body)})()
	if answer.Model != "" || answer.Outcome != "kept" || answer.Reason != "no_human_text" || hits.Load() != 0 {
		t.Fatalf("answer = %+v, cloud hits %d", answer, hits.Load())
	}
	if got := tail("abc", -1); got != "" {
		t.Errorf("tail with a negative bound = %q", got)
	}
}

// A repeated short ask in a later turn is a new ask, not the old decision.
func TestRepeatedAskGetsItsOwnDecision(t *testing.T) {
	var hits atomic.Int32
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"model":"claude-sonnet-5-5","reason":"ranked"}`)
	}))
	defer cloud.Close()
	link := newLink(cloudHome(t, cloud.URL, true, `{"access_token":"`+token(time.Now().Add(time.Hour))+`"}`))
	for _, messages := range []string{
		`{"role":"user","content":"yes"}`,
		`{"role":"user","content":"yes"},{"role":"assistant","content":"Shall I also add a test?"},{"role":"user","content":"yes"}`,
	} {
		body := `{"model":"claude-opus-5-5","messages":[` + messages + `]}`
		link.Ask(t.Context(), gateway.RouteAsk{Provider: "anthropic", Endpoint: "/v1/messages", Model: "claude-opus-5-5", SessionID: "s1", Body: []byte(body)})()
	}
	if hits.Load() != 2 {
		t.Errorf("cloud asked %d times for two asks, want twice", hits.Load())
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
			if body.SchemaVersion != 1 || strings.Contains(string(raw), promptText) {
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
	release := make(chan struct{})
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runtime/events" {
			posts.Add(1)
			<-release // the send hangs until the test lets it fail
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, `{"data":{}}`)
	}))
	defer cloud.Close()
	link := newLink(cloudHome(t, cloud.URL, true, `{"access_token":"`+token(time.Now().Add(time.Hour))+`"}`))
	link.events.every = time.Hour
	// The request path must finish while a send is stuck: a full buffer drops.
	observed := make(chan struct{})
	go func() {
		for i := range 3 * eventQueueMax {
			link.Observe(record(i))
		}
		close(observed)
	}()
	select {
	case <-observed:
	case <-time.After(30 * time.Second):
		t.Fatal("Observe waited on a send")
	}
	close(release)
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
	// An older CLI on PATH writes `telemetry off` to ~/.caveman-cloud only.
	oldCLI := cloudHome(t, cloud.URL, true, signedIn)
	_ = os.MkdirAll(filepath.Join(oldCLI, ".caveman-cloud"), 0o700)
	_ = os.WriteFile(filepath.Join(oldCLI, ".caveman-cloud", "config.json"), []byte(`{"telemetry":{"enabled":false}}`), 0o600)
	if got := send(oldCLI); len(got) != 0 {
		t.Fatalf("the old file's telemetry off still sent %v", got)
	}
	t.Setenv("DO_NOT_TRACK", "1")
	if got := send(cloudHome(t, cloud.URL, true, signedIn)); len(got) != 0 {
		t.Fatalf("DO_NOT_TRACK still sent %v", got)
	}
}

// The old CLI wrote that opt-out under Node's os.homedir(). PowerShell and cmd
// leave HOME unset, and the user home there (USERPROFILE) is not the parent of
// a CAVEMAN_HOME set elsewhere.
func TestLegacyOptOutPathFollowsTheUserHome(t *testing.T) {
	cavemanHome := filepath.Join(t.TempDir(), "elsewhere", ".caveman")
	profile := t.TempDir()
	previous := userHomeDir
	defer func() { userHomeDir = previous }()
	userHomeDir = func() (string, error) { return profile, nil }
	for _, tt := range []struct{ home, want string }{
		{"", profile},
		{t.TempDir(), ""},
	} {
		t.Setenv("HOME", tt.home)
		want := filepath.Join(cmp.Or(tt.want, tt.home), ".caveman-cloud", "config.json")
		if _, legacy, _ := New(cavemanHome, nil).cloudPaths(); legacy != want {
			t.Fatalf("HOME=%q: legacy opt-out read from %s, want %s", tt.home, legacy, want)
		}
	}
	t.Setenv("HOME", "")
	userHomeDir = func() (string, error) { return "", fmt.Errorf("no home") }
	if _, legacy, _ := New(cavemanHome, nil).cloudPaths(); legacy != filepath.Join(filepath.Dir(cavemanHome), ".caveman-cloud", "config.json") {
		t.Fatalf("no user home: legacy opt-out read from %s", legacy)
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

// keychainHome is a signed-in CLI state whose secret lives in the keychain.
func keychainHome(t *testing.T, cloud string, extra map[string]any) string {
	t.Helper()
	home := t.TempDir()
	writeKeychainConfig(t, home, cloud, extra)
	return home
}

func writeKeychainConfig(t *testing.T, home, cloud string, extra map[string]any) {
	t.Helper()
	doc := map[string]any{"baseURL": cloud, "gatewayUrl": cloud, "tokenStore": "keychain", "modules": map[string]any{"routing": true}}
	for key, value := range extra {
		doc[key] = value
	}
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(home, "cloud.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitForAccess(t *testing.T, link *Link, want string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); link.settings().access != want; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("credential = %q, want %q", link.settings().access, want)
		}
	}
}

// Ordinary CLI commands rewrite cloud.json (telemetry watermarks, token
// refreshes). With the same secret that changes nothing: the credential never
// blanks and the decision a turn is using stays.
func TestCloudJSONRewriteWithTheSameSecretKeepsTheTurn(t *testing.T) {
	var hits atomic.Int32
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/route" {
			hits.Add(1)
		}
		_, _ = io.WriteString(w, `{"model":"claude-sonnet-5-5","reason":"ranked"}`)
	}))
	defer cloud.Close()
	secretA := token(time.Now().Add(time.Hour))
	home := keychainHome(t, cloud.URL, nil)
	link := New(home, nil)
	link.keychain = func() string { return `{"access_token":"` + secretA + `"}` }
	waitForAccess(t, link, secretA)
	if answer := link.Ask(t.Context(), messagesAsk("claude-opus-5-5"))(); answer.Model != "claude-sonnet-5-5" {
		t.Fatalf("first turn = %+v", answer)
	}
	for i := range 5 {
		time.Sleep(5 * time.Millisecond)
		writeKeychainConfig(t, home, cloud.URL, map[string]any{"telemetryTokens": i})
		if got := link.settings().access; got != secretA {
			t.Fatalf("credential blanked to %q after a cloud.json rewrite", got)
		}
		if answer := link.Ask(t.Context(), messagesAsk("claude-opus-5-5"))(); answer.Model != "claude-sonnet-5-5" {
			t.Fatalf("turn %d = %+v, want the same routed model", i, answer)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("cloud asked %d times for one ask, want once (no cache reset)", hits.Load())
	}
}

// A real re-login swaps the secret; the new login asks with its own token and
// no 401 pause from the old one survives.
func TestReLoginSwapsTheKeychainSecretWithoutAPause(t *testing.T) {
	secretA, secretB := token(time.Now().Add(time.Hour)), token(time.Now().Add(2*time.Hour))
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("authorization") != "Bearer "+secretB {
			w.WriteHeader(http.StatusUnauthorized) // the old session is revoked by the re-login
			return
		}
		_, _ = io.WriteString(w, `{"model":"claude-sonnet-5-5","reason":"ranked"}`)
	}))
	defer cloud.Close()
	var secret atomic.Value
	secret.Store(`{"access_token":"` + secretA + `"}`)
	home := keychainHome(t, cloud.URL, map[string]any{"organizationId": "org"})
	link := New(home, nil)
	link.keychain = func() string { return secret.Load().(string) }
	waitForAccess(t, link, secretA)
	if answer := link.Ask(t.Context(), messagesAsk("claude-opus-5-5"))(); answer.Reason != "cloud_401" {
		t.Fatalf("old login = %+v, want a 401", answer)
	}
	secret.Store(`{"access_token":"` + secretB + `"}`)
	time.Sleep(5 * time.Millisecond)
	writeKeychainConfig(t, home, cloud.URL, map[string]any{"organizationId": "org", "relogin": true})
	waitForAccess(t, link, secretB)
	next := messagesAsk("claude-opus-5-5")
	next.SessionID = "after-login"
	if answer := link.Ask(t.Context(), next)(); answer.Model != "claude-sonnet-5-5" {
		t.Fatalf("after re-login = %+v, want routed with the new token and no pause", answer)
	}
}

// A logout that stopped after revoking the session but before its final save
// leaves logoutPendingLocalCleanup in cloud.json: the proxy is signed out.
func TestLogoutPendingCleanupIsSignedOut(t *testing.T) {
	var hits atomic.Int32
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"model":"claude-sonnet-5-5","reason":"ranked"}`)
	}))
	defer cloud.Close()
	secret := token(time.Now().Add(time.Hour))
	home := keychainHome(t, cloud.URL, nil)
	link := New(home, nil)
	link.keychain = func() string { return `{"access_token":"` + secret + `"}` }
	link.events.every = time.Hour
	waitForAccess(t, link, secret)
	time.Sleep(5 * time.Millisecond)
	writeKeychainConfig(t, home, cloud.URL, map[string]any{"logoutPendingLocalCleanup": true})
	if link.settings().signedIn() {
		t.Fatal("signed in while a logout is pending")
	}
	if answer := link.Ask(t.Context(), messagesAsk("claude-opus-5-5"))(); answer.Model != "" {
		t.Fatalf("asked with a revoked token: %+v", answer)
	}
	link.Observe(record(1))
	link.flush()
	if hits.Load() != 0 {
		t.Fatalf("%d calls to the cloud while a logout is pending", hits.Load())
	}
}

// A turn that starts during a pause keeps the paused answer for its whole
// tool loop: the pause ending halfway never switches the model.
func TestTurnStartedInAPauseKeepsItsAnswer(t *testing.T) {
	var hits atomic.Int32
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, `{"model":"claude-sonnet-5-5","reason":"ranked"}`)
	}))
	defer cloud.Close()
	link := newLink(cloudHome(t, cloud.URL, true, `{"access_token":"`+token(time.Now().Add(time.Hour))+`"}`))
	var mu sync.Mutex
	now := time.Now()
	link.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	link.Ask(t.Context(), messagesAsk("claude-opus-5-5"))() // a 500: asks pause
	turn := messagesAsk("claude-opus-5-5")
	turn.SessionID = "s2"
	if first := link.Ask(t.Context(), turn)(); first.Model != "" || first.Reason != "cloud_500" {
		t.Fatalf("during the pause: %+v", first)
	}
	mu.Lock()
	now = now.Add(2 * failurePause)
	mu.Unlock()
	if again := link.Ask(t.Context(), turn)(); again.Model != "" || again.Reason != "cloud_500" || hits.Load() != 1 {
		t.Fatalf("the same turn after the pause: %+v, cloud asked %d times", again, hits.Load())
	}
	// The next ask is a new one.
	next := messagesAsk("claude-opus-5-5")
	next.SessionID = "s3"
	if answer := link.Ask(t.Context(), next)(); answer.Model != "claude-sonnet-5-5" {
		t.Fatalf("a new ask after the pause: %+v", answer)
	}
}
