// Command cavemem is the cavemem server + CLI for durable agent memory.
//
//	cavemem [mcp]              run the MCP server over stdio (default)
//	cavemem remember <text>|--stdin store a memory; print {id,...} JSON
//	cavemem recall <query> [limit] [token_budget] recall memories; 0 budget means unlimited
//	cavemem supersede <id> <text> replace a current memory, preserving history
//	cavemem history <id>      print oldest-to-newest memory versions
//	cavemem forget <id>       delete a memory; print {forgotten:bool} JSON
//	cavemem recover <handle>  write the byte-exact original for a recall hit to stdout
//
// The MCP server exposes cavemem_remember / cavemem_recall / cavemem_supersede /
// cavemem_history / cavemem_forget. The CLI subcommands are the surface the thin
// TS/Python wrappers shell out to.
// Everything reported is `inferred`. Logs go to stderr only.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"

	"github.com/JuliusBrussee/caveman/mcp"
	"github.com/JuliusBrussee/caveman/mem"
)

const usage = "cavemem [mcp] | remember <text>|--stdin | recall <query> [limit] [token_budget] | supersede <id> <text> | history <id> | forget <id> | recover <handle>"

// EX_DATAERR: wrappers can branch on size refusal without parsing stderr.
const memoryTooLargeExitCode = 65

func main() {
	args := os.Args[1:]
	// No subcommand or an explicit "mcp" runs the server over stdio.
	if len(args) == 0 || args[0] == "mcp" {
		runMCP()
		return
	}
	// help and unknown subcommands must not create the data directory; only
	// store-backed verbs open the store.
	var store *mem.Store
	if storeCommand(args[0]) {
		store = open()
		defer store.Close()
	}
	if code := handleArgs(store, args, os.Stdin, os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
}

func storeCommand(cmd string) bool {
	switch cmd {
	case "remember", "recall", "supersede", "history", "forget", "recover":
		return true
	}
	return false
}

// handleArgs dispatches a CLI subcommand against store, writing JSON (or raw
// recovered bytes) to stdout and diagnostics to stderr, and returns the process
// exit code (0 on success). It never calls os.Exit, so it is table-testable.
// The "mcp" server path is handled in main before a store is opened.
func handleArgs(store *mem.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	switch args[0] {
	case "remember":
		return cmdRemember(store, args[1:], stdin, stdout, stderr)
	case "recall":
		return cmdRecall(store, args[1:], stdout, stderr)
	case "supersede":
		return cmdSupersede(store, args[1:], stdout, stderr)
	case "history":
		return cmdHistory(store, args[1:], stdout, stderr)
	case "forget":
		return cmdForget(store, args[1:], stdout, stderr)
	case "recover":
		return cmdRecover(store, args[1:], stdout, stderr)
	case "help", "--help", "-h":
		fmt.Fprintln(stderr, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown cavemem subcommand: %s\n", args[0])
		return 2
	}
}

func open() *mem.Store {
	store, err := mem.Open(mem.Options{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "cavemem: %v\n", err)
		os.Exit(1)
	}
	return store
}

func runMCP() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	store := open()
	defer store.Close()
	srv := mcp.NewServer("cavemem", memTools(store), logger)
	if err := srv.Serve(os.Stdin, os.Stdout); err != nil {
		logger.Error("serve", "err", err)
		os.Exit(1)
	}
}

// memTools maps the store onto cavemem MCP tools, reusing the mcp
// framing. Every result is labeled inferred.
func memTools(store *mem.Store) []mcp.Tool {
	return []mcp.Tool{
		{
			Name:        "cavemem_remember",
			Description: "Store a durable memory for recall in a later session. Returns the memory id. Idempotent: remembering identical text twice stores it once.",
			InputSchema: mcp.ObjectSchema(map[string]any{"text": mcp.StringProp("The memory to store.")}, "text"),
			Handler: func(raw json.RawMessage) mcp.ToolResult {
				var a struct {
					Text string `json:"text"`
				}
				if err := json.Unmarshal(raw, &a); err != nil {
					return mcp.ToolError("cave_invalid_arguments", "remember: invalid arguments")
				}
				m, err := store.Remember(a.Text)
				if err != nil {
					if errors.Is(err, mem.ErrMemoryTooLarge) {
						return mcp.ToolError("cave_memory_too_large", err.Error())
					}
					return mcp.ToolError("cave_remember_failed", err.Error())
				}
				return mcp.ToolText(map[string]any{"id": m.ID, "created_at": m.CreatedAt, "basis": "inferred"})
			},
		},
		{
			Name:        "cavemem_recall",
			Description: "Recall stored memories relevant to a query, ranked by BM25 behind a conservative threshold (an off-topic query recalls nothing). Hits are packed within a token budget; each is compressed for injection with its inferred token cost and a recovery_handle for the full original (an oversized hit returns a head plus its handle).",
			InputSchema: mcp.ObjectSchema(map[string]any{
				"query": mcp.StringProp("What to recall."),
				"limit": map[string]any{"type": "integer", "description": "Max hits (default 5)."},
				"token_budget": map[string]any{
					"type":        "integer",
					"minimum":     0,
					"default":     mem.DefaultTokenBudget,
					"description": "Aggregate inferred-token cap (default 2000; explicit 0 disables the cap).",
				},
			}, "query"),
			Handler: func(raw json.RawMessage) mcp.ToolResult {
				var a struct {
					Query       string `json:"query"`
					Limit       int    `json:"limit"`
					TokenBudget *int   `json:"token_budget"`
				}
				if err := json.Unmarshal(raw, &a); err != nil || a.Query == "" {
					return mcp.ToolError("cave_invalid_arguments", "recall: missing query")
				}
				tokenBudget, err := externalTokenBudget(a.TokenBudget)
				if err != nil {
					return mcp.ToolError("cave_invalid_arguments", err.Error())
				}
				hits, err := store.Recall(a.Query, mem.RecallOptions{Limit: a.Limit, TokenBudget: tokenBudget})
				if err != nil {
					return mcp.ToolError("cave_recall_failed", err.Error())
				}
				return mcp.ToolText(map[string]any{"hits": hits, "basis": "inferred"})
			},
		},
		{
			Name:        "cavemem_supersede",
			Description: "Replace one current memory with a corrected version. Normal recall stops returning the old fact; history remains available.",
			InputSchema: mcp.ObjectSchema(map[string]any{
				"id":   mcp.StringProp("Current memory id to replace."),
				"text": mcp.StringProp("Corrected memory text."),
			}, "id", "text"),
			Handler: func(raw json.RawMessage) mcp.ToolResult {
				var a struct {
					ID   string `json:"id"`
					Text string `json:"text"`
				}
				if err := json.Unmarshal(raw, &a); err != nil || a.ID == "" || a.Text == "" {
					return mcp.ToolError("cave_invalid_arguments", "supersede: missing id or text")
				}
				memory, err := store.Supersede(a.ID, a.Text)
				if err != nil {
					return mcp.ToolError("cave_supersede_failed", err.Error())
				}
				return mcp.ToolText(map[string]any{
					"id":         memory.ID,
					"supersedes": memory.Supersedes,
					"created_at": memory.CreatedAt,
					"basis":      "inferred",
				})
			},
		},
		{
			Name:        "cavemem_history",
			Description: "Return oldest-to-newest versions for a memory supersession chain.",
			InputSchema: mcp.ObjectSchema(map[string]any{"id": mcp.StringProp("Any memory id in the chain.")}, "id"),
			Handler: func(raw json.RawMessage) mcp.ToolResult {
				var a struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(raw, &a); err != nil || a.ID == "" {
					return mcp.ToolError("cave_invalid_arguments", "history: missing id")
				}
				history, err := store.History(a.ID)
				if err != nil {
					return mcp.ToolError("cave_history_failed", err.Error())
				}
				return mcp.ToolText(map[string]any{"history": history, "basis": "inferred"})
			},
		},
		{
			Name:        "cavemem_forget",
			Description: "Delete a stored memory by id.",
			InputSchema: mcp.ObjectSchema(map[string]any{"id": mcp.StringProp("The memory id to forget.")}, "id"),
			Handler: func(raw json.RawMessage) mcp.ToolResult {
				var a struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(raw, &a); err != nil || a.ID == "" {
					return mcp.ToolError("cave_invalid_arguments", "forget: missing id")
				}
				ok, err := store.Forget(a.ID)
				if err != nil {
					return mcp.ToolError("cave_forget_failed", err.Error())
				}
				return mcp.ToolText(map[string]any{"forgotten": ok})
			},
		},
	}
}

func cmdRemember(store *mem.Store, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: cavemem remember <text>|--stdin")
		return 1
	}
	text := args[0]
	if text == "--stdin" {
		if len(args) != 1 {
			fmt.Fprintln(stderr, "usage: cavemem remember <text>|--stdin")
			return 1
		}
		data, err := io.ReadAll(io.LimitReader(stdin, mem.MaxMemoryBytes+1))
		if err != nil {
			fmt.Fprintf(stderr, "remember: read stdin: %v\n", err)
			return 1
		}
		text = string(data)
	}
	m, err := store.Remember(text)
	if err != nil {
		fmt.Fprintf(stderr, "remember: %v\n", err)
		if errors.Is(err, mem.ErrMemoryTooLarge) {
			return memoryTooLargeExitCode
		}
		return 1
	}
	writeJSON(stdout, map[string]any{"id": m.ID, "created_at": m.CreatedAt, "basis": "inferred"})
	return 0
}

func cmdRecall(store *mem.Store, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 || len(args) > 3 {
		fmt.Fprintln(stderr, "usage: cavemem recall <query> [limit] [token_budget]")
		return 1
	}
	limit := 0
	if len(args) > 1 {
		var err error
		limit, err = strconv.Atoi(args[1])
		if err != nil || limit < 0 {
			fmt.Fprintln(stderr, "recall: limit must be a non-negative integer")
			return 1
		}
	}
	tokenBudget := 0
	if len(args) > 2 {
		value, err := strconv.Atoi(args[2])
		if err != nil || value < 0 {
			fmt.Fprintln(stderr, "recall: token_budget must be a non-negative integer")
			return 1
		}
		tokenBudget, err = externalTokenBudget(&value)
		if err != nil {
			fmt.Fprintf(stderr, "recall: %v\n", err)
			return 1
		}
	}
	hits, err := store.Recall(args[0], mem.RecallOptions{Limit: limit, TokenBudget: tokenBudget})
	if err != nil {
		fmt.Fprintf(stderr, "recall: %v\n", err)
		return 1
	}
	writeJSON(stdout, map[string]any{"hits": hits, "basis": "inferred"})
	return 0
}

// externalTokenBudget distinguishes an omitted public option (safe default)
// from its explicit zero sentinel (unlimited). Negative public values fail
// closed instead of accidentally selecting an internal sentinel.
func externalTokenBudget(value *int) (int, error) {
	if value == nil {
		return 0, nil
	}
	if *value < 0 {
		return 0, fmt.Errorf("token_budget must be a non-negative integer")
	}
	if *value == 0 {
		return mem.UnlimitedTokenBudget, nil
	}
	return *value, nil
}

func cmdSupersede(store *mem.Store, args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: cavemem supersede <id> <text>")
		return 1
	}
	memory, err := store.Supersede(args[0], args[1])
	if err != nil {
		fmt.Fprintf(stderr, "supersede: %v\n", err)
		return 1
	}
	writeJSON(stdout, map[string]any{
		"id":         memory.ID,
		"supersedes": memory.Supersedes,
		"created_at": memory.CreatedAt,
		"basis":      "inferred",
	})
	return 0
}

func cmdHistory(store *mem.Store, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: cavemem history <id>")
		return 1
	}
	history, err := store.History(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "history: %v\n", err)
		return 1
	}
	writeJSON(stdout, map[string]any{"history": history, "basis": "inferred"})
	return 0
}

func cmdForget(store *mem.Store, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: cavemem forget <id>")
		return 1
	}
	ok, err := store.Forget(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "forget: %v\n", err)
		return 1
	}
	writeJSON(stdout, map[string]any{"forgotten": ok})
	return 0
}

// cmdRecover resolves a recall hit's recovery_handle to its byte-exact original
// (against cavemem's own CCR store, ~/.caveman/mem/ccr.db) and writes it to
// stdout. The editing skill uses this — never `caveman retrieve`, which reads a
// different CCR database.
func cmdRecover(store *mem.Store, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: cavemem recover <handle>")
		return 1
	}
	data, err := store.Recover(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "recover: %v\n", err)
		return 1
	}
	if _, err := stdout.Write(data); err != nil {
		fmt.Fprintf(stderr, "recover: write: %v\n", err)
		return 1
	}
	return 0
}

func writeJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
