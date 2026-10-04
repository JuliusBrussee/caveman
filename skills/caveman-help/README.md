# caveman-help

Quick-reference card. One shot, no mode change.

## What it does

Prints a cheat sheet of all caveman modes, sibling skills, deactivation triggers, and how to set the default mode via env var or config file. One-shot display — does not flip the active mode, write flag files, or persist anything. Use when you forget the slash commands.

## How to invoke

```
/caveman-help
```

Also triggers on "caveman help", "what caveman commands", "how do I use caveman".

## Example output

```
Modes:
  /caveman              the voice (default)
  /ultracave            grammar stripped
  /megacave             classical Chinese
  /caveman status       report the mode, change nothing

Skills:
  /caveman-commit       terse Conventional Commits
  /caveman-review       one-line PR comments
  /caveman-compress     smaller Markdown memory files
  /caveman-stats        recorded token usage (savings unknown)

Deactivate:
  "stop caveman" or "normal mode"
```

## See also

- [`SKILL.md`](./SKILL.md) — full reference card
- [Caveman README](../../README.md) — repo overview
