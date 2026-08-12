package gateway

import (
	"bytes"
	"crypto/sha256"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/gemini"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

// Cross-turn cache-prefix stability for the PAYG compress path on the
// OpenAI-family and Gemini adapters. Neither provider takes a client-declared
// cache_control breakpoint: OpenAI caches prompt prefixes automatically and Gemini
// caches them implicitly. That makes the divergence SILENT — a turn compressed on
// request N was re-sent as the client's original bytes on request N+1, missing the
// cache entry request N paid to create, on rows that claimed a reduction. These
// tests pin the fix per adapter.

const geminiRespBody = `{"candidates":[{"content":{"parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":1000,"candidatesTokenCount":10}}`

// newPAYGPrefixServer builds the PAYG compress session the probe used: a BYOK key
// (so the request classifies payg), MCP recovery (so the marker-only path runs
// without the server-side retrieve tool), and a durable replacement cache.
func newPAYGPrefixServer(adapter providers.Adapter, key string, comp Compressor, cache PrefixCache, rt *captureTransport) (*Server, *captureSink) {
	sink := &captureSink{}
	return New(Config{
		Adapters:       []providers.Adapter{adapter},
		Auth:           stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}},
		Creds:          stubCreds{key: key},
		Sink:           sink,
		Compressor:     comp,
		PrefixCache:    cache,
		HTTPClient:     &http.Client{Transport: rt},
		RecoveryViaMCP: true,
	}), sink
}

func payghTransport(turns int, resp string) *captureTransport {
	responses := make([]string, turns)
	for i := range responses {
		responses[i] = resp
	}
	return &captureTransport{responses: responses}
}

// openaiTurnConversation is an OpenAI chat session of n user turns with assistant
// replies interleaved: turn N's live zone is turn N+1's automatically-cached prefix.
func openaiTurnConversation(userTexts ...string) string {
	msgs := []string{`{"role":"system","content":"You are a helpful assistant."}`}
	for i, text := range userTexts {
		if i > 0 {
			msgs = append(msgs, `{"role":"assistant","content":"assistant reply `+strconv.Itoa(i)+`"}`)
		}
		msgs = append(msgs, `{"role":"user","content":"`+text+`"}`)
	}
	return `{"model":"gpt-5.5","messages":[` + strings.Join(msgs, ",") + `]}`
}

// openaiToolConversation is where an agent's real bytes are: each turn is a tool
// message carrying a large result, followed by the assistant's next tool call.
func openaiToolConversation(payloads ...string) string {
	msgs := []string{}
	for i, payload := range payloads {
		msgs = append(msgs,
			`{"role":"assistant","tool_calls":[{"id":"tc`+strconv.Itoa(i)+`","type":"function","function":{"name":"read","arguments":"{}"}}]}`,
			`{"role":"tool","tool_call_id":"tc`+strconv.Itoa(i)+`","content":"`+payload+`"}`)
	}
	return `{"model":"gpt-5.5","messages":[{"role":"user","content":"go"},` + strings.Join(msgs, ",") + `]}`
}

func geminiTurnConversation(userTexts ...string) string {
	items := make([]string, 0, len(userTexts)*2)
	for i, text := range userTexts {
		if i > 0 {
			items = append(items, `{"role":"model","parts":[{"text":"model reply `+strconv.Itoa(i)+`"}]}`)
		}
		items = append(items, `{"role":"user","parts":[{"text":"`+text+`"}]}`)
	}
	return `{"contents":[` + strings.Join(items, ",") + `]}`
}

const geminiPath = "/v1beta/models/gemini-3-pro:generateContent"

// TestOpenAIPAYGPrefixStableAcrossTurns is the exact probe that failed: an OpenAI
// PAYG compress session compresses turn 1, and turn 2 must still carry turn 1's
// replacement bytes rather than the client's originals.
func TestOpenAIPAYGPrefixStableAcrossTurns(t *testing.T) {
	t1, t2 := turnText(1), turnText(2)
	rt := payghTransport(2, chatRespBody)
	comp := &stableCompressor{}
	srv, _ := newPAYGPrefixServer(openai.New("https://upstream.test"), "sk-byok", comp, newTestPrefixCache(), rt)

	serveBody(t, srv, "/v1/chat/completions", openaiTurnConversation(t1), nil)
	serveBody(t, srv, "/v1/chat/completions", openaiTurnConversation(t1, t2), nil)

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

// TestOpenAIPAYGPrefixStableThreeTurns extends the invariant past a single hop and
// checks the literal byte prefix, which is what the provider's automatic cache keys
// on.
func TestOpenAIPAYGPrefixStableThreeTurns(t *testing.T) {
	t1, t2, t3 := turnText(1), turnText(2), turnText(3)
	rt := payghTransport(3, chatRespBody)
	comp := &stableCompressor{}
	srv, _ := newPAYGPrefixServer(openai.New("https://upstream.test"), "sk-byok", comp, newTestPrefixCache(), rt)

	serveBody(t, srv, "/v1/chat/completions", openaiTurnConversation(t1), nil)
	serveBody(t, srv, "/v1/chat/completions", openaiTurnConversation(t1, t2), nil)
	serveBody(t, srv, "/v1/chat/completions", openaiTurnConversation(t1, t2, t3), nil)

	third := string(rt.bodies[2])
	for _, text := range []string{t1, t2, t3} {
		if !strings.Contains(third, comp.expectedReplacement(t, text)) {
			t.Fatalf("turn 3 lost the stable replacement for %q:\n%s", text[:20], third)
		}
		if strings.Contains(third, text) {
			t.Fatalf("turn 3 forwarded an original the proxy had already compressed (%q):\n%s", text[:20], third)
		}
	}
	prev := string(rt.bodies[1])
	rep2 := comp.expectedReplacement(t, t2)
	at := strings.Index(prev, rep2)
	if at <= 0 {
		t.Fatalf("turn 2 body lost its own replacement: %s", prev)
	}
	shared := at + len(rep2)
	if len(third) < shared || third[:shared] != prev[:shared] {
		t.Fatalf("prefix diverged between turns:\nturn2=%q\nturn3=%q", prev[:shared], third[:shared])
	}
}

// TestOpenAIPAYGPrefixStableToolMessages runs the invariant over tool messages,
// which carry the bulk of an agent conversation's tokens.
func TestOpenAIPAYGPrefixStableToolMessages(t *testing.T) {
	p1 := strings.Repeat("file contents alpha ", 40)
	p2 := strings.Repeat("file contents beta ", 40)
	rt := payghTransport(2, chatRespBody)
	comp := &stableCompressor{}
	srv, _ := newPAYGPrefixServer(openai.New("https://upstream.test"), "sk-byok", comp, newTestPrefixCache(), rt)

	serveBody(t, srv, "/v1/chat/completions", openaiToolConversation(p1), nil)
	serveBody(t, srv, "/v1/chat/completions", openaiToolConversation(p1, p2), nil)

	if !strings.Contains(string(rt.bodies[1]), comp.expectedReplacement(t, p1)) {
		t.Fatalf("turn 2 lost turn 1's tool-result replacement:\n%s", rt.bodies[1])
	}
	if strings.Contains(string(rt.bodies[1]), p1) {
		t.Fatalf("tool-message prefix flipped back to originals:\n%s", rt.bodies[1])
	}
}

// TestOpenAIPAYGPrefixStableSurvivesRestart pins durability: a new process whose
// compressor now emits different bytes must still send what the FIRST process
// stored, because the durable cache — not the compressor — is the authority.
func TestOpenAIPAYGPrefixStableSurvivesRestart(t *testing.T) {
	t1, t2 := turnText(1), turnText(2)
	cache := newTestPrefixCache()

	rt1 := payghTransport(1, chatRespBody)
	first := &stableCompressor{nonce: "v1"}
	srv1, _ := newPAYGPrefixServer(openai.New("https://upstream.test"), "sk-byok", first, cache, rt1)
	serveBody(t, srv1, "/v1/chat/completions", openaiTurnConversation(t1), nil)

	rt2 := payghTransport(1, chatRespBody)
	second := &stableCompressor{nonce: "v2"}
	srv2, _ := newPAYGPrefixServer(openai.New("https://upstream.test"), "sk-byok", second, cache, rt2)
	serveBody(t, srv2, "/v1/chat/completions", openaiTurnConversation(t1, t2), nil)

	if !strings.Contains(string(rt2.bodies[0]), first.expectedReplacement(t, t1)) {
		t.Fatalf("post-restart request must reuse the stored replacement, not recompress:\n%s", rt2.bodies[0])
	}
	if strings.Contains(string(rt2.bodies[0]), second.expectedReplacement(t, t1)) {
		t.Fatalf("post-restart request recompressed a frozen turn and diverged the prefix:\n%s", rt2.bodies[0])
	}
}

// TestOpenAIPAYGPrefixCacheBeatsNonDeterministicCompressor pins WHO is
// authoritative once a replacement has gone upstream: the cache, never a fresh
// compression.
func TestOpenAIPAYGPrefixCacheBeatsNonDeterministicCompressor(t *testing.T) {
	t1, t2, t3 := turnText(1), turnText(2), turnText(3)
	rt := payghTransport(3, chatRespBody)
	srv, _ := newPAYGPrefixServer(openai.New("https://upstream.test"), "sk-byok", &driftCompressor{}, newTestPrefixCache(), rt)

	serveBody(t, srv, "/v1/chat/completions", openaiTurnConversation(t1), nil)
	serveBody(t, srv, "/v1/chat/completions", openaiTurnConversation(t1, t2), nil)
	serveBody(t, srv, "/v1/chat/completions", openaiTurnConversation(t1, t2, t3), nil)

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

// TestOpenAIPAYGPrefixCacheUnavailablePassesThrough pins the fail-open rule: a
// replacement the proxy cannot persist could not be reproduced next turn, so it
// never goes on the wire and the row claims nothing.
func TestOpenAIPAYGPrefixCacheUnavailablePassesThrough(t *testing.T) {
	body := openaiTurnConversation(turnText(1))
	rt := payghTransport(1, chatRespBody)
	cache := newTestPrefixCache()
	cache.failWrites = true
	srv, sink := newPAYGPrefixServer(openai.New("https://upstream.test"), "sk-byok", &stableCompressor{}, cache, rt)

	serveBody(t, srv, "/v1/chat/completions", body, nil)

	if sha256.Sum256(rt.bodies[0]) != sha256.Sum256([]byte(body)) {
		t.Fatalf("unpersistable replacement must be byte-identical passthrough:\n got %s\nwant %s", rt.bodies[0], body)
	}
	if row := sink.last(t); row.CompressionTokensBefore != 0 || row.RecoveryHandle != "" {
		t.Fatalf("row must claim no compression: %+v", row)
	}
}

// TestOpenAIPAYGPrefixSubstitutionBooksSavingsOnce is the no-fake-savings pin: a
// message's token delta is counted on the turn it was first compressed and never
// again, so a long conversation cannot re-book turn 1's reduction every request.
func TestOpenAIPAYGPrefixSubstitutionBooksSavingsOnce(t *testing.T) {
	t1, t2 := turnText(1), turnText(2)
	rt := payghTransport(3, chatRespBody)
	srv, sink := newPAYGPrefixServer(openai.New("https://upstream.test"), "sk-byok", &stableCompressor{}, newTestPrefixCache(), rt)

	serveBody(t, srv, "/v1/chat/completions", openaiTurnConversation(t1), nil)
	firstRow := sink.last(t)
	serveBody(t, srv, "/v1/chat/completions", openaiTurnConversation(t1, t2), nil)
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

	// The extreme of the same rule: a replayed request substitutes both blocks and
	// earns nothing, while still producing byte-identical upstream bytes.
	serveBody(t, srv, "/v1/chat/completions", openaiTurnConversation(t1, t2), nil)
	if !bytes.Equal(rt.bodies[1], rt.bodies[2]) {
		t.Fatalf("a replayed request must produce byte-identical upstream bytes:\n%s\n%s", rt.bodies[1], rt.bodies[2])
	}
	replayRow := sink.last(t)
	if replayRow.CompressionTokensBefore != 0 || replayRow.CompressionTokensAfter != 0 || replayRow.CompressionRatio != 0 {
		t.Fatalf("a substitution-only turn must claim no new reduction: %+v", replayRow)
	}
	if replayRow.RecoveryHandle == "" {
		t.Fatal("a substitution-only turn must still disclose its recovery handle")
	}
}

// TestGeminiPAYGPrefixStableAcrossTurns is the Gemini shape of the same probe.
func TestGeminiPAYGPrefixStableAcrossTurns(t *testing.T) {
	t1, t2, t3 := turnText(1), turnText(2), turnText(3)
	rt := payghTransport(3, geminiRespBody)
	comp := &stableCompressor{}
	srv, _ := newPAYGPrefixServer(gemini.New("https://upstream.test"), "AIzaTestKey", comp, newTestPrefixCache(), rt)

	serveBody(t, srv, geminiPath, geminiTurnConversation(t1), nil)
	serveBody(t, srv, geminiPath, geminiTurnConversation(t1, t2), nil)
	serveBody(t, srv, geminiPath, geminiTurnConversation(t1, t2, t3), nil)

	if !strings.Contains(string(rt.bodies[0]), comp.expectedReplacement(t, t1)) {
		t.Fatalf("turn 1 did not compress the live zone: %s", rt.bodies[0])
	}
	third := string(rt.bodies[2])
	for _, text := range []string{t1, t2, t3} {
		if !strings.Contains(third, comp.expectedReplacement(t, text)) {
			t.Fatalf("turn 3 lost the stable replacement for %q:\n%s", text[:20], third)
		}
		if strings.Contains(third, text) {
			t.Fatalf("turn 3 forwarded an original the proxy had already compressed (%q):\n%s", text[:20], third)
		}
	}
	prev := string(rt.bodies[1])
	rep2 := comp.expectedReplacement(t, t2)
	at := strings.Index(prev, rep2)
	if at <= 0 {
		t.Fatalf("turn 2 body lost its own replacement: %s", prev)
	}
	shared := at + len(rep2)
	if len(third) < shared || third[:shared] != prev[:shared] {
		t.Fatalf("prefix diverged between turns:\nturn2=%q\nturn3=%q", prev[:shared], third[:shared])
	}
}

// TestGeminiPAYGPrefixStableSurvivesRestart is the Gemini durability pin.
func TestGeminiPAYGPrefixStableSurvivesRestart(t *testing.T) {
	t1, t2 := turnText(1), turnText(2)
	cache := newTestPrefixCache()

	rt1 := payghTransport(1, geminiRespBody)
	first := &stableCompressor{nonce: "v1"}
	srv1, _ := newPAYGPrefixServer(gemini.New("https://upstream.test"), "AIzaTestKey", first, cache, rt1)
	serveBody(t, srv1, geminiPath, geminiTurnConversation(t1), nil)

	rt2 := payghTransport(1, geminiRespBody)
	second := &stableCompressor{nonce: "v2"}
	srv2, _ := newPAYGPrefixServer(gemini.New("https://upstream.test"), "AIzaTestKey", second, cache, rt2)
	serveBody(t, srv2, geminiPath, geminiTurnConversation(t1, t2), nil)

	if !strings.Contains(string(rt2.bodies[0]), first.expectedReplacement(t, t1)) {
		t.Fatalf("post-restart request must reuse the stored replacement, not recompress:\n%s", rt2.bodies[0])
	}
	if strings.Contains(string(rt2.bodies[0]), second.expectedReplacement(t, t1)) {
		t.Fatalf("post-restart request recompressed a frozen turn and diverged the prefix:\n%s", rt2.bodies[0])
	}
}

// TestGeminiPAYGPrefixCacheBeatsNonDeterministicCompressor is the Gemini authority
// pin against a compressor whose output changes on every call.
func TestGeminiPAYGPrefixCacheBeatsNonDeterministicCompressor(t *testing.T) {
	t1, t2 := turnText(1), turnText(2)
	rt := payghTransport(2, geminiRespBody)
	srv, _ := newPAYGPrefixServer(gemini.New("https://upstream.test"), "AIzaTestKey", &driftCompressor{}, newTestPrefixCache(), rt)

	serveBody(t, srv, geminiPath, geminiTurnConversation(t1), nil)
	serveBody(t, srv, geminiPath, geminiTurnConversation(t1, t2), nil)

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
	if !strings.Contains(string(rt.bodies[1]), emitted) {
		t.Fatalf("turn 2 lost turn 1's exact emitted bytes %q:\n%s", emitted, rt.bodies[1])
	}
}

// TestGeminiPAYGPrefixCacheUnavailablePassesThrough is the Gemini fail-open pin.
func TestGeminiPAYGPrefixCacheUnavailablePassesThrough(t *testing.T) {
	body := geminiTurnConversation(turnText(1))
	rt := payghTransport(1, geminiRespBody)
	cache := newTestPrefixCache()
	cache.failWrites = true
	srv, sink := newPAYGPrefixServer(gemini.New("https://upstream.test"), "AIzaTestKey", &stableCompressor{}, cache, rt)

	serveBody(t, srv, geminiPath, body, nil)

	if sha256.Sum256(rt.bodies[0]) != sha256.Sum256([]byte(body)) {
		t.Fatalf("unpersistable replacement must be byte-identical passthrough:\n got %s\nwant %s", rt.bodies[0], body)
	}
	if row := sink.last(t); row.CompressionTokensBefore != 0 || row.RecoveryHandle != "" {
		t.Fatalf("row must claim no compression: %+v", row)
	}
}

// TestGeminiPAYGPrefixSubstitutionBooksSavingsOnce is the Gemini no-fake-savings
// pin: substituting an earlier turn re-books nothing.
func TestGeminiPAYGPrefixSubstitutionBooksSavingsOnce(t *testing.T) {
	t1, t2 := turnText(1), turnText(2)
	rt := payghTransport(2, geminiRespBody)
	srv, sink := newPAYGPrefixServer(gemini.New("https://upstream.test"), "AIzaTestKey", &stableCompressor{}, newTestPrefixCache(), rt)

	serveBody(t, srv, geminiPath, geminiTurnConversation(t1), nil)
	firstRow := sink.last(t)
	serveBody(t, srv, geminiPath, geminiTurnConversation(t1, t2), nil)
	secondRow := sink.last(t)

	if firstRow.CompressionTokensBefore != 100 || firstRow.CompressionTokensAfter != 40 {
		t.Fatalf("turn 1 row = before %d after %d, want 100/40", firstRow.CompressionTokensBefore, firstRow.CompressionTokensAfter)
	}
	if secondRow.CompressionTokensBefore != 100 || secondRow.CompressionTokensAfter != 40 {
		t.Fatalf("turn 2 row = before %d after %d, want 100/40 (turn 1's delta must not be re-booked)", secondRow.CompressionTokensBefore, secondRow.CompressionTokensAfter)
	}
	if !strings.Contains(secondRow.RecoveryHandle, contentHandle([]byte(t1))) {
		t.Fatalf("turn 2 must still disclose the substituted block's recovery handle: %q", secondRow.RecoveryHandle)
	}
}
