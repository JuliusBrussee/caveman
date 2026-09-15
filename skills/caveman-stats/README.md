# caveman-stats

Recorded session usage. Savings stay unknown without a measured comparison.

## What it does

Reads the current Claude Code session log and reports output tokens, cache-read input tokens, and response counts. When a mode-transition log is available, it separates output by the mode active for each response. Missing mode attribution stays unknown. Numbers come from the JSONL session log on disk — the model itself does not compute or estimate them. Output is injected by the `caveman-mode-tracker` hook, which intercepts `/caveman-stats`, runs the stats script, and hands the formatted block back as `additionalContext` with an instruction to print it verbatim.

The transcript does not contain the same session without Caveman, so the report cannot calculate tokens saved, a reduction percentage, dollars saved, rule overhead, or a net result. Earlier releases applied a fixed ratio without a committed reviewed benchmark. Those estimates no longer appear in session reports, lifetime totals, shared summaries, or the statusline.

Recorded history stays on disk. New snapshots contain observed usage and mode attribution; historical estimated-savings fields are ignored. Original/current memory-file pairs show byte-size differences separately, without treating them as provider savings.

## How to invoke

```
/caveman-stats
/caveman-stats --all
/caveman-stats --since 7d
/caveman-stats --share
```

Claude Code's hook runs `src/hooks/caveman-stats.js` and tells the model to relay the generated report verbatim. The model does not estimate the numbers.

## Example output

An illustrative transcript with one response and 1,000 output tokens produces:

```
Turns:    1
──────────────────────────────────
Output tokens:         1,000
Cache-read tokens:     2,400
──────────────────────────────────
Mode: full (current mode; no transition log)
Savings: unknown — no measured comparison for this session.
```

## See also

- [`SKILL.md`](./SKILL.md) — hook contract
- [Honest Numbers](../../docs/HONEST-NUMBERS.md) — measurement limits
- [Caveman README](../../README.md) — repo overview

Gemini CLI uses its native `/stats model` or `/stats session` report. The Caveman
command points there; it cannot access Gemini's live metrics or aggregate Claude
transcripts as Gemini usage. [Gemini command reference](https://geminicli.com/docs/reference/commands/).
