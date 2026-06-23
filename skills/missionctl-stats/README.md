# missionctl-stats

Real session token receipts. No AI estimation.

## What it does

Reads the current Claude Code session log directly and reports actual input/output token usage plus estimated savings versus a non-missionctl baseline. Numbers come from the JSONL session log on disk — the model itself does not compute or estimate them. Output is injected by the `missionctl-mode-tracker` hook, which intercepts `/missionctl-stats` and returns the formatted stats as a blocked-decision reason.

Each run also writes a lifetime-savings suffix file used by the statusline badge (`📡 12.4k`).

## How to invoke

```
/missionctl-stats
```

## Example output

```
Session: 47 turns
Input:   12,304 tokens
Output:   3,891 tokens (missionctl)
Baseline: 11,247 tokens (estimated without missionctl)
Saved:    7,356 tokens (~65%)
```

## See also

- [`SKILL.md`](./SKILL.md) — hook contract and mechanics
- [missionctl README](../../README.md) — repo overview
