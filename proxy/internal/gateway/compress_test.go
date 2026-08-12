package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/gemini"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

// stubCompressor is a test Compressor seam. With out set it shrinks every segment
// to out (reporting before/after tokens); with out nil it echoes the segment with
// no token delta (the engine no-op case). storeErr simulates an unstorable original.
type stubCompressor struct {
	calls              int
	out                []byte
	before             int
	after              int
	handle             string
	recovered          []byte
	lastRetrieveHandle string
	lastRetrieveQuery  string
	storeCalls         int
	storeErr           error
	typedContentType   string
	typedContentTypes  []string
}

func (c *stubCompressor) CompressSegment(seg []byte) ([]byte, int, int) {
	c.calls++
	if c.out == nil {
		return seg, len(seg), len(seg)
	}
	return c.out, c.before, c.after
}

func (c *stubCompressor) CompressSegmentType(seg []byte, contentType string) ([]byte, int, int) {
	c.typedContentType = contentType
	c.typedContentTypes = append(c.typedContentTypes, contentType)
	return c.CompressSegment(seg)
}

func (c *stubCompressor) StoreOriginal(body []byte) (string, error) {
	c.storeCalls++
	if c.storeErr != nil {
		return "", c.storeErr
	}
	if c.handle == "" {
		return "ccr_stub", nil
	}
	return c.handle, nil
}

func (c *stubCompressor) RetrieveOriginal(handle, query string) ([]byte, error) {
	c.lastRetrieveHandle = handle
	c.lastRetrieveQuery = query
	if c.recovered == nil {
		return nil, errors.New("missing test recovery")
	}
	return c.recovered, nil
}

type storeOnlyCompressor struct {
	calls      int
	storeCalls int
	out        []byte
	before     int
	after      int
	handle     string
}

func (c *storeOnlyCompressor) CompressSegment(seg []byte) ([]byte, int, int) {
	c.calls++
	return c.out, c.before, c.after
}

func (c *storeOnlyCompressor) StoreOriginal(body []byte) (string, error) {
	c.storeCalls++
	return c.handle, nil
}

type errReadCloser struct{}

func (errReadCloser) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func (errReadCloser) Close() error {
	return nil
}

func newCompressTestServer(t *testing.T, upstream string, sink TelemetrySink, comp Compressor) *Server {
	t.Helper()
	return New(Config{
		Adapters:   []providers.Adapter{openai.New(upstream)},
		Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}},
		Creds:      stubCreds{key: "sk-byok"},
		Sink:       sink,
		Compressor: comp,
		HTTPClient: &http.Client{},
	})
}

var chatReqBody = `{"model":"gpt-5.5","messages":[{"role":"user","content":"` + strings.Repeat("the quick brown fox jumps over the lazy dog and keeps running for a very long time. ", 10) + `"}]}`

func TestExplicitPassThroughSuppressesCompressMode(t *testing.T) {
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamBody = string(body)
		_, _ = io.WriteString(w, `{"id":"chatcmpl","model":"gpt-5.5","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	sink := &captureSink{}
	comp := &stubCompressor{out: []byte("SHORT"), before: 100, after: 5, handle: "ccr_fixture"}
	srv := New(Config{
		Adapters:       []providers.Adapter{openai.New(upstream.URL)},
		Auth:           stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}},
		Creds:          stubCreds{key: "sk-byok"},
		Sink:           sink,
		Compressor:     comp,
		RecoveryViaMCP: true,
		HTTPClient:     &http.Client{},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatReqBody))
	req.Header.Set("x-cave-transforms", "caveman.pass-through.v1")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || upstreamBody != chatReqBody || comp.calls != 0 || comp.storeCalls != 0 {
		t.Fatalf("explicit pass-through ran compression: status=%d calls=%d stores=%d body=%s", rec.Code, comp.calls, comp.storeCalls, upstreamBody)
	}
	if row := sink.last(t); row.RawRequestSHA256 != row.TransformedRequestSHA256 || row.RecoveryHandle != "" {
		t.Fatalf("explicit pass-through recorded compression: %+v", row)
	}
}

func TestCompiledMultiRouteExecutesOrderedLockedWinners(t *testing.T) {
	toolResult := strings.Repeat(`{"rows":[{"id":1,"ok":true}]}`, 30)
	userHistory := strings.Repeat("ordered conversation history ", 30)
	body := []byte(`{"model":"gpt-5.5","messages":[` +
		`{"role":"user","content":` + strconv.Quote(userHistory) + `},` +
		`{"role":"tool","tool_call_id":"call_1","content":` + strconv.Quote(toolResult) + `}]}`)
	comp := &stubCompressor{out: []byte("SHORT"), before: 200, after: 10}
	srv := &Server{compressor: comp}
	transform := providers.TransformResult{Body: body}
	routes := []compiledRoute{
		{SegmentKind: "tool_result", TransformID: "caveman.engine.json.v1", ContentType: "json"},
		{SegmentKind: "history", TransformID: "caveman.engine.text.v1", ContentType: "text"},
	}
	outcome := srv.compressRequest(
		openai.New("http://example.invalid"),
		body,
		providers.RequestMetadata{Provider: "openai", Endpoint: "/v1/chat/completions"},
		&transform,
		"req-routes",
		routes,
	)
	if outcome == nil {
		t.Fatal("compiled route map did not transform matching provider blocks")
	}
	if got := strings.Join(comp.typedContentTypes, ","); got != "text,json" {
		t.Fatalf("provider execution order = %q, want text,json", got)
	}
	if got := strings.Join(transform.OptimizerIDs, ","); got != "caveman.engine.json.v1,caveman.engine.text.v1" {
		t.Fatalf("optimizer ids = %q", got)
	}
	if !strings.Contains(string(transform.Body), "SHORT") || len(outcome.handles) != 2 {
		t.Fatalf("route output/recovery evidence missing: body=%s handles=%v", transform.Body, outcome.handles)
	}
}

func TestCompiledRouteSemanticMismatchPassesThrough(t *testing.T) {
	userHistory := strings.Repeat("history with no tool result ", 30)
	body := []byte(`{"model":"gpt-5.5","messages":[` +
		`{"role":"user","content":` + strconv.Quote(userHistory) + `}]}`)
	comp := &stubCompressor{out: []byte("SHORT"), before: 200, after: 10}
	srv := &Server{compressor: comp}
	transform := providers.TransformResult{Body: body}
	outcome := srv.compressRequest(
		openai.New("http://example.invalid"),
		body,
		providers.RequestMetadata{Provider: "openai", Endpoint: "/v1/chat/completions"},
		&transform,
		"req-route-mismatch",
		[]compiledRoute{{
			SegmentKind: "tool_result",
			TransformID: "caveman.engine.json.v1",
			ContentType: "json",
		}},
	)
	if outcome != nil || !bytes.Equal(transform.Body, body) || len(comp.typedContentTypes) != 0 {
		t.Fatal("unmatched compiled semantics must preserve original bytes without compressor call")
	}
}

func TestCompiledPartialRouteTransformsOnlyMatchingLiveKind(t *testing.T) {
	history := strings.Repeat("unselected live history ", 30)
	toolResult := strings.Repeat(`{"selected":true,"rows":[1,2,3]}`, 30)
	body := []byte(`{"model":"gpt-5.5","messages":[` +
		`{"role":"user","content":` + strconv.Quote(history) + `},` +
		`{"role":"tool","tool_call_id":"call_1","content":` + strconv.Quote(toolResult) + `}]}`)
	comp := &stubCompressor{out: []byte("TOOL_SHORT"), before: 200, after: 10}
	srv := &Server{compressor: comp}
	transform := providers.TransformResult{Body: body}
	outcome := srv.compressRequest(
		openai.New("http://example.invalid"),
		body,
		providers.RequestMetadata{Provider: "openai", Endpoint: "/v1/chat/completions"},
		&transform,
		"req-partial-route",
		[]compiledRoute{{
			SegmentKind: "tool_result",
			TransformID: "caveman.engine.json.v1",
			ContentType: "json",
		}},
	)
	if outcome == nil {
		t.Fatal("matching route in partial plan did not transform")
	}
	if !bytes.Contains(transform.Body, []byte(strconv.Quote(history))) {
		t.Fatalf("unselected history changed: %s", transform.Body)
	}
	if !bytes.Contains(transform.Body, []byte("TOOL_SHORT")) {
		t.Fatalf("selected tool result was not transformed: %s", transform.Body)
	}
	if got := strings.Join(transform.OptimizerIDs, ","); got != "caveman.engine.json.v1" {
		t.Fatalf("optimizer ids = %q, want selected tool-result route only", got)
	}
}

func TestCompiledRouteNeverReplaysCachedReplacementFromUnselectedKind(t *testing.T) {
	toolResult := strings.Repeat(`{"secret":"original"}`, 40)
	history := strings.Repeat("selected live history ", 40)
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":` + strconv.Quote(toolResult) + `}]},` +
		`{"role":"assistant","content":"ok"},` +
		`{"role":"user","content":` + strconv.Quote(history) + `}` +
		`]}`)
	cache := newTestPrefixCache()
	if _, err := cache.RememberReplacement("unlocked", []byte(toolResult), []byte("WRONG_LOCK_REPLACEMENT"), "ccr:old"); err != nil {
		t.Fatal(err)
	}
	comp := &stubCompressor{out: []byte("SHORT"), before: 200, after: 10}
	srv := &Server{compressor: comp, prefixCache: cache}
	transform := providers.TransformResult{Body: body}
	outcome := srv.compressRequest(
		anthropic.New("http://example.invalid"),
		body,
		providers.RequestMetadata{Provider: "anthropic", Endpoint: "/v1/messages"},
		&transform,
		"req-route-cache-scope",
		[]compiledRoute{{
			SegmentKind: "history",
			TransformID: "caveman.engine.text.v1",
			ContentType: "text",
		}},
	)
	if outcome == nil {
		t.Fatal("selected history route did not transform")
	}
	if bytes.Contains(transform.Body, []byte("WRONG_LOCK_REPLACEMENT")) ||
		!bytes.Contains(transform.Body, []byte(strconv.Quote(toolResult))) {
		t.Fatalf("cached replacement from unselected semantic kind leaked into body: %s", transform.Body)
	}
	if got := strings.Join(transform.OptimizerIDs, ","); got != "caveman.engine.text.v1" {
		t.Fatalf("optimizer ids = %q, want selected history route only", got)
	}
}

const chatRespBody = `{"id":"chatcmpl_1","object":"chat.completion","model":"gpt-5.5","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1000,"completion_tokens":20}}`

func echoUpstream(t *testing.T, got *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*got = string(b)
		w.Header().Set("content-type", "application/json")
		w.Header().Set("x-request-id", "up-1")
		_, _ = io.WriteString(w, chatRespBody)
	}))
}

func containsStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// TestCompressModeShrinksRecordsAndDiscloses proves the S4 compress path end to
// end: the engine seam is called, the rewritten (smaller) body is sent upstream,
// the CCR handle + ratio are disclosed in headers and recorded, the optimizer id is
// attributed, and an inferred (never verified) compression saving is recorded.
func TestCompressModeShrinksRecordsAndDiscloses(t *testing.T) {
	sink := &captureSink{}
	comp := &stubCompressor{out: []byte("X"), before: 100, after: 40, handle: "ccr_test123"}
	rt := &captureTransport{responses: []string{chatRespBody}}
	srv := New(Config{
		Adapters:   []providers.Adapter{openai.New("https://upstream.test")},
		Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}},
		Creds:      stubCreds{key: "sk-byok"},
		Sink:       sink,
		Compressor: comp,
		HTTPClient: &http.Client{Transport: rt},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatReqBody))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if comp.calls == 0 {
		t.Fatal("compressor was never called in compress mode")
	}
	if len(rt.bodies) != 1 {
		t.Fatalf("upstream calls = %d, want 1", len(rt.bodies))
	}
	gotUpstreamBody := string(rt.bodies[0])
	if strings.Contains(gotUpstreamBody, "lazy dog") {
		t.Errorf("upstream still got the original content, want the rewritten body: %s", gotUpstreamBody)
	}
	if !strings.Contains(gotUpstreamBody, `"content":"X`) || !strings.Contains(gotUpstreamBody, "<<ccr:ccr_test123>>") {
		t.Errorf("upstream body missing compressed content marker: %s", gotUpstreamBody)
	}
	if got := rec.Header().Get("x-caveman-recovery-handle"); got != "ccr_test123" {
		t.Errorf("x-caveman-recovery-handle = %q, want ccr_test123", got)
	}
	if got := rec.Header().Get("x-caveman-compression-ratio"); got != "0.6000" {
		t.Errorf("x-caveman-compression-ratio = %q, want 0.6000", got)
	}
	if got := rec.Header().Get("x-cave-optimization"); !strings.Contains(got, "caveman-compression") {
		t.Errorf("x-cave-optimization = %q, want it to include caveman-compression", got)
	}

	row := sink.last(t)
	if row.RecoveryHandle != "ccr_test123" {
		t.Errorf("row.RecoveryHandle = %q, want ccr_test123", row.RecoveryHandle)
	}
	if row.CompressionTokensBefore != 100 || row.CompressionTokensAfter != 40 {
		t.Errorf("compression tokens = (%d, %d), want (100, 40)", row.CompressionTokensBefore, row.CompressionTokensAfter)
	}
	if !containsStr(row.OptimizationIDs, "caveman-compression") {
		t.Errorf("OptimizationIDs = %v, want to include caveman-compression", row.OptimizationIDs)
	}
	if row.Basis != "inferred" {
		t.Errorf("basis = %q, want inferred (standalone never claims verified)", row.Basis)
	}
	if row.SavingsUSD <= 0 {
		t.Errorf("savings = %v, want > 0 (compression removed input tokens for a priced model)", row.SavingsUSD)
	}
	if row.RawRequestSHA256 == row.TransformedRequestSHA256 {
		t.Error("compress mode shrank the body, so raw and transformed hashes must differ")
	}
}

func TestLockedBuildExecutesExactTypedTransform(t *testing.T) {
	sink := &captureSink{}
	comp := &stubCompressor{out: []byte("X"), before: 100, after: 40, handle: "ccr_typed"}
	rt := &captureTransport{responses: []string{chatRespBody}}
	srv := New(Config{
		Adapters:   []providers.Adapter{openai.New("https://upstream.test")},
		Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}},
		Creds:      stubCreds{key: "sk-byok"},
		Sink:       sink,
		Compressor: comp,
		HTTPClient: &http.Client{Transport: rt},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatReqBody))
	digest := strings.Repeat("a", 64)
	req.Header.Set("x-cave-agent-build", digest)
	req.Header.Set("x-cave-efficiency-plan", digest)
	req.Header.Set("x-cave-transforms", "caveman.engine.json.v1")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if comp.typedContentType != "json" {
		t.Fatalf("typed content = %q, want json", comp.typedContentType)
	}
	if got := rec.Header().Get("x-cave-optimization"); !strings.Contains(got, "caveman.engine.json.v1") {
		t.Fatalf("x-cave-optimization = %q", got)
	}
	if got := sink.last(t).OptimizationIDs; !containsStr(got, "caveman.engine.json.v1") {
		t.Fatalf("optimization ids = %v", got)
	}
}

const retrieveCallResp = `{"id":"chatcmpl_r","object":"chat.completion","model":"gpt-5.5","choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_abc","type":"function","function":{"name":"caveman_retrieve","arguments":"{\"handle\":\"ccr_test123\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":500,"completion_tokens":10}}`

func TestCompressModeRetrieveRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, string(b))
		n := len(calls)
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		w.Header().Set("x-request-id", "up-retrieve")
		if n == 1 {
			_, _ = io.WriteString(w, retrieveCallResp)
			return
		}
		_, _ = io.WriteString(w, chatRespBody)
	}))
	defer upstream.Close()

	sink := &captureSink{}
	comp := &stubCompressor{
		out:       []byte("X"),
		before:    100,
		after:     40,
		handle:    "ccr_test123",
		recovered: []byte(chatReqBody),
	}
	srv := newCompressTestServer(t, upstream.URL, sink, comp)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatReqBody))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("upstream calls = %d, want 2 (initial + retrieve continuation)", len(calls))
	}
	if !strings.Contains(calls[0], retrieveToolName) {
		t.Errorf("initial upstream call missing caveman_retrieve tool: %s", calls[0])
	}
	if !strings.Contains(calls[1], "the quick brown fox") || !strings.Contains(calls[1], `"role":"tool"`) || !strings.Contains(calls[1], "call_abc") {
		t.Errorf("continuation missing recovered original tool result: %s", calls[1])
	}
	if strings.Contains(rec.Body.String(), retrieveToolName) || strings.Contains(rec.Body.String(), "call_abc") {
		t.Errorf("client saw internal retrieve turn: %s", rec.Body.String())
	}
	if comp.lastRetrieveHandle != "ccr_test123" {
		t.Errorf("RetrieveOriginal handle = %q, want stored handle ccr_test123", comp.lastRetrieveHandle)
	}
	row := sink.last(t)
	if row.InputTokens != 1500 || row.OutputTokens != 30 {
		t.Errorf("usage = (%d in, %d out), want (1500 in, 30 out)", row.InputTokens, row.OutputTokens)
	}
	if row.SavingsUSD != 0 {
		t.Errorf("retrieve continuation must not claim compression savings, got %v", row.SavingsUSD)
	}
}

const responsesRetrieveCallResp = `{"id":"resp_retrieve","object":"response","status":"completed","model":"gpt-5.5","output":[{"type":"reasoning","id":"rs_1","summary":[]},{"type":"function_call","id":"fc_1","call_id":"call_responses","name":"caveman_retrieve","arguments":"{\"handle\":\"ccr_test123\",\"query\":\"lazy dog\"}","status":"completed"}],"usage":{"input_tokens":500,"output_tokens":10,"total_tokens":510}}`

const responsesFinalResp = `{"id":"resp_final","object":"response","status":"completed","model":"gpt-5.5","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":1000,"output_tokens":20,"total_tokens":1020}}`

func TestCompressModeResponsesRetrieveRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, string(body))
		n := len(calls)
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		if n == 1 {
			_, _ = io.WriteString(w, responsesRetrieveCallResp)
			return
		}
		_, _ = io.WriteString(w, responsesFinalResp)
	}))
	defer upstream.Close()

	original := strings.Repeat("the quick brown fox jumps over the lazy dog and keeps running. ", 20)
	reqBody := `{"model":"gpt-5.5","stream":false,"input":[{"role":"user","content":[{"type":"input_text","text":` + strconv.Quote(original) + `}]}]}`
	sink := &captureSink{}
	comp := &stubCompressor{
		out:       []byte("X"),
		before:    200,
		after:     4,
		handle:    "ccr_test123",
		recovered: []byte(original),
	}
	srv := newCompressTestServer(t, upstream.URL, sink, comp)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("upstream calls = %d, want 2", len(calls))
	}
	if strings.Contains(calls[0], original) || !strings.Contains(calls[0], `"name":"caveman_retrieve"`) || strings.Contains(calls[0], `"function":`) {
		t.Fatalf("initial Responses request lacks compressed content or flat tool: %s", calls[0])
	}
	if !strings.Contains(calls[1], `"type":"function_call_output"`) || !strings.Contains(calls[1], `"call_id":"call_responses"`) || !strings.Contains(calls[1], "lazy dog") {
		t.Fatalf("Responses continuation lacks recovered output: %s", calls[1])
	}
	if strings.Contains(rec.Body.String(), retrieveToolName) || !strings.Contains(rec.Body.String(), `"text":"done"`) {
		t.Fatalf("client response leaked internal retrieve turn or lost output: %s", rec.Body.String())
	}
	if comp.lastRetrieveHandle != "ccr_test123" || comp.lastRetrieveQuery != "lazy dog" {
		t.Fatalf("retrieve = handle %q query %q", comp.lastRetrieveHandle, comp.lastRetrieveQuery)
	}
	row := sink.last(t)
	if row.InputTokens != 1500 || row.OutputTokens != 30 || row.SavingsUSD != 0 {
		t.Fatalf("usage/savings = (%d,%d,%v), want (1500,30,0)", row.InputTokens, row.OutputTokens, row.SavingsUSD)
	}
}

const geminiRetrieveCallResp = `{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"sig"},{"functionCall":{"id":"call_gemini","name":"caveman_retrieve","args":{"handle":"ccr_test123","query":"lazy dog"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":500,"candidatesTokenCount":10,"totalTokenCount":510}}`

const geminiFinalResp = `{"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1000,"candidatesTokenCount":20,"totalTokenCount":1020}}`

func TestCompressModeGeminiRetrieveRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, string(body))
		n := len(calls)
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		if n == 1 {
			_, _ = io.WriteString(w, geminiRetrieveCallResp)
			return
		}
		_, _ = io.WriteString(w, geminiFinalResp)
	}))
	defer upstream.Close()

	original := strings.Repeat("the quick brown fox jumps over the lazy dog and keeps running. ", 20)
	reqBody := `{"contents":[{"role":"user","parts":[{"text":` + strconv.Quote(original) + `}]}]}`
	sink := &captureSink{}
	comp := &stubCompressor{
		out:       []byte("X"),
		before:    200,
		after:     4,
		handle:    "ccr_test123",
		recovered: []byte(original),
	}
	srv := New(Config{
		Adapters:   []providers.Adapter{gemini.New(upstream.URL)},
		Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}},
		Creds:      stubCreds{key: "AIzaTestKey"},
		Sink:       sink,
		Compressor: comp,
		HTTPClient: &http.Client{},
	})
	const route = "/v1beta/models/gemini-2.5-flash:generateContent"
	req := httptest.NewRequest(http.MethodPost, route, strings.NewReader(reqBody))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("upstream calls = %d, want 2", len(calls))
	}
	if strings.Contains(calls[0], original) || !strings.Contains(calls[0], `"functionDeclarations"`) || !strings.Contains(calls[0], retrieveToolName) {
		t.Fatalf("initial Gemini request lacks compressed content or retrieve declaration: %s", calls[0])
	}
	if !strings.Contains(calls[1], `"functionResponse"`) || !strings.Contains(calls[1], `"id":"call_gemini"`) || !strings.Contains(calls[1], "lazy dog") || !strings.Contains(calls[1], `"thoughtSignature":"sig"`) {
		t.Fatalf("Gemini continuation lacks recovered output or thought signature: %s", calls[1])
	}
	if strings.Contains(rec.Body.String(), retrieveToolName) || !strings.Contains(rec.Body.String(), `"text":"done"`) {
		t.Fatalf("client response leaked internal retrieve turn or lost output: %s", rec.Body.String())
	}
	if comp.lastRetrieveHandle != "ccr_test123" || comp.lastRetrieveQuery != "lazy dog" {
		t.Fatalf("retrieve = handle %q query %q", comp.lastRetrieveHandle, comp.lastRetrieveQuery)
	}
	row := sink.last(t)
	if row.InputTokens != 1500 || row.OutputTokens != 30 || row.SavingsUSD != 0 {
		t.Fatalf("usage/savings = (%d,%d,%v), want (1500,30,0)", row.InputTokens, row.OutputTokens, row.SavingsUSD)
	}
}

const retrieveCallRespWithQuery = `{"id":"chatcmpl_r","object":"chat.completion","model":"gpt-5.5","choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_q","type":"function","function":{"name":"caveman_retrieve","arguments":"{\"handle\":\"ccr_test123\",\"query\":\"lazy dog\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":500,"completion_tokens":10}}`

// TestCompressModeRetrievePassesQuery proves the model's optional query argument is
// parsed from the retrieve tool call and handed to the retriever, so recovery can be
// narrowed to the relevant sections instead of always returning the whole original.
// The retrieve continuation still claims zero compression savings.
func TestCompressModeRetrievePassesQuery(t *testing.T) {
	var mu sync.Mutex
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		if n == 1 {
			_, _ = io.WriteString(w, retrieveCallRespWithQuery)
			return
		}
		_, _ = io.WriteString(w, chatRespBody)
	}))
	defer upstream.Close()

	sink := &captureSink{}
	comp := &stubCompressor{out: []byte("X"), before: 100, after: 40, handle: "ccr_test123", recovered: []byte(chatReqBody)}
	srv := newCompressTestServer(t, upstream.URL, sink, comp)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatReqBody))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if comp.lastRetrieveQuery != "lazy dog" {
		t.Errorf("RetrieveOriginal query = %q, want %q (model's query arg must be parsed and passed)", comp.lastRetrieveQuery, "lazy dog")
	}
	if comp.lastRetrieveHandle != "ccr_test123" {
		t.Errorf("RetrieveOriginal handle = %q, want stored handle ccr_test123 (never the model-supplied one)", comp.lastRetrieveHandle)
	}
	if row := sink.last(t); row.SavingsUSD != 0 {
		t.Errorf("retrieve continuation must not claim compression savings, got %v", row.SavingsUSD)
	}
}

var chatReqWithUserRetrieveTool = `{"model":"gpt-5.5","messages":[{"role":"user","content":"` + strings.Repeat("the quick brown fox jumps over the lazy dog and keeps running for a very long time. ", 10) + `"}],"tools":[{"type":"function","function":{"name":"caveman_retrieve","description":"user-owned tool","parameters":{"type":"object","properties":{"query":{"type":"string"}}}}}]}`

func TestCompressModeDoesNotHijackUserRetrieveTool(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, string(b))
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, retrieveCallResp)
	}))
	defer upstream.Close()

	sink := &captureSink{}
	comp := &stubCompressor{out: []byte("X"), before: 100, after: 40, handle: "ccr_test123", recovered: []byte(chatReqBody)}
	srv := newCompressTestServer(t, upstream.URL, sink, comp)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatReqWithUserRetrieveTool))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("upstream calls = %d, want 1 because user-owned tool must not be intercepted", len(calls))
	}
	if calls[0] != chatReqWithUserRetrieveTool {
		t.Errorf("user-owned retrieve tool must force compression pass-through:\n got %s\nwant %s", calls[0], chatReqWithUserRetrieveTool)
	}
	if comp.lastRetrieveHandle != "" {
		t.Errorf("proxy retrieved user-owned tool with handle %q", comp.lastRetrieveHandle)
	}
	if comp.calls != 0 || comp.storeCalls != 0 {
		t.Errorf("user-owned retrieve tool must preflight before compression/store: compress=%d store=%d", comp.calls, comp.storeCalls)
	}
	if !strings.Contains(rec.Body.String(), retrieveToolName) || !strings.Contains(rec.Body.String(), "call_abc") {
		t.Errorf("client should receive its own tool call unchanged, got: %s", rec.Body.String())
	}
	row := sink.last(t)
	if row.RecoveryHandle != "" || row.SavingsUSD != 0 || row.RawRequestSHA256 != row.TransformedRequestSHA256 {
		t.Errorf("user-owned retrieve tool must claim no compression: handle=%q savings=%v raw=%s transformed=%s", row.RecoveryHandle, row.SavingsUSD, row.RawRequestSHA256, row.TransformedRequestSHA256)
	}
}

func TestCompressModeRetrieverMissingPassesThrough(t *testing.T) {
	var gotUpstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotUpstreamBody = string(b)
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, chatRespBody)
	}))
	defer upstream.Close()

	sink := &captureSink{}
	comp := &storeOnlyCompressor{out: []byte("X"), before: 100, after: 40, handle: "ccr_test123"}
	srv := newCompressTestServer(t, upstream.URL, sink, comp)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatReqBody))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(gotUpstreamBody, retrieveToolName) {
		t.Fatalf("upstream request advertised unavailable internal retrieve tool: %s", gotUpstreamBody)
	}
	if gotUpstreamBody != chatReqBody {
		t.Errorf("missing retriever must forward original bytes:\n got %q\nwant %q", gotUpstreamBody, chatReqBody)
	}
	if comp.calls != 0 || comp.storeCalls != 0 {
		t.Errorf("missing retriever must preflight before compression/store: compress=%d store=%d", comp.calls, comp.storeCalls)
	}
	row := sink.last(t)
	if row.RecoveryHandle != "" || row.SavingsUSD != 0 || row.RawRequestSHA256 != row.TransformedRequestSHA256 {
		t.Errorf("missing retriever must claim nothing: handle=%q savings=%v", row.RecoveryHandle, row.SavingsUSD)
	}
}

var chatReqStream = `{"model":"gpt-5.5","stream":true,"messages":[{"role":"user","content":"` + strings.Repeat("the quick brown fox jumps over the lazy dog and keeps running for a very long time. ", 10) + `"}]}`

func TestCompressModeStreamPassesThrough(t *testing.T) {
	var gotUpstreamBody string
	upstream := echoUpstream(t, &gotUpstreamBody)
	defer upstream.Close()

	sink := &captureSink{}
	comp := &stubCompressor{out: []byte("X"), before: 100, after: 40, handle: "ccr_test123", recovered: []byte(chatReqStream)}
	srv := newCompressTestServer(t, upstream.URL, sink, comp)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatReqStream))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotUpstreamBody != chatReqStream {
		t.Errorf("streaming compression must pass through original bytes:\n got %s\nwant %s", gotUpstreamBody, chatReqStream)
	}
	if comp.calls != 0 || comp.storeCalls != 0 {
		t.Errorf("streaming requests must preflight before compression/store: compress=%d store=%d", comp.calls, comp.storeCalls)
	}
	row := sink.last(t)
	if row.RecoveryHandle != "" || row.SavingsUSD != 0 || row.RawRequestSHA256 != row.TransformedRequestSHA256 {
		t.Errorf("streaming pass-through must claim no compression: handle=%q savings=%v", row.RecoveryHandle, row.SavingsUSD)
	}
}

func newMCPRecoveryTestServer(t *testing.T, upstream string, sink TelemetrySink, comp Compressor) *Server {
	t.Helper()
	return New(Config{
		Adapters:       []providers.Adapter{openai.New(upstream)},
		Auth:           stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}},
		Creds:          stubCreds{key: "sk-byok"},
		Sink:           sink,
		Compressor:     comp,
		RecoveryViaMCP: true,
		HTTPClient:     &http.Client{},
	})
}

// TestMCPRecoveryCompressesStreamWithMarkerNoServerLoop proves the headroom-style
// streaming path: under MCP recovery a STREAMING request is compressed (it is not
// passed through), the recovery handle is disclosed as a content marker instead of
// an injected tool, the proxy runs no server-side retrieve loop, and — because
// retrieves now happen off-proxy via the agent's MCP tool — it claims zero savings.
func TestMCPRecoveryCompressesStreamWithMarkerNoServerLoop(t *testing.T) {
	var gotUpstreamBody string
	upstream := echoUpstream(t, &gotUpstreamBody)
	defer upstream.Close()

	sink := &captureSink{}
	comp := &stubCompressor{out: []byte("X"), before: 100, after: 40, handle: "ccr_test123", recovered: []byte(chatReqStream)}
	srv := newMCPRecoveryTestServer(t, upstream.URL, sink, comp)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatReqStream))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if comp.calls == 0 || comp.storeCalls == 0 {
		t.Fatalf("MCP recovery must compress + store a streaming request: compress=%d store=%d", comp.calls, comp.storeCalls)
	}
	if strings.Contains(gotUpstreamBody, "lazy dog") || !strings.Contains(gotUpstreamBody, `"content":"X`) {
		t.Errorf("upstream should get the compressed body, got: %s", gotUpstreamBody)
	}
	if strings.Contains(gotUpstreamBody, retrieveToolName) || !strings.Contains(gotUpstreamBody, "<<ccr:ccr_test123>>") {
		t.Errorf("MCP recovery must use only in-block CCR marker, got: %s", gotUpstreamBody)
	}
	if strings.Contains(gotUpstreamBody, `"tools"`) {
		t.Errorf("MCP recovery must NOT inject a tool (the agent owns caveman_retrieve): %s", gotUpstreamBody)
	}
	if comp.lastRetrieveHandle != "" {
		t.Errorf("MCP recovery must not run the server-side retrieve loop, but RetrieveOriginal was called with %q", comp.lastRetrieveHandle)
	}
	row := sink.last(t)
	if row.RecoveryHandle != "ccr_test123" {
		t.Errorf("handle must still be disclosed in telemetry, got %q", row.RecoveryHandle)
	}
	if row.SavingsUSD != 0 {
		t.Errorf("MCP recovery claims no savings (retrieves are off-proxy), got %v", row.SavingsUSD)
	}
	if row.RawRequestSHA256 == row.TransformedRequestSHA256 {
		t.Error("MCP recovery reshaped the body, so raw and transformed hashes must differ")
	}
}

// TestMCPRecoveryCompressesEvenWhenRetrieveToolPresent proves MCP recovery does not
// bail on a pre-existing caveman_retrieve tool (which, under wrap+MCP, is the agent's
// own tool — expected, not a collision): it still compresses and never injects a
// second one or retrieves server-side.
func TestMCPRecoveryCompressesEvenWhenRetrieveToolPresent(t *testing.T) {
	var gotUpstreamBody string
	upstream := echoUpstream(t, &gotUpstreamBody)
	defer upstream.Close()

	sink := &captureSink{}
	comp := &stubCompressor{out: []byte("X"), before: 100, after: 40, handle: "ccr_test123", recovered: []byte(chatReqWithUserRetrieveTool)}
	srv := newMCPRecoveryTestServer(t, upstream.URL, sink, comp)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatReqWithUserRetrieveTool))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if comp.calls == 0 {
		t.Fatal("MCP recovery must compress even when caveman_retrieve is already present")
	}
	if !strings.Contains(gotUpstreamBody, `"content":"X`) || !strings.Contains(gotUpstreamBody, "<<ccr:ccr_test123>>") {
		t.Errorf("expected compressed body + recovery marker, got: %s", gotUpstreamBody)
	}
	if comp.lastRetrieveHandle != "" {
		t.Errorf("MCP recovery must not retrieve server-side, got handle %q", comp.lastRetrieveHandle)
	}
	if row := sink.last(t); row.SavingsUSD != 0 {
		t.Errorf("MCP recovery claims no savings, got %v", row.SavingsUSD)
	}
}

func TestStripRetrieveCallNormalizesOpenAINullContent(t *testing.T) {
	cleaned, ok := stripRetrieveCall("openai", "/v1/chat/completions", []byte(retrieveCallResp))
	if !ok {
		t.Fatal("stripRetrieveCall did not remove internal retrieve tool")
	}
	if strings.Contains(string(cleaned), retrieveToolName) || strings.Contains(string(cleaned), "tool_calls") {
		t.Fatalf("cleaned response still contains retrieve tool call: %s", string(cleaned))
	}
	if !strings.Contains(string(cleaned), `"content":""`) {
		t.Fatalf("cleaned response should replace null content with empty string: %s", string(cleaned))
	}
}

func TestRetrieveLoopReadErrorReturnsGatewayFailure(t *testing.T) {
	upstreamURL, err := url.Parse("http://upstream.invalid/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{compressor: &stubCompressor{recovered: []byte(chatReqBody)}}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Length": []string{"999"}},
		Body:       errReadCloser{},
	}
	out, _, _, _ := srv.runRetrieveLoop(context.Background(), http.DefaultClient, upstreamURL, http.Header{}, []byte(chatReqBody), []byte(chatReqBody), []string{"ccr_test123"}, resp, openai.New("http://upstream.invalid"), "openai", "/v1/chat/completions", "req_test")
	defer out.Body.Close()
	b, _ := io.ReadAll(out.Body)

	if out.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %s)", out.StatusCode, string(b))
	}
	if out.Header.Get("Content-Length") != "" {
		t.Fatalf("synthetic retrieve failure should not preserve stale Content-Length: %s", out.Header.Get("Content-Length"))
	}
	if !strings.Contains(string(b), "cave_retrieve_response_read_failed") {
		t.Fatalf("body missing read-failed error: %s", string(b))
	}
}

func TestRetrieveLoopRejectsHandleOutsideCurrentRequest(t *testing.T) {
	upstreamURL, err := url.Parse("http://upstream.invalid/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	comp := &stubCompressor{recovered: []byte("secret from another request")}
	srv := &Server{compressor: comp}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(retrieveCallResp)),
	}
	out, _, retrieved, _ := srv.runRetrieveLoop(context.Background(), http.DefaultClient, upstreamURL, http.Header{}, []byte(chatReqBody), []byte(chatReqBody), []string{"ccr_current"}, resp, openai.New("http://upstream.invalid"), "openai", "/v1/chat/completions", "req_test")
	defer out.Body.Close()
	body, _ := io.ReadAll(out.Body)
	if !retrieved {
		t.Fatal("retrieve attempt should remain visible in telemetry")
	}
	if comp.lastRetrieveHandle != "" {
		t.Fatalf("cross-request handle reached CCR store: %q", comp.lastRetrieveHandle)
	}
	if strings.Contains(string(body), retrieveToolName) {
		t.Fatalf("rejected internal retrieve call leaked to client: %s", body)
	}
}

func TestRetrieveLoopLargeResponseFallsBackByteExact(t *testing.T) {
	t.Setenv("CAVE_MAX_RETRIEVE_RESPONSE_BYTES", "32")
	upstreamURL, err := url.Parse("http://upstream.invalid/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"id":"large","choices":[{"message":{"content":"` + strings.Repeat("x", 200) + `"}}]}`)
	srv := &Server{compressor: &stubCompressor{recovered: []byte(chatReqBody)}}
	originalRequest := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"original"}]}`)
	var replayed []byte
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		replayed, _ = io.ReadAll(req.Body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"replayed","choices":[]}`)),
		}, nil
	})}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Length": []string{strconv.Itoa(len(original))}},
		Body:       io.NopCloser(bytes.NewReader(original)),
	}
	out, _, retrieved, replayedOriginal := srv.runRetrieveLoop(context.Background(), client, upstreamURL, http.Header{}, []byte(chatReqBody), originalRequest, []string{"ccr_test123"}, resp, openai.New("http://upstream.invalid"), "openai", "/v1/chat/completions", "req_test")
	defer out.Body.Close()
	got, err := io.ReadAll(out.Body)
	if err != nil {
		t.Fatal(err)
	}
	if out.StatusCode != http.StatusOK || retrieved || !replayedOriginal || !bytes.Equal(replayed, originalRequest) || !bytes.Contains(got, []byte(`"replayed"`)) {
		t.Fatalf("large response fallback did not replay original: status=%d retrieved=%v original=%v replayed=%s body=%s", out.StatusCode, retrieved, replayedOriginal, replayed, got)
	}
}

func TestLargeRetrieveResponseReplaysOriginalAndClearsClaims(t *testing.T) {
	t.Setenv("CAVE_MAX_RETRIEVE_RESPONSE_BYTES", "128")
	large := `{"id":"large","choices":[{"message":{"content":"` + strings.Repeat("x", 512) + `"}}]}`
	rt := &captureTransport{responses: []string{large, chatRespBody}}
	sink := &captureSink{}
	comp := &stubCompressor{out: []byte("X"), before: 100, after: 40, handle: "ccr_test123", recovered: []byte(chatReqBody)}
	srv := New(Config{
		Adapters:   []providers.Adapter{openai.New("https://upstream.test")},
		Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}},
		Creds:      stubCreds{key: "sk-byok"},
		Sink:       sink,
		Compressor: comp,
		HTTPClient: &http.Client{Transport: rt},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatReqBody))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || len(rt.bodies) != 2 {
		t.Fatalf("status/calls = %d/%d, want 200/2", rec.Code, len(rt.bodies))
	}
	if !bytes.Contains(rt.bodies[0], []byte(retrieveToolName)) || !bytes.Contains(rt.bodies[0], []byte("<<ccr:ccr_test123>>")) {
		t.Fatalf("first attempt did not exercise retrieve path: %s", rt.bodies[0])
	}
	if !bytes.Equal(rt.bodies[1], []byte(chatReqBody)) {
		t.Fatalf("fallback must replay exact original request:\n got %s\nwant %s", rt.bodies[1], chatReqBody)
	}
	row := sink.last(t)
	if row.RecoveryHandle != "" || len(row.OptimizationIDs) != 0 || row.RawRequestSHA256 != row.TransformedRequestSHA256 {
		t.Fatalf("original replay retained transform claims: %+v", row)
	}
	if rec.Header().Get("x-caveman-recovery-handle") != "" {
		t.Fatalf("original replay retained compression disclosure: %v", rec.Header())
	}
}

// TestCompressModeNilCompressorPassesThrough proves compress mode with no engine
// wired is a pure pass-through.
func TestCompressModeNilCompressorPassesThrough(t *testing.T) {
	var gotUpstreamBody string
	upstream := echoUpstream(t, &gotUpstreamBody)
	defer upstream.Close()

	sink := &captureSink{}
	srv := newCompressTestServer(t, upstream.URL, sink, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatReqBody))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotUpstreamBody != chatReqBody {
		t.Errorf("nil compressor must forward original bytes:\n got %q\nwant %q", gotUpstreamBody, chatReqBody)
	}
	if got := rec.Header().Get("x-caveman-recovery-handle"); got != "" {
		t.Errorf("recovery handle header = %q, want empty (no compression occurred)", got)
	}
	row := sink.last(t)
	if row.RecoveryHandle != "" || row.SavingsUSD != 0 {
		t.Errorf("nil compressor recorded compression: handle=%q savings=%v", row.RecoveryHandle, row.SavingsUSD)
	}
	if row.RawRequestSHA256 != row.TransformedRequestSHA256 {
		t.Error("nil compressor must not alter bytes: raw and transformed hashes must match")
	}
}

// TestCompressModeUnstorableOriginalFailsClosed proves the CCR-or-pass-through
// contract: if the original cannot be stored for recovery, the proxy forwards the
// ORIGINAL bytes rather than send irreversibly-compressed bytes.
func TestCompressModeUnstorableOriginalFailsClosed(t *testing.T) {
	var gotUpstreamBody string
	upstream := echoUpstream(t, &gotUpstreamBody)
	defer upstream.Close()

	sink := &captureSink{}
	comp := &stubCompressor{out: []byte("X"), before: 100, after: 40, storeErr: errors.New("disk full")}
	srv := newCompressTestServer(t, upstream.URL, sink, comp)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatReqBody))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (compression failure must fail open)", rec.Code)
	}
	if !strings.Contains(gotUpstreamBody, retrieveToolName) || strings.Contains(gotUpstreamBody, "<<ccr:") {
		t.Errorf("unstorable original may only receive static retrieve tool, got:\n%s", gotUpstreamBody)
	}
	row := sink.last(t)
	if row.RecoveryHandle != "" || row.SavingsUSD != 0 {
		t.Errorf("failed compression must claim nothing: handle=%q savings=%v", row.RecoveryHandle, row.SavingsUSD)
	}
	if row.RawRequestSHA256 == row.TransformedRequestSHA256 {
		t.Error("static retrieve tool injection should make transformed hash differ")
	}
}

// TestCompressModeNoReductionPassesThrough proves that when the engine does not
// actually shrink the content (no-op), compress mode passes the body through and
// claims no saving.
func TestCompressModeNoReductionPassesThrough(t *testing.T) {
	var gotUpstreamBody string
	upstream := echoUpstream(t, &gotUpstreamBody)
	defer upstream.Close()

	sink := &captureSink{}
	comp := &stubCompressor{} // out nil → echoes the segment, before == after
	srv := newCompressTestServer(t, upstream.URL, sink, comp)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatReqBody))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if comp.calls == 0 {
		t.Fatal("compressor should have been invoked even when it does not shrink")
	}
	if !strings.Contains(gotUpstreamBody, retrieveToolName) || strings.Contains(gotUpstreamBody, "<<ccr:") {
		t.Errorf("no-reduction compress may only receive static retrieve tool, got:\n%s", gotUpstreamBody)
	}
	row := sink.last(t)
	if row.RecoveryHandle != "" || row.SavingsUSD != 0 {
		t.Errorf("no-reduction compress must claim nothing: handle=%q savings=%v", row.RecoveryHandle, row.SavingsUSD)
	}
}

// TestTransformRejected4xxFailsOpenWithOriginalBytes proves the byte-safe
// fail-open retry: when the upstream rejects a request whose bytes the proxy
// modified (Anthropic answers subscription-OAuth requests with a changed system
// prefix with an opaque 429 — measured 2026-07-07), the proxy retries ONCE with
// the original bytes, returns that response, and claims no optimization.
func TestTransformRejected4xxFailsOpenWithOriginalBytes(t *testing.T) {
	sink := &captureSink{}
	comp := &stubCompressor{out: []byte("X"), before: 100, after: 40, handle: "ccr_test123", recovered: []byte(chatReqStream)}
	rt := &captureTransport{
		statuses:  []int{http.StatusTooManyRequests, http.StatusOK},
		responses: []string{`{"type":"error","error":{"type":"rate_limit_error","message":"Error"}}`, chatRespBody},
	}
	srv := New(Config{
		Adapters:       []providers.Adapter{openai.New("https://upstream.test")},
		Auth:           stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}},
		Creds:          stubCreds{key: "sk-byok"},
		Sink:           sink,
		Compressor:     comp,
		RecoveryViaMCP: true,
		HTTPClient:     &http.Client{Transport: rt},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatReqStream))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after fail-open retry (body %s)", rec.Code, rec.Body.String())
	}
	if len(rt.bodies) != 2 {
		t.Fatalf("upstream calls = %d, want 2 (rejected transform, then original)", len(rt.bodies))
	}
	bodies := []string{string(rt.bodies[0]), string(rt.bodies[1])}
	if !strings.Contains(bodies[0], `"content":"X`) || !strings.Contains(bodies[0], "<<ccr:ccr_test123>>") {
		t.Errorf("first attempt should carry the transformed body, got: %s", bodies[0])
	}
	if bodies[1] != chatReqStream {
		t.Errorf("retry must resend the ORIGINAL bytes:\n got %s\nwant %s", bodies[1], chatReqStream)
	}
	row := sink.last(t)
	if row.StatusCode != http.StatusOK {
		t.Errorf("telemetry status = %d, want 200", row.StatusCode)
	}
	if row.RecoveryHandle != "" || row.SavingsUSD != 0 || len(row.OptimizationIDs) != 0 {
		t.Errorf("fail-open retry must claim no optimization: handle=%q savings=%v ids=%v", row.RecoveryHandle, row.SavingsUSD, row.OptimizationIDs)
	}
	if row.RawRequestSHA256 != row.TransformedRequestSHA256 {
		t.Error("after fail-open retry the recorded hashes must match (original bytes were sent)")
	}
}

// TestJoinRecoveryHandlesBounded pins the row bound: every turn re-substitutes every
// earlier block, so the disclosed handle list must dedupe and stay short instead of
// growing one entry per conversation turn in every row.
func TestJoinRecoveryHandlesBounded(t *testing.T) {
	if got := joinRecoveryHandles([]string{"ccr_a", "ccr_b", "ccr_a"}); got != "ccr_a,ccr_b" {
		t.Errorf("duplicate handles must collapse, got %q", got)
	}
	many := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		many = append(many, "ccr_"+strconv.Itoa(i))
	}
	got := joinRecoveryHandles(many)
	parts := strings.Split(got, ",")
	if len(parts) != recoveryHandleListMax+1 {
		t.Fatalf("handle list = %q (%d parts), want %d handles plus an elision token", got, len(parts), recoveryHandleListMax)
	}
	if parts[0] != "+32" {
		t.Errorf("elision token = %q, want +32 — a truncated list must say how much it dropped", parts[0])
	}
	// The newest block (the one this turn actually compressed) is last and must survive.
	if parts[len(parts)-1] != "ccr_39" {
		t.Errorf("last handle = %q, want ccr_39", parts[len(parts)-1])
	}
}
