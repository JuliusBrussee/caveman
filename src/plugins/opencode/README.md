# caveman — opencode plugin

Native opencode plugin. One default export serves both plugin APIs:

- **V1** (`server()`) — the `event` + `chat.message` +
  `experimental.chat.system.transform` hooks.
- **V2** (`setup(ctx)`) — `ctx.event.subscribe()` +
  `ctx.session.hook('prompt'|'context'|'compaction'|'generate'|'title')`.

`setup()` bails when the context is a V1 input (no `ctx.session`); a host that
calls both entrypoints (some V1 builds do) still uses only `server()`.

## What this ships

| File | Role |
|---|---|
| `plugin.js` | ESM module. Default-exports `{ id, setup, server }`. |
| `package.json` | Marks the directory as ESM so the loader reads `plugin.js` correctly. |
| `commands/*.md` | Slash-command prompt templates (`/caveman`, `/ultracave`, `/megacave`, `/caveman-commit`, …). |

The installer (`bin/install.js --only opencode`) copies these alongside
`src/hooks/caveman-config.js` (for the symlink-safe flag-write helpers, renamed
to `caveman-config.cjs` because this directory is `"type": "module"`) into
`~/.config/opencode/plugins/caveman/`. It also writes a **flat shim**
`~/.config/opencode/plugins/caveman.js` that re-exports the real plugin, because
opencode **V2 only discovers local plugins as flat files** under `plugins/`
(`plugins/*.{js,ts}`), not subdirectories.

Config wiring is version-gated:

- **V1** — a `"plugin"` entry pointing at the flat shim (deduped against
  auto-discovery on builds that also scan).
- **V2** — no config entry; the flat shim is auto-discovered. (V2 renames the
  key to `"plugins"` and rejects a file-path entry outright:
  `configured plugin path must be a directory`.)

If a V1 install's config is used by a V2 binary before re-running the installer,
the stale `"plugin"` entry emits that warning but the shim still loads via
auto-discovery; re-run the installer on V2 to clear it.

## What it does

- Writes the configured default mode to `~/.config/opencode/.caveman-active` on
  plugin load and on `session.created`, via the same `safeWriteFlag` helper
  Claude Code uses (O_NOFOLLOW, atomic temp+rename, 0600 perms, symlink
  refusal, ownership check).
- Flips the flag in response to `/caveman`, `/ultracave`, `/megacave`,
  `/caveman off`, `/caveman-commit`, `/caveman-review`, `/caveman-compress`, and
  natural language ("turn on caveman", "stop caveman", "normal mode"). On V2 the
  `prompt` hook receives the raw (unexpanded) command text and parses it
  directly.
- When a non-independent mode is active, appends a one-line reinforcement to
  keep caveman in the model's attention each turn — the V1 system transform
  (`string[]`) or the V2 `event.system` (`SystemPart[]`), whichever the host
  provides. `/caveman status` rewrites the prompt to report the active flag.

## What it does NOT do

- **No statusline badge.** opencode's TUI does not expose a plugin-writable
  statusline. The flag file is at `~/.config/opencode/.caveman-active` if
  you want to surface mode in your shell prompt.
- **No system-prompt injection from `session.created`.** The always-on caveman
  ruleset comes from `~/.config/opencode/AGENTS.md` (also written by the
  installer) so the rules load even when the plugin runtime is broken.

## Why no separate npm package

Plugin code reuses `caveman-config.js` from the main repo. Shipping as an
in-repo plugin avoids a second release cadence and a name collision with
the existing third-party `opencode-caveman` npm package.
