# cavemem: from Claude-only to a real multi-agent memory feature

Status: plan. Written 2026-08-16 against `optimized` @ 766dce6.

The core (`mem/`, 724 LOC + ~925 LOC tests) is sound: SQLite + BM25, engine
compression on every recall hit, CCR recovery handles, supersede/history,
budget packing. `go test ./mem/...` passes. Nothing below asks to rewrite it.

What is broken is reach and trust:

| Gap | Where | Today |
|---|---|---|
| MCP server orphaned | `packages/cli/src/index.ts:9823` | `mcpInstall` rejects `cavemem`; 6 agents that could get memory tools get none |
| Auto-recall is Claude-only | `index.ts:6014`, `agents/profiles/claude.json:24` | 1 of 8 profiles has `memory_hook` |
| No way to inspect the store | `mem/cmd/cavemem/main.go:56` | no `list`, no `stats` — you cannot trust what you cannot see |
| One global pool | `mem/store.go:35-43` | no scope column; repo A's memories are candidates for repo B's prompts |
| CLI drops `token_budget` | `index.ts:13107` | binary/MCP/JS/Py all take it; porcelain forwards only `--limit` |

Ordering is by leverage per line of diff. P0+P1 is the feature; P2 is what makes
it good rather than merely wide.

---

## P0 — Point the existing surfaces at cavemem (~half day)

### 0.1 Register cavemem as an installable MCP server

`installMcpForAgent` (`index.ts:9894`) already writes MCP config for **claude,
codex, opencode, gemini, hermes, openclaw** — six agents, six formats, all
solved. The only thing stopping cavemem from using it is the allowlist string
check one function up.

- `index.ts:9823` — add `cavemem` to the valid-server set and the error text.
- `index.ts:9827-9845` — resolve branch: `resolveGoBin("cavemem", "CAVEMEM_BIN")`,
  error `cavemem binary not found — run \`caveman setup --install\` first`
  (mirrors the `caveman-browse` branch exactly).
- `index.ts:9873-9880` — marker tool name `cavemem_recall`; success line
  `memory tools installed (remember · recall · supersede · history · forget)`.
- `mcpUninstall` + `mcpUsage()` — same string list.
- Aider stays on the existing default branch: prints the manual command, does
  **not** mark installed. That is the honest ceiling, keep it.

Test: extend the existing mcp-install runtime test with `--server cavemem` for
one JSON agent (gemini) and one TOML agent (codex).

### 0.2 `mem recall --budget N`

`index.ts:13107` forwards only `--limit`. cavemem takes `recall <q> <limit>
<budget>` and documents `0` = unlimited. Forward it; keep the 2000 default when
the flag is absent. ~4 lines + a `mem.runtime.mjs` assertion.

### 0.3 `mem list` / `mem stats`

New cavemem subcommands (`mem/cmd/cavemem/main.go:56` switch + `mem/store.go`):

- `list [--limit N]` → current memories, newest first: `id`, first 80 chars,
  `created_at`, (after P2) `scope`, `source`. JSON out, same as everything else.
- `stats` → count, total raw bytes, superseded count, db path.

Also register as MCP tools (`memTools`, `main.go:111`) — an agent that can
recall but cannot enumerate will re-remember the same fact forever.

CLI passthrough in `mem()` (`index.ts:13081`) + `memUsage()` lines.

---

## P1 — Auto-recall on every agent that already has a live prompt hook (~1 day)

**The reuse.** `nativeHook()` (`index.ts:10975`) is already installed on
**claude, codex, gemini** via `nativeHooksDocument` (`index.ts:5989`), on
**opencode** via the plugin bridge (`index.ts:6454`) and on **hermes** via its
plugin (`index.ts:6887`). It already:

- normalizes the prompt event across hosts — gemini's `BeforeAgent` →
  `UserPromptSubmit` (`index.ts:10990`);
- extracts the prompt from three field spellings (`index.ts:10753`);
- emits `additionalContext` in each host's correct shape — hermes wants
  `{context}`, the rest want `hookSpecificOutput` (`index.ts:11076-11082`).

That is every hard part of auto-recall, already written and shipped. The
Claude-only `mem recall-hook` entry (`index.ts:6014`, `11346`) duplicates a
subset of it.

### 1.1 Move recall into the native hook

- In `nativeHook()`, when `normalizedEvent === "UserPromptSubmit"` **and**
  `wrapRuntimeConfig().autoRecall` is on (the `remember.recall` key already
  exists, `index.ts:3041`/`3223`), run the local recall and prepend its blocks
  to the context string the function already builds.
- Reuse the block formatting from `memRecallHook` verbatim — the
  `[cavemem recall · +N tokens · basis inferred · recover: …]` disclosure is a
  product invariant, not decoration.
- Fail-open stays absolute: any error → no blocks, hook still exits 0.

**Privacy invariant — do not break it.** `nativeHook` deliberately never sends
the raw prompt across the adapter boundary; `nativeRuntimeRequest` digests it
(`nativePayloadDigest`, `index.ts:10754`). Recall runs *in the hook process*
against the local SQLite file, so the raw prompt still never leaves. Add a test
that asserts the runtime request payload is byte-identical with auto-recall on
and off.

### 1.2 Keep `mem recall-hook`

Leave the standalone command as a thin alias so existing installs keep working,
and keep `recallHookEntry` recognition in `memHook` uninstall so `caveman mem
hook uninstall` still strips the old entry. Drop the `includeRecall` special
case from `nativeHooksDocument` once 1.1 lands — no new hook entry is needed
when the hook already fires.

### 1.3 Profiles

`agents/profiles/schema.json:157` `memory_hook.oneOf` gains, one per verified
surface:

- `codex-userpromptsubmit` — codex lifecycle already lists `UserPromptSubmit`
  (`index.ts:5995`)
- `gemini-beforeagent` — mapped at `index.ts:10991`
- `opencode-plugin` — `chat.message` + `experimental.chat.system.transform`,
  the contract documented in `src/plugins/opencode/plugin.js:26-36`
- `hermes-plugin` — `index.ts:6887`

Set them in the four profiles. `compile.mjs` already fails closed on an unknown
method, so a typo is a build error, not a silent no-op.

**Do not add a method you have not run.** Each one needs a real turn against a
real binary before the profile claims it; a profile that claims a surface it
does not have makes `caveman mem hook install` lie. Aider gets nothing and the
existing warn line already says so.

Result after P1: 6 agents with memory MCP tools, 5 with automatic priced recall,
1 (aider) honestly on skill + manual recall.

---

## P2 — Make it trustworthy at multi-agent scale (~1 day)

Reach without these two is a liability: more agents writing into one flat global
pool, with no way to see or scope it.

### 2.1 Scope

`mem/store.go:35` — add `scope TEXT` (NULL = global). Migration is an idempotent
`ALTER TABLE memories ADD COLUMN scope TEXT` guarded by a `PRAGMA table_info`
check; existing rows stay NULL, i.e. global, so behavior is unchanged for
anyone who upgrades.

- `remember --project` stamps the repo root (git toplevel, else cwd).
- `Recall` filters `scope IS NULL OR scope = ?` with the caller's cwd repo root.
- MCP `remember` gains an optional `scope` arg; the recall hook passes cwd.

Default stays global. This is opt-in narrowing, not a behavior change.

### 2.2 Provenance

`source TEXT` column: `manual`, `caveman_learn`, or an agent id. The proxy
already tracks `source_kind` in its own learnings table
(`proxy/internal/store/store.go:169`) and writes learnings through
`mem.Remember` (`proxy/internal/store/learn.go:295-305`) — pass it through so
`mem list` can answer "who wrote this and why is it in my context".

### 2.3 Hygiene

`mem forget --superseded-before <date>` to prune history that no longer earns
its bytes. Exact-duplicate dedupe already exists (ids are content-addressed,
`Remember` is idempotent) — nothing to build there.

---

## P3 — Measure recall quality before touching the ranker (~half day)

Recall is lexical BM25 at threshold 0.1 (`mem/store.go:44-47`). It will miss
paraphrases. Resist fixing that blind.

1. Fixture set under `mem/testdata/`: 20-30 (memory, query) pairs including
   paraphrases, and **negatives** that must recall nothing.
2. Report precision/recall at the current threshold. Commit the numbers.
3. Only then decide. Cheapest rung that could help is stemming + stopword
   handling inside `mem/bm25.go`. Embeddings are the last rung — a model
   dependency, a second store, and an honesty problem (what basis is a vector
   hit?). Do not add them because they sound better.

Also bench the hot path: `Recall` loads *all* current memories
(`mem/store.go:600`) then scores. Fine at hundreds, not at ten thousand, and P1
puts it on every single prompt. Measure at 10k rows; if it hurts, the answer is
SQLite FTS5 as a prefilter, not a rewrite. Leave a `ponytail:` comment naming
the O(n)-per-prompt ceiling either way.

---

## P4 — Docs and tests (folded into each phase, not deferred)

- `mem/README.md` — replace the hand-edit `mcpServers` snippet with
  `caveman mcp install --server cavemem`.
- `README.md` What You Get row + `INSTALL.md` — memory now spans N agents. Real
  count, no rounding up, caveman voice preserved.
- `docs/technical/local-tools.md` — `list`/`stats`/`--budget`/scope.
- Tests, per phase: cavemem MCP install (JSON + TOML agents); recall injection
  shape per agent (hermes `{context}` vs `hookSpecificOutput`); prompt-not-in-
  runtime-request; scope filter; migration against an old db file; `mem list`.

---

## Sequencing

P0 → P1 → P2 → P3. P0 alone takes the feature from one agent to six and is
mostly allowlist strings. P1 is the one that needs care, and its care is
verification of four host surfaces, not new architecture. P2 before any
announcement — shipping wide reach onto an unscoped, unlistable global store is
how a memory feature earns a bad reputation on day one.
