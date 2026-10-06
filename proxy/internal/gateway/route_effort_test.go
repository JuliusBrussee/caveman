package gateway

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

func TestRouteRunReadsLabelsAndSessionKeys(t *testing.T) {
	h := http.Header{}
	h.Set("X-Claude-Code-Session-Id", "sess-1")
	h.Set("X-Claude-Code-Agent-Id", "agent-7")
	h.Set("X-Claude-Code-Parent-Agent-Id", "agent-3")
	h.Set("X-Claude-Code-Request-Class", "subagent")
	h.Set("X-Codex-Turn-Metadata", strings.Repeat("é", 2000)) // 4000 bytes: within its 16 KiB
	h.Set("X-Openai-Subagent", strings.Repeat("s", 257))      // over 256: left out, never cut
	h.Set("X-Unlisted", "never sent")
	run := newRouteRun(h, "", "/v1/messages", nil)
	if run.key != "sess-1#agent-7" || run.parent != "sess-1#agent-3" || run.perRequest || run.compacted {
		t.Fatalf("run = %+v", run)
	}
	if len(run.labels) != 4 || run.labels["x-claude-code-request-class"] != "subagent" || run.labels["x-unlisted"] != "" || run.labels["x-openai-subagent"] != "" {
		t.Fatalf("labels = %v", run.labels)
	}
	if got := run.labels["x-codex-turn-metadata"]; got != strings.Repeat("é", 2000) {
		t.Errorf("turn metadata is %d bytes, want it whole", len(got))
	}
	h.Set("X-Codex-Turn-Metadata", strings.Repeat("x", 16<<10+1))
	if got := newRouteRun(h, "", "/v1/messages", nil).labels["x-codex-turn-metadata"]; got != "" {
		t.Errorf("turn metadata over 16 KiB went (%d bytes)", len(got))
	}
	// A Codex thread is its own session; a child thread hangs off its parent's.
	codex := http.Header{}
	codex.Set("Session-Id", "codex-session")
	codex.Set("Thread-Id", "thread-2")
	codex.Set("X-Codex-Parent-Thread-Id", "thread-1")
	if run := newRouteRun(codex, "", "/v1/responses", nil); run.key != "thread-2" || run.parent != "thread-1" || run.labels["thread-id"] != "thread-2" || run.labels["x-codex-parent-thread-id"] != "thread-1" {
		t.Errorf("codex child thread: %+v", run)
	}
	// The caller's x-cave-session wins; a child without a parent agent hangs off the session.
	h.Del("X-Claude-Code-Parent-Agent-Id")
	if run := newRouteRun(h, "cave-s", "/v1/messages", nil); run.key != "cave-s#agent-7" || run.parent != "cave-s" {
		t.Errorf("run = %+v", run)
	}
	for _, labels := range []map[string]string{
		{"X-Claude-Code-Compaction": "1"},
		{"X-Claude-Code-Request-Class": "compaction"},
		{"X-Claude-Code-Request-Class": "auxiliary"},
	} {
		h := http.Header{}
		for name, value := range labels {
			h.Set(name, value)
		}
		if run := newRouteRun(h, "s", "/v1/messages", nil); !run.perRequest {
			t.Errorf("%v is answered per request", labels)
		}
	}
	h = http.Header{}
	h.Set("X-Claude-Code-Context-Compacted", "true")
	if run := newRouteRun(h, "s", "/v1/messages", nil); run.perRequest || !run.compacted || run.key != "s" {
		t.Errorf("after a compaction: %+v", run)
	}
	if run := newRouteRun(http.Header{}, "", "/v1/messages", nil); run.key != "" || len(run.labels) != 0 {
		t.Errorf("no session: %+v", run)
	}
}

func TestEffortForResponsesChatAndTopLevel(t *testing.T) {
	s := &Server{}
	cases := []struct{ endpoint, body, mode, want string }{
		{"/v1/responses", `{"model":"gpt-6-sol","input":"x"}`, "", `{"model":"gpt-6-sol","input":"x","reasoning":{"effort":"low"}}`},
		{"/v1/responses", `{"reasoning":{"effort":"high","summary":"auto"},"input":"x"}`, "", `{"reasoning":{"effort":"low","summary":"auto"},"input":"x"}`},
		{"/v1/responses", `{"reasoning":null,"input":"x"}`, "", `{"reasoning":{"effort":"low"},"input":"x"}`},
		{"/v1/chat/completions", `{"messages":[]}`, "", `{"messages":[],"reasoning_effort":"low"}`},
		{"/v1/chat/completions", `{"reasoning_effort":"high","messages":[]}`, "", `{"reasoning_effort":"low","messages":[]}`},
		{"/v1/messages", `{"messages":[{"role":"user","content":"x"}]}`, "top", `{"messages":[{"role":"user","content":"x"}],"output_config":{"effort":"low"}}`},
		{"/v1/messages", `{"output_config":{"effort":"high","format":null},"messages":[]}`, "top", `{"output_config":{"effort":"low","format":null},"messages":[]}`},
		// No mode on Anthropic is the top-level field too.
		{"/v1/messages", `{"output_config":{},"messages":[]}`, "", `{"output_config":{"effort":"low"},"messages":[]}`},
	}
	for _, c := range cases {
		provider := "openai"
		if c.endpoint == "/v1/messages" {
			provider = "anthropic"
		}
		got := s.applyEffort(newRouteRun(http.Header{}, "", "/v1/messages", nil), provider, c.endpoint, "m", []byte(c.body), RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: c.mode})
		if string(got) != c.want {
			t.Errorf("%s %s:\n got %s\nwant %s", c.endpoint, c.body, got, c.want)
		}
	}
	// No effort, or the route stage off: the body is left as sent.
	body := `{"reasoning_effort":"high","messages":[]}`
	for _, answer := range []RouteAnswer{{Outcome: "kept"}, {Outcome: "off", Effort: "low"}, {Outcome: "degraded"}} {
		if got := s.applyEffort(newRouteRun(http.Header{}, "", "/v1/messages", nil), "openai", "/v1/chat/completions", "m", []byte(body), answer); string(got) != body {
			t.Errorf("%+v changed the body: %s", answer, got)
		}
	}
}

// upstreamLog records what the stub provider was sent.
type upstreamLog struct {
	mu      sync.Mutex
	bodies  [][]byte
	headers []http.Header
}

func (u *upstreamLog) last() ([]byte, http.Header) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.bodies[len(u.bodies)-1], u.headers[len(u.headers)-1]
}

// effortServer is an Anthropic proxy whose stub provider answers with respond.
func effortServer(t *testing.T, cloud CloudLink, respond func(body []byte) (int, string)) (*Server, *upstreamLog) {
	t.Helper()
	log := &upstreamLog{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		log.mu.Lock()
		log.bodies, log.headers = append(log.bodies, raw), append(log.headers, r.Header.Clone())
		log.mu.Unlock()
		status, answer := http.StatusOK, `{"id":"m","type":"message","model":"claude-opus-5-5-20261001","content":[],"usage":{"input_tokens":100,"cache_read_input_tokens":50000,"cache_creation_input_tokens":1200,"output_tokens":2}}`
		if respond != nil {
			if code, text := respond(raw); code != 0 {
				status, answer = code, text
			}
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(upstream.Close)
	return New(Config{
		Adapters:   []providers.Adapter{anthropic.New("https://api.anthropic.com")},
		Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		Creds:      stubCreds{key: "sk-byok"},
		Sink:       &captureSink{},
		HTTPClient: &http.Client{Transport: toStub(upstream.URL)},
		Cloud:      cloud,
	}), log
}

func post(t *testing.T, srv *Server, body string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("x-api-key", "sk-ant-api-key")
	req.Header.Set("x-cave-agent", "claude")
	req.Header.Set("x-claude-code-session-id", "sess-1")
	for name, value := range header {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// convo builds an Anthropic body: output_config first, then the messages.
func convo(effort string, messages ...string) string {
	return `{"model":"claude-opus-5-5","max_tokens":5,"output_config":{"effort":"` + effort + `"},"messages":[` + strings.Join(messages, ",") + `]}`
}

const (
	uA   = `{"role":"user","content":"plan the migration"}`
	aB   = `{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"sig-b"},{"type":"text","text":"Three steps."}]}`
	uC   = `{"role":"user","content":"now do step one"}`
	aD   = `{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{}}]}`
	uTR  = `{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}`
	aE   = `{"role":"assistant","content":[{"type":"text","text":"Done."}]}`
	uF   = `{"role":"user","content":"and step two"}`
	aG   = `{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Read","input":{}}]}`
	uTR2 = `{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"file"}]}`
)

func mark(effort string) string {
	return `{"role":"system","content":[],"output_config":{"effort":"` + effort + `"}}`
}

func TestPerMessageEffortMarksReplayByteIdentical(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}}
	srv, log := effortServer(t, cloud, nil)

	// The first per-message request of a conversation already under way: the
	// top-level effort stays the request's own, a mark goes before the last user turn.
	post(t, srv, convo("high", uA, aB, uC), nil)
	sent1, header1 := log.last()
	if want := convo("high", uA, aB, mark("low"), uC); string(sent1) != want {
		t.Fatalf("first insert:\n got %s\nwant %s", sent1, want)
	}
	if header1.Get("anthropic-beta") != perMessageBeta {
		t.Errorf("anthropic-beta = %q", header1.Get("anthropic-beta"))
	}
	// The tool loop resends history without the mark: it comes back at the same
	// place, and every byte before the newest user turn is unchanged.
	post(t, srv, convo("high", uA, aB, uC, aD, uTR), map[string]string{"anthropic-beta": "context-1m-2025-08-07"})
	sent2, header2 := log.last()
	if want := convo("high", uA, aB, mark("low"), uC, aD, uTR); string(sent2) != want {
		t.Fatalf("replay:\n got %s\nwant %s", sent2, want)
	}
	newest := bytes.LastIndex(sent1, []byte(uC))
	if !bytes.HasPrefix(sent2, sent1[:newest+len(uC)]) {
		t.Errorf("the prefix changed between two consecutive requests")
	}
	if header2.Get("anthropic-beta") != "context-1m-2025-08-07,"+perMessageBeta {
		t.Errorf("anthropic-beta = %q, want the agent's betas then ours", header2.Get("anthropic-beta"))
	}
	// A new effort on a turn whose last user message carries a tool result goes
	// at the end, never between the tool_use and its tool_result.
	cloud.answer = RouteAnswer{Outcome: "kept", Effort: "medium", EffortMode: "message"}
	post(t, srv, convo("high", uA, aB, uC, aD, uTR), nil)
	sent3, _ := log.last()
	if want := convo("high", uA, aB, mark("low"), uC, aD, uTR, mark("medium")); string(sent3) != want {
		t.Fatalf("tool-result placement:\n got %s\nwant %s", sent3, want)
	}
	// The next turn: both marks replay; a new ask at another effort adds a third
	// before its user turn. The agent's own beta that carries ours is left alone.
	cloud.answer = RouteAnswer{Outcome: "kept", Effort: "high", EffortMode: "message"}
	post(t, srv, convo("high", uA, aB, uC, aD, uTR, aE, uF), map[string]string{"anthropic-beta": "per-turn-control-2026-07-01"})
	sent4, header4 := log.last()
	if want := convo("high", uA, aB, mark("low"), uC, aD, uTR, mark("medium"), aE, mark("high"), uF); string(sent4) != want {
		t.Fatalf("third mark:\n got %s\nwant %s", sent4, want)
	}
	if !bytes.HasPrefix(sent4, sent3[:len(sent3)-2]) {
		t.Errorf("the prefix changed between two consecutive requests")
	}
	if header4.Get("anthropic-beta") != "per-turn-control-2026-07-01" {
		t.Errorf("anthropic-beta = %q, want the agent's own beta only", header4.Get("anthropic-beta"))
	}
	// The same effort as the one in force adds nothing.
	post(t, srv, convo("high", uA, aB, uC, aD, uTR, aE, uF, aG, uTR2), nil)
	sent5, _ := log.last()
	if want := convo("high", uA, aB, mark("low"), uC, aD, uTR, mark("medium"), aE, mark("high"), uF, aG, uTR2); string(sent5) != want {
		t.Fatalf("no-op:\n got %s\nwant %s", sent5, want)
	}
	// History rewritten after the first two marks (aE changed): the third mark's
	// anchor no longer matches, so it and every later one drop; earlier ones stay.
	cloud.answer = RouteAnswer{Outcome: "kept", Effort: "medium", EffortMode: "message"}
	aE2 := `{"role":"assistant","content":[{"type":"text","text":"Rewritten."}]}`
	post(t, srv, convo("high", uA, aB, uC, aD, uTR, aE2, uF), nil)
	sent6, _ := log.last()
	if want := convo("high", uA, aB, mark("low"), uC, aD, uTR, mark("medium"), aE2, uF); string(sent6) != want {
		t.Fatalf("anchor break:\n got %s\nwant %s", sent6, want)
	}
	// A compacted history matches no anchor: no marks, no beta.
	cloud.answer = RouteAnswer{Outcome: "kept", Effort: "high", EffortMode: "message"}
	post(t, srv, convo("high", `{"role":"user","content":"summary"}`, uF), nil)
	sent7, header7 := log.last()
	if want := convo("high", `{"role":"user","content":"summary"}`, uF); string(sent7) != want || header7.Get("anthropic-beta") != "" {
		t.Fatalf("compacted:\n got %s (beta %q)\nwant %s", sent7, header7.Get("anthropic-beta"), want)
	}
}

// A fresh conversation needs no mark: its top-level effort is the routed one,
// and stays fixed for the session even though the agent keeps sending its own.
func TestPerMessageEffortFreshConversationSetsTheTopLevel(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}}
	srv, log := effortServer(t, cloud, nil)
	post(t, srv, convo("high", uA), nil)
	if sent, header := log.last(); string(sent) != convo("low", uA) || header.Get("anthropic-beta") != "" {
		t.Fatalf("fresh: %s (beta %q)", sent, header.Get("anthropic-beta"))
	}
	cloud.answer = RouteAnswer{Outcome: "kept", Effort: "high", EffortMode: "message"}
	post(t, srv, convo("high", uA, aB, uC), nil)
	if sent, _ := log.last(); string(sent) != convo("low", uA, aB, mark("high"), uC) {
		t.Fatalf("second turn: %s", sent)
	}
}

// Compaction and side requests get the session's marks back but never a new
// one; a forked child resending the parent's history inherits its marks.
func TestPerMessageEffortSideRequestsAndChildren(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}}
	srv, log := effortServer(t, cloud, nil)
	post(t, srv, convo("high", uA, aB, uC), nil)
	summarize := `{"role":"user","content":"summarize the conversation"}`
	cloud.answer = RouteAnswer{Outcome: "kept", Effort: "max", EffortMode: "message"}
	post(t, srv, convo("high", uA, aB, uC, aE, summarize), map[string]string{"x-claude-code-compaction": "1"})
	if sent, _ := log.last(); string(sent) != convo("high", uA, aB, mark("low"), uC, aE, summarize) {
		t.Fatalf("compaction: %s", sent)
	}
	if ask := cloud.asks[len(cloud.asks)-1]; !ask.PerRequest || ask.SessionID != "sess-1" {
		t.Errorf("compaction ask = %+v", ask)
	}
	cloud.answer = RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}
	task := `{"role":"user","content":"child task"}`
	post(t, srv, convo("high", uA, aB, uC, aE, task), map[string]string{"x-claude-code-agent-id": "a1"})
	if sent, _ := log.last(); string(sent) != convo("high", uA, aB, mark("low"), uC, aE, task) {
		t.Fatalf("forked child: %s", sent)
	}
	if ask := cloud.asks[len(cloud.asks)-1]; ask.SessionID != "sess-1#a1" || ask.ParentSessionID != "sess-1" || ask.PerRequest {
		t.Errorf("child ask = %+v", ask)
	}
	// A fresh child (its own conversation) inherits nothing: its top-level
	// effort is the routed one, like any fresh conversation.
	post(t, srv, convo("high", task), map[string]string{"x-claude-code-agent-id": "a2"})
	if sent, _ := log.last(); string(sent) != convo("low", task) {
		t.Fatalf("fresh child: %s", sent)
	}
}

// The agent's own per-message effort counts as in force, and a new mark goes
// after it, never before.
func TestPerMessageEffortCountsTheAgentsOwn(t *testing.T) {
	own := `{"role":"system","content":[{"type":"text","text":"note"}],"output_config":{"effort":"high"}}`
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "high", EffortMode: "message"}}
	srv, log := effortServer(t, cloud, nil)
	post(t, srv, convo("medium", uA, aB, own, uC), nil)
	if sent, header := log.last(); string(sent) != convo("medium", uA, aB, own, uC) || header.Get("anthropic-beta") != "" {
		t.Fatalf("the agent's effort in force: %s", sent)
	}
	cloud.answer = RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}
	post(t, srv, convo("medium", uA, aB, own, uC), nil)
	if sent, _ := log.last(); string(sent) != convo("medium", uA, aB, own, mark("low"), uC) {
		t.Fatalf("a new mark after the agent's: %s", sent)
	}
}

// errorBody is Anthropic's 400 envelope around message.
func errorBody(message string) string {
	encoded, _ := json.Marshal(message)
	return `{"type":"error","error":{"type":"invalid_request_error","message":` + string(encoded) + `}}`
}

// Marks refused: one retry with top-level effort only (no marks, no beta); once
// that is served the session's latch is on and the next ask says so.
func TestPerMessageEffortHealsARefusedMark(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}}
	srv, log := effortServer(t, cloud, func(body []byte) (int, string) {
		if bytes.Contains(body, []byte(`"role":"system"`)) {
			return http.StatusBadRequest, errorBody("output_config.effort requires a model that supports per-turn effort; this model does not")
		}
		return 0, ""
	})
	if rec := post(t, srv, convo("high", uA, aB, uC), nil); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(log.bodies) != 2 {
		t.Fatalf("upstream attempts = %d, want the marked one and one heal", len(log.bodies))
	}
	if string(log.bodies[1]) != convo("low", uA, aB, uC) || log.headers[1].Get("anthropic-beta") != "" {
		t.Fatalf("heal sent %s (beta %q)", log.bodies[1], log.headers[1].Get("anthropic-beta"))
	}
	post(t, srv, convo("high", uA, aB, uC, aE, uF), nil)
	if ask := cloud.asks[len(cloud.asks)-1]; !ask.PerMessageOff {
		t.Errorf("the next ask did not report the latch")
	}
	if sent, _ := log.last(); bytes.Contains(sent, []byte(`"role":"system"`)) || len(log.bodies) != 3 {
		t.Errorf("a latched session got a mark: %s", sent)
	}
}

// A broken thinking binding: one retry without thinking blocks and without marks.
func TestPerMessageEffortHealsTheThinkingBinding(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}}
	srv, log := effortServer(t, cloud, bindingUntilDropBlock)
	if rec := post(t, srv, convo("high", uA, aB, uC), nil); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(log.bodies) != 2 {
		t.Fatalf("upstream attempts = %d", len(log.bodies))
	}
	// The marks and every thinking block stay; the provider drops the unbound ones.
	if want := withDropBlockConvo(convo("high", uA, aB, mark("low"), uC)); string(log.bodies[1]) != want {
		t.Fatalf("heal sent %s\nwant %s", log.bodies[1], want)
	}
	if beta := log.headers[1].Get("anthropic-beta"); beta != perMessageBeta+","+bindingBeta {
		t.Errorf("heal betas %q", beta)
	}
	if cloud.asks[0].PerMessageOff {
		t.Error("a binding heal is no latch")
	}
}

const bindingError = "messages.1.content.0: Invalid `signature` in `thinking` block. The block is bound to a different conversation. Remove the block, or set `thinking.block_binding.prefix_mismatch_behavior` to \"drop_block\"."

// bindingUntilDropBlock refuses a body with a thinking block unless it asks for drop_block.
func bindingUntilDropBlock(body []byte) (int, string) {
	if bytes.Contains(body, []byte(`"signature"`)) && !bytes.Contains(body, []byte(`"prefix_mismatch_behavior":"drop_block"`)) {
		return http.StatusBadRequest, errorBody(bindingError)
	}
	return 0, ""
}

// withDropBlockConvo is a convo body with the drop_block thinking field appended.
func withDropBlockConvo(body string) string {
	return strings.TrimSuffix(body, "}") + `,"thinking":{"type":"adaptive","block_binding":{"prefix_mismatch_behavior":"drop_block"}}}`
}

// Refusal wording on a request without marks latches the session at once; an
// unrelated 400 neither heals nor latches. Neither retries more than once.
func TestPerMessageEffortLatchesOnRefusalWithoutMarks(t *testing.T) {
	for _, c := range []struct {
		message string
		latched bool
	}{
		{"messages.3: output_config.effort 'low' differs from the 'high' in effect before it; effort cannot change when thinking is disabled on this model. Use effort 'high', or enable thinking.", true},
		{"max_tokens: field required", false},
	} {
		cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept"}}
		srv, log := effortServer(t, cloud, func([]byte) (int, string) { return http.StatusBadRequest, errorBody(c.message) })
		post(t, srv, convo("high", uA), nil)
		if len(log.bodies) != 1 {
			t.Errorf("%q: %d upstream attempts for an unmodified request, want 1", c.message, len(log.bodies))
		}
		post(t, srv, convo("high", uA), nil)
		if got := cloud.asks[1].PerMessageOff; got != c.latched {
			t.Errorf("%q: latch = %v", c.message, got)
		}
	}
}

// Each answer is remembered as the session's last: the model the provider
// names, the effort in force, the usage and its age.
func TestRouteAskReportsTheSessionsLastRequest(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}}
	srv, _ := effortServer(t, cloud, nil)
	post(t, srv, convo("high", uA, aB, uC), map[string]string{"x-claude-code-context-compacted": "1"})
	if cloud.asks[0].Last != nil {
		t.Fatalf("the first ask carried last: %+v", cloud.asks[0].Last)
	}
	post(t, srv, convo("high", uA, aB, uC, aD, uTR), nil)
	last := cloud.asks[1].Last
	want := RouteLast{Model: "claude-opus-5-5-20261001", Effort: "low", AgeS: 0, InputTokens: 51300, CacheReadTokens: 50000, CacheWriteTokens: 1200, Compacted: true}
	if last == nil || *last != want {
		t.Fatalf("last = %+v, want %+v", last, want)
	}
	// Another session has its own.
	post(t, srv, convo("high", uA), map[string]string{"x-claude-code-session-id": "sess-2"})
	if cloud.asks[2].Last != nil {
		t.Errorf("a new session carried last: %+v", cloud.asks[2].Last)
	}
}

// OpenAI chat and Responses: the effort goes into the request's own field and
// last reads the provider's usage object and the model it names.
func TestRouteEffortAndLastOnOpenAI(t *testing.T) {
	for _, c := range []struct{ path, body, answer, sent string }{
		{
			"/v1/chat/completions", `{"model":"gpt-6-sol","messages":[{"role":"user","content":"go"}]}`,
			`{"id":"c","object":"chat.completion","model":"gpt-6-sol-2026-09-01","choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":5,"total_tokens":1005,"prompt_tokens_details":{"cached_tokens":800}}}`,
			`{"model":"gpt-6-sol","messages":[{"role":"user","content":"go"}],"reasoning_effort":"low"}`,
		},
		{
			"/v1/responses", `{"model":"gpt-6-sol","input":"go"}`,
			`{"id":"r","object":"response","model":"gpt-6-sol-2026-09-01","output":[],"usage":{"input_tokens":1000,"output_tokens":5,"total_tokens":1005,"input_tokens_details":{"cached_tokens":800}}}`,
			`{"model":"gpt-6-sol","input":"go","reasoning":{"effort":"low"}}`,
		},
	} {
		var sent []byte
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sent, _ = io.ReadAll(r.Body)
			w.Header().Set("content-type", "application/json")
			_, _ = io.WriteString(w, c.answer)
		}))
		cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low"}}
		srv := New(Config{
			Adapters: []providers.Adapter{openai.New("https://api.openai.com")}, Auth: stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
			Creds: stubCreds{key: "sk-byok"}, Sink: &captureSink{}, HTTPClient: &http.Client{Transport: toStub(upstream.URL)}, Cloud: cloud,
		})
		for range 2 {
			req := httptest.NewRequest(http.MethodPost, c.path, strings.NewReader(c.body))
			req.Header.Set("authorization", "Bearer sk-proj-api-key")
			req.Header.Set("session_id", "codex-1")
			srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
		}
		upstream.Close()
		if string(sent) != c.sent {
			t.Errorf("%s sent %s", c.path, sent)
		}
		want := RouteLast{Model: "gpt-6-sol-2026-09-01", Effort: "low", InputTokens: 1000, CacheReadTokens: 800}
		if len(cloud.asks) != 2 || cloud.asks[1].Last == nil || *cloud.asks[1].Last != want || cloud.asks[1].SessionID != "codex-1" {
			t.Errorf("%s: asks %d, last %+v", c.path, len(cloud.asks), cloud.asks[len(cloud.asks)-1].Last)
		}
	}
}

// A session without marks: every failure leaves the request byte-identical, and
// so does a message answer without a session.
func TestRouteEffortFailsOpenByteIdentical(t *testing.T) {
	body := convo("high", uA, aB, uC)
	for _, answer := range []RouteAnswer{
		{Outcome: "degraded", Reason: "timeout"},
		{Outcome: "paused", Reason: "allowance"},
		{Outcome: "off", Effort: "low", EffortMode: "message"},
	} {
		srv, log := effortServer(t, &fakeCloud{answer: answer}, nil)
		post(t, srv, body, nil)
		if sent, _ := log.last(); string(sent) != body {
			t.Errorf("%s: sent %s", answer.Outcome, sent)
		}
	}
	srv, log := effortServer(t, &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}}, nil)
	post(t, srv, body, map[string]string{"x-claude-code-session-id": ""})
	if sent, _ := log.last(); string(sent) != body {
		t.Errorf("no session: sent %s", sent)
	}
}

func TestPerMessageBetaHeader(t *testing.T) {
	for in, want := range map[string]string{
		"":    perMessageBeta,
		"a-1": "a-1," + perMessageBeta,
		"a-1, mid-conversation-effort-2026-08-01": "a-1, mid-conversation-effort-2026-08-01",
		perMessageBeta: perMessageBeta,
	} {
		h := http.Header{}
		if in != "" {
			h.Set("anthropic-beta", in)
		}
		if got := withPerMessageBeta(h).Get("anthropic-beta"); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// The route stage's per-request work on a 5 MB Claude Code-shaped body
// (messages first, output_config last) whose session replays two marks.
func BenchmarkRouteEffort5MB(b *testing.B) {
	result := `{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"` + strings.Repeat("lorem ipsum dolor sit amet ", 400) + `"}]}`
	messages := []string{uA, aB}
	for size := 0; size < 5<<20; size += len(result) + len(aD) {
		messages = append(messages, aD, result)
	}
	body := []byte(`{"model":"claude-opus-5-5","messages":[` + strings.Join(append(messages, aE, uC), ",") + `],"max_tokens":5,"output_config":{"effort":"high"}}`)
	s := &Server{}
	header := http.Header{"X-Claude-Code-Session-Id": {"s"}}
	s.applyEffort(newRouteRun(header, "", "/v1/messages", nil), "anthropic", "/v1/messages", "claude-opus-5-5", body, RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"})
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for b.Loop() {
		run := newRouteRun(header, "", "/v1/messages", nil)
		if s.applyEffort(run, "anthropic", "/v1/messages", "claude-opus-5-5", body, RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}); !run.marked || run.effort != "low" {
			b.Fatal("no marks replayed")
		}
		_ = withPerMessageBeta(header)
	}
}

// Agents and the breakpoint planner move cache_control every request; the
// anchors ignore it, so the marks stay put.
func TestPerMessageMarksSurviveMovingCacheControl(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "medium", EffortMode: "message"}}
	srv, log := effortServer(t, cloud, nil)
	cached := func(message string) string { // cache_control on the message's last block
		return strings.Replace(message, `}]}`, `,"cache_control":{"type":"ephemeral"}}]}`, 1)
	}
	post(t, srv, convo("high", uA, aB, uC, aD, cached(uTR)), nil)
	if sent, _ := log.last(); string(sent) != convo("high", uA, aB, uC, aD, cached(uTR), mark("medium")) {
		t.Fatalf("end mark: %s", sent)
	}
	post(t, srv, convo("high", uA, aB, uC, aD, uTR, aE, cached(`{"role":"user","content":[{"type":"text","text":"and step two"}]}`)), nil)
	want := convo("high", uA, aB, uC, aD, uTR, mark("medium"), aE, cached(`{"role":"user","content":[{"type":"text","text":"and step two"}]}`))
	if sent, _ := log.last(); string(sent) != want {
		t.Fatalf("the breakpoint moved off the anchor:\n got %s\nwant %s", sent, want)
	}
}

// A side request with its own history reads the session's marks but never
// changes them; the main thread keeps its marks.
func TestSideRequestLeavesTheSessionsMarks(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}}
	srv, log := effortServer(t, cloud, nil)
	post(t, srv, convo("high", uA, aB, uC), nil)
	post(t, srv, convo("high", `{"role":"user","content":"title this"}`), map[string]string{"x-claude-code-request-class": "auxiliary"})
	if sent, _ := log.last(); string(sent) != convo("high", `{"role":"user","content":"title this"}`) {
		t.Fatalf("side request: %s", sent)
	}
	post(t, srv, convo("high", uA, aB, uC, aD, uTR), nil)
	if sent, _ := log.last(); string(sent) != convo("high", uA, aB, mark("low"), uC, aD, uTR) {
		t.Fatalf("main thread after a side request: %s", sent)
	}
	if cloud.asks[2].Last == nil || cloud.asks[2].Last.InputTokens == 0 || len(cloud.asks) != 3 {
		t.Fatalf("asks = %d", len(cloud.asks))
	}
}

// A compressed refusal is read decoded: the heal and the latch still fire.
func TestPerMessageHealReadsACompressedRefusal(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}}
	var mu sync.Mutex
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		attempts++
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		if bytes.Contains(raw, []byte(`"role":"system"`)) && r.Header.Get("accept-encoding") == "gzip" {
			var zipped bytes.Buffer
			zw := gzip.NewWriter(&zipped)
			_, _ = io.WriteString(zw, errorBody("output_config.effort requires a model that supports per-turn effort; this model does not"))
			_ = zw.Close()
			w.Header().Set("content-encoding", "gzip")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write(zipped.Bytes())
			return
		}
		_, _ = io.WriteString(w, `{"id":"m","type":"message","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()
	srv := New(Config{
		Adapters: []providers.Adapter{anthropic.New("https://api.anthropic.com")}, Auth: stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		Creds: stubCreds{key: "sk-byok"}, Sink: &captureSink{}, HTTPClient: &http.Client{Transport: toStub(upstream.URL)}, Cloud: cloud,
	})
	if rec := post(t, srv, convo("high", uA, aB, uC), map[string]string{"accept-encoding": "gzip"}); rec.Code != http.StatusOK || attempts != 2 {
		t.Fatalf("status %d after %d attempts", rec.Code, attempts)
	}
	post(t, srv, convo("high", uA, aB, uC, aD, uTR), map[string]string{"accept-encoding": "gzip"})
	if !cloud.asks[1].PerMessageOff || attempts != 3 {
		t.Errorf("latch %v, attempts %d", cloud.asks[1].PerMessageOff, attempts)
	}
}

// The binding heal is for history the marks changed: a routed model is left
// to the original-bytes retry on the asked one (which rejects the decision),
// and an agent's own broken binding is not touched.
func TestBindingHealOnTheAskedModel(t *testing.T) {
	binding := errorBody("messages.1.content.0: Invalid `signature` in `thinking` block. The block is bound to a different conversation.")
	rejected := 0
	cloud := &fakeCloud{answer: RouteAnswer{Model: "claude-sonnet-5-5", Outcome: "routed", Effort: "low", EffortMode: "message", Reject: func() { rejected++ }}}
	srv, log := effortServer(t, cloud, func(body []byte) (int, string) {
		if bytes.Contains(body, []byte(`claude-sonnet-5-5`)) {
			return http.StatusBadRequest, binding
		}
		return 0, ""
	})
	post(t, srv, convo("high", uA, aB, uC), nil)
	if len(log.bodies) != 2 || string(log.bodies[1]) != convo("high", uA, aB, uC) || rejected != 1 {
		t.Fatalf("routed model: %d attempts, last %s, rejected %d", len(log.bodies), log.bodies[len(log.bodies)-1], rejected)
	}
	// Any binding 400 on the asked model is healed, the route stage's doing or
	// not: a proxy that forgot its marks (restart, eviction) has no way to know.
	cloud2 := &fakeCloud{answer: RouteAnswer{Outcome: "kept"}}
	srv2, log2 := effortServer(t, cloud2, bindingUntilDropBlock)
	if rec := post(t, srv2, convo("high", uA, aB, uC), nil); rec.Code != http.StatusOK || len(log2.bodies) != 2 {
		t.Errorf("an untouched request: status %d after %d attempts", rec.Code, len(log2.bodies))
	}
}

// After a served binding heal the session's later requests ask for drop_block
// up front (one attempt each), a forked child of it too.
func TestBindingHealIsRemembered(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}}
	srv, log := effortServer(t, cloud, bindingUntilDropBlock)
	post(t, srv, convo("high", uA, aB, uC), nil)
	if len(log.bodies) != 2 {
		t.Fatalf("attempts = %d", len(log.bodies))
	}
	aH := `{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"sig-h"},{"type":"text","text":"Ok."}]}`
	post(t, srv, convo("high", uA, aB, uC, aH, uF), nil)
	if len(log.bodies) != 3 {
		t.Fatalf("attempts = %d, want the remembered heal to need no 400", len(log.bodies))
	}
	sent, header := log.last()
	if string(sent) != withDropBlockConvo(convo("high", uA, aB, mark("low"), uC, aH, uF)) || header.Get("anthropic-beta") != bindingBeta+","+perMessageBeta {
		t.Fatalf("remembered heal: %s (betas %q)", sent, header.Get("anthropic-beta"))
	}
	post(t, srv, convo("high", uA, aB, uC, aH, `{"role":"user","content":"child"}`), map[string]string{"x-claude-code-agent-id": "a1"})
	if len(log.bodies) != 4 {
		t.Fatalf("a forked child of a healed session paid a 400: %d attempts", len(log.bodies))
	}
}

// With between_tools (no drop_block there) the heal strips thinking from the
// failing message on, and later requests strip the same blocks up front.
func TestBindingHealStripsWhereDropBlockIsRefused(t *testing.T) {
	between := func(body string) string {
		return strings.Replace(body, `"max_tokens":5,`, `"max_tokens":5,"thinking":{"type":"between_tools"},`, 1)
	}
	aX := `{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"sig-x"},{"type":"text","text":"Early."}]}`
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept"}}
	srv, log := effortServer(t, cloud, func(body []byte) (int, string) {
		if bytes.Contains(body, []byte(`sig-b`)) {
			return http.StatusBadRequest, errorBody("messages.3.content.0: Invalid `signature` in `thinking` block. The block is bound to a different conversation.")
		}
		return 0, ""
	})
	uX := `{"role":"user","content":"first"}`
	post(t, srv, between(convo("high", uX, aX, uA, aB, uC)), nil)
	healedB := `{"role":"assistant","content":[{"type":"text","text":"Three steps."}]}`
	if len(log.bodies) != 2 || string(log.bodies[1]) != between(convo("high", uX, aX, uA, healedB, uC)) {
		t.Fatalf("strip heal: %d attempts, sent %s", len(log.bodies), log.bodies[len(log.bodies)-1])
	}
	post(t, srv, between(convo("high", uX, aX, uA, aB, uC, aE, uF)), nil)
	if len(log.bodies) != 3 {
		t.Fatalf("attempts = %d, want the remembered strip to need no 400", len(log.bodies))
	}
	if sent, _ := log.last(); string(sent) != between(convo("high", uX, aX, uA, healedB, uC, aE, uF)) {
		t.Fatalf("remembered strip: %s", sent)
	}
}

// A session's marks come back whatever the answer: a Cloud failure, routing
// off, or a top-level answer without an effort.
func TestPerMessageMarksComeBackOnEveryAnswer(t *testing.T) {
	for _, answer := range []RouteAnswer{
		{Outcome: "degraded", Reason: "timeout"},
		{Outcome: "off"},
		{Outcome: "kept", EffortMode: "top"},
	} {
		cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}}
		srv, log := effortServer(t, cloud, nil)
		post(t, srv, convo("high", uA, aB, uC), nil)
		cloud.answer = answer
		post(t, srv, convo("high", uA, aB, uC, aD, uTR), nil)
		// Without Cloud's effort the agent's own top-level one comes back, as a mark.
		if sent, header := log.last(); string(sent) != convo("high", uA, aB, mark("low"), uC, aD, uTR, mark("high")) || header.Get("anthropic-beta") != perMessageBeta {
			t.Errorf("%+v: %s", answer, sent)
		}
	}
	// Right after a routed turn a Cloud failure sends the asked model: it has
	// not refused marks, so it gets them too.
	cloud := &fakeCloud{answer: RouteAnswer{Model: "claude-sonnet-5-5", Outcome: "routed", Effort: "low", EffortMode: "message"}}
	srv, log := effortServer(t, cloud, nil)
	post(t, srv, convo("high", uA, aB, uC), nil)
	cloud.answer = RouteAnswer{Outcome: "degraded"}
	post(t, srv, convo("high", uA, aB, uC, aD, uTR), nil)
	if sent, _ := log.last(); string(sent) != convo("high", uA, aB, mark("low"), uC, aD, uTR, mark("high")) {
		t.Errorf("the asked model after a routed turn: %s", sent)
	}
}

// Requests of one session, its child and a side request at once share no
// mutable state outside the lock (run with -race).
func TestRouteSessionsUnderConcurrency(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}}
	srv, _ := effortServer(t, cloud, nil)
	post(t, srv, convo("high", uA, aB, uC), nil)
	var wg sync.WaitGroup
	for i := range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch i % 3 {
			case 0:
				post(t, srv, convo("high", uA, aB, uC, aD, uTR), nil)
			case 1:
				post(t, srv, convo("high", uA, aB, uC, aE, `{"role":"user","content":"child"}`), map[string]string{"x-claude-code-agent-id": "a1"})
			default:
				post(t, srv, convo("high", uA, aB, uC, aE, uF), map[string]string{"x-claude-code-request-class": "auxiliary"})
			}
		}()
	}
	wg.Wait()
}

// A routed request does not offer br upstream: its answer is read decoded.
// With nothing left Go's transport offers gzip and decodes it itself.
func TestRoutedRequestsDropBrotli(t *testing.T) {
	srv, log := effortServer(t, &fakeCloud{answer: RouteAnswer{Outcome: "kept"}}, nil)
	for offered, want := range map[string]string{"gzip, deflate, br": "gzip, deflate", "br;q=1.0": "gzip", "zstd, gzip": "zstd, gzip"} {
		post(t, srv, convo("high", uA), map[string]string{"accept-encoding": offered})
		if _, header := log.last(); header.Get("accept-encoding") != want {
			t.Errorf("%q went upstream as %q, want %q", offered, header.Get("accept-encoding"), want)
		}
	}
}

// A provider refusing a routed effort rejects the decision like a refused model.
func TestRefusedEffortRejectsTheDecision(t *testing.T) {
	rejected := 0
	var sent [][]byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		sent = append(sent, raw)
		w.Header().Set("content-type", "application/json")
		if bytes.Contains(raw, []byte(`reasoning_effort`)) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"Unsupported parameter: 'reasoning_effort'","type":"invalid_request_error"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"c","object":"chat.completion","model":"gpt-6-sol","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer upstream.Close()
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low", Reject: func() { rejected++ }}}
	srv := New(Config{
		Adapters: []providers.Adapter{openai.New("https://api.openai.com")}, Auth: stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		Creds: stubCreds{key: "sk-byok"}, Sink: &captureSink{}, HTTPClient: &http.Client{Transport: toStub(upstream.URL)}, Cloud: cloud,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-6-sol","messages":[]}`))
	req.Header.Set("authorization", "Bearer sk-proj-api-key")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || len(sent) != 2 || rejected != 1 {
		t.Fatalf("status %d, attempts %d, rejected %d", rec.Code, len(sent), rejected)
	}
}

// Every label the gateway reads is one route-ask-v1 allows, at the same bound.
func TestRouteLabelsMatchTheContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "packages", "shared", "contracts", "schemas", "route-ask-v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties struct {
			Request struct {
				Properties struct {
					Labels struct {
						Properties map[string]struct {
							Ref       string `json:"$ref"`
							MaxLength int    `json:"maxLength"`
						} `json:"properties"`
					} `json:"labels"`
				} `json:"properties"`
			} `json:"request"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	allowed := schema.Properties.Request.Properties.Labels.Properties
	if len(allowed) != len(routeLabelNames) {
		t.Errorf("the contract allows %d labels, the gateway reads %d", len(allowed), len(routeLabelNames))
	}
	for _, name := range routeLabelNames {
		spec, ok := allowed[name]
		bound := spec.MaxLength
		if spec.Ref == "#/$defs/label" {
			bound = 256
		}
		if !ok || bound != RouteLabelMax(name) {
			t.Errorf("%s: in the contract %v, bound %d, gateway %d", name, ok, bound, RouteLabelMax(name))
		}
	}
}

// chunked hands out its chunks one Read at a time.
type chunked struct{ chunks [][]byte }

func (c *chunked) Read(p []byte) (int, error) {
	if len(c.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.chunks[0])
	if c.chunks[0] = c.chunks[0][n:]; len(c.chunks[0]) == 0 {
		c.chunks = c.chunks[1:]
	}
	return n, nil
}

// The agent's copy names the asked model at every split of the stream,
// in each provider's stream shape; nothing else changes.
func TestShownModelInStreamsAtEverySplit(t *testing.T) {
	cases := []struct{ name, sent, in, want string }{
		{
			"anthropic message_start", "claude-sonnet-5-5",
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-5-5\",\"content\":[]}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"model\\\":\\\"claude-sonnet-5-5\\\"}\"}}\n\n",
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[]}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"model\\\":\\\"claude-sonnet-5-5\\\"}\"}}\n\n",
		},
		{
			"chat chunks, dated id", "gpt-6-sol",
			"data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-6-sol-2026-09-01\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"model\": \"gpt-6-sol-2026-09-01\",\"choices\":[]}\n\ndata: [DONE]\n\n",
			"data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-6-luna\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"model\": \"gpt-6-luna\",\"choices\":[]}\n\ndata: [DONE]\n\n",
		},
		{
			"responses events", "gpt-6-sol",
			"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"instructions\":\"say \\\"model\\\": \\\"gpt-6-sol\\\"\",\"model\":\"gpt-6-sol\"}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"model\":\"gpt-6-sol\",\"usage\":{}}}\n\n",
			"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"instructions\":\"say \\\"model\\\": \\\"gpt-6-sol\\\"\",\"model\":\"gpt-6-luna\"}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"model\":\"gpt-6-luna\",\"usage\":{}}}\n\n",
		},
	}
	asked := map[string]string{"claude-sonnet-5-5": "claude-opus-5-5", "gpt-6-sol": "gpt-6-luna"}
	for _, c := range cases {
		in := []byte(c.in)
		for i := 0; i <= len(in); i++ {
			for _, j := range []int{i, min(i+3, len(in)), min(i+40, len(in))} {
				got, err := io.ReadAll(newShownModel(&chunked{chunks: [][]byte{in[:i:i], in[i:j:j], in[j:]}}, c.sent, asked[c.sent]))
				if err != nil || string(got) != c.want {
					t.Fatalf("%s split at %d/%d:\n got %q\nwant %q", c.name, i, j, got, c.want)
				}
			}
		}
	}
}

// A stream stays live: a complete event is handed out without waiting for
// more input, and a pair split across reads is held only until it completes.
func TestShownModelStaysIncremental(t *testing.T) {
	reader, writer := io.Pipe()
	shown := newShownModel(reader, "claude-sonnet-5-5", "claude-opus-5-5")
	go func() {
		_, _ = writer.Write([]byte("data: {\"model\":\"claude-sonnet-5-5\"}\n\ndata: {\"mod"))
		_, _ = writer.Write([]byte("el\":\"claude-sonnet-5-5\"}\n\n"))
		_ = writer.Close()
	}()
	buf := make([]byte, 1024)
	n, err := shown.Read(buf)
	if err != nil || string(buf[:n]) != "data: {\"model\":\"claude-opus-5-5\"}\n\ndata: {" {
		t.Fatalf("first read = %q, %v", buf[:n], err)
	}
	rest, _ := io.ReadAll(shown)
	if string(rest) != "\"model\":\"claude-opus-5-5\"}\n\n" {
		t.Fatalf("rest = %q", rest)
	}
}

// When the request moved, the agent reads the model it asked for, in a JSON
// answer (top-level "model" only, Content-Length right) and in a stream;
// usage, the recorded row and last keep the served model.
func TestRoutedAnswerShowsTheAskedModel(t *testing.T) {
	for _, stream := range []bool{false, true} {
		cloud := &fakeCloud{answer: RouteAnswer{Model: "claude-sonnet-5-5", Outcome: "routed"}}
		srv, log := effortServer(t, cloud, func(body []byte) (int, string) {
			if stream {
				return 0, ""
			}
			return http.StatusOK, `{"id":"m","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[{"type":"tool_use","id":"t","name":"Write","input":{"model":"claude-sonnet-5-5"}}],"usage":{"input_tokens":10,"output_tokens":2}}`
		})
		if stream {
			srv, log = streamServer(t, cloud)
		}
		rec := post(t, srv, convo("high", uA), nil)
		sent, upstreamHeader := log.last()
		if !bytes.Contains(sent, []byte(`"model":"claude-sonnet-5-5"`)) || upstreamHeader.Get("accept-encoding") == "br" {
			t.Fatalf("upstream got %s", sent)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"model":"claude-opus-5-5"`) || strings.Count(body, "claude-sonnet-5-5") != map[bool]int{false: 1, true: 0}[stream] {
			t.Errorf("stream=%v: the agent read %s", stream, body)
		}
		if !stream && rec.Header().Get("content-length") != strconv.Itoa(len(body)) {
			t.Errorf("content-length %s for %d bytes", rec.Header().Get("content-length"), len(body))
		}
		if rec.Header().Get("x-caveman-routed-from") != "claude-opus-5-5" || cloud.observed[0].Model != "claude-sonnet-5-5" {
			t.Errorf("routed-from %q, recorded model %q", rec.Header().Get("x-caveman-routed-from"), cloud.observed[0].Model)
		}
		post(t, srv, convo("high", uA, aB, uC), nil)
		if last := cloud.asks[1].Last; last == nil || last.Model != "claude-sonnet-5-5" {
			t.Errorf("stream=%v: last = %+v, want the served model", stream, last)
		}
	}
}

// streamServer answers every request with an Anthropic stream naming the model sent.
func streamServer(t *testing.T, cloud CloudLink) (*Server, *upstreamLog) {
	t.Helper()
	log := &upstreamLog{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		log.mu.Lock()
		log.bodies, log.headers = append(log.bodies, raw), append(log.headers, r.Header.Clone())
		log.mu.Unlock()
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(raw, &req)
		w.Header().Set("content-type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, event := range []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"` + req.Model + `","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}` + "\n\n",
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}` + "\n\n",
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}` + "\n\n",
		} {
			_, _ = io.WriteString(w, event)
			flusher.Flush()
		}
	}))
	t.Cleanup(upstream.Close)
	return New(Config{
		Adapters: []providers.Adapter{anthropic.New("https://api.anthropic.com")}, Auth: stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		Creds: stubCreds{key: "sk-byok"}, Sink: &captureSink{}, HTTPClient: &http.Client{Transport: toStub(upstream.URL)}, Cloud: cloud,
	}), log
}

// OpenAI chat and Responses JSON answers show the asked model too.
func TestRoutedOpenAIAnswerShowsTheAskedModel(t *testing.T) {
	for path, answer := range map[string]string{
		"/v1/chat/completions": `{"id":"c","object":"chat.completion","model":"gpt-6-luna-2026-09-01","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
		"/v1/responses":        `{"id":"r","object":"response","instructions":"x","model":"gpt-6-luna-2026-09-01","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`,
	} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("content-type", "application/json")
			_, _ = io.WriteString(w, answer)
		}))
		cloud := &fakeCloud{answer: RouteAnswer{Model: "gpt-6-luna", Outcome: "routed"}}
		srv := New(Config{
			Adapters: []providers.Adapter{openai.New("https://api.openai.com")}, Auth: stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
			Creds: stubCreds{key: "sk-byok"}, Sink: &captureSink{}, HTTPClient: &http.Client{Transport: toStub(upstream.URL)}, Cloud: cloud,
		})
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"gpt-6-sol","input":"go","messages":[]}`))
		req.Header.Set("authorization", "Bearer sk-proj-api-key")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		upstream.Close()
		if want := strings.Replace(answer, "gpt-6-luna-2026-09-01", "gpt-6-sol", 1); rec.Body.String() != want {
			t.Errorf("%s: the agent read %s", path, rec.Body.String())
		}
		if cloud.observed[0].Model != "gpt-6-luna" {
			t.Errorf("%s: recorded model %q", path, cloud.observed[0].Model)
		}
	}
}

// Without an effort from Cloud the agent's own top-level effort runs: routed
// low, then routing off while the agent asks for max puts max back, cache-safely.
func TestAgentsOwnEffortComesBackWithoutCloud(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}}
	srv, log := effortServer(t, cloud, nil)
	post(t, srv, convo("high", uA, aB, uC), nil)
	cloud.answer = RouteAnswer{Outcome: "off"}
	post(t, srv, convo("max", uA, aB, uC, aE, uF), nil)
	sent, _ := log.last()
	if string(sent) != convo("high", uA, aB, mark("low"), uC, aE, mark("max"), uF) || effortInForce("/v1/messages", sent) != "max" {
		t.Fatalf("routing off: %s", sent)
	}
}

// An unlabeled request with its own short history (a side request without hint
// headers) matches none of the session's marks and leaves them; a model outside
// the pool still gets them back.
func TestMarksSurviveAnUnlabeledSideRequest(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}}
	srv, log := effortServer(t, cloud, nil)
	post(t, srv, convo("high", uA, aB, uC), nil)
	cloud.answer = RouteAnswer{Outcome: "kept"}
	post(t, srv, convo("high", `{"role":"user","content":"title?"}`, `{"role":"assistant","content":"T"}`, `{"role":"user","content":"ok"}`), nil)
	cloud.answer = RouteAnswer{Outcome: "off", Reason: "model_outside_pool"}
	post(t, srv, convo("low", uA, aB, uC, aD, uTR), nil)
	if sent, _ := log.last(); string(sent) != convo("high", uA, aB, mark("low"), uC, aD, uTR) {
		t.Fatalf("after an unlabeled side request: %s", sent)
	}
}

// A model that refused marks gets Cloud's effort top-level on every later
// request of the turn, as the heal that found out sent it.
func TestRefusedModelTakesTheEffortTopLevel(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}}
	srv, log := effortServer(t, cloud, func(body []byte) (int, string) {
		if bytes.Contains(body, []byte(`"role":"system"`)) {
			return http.StatusBadRequest, errorBody("output_config.effort requires a model that supports per-turn effort; this model does not")
		}
		return 0, ""
	})
	post(t, srv, convo("high", uA, aB, uC), nil)
	post(t, srv, convo("high", uA, aB, uC, aD, uTR), nil)
	if sent, _ := log.last(); string(sent) != convo("low", uA, aB, uC, aD, uTR) || len(log.bodies) != 3 {
		t.Fatalf("tool loop after the refusal: %s (%d attempts)", sent, len(log.bodies))
	}
}

// A routed request's 429 is passed on: no original-bytes retry, no reject.
func TestRateLimitOnARoutedRequestIsNotRetried(t *testing.T) {
	rejected := 0
	cloud := &fakeCloud{answer: RouteAnswer{Model: "claude-sonnet-5-5", Outcome: "routed", Effort: "low", EffortMode: "message", Reject: func() { rejected++ }}}
	srv, log := effortServer(t, cloud, func([]byte) (int, string) {
		return http.StatusTooManyRequests, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`
	})
	if rec := post(t, srv, convo("high", uA, aB, uC), nil); rec.Code != http.StatusTooManyRequests || len(log.bodies) != 1 || rejected != 0 {
		t.Fatalf("status %d, attempts %d, rejected %d", rec.Code, len(log.bodies), rejected)
	}
}

// A moved stream that arrived with a Content-Length goes on without one: the
// agent's copy is another length.
func TestMovedStreamDropsContentLength(t *testing.T) {
	stream := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"claude-sonnet-5-5\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("content-type", "text/event-stream")
		w.Header().Set("content-length", strconv.Itoa(len(stream)))
		_, _ = io.WriteString(w, stream)
	}))
	defer upstream.Close()
	cloud := &fakeCloud{answer: RouteAnswer{Model: "claude-sonnet-5-5", Outcome: "routed"}}
	srv := New(Config{
		Adapters: []providers.Adapter{anthropic.New("https://api.anthropic.com")}, Auth: stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		Creds: stubCreds{key: "sk-byok"}, Sink: &captureSink{}, HTTPClient: &http.Client{Transport: toStub(upstream.URL)}, Cloud: cloud,
	})
	proxied := httptest.NewServer(srv.Handler())
	defer proxied.Close()
	req, _ := http.NewRequest(http.MethodPost, proxied.URL+"/v1/messages", strings.NewReader(convo("high", uA)))
	req.Header.Set("x-api-key", "sk-ant-api-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil || string(got) != strings.Replace(stream, "claude-sonnet-5-5", "claude-opus-5-5", 1) {
		t.Fatalf("read %q, %v", got, err)
	}
}

// The side-request labels mirror Cloud's request kinds: Codex's subagent kind
// and turn metadata, OpenCode's agent name, and a Codex parent thread from the
// turn metadata when no header names it. Invalid UTF-8 never goes.
func TestSideRequestLabelsMirrorCloud(t *testing.T) {
	cases := []struct {
		endpoint  string
		headers   map[string]string
		side, aux bool
	}{
		{"/v1/responses", map[string]string{"X-Openai-Subagent": "compact"}, true, false},
		{"/v1/responses", map[string]string{"X-Openai-Subagent": "memory_consolidation"}, true, true},
		{"/v1/responses", map[string]string{"X-Openai-Subagent": "something_new"}, true, true},
		{"/v1/responses", map[string]string{"X-Openai-Subagent": "review"}, false, false},
		{"/v1/responses", map[string]string{"X-Openai-Subagent": "collab_spawn"}, false, false},
		{"/v1/responses", map[string]string{"X-Codex-Turn-Metadata": `{"request_kind":"compaction"}`}, true, false},
		{"/v1/responses", map[string]string{"X-Codex-Turn-Metadata": `{"request_kind":"memory"}`}, true, true},
		{"/v1/responses", map[string]string{"X-Codex-Turn-Metadata": `{"subagent_kind":"compact"}`}, true, false},
		{"/v1/messages", map[string]string{"X-Caveman-Agent": "title"}, true, true},
		{"/v1/messages", map[string]string{"X-Caveman-Agent": "summary"}, true, true},
		{"/v1/messages", map[string]string{"X-Caveman-Agent": "compaction"}, true, false},
		{"/v1/messages", map[string]string{"X-Caveman-Agent": "title", "X-Claude-Code-Agent-Id": "a1"}, false, false},
		{"/v1/messages", map[string]string{"X-Parent-Session-Id": "p1"}, false, false},
	}
	for _, c := range cases {
		h := http.Header{}
		for name, value := range c.headers {
			h.Set(name, value)
		}
		if run := newRouteRun(h, "s", c.endpoint, nil); run.perRequest != c.side || run.auxiliary != c.aux {
			t.Errorf("%v: per request %v auxiliary %v, want %v %v", c.headers, run.perRequest, run.auxiliary, c.side, c.aux)
		}
	}
	// Codex's parent thread and turn metadata from the body.
	body := []byte(`{"model":"gpt-6-sol","client_metadata":{"x-codex-turn-metadata":"{\"parent_thread_id\":\"thread-1\"}"},"input":"go"}`)
	h := http.Header{}
	h.Set("Thread-Id", "thread-2")
	if run := newRouteRun(h, "", "/v1/responses", body); run.key != "thread-2" || run.parent != "thread-1" || run.labels["x-codex-turn-metadata"] == "" {
		t.Errorf("codex child from turn metadata: %+v", run)
	}
	h = http.Header{}
	h.Set("X-Openai-Subagent", "comp\xffact")
	if run := newRouteRun(h, "s", "/v1/responses", nil); len(run.labels) != 0 {
		t.Errorf("invalid UTF-8 went: %v", run.labels)
	}
}
