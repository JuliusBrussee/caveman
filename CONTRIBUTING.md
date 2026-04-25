# Contributing

Improvements to the Layman prompt are welcome. Open a PR with before/after examples showing the change.

## How

1. Fork repo
2. Edit `skills/layman/SKILL.md` — this is the main copy you need to touch
3. Open PR with:
   - **Before:** current agent handoff
   - **After:** clearer Layman handoff
   - One sentence explaining why the change helps understanding

> **Note:** `layman/SKILL.md`, `plugins/layman/skills/layman/SKILL.md`, `.cursor/skills/layman/SKILL.md`, and `layman.skill` are auto-synced by CI after merge. Do not edit them directly.
> 
> **Note on compress skill:** If you are modifying the compress skill, edit `layman-compress/SKILL.md` or `layman-compress/scripts/`. CI will automatically sync these changes to `skills/compress/` and `plugins/layman/skills/compress/`.

Small focused change > big rewrite. Keep wording plain, useful, and accurate.

## Ideas

See [issues labeled `good first issue`](../../issues?q=label%3A%22good+first+issue%22) for starter tasks.
