package browse

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/engine/tokens"
	"github.com/JuliusBrussee/caveman/mcp"
)

type fakeDriver struct {
	raw         []byte
	snapshot    int
	lastURL     string
	lastAct     ActionRequest
	lastTgt     Target
	evalValue   any
	snapshotErr error
	closeErr    error
	closed      bool
}

func (f *fakeDriver) Snapshot(_ context.Context, url string, _ time.Duration) ([]byte, error) {
	f.snapshot++
	f.lastURL = url
	if f.snapshotErr != nil {
		return nil, f.snapshotErr
	}
	return f.raw, nil
}

func (f *fakeDriver) Act(_ context.Context, req ActionRequest, target Target) (ActionResult, error) {
	f.lastAct = req
	f.lastTgt = target
	if req.Text == "explode" {
		return ActionResult{}, errors.New("boom")
	}
	return ActionResult{OK: true, Settled: true}, nil
}

func (f *fakeDriver) Eval(_ context.Context, expression string) (any, error) {
	if expression == "throw" {
		return nil, errors.New("boom")
	}
	return f.evalValue, nil
}

func (f *fakeDriver) Close() error {
	f.closed = true
	return f.closeErr
}

func testSession(t *testing.T) (*Session, *fakeDriver) {
	t.Helper()
	raw, err := os.ReadFile("../engine/compressors/testdata/axtree_cdp_local_page.json")
	if err != nil {
		t.Fatal(err)
	}
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	driver := &fakeDriver{raw: raw, evalValue: map[string]any{"ok": true}}
	return NewSession(engine.New(store, nil), driver, nil), driver
}

func TestBrowserToolsExactlyFour(t *testing.T) {
	s, _ := testSession(t)
	tools := BrowserTools(s)
	got := make([]string, len(tools))
	for i, tool := range tools {
		got[i] = tool.Name
		if tool.Description == "" || len(tool.InputSchema) == 0 {
			t.Fatalf("tool %s missing description/schema", tool.Name)
		}
	}
	want := []string{ToolSnapshot, ToolAct, ToolEval, ToolRecover}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tools=%v want=%v", got, want)
	}
	if !tools[3].ExemptResultCap {
		t.Fatal("byte-exact browser_recover must be exempt from generic MCP result cap")
	}
	definitions := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		definitions = append(definitions, map[string]any{
			"name": tool.Name, "description": tool.Description, "inputSchema": tool.InputSchema,
		})
	}
	catalog, err := json.Marshal(definitions)
	if err != nil {
		t.Fatal(err)
	}
	catalogTokens := tokens.Default().Count(catalog)
	if catalogTokens > 300 {
		t.Fatalf("four-tool catalog exceeds 300-token budget: %d", catalogTokens)
	}
	t.Logf("browser MCP catalog tokens=%d", catalogTokens)
}

func TestSessionCloseAndTargetSnapshotsAreSafe(t *testing.T) {
	if err := NewSession(nil, nil, nil).Close(); err != nil {
		t.Fatalf("nil driver close: %v", err)
	}

	s, driver := testSession(t)
	s.LoadTargets(map[string]Target{"u1": {BackendDOMNodeID: 7}})
	got := s.TargetsSnapshot()
	got["u1"] = Target{BackendDOMNodeID: 99}
	got["injected"] = Target{BackendDOMNodeID: 8}
	if original, ok := s.lookupTarget("u1"); !ok || original.BackendDOMNodeID != 7 {
		t.Fatalf("snapshot mutated session target: %+v ok=%v", original, ok)
	}
	if _, ok := s.lookupTarget("injected"); ok {
		t.Fatal("snapshot map leaked writes into session")
	}

	driver.closeErr = errors.New("close failed")
	if err := s.Close(); !errors.Is(err, driver.closeErr) {
		t.Fatalf("Close error = %v", err)
	}
	if !driver.closed {
		t.Fatal("Close did not reach driver")
	}
}

func TestUnavailableSessionFailsClosedWithoutPanicking(t *testing.T) {
	s := NewSession(nil, nil, nil)
	for name, got := range map[string]mcp.ToolResult{
		"snapshot": s.snapshotTool(jsonArgs(t, map[string]any{})),
		"act":      s.actTool(jsonArgs(t, map[string]any{"action": "wait"})),
		"eval":     s.evalTool(jsonArgs(t, map[string]any{"expression": "1+1"})),
		"recover":  s.recoverTool(jsonArgs(t, map[string]any{"recovery_handle": "ccr_missing"})),
	} {
		if !got.IsError || !strings.Contains(got.Content[0].Text, "cave_browser_unavailable") {
			t.Fatalf("%s unavailable result = %+v", name, got)
		}
	}
}

func TestSnapshotCachesUIDTargetsAndRecoversExactAXTree(t *testing.T) {
	s, driver := testSession(t)
	tr := s.snapshotTool(jsonArgs(t, map[string]any{"url": "http://127.0.0.1:3000"}))
	if tr.IsError {
		t.Fatalf("snapshot failed: %s", tr.Content[0].Text)
	}
	var p snapshotPayload
	decodeToolText(t, tr, &p)
	if p.Basis != engine.BasisInferred || p.Ratio <= 0 || p.RecoveryHandle == nil {
		t.Fatalf("snapshot did not report inferred recovery-backed reduction: %+v", p)
	}
	if driver.lastURL != "http://127.0.0.1:3000" {
		t.Fatalf("url not forwarded: %q", driver.lastURL)
	}
	if !strings.Contains(p.UIDs, `[ua] button "Save settings"`) {
		t.Fatalf("snapshot missing expected uid/button:\n%s", p.UIDs)
	}
	if p.ViewTokens <= 0 || p.ViewTokens >= p.TokensAfter {
		t.Fatalf("view/delivery accounting invalid: %+v", p)
	}
	delivered := tokens.Default().Count([]byte(tr.Content[0].Text))
	if p.TokensAfter != delivered {
		t.Fatalf("tokens_after=%d must count exact agent-visible payload=%d", p.TokensAfter, delivered)
	}
	wantRatio := float64(p.TokensBefore-p.TokensAfter) / float64(p.TokensBefore)
	if math.Abs(p.Ratio-wantRatio) > 1e-12 {
		t.Fatalf("delivery ratio=%v want=%v", p.Ratio, wantRatio)
	}
	if p.TokensAfter > 128 {
		t.Fatalf("captured fixture exceeded agent-visible token budget: %+v", p)
	}
	t.Logf("captured snapshot tokens raw=%d view=%d delivered=%d", p.TokensBefore, p.ViewTokens, p.TokensAfter)
	target, ok := s.lookupTarget("ua")
	if !ok || target.BackendDOMNodeID != 10 {
		t.Fatalf("uid target cache missing button: %+v ok=%v", target, ok)
	}

	recovered := s.recoverTool(jsonArgs(t, map[string]any{"recovery_handle": *p.RecoveryHandle}))
	if recovered.IsError || recovered.Content[0].Text != string(driver.raw) {
		t.Fatalf("recover not byte-exact: error=%v", recovered.IsError)
	}
	if strings.Contains(recovered.Content[0].Text, "verified") || strings.Contains(p.UIDs, "verified") {
		t.Fatal("browse path must never emit verified")
	}
}

// iframeAXTree mimics Accessibility.getFullAXTree for a host frame that embeds
// an <iframe>: node "3" is the iframe boundary whose child document root "100"
// lives in another frame and is therefore absent from this payload.
const iframeAXTree = `[
  {"nodeId":"1","role":{"value":"RootWebArea"},"name":{"value":"Host Page"},"backendDOMNodeId":1,"childIds":["2","3"]},
  {"nodeId":"2","role":{"value":"button"},"name":{"value":"Save settings"},"backendDOMNodeId":10},
  {"nodeId":"3","role":{"value":"Iframe"},"name":{"value":"Embedded report"},"backendDOMNodeId":11,"childIds":["100"]}
]`

// TestSnapshotIframeTreeStillCompressesToUIDs is the issue #140 pass-through
// regression at the tool boundary: before the fix an iframe's dangling childId
// made the AX tree reject, the engine passed the raw JSON through, and
// snapshotTool dumped that raw tree into `uids` with a nil recovery handle.
// After the fix the frame-visible nodes compress into a usable uid map.
func TestSnapshotIframeTreeStillCompressesToUIDs(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	s := NewSession(engine.New(store, nil), &fakeDriver{raw: []byte(iframeAXTree)}, nil)

	tr := s.snapshotTool(jsonArgs(t, map[string]any{"url": "http://host"}))
	if tr.IsError {
		t.Fatalf("iframe snapshot must not fail closed: %s", tr.Content[0].Text)
	}
	var p snapshotPayload
	decodeToolText(t, tr, &p)
	if p.RecoveryHandle == nil || p.Ratio <= 0 || p.Basis != engine.BasisInferred {
		t.Fatalf("iframe snapshot did not produce a recovery-backed reduction: %+v", p)
	}
	if !strings.Contains(p.UIDs, `[ua] button "Save settings"`) {
		t.Fatalf("frame-visible button lost its uid handle:\n%s", p.UIDs)
	}
	if strings.Contains(p.UIDs, `childIds`) || strings.Contains(p.UIDs, `nodeId`) {
		t.Fatalf("raw AX tree was dumped into uids instead of the curated view:\n%s", p.UIDs)
	}
	if target, ok := s.lookupTarget("ua"); !ok || target.BackendDOMNodeID != 10 {
		t.Fatalf("uid target cache missing iframe-page button: %+v ok=%v", target, ok)
	}
}

// TestSnapshotPassThroughFailsClosedAndKeepsUIDCache pins the second half of
// issue #140: when the engine genuinely cannot produce a recovery-backed view
// (here: no CCR store, so the S4 a11y compressor fails closed to pass-through),
// snapshotTool must return a fail-closed error rather than dumping the raw tree
// into `uids`, and must NOT wipe the previously cached uid targets.
func TestSnapshotPassThroughFailsClosedAndKeepsUIDCache(t *testing.T) {
	// engine.New(nil, nil): no store → S4 a11y compressor passes through.
	s := NewSession(engine.New(nil, nil), &fakeDriver{raw: []byte(iframeAXTree)}, nil)
	s.LoadTargets(map[string]Target{"u1": {BackendDOMNodeID: 7}})

	tr := s.snapshotTool(jsonArgs(t, map[string]any{"url": "http://host"}))
	if !tr.IsError || !strings.Contains(tr.Content[0].Text, "cave_browser_snapshot_uncompressed") {
		t.Fatalf("pass-through snapshot must fail closed, got %+v", tr)
	}
	if strings.Contains(tr.Content[0].Text, `"nodeId"`) || strings.Contains(tr.Content[0].Text, `"childIds"`) {
		t.Fatalf("raw AX tree leaked into pass-through error payload: %s", tr.Content[0].Text)
	}
	if target, ok := s.lookupTarget("u1"); !ok || target.BackendDOMNodeID != 7 {
		t.Fatalf("pass-through wiped the prior uid cache: %+v ok=%v", target, ok)
	}
}

func TestBrowserToolsFailClosedOnInvalidArgumentsAndDriverErrors(t *testing.T) {
	s, driver := testSession(t)

	if got := s.snapshotTool(json.RawMessage(`{`)); !got.IsError ||
		!strings.Contains(got.Content[0].Text, "cave_invalid_arguments") {
		t.Fatalf("invalid snapshot arguments = %+v", got)
	}
	driver.snapshotErr = errors.New("browser unavailable")
	if got := s.snapshotTool(jsonArgs(t, map[string]any{})); !got.IsError ||
		!strings.Contains(got.Content[0].Text, "cave_browser_snapshot_failed") {
		t.Fatalf("snapshot driver failure = %+v", got)
	}
	driver.snapshotErr = nil

	if got := s.actTool(json.RawMessage(`{`)); !got.IsError ||
		!strings.Contains(got.Content[0].Text, "cave_invalid_arguments") {
		t.Fatalf("invalid act arguments = %+v", got)
	}
	s.LoadTargets(map[string]Target{"u1": {BackendDOMNodeID: 7}})
	if got := s.actTool(jsonArgs(t, map[string]any{"action": "click", "uid": "u1", "text": "explode"})); !got.IsError ||
		!strings.Contains(got.Content[0].Text, "cave_browser_action_failed") {
		t.Fatalf("action driver failure = %+v", got)
	}

	if got := s.evalTool(jsonArgs(t, map[string]any{})); !got.IsError ||
		!strings.Contains(got.Content[0].Text, "cave_invalid_arguments") {
		t.Fatalf("invalid eval arguments = %+v", got)
	}
	if got := s.evalTool(jsonArgs(t, map[string]any{"expression": "throw"})); !got.IsError ||
		!strings.Contains(got.Content[0].Text, "cave_browser_eval_failed") {
		t.Fatalf("eval driver failure = %+v", got)
	}

	if got := s.recoverTool(json.RawMessage(`{`)); !got.IsError ||
		!strings.Contains(got.Content[0].Text, "cave_invalid_arguments") {
		t.Fatalf("invalid recovery arguments = %+v", got)
	}
}

func TestSnapshotQueryFocusesOutputAndTargetCache(t *testing.T) {
	s, _ := testSession(t)
	tr := s.snapshotTool(jsonArgs(t, map[string]any{"url": "http://local", "query": "save settings"}))
	if tr.IsError {
		t.Fatalf("query snapshot failed: %s", tr.Content[0].Text)
	}
	var p snapshotPayload
	decodeToolText(t, tr, &p)
	if !strings.Contains(p.UIDs, `[ua] button "Save settings"`) || strings.Contains(p.UIDs, "Email address") {
		t.Fatalf("query did not focus snapshot:\n%s", p.UIDs)
	}
	if len(s.TargetsSnapshot()) != 1 {
		t.Fatalf("query snapshot cached hidden targets: %+v", s.TargetsSnapshot())
	}
}

func TestSnapshotRejectsDangerousURLsAndUnboundedWaitWithoutDriving(t *testing.T) {
	s, driver := testSession(t)
	s.LoadTargets(map[string]Target{"prior": {BackendDOMNodeID: 7}})
	for _, args := range []map[string]any{
		{"url": "file:///etc/passwd"},
		{"url": "javascript:document.body.innerText='owned'"},
		{"url": "chrome://settings"},
		{"url": "relative/path"},
		{"wait": -1},
		{"wait": 30_001},
	} {
		got := s.snapshotTool(jsonArgs(t, args))
		if !got.IsError {
			t.Fatalf("unsafe snapshot args succeeded: %+v", args)
		}
	}
	if driver.snapshot != 0 {
		t.Fatalf("invalid snapshot reached browser %d times", driver.snapshot)
	}
	if _, ok := s.lookupTarget("prior"); !ok {
		t.Fatal("rejected snapshot wiped prior uid cache")
	}
}

func TestActRejectsUnknownActionBeforeUIDLookup(t *testing.T) {
	s, _ := testSession(t)
	got := s.actTool(jsonArgs(t, map[string]any{"action": "launch-missiles", "uid": "missing"}))
	if !got.IsError || !strings.Contains(got.Content[0].Text, "cave_unknown_action") {
		t.Fatalf("unknown action did not fail with stable code: %+v", got)
	}
}

func TestActUsesCachedTargetAndFailsClosedOnUnknownUID(t *testing.T) {
	s, driver := testSession(t)
	tr := s.snapshotTool(jsonArgs(t, map[string]any{"url": "http://local"}))
	var p snapshotPayload
	decodeToolText(t, tr, &p)
	if p.RecoveryHandle == nil {
		t.Fatal("snapshot did not produce recovery handle")
	}

	act := s.actTool(jsonArgs(t, map[string]any{"uid": "ua", "action": "click"}))
	if act.IsError {
		t.Fatalf("act failed: %s", act.Content[0].Text)
	}
	var res ActionResult
	decodeToolText(t, act, &res)
	if !res.OK || !res.Settled || driver.lastTgt.BackendDOMNodeID != 10 || driver.lastAct.Action != "click" {
		t.Fatalf("act did not dispatch through cached target: res=%+v target=%+v req=%+v", res, driver.lastTgt, driver.lastAct)
	}

	unknown := s.actTool(jsonArgs(t, map[string]any{"uid": "missing", "action": "click"}))
	if !unknown.IsError || !strings.Contains(unknown.Content[0].Text, "cave_unknown_uid") {
		t.Fatalf("unknown uid must fail closed, got %+v", unknown)
	}
}

func TestRecoverUnknownHandleFailsClosed(t *testing.T) {
	s, _ := testSession(t)
	tr := s.recoverTool(jsonArgs(t, map[string]any{"recovery_handle": "ccr_missing"}))
	if !tr.IsError || !strings.Contains(tr.Content[0].Text, "cave_unknown_handle") {
		t.Fatalf("unknown handle must fail closed, got %+v", tr)
	}
}

func TestEvalReturnsResult(t *testing.T) {
	s, _ := testSession(t)
	tr := s.evalTool(jsonArgs(t, map[string]any{"expression": "1+1"}))
	if tr.IsError {
		t.Fatalf("eval failed: %s", tr.Content[0].Text)
	}
	var got map[string]any
	decodeToolText(t, tr, &got)
	if got["result"] == nil {
		t.Fatalf("missing eval result: %+v", got)
	}
}

func jsonArgs(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decodeToolText(t *testing.T, tr mcp.ToolResult, out any) {
	t.Helper()
	if len(tr.Content) == 0 {
		t.Fatal("tool result has no content")
	}
	if err := json.Unmarshal([]byte(tr.Content[0].Text), out); err != nil {
		t.Fatalf("decode %q: %v", tr.Content[0].Text, err)
	}
}
