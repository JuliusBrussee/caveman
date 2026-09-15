package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/JuliusBrussee/caveman/browse"
	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/mcp"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if len(os.Args) > 1 {
		runDirect(logger, os.Args[1:])
		return
	}

	store, err := openRecoveryStore()
	if err != nil {
		logger.Error("open recovery store", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	driver, err := browse.NewCDPDriver(context.Background(), browse.CDPOptions{
		Endpoint:    os.Getenv("CAVEMAN_BROWSE_CDP"),
		UserDataDir: os.Getenv("CAVEMAN_BROWSE_USER_DATA_DIR"),
		BrowserPath: os.Getenv("CAVEMAN_BROWSE_CHROME"),
		Headless:    os.Getenv("CAVEMAN_BROWSE_HEADFUL") != "1",
	})
	if err != nil {
		logger.Error("open browser driver", "err", err)
		os.Exit(1)
	}
	defer driver.Close()

	eng := engine.New(store, nil)
	session := browse.NewSession(eng, driver, logger)
	defer session.Close()

	srv := mcp.NewServer("caveman-browse", browse.BrowserTools(session), logger)
	if err := srv.Serve(os.Stdin, os.Stdout); err != nil {
		logger.Error("serve", "err", err)
		os.Exit(1)
	}
}

func openRecoveryStore() (*ccr.Store, error) {
	if os.Getenv("CAVEMAN_BROWSE_EPHEMERAL") == "1" {
		return ccr.OpenMemory()
	}
	path := os.Getenv("CAVEMAN_CCR_DB")
	if path == "" {
		home := os.Getenv("CAVEMAN_HOME")
		if home == "" {
			h, err := os.UserHomeDir()
			if err != nil {
				return ccr.OpenMemory()
			}
			home = filepath.Join(h, ".caveman")
		}
		path = filepath.Join(home, "ccr.db")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return ccr.Open(path)
}

type directState struct {
	Endpoint string                   `json:"endpoint"`
	TargetID string                   `json:"target_id"`
	Targets  map[string]browse.Target `json:"targets"`
	Owned    bool                     `json:"owned"`
}

func runDirect(logger *slog.Logger, args []string) {
	store, err := openRecoveryStore()
	if err != nil {
		logger.Error("open recovery store", "err", err)
		os.Exit(1)
	}
	defer store.Close()
	eng := engine.New(store, nil)
	switch args[0] {
	case "snapshot":
		session, driver, endpoint, owned, started := directBrowserSession(logger, eng, "")
		url := ""
		if len(args) > 1 {
			url = args[1]
		}
		query := ""
		if len(args) > 2 {
			query = strings.Join(args[2:], " ")
		}
		res := callTool(session, browse.ToolSnapshot, map[string]any{"url": url, "query": query})
		if !res.IsError {
			saveDirectState(logger, directState{Endpoint: endpoint, TargetID: driver.TargetID(), Targets: session.TargetsSnapshot(), Owned: owned})
		} else if started {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := driver.Shutdown(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("close browser after failed first snapshot", "err", err)
			}
			cancel()
		}
		writeDirectResult(res)
	case "act":
		if len(args) < 3 {
			logger.Error("usage: caveman-browse act <uid> <action> [text]")
			os.Exit(2)
		}
		state := loadDirectState(logger)
		session, _, _, _, _ := directBrowserSession(logger, eng, state.TargetID)
		session.LoadTargets(state.Targets)
		payload := map[string]any{"uid": args[1], "action": args[2]}
		if len(args) > 3 {
			text := strings.Join(args[3:], " ")
			payload["text"] = text
			payload["option"] = text
		}
		writeDirectResult(callTool(session, browse.ToolAct, payload))
	case "eval":
		if len(args) < 2 {
			logger.Error("usage: caveman-browse eval <expression>")
			os.Exit(2)
		}
		state := loadDirectState(logger)
		session, _, _, _, _ := directBrowserSession(logger, eng, state.TargetID)
		writeDirectResult(callTool(session, browse.ToolEval, map[string]any{"expression": strings.Join(args[1:], " ")}))
	case "recover":
		if len(args) < 2 {
			logger.Error("usage: caveman-browse recover <handle> [query]")
			os.Exit(2)
		}
		session := browse.NewSession(eng, nilDriver{}, logger)
		if len(args) > 2 {
			writeDirectResult(callTool(session, browse.ToolRecover, map[string]any{"recovery_handle": args[1], "query": strings.Join(args[2:], " ")}))
		} else {
			writeDirectResult(callTool(session, browse.ToolRecover, map[string]any{"recovery_handle": args[1]}))
		}
	case "close":
		closeDirectBrowser(logger, eng)
	default:
		logger.Error("unknown caveman-browse command", "command", args[0])
		os.Exit(2)
	}
}

func callTool(session *browse.Session, name string, payload any) mcp.ToolResult {
	b, _ := json.Marshal(payload)
	for _, tool := range browse.BrowserTools(session) {
		if tool.Name == name {
			return tool.Handler(b)
		}
	}
	return mcp.ToolError("cave_unknown_tool", "unknown tool")
}

func writeDirectResult(res mcp.ToolResult) {
	if len(res.Content) > 0 {
		if res.IsError {
			os.Stderr.WriteString(res.Content[0].Text)
			os.Stderr.WriteString("\n")
		} else {
			os.Stdout.WriteString(res.Content[0].Text)
			os.Stdout.WriteString("\n")
		}
	}
	if res.IsError {
		os.Exit(1)
	}
}

func directBrowserSession(logger *slog.Logger, eng *engine.Engine, targetID string) (*browse.Session, *browse.CDPDriver, string, bool, bool) {
	endpoint, owned, started := directEndpoint(logger)
	driver, err := browse.NewCDPDriver(context.Background(), browse.CDPOptions{
		Endpoint:    endpoint,
		BrowserPath: os.Getenv("CAVEMAN_BROWSE_CHROME"),
		Headless:    os.Getenv("CAVEMAN_BROWSE_HEADFUL") != "1",
		TargetID:    targetID,
	})
	if err != nil {
		logger.Error("open browser driver", "err", err)
		os.Exit(1)
	}
	return browse.NewSession(eng, driver, logger), driver, endpoint, owned, started
}

func directEndpoint(logger *slog.Logger) (string, bool, bool) {
	if endpoint := os.Getenv("CAVEMAN_BROWSE_CDP"); endpoint != "" {
		return endpoint, false, false
	}
	port := 9333
	if raw := os.Getenv("CAVEMAN_BROWSE_PORT"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 65535 {
			logger.Error("invalid CAVEMAN_BROWSE_PORT", "value", raw)
			os.Exit(2)
		}
		port = parsed
	}
	endpoint := "http://127.0.0.1:" + strconv.Itoa(port)
	if chromeHealthy(endpoint) {
		return endpoint, directStateOwnsEndpoint(endpoint), false
	}
	pid := launchDirectChrome(logger, port)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if chromeHealthy(endpoint) {
			return endpoint, true, true
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := terminateDetachedProcess(pid); err != nil {
		logger.Warn("terminate unready Chrome", "pid", pid, "err", err)
	}
	logger.Error("browser did not become ready", "endpoint", endpoint)
	os.Exit(1)
	return endpoint, false, false
}

func directStateOwnsEndpoint(endpoint string) bool {
	b, err := os.ReadFile(statePath())
	if err != nil {
		return false
	}
	var state directState
	return json.Unmarshal(b, &state) == nil && state.Owned && state.Endpoint == endpoint
}

func chromeHealthy(endpoint string) bool {
	client := http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(strings.TrimRight(endpoint, "/") + "/json/version")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	var version struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&version); err != nil {
		return false
	}
	return strings.HasPrefix(version.WebSocketDebuggerURL, "ws://") || strings.HasPrefix(version.WebSocketDebuggerURL, "wss://")
}

func launchDirectChrome(logger *slog.Logger, port int) int {
	chrome := os.Getenv("CAVEMAN_BROWSE_CHROME")
	if chrome == "" {
		chrome = defaultChromePath()
	}
	if chrome == "" {
		logger.Error("cannot find Chrome; set CAVEMAN_BROWSE_CHROME")
		os.Exit(1)
	}
	profile := os.Getenv("CAVEMAN_BROWSE_USER_DATA_DIR")
	if profile == "" {
		profile = browse.DefaultUserDataDir(os.Getenv("CAVEMAN_HOME"))
	}
	if err := os.MkdirAll(profile, 0o700); err != nil {
		logger.Error("create browse profile", "err", err)
		os.Exit(1)
	}
	args := []string{
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-gpu",
		"--remote-debugging-port=" + strconv.Itoa(port),
		"--user-data-dir=" + profile,
		"about:blank",
	}
	if os.Getenv("CAVEMAN_BROWSE_HEADFUL") != "1" {
		args = append([]string{"--headless=new"}, args...)
	}
	logPath := filepath.Join(filepath.Dir(statePath()), "browse-chrome.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		logger.Error("open Chrome log", "err", err)
		os.Exit(1)
	}
	cmd := exec.Command(chrome, args...)
	configureDetachedProcess(cmd)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		logger.Error("launch Chrome", "err", err)
		os.Exit(1)
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		logger.Warn("release Chrome process", "err", err)
	}
	_ = logFile.Close()
	return pid
}

func closeDirectBrowser(logger *slog.Logger, eng *engine.Engine) {
	state := loadDirectState(logger)
	if !state.Owned {
		_ = os.Remove(statePath())
		writeDirectResult(mcp.ToolText(map[string]any{"ok": true, "note": "external CDP browser left running; local state removed"}))
		return
	}
	if !chromeHealthy(state.Endpoint) {
		_ = os.Remove(statePath())
		writeDirectResult(mcp.ToolText(map[string]any{"ok": true, "note": "browser already closed; stale state removed"}))
		return
	}
	_, driver, _, _, _ := directBrowserSession(logger, eng, state.TargetID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := driver.Shutdown(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("close browser", "err", err)
		os.Exit(1)
	}
	_ = os.Remove(statePath())
	writeDirectResult(mcp.ToolText(map[string]any{"ok": true}))
}

func defaultChromePath() string {
	candidates := defaultChromeCandidates(runtime.GOOS, os.Getenv)
	for _, path := range candidates {
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path
		}
	}
	for _, name := range []string{"google-chrome", "chromium", "chrome"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}

func defaultChromeCandidates(goos string, getenv func(string) string) []string {
	if goos == "darwin" {
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	}
	if goos != "windows" {
		return nil
	}
	roots := []string{
		getenv("LOCALAPPDATA"),
		getenv("PROGRAMFILES"),
		getenv("PROGRAMFILES(X86)"),
		getenv("ProgramW6432"),
	}
	var candidates []string
	seen := map[string]bool{}
	for _, root := range roots {
		if root == "" {
			continue
		}
		for _, relative := range []string{
			filepath.Join("Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join("Chromium", "Application", "chrome.exe"),
		} {
			candidate := filepath.Join(root, relative)
			key := strings.ToLower(candidate)
			if !seen[key] {
				seen[key] = true
				candidates = append(candidates, candidate)
			}
		}
	}
	return candidates
}

func statePath() string {
	home := os.Getenv("CAVEMAN_HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".caveman")
		}
	}
	return filepath.Join(home, "browse-session.json")
}

func saveDirectState(logger *slog.Logger, state directState) {
	if state.Endpoint == "" || state.TargetID == "" {
		return
	}
	if state.Targets == nil {
		state.Targets = map[string]browse.Target{}
	}
	path := statePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		logger.Warn("create state dir", "err", err)
		return
	}
	b, _ := json.MarshalIndent(state, "", "  ")
	tmp, err := os.CreateTemp(filepath.Dir(path), ".browse-session-*")
	if err != nil {
		logger.Warn("create direct state", "err", err)
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	err = tmp.Chmod(0o600)
	if err == nil {
		_, err = tmp.Write(append(b, '\n'))
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmpPath, path)
	}
	if err != nil {
		logger.Warn("write direct state", "err", err)
	}
}

func loadDirectState(logger *slog.Logger) directState {
	b, err := os.ReadFile(statePath())
	if err != nil {
		logger.Error("no prior browser_snapshot state; run `caveman browse <url>` first", "err", err)
		os.Exit(1)
	}
	var state directState
	if err := json.Unmarshal(b, &state); err != nil || state.TargetID == "" || state.Targets == nil {
		logger.Error("invalid prior browser_snapshot state", "err", err)
		os.Exit(1)
	}
	if state.Endpoint != "" && os.Getenv("CAVEMAN_BROWSE_CDP") == "" {
		os.Setenv("CAVEMAN_BROWSE_CDP", state.Endpoint)
	}
	return state
}

type nilDriver struct{}

func (nilDriver) Snapshot(context.Context, string, time.Duration) ([]byte, error) {
	return nil, context.Canceled
}
func (nilDriver) Act(context.Context, browse.ActionRequest, browse.Target) (browse.ActionResult, error) {
	return browse.ActionResult{}, context.Canceled
}
func (nilDriver) Eval(context.Context, string) (any, error) { return nil, context.Canceled }
func (nilDriver) Close() error                              { return nil }
