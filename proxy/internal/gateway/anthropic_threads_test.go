package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
)

type threadRoundTripper struct {
	bodies  [][]byte
	headers []http.Header
	reply   func(idx int, body []byte) (int, string)
}

func (rt *threadRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	raw, _ := io.ReadAll(r.Body)
	idx := len(rt.bodies)
	rt.bodies = append(rt.bodies, raw)
	rt.headers = append(rt.headers, r.Header.Clone())
	status, text := http.StatusOK, `{"id":"msg_reply","type":"message","model":"claude-sonnet-4-6","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":5}}`
	if rt.reply != nil {
		if code, body := rt.reply(idx, raw); code != 0 {
			status, text = code, body
		}
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(text)),
		Request:    r,
	}, nil
}

// Pi 1.6.0 uses Anthropic message-threads-2026-08-12: Turn 1 sends thread:create,
// Turn 2 sends thread:continue omitting system/tools/prior messages, and an
// expired thread falls back to a full thread:create resend. With MCP recovery,
// Turn 2's delta compresses and memoizes in prefixCache so a Turn 3 fallback
// reproduces the exact compressed bytes without busting prefix stability.
func TestAnthropicMessageThreadsMCPCompressAndFallbackReplay(t *testing.T) {
	rt := &threadRoundTripper{}
	prefixCache := newTestPrefixCache()
	comp := &stubCompressor{out: []byte("COMPRESSED_BLOCK"), before: 400, after: 12}
	srv := New(Config{
		Adapters:       []providers.Adapter{anthropic.New("https://api.anthropic.com")},
		Auth:           stubAuth{rc: RequestContext{RuntimeMode: "compress"}},
		Creds:          passthroughTestCreds{},
		HTTPClient:     &http.Client{Transport: rt},
		Compressor:     comp,
		PrefixCache:    prefixCache,
		RecoveryViaMCP: true,
	})

	large1 := strings.Repeat("turn1 tool payload line with repeated detail\n", 40)
	large2 := strings.Repeat("turn2 delta tool payload line with repeated detail\n", 40)
	send := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/w/pi/v1/messages", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", "sk-ant-test-key")
		req.Header.Set("anthropic-beta", "message-threads-2026-08-12")
		req.Header.Set("x-cave-session", "pi-thread-sess")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		return rec
	}

	// Turn 1: thread:create with full system, tools, and user message.
	turn1 := `{"model":"claude-sonnet-4-6","max_tokens":1024,"thread":{"type":"create"},"system":[{"type":"text","text":"system","cache_control":{"type":"ephemeral"}}],"tools":[{"name":"mcp__caveman__caveman_retrieve","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":[{"type":"text","text":` + jsonQuote(large1) + `}]}]}`
	send(turn1)

	// Turn 2: thread:continue with ONLY delta tool_result (no system/tools).
	turn2 := `{"model":"claude-sonnet-4-6","max_tokens":1024,"thread":{"type":"continue","previous_message_id":"msg_reply"},"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":` + jsonQuote(large2) + `}]}]}`
	send(turn2)

	// Turn 3: thread expired -> Pi resends full conversation with thread:create.
	turn3 := `{"model":"claude-sonnet-4-6","max_tokens":1024,"thread":{"type":"create"},"system":[{"type":"text","text":"system","cache_control":{"type":"ephemeral"}}],"tools":[{"name":"mcp__caveman__caveman_retrieve","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":[{"type":"text","text":` + jsonQuote(large1) + `,"cache_control":{"type":"ephemeral"}}]},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":` + jsonQuote(large2) + `,"cache_control":{"type":"ephemeral"}}]},{"role":"assistant","content":[{"type":"text","text":"done"}]},{"role":"user","content":[{"type":"text","text":"next question"}]}]}`
	send(turn3)

	if len(rt.bodies) != 3 {
		t.Fatalf("upstream requests = %d, want 3", len(rt.bodies))
	}
	if bytes.Contains(rt.bodies[1], []byte(large2)) {
		t.Fatalf("turn 2 thread:continue delta was not compressed")
	}
	if bytes.Contains(rt.bodies[1], []byte(`"tools"`)) {
		t.Fatalf("turn 2 thread:continue must not have tools injected: %s", rt.bodies[1])
	}
	// Turn 3 must replay the exact memoized compressed bytes for both Turn 1 and Turn 2.
	if bytes.Contains(rt.bodies[2], []byte(large1)) || bytes.Contains(rt.bodies[2], []byte(large2)) {
		t.Fatalf("turn 3 fallback thread:create did not substitute cached replacements")
	}
}

// Without MCP recovery, PAYG requests carrying Anthropic message threads must NOT
// inject the server-side caveman_retrieve tool: thread:continue omits tools (400)
// and thread:create binds server state per reply.
func TestAnthropicMessageThreadsSkipServerRetrieve(t *testing.T) {
	rt := &threadRoundTripper{}
	srv := New(Config{
		Adapters:   []providers.Adapter{anthropic.New("https://api.anthropic.com")},
		Auth:       stubAuth{rc: RequestContext{RuntimeMode: "compress"}},
		Creds:      passthroughTestCreds{},
		HTTPClient: &http.Client{Transport: rt},
		Compressor: &stubCompressor{out: []byte("COMPRESSED_BLOCK"), before: 400, after: 12},
	})

	large := strings.Repeat("payload line that would trigger server retrieve\n", 40)
	for name, body := range map[string]string{
		"create":   `{"model":"claude-sonnet-4-6","max_tokens":1024,"thread":{"type":"create"},"messages":[{"role":"user","content":[{"type":"text","text":` + jsonQuote(large) + `}]}]}`,
		"continue": `{"model":"claude-sonnet-4-6","max_tokens":1024,"thread":{"type":"continue","previous_message_id":"msg_01"},"messages":[{"role":"user","content":[{"type":"text","text":` + jsonQuote(large) + `}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			before := len(rt.bodies)
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("x-api-key", "sk-ant-test-key")
			req.Header.Set("anthropic-beta", "message-threads-2026-08-12")
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			sent := rt.bodies[before]
			if bytes.Contains(sent, []byte("caveman_retrieve")) {
				t.Fatalf("%s unexpectedly injected caveman_retrieve: %s", name, sent)
			}
			if string(sent) != body {
				t.Fatalf("%s mutated body without MCP recovery", name)
			}
		})
	}
}

// Message-thread requests must preserve effort/betas fingerprint across turns,
// treat thread:continue as non-fresh, and keep Auto on Turn 1's Anthropic model
// rather than swapping models or pool hosts mid-thread.
func TestAnthropicMessageThreadsPreserveFingerprintAndModel(t *testing.T) {
	createBody := `{"model":"caveman-auto","max_tokens":1024,"thread":{"type":"create"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"turn 1"}]}`
	continueBody := `{"model":"caveman-auto","max_tokens":1024,"thread":{"type":"continue","previous_message_id":"msg_1"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"turn 2"}]}`

	if !messagesWire.fresh([]byte(createBody)) {
		t.Fatal("thread:create first turn should be fresh")
	}
	if messagesWire.fresh([]byte(continueBody)) {
		t.Fatal("thread:continue must not be reported as fresh")
	}

	rt := &threadRoundTripper{}
	var call int
	cloud := &fakeCloud{}
	srv := New(Config{
		Adapters:   []providers.Adapter{anthropic.New("https://api.anthropic.com")},
		Auth:       stubAuth{rc: RequestContext{RuntimeMode: "record"}},
		Creds:      passthroughTestCreds{},
		HTTPClient: &http.Client{Transport: rt},
		Cloud:      cloud,
	})

	// Turn 1: Auto routes to claude-sonnet-4-6, asks effort "low" in message mode,
	// and also proposes a pool Target. Message-thread creation must clear Target
	// so Turn 1 stays on Anthropic (avoiding an orphaned message ID on Turn 2).
	cloud.answer = RouteAnswer{
		Outcome:    "routed",
		Model:      "claude-sonnet-4-6",
		Effort:     "low",
		EffortMode: "message",
		Target:     &RouteTarget{Host: "openai", Model: "gpt-5.4", PoolID: "pool-1", Via: "cloud"},
	}
	req1 := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(createBody))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("x-api-key", "sk-ant-test-key")
	req1.Header.Set("anthropic-beta", "message-threads-2026-08-12")
	req1.Header.Set("x-cave-session", "thread-route-sess")
	rec1 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("turn 1 status = %d: %s", rec1.Code, rec1.Body.String())
	}

	// Turn 2: Cloud tries to switch model to claude-opus-4-6 and move to a pool host.
	call++
	_ = call
	cloud.answer = RouteAnswer{
		Outcome:    "routed",
		Model:      "claude-opus-4-6",
		Effort:     "max",
		EffortMode: "top",
		Target:     &RouteTarget{Host: "openai", Model: "gpt-5.4", PoolID: "pool-1", Via: "cloud"},
	}
	req2 := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(continueBody))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("x-api-key", "sk-ant-test-key")
	req2.Header.Set("anthropic-beta", "message-threads-2026-08-12")
	req2.Header.Set("x-cave-session", "thread-route-sess")
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("turn 2 status = %d: %s", rec2.Code, rec2.Body.String())
	}

	if len(rt.bodies) != 2 {
		t.Fatalf("upstream calls = %d, want 2", len(rt.bodies))
	}
	for i, raw := range rt.bodies {
		var parsed struct {
			Model        string `json:"model"`
			OutputConfig struct {
				Effort string `json:"effort"`
			} `json:"output_config"`
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Fatalf("unmarshal turn %d: %v", i+1, err)
		}
		if parsed.Model != "claude-sonnet-4-6" {
			t.Fatalf("turn %d model = %q, want claude-sonnet-4-6 (must stay pinned across thread)", i+1, parsed.Model)
		}
		if parsed.OutputConfig.Effort != "high" {
			t.Fatalf("turn %d effort = %q, want original high (must not mutate thread fingerprint)", i+1, parsed.OutputConfig.Effort)
		}
		if len(parsed.Messages) != 1 {
			t.Fatalf("turn %d injected extra messages (marks): %d", i+1, len(parsed.Messages))
		}
		if gotBeta := rt.headers[i].Get("anthropic-beta"); strings.Contains(gotBeta, perMessageBeta) || strings.Contains(gotBeta, bindingBeta) {
			t.Fatalf("turn %d added extra beta header %q", i+1, gotBeta)
		}
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
