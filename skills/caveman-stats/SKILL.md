---
name: caveman-stats
description: >
  Show real token usage and estimated savings for the current session.
  Reads directly from the current Claude Code or Codex session log — no AI estimation.
  Triggers on /caveman-stats. Output is injected by the mode-tracker hook;
  the model itself does not compute the numbers.
---

This skill is delivered by `caveman-stats.js` through the mode-tracker hook on `/caveman-stats`. The model does not need to do anything when this skill fires — the hook returns `decision: "block"` with the formatted stats as the reason. The user sees the numbers immediately.
