---
name: caveman-stats
description: >
  Real token usage and estimated savings for the session, read from the
  Claude Code session log (hook injects the numbers, no AI estimation).
  Triggers on /caveman-stats.
---

This skill is delivered by `hooks/caveman-stats.js` (read by `hooks/caveman-mode-tracker.js` on `/caveman-stats`). The model does not need to do anything when this skill fires — the hook returns `decision: "block"` with the formatted stats as the reason. The user sees the numbers immediately.
