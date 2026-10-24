---
name: caveman-help
description: >
  Quick-reference card for the three caveman skills and their commands.
  Trigger: /caveman-help or "caveman help".
---

# Caveman Help

Display this reference card when invoked. One-shot — do NOT change mode, write flag files, or persist anything. Output in caveman style.

## Modes

| Mode | Trigger | What change |
|------|---------|-------------|
| **caveman** | `/caveman` | The voice. Answer first, no fluff, every fact kept. Default. |
| **ultracave** | `/ultracave` (alias `/caveman ultra`) | Grammar stripped. Fragments, one word when enough, each fact once. |
| **megacave** | `/megacave` (alias `/caveman wenyan`) | Classical Chinese 文言文. Far fewer characters, technical terms verbatim. |

Mode stick until changed or session end.
`/caveman status` reports current mode without changing it. Claude Code and the standalone OpenCode plugin read stored state; other hosts answer from the conversation and mark it `(not tracked by this host)`.

## Skills

| Skill | Trigger | What it do |
|-------|---------|-----------|
| **caveman-commit** | `/caveman-commit` | Terse commit messages. Conventional Commits. ≤50 char subject. |
| **caveman-review** | `/caveman-review` | One-line PR comments: `L42: bug: user null. Add guard.` |
| **caveman-compress** | `/caveman-compress <file>` | Compress .md files to caveman prose. 33.2% fewer tokens on the repo's five test files. |
| **caveman-help** | `/caveman-help` | This card. |

## Deactivate

Say "stop caveman" or "normal mode". Resume anytime with `/caveman`.

## Language

Keep user's language by default — reply in the language user writes, never switch regardless of example text or multilingual context elsewhere. Compress the style, not the language. Technical terms, code, commands, commit types, and exact error strings stay verbatim unless user ask for translation.

## Configure Default Mode

Default mode = `caveman`. Change it:

**Environment variable** (highest priority):
```bash
export CAVEMAN_DEFAULT_MODE=ultracave
```

**Config file** (`~/.config/caveman/config.json`):
```json
{ "defaultMode": "ultracave" }
```

For opt-in activation in Claude Code, set `defaultMode` to `manual`. Sessions start off; `/caveman` or `talk like caveman` activates caveman. `off` also disables bare-command activation. `manual` is a startup policy, not a mode. OpenCode still starts in caveman mode because its installer also supplies static activation rules.

Set `"off"` to suppress automatic and bare-command activation. Use `"manual"` when bare `/caveman` should activate.

Resolution: env var > repo config > user config > `caveman`. Legacy values `lite`, `full`, `ultra`, `wenyan*` still resolve.

## More

Full docs: https://github.com/JuliusBrussee/caveman
