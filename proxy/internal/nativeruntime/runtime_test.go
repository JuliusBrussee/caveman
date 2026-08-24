package nativeruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/proxy/internal/repointel"
	"github.com/JuliusBrussee/caveman/proxy/internal/sessionusage"
)

type fixedUsageReader struct{ value sessionusage.Snapshot }

func (r fixedUsageReader) SessionUsage(string) (sessionusage.Snapshot, error) { return r.value, nil }

func TestClassifyToolCoversClosedLedgerObjectTypes(t *testing.T) {
	tests := []struct {
		name       string
		tool       Tool
		wantType   ccr.ObjectType
		wantSource string
		wantDep    string
	}{
		{"file observation", Tool{Name: "read_file", Input: json.RawMessage(`{"path":"src/auth.ts"}`)}, ccr.ObjectFileObservation, "src/auth.ts", "src/auth.ts"},
		{"search result", Tool{Name: "rg", Input: json.RawMessage(`{"pattern":"authorize","path":"src"}`)}, ccr.ObjectSearchResult, "authorize", "src"},
		{"command result", Tool{Name: "exec_command", Input: json.RawMessage(`{"command":"git status"}`)}, ccr.ObjectCommandResult, "git status", ""},
		{"test result", Tool{Name: "exec_command", Input: json.RawMessage(`{"command":"go test ./..."}`)}, ccr.ObjectTestResult, "go test ./...", ""},
		{"build result", Tool{Name: "exec_command", Input: json.RawMessage(`{"command":"go build ./..."}`)}, ccr.ObjectBuildResult, "go build ./...", ""},
		{"diff snapshot", Tool{Name: "apply_patch", Input: json.RawMessage(`{"file_path":"src/auth.ts"}`)}, ccr.ObjectDiffSnapshot, "src/auth.ts", "src/auth.ts"},
		{"browser snapshot", Tool{Name: "browser_screenshot", Input: json.RawMessage(`{"url":"https://example.invalid"}`)}, ccr.ObjectBrowserSnapshot, "browser_screenshot", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotType, gotSource, gotDep := classifyTool(&test.tool)
			if gotType != test.wantType || gotSource != test.wantSource || gotDep != test.wantDep {
				t.Fatalf("classifyTool() = (%q, %q, %q), want (%q, %q, %q)", gotType, gotSource, gotDep, test.wantType, test.wantSource, test.wantDep)
			}
		})
	}
}

func TestRuntimeBuildsContractStoresExactOutputAndInformsOnRepeat(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	session := Session{ID: "s1", CWD: "/repo", RepositoryState: "git:abc"}

	if _, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1,
		Agent:           Agent{ID: "claude", Version: "1.0", Surface: "cli"},
		Session:         session,
		Event:           Event{Type: "prompt.submit"},
		Prompt:          &PayloadDigest{Bytes: 42, SHA256: "sha256:prompt"},
	}); err != nil {
		t.Fatal(err)
	}
	objects, err := store.ListSessionObjects("s1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if !containsType(objects, ccr.ObjectTaskContract) {
		t.Fatalf("prompt.submit did not create TaskContract: %+v", objects)
	}
	if !containsType(objects, ccr.ObjectTaskDecision) {
		t.Fatalf("prompt.submit did not append Decision Ledger record: %+v", objects)
	}

	output := strings.Repeat("auth refresh details\n", 300)
	after, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1,
		Agent:           Agent{ID: "claude", Surface: "cli"},
		Session:         session,
		Event:           Event{Type: "tool.after"},
		Tool: &Tool{
			Name:   "read_file",
			Input:  json.RawMessage(`{"path":"src/auth.ts"}`),
			Output: []byte(output),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if after.Action != "allow" || after.RecoveryRef == "" || after.OutputReplacement == "" {
		t.Fatalf("large exact output should be safely maskable: %+v", after)
	}
	objectID := strings.TrimPrefix(after.RecoveryRef, "ccr://")
	exact, err := store.Get(objectID)
	if err != nil || string(exact) != output {
		t.Fatalf("typed output was not byte-exact recoverable: bytes=%d err=%v", len(exact), err)
	}
	maskedObject, err := store.GetObject(objectID)
	if err != nil || maskedObject.Lifecycle != ccr.Warm {
		t.Fatalf("masked exact output must transition to warm lifecycle: lifecycle=%q err=%v", maskedObject.Lifecycle, err)
	}

	repeat, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1,
		Agent:           Agent{ID: "claude", Surface: "cli"},
		Session:         session,
		Event:           Event{Type: "tool.before"},
		Tool:            &Tool{Name: "read_file", Input: json.RawMessage(`{"path":"src/auth.ts"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if repeat.Action != "observe" || !strings.Contains(repeat.Context, after.RecoveryRef) {
		t.Fatalf("current repeated read should inform with exact reference: %+v", repeat)
	}
	objects, err = store.ListSessionObjects("s1", 100)
	if err != nil {
		t.Fatal(err)
	}
	decisionCount := 0
	for _, object := range objects {
		if object.Type == ccr.ObjectTaskDecision {
			decisionCount++
			if strings.Contains(string(object.Data), output) {
				t.Fatal("Decision Ledger copied raw tool output")
			}
		}
	}
	if decisionCount != 3 {
		t.Fatalf("Decision Ledger count = %d, want 3", decisionCount)
	}
}

func TestFileRepeatRequiresMatchingExternalInputState(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	session := Session{ID: "external-file", RepositoryState: "git:same"}
	after := Request{
		ProtocolVersion: 1, Agent: Agent{ID: "claude"}, Session: session, Event: Event{Type: "tool.after"},
		Tool: &Tool{Name: "read_file", InputState: "sha256:file-v1", Input: json.RawMessage(`{"path":"generated/client.ts"}`), Output: []byte("v1")},
	}
	if _, err := runtime.Handle(context.Background(), after); err != nil {
		t.Fatal(err)
	}
	current, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, Agent: Agent{ID: "claude"}, Session: session, Event: Event{Type: "tool.before"},
		Tool: &Tool{Name: "read_file", InputState: "sha256:file-v1", Input: json.RawMessage(`{"path":"generated/client.ts"}`)},
	})
	if err != nil || current.Action != "observe" {
		t.Fatalf("matching file state should inform: response=%+v err=%v", current, err)
	}
	changed, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, Agent: Agent{ID: "claude"}, Session: session, Event: Event{Type: "tool.before"},
		Tool: &Tool{Name: "read_file", InputState: "sha256:file-v2", Input: json.RawMessage(`{"path":"generated/client.ts"}`)},
	})
	if err != nil || changed.Action != "allow" || changed.RecoveryRef != "" || changed.Context != "" {
		t.Fatalf("external file change must suppress stale hint: response=%+v err=%v", changed, err)
	}
}

func TestTaskContractKeepsPolicyAcrossFollowupsAndInjectsOnlyOnPolicyChange(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	session := Session{ID: "task-profile", RepositoryState: "git:abc"}

	first, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1,
		PolicyMode:      "safe",
		Agent:           Agent{ID: "claude"},
		Session:         session,
		Event:           Event{Type: "prompt.submit"},
		Prompt:          &PayloadDigest{Bytes: 99, SHA256: "sha256:first"},
		TaskProfile:     &TaskProfile{Type: "feature"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Context, "dynamic tail") || !strings.Contains(first.Context, "policy=lean-build") || strings.Contains(first.Context, "sha256:first") {
		t.Fatalf("wrong bounded task context: %q", first.Context)
	}
	followup, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1,
		PolicyMode:      "safe",
		Agent:           Agent{ID: "claude"},
		Session:         session,
		Event:           Event{Type: "prompt.submit"},
		Prompt:          &PayloadDigest{Bytes: 101, SHA256: "sha256:second"},
		TaskProfile:     &TaskProfile{Type: "bugfix", Terms: []string{"fix"}, Continuation: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(followup.Context, "Caveman task contract") || strings.Contains(followup.Context, "Active skill") {
		t.Fatalf("explicit continuation repeated unchanged task policy: %q", followup.Context)
	}
	samePolicy, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1,
		PolicyMode:      "safe",
		Agent:           Agent{ID: "claude"},
		Session:         session,
		Event:           Event{Type: "prompt.submit"},
		Prompt:          &PayloadDigest{Bytes: 102, SHA256: "sha256:third"},
		TaskProfile:     &TaskProfile{Type: "feature"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(samePolicy.Context, "Caveman task contract") || strings.Contains(samePolicy.Context, "Active skill") {
		t.Fatalf("same explicit policy repeated task instructions: %q", samePolicy.Context)
	}
	changed, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1,
		PolicyMode:      "safe",
		Agent:           Agent{ID: "claude"},
		Session:         session,
		Event:           Event{Type: "prompt.submit"},
		Prompt:          &PayloadDigest{Bytes: 103, SHA256: "sha256:fourth"},
		TaskProfile:     &TaskProfile{Type: "migration"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(changed.Context, "policy=migration") {
		t.Fatalf("explicit task-type change did not update task policy: %q", changed.Context)
	}
	compact, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1,
		PolicyMode:      "safe",
		Agent:           Agent{ID: "claude"},
		Session:         session,
		Event:           Event{Type: "context.compact.after"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compact.Context, "dynamic tail") || !strings.Contains(compact.Context, "type=migration") || !strings.Contains(compact.Context, "Active skill migration:") || !strings.Contains(compact.Context, "exact recovery") {
		t.Fatalf("wrong compact state: %q", compact.Context)
	}

	objects, err := store.ListSessionObjects(session.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	current, stale := 0, 0
	for _, object := range objects {
		if object.Type != ccr.ObjectTaskContract {
			continue
		}
		if object.Currentness == ccr.Current {
			current++
		} else if object.Currentness == ccr.Stale {
			stale++
		}
		if strings.Contains(string(object.Data), "raw user prompt") {
			t.Fatal("raw prompt entered Task Contract")
		}
	}
	if current != 1 || stale != 2 {
		t.Fatalf("task contract lifecycle current=%d stale=%d, want 1/2", current, stale)
	}
}

func TestTaskContractStartsNewGeneralTaskAndRefreshesGoalIdentity(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	session := Session{ID: "task-boundary", RepositoryState: "git:abc"}

	_, err = runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, PolicyMode: "safe", Agent: Agent{ID: "claude"}, Session: session,
		Event: Event{Type: "prompt.submit"}, Prompt: &PayloadDigest{Bytes: 10, SHA256: "sha256:migration"},
		TaskProfile: &TaskProfile{Type: "migration", Terms: []string{"schema", "users"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, PolicyMode: "safe", Agent: Agent{ID: "claude"}, Session: session,
		Event: Event{Type: "prompt.submit"}, Prompt: &PayloadDigest{Bytes: 20, SHA256: "sha256:investor"},
		TaskProfile: &TaskProfile{Type: "general", Terms: []string{"investor", "update"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Context, "policy=core") || !strings.Contains(response.Context, "prior task overlay ended") || strings.Contains(response.Context, "Active skill") {
		t.Fatalf("general task did not end prior overlay: %q", response.Context)
	}
	objects, err := store.ListSessionObjects(session.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, object := range objects {
		if object.Type != ccr.ObjectTaskContract || object.Currentness != ccr.Current {
			continue
		}
		var contract storedTaskContract
		if err := json.Unmarshal(object.Data, &contract); err != nil {
			t.Fatal(err)
		}
		found = true
		if contract.TaskType != "general" || contract.GoalRef == nil || contract.GoalRef.SHA256 != "sha256:investor" {
			t.Fatalf("new task did not refresh contract identity: %+v", contract)
		}
		var raw map[string]any
		if err := json.Unmarshal(object.Data, &raw); err != nil {
			t.Fatal(err)
		}
		budget, ok := raw["change_budget"].(map[string]any)
		if !ok {
			t.Fatalf("change budget missing: %+v", raw)
		}
		for _, field := range []string{"new_dependencies", "new_services"} {
			if _, numeric := budget[field].(float64); numeric {
				t.Fatalf("%s must be justification-based, got numeric ceiling %#v", field, budget[field])
			}
		}
	}
	if !found {
		t.Fatal("current task contract missing")
	}
}

func TestModelVisibleTaskPolicyHasNoCountBasedCodePressure(t *testing.T) {
	source, err := os.ReadFile("runtime.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"new-dependency budget=",
		"fewest responsible files",
		"fewest files",
		"minimum lines",
		"keep it direct",
	} {
		if strings.Contains(strings.ToLower(string(source)), forbidden) {
			t.Fatalf("runtime contains count-based code pressure %q", forbidden)
		}
	}
}

func TestFullProfileWarmsRepositoryMapAndInjectsTypedTaskEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/repo\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "auth"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "auth", "refresh.go"), []byte("package auth\n\nfunc RotateToken() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "auth", "refresh_test.go"), []byte("package auth\n\nfunc TestRotateToken() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET_TOKEN=must-not-enter-evidence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := ccr.Open(filepath.Join(t.TempDir(), "repository-evidence.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	session := Session{ID: "repository-evidence", CWD: root, RepositoryState: "git:repo-state"}
	if _, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, PolicyMode: "safe", Profile: "full-safe", Agent: Agent{ID: "codex"},
		Session: session, Event: Event{Type: "session.start"},
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		objects, listErr := store.ListSessionObjects(session.ID, 100)
		if listErr != nil {
			t.Fatal(listErr)
		}
		if containsType(objects, ccr.ObjectRepositoryMap) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("repository map did not warm: %+v", objects)
		}
		time.Sleep(10 * time.Millisecond)
	}
	response, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, PolicyMode: "safe", Profile: "full-safe", Agent: Agent{ID: "codex"},
		Session: session, Event: Event{Type: "prompt.submit"}, Prompt: &PayloadDigest{Bytes: 77, SHA256: "sha256:prompt-only"},
		TaskProfile: &TaskProfile{Type: "bugfix", Terms: []string{"auth", "rotate", "sk-secret-should-drop", "../../escape"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Context, "auth/refresh.go") || !strings.Contains(response.Context, "observed local metadata") || !strings.HasPrefix(response.RecoveryRef, "ccr://") {
		t.Fatalf("task evidence missing from dynamic context: %+v", response)
	}
	objects, err := store.ListSessionObjects(session.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !containsType(objects, ccr.ObjectEvidenceBundle) {
		t.Fatalf("typed evidence bundle missing: %+v", objects)
	}
	for _, object := range objects {
		if strings.Contains(string(object.Data), "must-not-enter-evidence") || strings.Contains(string(object.Data), "sk-secret-should-drop") || strings.Contains(string(object.Data), "../../escape") {
			t.Fatalf("sensitive or traversal-shaped task data entered CCR: %s", object.Type)
		}
	}
	if _, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, PolicyMode: "safe", Profile: "full-safe", Agent: Agent{ID: "codex"}, Session: session,
		Event: Event{Type: "tool.after"}, Tool: &Tool{Name: "apply_patch", Input: json.RawMessage(`{"path":"auth/refresh.go"}`), Output: []byte("patch applied")},
	}); err != nil {
		t.Fatal(err)
	}
	objects, err = store.ListSessionObjects(session.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	foundImpact := false
	for _, object := range objects {
		if object.Type == ccr.ObjectExecutionState && strings.Contains(string(object.Data), "auth/refresh_test.go") && strings.Contains(string(object.Data), "conservative") {
			foundImpact = true
		}
	}
	if !foundImpact {
		t.Fatalf("conservative test-impact state missing: %+v", objects)
	}
}

func waitForRepositoryMap(t *testing.T, store *ccr.Store, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		objects, err := store.ListSessionObjects(sessionID, 100)
		if err != nil {
			t.Fatal(err)
		}
		if containsType(objects, ccr.ObjectRepositoryMap) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("repository map did not warm: %+v", objects)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRenderRepositoryEvidenceShowsOnlyDirectMatches(t *testing.T) {
	ref := "ccr://ccr_obj_09f4b7e4dd159d2070fb87854ab9b67f"
	for _, tc := range []struct {
		name   string
		bundle repointel.Bundle
	}{
		{"metadata only", repointel.Bundle{
			Strength: repointel.StrengthMetadata,
			Items:    []repointel.EvidenceItem{{Path: "lib/python3.14/xml/sax/xmlreader.py", LineStart: 107, Reasons: []string{"BM25 metadata relevance"}}},
		}},
		{"no items", repointel.Bundle{Strength: repointel.StrengthNone}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderRepositoryEvidence(tc.bundle, ref); got != "" {
				t.Fatalf("low-confidence bundle still injected a claim: %q", got)
			}
		})
	}

	mixed := repointel.Bundle{
		Strength: repointel.StrengthDirect,
		Items: []repointel.EvidenceItem{
			{Path: "src/auth/rotate.go", LineStart: 12, Direct: true, Reasons: []string{"symbol matches task terms"}},
			{Path: "internal/unrelated.go", Reasons: []string{"BM25 metadata relevance"}},
		},
		Scout: repointel.ScoutDecision{Status: repointel.ScoutNotConfigured, Recommended: true},
	}
	got := renderRepositoryEvidence(mixed, ref)
	if !strings.Contains(got, "src/auth/rotate.go:12") {
		t.Fatalf("direct hit dropped even though scout is unconfigured: %q", got)
	}
	if strings.Contains(got, "internal/unrelated.go") {
		t.Fatalf("metadata-only item rendered alongside direct hit: %q", got)
	}
}

func TestRepositoryEvidenceInjectsNothingWithoutDirectMatch(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "handler.go"), []byte("package main\n\nfunc Serve() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := ccr.Open(filepath.Join(t.TempDir(), "weak-evidence.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	session := Session{ID: "weak-evidence", CWD: root, RepositoryState: "git:weak"}
	if _, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, PolicyMode: "safe", Profile: "full-safe", Agent: Agent{ID: "claude"},
		Session: session, Event: Event{Type: "session.start"},
	}); err != nil {
		t.Fatal(err)
	}
	waitForRepositoryMap(t, store, session.ID)
	response, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, PolicyMode: "safe", Profile: "full-safe", Agent: Agent{ID: "claude"},
		Session: session, Event: Event{Type: "prompt.submit"}, Prompt: &PayloadDigest{Bytes: 20, SHA256: "sha256:weak"},
		TaskProfile: &TaskProfile{Type: "bugfix", Terms: []string{"unrelatedterm"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(response.Context, "Likely implementation path") || strings.Contains(response.Context, "ccr://") {
		t.Fatalf("weak evidence injected a path claim: %q", response.Context)
	}
	if response.RecoveryRef != "" {
		t.Fatalf("weak evidence still published a recovery handle: %q", response.RecoveryRef)
	}
	objects, err := store.ListSessionObjects(session.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if containsType(objects, ccr.ObjectEvidenceBundle) {
		t.Fatalf("weak evidence bundle must not be recorded as shown evidence: %+v", objects)
	}

	// Positive control: the same warmed session must still inject when a term
	// actually matches, so the assertions above cannot pass on a dead path.
	matched, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, PolicyMode: "safe", Profile: "full-safe", Agent: Agent{ID: "claude"},
		Session: session, Event: Event{Type: "prompt.submit"}, Prompt: &PayloadDigest{Bytes: 20, SHA256: "sha256:matched"},
		TaskProfile: &TaskProfile{Type: "bugfix", Terms: []string{"handler"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(matched.Context, "handler.go") || matched.RecoveryRef == "" {
		t.Fatalf("direct match must still inject evidence: context=%q ref=%q", matched.Context, matched.RecoveryRef)
	}
}

func TestRepositoryEvidenceIgnoresPixiVendorTree(t *testing.T) {
	root := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/proxy.py", "def proxy():\n    return 1\n")
	write(".pixi/envs/default/conda-meta/history", "")
	write(".pixi/envs/default/lib/python3.14/site-packages/zmq/devices/proxydevice.py", "class ProxyDevice:\n    pass\n")
	write(".pixi/envs/default/lib/python3.14/xml/sax/xmlreader.py", "class XMLReader:\n    def parse(self):\n        pass\n")
	store, err := ccr.Open(filepath.Join(t.TempDir(), "pixi-evidence.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	session := Session{ID: "pixi-evidence", CWD: root, RepositoryState: "git:pixi"}
	if _, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, PolicyMode: "safe", Profile: "full-safe", Agent: Agent{ID: "claude"},
		Session: session, Event: Event{Type: "session.start"},
	}); err != nil {
		t.Fatal(err)
	}
	waitForRepositoryMap(t, store, session.ID)
	response, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, PolicyMode: "safe", Profile: "full-safe", Agent: Agent{ID: "claude"},
		Session: session, Event: Event{Type: "prompt.submit"}, Prompt: &PayloadDigest{Bytes: 40, SHA256: "sha256:proxy-prompt"},
		TaskProfile: &TaskProfile{Type: "bugfix", Terms: []string{"proxy"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(response.Context, ".pixi") || strings.Contains(response.Context, "proxydevice.py") || strings.Contains(response.Context, "xmlreader.py") {
		t.Fatalf("injected pixi/vendored path: %q", response.Context)
	}
	if !strings.Contains(response.Context, "src/proxy.py") {
		t.Fatalf("project source missing from evidence: %q", response.Context)
	}
}

func TestRepositoryIntelligenceAblationIsMechanismReal(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		profile string
		want    bool
	}{
		{profile: "cache-aware", want: false},
		{profile: "full-safe", want: true},
	} {
		t.Run(tc.profile, func(t *testing.T) {
			store, err := ccr.Open(filepath.Join(t.TempDir(), "repository-profile.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			runtime := New(store)
			session := Session{ID: "repo-profile-" + tc.profile, CWD: root, RepositoryState: "git:one"}
			if _, err := runtime.Handle(context.Background(), Request{
				ProtocolVersion: 1, PolicyMode: "safe", Profile: tc.profile, Agent: Agent{ID: "claude"},
				Session: session, Event: Event{Type: "session.start"},
			}); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				objects, listErr := store.ListSessionObjects(session.ID, 100)
				if listErr != nil {
					t.Fatal(listErr)
				}
				got := containsType(objects, ccr.ObjectRepositoryMap)
				if got || !tc.want {
					if got != tc.want {
						t.Fatalf("repository mechanism active=%t want=%t: %+v", got, tc.want, objects)
					}
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatalf("repository mechanism did not activate for %s", tc.profile)
		})
	}
}

func TestExperimentProfilesToggleRealRuntimeMechanisms(t *testing.T) {
	large := []byte(strings.Repeat("profile output\n", 300))
	for _, tc := range []struct {
		profile      string
		wantContract bool
		wantCapture  bool
		wantMask     bool
	}{
		{profile: "core"},
		{profile: "core-lean-build"},
		{profile: "ledger", wantContract: true, wantCapture: true},
		{profile: "ccr-masking", wantCapture: true, wantMask: true},
		{profile: "cache-aware", wantContract: true},
		{profile: "full-safe", wantContract: true, wantCapture: true, wantMask: true},
		{profile: "full-max", wantContract: true, wantCapture: true, wantMask: true},
	} {
		t.Run(tc.profile, func(t *testing.T) {
			store, err := ccr.OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			runtime := New(store)
			session := Session{ID: "profile-" + tc.profile, RepositoryState: "git:abc"}
			prompt, err := runtime.Handle(context.Background(), Request{
				ProtocolVersion: 1, PolicyMode: "safe", Profile: tc.profile,
				Agent: Agent{ID: "claude"}, Session: session, Event: Event{Type: "prompt.submit"},
				Prompt: &PayloadDigest{Bytes: 8, SHA256: "sha256:profile"}, TaskProfile: &TaskProfile{Type: "feature"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if (prompt.Context != "") != tc.wantContract {
				t.Fatalf("contract context=%q want=%t", prompt.Context, tc.wantContract)
			}
			after, err := runtime.Handle(context.Background(), Request{
				ProtocolVersion: 1, PolicyMode: "safe", Profile: tc.profile,
				Agent: Agent{ID: "claude"}, Session: session, Event: Event{Type: "tool.after"},
				Tool: &Tool{Name: "read_file", Input: json.RawMessage(`{"path":"src/a.ts"}`), Output: large},
			})
			if err != nil {
				t.Fatal(err)
			}
			if (after.RecoveryRef != "") != tc.wantCapture || (after.OutputReplacement != "") != tc.wantMask {
				t.Fatalf("profile leaked/witheld mechanism: %+v", after)
			}
		})
	}
}

func TestSessionEndWritesHonestReceiptOutsideModelContext(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	receiptDir := filepath.Join(t.TempDir(), "receipts")
	runtime := NewWithReceiptsAndUsage(store, receiptDir, fixedUsageReader{value: sessionusage.Snapshot{
		Status: "correlated", SessionID: "claude:host-9", Requests: 2, ProviderCompleteRequests: 2,
		InputTokens: 1200, OutputTokens: 80, CachedInputTokens: 700,
		CatalogListPriceSubtotalUSD: 0.0123, InferredSavingsUSD: 0.004,
		CompressionTokensBefore: 900, CompressionTokensAfter: 300,
		CompressionTokenCountBasis: "estimated_engine_o200k", TokenUsageCoverage: "provider_complete",
		CostBasis: "catalog_list_price_subtotal_provider_complete_priced_rows", SavingsBasis: "inferred_standalone_not_verified",
	}})
	session := Session{ID: "claude:host-9", HostSessionID: "host-9"}
	large := []byte(strings.Repeat("safe result\n", 300))
	if _, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1,
		Agent:           Agent{ID: "claude"},
		Session:         session,
		Event:           Event{Type: "tool.after"},
		Tool:            &Tool{Name: "Bash", Input: json.RawMessage(`{"command":"go test ./..."}`), Output: large},
	}); err != nil {
		t.Fatal(err)
	}
	ended, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1,
		Agent:           Agent{ID: "claude"},
		Session:         session,
		Event:           Event{Type: "session.end"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ended.Message != fmt.Sprintf("Caveman · tests passed · 0 repeats detected · %d B exact context recorded", len(large)) {
		t.Fatalf("wrong compact receipt line: %q", ended.Message)
	}
	data, err := os.ReadFile(filepath.Join(receiptDir, "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt Receipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Schema != ReceiptSchema || receipt.SessionID != session.ID {
		t.Fatalf("wrong receipt identity: %+v", receipt)
	}
	if got := receipt.Execution["exact_context_recorded"]; got.Value != int64(len(large)) || got.Basis != "observed" {
		t.Fatalf("wrong exact context metric: %+v", got)
	}
	if got := receipt.Execution["mask_candidate_context"]; got.Value != int64(len(large)) || got.Basis != "observed" {
		t.Fatalf("wrong mask candidate metric: %+v", got)
	}
	if receipt.Claims["task_savings"] != "not_verified_without_paired_baseline" {
		t.Fatalf("receipt overclaimed task savings: %+v", receipt.Claims)
	}
	if receipt.Outcome["tests"] != "passed_observed_host_event" {
		t.Fatalf("test outcome did not use host success event: %+v", receipt.Outcome)
	}
	if receipt.ProviderUsage.Status != "correlated" || receipt.ProviderUsage.InputTokens != 1200 || receipt.ProviderUsage.CatalogListPriceSubtotalUSD != 0.0123 {
		t.Fatalf("provider usage was not joined by session: %+v", receipt.ProviderUsage)
	}
	if receipt.Claims["compression_reduction"] != "inferred_correlated_estimated_engine_o200k" {
		t.Fatalf("wrong correlated compression claim: %+v", receipt.Claims)
	}
	if strings.Contains(string(data), string(large)) {
		t.Fatal("receipt copied raw model-visible output")
	}
	info, err := os.Stat(filepath.Join(receiptDir, "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	// POSIX permission bits are synthetic on Windows; NTFS ACLs govern there.
	if goruntime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt permissions = %v", info.Mode().Perm())
	}
	objects, err := store.ListSessionObjects(session.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range objects {
		if object.Lifecycle != ccr.LifecycleArchived {
			t.Fatalf("session-end object not archived: type=%s lifecycle=%s", object.Type, object.Lifecycle)
		}
	}
}

func TestSessionEndDoesNotAttestApproximateFallbackCompression(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	receiptDir := filepath.Join(t.TempDir(), "receipts")
	runtime := NewWithReceiptsAndUsage(store, receiptDir, fixedUsageReader{value: sessionusage.Snapshot{
		Status: "correlated", SessionID: "claude:recent", CorrelationBasis: "unique_recent_session",
		Requests: 1, CompressionTokensBefore: 900, CompressionTokensAfter: 300,
		CompressionTokenCountBasis: "estimated_engine_o200k",
	}})
	_, err = runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, Agent: Agent{ID: "claude"}, Session: Session{ID: "claude:recent"}, Event: Event{Type: "session.end"},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(receiptDir, "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt Receipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.ProviderUsage.CorrelationBasis != "unique_recent_session" {
		t.Fatalf("receipt lost correlation basis: %+v", receipt.ProviderUsage)
	}
	if receipt.Claims["compression_reduction"] != "not_attested_due_approximate_session_correlation" {
		t.Fatalf("approximate fallback minted compression claim: %+v", receipt.Claims)
	}
}

func TestCompactionTransitionsTaskStateHotEvidenceWarmAndRawObservationsCold(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	session := Session{ID: "lifecycle"}
	if _, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, Agent: Agent{ID: "claude"}, Session: session, Event: Event{Type: "prompt.submit"},
		Prompt: &PayloadDigest{Bytes: 4, SHA256: "sha256:task"}, TaskProfile: &TaskProfile{Type: "feature"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, Agent: Agent{ID: "claude"}, Session: session, Event: Event{Type: "tool.after"},
		Tool: &Tool{Name: "read_file", Input: json.RawMessage(`{"path":"src/a.go"}`), Output: []byte("small exact observation")},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, Agent: Agent{ID: "claude"}, Session: session, Event: Event{Type: "context.compact.before"},
	}); err != nil {
		t.Fatal(err)
	}
	objects, err := store.ListSessionObjects(session.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	foundContract, foundColdObservation, foundWarmDecision := false, false, false
	for _, object := range objects {
		switch object.Type {
		case ccr.ObjectTaskContract:
			foundContract = object.Lifecycle == ccr.Hot
		case ccr.ObjectFileObservation:
			foundColdObservation = object.Lifecycle == ccr.Cold
		case ccr.ObjectTaskDecision:
			if object.Lifecycle == ccr.Warm {
				foundWarmDecision = true
			}
		}
	}
	if !foundContract || !foundColdObservation || !foundWarmDecision {
		t.Fatalf("wrong post-compaction lifecycle: %+v", objects)
	}
}

func TestDeferredMaskIsInjectedOnceForHostsWithoutDirectOutputRewrite(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	session := Session{ID: "codex:deferred-mask"}
	large := []byte(strings.Repeat("bounded output\n", 200))
	after, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, PolicyMode: "safe", Profile: "full-safe", Agent: Agent{ID: "codex"},
		Session: session, Event: Event{Type: "tool.after"},
		Tool: &Tool{Name: "read_file", Input: json.RawMessage(`{"path":"src/current.go"}`), Output: large},
	})
	if err != nil {
		t.Fatal(err)
	}
	if after.OutputReplacement == "" || after.RecoveryRef == "" {
		t.Fatalf("runtime did not produce exact mask: %+v", after)
	}
	first, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, PolicyMode: "safe", Profile: "full-safe", Agent: Agent{ID: "codex"},
		Session: session, Event: Event{Type: "prompt.submit"},
		Prompt: &PayloadDigest{Bytes: 4, SHA256: "sha256:task"}, TaskProfile: &TaskProfile{Type: "feature"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Context, "deferred exact-output masks") || !strings.Contains(first.Context, after.RecoveryRef) {
		t.Fatalf("deferred mask missing: %q", first.Context)
	}
	second, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, PolicyMode: "safe", Profile: "full-safe", Agent: Agent{ID: "codex"},
		Session: session, Event: Event{Type: "prompt.submit"},
		Prompt: &PayloadDigest{Bytes: 4, SHA256: "sha256:task"}, TaskProfile: &TaskProfile{Type: "feature"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(second.Context, "deferred exact-output masks") {
		t.Fatalf("deferred mask flooded later turn: %q", second.Context)
	}
}

func TestSubagentEvidenceMergesAsTypedPointersIntoParentScope(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	child := Session{ID: "codex:child", ParentSessionID: "codex:parent", RepositoryState: "git:one"}
	if _, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, PolicyMode: "safe", Agent: Agent{ID: "codex"}, Session: child, Event: Event{Type: "subagent.start"},
	}); err != nil {
		t.Fatal(err)
	}
	after, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, PolicyMode: "safe", Agent: Agent{ID: "codex"}, Session: child, Event: Event{Type: "tool.after"},
		Tool: &Tool{Name: "read_file", Input: json.RawMessage(`{"path":"src/child.go"}`), Output: []byte("child evidence")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, PolicyMode: "safe", Agent: Agent{ID: "codex"}, Session: child, Event: Event{Type: "subagent.stop"},
	}); err != nil {
		t.Fatal(err)
	}
	parentObjects, err := store.ListSessionObjects("codex:parent", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(parentObjects) != 1 || parentObjects[0].Type != ccr.ObjectExecutionState || parentObjects[0].Source != "native:subagent-merge" {
		t.Fatalf("wrong parent merge: %+v", parentObjects)
	}
	if !strings.Contains(string(parentObjects[0].Data), after.RecoveryRef) || strings.Contains(string(parentObjects[0].Data), "child evidence") {
		t.Fatalf("merge must preserve pointer without raw duplication: %s", parentObjects[0].Data)
	}
}

func TestSessionResumeSurvivesRuntimeAndStoreRestart(t *testing.T) {
	database := filepath.Join(t.TempDir(), "ccr.db")
	store, err := ccr.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	session := Session{ID: "claude:resume", RepositoryState: "git:stable"}
	runtime := New(store)
	after, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, PolicyMode: "safe", Agent: Agent{ID: "claude"}, Session: session, Event: Event{Type: "tool.after"},
		Tool: &Tool{Name: "read_file", Input: json.RawMessage(`{"path":"src/resume.go"}`), Output: []byte("persisted observation")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = ccr.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	resumed := New(store)
	before, err := resumed.Handle(context.Background(), Request{
		ProtocolVersion: 1, PolicyMode: "safe", Agent: Agent{ID: "claude"}, Session: session, Event: Event{Type: "tool.before"},
		Tool: &Tool{Name: "read_file", Input: json.RawMessage(`{"path":"src/resume.go"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if before.Action != "observe" || before.RecoveryRef != after.RecoveryRef {
		t.Fatalf("restart lost current session evidence: before=%+v after=%+v", before, after)
	}
}

func TestUniqueRecentSessionFailsClosedOnAmbiguityAndStaleness(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	for _, sessionID := range []string{"claude:one", "claude:two"} {
		if _, err := runtime.Handle(context.Background(), Request{
			ProtocolVersion: 1, Agent: Agent{ID: "claude"}, Session: Session{ID: sessionID}, Event: Event{Type: "session.start"},
		}); err != nil {
			t.Fatal(err)
		}
		if got, _ := runtime.UniqueRecentSession(time.Now(), 5*time.Second, "", ""); sessionID == "claude:one" && got != sessionID {
			t.Fatalf("single recent session = %q, want %q", got, sessionID)
		}
	}
	if got, _ := runtime.UniqueRecentSession(time.Now(), 5*time.Second, "", ""); got != "" {
		t.Fatalf("ambiguous sessions correlated as %q", got)
	}
	if _, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, Agent: Agent{ID: "claude"}, Session: Session{ID: "claude:two"}, Event: Event{Type: "session.end"},
	}); err != nil {
		t.Fatal(err)
	}
	if got, basis := runtime.UniqueRecentSession(time.Now(), 5*time.Second, "", ""); got != "claude:one" || basis != "unique_recent_session" {
		t.Fatalf("remaining recent session = %q basis=%q", got, basis)
	}
	runtime.mu.Lock()
	runtime.activeSessions["claude:one"] = sessionActivity{At: time.Now().Add(-time.Minute)}
	runtime.mu.Unlock()
	if got, _ := runtime.UniqueRecentSession(time.Now(), 5*time.Second, "", ""); got != "" {
		t.Fatalf("stale session correlated as %q", got)
	}
}

func TestUniqueRecentSessionUsesKnownModelToRejectOtherSessions(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	for _, tc := range []struct {
		id    string
		model string
	}{{"claude:opus", "claude-opus-5"}, {"claude:sonnet", "claude-sonnet-5"}} {
		if _, err := runtime.Handle(context.Background(), Request{
			ProtocolVersion: 1, Agent: Agent{ID: "claude"}, Session: Session{ID: tc.id}, Event: Event{Type: "prompt.submit"},
			Model: &Model{Provider: "anthropic", Model: tc.model}, Prompt: &PayloadDigest{Bytes: 1, SHA256: "sha256:model"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, basis := runtime.UniqueRecentSession(time.Now(), 5*time.Second, "anthropic", "claude-sonnet-5")
	if got != "claude:sonnet" || basis != "unique_recent_time_model" {
		t.Fatalf("model-aware candidate = %q basis=%q", got, basis)
	}
	if got, _ := runtime.UniqueRecentSession(time.Now(), 5*time.Second, "anthropic", ""); got != "" {
		t.Fatalf("missing request model must preserve ambiguity, got %q", got)
	}
}

func TestEditInvalidatesDependentObservationAndSecretsAreNotStored(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	session := Session{ID: "s2", CWD: "/repo", RepositoryState: "git:abc"}
	read, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1,
		Agent:           Agent{ID: "codex"},
		Session:         session,
		Event:           Event{Type: "tool.after"},
		Tool:            &Tool{Name: "read_file", Input: json.RawMessage(`{"path":"src/auth.ts"}`), Output: []byte(strings.Repeat("safe\n", 300))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1,
		Agent:           Agent{ID: "codex"},
		Session:         session,
		Event:           Event{Type: "tool.after"},
		Tool:            &Tool{Name: "apply_patch", Input: json.RawMessage(`{"path":"src/auth.ts"}`), Output: []byte("Done")},
	}); err != nil {
		t.Fatal(err)
	}
	obj, err := store.GetObject(strings.TrimPrefix(read.RecoveryRef, "ccr://"))
	if err != nil || obj.Currentness != ccr.Stale {
		t.Fatalf("edit did not invalidate dependent observation: currentness=%q err=%v", obj.Currentness, err)
	}

	secret := []byte("Authorization: Bearer abcdefghijklmnopqrstuvwxyz012345")
	response, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1,
		Agent:           Agent{ID: "codex"},
		Session:         session,
		Event:           Event{Type: "tool.after"},
		Tool:            &Tool{Name: "read_file", Input: json.RawMessage(`{"path":"secrets.txt"}`), Output: secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.RecoveryRef != "" || response.OutputReplacement != "" {
		t.Fatalf("secret-shaped output must stay host-only and unmasked: %+v", response)
	}
	objects, err := store.ListSessionObjects("s2", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range objects {
		if strings.Contains(string(object.Data), "abcdefghijklmnopqrstuvwxyz012345") {
			t.Fatal("secret-shaped output reached CCR")
		}
	}
}

func TestRuntimeFailsClosedOnUnknownProtocolOrEvent(t *testing.T) {
	store, _ := ccr.OpenMemory()
	defer store.Close()
	runtime := New(store)
	for _, request := range []Request{
		{ProtocolVersion: 2, Session: Session{ID: "s"}, Event: Event{Type: "session.start"}},
		{ProtocolVersion: 1, Session: Session{ID: "s"}, Event: Event{Type: "future.event"}},
		{ProtocolVersion: 1, PolicyMode: "future", Session: Session{ID: "s"}, Event: Event{Type: "session.start"}},
		{ProtocolVersion: 1, PolicyMode: "safe", Profile: "future", Session: Session{ID: "s"}, Event: Event{Type: "session.start"}},
	} {
		if _, err := runtime.Handle(context.Background(), request); err == nil {
			t.Fatalf("invalid request must fail closed: %+v", request)
		}
	}
}

func TestRuntimeIdleLifecycleTracksSessionsAndRecoversFromMissingEnd(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	session := Session{ID: "idle-session"}
	if _, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, Agent: Agent{ID: "claude"}, Session: session, Event: Event{Type: "session.start"},
	}); err != nil {
		t.Fatal(err)
	}
	if active, _ := runtime.IdleSnapshot(); active != 1 {
		t.Fatalf("active sessions = %d, want 1", active)
	}
	started := time.Now()
	if !runtime.WaitForIdle(context.Background(), 30*time.Millisecond) {
		t.Fatal("crashed session without SessionEnd must eventually idle")
	}
	if elapsed := time.Since(started); elapsed < 25*time.Millisecond {
		t.Fatalf("idle returned too early: %v", elapsed)
	}
	if active, _ := runtime.IdleSnapshot(); active != 0 {
		t.Fatalf("expired sessions = %d, want 0", active)
	}

	if _, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, Agent: Agent{ID: "claude"}, Session: session, Event: Event{Type: "session.start"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, Agent: Agent{ID: "claude"}, Session: session, Event: Event{Type: "session.end"},
	}); err != nil {
		t.Fatal(err)
	}
	if active, _ := runtime.IdleSnapshot(); active != 0 {
		t.Fatalf("SessionEnd left %d active sessions", active)
	}
}

// A wrap heartbeat must hold off idle exit even with zero session activity: an
// open-but-quiet agent still points its ANTHROPIC_BASE_URL at this proxy (#860).
func TestKeepaliveHoldsOffIdleExit(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	stop := make(chan struct{})
	// Heartbeat interval must sit far below the idle threshold, not merely
	// under it. Windows' default timer granularity is ~15.6ms, so a 10ms ticker
	// really fires every ~16ms and a loaded CI runner stretches that further; a
	// 30ms threshold left barely a 2x margin and the idle timer won this race
	// intermittently on windows-latest. 5ms against 250ms is ~50x, which
	// survives a scheduling stall an order of magnitude worse than anything
	// observed.
	const heartbeat = 5 * time.Millisecond
	const idleAfter = 250 * time.Millisecond
	go func() {
		ticker := time.NewTicker(heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				runtime.Keepalive()
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	if runtime.WaitForIdle(ctx, idleAfter) {
		t.Fatal("idle exit fired while keepalive heartbeats were arriving")
	}
	close(stop)
	if !runtime.WaitForIdle(context.Background(), idleAfter) {
		t.Fatal("idle exit must fire once heartbeats stop")
	}
}

func TestRecordModeCollectsMetadataWithoutCoreStateReuseOrExactCCR(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	session := Session{ID: "record-session", RepositoryState: "git:abc"}
	for _, request := range []Request{
		{ProtocolVersion: 1, PolicyMode: "record", Profile: "full-max", Agent: Agent{ID: "claude"}, Session: session, Event: Event{Type: "prompt.submit"}, Prompt: &PayloadDigest{Bytes: 10, SHA256: "sha256:prompt"}},
		{ProtocolVersion: 1, PolicyMode: "record", Agent: Agent{ID: "claude"}, Session: session, Event: Event{Type: "tool.after"}, Tool: &Tool{Name: "read_file", Input: json.RawMessage(`{"path":"src/a.ts"}`), Output: []byte("exact output must not enter record CCR")}},
		{ProtocolVersion: 1, PolicyMode: "record", Agent: Agent{ID: "claude"}, Session: session, Event: Event{Type: "tool.before"}, Tool: &Tool{Name: "read_file", Input: json.RawMessage(`{"path":"src/a.ts"}`)}},
	} {
		response, handleErr := runtime.Handle(context.Background(), request)
		if handleErr != nil {
			t.Fatal(handleErr)
		}
		if response.PolicyMode != "record" || response.Profile != "record-only" || response.Action != "allow" || response.Context != "" || response.RecoveryRef != "" || response.OutputReplacement != "" {
			t.Fatalf("record mode transformed behavior: %+v", response)
		}
	}
	objects, err := store.ListSessionObjects(session.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 3 {
		t.Fatalf("record mode should append only three metadata decisions: %+v", objects)
	}
	for _, object := range objects {
		if object.Type != ccr.ObjectTaskDecision || strings.Contains(string(object.Data), "exact output") {
			t.Fatalf("record mode persisted transformed state or raw output: %+v", object)
		}
	}
}

func TestRuntimeNeverSuggestsUnsafeOrUnprovenReuse(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)

	unsafe := []struct {
		name  string
		input string
	}{
		{name: "bash", input: `{"command":"curl https://example.com"}`},
		{name: "bash", input: `{"command":"date"}`},
		{name: "browser_snapshot", input: `{}`},
	}
	for _, tool := range unsafe {
		after, handleErr := runtime.Handle(context.Background(), Request{
			ProtocolVersion: 1,
			Agent:           Agent{ID: "codex"},
			Session:         Session{ID: "unsafe", RepositoryState: "git:abc"},
			Event:           Event{Type: "tool.after"},
			Tool:            &Tool{Name: tool.name, Input: json.RawMessage(tool.input), Output: []byte("result")},
		})
		if handleErr != nil {
			t.Fatal(handleErr)
		}
		before, handleErr := runtime.Handle(context.Background(), Request{
			ProtocolVersion: 1,
			Agent:           Agent{ID: "codex"},
			Session:         Session{ID: "unsafe", RepositoryState: "git:abc"},
			Event:           Event{Type: "tool.before"},
			Tool:            &Tool{Name: tool.name, Input: json.RawMessage(tool.input)},
		})
		if handleErr != nil {
			t.Fatal(handleErr)
		}
		if after.RecoveryRef == "" || before.Action != "allow" || before.RecoveryRef != "" {
			t.Fatalf("%s must remain recoverable but never suggested for reuse: after=%+v before=%+v", tool.name, after, before)
		}
	}

	if _, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1,
		Agent:           Agent{ID: "codex"},
		Session:         Session{ID: "search", RepositoryState: "git:abc"},
		Event:           Event{Type: "tool.after"},
		Tool:            &Tool{Name: "search", Input: json.RawMessage(`{"query":"needle"}`), Output: []byte("match")},
	}); err != nil {
		t.Fatal(err)
	}
	changed, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1,
		Agent:           Agent{ID: "codex"},
		Session:         Session{ID: "search", RepositoryState: "git:def"},
		Event:           Event{Type: "tool.before"},
		Tool:            &Tool{Name: "search", Input: json.RawMessage(`{"query":"needle"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed.Action != "allow" || changed.RecoveryRef != "" {
		t.Fatalf("changed repository state must suppress reuse hint: %+v", changed)
	}
}

func TestInvalidUTF8ToolOutputRoundTripsByteExact(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	original := []byte{0xff, 0xfe, 0x00, 'x', '\n'}
	after, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, PolicyMode: "safe", Agent: Agent{ID: "claude"}, Session: Session{ID: "invalid-utf8"}, Event: Event{Type: "tool.after"},
		Tool: &Tool{Name: "read_file", Input: json.RawMessage(`{"path":"fixture.bin"}`), Output: original},
	})
	if err != nil {
		t.Fatal(err)
	}
	object, err := store.GetObject(trimCCRReference(after.RecoveryRef))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(object.Data, original) {
		t.Fatalf("invalid UTF-8 changed: got %x want %x", object.Data, original)
	}
}

func TestRepeatLoopHintsOnceEscalatesOnceAndNeverFloods(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	session := Session{ID: "repeat-loop"}
	if _, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, Agent: Agent{ID: "codex"}, Session: session, Event: Event{Type: "tool.after"},
		Tool: &Tool{Name: "read_file", InputState: "sha256:auth-v1", Input: json.RawMessage(`{"path":"src/auth.go"}`), Output: []byte("current auth evidence")},
	}); err != nil {
		t.Fatal(err)
	}
	contexts := make([]string, 0, 4)
	actions := make([]string, 0, 4)
	for range 4 {
		response, handleErr := runtime.Handle(context.Background(), Request{
			ProtocolVersion: 1, Agent: Agent{ID: "codex"}, Session: session, Event: Event{Type: "tool.before"},
			Tool: &Tool{Name: "read_file", InputState: "sha256:auth-v1", Input: json.RawMessage(`{"path":"src/auth.go"}`)},
		})
		if handleErr != nil {
			t.Fatal(handleErr)
		}
		contexts = append(contexts, response.Context)
		actions = append(actions, response.Action)
	}
	if actions[0] != "observe" || !strings.Contains(contexts[0], "Current observation") {
		t.Fatalf("first repeat must receive one currentness hint: actions=%v contexts=%q", actions, contexts)
	}
	if actions[1] != "allow" || contexts[1] != "" {
		t.Fatalf("second repeat hint must be suppressed: actions=%v contexts=%q", actions, contexts)
	}
	if actions[2] != "observe" || !strings.Contains(contexts[2], "Verified repeat loop") {
		t.Fatalf("third repeat must receive one loop intervention: actions=%v contexts=%q", actions, contexts)
	}
	if actions[3] != "allow" || contexts[3] != "" {
		t.Fatalf("later repeats must not flood context: actions=%v contexts=%q", actions, contexts)
	}
}

func TestExplainDecisionReturnsDeterministicBasisWithoutRawOutput(t *testing.T) {
	store, err := ccr.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	response, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1,
		Agent:           Agent{ID: "claude"},
		Session:         Session{ID: "explain-session"},
		Event:           Event{Type: "tool.after", TimestampMS: 1770000000000},
		Tool:            &Tool{Name: "read_file", Input: json.RawMessage(`{"path":"src/a.ts"}`), Output: []byte("private raw output")},
	})
	if err != nil {
		t.Fatal(err)
	}
	explanation, err := ExplainDecision(store, response.DecisionID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(explanation)
	if err != nil {
		t.Fatal(err)
	}
	if explanation.Schema != WhySchema || explanation.SessionID != "explain-session" || explanation.Reason != "exact_output_recorded" {
		t.Fatalf("wrong explanation: %+v", explanation)
	}
	if strings.Contains(string(data), "private raw output") {
		t.Fatal("why explanation leaked raw tool output")
	}
	if _, err := ExplainDecision(store, "../../not-a-decision"); err == nil {
		t.Fatal("invalid decision id must fail closed")
	}
}

func containsType(objects []ccr.Object, objectType ccr.ObjectType) bool {
	for _, object := range objects {
		if object.Type == objectType {
			return true
		}
	}
	return false
}

// The rendered block drops metadata-only items, but the ccr:// handle it hands
// the model must not carry them either: retrieving the bundle is one step away,
// and the handle reads as authoritative.
func TestStoredEvidenceBundleCarriesOnlyDirectItems(t *testing.T) {
	root := t.TempDir()
	// `handler.go` matches the term in its own name; `handler/serve.go` matches
	// only because an ancestor directory does, which is the ranking that pointed
	// at unrelated files. Fillers keep BM25 IDF positive.
	files := map[string]string{
		"handler.go":       "package main\n\nfunc Serve() {}\n",
		"handler/serve.go": "package handler\n\nfunc Relay() {}\n",
	}
	for index := 0; index < 12; index++ {
		files[filepath.Join("pkg", fmt.Sprintf("filler%d", index), "mod.go")] = "package filler\n"
	}
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	store, err := ccr.Open(filepath.Join(t.TempDir(), "direct-only.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := New(store)
	session := Session{ID: "direct-only", CWD: root, RepositoryState: "git:direct-only"}
	if _, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, PolicyMode: "safe", Profile: "full-safe", Agent: Agent{ID: "claude"},
		Session: session, Event: Event{Type: "session.start"},
	}); err != nil {
		t.Fatal(err)
	}
	waitForRepositoryMap(t, store, session.ID)
	response, err := runtime.Handle(context.Background(), Request{
		ProtocolVersion: 1, PolicyMode: "safe", Profile: "full-safe", Agent: Agent{ID: "claude"},
		Session: session, Event: Event{Type: "prompt.submit"}, Prompt: &PayloadDigest{Bytes: 20, SHA256: "sha256:direct"},
		TaskProfile: &TaskProfile{Type: "bugfix", Terms: []string{"handler"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.RecoveryRef == "" {
		t.Fatal("direct match published no recovery handle")
	}
	object, err := store.GetObject(strings.TrimPrefix(response.RecoveryRef, "ccr://"))
	if err != nil {
		t.Fatal(err)
	}
	var bundle repointel.Bundle
	if err := json.Unmarshal(object.Data, &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Items) == 0 {
		t.Fatal("stored bundle has no items")
	}
	for _, item := range bundle.Items {
		if !item.Direct {
			t.Fatalf("metadata-only item stored behind the ccr:// handle: %+v", item)
		}
	}
}
