---
description: Compress a memory file (CLAUDE.md, AGENTS.md, etc.) into caveman format (~46% reduction)
---

Compress natural language files (CLAUDE.md, AGENTS.md, PLAN.md, etc.) into caveman-speak to reduce input tokens. Preserves code, URLs, paths, and technical substance. Original is backed up as `<filename>.original.md`.

Usage: `/caveman-compress <filepath>`

Example: `/caveman-compress AGENTS.md`

Calls the caveman-compress skill scripts (runs via `python3 -m scripts <absolute_filepath>` from the skill directory). Output: compressed file overwrites original; human-readable backup saved alongside.
