package gateway

import (
	"bytes"
	"crypto/sha256"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/compressors"
	"github.com/JuliusBrussee/caveman/proxy/providers"
	"github.com/JuliusBrussee/caveman/proxy/providers/anthropic"
)

// The tool-schema annotation strip removes documentation-only JSON-Schema
// annotation keywords from inside a tool catalog's schemas — the very front of
// the provider cache prefix. It changes model-visible bytes, so it is default
// OFF, opt-in per operator, gated on the same local-wrap conditions live-zone
// compression needs, and every rewritten catalog has its exact original in CCR
// first.

// toolCatalog is a Claude-Code-shaped catalog: schema annotations worth removing,
// an MCP annotations block whose "title" is the tool's display NAME, a property
// legitimately named "title", descriptions that must survive, and a cache_control
// breakpoint riding the last tool.
const toolCatalog = `[` +
	`{"name":"Read","description":"Read a file from disk.","annotations":{"title":"Read a file","readOnlyHint":true},` +
	`"input_schema":{"$schema":"https://json-schema.org/draft/2020-12/schema","title":"Read args","examples":[{"path":"main.go"}],"type":"object",` +
	`"properties":{"title":{"type":"string","title":"Document title"},"path":{"type":"string","deprecated":false}},"required":["path"]}},` +
	`{"name":"Write","description":"Write a file to disk.",` +
	`"input_schema":{"type":"object","title":"Write args","properties":{"path":{"type":"string"}}},` +
	`"cache_control":{"type":"ephemeral"}}` +
	`]`

func toolCatalogRequest(live string) string {
	return `{"model":"claude-sonnet-4-6","max_tokens":1024,` +
		`"system":[{"type":"text","text":"You are Claude Code."}],` +
		`"tools":` + toolCatalog + `,` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"` + live + `"}]}]}`
}

func strippedToolCatalog(t *testing.T) string {
	t.Helper()
	out, ok := compressors.StripToolSchemaAnnotations([]byte(toolCatalog))
	if !ok {
		t.Fatal("fixture catalog is not strippable")
	}
	return string(out)
}

// toolSchemaStripCompressor is a Compressor that never rewrites message content,
// so a test exercises the tool-schema path alone. It strips through the engine's
// canonical function — the same one the binary wires.
type toolSchemaStripCompressor struct {
	mu     sync.Mutex
	stored [][]byte
	strips int
}

func (c *toolSchemaStripCompressor) CompressSegment(seg []byte) ([]byte, int, int) {
	return seg, 0, 0
}

func (c *toolSchemaStripCompressor) StoreOriginal(body []byte) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stored = append(c.stored, append([]byte(nil), body...))
	return contentHandle(body), nil
}

func (c *toolSchemaStripCompressor) StripToolSchema(tools []byte) ([]byte, bool) {
	c.mu.Lock()
	c.strips++
	c.mu.Unlock()
	return compressors.StripToolSchemaAnnotations(tools)
}

// noStripCompressor implements Compressor but not ToolSchemaStripper.
type noStripCompressor struct{ stored int }

func (c *noStripCompressor) CompressSegment(seg []byte) ([]byte, int, int) { return seg, 0, 0 }

func (c *noStripCompressor) StoreOriginal(body []byte) (string, error) {
	c.stored++
	return contentHandle(body), nil
}

func newToolSchemaStripServer(t *testing.T, comp Compressor, rt *captureTransport, cfg Config) (*Server, *captureSink) {
	t.Helper()
	sink := &captureSink{}
	if cfg.PrefixCache == nil {
		cfg.PrefixCache = newTestPrefixCache()
	}
	if cfg.Auth == nil {
		cfg.Auth = stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}}
	}
	cfg.Adapters = []providers.Adapter{anthropic.New("https://upstream.test")}
	cfg.Creds = passthroughTestCreds{}
	cfg.Sink = sink
	cfg.Compressor = comp
	cfg.HTTPClient = &http.Client{Transport: rt}
	return New(cfg), sink
}

// TestToolSchemaStripIsOffByDefault is the default-off pin: a wrap with every
// technical condition satisfied and no strip configured forwards the catalog
// byte-identically and writes no recovery record.
func TestToolSchemaStripIsOffByDefault(t *testing.T) {
	body := toolCatalogRequest("newest turn")
	rt := &captureTransport{responses: []string{subMessageRespBody}}
	comp := &toolSchemaStripCompressor{}
	srv, sink := newToolSchemaStripServer(t, comp, rt, Config{RecoveryViaMCP: true})

	rec := serveBody(t, srv, "/v1/messages", body, subscriptionAgentHeaders)

	if sha256.Sum256(rt.bodies[0]) != sha256.Sum256([]byte(body)) {
		t.Fatalf("default must be byte-identical passthrough:\n got %s\nwant %s", rt.bodies[0], body)
	}
	if comp.strips != 0 || len(comp.stored) != 0 {
		t.Fatalf("default must not strip or store: strips=%d stored=%d", comp.strips, len(comp.stored))
	}
	if got := rec.Header().Get("x-caveman-toolschema-strip"); got != "" {
		t.Fatalf("default disclosed a strip: %q", got)
	}
	if row := sink.last(t); len(row.OptimizationIDs) != 0 {
		t.Fatalf("default row claimed an optimization: %+v", row.OptimizationIDs)
	}
}

// TestToolSchemaStripRemovesAnnotationsAndDisclosesRecovery pins the whole
// contract of an enabled strip: the four annotations leave, everything that
// selects a tool stays, cache_control survives byte-for-byte, the exact original
// catalog is in CCR before the rewritten bytes ship, and the response discloses
// both the strip and its recovery handle.
func TestToolSchemaStripRemovesAnnotationsAndDisclosesRecovery(t *testing.T) {
	body := toolCatalogRequest("newest turn")
	rt := &captureTransport{responses: []string{subMessageRespBody}}
	comp := &toolSchemaStripCompressor{}
	srv, sink := newToolSchemaStripServer(t, comp, rt, Config{
		RecoveryViaMCP:  true,
		ToolSchemaStrip: toolSchemaStripMode,
	})

	rec := serveBody(t, srv, "/v1/messages", body, subscriptionAgentHeaders)

	upstream := string(rt.bodies[0])
	if !strings.Contains(upstream, `"tools":`+strippedToolCatalog(t)+`,`) {
		t.Fatalf("upstream catalog is not the canonical strip: %s", upstream)
	}
	for _, gone := range []string{
		`"$schema"`, `"examples"`, `"deprecated"`,
		`"title":"Read args"`, `"title":"Write args"`, `"title":"Document title"`,
	} {
		if strings.Contains(upstream, gone) {
			t.Fatalf("annotation %s survived: %s", gone, upstream)
		}
	}
	for _, kept := range []string{
		`"cache_control":{"type":"ephemeral"}`,
		`"description":"Read a file from disk."`,
		`"description":"Write a file to disk."`,
		// MCP's annotations.title is the tool's human-readable NAME — model-visible
		// and selection-relevant. The strip must never reach inside a non-schema
		// container to take it.
		`"annotations":{"title":"Read a file","readOnlyHint":true}`,
		`"properties":{"title":{"type":"string"},"path":{"type":"string"}}`,
		`"required":["path"]`,
	} {
		if !strings.Contains(upstream, kept) {
			t.Fatalf("selection-relevant bytes %s were lost: %s", kept, upstream)
		}
	}
	if strings.Index(upstream, `"name":"Read"`) > strings.Index(upstream, `"name":"Write"`) {
		t.Fatalf("tool order changed: %s", upstream)
	}
	if len(comp.stored) != 1 || string(comp.stored[0]) != toolCatalog {
		t.Fatalf("CCR must hold the exact original catalog, got %d records: %s", len(comp.stored), comp.stored)
	}
	if rec.Header().Get("x-caveman-toolschema-strip") != toolSchemaStripMode ||
		rec.Header().Get("x-caveman-toolschema-recovery-handle") != contentHandle([]byte(toolCatalog)) {
		t.Fatalf("strip disclosure missing: %v", rec.Header())
	}
	if !strings.Contains(rec.Header().Get("x-cave-optimization"), toolSchemaStripOptimizerID) {
		t.Fatalf("optimization header = %q", rec.Header().Get("x-cave-optimization"))
	}
	row := sink.last(t)
	if strings.Join(row.OptimizationIDs, ",") != toolSchemaStripOptimizerID {
		t.Fatalf("row optimizations = %v", row.OptimizationIDs)
	}
	// A row that names an optimizer must also name where the original bytes live,
	// or the audit trail claims a rewrite nobody can undo.
	if row.RecoveryHandle != contentHandle([]byte(toolCatalog)) {
		t.Fatalf("row recovery handle = %q, want the stored catalog handle", row.RecoveryHandle)
	}
	if row.CompressionTokensBefore != 0 || row.CompressionRatio != 0 || row.SavingsUSD != 0 {
		t.Fatalf("the strip must claim no token or dollar saving: %+v", row)
	}
}

// TestToolSchemaStripIsCacheStable is the reason a frozen-prefix rewrite is
// admissible at all: two consecutive requests carrying the same catalog must
// produce byte-identical upstream bodies, so the second turn hits the prefix the
// first turn paid to cache.
func TestToolSchemaStripIsCacheStable(t *testing.T) {
	body := toolCatalogRequest("newest turn")
	rt := &captureTransport{responses: []string{subMessageRespBody, subMessageRespBody}}
	comp := &toolSchemaStripCompressor{}
	srv, _ := newToolSchemaStripServer(t, comp, rt, Config{
		RecoveryViaMCP:  true,
		ToolSchemaStrip: toolSchemaStripMode,
	})

	serveBody(t, srv, "/v1/messages", body, subscriptionAgentHeaders)
	serveBody(t, srv, "/v1/messages", body, subscriptionAgentHeaders)

	if len(rt.bodies) != 2 {
		t.Fatalf("want 2 upstream requests, got %d", len(rt.bodies))
	}
	if !bytes.Equal(rt.bodies[0], rt.bodies[1]) {
		t.Fatalf("turn 2 diverged from turn 1:\n%s\n%s", rt.bodies[0], rt.bodies[1])
	}
	if bytes.Equal(rt.bodies[0], []byte(body)) {
		t.Fatal("neither turn was stripped, so stability proves nothing")
	}
	// A different live turn must leave the catalog span identical too — the prefix
	// is what the provider cached, and it may not move when the tail grows.
	stripped := strippedToolCatalog(t)
	serveBody(t, srv, "/v1/messages", toolCatalogRequest("a later, longer turn"), subscriptionAgentHeaders)
	if !strings.Contains(string(rt.bodies[2]), `"tools":`+stripped+`,`) {
		t.Fatalf("catalog moved when the conversation grew: %s", rt.bodies[2])
	}
}

// TestToolSchemaStripRecordModeIsPassThrough keeps record mode what it has always
// been: a byte-safe pass-through, whatever the operator configured.
func TestToolSchemaStripRecordModeIsPassThrough(t *testing.T) {
	body := toolCatalogRequest("newest turn")
	rt := &captureTransport{responses: []string{subMessageRespBody}}
	comp := &toolSchemaStripCompressor{}
	srv, sink := newToolSchemaStripServer(t, comp, rt, Config{
		Auth:            stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "record"}},
		RecoveryViaMCP:  true,
		ToolSchemaStrip: toolSchemaStripMode,
	})

	rec := serveBody(t, srv, "/v1/messages", body, subscriptionAgentHeaders)

	if sha256.Sum256(rt.bodies[0]) != sha256.Sum256([]byte(body)) {
		t.Fatalf("record mode transformed the request:\n got %s\nwant %s", rt.bodies[0], body)
	}
	if comp.strips != 0 || len(comp.stored) != 0 {
		t.Fatalf("record mode stripped or stored: strips=%d stored=%d", comp.strips, len(comp.stored))
	}
	if rec.Header().Get("x-caveman-toolschema-strip") != "" {
		t.Fatal("record mode disclosed a strip")
	}
	if row := sink.last(t); row.RawRequestSHA256 != row.TransformedRequestSHA256 {
		t.Fatalf("record row is not byte-identical: %+v", row)
	}
}

// TestToolSchemaStripFailsClosed covers every way the lever must decline: an
// unrecognized flag value, the explicit request-wide opt-out, a missing recovery
// path, a compressor that cannot strip, and a catalog we cannot extract.
func TestToolSchemaStripFailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		body    string
		headers map[string]string
	}{
		{
			name: "unrecognized flag value",
			cfg:  Config{RecoveryViaMCP: true, ToolSchemaStrip: "aggressive"},
		},
		{
			name: "no MCP recovery",
			cfg:  Config{ToolSchemaStrip: toolSchemaStripMode},
		},
		{
			name: "operator off-switch",
			cfg:  Config{RecoveryViaMCP: true, ToolSchemaStrip: toolSchemaStripMode, SubscriptionCompress: "off"},
		},
		{
			name:    "request-wide pass-through opt-out",
			cfg:     Config{RecoveryViaMCP: true, ToolSchemaStrip: toolSchemaStripMode},
			headers: map[string]string{"x-cave-transforms": "caveman.pass-through.v1"},
		},
		{
			name: "tools field is not an array",
			cfg:  Config{RecoveryViaMCP: true, ToolSchemaStrip: toolSchemaStripMode},
			body: `{"model":"claude-sonnet-4-6","max_tokens":1,"tools":{"name":"Read","title":"Read"},"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "no tools field at all",
			cfg:  Config{RecoveryViaMCP: true, ToolSchemaStrip: toolSchemaStripMode},
			body: `{"model":"claude-sonnet-4-6","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name: "catalog has nothing to strip",
			cfg:  Config{RecoveryViaMCP: true, ToolSchemaStrip: toolSchemaStripMode},
			body: `{"model":"claude-sonnet-4-6","max_tokens":1,"tools":[{"name":"Read","description":"Read a file."}],"messages":[{"role":"user","content":"hi"}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body
			if body == "" {
				body = toolCatalogRequest("newest turn")
			}
			headers := map[string]string{}
			for k, v := range subscriptionAgentHeaders {
				headers[k] = v
			}
			for k, v := range tc.headers {
				headers[k] = v
			}
			rt := &captureTransport{responses: []string{subMessageRespBody}}
			comp := &toolSchemaStripCompressor{}
			srv, _ := newToolSchemaStripServer(t, comp, rt, tc.cfg)

			rec := serveBody(t, srv, "/v1/messages", body, headers)

			if sha256.Sum256(rt.bodies[0]) != sha256.Sum256([]byte(body)) {
				t.Fatalf("must be byte-identical passthrough:\n got %s\nwant %s", rt.bodies[0], body)
			}
			if len(comp.stored) != 0 {
				t.Fatalf("declined strip still wrote %d recovery records", len(comp.stored))
			}
			if rec.Header().Get("x-caveman-toolschema-strip") != "" {
				t.Fatal("declined strip was disclosed as applied")
			}
		})
	}
}

// TestToolSchemaStripWithoutStripperCapabilityPassesThrough pins the seam: an
// embedder whose Compressor cannot strip keeps the catalog, whatever the operator
// configured.
func TestToolSchemaStripWithoutStripperCapabilityPassesThrough(t *testing.T) {
	body := toolCatalogRequest("newest turn")
	rt := &captureTransport{responses: []string{subMessageRespBody}}
	comp := &noStripCompressor{}
	srv, _ := newToolSchemaStripServer(t, comp, rt, Config{
		RecoveryViaMCP:  true,
		ToolSchemaStrip: toolSchemaStripMode,
	})

	serveBody(t, srv, "/v1/messages", body, subscriptionAgentHeaders)

	if sha256.Sum256(rt.bodies[0]) != sha256.Sum256([]byte(body)) {
		t.Fatalf("a compressor that cannot strip must pass through:\n got %s\nwant %s", rt.bodies[0], body)
	}
	if comp.stored != 0 {
		t.Fatalf("a compressor that cannot strip wrote %d recovery records", comp.stored)
	}
}

// TestToolSchemaStripWithoutPrefixCachePassesThrough pins the maintainability
// half of the gate. Without a durable replacement cache the proxy cannot keep
// message rewrites byte-stable across turns, and the strip inherits that same
// fail-closed condition rather than restating a looser one of its own.
func TestToolSchemaStripWithoutPrefixCachePassesThrough(t *testing.T) {
	body := toolCatalogRequest("newest turn")
	rt := &captureTransport{responses: []string{subMessageRespBody}}
	comp := &toolSchemaStripCompressor{}
	srv := New(Config{
		Adapters:        []providers.Adapter{anthropic.New("https://upstream.test")},
		Auth:            stubAuth{rc: RequestContext{Label: "local", RuntimeMode: "compress"}},
		Creds:           passthroughTestCreds{},
		Sink:            &captureSink{},
		Compressor:      comp,
		HTTPClient:      &http.Client{Transport: rt},
		RecoveryViaMCP:  true,
		ToolSchemaStrip: toolSchemaStripMode,
	})

	serveBody(t, srv, "/v1/messages", body, subscriptionAgentHeaders)

	if sha256.Sum256(rt.bodies[0]) != sha256.Sum256([]byte(body)) {
		t.Fatalf("no prefix cache must be byte-identical passthrough:\n got %s\nwant %s", rt.bodies[0], body)
	}
	if comp.strips != 0 || len(comp.stored) != 0 {
		t.Fatalf("no prefix cache still stripped: strips=%d stored=%d", comp.strips, len(comp.stored))
	}
}
