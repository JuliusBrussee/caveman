---
name: caveman-help
description: >
  Quick-reference card for all caveman modes, skills, and commands.
  One-shot display, not a persistent mode. Trigger: /caveman-help,
  "caveman help", "what caveman commands", "how do I use caveman".
---

# Caveman Help

Display this reference card when invoked. One-shot — do NOT change mode, write flag files, or persist anything. Output in caveman style.

## Modes

| Mode | Trigger | What change |
|------|---------|-------------|
| **Lite** | `/caveman lite` | Drop filler. Keep sentence structure. |
| **Full** | `/caveman` | Drop articles, filler, pleasantries, hedging. Fragments OK. Default. |
| **Ultra** | `/caveman ultra` | Extreme compression. Bare fragments. Tables over prose. |
| **Wenyan-Lite** | `/caveman wenyan-lite` | Classical Chinese style, light compression. |
| **Wenyan-Full** | `/caveman wenyan` | Full 文言文. Maximum classical terseness. |
| **Wenyan-Ultra** | `/caveman wenyan-ultra` | Extreme. Ancient scholar on a budget. |

Mode stick until changed or session end.

## Skills

| Skill | Trigger | What it do |
|-------|---------|-----------|
| **caveman-commit** | `/caveman-commit` | Terse commit messages. Conventional Commits. ≤50 char subject. |
| **caveman-review** | `/caveman-review` | One-line PR comments: `L42: bug: user null. Add guard.` |
| **caveman-compress** | `/caveman-compress <file>` | Compress .md files to caveman prose. Saves ~46% input tokens. |
| **caveman-help** | `/caveman-help` | This card. |

## Deactivate

Say "stop caveman" or "normal mode". Resume anytime with `/caveman`.

## Configure Default Mode

Default mode = `full`. Change it:

**Environment variable** (highest priority):
```bash
export CAVEMAN_DEFAULT_MODE=ultra
```

**Repo-local config** (checked-in per-project default — `<repo>/.caveman/config.json` or `<repo>/.caveman.json`):
```json
{ "defaultMode": "lite" }
```
Walks up from `cwd` to find the nearest one, so any subdir of the repo picks it up. Useful for pinning a project's default mode without polluting every contributor's env or user config.

**User config file** (`~/.config/caveman/config.json` — or `$XDG_CONFIG_HOME/caveman/config.json` / `%APPDATA%\caveman\config.json`):
```json
{ "defaultMode": "lite" }
```

Set `"off"` to disable auto-activation on session start. User can still activate manually with `/caveman`.

Resolution: env var > repo-local config > user config > `full`.

## More

Full docs: https://github.com/JuliusBrussee/caveman
