package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

// estimateStub implements Compressor + Estimator. EstimateSegment reports a fixed
// would-be reduction; StoreOriginal records that it was called so a test can prove
// the observe path never stores a CCR original. CompressSegment is a no-op — the
// observe path must never call it.
type estimateStub struct {
	estimateCalls int
	compressCalls int
	storeCalls    int
	before, after int
}

func (e *estimateStub) CompressSegment(seg []byte) ([]byte, int, int) {
	e.compressCalls++
	return seg, 0, 0
}

func (e *estimateStub) StoreOriginal(body []byte) (string, error) {
	e.storeCalls++
	return "ccr_should_not_happen", nil
}

func (e *estimateStub) EstimateSegment(seg []byte) (int, int) {
	e.estimateCalls++
	return e.before, e.after
}

func newObserveTestServer(t *testing.T, upstream string, sink TelemetrySink, comp Compressor) *Server {
	t.Helper()
	return New(Config{
		Adapters:        []providers.Adapter{openai.New(upstream)},
		Auth:            stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		Creds:           stubCreds{key: "sk-byok"},
		Sink:            sink,
		Compressor:      comp,
		ObserveEstimate: true,
		HTTPClient:      &http.Client{},
	})
}

// TestObserveEstimateForwardsByteIdenticalAndRecordsWouldSave proves the record-mode
// observe path: the request reaches the upstream BYTE-IDENTICAL (record mode is
// absolute pass-through), no CCR original is ever stored, no saving is booked, yet
// would_save_tokens accumulates the engine's estimate and would_save_usd is priced
// (list-price eligible openai PAYG). Basis stays inferred.
func TestObserveEstimateForwardsByteIdenticalAndRecordsWouldSave(t *testing.T) {
	var gotUpstreamBody string
	upstream := echoUpstream(t, &gotUpstreamBody)
	defer upstream.Close()

	sink := &captureSink{}
	comp := &estimateStub{before: 100, after: 40}
	srv := newObserveTestServer(t, upstream.URL, sink, comp)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatReqBody))
	req.Header.Set("authorization", "Bearer sk-from-agent")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	// byte-safety: record mode forwards the ORIGINAL bytes unchanged.
	if gotUpstreamBody != chatReqBody {
		t.Errorf("observe-estimate must forward original bytes byte-for-byte:\n got %q\nwant %q", gotUpstreamBody, chatReqBody)
	}
	if comp.estimateCalls == 0 {
		t.Fatal("estimator was never called in observe mode")
	}
	if comp.storeCalls != 0 {
		t.Errorf("observe-estimate must NOT store a CCR original, got %d StoreOriginal calls", comp.storeCalls)
	}
	if comp.compressCalls != 0 {
		t.Errorf("observe-estimate must use EstimateSegment only, not CompressSegment, got %d compress calls", comp.compressCalls)
	}
	// No compression header may be disclosed in record mode.
	if got := rec.Header().Get("x-caveman-recovery-handle"); got != "" {
		t.Errorf("record mode must not disclose a recovery handle, got %q", got)
	}
	if got := rec.Header().Get("x-cave-optimization"); got != "none" {
		t.Errorf("x-cave-optimization = %q, want none (record mode applies no optimizer)", got)
	}

	row := sink.last(t)
	if row.WouldSaveTokens != 60 {
		t.Errorf("would_save_tokens = %d, want 60 (100 - 40)", row.WouldSaveTokens)
	}
	if row.WouldSaveUSD == nil || *row.WouldSaveUSD <= 0 {
		t.Errorf("would_save_usd = %v, want a positive priced figure for list-price-eligible openai PAYG", row.WouldSaveUSD)
	}
	if row.SavingsUSD != 0 {
		t.Errorf("observe-estimate must book ZERO savings_usd, got %v", row.SavingsUSD)
	}
	if row.RecoveryHandle != "" || row.CompressionTokensBefore != 0 || row.CompressionTokensAfter != 0 {
		t.Errorf("record mode must record no compression fields: handle=%q before=%d after=%d", row.RecoveryHandle, row.CompressionTokensBefore, row.CompressionTokensAfter)
	}
	if row.Basis != "inferred" {
		t.Errorf("basis = %q, want inferred", row.Basis)
	}
	if row.RawRequestSHA256 != row.TransformedRequestSHA256 {
		t.Error("observe-estimate is pass-through: raw and transformed hashes must match")
	}
}

// TestObserveEstimateOffRecordsNothing proves record mode with observe-estimate off
// (the default) records no would-have-saved figure even with an estimator wired.
func TestObserveEstimateOffRecordsNothing(t *testing.T) {
	var gotUpstreamBody string
	upstream := echoUpstream(t, &gotUpstreamBody)
	defer upstream.Close()

	sink := &captureSink{}
	comp := &estimateStub{before: 100, after: 40}
	srv := New(Config{
		Adapters:   []providers.Adapter{openai.New(upstream.URL)},
		Auth:       stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		Creds:      stubCreds{key: "sk-byok"},
		Sink:       sink,
		Compressor: comp,
		// ObserveEstimate omitted (false).
		HTTPClient: &http.Client{},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatReqBody))
	req.Header.Set("authorization", "Bearer sk-from-agent")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if comp.estimateCalls != 0 {
		t.Errorf("observe-estimate off must not call the estimator, got %d calls", comp.estimateCalls)
	}
	row := sink.last(t)
	if row.WouldSaveTokens != 0 || row.WouldSaveUSD != nil {
		t.Errorf("observe-estimate off must record nothing: tokens=%d usd=%v", row.WouldSaveTokens, row.WouldSaveUSD)
	}
}
