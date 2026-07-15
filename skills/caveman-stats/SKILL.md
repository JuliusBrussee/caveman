---
name: caveman-stats
description: >
  Show host-native token usage and Caveman-attributed output savings for the
  current session. Triggers on /caveman-stats or an explicit stats request.
---

## Claude Code

This skill is delivered by `hooks/caveman-stats.js` (read by `hooks/caveman-mode-tracker.js` on `/caveman-stats`). The model does not compute the numbers. The hook returns `decision: "block"` with the formatted stats so the user sees them immediately.

## Hermes Agent

Call the native `caveman_stats` tool. Do not calculate usage in the model and do not infer a session ID from filenames or recent sessions; Hermes supplies the active dispatch `session_id`.

Current-session model, token counters, cache counters, reasoning tokens, and stored cost come directly from Hermes `SessionDB`. Cost is shown only when Hermes stored actual or estimated cost; Caveman has no provider-specific price table.

Caveman attribution uses only output-token deltas observed while the plugin was active:

- Mode changes inside one session are attributed per turn.
- Output before plugin activation or without a known mode is reported as unknown and excluded from savings.
- Only `full` uses the measured historical ratio: Caveman output is treated as 35% of normal output, so estimated normal output is `round(full_output / 0.35)`.
- The estimate covers output tokens only; input/cache/reasoning are unchanged.
- Lifetime totals come from the bounded Caveman history under the active `HERMES_HOME`.

Present tool output verbatim or summarize it without changing labels, counters, or the estimate boundary.
