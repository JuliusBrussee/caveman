package gateway

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

// panickingCompressor stands in for an engine defect reached only by a
// particular payload shape — #1508's whitespace-only listing body panicked
// inside unwrapFileWrapper. The seam is the same for any future one.
type panickingCompressor struct {
	stubCompressor
	panicked int
}

func (c *panickingCompressor) CompressSegment(seg []byte) ([]byte, int, int) {
	c.panicked++
	panic("engine: slice bounds out of range [10:0]")
}

func (c *panickingCompressor) CompressSegmentType(seg []byte, contentType string) ([]byte, int, int) {
	return c.CompressSegment(seg)
}

func (c *panickingCompressor) CompressSegmentQuery(seg []byte, query string) ([]byte, int, int) {
	return c.CompressSegment(seg)
}

// TestCompressSurvivesCompressorPanic pins the byte-safe fail-open invariant
// against a panic, not just an error (#1508). An engine panic inside the
// per-block compress loop unwound the whole handler: net/http recovered, so the
// process lived, but the client connection closed with no response, nothing was
// sent upstream, and no row or log line was written — invisible under
// `caveman wrap`, where stderr goes to /dev/null. Retries failed identically
// because the offending block stayed in the live zone, so the conversation
// could not recover.
func TestCompressSurvivesCompressorPanic(t *testing.T) {
	var gotUpstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotUpstreamBody = string(body)
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, chatRespBody)
	}))
	defer upstream.Close()

	toolOutput := strings.Repeat(`[{"host":"alpha","status":"ok"},{"host":"beta","status":"blocked"}]`, 12)
	body := `{"model":"gpt-5.5","messages":[{"role":"user","content":"which host is blocked"},{"role":"tool","tool_call_id":"c1","content":` + quoteJSON(toolOutput) + `}]}`

	comp := &panickingCompressor{}
	var logBuf strings.Builder
	srv := New(Config{
		Adapters:       []providers.Adapter{openai.New(upstream.URL)},
		Auth:           stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}},
		Creds:          stubCreds{key: "sk-byok"},
		Sink:           &captureSink{},
		Compressor:     comp,
		RecoveryViaMCP: true,
		HTTPClient:     &http.Client{},
		Logger:         slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if comp.panicked == 0 {
		t.Fatal("compressor was never reached; the test proves nothing")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fail-open): %s", rec.Code, rec.Body.String())
	}
	if gotUpstreamBody != body {
		t.Fatalf("upstream did not receive the original bytes:\n got %s\nwant %s", gotUpstreamBody, body)
	}
	if !strings.Contains(logBuf.String(), "panic") {
		t.Fatalf("a swallowed panic must still be logged; log was: %s", logBuf.String())
	}
}

// TestObserveEstimateSurvivesCompressorPanic covers the other loop that reaches
// the engine with caller bytes. estimateRequest runs in record mode and changes
// nothing that is forwarded, so losing the request to a panic there is pure
// damage: the observation is optional, the inference is not.
func TestObserveEstimateSurvivesCompressorPanic(t *testing.T) {
	var gotUpstreamBody string
	upstream := echoUpstream(t, &gotUpstreamBody)
	defer upstream.Close()

	toolOutput := strings.Repeat(`[{"host":"alpha"},{"host":"beta"}]`, 16)
	body := `{"model":"gpt-5.5","messages":[{"role":"user","content":"which host"},{"role":"tool","tool_call_id":"c1","content":` + quoteJSON(toolOutput) + `}]}`

	comp := &panickingEstimator{}
	srv := New(Config{
		Adapters:        []providers.Adapter{openai.New(upstream.URL)},
		Auth:            stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		Creds:           stubCreds{key: "sk-byok"},
		Sink:            &captureSink{},
		Compressor:      comp,
		ObserveEstimate: true,
		HTTPClient:      &http.Client{},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if comp.panicked == 0 {
		t.Fatal("estimator was never reached; the test proves nothing")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fail-open): %s", rec.Code, rec.Body.String())
	}
	if gotUpstreamBody != body {
		t.Fatal("observe estimate changed forwarded request bytes")
	}
}

type panickingEstimator struct {
	stubCompressor
	panicked int
}

func (c *panickingEstimator) EstimateSegment(seg []byte) (int, int) {
	c.panicked++
	panic("engine: slice bounds out of range [10:0]")
}

// TestCompressDoesNotSwallowAbortHandler keeps the guard from absorbing
// net/http's deliberate control-flow sentinel. A recover() broad enough to eat
// http.ErrAbortHandler turns an intended connection abort into a silent
// success, which is the fail-open-validator shape this guard exists to avoid.
func TestCompressDoesNotSwallowAbortHandler(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, chatRespBody)
	}))
	defer upstream.Close()

	toolOutput := strings.Repeat(`[{"host":"alpha"},{"host":"beta"}]`, 16)
	body := `{"model":"gpt-5.5","messages":[{"role":"user","content":"which host"},{"role":"tool","tool_call_id":"c1","content":` + quoteJSON(toolOutput) + `}]}`

	srv := New(Config{
		Adapters:       []providers.Adapter{openai.New(upstream.URL)},
		Auth:           stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}},
		Creds:          stubCreds{key: "sk-byok"},
		Sink:           &captureSink{},
		Compressor:     &abortingCompressor{},
		RecoveryViaMCP: true,
		HTTPClient:     &http.Client{},
	})

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		srv.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	}()
	if recovered != http.ErrAbortHandler {
		t.Fatalf("recovered = %v, want http.ErrAbortHandler to propagate", recovered)
	}
}

type abortingCompressor struct{ stubCompressor }

func (c *abortingCompressor) CompressSegment(seg []byte) ([]byte, int, int) {
	panic(http.ErrAbortHandler)
}

func (c *abortingCompressor) CompressSegmentQuery(seg []byte, query string) ([]byte, int, int) {
	return c.CompressSegment(seg)
}
