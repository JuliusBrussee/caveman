# CLAUDE.md — caveman

## README is a product artifact

README = product front door. Non-technical people read it to decide if caveman worth install. Treat like UI copy.

**Rules for any README change:**

- Readable by non-AI-agent users. If you write "SessionStart hook injects system context," invisible to most — translate it.
- Keep Before/After examples first. That the pitch.
- Install table always complete + accurate. One broken install command costs real user.
- What You Get table must sync with actual code. Feature ships or removed → update table.
- Preserve voice. Caveman speak in README on purpose. "Brain still big." "Cost go down forever." "One rock. That it." — intentional brand. Don't normalize.
- Benchmark numbers from real runs in `benchmarks/` and `evals/`. Never invent or round. Re-run if doubt.
- Adding new agent to install table → add detail block in `<details>` section below.
- Readability check before any README commit: would non-programmer understand + install within 60 seconds?

---

## Project overview

Caveman makes AI coding agents respond in compressed caveman-style prose while preserving technical substance, code, commands, and exact errors. Publish no reduction or quality-equivalence percentage without a committed reviewed benchmark. Ships as Claude Code plugin, Codex plugin, Gemini CLI extension, and agent rule files for Cursor, Windsurf, Cline, Copilot, and other profiles via `npx skills`.

## Repository routing

This repo is source of truth for Caveman skills, Engine, and MV3 directive
extension. Agent SDK + initializer work belongs in
`/Users/julb/Desktop/GitHub/caveman-agent-sdk`
(`JuliusBrussee/agent-sdk`). Proprietary Pebble runtime, policy, sessions, TUI,
distribution, and conformance work belongs in
`/Users/julb/Desktop/GitHub/caveman-coding-agent`
(intended `JuliusBrussee/caveman-coding-agent`). Browse driver/MCP/benchmark/plugin work
belongs in `/Users/julb/Desktop/GitHub/caveman-browse`
(`JuliusBrussee/caveman-browse`). Matching `packages/agent/`,
`packages/create-caveman-agent/`, and `browse/` directories here are
historical/consumer copies; edit only for pinned integration, migration/removal,
or an explicitly requested cross-repo sync.

Visibility is separate from ownership: this repo and `caveman-browse` are
public now; `caveman-agent-sdk` is private during development and planned for
public release after its release gates; `caveman-coding-agent` and
Caveman-Cloud remain private commercial source.

---

## What lives where

Post-cleanup layout. Sources of truth at the top, distribution mirrors below, build outputs in `dist/`, human docs alongside each skill.

```
caveman/
├── README.md                    # Front door (product pitch)
├── INSTALL.md                   # Per-agent install commands
├── CONTRIBUTING.md              # Dev guide
├── CLAUDE.md                    # This file (maintainer instructions)
├── AGENTS.md / GEMINI.md        # Autodiscovery files (must stay at root)
│
├── install.sh / install.ps1     # 30-line shims → bin/install.js
│
├── bin/                         # Unified installer
│   ├── install.js               # Single source for all 30+ agents (PROVIDERS array)
│   └── lib/settings.js          # JSONC-tolerant settings.json reader/writer
│
├── skills/                      # ALL skills, single source of truth
│   ├── caveman/{SKILL.md, README.md}
│   ├── caveman-commit/{SKILL.md, README.md}
│   ├── caveman-review/{SKILL.md, README.md}
│   ├── caveman-help/{SKILL.md, README.md}
│   ├── caveman-stats/{SKILL.md, README.md}
│   ├── caveman-compress/{SKILL.md, README.md, scripts/}
│   └── cavecrew/{SKILL.md, README.md}
│
├── agents/                      # cavecrew subagents (single source — kept at root for plugin auto-discovery)
│   └── docs/                    # profile-registry docs — NOT agents (see note below)
├── commands/                    # Codex/Gemini TOML command stubs (root for plugin auto-discovery)
│
├── src/                         # Internal source — not auto-discovered by plugin
│   ├── hooks/                   # Claude Code hooks (installer reads here)
│   ├── rules/                   # Auto-activation rule body (single source)
│   ├── tools/                   # caveman-init.js (per-repo rule writer)
│   └── mcp-servers/             # caveman-shrink npm-published MCP middleware
│
├── packages/                    # Current public packages
│   ├── agent/                   # historical copy; source = caveman-agent-sdk
│   ├── create-caveman-agent/    # historical copy; source = caveman-agent-sdk
│   ├── cli/                     # @caveman-ai/cli
│   ├── pi-extension/            # @caveman-ai/pi — native Pi extension (bundled into the CLI, published on `pi-v*` tags)
│   ├── sdk/                     # TypeScript + Python gateway clients
│   ├── subagent-tax/            # Local harness-prefix benchmark
│   └── shared/                  # Contracts + binary installer
├── engine/ · proxy/             # BSL local compression runtime + provider proxy
├── rewriter/                    # Prompt rewriter
├── mcp/ · mem/ · shrink/        # Recovery tools, memory, output compression
├── browse/                      # consumer copy; source = caveman-browse
├── extension/                   # MV3 extension source
├── shared/                       # Provider catalog + BSL platform libraries
│
├── .claude-plugin/              # Claude Code plugin manifest (REQUIRED at root)
├── plugins/caveman/             # Claude Code plugin distribution (CI-mirrored)
│   ├── skills/                  # ← from skills/
│   └── agents/                  # ← from agents/
│
├── dist/                        # Build artifacts (gitignored)
│   └── caveman.skill            # ZIP of skills/caveman/, rebuilt by CI
│
├── tests/                       # All tests (Node + Python)
├── benchmarks/                  # Real token measurements through Claude API
├── evals/                       # Three-arm eval harness
├── docs/                        # User-facing docs site
└── .github/workflows/           # CI sync
```

---

## File structure and what owns what

### Single source of truth files — edit only these

| File | What it controls |
|------|-----------------|
| `skills/caveman/SKILL.md` | Caveman behavior: intensity levels, rules, wenyan mode, auto-clarity, persistence. Only file to edit for behavior changes. |
| `src/rules/caveman-activate.md` | Always-on auto-activation rule body. Consumed by `src/tools/caveman-init.js` when a user runs `npx caveman --with-init` (per-repo IDE rule files). Edit here, not in any per-agent rule copy. |
| `src/rules/caveman-openclaw-bootstrap.md` | Marker-fenced bootstrap snippet appended to `~/.openclaw/workspace/SOUL.md` by `bin/lib/openclaw.js`. Drives always-on caveman through the OpenClaw gateway. Must include the SENTINEL `Respond terse like smart caveman` and stay well under OpenClaw's 12K-per-bootstrap-file cap. |
| `bin/lib/openclaw.js` | OpenClaw install/uninstall helper. Frontmatter merge (`version`, `always: true`), SOUL.md marker append/strip, idempotent. Shared by `bin/install.js` and `src/tools/caveman-init.js`. |
| `skills/caveman-commit/SKILL.md` | Caveman commit message behavior. Fully independent skill. |
| `skills/caveman-review/SKILL.md` | Caveman code review behavior. Fully independent skill. |
| `skills/caveman-help/SKILL.md` | Quick-reference card. One-shot display, not a persistent mode. |
| `skills/caveman-compress/SKILL.md` | Compress sub-skill behavior. |
| `skills/cavecrew/SKILL.md` | Cavecrew decision guide — when to delegate to caveman subagents vs vanilla. Edit only here. |
| `skills/{caveman-setup,caveman-discover,caveman-learn,caveman-manage,caveman-optimize,caveman-explore,caveman-evidence-review}/SKILL.md` | Engine/proxy driver skills. Ship with the plugin (see the auto-discovery note below). |
| `skills/{investigate-first,lean-build,surgical-patch,safe-refactor,migration,verify-and-stop}/SKILL.md` | Token-discipline work patterns — same goal as caveman prose (fewer output tokens) applied to code volume rather than wording. Deliberately un-branded so they read as generic patterns to the model. Ship with the plugin. |
| `agents/cavecrew-investigator.md` | Read-only locator subagent (haiku). Output contract: `path:line — symbol — note`. |
| `agents/cavecrew-builder.md` | Surgical 1-2 file editor subagent. Refuses 3+ file scope. |
| `agents/cavecrew-reviewer.md` | Diff/file reviewer subagent (haiku). One-line findings with severity emoji. |
| `src/plugins/opencode/plugin.js` | opencode native plugin. ESM Bun module — `session.created` writes flag, `tui.prompt.append` parses slash/natural-language activation and appends per-prompt reinforcement. Reuses `caveman-config.js` via `createRequire`. |
| `src/plugins/opencode/commands/*.md` | Six opencode slash-command prompt templates (`/caveman`, `/caveman-{commit,review,compress,stats,help}`). |

### `skills/` is auto-discovered wholesale — every subdirectory ships

`.claude-plugin/marketplace.json` sets `"source": "./"`, so the plugin root IS
the repo root, and Claude Code auto-discovers **every** `skills/*/SKILL.md`
with no `skills` key in `plugin.json` to gate it. Adding a directory under
`skills/` therefore installs it into every plugin user's agent, where its
`description` competes for activation on ordinary work.

There is no allowlist. Before adding a directory here, decide whether it
should reach end users; if not, it belongs under `packages/` or another root.
Keep the two tables above and the README "What you get" table in sync with the
actual directory listing — `ls skills/` is the source of truth.

### `agents/*.md` is auto-discovered too — and `plugin.json` must NOT list them

Same mechanism, sharper edge. `plugin.json` used to carry an explicit
`"agents": ["./agents/cavecrew-*.md"]` array. On Claude Code 2.1.235 that array
loads **zero** agents — `claude plugin details caveman` reports `Agents (0)`
with it and `Agents (3)` without it, so the three cavecrew subagents the
`cavecrew` skill delegates to did not exist for any plugin user. The docs say
`string | string[]` is valid and a directory string is rejected outright
(`agents: Invalid input`), so the default `agents/` scan is the only path that
works. Do not re-add the key.

Consequence: **every `.md` anywhere in `agents/` becomes a subagent**, named
from its frontmatter or filename. Maintainer documentation belongs outside that
tree; profile-registry orientation lives in
`docs/technical/agent-profile-registry.md`. `tests/verify_repo.py` fails build
if any markdown beyond three intended cavecrew agents appears under `agents/`.

### `commands/*.md` shadows same-named skills — keep the `.md` stubs unique

Claude Code loads `commands/*.md` as flat skills alongside `skills/*/SKILL.md`,
so a stub sharing a skill's name registers that name twice. `caveman.md`,
`caveman-commit.md`, `caveman-review.md` and `caveman-stats.md` each duplicated
a real skill — a 3-line stub competing with the full ruleset for the same slash
command — and were removed. The `.toml` stubs are Codex/Gemini-only and are not
scanned, so `commands/` keeps one `.md`: `caveman-init.md`, which has no skill
twin. Guarded by `tests/verify_repo.py`.

### Auto-generated / auto-synced — do not edit directly

We removed the agent-specific dotdir mirrors at the repo root (`.cursor/`, `.windsurf/`, `.clinerules/`, `.github/copilot-instructions.md`, root `caveman/SKILL.md`). They were never read by the installer — only used to self-apply caveman to this repo when a maintainer opened it in Cursor/Windsurf/Cline. Devs who want caveman in their editor while editing this repo should run `npx caveman --with-init` once (writes per-repo rule files from `src/rules/caveman-activate.md` via `src/tools/caveman-init.js`). For per-user installs through the upstream skills CLI, `npx caveman --only <agent>` runs `npx skills add ... -a <profile>`.

A handful of dotdir leftovers (`.junie/`, `.kiro/`, `.roo/`, `.agents/`) still hold a stale `cavecrew/SKILL.md` mirror from before the cleanup. They aren't read by anything in the current install path; remove on sight, no migration needed.

What's left is the Claude Code plugin distribution (required by the plugin loader) and the release ZIP.

| File | Synced from |
|------|-------------|
| `plugins/caveman/skills/caveman/SKILL.md` | `skills/caveman/SKILL.md` |
| `plugins/caveman/skills/caveman-compress/SKILL.md` (+ `scripts/`) | `skills/caveman-compress/SKILL.md` (+ `scripts/`) |
| `plugins/caveman/skills/cavecrew/SKILL.md` | `skills/cavecrew/SKILL.md` |
| `plugins/caveman/agents/cavecrew-*.md` | `agents/cavecrew-*.md` |
| `dist/caveman.skill` | ZIP of `skills/caveman/` directory (gitignored; rebuilt by CI on release) |

Skills not in this table (`caveman-commit`, `caveman-review`, `caveman-help`, `caveman-stats`) are not mirrored into the Claude Code plugin distribution by CI. They reach Claude Code through the standalone hook + skill install path, and reach other agents via `npx skills add`. A `plugins/caveman/skills/caveman-stats/` directory is currently checked in as a hand-committed copy; the sync workflow does not touch it, so don't rely on edits there to propagate.

---

## CI sync workflow

`.github/workflows/sync-skill.yml` triggers on main push when `skills/**/SKILL.md` or `agents/cavecrew-*.md` changes.

What it does:
1. Copies `skills/caveman/SKILL.md` and `skills/cavecrew/SKILL.md` into their `plugins/caveman/skills/<name>/` mirrors so the Claude Code plugin loader sees the latest behavior.
2. Copies `skills/caveman-compress/SKILL.md` and its `scripts/` into `plugins/caveman/skills/caveman-compress/`.
3. Copies `agents/cavecrew-*.md` into `plugins/caveman/agents/`.
4. Rebuilds `dist/caveman.skill` (ZIP of `skills/caveman/`) for the release artifact.
5. Commits and pushes with `[skip ci]` to avoid loops.

CI bot commits as `github-actions[bot]`. After PR merge, wait for workflow before declaring release complete.

The old steps that mirrored SKILL.md and rules into root dotdirs (`.cursor/`, `.windsurf/`, `.clinerules/`, `.github/copilot-instructions.md`) are gone — those mirrors no longer exist. The old `caveman-compress/` → `skills/compress/` rename-on-sync is also gone now that compress lives at `skills/caveman-compress/`.

---

## Hook system (Claude Code)

Three hooks in `src/hooks/` plus a `caveman-config.js` shared module, a `caveman-parse.js` shared mode-change parser and a `package.json` CommonJS marker.

**Mode state is per session.** Each session's mode lives in `$CLAUDE_CONFIG_DIR/.caveman-sessions/<session_id>.mode`, keyed by the `session_id` Claude Code puts in every hook payload *and* in the statusline's stdin JSON. `$CLAUDE_CONFIG_DIR/.caveman-active` survives as a last-write-wins compat mirror (falls back to `~/.claude/`).

```
SessionStart hook ──┐                                        ┌── UserPromptSubmit hook
  (session_id,      │                                        │     (session_id, prompt)
   source)          ▼                                        ▼
        $CLAUDE_CONFIG_DIR/.caveman-sessions/<session_id>.mode
                             │             │
                          mirrors       reads
                             ▼             ▼
              .caveman-active      caveman-statusline.sh ◀── session JSON on stdin
           (last-write-wins,        [CAVEMAN] / [CAVEMAN:ULTRA] / ...
            compat only)
```

Two invariants that are load-bearing, not stylistic:

- **The legacy mirror never holds the literal `off`.** Deactivation deletes it. `off` is in `VALID_MODES`, so an older `caveman-mode-tracker.js` reading `off` from that path would pass its `!INDEPENDENT_MODES.has(...)` check and inject "CAVEMAN MODE ACTIVE (off)", and an older `caveman-statusline.sh` would render `[CAVEMAN:OFF]`. Mixed-version installs are real: plugin hooks and standalone hooks can both be registered at once, and `statusLine` holds an absolute path baked in at install time.
- **`recordModeChange` compares against the session's own state, never the mirror.** That's why `readSessionModeRaw` exists alongside `resolveActiveMode` — diffing against the mirror would compare one session's transition to whatever another session wrote last and spam the log with phantom entries.

Both spellings of off are READ everywhere: a missing file (old semantics) and a literal `off` (new, durable).

`src/hooks/package.json` pins the directory to `{"type": "commonjs"}` so the `.js` hooks resolve as CJS even when an ancestor `package.json` (e.g. `~/.claude/package.json` from another plugin) declares `"type": "module"`. Without this, `require()` blows up with `ReferenceError: require is not defined in ES module scope`.

All hooks honor `CLAUDE_CONFIG_DIR` for non-default Claude Code config locations.

### `src/hooks/caveman-config.js` — shared module

Exports:
- `getDefaultMode()` — resolves default mode in order: `CAVEMAN_DEFAULT_MODE` env var → repo-local config (`<cwd>/.caveman/config.json` or `<cwd>/.caveman.json`, walking up to the filesystem root) → user config (`$XDG_CONFIG_HOME/caveman/config.json` / `~/.config/caveman/config.json` / `%APPDATA%\caveman\config.json`) → `'full'`. The env var short-circuits before any cwd walk. Repo-local config lets a team check in a per-project default without polluting every contributor's env or user config.
- `findRepoConfigPath(start)` — walks up from `start` (default `process.cwd()`) looking for the first `.caveman/config.json` or `.caveman.json`. Bounded to 64 ancestors. Refuses symlinked files (symmetric with `safeWriteFlag` / `readFlag`).
- `safeWriteFlag(flagPath, content)` — symlink-safe flag write. Refuses if flag target or its immediate parent is a symlink. Opens with `O_NOFOLLOW` where supported. Atomic temp + rename. Creates with `0600`. Protects against local attackers replacing the predictable flag path with a symlink to clobber files writable by the user. Used by both write hooks. Silent-fails on all filesystem errors.
- `validateSessionId(id)` — returns the id or `null`. Whitelist `^[A-Za-z0-9_-]{1,128}$`. **A session id becomes part of a filesystem path, so nothing may interpolate one without passing it through here first.** The symlink hardening in `safeWriteFlag` does not cover traversal.
- `resolveActiveMode(claudeDir, sessionId)` — what mode is in effect: session file → legacy mirror, with both a missing file and a literal `off` collapsing to `null`. The reader every hook should use.
- `readSessionModeRaw(claudeDir, sessionId)` — literal stored value for **this** session, no legacy fallback. Two callers only: `recordModeChange` (see the invariant above) and the SessionStart continuation branch, which has to tell a stored `off` apart from "nothing stored yet".
- `writeSessionMode(claudeDir, sessionId, modeOrNull)` — the single writer. Writes `off` literally to the session file, unlinks the legacy mirror. Rejects anything outside `VALID_MODES`.
- `writeSessionPrev` / `readSessionPrev` / `clearSessionPrev` — the displaced-prose-mode memory for one-shot skills (#599), scoped per session so two windows running `/caveman-commit` don't overwrite each other's return target.
- `gcSessionStore(claudeDir, opts)` — mtime sweep of `.caveman-sessions/`, 14-day TTL (`CAVEMAN_SESSION_TTL_MS` overrides for tests), `maxDeletes` cap, refuses symlinks. Called from SessionStart on new sessions only — never on `compact`, which is frequent and shares the 5s hook budget.
- `recordModeChange(claudeDir, newMode, sessionId)` — third arg tags the log entry; omitted (not null) when unknown, so pre-existing readers see the old shape.

**Every function above accepts a `sessionId` that may be `null` or malformed and degrades to the legacy machine-wide behavior.** That is the entire backward-compatibility story: the old code path *is* the fallback branch, which is why the existing tests — none of which send a `session_id` — pass unchanged. The three hook entrypoints resolve these helpers individually (`cfg.writeSessionMode || <legacy stub>`) rather than adding them to their `requireSibling` shape checks: a `caveman-config.js` from before per-session state satisfies those checks, and hard-failing over the newer exports would trade "machine-wide mode, as it always worked" for "no state at all" on exactly the plugin-cache-drift scenario #848 is about.

### `src/hooks/caveman-activate.js` — SessionStart hook

Runs on every SessionStart — `source` is `startup`, `resume`, `clear`, `compact` or `fork`. Four things:
1. Reads the hook payload from stdin for `session_id`, `source` and `cwd`
2. Resolves this session's mode and persists it via `writeSessionMode`
3. Emits caveman ruleset as hidden stdout — Claude Code injects SessionStart hook stdout as system context, invisible to user
4. Checks `settings.json` for statusline config; if missing, appends nudge to offer setup on first interaction (one-shot, gated by `.caveman-nudge-shown`)

**`source` branching.** `RESET_SOURCES` is `{startup, clear}` — only those re-derive `getDefaultMode()`. Everything else (`compact`/`resume`/`fork`, an unrecognized source, and the watchdog's `unknown`) READS the stored mode via `readSessionModeRaw`, falling back to the legacy mirror for a session that predates the store, and only then to the default. Reading the LITERAL value is the point: #691 already branched on `source`, but it read the legacy flag where off is spelled "no file", so a deactivated session found nothing and re-derived the default anyway — "stop caveman" was still undone by the next auto-compaction. A stored `off` now short-circuits to `OK` with no ruleset. It still re-emits when a mode IS active: compaction is what prunes the rules out of context, which is the whole reason this hook runs on every source. `clear` counts as a fresh start (explicit user reset — nothing else in the conversation survives it, so neither should a "stop caveman" from before it); this deliberately differs from #691's comment, which grouped `clear` with the continuations.

**The stdin contour is load-bearing.** Activation runs on the first COMPLETE JSON object, not at EOF, with a 2000ms `PAYLOAD_WATCHDOG_MS` backstop and `process.stdin.unref()` in `finish()`. `pause()` alone is not enough — it stops reading but leaves the pipe handle referenced, so a host that holds the write end open (Windows pipe close lags arbitrarily, #729/#833) burns the whole 5s budget. The watchdog must NOT assume `startup`: it reports `unknown`, which preserves the session's stored mode rather than resetting it. Several suites also invoke this hook via `subprocess.run()` with no `input=`, inheriting a stdin that never reaches EOF — `tests/test_hooks.py::test_hook_never_blocks_on_stdin_that_never_closes` guards that path.

Silent-fails on all filesystem errors — never blocks session start.

### `src/hooks/caveman-mode-tracker.js` — UserPromptSubmit hook

Reads JSON from stdin — `session_id` scopes every read and write. Three responsibilities:

**1. Slash-command activation.** If prompt starts with `/caveman`, writes the session's mode via `writeSessionMode`:
- `/caveman` → configured default (see `caveman-config.js`, defaults to `full`)
- `/caveman lite` → `lite`
- `/caveman ultra` → `ultra`
- `/caveman wenyan` or `/caveman wenyan-full` → `wenyan` (alias) / `wenyan-full`
- `/caveman wenyan-lite` → `wenyan-lite`
- `/caveman wenyan-ultra` → `wenyan-ultra`
- `/caveman-commit` → `commit`
- `/caveman-review` → `review`
- `/caveman-compress` → `compress`

**2. Natural-language activation/deactivation.** Matches phrases like "activate caveman", "turn on caveman mode", "talk like caveman" and writes the configured default mode. Matches "stop caveman", "disable caveman", "normal mode", "deactivate caveman" etc. and stores a durable `off` for the session. README promises these triggers, the hook enforces them.

**3. Per-turn reinforcement.** When the session's mode is a non-independent one (i.e. not `commit`/`review`/`compress`), emits a small `hookSpecificOutput` JSON reminder so the model keeps caveman style after other plugins inject competing instructions mid-conversation. The full ruleset still comes from SessionStart — this is just an attention anchor.

### `src/hooks/caveman-statusline.sh` — Statusline badge

Reads the session JSON Claude Code pipes to it on stdin, extracts `session_id` (pure bash — no `jq` dependency), and reads `.caveman-sessions/<id>.mode`; falls back to `$CLAUDE_CONFIG_DIR/.caveman-active` when there is no usable id. Outputs colored badge string for Claude Code statusline:
- `full` or empty → `[CAVEMAN]` (orange)
- `off` → nothing at all. Never `[CAVEMAN:OFF]` — that reads as a mode rather than the absence of one
- anything else → `[CAVEMAN:<MODE_UPPERCASED>]` (orange)

Stdin is bounded the same way as the SessionStart hook: `[ ! -t 0 ]` skips an interactive terminal, and `read -r -d '' -t 1` caps the wait. The timeout is an **integer on purpose** — macOS ships bash 3.2, which rejects `-t 0.3` with `invalid timeout specification`.

Then appends the lifetime-savings suffix (`⛏ 12.4k`) read from `$CLAUDE_CONFIG_DIR/.caveman-statusline-suffix` — written by `caveman-stats.js` on every `/caveman-stats` run. **Default on**; users opt out with `CAVEMAN_STATUSLINE_SAVINGS=0`. The suffix file is absent until `/caveman-stats` runs at least once, so fresh installs render no fake number.

Configured in `settings.json` under `statusLine.command`. PowerShell counterpart at `src/hooks/caveman-statusline.ps1` for Windows. Both scripts symlink-refuse and whitelist-validate the flag/suffix file contents — never echo arbitrary bytes.

### Hook installation

**Plugin install** — hooks wired automatically by plugin system.

**Standalone install** — `bin/install.js` (the unified Node installer) copies hook files into `$CLAUDE_CONFIG_DIR/hooks/` and merges SessionStart + UserPromptSubmit + statusline into `settings.json`. Uses the JSONC-tolerant helpers in `bin/lib/settings.js` so a commented `settings.json` no longer crashes the merge. Defensive `validateHookFields` runs before every write to prevent a single malformed hook from poisoning the entire file (Claude Code Zod silently discards the whole `settings.json` on schema mismatch).

The `install.sh` / `install.ps1` shims at the repo root delegate to `bin/install.js` via `node` (local clone) or `npx -y github:JuliusBrussee/caveman` (curl|bash). No legacy fallback path remains — earlier `install.sh.legacy` / `install.ps1.legacy` files were removed.

**Uninstall** — `npx -y github:JuliusBrussee/caveman -- --uninstall` (or `node bin/install.js --uninstall` from a clone). Strips caveman hook entries from `settings.json` via substring marker `caveman`, deletes hook files, and removes the Claude plugin / Gemini extension. Skill installs done via `npx skills add` must be removed via the IDE's skill manager (we don't track them).

---

## Skill system

Skills = Markdown files with YAML frontmatter consumed by Claude Code's skill/plugin system and by `npx skills` for other agents.

Each skill has a human-facing `README.md` alongside the LLM-facing `SKILL.md`. The README explains what the skill does for users browsing GitHub; the SKILL.md is the prompt body the agent loads. Don't merge them — different audiences, different formats.

### Intensity levels

Defined in `skills/caveman/SKILL.md`. Six levels: `lite`, `full` (default), `ultra`, `wenyan-lite`, `wenyan-full`, `wenyan-ultra`. Persists until changed or session ends.

### Auto-clarity rule

Caveman drops to normal prose for: security warnings, irreversible action confirmations, multi-step sequences where fragment ambiguity risks misread, user confused or repeating question. Resumes after. Defined in skill — preserve in any SKILL.md edit.

### caveman-compress

Sub-skill in `skills/caveman-compress/SKILL.md`. Takes file path, compresses prose to caveman style, writes to original path, saves backup at `<filename>.original.md`. Validates headings, code blocks, URLs, file paths, commands preserved. Retries up to 2 times on failure with targeted patches only. Requires Python 3.10+.

The slash command is `/caveman-compress` everywhere — same name in plugin and standalone install. CI no longer renames the directory on sync (the old `caveman-compress/` → `skills/compress/` sed rename is gone now that the source lives at `skills/caveman-compress/`).

### caveman-commit / caveman-review

Independent skills in `skills/caveman-commit/SKILL.md` and `skills/caveman-review/SKILL.md`. Both have own `description` and `name` frontmatter so they load independently. caveman-commit: Conventional Commits, ≤50 char subject. caveman-review: one-line comments in `L<line>: <severity> <problem>. <fix>.` format.

---

## Agent distribution

How caveman reaches each agent type:

| Agent | Mechanism | Auto-activates? |
|-------|-----------|----------------|
| Claude Code | Plugin (hooks + skills) or standalone hooks | Yes — SessionStart hook injects rules |
| Codex | Plugin in `plugins/caveman/` plus repo `.codex/hooks.json` and `.codex/config.toml` | Yes on macOS/Linux — SessionStart hook |
| Gemini CLI | Extension with `GEMINI.md` context file | Yes — context file loads every session |
| opencode | Native plugin (`src/plugins/opencode/`) copied into `~/.config/opencode/plugins/caveman/` + `AGENTS.md` ruleset + skills/agents/commands directories. Plugin uses `session.created` and `tui.prompt.append` lifecycle hooks. No statusline (opencode TUI exposes no plugin-writable badge). | Yes — `session.created` writes flag, `AGENTS.md` carries always-on ruleset |
| OpenClaw | Workspace skill at `~/.openclaw/workspace/skills/caveman/SKILL.md` (frontmatter merged with `version` + `always: true`) plus a marker-fenced bootstrap block in `~/.openclaw/workspace/SOUL.md`. Both writes go through `bin/lib/openclaw.js`; workspace path is overridable via `OPENCLAW_WORKSPACE`. | Yes — SOUL.md is auto-injected each turn under "Project Context" (subject to OpenClaw's 12K-per-file / 60K-total bootstrap caps) |
| Cursor | `npx skills add ... -a cursor` (default via `--only cursor`) writes the upstream skill profile; per-repo `.cursor/rules/caveman.mdc` via `--with-init` (calls `src/tools/caveman-init.js`) | Yes — always-on rule |
| Windsurf | `npx skills add ... -a windsurf` (default via `--only windsurf`); per-repo `.windsurf/rules/caveman.md` via `--with-init` | Yes — always-on rule |
| Cline | `npx skills add ... -a cline` (default via `--only cline`); per-repo `.clinerules/caveman.md` via `--with-init` | Yes — Cline auto-discovers `.clinerules/` |
| Copilot | `npx skills add ... -a github-copilot` (soft probe — pass `--only copilot`); per-repo `.github/copilot-instructions.md` + `AGENTS.md` via `--with-init` | Yes — repo-wide instructions |
| Others (Junie, Trae, Warp, Tabnine, Mistral, Qwen, Devin, Droid, ForgeCode, Bob, Crush, iFlow, OpenHands, Qoder, Rovo Dev, Replit, Antigravity, …) | `npx skills add JuliusBrussee/caveman -a <profile>` | No — user must say `/caveman` each session |

opencode reaches Tier 1 minus the statusline (opencode's TUI has no plugin-writable badge). Mode flag lives at `~/.config/opencode/.caveman-active` for any external tooling that wants to surface it.

For agents without hook systems, the always-on snippet lives in `INSTALL.md`'s "Want it always on?" section — keep current with `src/rules/caveman-activate.md`.

**Adding a new agent.** Edit the `PROVIDERS` array in `bin/install.js` — single source of truth, no more bash/PS1 dual-source drift. Each entry has `id`, `label`, `mech`, `detect` (clause spec like `command:foo||dir:$HOME/x`), optional `profile` (vercel-labs/skills slug), optional `soft: true` (config-dir-only detection).

1. The profile slug must exist in upstream [vercel-labs/skills](https://github.com/vercel-labs/skills). Verify against the README before merging — wrong slugs cause `npx skills add` to fail at runtime, not at install-script load.
2. Run `node bin/install.js --list` to confirm the new row renders correctly.
3. Soft probes (config-dir-only) are fine but tag them with `soft: true`. They render with `(soft)` in `--list` so users know detection is best-effort.

---

## Evals

`evals/` has three-arm harness:
- `__baseline__` — no system prompt
- `__terse__` — `Answer concisely.`
- `<skill>` — `Answer concisely.\n\n{SKILL.md}`

Honest delta = **skill vs terse**, not skill vs baseline. Baseline comparison conflates skill with generic terseness — that cheating. Harness designed to prevent this.

`llm_run.py` calls `claude -p --system-prompt ...` per (prompt, arm), saves to `evals/snapshots/results.json`. `measure.py` reads snapshot offline with tiktoken (OpenAI BPE — approximates Claude tokenizer, ratios meaningful, absolute numbers approximate).

Add skill: drop `skills/<name>/SKILL.md`. Harness auto-discovers. Add prompt: append line to `evals/prompts/en.txt`.

Snapshots committed to git. CI reads without API calls. Only regenerate when SKILL.md or prompts change.

---

## Benchmarks

`benchmarks/` runs real prompts through Claude API (not Claude Code CLI), records raw token counts. Results committed as JSON in `benchmarks/results/`. Benchmark table in README generated from results — update when regenerating.

To reproduce: `uv run python benchmarks/run.py` (needs `ANTHROPIC_API_KEY` in `.env.local`).

---

## Key rules for agents working here

- Edit `skills/<name>/SKILL.md` for behavior changes. Never edit synced copies under `plugins/caveman/skills/`.
- Edit `src/rules/caveman-activate.md` for auto-activation rule changes. Never edit any per-agent rule copy a user has on their machine.
- Edit `src/rules/caveman-openclaw-bootstrap.md` for the OpenClaw SOUL.md bootstrap snippet. Keep the `<!-- caveman-begin -->` / `<!-- caveman-end -->` markers and the `Respond terse like smart caveman` sentinel — `bin/lib/openclaw.js` keys idempotency off both. If you change the embedded fallback in `bin/lib/openclaw.js`, keep it byte-equivalent to the file.
- Per-skill human docs live in `skills/<name>/README.md`. The LLM-facing body is in `SKILL.md`. Don't merge them — different audiences.
- Build artifacts go in `dist/`. Never check files into `dist/` manually — CI rebuilds them on push, and `dist/` is gitignored.
- README most important file for user-facing impact. Optimize for non-technical readers. Preserve caveman voice.
- `INSTALL.md` is the per-agent install reference. Keep the install table in `README.md` short and link out to `INSTALL.md` for the full matrix.
- Benchmark and eval numbers must be real. Never fabricate or estimate.
- CI workflow commits back to main after merge. Account for when checking branch state.
- Hook files must silent-fail on all filesystem errors. Never let hook crash block session start.
- Any new flag file write must go through `safeWriteFlag()` in `caveman-config.js`. Direct `fs.writeFileSync` on predictable user-owned paths reopens the symlink-clobber attack surface.
- Mode state reads/writes go through the `caveman-config.js` session helpers (`resolveActiveMode` / `writeSessionMode` / …), never by joining a path by hand. Any `session_id` that reaches a path must pass `validateSessionId()` first.
- **Keep mode-state logic in `caveman-config.js`.** `src/plugins/opencode/plugin.js` cannot `require()` from disk (compiled Bun binary) — it reads that file and evaluates it through `new Function(...)` with a `createRequire` that resolves only node built-ins. Parsing already lives in `caveman-parse.js` (#602) and loads the same way, so a second parser copy is no longer the risk; a THIRD home for the state primitives would be, because opencode would keep its own drifting version of them.
- The two statusline scripts have no shared runtime with the JS, so they re-implement path resolution by hand. `tests/verify_repo.py::verify_powershell_static` greps both against the `SESSIONS_DIRNAME` and `SESSION_ID_RE` constants to catch drift — there is no behavioral `.ps1` test on the POSIX runners, so that grep is the only guard on the Windows badge.
- Editing anything in `src/hooks/` means regenerating `src/hooks/checksums.sha256` (same file set, recomputed digests) — `tests/verify_repo.py` fails the build otherwise, and `bin/install.js` verifies remote hook downloads against the manifest for the pinned ref.
- Hooks must respect `CLAUDE_CONFIG_DIR` env var, not hardcode `~/.claude`. Same for `bin/install.js` / statusline scripts.
- **Any entrypoint that reads a host hook payload from stdin returns on the first complete JSON object, never at EOF.** Under the Windows pipe implementation the host's close lags arbitrarily (#729/#833, #949), so a reader that waits for EOF burns the host's whole budget with its work done. The readers that follow this rule: `caveman-activate.js` and `caveman-mode-tracker.js` (per-chunk parse), `packages/cli/src/native-hook-fast.ts` (`stdin()`), `readHookStdin()` in `packages/cli/src/index.ts` (shrink-hook, memory recall, native-hook), and `readNativeHookPayload` in `proxy/cmd/caveman-proxy/main.go`. A new hook callback uses one of those, not `readStdin()` or `io.ReadAll`. Each has a keep-the-writer-open regression test; add one for any new reader.
- `bin/install.js` is the only installer source. `install.sh` / `install.ps1` at repo root are 30-line shims that delegate to it. Never re-add per-OS install logic to the shims — that's how we got the Windows quoting bug (#249).
- Any settings.json read in installer or hooks must go through `bin/lib/settings.js` `readSettings()` so JSONC comments don't crash the merge. Any settings.json write must run through `validateHookFields()` first.
