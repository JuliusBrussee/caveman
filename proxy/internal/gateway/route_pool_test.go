package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/translate"
	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
)

// poolStub is every upstream at once, told apart by path: the harness's own
// Anthropic API (/v1/messages), a pool host's chat wire (/chat/completions)
// and the Cloud gateway (/gw/v1/messages). Host down.example never answers.
type poolStub struct {
	mu       sync.Mutex
	got      map[string][]*http.Request
	bodies   map[string][]string
	poolCode int    // status the pool host answers
	poolSSE  string // a streamed chat answer, when set
	respSSE  string // the Responses host's stream
	respCode int
	gwCode   int
	cloud    *fakeCloud
}

func (p *poolStub) server(t *testing.T) *Server {
	t.Helper()
	p.got, p.bodies = map[string][]*http.Request{}, map[string][]string{}
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.got[r.URL.Path] = append(p.got[r.URL.Path], r)
		p.bodies[r.URL.Path] = append(p.bodies[r.URL.Path], string(raw))
		p.mu.Unlock()
		switch r.URL.Path {
		case "/chat/completions":
			if p.poolCode != 0 {
				w.WriteHeader(p.poolCode)
				_, _ = io.WriteString(w, `{"error":{"message":"overloaded"}}`)
				return
			}
			if p.poolSSE != "" {
				w.Header().Set("content-type", "text/event-stream")
				_, _ = io.WriteString(w, p.poolSSE)
				return
			}
			w.Header().Set("content-type", "application/json")
			_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"gpt-6.1-sol","choices":[{"index":0,"message":{"role":"assistant","content":"pool says hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":3}}`)
		case "/responses":
			if p.respCode != 0 {
				w.WriteHeader(p.respCode)
				return
			}
			w.Header().Set("content-type", "text/event-stream")
			_, _ = io.WriteString(w, p.respSSE)
		case "/gw/v1/messages":
			if p.gwCode != 0 {
				w.WriteHeader(p.gwCode)
				return
			}
			w.Header().Set("content-type", "application/json")
			_, _ = io.WriteString(w, `{"id":"m","type":"message","model":"gpt-6.1-sol","content":[{"type":"text","text":"cloud says hi"}],"usage":{"input_tokens":9,"output_tokens":2}}`)
		default:
			var req struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(raw, &req)
			w.Header().Set("content-type", "application/json")
			_, _ = io.WriteString(w, `{"id":"m","type":"message","model":"`+req.Model+`","content":[{"type":"text","text":"harness says hi"}],"usage":{"input_tokens":10,"output_tokens":2}}`)
		}
	}))
	t.Cleanup(stub.Close)
	target, _ := url.Parse(stub.URL)
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "down.example" {
			return nil, errors.New("connection refused")
		}
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		return http.DefaultTransport.RoundTrip(r)
	})
	return New(Config{
		Adapters:   []providers.Adapter{anthropic.New("https://api.anthropic.com")},
		Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		Creds:      stubCreds{key: "sk-byok"},
		Sink:       &captureSink{},
		HTTPClient: &http.Client{Transport: transport},
		Cloud:      p.cloud,
	})
}

func (p *poolStub) last(path string) (*http.Request, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.got[path])
	if n == 0 {
		return nil, ""
	}
	return p.got[path][n-1], p.bodies[path][n-1]
}

func localTarget(host string) *RouteTarget {
	header := http.Header{}
	header.Set("authorization", "Bearer sk-openai")
	return &RouteTarget{PoolID: "openai/gpt-6.1-sol", Via: "local", Host: "openai", Model: "gpt-6.1-sol", Wire: translate.Chat,
		URL: "https://" + host + "/chat/completions", Header: header, Affinity: "x-session-affinity",
		Translate: translate.Options{Model: "gpt-6.1-sol", Dialect: "openai_chat", Route: "openai", MaxTokensField: "max_completion_tokens"}}
}

func poolSend(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("x-api-key", "sk-ant-api-key")
	req.Header.Set("x-cave-agent", "claude")
	req.Header.Set("x-claude-code-session-id", "sess-1")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

const poolBody = `{"model":"claude-opus-5-5","max_tokens":50,"messages":[{"role":"user","content":"fix the bug"}]}`

type poolCase struct {
	stub     *poolStub
	srv      *Server
	rejected *atomic.Int32
}

func newPoolCase(t *testing.T, target *RouteTarget, effort string) poolCase {
	rejected := &atomic.Int32{}
	stub := &poolStub{}
	stub.cloud = &fakeCloud{answer: RouteAnswer{Outcome: "routed", Effort: effort, Target: target, Reject: func() { rejected.Add(1) }}}
	return poolCase{stub: stub, srv: stub.server(t), rejected: rejected}
}

func TestPoolLocalTargetTranslatesAndNeverTouchesTheHarnessPath(t *testing.T) {
	c := newPoolCase(t, localTarget("api.openai.com"), "high")
	rec := poolSend(t, c.srv, poolBody)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "pool says hi") || !strings.Contains(rec.Body.String(), `"claude-opus-5-5"`) {
		t.Fatalf("answer %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("x-caveman-routed-from") != "claude-opus-5-5" {
		t.Errorf("routed-from = %q", rec.Header().Get("x-caveman-routed-from"))
	}
	req, body := c.stub.last("/chat/completions")
	if req == nil {
		t.Fatal("the pool host was not called")
	}
	if req.Header.Get("authorization") != "Bearer sk-openai" || req.Header.Get("x-api-key") != "" || req.Header.Get("x-session-affinity") == "" ||
		strings.Contains(req.Header.Get("x-session-affinity"), "sess-1") {
		t.Errorf("pool headers = %v", req.Header)
	}
	var sent map[string]any
	_ = json.Unmarshal([]byte(body), &sent)
	if sent["model"] != "gpt-6.1-sol" || sent["reasoning_effort"] != "high" || sent["max_completion_tokens"] == nil {
		t.Errorf("pool body = %s", body)
	}
	if req, _ := c.stub.last("/v1/messages"); req != nil {
		t.Error("the harness's own provider was called too")
	}
}

func TestPoolLocalTargetStreamsBackInTheCallersGrammar(t *testing.T) {
	c := newPoolCase(t, localTarget("api.openai.com"), "")
	c.stub.poolSSE = "data: {\"id\":\"c1\",\"model\":\"gpt-6.1-sol\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"str\"}}]}\n\n" +
		"data: {\"id\":\"c1\",\"model\":\"gpt-6.1-sol\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"eamed\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"c1\",\"model\":\"gpt-6.1-sol\",\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n"
	rec := poolSend(t, c.srv, `{"model":"claude-opus-5-5","max_tokens":50,"stream":true,"messages":[{"role":"user","content":"fix the bug"}]}`)
	out := rec.Body.String()
	for _, want := range []string{"event: message_start", `"claude-opus-5-5"`, "content_block_delta", "str", "eamed", "message_stop"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stream lacks %q:\n%s", want, out)
		}
	}
}

func TestPoolFailureBeforeTheFirstByteFallsBackToTheAskedModel(t *testing.T) {
	for name, setup := range map[string]func(*poolCase){
		"pool 503":       func(c *poolCase) { c.stub.poolCode = 503 },
		"pool 401":       func(c *poolCase) { c.stub.poolCode = 401 },
		"pool down":      func(c *poolCase) { c.stub.cloud.answer.Target = localTarget("down.example") },
		"gateway error":  func(c *poolCase) { c.stub.gwCode = 502; c.stub.cloud.answer.Target = cloudTarget() },
		"untranslatable": func(c *poolCase) { c.stub.cloud.answer.Target.Wire = "unknown-grammar" },
	} {
		t.Run(name, func(t *testing.T) {
			c := newPoolCase(t, localTarget("api.openai.com"), "high")
			setup(&c)
			rec := poolSend(t, c.srv, poolBody)
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "harness says hi") {
				t.Fatalf("answer %d: %s", rec.Code, rec.Body.String())
			}
			req, body := c.stub.last("/v1/messages")
			if req == nil || req.Header.Get("x-api-key") != "sk-byok" || !strings.Contains(body, `"claude-opus-5-5"`) || strings.Contains(body, `"effort"`) {
				t.Fatalf("fallback request = %v %s", req, body)
			}
			if rec.Header().Get("x-caveman-routed-from") != "" {
				t.Error("a fallback must not say it was routed")
			}
			if c.rejected.Load() != 1 {
				t.Errorf("Reject called %d times, want 1", c.rejected.Load())
			}
		})
	}
}

func cloudTarget() *RouteTarget {
	header := http.Header{}
	header.Set("authorization", "Bearer cave_project_key")
	header.Set("x-caveman-route", "cloud/gpt-6.1-sol")
	return &RouteTarget{PoolID: "cloud/gpt-6.1-sol", Via: "cloud", Host: "cloud", Model: "gpt-6.1-sol", Wire: translate.Messages,
		URL: "https://cloud.example/gw/v1/messages", Header: header}
}

func TestPoolCloudTargetSendsTheCallersGrammarToTheGateway(t *testing.T) {
	c := newPoolCase(t, cloudTarget(), "low")
	rec := poolSend(t, c.srv, poolBody)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "cloud says hi") || !strings.Contains(rec.Body.String(), `"model":"claude-opus-5-5"`) {
		t.Fatalf("answer %d: %s", rec.Code, rec.Body.String())
	}
	req, body := c.stub.last("/gw/v1/messages")
	if req == nil {
		t.Fatal("the gateway was not called")
	}
	if req.Header.Get("x-caveman-route") != "cloud/gpt-6.1-sol" || req.Header.Get("authorization") != "Bearer cave_project_key" ||
		req.Header.Get("x-api-key") != "" || req.Header.Get("anthropic-version") == "" {
		t.Errorf("gateway headers = %v", req.Header)
	}
	if !strings.Contains(body, `"model":"claude-opus-5-5"`) || !strings.Contains(body, `"output_config":{"effort":"low"}`) {
		t.Errorf("gateway body = %s", body)
	}
}

func TestHarnessPathDropsReasoningAPoolHostWrote(t *testing.T) {
	c := newPoolCase(t, localTarget("api.openai.com"), "")
	if rec := poolSend(t, c.srv, poolBody); !strings.Contains(rec.Body.String(), "pool says hi") {
		t.Fatalf("pool turn: %s", rec.Body.String())
	}
	c.stub.cloud.answer = RouteAnswer{Outcome: "kept"}
	body := `{"model":"claude-opus-5-5","max_tokens":50,"messages":[{"role":"user","content":"a"},{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"caveman:v1:openai:gpt-6.1-sol"},{"type":"text","text":"b"}]},{"role":"user","content":"c"}]}`
	if rec := poolSend(t, c.srv, body); rec.Code != 200 {
		t.Fatalf("answer %d: %s", rec.Code, rec.Body.String())
	}
	if _, sent := c.stub.last("/v1/messages"); strings.Contains(sent, "caveman:v1") || !strings.Contains(sent, `"text":"b"`) {
		t.Fatalf("harness body = %s", sent)
	}
	// A session no pool host served goes byte for byte.
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("x-api-key", "sk-ant-api-key")
	req.Header.Set("x-claude-code-session-id", "another-session")
	c.srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
	if _, sent := c.stub.last("/v1/messages"); sent != body {
		t.Fatalf("an unpooled session's body changed: %s", sent)
	}
	if out := nativeHistory("anthropic", "/v1/messages", []byte(poolBody)); string(out) != poolBody {
		t.Fatalf("a clean body changed: %s", out)
	}
}

func TestPoolCutStreamNeverEndsAsACleanEOF(t *testing.T) {
	c := newPoolCase(t, localTarget("api.openai.com"), "")
	c.stub.poolSSE = "data: {\"id\":\"c1\",\"model\":\"gpt-6.1-sol\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"half\"}}]}\n\n"
	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler {
			t.Fatalf("recovered %v, want http.ErrAbortHandler", recovered)
		}
	}()
	poolSend(t, c.srv, `{"model":"claude-opus-5-5","max_tokens":50,"stream":true,"messages":[{"role":"user","content":"fix the bug"}]}`)
	t.Fatal("a cut stream returned normally")
}

func chatgptTarget() *RouteTarget {
	header := http.Header{}
	header.Set("authorization", "Bearer chatgpt-access")
	return &RouteTarget{PoolID: "chatgpt/gpt-6.1-sol", Via: "local", Host: "chatgpt", Model: "gpt-6.1-sol", Wire: translate.Responses,
		URL: "https://api.openai.com/responses", Header: header,
		Translate: translate.Options{Model: "gpt-6.1-sol", Route: "chatgpt", ChatGPTLogin: true}}
}

// A Claude Code request runs on the ChatGPT login (Responses only) and reads
// an Anthropic stream back; a refusal before the first byte falls back.
func TestPoolClaudeCodeOnTheChatGPTLogin(t *testing.T) {
	c := newPoolCase(t, chatgptTarget(), "high")
	c.stub.respSSE = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\"}}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"item_id\":\"m1\",\"delta\":\"plan says hi\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"usage\":{\"input_tokens\":12,\"output_tokens\":3}}}\n\n"
	rec := poolSend(t, c.srv, `{"model":"claude-opus-5-5","max_tokens":50,"stream":true,"tools":[{"name":"Read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"fix the bug"}]}`)
	out := rec.Body.String()
	for _, want := range []string{"event: message_start", `"claude-opus-5-5"`, "plan says hi", "message_stop"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stream lacks %q:\n%s", want, out)
		}
	}
	req, body := c.stub.last("/responses")
	if req == nil || req.Header.Get("authorization") != "Bearer chatgpt-access" || req.Header.Get("x-api-key") != "" || req.Header.Get("anthropic-version") != "" {
		t.Fatalf("responses request = %v", req)
	}
	var sent map[string]any
	_ = json.Unmarshal([]byte(body), &sent)
	if sent["model"] != "gpt-6.1-sol" || sent["store"] != false || sent["stream"] != true || sent["additional_tools"] == nil ||
		encode(sent["reasoning"]) != `{"effort":"high","summary":"auto"}` {
		t.Fatalf("responses body = %s", body)
	}

	c = newPoolCase(t, chatgptTarget(), "high")
	c.stub.respCode = 401
	if rec := poolSend(t, c.srv, poolBody); !strings.Contains(rec.Body.String(), "harness says hi") || c.rejected.Load() != 1 {
		t.Fatalf("fallback: %s", rec.Body.String())
	}
}

func encode(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
