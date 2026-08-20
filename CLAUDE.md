# CLAUDE.md — caveman (maintainer instructions)

Architecture reference (directory layout, hook system internals, CI sync workflow, skill
system, evals/benchmarks): `docs/MAINTAINER_GUIDE.md`. This file holds only the rules an
agent must follow when editing this repo.

Caveman makes AI coding agents respond in compressed caveman-style prose — cuts 65% output
tokens (measured), full technical accuracy. Ships as Claude Code plugin, Codex plugin,
Gemini CLI extension, agent rule files for 40+ others via `npx skills`.

## README is a product artifact

README = product front door, non-technical readers decide install from it. Any README
change: readable by non-AI-agent users (translate jargon like "SessionStart hook injects
system context"), Before/After examples first, install table complete + accurate, What You
Get table synced with actual code (feature ships or removed → update table), preserve
caveman voice intentionally ("Brain still big.", "One rock. That it." — don't normalize),
benchmark numbers from real `benchmarks/`/`evals/` runs only — never invent or round,
re-run if doubt. New agent in install table → add detail block in `<details>` section.
60-second non-programmer readability check before any README commit.

## Key rules for agents working here

- Edit `skills/<name>/SKILL.md` for behavior changes. Never edit synced copies under `plugins/caveman/skills/`.
- Edit `src/rules/caveman-activate.md` for auto-activation rule changes. Never edit a per-agent rule copy on a user's machine.
- Edit `src/rules/caveman-openclaw-bootstrap.md` for the OpenClaw SOUL.md bootstrap snippet — keep `<!-- caveman-begin -->`/`<!-- caveman-end -->` markers and the `Respond terse like smart caveman` sentinel; `bin/lib/openclaw.js` keys idempotency off both, keep its embedded fallback byte-equivalent.
- Per-skill human docs live in `skills/<name>/README.md`, LLM-facing body in `SKILL.md`. Don't merge — different audiences.
- Build artifacts go in `dist/`. Never check files into `dist/` manually — CI rebuilds on push, gitignored.
- `INSTALL.md` is the per-agent install reference. Keep the README install table short, link out to it.
- Benchmark and eval numbers must be real. Never fabricate or estimate.
- CI workflow commits back to main after merge — account for this when checking branch state.
- Hook files must silent-fail on all filesystem errors. Never let a hook crash block session start.
- Any new flag file write must go through `safeWriteFlag()` in `caveman-config.js` — direct `fs.writeFileSync` on predictable user-owned paths reopens the symlink-clobber attack surface.
- Hooks must respect `CLAUDE_CONFIG_DIR` env var, never hardcode `~/.claude`. Same for `bin/install.js`/statusline scripts.
- `bin/install.js` is the only installer source — `install.sh`/`install.ps1` are 30-line shims, never re-add per-OS logic to them (Windows quoting bug #249).
- Any settings.json read in installer/hooks goes through `bin/lib/settings.js` `readSettings()` (JSONC-tolerant). Any write runs through `validateHookFields()` first.
