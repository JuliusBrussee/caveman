---
name: caveman-stats
description: >
  Show real token usage and estimated savings for the current session.
  Reads directly from the Claude Code or Codex session log — no AI estimation.
  Triggers on /caveman-stats. Output is injected by the runtime hook;
  the model itself does not compute the numbers.
---

This skill is delivered by `hooks/caveman-stats.js` for Claude Code and `plugins/caveman/scripts/caveman-codex-hook.js` for Codex. The model does not need to do anything when this skill fires — the hook returns `decision: "block"` with the formatted stats as the reason. The user sees the numbers immediately.
