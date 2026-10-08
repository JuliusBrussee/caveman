package gateway

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/internal/translate"
	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
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
	gwSSE    string // the gateway's stream, when set
	cloud    *fakeCloud
	sink     *captureSink
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
		case "/responses", "/v1/responses":
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
			if p.gwSSE != "" {
				w.Header().Set("content-type", "text/event-stream")
				_, _ = io.WriteString(w, p.gwSSE)
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
	p.sink = &captureSink{}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "down.example" {
			return nil, errors.New("connection refused")
		}
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		return http.DefaultTransport.RoundTrip(r)
	})
	return New(Config{
		Adapters:   []providers.Adapter{anthropic.New("https://api.anthropic.com"), openai.New("https://api.openai.com")},
		Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		Creds:      stubCreds{key: "sk-byok"},
		Sink:       p.sink,
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
	header.Set("authorization", "Bearer sk-fireworks")
	return &RouteTarget{PoolID: "fireworks/kimi-k3", Via: "local", Host: "fireworks", Model: "kimi-k3", Wire: translate.Chat,
		URL: "https://" + host + "/chat/completions", Header: header, Affinity: "x-session-affinity",
		Translate: translate.Options{Model: "accounts/fireworks/models/kimi-k3", Dialect: "openai_chat", Route: "fireworks/kimi-k3"}}
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
	if req.Header.Get("authorization") != "Bearer sk-fireworks" || req.Header.Get("x-api-key") != "" || req.Header.Get("x-session-affinity") == "" ||
		strings.Contains(req.Header.Get("x-session-affinity"), "sess-1") {
		t.Errorf("pool headers = %v", req.Header)
	}
	var sent map[string]any
	_ = json.Unmarshal([]byte(body), &sent)
	if sent["model"] != "accounts/fireworks/models/kimi-k3" || sent["reasoning_effort"] != "high" || sent["max_tokens"] == nil {
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
			if req == nil || req.Header.Get("x-api-key") != "sk-byok" || !strings.Contains(body, `"claude-opus-5-5"`) || !strings.Contains(body, `"output_config":{"effort":"high"}`) {
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
	header.Set("x-caveman-route", "cloud:openai:gpt-6.1-sol")
	return &RouteTarget{PoolID: "cloud:openai:gpt-6.1-sol", Via: "cloud", Host: "cloud", Model: "gpt-6.1-sol", Wire: translate.Messages,
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
	if req.Header.Get("x-caveman-route") != "cloud:openai:gpt-6.1-sol" || req.Header.Get("authorization") != "Bearer cave_project_key" ||
		req.Header.Get("x-api-key") != "" || req.Header.Get("anthropic-version") == "" {
		t.Errorf("gateway headers = %v", req.Header)
	}
	if body != poolBody {
		t.Errorf("gateway body = %s, want the agent's own bytes", body)
	}
}

func TestHarnessPathDropsReasoningAPoolHostWrote(t *testing.T) {
	c := newPoolCase(t, localTarget("api.openai.com"), "")
	if rec := poolSend(t, c.srv, poolBody); !strings.Contains(rec.Body.String(), "pool says hi") {
		t.Fatalf("pool turn: %s", rec.Body.String())
	}
	c.stub.cloud.answer = RouteAnswer{Outcome: "kept"}
	body := `{"model":"claude-opus-5-5","max_tokens":50,"messages":[{"role":"user","content":"a"},{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"caveman:v1:17:fireworks/kimi-k3:accounts/fireworks/models/kimi-k3"},{"type":"text","text":"b"}]},{"role":"user","content":"c"}]}`
	if rec := poolSend(t, c.srv, body); rec.Code != 200 {
		t.Fatalf("answer %d: %s", rec.Code, rec.Body.String())
	}
	if _, sent := c.stub.last("/v1/messages"); strings.Contains(sent, "caveman:v1") || !strings.Contains(sent, `"text":"b"`) {
		t.Fatalf("harness body = %s", sent)
	}
	// A session this process never saw pooled (a restart, an evicted entry,
	// a resumed conversation) is cleaned all the same.
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("x-api-key", "sk-ant-api-key")
	req.Header.Set("x-claude-code-session-id", "another-session")
	c.srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
	if _, sent := c.stub.last("/v1/messages"); strings.Contains(sent, "caveman:v1") {
		t.Fatalf("an unseen session's foreign reasoning reached Anthropic: %s", sent)
	}
	if out := nativeHistory("anthropic", "/v1/messages", []byte(poolBody)); string(out) != poolBody {
		t.Fatalf("a clean body changed: %s", out)
	}
	// A chat caller's history: the reasoning a Messages or Responses host
	// wrote (an envelope in reasoning_details, its text) goes to no chat API.
	envelope := base64.StdEncoding.EncodeToString([]byte(`{"caveman":"v1","blocks":[{"type":"thinking","thinking":"t","signature":"sig"}]}`))
	chat := `{"model":"gpt-x","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b","reasoning_content":"t","reasoning_details":[{"type":"reasoning.encrypted","data":"` + envelope + `"}]},{"role":"user","content":"c"}]}`
	if out := string(nativeHistory("openai", "/v1/chat/completions", []byte(chat))); strings.Contains(out, envelope) || strings.Contains(out, `"t"`) || !strings.Contains(out, `"content":"b"`) {
		t.Fatalf("chat history to OpenAI = %s", out)
	}
	if clean := `{"model":"gpt-x","messages":[{"role":"user","content":"a"}]}`; string(nativeHistory("openai", "/v1/chat/completions", []byte(clean))) != clean {
		t.Fatal("a clean chat body changed")
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

// The pool host gets its own reasoning back on the next turn: the harness
// path's cleaning never touches what the pool host is sent.
func TestPoolHostGetsItsOwnReasoningBackOnTheNextTurn(t *testing.T) {
	target := localTarget("api.openai.com")
	target.Translate.Replay = true
	c := newPoolCase(t, target, "")
	if rec := poolSend(t, c.srv, poolBody); !strings.Contains(rec.Body.String(), "pool says hi") {
		t.Fatalf("turn 1: %s", rec.Body.String())
	}
	turn2 := `{"model":"claude-opus-5-5","max_tokens":50,"messages":[{"role":"user","content":"fix the bug"},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"my own plan","signature":"caveman:v1:17:fireworks/kimi-k3:accounts/fireworks/models/kimi-k3"},{"type":"text","text":"pool says hi"}]},` +
		`{"role":"user","content":"go on"}]}`
	if rec := poolSend(t, c.srv, turn2); !strings.Contains(rec.Body.String(), "pool says hi") {
		t.Fatalf("turn 2: %s", rec.Body.String())
	}
	_, body := c.stub.last("/chat/completions")
	if !strings.Contains(body, `"reasoning_content":"my own plan"`) {
		t.Fatalf("turn 2 lost the host's reasoning: %s", body)
	}
}

// A 2xx that fails before any content still falls back to the asked model.
func TestPoolFailureAfterHeadersBeforeContentFallsBack(t *testing.T) {
	c := newPoolCase(t, localTarget("api.openai.com"), "")
	c.stub.poolSSE = "data: {\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy\"}}\n\n"
	rec := poolSend(t, c.srv, `{"model":"claude-opus-5-5","max_tokens":50,"stream":true,"messages":[{"role":"user","content":"fix the bug"}]}`)
	if !strings.Contains(rec.Body.String(), "harness says hi") || strings.Contains(rec.Body.String(), "busy") || c.rejected.Load() != 1 {
		t.Fatalf("answer: %s", rec.Body.String())
	}
}

// A Claude Code request with tools on an OpenAI API key goes out on the
// Responses wire with function tools, at the answered effort (the catalog
// lists max for gpt-6.1-sol).
func TestPoolOpenAIKeyIsResponsesOnly(t *testing.T) {
	header := http.Header{}
	header.Set("authorization", "Bearer sk-openai")
	target := &RouteTarget{PoolID: "openai/gpt-6.1-sol", Via: "local", Host: "openai", Model: "gpt-6.1-sol", Wire: translate.Responses,
		URL: "https://api.openai.com/v1/responses", Header: header, Translate: translate.Options{Model: "gpt-6.1-sol", Route: "openai"}}
	c := newPoolCase(t, target, "max")
	c.stub.respSSE = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"gpt says hi\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\"}}\n\n"
	rec := poolSend(t, c.srv, `{"model":"claude-opus-5-5","max_tokens":50,"stream":true,"tools":[{"name":"Bash","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"fix the bug"}]}`)
	if !strings.Contains(rec.Body.String(), "gpt says hi") {
		t.Fatalf("answer: %s", rec.Body.String())
	}
	req, body := c.stub.last("/v1/responses")
	if req == nil {
		t.Fatal("not sent on the Responses wire")
	}
	if got, _ := c.stub.last("/chat/completions"); got != nil {
		t.Fatal("an OpenAI request went to chat completions")
	}
	var sent map[string]any
	_ = json.Unmarshal([]byte(body), &sent)
	if encode(sent["tools"]) != `[{"name":"Bash","parameters":{"type":"object"},"strict":false,"type":"function"}]` || encode(sent["reasoning"]) != `{"effort":"max","summary":"auto"}` {
		t.Fatalf("responses body = %s", body)
	}
}

func TestPoolCloudTargetCarriesTheAnsweredEffort(t *testing.T) {
	c := newPoolCase(t, cloudTarget(), "xhigh")
	poolSend(t, c.srv, poolBody)
	if req, body := c.stub.last("/gw/v1/messages"); req == nil || req.Header.Get("x-caveman-effort") != "xhigh" || body != poolBody {
		t.Fatalf("gateway request = %v %s", req, body)
	}
	c = newPoolCase(t, cloudTarget(), "")
	poolSend(t, c.srv, poolBody)
	if req, _ := c.stub.last("/gw/v1/messages"); req == nil || req.Header.Values("x-caveman-effort") != nil {
		t.Fatalf("an answer without effort sent x-caveman-effort: %v", req)
	}
}

// A gateway 2xx that fails before content still falls back.
func TestPoolCloudFailureBeforeContentFallsBack(t *testing.T) {
	c := newPoolCase(t, cloudTarget(), "")
	c.stub.gwSSE = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy\"}}\n\n"
	rec := poolSend(t, c.srv, `{"model":"claude-opus-5-5","max_tokens":50,"stream":true,"messages":[{"role":"user","content":"fix the bug"}]}`)
	if !strings.Contains(rec.Body.String(), "harness says hi") || strings.Contains(rec.Body.String(), "busy") {
		t.Fatalf("answer: %s", rec.Body.String())
	}
}

// An OpenAI login the pool holds is not the harness's credential unless it
// is the same key: its reasoning comes back tagged to that login, never as
// the harness's own OpenAI reasoning.
func TestPoolOpenAILoginReasoningIsTaggedUnlessItIsTheHarnessKey(t *testing.T) {
	for key, route := range map[string]string{"sk-openai": "openai-login", "sk-byok": "openai"} {
		header := http.Header{}
		header.Set("authorization", "Bearer "+key)
		target := &RouteTarget{PoolID: "openai/gpt-6.1-sol", Via: "local", Host: "openai", Model: "gpt-6.1-sol", Wire: translate.Responses,
			URL: "https://api.openai.com/v1/responses", Header: header, Translate: translate.Options{Model: "gpt-6.1-sol", Route: "openai"}}
		c := newPoolCase(t, target, "")
		c.stub.respSSE = "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"reasoning\",\"id\":\"rs_1\",\"summary\":[],\"encrypted_content\":\"BLOB\"}}\n\n" +
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"gpt says hi\"}\n\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\"}}\n\n"
		rec := poolSend(t, c.srv, `{"model":"claude-opus-5-5","max_tokens":50,"stream":true,"messages":[{"role":"user","content":"fix the bug"}]}`)
		if !strings.Contains(rec.Body.String(), `"signature":"caveman:r1:`+strconv.Itoa(len(route))+":"+route+`:BLOB"`) {
			t.Fatalf("%s: want route %s:\n%s", key, route, rec.Body.String())
		}
	}
}

// A failed target falls back at the answered effort fitted to the asked
// model: the word was chosen for the target, and minimal to Claude or max to
// OpenAI is a 400.
func TestPoolFallbackFitsTheEffortToTheAskedModel(t *testing.T) {
	c := newPoolCase(t, localTarget("down.example"), "minimal")
	poolSend(t, c.srv, poolBody)
	if _, body := c.stub.last("/v1/messages"); !strings.Contains(body, `"output_config":{"effort":"low"}`) {
		t.Fatalf("minimal on Claude: %s", body)
	}
	c = newPoolCase(t, localTarget("down.example"), "none")
	poolSend(t, c.srv, poolBody)
	if _, body := c.stub.last("/v1/messages"); strings.Contains(body, `"effort"`) {
		t.Fatalf("none on Claude: %s", body)
	}
	c = newPoolCase(t, localTarget("down.example"), "max")
	poolSend(t, c.srv, `{"model":"claude-sonnet-5-5","max_tokens":50,"thinking":{"type":"between_tools"},"messages":[{"role":"user","content":"fix the bug"}]}`)
	if _, body := c.stub.last("/v1/messages"); !strings.Contains(body, `"output_config":{"effort":"high"}`) {
		t.Fatalf("max with thinking off: %s", body)
	}
	// gpt-uncataloged has no catalog row (OpenAI's common set); the catalog lists max for the others.
	for model, want := range map[string]string{"gpt-uncataloged": "xhigh", "gpt-6.1-sol": "max", "gpt-6-sol": "max"} {
		c = newPoolCase(t, localTarget("down.example"), "max")
		c.stub.respSSE = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"harness says hi\"}\n\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\"}}\n\n"
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"`+model+`","stream":true,"reasoning":{"effort":"low"},"input":[{"role":"user","content":"fix the bug"}]}`))
		req.Header.Set("authorization", "Bearer sk-proj-harness")
		req.Header.Set("x-cave-agent", "codex")
		req.Header.Set("session_id", "thread-1")
		rec := httptest.NewRecorder()
		c.srv.Handler().ServeHTTP(rec, req)
		if _, body := c.stub.last("/v1/responses"); !strings.Contains(rec.Body.String(), "harness says hi") || !strings.Contains(body, `"reasoning":{"effort":"`+want+`"}`) {
			t.Fatalf("max on %s: %s\nanswer %s", model, body, rec.Body.String())
		}
	}
}

// Pass-through traffic and other origins are never cleaned: only the
// provider's own API refuses another host's reasoning.
func TestHarnessCleaningOnlyOnTheProvidersOwnAPI(t *testing.T) {
	body := `{"model":"claude-opus-5-5","max_tokens":50,"messages":[{"role":"user","content":"a"},{"role":"assistant","content":[{"type":"thinking","thinking":"x","signature":"caveman:v1:9:fireworks:kimi"},{"type":"text","text":"b"}]},{"role":"user","content":"c"}]}`
	stub := &poolStub{cloud: &fakeCloud{}}
	pooled := stub.server(t)
	server := func(origin string) *Server { // no Cloud: the harness path outside the route stage
		return New(Config{
			Adapters: []providers.Adapter{anthropic.New(origin)},
			Auth:     stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
			Creds:    stubCreds{key: "sk-byok"}, Sink: &captureSink{}, HTTPClient: pooled.httpClient,
		})
	}
	send := func(srv *Server, header string) string {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		req.Header.Set("x-api-key", "sk-ant-api-key")
		if header != "" {
			req.Header.Set("x-cave-transforms", header)
		}
		srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
		_, sent := stub.last("/v1/messages")
		return sent
	}
	own := server("https://api.anthropic.com")
	if sent := send(own, ""); strings.Contains(sent, "caveman:v1") {
		t.Fatalf("own API kept another host's reasoning: %s", sent)
	}
	if sent := send(own, "caveman.pass-through.v1"); sent != body {
		t.Fatalf("pass-through was changed: %s", sent)
	}
	other := server("https://llm.example.com")
	if sent := send(other, ""); sent != body || len(stub.got["/v1/messages"]) != 3 {
		t.Fatalf("another origin was changed (%d sent): %s", len(stub.got["/v1/messages"]), sent)
	}
}

// A chat request carries the runtime's reasoning envelopes to no chat API,
// whatever its origin (routing off included): DeepSeek never gets another
// host's reasoning. A clean body goes byte for byte, pass-through untouched.
func TestHarnessChatCleaningOnEveryOrigin(t *testing.T) {
	envelope := base64.StdEncoding.EncodeToString([]byte(`{"caveman":"v1","blocks":[{"type":"thinking","thinking":"t","signature":"sig"}]}`))
	dirty := `{"model":"deepseek-chat","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b","reasoning_content":"t","reasoning_details":[{"type":"reasoning.encrypted","data":"` + envelope + `"}]},{"role":"user","content":"c"}]}`
	clean := `{"model":"deepseek-chat",  "messages":[{"role":"user","content":"a"}]}`
	stub := &poolStub{cloud: &fakeCloud{}}
	pooled := stub.server(t)
	srv := New(Config{
		Adapters: []providers.Adapter{openai.New("https://api.deepseek.com")},
		Auth:     stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		Creds:    stubCreds{key: "sk-byok"}, Sink: &captureSink{}, HTTPClient: pooled.httpClient,
	})
	send := func(body, header string) string {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("authorization", "Bearer sk-deepseek")
		if header != "" {
			req.Header.Set("x-cave-transforms", header)
		}
		srv.Handler().ServeHTTP(httptest.NewRecorder(), req)
		_, sent := stub.last("/v1/chat/completions")
		return sent
	}
	if sent := send(dirty, ""); strings.Contains(sent, envelope) || strings.Contains(sent, "reasoning_content") || !strings.Contains(sent, `"content":"b"`) {
		t.Fatalf("another host's reasoning reached the chat API: %s", sent)
	}
	if sent := send(clean, ""); sent != clean {
		t.Fatalf("a clean body was changed: %s", sent)
	}
	if sent := send(dirty, "caveman.pass-through.v1"); sent != dirty {
		t.Fatalf("pass-through was changed: %s", sent)
	}
}

// The host's own failure after content is relayed as the stream's end: the
// agent gets it once and the connection ends cleanly, no abort.
func TestPoolUpstreamFailureAfterContentEndsCleanly(t *testing.T) {
	c := newPoolCase(t, cloudTarget(), "")
	c.stub.gwSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"x\",\"usage\":{\"input_tokens\":1}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n" +
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy\"}}\n\n"
	rec := poolSend(t, c.srv, `{"model":"claude-opus-5-5","max_tokens":50,"stream":true,"messages":[{"role":"user","content":"fix the bug"}]}`)
	out := rec.Body.String()
	if !strings.Contains(out, "partial") || strings.Count(out, "event: error") != 1 || strings.Contains(out, "harness says hi") {
		t.Fatalf("answer: %s", out)
	}
}

// The row says a pool attempt happened: on a fallback, the entry and why it
// failed; on a served answer, the entry and the host's own answer id.
func TestPoolAttemptIsOnTheRow(t *testing.T) {
	last := func(c poolCase) RequestRecord {
		c.stub.sink.mu.Lock()
		defer c.stub.sink.mu.Unlock()
		return c.stub.sink.rows[len(c.stub.sink.rows)-1]
	}
	c := newPoolCase(t, localTarget("api.fireworks.ai"), "")
	c.stub.poolCode = 400
	poolSend(t, c.srv, poolBody)
	if row := last(c); row.RoutePoolID != "fireworks/kimi-k3" || row.RouteReason != "pool_400" || row.Model != "claude-opus-5-5" {
		t.Fatalf("fallback row = %q %q %q", row.RoutePoolID, row.RouteReason, row.Model)
	}
	c = newPoolCase(t, localTarget("api.fireworks.ai"), "")
	poolSend(t, c.srv, poolBody)
	if row := last(c); row.RoutePoolID != "fireworks/kimi-k3" || row.UpstreamResponseID != "c1" {
		t.Fatalf("served row = %q %q", row.RoutePoolID, row.UpstreamResponseID)
	}
}
