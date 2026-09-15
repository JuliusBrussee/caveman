package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
)

// --- test harness -----------------------------------------------------------

type respOut struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

// run drives the server with the given JSON-RPC request lines and returns the
// decoded responses plus whatever the server logged (which must be empty on
// stdout — logs go to the returned log buffer, never to out).
func run(t *testing.T, eng Engine, requests ...string) ([]respOut, string) {
	t.Helper()
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	srv := NewServer("caveman", EngineTools(eng, logger), logger)

	in := strings.NewReader(strings.Join(requests, "\n") + "\n")
	var out bytes.Buffer
	if err := srv.Serve(in, &out); err != nil {
		t.Fatalf("serve: %v", err)
	}

	var resps []respOut
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, "\n") {
			t.Fatalf("response frame contains an embedded newline (breaks stdio framing): %q", line)
		}
		var r respOut
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("response line is not valid JSON (%v): %q", err, line)
		}
		if r.JSONRPC != "2.0" {
			t.Fatalf("response missing jsonrpc 2.0: %q", line)
		}
		resps = append(resps, r)
	}
	return resps, logBuf.String()
}

func decodeTool(t *testing.T, raw json.RawMessage) ToolResult {
	t.Helper()
	var tr ToolResult
	if err := json.Unmarshal(raw, &tr); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	if len(tr.Content) == 0 {
		t.Fatal("tool result has no content")
	}
	return tr
}

func realEngine(t *testing.T) *engine.Engine {
	t.Helper()
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return engine.New(store, nil)
}

// --- mock engine (pure-plumbing) -------------------------------------------

type mockEngine struct {
	compress   func([]byte, engine.Options) (engine.Result, error)
	retrieve   func(string) ([]byte, error)
	stats      func() (ccr.Stats, error)
	toonEncode func([]byte) ([]byte, error)
	toonDecode func([]byte) ([]byte, error)
}

func (m mockEngine) Compress(in []byte, o engine.Options) (engine.Result, error) {
	return m.compress(in, o)
}
func (m mockEngine) Retrieve(h string) ([]byte, error)         { return m.retrieve(h) }
func (m mockEngine) RetrieveQuery(h, _ string) ([]byte, error) { return m.retrieve(h) }
func (m mockEngine) Stats() (ccr.Stats, error)                 { return m.stats() }
func (m mockEngine) EncodeTOON(in []byte) ([]byte, error)      { return m.toonEncode(in) }
func (m mockEngine) DecodeTOON(in []byte) ([]byte, error)      { return m.toonDecode(in) }

// --- tests ------------------------------------------------------------------

func TestToolsListExactlyFive(t *testing.T) {
	eng := mockEngine{} // never called by tools/list — pure plumbing
	resps, _ := run(t, eng, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if len(resps) != 1 {
		t.Fatalf("want 1 response, got %d", len(resps))
	}
	var res struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resps[0].Result, &res); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	got := []string{}
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
		if tool.Description == "" || len(tool.InputSchema) == 0 {
			t.Errorf("tool %q missing description or schema", tool.Name)
		}
		if !json.Valid(tool.InputSchema) {
			t.Errorf("tool %q has invalid inputSchema", tool.Name)
		}
	}
	want := []string{ToolCompress, ToolRetrieve, ToolStats, ToolToonEncode, ToolToonDecode}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tools = %v, want exactly %v", got, want)
	}
}

func TestToolsListIncludesOptionalMetadata(t *testing.T) {
	tool := Tool{
		Name:        "large_result",
		Description: "Return one large result.",
		InputSchema: ObjectSchema(map[string]any{}),
		Meta:        map[string]any{"anthropic/maxResultSizeChars": 100000},
		Handler:     func(json.RawMessage) ToolResult { return ToolRawText("ok") },
	}
	srv := NewServer("metadata-test", []Tool{tool}, nil)
	var out bytes.Buffer
	if err := srv.Serve(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var response struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	meta, ok := response.Result.Tools[0]["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("_meta = %#v, want object", response.Result.Tools[0]["_meta"])
	}
	if got := meta["anthropic/maxResultSizeChars"]; got != float64(100000) {
		t.Fatalf("maxResultSizeChars = %#v, want 100000", got)
	}

	plain := NewServer("metadata-test", []Tool{{Name: "plain", InputSchema: ObjectSchema(map[string]any{})}}, nil)
	out.Reset()
	if err := plain.Serve(strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`), &out); err != nil {
		t.Fatalf("serve plain: %v", err)
	}
	if strings.Contains(out.String(), `"_meta"`) {
		t.Fatalf("empty metadata must be omitted: %s", out.String())
	}
}

func TestPlumbingDispatchWithMockEngine(t *testing.T) {
	called := false
	eng := mockEngine{
		compress: func(in []byte, _ engine.Options) (engine.Result, error) {
			called = true
			return engine.Result{Output: []byte("SMALL"), Ratio: 0.5, TokensBefore: 10, TokensAfter: 5, Basis: engine.BasisInferred, ContentType: "json", RecoveryHandle: "ccr_abc"}, nil
		},
	}
	resps, _ := run(t, eng, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"caveman_compress","arguments":{"input":"whatever"}}}`)
	if !called {
		t.Fatal("compress was not dispatched to the engine")
	}
	tr := decodeTool(t, resps[0].Result)
	if tr.IsError {
		t.Fatal("unexpected isError")
	}
	var p compressPayload
	if err := json.Unmarshal([]byte(tr.Content[0].Text), &p); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if p.Compressed != "SMALL" || p.Ratio != 0.5 || p.RecoveryHandle == nil || *p.RecoveryHandle != "ccr_abc" {
		t.Fatalf("plumbing did not pass engine result through faithfully: %+v", p)
	}
}

func TestCompressToolForwardsContentType(t *testing.T) {
	var got engine.Options
	eng := mockEngine{
		compress: func(in []byte, o engine.Options) (engine.Result, error) {
			got = o
			return engine.Result{
				Output:          []byte("rows[2]{id,name}:\n  1,a\n  2,b"),
				Ratio:           0.5,
				TokensBefore:    40,
				TokensAfter:     20,
				Basis:           engine.BasisInferred,
				ContentType:     engine.TypeTOON,
				Method:          "toon",
				LosslessToModel: boolPtr(true),
				RecoveryHandle:  "ccr_toon",
			}, nil
		},
	}
	resps, _ := run(t, eng, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"caveman_compress","arguments":{"input":"{\"rows\":[{\"id\":1,\"name\":\"a\"},{\"id\":2,\"name\":\"b\"}]}","content_type":"toon"}}}`)
	if got.Type != engine.TypeTOON || got.Mode != engine.ModeCompress {
		t.Fatalf("options = %+v, want compress/toon", got)
	}
	tr := decodeTool(t, resps[0].Result)
	var p compressPayload
	if err := json.Unmarshal([]byte(tr.Content[0].Text), &p); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if p.Method != "toon" || p.LosslessToModel == nil || !*p.LosslessToModel {
		t.Fatalf("metadata not forwarded: %+v", p)
	}
}

func TestCompressMalformedIsBytePreservingPassThrough(t *testing.T) {
	const malformed = `{not valid json at all`
	resps, _ := run(t, realEngine(t),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"caveman_compress","arguments":{"input":`+jsonStr(malformed)+`}}}`)
	tr := decodeTool(t, resps[0].Result)
	if tr.IsError {
		t.Fatal("malformed input must NOT be an error (it is a pass-through)")
	}
	var p compressPayload
	if err := json.Unmarshal([]byte(tr.Content[0].Text), &p); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if p.Compressed != malformed {
		t.Errorf("pass-through must be byte-identical: got %q", p.Compressed)
	}
	if p.Ratio != 0 {
		t.Errorf("pass-through ratio must be 0, got %v", p.Ratio)
	}
	if p.RecoveryHandle != nil {
		t.Errorf("pass-through recovery_handle must be null, got %v", *p.RecoveryHandle)
	}
}

func TestCompressCCRErrorPreservesPassThroughTokenAccounting(t *testing.T) {
	const original = `{"rows":[1,2,3]}`
	eng := mockEngine{
		compress: func([]byte, engine.Options) (engine.Result, error) {
			return engine.Result{
				Output:       []byte(original),
				Ratio:        0,
				TokensBefore: 8,
				TokensAfter:  8,
				Basis:        engine.BasisInferred,
				ContentType:  "json",
			}, errors.New("ccr write failed")
		},
	}
	resps, _ := run(t, eng,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"caveman_compress","arguments":{"input":`+jsonStr(original)+`}}}`)
	tr := decodeTool(t, resps[0].Result)
	if tr.IsError {
		t.Fatalf("accounted engine pass-through must remain usable: %+v", tr)
	}
	var p compressPayload
	if err := json.Unmarshal([]byte(tr.Content[0].Text), &p); err != nil {
		t.Fatal(err)
	}
	if p.Compressed != original || p.TokensBefore != 8 || p.TokensAfter != 8 || p.Ratio != 0 || p.ContentType != "json" || p.RecoveryHandle != nil {
		t.Fatalf("CCR fallback lost truthful accounting: %+v", p)
	}
}

func TestRetrieveUnknownHandleFailsClosed(t *testing.T) {
	resps, _ := run(t, realEngine(t),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"caveman_retrieve","arguments":{"recovery_handle":"ccr_does_not_exist"}}}`)
	tr := decodeTool(t, resps[0].Result)
	if !tr.IsError {
		t.Fatal("unknown handle must be an error, never a fabricated payload")
	}
	if !strings.Contains(tr.Content[0].Text, "cave_unknown_handle") {
		t.Fatalf("error must carry a cave_snake_code, got %q", tr.Content[0].Text)
	}
}

func TestRetrieveNormalizesMarkerForms(t *testing.T) {
	const canonical = "ccr_abc123"
	for _, supplied := range []string{canonical, "ccr:" + canonical, "<<ccr:" + canonical + ">>"} {
		var got string
		eng := mockEngine{
			retrieve: func(handle string) ([]byte, error) {
				got = handle
				return []byte("original"), nil
			},
		}
		resps, _ := run(t, eng, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"caveman_retrieve","arguments":{"recovery_handle":`+jsonStr(supplied)+`}}}`)
		result := decodeTool(t, resps[0].Result)
		if result.IsError || got != canonical {
			t.Fatalf("supplied=%q normalized=%q error=%t", supplied, got, result.IsError)
		}
	}
}

func TestStatsIsInferredSessionScopedNeverVerified(t *testing.T) {
	resps, logs := run(t, realEngine(t),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"caveman_stats"}}`)
	tr := decodeTool(t, resps[0].Result)
	var p statsPayload
	if err := json.Unmarshal([]byte(tr.Content[0].Text), &p); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if p.Basis != "inferred" {
		t.Errorf("basis = %q, want inferred", p.Basis)
	}
	if p.Scope != "session" {
		t.Errorf("scope = %q, want session", p.Scope)
	}
	// The literal "verified" must never appear anywhere in the output or logs.
	if strings.Contains(tr.Content[0].Text, "verified") || strings.Contains(logs, "verified") {
		t.Error("the string \"verified\" must never appear")
	}
}

func TestToonEncodeHappyPathIncludesBothSizes(t *testing.T) {
	const uniform = `{"rows":[{"id":1,"name":"a"},{"id":2,"name":"b"},{"id":3,"name":"c"}]}`
	resps, _ := run(t, realEngine(t),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"caveman_toon_encode","arguments":{"input":`+jsonStr(uniform)+`}}}`)
	tr := decodeTool(t, resps[0].Result)
	if tr.IsError {
		t.Fatalf("unexpected isError: %s", tr.Content[0].Text)
	}
	var p toonEncodePayload
	if err := json.Unmarshal([]byte(tr.Content[0].Text), &p); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if !p.Encoded || p.Output == uniform || p.Output == "" {
		t.Fatalf("uniform JSON should TOON-encode, got %+v", p)
	}
	if p.InputBytes != len(uniform) || p.OutputBytes != len(p.Output) {
		t.Fatalf("sizes must report both sides honestly: %+v", p)
	}
}

func TestToonEncodeDegradesByteSafeWithNote(t *testing.T) {
	const notJSON = `{definitely not json`
	resps, _ := run(t, realEngine(t),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"caveman_toon_encode","arguments":{"input":`+jsonStr(notJSON)+`}}}`)
	tr := decodeTool(t, resps[0].Result)
	if tr.IsError {
		t.Fatal("un-encodable input must degrade byte-safe, not error")
	}
	var p toonEncodePayload
	if err := json.Unmarshal([]byte(tr.Content[0].Text), &p); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if p.Encoded || p.Output != notJSON {
		t.Fatalf("pass-through must be byte-identical with encoded=false: %+v", p)
	}
	if !strings.Contains(p.Note, "not encoded") {
		t.Fatalf("degrade must say why, got note %q", p.Note)
	}
}

func TestToonDecodeRoundTrips(t *testing.T) {
	const uniform = `{"rows":[{"id":1,"name":"a"},{"id":2,"name":"b"}]}`
	eng := realEngine(t)
	encoded, err := eng.EncodeTOON([]byte(uniform))
	if err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	resps, _ := run(t, eng,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"caveman_toon_decode","arguments":{"input":`+jsonStr(string(encoded))+`}}}`)
	tr := decodeTool(t, resps[0].Result)
	if tr.IsError {
		t.Fatalf("round-trip decode errored: %s", tr.Content[0].Text)
	}
	var want, got any
	if err := json.Unmarshal([]byte(uniform), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(tr.Content[0].Text), &got); err != nil {
		t.Fatalf("decode output is not JSON: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round-trip mismatch: want %v got %v", want, got)
	}
}

func TestToonDecodeFailsLoudlyOnGarbage(t *testing.T) {
	// A bare string is a VALID scalar TOON document, so true garbage here means
	// structurally broken tabular TOON: 2 columns declared, rows carry 1.
	const garbage = "rows[2]{id,name}:\n  1\n  2"
	resps, _ := run(t, realEngine(t),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"caveman_toon_decode","arguments":{"input":`+jsonStr(garbage)+`}}}`)
	tr := decodeTool(t, resps[0].Result)
	if !tr.IsError {
		t.Fatal("invalid TOON must be a loud error, never emitted as JSON")
	}
	if !strings.Contains(tr.Content[0].Text, "cave_invalid_toon") {
		t.Fatalf("error must carry a cave_snake_code, got %q", tr.Content[0].Text)
	}
	if strings.Contains(tr.Content[0].Text, garbage) {
		t.Fatal("the raw input must never appear in the decode result")
	}
}

func TestToonDecodeEngineErrorFailsLoudly(t *testing.T) {
	eng := mockEngine{
		toonDecode: func([]byte) ([]byte, error) { return nil, errors.New("engine down") },
	}
	resps, _ := run(t, eng,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"caveman_toon_decode","arguments":{"input":"rows[1]{id}:\n  1"}}}`)
	tr := decodeTool(t, resps[0].Result)
	if !tr.IsError || !strings.Contains(tr.Content[0].Text, "cave_invalid_toon") {
		t.Fatalf("engine failure must fail loudly, got %+v", tr)
	}
}

func TestFullCycleCompressRetrieveStats(t *testing.T) {
	eng := realEngine(t)
	// A repetitive JSON array compresses (S4) and yields a recovery handle.
	big := `{"items":[` + strings.Repeat(`{"k":"v","n":1},`, 50) + `{"k":"v","n":1}]}`
	resps, _ := run(t, eng,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"caveman_compress","arguments":{"input":`+jsonStr(big)+`}}}`)
	var cp compressPayload
	json.Unmarshal([]byte(decodeTool(t, resps[0].Result).Content[0].Text), &cp)
	if cp.RecoveryHandle == nil {
		t.Fatalf("expected a recovery handle for compressible JSON, got pass-through ratio=%v", cp.Ratio)
	}
	// Retrieve must return the exact original.
	resps2, _ := run(t, eng,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"caveman_retrieve","arguments":{"recovery_handle":`+jsonStr(*cp.RecoveryHandle)+`}}}`)
	tr2 := decodeTool(t, resps2[0].Result)
	if tr2.IsError || tr2.Content[0].Text != big {
		t.Fatalf("retrieve did not return the byte-exact original")
	}
}

func TestUnknownMethodIsJSONRPCError(t *testing.T) {
	resps, _ := run(t, mockEngine{}, `{"jsonrpc":"2.0","id":9,"method":"does/not/exist"}`)
	if resps[0].Error == nil || resps[0].Error.Code != codeMethodNotFound {
		t.Fatalf("unknown method must return method-not-found, got %+v", resps[0])
	}
}

func TestNotificationGetsNoResponse(t *testing.T) {
	resps, _ := run(t, mockEngine{}, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if len(resps) != 0 {
		t.Fatalf("a notification must get no response, got %d", len(resps))
	}
}

func TestInitializeUsesSuppliedBuildVersion(t *testing.T) {
	var out bytes.Buffer
	srv := NewServerVersion("caveman", "9.8.7-test", nil, nil)
	if err := srv.Serve(strings.NewReader(
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{}}\n",
	), &out); err != nil {
		t.Fatalf("serve initialize: %v", err)
	}
	var response struct {
		Result struct {
			ServerInfo struct {
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &response); err != nil {
		t.Fatalf("decode initialize: %v", err)
	}
	if response.Result.ServerInfo.Version != "9.8.7-test" {
		t.Fatalf("version=%q, want build stamp", response.Result.ServerInfo.Version)
	}
}

func TestInitializeNegotiatesSupportedProtocolVersion(t *testing.T) {
	resps, _ := run(t, mockEngine{}, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`)
	if len(resps) != 1 || resps[0].Error != nil {
		t.Fatalf("supported initialize rejected: %+v", resps)
	}
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(resps[0].Result, &result); err != nil {
		t.Fatalf("decode initialize result: %v", err)
	}
	if result.ProtocolVersion != defaultProtocolVersion {
		t.Fatalf("negotiated version=%q, want %q", result.ProtocolVersion, defaultProtocolVersion)
	}
}

// A client asking for a version this adapter does not implement must get the
// version it DOES implement, not an error. Erroring here dropped the server
// from every client past 2024-11-05 — and because `caveman wrap` reads recovery
// availability from an install-time marker rather than from the live agent, the
// proxy kept eliding content that no longer had a caveman_retrieve to expand it.
func TestInitializeOffersItsOwnVersionToNewerClients(t *testing.T) {
	for _, requested := range []string{"2025-06-18", "2025-03-26", "9999-01-01", ""} {
		params := `{"protocolVersion":"` + requested + `"}`
		resps, _ := run(t, mockEngine{}, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":`+params+`}`)
		if len(resps) != 1 || resps[0].Error != nil {
			t.Fatalf("client version %q was refused: %+v", requested, resps)
		}
		var result struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if err := json.Unmarshal(resps[0].Result, &result); err != nil {
			t.Fatalf("decode initialize result: %v", err)
		}
		if result.ProtocolVersion != defaultProtocolVersion {
			t.Fatalf("client version %q negotiated %q, want the adapter's own %q",
				requested, result.ProtocolVersion, defaultProtocolVersion)
		}
	}
}

// TestZeroEgressNoNetworkImports proves the adapter cannot egress: it imports no
// network package. Combined with the in-memory full-cycle test above (which runs
// with no disk or network), this is the egress guarantee from PRD §11.6.
func TestZeroEgressNoNetworkImports(t *testing.T) {
	forbidden := map[string]bool{
		`"net"`: true, `"net/http"`: true, `"net/rpc"`: true, `"os/exec"`: true,
	}
	dirs := []string{".", filepath.Join("cmd", "caveman-mcp")}
	fset := token.NewFileSet()
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read dir %s: %v", dir, err)
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parse %s: %v", e.Name(), err)
			}
			for _, imp := range f.Imports {
				if forbidden[imp.Path.Value] {
					t.Errorf("%s/%s imports %s — the MCP adapter must open no network/exec path", dir, e.Name(), imp.Path.Value)
				}
			}
		}
	}
}

// serveLines drives the server with raw request lines and returns the raw
// non-empty output lines (a JSON-RPC batch response is a single array line, so
// this harness — unlike run — does not assume one object per line).
func serveLines(t *testing.T, srv *Server, lines ...string) []string {
	t.Helper()
	in := strings.NewReader(strings.Join(lines, "\n") + "\n")
	var out bytes.Buffer
	if err := srv.Serve(in, &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var got []string
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if l != "" {
			got = append(got, l)
		}
	}
	return got
}

// A malformed line must not kill the session: the server responds -32700 for the
// bad line, RESYNCHRONIZES to the next newline, and keeps serving. Sub-issue #1.
func TestParseErrorResyncsAndKeepsServing(t *testing.T) {
	resps, _ := run(t, mockEngine{},
		`{ this is not valid json`,
		`{"jsonrpc":"2.0","id":42,"method":"tools/list"}`)
	if len(resps) != 2 {
		t.Fatalf("want 2 responses (parse error + tools/list), got %d: %+v", len(resps), resps)
	}
	if resps[0].Error == nil || resps[0].Error.Code != codeParseError {
		t.Fatalf("first response must be a -32700 parse error, got %+v", resps[0])
	}
	// The valid request that FOLLOWED the malformed byte must still be answered.
	if resps[1].Error != nil {
		t.Fatalf("valid request after a parse error was not served: %+v", resps[1])
	}
	if string(resps[1].ID) != "42" {
		t.Fatalf("second response id = %s, want 42", resps[1].ID)
	}
}

// A JSON-RPC batch (array) must be handled per JSON-RPC — one array of responses
// — and must not terminate the stream. Sub-issue #2.
func TestBatchRequestIsHandledAndDoesNotKillStream(t *testing.T) {
	srv := NewServer("caveman", EngineTools(mockEngine{}, nil), nil)
	lines := serveLines(t, srv,
		`[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","id":2,"method":"ping"}]`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	if len(lines) != 2 {
		t.Fatalf("want 2 output lines (batch array + tools/list), got %d: %v", len(lines), lines)
	}
	var batch []respOut
	if err := json.Unmarshal([]byte(lines[0]), &batch); err != nil {
		t.Fatalf("batch response is not a JSON array: %v (%q)", err, lines[0])
	}
	if len(batch) != 2 {
		t.Fatalf("batch must answer both requests, got %d: %v", len(batch), batch)
	}
	// The single request that FOLLOWED the batch proves the stream survived.
	var follow respOut
	if err := json.Unmarshal([]byte(lines[1]), &follow); err != nil {
		t.Fatalf("post-batch response invalid: %v (%q)", err, lines[1])
	}
	if string(follow.ID) != "3" {
		t.Fatalf("post-batch id = %s, want 3", follow.ID)
	}
}

// A handler panic must be contained as a cave_tool_panicked ToolError; the
// process must survive and keep serving. Sub-issue #3.
func TestHandlerPanicYieldsToolErrorNotCrash(t *testing.T) {
	boom := Tool{
		Name:        "boom",
		Description: "panics",
		InputSchema: ObjectSchema(map[string]any{}),
		Handler:     func(json.RawMessage) ToolResult { panic("kaboom") },
	}
	safe := Tool{
		Name:        "safe",
		Description: "ok",
		InputSchema: ObjectSchema(map[string]any{}),
		Handler:     func(json.RawMessage) ToolResult { return ToolRawText("ok") },
	}
	srv := NewServer("caveman", []Tool{boom, safe}, nil)
	lines := serveLines(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"boom","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"safe","arguments":{}}}`)
	if len(lines) != 2 {
		t.Fatalf("want 2 responses (panic contained + safe), got %d: %v", len(lines), lines)
	}
	var r0 respOut
	if err := json.Unmarshal([]byte(lines[0]), &r0); err != nil {
		t.Fatalf("panic response invalid: %v", err)
	}
	tr := decodeTool(t, r0.Result)
	if !tr.IsError || !strings.Contains(tr.Content[0].Text, "cave_tool_panicked") {
		t.Fatalf("panic must surface as a cave_tool_panicked tool error, got %+v", tr)
	}
	// The next call proves the process survived the panic.
	var r1 respOut
	if err := json.Unmarshal([]byte(lines[1]), &r1); err != nil {
		t.Fatalf("post-panic response invalid: %v", err)
	}
	if tr1 := decodeTool(t, r1.Result); tr1.IsError {
		t.Fatalf("server did not survive the panic: %+v", tr1)
	}
}

// A request with no usable id (absent OR explicitly null) is a JSON-RPC
// notification and must get no response. Sub-issue #4.
func TestIdlessRequestGetsNoResponse(t *testing.T) {
	for _, line := range []string{
		`{"jsonrpc":"2.0","method":"tools/list"}`,     // id absent
		`{"jsonrpc":"2.0","id":null,"method":"ping"}`, // id explicitly null
	} {
		resps, _ := run(t, mockEngine{}, line)
		if len(resps) != 0 {
			t.Fatalf("id-less request %q must get no response, got %d: %+v", line, len(resps), resps)
		}
	}
}

// An inbound message longer than the cap must be rejected with
// cave_payload_too_large, and the stream must survive to serve the next request.
// Sub-issue #5 (inbound).
func TestOversizedInboundRejectedWithCaveCode(t *testing.T) {
	srv := NewServer("caveman", EngineTools(mockEngine{}, nil), nil)
	srv.maxInboundBytes = 256
	// A syntactically VALID request whose line exceeds the cap — so a rejection
	// can only come from the size guard, not from a parse error.
	big := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"caveman_compress","arguments":{"input":"` +
		strings.Repeat("x", 1024) + `"}}}`
	lines := serveLines(t, srv, big, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if len(lines) != 2 {
		t.Fatalf("want 2 responses (reject + survivor), got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "cave_payload_too_large") {
		t.Fatalf("oversized inbound must be rejected with cave_payload_too_large, got %q", lines[0])
	}
	var r1 respOut
	if err := json.Unmarshal([]byte(lines[1]), &r1); err != nil || string(r1.ID) != "2" {
		t.Fatalf("stream did not survive an oversized inbound message: %q (%v)", lines[1], err)
	}
}

// A tool result larger than the cap must be replaced with a fail-closed
// cave_payload_too_large error rather than dumped whole into host context.
// Sub-issue #5 (outbound).
func TestOversizedResultRejectedWithCaveCode(t *testing.T) {
	huge := Tool{
		Name:        "huge",
		Description: "returns a huge block",
		InputSchema: ObjectSchema(map[string]any{}),
		Handler:     func(json.RawMessage) ToolResult { return ToolRawText(strings.Repeat("y", 4096)) },
	}
	srv := NewServer("caveman", []Tool{huge}, nil)
	srv.maxResultBytes = 512
	lines := serveLines(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"huge","arguments":{}}}`)
	if len(lines) != 1 {
		t.Fatalf("want 1 response, got %d: %v", len(lines), lines)
	}
	var r respOut
	if err := json.Unmarshal([]byte(lines[0]), &r); err != nil {
		t.Fatalf("response invalid: %v", err)
	}
	tr := decodeTool(t, r.Result)
	if !tr.IsError || !strings.Contains(tr.Content[0].Text, "cave_payload_too_large") {
		t.Fatalf("oversized result must fail closed with cave_payload_too_large, got %+v", tr)
	}
	if strings.Contains(tr.Content[0].Text, strings.Repeat("y", 512)) {
		t.Fatal("the oversized payload must not be emitted in the rejection")
	}
}

// Recovery must NEVER fail closed on size. The CCR store is shared with the
// gateway, which has no 16 MiB ceiling, so a handle's original can exceed
// maxResultBytes; caveman_retrieve must still return the exact original,
// not cave_payload_too_large (root CLAUDE.md rule #2). Regression for
// the review finding on the #139 fix.
func TestRetrievePayoutExemptFromResultCap(t *testing.T) {
	original := strings.Repeat("z", defaultMaxResultBytes+4096) // > 16 MiB
	eng := mockEngine{
		retrieve: func(string) ([]byte, error) { return []byte(original), nil },
	}
	srv := NewServer("caveman", EngineTools(eng, nil), nil) // production default cap
	lines := serveLines(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"caveman_retrieve","arguments":{"recovery_handle":"ccr_big"}}}`)
	if len(lines) != 1 {
		t.Fatalf("want 1 response, got %d", len(lines))
	}
	var r respOut
	if err := json.Unmarshal([]byte(lines[0]), &r); err != nil {
		t.Fatalf("response invalid: %v", err)
	}
	tr := decodeTool(t, r.Result)
	if tr.IsError {
		t.Fatalf("recovery must never fail closed on size; got isError with %d content bytes", toolResultSize(tr))
	}
	if tr.Content[0].Text != original {
		t.Fatalf("recovery must be byte-exact: got %d bytes, want %d", len(tr.Content[0].Text), len(original))
	}
}

// jsonStr quotes s as a JSON string literal for embedding in a request line.
func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func boolPtr(v bool) *bool { return &v }
