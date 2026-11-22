// Command caveman-mcp is the Caveman MCP server over stdio. An MCP host (Claude
// Code, Cursor, …) spawns it and speaks JSON-RPC on stdin/stdout; the three
// caveman_* tools wrap the Caveman Engine. Logs go to stderr only — stdout
// is the protocol channel.
//
// By default it opens the SHARED file recovery store (CAVEMAN_CCR_DB, else
// ~/.caveman/ccr.db) — the same store the Caveman gateway writes — so a
// caveman_retrieve here resolves handles the proxy disclosed (this is what lets a
// wrapped agent recover proxy-elided detail on streaming requests). Set
// CAVEMAN_MCP_EPHEMERAL=1 for a fresh in-memory store that touches no disk.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/mcp"
	"github.com/JuliusBrussee/caveman/mcp/codemode"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "codemode" {
		os.Exit(runCodemode(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "tools" {
		os.Exit(runCompanionTools(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	if handled, code := handleArgs(os.Args[1:], os.Stdout, os.Stderr); handled {
		os.Exit(code)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	store, err := openRecoveryStore()
	if err != nil {
		logger.Error("open recovery store", "err", err)
		os.Exit(1)
	}
	defer store.Close()

	eng := engine.New(store, nil)
	srv := mcp.NewServerVersion("caveman", version, mcp.EngineTools(eng, logger), logger)
	if err := srv.Serve(os.Stdin, os.Stdout); err != nil {
		logger.Error("serve", "err", err)
		os.Exit(1)
	}
}

func runCodemode(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("codemode", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "Path to JSON MCP server config file (mcpServers)")
	workspace := fs.String("workspace", "", "Workspace root directory (defaults to cwd)")
	discover := fs.Bool("discover", false, "Auto-discover MCP servers from user-home agent configs (Claude, Codex, Gemini, Qwen, OpenCode, Cursor, OpenClaw, Hermes)")
	trustWorkspace := fs.Bool("trust-workspace", false, "Also auto-discover workspace-local .mcp.json/.gemini/.qwen configs during --discover (opt-in for trusted repos)")
	noBuiltins := fs.Bool("no-builtins", false, "Disable built-in repo/pr/browse structured upstreams")
	noJS := fs.Bool("no-js", false, "Disable embedded QuickJS sandbox (use pure-Go pipeline steps only)")
	allowWritesInExecute := fs.Bool("allow-writes-in-execute", false, "Allow mutating tools inside codemode_execute (default: read-only execute + separate codemode_mutate)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ws := *workspace
	if ws == "" {
		ws, _ = os.Getwd()
	}

	store, err := openRecoveryStore()
	if err != nil {
		logger.Error("open recovery store", "err", err)
		return 1
	}
	defer store.Close()

	eng := engine.New(store, nil)
	gw := codemode.NewGateway(store, eng, logger)
	defer gw.Close()
	if *noJS {
		gw.SetJSRunnerEnabled(false)
	}
	if *allowWritesInExecute {
		gw.SetAllowWritesInExecute(true)
	}

	ctx := context.Background()
	if !*noBuiltins {
		for _, up := range []codemode.Upstream{
			codemode.NewRepoUpstream(ws, store, eng),
			codemode.NewPRUpstream(ws, store, eng, nil),
			codemode.NewBrowseUpstream(eng, logger, nil),
		} {
			if err := gw.AddUpstream(ctx, up); err != nil {
				logger.Warn("register builtin upstream failed", "upstream", up.Name(), "err", err)
			}
		}
	}

	servers := map[string]codemode.ServerConfig{}
	if *discover || *configPath == "" {
		home, _ := os.UserHomeDir()
		discoverWS := ""
		if *trustWorkspace {
			discoverWS = ws
		}
		for k, v := range codemode.DiscoverUserMCPConfigs(discoverWS, home) {
			servers[k] = v
		}
	}
	if *configPath != "" {
		loaded, err := codemode.LoadConfigFile(*configPath)
		if err != nil {
			logger.Error("load codemode config", "path", *configPath, "err", err)
			return 1
		}
		for k, v := range loaded {
			servers[k] = v
		}
	}

	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		up, err := codemode.StartUpstream(ctx, name, servers[name], logger)
		if err != nil {
			logger.Warn("downstream mcp server skipped", "server", name, "err", err)
			continue
		}
		if err := gw.AddUpstream(ctx, up); err != nil {
			logger.Warn("index downstream mcp server failed", "server", name, "err", err)
			if closer, ok := up.(io.Closer); ok {
				_ = closer.Close()
			}
		}
	}

	srv := mcp.NewServerVersion("caveman-codemode", version, gw.Tools(), logger)
	if err := srv.Serve(stdin, stdout); err != nil {
		logger.Error("serve codemode", "err", err)
		return 1
	}
	return 0
}

// runCompanionTools serves Caveman's bounded structured tools (repo, pr,
// optional lazy browse, and caveman_retrieve) directly as flat MCP tools
// without wrapping external MCP servers — designed to plug straight into
// harnesses with native codemodes (Claude Code, Codex CLI, OpenCode).
func runCompanionTools(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tools", flag.ContinueOnError)
	fs.SetOutput(stderr)
	workspace := fs.String("workspace", "", "Workspace root directory (defaults to cwd)")
	noBrowse := fs.Bool("no-browse", false, "Disable lazy browser_snapshot/browser_act/browser_eval tools")
	noRetrieve := fs.Bool("no-retrieve", false, "Omit caveman_retrieve (use when caveman wrap already registers caveman MCP)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ws := *workspace
	if ws == "" {
		ws, _ = os.Getwd()
	}

	store, err := openRecoveryStore()
	if err != nil {
		logger.Error("open recovery store", "err", err)
		return 1
	}
	defer store.Close()

	eng := engine.New(store, nil)
	tools, cleanup := codemode.CompanionToolsWithOptions(ws, store, eng, logger, codemode.CompanionOptions{
		IncludeBrowse:   !*noBrowse,
		IncludeRetrieve: !*noRetrieve,
	})
	defer func() { _ = cleanup() }()

	srv := mcp.NewServerVersion("caveman-tools", version, tools, logger)
	if err := srv.Serve(stdin, stdout); err != nil {
		logger.Error("serve companion tools", "err", err)
		return 1
	}
	return 0
}

func handleArgs(args []string, stdout, stderr io.Writer) (bool, int) {
	if len(args) == 0 {
		return false, 0
	}
	if len(args) == 2 && args[0] == "version" && args[1] == "--json" {
		err := json.NewEncoder(stdout).Encode(map[string]any{
			"version":      version,
			"schema":       "caveman.mcp.version.v1",
			"capabilities": []string{"mcp_recovery", "build_stamped_version", "recovery_verification", "codemode"},
		})
		if err != nil {
			return true, 1
		}
		return true, 0
	}
	_, _ = io.WriteString(stderr, "usage: caveman-mcp [version --json]\n")
	return true, 2
}

// openRecoveryStore opens the shared file CCR store the proxy uses, so handles are
// resolvable across processes. CAVEMAN_MCP_EPHEMERAL=1 forces an in-memory store
// (no disk). The path mirrors the proxy: CAVEMAN_CCR_DB, else ~/.caveman/ccr.db.
func openRecoveryStore() (*ccr.Store, error) {
	if os.Getenv("CAVEMAN_MCP_EPHEMERAL") == "1" {
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
		if err := os.MkdirAll(home, 0o700); err != nil {
			return nil, fmt.Errorf("create recovery directory %q: %w", home, err)
		}
		path = filepath.Join(home, "ccr.db")
	}
	return ccr.Open(path)
}
