# Contributing

Improvements to the SKILL.md prompt are welcome — open a PR with before/after examples showing the change.

## How

1. Fork repo
2. Edit canonical sources only:
   - `skills/caveman/`
   - `skills/compress/`
3. Open PR with:
   - **Before:** what caveman say now
   - **After:** what caveman say with change
   - One sentence why change better

> **Single source of truth:** only `skills/` is committed source for skills.
>
> Mirrors are synced by CI into:
> - `caveman/SKILL.md`
> - `plugins/caveman/skills/caveman/SKILL.md`
> - `plugins/caveman/skills/compress/`
> - `caveman.skill`

Small focused change > big rewrite. Caveman like simple.

## Ideas

See [issues labeled `good first issue`](../../issues?q=label%3A%22good+first+issue%22) for starter tasks.
