package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/JuliusBrussee/caveman/mcp"
	"github.com/JuliusBrussee/caveman/mem"
)

// helperBinaryName appends .exe on Windows so exec finds the built helper.
func helperBinaryName(dir string) string {
	name := filepath.Join(dir, "cavemem")
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func testStore(t *testing.T) *mem.Store {
	t.Helper()
	s, err := mem.Open(mem.Options{InMemory: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestHandleArgsDispatch(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string // substring; "" means stdout must be empty
		wantStderr string // substring; "" means no assertion
	}{
		{name: "remember prints id", args: []string{"remember", "a durable fact about vault"}, wantCode: 0, wantStdout: `"id"`},
		{name: "remember missing text", args: []string{"remember"}, wantCode: 1, wantStderr: "usage: cavemem remember"},
		{name: "remember empty text", args: []string{"remember", "   "}, wantCode: 1, wantStderr: "remember:"},
		{name: "recall missing query", args: []string{"recall"}, wantCode: 1, wantStderr: "usage: cavemem recall"},
		{name: "recall unknown query recalls nothing", args: []string{"recall", "totally unrelated topic"}, wantCode: 0, wantStdout: `"hits": []`},
		{name: "recover missing handle", args: []string{"recover"}, wantCode: 1, wantStderr: "usage: cavemem recover"},
		{name: "recover unknown handle", args: []string{"recover", "ccr_deadbeef"}, wantCode: 1, wantStderr: "recover:"},
		{name: "history missing id", args: []string{"history"}, wantCode: 1, wantStderr: "usage: cavemem history"},
		{name: "forget unknown id reports false", args: []string{"forget", "mem_absent"}, wantCode: 0, wantStdout: `"forgotten": false`},
		{name: "help", args: []string{"help"}, wantCode: 0, wantStderr: "cavemem [mcp]"},
		{name: "unknown subcommand exits two", args: []string{"frobnicate"}, wantCode: 2, wantStderr: "unknown cavemem subcommand: frobnicate"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := testStore(t)
			var stdout, stderr bytes.Buffer
			code := handleArgs(store, tc.args, strings.NewReader(""), &stdout, &stderr)
			if code != tc.wantCode {
				t.Fatalf("code=%d want %d (stderr=%q)", code, tc.wantCode, stderr.String())
			}
			if tc.wantStdout == "" {
				if stdout.Len() != 0 {
					t.Fatalf("stdout=%q want empty", stdout.String())
				}
			} else if !strings.Contains(stdout.String(), tc.wantStdout) {
				t.Fatalf("stdout=%q want substring %q", stdout.String(), tc.wantStdout)
			}
			if tc.wantStderr != "" && !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Fatalf("stderr=%q want substring %q", stderr.String(), tc.wantStderr)
			}
		})
	}
}

func TestHandleArgsRememberRecallRoundTrip(t *testing.T) {
	store := testStore(t)
	var out, errBuf bytes.Buffer
	if code := handleArgs(store, []string{"remember", "the staging cluster runs in eu-west-1"}, strings.NewReader(""), &out, &errBuf); code != 0 {
		t.Fatalf("remember code=%d stderr=%q", code, errBuf.String())
	}
	var remembered struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out.Bytes(), &remembered); err != nil || remembered.ID == "" {
		t.Fatalf("decode remember output %q: %v", out.String(), err)
	}

	out.Reset()
	errBuf.Reset()
	if code := handleArgs(store, []string{"recall", "which region does the staging cluster run in"}, strings.NewReader(""), &out, &errBuf); code != 0 {
		t.Fatalf("recall code=%d stderr=%q", code, errBuf.String())
	}
	var recalled struct {
		Hits []struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(out.Bytes(), &recalled); err != nil {
		t.Fatalf("decode recall output %q: %v", out.String(), err)
	}
	if len(recalled.Hits) == 0 || recalled.Hits[0].ID != remembered.ID {
		t.Fatalf("expected to recall the remembered memory %s, got %+v", remembered.ID, recalled.Hits)
	}
}

func TestHandleArgsRecallTokenBudget(t *testing.T) {
	store := testStore(t)
	if _, err := store.Remember("kubernetes ingress routing uses nginx with regional failover"); err != nil {
		t.Fatalf("remember: %v", err)
	}

	recall := func(args ...string) []mem.Hit {
		t.Helper()
		var out, errBuf bytes.Buffer
		if code := handleArgs(store, append([]string{"recall"}, args...), strings.NewReader(""), &out, &errBuf); code != 0 {
			t.Fatalf("recall %v code=%d stderr=%q", args, code, errBuf.String())
		}
		var payload struct {
			Hits []mem.Hit `json:"hits"`
		}
		if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
			t.Fatalf("decode recall output %q: %v", out.String(), err)
		}
		return payload.Hits
	}

	bounded := recall("kubernetes ingress routing", "5", "1")
	unlimited := recall("kubernetes ingress routing", "5", "0")
	if len(bounded) != 1 || bounded[0].TokensAdded > 1 {
		t.Fatalf("one-token recall = %+v", bounded)
	}
	if len(unlimited) != 1 || unlimited[0].TokensAdded <= bounded[0].TokensAdded {
		t.Fatalf("explicit unlimited recall did not exceed bounded result: bounded=%+v unlimited=%+v", bounded, unlimited)
	}

	var out, errBuf bytes.Buffer
	if code := handleArgs(store, []string{"recall", "query", "5", "-1"}, strings.NewReader(""), &out, &errBuf); code != 1 {
		t.Fatalf("negative public budget code=%d want 1", code)
	}
	if !strings.Contains(errBuf.String(), "non-negative integer") {
		t.Fatalf("stderr=%q want validation error", errBuf.String())
	}
}

func TestRecallToolExposesAndMapsTokenBudget(t *testing.T) {
	store := testStore(t)
	if _, err := store.Remember("kubernetes ingress routing uses nginx with regional failover"); err != nil {
		t.Fatalf("remember: %v", err)
	}
	var recallTool mcp.Tool
	for _, tool := range memTools(store) {
		if tool.Name == "cavemem_recall" {
			recallTool = tool
			break
		}
	}
	if recallTool.Handler == nil {
		t.Fatal("cavemem_recall tool missing")
	}
	properties := recallTool.InputSchema["properties"].(map[string]any)
	budgetSchema := properties["token_budget"].(map[string]any)
	if budgetSchema["default"] != mem.DefaultTokenBudget || budgetSchema["minimum"] != 0 {
		t.Fatalf("token_budget schema=%v", budgetSchema)
	}

	call := func(raw string) []mem.Hit {
		t.Helper()
		result := recallTool.Handler(json.RawMessage(raw))
		if result.IsError || len(result.Content) != 1 {
			t.Fatalf("tool result=%+v", result)
		}
		var payload struct {
			Hits []mem.Hit `json:"hits"`
		}
		if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
			t.Fatalf("decode tool result %q: %v", result.Content[0].Text, err)
		}
		return payload.Hits
	}
	bounded := call(`{"query":"kubernetes ingress routing","limit":5,"token_budget":1}`)
	unlimited := call(`{"query":"kubernetes ingress routing","limit":5,"token_budget":0}`)
	if len(bounded) != 1 || len(unlimited) != 1 || unlimited[0].TokensAdded <= bounded[0].TokensAdded {
		t.Fatalf("MCP sentinel not effective: bounded=%+v unlimited=%+v", bounded, unlimited)
	}
	if result := recallTool.Handler(json.RawMessage(`{"query":"q","token_budget":-1}`)); !result.IsError {
		t.Fatalf("negative public budget must fail: %+v", result)
	}
}

func TestExternalTokenBudgetMapping(t *testing.T) {
	if got, err := externalTokenBudget(nil); err != nil || got != 0 {
		t.Fatalf("omitted = %d, %v; want Go default", got, err)
	}
	zero, positive, negative := 0, 17, -1
	if got, err := externalTokenBudget(&zero); err != nil || got != mem.UnlimitedTokenBudget {
		t.Fatalf("zero = %d, %v; want unlimited sentinel", got, err)
	}
	if got, err := externalTokenBudget(&positive); err != nil || got != positive {
		t.Fatalf("positive = %d, %v", got, err)
	}
	if _, err := externalTokenBudget(&negative); err == nil {
		t.Fatal("negative public budget must fail")
	}
}

func TestHandleArgsRememberTooLarge(t *testing.T) {
	store := testStore(t)
	var out, errBuf bytes.Buffer
	code := handleArgs(store, []string{"remember", strings.Repeat("x", mem.MaxMemoryBytes+1)}, strings.NewReader(""), &out, &errBuf)
	if code != 65 {
		t.Fatalf("code=%d want 65", code)
	}
	if !strings.Contains(errBuf.String(), "cave_memory_too_large") {
		t.Fatalf("stderr=%q want cave_memory_too_large", errBuf.String())
	}
}

func TestRememberStdinTooLargeSubprocessExits65(t *testing.T) {
	tmp := t.TempDir()
	bin := helperBinaryName(tmp)
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build cavemem: %v", err)
	}
	cmd := exec.Command(bin, "remember", "--stdin")
	cmd.Env = append(os.Environ(), "CAVEMAN_HOME="+filepath.Join(tmp, "home"))
	cmd.Stdin = strings.NewReader(strings.Repeat("x", mem.MaxMemoryBytes+1))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 65 {
		t.Fatalf("err=%v exit=%v stderr=%q want exit 65", err, exitErr, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cave_memory_too_large") {
		t.Fatalf("stderr=%q want cave_memory_too_large", stderr.String())
	}
}

// TestConcurrentRememberSubprocesses drives N independent cavemem processes at
// one shared mem.db file — the real shape of Promise.all(facts.map(remember))
// from the JS client, which each spawn a fresh binary. On the pre-fix store the
// contending processes returned SQLITE_BUSY and dropped most writes; the
// busy_timeout(5000)+WAL DSN makes them wait so every write lands.
func TestConcurrentRememberSubprocesses(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and execs the cavemem binary; skipped under -short")
	}
	tmp := t.TempDir()
	bin := helperBinaryName(tmp)
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build cavemem: %v", err)
	}

	home := filepath.Join(tmp, "home")
	const n = 24
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(bin, "remember", fmtFact(i))
			cmd.Env = append(os.Environ(), "CAVEMAN_HOME="+home)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				errs <- &subprocErr{i: i, err: err, stderr: stderr.String()}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("%v", err)
	}

	store, err := mem.Open(mem.Options{Dir: filepath.Join(home, "mem")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	count, err := store.Count()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != n {
		t.Fatalf("only %d of %d subprocess writes landed", count, n)
	}
}

type subprocErr struct {
	i      int
	err    error
	stderr string
}

func (e *subprocErr) Error() string {
	return "remember " + strconv.Itoa(e.i) + ": " + e.err.Error() + " (" + strings.TrimSpace(e.stderr) + ")"
}

func fmtFact(i int) string {
	return "subprocess memory number " + strconv.Itoa(i) + " about distinct topic " + strconv.Itoa(i)
}
