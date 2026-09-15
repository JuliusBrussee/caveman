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
- The wrappers shell out to the gated binary — keep recall/scoring logic in Go, never forked into TS/Python.

## Gotchas (honesty invariants)
- **byte-safe write** — raw text is written to SQLite before any engine call, so a memory is never lost; compression is transient, at recall time only.
- **bounded recall** — omitted/Go-zero `TokenBudget` defaults to 2000 inferred tokens. Public CLI/MCP/JS/Python callers must explicitly pass `token_budget=0` for unlimited recall; adapters map it to `UnlimitedTokenBudget`.
- **fail toward nothing** — a query below the threshold (or with no term overlap) recalls nothing, never a guess.
- **current-only recall** — superseded versions stay auditable through `History` but never enter normal recall.
- **inferred-only** — `tokens_added`, score, and basis are inferred estimates; never `verified`.
- **reversible** — a compressed recall hit carries a CCR `recovery_handle`; `Recover` returns the byte-exact original.

See ../../CLAUDE.md (root) · ../engine/CLAUDE.md · ../mcp/CLAUDE.md
