# missionctl-help

Quick-reference card. One shot, no mode change.

## What it does

Prints a cheat sheet of all missionctl modes, sibling skills, deactivation triggers, and how to set the default mode via env var or config file. One-shot display — does not flip the active mode, write flag files, or persist anything. Use when you forget the slash commands.

## How to invoke

```
/missionctl-help
```

Also triggers on "missionctl help", "what missionctl commands", "how do I use missionctl".

## Example output

```
Modes:
  /missionctl              full (default)
  /missionctl lite         lighter
  /missionctl ultra        extreme
  /missionctl wenyan       classical Chinese

Skills:
  /missionctl-commit       terse Conventional Commits
  /missionctl-review       one-line PR comments
  /missionctl-stats        session token savings

Deactivate:
  "stop missionctl" or "normal mode"
```

## See also

- [`SKILL.md`](./SKILL.md) — full reference card
- [missionctl README](../../README.md) — repo overview
