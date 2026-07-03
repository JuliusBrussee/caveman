# caveman-stats

Real session token receipts. No AI estimation.

## What it does

Reads the current Claude Code session log directly and reports actual input/output token usage plus estimated savings versus a non-caveman baseline. Numbers come from the JSONL session log on disk — the model itself does not compute or estimate them. Output is injected by the `caveman-mode-tracker` hook, which intercepts `/caveman-stats` and returns the formatted stats as a blocked-decision reason.

It also watches the two token economies compression tools can't see:

- **Cache hit rate** — how much of your input was served from the prompt cache (billed ~0.1×) vs paid at full price. A low rate deep into a session means something invalidates the cache every turn (timestamp in a system prompt or hook, changing tool set) — the stats output warns when it spots that fingerprint, because fixing it usually saves more than any compression. See [docs/token-economy.md](../../docs/token-economy.md).
- **Turns per prompt** — assistant round trips per typed prompt. If this creeps up after tightening compression elsewhere (e.g. aggressive tool-output truncation), the model is missing context and burning extra turns — costing more than the compression saved.

Each run also writes a lifetime-savings suffix file used by the statusline badge (`⛏ 12.4k`).

## How to invoke

```
/caveman-stats
```

## Example output

```
Caveman Stats
──────────────────────────────────
Turns:    12 assistant / 5 prompts (2.4 per prompt)
──────────────────────────────────
Output tokens:         3,891
Input tokens:          2,140 uncached · 4,320 cache-write · 148,900 cache-read
Cache hit rate:        96%
──────────────────────────────────
Est. without caveman:  11,117
Est. tokens saved:     7,226 (~65%)
```

## See also

- [`SKILL.md`](./SKILL.md) — hook contract and mechanics
- [Caveman README](../../README.md) — repo overview
