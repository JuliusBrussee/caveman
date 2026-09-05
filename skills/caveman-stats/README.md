# caveman-stats

Real session token receipts. No AI estimation.

## What it does

Reads the current Claude Code session log directly and reports actual input/output token usage. Numbers come from the JSONL session log on disk: the model itself does not compute or estimate them. Output is injected by the `caveman-mode-tracker` hook, which intercepts `/caveman-stats` and returns the formatted stats as a blocked-decision reason.

Output also includes an `Est. rule overhead` line whenever caveman was active with a known turn count. That estimates the per-turn INPUT-token cost of the rules the skill injects every turn: default 1,250 tokens/turn, override with `CAVEMAN_RULE_OVERHEAD_TOKENS` if you've measured your own setup. Background: `docs/HONEST-NUMBERS.md`.

No output-savings estimate is printed. The repository has no committed reviewed benchmark result to back a counterfactual "saved N tokens" figure, so the output points at `docs/HONEST-NUMBERS.md` instead of guessing: compare your own provider-billed totals with and without caveman. The statusline suffix that used to show a gross-savings number is written empty for the same reason.

## How to invoke

```
/caveman-stats
```

## Example output

```
Caveman Stats
──────────────────────────────────
Turns:    47
──────────────────────────────────
Output tokens:         3,891
Cache-read tokens:     12,304
──────────────────────────────────
Est. rule overhead:    58,750 (input, ~1,250/turn over 47 turns)
No output-savings estimate is published (docs/HONEST-NUMBERS.md): compare provider-billed totals with and without caveman for your own workload.
```

(Numbers above are illustrative. See `docs/HONEST-NUMBERS.md` for why an output-savings estimate is not published.)

## See also

- [`SKILL.md`](./SKILL.md) — hook contract and mechanics
- [Caveman README](../../README.md) — repo overview
