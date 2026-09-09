---
name: caveman-stats
description: >
  Show real token usage and estimated savings for the current session, read
  from the session log. Trigger: /caveman-stats.
---

This skill is delivered by the `caveman-stats.js` hook script — repo source `src/hooks/caveman-stats.js`, installed alongside the other hooks, where `src/hooks/caveman-mode-tracker.js` resolves it next to itself and runs it on `/caveman-stats`. The hook does not block the prompt: it returns the formatted stats in `hookSpecificOutput.additionalContext` together with an instruction to print that block verbatim inside a fenced code block and say nothing else. Do exactly that. The numbers are read from the session log on disk, so never recompute, re-round, or re-estimate them.

Output also includes `Est. rule overhead` and `Est. net` lines wherever a savings estimate exists with a known turn count. Rule overhead is the estimated per-turn INPUT-token cost of the injected caveman rules (default 1,250 tokens/turn, override with `CAVEMAN_RULE_OVERHEAD_TOKENS`) times the turn count. Net is savings minus that overhead — when negative, the output says so plainly and suggests turning caveman off for that workload, rather than hiding the net-negative regime behind a gross-savings number (see `docs/HONEST-NUMBERS.md`).
