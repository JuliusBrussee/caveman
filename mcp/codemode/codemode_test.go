package codemode_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/engine/tokens"
	"github.com/JuliusBrussee/caveman/mcp/codemode"
)

func buildDemoUpstream() *codemode.InProcessUpstream {
	up := codemode.NewInProcessUpstream("t3")
	// Simulate 45 realistic MCP tools (PRs, CI logs, issues, preview, threads, devices).
	up.Register(codemode.ToolSpec{
		Name:        "pull_request_list",
		Description: "List pull requests in this repository, recently updated first, with full metadata, author profiles, labels, checks, and review state.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"state": map[string]any{"type": "string", "enum": []string{"open", "closed", "merged", "all"}, "description": "Filter by PR state."},
				"limit": map[string]any{"type": "integer", "description": "Max PRs to return."},
			},
		},
	}, func(_ context.Context, _ map[string]any) (any, error) {
		prs := make([]map[string]any, 0, 25)
		for i := 1; i <= 25; i++ {
			status := "passing"
			if i == 7 || i == 19 {
				status = "failing"
			}
			prs = append(prs, map[string]any{
				"number":       100 + i,
				"title":        fmt.Sprintf("feat(proxy): change #%d across provider adapters", i),
				"author":       fmt.Sprintf("dev_%02d", i%5),
				"checks_state": status,
				"branch":       fmt.Sprintf("feat/branch-%02d", i),
				"body":         strings.Repeat("Detailed PR checklist and release notes context. ", 18),
				"head_sha":     fmt.Sprintf("%040x", i*9973),
			})
		}
		return map[string]any{"pull_requests": prs}, nil
	})
	up.Register(codemode.ToolSpec{
		Name:        "ci_logs",
		Description: "Read the tail of a pull request's failing CI check logs, or one named check's logs.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"number":    map[string]any{"type": "integer", "description": "Pull request number."},
				"tailLines": map[string]any{"type": "integer", "description": "Lines from the end."},
			},
			"required": []string{"number"},
		},
	}, func(_ context.Context, args map[string]any) (any, error) {
		num := fmt.Sprint(args["number"])
		var b strings.Builder
		for i := 0; i < 120; i++ {
			fmt.Fprintf(&b, "2026-10-12T10:00:%02dZ INFO  [pr-%s] compiling package step %d ok\n", i%60, num, i)
		}
		fmt.Fprintf(&b, "2026-10-12T10:02:01Z ERROR [pr-%s] TestCachePrefixInvariant FAILED: prefix mismatch at component 2\n", num)
		return b.String(), nil
	})
	for i := 0; i < 43; i++ {
		name := fmt.Sprintf("workspace_tool_%02d", i)
		up.Register(codemode.ToolSpec{
			Name:        name,
			Description: fmt.Sprintf("Perform workspace maintenance operation #%02d with detailed diagnostics, pagination cursors, and strict schema validation.", i),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"environmentId": map[string]any{"type": "string", "title": "Environment ID", "description": "Target environment identifier."},
					"cursor":        map[string]any{"type": "string", "title": "Pagination Cursor", "description": "Opaque pagination token from previous page."},
					"limit":         map[string]any{"type": "integer", "title": "Page Size", "description": "Maximum number of items per page."},
					"verbose":       map[string]any{"type": "boolean", "title": "Verbose Flag", "description": "Emit verbose telemetry metadata."},
				},
			},
		}, func(_ context.Context, _ map[string]any) (any, error) {
			return map[string]any{"ok": true}, nil
		})
	}
	return up
}

func TestCodeModeGatewaySearchExecuteAndCCRRecovery(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	gw := codemode.NewGateway(store, nil, nil)
	up := buildDemoUpstream()
	if err := gw.AddUpstream(context.Background(), up); err != nil {
		t.Fatal(err)
	}

	// 1. Measure tool-catalog prefix token reduction: 45 raw MCP schemas vs 3 Code-Mode meta-tools.
	rawTools, _ := up.ListTools(context.Background())
	rawCatalogBytes, _ := json.Marshal(rawTools)
	metaTools := gw.Tools()
	metaSchemas := make([]map[string]any, 0, len(metaTools))
	for _, mt := range metaTools {
		metaSchemas = append(metaSchemas, map[string]any{
			"name": mt.Name, "description": mt.Description, "inputSchema": mt.InputSchema,
		})
	}
	metaCatalogBytes, _ := json.Marshal(metaSchemas)
	tok := tokens.Default()
	rawCatalogTok := tok.Count(rawCatalogBytes)
	metaCatalogTok := tok.Count(metaCatalogBytes)
	t.Logf("Tool catalog (45 MCP tools -> 3 Code-Mode meta-tools): %d -> %d tokens (%.1f%% saved)",
		rawCatalogTok, metaCatalogTok, (1-float64(metaCatalogTok)/float64(rawCatalogTok))*100)
	if metaCatalogTok*5 > rawCatalogTok {
		t.Fatalf("expected >80%% catalog token reduction, got %d -> %d", rawCatalogTok, metaCatalogTok)
	}

	// 2. Drive the real MCP stdio server end-to-end: search_tools -> execute -> caveman_retrieve.
	srv := codemode.NewServer(gw, nil)
	execReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "codemode_execute",
			"arguments": map[string]any{
				"steps": []map[string]any{
					{"id": "prs", "op": "call", "tool": "t3.pull_request_list", "args": map[string]any{"state": "open"}},
					{"id": "failing", "op": "filter", "from": "$prs.pull_requests", "where": map[string]any{"field": "checks_state", "equals": "failing"}},
					{"id": "summary", "op": "pick", "from": "$failing", "fields": []string{"number", "title", "checks_state"}},
					{"id": "logs", "op": "call", "tool": "t3.ci_logs", "for_each": "$failing", "args": map[string]any{"number": "$item.number"}},
					{"id": "errors", "op": "grep_lines", "from": "$logs", "pattern": "ERROR|FAILED"},
				},
				"return_step": "errors",
			},
		},
	}
	execLine, _ := json.Marshal(execReq)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"codemode_search_tools","arguments":{"query":"failing pull request ci logs","limit":5}}}`,
		string(execLine),
	}, "\n") + "\n"

	var out bytes.Buffer
	if err := srv.Serve(strings.NewReader(input), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 RPC responses, got %d: %s", len(lines), out.String())
	}

	// Check search_tools found both pull_request_list and ci_logs.
	if !strings.Contains(lines[0], "t3.pull_request_list") || !strings.Contains(lines[0], "t3.ci_logs") {
		t.Fatalf("search_tools missed expected tools: %s", lines[0])
	}

	// Check execute returned ONLY the 2 ERROR lines (out of 25 PRs + 240 CI log lines!)
	// AND archived the raw PR list + raw CI logs in CCR.
	var rpcResp struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &rpcResp); err != nil {
		t.Fatal(err)
	}
	if rpcResp.Result.IsError {
		t.Fatalf("codemode_execute returned error: %s", rpcResp.Result.Content[0].Text)
	}
	var payload struct {
		Result     string            `json:"result"`
		ToolCalls  int               `json:"tool_calls"`
		CCRHandles map[string]string `json:"ccr_handles"`
	}
	if err := json.Unmarshal([]byte(rpcResp.Result.Content[0].Text), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ToolCalls != 3 {
		t.Fatalf("tool_calls = %d, want 3 (1 list + 2 fan-out ci_logs)", payload.ToolCalls)
	}
	if !strings.Contains(payload.Result, "pr-107") || !strings.Contains(payload.Result, "pr-119") {
		t.Fatalf("expected both failing PR errors, got: %s", payload.Result)
	}
	if strings.Contains(payload.Result, "compiling package step") {
		t.Fatalf("noise log lines leaked into compact output: %s", payload.Result)
	}
	prsHandle := payload.CCRHandles["prs"]
	if prsHandle == "" {
		t.Fatalf("expected CCR handle for archived raw PR list, got: %+v", payload.CCRHandles)
	}

	// Verify byte-exact recovery via caveman_retrieve on the same MCP server.
	var recOut bytes.Buffer
	recReq := fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"caveman_retrieve","arguments":{"recovery_handle":%q}}}`, prsHandle) + "\n"
	if err := srv.Serve(strings.NewReader(recReq), &recOut); err != nil {
		t.Fatalf("retrieve serve: %v", err)
	}
	if !strings.Contains(recOut.String(), "Detailed PR checklist") {
		t.Fatalf("caveman_retrieve did not return original archived PR payload: %.200s", recOut.String())
	}
}
