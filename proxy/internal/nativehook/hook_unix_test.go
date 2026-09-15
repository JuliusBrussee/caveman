//go:build !windows

package nativehook

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/nativeruntime"
)

func TestRunPreToolUsesNativeBridgeAndIncludesFileCurrentness(t *testing.T) {
	home := shortTempDir(t)
	runDir := filepath.Join(home, "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, "generated.ts")
	if err := os.WriteFile(file, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", nativeruntime.SocketPath(home))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	requestCh := make(chan nativeruntime.Request, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		var request nativeruntime.Request
		if json.NewDecoder(conn).Decode(&request) == nil {
			requestCh <- request
			_ = json.NewEncoder(conn).Encode(nativeruntime.Response{
				ProtocolVersion: 1, PolicyMode: "safe", Profile: "full-safe", Action: "observe",
				Context: "current observation available", Visibility: "silent", FailOpen: true,
			})
		}
	}()
	t.Setenv("CAVEMAN_NATIVE_MODE", "safe")
	raw := []byte(`{"hook_event_name":"PreToolUse","session_id":"host-1","tool_name":"read_file","cwd":` + string(mustJSON(t, home)) + `,"tool_input":{"path":"generated.ts"}}`)
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), home, "claude", "", raw, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 || !strings.Contains(stdout.String(), "current observation available") {
		t.Fatalf("unexpected hook output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	select {
	case request := <-requestCh:
		if request.Event.Type != "tool.before" || request.Session.ID != "claude:host-1" {
			t.Fatalf("wrong request identity: %+v", request)
		}
		if request.Tool == nil || !strings.HasPrefix(request.Tool.InputState, "sha256:") {
			t.Fatalf("file currentness identity missing: %+v", request.Tool)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime request not observed")
	}
}

func TestRunInvalidRuntimeResponseFailsOpenAndRecordsBoundedFallback(t *testing.T) {
	home := shortTempDir(t)
	if err := os.MkdirAll(filepath.Join(home, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", nativeruntime.SocketPath(home))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		var request nativeruntime.Request
		_ = json.NewDecoder(conn).Decode(&request)
		_, _ = conn.Write([]byte(`{"protocol_version":1,"action":"invented","visibility":"silent","fail_open":true}` + "\n"))
	}()
	t.Setenv("CAVEMAN_NATIVE_MODE", "safe")
	raw := []byte(`{"hook_event_name":"PreToolUse","session_id":"host-secret","tool_name":"read_file","tool_input":{"path":"secret.txt"}}`)
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), home, "claude", "", raw, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("invalid decision intervened stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	fallback, err := os.ReadFile(filepath.Join(home, "runtime", "native-events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(fallback, []byte("secret.txt")) || !bytes.Contains(fallback, []byte(`"payload_sha256"`)) {
		t.Fatalf("fallback must be bounded metadata only: %s", fallback)
	}
}

func TestRepositoryStateChangesForExternalEditIndexAndWorktree(t *testing.T) {
	repository := shortTempDir(t)
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.email", "native-test@example.invalid")
	runGit(t, repository, "config", "user.name", "Native Test")
	tracked := filepath.Join(repository, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "tracked.txt")
	runGit(t, repository, "commit", "-m", "initial")

	initial := currentRepositoryState(context.Background(), repository)
	if initial == "" {
		t.Fatal("initial repository state unavailable")
	}
	if err := os.WriteFile(tracked, []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	externalEdit := currentRepositoryState(context.Background(), repository)
	if externalEdit == "" || externalEdit == initial {
		t.Fatalf("external edit did not change state: initial=%q edit=%q", initial, externalEdit)
	}
	runGit(t, repository, "add", "tracked.txt")
	indexEdit := currentRepositoryState(context.Background(), repository)
	if indexEdit == "" || indexEdit == externalEdit {
		t.Fatalf("index change did not change state: edit=%q index=%q", externalEdit, indexEdit)
	}
	runGit(t, repository, "commit", "-m", "second")
	worktree := filepath.Join(filepath.Dir(repository), filepath.Base(repository)+"-worktree")
	t.Cleanup(func() { _ = os.RemoveAll(worktree) })
	runGit(t, repository, "worktree", "add", "-b", "native-second", worktree)
	mainState := currentRepositoryState(context.Background(), repository)
	worktreeState := currentRepositoryState(context.Background(), worktree)
	if mainState == "" || worktreeState == "" || mainState == worktreeState {
		t.Fatalf("worktrees must have distinct currentness: main=%q second=%q", mainState, worktreeState)
	}
}

func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	path, err := os.MkdirTemp("/tmp", "cave-hook-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	return path
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
