# Contributing to caveman

Thanks for considering a contribution. Caveman is a multi-agent skill that
makes 30+ AI coding agents talk in compressed caveman-style prose. Most
contributions fall into one of three buckets:

1. **Editing skill prose** — change how caveman speaks, what intensity levels do, what slash commands trigger.
2. **Adding a new agent** — wire a fresh editor/CLI/IDE into the unified installer.
3. **Fixing the hooks or installer** — Claude Code hooks, the Node installer, the per-repo init script.

Caveman like simple. Small focused PR > big rewrite.

---

## Quick orientation

The repo distributes one skill (caveman) plus a handful of sub-skills
(caveman-commit, caveman-review, caveman-compress, cavecrew-*) to many
agents through different distribution mechanisms (Claude Code plugin, Codex
plugin, Gemini extension, Cursor/Windsurf/Cline rule files, `npx skills` for
the long tail). A single Node installer at `bin/install.js` detects which
agents are on the user's machine and installs the right thing for each.

Sources of truth live at the **top level** of the repo. Agent-specific
copies live under `plugins/caveman/` and similar mirror dirs — those are
**rebuilt by CI** and edits there are reverted.

---

## What to edit (sources of truth)

| I want to change... | Edit this file |
|---|---|
| Caveman behavior (intensity levels, voice, rules) | `skills/caveman/SKILL.md` |
| Caveman commit-message format | `skills/caveman-commit/SKILL.md` |
| Caveman code-review format | `skills/caveman-review/SKILL.md` |
| Caveman compress logic | `skills/caveman-compress/SKILL.md` and `skills/caveman-compress/scripts/` |
| Caveman quick-reference card | `skills/caveman-help/SKILL.md` |
| Cavecrew decision guide (when to delegate to subagents) | `skills/cavecrew/SKILL.md` |
| cavecrew subagent definitions | `agents/cavecrew-investigator.md`, `agents/cavecrew-builder.md`, `agents/cavecrew-reviewer.md` |
| Auto-activation rule body (Cursor/Windsurf/Cline/Copilot) | `src/rules/caveman-activate.md` |
| Add support for a new agent | `bin/install.js` (PROVIDERS array) |
| Per-repo init script (drops rule files into a user's repo) | `src/tools/caveman-init.js` |
| Claude Code hooks | `src/hooks/caveman-activate.js`, `src/hooks/caveman-mode-tracker.js`, `src/hooks/caveman-config.js`, `src/hooks/caveman-statusline.sh`, `src/hooks/caveman-statusline.ps1` |
| Settings.json read/write helpers | `bin/lib/settings.js` |
| MCP shrink server | `src/mcp-servers/caveman-shrink/` |

That's it. Every other markdown file with `SKILL.md` in the path is a copy.

---

## What NOT to edit (CI-generated mirrors)

Edits to these files are wiped by the next CI run. The
`.github/workflows/sync-skill.yml` job rebuilds them from the sources above
on every push to `main`.

| Path | Rebuilt from |
|------|--------------|
| `plugins/caveman/skills/caveman/SKILL.md` | `skills/caveman/SKILL.md` |
| `plugins/caveman/skills/caveman-commit/SKILL.md` | `skills/caveman-commit/SKILL.md` |
| `plugins/caveman/skills/caveman-review/SKILL.md` | `skills/caveman-review/SKILL.md` |
| `plugins/caveman/skills/caveman-compress/SKILL.md` | `skills/caveman-compress/SKILL.md` |
| `plugins/caveman/skills/cavecrew/SKILL.md` | `skills/cavecrew/SKILL.md` |
| `plugins/caveman/agents/cavecrew-*.md` | `agents/cavecrew-*.md` |
| `dist/caveman.skill` | ZIP of `skills/caveman/` |
| `.codex-plugin/plugin.json` (inside any plugin distribution) | generated at package time |

When in doubt: if the file lives under `plugins/`, `dist/`, or any agent
dotdir mirror, it's a build artifact. Edit the top-level source instead.

---

## Adding a new agent

The unified Node installer at `bin/install.js` is the **single source of
truth** for the supported-agent list. The README and `INSTALL.md` install
tables mirror it by hand — bash and PowerShell shims at the repo root just
delegate to it.

1. Confirm the agent has a distribution path. Either:
   - it has a profile slug in upstream [vercel-labs/skills](https://github.com/vercel-labs/skills) (most common), or
   - it has a native plugin / extension / rule-file mechanism we can target.
2. Append a row to the `PROVIDERS` array in `bin/install.js`. Each row needs:
   - `id` — short kebab-case identifier (e.g. `windsurf`)
   - `label` — human display name (e.g. `Windsurf`)
   - `mech` — distribution mechanism (`plugin`, `extension`, `rules-file`, `skills-cli`, …)
   - `detect` — clause spec like `command:foo||dir:$HOME/x` describing how to detect the agent
   - `profile` — the vercel-labs/skills slug, if applicable
   - `soft: true` — set when detection is config-dir-only (best-effort)
3. Run `node bin/install.js --list` and confirm the new row renders correctly. Soft probes should show as `(soft)`.
4. Add a row to the install tables in `README.md` and `INSTALL.md`.
5. No CI changes needed — the workflow re-reads `bin/install.js` automatically.

Bad slug? `npx skills add` fails at install **runtime**, not at install-script
load. Always verify the slug against the vercel-labs/skills README before
merging.

---

## Adding a new skill

1. Create `skills/<name>/SKILL.md` with frontmatter:
   ```yaml
   ---
   name: <name>
   description: <one sentence, present tense>
   ---
   ```
2. Create `skills/<name>/README.md` — human-facing summary, install hint, example.
3. Add `skills/<name>/scripts/` if the skill ships helpers (Python or Node).
4. If the skill should be in the Claude Code plugin, add a sync step to `.github/workflows/sync-skill.yml` so CI mirrors it into `plugins/caveman/skills/<name>/`.
5. If it's user-invocable as a slash command, add a row to the slash-command table in `README.md` and `INSTALL.md`.
6. Add an eval prompt to `evals/prompts/en.txt` if you want the eval harness to score it.

---

## Running tests

```bash
# Installer unit + e2e tests (Node)
npm test

# Compress-skill safety tests (Python)
python3 -m unittest tests.test_compress_safety

# Per-repo init tests
node tests/test_caveman_init.js

# Flag-file symlink-safety tests
node tests/test_symlink_flag.js
```

CI runs all of the above on every PR. If any test depends on a network or
external SDK, it must skip cleanly when the dependency is missing — never
gate the whole suite on optional creds.

---

## Running benchmarks and evals

Benchmarks hit the real Claude API and record raw token counts:

```bash
uv run python benchmarks/run.py     # needs ANTHROPIC_API_KEY in .env.local
```

Evals are a three-arm offline harness (`__baseline__`, `__terse__`, each skill):

```bash
python evals/llm_run.py             # regenerates evals/snapshots/results.json
python evals/measure.py             # reads snapshot, prints token deltas
```

Snapshots are committed to git. Only regenerate when a `SKILL.md` or
`evals/prompts/en.txt` changes. Numbers in `README.md` and any docs come from
real runs — never invent or round.

---

## Pull-request guidelines

- **Conventional Commits** for the commit subject. See `skills/caveman-commit/SKILL.md` for the format we use here.
- **One concern per PR.** A README copy-edit and an installer fix go in separate PRs.
- **Update `package.json` `files`** if you add a new top-level directory the installer needs to ship to npm. Files outside that array don't get published.
- **Show before/after** for prose changes to any `SKILL.md`. One sentence on why the new wording is better.
- **Mention the CI sync.** If you edited a source-of-truth file, note it: "CI will resync `plugins/caveman/skills/...` on merge."

PR descriptions don't need to be long. Caveman style fine. Just say what change, why.

---

## Code style

A handful of invariants that have bitten us before. Keep them.

- **Hooks must silent-fail on filesystem errors.** A `try/catch` that swallows the error is correct here. A hook that throws blocks Claude Code session start — that's user-facing breakage. See existing patterns in `src/hooks/caveman-activate.js`.
- **Settings.json reads and writes go through `bin/lib/settings.js`.** It tolerates JSONC comments. Direct `JSON.parse` on a user's `settings.json` will crash on a single `// comment`.
- **Validate hook entries before writing.** Use `validateHookFields()` in `bin/lib/settings.js`. Claude Code's Zod schema silently discards the **entire** `settings.json` on a single bad hook entry — one malformed write poisons the user's whole config.
- **Symlink-safe flag writes via `safeWriteFlag()`** in `src/hooks/caveman-config.js`. The flag file lives at a predictable path under `$CLAUDE_CONFIG_DIR/`; without `O_NOFOLLOW` and a parent-symlink check, a local attacker can clobber any file the user can write.
- **Honor `CLAUDE_CONFIG_DIR`.** Hooks, the installer, and the statusline scripts must respect it — never hardcode `~/.claude`.
- **`install.sh` and `install.ps1` at the repo root are 30-line shims** that delegate to `bin/install.js`. Don't re-add per-OS install logic to them. Quoting bugs that way lie.

---

## Ideas

See [issues labeled `good first issue`](../../issues?q=label%3A%22good+first+issue%22)
for starter tasks. Or grep `TODO` / `FIXME` in `src/hooks/`, `bin/`, `src/tools/` —
each one is a real lead.

Caveman like contribution. You bring rock, caveman put rock in pile. Pile
get bigger. Brain still big.
