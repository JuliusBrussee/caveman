package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/browse"
)

func TestOpenRecoveryStoreCreatesFreshCavemanHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "new", "caveman-home")
	t.Setenv("CAVEMAN_HOME", home)
	t.Setenv("CAVEMAN_CCR_DB", "")
	t.Setenv("CAVEMAN_BROWSE_EPHEMERAL", "")
	store, err := openRecoveryStore()
	if err != nil {
		t.Fatalf("fresh home must open recovery store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "ccr.db")); err != nil {
		t.Fatalf("recovery DB missing in fresh home: %v", err)
	}
}

func TestSaveDirectStateIsPrivateAndComplete(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CAVEMAN_HOME", home)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	want := directState{
		Endpoint: "http://127.0.0.1:9444",
		TargetID: "target-1",
		Targets:  map[string]browse.Target{"ua": {BackendDOMNodeID: 10}},
		Owned:    true,
	}
	saveDirectState(logger, want)
	info, err := os.Stat(statePath())
	if err != nil {
		t.Fatal(err)
	}
	// POSIX permission bits are synthetic on Windows; NTFS ACLs govern there.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode=%o want 600", info.Mode().Perm())
	}
	b, err := os.ReadFile(statePath())
	if err != nil {
		t.Fatal(err)
	}
	var got directState
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("state file is not complete JSON: %v", err)
	}
	if got.Endpoint != want.Endpoint || got.TargetID != want.TargetID || got.Targets["ua"].BackendDOMNodeID != 10 || !got.Owned {
		t.Fatalf("state drifted: %+v", got)
	}
	if !directStateOwnsEndpoint(want.Endpoint) {
		t.Fatal("owned endpoint was not recognized from private state")
	}
}

func TestSaveDirectStateKeepsZeroTargetSessionCloseable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CAVEMAN_HOME", home)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	saveDirectState(logger, directState{
		Endpoint: "http://127.0.0.1:9555",
		TargetID: "target-empty",
		Targets:  map[string]browse.Target{},
		Owned:    true,
	})
	b, err := os.ReadFile(statePath())
	if err != nil {
		t.Fatal(err)
	}
	var got directState
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.TargetID != "target-empty" || got.Targets == nil || len(got.Targets) != 0 || !got.Owned {
		t.Fatalf("zero-target session state was dropped: %+v", got)
	}
}

func TestDefaultChromeCandidatesPreserveMacPaths(t *testing.T) {
	got := defaultChromeCandidates("darwin", func(string) string { return "" })
	want := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
	}
	if len(got) != len(want) {
		t.Fatalf("candidates=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidate[%d]=%q want %q", i, got[i], want[i])
		}
	}
}

func TestDefaultChromeCandidatesIncludeWindowsInstallRoots(t *testing.T) {
	env := map[string]string{
		"LOCALAPPDATA":      `C:\Users\cave\AppData\Local`,
		"PROGRAMFILES":      `C:\Program Files`,
		"PROGRAMFILES(X86)": `C:\Program Files (x86)`,
	}
	got := defaultChromeCandidates("windows", func(key string) string { return env[key] })
	joined := strings.Join(got, "\n")
	for _, want := range []string{
		filepath.Join(env["LOCALAPPDATA"], "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(env["PROGRAMFILES"], "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(env["PROGRAMFILES(X86)"], "Chromium", "Application", "chrome.exe"),
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("Windows Chrome candidate %q missing from %v", want, got)
		}
	}
}
