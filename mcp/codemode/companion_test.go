package codemode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
)

func TestCompanionToolsFlatSurfaceAnnotationsAndCCR(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ws := t.TempDir()
	_ = os.WriteFile(filepath.Join(ws, "hello.go"), []byte("package demo\n"+strings.Repeat("// comment line\n", 120)+"func Hello() string { return \"hi\" }\n"), 0o600)

	tools, cleanup := CompanionTools(ws, store, engine.New(store, nil), nil, true)
	defer func() { _ = cleanup() }()

	byName := map[string]int{}
	for i, tool := range tools {
		byName[tool.Name] = i
	}
	expected := []string{
		"repo_batch_read",
		"repo_symbol_outline",
		"repo_search_code",
		"pr_read",
		"pr_ci_logs",
		"browser_snapshot",
		"browser_act",
		"browser_eval",
		"caveman_retrieve",
	}
	for _, name := range expected {
		if _, ok := byName[name]; !ok {
			t.Fatalf("CompanionTools missing %q (have %v)", name, byName)
		}
	}

	// Verify read-only vs write annotations for native host permission prompts.
	if ro, _ := tools[byName["repo_batch_read"]].Annotations["readOnlyHint"].(bool); !ro {
		t.Fatalf("repo_batch_read must be annotated readOnlyHint=true")
	}
	if ro, _ := tools[byName["browser_act"]].Annotations["readOnlyHint"].(bool); ro {
		t.Fatalf("browser_act must be annotated readOnlyHint=false")
	}

	// Invoke repo_batch_read directly (flat call, no codemode_execute wrapper) and recover via caveman_retrieve.
	res := tools[byName["repo_batch_read"]].Handler(json.RawMessage(`{"paths":["hello.go"],"max_lines_per_file":10}`))
	if res.IsError || !strings.Contains(res.Content[0].Text, `"truncated":true`) {
		t.Fatalf("repo_batch_read unexpected: %+v", res)
	}
	var decoded struct {
		Files []struct {
			RecoveryHandle string `json:"recovery_handle"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].Text), &decoded); err != nil || len(decoded.Files) != 1 || decoded.Files[0].RecoveryHandle == "" {
		t.Fatalf("missing recovery_handle: %s", res.Content[0].Text)
	}
	ret := tools[byName["caveman_retrieve"]].Handler(json.RawMessage(`{"recovery_handle":"` + decoded.Files[0].RecoveryHandle + `"}`))
	if ret.IsError || !strings.Contains(ret.Content[0].Text, "func Hello()") {
		t.Fatalf("caveman_retrieve failed: %+v", ret)
	}

	// Verify CompanionToolsWithOptions can omit caveman_retrieve (--no-retrieve).
	noRetTools, cleanup2 := CompanionToolsWithOptions(ws, store, engine.New(store, nil), nil, CompanionOptions{IncludeBrowse: false, IncludeRetrieve: false})
	defer func() { _ = cleanup2() }()
	for _, tool := range noRetTools {
		if tool.Name == "caveman_retrieve" || strings.HasPrefix(tool.Name, "browser_") {
			t.Fatalf("unexpected tool %q when disabled", tool.Name)
		}
	}
}

func TestRepoSearchBlocksSymlinkEscapeAndPRCiLogsUsesNumber(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	outside := t.TempDir()
	secretFile := filepath.Join(outside, "secret.txt")
	_ = os.WriteFile(secretFile, []byte("TOP_SECRET_TOKEN_12345\n"), 0o600)

	ws := t.TempDir()
	_ = os.WriteFile(filepath.Join(ws, "ok.txt"), []byte("normal workspace line\n"), 0o600)
	_ = os.Symlink(secretFile, filepath.Join(ws, "leak.txt"))

	repo := NewRepoUpstream(ws, store, engine.New(store, nil))
	resAny, err := repo.CallTool(context.Background(), "search_code", map[string]any{"query": "TOP_SECRET_TOKEN_12345"})
	if err != nil {
		t.Fatalf("search_code: %v", err)
	}
	res := resAny.(map[string]any)
	if res["total_matches"].(int) != 0 {
		t.Fatalf("search_code must NOT follow symlinks outside workspace, got: %+v", res)
	}

	// Verify pr_ci_logs honors args["number"] to locate the PR's failing Actions run!
	var sawRunViewID string
	pr := NewPRUpstream(ws, store, engine.New(store, nil), func(_ context.Context, _, name string, args ...string) ([]byte, []byte, error) {
		if name == "gh" && len(args) >= 3 && args[0] == "pr" && args[1] == "checks" && args[2] == "42" {
			return []byte(`[{"name":"ci","state":"FAILURE","bucket":"fail","link":"https://github.com/org/repo/actions/runs/987654321/job/1"}]`), nil, nil
		}
		if name == "gh" && len(args) >= 3 && args[0] == "run" && args[1] == "view" {
			sawRunViewID = args[2]
			return []byte("--- FAIL: TestPR42\n"), nil, nil
		}
		return nil, []byte("unexpected gh call: " + strings.Join(args, " ")), os.ErrInvalid
	})
	ciOut, err := pr.CallTool(context.Background(), "ci_logs", map[string]any{"number": 42})
	if err != nil {
		t.Fatalf("ci_logs(number=42): %v", err)
	}
	if sawRunViewID != "987654321" {
		t.Fatalf("ci_logs ignored PR number 42; fetched run %q instead of 987654321 (out=%+v)", sawRunViewID, ciOut)
	}
}
