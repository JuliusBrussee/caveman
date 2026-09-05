---
name: caveman-stats
description: >
  Show real token usage for the current session, read from the session log.
  Trigger: /caveman-stats.
---

This skill is delivered by `hooks/caveman-stats.js` (read by `hooks/caveman-mode-tracker.js` on `/caveman-stats`). The model does not need to do anything when this skill fires — the hook returns `decision: "block"` with the formatted stats as the reason. The user sees the numbers immediately.

Output also includes an `Est. rule overhead` line whenever caveman was active with a known turn count. That is the estimated per-turn INPUT-token cost of the injected caveman rules (default 1,250 tokens/turn, override with `CAVEMAN_RULE_OVERHEAD_TOKENS`) times the turn count, a sourced figure per `docs/HONEST-NUMBERS.md`. No output-savings estimate is printed: the repository has no committed reviewed benchmark result to back one, so the output points at `docs/HONEST-NUMBERS.md` instead of guessing.
