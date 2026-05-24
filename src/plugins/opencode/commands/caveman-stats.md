---
description: Show caveman lifetime token-savings stats
---
Read ~/.config/opencode/.caveman-history.jsonl (JSONL, one JSON object per session).
Compute and display as a table:
- Sessions: line count
- Total time: sum of duration fields (seconds), shown in minutes
- Est. tokens saved: sum(duration) × 0.75
- Most-used mode: mode with highest count
- Last session: most recent entry's duration (min) + mode

Return as markdown table only.
