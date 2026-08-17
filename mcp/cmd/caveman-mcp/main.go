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
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
	"github.com/JuliusBrussee/caveman/mcp"
)

var version = "dev"

func main() {
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

func handleArgs(args []string, stdout, stderr io.Writer) (bool, int) {
	if len(args) == 0 {
		return false, 0
	}
	if len(args) == 2 && args[0] == "version" && args[1] == "--json" {
		err := json.NewEncoder(stdout).Encode(map[string]any{
			"version":      version,
			"schema":       "caveman.mcp.version.v1",
			"capabilities": []string{"mcp_recovery", "build_stamped_version"},
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
		path = filepath.Join(home, "ccr.db")
	}
	return ccr.Open(path)
}
