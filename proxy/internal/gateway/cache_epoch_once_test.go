package gateway

import (
	"testing"
)

// TestCacheEpochGateObservesOncePerRequest pins the fix for the double-observe.
//
// derivedEpochAllows RE-ANCHORS the stored frozen prefix on every observe. The
// compress path called the gate twice per request — once with the client's
// original body before compression, once with the transformed body before the
// tool-schema strip — so turn N's baseline was anchored to bytes the client
// never sends. Turn N+1 compared the original against that baseline, saw
// divergence, and skipped compression for the whole turn, flipping the provider
// cache prefix the gate exists to protect; with the strip enabled on a wrapped
// session, compression alternated on and off every other turn.
func TestCacheEpochGateObservesOncePerRequest(t *testing.T) {
	body := toolCatalogRequest("newest turn")
	rt := &captureTransport{responses: []string{subMessageRespBody}}
	comp := &toolSchemaStripCompressor{}
	srv, _ := newToolSchemaStripServer(t, comp, rt, Config{
		RecoveryViaMCP:  true,
		ToolSchemaStrip: toolSchemaStripMode,
	})

	headers := map[string]string{"x-cave-session": "sess-epoch"}
	for k, v := range subscriptionAgentHeaders {
		headers[k] = v
	}
	serveBody(t, srv, "/v1/messages", body, headers)

	// Both the compression gate and the tool-schema-strip gate ran this request.
	if comp.strips == 0 {
		t.Fatal("test setup: the strip did not run, so the second gate was never reached")
	}
	srv.cacheEpochGate.mu.Lock()
	got := srv.cacheEpochGate.observations
	srv.cacheEpochGate.mu.Unlock()
	if got != 1 {
		t.Fatalf("epoch gate observed %d times in one request, want 1 — a second observe re-anchors the baseline on transformed bytes", got)
	}
}
