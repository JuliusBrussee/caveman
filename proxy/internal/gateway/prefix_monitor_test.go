package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
)

// anthropicSessionBody is a Claude Code shaped Anthropic request whose system text
// is parameterized so a test can hold every frozen component stable except one.
func anthropicSessionBody(systemText, liveText string) string {
	turn1 := strings.Repeat("turn one project context ", 30)
	return `{"model":"claude-sonnet-4-6","max_tokens":1024,` +
		`"system":[{"type":"text","text":"` + systemText + `","cache_control":{"type":"ephemeral"}}],` +
		`"tools":[{"name":"Read","description":"Read a file","input_schema":{"type":"object"}}],` +
		`"messages":[` +
		`{"role":"user","content":[` + subCachedBlock(turn1) + `]},` +
		`{"role":"assistant","content":[` + subBlock("assistant one") + `]},` +
		`{"role":"user","content":[` + subBlock(liveText) + `]}` +
		`]}`
}

// TestPrefixMonitorDetectsNonExtendingPrefix unit-proves the ported
// providerFrozenExtends check: a first observation never busts, an append-only
// extension never busts, and a prefix that mutates an already-frozen component is
// flagged with the FIRST diverging component index.
func TestPrefixMonitorDetectsNonExtendingPrefix(t *testing.T) {
	m := newPrefixMonitor()

	if bust, idx := m.observe("", "a,b"); bust || idx != -1 {
		t.Fatalf("empty session must not compare: bust=%v idx=%d", bust, idx)
	}
	if bust, idx := m.observe("s1", "h0,h1,h2"); bust || idx != -1 {
		t.Fatalf("first observation must not bust: bust=%v idx=%d", bust, idx)
	}
	// Append-only extension (new component appended) is a legitimate cache extension.
	if bust, idx := m.observe("s1", "h0,h1,h2,h3"); bust || idx != -1 {
		t.Fatalf("append-only extension must not bust: bust=%v idx=%d", bust, idx)
	}
	// A mutated component at index 1 is NOT an extension: first divergence is 1.
	if bust, idx := m.observe("s1", "h0,MUT,h2,h3,h4"); !bust || idx != 1 {
		t.Fatalf("mutated component must bust at index 1: bust=%v idx=%d", bust, idx)
	}
	// A shorter prefix that drops the tail also fails to extend; first divergence is
	// where the current prefix runs out relative to the prior one.
	m2 := newPrefixMonitor()
	m2.observe("s2", "h0,h1,h2,h3")
	if bust, idx := m2.observe("s2", "h0,h1"); !bust || idx != 2 {
		t.Fatalf("shrinking prefix must bust at index 2: bust=%v idx=%d", bust, idx)
	}
}

// TestProxyRecordsCacheBustOnNonExtendingPrefix drives two requests in one session
// through the proxy: the second changes an already-frozen component (system), so its
// telemetry row is flagged cache_bust while the first is not — observe-only, traffic
// is never blocked or modified.
func TestProxyRecordsCacheBustOnNonExtendingPrefix(t *testing.T) {
	rt := &captureTransport{responses: []string{subMessageRespBody, subMessageRespBody}}
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
	live := strings.Repeat("newest live turn bytes ", 20)
	serveBody(t, srv, "/v1/messages", anthropicSessionBody("You are Claude Code.", live), headers)
	serveBody(t, srv, "/v1/messages", anthropicSessionBody("You are a DIFFERENT assistant now.", live), headers)

	if len(sink.rows) != 2 {
		t.Fatalf("recorded %d rows, want 2", len(sink.rows))
	}
	if sink.rows[0].CacheBust {
		t.Fatalf("first request in a session must never be a cache bust: %+v", sink.rows[0])
	}
	if !sink.rows[1].CacheBust {
		t.Fatalf("a request whose frozen system component changed must be flagged cache_bust: %+v", sink.rows[1])
	}
	// Observe-only: both requests still reached the upstream (no blocking).
	if len(rt.bodies) != 2 {
		t.Fatalf("observe-only monitor must never block traffic: upstream calls=%d", len(rt.bodies))
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

// TestCacheEpochAllowsRunsGuardWithoutHeaders proves SLICE 2 (issue #133): the
// header-less derived-epoch gate runs for wrap clients (Claude Code/Codex/Gemini),
// is EXTENSION-TOLERANT (append-only frozen-prefix growth as the cache floor
// advances stays allowed — the regression the first cut introduced), and still
// denies a genuine frozen-component change. It never blocks traffic; a denial only
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
	// A genuine divergence: the frozen system component changed.
	bodyDrift := []byte(anthropicRawBody("You are a DIFFERENT assistant now.",
		cachedUserMsg(turn1), assistantMsg("assistant one"), cachedUserMsg(turn2), liveUserMsg("live three")))

	// Self-validate the construction: turn-1's frozen components must be a strict
	// comma-prefix of turn-2's (append-only), and turn-drift's must not be.
	_, compsT1, okT1 := providerPrefixEvidence(adapter, bodyT1, meta)
	_, compsT2, okT2 := providerPrefixEvidence(adapter, bodyT2, meta)
	_, compsDrift, okD := providerPrefixEvidence(adapter, bodyDrift, meta)
	if !okT1 || !okT2 || !okD {
		t.Fatalf("frozen prefix evidence unavailable: t1=%v t2=%v drift=%v", okT1, okT2, okD)
	}
	if !strings.HasPrefix(compsT2+",", compsT1+",") {
		t.Fatalf("test setup: turn 2 must append-only extend turn 1\n t1=%s\n t2=%s", compsT1, compsT2)
	}
	if strings.HasPrefix(compsDrift+",", compsT1+",") {
		t.Fatalf("test setup: drift body must NOT extend turn 1 (system changed)\n t1=%s\n drift=%s", compsT1, compsDrift)
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
	// A genuine frozen-component change is denied (forward original bytes).
	if srv.cacheEpochAllows(noHeaderReq(), adapter, meta, bodyDrift, "sess-guard") {
		t.Fatal("a changed frozen component must be denied by the derived-epoch gate")
	}
	// After the divergence re-anchors, an append-only extension of the NEW prefix is
	// allowed again — the gate never gets stuck (the old whole-prefix bug).
	bodyDriftGrown := []byte(anthropicRawBody("You are a DIFFERENT assistant now.",
		cachedUserMsg(turn1), assistantMsg("assistant one"), cachedUserMsg(turn2), assistantMsg("assistant two"), cachedUserMsg("turn three"), liveUserMsg("live four")))
	if !srv.cacheEpochAllows(noHeaderReq(), adapter, meta, bodyDriftGrown, "sess-guard") {
		t.Fatal("gate must re-anchor after divergence and allow subsequent extension")
	}
}
