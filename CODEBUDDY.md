# CODEBUDDY.md — caveman

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

Caveman makes AI coding agents respond in compressed caveman-style prose — cuts ~65-75% output tokens, full technical accuracy. Ships as Claude Code plugin, CodeBuddy Code plugin, Codex plugin, Gemini CLI extension, agent rule files for Cursor, Windsurf, Cline, Copilot, 40+ others via `npx skills`.

---

## What lives where

Post-cleanup layout. Sources of truth at the top, distribution mirrors below, build outputs in `dist/`, human docs alongside each skill.

```
caveman/
├── README.md                    # Front door (product pitch)
├── INSTALL.md                   # Per-agent install commands
├── CONTRIBUTING.md              # Dev guide
├── CLAUDE.md                    # Claude Code maintainer instructions
├── CODEBUDDY.md                 # This file (CodeBuddy Code maintainer instructions)
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
├── commands/                    # Codex/Gemini TOML command stubs (root for plugin auto-discovery)
│
├── src/                         # Internal source — not auto-discovered by plugin
│   ├── hooks/                   # Claude Code / CodeBuddy Code hooks (installer reads here)
│   ├── rules/                   # Auto-activation rule body (single source)
│   ├── tools/                   # caveman-init.js (per-repo rule writer)
│   └── mcp-servers/             # caveman-shrink npm-published MCP middleware
│
├── .claude-plugin/              # Claude Code plugin manifest (REQUIRED at root)
├── .codebuddy-plugin/           # CodeBuddy Code plugin manifest (REQUIRED at root)
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

## Hook system (CodeBuddy Code)

Identical to Claude Code hooks. Three hooks in `src/hooks/` plus a `caveman-config.js` shared module and a `package.json` CommonJS marker. Communicate via flag file at `$CODEBUDDY_CONFIG_DIR/.caveman-active` (falls back to `~/.codebuddy/.caveman-active`).

```
SessionStart hook ──writes "full"──▶ $CODEBUDDY_CONFIG_DIR/.caveman-active ◀──writes mode── UserPromptSubmit hook
                                                       │
                                                    reads
                                                       ▼
                                              caveman-statusline.sh
                                            [CAVEMAN] / [CAVEMAN:ULTRA] / ...
```

All hooks honor `CODEBUDDY_CONFIG_DIR` for non-default config locations.

### Hook installation

**Plugin install** — hooks wired automatically by plugin system via `.codebuddy-plugin/plugin.json`.

**Standalone install** — `bin/install.js` (the unified Node installer) copies hook files into `$CODEBUDDY_CONFIG_DIR/hooks/` and merges SessionStart + UserPromptSubmit + statusline into `settings.json`. Uses the JSONC-tolerant helpers in `bin/lib/settings.js`.

**Uninstall** — `npx -y github:JuliusBrussee/caveman -- --uninstall` removes codebuddy hooks, plugin, and MCP shrink entries.

---

## Key rules for agents working here

- Edit `skills/<name>/SKILL.md` for behavior changes. Never edit synced copies under `plugins/caveman/skills/`.
- Edit `src/rules/caveman-activate.md` for auto-activation rule changes.
- Hook files must silent-fail on all filesystem errors. Never let hook crash block session start.
- Hooks must respect `CODEBUDDY_CONFIG_DIR` env var, not hardcode `~/.codebuddy`.
- `bin/install.js` is the only installer source.
- Any settings.json read in installer or hooks must go through `bin/lib/settings.js` `readSettings()`.
