package gateway

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
)

// observeSame feeds the monitor one request whose client and forwarded
// components are the same comma-separated list, all of it cached.
func observeSame(m *prefixMonitor, session, components string) (bool, int) {
	parts := splitComponents(components)
	cause, index := m.observe(session, observation{client: parts, forwarded: parts, cached: len(parts)})
	return cause != "", index
}

func rebaseIf(rawRetry bool) string {
	if rawRetry {
		return bustCauseRawRetry
	}
	return ""
}

func splitComponents(components string) [][]byte {
	var out [][]byte
	for _, part := range strings.Split(components, ",") {
		out = append(out, []byte(part))
	}
	return out
}

// TestPrefixMonitorDetectsNonExtendingPrefix unit-proves the ported
// providerFrozenExtends check: a first observation never busts, an append-only
// extension never busts, and a prefix that mutates an already-cached message of
// the same conversation is flagged with the FIRST diverging component index.
func TestPrefixMonitorDetectsNonExtendingPrefix(t *testing.T) {
	m := newPrefixMonitor()

	if bust, idx := observeSame(m, "", "a,b"); bust || idx != -1 {
		t.Fatalf("empty session must not compare: bust=%v idx=%d", bust, idx)
	}
	if bust, idx := observeSame(m, "s1", "sys,tools,m1,m2"); bust || idx != -1 {
		t.Fatalf("first observation must not bust: bust=%v idx=%d", bust, idx)
	}
	// Append-only extension (new component appended) is a legitimate cache extension.
	if bust, idx := observeSame(m, "s1", "sys,tools,m1,m2,m3"); bust || idx != -1 {
		t.Fatalf("append-only extension must not bust: bust=%v idx=%d", bust, idx)
	}
	// A mutated cached message is NOT an extension: first divergence is 3.
	if bust, idx := observeSame(m, "s1", "sys,tools,m1,MUT,m3,m4"); !bust || idx != 3 {
		t.Fatalf("mutated component must bust at index 3: bust=%v idx=%d", bust, idx)
	}
	// A shorter prefix that drops the tail also fails to extend; first divergence is
	// where the current prefix runs out relative to the prior one.
	m2 := newPrefixMonitor()
	observeSame(m2, "s2", "sys,tools,m1,m2,m3")
	if bust, idx := observeSame(m2, "s2", "sys,tools,m1"); !bust || idx != 3 {
		t.Fatalf("shrinking prefix must bust at index 3: bust=%v idx=%d", bust, idx)
	}
}

// TestPrefixMonitorInterleavedConversations pins #1094: a header-less Claude Code
// process correlates its main thread, subagents and side requests to ONE session,
// each with its own system prompt. Interleaving them must not read as drift, and
// a sibling that shares system/tools but not the first message is a new
// conversation, not a bust. Drift INSIDE one of them is still caught.
func TestPrefixMonitorInterleavedConversations(t *testing.T) {
	m := newPrefixMonitor()
	steps := []string{
		"sysA,tools,a1",       // main thread, turn 1
		"sysB,tools",          // side request with its own system prompt
		"sysA,tools,a1,a2",    // main thread, turn 2: extends turn 1
		"sysA,tools,b1",       // sibling subagent: same system/tools, own first message
		"sysA,tools,a1,a2,a3", // main thread, turn 3
		"sysB,tools",          // the side request again (repeat, not drift)
		"sysA,tools,b1,b2",    // sibling, turn 2
		"sysA,tools,a1,a2,a3,a4",
	}
	for i, comps := range steps {
		if bust, idx := observeSame(m, "s", comps); bust {
			t.Fatalf("step %d (%s) flagged as bust at %d: interleaved conversations are not drift", i, comps, idx)
		}
	}
	if bust, idx := observeSame(m, "s", "sysA,tools,a1,MUT,a3,a4"); !bust || idx != 3 {
		t.Fatalf("mutation inside the main thread must still bust at 3: bust=%v idx=%d", bust, idx)
	}
	if bust, idx := observeSame(m, "s", "sysA,tools,b1,b2,b3"); bust {
		t.Fatalf("the sibling must keep extending its own anchor: bust at %d", idx)
	}
	// A different system or tools component is another conversation, never a bust:
	// the monitor cannot tell a subagent's prompt from an injected one.
	if bust, idx := observeSame(m, "s", "SYSMUT,tools,a1,a2"); bust {
		t.Fatalf("a changed system component must read as another conversation: bust at %d", idx)
	}
	if bust, idx := observeSame(m, "s", "sysA,TOOLSMUT,a1,a2"); bust {
		t.Fatalf("a changed tools component must read as another conversation: bust at %d", idx)
	}
}

// TestPrefixMonitorMatchesClosestAnchor pins the anchor choice: a request that
// caches only system and tools (title generation, a classifier, a subagent's
// first turn) leaves a bare anchor that EVERY same-conversation request extends.
// Matching it first would absorb a drifted main-thread request and mask the bust.
func TestPrefixMonitorMatchesClosestAnchor(t *testing.T) {
	m := newPrefixMonitor()
	observeSame(m, "s", "sysA,tools")
	observeSame(m, "s", "sysA,tools,m1,m2,m3")
	observeSame(m, "s", "sysA,tools") // the bare request again
	if bust, idx := observeSame(m, "s", "sysA,tools,m1,MUT,m3"); !bust || idx != 3 {
		t.Fatalf("drift must be judged against the closest anchor, not the bare one: bust=%v idx=%d", bust, idx)
	}
	if bust, idx := observeSame(m, "s", "sysA,tools,m1,m2,m3,m4"); bust {
		t.Fatalf("the main thread must still extend its own anchor: bust at %d", idx)
	}
	if bust, idx := observeSame(m, "s", "sysA,tools"); bust {
		t.Fatalf("the bare request must still not be a bust: bust at %d", idx)
	}
}

func TestPrefixMonitorBoundsAnchorsPerSession(t *testing.T) {
	m := newPrefixMonitor()
	for i := 0; i <= maxAnchorsPerSession; i++ {
		observeSame(m, "s", "sys"+strings.Repeat("x", i)+",tools")
	}
	if got := len(m.last["s"]); got != maxAnchorsPerSession {
		t.Fatalf("session holds %d anchors, want at most %d", got, maxAnchorsPerSession)
	}
}

// TestPrefixMonitorClassifiesWhoChangedTheBytes: an anchor keeps what the
// client sent and what went upstream, so a request whose client bytes repeat a
// cached prefix is held to the forwarded bytes of that prefix. A changed
// forwarded byte under unchanged client bytes is caveman's bust; a changed
// client byte is the client's.
func TestPrefixMonitorClassifiesWhoChangedTheBytes(t *testing.T) {
	m := newPrefixMonitor()
	observe := func(client, forwarded string, rawRetry bool) (string, int) {
		c := splitComponents(client)
		return m.observe("s", observation{client: c, forwarded: splitComponents(forwarded), cached: len(c), rebase: rebaseIf(rawRetry)})
	}
	if cause, _ := observe("sys,tools,m1", "sys,tools,M1", false); cause != "" {
		t.Fatalf("first observation flagged %q", cause)
	}
	if cause, _ := observe("sys,tools,m1,m2", "sys,tools,M1,M2", false); cause != "" {
		t.Fatalf("re-sending the cached replacement is an extension, got %q", cause)
	}
	if cause, idx := observe("sys,tools,m1,m2,m3", "sys,tools,M1,m2,M3", false); cause != bustCauseCaveman || idx != 3 {
		t.Fatalf("a flipped decision under unchanged client bytes is caveman's bust at 3, got %q at %d", cause, idx)
	}
	if cause, _ := observe("sys,tools,m1,m2,m3,m4", "sys,tools,M1,m2,M3,M4", false); cause != "" {
		t.Fatalf("the request after the bust is held to what the bust cached, got %q", cause)
	}
	if cause, idx := observe("sys,tools,m1,EDIT,m3", "sys,tools,M1,EDIT,M3", false); cause != bustCauseClient || idx != 3 {
		t.Fatalf("the client's own edit is the client's bust at 3, got %q at %d", cause, idx)
	}
}

// TestPrefixMonitorHoldsRequestsToTheRawRetry: the retry the provider accepted
// raw is the invariant's one exception, and what extends it is held to it — by
// the raw pin that covers it, also over a shorter compressed request.
func TestPrefixMonitorHoldsRequestsToTheRawRetry(t *testing.T) {
	m := newPrefixMonitor()
	observe := func(client, forwarded string, rawRetry bool, pinned int) (string, int) {
		c := splitComponents(client)
		return m.observe("s", observation{client: c, forwarded: splitComponents(forwarded), cached: len(c), rebase: rebaseIf(rawRetry), pinned: pinned})
	}
	observe("sys,tools,m1", "sys,tools,M1", false, 0)
	if cause, idx := observe("sys,tools,m1,m2", "sys,tools,m1,m2", true, 4); cause != bustCauseRawRetry || idx != 2 {
		t.Fatalf("the raw retry must be classified raw_retry at 2, got %q at %d", cause, idx)
	}
	if cause, _ := observe("sys,tools,m1,m2,m3", "sys,tools,m1,m2,m3", false, 4); cause != "" {
		t.Fatalf("the pinned turn after the raw retry was flagged %q", cause)
	}
	if cause, _ := observe("sys,tools,m1", "sys,tools,M1", false, 0); cause == bustCauseCaveman {
		t.Fatal("a shorter request is not held to a longer one")
	}
	if cause, _ := observe("sys,tools,m1,m2,m3,m4", "sys,tools,m1,m2,m3,m4", false, 4); cause != "" {
		t.Fatalf("a pinned turn covering a shorter compressed request was flagged %q", cause)
	}
	observe("sys,tools,m1", "sys,tools,M1", false, 0)
	if cause, idx := observe("sys,tools,m1,other", "sys,tools,m1,other", false, 0); cause != bustCauseCaveman || idx != 2 {
		t.Fatalf("raw bytes no pin covers are caveman's bust at 2, got %q at %d", cause, idx)
	}
}

// anthropicRawBody builds a Claude Code shaped Anthropic request from an explicit
// system text and message list, so a test can control the cache floor precisely.
func anthropicRawBody(systemText string, messages ...string) string {
	return `{"model":"claude-sonnet-4-6","max_tokens":1024,` +
		`"system":[{"type":"text","text":"` + systemText + `","cache_control":{"type":"ephemeral"}}],` +
		`"tools":[{"name":"Read","description":"Read a file","input_schema":{"type":"object"}}],` +
		`"messages":[` + strings.Join(messages, ",") + `]}`
}

func cachedUserMsg(text string) string {
	return `{"role":"user","content":[` + subCachedBlock(text) + `]}`
}
func liveUserMsg(text string) string { return `{"role":"user","content":[` + subBlock(text) + `]}` }
func assistantMsg(text string) string {
	return `{"role":"assistant","content":[` + subBlock(text) + `]}`
}

// TestProxyRecordsCacheBustOnNonExtendingPrefix drives three requests in one
// session through the proxy: the second rewrites an already-frozen assistant turn,
// so its telemetry row is flagged cache_bust; the third is a side request with a
// different system prompt — another conversation in the same session, not a bust.
// Observe-only: traffic is never blocked or modified.
func TestProxyRecordsCacheBustOnNonExtendingPrefix(t *testing.T) {
	rt := &captureTransport{responses: []string{subMessageRespBody, subMessageRespBody, subMessageRespBody}}
	sink := &captureSink{}
	srv := New(Config{
		Adapters:   []providers.Adapter{anthropic.New("https://upstream.test")},
		Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		Creds:      passthroughTestCreds{},
		Sink:       sink,
		HTTPClient: &http.Client{Transport: rt},
	})
	headers := map[string]string{
		"x-cave-session":    "sessmono",
		"x-api-key":         "sk-ant-test",
		"anthropic-version": "2023-06-01",
	}
	turn1 := strings.Repeat("turn one project context ", 30)
	turn2 := strings.Repeat("turn two file contents ", 30)
	serveBody(t, srv, "/v1/messages", anthropicRawBody("You are Claude Code.",
		cachedUserMsg(turn1), assistantMsg("assistant one"), cachedUserMsg(turn2), liveUserMsg("live one")), headers)
	serveBody(t, srv, "/v1/messages", anthropicRawBody("You are Claude Code.",
		cachedUserMsg(turn1), assistantMsg("assistant one REWRITTEN"), cachedUserMsg(turn2), liveUserMsg("live two")), headers)
	serveBody(t, srv, "/v1/messages", anthropicRawBody("You are a haiku side request.",
		cachedUserMsg(turn1), liveUserMsg("classify this")), headers)

	if len(sink.rows) != 3 {
		t.Fatalf("recorded %d rows, want 3", len(sink.rows))
	}
	if sink.rows[0].CacheBust {
		t.Fatalf("first request in a session must never be a cache bust: %+v", sink.rows[0])
	}
	if !sink.rows[1].CacheBust {
		t.Fatalf("a request whose frozen assistant turn changed must be flagged cache_bust: %+v", sink.rows[1])
	}
	if sink.rows[2].CacheBust {
		t.Fatalf("a different conversation in the same session must not be flagged cache_bust: %+v", sink.rows[2])
	}
	// Observe-only: all requests still reached the upstream (no blocking).
	if len(rt.bodies) != 3 {
		t.Fatalf("observe-only monitor must never block traffic: upstream calls=%d", len(rt.bodies))
	}
}

// TestCacheEpochAllowsRunsGuardWithoutHeaders pins what the epoch check may
// veto. Header-less wrap clients (Claude Code, Codex, Gemini CLI) are always
// allowed — including when their own prefix diverged. The derived gate that
// used to refuse them there compared the client's own bytes, so it only ever
// saw client-caused divergence, and refusing forwarded the original bytes,
// which dropped every earlier substitution and busted the prefix at the first
// compressed block (#1105). A framework's explicit declaration is still
// enforced: a complete one is allowed until its prefix drifts, a partial one
// fails closed.
func TestCacheEpochAllowsRunsGuardWithoutHeaders(t *testing.T) {
	srv := New(Config{
		Adapters:    []providers.Adapter{anthropic.New("https://upstream.test")},
		PrefixCache: newTestPrefixCache(),
	})
	adapter := anthropic.New("https://upstream.test")
	meta := providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"}
	request := func(headers ...string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		for i := 0; i+1 < len(headers); i += 2 {
			r.Header.Set(headers[i], headers[i+1])
		}
		return r
	}

	turn1 := strings.Repeat("turn one project context ", 30)
	turn2 := strings.Repeat("turn two file contents ", 30)
	bodyT1 := []byte(anthropicRawBody("You are Claude Code.", cachedUserMsg(turn1), liveUserMsg("live one")))
	bodyT2 := []byte(anthropicRawBody("You are Claude Code.",
		cachedUserMsg(turn1), assistantMsg("assistant one"), cachedUserMsg(turn2), liveUserMsg("live two")))
	// The client rewrote an already-frozen assistant turn.
	bodyDrift := []byte(anthropicRawBody("You are Claude Code.",
		cachedUserMsg(turn1), assistantMsg("assistant one REWRITTEN"), cachedUserMsg(turn2), liveUserMsg("live three")))

	for i, body := range [][]byte{bodyT1, bodyT2, bodyDrift, bodyT2} {
		for _, session := range []string{"", "sess-guard"} {
			if !srv.cacheEpochAllows(request(), adapter, meta, body, session) {
				t.Fatalf("header-less request %d (session %q) was refused", i, session)
			}
		}
	}

	declared := func(digest string) *http.Request {
		return request("x-cave-cache-epoch", "epoch-1", "x-cave-cache-prefix-sha256", strings.Repeat(digest, 64))
	}
	if !srv.cacheEpochAllows(declared("a"), adapter, meta, bodyT1, "sess-guard") {
		t.Fatal("a complete framework declaration must be allowed")
	}
	if !srv.cacheEpochAllows(declared("a"), adapter, meta, bodyT2, "sess-guard") {
		t.Fatal("the same declared prefix must stay allowed")
	}
	if srv.cacheEpochAllows(declared("b"), adapter, meta, bodyT2, "sess-guard") {
		t.Fatal("a declared prefix that drifted must veto new compression")
	}
	if srv.cacheEpochAllows(request("x-cave-cache-epoch", "epoch-2"), adapter, meta, bodyT1, "sess-guard") {
		t.Fatal("a partial declaration must fail closed")
	}
}

// TestPrefixMonitorFlagsCavemanBust: turn 1's replacement row is gone by turn
// 2 (the store evicted it), so the proxy re-sends raw bytes where turn 1 sent
// the replacement while the client's bytes did not change. That is caveman's
// own bust, which the frozen-floor monitor never saw: it only compared the
// messages below the floor, and the one turn 1 cached sat above it.
func TestPrefixMonitorFlagsCavemanBust(t *testing.T) {
	x, y := turnText(1), turnText(2)
	var logs bytes.Buffer
	cache := newTestPrefixCache()
	rt := prefixStableTransport(2)
	srv, sink := newSubscriptionCompressServer(&stableCompressor{}, rt, Config{
		PrefixCache:    cache,
		RecoveryViaMCP: true,
		Logger:         slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})),
	})
	headers := withHeaders(subscriptionAgentHeaders, "x-cave-session", "sess-tripwire")

	serveBody(t, srv, "/v1/messages", newestMarkedConversation(x), headers)
	cache.mu.Lock()
	cache.entries = map[string]testReplacement{}
	cache.mu.Unlock()
	serveBody(t, srv, "/v1/messages", newestMarkedConversation(x, y), headers)

	if !bytes.Contains(rt.bodies[0], []byte("<<ccr:")) || !strings.Contains(string(rt.bodies[1]), x) {
		t.Fatal("test setup: want turn 1 compressed and turn 2 re-sending it raw")
	}
	if row := sink.last(t); !row.CacheBust || row.CacheBustCause != bustCauseCaveman {
		t.Fatalf("a caveman-caused bust was not flagged as caveman's: bust=%v cause=%q", row.CacheBust, row.CacheBustCause)
	}
	if !strings.Contains(logs.String(), `level=ERROR msg="caveman changed bytes the provider already cached"`) {
		t.Fatalf("no ERROR line for a caveman-caused bust:\n%s", logs.String())
	}
}

// TestPrefixMonitorClientEditIsNotAWarning: the client rewriting its own
// history is still flagged on the row, but it is not caveman's doing, so it
// must not raise a warning (nearly every old WARN was one of these).
func TestPrefixMonitorClientEditIsNotAWarning(t *testing.T) {
	var logs bytes.Buffer
	rt := &captureTransport{responses: []string{subMessageRespBody, subMessageRespBody}}
	sink := &captureSink{}
	srv := New(Config{
		Adapters:   []providers.Adapter{anthropic.New("https://upstream.test")},
		Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		Creds:      passthroughTestCreds{},
		Sink:       sink,
		HTTPClient: &http.Client{Transport: rt},
		Logger:     slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})),
	})
	headers := map[string]string{"x-cave-session": "sess-edit", "x-api-key": "sk-ant-test", "anthropic-version": "2023-06-01"}
	turn1 := strings.Repeat("turn one project context ", 30)
	turn2 := strings.Repeat("turn two file contents ", 30)
	serveBody(t, srv, "/v1/messages", anthropicRawBody("You are Claude Code.",
		cachedUserMsg(turn1), assistantMsg("assistant one"), cachedUserMsg(turn2), liveUserMsg("live one")), headers)
	serveBody(t, srv, "/v1/messages", anthropicRawBody("You are Claude Code.",
		cachedUserMsg(turn1), assistantMsg("assistant one REWRITTEN"), cachedUserMsg(turn2), liveUserMsg("live two")), headers)

	if row := sink.last(t); !row.CacheBust || row.CacheBustCause != bustCauseClient {
		t.Fatalf("the client's own edit must still be flagged, as the client's: bust=%v cause=%q", row.CacheBust, row.CacheBustCause)
	}
	if strings.Contains(logs.String(), "level=WARN") || strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("a client-caused change was logged as a warning:\n%s", logs.String())
	}
}

// TestPrefixMonitorSkipsCallerOptOut: a framework caller that opts one request
// out of transforms chose raw bytes for it; the cache that costs is not
// caveman's doing and must not count as caveman's bust.
func TestPrefixMonitorSkipsCallerOptOut(t *testing.T) {
	x, y := turnText(1), turnText(2)
	rt := prefixStableTransport(2)
	srv, sink := newSubscriptionCompressServer(&stableCompressor{}, rt, Config{RecoveryViaMCP: true})
	headers := withHeaders(subscriptionAgentHeaders, "x-cave-session", "sess-optout")

	serveBody(t, srv, "/v1/messages", newestMarkedConversation(x), headers)
	serveBody(t, srv, "/v1/messages", newestMarkedConversation(x, y), withHeaders(headers, "x-cave-transforms", "caveman.pass-through.v1"))

	if !bytes.Contains(rt.bodies[0], []byte("<<ccr:")) || !bytes.Equal(rt.bodies[1], []byte(newestMarkedConversation(x, y))) {
		t.Fatal("test setup: want turn 1 compressed and the opted-out turn 2 raw")
	}
	if row := sink.last(t); row.CacheBustCause == bustCauseCaveman {
		t.Fatal("a caller opt-out was counted as caveman's bust")
	}
}

// TestTripwireFollowsThePinAcrossSessions: the raw retry that pinned a
// conversation can arrive without a session id (correlation is best effort),
// so the tripwire cannot count on having seen it. The pin itself says the turn
// after it is held to the raw request, not to the compressed turn before it.
func TestTripwireFollowsThePinAcrossSessions(t *testing.T) {
	h := newInvariantHarness(t)
	c := newCCConversation("You are Claude Code.", "sess-a")
	h.send(c.user(filler("one")), sendOpts{})
	c.session = ""
	h.send(c.user(filler("two")), sendOpts{respond: rejectTransformed(http.StatusTooManyRequests, nil)})
	c.session = "sess-a"
	h.send(c.user(filler("three")), sendOpts{})
	h.compressedSomething()
	h.assert()
}

// TestTripwireLeverFreezeIsNotCavemansBust: the harm tripwire freezing the
// tool-schema strip for a session is a deliberate one-time rollover of the
// catalog (see stripToolSchema). The cache tripwire must not count it as a
// caveman bug: it is not one, and status would raise it as one.
func TestTripwireLeverFreezeIsNotCavemansBust(t *testing.T) {
	marked := func(live string) string {
		return `{"model":"claude-sonnet-4-6","max_tokens":1024,` +
			`"system":[{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}}],` +
			`"tools":` + toolCatalog + `,` +
			`"messages":[{"role":"user","content":[{"type":"text","text":"` + live + `","cache_control":{"type":"ephemeral"}}]}]}`
	}
	responses := []string{
		planRespBody(1_000),
		planRespBody(10 * tripwireCacheCreationFloorTokens),
		planRespBody(10 * tripwireCacheCreationFloorTokens),
		planRespBody(10 * tripwireCacheCreationFloorTokens),
		planRespBody(1_000),
	}
	rt := &captureTransport{responses: responses}
	srv, sink := newToolSchemaStripServer(t, &toolSchemaStripCompressor{}, rt, Config{RecoveryViaMCP: true, ToolSchemaStrip: toolSchemaStripMode})
	headers := withHeaders(subscriptionAgentHeaders, "x-cave-session", "sess-harm")
	var last string
	for range responses {
		last = serveBody(t, srv, "/v1/messages", marked("same turn"), headers).Header().Get("x-caveman-tripwire")
	}
	if last != toolSchemaStripOptimizerID+"=frozen" || strings.Contains(string(rt.bodies[len(rt.bodies)-1]), strippedToolCatalog(t)) {
		t.Fatalf("test setup: want the strip frozen and the last request on the original catalog (tripwire %q)", last)
	}
	row := sink.last(t)
	if row.CacheBustCause == bustCauseCaveman {
		t.Fatal("the harm tripwire's deliberate rollover was counted as caveman's bust")
	}
	if row.CacheBustCause != bustCauseLeverFreeze {
		t.Fatalf("the rollover must be recorded as %q, got %q", bustCauseLeverFreeze, row.CacheBustCause)
	}
}

// TestTripwireFlagsADroppedBreakpoint: a forwarded request that caches less
// than the client asked for leaves the next turn no entry to read, and only
// caveman changes forwarded bytes.
func TestTripwireFlagsADroppedBreakpoint(t *testing.T) {
	var logs bytes.Buffer
	srv := New(Config{PrefixCache: newTestPrefixCache(), Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))})
	adapter := anthropic.New("https://upstream.test")
	meta := providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages", Model: "claude-fable-5"}
	body := []byte(`{"model":"claude-fable-5","system":"S","messages":[{"role":"user","content":[{"type":"text","text":"a long log","cache_control":{"type":"ephemeral"}}]}]}`)
	accepted := []byte(`{"model":"claude-fable-5","system":"S","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]}]}`)

	cause := srv.observeCachedPrefix(adapter, meta, body, accepted, acceptance{sent: srv.prefixSeq.Add(1), session: "sess-marker", requestID: "req-marker"})
	if cause != bustCauseCaveman {
		t.Fatalf("a dropped breakpoint must count as caveman's bust, got %q", cause)
	}
	if !strings.Contains(logs.String(), `level=ERROR msg="caveman dropped a cache breakpoint the client set"`) {
		t.Fatalf("no ERROR line for a dropped breakpoint:\n%s", logs.String())
	}
}
