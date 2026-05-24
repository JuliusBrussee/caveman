# caveman — opencode plugin

Native opencode plugin. Mirrors the Claude Code hook architecture using
opencode's `session.created` + `tui.prompt.append` lifecycle hooks.

## What this ships

| File | Role |
|---|---|
| `plugin.js` | ESM Bun module. Default-exports an opencode `Plugin` factory. |
| `package.json` | Marks the directory as ESM so Bun loads `plugin.js` correctly. |
| `commands/*.md` | Six slash-command prompt templates (`/caveman`, `/caveman-commit`, …). |

The installer (`bin/install.js --only opencode`) copies these alongside
`src/hooks/caveman-config.js` (for the symlink-safe flag-write helpers, renamed
to `caveman-config.cjs` because this directory is `"type": "module"`) into
`~/.config/opencode/plugins/caveman/` and patches `opencode.json` with a
`"plugin"` array entry.

## What it does

- `session.created` → writes the configured default mode to
  `~/.config/opencode/.caveman-active` via the same `safeWriteFlag` helper
  Claude Code uses (O_NOFOLLOW, atomic temp+rename, 0600 perms, symlink
  refusal, ownership check). Also records the session start timestamp for
  duration tracking.
- `session.deleted` → logs session duration and active mode to
  `~/.config/opencode/.caveman-history.jsonl` via the symlink-safe
  `appendFlag` helper. Accumulates a lifetime history of caveman usage.
- `tui.prompt.append` → three responsibilities:
  1. Intercepts `/caveman-stats` and injects computed lifetime stats
     (session count, total time, estimated tokens saved, most-used mode)
     directly into the prompt context so the model renders them inline.
  2. Flips the flag in response to `/caveman[ <level>]`,
     `/caveman-commit`, `/caveman-review`, `/caveman-compress`, and
     natural language ("turn on caveman", "stop caveman", "normal mode").
  3. When a non-independent mode is active, appends a one-line
     reinforcement to keep caveman in the model's attention each turn.

## What it does NOT do

- **No statusline badge.** opencode's TUI does not expose a plugin-writable
  statusline. The flag file is at `~/.config/opencode/.caveman-active` if
  you want to surface mode in your shell prompt.
- **No system-prompt injection from `session.created`.** opencode's docs
  don't expose a return shape for that. The always-on caveman ruleset comes
  from `~/.config/opencode/AGENTS.md` (also written by the installer) so
  the rules load even when the plugin runtime is broken.
- **No USD cost estimates.** Unlike the Claude Code `caveman-stats.js` hook,
  the opencode plugin's `/caveman-stats` uses a simplified duration-based
  estimate (tokens ≈ 0.75/sec) rather than parsing session JSONL. Install
  `opencode-claude-hooks` and configure a `SessionEnd` hook to bridge the
  full Claude Code stats pipeline if you need precise token-level tracking.

## Why no separate npm package

Plugin code reuses `caveman-config.js` from the main repo. Shipping as an
in-repo plugin avoids a second release cadence and a name collision with
the existing third-party `opencode-caveman` npm package.
