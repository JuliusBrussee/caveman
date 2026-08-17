package standalone

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/proxy/internal/config"
	"github.com/JuliusBrussee/caveman/proxy/internal/store"
)

// TestEngineCompressor_BestOf_ReEncodesJSONToTOON proves the seam the proxy uses
// for "replace tool output via the proxy": with CAVE_ENGINE_TOON=best-of, the
// engine-backed compressor re-encodes a uniform-JSON segment (the kind found in a
// tool_result) into the denser TOON form, and reports a real token reduction. The
// array is kept small so lossless TOON beats lossy elision (which only triggers on
// longer arrays) — i.e. the model still sees every row.
func TestEngineCompressor_BestOf_ReEncodesJSONToTOON(t *testing.T) {
	t.Setenv("CAVE_ENGINE_TOON", "best-of")

	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatalf("open ccr: %v", err)
	}
	defer store.Close()

	comp := NewEngineCompressor(store) // engine.New reads the env at construction
	segment := []byte(`{"rows":[{"id":1,"name":"ana","city":"boulder"},{"id":2,"name":"luis","city":"denver"},{"id":3,"name":"sam","city":"aspen"},{"id":4,"name":"kai","city":"tahoe"}]}`)

	out, before, after := comp.CompressSegment(segment)
	if !strings.Contains(string(out), "rows[4]{id,name,city}:") {
		t.Fatalf("expected a TOON tabular header, got:\n%s", out)
	}
	if after >= before || before == 0 {
		t.Fatalf("expected a real token reduction, got before=%d after=%d", before, after)
	}
}

// TestStandaloneCompressMode_TOON_ReplacesToolOutput is the end-to-end proof that
// the proxy replaces a tool_result's verbose JSON with TOON before it reaches the
// model. The agent sends JSON; the upstream (model) receives TOON; neither side
// ever holds both forms — the doubling the design must avoid.
func TestStandaloneCompressMode_TOON_ReplacesToolOutput(t *testing.T) {
	t.Setenv("CAVE_ENGINE_TOON", "best-of")

	const respBody = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":100,"output_tokens":5}}`
	upstream := &captureUpstreamTransport{response: respBody}

	spend, err := store.Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer spend.Close()

	recovery, err := ccr.OpenMemory()
	if err != nil {
		t.Fatalf("open ccr: %v", err)
	}
	defer recovery.Close()

	cfg := config.Config{
		Mode:      "compress",
		Providers: map[string]config.ProviderConfig{"anthropic": {BaseURL: "https://upstream.test"}},
	}
	srv := New(cfg, spend, Options{HTTPClient: &http.Client{Transport: upstream}, Compressor: NewEngineCompressor(recovery)})

	toolJSON := `{"rows":[{"id":1,"name":"ana","city":"boulder"},{"id":2,"name":"luis","city":"denver"},{"id":3,"name":"sam","city":"aspen"},{"id":4,"name":"kai","city":"tahoe"}]}`
	reqMap := map[string]any{
		"model":      "claude-sonnet-4-6",
		"max_tokens": 1024,
		"messages": []any{
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": toolJSON},
			}},
		},
	}
	reqBytes, _ := json.Marshal(reqMap)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(reqBytes)))
	req.Header.Set("x-api-key", "sk-ant-api-test")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	gotUpstreamBody := string(upstream.body)
	if !strings.Contains(gotUpstreamBody, "rows[4]{id,name,city}:") {
		t.Fatalf("upstream did not receive TOON-encoded tool output:\n%s", gotUpstreamBody)
	}
	if strings.Contains(gotUpstreamBody, `"name":"ana"`) {
		t.Fatalf("upstream still contains the original verbose JSON tool output:\n%s", gotUpstreamBody)
	}
}
