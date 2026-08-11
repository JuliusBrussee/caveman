package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

type captureTransport struct {
	mu        sync.Mutex
	bodies    [][]byte
	headers   []http.Header
	responses []string
	statuses  []int
}

func (t *captureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	t.mu.Lock()
	t.bodies = append(t.bodies, append([]byte(nil), body...))
	t.headers = append(t.headers, r.Header.Clone())
	idx := len(t.bodies) - 1
	t.mu.Unlock()

	status := http.StatusOK
	if idx < len(t.statuses) && t.statuses[idx] != 0 {
		status = t.statuses[idx]
	}
	respBody := chatRespBody
	if idx < len(t.responses) && t.responses[idx] != "" {
		respBody = t.responses[idx]
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"up-live"}},
		Body:       io.NopCloser(strings.NewReader(respBody)),
		Request:    r,
	}, nil
}

type liveZoneCompressor struct {
	mu       sync.Mutex
	calls    int
	outputs  [][]byte
	before   []int
	after    []int
	stored   [][]byte
	recovery map[string][]byte
}

func (c *liveZoneCompressor) CompressSegment(seg []byte) ([]byte, int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := c.calls
	c.calls++
	out := []byte("Z")
	if i < len(c.outputs) && c.outputs[i] != nil {
		out = c.outputs[i]
	}
	before := 100
	if i < len(c.before) && c.before[i] != 0 {
		before = c.before[i]
	}
	after := 40
	if i < len(c.after) && c.after[i] != 0 {
		after = c.after[i]
	}
	return out, before, after
}

func (c *liveZoneCompressor) StoreOriginal(body []byte) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stored = append(c.stored, append([]byte(nil), body...))
	sum := sha256.Sum256(body)
	handle := "ccr_" + hex.EncodeToString(sum[:16])
	if c.recovery == nil {
		c.recovery = map[string][]byte{}
	}
	c.recovery[handle] = append([]byte(nil), body...)
	return handle, nil
}

func (c *liveZoneCompressor) RetrieveOriginal(handle, query string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.recovery[handle]...), nil
}

func newSocketFreeCompressServer(adapter providers.Adapter, comp Compressor, rt *captureTransport, creds CredentialResolver) *Server {
	return newSocketFreeCompressServerWithConfig(adapter, comp, rt, creds, Config{})
}

func newSocketFreeCompressServerWithConfig(adapter providers.Adapter, comp Compressor, rt *captureTransport, creds CredentialResolver, cfg Config) *Server {
	if creds == nil {
		creds = stubCreds{key: "sk-byok"}
	}
	cfg.Adapters = []providers.Adapter{adapter}
	cfg.Auth = stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}}
	cfg.Creds = creds
	cfg.Sink = &captureSink{}
	cfg.Compressor = comp
	cfg.HTTPClient = &http.Client{Transport: rt}
	return New(cfg)
}

func serveBody(t *testing.T, srv *Server, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	return rec
}

func TestLiveZoneDeterminismAndMarkerContentAddressing(t *testing.T) {
	live := strings.Repeat("deterministic live block ", 40)
	body := `{"model":"gpt-5.5","messages":[{"role":"user","content":"` + live + `"}]}`
	rt := &captureTransport{responses: []string{chatRespBody, chatRespBody}}
	comp := &liveZoneCompressor{}
	srv := newSocketFreeCompressServer(openai.New("https://upstream.test"), comp, rt, nil)

	serveBody(t, srv, "/v1/chat/completions", body, nil)
	serveBody(t, srv, "/v1/chat/completions", body, nil)

	if len(rt.bodies) != 2 {
		t.Fatalf("upstream calls = %d, want 2", len(rt.bodies))
	}
	if !bytes.Equal(rt.bodies[0], rt.bodies[1]) {
		t.Fatalf("same input must produce byte-identical transformed body:\n%s\n%s", rt.bodies[0], rt.bodies[1])
	}
	handle := contentHandle([]byte(live))
	if !bytes.Contains(rt.bodies[0], []byte("<<ccr:"+handle+">>")) {
		t.Fatalf("body missing deterministic content-addressed marker %s: %s", handle, rt.bodies[0])
	}
	if len(comp.stored) != 2 || !bytes.Equal(comp.stored[0], []byte(live)) || !bytes.Equal(comp.stored[1], []byte(live)) {
		t.Fatalf("StoreOriginal must receive original block bytes each time: %q", comp.stored)
	}
}

func TestLiveZoneTokenAwarePerBlockRevert(t *testing.T) {
	keep := strings.Repeat("KEEP_BLOCK ", 70)
	shrink := strings.Repeat("SHRINK_BLOCK ", 70)
	body := `{"model":"gpt-5.5","messages":[{"role":"user","content":[{"type":"text","text":"` + keep + `"},{"type":"text","text":"` + shrink + `"}]}]}`
	rt := &captureTransport{}
	comp := &liveZoneCompressor{
		outputs: [][]byte{[]byte("NO_SHRINK"), []byte("YES_SHRINK")},
		before:  []int{100, 100},
		after:   []int{100, 40},
	}
	srv := newSocketFreeCompressServer(openai.New("https://upstream.test"), comp, rt, nil)

	rec := serveBody(t, srv, "/v1/chat/completions", body, nil)
	upstream := string(rt.bodies[0])
	if !strings.Contains(upstream, keep) {
		t.Fatalf("non-shrinking block must stay original: %s", upstream)
	}
	if strings.Contains(upstream, shrink) || !strings.Contains(upstream, "YES_SHRINK") || !strings.Contains(upstream, "<<ccr:") {
		t.Fatalf("shrinking block missing replacement/marker: %s", upstream)
	}
	if got := rec.Header().Get("x-caveman-tokens-before"); got != "100" {
		t.Fatalf("tokens-before = %q, want 100", got)
	}
	if len(comp.stored) != 1 || !bytes.Equal(comp.stored[0], []byte(shrink)) {
		t.Fatalf("only shrinking block should be stored, got %q", comp.stored)
	}
}

func TestSubscriptionDefaultPassesThroughWithoutMutation(t *testing.T) {
	live := strings.Repeat("subscription live block ", 40)
	body := `{"model":"claude-sonnet-4-6","system":"System fixed","tools":[{"name":"read","description":"Read fixed","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"` + live + `"}]}`
	rt := &captureTransport{responses: []string{`{"id":"msg","type":"message","model":"claude-sonnet-4-6","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1000,"output_tokens":10}}`}}
	comp := &liveZoneCompressor{}
	srv := newSocketFreeCompressServer(anthropic.New("https://upstream.test"), comp, rt, passthroughTestCreds{})

	serveBody(t, srv, "/v1/messages", body, map[string]string{
		"user-agent":        "codex-cli/0.1",
		"authorization":     "Bearer sk-ant-oat-test",
		"anthropic-beta":    "oauth-2025-04-20",
		"accept-encoding":   "identity",
		"anthropic-version": "2023-06-01",
	})

	if len(rt.bodies) != 1 {
		t.Fatalf("upstream calls = %d, want 1", len(rt.bodies))
	}
	if sha256.Sum256(rt.bodies[0]) != sha256.Sum256([]byte(body)) {
		t.Fatalf("subscription default must be byte-identical passthrough:\n got %s\nwant %s", rt.bodies[0], body)
	}
	upstream := string(rt.bodies[0])
	if strings.Contains(upstream, retrieveToolName) || strings.Contains(upstream, "<<ccr:") {
		t.Fatalf("subscription default must inject no tool and add no marker: %s", upstream)
	}
	if comp.calls != 0 || len(comp.stored) != 0 {
		t.Fatalf("subscription default must not invoke compression/store: compress=%d stored=%d", comp.calls, len(comp.stored))
	}
	headers := rt.headers[0]
	if got := headers.Get("authorization"); got != "Bearer sk-ant-oat-test" {
		t.Fatalf("authorization = %q", got)
	}
	if got := headers.Get("anthropic-beta"); got != "oauth-2025-04-20" {
		t.Fatalf("anthropic-beta = %q", got)
	}
	if got := headers.Get("accept-encoding"); got != "identity" {
		t.Fatalf("accept-encoding = %q", got)
	}
	if got := headers.Get("user-agent"); got != "codex-cli/0.1" {
		t.Fatalf("user-agent = %q", got)
	}
}

func TestSubscriptionLiveZoneOptInCompressesWithoutToolInjectionOrHeaderMutation(t *testing.T) {
	live := strings.Repeat("subscription live block ", 40)
	body := `{"model":"claude-sonnet-4-6","system":"System fixed","tools":[{"name":"read","description":"Read fixed","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"` + live + `"}]}`
	rt := &captureTransport{responses: []string{`{"id":"msg","type":"message","model":"claude-sonnet-4-6","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1000,"output_tokens":10}}`}}
	comp := &liveZoneCompressor{}
	srv := newSocketFreeCompressServerWithConfig(
		anthropic.New("https://upstream.test"),
		comp,
		rt,
		passthroughTestCreds{},
		Config{SubscriptionCompress: "live_zone", RecoveryViaMCP: true, PrefixCache: newTestPrefixCache()},
	)

	// Subscription without an account stays S0 because real Claude Code subscription
	// sessions produced opaque 429s after byte mutation. With a valid wrap
	// entitlement the same marker-only live-zone path PAYG uses is unlocked.
	serveBody(t, srv, "/v1/messages", body, map[string]string{
		"user-agent":        "codex-cli/0.1",
		"authorization":     "Bearer sk-ant-oat-test",
		"anthropic-beta":    "oauth-2025-04-20",
		"accept-encoding":   "identity",
		"anthropic-version": "2023-06-01",
	})

	upstream := string(rt.bodies[0])
	if !strings.Contains(upstream, `"system":"System fixed"`) || !strings.Contains(upstream, `"tools":[{"name":"read","description":"Read fixed","input_schema":{"type":"object"}}]`) {
		t.Fatalf("subscription must not mutate system/tools bytes: %s", upstream)
	}
	if strings.Contains(upstream, retrieveToolName) {
		t.Fatalf("subscription must not inject server-side retrieve tool: %s", upstream)
	}
	if !strings.Contains(upstream, "<<ccr:") {
		t.Fatalf("subscription live-zone compression should disclose in-block marker: %s", upstream)
	}
	headers := rt.headers[0]
	if got := headers.Get("authorization"); got != "Bearer sk-ant-oat-test" {
		t.Fatalf("authorization = %q", got)
	}
	if got := headers.Get("anthropic-beta"); got != "oauth-2025-04-20" {
		t.Fatalf("anthropic-beta = %q", got)
	}
	if got := headers.Get("accept-encoding"); got != "identity" {
		t.Fatalf("accept-encoding = %q", got)
	}
	if got := headers.Get("user-agent"); got != "codex-cli/0.1" {
		t.Fatalf("user-agent = %q", got)
	}
}

func TestThreeTurnLiveZoneKeepsFrozenPrefixAndStaticTools(t *testing.T) {
	system := `{"role":"system","content":"stable system"}`
	user1 := `{"role":"user","content":"` + strings.Repeat("turn one client bytes ", 40) + `"}`
	assistant1 := `{"role":"assistant","content":"assistant one"}`
	user2 := `{"role":"user","content":"` + strings.Repeat("turn two client bytes ", 40) + `"}`
	assistant2 := `{"role":"assistant","content":"assistant two"}`
	user3 := `{"role":"user","content":"` + strings.Repeat("turn three client bytes ", 40) + `"}`
	turns := []string{
		`{"model":"gpt-5.5","messages":[` + system + `,` + user1 + `]}`,
		`{"model":"gpt-5.5","messages":[` + system + `,` + user1 + `,` + assistant1 + `,` + user2 + `]}`,
		`{"model":"gpt-5.5","messages":[` + system + `,` + user1 + `,` + assistant1 + `,` + user2 + `,` + assistant2 + `,` + user3 + `]}`,
	}
	rt := &captureTransport{responses: []string{chatRespBody, chatRespBody, chatRespBody}}
	srv := newSocketFreeCompressServer(openai.New("https://upstream.test"), &liveZoneCompressor{}, rt, nil)
	for _, body := range turns {
		serveBody(t, srv, "/v1/chat/completions", body, nil)
	}
	if len(rt.bodies) != 3 {
		t.Fatalf("upstream calls = %d, want 3", len(rt.bodies))
	}
	toolBytes := extractToolsSuffix(t, string(rt.bodies[0]))
	for i := 1; i < len(rt.bodies); i++ {
		if got := extractToolsSuffix(t, string(rt.bodies[i])); got != toolBytes {
			t.Fatalf("turn %d tool bytes changed:\n%s\n%s", i+1, got, toolBytes)
		}
	}
	assertContainsInOrder(t, string(rt.bodies[1]), []string{system, user1, assistant1})
	assertContainsInOrder(t, string(rt.bodies[2]), []string{system, user1, assistant1, user2, assistant2})
	if strings.Contains(string(rt.bodies[1]), user2) || strings.Contains(string(rt.bodies[2]), user3) {
		t.Fatalf("latest user should be live-zone compressed only:\nturn2=%s\nturn3=%s", rt.bodies[1], rt.bodies[2])
	}
	// I1/I2 at the integration level: every byte before the live-zone splice
	// point must be identical to the client bytes, not merely contain them.
	assertFrozenPrefixByteEqual(t, turns[1], string(rt.bodies[1]), "turn two client bytes")
	assertFrozenPrefixByteEqual(t, turns[2], string(rt.bodies[2]), "turn three client bytes")
}

// assertFrozenPrefixByteEqual asserts the upstream body is byte-identical to the
// client body for every byte before the live-zone content (located by marker).
func assertFrozenPrefixByteEqual(t *testing.T, clientBody, upstreamBody, liveMarker string) {
	t.Helper()
	cut := strings.Index(clientBody, liveMarker)
	if cut <= 0 {
		t.Fatalf("live marker %q not found in client body", liveMarker)
	}
	prefix := clientBody[:cut]
	if len(upstreamBody) < cut || upstreamBody[:cut] != prefix {
		t.Fatalf("frozen prefix bytes diverged before live zone:\nclient=%q\nupstream=%q", prefix, upstreamBody[:min(len(upstreamBody), cut)])
	}
}

func contentHandle(original []byte) string {
	sum := sha256.Sum256(original)
	return "ccr_" + hex.EncodeToString(sum[:16])
}

func extractToolsSuffix(t *testing.T, body string) string {
	t.Helper()
	idx := strings.Index(body, `"tools":[`)
	if idx < 0 {
		t.Fatalf("body missing tools: %s", body)
	}
	return body[idx:]
}

func assertContainsInOrder(t *testing.T, body string, parts []string) {
	t.Helper()
	offset := 0
	for _, part := range parts {
		idx := strings.Index(body[offset:], part)
		if idx < 0 {
			t.Fatalf("body missing ordered part %s after %d:\n%s", part, offset, body)
		}
		offset += idx + len(part)
	}
}
