# caveman for Hermes Agent

A community port of caveman to [Hermes Agent](https://github.com/NousResearch/hermes) —
an AI agent with its own skills system (`~/.hermes/skills/`), TUI, desktop app, and
messaging gateway. Hermes isn't reachable through the unified `bin/install.js` path
(no vercel-labs/skills slug, no Claude-Code plugin/hook surface), so this port lives in
its own directory and installs via a small shell script.

Self-contained and additive — it does **not** touch the Node installer, CI mirrors, or
any top-level source of truth.

## What's here

| File | Purpose |
|---|---|
| `skills/caveman/SKILL.md` | Caveman terse-output mode, in Hermes skill format (`metadata.hermes` frontmatter). Levels lite/full/ultra + wenyan. |
| `skills/cavecrew/SKILL.md` | Cavecrew decision guide reworked for Hermes `delegate_task` (the Claude-Code `agents/cavecrew-*.md` files have no Hermes equivalent — role contracts are passed inline as subagent `context`). Marked experimental. |
| `install.sh` | Idempotent installer → `~/.hermes/skills/productivity/`. `--dry-run` supported. |
| `benchmarks/` | tiktoken token-reduction measurement + results. |

## Install

```bash
cd ports/hermes
./install.sh                 # → ~/.hermes/skills/productivity/
./install.sh --dry-run       # preview
hermes skills list | grep -E 'caveman|cavecrew'
```

Use: say "caveman mode" (levels: `caveman lite|full|ultra`). Off: "normal mode".

## What's different from the Claude Code distribution

- **No hooks / statusline / MCP shrink / slash commands** — Hermes has no equivalent
  surfaces. This is skills-only.
- **cavecrew → `delegate_task`** instead of agent files with `tools:`/`model:` frontmatter.
  Toolsets use Hermes names (`file`, `terminal`). Marked experimental + context-bound
  because each crew task costs a full delegation round-trip.
- **Verbose-profile conflict handled** — Hermes profiles can mandate verbose answer
  formats; the skill explicitly suspends those while caveman is active, but never
  compresses safety-critical output (Auto-Clarity).
- **Honest per-level numbers** — measured full ≈ 53%, ultra ≈ 71% (see `benchmarks/`),
  rather than a single headline figure.

## Note for maintainers

Kept deliberately separate and minimal for reviewability. If you'd prefer native
integration, I'm happy to wire Hermes into the `PROVIDERS` array in `bin/install.js`
with a `mech: hermes-skills` distribution path and a `command:hermes` detection clause —
say the word and I'll follow up with that PR.

Upstream port maintained at https://github.com/nickgogerty/caveman-hermes.
