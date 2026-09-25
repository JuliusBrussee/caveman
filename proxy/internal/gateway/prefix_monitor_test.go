package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
)

// TestPrefixMonitorDetectsNonExtendingPrefix unit-proves the ported
// providerFrozenExtends check: a first observation never busts, an append-only
// extension never busts, and a prefix that mutates an already-frozen message of
// the same conversation is flagged with the FIRST diverging component index.
func TestPrefixMonitorDetectsNonExtendingPrefix(t *testing.T) {
	m := newPrefixMonitor()

	if bust, idx := m.observe("", "a,b"); bust || idx != -1 {
		t.Fatalf("empty session must not compare: bust=%v idx=%d", bust, idx)
	}
	if bust, idx := m.observe("s1", "lit,sys,tools,m1,m2"); bust || idx != -1 {
		t.Fatalf("first observation must not bust: bust=%v idx=%d", bust, idx)
	}
	// Append-only extension (new component appended) is a legitimate cache extension.
	if bust, idx := m.observe("s1", "lit,sys,tools,m1,m2,m3"); bust || idx != -1 {
		t.Fatalf("append-only extension must not bust: bust=%v idx=%d", bust, idx)
	}
	// A mutated frozen message is NOT an extension: first divergence is 4.
	if bust, idx := m.observe("s1", "lit,sys,tools,m1,MUT,m3,m4"); !bust || idx != 4 {
		t.Fatalf("mutated component must bust at index 4: bust=%v idx=%d", bust, idx)
	}
	// A shorter prefix that drops the tail also fails to extend; first divergence is
	// where the current prefix runs out relative to the prior one.
	m2 := newPrefixMonitor()
	m2.observe("s2", "lit,sys,tools,m1,m2,m3")
	if bust, idx := m2.observe("s2", "lit,sys,tools,m1"); !bust || idx != 4 {
		t.Fatalf("shrinking prefix must bust at index 4: bust=%v idx=%d", bust, idx)
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
		"lit,sysA,tools,a1",       // main thread, turn 1
		"lit,sysB,tools",          // side request with its own system prompt
		"lit,sysA,tools,a1,a2",    // main thread, turn 2: extends turn 1
		"lit,sysA,tools,b1",       // sibling subagent: same system/tools, own first message
		"lit,sysA,tools,a1,a2,a3", // main thread, turn 3
		"lit,sysB,tools",          // the side request again (repeat, not drift)
		"lit,sysA,tools,b1,b2",    // sibling, turn 2
		"lit,sysA,tools,a1,a2,a3,a4",
	}
	for i, comps := range steps {
		if bust, idx := m.observe("s", comps); bust {
			t.Fatalf("step %d (%s) flagged as bust at %d: interleaved conversations are not drift", i, comps, idx)
		}
	}
	if bust, idx := m.observe("s", "lit,sysA,tools,a1,MUT,a3,a4"); !bust || idx != 4 {
		t.Fatalf("mutation inside the main thread must still bust at 4: bust=%v idx=%d", bust, idx)
	}
	if bust, idx := m.observe("s", "lit,sysA,tools,b1,b2,b3"); bust {
		t.Fatalf("the sibling must keep extending its own anchor: bust at %d", idx)
	}
	// A different system or tools component is another conversation, never a bust:
	// the monitor cannot tell a subagent's prompt from an injected one.
	if bust, idx := m.observe("s", "lit,SYSMUT,tools,a1,a2"); bust {
		t.Fatalf("a changed system component must read as another conversation: bust at %d", idx)
	}
	if bust, idx := m.observe("s", "lit,sysA,TOOLSMUT,a1,a2"); bust {
		t.Fatalf("a changed tools component must read as another conversation: bust at %d", idx)
	}
}

// TestPrefixMonitorMatchesClosestAnchor pins the anchor choice: a floor-0 request
// (title generation, a classifier, a subagent's first turn) leaves a bare
// [literal, system, tools] anchor that EVERY same-conversation request extends.
// Matching it first would absorb a drifted main-thread request and mask the bust.
func TestPrefixMonitorMatchesClosestAnchor(t *testing.T) {
	m := newPrefixMonitor()
	m.observe("s", "lit,sysA,tools")
	m.observe("s", "lit,sysA,tools,m1,m2,m3")
	m.observe("s", "lit,sysA,tools") // floor-0 again: most recently matched
	if bust, idx := m.observe("s", "lit,sysA,tools,m1,MUT,m3"); !bust || idx != 4 {
		t.Fatalf("drift must be judged against the closest anchor, not the bare one: bust=%v idx=%d", bust, idx)
	}
	if bust, idx := m.observe("s", "lit,sysA,tools,m1,m2,m3,m4"); bust {
		t.Fatalf("the main thread must still extend its own anchor: bust at %d", idx)
	}
	if bust, idx := m.observe("s", "lit,sysA,tools"); bust {
		t.Fatalf("the floor-0 request must still match its bare anchor: bust at %d", idx)
	}
}

func TestPrefixMonitorBoundsAnchorsPerSession(t *testing.T) {
	m := newPrefixMonitor()
	for i := 0; i <= maxAnchorsPerSession; i++ {
		m.observe("s", "lit,sys"+strings.Repeat("x", i)+",tools")
	}
	if got := len(m.last["s"]); got != maxAnchorsPerSession {
		t.Fatalf("session holds %d anchors, want at most %d", got, maxAnchorsPerSession)
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

// TestCacheEpochAllowsRunsGuardWithoutHeaders proves SLICE 2 (issue #133): the
// header-less derived-epoch gate runs for wrap clients (Claude Code/Codex/Gemini),
// is EXTENSION-TOLERANT (append-only frozen-prefix growth as the cache floor
// advances stays allowed — the regression the first cut introduced), tolerates
// another conversation interleaved under the same session (#1094), and still
// denies a genuine frozen-message change. It never blocks traffic; a denial only
// forwards original bytes.
func TestCacheEpochAllowsRunsGuardWithoutHeaders(t *testing.T) {
	srv := New(Config{
		Adapters:    []providers.Adapter{anthropic.New("https://upstream.test")},
		PrefixCache: newTestPrefixCache(),
	})
	adapter := anthropic.New("https://upstream.test")
	meta := providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"}
	noHeaderReq := func() *http.Request {
		return httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	}

	turn1 := strings.Repeat("turn one project context ", 30)
	turn2 := strings.Repeat("turn two file contents ", 30)
	// Turn 1: floor freezes [system, tools, turn1-user].
	bodyT1 := []byte(anthropicRawBody("You are Claude Code.", cachedUserMsg(turn1), liveUserMsg("live one")))
	// Turn 2: the SAME system/tools/turn1 prefix plus new frozen turns (floor advanced).
	// This is an append-only extension of turn 1's frozen prefix — it MUST be allowed.
	bodyT2 := []byte(anthropicRawBody("You are Claude Code.",
		cachedUserMsg(turn1), assistantMsg("assistant one"), cachedUserMsg(turn2), liveUserMsg("live two")))
	// A side request under the same correlated session with its own system prompt.
	bodySide := []byte(anthropicRawBody("You are a haiku side request.", cachedUserMsg(turn1), liveUserMsg("classify")))
	// Turn 3 of the main thread, after the side request interleaved.
	bodyT3 := []byte(anthropicRawBody("You are Claude Code.",
		cachedUserMsg(turn1), assistantMsg("assistant one"), cachedUserMsg(turn2), assistantMsg("assistant two"), cachedUserMsg("turn three"), liveUserMsg("live three")))
	// A genuine divergence: an already-frozen assistant turn was rewritten.
	bodyDrift := []byte(anthropicRawBody("You are Claude Code.",
		cachedUserMsg(turn1), assistantMsg("assistant one REWRITTEN"), cachedUserMsg(turn2), assistantMsg("assistant two"), cachedUserMsg("turn three"), liveUserMsg("live four")))

	// Self-validate the construction: turn-1's frozen components must be a strict
	// comma-prefix of turn-2's (append-only), and drift's must not extend turn-3's.
	_, compsT1, okT1 := providerPrefixEvidence(adapter, bodyT1, meta)
	_, compsT2, okT2 := providerPrefixEvidence(adapter, bodyT2, meta)
	_, compsT3, okT3 := providerPrefixEvidence(adapter, bodyT3, meta)
	_, compsDrift, okD := providerPrefixEvidence(adapter, bodyDrift, meta)
	if !okT1 || !okT2 || !okT3 || !okD {
		t.Fatalf("frozen prefix evidence unavailable: t1=%v t2=%v t3=%v drift=%v", okT1, okT2, okT3, okD)
	}
	if !strings.HasPrefix(compsT2+",", compsT1+",") {
		t.Fatalf("test setup: turn 2 must append-only extend turn 1\n t1=%s\n t2=%s", compsT1, compsT2)
	}
	if strings.HasPrefix(compsDrift+",", compsT3+",") {
		t.Fatalf("test setup: drift body must NOT extend turn 3 (assistant turn rewritten)\n t3=%s\n drift=%s", compsT3, compsDrift)
	}

	// No correlated session id: cannot derive an epoch, so legacy behavior (allow).
	if !srv.cacheEpochAllows(noHeaderReq(), adapter, meta, bodyT1, "") {
		t.Fatal("no session id must fall back to legacy allow")
	}
	// Turn 1 opens the epoch (allowed).
	if !srv.cacheEpochAllows(noHeaderReq(), adapter, meta, bodyT1, "sess-guard") {
		t.Fatal("first derived-epoch request must be allowed")
	}
	// Turn 2 (append-only growth) must STILL be allowed — this is the fix.
	if !srv.cacheEpochAllows(noHeaderReq(), adapter, meta, bodyT2, "sess-guard") {
		t.Fatal("append-only frozen-prefix growth must stay allowed (no turn-2 regression)")
	}
	// A side request with its own system prompt is another conversation: allowed,
	// and it must not displace the main thread's anchor (#1094).
	if !srv.cacheEpochAllows(noHeaderReq(), adapter, meta, bodySide, "sess-guard") {
		t.Fatal("a side request under the same session must be allowed")
	}
	if !srv.cacheEpochAllows(noHeaderReq(), adapter, meta, bodyT3, "sess-guard") {
		t.Fatal("main-thread growth after an interleaved side request must stay allowed")
	}
	// A genuine frozen-component change is denied (forward original bytes).
	if srv.cacheEpochAllows(noHeaderReq(), adapter, meta, bodyDrift, "sess-guard") {
		t.Fatal("a changed frozen component must be denied by the derived-epoch gate")
	}
	// After the divergence re-anchors, an append-only extension of the NEW prefix is
	// allowed again — the gate never gets stuck (the old whole-prefix bug).
	bodyDriftGrown := []byte(anthropicRawBody("You are Claude Code.",
		cachedUserMsg(turn1), assistantMsg("assistant one REWRITTEN"), cachedUserMsg(turn2), assistantMsg("assistant two"), cachedUserMsg("turn three"), assistantMsg("assistant three"), cachedUserMsg("turn four"), liveUserMsg("live five")))
	if !srv.cacheEpochAllows(noHeaderReq(), adapter, meta, bodyDriftGrown, "sess-guard") {
		t.Fatal("gate must re-anchor after divergence and allow subsequent extension")
	}
}
