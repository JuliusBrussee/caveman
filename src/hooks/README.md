# missionctl Hooks

These hooks are **bundled with the missionctl plugin** and activate automatically when the plugin is installed. No manual setup required.

If you installed missionctl standalone (without the plugin), the unified Node installer at `bin/install.js` wires them into your `settings.json` for you — run `node bin/install.js --only claude` from a clone, or `npx -y github:dnl-re/missionctl -- --only claude` for the curl-pipe path.

## What's Included

### `missionctl-activate.js` — SessionStart hook

- Runs once when Claude Code starts
- Writes `full` to `$CLAUDE_CONFIG_DIR/.missionctl-active` (default `~/.claude/.missionctl-active`) via the symlink-safe `safeWriteFlag` helper
- Emits missionctl rules as hidden SessionStart context
- Detects missing statusline config and emits setup nudge (Claude will offer to help)

### `missionctl-mode-tracker.js` — UserPromptSubmit hook

- Fires on every user prompt, checks for `/missionctl` commands and natural-language activation/deactivation phrases ("operational brevity mode", "stop missionctl", "normal mode")
- Writes the active mode to the flag file when a missionctl command is detected; deletes it on deactivation
- Emits a small per-turn reinforcement reminder when the flag is set to a non-independent mode (`lite`/`full`/`ultra`/`wenyan*`)
- Supports: `lite`, `full`, `ultra`, `wenyan`, `wenyan-lite`, `wenyan-full`, `wenyan-ultra`, `commit`, `review`, `compress`

### `missionctl-statusline.sh` / `missionctl-statusline.ps1` — Statusline badge script

- Reads `$CLAUDE_CONFIG_DIR/.missionctl-active` (default `~/.claude/.missionctl-active`) and outputs a colored badge
- Shows `[missionctl]`, `[missionctl:ULTRA]`, `[missionctl:WENYAN]`, etc.
- Appends the lifetime savings suffix `📡 12.4k` from `$CLAUDE_CONFIG_DIR/.missionctl-statusline-suffix` (written by `missionctl-stats.js` on each `/missionctl-stats` run; absent until the first run, so fresh installs render no fake number). Opt out with `MISSIONCTL_STATUSLINE_SAVINGS=0`.

## Statusline Badge

The statusline badge shows which missionctl mode is active directly in your Claude Code status bar.

**Plugin users:** If you do not already have a `statusLine` configured, Claude will detect that on your first session after install and offer to set it up for you. Accept and you're done.

If you already have a custom statusline, missionctl does not overwrite it and Claude stays quiet. Add the badge snippet to your existing script instead.

**Standalone users:** the unified installer (`bin/install.js`, invoked by the `install.sh` / `install.ps1` shims at the repo root) wires the statusline automatically if you do not already have a custom statusline. If you do, the installer leaves it alone and prints the merge note.

**Manual setup:** If you need to configure it yourself, add one of these to `~/.claude/settings.json`:

```json
{
  "statusLine": {
    "type": "command",
    "command": "bash /path/to/missionctl-statusline.sh"
  }
}
```

```json
{
  "statusLine": {
    "type": "command",
    "command": "powershell -ExecutionPolicy Bypass -File C:\\path\\to\\missionctl-statusline.ps1"
  }
}
```

Replace the path with the actual script location (e.g. `~/.claude/hooks/` for standalone installs, or the plugin install directory for plugin installs).

**Custom statusline:** If you already have a statusline script, add this snippet to it:

```bash
missionctl_text=""
missionctl_flag="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/.missionctl-active"
if [ -f "$missionctl_flag" ]; then
  missionctl_mode=$(cat "$missionctl_flag" 2>/dev/null)
  if [ "$missionctl_mode" = "full" ] || [ -z "$missionctl_mode" ]; then
    missionctl_text=$'\033[38;5;172m[missionctl]\033[0m'
  else
    missionctl_suffix=$(echo "$missionctl_mode" | tr '[:lower:]' '[:upper:]')
    missionctl_text=$'\033[38;5;172m[missionctl:'"${missionctl_suffix}"$']\033[0m'
  fi
fi
```

Badge examples:
- `/missionctl` → `[missionctl]`
- `/missionctl ultra` → `[missionctl:ULTRA]`
- `/missionctl wenyan` → `[missionctl:WENYAN]`
- `/missionctl-commit` → `[missionctl:COMMIT]`
- `/missionctl-review` → `[missionctl:REVIEW]`

## How It Works

```
SessionStart hook ──writes "full"──▶ $CLAUDE_CONFIG_DIR/.missionctl-active ◀──writes mode── UserPromptSubmit hook
                                              │
                                           reads
                                              ▼
                                     Statusline script
                                    [missionctl:ULTRA] │ ...
```

SessionStart stdout is injected as hidden system context — Claude sees it, users don't. The statusline runs as a separate process. The flag file is the bridge.

## Uninstall

If installed via plugin: disable the plugin — hooks deactivate automatically.

If installed via the standalone Node installer:
```bash
npx -y github:dnl-re/missionctl -- --uninstall
# or, from a clone:
node bin/install.js --uninstall
```

Or manually:
1. Remove the missionctl hook files from `$CLAUDE_CONFIG_DIR/hooks/` (default `~/.claude/hooks/`): `missionctl-activate.js`, `missionctl-mode-tracker.js`, `missionctl-stats.js`, `missionctl-config.js`, and `missionctl-statusline.{sh,ps1}`.
2. Remove the SessionStart, UserPromptSubmit, and statusLine entries from `$CLAUDE_CONFIG_DIR/settings.json`.
3. Delete `$CLAUDE_CONFIG_DIR/.missionctl-active` (and `$CLAUDE_CONFIG_DIR/.missionctl-statusline-suffix` if you ran `/missionctl-stats`).
