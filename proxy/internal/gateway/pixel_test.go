package gateway

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/gemini"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

type pixelStoreCompressor struct {
	storeCalls   int
	segmentCalls int
	handle       string
	storeErr     error
	original     []byte
}

func (c *pixelStoreCompressor) CompressSegment(seg []byte) ([]byte, int, int) {
	c.segmentCalls++
	return seg, len(seg), len(seg)
}

func (c *pixelStoreCompressor) StoreOriginal(body []byte) (string, error) {
	c.storeCalls++
	c.original = append([]byte(nil), body...)
	if c.storeErr != nil {
		return "", c.storeErr
	}
	if c.handle == "" {
		return "ccr_pixel", nil
	}
	return c.handle, nil
}

func newPixelTestServer(t *testing.T, upstream string, mode string, adapter providers.Adapter, sink TelemetrySink, comp Compressor, recoveryViaMCP bool) *Server {
	t.Helper()
	return New(Config{
		Adapters:       []providers.Adapter{adapter},
		Auth:           stubAuth{rc: RequestContext{Label: "local", RuntimeMode: mode}},
		Creds:          stubCreds{key: "sk-byok"},
		Sink:           sink,
		Compressor:     comp,
		RecoveryViaMCP: recoveryViaMCP,
		HTTPClient:     &http.Client{},
	})
}

func capturePixelUpstream(t *testing.T, response string) (*httptest.Server, *[]byte) {
	t.Helper()
	got := []byte(nil)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got[:0], b...)
		w.Header().Set("content-type", "application/json")
		w.Header().Set("x-request-id", "up-pixel")
		_, _ = io.WriteString(w, response)
	}))
	return upstream, &got
}

func TestPixelModeAnthropicImagesAndStoresOriginal(t *testing.T) {
	t.Setenv("CAVE_PIXEL_MODELS", "")
	body := anthropicPixelBody("claude-fable-5", false)
	upstream, got := capturePixelUpstream(t, anthropicPixelResponse("claude-fable-5"))
	defer upstream.Close()

	sink := &captureSink{}
	comp := &pixelStoreCompressor{handle: "ccr_pixel_anthropic"}
	srv := newPixelTestServer(t, upstream.URL, "pixel", anthropic.New(upstream.URL), sink, comp, false)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	upstreamBody := string(*got)
	if !strings.Contains(upstreamBody, `"type":"image"`) {
		t.Fatalf("upstream body missing image blocks: %s", upstreamBody)
	}
	assertPixelStaticSlabIntact(t, upstreamBody, "STATIC_CONTEXT_LINE", 1300)
	if strings.Contains(upstreamBody, "TOOL_RESULT_A row with values") {
		t.Fatalf("live-zone tool result should be imaged, got: %s", upstreamBody)
	}
	if got := rec.Header().Get("x-caveman-recovery-handle"); got != "ccr_pixel_anthropic" {
		t.Fatalf("recovery handle = %q, want ccr_pixel_anthropic", got)
	}
	if rec.Header().Get("x-caveman-tokens-before") == "" || rec.Header().Get("x-caveman-tokens-after") == "" {
		t.Fatalf("token estimate headers missing: before=%q after=%q", rec.Header().Get("x-caveman-tokens-before"), rec.Header().Get("x-caveman-tokens-after"))
	}
	if got := rec.Header().Get("x-cave-optimization"); got != pixelOptimizerID {
		t.Fatalf("x-cave-optimization = %q, want %s", got, pixelOptimizerID)
	}
	if comp.storeCalls != 1 || comp.segmentCalls != 0 {
		t.Fatalf("pixel should store original once and not call CompressSegment: store=%d segment=%d", comp.storeCalls, comp.segmentCalls)
	}
	if !bytes.Equal(comp.original, body) {
		t.Fatal("CCR store did not receive byte-exact original")
	}
	row := sink.last(t)
	if row.RecoveryHandle != "ccr_pixel_anthropic" || !containsStr(row.OptimizationIDs, pixelOptimizerID) {
		t.Fatalf("row missing pixel recovery/optimizer: %+v", row)
	}
	if row.SavingsUSD != 0 || row.Basis != "inferred" {
		t.Fatalf("pixel must stay inferred-only and book no savings: basis=%s savings=%v", row.Basis, row.SavingsUSD)
	}
}

func TestPixelModeDisallowedModelPassesThrough(t *testing.T) {
	t.Setenv("CAVE_PIXEL_MODELS", "")
	body := anthropicPixelBody("claude-opus-4-8", false)
	upstream, got := capturePixelUpstream(t, anthropicPixelResponse("claude-opus-4-8"))
	defer upstream.Close()

	sink := &captureSink{}
	comp := &pixelStoreCompressor{handle: "ccr_disallowed"}
	srv := newPixelTestServer(t, upstream.URL, "pixel", anthropic.New(upstream.URL), sink, comp, false)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !bytes.Equal(*got, body) {
		t.Fatalf("disallowed model changed bytes:\n got %s\nwant %s", string(*got), string(body))
	}
	if rec.Header().Get("x-caveman-recovery-handle") != "" || rec.Header().Get("x-caveman-tokens-before") != "" {
		t.Fatalf("disallowed model set compression headers")
	}
	if comp.storeCalls != 0 {
		t.Fatalf("disallowed model stored original %d times", comp.storeCalls)
	}
	if row := sink.last(t); row.RawRequestSHA256 != row.TransformedRequestSHA256 || row.RecoveryHandle != "" {
		t.Fatalf("disallowed row should be byte-identical/no handle: %+v", row)
	}
}

func TestPixelModeRecordPassesThrough(t *testing.T) {
	t.Setenv("CAVE_PIXEL_MODELS", "")
	body := anthropicPixelBody("claude-fable-5", false)
	upstream, got := capturePixelUpstream(t, anthropicPixelResponse("claude-fable-5"))
	defer upstream.Close()

	sink := &captureSink{}
	comp := &pixelStoreCompressor{handle: "ccr_record"}
	srv := newPixelTestServer(t, upstream.URL, "record", anthropic.New(upstream.URL), sink, comp, false)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !bytes.Equal(*got, body) {
		t.Fatalf("record mode changed bytes")
	}
	if comp.storeCalls != 0 || rec.Header().Get("x-caveman-recovery-handle") != "" {
		t.Fatalf("record mode touched pixel recovery: store=%d handle=%q", comp.storeCalls, rec.Header().Get("x-caveman-recovery-handle"))
	}
}

func TestExplicitPassThroughSuppressesPixelMode(t *testing.T) {
	t.Setenv("CAVE_PIXEL_MODELS", "")
	body := anthropicPixelBody("claude-fable-5", false)
	upstream, got := capturePixelUpstream(t, anthropicPixelResponse("claude-fable-5"))
	defer upstream.Close()
	sink := &captureSink{}
	comp := &pixelStoreCompressor{handle: "ccr_pixel_forbidden"}
	srv := newPixelTestServer(t, upstream.URL, "pixel", anthropic.New(upstream.URL), sink, comp, false)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	req.Header.Set("x-cave-transforms", "caveman.pass-through.v1")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !bytes.Equal(*got, body) || comp.storeCalls != 0 {
		t.Fatalf("explicit pass-through ran pixel: status=%d stores=%d body=%s", rec.Code, comp.storeCalls, string(*got))
	}
	if row := sink.last(t); row.RawRequestSHA256 != row.TransformedRequestSHA256 || row.RecoveryHandle != "" {
		t.Fatalf("explicit pass-through recorded pixel: %+v", row)
	}
}

func TestPixelModeSubscriptionDefaultPassesThrough(t *testing.T) {
	t.Setenv("CAVE_PIXEL_MODELS", "")
	body := anthropicPixelBody("claude-fable-5", false)
	rt := &captureTransport{responses: []string{anthropicPixelResponse("claude-fable-5")}}
	sink := &captureSink{}
	comp := &pixelStoreCompressor{handle: "ccr_subscription_pixel"}
	srv := New(Config{
		Adapters:   []providers.Adapter{anthropic.New("https://upstream.test")},
		Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "pixel"}},
		Creds:      passthroughTestCreds{},
		Sink:       sink,
		Compressor: comp,
		HTTPClient: &http.Client{Transport: rt},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	req.Header.Set("user-agent", "codex-cli/0.1")
	req.Header.Set("authorization", "Bearer sk-ant-oat-test")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(rt.bodies) != 1 || !bytes.Equal(rt.bodies[0], body) {
		t.Fatalf("subscription pixel default must pass through byte-identically:\n got %s\nwant %s", rt.bodies[0], body)
	}
	if comp.storeCalls != 0 || rec.Header().Get("x-caveman-recovery-handle") != "" || rec.Header().Get("x-cave-optimization") != "none" {
		t.Fatalf("subscription pixel default must not store or claim optimization: store=%d handle=%q opt=%q", comp.storeCalls, rec.Header().Get("x-caveman-recovery-handle"), rec.Header().Get("x-cave-optimization"))
	}
	if row := sink.last(t); row.RawRequestSHA256 != row.TransformedRequestSHA256 || row.RecoveryHandle != "" {
		t.Fatalf("subscription pixel row should be byte-identical/no handle: %+v", row)
	}
}

func TestPixelModeStoreFailurePassesThrough(t *testing.T) {
	t.Setenv("CAVE_PIXEL_MODELS", "")
	body := anthropicPixelBody("claude-fable-5", false)
	upstream, got := capturePixelUpstream(t, anthropicPixelResponse("claude-fable-5"))
	defer upstream.Close()

	sink := &captureSink{}
	comp := &pixelStoreCompressor{storeErr: errors.New("store failed")}
	srv := newPixelTestServer(t, upstream.URL, "pixel", anthropic.New(upstream.URL), sink, comp, false)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !bytes.Equal(*got, body) {
		t.Fatalf("store failure changed bytes:\n got %s\nwant %s", string(*got), string(body))
	}
	if comp.storeCalls != 1 {
		t.Fatalf("store calls = %d, want 1", comp.storeCalls)
	}
	if rec.Header().Get("x-caveman-recovery-handle") != "" || rec.Header().Get("x-cave-optimization") != "none" {
		t.Fatalf("store failure claimed compression: handle=%q opt=%q", rec.Header().Get("x-caveman-recovery-handle"), rec.Header().Get("x-cave-optimization"))
	}
}

func TestPixelModeMalformedJSONPassesThrough(t *testing.T) {
	t.Setenv("CAVE_PIXEL_MODELS", "gemini-bad")
	body := []byte(`{"contents":[`)
	upstream, got := capturePixelUpstream(t, geminiPixelResponse())
	defer upstream.Close()

	sink := &captureSink{}
	comp := &pixelStoreCompressor{handle: "ccr_bad"}
	srv := newPixelTestServer(t, upstream.URL, "pixel", gemini.New(upstream.URL), sink, comp, false)

	req := httptest.NewRequest(http.MethodPost, "/gemini/v1beta/models/gemini-bad:generateContent", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !bytes.Equal(*got, body) {
		t.Fatalf("malformed JSON changed bytes")
	}
	if comp.storeCalls != 0 || rec.Header().Get("x-caveman-recovery-handle") != "" {
		t.Fatalf("malformed JSON should not store/claim: store=%d handle=%q", comp.storeCalls, rec.Header().Get("x-caveman-recovery-handle"))
	}
}

func TestPixelModeGeminiImagesAllowedModel(t *testing.T) {
	t.Setenv("CAVE_PIXEL_MODELS", "gemini-allow")
	body := geminiPixelBody()
	upstream, got := capturePixelUpstream(t, geminiPixelResponse())
	defer upstream.Close()

	sink := &captureSink{}
	comp := &pixelStoreCompressor{handle: "ccr_pixel_gemini"}
	srv := newPixelTestServer(t, upstream.URL, "pixel", gemini.New(upstream.URL), sink, comp, false)

	req := httptest.NewRequest(http.MethodPost, "/gemini/v1beta/models/gemini-allow:generateContent", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	upstreamBody := string(*got)
	if !bytes.Equal(*got, body) {
		t.Fatalf("Gemini pixel path should pass through until a Gemini live-zone walker exists:\n got %s\nwant %s", upstreamBody, string(body))
	}
	if got := rec.Header().Get("x-caveman-recovery-handle"); got != "" {
		t.Fatalf("Gemini pass-through should not set recovery handle, got %q", got)
	}
}

func TestPixelModeOpenAIImagesGPT56(t *testing.T) {
	t.Setenv("CAVE_PIXEL_MODELS", "")
	t.Setenv("CAVE_PIXEL_GPT_PROFILES", "")
	body := openAIPixelBody()
	upstream, got := capturePixelUpstream(t, openAIPixelResponse())
	defer upstream.Close()

	sink := &captureSink{}
	comp := &pixelStoreCompressor{handle: "ccr_pixel_openai"}
	srv := newPixelTestServer(t, upstream.URL, "pixel", openai.New(upstream.URL), sink, comp, false)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	upstreamBody := string(*got)
	if !strings.Contains(upstreamBody, `"type":"image_url"`) || !strings.Contains(upstreamBody, `data:image/png;base64,`) {
		t.Fatalf("OpenAI upstream body missing image_url parts: %s", upstreamBody)
	}
	assertPixelStaticSlabIntact(t, upstreamBody, "OPENAI_SYSTEM_CONTEXT", 3000)
	if got := rec.Header().Get("x-caveman-recovery-handle"); got != "ccr_pixel_openai" {
		t.Fatalf("handle = %q, want ccr_pixel_openai", got)
	}
}

func TestPixelModeOpenAIResponsesStringInput(t *testing.T) {
	t.Setenv("CAVE_PIXEL_MODELS", "")
	body := []byte(`{"model":"gpt-5.6","input":"` + strings.Repeat("RESPONSES_STRING_INPUT detailed tool output.\\n", 2600) + `"}`)
	upstream, got := capturePixelUpstream(t, openAIPixelResponse())
	defer upstream.Close()
	comp := &pixelStoreCompressor{handle: "ccr_pixel_responses_string"}
	srv := newPixelTestServer(t, upstream.URL, "pixel", openai.New(upstream.URL), &captureSink{}, comp, false)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(*got, []byte(`"type":"input_image"`)) || bytes.Contains(*got, []byte("RESPONSES_STRING_INPUT")) {
		t.Fatalf("Responses string input was not transformed into input_image: %s", *got)
	}
	if rec.Header().Get("x-caveman-recovery-handle") != "ccr_pixel_responses_string" || !bytes.Equal(comp.original, body) {
		t.Fatal("Responses transform did not durably bind byte-exact original")
	}
}

func TestPixelModeOpenAIResponsesUserAndToolOutput(t *testing.T) {
	t.Setenv("CAVE_PIXEL_MODELS", "")
	body := []byte(`{"model":"gpt-5.6","input":[` +
		`{"role":"user","content":[{"type":"input_text","text":"` + strings.Repeat("RESPONSES_USER_INPUT detailed context.\\n", 2600) + `"}]},` +
		`{"type":"function_call_output","call_id":"call_1","output":"` + strings.Repeat("RESPONSES_TOOL_OUTPUT detailed rows.\\n", 2600) + `"}` +
		`]}`)
	upstream, got := capturePixelUpstream(t, openAIPixelResponse())
	defer upstream.Close()
	comp := &pixelStoreCompressor{handle: "ccr_pixel_responses_items"}
	srv := newPixelTestServer(t, upstream.URL, "pixel", openai.New(upstream.URL), &captureSink{}, comp, false)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if gotCount := bytes.Count(*got, []byte(`"type":"input_image"`)); gotCount < 2 {
		t.Fatalf("Responses user/tool live zones emitted %d images, want both transformed: %s", gotCount, *got)
	}
	if bytes.Contains(*got, []byte("RESPONSES_USER_INPUT")) || bytes.Contains(*got, []byte("RESPONSES_TOOL_OUTPUT")) {
		t.Fatalf("Responses live text survived transformed request: %s", *got)
	}
}

func TestPixelModeStreamingAnthropicMCPAndPlain(t *testing.T) {
	t.Setenv("CAVE_PIXEL_MODELS", "")
	body := anthropicPixelBody("claude-fable-5", true)

	upstreamMCP, gotMCP := capturePixelUpstream(t, anthropicPixelResponse("claude-fable-5"))
	defer upstreamMCP.Close()
	sinkMCP := &captureSink{}
	compMCP := &pixelStoreCompressor{handle: "ccr_pixel_mcp"}
	srvMCP := newPixelTestServer(t, upstreamMCP.URL, "pixel", anthropic.New(upstreamMCP.URL), sinkMCP, compMCP, true)

	reqMCP := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	recMCP := httptest.NewRecorder()
	srvMCP.Handler().ServeHTTP(recMCP, reqMCP)
	if recMCP.Code != http.StatusOK {
		t.Fatalf("MCP status = %d, want 200", recMCP.Code)
	}
	mcpBody := string(*gotMCP)
	if !strings.Contains(mcpBody, `"type":"image"`) || strings.Contains(mcpBody, "caveman_retrieve") {
		t.Fatalf("MCP streaming body should have live-zone images and no injected marker/tool: %s", mcpBody)
	}

	upstreamPlain, gotPlain := capturePixelUpstream(t, anthropicPixelResponse("claude-fable-5"))
	defer upstreamPlain.Close()
	sinkPlain := &captureSink{}
	compPlain := &pixelStoreCompressor{handle: "ccr_pixel_plain_stream"}
	srvPlain := newPixelTestServer(t, upstreamPlain.URL, "pixel", anthropic.New(upstreamPlain.URL), sinkPlain, compPlain, false)

	reqPlain := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	recPlain := httptest.NewRecorder()
	srvPlain.Handler().ServeHTTP(recPlain, reqPlain)
	if recPlain.Code != http.StatusOK {
		t.Fatalf("plain status = %d, want 200", recPlain.Code)
	}
	plainBody := string(*gotPlain)
	if !strings.Contains(plainBody, `"type":"image"`) {
		t.Fatalf("plain streaming body missing image: %s", plainBody)
	}
	if strings.Contains(plainBody, "caveman_retrieve") {
		t.Fatalf("plain streaming body should not contain MCP marker: %s", plainBody)
	}
	if got := recPlain.Header().Get("x-caveman-recovery-handle"); got != "ccr_pixel_plain_stream" {
		t.Fatalf("plain stream handle = %q, want ccr_pixel_plain_stream", got)
	}
}

func anthropicPixelBody(model string, stream bool) []byte {
	streamField := "false"
	if stream {
		streamField = "true"
	}
	return []byte(`{"model":"` + model + `","stream":` + streamField + `,"max_tokens":128,"system":"` + strings.Repeat("STATIC_CONTEXT_LINE stable policy detail.\\n", 1300) + `","messages":[{"role":"user","content":[{"type":"text","text":"summarize retained context"},{"type":"tool_result","tool_use_id":"tool_a","content":"` + strings.Repeat("TOOL_RESULT_A row with values.\\n", 420) + `"},{"type":"tool_result","tool_use_id":"tool_b","content":"` + strings.Repeat("TOOL_RESULT_B row with values.\\n", 420) + `"}]}]}`)
}

func assertPixelStaticSlabIntact(t *testing.T, upstreamBody string, token string, originalRepeats int) {
	t.Helper()
	if got := strings.Count(upstreamBody, token); got != originalRepeats {
		t.Fatalf("static slab token count for %q = %d, want %d: %s", token, got, originalRepeats, upstreamBody)
	}
}

func openAIPixelBody() []byte {
	return []byte(`{"model":"gpt-5.6","messages":[{"role":"system","content":"` + strings.Repeat("OPENAI_SYSTEM_CONTEXT detailed instruction.\\n", 3000) + `"},{"role":"user","content":"` + strings.Repeat("OPENAI_LIVE_USER_CONTEXT detailed instruction.\\n", 2600) + `"}]}`)
}

func geminiPixelBody() []byte {
	return []byte(`{"systemInstruction":{"parts":[{"text":"` + strings.Repeat("GEMINI_SYSTEM_CONTEXT stable instruction.\\n", 2600) + `"}]},"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"temperature":0}}`)
}

func anthropicPixelResponse(model string) string {
	return `{"id":"msg_pixel","type":"message","model":"` + model + `","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1000,"output_tokens":10}}`
}

func openAIPixelResponse() string {
	return `{"id":"chatcmpl_pixel","object":"chat.completion","model":"gpt-5.6","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1000,"completion_tokens":10}}`
}

func geminiPixelResponse() string {
	return `{"candidates":[{"content":{"parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":1000,"candidatesTokenCount":10}}`
}
