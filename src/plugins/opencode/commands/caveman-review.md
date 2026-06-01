---
description: Caveman-style code review — one-line findings with severity
---
Review the current diff (or files: $ARGUMENTS).

One line per finding. Format: `L<line>: <severity> <problem>. <fix>.`
Severity emoji: 🔴 critical · 🟡 warn · 🟢 nit. Skip non-issues.
Group by file. End with a one-line verdict.
