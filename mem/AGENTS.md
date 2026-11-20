# mem: cavemem, durable agent memory

Cross-session memory with five verbs: remember, recall, supersede, history, forget. A local SQLite
store holds the raw memories, which are the source of truth. Recall ranks them with deterministic
BM25 behind a conservative threshold and compresses each hit through the
[engine](../engine/AGENTS.md), so the token cost is honest and dropped detail stays recoverable
through CCR. Every figure is `inferred`.

## Layout
- `store.go`: `Open`, `Remember`, `Recall`, `Supersede`, `History`, `Forget`, `Recover` over SQLite plus the engine. Older schemas migrate in place.
- `bm25.go`: deterministic tokenizer and BM25 scorer (scores are non-negative, so the threshold means something).
- `cmd/cavemem/`: the binary. With no arguments or `mcp` it serves MCP through `mcp.NewServer`; otherwise CLI subcommands that print JSON (`remember`, `recall`, `supersede`, `history`, `forget`, `recover`, `version --json`).
- `js/`, `py/`: thin JS and Python clients that run the binary (`CAVEMEM_BIN` or `PATH`). They reimplement nothing.

MCP tools: `cavemem_remember(text)`, `cavemem_recall(query, limit?, token_budget?)`,
`cavemem_supersede(id, text)`, `cavemem_history(id)`, `cavemem_forget(id)`.

## Build and test
- `go build ./mem/...` and `go test ./mem/...` from the repo root.
- `bun run --filter cavemem test` for the JS client; `python -m pytest -q tests` from `mem/py` for the Python client (CI's `root-surfaces` job).

## Invariants
- **Byte-safe write.** Raw text reaches SQLite before any engine call, so a memory is never lost. Compression happens only at recall time.
- **Logic stays in Go.** The clients send memory text through `remember --stdin` (never one huge argv element) and keep no validation, recall or scoring logic of their own.
- **Single-writer store.** `mem.db` and `ccr.db` open with `SetMaxOpenConns(1)` and `ccr.SQLiteDSN` (`busy_timeout(5000)`, `journal_mode(WAL)`). Schema setup, migration and the `Remember` insert run inside `ccr.RetryOnBusy` (30 s wall budget) because a fan-out of fresh `cavemem remember` processes can outlast one busy timeout. Without it, concurrent writes are dropped as `SQLITE_BUSY`.
- **Bounded recall.** `Recall` packs hits in BM25 rank order under `RecallOptions.TokenBudget` (`DefaultTokenBudget` = 2000) using `engine/contextwindow`. An omitted or Go-zero budget stays bounded; public CLI, MCP, JS and Python callers must pass `token_budget=0` explicitly for unlimited, which adapters map to `UnlimitedTokenBudget`. A top hit too large for the budget comes back as a compressed head plus a CCR `recovery_handle`, never its whole body (one unbounded recall once returned 440,000 tokens).
- **Bounded remember.** One memory is capped at `MaxMemoryBytes` (256 KiB) and fails closed with `cave_memory_too_large` (`ErrMemoryTooLarge`). The CLI exits 65 and both clients export `MEMORY_TOO_LARGE_EXIT_CODE`. A memory is a fact, not a file dump.
- **Version-gated `supersede --stdin`.** `version --json` advertises the `supersede-stdin` capability. An older build reads `--stdin` as the replacement text, stores the literal flag, expires the real memory and exits 0. Clients and `caveman mem` send replacements by argv when they fit and require the capability before using stdin, refusing before any write when it is absent.
- **Fail toward nothing.** A query below the threshold or with no term overlap recalls nothing, never a guess.
- **Current-only recall.** Superseded versions stay visible through `History` but never enter normal recall.
- **Reversible and inferred.** A compressed hit carries a `recovery_handle`; `Recover` returns the byte-exact original. `tokens_added`, score and basis are estimates, never `verified`.

See the root [AGENTS.md](../AGENTS.md) and [mcp/AGENTS.md](../mcp/AGENTS.md).
