package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
	"github.com/JuliusBrussee/caveman/proxy/providers/openai"
)

type dedupTestCompressor struct {
	eng   *engine.Engine
	store *ccr.Store
}

func (c *dedupTestCompressor) CompressSegment(seg []byte) ([]byte, int, int) {
	res, err := c.eng.Compress(seg, engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		return seg, 0, 0
	}
	return res.Output, res.TokensBefore, res.TokensAfter
}

func (c *dedupTestCompressor) StoreOriginal(body []byte) (string, error) {
	return c.store.Put(ccr.Recovery{ContentType: "text", Compressor: "gateway", Original: body})
}

type dedupAuth struct{ rc RequestContext }

func (a dedupAuth) Authenticate(_ context.Context, _ *http.Request) (RequestContext, error) {
	return a.rc, nil
}

type dedupCreds struct{ cred providers.Credential }

func (c dedupCreds) Resolve(_ string, _ *http.Request) providers.Credential { return c.cred }

type dedupRoundTripper struct {
	bodies [][]byte
}

func (rt *dedupRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	rt.bodies = append(rt.bodies, b)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":100,"output_tokens":10,"cache_read_input_tokens":80,"cache_creation_input_tokens":0}}`)),
	}, nil
}

type memPrefixCache struct {
	rows map[string]struct {
		rep    []byte
		handle string
	}
}

func newMemPrefixCache() *memPrefixCache {
	return &memPrefixCache{rows: map[string]struct {
		rep    []byte
		handle string
	}{}}
}

func (c *memPrefixCache) LookupReplacement(scope string, original []byte) ([]byte, string, bool) {
	k := scope + "\x00" + string(original)
	r, ok := c.rows[k]
	if !ok {
		return nil, "", false
	}
	return r.rep, r.handle, true
}

func (c *memPrefixCache) RememberReplacement(scope string, original, replacement []byte, handle string) ([]byte, error) {
	k := scope + "\x00" + string(original)
	if existing, ok := c.rows[k]; ok {
		return existing.rep, nil
	}
	cp := append([]byte(nil), replacement...)
	c.rows[k] = struct {
		rep    []byte
		handle string
	}{rep: cp, handle: handle}
	return cp, nil
}

func TestLiveZoneDuplicateReadCollapsingPreservesPrefixAndRecovers(t *testing.T) {
	t.Parallel()
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	rt := &dedupRoundTripper{}
	sink := &captureSink{}
	pc := newMemPrefixCache()
	srv := New(Config{
		Adapters:       []providers.Adapter{anthropic.New("https://upstream.test")},
		Auth:           dedupAuth{rc: RequestContext{RuntimeMode: "compress"}},
		Creds:          dedupCreds{cred: providers.Credential{Mode: "ephemeral_header", Key: "sk-ant-test"}},
		Sink:           sink,
		Compressor:     &dedupTestCompressor{eng: engine.New(store, nil), store: store},
		PrefixCache:    pc,
		RecoveryViaMCP: true,
		HTTPClient:     &http.Client{Transport: rt},
	})

	// Build a large repeatable tool_result (> 1024 bytes).
	var bigFile strings.Builder
	bigFile.WriteString("package billing\n\n")
	for i := 0; i < 30; i++ {
		bigFile.WriteString("func ComputeInvoiceLineItem(qty int, unitCents int64) int64 {\n")
		bigFile.WriteString("    subtotal := int64(qty) * unitCents\n")
		bigFile.WriteString("    if subtotal < 0 { return 0 }\n")
		bigFile.WriteString("    return subtotal\n")
		bigFile.WriteString("}\n")
	}
	rawFile := bigFile.String()

	sendTurn := func(msgs []map[string]any) {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{
			"model":      "claude-sonnet-4-6",
			"max_tokens": 128,
			"system":     "You are a coding assistant.",
			"messages":   msgs,
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(payload)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", "sk-ant-test")
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("x-cave-session", "dedup-sess-1")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	}

	// Turn 1: agent reads billing.go for the first time (marked with cache_control).
	turn1Msgs := []map[string]any{
		{"role": "user", "content": "Inspect billing.go"},
		{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": "t1", "name": "Read", "input": map[string]any{"path": "billing.go"}}}},
		{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "t1", "content": rawFile, "cache_control": map[string]any{"type": "ephemeral"}}}},
	}
	sendTurn(turn1Msgs)
	body1 := string(rt.bodies[0])
	if strings.Contains(body1, "duplicate tool output") {
		t.Fatalf("turn 1 (first occurrence) must NOT collapse as duplicate:\n%s", body1)
	}

	// Turn 2: agent re-reads the EXACT same billing.go in the live zone while turn 1's copy is frozen.
	turn2Msgs := append(append([]map[string]any{}, turn1Msgs...),
		map[string]any{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": "t2", "name": "Read", "input": map[string]any{"path": "billing.go"}}}},
		map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "t2", "content": rawFile, "cache_control": map[string]any{"type": "ephemeral"}}}},
	)
	sendTurn(turn2Msgs)
	body2 := string(rt.bodies[1])
	if !strings.Contains(body2, "duplicate tool output") {
		t.Fatalf("turn 2 live duplicate tool_result should collapse to compact stub:\n%s", body2)
	}
	row2 := sink.last(t)
	if row2.CacheBust {
		t.Fatalf("turn 2 must NOT bust cached prefix: cause=%q", row2.CacheBustCause)
	}
	if !strings.Contains(strings.Join(row2.OptimizationIDs, ","), dedupOptimizerID) {
		t.Fatalf("expected %s in OptimizationIDs, got %v", dedupOptimizerID, row2.OptimizationIDs)
	}

	// Extract CCR handle from the duplicate stub and verify byte-exact recovery.
	var parsed2 struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(rt.bodies[1], &parsed2)
	var lastBlocks []struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal(parsed2.Messages[len(parsed2.Messages)-1].Content, &lastBlocks)
	dupHandle := ccrHandleOf([]byte(lastBlocks[0].Content))
	if dupHandle == "" {
		t.Fatalf("duplicate stub missing <<ccr:...>> handle: %s", lastBlocks[0].Content)
	}
	recovered, gerr := store.Get(dupHandle)
	if gerr != nil || string(recovered) != rawFile {
		t.Fatalf("CCR recovery of collapsed duplicate failed: err=%v len=%d want=%d", gerr, len(recovered), len(rawFile))
	}

	// Turn 3: turn 2's duplicate is now FROZEN; must re-substitute identically with 0 cache busts!
	turn3Msgs := append(append([]map[string]any{}, turn2Msgs...),
		map[string]any{"role": "assistant", "content": "Checked."},
		map[string]any{"role": "user", "content": "Now summarize the invoice formula."},
	)
	sendTurn(turn3Msgs)
	row3 := sink.last(t)
	if row3.CacheBust {
		t.Fatalf("turn 3 must NOT bust cached prefix after duplicate froze: cause=%q", row3.CacheBustCause)
	}

	// Turn 4 (post-compaction): turn 1's copy was dropped so only ONE copy remains.
	// It becomes occurrence 0 -> must NOT emit a dangling "duplicate tool output" pointer!
	compactedMsgs := []map[string]any{
		{"role": "user", "content": "Compacted summary"},
		{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": "t2", "name": "Read", "input": map[string]any{"path": "billing.go"}}}},
		{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "t2", "content": rawFile}}},
	}
	sendTurn(compactedMsgs)
	body4 := string(rt.bodies[3])
	if strings.Contains(body4, "duplicate tool output") {
		t.Fatalf("post-compaction single remaining copy must NOT render dangling duplicate stub:\n%s", body4)
	}

	// Sanity check: OpenAI Responses adapter also supports duplicate collapsing on function_call_output.
	oaAdapter := openai.New("https://upstream.test")
	oaBody, _ := json.Marshal(map[string]any{
		"model": "gpt-5.4",
		"input": []map[string]any{
			{"role": "user", "content": "read billing.go twice"},
			{"type": "function_call", "call_id": "c1", "name": "exec_command", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "c1", "output": rawFile},
			{"type": "function_call", "call_id": "c2", "name": "exec_command", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "c2", "output": rawFile},
		},
	})
	oaMeta := providers.RequestMetadata{Provider: "openai", Endpoint: "/v1/responses", Model: "gpt-5.4"}
	oaTransform := providers.TransformResult{Body: oaBody}
	oaComp := srv.rewriteRequest(oaAdapter, oaBody, oaMeta, &oaTransform, "req-oa-dup", nil, true)
	if oaComp == nil || !strings.Contains(string(oaTransform.Body), "duplicate tool output") {
		t.Fatalf("expected OpenAI Responses duplicate tool output collapse, got comp=%+v body=%s", oaComp, string(oaTransform.Body))
	}
}
