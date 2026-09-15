package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Exercise the shipped entrypoint in fresh homes: an installed binary may be
// launched directly without the npm downloader having created .caveman first.
func TestRecoveryStoreFreshHomeProcess(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "caveman-mcp")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build MCP binary: %v\n%s", err, output)
	}

	const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`
	const stats = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"caveman_stats"}}`
	original := `{"items":[` + strings.Repeat(`{"k":"v","n":1},`, 50) + `{"k":"v","n":1}]}`
	inputJSON, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	compress := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"caveman_compress","arguments":{"input":` + string(inputJSON) + `}}}`

	for _, customHome := range []bool{false, true} {
		name := "default"
		if customHome {
			name = "CAVEMAN_HOME"
		}
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			storeHome := filepath.Join(home, ".caveman")
			overrides := []string{}
			if customHome {
				storeHome = filepath.Join(home, "profiles", "new-caveman-home")
				overrides = append(overrides, "CAVEMAN_HOME="+storeHome)
			}
			stdout, stderr, err := runRecoveryProcess(binary, home, overrides, initialize+"\n"+compress+"\n"+compress+"\n"+stats+"\n")
			if err != nil {
				t.Fatalf("fresh home startup: %v\nstderr: %s", err, stderr)
			}
			lines := bytes.Split(bytes.TrimSpace(stdout), []byte("\n"))
			if len(lines) != 4 || !bytes.Contains(lines[0], []byte(`"protocolVersion":"2024-11-05"`)) {
				t.Fatalf("initialize/compress responses: %s", stdout)
			}
			var compressed struct {
				RecoveryHandle string `json:"recovery_handle"`
				TokensBefore   int    `json:"tokens_before"`
				TokensAfter    int    `json:"tokens_after"`
			}
			if err := json.Unmarshal([]byte(recoveryToolText(t, lines[1])), &compressed); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(compressed.RecoveryHandle, "ccr_") {
				t.Fatalf("compression did not persist a recovery: %s", lines[1])
			}
			assertRecoveryProcessStats(t, lines[3], 2, 2*compressed.TokensBefore, 2*compressed.TokensAfter)
			info, err := os.Stat(storeHome)
			if err != nil || !info.IsDir() {
				t.Fatalf("recovery directory missing: info=%v err=%v", info, err)
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
				t.Fatalf("recovery directory permissions=%o, want 700", info.Mode().Perm())
			}
			// A new process must recover the exact bytes persisted by the first.
			retrieve := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"caveman_retrieve","arguments":{"recovery_handle":"` + compressed.RecoveryHandle + `"}}}`
			var recoveryInput strings.Builder
			recoveryInput.WriteString(initialize + "\n" + stats + "\n" + retrieve + "\n" + retrieve + "\n")
			// A query with no rankable match falls back to the exact original.
			// Many distinct calls must not add a payout wrapper or make a later
			// repeated full retrieve unavailable in the same process.
			for i := 0; i < 8; i++ {
				fmt.Fprintf(&recoveryInput, "{\"jsonrpc\":\"2.0\",\"id\":%d,\"method\":\"tools/call\",\"params\":{\"name\":\"caveman_retrieve\",\"arguments\":{\"recovery_handle\":%q,\"query\":\"zznevermatches%d\"}}}\n", i+10, compressed.RecoveryHandle, i)
			}
			recoveryInput.WriteString(retrieve + "\n")
			stdout, stderr, err = runRecoveryProcess(binary, home, overrides, recoveryInput.String())
			if err != nil {
				t.Fatalf("restart recovery: %v\nstderr: %s", err, stderr)
			}
			lines = bytes.Split(bytes.TrimSpace(stdout), []byte("\n"))
			if len(lines) != 13 {
				t.Fatalf("restart/repeated recovery response count=%d, want 13: %s", len(lines), stdout)
			}
			for i, line := range lines[2:] {
				if recoveryToolText(t, line) != original {
					t.Fatalf("recovery %d did not return exact original: %s", i, line)
				}
			}
			assertRecoveryProcessStats(t, lines[1], 0, 0, 0)
		})
	}

	t.Run("explicit DB parent remains caller-owned", func(t *testing.T) {
		home := t.TempDir()
		parent := filepath.Join(home, "missing-explicit-parent")
		stdout, stderr, err := runRecoveryProcess(binary, home, []string{"CAVEMAN_CCR_DB=" + filepath.Join(parent, "ccr.db")}, initialize+"\n")
		if err == nil || len(stdout) != 0 || !strings.Contains(string(stderr), "resolve sqlite parent") {
			t.Fatalf("unexpected startup: err=%v stdout=%s stderr=%s", err, stdout, stderr)
		}
		if _, err := os.Stat(parent); !os.IsNotExist(err) {
			t.Fatalf("explicit parent was created: %v", err)
		}
	})

	t.Run("home file fails clearly", func(t *testing.T) {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, ".caveman"), []byte("keep me"), 0o600); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, err := runRecoveryProcess(binary, home, nil, initialize+"\n")
		if err == nil || len(stdout) != 0 || !strings.Contains(string(stderr), "create recovery directory") {
			t.Fatalf("unexpected startup: err=%v stdout=%s stderr=%s", err, stdout, stderr)
		}
	})

	t.Run("database symlink remains rejected", func(t *testing.T) {
		home := t.TempDir()
		storeHome := filepath.Join(home, ".caveman")
		if err := os.Mkdir(storeHome, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(home, "untouched")
		if err := os.WriteFile(target, []byte("keep me"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(storeHome, "ccr.db")); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("symlink privilege unavailable: %v", err)
			}
			t.Fatal(err)
		}
		stdout, stderr, err := runRecoveryProcess(binary, home, nil, initialize+"\n")
		if err == nil || len(stdout) != 0 || !strings.Contains(string(stderr), "secure sqlite") {
			t.Fatalf("unexpected startup: err=%v stdout=%s stderr=%s", err, stdout, stderr)
		}
		data, err := os.ReadFile(target)
		if err != nil || string(data) != "keep me" {
			t.Fatalf("symlink target changed: %q err=%v", data, err)
		}
	})
}

func assertRecoveryProcessStats(t *testing.T, response []byte, requests, before, after int) {
	t.Helper()
	var stats struct {
		Requests     int    `json:"requests"`
		TokensBefore int    `json:"tokens_before"`
		TokensAfter  int    `json:"tokens_after"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal([]byte(recoveryToolText(t, response)), &stats); err != nil {
		t.Fatal(err)
	}
	if stats.Requests != requests || stats.TokensBefore != before || stats.TokensAfter != after || stats.Scope != "session" {
		t.Fatalf("session stats=%+v, want requests=%d before=%d after=%d scope=session", stats, requests, before, after)
	}
}

func runRecoveryProcess(binary, home string, overrides []string, input string) ([]byte, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		key = strings.ToUpper(key)
		if key == "HOME" || key == "USERPROFILE" || strings.HasPrefix(key, "CAVEMAN_") || strings.HasPrefix(key, "CAVE_") {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "HOME="+home, "USERPROFILE="+home)
	cmd.Env = append(cmd.Env, overrides...)
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func recoveryToolText(t *testing.T, response []byte) string {
	t.Helper()
	var envelope struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Result.IsError || len(envelope.Result.Content) != 1 {
		t.Fatalf("tool failed: %s", response)
	}
	return envelope.Result.Content[0].Text
}
