---
name: layman-help
description: >
  Quick-reference card for Layman modes, skills, and commands. One-shot display,
  not a persistent mode. Trigger: /layman-help, "layman help",
  "what layman commands", or "how do I use layman".
---

# Layman Help

Display this reference card when invoked. One-shot only. Do not change mode, write flag files, or persist anything.

## Modes

| Mode | Trigger | What it does |
|------|---------|--------------|
| **Summary** | `/layman` or `/layman summary` | Final handoff with Done, Why it matters, What changed, Check this, and Warning if needed. |
| **Explain** | `/layman explain` | Adds more context and explains necessary technical terms in plain English. |
| **Brief** | `/layman brief` or `/layman full` | Short technical answers with filler removed. |
| **Lite** | `/layman lite` | Light brevity, normal sentences. |
| **Ultra** | `/layman ultra` | Maximum compression. |
| **Wenyan** | `/layman wenyan` | Classical Chinese terse style. |

## Skills

| Skill | Trigger | What it does |
|-------|---------|--------------|
| **layman-commit** | `/layman-commit` | Clear Conventional Commit messages. |
| **layman-review** | `/layman-review` | Clear, actionable code review comments. |
| **layman-compress** | `/layman:compress <file>` | Simplifies long Markdown memory files while preserving facts and structure. |
| **layman-help** | `/layman-help` | Shows this card. |

## Deactivate

Say "stop layman" or "normal mode". Resume anytime with `/layman`.

## Configure Default Mode

Default mode = `summary`.

Environment variable:

```bash
export LAYMAN_DEFAULT_MODE=explain
```

Config file (`~/.config/layman/config.json`):

```json
{ "defaultMode": "summary" }
```

Set `"off"` to disable auto-activation on session start. User can still activate manually with `/layman`.

Resolution: env var > config file > `summary`.

Full docs: https://github.com/vamsi920/layman
