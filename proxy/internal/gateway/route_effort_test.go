package gateway

import (
	"bytes"
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

func TestRouteRunReadsLabelsAndSessionKeys(t *testing.T) {
	h := http.Header{}
	h.Set("X-Claude-Code-Session-Id", "sess-1")
	h.Set("X-Claude-Code-Agent-Id", "agent-7")
	h.Set("X-Claude-Code-Parent-Agent-Id", "agent-3")
	h.Set("X-Claude-Code-Request-Class", "subagent")
	h.Set("X-Codex-Turn-Metadata", strings.Repeat("é", 200)) // 400 bytes
	h.Set("X-Unlisted", "never sent")
	run := newRouteRun(h, "")
	if run.key != "sess-1#agent-7" || run.parent != "sess-1#agent-3" || run.perRequest || run.compacted {
		t.Fatalf("run = %+v", run)
	}
	if len(run.labels) != 4 || run.labels["x-claude-code-request-class"] != "subagent" || run.labels["x-unlisted"] != "" {
		t.Fatalf("labels = %v", run.labels)
	}
	if got := run.labels["x-codex-turn-metadata"]; len(got) != 256 || got != strings.Repeat("é", 128) {
		t.Errorf("a long label is %d bytes, want 256 cut on a rune boundary", len(got))
	}
	// The caller's x-cave-session wins; a child without a parent agent hangs off the session.
	h.Del("X-Claude-Code-Parent-Agent-Id")
	if run := newRouteRun(h, "cave-s"); run.key != "cave-s#agent-7" || run.parent != "cave-s" {
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
		if run := newRouteRun(h, "s"); !run.perRequest {
			t.Errorf("%v is answered per request", labels)
		}
	}
	h = http.Header{}
	h.Set("X-Claude-Code-Context-Compacted", "true")
	if run := newRouteRun(h, "s"); run.perRequest || !run.compacted || run.key != "s" {
		t.Errorf("after a compaction: %+v", run)
	}
	if run := newRouteRun(http.Header{}, ""); run.key != "" || len(run.labels) != 0 {
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
		got := s.applyEffort(newRouteRun(http.Header{}, ""), provider, c.endpoint, []byte(c.body), RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: c.mode})
		if string(got) != c.want {
			t.Errorf("%s %s:\n got %s\nwant %s", c.endpoint, c.body, got, c.want)
		}
	}
	// No effort, or the route stage off: the body is left as sent.
	body := `{"reasoning_effort":"high","messages":[]}`
	for _, answer := range []RouteAnswer{{Outcome: "kept"}, {Outcome: "off", Effort: "low"}, {Outcome: "degraded"}} {
		if got := s.applyEffort(newRouteRun(http.Header{}, ""), "openai", "/v1/chat/completions", []byte(body), answer); string(got) != body {
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
	cloud.answer = RouteAnswer{Outcome: "kept"}
	aE2 := `{"role":"assistant","content":[{"type":"text","text":"Rewritten."}]}`
	post(t, srv, convo("high", uA, aB, uC, aD, uTR, aE2, uF), nil)
	sent6, _ := log.last()
	if want := convo("high", uA, aB, mark("low"), uC, aD, uTR, mark("medium"), aE2, uF); string(sent6) != want {
		t.Fatalf("anchor break:\n got %s\nwant %s", sent6, want)
	}
	// A compacted history matches no anchor: no marks, no beta.
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
	cloud.answer = RouteAnswer{Outcome: "kept"}
	task := `{"role":"user","content":"child task"}`
	post(t, srv, convo("high", uA, aB, uC, aE, task), map[string]string{"x-claude-code-agent-id": "a1"})
	if sent, _ := log.last(); string(sent) != convo("high", uA, aB, mark("low"), uC, aE, task) {
		t.Fatalf("forked child: %s", sent)
	}
	if ask := cloud.asks[len(cloud.asks)-1]; ask.SessionID != "sess-1#a1" || ask.ParentSessionID != "sess-1" || ask.PerRequest {
		t.Errorf("child ask = %+v", ask)
	}
	// A fresh child (its own conversation) inherits nothing.
	post(t, srv, convo("high", task), map[string]string{"x-claude-code-agent-id": "a2"})
	if sent, _ := log.last(); string(sent) != convo("high", task) {
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
	srv, log := effortServer(t, cloud, func(body []byte) (int, string) {
		if bytes.Contains(body, []byte(`"thinking"`)) {
			return http.StatusBadRequest, errorBody("messages.1.content.0: Invalid `signature` in `thinking` block. The block is bound to a different conversation. Remove the block, or set `thinking.block_binding.prefix_mismatch_behavior` to \"drop_block\".")
		}
		return 0, ""
	})
	if rec := post(t, srv, convo("high", uA, aB, uC), nil); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(log.bodies) != 2 {
		t.Fatalf("upstream attempts = %d", len(log.bodies))
	}
	healedB := `{"role":"assistant","content":[{"type":"text","text":"Three steps."}]}`
	if string(log.bodies[1]) != convo("high", uA, healedB, uC) {
		t.Fatalf("heal sent %s", log.bodies[1])
	}
	if cloud.asks[0].PerMessageOff {
		t.Error("a binding heal is no latch")
	}
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

// Every failure leaves the request byte-identical; no session, no marks.
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
	s.applyEffort(newRouteRun(header, ""), "anthropic", "/v1/messages", body, RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"})
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for b.Loop() {
		run := newRouteRun(header, "")
		if s.applyEffort(run, "anthropic", "/v1/messages", body, RouteAnswer{Outcome: "kept", Effort: "low", EffortMode: "message"}); !run.marked || run.effort != "low" {
			b.Fatal("no marks replayed")
		}
		_ = withPerMessageBeta(header)
	}
}
