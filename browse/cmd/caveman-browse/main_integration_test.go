//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/browse"
)

const directHelperArgsEnv = "CAVEMAN_BROWSE_DIRECT_HELPER_ARGS"

func TestDirectCLIHelper(t *testing.T) {
	raw := os.Getenv(directHelperArgsEnv)
	if raw == "" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	runDirect(slog.New(slog.NewTextHandler(os.Stderr, nil)), args)
	os.Exit(0)
}

func TestDirectCommandsPersistChromeAcrossProcesses(t *testing.T) {
	chrome := os.Getenv("CAVEMAN_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("CAVEMAN_BROWSE_CHROME is required")
	}
	port := freePort(t)
	home := t.TempDir()
	baseEnv := append(os.Environ(),
		"CAVEMAN_HOME="+home,
		"CAVEMAN_BROWSE_CHROME="+chrome,
		"CAVEMAN_BROWSE_PORT="+strconv.Itoa(port),
	)
	closed := false
	t.Cleanup(func() {
		if !closed {
			_, _ = runDirectHelper(baseEnv, []string{"close"})
		}
	})

	html := `<main><h1>Browse Proof</h1><button onclick="document.querySelector('h1').textContent='Saved'">Save</button></main>`
	pageURL := "data:text/html," + url.PathEscape(html)
	snapshot, err := runDirectHelper(baseEnv, []string{"snapshot", pageURL, "Save"})
	if err != nil {
		t.Fatalf("snapshot subprocess: %v\n%s", err, snapshot)
	}
	var payload struct {
		UIDs           string `json:"uids"`
		RecoveryHandle string `json:"recovery_handle"`
		TokensBefore   int    `json:"tokens_before"`
		TokensAfter    int    `json:"tokens_after"`
	}
	if err := json.Unmarshal(firstLine(snapshot), &payload); err != nil {
		t.Fatalf("decode snapshot %q: %v", snapshot, err)
	}
	uid := compactUID(payload.UIDs, "Save")
	if uid == "" || payload.RecoveryHandle == "" || payload.TokensAfter >= payload.TokensBefore {
		t.Fatalf("snapshot missing efficient actionable state: %+v", payload)
	}

	act, err := runDirectHelper(baseEnv, []string{"act", uid, "click"})
	if err != nil {
		t.Fatalf("act subprocess could not reattach: %v\n%s", err, act)
	}
	var action struct {
		OK      bool `json:"ok"`
		Settled bool `json:"settled"`
	}
	if err := json.Unmarshal(firstLine(act), &action); err != nil || !action.OK || action.Settled {
		t.Fatalf("action result must be dispatched but unverified: %s err=%v", act, err)
	}

	evaluated, err := runDirectHelper(baseEnv, []string{"eval", "document.querySelector('h1').textContent"})
	if err != nil {
		t.Fatalf("eval subprocess could not reattach: %v\n%s", err, evaluated)
	}
	var result struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(firstLine(evaluated), &result); err != nil || result.Result != "Saved" {
		t.Fatalf("cross-process click did not mutate page: %s err=%v", evaluated, err)
	}

	closedOutput, err := runDirectHelper(baseEnv, []string{"close"})
	if err != nil {
		t.Fatalf("close subprocess: %v\n%s", err, closedOutput)
	}
	closed = true
	deadline := time.Now().Add(3 * time.Second)
	endpoint := "http://127.0.0.1:" + strconv.Itoa(port)
	for chromeHealthy(endpoint) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if chromeHealthy(endpoint) {
		t.Fatal("browse close left detached Chrome running")
	}
}

func TestFailedFirstSnapshotClosesNewOwnedChrome(t *testing.T) {
	chrome := os.Getenv("CAVEMAN_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("CAVEMAN_BROWSE_CHROME is required")
	}
	port := freePort(t)
	home := t.TempDir()
	baseEnv := append(os.Environ(),
		"CAVEMAN_HOME="+home,
		"CAVEMAN_BROWSE_CHROME="+chrome,
		"CAVEMAN_BROWSE_PORT="+strconv.Itoa(port),
	)
	output, err := runDirectHelper(baseEnv, []string{"snapshot", "file:///etc/passwd"})
	if err == nil || !strings.Contains(output, "cave_browser_url_denied") {
		t.Fatalf("unsafe first snapshot did not fail closed: err=%v output=%s", err, output)
	}
	endpoint := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(3 * time.Second)
	for chromeHealthy(endpoint) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if chromeHealthy(endpoint) {
		t.Fatal("failed first snapshot orphaned owned Chrome")
	}
	if _, err := os.Stat(home + "/browse-session.json"); !os.IsNotExist(err) {
		t.Fatalf("failed first snapshot persisted unusable state: %v", err)
	}
}

func TestCloseNeverShutsExternalCDPEndpoint(t *testing.T) {
	chrome := os.Getenv("CAVEMAN_BROWSE_CHROME")
	if chrome == "" {
		t.Skip("CAVEMAN_BROWSE_CHROME is required")
	}
	port := freePort(t)
	home := t.TempDir()
	t.Setenv("CAVEMAN_HOME", home)
	t.Setenv("CAVEMAN_BROWSE_CHROME", chrome)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	_ = launchDirectChrome(logger, port)
	endpoint := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(5 * time.Second)
	for !chromeHealthy(endpoint) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !chromeHealthy(endpoint) {
		t.Fatal("external test Chrome did not start")
	}
	t.Cleanup(func() {
		driver, err := browse.NewCDPDriver(context.Background(), browse.CDPOptions{Endpoint: endpoint})
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = driver.Shutdown(ctx)
	})
	baseEnv := append(os.Environ(),
		"CAVEMAN_HOME="+home,
		"CAVEMAN_BROWSE_CDP="+endpoint,
	)
	pageURL := "data:text/html," + url.PathEscape(`<main><button>External</button></main>`)
	if output, err := runDirectHelper(baseEnv, []string{"snapshot", pageURL, "External"}); err != nil {
		t.Fatalf("external snapshot: %v\n%s", err, output)
	}
	closed, err := runDirectHelper(baseEnv, []string{"close"})
	if err != nil || !strings.Contains(closed, "external CDP browser left running") {
		t.Fatalf("external close result: err=%v output=%s", err, closed)
	}
	if !chromeHealthy(endpoint) {
		t.Fatal("close terminated externally owned Chrome")
	}
}

func runDirectHelper(baseEnv []string, args []string) (string, error) {
	b, _ := json.Marshal(args)
	env := append(append([]string{}, baseEnv...), directHelperArgsEnv+"="+string(b))
	cmd := exec.Command(os.Args[0], "-test.run=^TestDirectCLIHelper$")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func firstLine(output string) []byte {
	line, _, _ := strings.Cut(output, "\n")
	return []byte(line)
}

func compactUID(snapshot, name string) string {
	re := regexp.MustCompile(`(?m)^\s*\[([^]]+)\]\s+[^\n]*` + regexp.QuoteMeta(strconv.Quote(name)))
	match := re.FindStringSubmatch(snapshot)
	if len(match) == 2 {
		return match[1]
	}
	return ""
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}
