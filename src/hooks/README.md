# Caveman Hooks

These hooks are **bundled with the caveman plugin** and activate automatically when the plugin is installed. No manual setup required.

If you installed caveman standalone (without the plugin), the unified Node installer at `cli/install.js` wires them into your `settings.json` for you — run `node cli/install.js --only claude` from a clone, or `npx -y github:JuliusBrussee/caveman -- --only claude` for the curl-pipe path.

## What's Included

Each Claude Code session gets its own mode flag,
`$CLAUDE_CONFIG_DIR/.caveman-active-<session_id>` (default
`~/.claude/.caveman-active-<session_id>`) — so toggling caveman in one
session never affects another running concurrently. A session falls back to
the shared legacy `$CLAUDE_CONFIG_DIR/.caveman-active` only when it has no
resolvable session id, or has never written its own scoped file yet.
Deactivating caveman ("off") is now represented by writing the literal
content `off` to the flag file, not by deleting it — absence means "never
touched," not "explicitly off." Run `/caveman-stats` inside a session to see
its exact resolved flag path.

### `caveman-activate.js` — SessionStart hook

- Runs once when Claude Code starts
- Writes `full` to the session's scoped flag file (falling back to the legacy global path per above) via the symlink-safe `safeWriteFlag` helper
- Emits caveman rules as hidden SessionStart context
- Detects missing statusline config and emits setup nudge (Claude will offer to help)

### `caveman-mode-tracker.js` — UserPromptSubmit hook

- Fires on every user prompt, checks for `/caveman` commands and natural-language activation/deactivation phrases ("talk like caveman", "stop caveman", "normal mode")
- Writes the active mode to the session's scoped flag file when a caveman command is detected; writes `off` content on deactivation
- Emits a small per-turn reinforcement reminder when the flag is set to a non-independent mode (`lite`/`full`/`ultra`/`wenyan*`)
- Supports: `lite`, `full`, `ultra`, `wenyan`, `wenyan-lite`, `wenyan-full`, `wenyan-ultra`, `commit`, `review`, `compress`

### `caveman-statusline.sh` / `caveman-statusline.ps1` — Statusline badge script

- Reads the session's scoped flag file (falling back to the legacy global path per above, resolved from the `session_id` Claude Code passes on stdin) and outputs a colored badge
- Shows `[CAVEMAN]`, `[CAVEMAN:ULTRA]`, `[CAVEMAN:WENYAN]`, etc. Renders nothing when the resolved mode is `off`.
- Appends the lifetime savings suffix `⛏ 12.4k` from `$CLAUDE_CONFIG_DIR/.caveman-statusline-suffix` (written by `caveman-stats.js` on each `/caveman-stats` run; absent until the first run, so fresh installs render no fake number). Opt out with `CAVEMAN_STATUSLINE_SAVINGS=0`.

## Statusline Badge

The statusline badge shows which caveman mode is active directly in your Claude Code status bar.

**Plugin users:** If you do not already have a `statusLine` configured, Claude will detect that on your first session after install and offer to set it up for you. Accept and you're done.

If you already have a custom statusline, caveman does not overwrite it and Claude stays quiet. Add the badge snippet to your existing script instead.

**Standalone users:** the unified installer (`cli/install.js`, invoked by the `install.sh` / `install.ps1` shims at the repo root) wires the statusline automatically if you do not already have a custom statusline. If you do, the installer leaves it alone and prints the merge note.

**Manual setup:** If you need to configure it yourself, add one of these to `~/.claude/settings.json`:

```json
{
  "statusLine": {
    "type": "command",
    "command": "bash /path/to/caveman-statusline.sh"
  }
}
```

```json
{
  "statusLine": {
    "type": "command",
    "command": "powershell -ExecutionPolicy Bypass -File C:\\path\\to\\caveman-statusline.ps1"
  }
}
```

Replace the path with the actual script location (e.g. `~/.claude/hooks/` for standalone installs, or the plugin install directory for plugin installs).

**Custom statusline:** If you already have a statusline script, invoke the
shipped `caveman-statusline.sh` directly and append its output, rather than
hand-rolling a second implementation of the flag-resolution/rendering logic
(that logic — per-session scoping, the legacy fallback, `off` rendering
nothing — changes over time, and a hand-duplicated snippet will silently
drift out of sync with it). Claude Code passes the same stdin JSON to every
`statusLine` command, so pipe it straight through:

```bash
caveman_text=$(printf '%s' "$STDIN_JSON" | bash "$CAVEMAN_HOOKS_DIR/caveman-statusline.sh")
```

Replace `$STDIN_JSON` with however your script already captures its own
stdin, and `$CAVEMAN_HOOKS_DIR` with the actual hooks directory (e.g.
`~/.claude/hooks` for standalone installs, or the plugin install directory
for plugin installs).

Badge examples:
- `/caveman` → `[CAVEMAN]`
- `/caveman ultra` → `[CAVEMAN:ULTRA]`
- `/caveman wenyan` → `[CAVEMAN:WENYAN]`
- `/caveman-commit` → `[CAVEMAN:COMMIT]`
- `/caveman-review` → `[CAVEMAN:REVIEW]`

## How It Works

```
SessionStart hook ──writes "full"──▶ .caveman-active-<session_id> ◀──writes mode── UserPromptSubmit hook
                                     (or legacy .caveman-active)
                                              │
                                           reads
                                              ▼
                                     Statusline script
                                    [CAVEMAN:ULTRA] │ ...
```

SessionStart stdout is injected as hidden system context — Claude sees it, users don't. The statusline runs as a separate process. The scoped flag file (one per session, falling back to the legacy global path when no session id is available) is the bridge.

## Uninstall

If installed via plugin: disable the plugin — hooks deactivate automatically.

If installed via the standalone Node installer:
```bash
npx -y github:JuliusBrussee/caveman -- --uninstall
# or, from a clone:
node cli/install.js --uninstall
```

Or manually:
1. Remove the caveman hook files from `$CLAUDE_CONFIG_DIR/hooks/` (default `~/.claude/hooks/`): `caveman-activate.js`, `caveman-mode-tracker.js`, `caveman-stats.js`, `caveman-config.js`, and `caveman-statusline.{sh,ps1}`.
2. Remove the SessionStart, UserPromptSubmit, and statusLine entries from `$CLAUDE_CONFIG_DIR/settings.json`.
3. Delete `$CLAUDE_CONFIG_DIR/.caveman-active` and every `$CLAUDE_CONFIG_DIR/.caveman-active-<session_id>` (and matching `.prev` files), plus `$CLAUDE_CONFIG_DIR/.caveman-statusline-suffix` if you ran `/caveman-stats`. `.caveman-history.jsonl` is your lifetime savings ledger — kept unless you delete it yourself.
