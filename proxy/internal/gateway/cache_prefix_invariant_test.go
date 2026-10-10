package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/compressors"
	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

// The cache-prefix invariant (#1105): caveman must never change bytes the
// provider already cached.
//
// For accepted requests P then R, let k be P's last cache_control-marked
// message. If R's client bytes over system, tools and messages[0..k]
// (cache_control stripped) equal P's, R's FORWARDED bytes over that range must
// equal P's forwarded bytes.
//
// The one exception is a request the provider rejected in transformed form and
// accepted through the raw retry. That request may differ from what it
// extends, and the provider now holds its range in two forms: raw, and the
// replaced form earlier requests cached. A later request follows the LONGEST
// lineage it extends. When a raw retry that cached the conversation's own bytes
// (its first message onward) is at least as long as any replaced prefix the
// request extends, the request is held to the raw form and to nothing
// replaced; otherwise it is held to the replaced form and to nothing in the raw
// lineage (the retry, and requests that went out raw extending it). A retry
// that cached only system and tools re-bases nothing: every other conversation
// of the agent keeps that range warm in the replaced form.
//
// The scenarios below drive Claude Code shaped traffic through Server.Handler
// and check every pair of accepted requests against that rule.

// exchange is one client request as the provider accepted it.
type exchange struct {
	client    []byte
	forwarded []byte
	// rawRetry: the transformed attempt was rejected and the raw retry accepted.
	rawRetry bool
	// streamRaw: a PAYG request without MCP recovery that streamed. The
	// server-side retrieve tool cannot ride a stream, so it goes out raw, and a
	// conversation compressed on earlier turns re-caches raw from it: the
	// invariant's second exception, held to like a raw retry.
	streamRaw bool
	// group > 0 marks requests that were in flight together. The provider may
	// have processed either one first, so the rule is checked both ways.
	group int
	// wholePrompt: an OpenAI request, which the provider caches whole.
	wholePrompt bool
}

type prefixView struct {
	client, forwarded [][]byte
	cached            int
}

func cachedPrefixView(t testing.TB, x exchange) prefixView {
	t.Helper()
	split := anthropic.CachedPrefixComponents
	if x.wholePrompt {
		split = wholePromptComponents
	}
	client, cached, ok := split(x.client)
	if !ok {
		t.Fatalf("client body has no cached prefix: %.300s", x.client)
	}
	forwarded, forwardedCached, ok := split(x.forwarded)
	if !ok || len(forwarded) != len(client) {
		t.Fatalf("forwarded body does not mirror the client's components (ok=%v %d vs %d): %.300s", ok, len(forwarded), len(client), x.forwarded)
	}
	if forwardedCached < cached {
		// The provider writes no entry at a breakpoint caveman removed, so the
		// next turn has none to read.
		t.Errorf("the client asked the provider to cache %d components, the forwarded request caches %d: a breakpoint was dropped", cached, forwardedCached)
	}
	return prefixView{client: client, forwarded: forwarded, cached: cached}
}

// extendsRange reports whether components starts with prefix[:n].
func extendsRange(components, prefix [][]byte, n int) bool {
	if len(components) < n || len(prefix) < n {
		return false
	}
	for i := 0; i < n; i++ {
		if !bytes.Equal(components[i], prefix[i]) {
			return false
		}
	}
	return true
}

func assertCachedPrefixPreserved(t testing.TB, xs []exchange) {
	t.Helper()
	views := make([]prefixView, len(xs))
	for i, x := range xs {
		views[i] = cachedPrefixView(t, x)
	}
	for j := range xs {
		if rawForm(xs[j]) {
			continue // the exception itself: it may differ from what it extends
		}
		followsRaw := followsRawLineage(xs, views, j)
		for i := range xs {
			if i >= j && !sameGroup(xs, i, j) {
				continue
			}
			p, r := views[i], views[j]
			if !extendsRange(r.client, p.client, p.cached) {
				continue
			}
			if followsRaw && replacedForm(p) || !followsRaw && inRawLineage(xs, views, i) {
				continue // the other form of a range a raw retry split
			}
			for c := 0; c < p.cached; c++ {
				if !bytes.Equal(r.forwarded[c], p.forwarded[c]) {
					t.Errorf("request %d changed bytes request %d cached: component %d of %d differs although the client sent the same bytes\n  cached:  %.240q\n  re-sent: %.240q",
						j, i, c, p.cached, p.forwarded[c], r.forwarded[c])
					break
				}
			}
		}
	}
}

// sameGroup reports whether two requests were in flight together. The
// provider may have processed either first, so the rule is checked both ways
// between them, and neither counts toward the other's lineage.
func sameGroup(xs []exchange, a, b int) bool {
	return a != b && xs[a].group > 0 && xs[a].group == xs[b].group
}

// replacedForm reports whether P's cached range went out other than the client
// sent it.
func replacedForm(p prefixView) bool {
	return !extendsRange(p.forwarded, p.client, p.cached)
}

// rawForm reports whether x is one of the invariant's exceptions: a raw
// retry, or a PAYG stream that had to go out raw.
func rawForm(x exchange) bool { return x.rawRetry || x.streamRaw }

// rawAnchor reports whether k is an exception that cached the conversation's
// own bytes, the only kind that re-bases what extends it.
func rawAnchor(xs []exchange, views []prefixView, k int) bool {
	return rawForm(xs[k]) && views[k].cached >= conversationComponents
}

// followsRawLineage reports whether R (j) is held to the raw form: the longest
// raw anchor it extends is at least as long as the longest prefix it extends
// that went out replaced. A request in flight with R counts only as far as R
// followed it: the provider may have accepted it before or after R's bytes
// were decided, and either is right.
func followsRawLineage(xs []exchange, views []prefixView, j int) bool {
	rawLen, repLen := 0, 0
	for k := range xs {
		concurrent := sameGroup(xs, k, j)
		if k == j || (k > j && !concurrent) || views[k].cached < conversationComponents || !extendsRange(views[j].client, views[k].client, views[k].cached) {
			continue
		}
		followed := extendsRange(views[j].forwarded, views[k].forwarded, views[k].cached)
		switch {
		case rawForm(xs[k]) && (!concurrent || followed):
			rawLen = max(rawLen, views[k].cached)
		case !rawForm(xs[k]) && replacedForm(views[k]) && (!concurrent || followed):
			repLen = max(repLen, views[k].cached)
		}
	}
	return rawLen > 0 && rawLen >= repLen
}

// inRawLineage reports whether P (i) went out in the raw form of a range a raw
// retry split: it is a raw retry, or it went out raw extending a raw anchor
// (one in flight with it only if it followed it, as in followsRawLineage).
func inRawLineage(xs []exchange, views []prefixView, i int) bool {
	if rawForm(xs[i]) {
		return true
	}
	if replacedForm(views[i]) {
		return false
	}
	for k := range xs {
		concurrent := sameGroup(xs, k, i)
		if k == i || (k > i && !concurrent) || !rawAnchor(xs, views, k) || !extendsRange(views[i].client, views[k].client, views[k].cached) {
			continue
		}
		if !concurrent || extendsRange(views[i].forwarded, views[k].forwarded, views[k].cached) {
			return true
		}
	}
	return false
}

// --- the simulated client ---------------------------------------------------------

func jsonText(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func textBlock(s string) string { return `{"type":"text","text":` + jsonText(s) + `}` }

func toolUseBlock(id string) string {
	return `{"type":"tool_use","id":"` + id + `","name":"Read","input":{"path":"f"}}`
}

func toolResultBlock(id, content string) string {
	return `{"type":"tool_result","tool_use_id":"` + id + `","content":` + jsonText(content) + `}`
}

// filler is a block big enough to be a compression candidate.
func filler(label string) string { return strings.Repeat(label+" ", 600/(len(label)+1)+1) }

type ccMessage struct {
	role   string
	blocks []string
}

func (m ccMessage) json(marked bool) string {
	blocks := append([]string(nil), m.blocks...)
	if marked {
		last := blocks[len(blocks)-1]
		blocks[len(blocks)-1] = last[:len(last)-1] + `,"cache_control":{"type":"ephemeral"}}`
	}
	return `{"role":"` + m.role + `","content":[` + strings.Join(blocks, ",") + `]}`
}

// ccConversation builds Claude Code shaped requests: a marked system prompt and
// a marker on the newest message, which moves forward every turn. systemOnly
// leaves the messages unmarked, so the request caches only system and tools
// (a classifier or title side request).
type ccConversation struct {
	system     string
	model      string
	session    string
	messages   []ccMessage
	tools      int
	systemOnly bool
	// stream asks for a streamed response.
	stream bool
}

func newCCConversation(system, session string) *ccConversation {
	return &ccConversation{system: system, model: "claude-sonnet-4-6", session: session}
}

func (c *ccConversation) clone() *ccConversation {
	out := *c
	out.messages = append([]ccMessage(nil), c.messages...)
	return &out
}

func (c *ccConversation) reply() {
	if n := len(c.messages); n > 0 && c.messages[n-1].role == "user" {
		c.messages = append(c.messages, ccMessage{role: "assistant", blocks: []string{textBlock("answer " + strconv.Itoa(n))}})
	}
}

// user appends a user turn.
func (c *ccConversation) user(text string) *ccConversation {
	c.reply()
	c.messages = append(c.messages, ccMessage{role: "user", blocks: []string{textBlock(text)}})
	return c
}

// toolResult appends a tool call and its result.
func (c *ccConversation) toolResult(content string) *ccConversation {
	c.reply()
	c.tools++
	id := "toolu_" + strconv.Itoa(c.tools)
	c.messages = append(c.messages,
		ccMessage{role: "assistant", blocks: []string{toolUseBlock(id)}},
		ccMessage{role: "user", blocks: []string{toolResultBlock(id, content)}})
	return c
}

func (c *ccConversation) body() []byte {
	parts := make([]string, len(c.messages))
	for i, m := range c.messages {
		parts[i] = m.json(i == len(c.messages)-1 && !c.systemOnly)
	}
	stream := ""
	if c.stream {
		stream = `"stream":true,`
	}
	return []byte(`{"model":"` + c.model + `","max_tokens":1024,` + stream +
		`"system":[{"type":"text","text":` + jsonText(c.system) + `,"cache_control":{"type":"ephemeral"}}],` +
		`"tools":[{"name":"Read","description":"Read a file","input_schema":{"type":"object","title":"Read args"}}],` +
		`"messages":[` + strings.Join(parts, ",") + `]}`)
}

// --- the harness ------------------------------------------------------------------

// invariantCompressor compresses deterministically by content. With
// declineFirst it turns a block down the first time it sees it and compresses
// it afterwards, the way a query-aware compressor or a failed recovery write
// can; failStore makes the recovery write fail.
type invariantCompressor struct {
	mu           sync.Mutex
	nonce        string
	declineFirst bool
	failStore    bool
	seen         map[string]bool
}

func (c *invariantCompressor) CompressSegment(seg []byte) ([]byte, int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := contentHandle(seg)
	if c.declineFirst && !c.seen[key] {
		if c.seen == nil {
			c.seen = map[string]bool{}
		}
		c.seen[key] = true
		return nil, 0, 0
	}
	return []byte("CMP" + c.nonce + ":" + key), 100, 40
}

func (c *invariantCompressor) StoreOriginal(body []byte) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failStore {
		return "", errTestPrefixCacheDown
	}
	return contentHandle(body), nil
}

func (c *invariantCompressor) StripToolSchema(tools []byte) ([]byte, bool) {
	return compressors.StripToolSchemaAnnotations(tools)
}

// RetrieveOriginal serves the server-side retrieve loop of the PAYG path; the
// harness never makes the model call it.
func (c *invariantCompressor) RetrieveOriginal(handle, query string) ([]byte, error) {
	return nil, errTestPrefixCacheDown
}

type invariantTagKey struct{}

type upstreamAttempt struct {
	body   []byte
	status int
}

// respondFunc decides one upstream attempt's status and headers.
type respondFunc func(attempt int, body []byte) (int, http.Header)

type invariantTransport struct {
	mu       sync.Mutex
	attempts map[int][]upstreamAttempt
	respond  map[int]respondFunc
	holds    map[int]*upstreamHold
}

// upstreamHold parks a request's first upstream attempt: entered closes when
// it reaches the provider, and the provider answers once release is closed.
type upstreamHold struct {
	entered, release chan struct{}
}

func newUpstreamHold() *upstreamHold {
	return &upstreamHold{entered: make(chan struct{}), release: make(chan struct{})}
}

func (t *invariantTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	tag, _ := r.Context().Value(invariantTagKey{}).(int)
	t.mu.Lock()
	hold := t.holds[tag]
	delete(t.holds, tag)
	t.mu.Unlock()
	if hold != nil {
		close(hold.entered)
		<-hold.release
	}
	t.mu.Lock()
	attempt := len(t.attempts[tag])
	status, header := http.StatusOK, http.Header{}
	if f := t.respond[tag]; f != nil {
		status, header = f(attempt, body)
		if header == nil {
			header = http.Header{}
		}
	}
	t.attempts[tag] = append(t.attempts[tag], upstreamAttempt{body: body, status: status})
	t.mu.Unlock()
	header.Set("Content-Type", "application/json")
	payload := subMessageRespBody
	if status >= 400 {
		payload = `{"type":"error","error":{"type":"rate_limit_error","message":"Error"}}`
	}
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: header, Body: io.NopCloser(strings.NewReader(payload)), Request: r}, nil
}

type invariantHarness struct {
	t     testing.TB
	comp  *invariantCompressor
	cache *testPrefixCache
	rt    *invariantTransport
	srv   *Server
	sink  *captureSink
	// strip turns on the tool-schema annotation strip.
	strip bool
	// payg sends API-key requests to a proxy without MCP recovery: compress
	// mode then injects its server-side retrieve tool, on non-streaming turns.
	payg bool

	mu   sync.Mutex
	tag  int
	xs   []exchange
	sent int
}

func newInvariantHarness(t testing.TB) *invariantHarness {
	h := &invariantHarness{
		t:     t,
		comp:  &invariantCompressor{},
		cache: newTestPrefixCache(),
		rt:    &invariantTransport{attempts: map[int][]upstreamAttempt{}, respond: map[int]respondFunc{}, holds: map[int]*upstreamHold{}},
		sink:  &captureSink{},
	}
	h.restart("")
	return h
}

// restart replaces the proxy process: in-memory state is gone, the durable
// replacement cache survives, and the engine may now compress differently.
func (h *invariantHarness) restart(nonce string) {
	h.comp.mu.Lock()
	h.comp.nonce = nonce
	h.comp.mu.Unlock()
	strip := ""
	if h.strip {
		strip = toolSchemaStripMode
	}
	h.srv = New(Config{
		Adapters:        []providers.Adapter{anthropic.New("https://upstream.test")},
		Auth:            stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}},
		Creds:           passthroughTestCreds{},
		Sink:            h.sink,
		Compressor:      h.comp,
		PrefixCache:     h.cache,
		HTTPClient:      &http.Client{Transport: h.rt},
		RecoveryViaMCP:  !h.payg,
		ToolSchemaStrip: strip,
	})
}

type sendOpts struct {
	respond respondFunc
	group   int
	hold    *upstreamHold
}

// send serves one client request and records it if the provider accepted it.
func (h *invariantHarness) send(c *ccConversation, o sendOpts) (int, []upstreamAttempt) {
	return h.sendBody(c.body(), c.session, o)
}

func (h *invariantHarness) sendBody(body []byte, session string, o sendOpts) (int, []upstreamAttempt) {
	h.mu.Lock()
	h.tag++
	tag := h.tag
	srv := h.srv
	h.mu.Unlock()
	h.rt.mu.Lock()
	if o.respond != nil {
		h.rt.respond[tag] = o.respond
	}
	if o.hold != nil {
		h.rt.holds[tag] = o.hold
	}
	h.rt.mu.Unlock()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), invariantTagKey{}, tag))
	headers := subscriptionAgentHeaders
	if h.payg {
		headers = map[string]string{"x-api-key": "sk-ant-api03-invariant", "anthropic-version": "2023-06-01"}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if session != "" {
		req.Header.Set("x-cave-session", session)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	h.rt.mu.Lock()
	attempts := h.rt.attempts[tag]
	h.rt.mu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sent++
	if rec.Code < 400 && len(attempts) > 0 && attempts[len(attempts)-1].status < 400 {
		h.xs = append(h.xs, exchange{
			client:    body,
			forwarded: attempts[len(attempts)-1].body,
			rawRetry:  len(attempts) > 1,
			// A PAYG stream is raw only when it went out as sent: one carrying
			// the caveman MCP tool compresses on the marker path instead.
			streamRaw: h.payg && bytes.Contains(body, []byte(`"stream":true`)) && bytes.Equal(attempts[len(attempts)-1].body, body),
			group:     o.group,
		})
	}
	return rec.Code, attempts
}

func (h *invariantHarness) assert() {
	h.t.Helper()
	assertCachedPrefixPreserved(h.t, h.xs)
	// The runtime tripwire must agree: a run the invariant accepts has no bust
	// caveman caused.
	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()
	for i, row := range h.sink.rows {
		if row.CacheBustCause == bustCauseCaveman {
			h.t.Errorf("tripwire reported a caveman bust on recorded request %d of %d", i, len(h.sink.rows))
		}
	}
}

// compressedSomething guards against a scenario passing vacuously.
func (h *invariantHarness) compressedSomething() {
	h.t.Helper()
	for _, x := range h.xs {
		if bytes.Contains(x.forwarded, []byte("<<ccr:")) {
			return
		}
	}
	h.t.Fatal("scenario never compressed anything, so it proves nothing")
}

func rejectTransformed(status int, header http.Header) respondFunc {
	return func(attempt int, body []byte) (int, http.Header) {
		if attempt == 0 && bytes.Contains(body, []byte("<<ccr:")) {
			return status, header.Clone()
		}
		return http.StatusOK, nil
	}
}

func rateLimitResponse(attempt int, body []byte) (int, http.Header) {
	if attempt == 0 {
		return http.StatusTooManyRequests, http.Header{"Retry-After": {"7"}, "Anthropic-Ratelimit-Requests-Remaining": {"0"}}
	}
	return http.StatusOK, nil
}

// --- scenarios --------------------------------------------------------------------

func TestCachePrefixInvariant(t *testing.T) {
	const session = "sess-1105"
	for _, tc := range []struct {
		name string
		run  func(t *testing.T, h *invariantHarness)
	}{
		{"ten turns with a moving marker", func(t *testing.T, h *invariantHarness) {
			main := newCCConversation("You are Claude Code.", session)
			for i := 1; i <= 10; i++ {
				h.send(main.user(filler("main turn "+strconv.Itoa(i))), sendOpts{})
			}
		}},
		{"main, subagents, side request and fork under one session", func(t *testing.T, h *invariantHarness) {
			interleaved(h, session)
		}},
		{"main, subagents, side request and fork uncorrelated", func(t *testing.T, h *invariantHarness) {
			interleaved(h, "")
		}},
		{"rewind and edit", func(t *testing.T, h *invariantHarness) {
			main := newCCConversation("You are Claude Code.", session)
			h.send(main.user(filler("u1")), sendOpts{})
			h.send(main.user(filler("u2")), sendOpts{})
			rewindPoint := main.clone()
			h.send(main.user(filler("u3")), sendOpts{})
			// Esc-Esc: back to after u1's answer, a different second prompt.
			rewound := rewindPoint
			rewound.messages = rewound.messages[:len(rewound.messages)-1]
			h.send(rewound.user(filler("u2 edited")), sendOpts{})
			h.send(rewound.user(filler("u4")), sendOpts{})
		}},
		{"client clears an old tool_result", func(t *testing.T, h *invariantHarness) {
			main := newCCConversation("You are Claude Code.", session)
			h.send(main.user(filler("read the files")), sendOpts{})
			for i := 1; i <= 3; i++ {
				h.send(main.toolResult(filler("file "+strconv.Itoa(i))), sendOpts{})
			}
			// Microcompaction rewrites the first tool_result in place.
			for i, m := range main.messages {
				if strings.Contains(m.blocks[0], `"tool_result"`) {
					main.messages[i].blocks[0] = toolResultBlock("toolu_1", "[Old tool result content cleared]")
					break
				}
			}
			h.send(main.user(filler("keep going")), sendOpts{})
			h.send(main.toolResult(filler("file 4")), sendOpts{})
		}},
		{"compact then continue", func(t *testing.T, h *invariantHarness) {
			main := newCCConversation("You are Claude Code.", session)
			for i := 1; i <= 3; i++ {
				h.send(main.toolResult(filler("before compact "+strconv.Itoa(i))), sendOpts{})
			}
			h.send(main.clone().user(filler("summarize the conversation so far")), sendOpts{})
			after := newCCConversation("You are Claude Code.", session)
			h.send(after.user(filler("summary of the earlier conversation")), sendOpts{})
			h.send(after.toolResult(filler("after compact")), sendOpts{})
		}},
		{"model switch", func(t *testing.T, h *invariantHarness) {
			main := newCCConversation("You are Claude Code.", session)
			h.send(main.user(filler("sonnet 1")), sendOpts{})
			h.send(main.user(filler("sonnet 2")), sendOpts{})
			main.model = "claude-opus-4-8"
			h.send(main.user(filler("opus 3")), sendOpts{})
			main.model = "claude-sonnet-4-6"
			h.send(main.user(filler("sonnet 4")), sendOpts{})
		}},
		{"proxy restart with the same cache", func(t *testing.T, h *invariantHarness) {
			main := newCCConversation("You are Claude Code.", session)
			h.send(main.user(filler("before restart 1")), sendOpts{})
			h.send(main.toolResult(filler("before restart 2")), sendOpts{})
			h.restart("v2")
			h.send(main.user(filler("after restart 1")), sendOpts{})
			h.send(main.toolResult(filler("after restart 2")), sendOpts{})
		}},
		{"first-sight decline then accept", func(t *testing.T, h *invariantHarness) {
			shared := filler("the same file read by two conversations")
			b := newCCConversation("You are subagent B.", session)
			h.comp.declineFirst = true // B's first sight of the file is declined
			h.send(b.toolResult(shared), sendOpts{})
			h.comp.declineFirst = false
			a := newCCConversation("You are subagent A.", session)
			h.send(a.toolResult(shared), sendOpts{})
			h.send(b.user(filler("b next")), sendOpts{})
			h.send(a.user(filler("a next")), sendOpts{})
			h.send(b.user(filler("b last")), sendOpts{})
		}},
		{"memo write failure on one turn", func(t *testing.T, h *invariantHarness) {
			main := newCCConversation("You are Claude Code.", session)
			h.send(main.user(filler("w1")), sendOpts{})
			h.send(main.user(filler("w2")), sendOpts{})
			h.cache.mu.Lock()
			h.cache.failWrites = true
			h.cache.mu.Unlock()
			h.send(main.user(filler("w3 while the store is down")), sendOpts{})
			h.cache.mu.Lock()
			h.cache.failWrites = false
			h.cache.mu.Unlock()
			// The client re-sends the accepted turn (an aborted stream, a retry):
			// the block it carried raw is live again and must stay raw.
			h.send(main, sendOpts{})
			h.send(main.user(filler("w4")), sendOpts{})
			h.send(main.user(filler("w5")), sendOpts{})
		}},
		{"a retried turn after a restart re-sends the stored replacement", func(t *testing.T, h *invariantHarness) {
			// The client re-sends turn 1 (an aborted stream) to a restarted proxy
			// whose engine now compresses the block differently, and the lookup
			// fails. The store answers the write with the row on record, and
			// that row, not this process's own candidate, is what goes out.
			main := newCCConversation("You are Claude Code.", session).user(filler("retried turn"))
			h.send(main, sendOpts{})
			h.restart("v2")
			h.cache.mu.Lock()
			h.cache.failLookups = true
			h.cache.mu.Unlock()
			h.send(main, sendOpts{})
		}},
		{"memo lookup error on one turn", func(t *testing.T, h *invariantHarness) {
			main := newCCConversation("You are Claude Code.", session)
			h.send(main.user(filler("l1")), sendOpts{})
			h.send(main.user(filler("l2")), sendOpts{})
			h.cache.mu.Lock()
			h.cache.failLookups = true
			h.cache.mu.Unlock()
			h.send(main.user(filler("l3 while lookups fail")), sendOpts{})
			h.cache.mu.Lock()
			h.cache.failLookups = false
			h.cache.mu.Unlock()
			h.send(main.user(filler("l4")), sendOpts{})
		}},
		{"another conversation's double fault leaves a stored row alone", func(t *testing.T, h *invariantHarness) {
			shared := filler("a file both conversations read")
			a := newCCConversation("You are Claude Code.", "sess-a")
			h.send(a.toolResult(shared), sendOpts{}) // compressed and stored
			h.send(a.user(filler("a two")), sendOpts{})
			// C first sees the file below its cache floor while the store can
			// neither read nor write: C sends it raw and cannot record that.
			c := newCCConversation("You are another conversation.", "sess-c")
			c.toolResult(shared)
			c.user(filler("c two"))
			h.cache.mu.Lock()
			h.cache.failLookups, h.cache.failWrites = true, true
			h.cache.mu.Unlock()
			h.send(c, sendOpts{})
			h.cache.mu.Lock()
			h.cache.failLookups, h.cache.failWrites = false, false
			h.cache.mu.Unlock()
			h.send(a.user(filler("a three")), sendOpts{}) // A never saw the outage
		}},
		{"recovery store failure on one turn", func(t *testing.T, h *invariantHarness) {
			main := newCCConversation("You are Claude Code.", session)
			h.send(main.user(filler("s1")), sendOpts{})
			h.comp.mu.Lock()
			h.comp.failStore = true
			h.comp.mu.Unlock()
			h.send(main.user(filler("s2 while CCR is down")), sendOpts{})
			h.comp.mu.Lock()
			h.comp.failStore = false
			h.comp.mu.Unlock()
			h.send(main, sendOpts{}) // the client re-sends the accepted turn
			h.send(main.user(filler("s3")), sendOpts{})
			h.send(main.user(filler("s4")), sendOpts{})
		}},
		{"rate limit 429 with retry-after", func(t *testing.T, h *invariantHarness) {
			main := newCCConversation("You are Claude Code.", session)
			h.send(main.user(filler("r1")), sendOpts{})
			main.user(filler("r2"))
			status, attempts := h.send(main, sendOpts{respond: rateLimitResponse})
			if status != http.StatusTooManyRequests || len(attempts) != 1 {
				t.Fatalf("a rate limit must reach the client unreplayed: status=%d upstream attempts=%d", status, len(attempts))
			}
			_, again := h.send(main, sendOpts{})
			if len(again) != 1 || !bytes.Equal(again[0].body, attempts[0].body) {
				t.Fatal("the client's retry must reproduce the rate-limited request's transformed bytes")
			}
			h.send(main.user(filler("r3")), sendOpts{})
		}},
		{"opaque 429 accepted raw", func(t *testing.T, h *invariantHarness) {
			rawRetryScenario(t, h, session, http.StatusTooManyRequests)
		}},
		{"400 accepted raw", func(t *testing.T, h *invariantHarness) {
			rawRetryScenario(t, h, session, http.StatusBadRequest)
		}},
		{"401 accepted raw", func(t *testing.T, h *invariantHarness) {
			rawRetryScenario(t, h, session, http.StatusUnauthorized)
		}},
		{"raw retry in a subagent leaves the main thread alone", func(t *testing.T, h *invariantHarness) {
			main := newCCConversation("You are Claude Code.", session)
			sub := newCCConversation("You are a subagent.", session)
			h.send(main.user(filler("main 1")), sendOpts{})
			h.send(main.toolResult(filler("main 2")), sendOpts{})
			h.send(sub.user(filler("sub 1")), sendOpts{})
			if _, attempts := h.send(sub.toolResult(filler("sub 2")), sendOpts{respond: rejectTransformed(http.StatusBadRequest, nil)}); len(attempts) != 2 {
				t.Fatalf("test setup: the subagent turn should have taken the raw retry, attempts=%d", len(attempts))
			}
			h.send(main.user(filler("main 3")), sendOpts{})
			h.send(sub.user(filler("sub 3")), sendOpts{})
			h.send(main.toolResult(filler("main 4")), sendOpts{})
		}},
		{"fork and main concurrently carry the same new live block", func(t *testing.T, h *invariantHarness) {
			main := newCCConversation("You are Claude Code.", session)
			h.send(main.user(filler("c1")), sendOpts{})
			h.send(main.toolResult(filler("c2")), sendOpts{})
			for round := 0; round < 4; round++ {
				main.toolResult(filler("concurrent new block " + strconv.Itoa(round)))
				fork := main.clone().user(filler("suggest the next prompt"))
				var wg sync.WaitGroup
				for _, c := range []*ccConversation{main, fork} {
					wg.Add(1)
					go func(c *ccConversation) {
						defer wg.Done()
						h.send(c, sendOpts{group: round + 1})
					}(c)
				}
				wg.Wait()
			}
			h.send(main.user(filler("after the race")), sendOpts{})
		}},
		{"raw retry while a fork of the same turn is in flight", func(t *testing.T, h *invariantHarness) {
			main := newCCConversation("You are Claude Code.", session)
			h.send(main.user(filler("f1")), sendOpts{})
			h.send(main.toolResult(filler("f2")), sendOpts{})
			main.toolResult(filler("f3"))
			fork := main.clone().user(filler("suggest the next prompt"))
			// The fork reaches the provider compressed, then main's turn is
			// rejected transformed and accepted raw, and only then is the fork
			// answered: the fork was sent before the raw retry existed.
			hold := newUpstreamHold()
			done := make(chan struct{})
			go func() {
				defer close(done)
				h.send(fork, sendOpts{group: 1, hold: hold})
			}()
			<-hold.entered
			if _, attempts := h.send(main, sendOpts{group: 1, respond: rejectTransformed(http.StatusBadRequest, nil)}); len(attempts) != 2 {
				t.Fatalf("test setup: main's turn should have taken the raw retry, attempts=%d", len(attempts))
			}
			close(hold.release)
			<-done
			h.send(main.user(filler("f4")), sendOpts{})
		}},
		{"subagents with identical first messages", func(t *testing.T, h *invariantHarness) {
			task := filler("explore the repository and report")
			a := newCCConversation("You are an Explore subagent.", session).user(task)
			b := a.clone()
			h.send(a, sendOpts{})
			h.send(b, sendOpts{})
			h.send(a.toolResult(filler("a reads x")), sendOpts{})
			h.send(b.toolResult(filler("b reads y")), sendOpts{})
			h.send(a.toolResult(filler("a reads z")), sendOpts{})
			h.send(b.user(filler("b wraps up")), sendOpts{})
		}},
		{"tool-schema strip whose first recovery write fails", func(t *testing.T, h *invariantHarness) {
			// The catalog's first sight cannot store its original, so it goes
			// out unstripped, and that is its decision on every later turn.
			h.strip = true
			h.restart("")
			main := newCCConversation("You are Claude Code.", session)
			h.comp.mu.Lock()
			h.comp.failStore = true
			h.comp.mu.Unlock()
			h.send(main.user(filler("first sight while CCR is down")), sendOpts{})
			h.comp.mu.Lock()
			h.comp.failStore = false
			h.comp.mu.Unlock()
			h.send(main.user(filler("store back")), sendOpts{})
			h.send(main.user(filler("store still back")), sendOpts{})
		}},
		{"tool-schema strip through a message store failure and a raw retry", func(t *testing.T, h *invariantHarness) {
			h.strip = true
			h.restart("")
			main := newCCConversation("You are Claude Code.", session)
			sub := newCCConversation("You are Claude Code.", session) // same catalog, own thread
			h.send(main.user(filler("strip 1")), sendOpts{})
			h.comp.mu.Lock()
			h.comp.failStore = true
			h.comp.mu.Unlock()
			h.send(main.user(filler("strip 2 while CCR is down")), sendOpts{})
			h.comp.mu.Lock()
			h.comp.failStore = false
			h.comp.mu.Unlock()
			h.send(sub.user(filler("sub 1")), sendOpts{})
			h.send(main.user(filler("strip 3")), sendOpts{respond: rejectTransformed(http.StatusBadRequest, nil)})
			h.send(main.user(filler("strip 4")), sendOpts{})
			h.send(sub.user(filler("sub 2")), sendOpts{})
			if !bytes.Contains(h.xs[0].forwarded, []byte(`"input_schema":{"type":"object"}`)) {
				t.Fatalf("test setup: the catalog was not stripped:\n%.400s", h.xs[0].forwarded)
			}
		}},
		{"raw retry of a sibling's first turn", func(t *testing.T, h *invariantHarness) {
			siblingRawRetry(t, h, session, false)
		}},
		{"raw retry of a sibling's first turn after a restart", func(t *testing.T, h *invariantHarness) {
			siblingRawRetry(t, h, session, true)
		}},
		{"raw retry of a sibling's first turn with the tool-schema strip", func(t *testing.T, h *invariantHarness) {
			h.strip = true
			h.restart("")
			siblingRawRetry(t, h, session, false)
		}},
		{"raw retry of a request caching only system and tools", func(t *testing.T, h *invariantHarness) {
			h.strip = true
			h.restart("")
			main := newCCConversation("You are Claude Code.", session)
			h.send(main.user(filler("main one")), sendOpts{})
			h.send(main.user(filler("main two")), sendOpts{})
			side := newCCConversation("You are Claude Code.", session).user(filler("side request, only the system marked"))
			side.systemOnly = true
			if _, attempts := h.send(side, sendOpts{respond: rejectTransformed(http.StatusBadRequest, nil)}); len(attempts) != 2 {
				t.Fatalf("test setup: the side request should have taken the raw retry, attempts=%d", len(attempts))
			}
			_, next := h.send(main.user(filler("main three")), sendOpts{})
			if !bytes.Contains(next[0].body, []byte(`"input_schema":{"type":"object"}`)) || !bytes.Contains(next[0].body, []byte("<<ccr:")) {
				t.Errorf("the main thread lost its stripped catalog or its substitutions to a side request's raw retry:\n%.300s", next[0].body)
			}
			_, fresh := h.send(newCCConversation("You are Claude Code.", session).user(filler("a new conversation")), sendOpts{})
			if !bytes.Contains(fresh[0].body, []byte("<<ccr:")) {
				t.Errorf("a new conversation of the same agent stopped compressing after a side request's raw retry:\n%.300s", fresh[0].body)
			}
		}},
		{"PAYG without MCP: a compressed conversation starts streaming", func(t *testing.T, h *invariantHarness) {
			h.payg = true
			h.restart("")
			main := newCCConversation("You are an SDK agent.", session)
			h.send(main.user(filler("p1")), sendOpts{})
			h.send(main.toolResult(filler("p2")), sendOpts{})
			main.stream = true // the server-side retrieve tool cannot ride a stream
			h.send(main.user(filler("p3")), sendOpts{})
			if cause := h.sink.last(t).CacheBustCause; cause != bustCauseStreamSwitch {
				t.Errorf("the stream after compressed turns must be recorded as %q, got %q", bustCauseStreamSwitch, cause)
			}
			main.stream = false
			_, next := h.send(main.toolResult(filler("p4")), sendOpts{})
			if bytes.Contains(next[0].body, []byte("<<ccr:")) || bytes.Contains(next[0].body, []byte(retrieveToolName)) {
				t.Errorf("the turn after the stream flipped back to the compressed prefix:\n%.300s", next[0].body)
			}
			main.stream = true
			h.send(main.user(filler("p5")), sendOpts{})
			main.stream = false
			h.send(main.user(filler("p6")), sendOpts{})
		}},
		{"PAYG without MCP: a conversation first seen streaming", func(t *testing.T, h *invariantHarness) {
			h.payg = true
			h.restart("")
			streamed := newCCConversation("You are an SDK agent.", session)
			streamed.stream = true
			h.send(streamed.user(filler("s1")), sendOpts{})
			streamed.stream = false
			_, next := h.send(streamed.toolResult(filler("s2")), sendOpts{})
			if !bytes.Equal(next[0].body, streamed.body()) {
				t.Errorf("a conversation cached raw by its streamed turn started compressing:\n%.300s", next[0].body)
			}
			h.restart("")
			h.send(streamed.user(filler("s3")), sendOpts{})
			other := newCCConversation("You are another SDK agent.", session)
			h.send(other.user(filler("o1")), sendOpts{}) // non-streaming: compresses
			h.send(other.user(filler("o2")), sendOpts{})
		}},
		{"two sessions sharing file contents", func(t *testing.T, h *invariantHarness) {
			file := filler("package main shared file contents")
			one := newCCConversation("You are Claude Code in repo one.", "sess-one")
			two := newCCConversation("You are Claude Code in repo two.", "sess-two")
			h.send(one.user(filler("one starts")), sendOpts{})
			h.send(two.toolResult(file), sendOpts{})
			h.send(one.toolResult(file), sendOpts{})
			h.send(two.user(filler("two continues")), sendOpts{})
			h.send(one.user(filler("one continues")), sendOpts{})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newInvariantHarness(t)
			tc.run(t, h)
			h.compressedSomething()
			h.assert()
		})
	}
}

// interleaved is a Claude Code process: the main thread, two subagents with
// their own system prompts, a haiku side request and the prompt-suggestion fork,
// all on one session (or none).
// TestPAYGStreamWithMCPToolIsNotAStreamSwitch: a PAYG proxy without
// CAVEMAN_RECOVERY=mcp still compresses a stream that carries the namespaced
// caveman MCP retrieve tool, on the marker path. Such a stream went out
// replaced, so it is neither a stream switch nor a stream-raw lineage, and a
// bust caveman causes on it must be reported as caveman's.
func TestPAYGStreamWithMCPToolIsNotAStreamSwitch(t *testing.T) {
	h := newInvariantHarness(t)
	h.payg = true
	h.restart("")
	c := newCCConversation("You are Claude Code.", "sess-payg-mcp")
	c.stream = true
	c.toolResult(filler("turn one"))
	withMCP := func() []byte {
		return bytes.Replace(c.body(), []byte(`"tools":[`),
			[]byte(`"tools":[{"name":"mcp__caveman__caveman_retrieve","description":"Recover elided bytes","input_schema":{"type":"object"}},`), 1)
	}
	_, first := h.sendBody(withMCP(), c.session, sendOpts{})
	if !bytes.Contains(first[0].body, []byte("<<ccr:")) {
		t.Fatalf("test setup: the MCP-carrying stream should compress on the marker path:\n%.300s", first[0].body)
	}
	if cause := h.sink.last(t).CacheBustCause; cause == bustCauseStreamSwitch {
		t.Fatalf("a compressed stream was recorded as %q", cause)
	}
	if h.srv.heldRawByStream(h.srv.adapters[0], providers.RequestMetadata{Provider: "anthropic", Model: c.model, Stream: true}, withMCP()) {
		t.Fatal("a compressed stream recorded a stream-raw lineage")
	}
	c.toolResult(filler("turn two"))
	h.sendBody(withMCP(), c.session, sendOpts{})
	h.assert()

	// The store loses every replacement row: turn 3 re-sends the cached blocks
	// raw, a bust caveman caused.
	h.cache.mu.Lock()
	for k := range h.cache.entries {
		if !strings.HasPrefix(k, rawPinScope+":") && !strings.HasPrefix(k, lineageScope+":") {
			delete(h.cache.entries, k)
		}
	}
	h.cache.mu.Unlock()
	c.user(filler("turn three"))
	h.sendBody(withMCP(), c.session, sendOpts{})
	if cause := h.sink.last(t).CacheBustCause; cause != bustCauseCaveman {
		t.Fatalf("a caveman bust on a compressed stream was recorded as %q, want %q", cause, bustCauseCaveman)
	}
}

func interleaved(h *invariantHarness, session string) {
	main := newCCConversation("You are Claude Code.", session)
	explore := newCCConversation("You are an Explore subagent.", session)
	plan := newCCConversation("You are a Plan subagent.", session)
	h.send(main.user(filler("main 1")), sendOpts{})
	h.send(newCCConversation("Write a 5-word title.", session).user(filler("main 1")), sendOpts{})
	h.send(main.toolResult(filler("main reads a")), sendOpts{})
	h.send(explore.user(filler("explore task")), sendOpts{})
	h.send(main.clone().user(filler("suggest the next prompt")), sendOpts{})
	h.send(plan.user(filler("plan task")), sendOpts{})
	h.send(explore.toolResult(filler("explore reads b")), sendOpts{})
	h.send(main.toolResult(filler("main reads c")), sendOpts{})
	h.send(plan.toolResult(filler("main reads a")), sendOpts{})
	h.send(explore.toolResult(filler("main reads c")), sendOpts{})
	h.send(main.clone().user(filler("suggest the next prompt")), sendOpts{})
	h.send(main.user(filler("main 2")), sendOpts{})
	h.send(plan.user(filler("plan wraps up")), sendOpts{})
	h.send(main.toolResult(filler("main reads d")), sendOpts{})
}

// rawRetryScenario: the provider rejects one transformed turn with status and
// accepts the original bytes. Every later request must extend that raw request.
func rawRetryScenario(t *testing.T, h *invariantHarness, session string, status int) {
	main := newCCConversation("You are Claude Code.", session)
	h.send(main.user(filler("t1")), sendOpts{})
	h.send(main.toolResult(filler("t2")), sendOpts{})
	_, attempts := h.send(main.user(filler("t3")), sendOpts{respond: rejectTransformed(status, nil)})
	if len(attempts) != 2 {
		t.Fatalf("the rejected transformed turn must be retried once with the original bytes, attempts=%d", len(attempts))
	}
	h.send(main.toolResult(filler("t4")), sendOpts{})
	h.send(main.clone().user(filler("suggest the next prompt")), sendOpts{})
	h.send(main.user(filler("t5")), sendOpts{})
}

// siblingRawRetry: two conversations open with the same system, tools and
// first message (two subagents given one task, repeated claude -p runs). The
// sibling has cached a longer compressed prefix when the other's first turn
// takes a raw retry. The raw pin covers that first turn only; the sibling,
// whose own cached prefix is longer, keeps it, also when the proxy restarted
// in between.
func siblingRawRetry(t *testing.T, h *invariantHarness, session string, restart bool) {
	task := filler("explore the repository and report")
	sibling := newCCConversation("You are an Explore subagent.", session).user(task)
	first := sibling.clone()
	h.send(sibling, sendOpts{})
	h.send(sibling.toolResult(filler("sibling reads x")), sendOpts{})
	if restart {
		h.restart("")
	}
	if _, attempts := h.send(first, sendOpts{respond: rejectTransformed(http.StatusBadRequest, nil)}); len(attempts) != 2 {
		t.Fatalf("test setup: the first turn should have taken the raw retry, attempts=%d", len(attempts))
	}
	h.send(sibling.toolResult(filler("sibling reads y")), sendOpts{})
	h.send(first.user(filler("the retried conversation goes on")), sendOpts{})
	h.send(sibling.user(filler("sibling wraps up")), sendOpts{})
}

// FuzzCachePrefixInvariant walks random sequences of the same operations —
// turns, tool results, side requests, forks, rewinds, client edits, compaction,
// model switches, restarts, provider rejections, store failures and declines —
// and checks the invariant over everything the provider accepted.
func FuzzCachePrefixInvariant(f *testing.F) {
	for _, seed := range [][]byte{
		{0, 0, 1, 0, 0, 1, 1, 1, 3, 0, 0, 0},
		{0, 0, 9, 0, 0, 0, 1, 0, 0, 0, 8, 0, 1, 0},
		{0, 1, 1, 2, 2, 0, 9, 1, 1, 1, 4, 1, 0, 1},
		{1, 0, 1, 0, 5, 0, 0, 0, 10, 0, 0, 0, 11, 0, 1, 0},
		{12, 0, 1, 3, 1, 4, 1, 3, 0, 0, 6, 0, 0, 0},
		{0, 0, 9, 2, 0, 0, 8, 0, 0, 0, 3, 0, 0, 0},
		{0, 2, 9, 3, 0, 2, 0, 2, 13, 0, 0, 0, 7, 0, 0, 0},
		{0, 0, 0, 1, 9, 0, 1, 1, 0, 0, 0, 1, 14, 0, 0, 1},
		{0, 0, 10, 0, 15, 0, 0, 0, 13, 0, 15, 0, 0, 0},
		// A raw retry without a session id between two turns that carry one.
		{14, 50, 14, 50, 9, 48, 14, 50, 14, 50},
		// A raw retry of a first turn while a sibling with the same opening has
		// cached a longer compressed prefix: the pin must not capture it.
		{15, 3, 0, 3, 9, 1, 15, 0, 0, 3},
		{32, 3, 0, 3, 9, 1, 15, 0, 0, 3}, // 32 % 17 == 15, even: the strip stays off
		// A raw retry of a request that marks only the system prompt.
		{1, 0, 0, 0, 9, 1, 16, 0, 0, 0, 16, 2, 0, 0},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, script []byte) {
		if len(script) > 80 {
			script = script[:80]
		}
		h := newInvariantHarness(t)
		if len(script) > 0 && script[0]&1 == 1 {
			h.strip = true
			h.restart("")
		}
		w := newFuzzWorld()
		for i := 0; i+1 < len(script); i += 2 {
			w.step(h, script[i], script[i+1])
		}
		h.assert()
	})
}

// fuzzWorld holds the conversations a fuzz script drives. Conversation 3
// shares conversation 0's system prompt and first message, like two subagents
// started with the same task.
type fuzzWorld struct {
	convs   []*ccConversation
	next    respondFunc
	retryOn bool
	turn    int
	// unpersisted: a store write failed, so some raw decisions live only in
	// this process (rawMemory). Restarting on top of that is a double fault the
	// proxy cannot see through, so the walk stops restarting from then on.
	unpersisted bool
}

func newFuzzWorld() *fuzzWorld {
	w := &fuzzWorld{convs: []*ccConversation{
		newCCConversation("You are Claude Code.", "sess-fuzz"),
		newCCConversation("You are an Explore subagent.", "sess-fuzz"),
		newCCConversation("You are Claude Code in another repo.", "sess-other"),
		newCCConversation("You are Claude Code.", "sess-fuzz"),
	}}
	opening := filler("open the task")
	w.convs[0].user(opening)
	w.convs[3].user(opening)
	w.convs[1].user(filler("explore the code"))
	w.convs[2].user(filler("other repo task"))
	return w
}

// content draws from a small pool so conversations share file contents.
func (w *fuzzWorld) content(arg byte) string {
	if arg&0x80 != 0 {
		return filler("shared file " + strconv.Itoa(int(arg&0x7)))
	}
	w.turn++
	return filler("unique content " + strconv.Itoa(w.turn))
}

func (w *fuzzWorld) send(h *invariantHarness, c *ccConversation) {
	o := sendOpts{respond: w.next}
	retry := w.retryOn
	w.next, w.retryOn = nil, false
	status, _ := h.send(c, o)
	if retry && status == http.StatusTooManyRequests {
		h.send(c, sendOpts{}) // the client honors retry-after and re-sends
	}
}

func (w *fuzzWorld) step(h *invariantHarness, op, arg byte) {
	c := w.convs[int(arg)%len(w.convs)]
	switch op % 17 {
	case 0:
		w.send(h, c.user(w.content(arg)))
	case 1:
		w.send(h, c.toolResult(w.content(arg)))
	case 2: // haiku side request
		h.send(newCCConversation("Summarize in five words.", c.session).user(w.content(arg)), sendOpts{})
	case 3: // prompt-suggestion fork
		w.send(h, c.clone().user(filler("suggest the next prompt")))
	case 4: // rewind to an earlier user turn and edit it
		if users := userIndexes(c); len(users) > 1 {
			c.messages = c.messages[:users[int(arg/4)%(len(users)-1)+1]]
			w.send(h, c.user(w.content(arg)))
		}
	case 5: // the client clears an old tool_result in place
		for i, m := range c.messages {
			if i < len(c.messages)-1 && strings.Contains(m.blocks[0], `"tool_result"`) && !strings.Contains(m.blocks[0], "cleared") {
				id := m.blocks[0][strings.Index(m.blocks[0], "toolu_"):]
				id = id[:strings.Index(id, `"`)]
				c.messages[i].blocks[0] = toolResultBlock(id, "[Old tool result content cleared]")
				w.send(h, c.user(w.content(arg)))
				break
			}
		}
	case 6: // compaction, then the conversation restarts from a summary
		w.send(h, c.clone().user(filler("summarize the conversation")))
		c.messages = nil
		w.send(h, c.user(filler("summary "+strconv.Itoa(int(arg)))))
	case 7:
		if c.model == "claude-sonnet-4-6" {
			c.model = "claude-opus-4-8"
		} else {
			c.model = "claude-sonnet-4-6"
		}
		w.send(h, c.user(w.content(arg)))
	case 8:
		if !w.unpersisted {
			h.restart("r" + strconv.Itoa(int(arg)))
		}
	case 9: // the provider rejects the next transformed request
		switch arg % 4 {
		case 0:
			w.next = rejectTransformed(http.StatusTooManyRequests, nil)
		case 1:
			w.next = rejectTransformed(http.StatusBadRequest, nil)
		case 2:
			w.next = rejectTransformed(http.StatusUnauthorized, nil)
		case 3:
			w.next, w.retryOn = rateLimitResponse, true
		}
	case 10:
		w.unpersisted = true
		h.cache.mu.Lock()
		h.cache.failWrites = true
		h.cache.mu.Unlock()
		w.send(h, c.user(w.content(arg)))
		h.cache.mu.Lock()
		h.cache.failWrites = false
		h.cache.mu.Unlock()
	case 11:
		h.cache.mu.Lock()
		h.cache.failLookups = true
		h.cache.mu.Unlock()
		w.send(h, c.user(w.content(arg)))
		h.cache.mu.Lock()
		h.cache.failLookups = false
		h.cache.mu.Unlock()
	case 12:
		h.comp.mu.Lock()
		h.comp.declineFirst = !h.comp.declineFirst
		h.comp.mu.Unlock()
	case 13:
		h.comp.mu.Lock()
		h.comp.failStore = true
		h.comp.mu.Unlock()
		w.send(h, c.toolResult(w.content(arg)))
		h.comp.mu.Lock()
		h.comp.failStore = false
		h.comp.mu.Unlock()
	case 15: // the client re-sends its last request unchanged
		if len(c.messages) > 0 {
			w.send(h, c)
		}
	case 14: // a turn that carries the session id, or drops it
		if c.session == "" {
			c.session = "sess-fuzz"
		} else {
			c.session = ""
		}
		w.send(h, c.user(w.content(arg)))
	case 16: // a side request on c's system and tools that marks only the system
		side := newCCConversation(c.system, c.session).user(w.content(arg))
		side.systemOnly = true
		w.send(h, side)
	}
}

func userIndexes(c *ccConversation) []int {
	var out []int
	for i, m := range c.messages {
		if m.role == "user" && !strings.Contains(m.blocks[0], `"tool_result"`) {
			out = append(out, i)
		}
	}
	return out
}

// newestMarkedConversation is the Claude Code shape: only the newest message
// carries cache_control, so the marker moves forward every turn.
func newestMarkedConversation(userTexts ...string) string {
	msgs := make([]string, 0, len(userTexts)*2)
	for i, text := range userTexts {
		if i > 0 {
			msgs = append(msgs, `{"role":"assistant","content":[`+subBlock("assistant "+strconv.Itoa(i))+`]}`)
		}
		block := subBlock(text)
		if i == len(userTexts)-1 {
			block = subCachedBlock(text)
		}
		msgs = append(msgs, `{"role":"user","content":[`+block+`]}`)
	}
	return `{"model":"claude-sonnet-4-6","max_tokens":1024,` +
		`"system":[{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}}],` +
		`"messages":[` + strings.Join(msgs, ",") + `]}`
}

func withHeaders(base map[string]string, extra ...string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for i := 0; i+1 < len(extra); i += 2 {
		out[extra[i]] = extra[i+1]
	}
	return out
}

// TestGateTripKeepsEarlierSubstitutions: a rewind makes the client's own
// prefix diverge from the previous turn. Whatever the proxy decides about NEW
// content, it must keep re-sending the replacement turn 1 was cached with.
func TestGateTripKeepsEarlierSubstitutions(t *testing.T) {
	t1, t2, t3, t2b := turnText(1), turnText(2), turnText(3), strings.Repeat("turn two rewritten ", 40)
	rt := prefixStableTransport(4)
	comp := &stableCompressor{}
	srv, _ := newPrefixStableServer(comp, newTestPrefixCache(), rt)
	headers := withHeaders(subscriptionAgentHeaders, "x-cave-session", "sess-rewind")

	serveBody(t, srv, "/v1/messages", newestMarkedConversation(t1), headers)
	serveBody(t, srv, "/v1/messages", newestMarkedConversation(t1, t2), headers)
	serveBody(t, srv, "/v1/messages", newestMarkedConversation(t1, t2, t3), headers)
	serveBody(t, srv, "/v1/messages", newestMarkedConversation(t1, t2b), headers)

	if !strings.Contains(string(rt.bodies[3]), comp.expectedReplacement(t, t1)) {
		t.Fatalf("the rewound request dropped turn 1's cached replacement:\n%s", rt.bodies[3])
	}
}

// TestExplicitEpochVetoKeepsSubstitutions: a framework's declared epoch may veto
// NEW compression, but the replacement it accepted on an earlier turn is already
// in the provider cache, so it is re-sent either way.
func TestExplicitEpochVetoKeepsSubstitutions(t *testing.T) {
	t1, t2 := turnText(1), turnText(2)
	rt := prefixStableTransport(2)
	comp := &stableCompressor{}
	srv, sink := newPrefixStableServer(comp, newTestPrefixCache(), rt)
	epoch := func(digest string) map[string]string {
		return withHeaders(subscriptionAgentHeaders, "x-cave-cache-epoch", "epoch-1", "x-cave-cache-prefix-sha256", strings.Repeat(digest, 64))
	}

	serveBody(t, srv, "/v1/messages", newestMarkedConversation(t1), epoch("a"))
	serveBody(t, srv, "/v1/messages", newestMarkedConversation(t1, t2), epoch("b")) // drift: veto

	second := string(rt.bodies[1])
	if !strings.Contains(second, comp.expectedReplacement(t, t1)) {
		t.Fatalf("an epoch veto dropped turn 1's cached replacement:\n%s", second)
	}
	if !strings.Contains(second, t2) {
		t.Fatalf("an epoch veto must still stop NEW compression:\n%s", second)
	}
	if sink.last(t).CompressionEligible {
		t.Fatal("a vetoed request is not a compression candidate")
	}
}

// TestToolSchemaStripIgnoresEpochVeto: the strip is a pure function of the tool
// catalog at the head of the prefix. Gating it per request flipped the catalog
// between stripped and original, busting the whole prefix.
func TestToolSchemaStripIgnoresEpochVeto(t *testing.T) {
	rt := &captureTransport{responses: []string{subMessageRespBody, subMessageRespBody}}
	srv, _ := newToolSchemaStripServer(t, &toolSchemaStripCompressor{}, rt, Config{RecoveryViaMCP: true, ToolSchemaStrip: toolSchemaStripMode})
	epoch := func(digest string) map[string]string {
		return withHeaders(subscriptionAgentHeaders, "x-cave-cache-epoch", "epoch-1", "x-cave-cache-prefix-sha256", strings.Repeat(digest, 64))
	}

	serveBody(t, srv, "/v1/messages", toolCatalogRequest("first turn"), epoch("a"))
	serveBody(t, srv, "/v1/messages", toolCatalogRequest("second turn"), epoch("b"))

	stripped := strippedToolCatalog(t)
	for i, body := range rt.bodies {
		if !strings.Contains(string(body), `"tools":`+stripped) {
			t.Fatalf("request %d did not carry the stripped catalog:\n%s", i+1, body)
		}
	}
}

func conversationWithSystem(system string, userTexts ...string) string {
	body := newestMarkedConversation(userTexts...)
	return strings.Replace(body, `"text":"You are Claude Code."`, `"text":`+jsonText(system), 1)
}

// TestMemoNeverFlipsABlockSentRaw: the replacement memo is keyed by content
// across conversations. A block one conversation already sent raw must never be
// substituted later because another conversation compressed the same bytes —
// whatever went out first is what the provider cached.
func TestMemoNeverFlipsABlockSentRaw(t *testing.T) {
	x, y := strings.Repeat("the same tool output ", 40), turnText(9)
	comp := &declineOnceCompressor{target: x}
	rt := prefixStableTransport(3)
	srv, _ := newPrefixStableServer(comp, newTestPrefixCache(), rt)

	serveBody(t, srv, "/v1/messages", conversationWithSystem("You are B.", x), subscriptionAgentHeaders)
	serveBody(t, srv, "/v1/messages", conversationWithSystem("You are A.", x), subscriptionAgentHeaders)
	serveBody(t, srv, "/v1/messages", conversationWithSystem("You are B.", x, y), subscriptionAgentHeaders)

	for i, body := range rt.bodies {
		if !strings.Contains(string(body), x) {
			t.Fatalf("request %d did not send x raw although B sent it raw first:\n%s", i+1, body)
		}
	}
	if !strings.Contains(string(rt.bodies[2]), (&stableCompressor{}).expectedReplacement(t, y)) {
		t.Fatalf("new content must still compress:\n%s", rt.bodies[2])
	}
}

// declineOnceCompressor turns target down the first time it sees it, the way a
// query-aware compressor or a failed recovery write can, and compresses it (and
// everything else) afterwards.
type declineOnceCompressor struct {
	stableCompressor
	target   string
	declined bool
}

func (c *declineOnceCompressor) CompressSegment(seg []byte) ([]byte, int, int) {
	c.mu.Lock()
	decline := string(seg) == c.target && !c.declined
	c.declined = c.declined || decline
	c.mu.Unlock()
	if decline {
		return nil, 0, 0
	}
	return c.stableCompressor.CompressSegment(seg)
}

// TestResumedHistoryNeverFlips: a block first seen below the cache floor (a
// --resume history built without the proxy) went out raw, so raw is its
// decision even when the same bytes later arrive as live content.
func TestResumedHistoryNeverFlips(t *testing.T) {
	history, live, next := strings.Repeat("history from before the proxy ", 30), turnText(2), turnText(3)
	rt := prefixStableTransport(3)
	comp := &stableCompressor{}
	srv, _ := newPrefixStableServer(comp, newTestPrefixCache(), rt)

	serveBody(t, srv, "/v1/messages", newestMarkedConversation(history, live), subscriptionAgentHeaders)
	serveBody(t, srv, "/v1/messages", conversationWithSystem("You are a subagent.", history), subscriptionAgentHeaders)
	serveBody(t, srv, "/v1/messages", newestMarkedConversation(history, live, next), subscriptionAgentHeaders)

	for i, body := range rt.bodies {
		if !strings.Contains(string(body), history) {
			t.Fatalf("request %d compressed history the resumed conversation sent raw:\n%s", i+1, body)
		}
	}
	if !strings.Contains(string(rt.bodies[2]), comp.expectedReplacement(t, live)) {
		t.Fatalf("the resumed conversation's own live turn must stay replaced:\n%s", rt.bodies[2])
	}
}

// TestUnpersistedRawDecisionHolds: when the replacement store cannot take a
// write, the turn's new block goes out raw and that decision cannot be stored.
// If the client then re-sends the accepted turn (an aborted stream, a retry),
// the block is live again and must still go out raw: the provider cached it so.
func TestUnpersistedRawDecisionHolds(t *testing.T) {
	t1, t2 := turnText(1), turnText(2)
	cache := newTestPrefixCache()
	rt := prefixStableTransport(3)
	srv, _ := newPrefixStableServer(&stableCompressor{}, cache, rt)

	serveBody(t, srv, "/v1/messages", newestMarkedConversation(t1), subscriptionAgentHeaders)
	cache.mu.Lock()
	cache.failWrites = true
	cache.mu.Unlock()
	serveBody(t, srv, "/v1/messages", newestMarkedConversation(t1, t2), subscriptionAgentHeaders)
	cache.mu.Lock()
	cache.failWrites = false
	cache.mu.Unlock()
	serveBody(t, srv, "/v1/messages", newestMarkedConversation(t1, t2), subscriptionAgentHeaders)

	if !strings.Contains(string(rt.bodies[1]), t2) || !bytes.Equal(rt.bodies[1], rt.bodies[2]) {
		t.Fatalf("the re-sent turn must reproduce the bytes the provider accepted:\n%s\n%s", rt.bodies[1], rt.bodies[2])
	}
}

// TestRateLimit429IsReturnedNotReplayed: a 429 that says it is a rate limit
// goes back to the client. Replaying it raw cannot beat the limit, and a raw
// replay accepted once the window frees caches bytes the next turn will not
// send. The client's own retry reproduces the transformed request exactly.
func TestRateLimit429IsReturnedNotReplayed(t *testing.T) {
	h := newInvariantHarness(t)
	main := newCCConversation("You are Claude Code.", "sess-429")
	t1 := filler("turn one")
	h.send(main.user(t1), sendOpts{})
	main.user(filler("turn two"))

	status, attempts := h.send(main, sendOpts{respond: rateLimitResponse})
	if status != http.StatusTooManyRequests || len(attempts) != 1 {
		t.Fatalf("rate limit: client status %d after %d upstream attempts, want 429 after 1", status, len(attempts))
	}
	status, again := h.send(main, sendOpts{})
	if status != http.StatusOK || len(again) != 1 || !bytes.Equal(again[0].body, attempts[0].body) {
		t.Fatalf("the client's retry must reproduce the rate-limited bytes (status %d)", status)
	}
	if !bytes.Contains(again[0].body, []byte("CMP:"+contentHandle([]byte(t1)))) {
		t.Fatalf("the retried turn lost turn one's replacement:\n%s", again[0].body)
	}
}

// TestOpaque429WithRateLimitHeadersIsRetriedRaw: Anthropic puts its
// anthropic-ratelimit-* headers on every response, not only on rate limits, so
// they cannot tell a rate limit from the opaque 429 the raw retry exists for.
// Only Retry-After can: a rate limit carries it, the spend-cap 429 does not
// (and replaying that one raw is harmless).
func TestOpaque429WithRateLimitHeadersIsRetriedRaw(t *testing.T) {
	h := newInvariantHarness(t)
	main := newCCConversation("You are Claude Code.", "sess-opaque-429")
	h.send(main.user(filler("turn one")), sendOpts{})
	opaque := rejectTransformed(http.StatusTooManyRequests, http.Header{
		"Anthropic-Ratelimit-Requests-Limit":     {"4000"},
		"Anthropic-Ratelimit-Requests-Remaining": {"3999"},
	})
	status, attempts := h.send(main.user(filler("turn two")), sendOpts{respond: opaque})
	if status != http.StatusOK || len(attempts) != 2 {
		t.Fatalf("an opaque 429 must take the raw retry: client status %d after %d upstream attempts, want 200 after 2", status, len(attempts))
	}
	h.assert()
}

// TestAcceptedRawRetryPinsTheConversationRaw: once the provider accepted a
// request only in its original form, that is what it cached, so every later
// request extending it goes out raw too. The pin follows the conversation, not
// the session: a subagent on the same session keeps compressing.
func TestAcceptedRawRetryPinsTheConversationRaw(t *testing.T) {
	h := newInvariantHarness(t)
	main := newCCConversation("You are Claude Code.", "sess-pin")
	sub := newCCConversation("You are a subagent.", "sess-pin")
	h.send(main.user(filler("one")), sendOpts{})
	h.send(sub.user(filler("sub one")), sendOpts{})

	main.user(filler("two"))
	rawBody := main.body()
	_, attempts := h.send(main, sendOpts{respond: rejectTransformed(http.StatusBadRequest, nil)})
	if len(attempts) != 2 || !bytes.Equal(attempts[1].body, rawBody) {
		t.Fatalf("test setup: want a rejected transformed attempt and an accepted raw retry, got %d attempts", len(attempts))
	}
	_, next := h.send(main.user(filler("three")), sendOpts{})
	if !bytes.Equal(next[0].body, main.body()) {
		t.Fatalf("the turn after an accepted raw retry must go out raw:\n%s", next[0].body)
	}
	_, subNext := h.send(sub.user(filler("sub two")), sendOpts{})
	if !bytes.Contains(subNext[0].body, []byte("<<ccr:")) {
		t.Fatalf("a different conversation on the same session must keep compressing:\n%s", subNext[0].body)
	}
	h.restart("")
	_, afterRestart := h.send(main.user(filler("four")), sendOpts{})
	if !bytes.Equal(afterRestart[0].body, main.body()) {
		t.Fatalf("the pin must survive a proxy restart:\n%s", afterRestart[0].body)
	}
	h.assert()
}

// TestPrefixMonitorAnchorsAcceptedBytes: the monitor must remember what the
// provider accepted. Anchoring the rejected transformed attempt of a raw retry
// made the next (correctly raw) turn read as a bust.
func TestPrefixMonitorAnchorsAcceptedBytes(t *testing.T) {
	short := "a first message too short to compress"
	m2, m3, m4 := turnText(2), turnText(3), turnText(4)
	rt := &captureTransport{
		statuses:  []int{200, 200, http.StatusBadRequest, 200, 200},
		responses: []string{subMessageRespBody, subMessageRespBody, subMessageRespBody, subMessageRespBody, subMessageRespBody},
	}
	srv, sink := newPrefixStableServer(&stableCompressor{}, newTestPrefixCache(), rt)
	headers := withHeaders(subscriptionAgentHeaders, "x-cave-session", "sess-monitor")

	serveBody(t, srv, "/v1/messages", newestMarkedConversation(short), headers)
	serveBody(t, srv, "/v1/messages", newestMarkedConversation(short, m2), headers)
	serveBody(t, srv, "/v1/messages", newestMarkedConversation(short, m2, m3), headers) // 400, then raw
	serveBody(t, srv, "/v1/messages", newestMarkedConversation(short, m2, m3, m4), headers)

	if len(rt.bodies) != 5 || !bytes.Equal(rt.bodies[4], []byte(newestMarkedConversation(short, m2, m3, m4))) {
		t.Fatalf("test setup: want the turn after the raw retry pinned raw, got %d upstream calls", len(rt.bodies))
	}
	if row := sink.last(t); row.CacheBust {
		t.Fatal("the turn that extends the accepted raw request was flagged as a cache bust")
	}
	if row := sink.rows[2]; row.CacheBustCause != bustCauseRawRetry {
		t.Fatalf("the raw retry itself must be classified raw_retry, got %q", row.CacheBustCause)
	}
}

// poisonSpliceAdapter fails reassembly whenever the poison block is replaced.
type poisonSpliceAdapter struct {
	anthropic.Adapter
	poison string
}

func (a poisonSpliceAdapter) ExtractStabilizable(body []byte, meta providers.RequestMetadata) ([]providers.RewritableBlock, func([][]byte) ([]byte, error), bool) {
	blocks, reassemble, ok := a.Adapter.ExtractStabilizable(body, meta)
	return blocks, func(reps [][]byte) ([]byte, error) {
		for i, rep := range reps {
			if rep != nil && string(blocks[i].Content) == a.poison {
				return nil, errTestPrefixCacheDown
			}
		}
		return reassemble(reps)
	}, ok
}

// TestLateSpliceFailureKeepsSubstitutions: when assembling this turn's new
// compression fails, the request must still re-send the replacements earlier
// turns were cached with, and the new block — which goes out raw — must not be
// left on record as compressed.
func TestLateSpliceFailureKeepsSubstitutions(t *testing.T) {
	x, y, z := turnText(1), turnText(2), turnText(3)
	rt := prefixStableTransport(3)
	comp := &stableCompressor{}
	srv := New(Config{
		Adapters:       []providers.Adapter{poisonSpliceAdapter{Adapter: anthropic.New("https://upstream.test").(anthropic.Adapter), poison: y}},
		Auth:           stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}},
		Creds:          passthroughTestCreds{},
		Compressor:     comp,
		PrefixCache:    newTestPrefixCache(),
		HTTPClient:     &http.Client{Transport: rt},
		RecoveryViaMCP: true,
	})

	serveBody(t, srv, "/v1/messages", newestMarkedConversation(x), subscriptionAgentHeaders)
	serveBody(t, srv, "/v1/messages", newestMarkedConversation(x, y), subscriptionAgentHeaders)
	serveBody(t, srv, "/v1/messages", newestMarkedConversation(x, y, z), subscriptionAgentHeaders)

	for i := 0; i < 3; i++ {
		body := string(rt.bodies[i])
		if !strings.Contains(body, comp.expectedReplacement(t, x)) {
			t.Fatalf("request %d dropped x's cached replacement:\n%s", i+1, body)
		}
		if i > 0 && !strings.Contains(body, y) {
			t.Fatalf("request %d did not keep y raw as it first went out:\n%s", i+1, body)
		}
	}
}

// TestCachePrefixInvariantRawRetryRacingAFork races a turn that takes the raw
// retry against a fork of it, with no ordering imposed: the fork may be
// decided before the pin, between the pin and the tripwire seeing the retry,
// or after both. Each order is legitimate, and neither the invariant nor the
// tripwire may call any of them caveman's bust.
func TestCachePrefixInvariantRawRetryRacingAFork(t *testing.T) {
	for round := 0; round < 20; round++ {
		h := newInvariantHarness(t)
		main := newCCConversation("You are Claude Code.", "sess-race")
		h.send(main.user(filler("r1")), sendOpts{})
		h.send(main.toolResult(filler("r2")), sendOpts{})
		main.toolResult(filler("r3 " + strconv.Itoa(round)))
		fork := main.clone().user(filler("suggest the next prompt"))
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			h.send(main, sendOpts{group: 1, respond: rejectTransformed(http.StatusBadRequest, nil)})
		}()
		go func() {
			defer wg.Done()
			h.send(fork, sendOpts{group: 1})
		}()
		wg.Wait()
		h.send(main.user(filler("r4")), sendOpts{})
		h.assert()
	}
}

// pixelHarness is the net for pixel mode: claude-fable-5 renders under an API
// key and a durable prefix cache. It keeps every request as the provider
// accepted it, for assertCachedPrefixPreserved.
type pixelHarness struct {
	t    *testing.T
	comp *pixelStoreCompressor
	rt   *captureTransport
	srv  *Server
	sink *captureSink
	xs   []exchange
}

func newPixelHarness(t *testing.T) *pixelHarness {
	t.Setenv("CAVE_PIXEL_MODELS", "")
	h := &pixelHarness{t: t, comp: &pixelStoreCompressor{}, rt: &captureTransport{}, sink: &captureSink{}}
	h.srv = New(Config{
		Adapters:    []providers.Adapter{anthropic.New("https://upstream.test")},
		Auth:        stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "pixel"}},
		Creds:       stubCreds{key: "sk-byok"},
		Sink:        h.sink,
		Compressor:  h.comp,
		PrefixCache: newTestPrefixCache(),
		HTTPClient:  &http.Client{Transport: h.rt},
	})
	return h
}

// send serves one request and returns the bytes the provider accepted; reject
// makes the provider refuse the first attempt, so the proxy takes the raw
// retry.
func (h *pixelHarness) send(body string, reject bool) []byte {
	h.t.Helper()
	h.rt.mu.Lock()
	first := len(h.rt.bodies)
	for len(h.rt.responses) < first+2 {
		h.rt.responses = append(h.rt.responses, anthropicPixelResponse("claude-fable-5"))
		h.rt.statuses = append(h.rt.statuses, 0)
	}
	if reject {
		h.rt.statuses[first] = http.StatusBadRequest
		h.rt.responses[first] = `{"type":"error","error":{"type":"invalid_request_error","message":"rejected"}}`
	}
	h.rt.mu.Unlock()
	serveBody(h.t, h.srv, "/v1/messages", body, map[string]string{"x-api-key": "sk-byok", "anthropic-version": "2023-06-01", "x-cave-session": "sess-pixel"})
	h.rt.mu.Lock()
	defer h.rt.mu.Unlock()
	attempts := h.rt.bodies[first:]
	h.xs = append(h.xs, exchange{client: []byte(body), forwarded: attempts[len(attempts)-1], rawRetry: len(attempts) > 1})
	return attempts[len(attempts)-1]
}

func (h *pixelHarness) assert() {
	h.t.Helper()
	assertCachedPrefixPreserved(h.t, h.xs)
	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()
	for i, row := range h.sink.rows {
		if row.CacheBustCause == bustCauseCaveman {
			h.t.Errorf("tripwire reported a caveman bust on recorded request %d of %d", i, len(h.sink.rows))
		}
	}
}

// pixelConversation is a Claude Code shaped claude-fable-5 conversation: each
// user turn one block, the newest one marked.
func pixelConversation(block func(i int, text, marker string) string, turns ...string) string {
	msgs := make([]string, 0, 2*len(turns))
	for i, text := range turns {
		if i > 0 {
			msgs = append(msgs, `{"role":"assistant","content":[{"type":"text","text":"read it `+strconv.Itoa(i)+`"}]}`)
		}
		marker := ""
		if i == len(turns)-1 {
			marker = `,"cache_control":{"type":"ephemeral"}`
		}
		msgs = append(msgs, `{"role":"user","content":[`+block(i, text, marker)+`]}`)
	}
	return `{"model":"claude-fable-5","max_tokens":128,"system":"You are Claude Code.","messages":[` + strings.Join(msgs, ",") + `]}`
}

// toolResultTurn is a long tool result; its marker sits on the tool_result
// block, outside the content pixel renders.
func toolResultTurn(i int, text, marker string) string {
	return `{"type":"tool_result","tool_use_id":"tool_` + strconv.Itoa(i) + `","content":` + jsonText(text) + marker + `}`
}

// textTurn is a long prompt or pasted log; pixel replaces the whole block,
// marker included.
func textTurn(_ int, text, marker string) string {
	return `{"type":"text","text":` + jsonText(text) + marker + `}`
}

// pixelRows is a tool result long enough to render.
func pixelRows(label string) string {
	return strings.Repeat(label+" row with values and a few more words.\n", 420)
}

func rendered(body []byte) bool { return imageCount(body) > 0 }

func imageCount(body []byte) int { return bytes.Count(body, []byte(`"type":"image"`)) }

func TestCachePrefixInvariantPixel(t *testing.T) {
	a, b, c := pixelRows("A"), pixelRows("B"), pixelRows("C")
	for _, tc := range []struct {
		name string
		run  func(t *testing.T, h *pixelHarness)
	}{
		{"raw retry pins the conversation", func(t *testing.T, h *pixelHarness) {
			// The provider cached turn 2 as text: turn 3 extends that text and
			// must not render turn 1 back in.
			if !rendered(h.send(pixelConversation(toolResultTurn, a), false)) {
				t.Fatal("test setup: turn 1 was not rendered")
			}
			if raw := h.send(pixelConversation(toolResultTurn, a, b), true); !bytes.Equal(raw, []byte(pixelConversation(toolResultTurn, a, b))) {
				t.Fatal("test setup: turn 2 should have been accepted raw")
			}
			h.send(pixelConversation(toolResultTurn, a, b, c), false)
		}},
		{"recovery store fails on a later turn", func(t *testing.T, h *pixelHarness) {
			// A failed write keeps only NEW content from rendering: the turn
			// still re-sends turn 1's renders, and its own block is on record
			// as the text it went out as.
			h.send(pixelConversation(toolResultTurn, a), false)
			h.comp.storeErr = errors.New("ccr down")
			if !rendered(h.send(pixelConversation(toolResultTurn, a, b), false)) {
				t.Error("the turn whose recovery write failed dropped turn 1's renders")
			}
			h.comp.storeErr = nil
			h.send(pixelConversation(toolResultTurn, a, b, c), false)
		}},
		{"recovery store fails on the first turn", func(t *testing.T, h *pixelHarness) {
			h.comp.storeErr = errors.New("ccr down")
			if rendered(h.send(pixelConversation(toolResultTurn, a), false)) {
				t.Fatal("test setup: turn 1 should have gone out as text")
			}
			h.comp.storeErr = nil
			// The client re-sends the same request once the store is back: the
			// provider cached the block as text, so it must stay text.
			h.send(pixelConversation(toolResultTurn, a), false)
			h.send(pixelConversation(toolResultTurn, a, b), false)
		}},
		{"a rendered text block keeps its breakpoint", func(t *testing.T, h *pixelHarness) {
			if !rendered(h.send(pixelConversation(textTurn, a), false)) {
				t.Fatal("test setup: turn 1 was not rendered")
			}
			h.send(pixelConversation(textTurn, a, b), false)
		}},
		{"a long conversation stays within the many-image limit", func(t *testing.T, h *pixelHarness) {
			// Past 20 images a request may carry no image over 2000 px, and
			// the renders are wider: the provider would reject every turn.
			var turns []string
			for i := 1; i <= 30; i++ {
				turns = append(turns, pixelRows("turn "+strconv.Itoa(i)))
				if n := imageCount(h.send(pixelConversation(toolResultTurn, turns...), false)); n > 20 {
					t.Fatalf("turn %d carried %d images", i, n)
				}
			}
			if !rendered(h.xs[len(h.xs)-1].forwarded) {
				t.Fatal("test setup: nothing was rendered")
			}
		}},
		{"client images count toward the many-image limit", func(t *testing.T, h *pixelHarness) {
			images := strings.Repeat(`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}},`, 20)
			withImages := func(i int, text, marker string) string { return images + toolResultTurn(i, text, marker) }
			if n := imageCount(h.send(pixelConversation(withImages, a), false)); n != 20 {
				t.Fatalf("the request carried %d images; the client's own 20 leave no room for a render", n)
			}
			h.send(pixelConversation(withImages, a, b), false)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPixelHarness(t)
			tc.run(t, h)
			h.assert()
		})
	}
}

// codexConversation is a Codex-shaped OpenAI Responses conversation.
type codexConversation struct {
	items []string
	calls int
}

func (c *codexConversation) user(text string) *codexConversation {
	if len(c.items) > 0 {
		c.items = append(c.items, `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done `+strconv.Itoa(len(c.items))+`"}]}`)
	}
	c.items = append(c.items, `{"type":"message","role":"user","content":[{"type":"input_text","text":`+jsonText(text)+`}]}`)
	return c
}

// parallelTools appends one round of parallel tool calls and their outputs:
// only the last output is live, the others are history at first sight.
func (c *codexConversation) parallelTools(outputs ...string) *codexConversation {
	first := c.calls
	for range outputs {
		c.calls++
		c.items = append(c.items, `{"type":"function_call","name":"shell","call_id":"call_`+strconv.Itoa(c.calls)+`","arguments":"{}"}`)
	}
	for i, out := range outputs {
		c.items = append(c.items, `{"type":"function_call_output","call_id":"call_`+strconv.Itoa(first+i+1)+`","output":`+jsonText(out)+`}`)
	}
	return c
}

func (c *codexConversation) body() string {
	return `{"model":"gpt-5.5","instructions":"You are Codex.","tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}],"input":[` + strings.Join(c.items, ",") + `]}`
}

// TestCachePrefixInvariantResponses drives Codex-shaped Responses traffic
// through the net. OpenAI caches the whole prompt, so every item counts, and
// parallel tool outputs arrive together with only the newest one live.
func TestCachePrefixInvariantResponses(t *testing.T) {
	rt := &captureTransport{}
	sink := &captureSink{}
	srv := New(Config{
		Adapters:       []providers.Adapter{openai.New("https://upstream.test")},
		Auth:           stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}},
		Creds:          stubCreds{key: "sk-byok"},
		Sink:           sink,
		Compressor:     &invariantCompressor{},
		PrefixCache:    newTestPrefixCache(),
		HTTPClient:     &http.Client{Transport: rt},
		RecoveryViaMCP: true,
	})
	var xs []exchange
	send := func(c *codexConversation, session string) {
		t.Helper()
		serveBody(t, srv, "/v1/responses", c.body(), map[string]string{"authorization": "Bearer sk-byok", "x-cave-session": session})
		rt.mu.Lock()
		defer rt.mu.Unlock()
		xs = append(xs, exchange{client: []byte(c.body()), forwarded: rt.bodies[len(rt.bodies)-1], wholePrompt: true})
	}

	shared := filler("output both conversations read")
	one := (&codexConversation{}).user(filler("fix the failing test"))
	send(one, "sess-codex-1")
	send(one.parallelTools(shared, filler("one reads a"), filler("one reads b")), "sess-codex-1")
	send(one.parallelTools(filler("one reads c"), filler("one reads d")), "sess-codex-1")
	two := (&codexConversation{}).user(filler("review the change"))
	send(two.parallelTools(filler("two reads a"), shared), "sess-codex-2") // one's history, two's live output
	send(one.user(filler("now run the suite")), "sess-codex-1")
	send(two.user(filler("summarize")), "sess-codex-2")

	compressed := false
	for _, x := range xs {
		compressed = compressed || bytes.Contains(x.forwarded, []byte("<<ccr:"))
	}
	if !compressed {
		t.Fatal("scenario never compressed anything, so it proves nothing")
	}
	assertCachedPrefixPreserved(t, xs)
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for i, row := range sink.rows {
		if row.CacheBustCause == bustCauseCaveman {
			t.Errorf("tripwire reported a caveman bust on recorded request %d of %d", i, len(sink.rows))
		}
	}
}
