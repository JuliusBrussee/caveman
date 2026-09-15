package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

// Cross-turn cache-prefix stability. Live-zone compression rewrites the newest
// turn; on the NEXT request the agent re-sends that same turn as its ORIGINAL
// bytes, now below the cache_control floor. Forwarding those originals would flip
// the upstream prefix back to a form the provider never cached — a guaranteed miss
// every turn, on a request the row claims to have shrunk. These tests pin the fix:
// a message this proxy compressed is substituted, byte-identically, forever after.

// testPrefixCache is the in-memory stand-in for the durable SQLite replacement
// cache the binary wires. Keyed by content hash, first-write-wins, and able to
// simulate an unavailable store and an eviction.
type testPrefixCache struct {
	mu         sync.Mutex
	entries    map[string]testReplacement
	failWrites bool
}

type testReplacement struct {
	replacement []byte
	handle      string
}

func newTestPrefixCache() *testPrefixCache {
	return &testPrefixCache{entries: map[string]testReplacement{}}
}

func (c *testPrefixCache) LookupReplacement(scope string, original []byte) ([]byte, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[scope+":"+contentHandle(original)]
	if !ok {
		return nil, "", false
	}
	return append([]byte(nil), entry.replacement...), entry.handle, true
}

func (c *testPrefixCache) RememberReplacement(scope string, original, replacement []byte, handle string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failWrites {
		return nil, errTestPrefixCacheDown
	}
	key := scope + ":" + contentHandle(original)
	if existing, ok := c.entries[key]; ok {
		return append([]byte(nil), existing.replacement...), nil
	}
	c.entries[key] = testReplacement{replacement: append([]byte(nil), replacement...), handle: handle}
	return append([]byte(nil), replacement...), nil
}

// evict drops one entry, standing in for the store's bounded-storage eviction.
func (c *testPrefixCache) evict(scope string, original []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, scope+":"+contentHandle(original))
}

type testPrefixCacheError struct{}

func (testPrefixCacheError) Error() string { return "prefix cache unavailable" }

var errTestPrefixCacheDown = testPrefixCacheError{}

// stableCompressor emits a distinct, content-derived replacement per block so a
// test can tell WHICH original a given upstream byte range came from. nonce stands
// in for a different engine build/process: change it and the same input compresses
// to different bytes, which is exactly what the durable cache has to absorb.
type stableCompressor struct {
	mu     sync.Mutex
	nonce  string
	calls  int
	stored [][]byte
}

func (c *stableCompressor) CompressSegment(seg []byte) ([]byte, int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return []byte("CMP" + c.nonce + ":" + contentHandle(seg)), 100, 40
}

func (c *stableCompressor) StoreOriginal(body []byte) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stored = append(c.stored, append([]byte(nil), body...))
	return contentHandle(body), nil
}

// expectedReplacement is the exact JSON-encoded string the splice must produce for
// a block this compressor shrank: compressed bytes, newline, CCR marker.
func (c *stableCompressor) expectedReplacement(t *testing.T, original string) string {
	t.Helper()
	handle := contentHandle([]byte(original))
	// The splice encodes with HTML escaping OFF, so the `<<ccr:` marker survives
	// literally; encode the expectation the same way.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode("CMP" + c.nonce + ":" + handle + "\n<<ccr:" + handle + ">>"); err != nil {
		t.Fatalf("encode expected replacement: %v", err)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// markedTurnConversation builds the Claude Code request shape for a conversation
// of n user turns: every user turn carries a cache_control breakpoint (Claude Code
// marks the NEWEST message), with assistant replies interleaved. This is the shape
// that triggers the frozen-floor clamp — turn N's live zone is turn N+1's frozen
// prefix.
func markedTurnConversation(userTexts ...string) string {
	msgs := make([]string, 0, len(userTexts)*2)
	for i, text := range userTexts {
		if i > 0 {
			msgs = append(msgs, `{"role":"assistant","content":[`+subBlock("assistant "+strconv.Itoa(i))+`]}`)
		}
		msgs = append(msgs, `{"role":"user","content":[`+subCachedBlock(text)+`]}`)
	}
	return `{"model":"claude-sonnet-4-6","max_tokens":1024,` +
		`"system":[{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}}],` +
		`"messages":[` + strings.Join(msgs, ",") + `]}`
}

func openAITurnConversation(userTexts ...string) string {
	msgs := []string{`{"role":"system","content":"stable system"}`}
	for i, text := range userTexts {
		if i > 0 {
			msgs = append(msgs, `{"role":"assistant","content":"assistant `+strconv.Itoa(i)+`"}`)
		}
		msgs = append(msgs, `{"role":"user","content":`+strconv.Quote(text)+`}`)
	}
	return `{"model":"gpt-5.5","messages":[` + strings.Join(msgs, ",") + `]}`
}

func turnText(n int) string {
	return strings.Repeat("turn "+strconv.Itoa(n)+" client bytes ", 40)
}

func newPrefixStableServer(comp Compressor, cache PrefixCache, rt *captureTransport) (*Server, *captureSink) {
	sink := &captureSink{}
	return New(Config{
		Adapters:       []providers.Adapter{anthropic.New("https://upstream.test")},
		Auth:           stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}},
		Creds:          passthroughTestCreds{},
		Sink:           sink,
		Compressor:     comp,
		PrefixCache:    cache,
		HTTPClient:     &http.Client{Transport: rt},
		RecoveryViaMCP: true,
	}), sink
}

func prefixStableTransport(turns int) *captureTransport {
	responses := make([]string, turns)
	for i := range responses {
		responses[i] = subMessageRespBody
	}
	return &captureTransport{responses: responses}
}

// TestPrefixStableAcrossTurns is the core regression: turn 1 compresses the newest
// turn; on turn 2 that message has dropped below the cache floor and arrives as the
// client's original bytes, and the upstream request must still carry the EXACT
// bytes turn 1 sent for it.
func TestPrefixStableAcrossTurns(t *testing.T) {
	t1, t2 := turnText(1), turnText(2)
	rt := prefixStableTransport(2)
	comp := &stableCompressor{}
	srv, _ := newPrefixStableServer(comp, newTestPrefixCache(), rt)

	serveBody(t, srv, "/v1/messages", markedTurnConversation(t1), subscriptionAgentHeaders)
	serveBody(t, srv, "/v1/messages", markedTurnConversation(t1, t2), subscriptionAgentHeaders)

	rep1 := comp.expectedReplacement(t, t1)
	if !strings.Contains(string(rt.bodies[0]), rep1) {
		t.Fatalf("turn 1 did not compress the live zone: %s", rt.bodies[0])
	}
	if !strings.Contains(string(rt.bodies[1]), rep1) {
		t.Fatalf("turn 2 must re-send turn 1's compressed bytes byte-identically:\n%s", rt.bodies[1])
	}
	if strings.Contains(string(rt.bodies[1]), t1) {
		t.Fatalf("turn 2 flipped the frozen prefix back to the client's originals:\n%s", rt.bodies[1])
	}
	if !strings.Contains(string(rt.bodies[1]), comp.expectedReplacement(t, t2)) {
		t.Fatalf("turn 2's own live zone should still compress: %s", rt.bodies[1])
	}
}

func TestCompiledPlanOverridesEntitlementCompression(t *testing.T) {
	digest := strings.Repeat("a", 64)
	headers := make(http.Header)
	headers.Set("x-cave-transforms", "caveman.pass-through.v1")
	if compiledPlanAllowsCompression(headers) {
		t.Fatal("unlocked explicit pass-through must suppress entitlement compression")
	}
	headers.Set("x-cave-agent-build", digest)
	headers.Set("x-cave-efficiency-plan", digest)
	if compiledPlanAllowsCompression(headers) {
		t.Fatal("locked baseline must suppress entitlement compression")
	}
	headers.Set("x-cave-transforms", "caveman.engine.text.v1")
	if !compiledPlanAllowsCompression(headers) {
		t.Fatal("locked engine text plan should permit compression")
	}
	headers.Set("x-cave-transform-location", "local")
	if compiledPlanAllowsCompression(headers) {
		t.Fatal("locally transformed Pi bytes must never be transformed again")
	}
	headers.Del("x-cave-transform-location")
	headers.Set("x-cave-transforms", "caveman.engine.json.v1")
	contentType, transformID, allowed := compiledPlanCompression(headers)
	if !allowed || contentType != "json" || transformID != "caveman.engine.json.v1" {
		t.Fatalf("typed plan = (%q, %q, %v)", contentType, transformID, allowed)
	}
	headers.Set("x-cave-transforms", "caveman.engine.json.v1,caveman.engine.text.v1")
	if compiledPlanAllowsCompression(headers) {
		t.Fatal("multi-transform plan without route map must fail closed")
	}
	routesJSON := []byte(`[{"segment_kind":"history","transform_id":"caveman.engine.json.v1"},{"segment_kind":"tool_result","transform_id":"caveman.engine.text.v1"}]`)
	headers.Set("x-cave-transform-routes", base64.RawURLEncoding.EncodeToString(routesJSON))
	routes, ok := compiledPlanRoutes(headers)
	if !ok || len(routes) != 2 || routes[0].ContentType != "json" || routes[1].ContentType != "text" {
		t.Fatalf("compiled routes = %#v, %v", routes, ok)
	}
	headers.Del("x-cave-transform-routes")
	headers.Set("x-cave-transforms", "caveman.unknown.v1")
	if compiledPlanAllowsCompression(headers) {
		t.Fatal("unknown locked transform must fail closed")
	}
}

// TestAnthropicPrefixStableTenTurns is EAB-114's full trace: every earlier turn's
// replacement remains byte-identical while each new turn appends past cache floor.
func TestAnthropicPrefixStableTenTurns(t *testing.T) {
	turns := make([]string, 10)
	for i := range turns {
		turns[i] = turnText(i + 1)
	}
	rt := prefixStableTransport(len(turns))
	comp := &stableCompressor{}
	srv, _ := newPrefixStableServer(comp, newTestPrefixCache(), rt)

	for i := range turns {
		serveBody(t, srv, "/v1/messages", markedTurnConversation(turns[:i+1]...), subscriptionAgentHeaders)
	}

	final := string(rt.bodies[len(rt.bodies)-1])
	for _, text := range turns {
		if !strings.Contains(final, comp.expectedReplacement(t, text)) {
			t.Fatalf("turn 10 lost stable replacement for %q:\n%s", text[:20], final)
		}
		if strings.Contains(final, text) {
			t.Fatalf("turn 10 forwarded original already compressed (%q):\n%s", text[:20], final)
		}
	}
	for i := 1; i < len(rt.bodies); i++ {
		previous := string(rt.bodies[i-1])
		replacement := comp.expectedReplacement(t, turns[i-1])
		boundary := strings.Index(previous, replacement) + len(replacement)
		current := string(rt.bodies[i])
		if boundary <= len(replacement) || len(current) < boundary || current[:boundary] != previous[:boundary] {
			t.Fatalf("prefix diverged at turn %d", i+1)
		}
	}
}

func TestOpenAIPrefixStableTenTurns(t *testing.T) {
	turns := make([]string, 10)
	responses := make([]string, 10)
	for i := range turns {
		turns[i] = turnText(i + 1)
		responses[i] = `{"id":"chatcmpl","model":"gpt-5.5","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":10}}`
	}
	rt := &captureTransport{responses: responses}
	comp := &stableCompressor{}
	srv := New(Config{
		Adapters:       []providers.Adapter{openai.New("https://upstream.test")},
		Auth:           stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}},
		Creds:          passthroughTestCreds{},
		Sink:           &captureSink{},
		Compressor:     comp,
		PrefixCache:    newTestPrefixCache(),
		HTTPClient:     &http.Client{Transport: rt},
		RecoveryViaMCP: true,
	})
	paygHeaders := map[string]string{"authorization": "Bearer sk-openai-test"}
	for i := range turns {
		serveBody(t, srv, "/v1/chat/completions", openAITurnConversation(turns[:i+1]...), paygHeaders)
	}
	final := string(rt.bodies[len(rt.bodies)-1])
	for _, text := range turns {
		if !strings.Contains(final, comp.expectedReplacement(t, text)) || strings.Contains(final, text) {
			t.Fatalf("OpenAI turn 10 prefix lost replacement for %q", text[:20])
		}
	}
}

// TestPrefixStableSurvivesRestart pins durability: a new process with a fresh
// compressor whose output has changed (a different engine build) must still send
// the replacement the ORIGINAL process stored, because the durable cache — not the
// compressor — is the authority on what a message's bytes are.
func TestPrefixStableSurvivesRestart(t *testing.T) {
	t1, t2 := turnText(1), turnText(2)
	cache := newTestPrefixCache()

	rt1 := prefixStableTransport(1)
	first := &stableCompressor{nonce: "v1"}
	srv1, _ := newPrefixStableServer(first, cache, rt1)
	serveBody(t, srv1, "/v1/messages", markedTurnConversation(t1), subscriptionAgentHeaders)

	rt2 := prefixStableTransport(1)
	second := &stableCompressor{nonce: "v2"}
	srv2, _ := newPrefixStableServer(second, cache, rt2)
	serveBody(t, srv2, "/v1/messages", markedTurnConversation(t1, t2), subscriptionAgentHeaders)

	if !strings.Contains(string(rt2.bodies[0]), first.expectedReplacement(t, t1)) {
		t.Fatalf("post-restart request must reuse the stored replacement, not recompress:\n%s", rt2.bodies[0])
	}
	if strings.Contains(string(rt2.bodies[0]), second.expectedReplacement(t, t1)) {
		t.Fatalf("post-restart request recompressed a frozen turn and diverged the prefix:\n%s", rt2.bodies[0])
	}
}

// TestPrefixCacheUnavailablePassesThrough pins the fail-open rule: a replacement
// the proxy cannot persist must never go on the wire, because it could not be
// reproduced next turn. The request is forwarded byte-identical and claims nothing.
func TestPrefixCacheUnavailablePassesThrough(t *testing.T) {
	body := markedTurnConversation(turnText(1))
	rt := prefixStableTransport(1)
	cache := newTestPrefixCache()
	cache.failWrites = true
	srv, sink := newPrefixStableServer(&stableCompressor{}, cache, rt)

	serveBody(t, srv, "/v1/messages", body, subscriptionAgentHeaders)

	if sha256.Sum256(rt.bodies[0]) != sha256.Sum256([]byte(body)) {
		t.Fatalf("unpersistable replacement must be byte-identical passthrough:\n got %s\nwant %s", rt.bodies[0], body)
	}
	if row := sink.last(t); row.CompressionTokensBefore != 0 || row.RecoveryHandle != "" {
		t.Fatalf("row must claim no compression: %+v", row)
	}
}

// TestPrefixSubstitutionBooksSavingsOnce is the no-fake-savings pin for the
// substitution path: a message's token delta is counted on the turn it was first
// compressed and never again, so a long conversation cannot re-book turn 1's
// reduction on every later request.
func TestPrefixSubstitutionBooksSavingsOnce(t *testing.T) {
	t1, t2 := turnText(1), turnText(2)
	rt := prefixStableTransport(2)
	srv, sink := newPrefixStableServer(&stableCompressor{}, newTestPrefixCache(), rt)

	serveBody(t, srv, "/v1/messages", markedTurnConversation(t1), subscriptionAgentHeaders)
	firstRow := sink.last(t)
	serveBody(t, srv, "/v1/messages", markedTurnConversation(t1, t2), subscriptionAgentHeaders)
	secondRow := sink.last(t)

	if firstRow.CompressionTokensBefore != 100 || firstRow.CompressionTokensAfter != 40 {
		t.Fatalf("turn 1 row = before %d after %d, want 100/40", firstRow.CompressionTokensBefore, firstRow.CompressionTokensAfter)
	}
	// Turn 2 rewrote two blocks but earned only ONE new reduction; 200/80 here would
	// mean turn 1's delta was claimed a second time.
	if secondRow.CompressionTokensBefore != 100 || secondRow.CompressionTokensAfter != 40 {
		t.Fatalf("turn 2 row = before %d after %d, want 100/40 (turn 1's delta must not be re-booked)", secondRow.CompressionTokensBefore, secondRow.CompressionTokensAfter)
	}
	if !strings.Contains(secondRow.RecoveryHandle, contentHandle([]byte(t1))) {
		t.Fatalf("turn 2 must still disclose the substituted block's recovery handle: %q", secondRow.RecoveryHandle)
	}
}

// TestPrefixSubstitutionOnlyTurnClaimsNoTokens covers the extreme of the same rule:
// a turn that only substitutes (no new live-zone content shrank) rewrites bytes but
// records a zero reduction.
func TestPrefixSubstitutionOnlyTurnClaimsNoTokens(t *testing.T) {
	body := markedTurnConversation(turnText(1))
	rt := prefixStableTransport(2)
	comp := &stableCompressor{}
	srv, sink := newPrefixStableServer(comp, newTestPrefixCache(), rt)

	serveBody(t, srv, "/v1/messages", body, subscriptionAgentHeaders)
	serveBody(t, srv, "/v1/messages", body, subscriptionAgentHeaders)

	if !bytes.Equal(rt.bodies[0], rt.bodies[1]) {
		t.Fatalf("a replayed request must produce byte-identical upstream bytes:\n%s\n%s", rt.bodies[0], rt.bodies[1])
	}
	row := sink.last(t)
	if row.CompressionTokensBefore != 0 || row.CompressionTokensAfter != 0 || row.CompressionRatio != 0 {
		t.Fatalf("a substitution-only turn must claim no new reduction: %+v", row)
	}
	if row.RecoveryHandle == "" {
		t.Fatal("a substitution-only turn must still disclose its recovery handle")
	}
}

// TestPrefixCacheEvictionDegradesToOriginals pins the bounded-storage behavior: an
// evicted entry is a plain miss, so that message reverts to the client's original
// bytes — one prefix rebuild, then stable again. It never produces a half-applied
// prefix.
func TestPrefixCacheEvictionDegradesToOriginals(t *testing.T) {
	t1, t2, t3 := turnText(1), turnText(2), turnText(3)
	rt := prefixStableTransport(3)
	cache := newTestPrefixCache()
	comp := &stableCompressor{}
	srv, _ := newPrefixStableServer(comp, cache, rt)

	serveBody(t, srv, "/v1/messages", markedTurnConversation(t1), subscriptionAgentHeaders)
	serveBody(t, srv, "/v1/messages", markedTurnConversation(t1, t2), subscriptionAgentHeaders)
	cache.evict("unlocked", []byte(t1))
	serveBody(t, srv, "/v1/messages", markedTurnConversation(t1, t2, t3), subscriptionAgentHeaders)

	third := string(rt.bodies[2])
	if !strings.Contains(third, t1) {
		t.Fatalf("an evicted message must fall back to the client's original bytes:\n%s", third)
	}
	if !strings.Contains(third, comp.expectedReplacement(t, t2)) {
		t.Fatalf("a still-cached message must keep its stable replacement:\n%s", third)
	}
}

type adapterWithoutPrefixStabilizer struct{ providers.Adapter }

// TestLiveZoneRequiresPrefixStabilizer pins the capability gate:
// OpenAI is eligible because it exposes frozen/live Responses/chat zones, while
// an otherwise identical adapter that does not expose that optional contract
// remains byte-identical passthrough.
func TestLiveZoneRequiresPrefixStabilizer(t *testing.T) {
	live := strings.Repeat("openai subscription live bytes ", 40)
	body := `{"model":"gpt-5.5","messages":[{"role":"user","content":"` + live + `"}]}`
	headers := map[string]string{"user-agent": "cursor/1.2.3", "authorization": "Bearer sk-ant-oat-test"}

	t.Run("prefix-stable OpenAI compresses", func(t *testing.T) {
		rt := &captureTransport{responses: []string{chatRespBody}}
		comp := &stableCompressor{}
		srv := newSocketFreeCompressServerWithConfig(openai.New("https://upstream.test"), comp, rt, passthroughTestCreds{}, Config{
			PrefixCache: newTestPrefixCache(), RecoveryViaMCP: true,
		})
		serveBody(t, srv, "/v1/chat/completions", body, headers)
		if sha256.Sum256(rt.bodies[0]) == sha256.Sum256([]byte(body)) || comp.calls == 0 {
			t.Fatalf("prefix-stable subscription traffic should compress: %s", rt.bodies[0])
		}
	})

	t.Run("adapter without prefix contract passes through", func(t *testing.T) {
		rt := &captureTransport{responses: []string{chatRespBody}}
		comp := &stableCompressor{}
		adapter := adapterWithoutPrefixStabilizer{Adapter: openai.New("https://upstream.test")}
		srv := newSocketFreeCompressServerWithConfig(adapter, comp, rt, passthroughTestCreds{}, Config{
			PrefixCache: newTestPrefixCache(), RecoveryViaMCP: true,
		})
		serveBody(t, srv, "/v1/chat/completions", body, headers)
		if sha256.Sum256(rt.bodies[0]) != sha256.Sum256([]byte(body)) || comp.calls != 0 {
			t.Fatalf("adapter without prefix contract must pass through unchanged: %s", rt.bodies[0])
		}
	})
}

// TestOAuthLiveZoneNeedsNoAccount pins the invariant for the OAuth classification:
// OAuth-authenticated traffic compresses with no Caveman account, but still fails
// closed without the agent's own caveman_retrieve MCP recovery — these paths are
// marker-only, so without it the elided detail would be unreachable.
func TestOAuthLiveZoneNeedsNoAccount(t *testing.T) {
	oauthHeaders := map[string]string{
		"authorization":     "Bearer header.payload.signature",
		"anthropic-version": "2023-06-01",
	}
	body := markedTurnConversation(turnText(1))

	t.Run("oauth without MCP recovery passes through", func(t *testing.T) {
		rt := prefixStableTransport(1)
		comp := &stableCompressor{}
		sink := &captureSink{}
		srv := New(Config{
			Adapters:       []providers.Adapter{anthropic.New("https://upstream.test")},
			Auth:           stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}},
			Creds:          passthroughTestCreds{},
			Sink:           sink,
			Compressor:     comp,
			PrefixCache:    newTestPrefixCache(),
			HTTPClient:     &http.Client{Transport: rt},
			RecoveryViaMCP: false,
		})

		serveBody(t, srv, "/v1/messages", body, oauthHeaders)

		if sha256.Sum256(rt.bodies[0]) != sha256.Sum256([]byte(body)) {
			t.Fatalf("oauth without MCP recovery must be byte-identical passthrough:\n got %s\nwant %s", rt.bodies[0], body)
		}
		if comp.calls != 0 {
			t.Fatalf("oauth without MCP recovery must not compress (calls=%d)", comp.calls)
		}
	})

	t.Run("account-less oauth compresses", func(t *testing.T) {
		rt := prefixStableTransport(1)
		comp := &stableCompressor{}
		srv, sink := newPrefixStableServer(comp, newTestPrefixCache(), rt)

		serveBody(t, srv, "/v1/messages", body, oauthHeaders)

		if !strings.Contains(string(rt.bodies[0]), comp.expectedReplacement(t, turnText(1))) {
			t.Fatalf("account-less oauth should take the live-zone path: %s", rt.bodies[0])
		}
		if row := sink.last(t); row.AuthMode != string(AuthModeOAuth) {
			t.Fatalf("auth_mode = %q, want oauth", row.AuthMode)
		}
	})
}

// driftCompressor is the adversary the durable cache exists to absorb: its output
// for the SAME input changes on every call. If anything other than the cache is
// authoritative for a previously-emitted replacement, the prefix drifts.
type driftCompressor struct {
	mu    sync.Mutex
	calls int
}

func (c *driftCompressor) CompressSegment(seg []byte) ([]byte, int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return []byte("DRIFT" + strconv.Itoa(c.calls) + ":" + contentHandle(seg)), 100, 40
}

func (c *driftCompressor) StoreOriginal(body []byte) (string, error) {
	return contentHandle(body), nil
}

// TestPrefixCacheBeatsNonDeterministicCompressor pins WHO is authoritative: once a
// replacement has gone upstream, the cache re-emits those exact bytes even when the
// compressor would now produce different ones (a different engine build, a
// non-deterministic pass). Re-compressing instead would burn the developer's prompt
// cache on every turn.
func TestPrefixCacheBeatsNonDeterministicCompressor(t *testing.T) {
	t1, t2, t3 := turnText(1), turnText(2), turnText(3)
	rt := prefixStableTransport(3)
	srv, _ := newPrefixStableServer(&driftCompressor{}, newTestPrefixCache(), rt)

	serveBody(t, srv, "/v1/messages", markedTurnConversation(t1), subscriptionAgentHeaders)
	serveBody(t, srv, "/v1/messages", markedTurnConversation(t1, t2), subscriptionAgentHeaders)
	serveBody(t, srv, "/v1/messages", markedTurnConversation(t1, t2, t3), subscriptionAgentHeaders)

	first := string(rt.bodies[0])
	start := strings.Index(first, "DRIFT")
	if start < 0 {
		t.Fatalf("turn 1 did not compress:\n%s", first)
	}
	end := strings.Index(first[start:], `\n<<ccr:`)
	if end < 0 {
		t.Fatalf("turn 1 replacement has no marker:\n%s", first)
	}
	emitted := first[start : start+end]
	if !strings.HasPrefix(emitted, "DRIFT1:") {
		t.Fatalf("unexpected first emission %q", emitted)
	}
	for i, body := range rt.bodies[1:] {
		if !strings.Contains(string(body), emitted) {
			t.Fatalf("turn %d lost turn 1's exact emitted bytes %q:\n%s", i+2, emitted, body)
		}
	}
}

// TestPrefixStableUnderConcurrency is the regression for the contention bug: the
// proxy serves turns concurrently (parallel agents, subagents), and the store that
// backs the cache is also the telemetry sink, so a lookup always races a write. If
// contention degrades a lookup into a miss, identical requests put DIFFERENT bytes
// on the wire and the provider's prompt cache is missed non-deterministically.
func TestPrefixStableUnderConcurrency(t *testing.T) {
	body := markedTurnConversation(turnText(1))
	const n = 8
	rt := prefixStableTransport(n)
	srv, _ := newPrefixStableServer(&driftCompressor{}, newTestPrefixCache(), rt)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			for k, v := range subscriptionAgentHeaders {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d", rec.Code)
			}
		}()
	}
	wg.Wait()

	if len(rt.bodies) != n {
		t.Fatalf("upstream calls = %d, want %d", len(rt.bodies), n)
	}
	seen := map[[32]byte]int{}
	for _, b := range rt.bodies {
		seen[sha256.Sum256(b)]++
	}
	if len(seen) != 1 {
		t.Fatalf("concurrent identical requests produced %d distinct upstream bodies; the prefix is not stable under concurrency", len(seen))
	}
}

// toolResultConversation is the realistic Claude Code shape: each turn is a user
// message carrying a large tool_result plus its cache_control breakpoint.
func toolResultConversation(payloads ...string) string {
	msgs := make([]string, 0, len(payloads)*2)
	for i, p := range payloads {
		if i > 0 {
			msgs = append(msgs, `{"role":"assistant","content":[{"type":"tool_use","id":"tu`+strconv.Itoa(i)+`","name":"read","input":{}}]}`)
		}
		msgs = append(msgs, `{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu`+strconv.Itoa(i)+`","content":"`+p+`","cache_control":{"type":"ephemeral"}}]}`)
	}
	return `{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[` + strings.Join(msgs, ",") + `]}`
}

// TestPrefixStableAcrossTurnsToolResult runs the same invariant over tool_result
// blocks, which is where the real bytes are in an agent conversation.
func TestPrefixStableAcrossTurnsToolResult(t *testing.T) {
	p1 := strings.Repeat("file contents alpha ", 40)
	p2 := strings.Repeat("file contents beta ", 40)
	rt := prefixStableTransport(2)
	comp := &stableCompressor{}
	srv, _ := newPrefixStableServer(comp, newTestPrefixCache(), rt)

	serveBody(t, srv, "/v1/messages", toolResultConversation(p1), subscriptionAgentHeaders)
	serveBody(t, srv, "/v1/messages", toolResultConversation(p1, p2), subscriptionAgentHeaders)

	if !strings.Contains(string(rt.bodies[1]), comp.expectedReplacement(t, p1)) {
		t.Fatalf("turn 2 lost turn 1's tool_result replacement:\n%s", rt.bodies[1])
	}
	if strings.Contains(string(rt.bodies[1]), p1) {
		t.Fatalf("tool_result prefix flipped back to originals:\n%s", rt.bodies[1])
	}
}
