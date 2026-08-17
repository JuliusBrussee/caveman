# mem — cavemem, durable agent memory (commercial Go core + MIT clients)

Durable, cross-session memory: `remember` / `recall` / `supersede` / `history` / `forget`. A local SQLite store holds the
**raw** memories (the source of truth); recall ranks them with deterministic **BM25** behind a
conservative threshold and compresses each hit through the [engine](../engine/CLAUDE.md) so the
inferred token cost is honest and the dropped detail stays recoverable via CCR. Everything is
`inferred`.

## Layout
- `store.go` — `Remember`/`Recall`/`Supersede`/`History`/`Forget`/`Recover` over SQLite + engine; legacy schemas migrate in place.
- `bm25.go` — deterministic tokenizer + BM25 scorer (non-negative scores, so the threshold is meaningful).
- `cmd/cavemem/` — MCP server + `remember`/`recall`/`supersede`/`history`/`forget` CLI subcommands (JSON).
- `js/` + `py/` — thin TS and Python clients that shell out to the binary (mirrored libraries; they reimplement nothing).

## The cavemem_* MCP tools
`cavemem_remember(text)` · `cavemem_recall(query, limit?, token_budget?)` ·
`cavemem_supersede(id, text)` · `cavemem_history(id)` · `cavemem_forget(id)`,
served through `mcp.NewServer` so the framing matches caveman-mcp exactly.

## Conventions
- Build/test: `make product-build PRODUCT=mem` / `make product-test PRODUCT=mem`
  for the Go core; root `make test` also runs mirrored JS/Python wrapper tests.
- The wrappers shell memory text to the gated binary through `remember --stdin`
  (never one huge argv element) — keep validation/recall/scoring logic in Go,
  never forked into TS/Python.

## Gotchas (honesty invariants)
- **byte-safe write** — raw text is written to SQLite before any engine call, so a memory is never lost; compression is transient, at recall time only.
- **single-writer store** — `mem.db` and `ccr.db` open with `SetMaxOpenConns(1)` + a DSN carrying `busy_timeout(5000)` + `journal_mode(WAL)` (`ccr.SQLiteDSN`). The proxy spend store uses the same DSN but does **not** force one pooled connection; mem/CCR's discipline is stricter. Cold-start migration is wrapped in a five-second-wall-budgeted `ccr.RetryOnBusy` because a fan-out of fresh `cavemem remember` processes (the JS client's `Promise.all(facts.map(remember))`) races on the multi-statement DDL; runtime single-statement writes lean on `busy_timeout` alone. Without this, 32 concurrent writes landed 1 and dropped 31 as `SQLITE_BUSY`.
- **bounded recall** — `Recall` packs hits greedily in BM25 rank order under `RecallOptions.TokenBudget` (default `DefaultTokenBudget` = 2000 inferred tokens), reusing `engine/contextwindow` packing. Public CLI/MCP/JS/Python callers must explicitly pass `token_budget=0` to request unlimited recall; adapters map that external zero to `UnlimitedTokenBudget`, while omitted/Go-zero stays bounded. A single top hit whose compressed form alone exceeds the budget is returned as a compressed **head + CCR recovery_handle** (the original is stored even when compression passed it through), never its whole body — this is what stops the measured 440,000-token single recall.
- **bounded remember** — `Remember` caps one memory at `MaxMemoryBytes` (256 KiB), failing closed with `cave_memory_too_large` (`ErrMemoryTooLarge`); CLI exits 65 and both wrappers export `MEMORY_TOO_LARGE_EXIT_CODE`. A memory is a fact to recall, not a file dump.
- **fail toward nothing** — a query below the threshold (or with no term overlap) recalls nothing, never a guess.
- **current-only recall** — superseded versions stay auditable through `History` but never enter normal recall.
- **inferred-only** — `tokens_added`, score, and basis are inferred estimates; never `verified`.
- **reversible** — a compressed recall hit carries a CCR `recovery_handle`; `Recover` returns the byte-exact original.

See ../../CLAUDE.md (root) · ../engine/CLAUDE.md · ../mcp/CLAUDE.md
