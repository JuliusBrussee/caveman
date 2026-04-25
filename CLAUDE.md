# CLAUDE.md — Layman

## Product

Layman is a Claude Code / Codex skill that turns completed AI coding-agent work into short plain-English summaries.

Positioning: Layman saves understanding. Brief Mode saves tokens.

Tagline:

> 🧠 why read big AI dump when simple words do trick — Claude Code / Codex skill that turns agent work into plain-English summaries anyone can understand

Theme: now anyone can code, for real, because the handoff is understandable.

## Core Behavior

Default mode is `summary`.

Final handoff format:

```txt
Layman Summary

Done:
- 1-4 short bullets

Why it matters:
1-2 plain-English sentences about impact.

What changed:
Simple explanation of the important technical change.

Check this:
Practical next verification step.

Warning:
Only include if there is a real risk, missing test, migration, manual step, security issue, or uncertainty.
```

Second mode is `explain`. Use `/layman explain` when the user wants more context. It may include a short `Terms:` section for necessary technical terms.

## Source Files

Edit these first:

| File | Purpose |
|------|---------|
| `skills/layman/SKILL.md` | Main Layman behavior |
| `rules/layman-activate.md` | Always-on rule copied into repo integrations |
| `skills/layman-commit/SKILL.md` | Clear commit messages |
| `skills/layman-review/SKILL.md` | Clear code review comments |
| `skills/layman-help/SKILL.md` | Help card |
| `layman-compress/SKILL.md` | Markdown memory-file simplifier |

Generated or synced copies:

| File | Synced from |
|------|-------------|
| `layman/SKILL.md` | `skills/layman/SKILL.md` |
| `plugins/layman/skills/layman/SKILL.md` | `skills/layman/SKILL.md` |
| `.cursor/skills/layman/SKILL.md` | `skills/layman/SKILL.md` |
| `.windsurf/skills/layman/SKILL.md` | `skills/layman/SKILL.md` |
| `layman.skill` | ZIP of `skills/layman/` |
| `.clinerules/layman.md` | `rules/layman-activate.md` |
| `.github/copilot-instructions.md` | `rules/layman-activate.md` |
| `.cursor/rules/layman.mdc` | `rules/layman-activate.md` plus frontmatter |
| `.windsurf/rules/layman.md` | `rules/layman-activate.md` plus frontmatter |

## Hooks

Claude Code hook files live in `hooks/`.

- `layman-config.js` resolves `LAYMAN_DEFAULT_MODE`, config file defaults, valid modes, and safe flag reads/writes.
- `layman-activate.js` writes `$CLAUDE_CONFIG_DIR/.layman-active` and emits the Layman ruleset at session start.
- `layman-mode-tracker.js` watches `/layman`, `/layman summary`, `/layman explain`, `/layman-commit`, `/layman-review`, and `/layman:compress`.
- `layman-statusline.sh` and `layman-statusline.ps1` render `[LAYMAN]` or `[LAYMAN:EXPLAIN]`.

Valid modes: `summary`, `explain`, `commit`, `review`, `compress`, `off`.

Hooks must silent-fail on filesystem errors and must respect `CLAUDE_CONFIG_DIR`.

## CI Sync

`.github/workflows/sync-skill.yml` syncs source files to agent-specific copies and rebuilds `layman.skill`.

Keep workflow paths in sync with renamed files.

## Verification

Run:

```bash
python3 tests/verify_repo.py
python3 -m unittest tests/test_hooks.py
```

## Writing Rules

- README is the product front door. Keep it clear for non-technical readers.
- Main product positioning is understanding-first.
- Token-saving behavior lives under Layman Brief modes only.
- Do not make Layman sound babyish or insulting.
- Do not hide risks, missing tests, migrations, or manual steps.
