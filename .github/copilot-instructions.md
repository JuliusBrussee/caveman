# Copilot Instructions — caveman

## Project overview

Caveman makes AI coding agents respond in compressed caveman-style prose — cuts ~65-75% output tokens while maintaining full technical accuracy. Ships as plugins/extensions/rule files for 30+ AI coding agents (Claude Code, Codex, Gemini CLI, Cursor, Windsurf, Cline, Copilot, and many others via `npx skills`).

## Build, test, lint

```bash
# Run all installer tests
npm test

# Run a single test file
node --test tests/installer/some-file.test.mjs

# Compress-skill safety tests (Python)
python3 -m unittest tests.test_compress_safety

# Per-repo init tests
node tests/test_caveman_init.js

# Flag-file symlink-safety tests
node tests/test_symlink_flag.js

# Benchmarks (needs ANTHROPIC_API_KEY in .env.local)
uv run python benchmarks/run.py

# Evals (three-arm offline harness)
python evals/llm_run.py
python evals/measure.py
```

No build step for the main codebase. `dist/caveman.skill` is a ZIP rebuilt by CI — never commit manually.

## Architecture

### Single source of truth model

Skills live in `skills/<name>/SKILL.md` — these are the **only** files to edit for behavior changes. CI syncs them into `plugins/caveman/skills/` for Claude Code plugin distribution. Edits under `plugins/` are wiped on next CI run.

### Distribution flow

```
skills/<name>/SKILL.md  ──CI sync──▶  plugins/caveman/skills/<name>/SKILL.md
src/rules/caveman-activate.md  ──caveman-init.js──▶  per-repo IDE rule files
bin/install.js  ──detects agents──▶  installs correct artifact per agent
```

### Hook system (Claude Code)

Three hooks in `src/hooks/` communicate via a flag file at `$CLAUDE_CONFIG_DIR/.caveman-active`:
- **caveman-activate.js** (SessionStart) — writes mode to flag, emits ruleset as system context
- **caveman-mode-tracker.js** (UserPromptSubmit) — handles `/caveman` slash commands, natural-language activation, per-turn reinforcement
- **caveman-config.js** — shared module with `getDefaultMode()`, `safeWriteFlag()`, config resolution

### Installer

`bin/install.js` is the single source of truth for all 30+ agents. The `PROVIDERS` array defines each agent's detection and install mechanism. `install.sh`/`install.ps1` at root are thin shims that delegate to it.

## Key conventions

### Sources of truth — only edit these

| Change | File |
|--------|------|
| Caveman behavior/voice | `skills/caveman/SKILL.md` |
| Commit message format | `skills/caveman-commit/SKILL.md` |
| Code review format | `skills/caveman-review/SKILL.md` |
| Compress logic | `skills/caveman-compress/SKILL.md` + `scripts/` |
| Auto-activation rule | `src/rules/caveman-activate.md` |
| Add a new agent | `bin/install.js` (PROVIDERS array) |
| Settings.json helpers | `bin/lib/settings.js` |

### Code style invariants

- **Hooks must silent-fail** on all filesystem errors. A thrown error blocks Claude Code session start.
- **Settings.json I/O goes through `bin/lib/settings.js`** — it tolerates JSONC comments. Raw `JSON.parse` will crash on user configs with comments.
- **Validate hook entries** with `validateHookFields()` before writing to settings.json. One bad entry makes Claude Code discard the entire file.
- **Flag writes via `safeWriteFlag()`** in `caveman-config.js` — prevents symlink-clobber attacks on predictable paths.
- **Honor `CLAUDE_CONFIG_DIR` env var** everywhere — never hardcode `~/.claude`.
- **`install.sh`/`install.ps1` are shims only** — never add install logic to them.

### PR conventions

- Conventional Commits for subject lines
- One concern per PR
- Show before/after for prose changes to any `SKILL.md`
- Benchmark/eval numbers must come from real runs — never invent or round

### README rules

The README uses intentional "caveman voice" branding. Preserve it. Optimize for non-technical readers who need to decide whether to install within 60 seconds.
