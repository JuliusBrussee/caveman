---
name: missionctl-help
description: >
  Quick-reference card for all missionctl modes, skills, and commands.
  One-shot display, not a persistent mode. Trigger: /missionctl-help,
  "missionctl help", "what missionctl commands", "how do I use missionctl".
---

# missionctl Help

Display this reference card when invoked. One-shot — do NOT change mode, write flag files, or persist anything. Output in missionctl style.

## Modes

| Mode | Trigger | What change |
|------|---------|-------------|
| **Lite** | `/missionctl lite` | Drop filler. Keep sentence structure. |
| **Full** | `/missionctl` | Drop articles, filler, pleasantries, hedging. Fragments OK. Default. |
| **Ultra** | `/missionctl ultra` | Extreme compression. Bare fragments. Tables over prose. |
| **Wenyan-Lite** | `/missionctl wenyan-lite` | Classical Chinese style, light compression. |
| **Wenyan-Full** | `/missionctl wenyan` | Full 文言文. Maximum classical terseness. |
| **Wenyan-Ultra** | `/missionctl wenyan-ultra` | Extreme. Ancient scholar on a budget. |

Mode stick until changed or session end.

## Skills

| Skill | Trigger | What it do |
|-------|---------|-----------|
| **missionctl-commit** | `/missionctl-commit` | Terse commit messages. Conventional Commits. ≤50 char subject. |
| **missionctl-review** | `/missionctl-review` | One-line PR comments: `L42: bug: user null. Add guard.` |
| **missionctl-compress** | `/missionctl-compress <file>` | Compress .md files to missionctl prose. Saves ~46% input tokens. |
| **missionctl-help** | `/missionctl-help` | This card. |

## Deactivate

Say "stop missionctl" or "normal mode". Resume anytime with `/missionctl`.

## Language

Keep user's language by default. User write Portuguese → reply Portuguese missionctl. Compress the style, not the language. Technical terms, code, commands, commit types, and exact error strings stay verbatim unless user ask for translation.

## Configure Default Mode

Default mode = `full`. Change it:

**Environment variable** (highest priority):
```bash
export MISSIONCTL_DEFAULT_MODE=ultra
```

**Config file** (`~/.config/missionctl/config.json`):
```json
{ "defaultMode": "lite" }
```

Set `"off"` to disable auto-activation on session start. User can still activate manually with `/missionctl`.

Resolution: env var > config file > `full`.

## More

Full docs: https://github.com/dnl-re/missionctl
