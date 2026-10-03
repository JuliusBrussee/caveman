---
description: Show caveman lifetime token-savings stats
---
Show caveman stats — total tokens saved, sessions, average compression ratio.

Read the lifetime history log at `~/.config/caveman/.caveman-history.jsonl`
(or wherever the caveman-stats script writes it). Output: total saved,
sessions counted, avg ratio. One short table.

When a budget is configured, also show ceiling, window, used, remaining
percent, active rung, next rung, hold on/off. Used vs ceiling only — no
new savings percentage.

opencode has no Claude JSONL. Do not invent usage numbers for the ladder.
