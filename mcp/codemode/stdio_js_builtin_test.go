package codemode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/browse"
	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/mcp"
)

// TestHelperMockMCPServer acts as a real stdio MCP server child process when
// spawned by TestStdioUpstreamEndToEnd with GO_WANT_MOCK_MCP_SERVER=1.
func TestHelperMockMCPServer(t *testing.T) {
	if os.Getenv("GO_WANT_MOCK_MCP_SERVER") != "1" {
		return
	}
	tools := []mcp.Tool{
		{
			Name:        "list_items",
			Description: "List mock workspace items with verbose metadata.",
			InputSchema: mcp.ObjectSchema(map[string]any{
				"state": mcp.StringProp("Filter state"),
			}),
			Handler: func(raw json.RawMessage) mcp.ToolResult {
				var a struct {
					State string `json:"state"`
				}
				_ = json.Unmarshal(raw, &a)
				items := make([]map[string]any, 0, 12)
				for i := 1; i <= 12; i++ {
					st := "open"
					if i%2 == 0 {
						st = "closed"
					}
					if a.State != "" && a.State != st {
						continue
					}
					items = append(items, map[string]any{
						"id":    i,
						"title": fmt.Sprintf("Mock Issue #%d", i),
						"state": st,
						"blob":  strings.Repeat("metadata-padding-", 15),
					})
				}
				return mcp.ToolText(map[string]any{"items": items})
			},
		},
		{
			Name:        "get_item",
			Description: "Get one mock item by id.",
			InputSchema: mcp.ObjectSchema(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, "id"),
			Handler: func(raw json.RawMessage) mcp.ToolResult {
				var a struct {
					ID int `json:"id"`
				}
				_ = json.Unmarshal(raw, &a)
				return mcp.ToolText(map[string]any{
					"id":      a.ID,
					"summary": fmt.Sprintf("Detail for item %d", a.ID),
				})
			},
		},
	}
	srv := mcp.NewServer("mock-downstream", tools, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_ = srv.Serve(os.Stdin, os.Stdout)
	os.Exit(0)
}

func TestStdioUpstreamEndToEnd(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	up, err := StartStdioUpstream(ctx, "tracker", ServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperMockMCPServer"},
		Env:     map[string]string{"GO_WANT_MOCK_MCP_SERVER": "1"},
	}, nil)
	if err != nil {
		t.Fatalf("StartStdioUpstream: %v", err)
	}
	defer up.Close()

	gw := NewGateway(store, engine.New(store, nil), nil)
	if err := gw.AddUpstream(ctx, up); err != nil {
		t.Fatalf("AddUpstream: %v", err)
	}
	if gw.CatalogSize() != 2 {
		t.Fatalf("CatalogSize=%d want 2", gw.CatalogSize())
	}

	// Verify search finds stdio-discovered tools.
	searchRes := gw.handleSearchTools(json.RawMessage(`{"query":"list items"}`))
	if searchRes.IsError || !strings.Contains(searchRes.Content[0].Text, "tracker.list_items") {
		t.Fatalf("search unexpected: %+v", searchRes)
	}

	// Verify multi-step pipeline chains stdio MCP calls and archives large raw payload in CCR.
	execRes := gw.handleExecute(json.RawMessage(`{
		"steps": [
			{"id":"list","op":"call","tool":"tracker.list_items","args":{"state":"open"}},
			{"id":"top2","op":"slice","from":"$list.items","start":0,"end":2},
			{"id":"details","op":"call","tool":"tracker.get_item","for_each":"$top2","args":{"id":"$item.id"}}
		],
		"return_step": "details"
	}`))
	if execRes.IsError {
		t.Fatalf("execute failed: %+v", execRes)
	}
	var decoded struct {
		Result     []map[string]any  `json:"result"`
		ToolCalls  int               `json:"tool_calls"`
		CCRHandles map[string]string `json:"ccr_handles"`
	}
	if err := json.Unmarshal([]byte(execRes.Content[0].Text), &decoded); err != nil {
		t.Fatalf("unmarshal exec result: %v (%s)", err, execRes.Content[0].Text)
	}
	if decoded.ToolCalls != 3 {
		t.Fatalf("tool_calls=%d want 3", decoded.ToolCalls)
	}
	if len(decoded.Result) != 2 || decoded.Result[0]["summary"] != "Detail for item 1" {
		t.Fatalf("unexpected details: %+v", decoded.Result)
	}
	handle := decoded.CCRHandles["list"]
	if handle == "" {
		t.Fatalf("expected CCR handle for large raw list response, got %+v", decoded.CCRHandles)
	}
	ret := gw.handleRetrieve(json.RawMessage(fmt.Sprintf(`{"recovery_handle":%q}`, handle)))
	if ret.IsError || !strings.Contains(ret.Content[0].Text, "Mock Issue #1") {
		t.Fatalf("retrieve failed: %+v", ret)
	}

	// Verify embedded QuickJS drives the live StdioUpstream end-to-end too.
	jsStdioRes := gw.handleExecute(json.RawMessage(`{
		"code": "const { items } = await mcp.tracker.list_items({ state: 'open' }); const first = await mcp.tracker.get_item({ id: items[0].id }); return { total: items.length, first };"
	}`))
	if jsStdioRes.IsError || !strings.Contains(jsStdioRes.Content[0].Text, "Detail for item 1") || !strings.Contains(jsStdioRes.Content[0].Text, `"total":6`) {
		t.Fatalf("QuickJS over StdioUpstream failed: %+v", jsStdioRes)
	}
}

func TestJSRunnerChainingAndSandboxIsolation(t *testing.T) {
	// Zero external runtime dependency: runs inside embedded pure-Go QuickJS
	// with no Node.js required on PATH.
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var maxConcurrent atomic.Int32
	var curConcurrent atomic.Int32
	var deletedID atomic.Int64

	up := NewInProcessUpstream("gh")
	up.Register(ToolSpec{
		Name: "list_prs",
		InputSchema: mcp.ObjectSchema(map[string]any{
			"state": mcp.StringProp("open|closed"),
		}),
	}, func(_ context.Context, _ map[string]any) (any, error) {
		return map[string]any{
			"prs": []map[string]any{
				{"number": 101, "title": "Fix cache", "checks": "failing", "padding": strings.Repeat("x", 1500)},
				{"number": 102, "title": "Docs typo", "checks": "passing", "padding": strings.Repeat("y", 1500)},
				{"number": 103, "title": "Add codemode", "checks": "failing", "padding": strings.Repeat("z", 1500)},
			},
		}, nil
	})
	up.Register(ToolSpec{
		Name: "get_ci_summary",
		InputSchema: mcp.ObjectSchema(map[string]any{
			"number": map[string]any{"type": "integer"},
		}, "number"),
	}, func(_ context.Context, args map[string]any) (any, error) {
		c := curConcurrent.Add(1)
		for {
			prev := maxConcurrent.Load()
			if c <= prev || maxConcurrent.CompareAndSwap(prev, c) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		curConcurrent.Add(-1)
		return map[string]any{
			"number": args["number"],
			"error":  fmt.Sprintf("FAIL TestPR%v", args["number"]),
		}, nil
	})
	up.Register(ToolSpec{
		Name:        "delete_branch",
		Annotations: &ToolAnnotations{ReadOnlyHint: boolPtr(false), DestructiveHint: boolPtr(true)},
		InputSchema: mcp.ObjectSchema(map[string]any{
			"id": map[string]any{"type": "integer"},
		}, "id"),
	}, func(_ context.Context, args map[string]any) (any, error) {
		if n, ok := args["id"].(json.Number); ok {
			v, _ := n.Int64()
			deletedID.Store(v)
		}
		return map[string]any{"deleted": args["id"]}, nil
	})

	gw := NewGateway(store, engine.New(store, nil), nil)
	if err := gw.AddUpstream(context.Background(), up); err != nil {
		t.Fatal(err)
	}

	// 1. Multi-step async QuickJS fan-out with true parallel Promise.all + filtering + CCR archival.
	code := `
const { prs } = await mcp.gh.list_prs({ state: "open" });
const failing = prs.filter(p => p.checks === "failing");
const details = await Promise.all(failing.map(p => mcp.gh.get_ci_summary({ number: p.number })));
console.log("checked", failing.length);
return { count: failing.length, failures: details };
`
	rawArgs, _ := json.Marshal(map[string]any{"code": code})
	res := gw.handleExecute(rawArgs)
	if res.IsError {
		t.Fatalf("QuickJS execute failed: %+v", res)
	}
	var got struct {
		Result struct {
			Count    int              `json:"count"`
			Failures []map[string]any `json:"failures"`
		} `json:"result"`
		ToolCalls  int               `json:"tool_calls"`
		Logs       []string          `json:"logs"`
		CCRHandles map[string]string `json:"ccr_handles"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].Text), &got); err != nil {
		t.Fatalf("decode QuickJS result: %v (%s)", err, res.Content[0].Text)
	}
	if got.Result.Count != 2 || got.ToolCalls != 3 || len(got.Result.Failures) != 2 {
		t.Fatalf("unexpected QuickJS result: %+v", got)
	}
	if maxConcurrent.Load() < 2 {
		t.Fatalf("expected Promise.all calls to execute concurrently in Go, maxConcurrent=%d", maxConcurrent.Load())
	}
	if len(got.Logs) != 1 || got.Logs[0] != "checked 2" {
		t.Fatalf("unexpected logs: %+v", got.Logs)
	}
	if len(got.CCRHandles) == 0 {
		t.Fatalf("expected CCR handle for large raw PR payload")
	}

	// 2. Read/write permission split enforced inside QuickJS:
	//    codemode_execute blocks mutating call with cave_write_not_allowed,
	//    while codemode_mutate executes it cleanly.
	mutCode, _ := json.Marshal(map[string]any{"code": `return await mcp.gh.delete_branch({ id: 77 });`})
	blockedWrite := gw.handleExecute(mutCode)
	if !blockedWrite.IsError || !strings.Contains(blockedWrite.Content[0].Text, "cave_write_not_allowed") {
		t.Fatalf("expected cave_write_not_allowed in read-only QuickJS execute, got: %+v", blockedWrite)
	}
	if deletedID.Load() != 0 {
		t.Fatalf("mutating handler ran despite read-only block")
	}
	allowedWrite := gw.handleMutate(mutCode)
	if allowedWrite.IsError || deletedID.Load() != 77 {
		t.Fatalf("codemode_mutate QuickJS failed: deletedID=%d res=%+v", deletedID.Load(), allowedWrite)
	}

	// 3. Sandbox isolation: OS/host globals absent + bridge globals deleted + dynamic codegen blocked.
	absentGlobals := `
return [
  typeof require,
  typeof process,
  typeof fs,
  typeof fetch,
  typeof std,
  typeof os,
  typeof globalThis.__caveman_dispatch,
  typeof globalThis.__caveman_log,
  typeof globalThis.__caveman_finish,
].every(t => t === 'undefined') ? 'isolated' : 'leaked';
`
	absJSON, _ := json.Marshal(map[string]any{"code": absentGlobals})
	absRes := gw.handleExecute(absJSON)
	if absRes.IsError || !strings.Contains(absRes.Content[0].Text, `"isolated"`) {
		t.Fatalf("sandbox global isolation check failed: %+v", absRes)
	}

	blockedCodeGen := []string{
		`return eval('1+1');`,
		`return Function('return 1')();`,
		`return (async () => {}).constructor('return 1')();`,
		`return this.constructor.constructor('return 1')();`,
	}
	for i, snippet := range blockedCodeGen {
		argsJSON, _ := json.Marshal(map[string]any{"code": snippet})
		r := gw.handleExecute(argsJSON)
		if !r.IsError {
			t.Fatalf("codegen escape %d should have been blocked by QuickJS sandbox, got: %s", i, r.Content[0].Text)
		}
	}

	// 4. Infinite loop deterministically interrupted within timeout_ms -> cave_js_timeout.
	startLoop := time.Now()
	loopRes := gw.handleExecute(json.RawMessage(`{"code":"while (true) {}","timeout_ms":150}`))
	if !loopRes.IsError || !strings.Contains(loopRes.Content[0].Text, "cave_js_timeout") {
		t.Fatalf("expected cave_js_timeout on infinite loop, got %+v", loopRes)
	}
	if elapsed := time.Since(startLoop); elapsed > 5*time.Second {
		t.Fatalf("infinite loop interrupt took too long: %s", elapsed)
	}

	// 5. Memory bomb hits QuickJS SetMemoryLimit cleanly -> cave_js_oom (never crashes Go).
	gw.SetJSMemoryLimit(4 * 1024 * 1024)
	oomRes := gw.handleExecute(json.RawMessage(`{"code":"let a = []; for (let i = 0; i < 1000000; i++) a.push('x'.repeat(1024)); return a.length;","timeout_ms":5000}`))
	if !oomRes.IsError || !strings.Contains(oomRes.Content[0].Text, "cave_js_oom") {
		t.Fatalf("expected cave_js_oom on memory bomb, got %+v", oomRes)
	}
	gw.SetJSMemoryLimit(defaultJSMemoryLimit)

	// 6. Deep recursion hits QuickJS stack limit cleanly -> cave_js_error (never overflows Go stack).
	stackRes := gw.handleExecute(json.RawMessage(`{"code":"function f() { return f() + 1; } return f();"}`))
	if !stackRes.IsError || !strings.Contains(stackRes.Content[0].Text, "cave_js_error") {
		t.Fatalf("expected cave_js_error on stack overflow, got %+v", stackRes)
	}

	// 7. Unsettled promise with no in-flight MCP calls fails fast instead of hanging.
	unsettledRes := gw.handleExecute(json.RawMessage(`{"code":"return new Promise(() => {});","timeout_ms":5000}`))
	if !unsettledRes.IsError || !strings.Contains(unsettledRes.Content[0].Text, "unsettled promise") {
		t.Fatalf("expected unsettled promise error, got %+v", unsettledRes)
	}

	// 8. Disabled JS runner fails cleanly.
	gw.SetJSRunnerEnabled(false)
	disRes := gw.handleExecute(json.RawMessage(`{"code":"return 1;"}`))
	if !disRes.IsError || !strings.Contains(disRes.Content[0].Text, "cave_js_disabled") {
		t.Fatalf("expected cave_js_disabled, got %+v", disRes)
	}
}

func TestBuiltinRepoPRAndBrowseUpstreams(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	eng := engine.New(store, nil)

	ws := t.TempDir()
	goSrc := `package sample

import (
	"fmt"
	"strings"
)

// Helper formats a greeting across several verbose lines to ensure
// AST outline compression measurably reduces the file size.
func Greet(name string, times int) string {
	var parts []string
	for i := 0; i < times; i++ {
		if strings.TrimSpace(name) == "" {
			parts = append(parts, "hello stranger")
		} else {
			parts = append(parts, fmt.Sprintf("hello %s #%d", name, i))
		}
	}
	return strings.Join(parts, "\n")
}

func Farewell(name string) string {
	msg := "goodbye " + name
	for range 10 {
		msg = strings.TrimSpace(msg)
	}
	return msg
}
`
	if err := os.WriteFile(filepath.Join(ws, "sample.go"), []byte(goSrc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "notes.txt"), []byte(strings.Repeat("line of text\n", 250)), 0o600); err != nil {
		t.Fatal(err)
	}

	repoUp := NewRepoUpstream(ws, store, eng)
	// Mock `gh` CLI runner for deterministic PR & CI tests without network.
	mockGH := func(_ context.Context, _ string, name string, args ...string) ([]byte, []byte, error) {
		if name != "gh" {
			return nil, nil, fmt.Errorf("unexpected binary %q", name)
		}
		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "pr view"):
			return []byte(`{"number":42,"title":"Add codemode","state":"OPEN","isDraft":false,"headRefOid":"abc123","comments":[{"author":{"login":"alice"},"body":"Looks good!"}],"reviews":[{"author":{"login":"bob"},"state":"APPROVED","body":"Ship it"}]}`), nil, nil
		case strings.HasPrefix(joined, "pr checks"):
			return []byte(`[{"name":"go-test","state":"FAILURE","bucket":"fail","workflow":"CI"}]`), nil, errors.New("exit status 1")
		case strings.HasPrefix(joined, "pr diff"):
			return []byte(strings.Repeat("+added line\n", 300)), nil, nil
		case strings.HasPrefix(joined, "run list"):
			return []byte(`[{"databaseId":999,"conclusion":"failure","status":"completed"}]`), nil, nil
		case strings.HasPrefix(joined, "run view 999 --log-failed"):
			return []byte(strings.Repeat("2026-01-01 INFO step ok\n", 250) + "FAIL\tgithub.com/JuliusBrussee/caveman/mcp/codemode\t0.12s\n"), nil, nil
		default:
			return nil, []byte("unhandled gh args: " + joined), errors.New("unexpected")
		}
	}
	prUp := NewPRUpstream(ws, store, eng, mockGH)

	// Fake browse.Driver that verifies lazy construction!
	driverBooted := 0
	fakeDriver := &stubBrowseDriver{
		axJSON: makeStubAXTree(40),
	}
	browseUp := NewBrowseUpstream(eng, nil, func(_ context.Context) (browse.Driver, error) {
		driverBooted++
		return fakeDriver, nil
	})

	gw := NewGateway(store, eng, nil)
	for _, u := range []Upstream{repoUp, prUp, browseUp} {
		if err := gw.AddUpstream(context.Background(), u); err != nil {
			t.Fatalf("AddUpstream %s: %v", u.Name(), err)
		}
	}
	defer gw.Close()

	// Lazy invariant: registering browseUp MUST NOT boot Chrome!
	if driverBooted != 0 {
		t.Fatalf("browse driver booted eagerly (%d times) during AddUpstream", driverBooted)
	}

	// 1. repo.batch_read: multi-file + truncation + path traversal rejection.
	batchRes, err := repoUp.CallTool(context.Background(), "batch_read", map[string]any{
		"paths":              []any{"sample.go", "notes.txt", "../outside.txt"},
		"max_lines_per_file": 20,
	})
	if err != nil {
		t.Fatalf("batch_read: %v", err)
	}
	batchJSON, _ := json.Marshal(batchRes)
	if !strings.Contains(string(batchJSON), "escapes workspace root") {
		t.Fatalf("expected path traversal rejection, got %s", batchJSON)
	}
	if !strings.Contains(string(batchJSON), `"truncated":true`) || !strings.Contains(string(batchJSON), `"recovery_handle":"ccr_`) {
		t.Fatalf("expected bounded truncation with CCR handle, got %s", batchJSON)
	}

	// 2. repo.symbol_outline: compresses Go source into signatures.
	outRes, err := repoUp.CallTool(context.Background(), "symbol_outline", map[string]any{
		"paths": []any{"sample.go"},
	})
	if err != nil {
		t.Fatalf("symbol_outline: %v", err)
	}
	outJSON, _ := json.Marshal(outRes)
	if !strings.Contains(string(outJSON), "func Greet") || !strings.Contains(string(outJSON), "func Farewell") {
		t.Fatalf("symbol_outline missing signatures: %s", outJSON)
	}

	// 3. pr.pr_read: bounded diff + summary + checks + comments.
	prRes, err := prUp.CallTool(context.Background(), "pr_read", map[string]any{
		"number":         42,
		"sections":       []any{"summary", "checks", "comments", "diff"},
		"max_diff_lines": 25,
	})
	if err != nil {
		t.Fatalf("pr_read: %v", err)
	}
	prJSON, _ := json.Marshal(prRes)
	if !strings.Contains(string(prJSON), `"title":"Add codemode"`) || !strings.Contains(string(prJSON), `"go-test"`) || !strings.Contains(string(prJSON), `"truncated":true`) {
		t.Fatalf("pr_read unexpected: %s", prJSON)
	}

	// 4. pr.ci_logs: resolves run 999, tails & archives raw log.
	ciRes, err := prUp.CallTool(context.Background(), "ci_logs", map[string]any{
		"tail_lines": 10,
	})
	if err != nil {
		t.Fatalf("ci_logs: %v", err)
	}
	ciJSON, _ := json.Marshal(ciRes)
	if !strings.Contains(string(ciJSON), `"run_id":999`) || !strings.Contains(string(ciJSON), "FAIL") || !strings.Contains(string(ciJSON), `"recovery_handle":"ccr_`) {
		t.Fatalf("ci_logs unexpected: %s", ciJSON)
	}

	// 5. browse.browser_snapshot + browser_eval: boots lazily once and returns compressed AX view.
	snapRes, err := browseUp.CallTool(context.Background(), browse.ToolSnapshot, map[string]any{
		"url": "https://example.test",
	})
	if err != nil {
		t.Fatalf("browser_snapshot: %v", err)
	}
	if driverBooted != 1 {
		t.Fatalf("driverBooted=%d want 1", driverBooted)
	}
	snapJSON, _ := json.Marshal(snapRes)
	if !strings.Contains(string(snapJSON), `"recovery_handle":"ccr_`) || !strings.Contains(string(snapJSON), "Submit Order") {
		t.Fatalf("browser_snapshot unexpected: %s", snapJSON)
	}

	evalRes, err := browseUp.CallTool(context.Background(), browse.ToolEval, map[string]any{
		"expression": "document.title",
	})
	if err != nil {
		t.Fatalf("browser_eval: %v", err)
	}
	if driverBooted != 1 {
		t.Fatalf("driverBooted=%d want 1 on second call", driverBooted)
	}
	evalJSON, _ := json.Marshal(evalRes)
	if !strings.Contains(string(evalJSON), "Example Title") {
		t.Fatalf("browser_eval unexpected: %s", evalJSON)
	}
}

func TestDiscoverUserMCPConfigs(t *testing.T) {
	home := t.TempDir()
	ws := filepath.Join(t.TempDir(), "myrepo")
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}

	codexTOML := `
model = "gpt-5"

[mcp_servers.postgres]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-postgres"]
env = { PGHOST = "localhost" }

[mcp_servers.caveman]
command = "caveman-mcp"

[mcp_servers.linear]
command = "linear-mcp"
[mcp_servers.linear.env]
LINEAR_API_KEY = "test-key"
`
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(codexTOML), 0o600); err != nil {
		t.Fatal(err)
	}

	claudeJSON := fmt.Sprintf(`{
  "mcpServers": {
    "github": { "command": "github-mcp", "args": ["stdio"] },
    "caveman-codemode": { "command": "caveman", "args": ["codemode"] }
  },
  "projects": {
    %q: {
      "mcpServers": {
        "sentry": { "command": "sentry-mcp" }
      }
    }
  }
}`, ws)
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(claudeJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	projectMCP := `{
  "mcpServers": {
    "github": { "command": "github-mcp-local", "args": ["--repo", "demo"] },
    "remote_only": { "url": "https://example.invalid/mcp" }
  }
}`
	if err := os.WriteFile(filepath.Join(ws, ".mcp.json"), []byte(projectMCP), 0o600); err != nil {
		t.Fatal(err)
	}

	got := DiscoverUserMCPConfigs(ws, home)
	if _, self := got["caveman"]; self {
		t.Fatalf("self server 'caveman' must be excluded: %+v", got)
	}
	if _, self := got["caveman-codemode"]; self {
		t.Fatalf("self server 'caveman-codemode' must be excluded: %+v", got)
	}
	// Remote Streamable HTTP URL entries are now supported!
	if got["remote_only"].URL != "https://example.invalid/mcp" {
		t.Fatalf("expected remote Streamable HTTP entry to be preserved, got %+v", got["remote_only"])
	}
	if got["postgres"].Command != "npx" || got["postgres"].Env["PGHOST"] != "localhost" {
		t.Fatalf("postgres config mismatch: %+v", got["postgres"])
	}
	if got["linear"].Command != "linear-mcp" || got["linear"].Env["LINEAR_API_KEY"] != "test-key" {
		t.Fatalf("linear config mismatch: %+v", got["linear"])
	}
	if got["sentry"].Command != "sentry-mcp" {
		t.Fatalf("project sentry config mismatch: %+v", got["sentry"])
	}
	// Project .mcp.json overrides ~/.claude.json
	if got["github"].Command != "github-mcp-local" {
		t.Fatalf("expected project .mcp.json override for github, got %+v", got["github"])
	}
}

func TestDiscoverMultiAgentConfigsGeminiOpenCodeCursorOpenClawHermes(t *testing.T) {
	home := t.TempDir()
	ws := filepath.Join(t.TempDir(), "ws")
	for _, dir := range []string{
		filepath.Join(home, ".cursor"),
		filepath.Join(home, ".gemini"),
		filepath.Join(home, ".config", "opencode"),
		filepath.Join(home, ".openclaw"),
		filepath.Join(home, ".hermes"),
		ws,
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	_ = os.WriteFile(filepath.Join(home, ".cursor", "mcp.json"), []byte(`{"mcpServers":{"stripe":{"url":"https://mcp.stripe.invalid/v1","headers":{"Authorization":"Bearer ${STRIPE_KEY}"}}}}`), 0o600)
	_ = os.WriteFile(filepath.Join(home, ".gemini", "settings.json"), []byte(`{"mcpServers":{"bigquery":{"command":"bq-mcp","args":["--readonly"]}}}`), 0o600)
	_ = os.WriteFile(filepath.Join(home, ".config", "opencode", "opencode.json"), []byte(`{"mcp":{"notion":{"type":"remote","url":"https://mcp.notion.invalid"},"localtool":{"type":"local","command":["local-mcp","serve"],"enabled":true},"disabled":{"type":"local","command":["skip-me"],"enabled":false}}}`), 0o600)
	_ = os.WriteFile(filepath.Join(home, ".openclaw", "openclaw.json"), []byte(`{"mcp":{"servers":{"slack":{"command":"slack-mcp"}}}}`), 0o600)
	_ = os.WriteFile(filepath.Join(home, ".hermes", "config.yaml"), []byte("mcp_servers:\n  datadog:\n    command: dd-mcp\n    args: [\"--site\", \"us1\"]\n"), 0o600)

	got := DiscoverUserMCPConfigs(ws, home)
	if got["stripe"].URL != "https://mcp.stripe.invalid/v1" {
		t.Fatalf("cursor stripe missing: %+v", got)
	}
	if got["bigquery"].Command != "bq-mcp" {
		t.Fatalf("gemini bigquery missing: %+v", got)
	}
	if got["notion"].URL != "https://mcp.notion.invalid" || got["localtool"].Command != "local-mcp" {
		t.Fatalf("opencode servers missing: %+v", got)
	}
	if _, ok := got["disabled"]; ok {
		t.Fatalf("disabled opencode server should be skipped")
	}
	if got["slack"].Command != "slack-mcp" {
		t.Fatalf("openclaw slack missing: %+v", got)
	}
	if got["datadog"].Command != "dd-mcp" || len(got["datadog"].Args) != 2 {
		t.Fatalf("hermes datadog mismatch: %+v", got["datadog"])
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHTTPUpstreamJSONAndSSEAndReadWriteSplit(t *testing.T) {
	t.Setenv("TEST_MCP_TOKEN", "secret-tok-123")
	var sawAuth, sawSession string
	client := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			sawAuth = r.Header.Get("Authorization")
			if sid := r.Header.Get("Mcp-Session-Id"); sid != "" {
				sawSession = sid
			}
			body, _ := io.ReadAll(r.Body)
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				} `json:"params"`
			}
			_ = json.Unmarshal(body, &req)
			hdr := http.Header{"Mcp-Session-Id": []string{"sess-xyz"}}
			switch req.Method {
			case "initialize":
				hdr.Set("Content-Type", "application/json")
				return &http.Response{
					StatusCode: 200, Header: hdr,
					Body: io.NopCloser(bytes.NewBufferString(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}}}}`, req.ID))),
				}, nil
			case "notifications/initialized":
				return &http.Response{StatusCode: 202, Header: hdr, Body: io.NopCloser(bytes.NewBuffer(nil))}, nil
			case "tools/list":
				// Return SSE stream response to verify text/event-stream framing!
				hdr.Set("Content-Type", "text/event-stream")
				sse := fmt.Sprintf("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"tools\":[{\"name\":\"list_tickets\",\"description\":\"List tickets\",\"annotations\":{\"readOnlyHint\":true}},{\"name\":\"delete_ticket\",\"description\":\"Delete ticket\",\"annotations\":{\"readOnlyHint\":false,\"destructiveHint\":true}}]}}\n\n", req.ID)
				return &http.Response{StatusCode: 200, Header: hdr, Body: io.NopCloser(bytes.NewBufferString(sse))}, nil
			case "tools/call":
				hdr.Set("Content-Type", "application/json")
				innerText, _ := json.Marshal(map[string]any{"called": req.Params.Name, "id": req.Params.Arguments["id"]})
				payload, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"result": map[string]any{
						"content": []map[string]any{{"type": "text", "text": string(innerText)}},
					},
				})
				return &http.Response{StatusCode: 200, Header: hdr, Body: io.NopCloser(bytes.NewReader(payload))}, nil
			default:
				return &http.Response{StatusCode: 404, Header: hdr, Body: io.NopCloser(bytes.NewBufferString("not found"))}, nil
			}
		}),
	}

	ctx := context.Background()
	httpUp, err := StartHTTPUpstream(ctx, "linear", ServerConfig{
		URL:     "https://mcp.linear.invalid/mcp",
		Headers: map[string]string{"Authorization": "Bearer ${TEST_MCP_TOKEN}"},
	}, client, nil)
	if err != nil {
		t.Fatalf("StartHTTPUpstream: %v", err)
	}
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	gw := NewGateway(store, engine.New(store, nil), nil)
	if err := gw.AddUpstream(ctx, httpUp); err != nil {
		t.Fatalf("AddUpstream: %v", err)
	}
	if sawAuth != "Bearer secret-tok-123" || sawSession != "sess-xyz" {
		t.Fatalf("headers mismatch: auth=%q session=%q", sawAuth, sawSession)
	}

	// 1. Search exposes access:"read" vs access:"write".
	sr := gw.handleSearchTools(json.RawMessage(`{"query":"ticket"}`))
	if sr.IsError || !strings.Contains(sr.Content[0].Text, `"access":"read"`) || !strings.Contains(sr.Content[0].Text, `"access":"write"`) {
		t.Fatalf("expected read/write access tags in search output: %s", sr.Content[0].Text)
	}

	// 2. Read-only codemode_execute succeeds on list_tickets.
	okRes := gw.handleExecute(json.RawMessage(`{"steps":[{"id":"t","op":"call","tool":"linear.list_tickets","args":{"id":1}}]}`))
	if okRes.IsError {
		t.Fatalf("read-only tool should succeed in codemode_execute: %+v", okRes)
	}

	// 3. Read-only codemode_execute BLOCKS delete_ticket with cave_write_not_allowed!
	blocked := gw.handleExecute(json.RawMessage(`{"steps":[{"id":"d","op":"call","tool":"linear.delete_ticket","args":{"id":7}}]}`))
	if !blocked.IsError || !strings.Contains(blocked.Content[0].Text, "cave_write_not_allowed") {
		t.Fatalf("mutating tool must be blocked in codemode_execute, got: %+v", blocked)
	}

	// 4. codemode_mutate executes single-tool shortcut cleanly!
	mutRes := gw.handleMutate(json.RawMessage(`{"tool":"linear.delete_ticket","args":{"id":7}}`))
	if mutRes.IsError || !strings.Contains(mutRes.Content[0].Text, "delete_ticket") {
		t.Fatalf("codemode_mutate failed: %+v", mutRes)
	}
}

func TestRepoSearchCode(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ws := t.TempDir()
	_ = os.MkdirAll(filepath.Join(ws, ".git"), 0o700)
	_ = os.WriteFile(filepath.Join(ws, ".git", "config"), []byte("needle inside git should be skipped\n"), 0o600)
	_ = os.WriteFile(filepath.Join(ws, "a.go"), []byte("package a\n// needle one\nfunc needleFunc() {}\n"), 0o600)
	_ = os.WriteFile(filepath.Join(ws, "b.ts"), []byte("export const other = 1;\n"), 0o600)

	repoUp := NewRepoUpstream(ws, store, engine.New(store, nil))
	res, err := repoUp.CallTool(context.Background(), "search_code", map[string]any{
		"query": "needle",
		"glob":  "*.go",
	})
	if err != nil {
		t.Fatalf("search_code: %v", err)
	}
	raw, _ := json.Marshal(res)
	if !strings.Contains(string(raw), `"total_matches":2`) || strings.Contains(string(raw), ".git/config") {
		t.Fatalf("unexpected search_code output: %s", raw)
	}
}

type stubBrowseDriver struct {
	axJSON []byte
	closed bool
}

func (d *stubBrowseDriver) Snapshot(_ context.Context, _ string, _ time.Duration) ([]byte, error) {
	return d.axJSON, nil
}
func (d *stubBrowseDriver) Act(_ context.Context, _ browse.ActionRequest, _ browse.Target) (browse.ActionResult, error) {
	return browse.ActionResult{OK: true}, nil
}
func (d *stubBrowseDriver) Eval(_ context.Context, _ string) (any, error) {
	return "Example Title", nil
}
func (d *stubBrowseDriver) Close() error { d.closed = true; return nil }

func makeStubAXTree(extraNoise int) []byte {
	nodes := []map[string]any{
		{
			"nodeId": "1", "role": map[string]any{"value": "RootWebArea"},
			"name":     map[string]any{"value": "Checkout Page"},
			"childIds": []string{"2", "3"},
		},
		{
			"nodeId": "2", "backendDOMNodeId": 101,
			"role": map[string]any{"value": "button"},
			"name": map[string]any{"value": "Submit Order"},
		},
	}
	children := []string{}
	for i := 0; i < extraNoise; i++ {
		id := fmt.Sprintf("n%d", i+10)
		children = append(children, id)
		nodes = append(nodes, map[string]any{
			"nodeId": id, "ignored": true,
			"role": map[string]any{"value": "generic"},
			"name": map[string]any{"value": strings.Repeat("layout-wrapper-noise ", 4)},
		})
	}
	nodes = append(nodes, map[string]any{
		"nodeId": "3", "role": map[string]any{"value": "generic"},
		"childIds": children,
	})
	raw, _ := json.Marshal(map[string]any{"nodes": nodes})
	return raw
}

func TestHermesYAMLAndCodexTOMLHardenedParsing(t *testing.T) {
	hermesYAML := `
model:
  name: gpt-5.4
mcp_servers:
    prod_api:
        url: https://mcp.example.com/v2#anchor # trailing comment
        headers: { Authorization: "Bearer ${env:HERMES_TOKEN}" }
        tools:
            include:
                - search_docs
                - get_doc
            exclude: [delete_doc]
    disabled_srv:
        command: skip-me
        enabled: false
    cli_tool:
        command: my-mcp
        args:
            - --flag
            - value
        env:
            MODE: prod
`
	parsed := ParseHermesMCPConfig([]byte(hermesYAML))
	if _, hasDisabled := parsed["disabled_srv"]; hasDisabled {
		t.Fatalf("enabled: false Hermes server must be skipped: %+v", parsed)
	}
	prod := parsed["prod_api"]
	if prod.URL != "https://mcp.example.com/v2#anchor" {
		t.Fatalf("URL fragment was truncated by comment stripper: %q", prod.URL)
	}
	if prod.Headers["Authorization"] != "Bearer ${env:HERMES_TOKEN}" {
		t.Fatalf("inline flow headers mismatch: %+v", prod.Headers)
	}
	if len(prod.IncludeTools) != 2 || prod.IncludeTools[0] != "search_docs" || len(prod.ExcludeTools) != 1 || prod.ExcludeTools[0] != "delete_doc" {
		t.Fatalf("Hermes tools include/exclude mismatch: include=%v exclude=%v", prod.IncludeTools, prod.ExcludeTools)
	}
	cli := parsed["cli_tool"]
	if cli.Command != "my-mcp" || len(cli.Args) != 2 || cli.Env["MODE"] != "prod" {
		t.Fatalf("4-space Hermes YAML mismatch: %+v", cli)
	}

	// Verify Hermes ${env:VAR} expansion works alongside ${VAR}.
	t.Setenv("HERMES_TOKEN", "htok_999")
	if got := expandHeaderEnv("Bearer ${env:HERMES_TOKEN}"); got != "Bearer htok_999" {
		t.Fatalf("expandHeaderEnv(${env:HERMES_TOKEN}) = %q", got)
	}

	// Verify filteredUpstream enforces include/exclude in both ListTools and CallTool.
	base := &stubNamedUpstream{
		name: "prod_api",
		tools: []ToolSpec{
			{Name: "search_docs", Description: "Search"},
			{Name: "get_doc", Description: "Get"},
			{Name: "delete_doc", Description: "Delete"},
		},
	}
	filtered := newFilteredUpstream(base, []string{"search_docs", "delete_doc"}, []string{"delete_doc"})
	listed, err := filtered.ListTools(context.Background())
	if err != nil || len(listed) != 1 || listed[0].Name != "search_docs" {
		t.Fatalf("filtered ListTools = %+v err=%v", listed, err)
	}
	if _, err := filtered.CallTool(context.Background(), "delete_doc", nil); err == nil {
		t.Fatalf("excluded tool must fail CallTool")
	}

	// Codex TOML hardening: quoted dotted names, multiline args, bearer_token_env_var,
	// env_http_headers, env_vars, enabled = false.
	codexTOML := `
[mcp_servers."my.dotted.srv"]
url = "https://codex.example.com/mcp"
bearer_token_env_var = "CODEX_MCP_KEY"
env_http_headers = { "X-Tenant" = "TENANT_ID" }

[mcp_servers."my.dotted.srv".http_headers]
X-Static = "hello"

[mcp_servers.multi]
command = "node"
args = [
  "server.js",
  "--stdio"
]
env_vars = ["HOME", "AWS_REGION"]

[mcp_servers.turned_off]
command = "never"
enabled = false
`
	cp := ParseCodexMCPConfig([]byte(codexTOML))
	if _, hasOff := cp["turned_off"]; hasOff {
		t.Fatalf("enabled=false Codex server must be skipped")
	}
	dotted := cp["my.dotted.srv"]
	if dotted.URL != "https://codex.example.com/mcp" || dotted.Headers["Authorization"] != "Bearer ${CODEX_MCP_KEY}" || dotted.Headers["X-Tenant"] != "${TENANT_ID}" || dotted.Headers["X-Static"] != "hello" {
		t.Fatalf("dotted Codex server mismatch: %+v", dotted)
	}
	multi := cp["multi"]
	if multi.Command != "node" || len(multi.Args) != 2 || multi.Args[1] != "--stdio" || multi.Env["AWS_REGION"] != "${AWS_REGION}" {
		t.Fatalf("multiline Codex server mismatch: %+v", multi)
	}
}

type stubNamedUpstream struct {
	name  string
	tools []ToolSpec
}

func (s *stubNamedUpstream) Name() string                                             { return s.name }
func (s *stubNamedUpstream) ListTools(context.Context) ([]ToolSpec, error)            { return s.tools, nil }
func (s *stubNamedUpstream) CallTool(context.Context, string, map[string]any) (any, error) { return "ok", nil }

func TestBrowseUpstreamSurvivesCanceledFirstCallContext(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var bootCtx context.Context
	up := NewBrowseUpstream(engine.New(store, nil), nil, func(ctx context.Context) (browse.Driver, error) {
		bootCtx = ctx
		return &stubBrowseDriver{axJSON: makeStubAXTree(5)}, nil
	})
	defer up.Close()

	call1Ctx, cancel1 := context.WithCancel(context.Background())
	if _, err := up.CallTool(call1Ctx, browse.ToolSnapshot, map[string]any{"url": "https://example.test"}); err != nil {
		t.Fatalf("call 1: %v", err)
	}
	// Cancel call 1's context — must NOT cancel the browser lifecycle context!
	cancel1()
	if bootCtx.Err() != nil {
		t.Fatalf("browser lifecycle context was canceled when call 1 ended: %v", bootCtx.Err())
	}
	if _, err := up.CallTool(context.Background(), browse.ToolEval, map[string]any{"expression": "1+1"}); err != nil {
		t.Fatalf("call 2 failed after call 1 ctx canceled: %v", err)
	}
}

func TestIsReadOnlyToolCompoundTokensAndQuickJSPrototypeHardening(t *testing.T) {
	for _, mutating := range []string{"get_and_delete-issue", "listAndDropTables", "read_or_update", "fetchAndCreateUser"} {
		if IsReadOnlyTool(ToolSpec{Name: mutating}) {
			t.Errorf("IsReadOnlyTool(%q) = true, want false (compound mutating token)", mutating)
		}
	}
	for _, safe := range []string{"get_issue", "listIssues", "search_code", "describeTable"} {
		if !IsReadOnlyTool(ToolSpec{Name: safe}) {
			t.Errorf("IsReadOnlyTool(%q) = false, want true", safe)
		}
	}

	// QuickJS prototype tampering must NOT crash or bypass completion.
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	gw := NewGateway(store, engine.New(store, nil), nil)
	if err := gw.AddUpstream(context.Background(), &stubNamedUpstream{
		name:  "demo",
		tools: []ToolSpec{{Name: "get_item", Description: "read"}},
	}); err != nil {
		t.Fatal(err)
	}
	jsCode := `
Function.prototype.call = function() { throw new Error("hijacked call"); };
Array.prototype.map = function() { throw new Error("hijacked map"); };
Map.prototype.get = function() { return null; };
const res = await mcp.demo.get_item({});
return { ok: res === "ok" };
`
	out := gw.handleExecute(json.RawMessage(fmt.Sprintf(`{"code":%q}`, jsCode)))
	if out.IsError || !strings.Contains(out.Content[0].Text, `"ok":true`) {
		t.Fatalf("QuickJS prototype hardening failed: %+v", out)
	}
}
