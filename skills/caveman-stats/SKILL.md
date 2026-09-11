---
name: caveman-stats
description: >
  Show real token usage and estimated savings for the current session, read
  from the session log. Trigger: /caveman-stats.
---

This skill is delivered by `hooks/caveman-stats.js` (read by `hooks/caveman-mode-tracker.js` on `/caveman-stats`). The model does not need to do anything when this skill fires — the hook returns `decision: "block"` with the formatted stats as the reason. The user sees the numbers immediately.

Output also includes `Est. rule overhead` and `Est. net` lines wherever a savings estimate exists with a known turn count. Rule overhead is the estimated per-turn INPUT-token cost of the injected caveman rules (default 1,250 tokens/turn, override with `CAVEMAN_RULE_OVERHEAD_TOKENS`) times the turn count. Net is savings minus that overhead — when negative, the output says so plainly and suggests turning caveman off for that workload, rather than hiding the net-negative regime behind a gross-savings number (see `docs/HONEST-NUMBERS.md`). When the model has a known price, an `Est. net (USD)` line prices that input at each turn's logged cache mix (a cache read bills ~0.1× the input rate, and input bills 1/5 of output). In long cached sessions the token net can read negative while the bill shows a saving, so the turn-it-off advice follows the USD net whenever there is one.
