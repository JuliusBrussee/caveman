package gateway

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/translate"
	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

func openRouterTarget() *RouteTarget {
	header := http.Header{}
	header.Set("authorization", "Bearer sk-or")
	return &RouteTarget{PoolID: "openrouter/kimi-k3", Via: "local", Host: "openrouter", Model: "kimi-k3", Wire: translate.Chat,
		URL: "https://openrouter.ai/chat/completions", Header: header, Affinity: "x-session-id",
		Translate: translate.Options{Model: "moonshotai/kimi-k3", Dialect: "openrouter", Route: "openrouter/kimi-k3"}}
}

func orAnswer(provider string, cached int) string {
	return fmt.Sprintf(`{"id":"gen-1","object":"chat.completion","model":"moonshotai/kimi-k3","provider":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3000,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":%d}}}`, provider, cached)
}

func pinOf(t *testing.T, body string) any {
	t.Helper()
	var sent map[string]any
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("pool body %s: %v", body, err)
	}
	return sent["provider"]
}

// Sticky on OpenRouter: x-session-id from the session (hashed); once warm on
// one of its providers the session's entry is pinned there with no fallback;
// a failure drops the pin.
func TestOpenRouterPinsTheProviderASessionIsWarmOn(t *testing.T) {
	c := newPoolCaseMode(t, openRouterTarget(), "", "compress")
	c.stub.poolJSON = orAnswer("Novita", 0) // served, nothing cached yet: not warm
	poolSend(t, c.srv, poolBody)
	req, body := c.stub.last("/chat/completions")
	if pinOf(t, body) != nil || req.Header.Get("x-session-id") == "" || strings.Contains(req.Header.Get("x-session-id"), "sess-1") {
		t.Fatalf("first request: body %s, x-session-id %q", body, req.Header.Get("x-session-id"))
	}
	c.stub.poolJSON = orAnswer("Novita", 2800) // a cache read on Novita: warm
	poolSend(t, c.srv, poolBody)
	if _, body := c.stub.last("/chat/completions"); pinOf(t, body) != nil {
		t.Fatalf("second request pinned before it was warm: %s", body)
	}
	poolSend(t, c.srv, poolBody)
	_, body = c.stub.last("/chat/completions")
	if pin, _ := json.Marshal(pinOf(t, body)); string(pin) != `{"allow_fallbacks":false,"order":["Novita"]}` {
		t.Fatalf("warm session not pinned: %s", body)
	}
	// The pinned provider fails: the asked model runs, and the pin goes.
	c.stub.poolCode = 503
	if rec := poolSend(t, c.srv, poolBody); !strings.Contains(rec.Body.String(), "harness says hi") {
		t.Fatalf("fallback: %s", rec.Body.String())
	}
	c.stub.poolCode = 0
	poolSend(t, c.srv, poolBody)
	if _, body := c.stub.last("/chat/completions"); pinOf(t, body) != nil {
		t.Fatalf("pin kept after a failure: %s", body)
	}
	// A stream names its provider in its first event.
	c.stub.poolJSON = ""
	c.stub.poolSSE = "data: {\"id\":\"gen-2\",\"provider\":\"Fireworks\",\"model\":\"moonshotai/kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"gen-2\",\"provider\":\"Fireworks\",\"choices\":[],\"usage\":{\"prompt_tokens\":3000,\"completion_tokens\":1,\"prompt_tokens_details\":{\"cached_tokens\":2900}}}\n\ndata: [DONE]\n\n"
	poolSend(t, c.srv, `{"model":"claude-opus-5-5","max_tokens":50,"stream":true,"messages":[{"role":"user","content":"fix the bug"}]}`)
	poolSend(t, c.srv, poolBody)
	if _, body := c.stub.last("/chat/completions"); !strings.Contains(body, `"order":["Fireworks"]`) {
		t.Fatalf("stream provider not pinned: %s", body)
	}
}

func TestProviderSniffReadsOnlyTheTopLevelOfAWholeAnswer(t *testing.T) {
	// A tool input naming "provider" comes before the answer's own field.
	whole := `{"content":[{"type":"tool_use","input":{"provider":"aws"}}],"usage":{},"provider":"Novita"}`
	sniff := &providerSniff{ReadCloser: io.NopCloser(strings.NewReader(whole))}
	_, _ = io.ReadAll(sniff)
	if got := sniff.provider(); got != "Novita" {
		t.Errorf("provider = %q", got)
	}
}

func TestWithCacheKeyNeverReplacesTheAgentsOwn(t *testing.T) {
	if got := string(withCacheKey([]byte(`{"prompt_cache_key":"mine","input":[]}`), "s")); got != `{"prompt_cache_key":"mine","input":[]}` {
		t.Errorf("replaced: %s", got)
	}
	if got := string(withCacheKey([]byte(`{"input":[]}`), "s")); !strings.HasPrefix(got, `{"input":[],"prompt_cache_key":"`) || strings.Contains(got, `"s"`) {
		t.Errorf("added: %s", got)
	}
}

// fanoutUpstream answers each request after delay, recording when it arrived
// and when its first byte went out.
type fanoutUpstream struct {
	mu       sync.Mutex
	arrived  []time.Time
	answered []time.Time
	delay    time.Duration
	mode     string // the runtime mode; "" is compress
}

func (u *fanoutUpstream) server(t *testing.T) *Server {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		u.mu.Lock()
		u.arrived = append(u.arrived, time.Now())
		delay := u.delay
		u.mu.Unlock()
		time.Sleep(delay)
		u.mu.Lock()
		u.answered = append(u.answered, time.Now())
		u.mu.Unlock()
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{"id":"m","type":"message","model":"claude-sonnet-5-5","content":[],"usage":{"input_tokens":10,"output_tokens":1}}`)
	}))
	t.Cleanup(upstream.Close)
	return New(Config{
		Adapters:   []providers.Adapter{anthropic.New("https://api.anthropic.com")},
		Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: cmp.Or(u.mode, "compress")}},
		Creds:      stubCreds{key: "sk-byok"},
		Sink:       &captureSink{},
		HTTPClient: &http.Client{Transport: toStub(upstream.URL)},
		Cloud:      &fakeCloud{answer: RouteAnswer{Outcome: "kept"}},
	})
}

const childBody = `{"model":"claude-sonnet-5-5","max_tokens":5,"system":[{"type":"text","text":"You are an explore agent.","cache_control":{"type":"ephemeral"}}],"tools":[{"name":"Read"}],"messages":[{"role":"user","content":"look at %d"}]}`

func sendChildren(t *testing.T, srv *Server, n int, agent func(i int) string) {
	t.Helper()
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		body := auto(t, fmt.Sprintf(childBody, i))
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			req.Header.Set("x-api-key", "sk-ant-api-key")
			req.Header.Set("x-claude-code-session-id", "parent-1")
			req.Header.Set("x-claude-code-agent-id", agent(i))
			srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
		}()
	}
	wg.Wait()
}

// Four siblings on one new prefix: one goes, three wait for its first byte.
// A sibling after that finds the prefix warm and waits for nobody.
func TestFanoutSendsOneSiblingFirst(t *testing.T) {
	up := &fanoutUpstream{delay: 300 * time.Millisecond}
	srv := up.server(t)
	sendChildren(t, srv, 4, func(i int) string { return fmt.Sprintf("child-%d", i) })
	if len(up.arrived) != 4 {
		t.Fatalf("upstream saw %d requests", len(up.arrived))
	}
	first := up.answered[0]
	for _, at := range up.arrived[1:] {
		if at.Before(first) {
			t.Fatalf("a sibling reached the upstream before the first one answered: arrivals %v, first answer %v", up.arrived, first)
		}
	}
	start := time.Now()
	up.mu.Lock()
	up.delay = 0
	up.mu.Unlock()
	sendChildren(t, srv, 1, func(int) string { return "child-late" })
	if waited := time.Since(start); waited > fanoutWait/2 {
		t.Errorf("a sibling on a warm prefix waited %v", waited)
	}
}

// The wait is bounded: a leader that never answers releases nobody, and the
// siblings go after fanoutWait; a cancelled sibling stops waiting at once.
func TestFanoutWaitIsBounded(t *testing.T) {
	defer func(wait time.Duration) { fanoutWait = wait }(fanoutWait)
	fanoutWait = 100 * time.Millisecond
	var f fanout
	key := [32]byte{1}
	release := f.enter(context.Background(), key)
	start := time.Now()
	f.enter(context.Background(), key)(true)
	if waited := time.Since(start); waited < fanoutWait || waited > 10*fanoutWait {
		t.Errorf("follower waited %v", waited)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start = time.Now()
	f.enter(ctx, key)
	if waited := time.Since(start); waited > fanoutWait/2 {
		t.Errorf("a cancelled follower waited %v", waited)
	}
	release(false) // a failed leader leaves the prefix cold
	release(true)  // and releasing twice changes nothing
	if _, warm := f.warm[key]; warm {
		t.Error("a failed leader marked the prefix warm")
	}
	// A forked child (history with an assistant turn) and a main session never wait.
	if _, ok := fanoutKey("", "anthropic", "m", translate.Messages, []byte(fmt.Sprintf(childBody, 1))); ok {
		t.Error("a main-session request was gated")
	}
	forked := `{"system":"s","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`
	if _, ok := fanoutKey("parent", "anthropic", "m", translate.Messages, []byte(forked)); ok {
		t.Error("a forked child was gated")
	}
}

// Context tokens: the provider's own count per byte, from the parent session
// for a child.
func TestContextTokensUseTheParentsTokensPerByte(t *testing.T) {
	cloud := &fakeCloud{answer: RouteAnswer{Outcome: "kept"}}
	srv, _ := effortServer(t, cloud, func([]byte) (int, string) {
		return 200, `{"id":"m","type":"message","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":40,"output_tokens":2}}`
	})
	parent := convo("high", uA, aB, uC)
	post(t, srv, parent, nil)
	if got := cloud.asks[0].ContextTokens; got != 0 {
		t.Fatalf("first ask: context tokens %d, want none (no count yet)", got)
	}
	child := convo("high", uA, aB, uC, aE, uF)
	post(t, srv, child, map[string]string{"x-claude-code-agent-id": "a1"})
	want := int(float64(len(child)) * 40 / float64(len(parent)))
	if got := cloud.asks[1].ContextTokens; got != want {
		t.Errorf("child: context tokens %d, want %d", got, want)
	}
}

// A child's cache affinity is its parent's: siblings and forked children
// land on the machine (or OpenRouter provider) that holds the family's prefix.
func TestChildrenShareTheirParentsAffinity(t *testing.T) {
	for _, mode := range []string{"compress", "record"} {
		c := newPoolCaseMode(t, localTarget("api.openai.com"), "", mode)
		poolSend(t, c.srv, poolBody)
		parent, _ := c.stub.last("/chat/completions")
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(auto(t, poolBody)))
		req.Header.Set("x-api-key", "sk-ant-api-key")
		req.Header.Set("x-claude-code-session-id", "sess-1")
		req.Header.Set("x-claude-code-agent-id", "child-1")
		c.srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
		child, _ := c.stub.last("/chat/completions")
		shared := child.Header.Get("x-session-affinity") == parent.Header.Get("x-session-affinity")
		// Record mode keeps the child's own, as before the cache mechanics.
		if parent.Header.Get("x-session-affinity") == "" || child.Header.Get("x-session-affinity") == "" || shared != (mode == "compress") {
			t.Errorf("%s: affinity parent %q, child %q", mode, parent.Header.Get("x-session-affinity"), child.Header.Get("x-session-affinity"))
		}
	}
}

// An event stream releases the leader at its first content event: comments,
// pings and the openings sent before the prompt is read wait; an error
// releases as not warm.
func TestFanoutReleasesAtTheFirstContentEvent(t *testing.T) {
	for _, c := range []struct {
		name, stream string
		at           int // bytes read when released, -1 never before the end
		warm         bool
	}{
		{"anthropic", "event: message_start\ndata: {\"type\":\"message_start\"}\n\nevent: ping\ndata: {\"type\": \"ping\"}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\"}\n\n", 3, true},
		{"responses", "event: response.created\ndata: {\"type\":\"response.created\"}\n\nevent: response.in_progress\ndata: {\"type\":\"response.in_progress\"}\n\nevent: response.output_item.added\ndata: {\"type\":\"response.output_item.added\"}\n\n", 3, true},
		{"openrouter comment", ": OPENROUTER PROCESSING\r\n\r\ndata: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\r\n\r\n", 2, true},
		{"error", "event: message_start\ndata: {\"type\":\"message_start\"}\n\nevent: error\ndata: {\"type\":\"error\"}\n\n", 2, false},
	} {
		events := strings.SplitAfter(c.stream, "\n\n")
		if strings.Contains(c.stream, "\r\n\r\n") {
			events = strings.SplitAfter(c.stream, "\r\n\r\n")
		}
		released, warm, reads := -1, false, 0
		body := &releaseOnRead{ReadCloser: io.NopCloser(io.MultiReader(func() []io.Reader {
			var readers []io.Reader
			for _, e := range events {
				if e != "" {
					readers = append(readers, strings.NewReader(e))
				}
			}
			return readers
		}()...)), ok: true, stream: true, release: func(ok bool) {
			if released < 0 {
				released, warm = reads, ok
			}
		}}
		buf := make([]byte, 1<<10)
		for {
			reads++
			if _, err := body.Read(buf); err != nil {
				break
			}
		}
		if released != c.at || warm != c.warm {
			t.Errorf("%s: released at read %d (warm %v), want %d (warm %v)", c.name, released, warm, c.at, c.warm)
		}
	}
}

// Siblings with different prompt_cache_keys may land on different OpenAI
// machines: they never wait on each other.
func TestFanoutKeyIncludesThePromptCacheKey(t *testing.T) {
	body := func(key string) []byte {
		return []byte(`{"model":"gpt-6-sol","instructions":"You are a worker.","tools":[{"type":"function","name":"shell"}],"prompt_cache_key":"` + key + `","input":[{"role":"user","content":"x"}]}`)
	}
	a, okA := fanoutKey("parent", "openai", "gpt-6-sol", translate.Responses, body("thread-a"))
	b, okB := fanoutKey("parent", "openai", "gpt-6-sol", translate.Responses, body("thread-b"))
	same, _ := fanoutKey("parent", "openai", "gpt-6-sol", translate.Responses, body("thread-a"))
	if !okA || !okB || a == b || a != same {
		t.Fatalf("gated %v/%v, different keys share a gate %v", okA, okB, a == b)
	}
	if _, ok := fanoutKey("parent", "openai", "gpt-6-sol", translate.Responses, []byte(`{"prompt_cache_key":"k","input":[]}`)); ok {
		t.Error("a key alone is no shared prefix")
	}
}

// A pinned provider that fails: once more on the same OpenRouter entry
// without the pin, so the request stays on the pool model.
func TestPinnedFailureRetriesTheEntryUnpinned(t *testing.T) {
	c := newPoolCaseMode(t, openRouterTarget(), "", "compress")
	c.stub.poolJSON = orAnswer("Novita", 2800)
	poolSend(t, c.srv, poolBody)
	poolSend(t, c.srv, poolBody) // pinned from here
	c.stub.poolCode, c.stub.poolFailPinned = 503, true
	before := len(c.stub.bodies["/chat/completions"])
	rec := poolSend(t, c.srv, poolBody)
	if !strings.Contains(rec.Body.String(), "hi") || strings.Contains(rec.Body.String(), "harness says hi") {
		t.Fatalf("answer: %s", rec.Body.String())
	}
	sent := c.stub.bodies["/chat/completions"][before:]
	if len(sent) != 2 || pinOf(t, sent[0]) == nil || pinOf(t, sent[1]) != nil {
		t.Fatalf("sends after the pinned failure: %v", sent)
	}
	if req, _ := c.stub.last("/v1/messages"); req != nil {
		t.Error("the asked model ran too")
	}
}

// A transport error on a pinned send may come after OpenRouter read the
// request: never sent to OpenRouter again; the pin goes and the asked model runs.
func TestPinnedTransportErrorIsNeverResent(t *testing.T) {
	c := newPoolCaseMode(t, openRouterTarget(), "", "compress")
	c.stub.poolJSON = orAnswer("Novita", 2800)
	poolSend(t, c.srv, poolBody)
	poolSend(t, c.srv, poolBody) // pinned from here
	var orSends atomic.Int32
	inner := c.srv.httpClient.Transport
	c.srv.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "openrouter.ai" {
			_, _ = io.ReadAll(r.Body) // the request reached the host
			orSends.Add(1)
			return nil, errors.New("read tcp: connection reset by peer")
		}
		return inner.RoundTrip(r)
	})}
	before := len(c.stub.bodies["/v1/messages"])
	rec := poolSend(t, c.srv, poolBody)
	if orSends.Load() != 1 || len(c.stub.bodies["/v1/messages"])-before != 1 || !strings.Contains(rec.Body.String(), "harness says hi") {
		t.Fatalf("OpenRouter sends %d, harness sends %d: %s", orSends.Load(), len(c.stub.bodies["/v1/messages"])-before, rec.Body.String())
	}
	if pin := c.srv.routes.pinned("sess-1", "openrouter/kimi-k3"); pin != "" {
		t.Errorf("pin kept after a transport error: %q", pin)
	}
}

// Record mode gets none of the cache mechanics: no pin, no prompt_cache_key,
// no family affinity (above), no fan-out wait.
func TestRecordModeGetsNoCacheMechanics(t *testing.T) {
	c := newPoolCase(t, openRouterTarget(), "") // record
	c.stub.poolJSON = orAnswer("Novita", 2800)
	for range 3 {
		poolSend(t, c.srv, poolBody)
	}
	if _, body := c.stub.last("/chat/completions"); pinOf(t, body) != nil {
		t.Errorf("record mode pinned: %s", body)
	}

	up := &fanoutUpstream{delay: 300 * time.Millisecond, mode: "record"}
	srv := up.server(t)
	sendChildren(t, srv, 3, func(i int) string { return fmt.Sprintf("child-%d", i) })
	for _, at := range up.arrived {
		if !at.Before(up.answered[0]) {
			t.Fatalf("record mode held a sibling: arrivals %v, first answer %v", up.arrived, up.answered[0])
		}
	}
}

// The prompt_cache_key on an OpenAI pool login: compress mode adds the
// session's, record mode sends none.
func TestPoolOpenAILoginCacheKeyByMode(t *testing.T) {
	for _, mode := range []string{"compress", "record"} {
		header := http.Header{}
		header.Set("authorization", "Bearer sk-openai")
		target := &RouteTarget{PoolID: "openai/gpt-6.1-sol", Via: "local", Host: "openai", Model: "gpt-6.1-sol", Wire: translate.Responses,
			URL: "https://api.openai.com/v1/responses", Header: header, Translate: translate.Options{Model: "gpt-6.1-sol", Route: "openai"}}
		c := newPoolCaseMode(t, target, "", mode)
		c.stub.respSSE = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"gpt says hi\"}\n\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\"}}\n\n"
		rec := poolSend(t, c.srv, `{"model":"claude-opus-5-5","max_tokens":50,"stream":true,"messages":[{"role":"user","content":"fix the bug"}]}`)
		_, body := c.stub.last("/v1/responses")
		keyed := strings.Contains(body, `"prompt_cache_key":"`+openai.SessionCacheKey("sess-1")+`"`)
		if !strings.Contains(rec.Body.String(), "gpt says hi") || keyed != (mode == "compress") {
			t.Errorf("%s: keyed %v, body %s", mode, keyed, body)
		}
	}
}
