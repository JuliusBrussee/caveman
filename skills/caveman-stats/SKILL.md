---
name: caveman-stats
description: >
  Show host-native token usage and Caveman-attributed output savings for the
  current session. Triggers on /caveman-stats or an explicit stats request.
---

## Claude Code

This skill is delivered by `hooks/caveman-stats.js` (read by `hooks/caveman-mode-tracker.js` on `/caveman-stats`). The model does not compute the numbers. The hook returns `decision: "block"` with formatted stats so user sees them immediately.

Output includes `Est. rule overhead` and `Est. net` when a savings estimate and known turn count exist. Rule overhead is estimated input-token cost of injected Caveman rules (default 1,250 tokens/turn; override `CAVEMAN_RULE_OVERHEAD_TOKENS`) times turn count. Net is savings minus overhead. Negative net must be stated plainly; see `docs/HONEST-NUMBERS.md`.

## Hermes Agent

Call native `caveman_stats` tool. Do not calculate usage in model or infer a session ID; Hermes supplies active dispatch `session_id`.

Current-session model, token/cache/reasoning counters, and stored cost come from Hermes `SessionDB`. Show cost only when Hermes stored actual or estimated cost; Caveman has no provider price table.

Caveman attribution uses output-token deltas observed while plugin active:

- Mode changes in one session attributed per turn.
- Output before activation or without known mode is unknown and excluded.
- Only `full` uses measured historical ratio: Caveman output = 35% normal output, so normal output estimate = `round(full_output / 0.35)`.
- Estimate covers output only; input/cache/reasoning unchanged.
- Lifetime totals come from bounded Caveman history under active `HERMES_HOME`.

Present native tool output verbatim or summarize without changing labels, counters, or estimate boundary.
