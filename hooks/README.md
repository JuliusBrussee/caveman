# Caveman Hooks

These hooks are **bundled with the caveman plugin** and activate automatically when the plugin is installed. No manual setup required.

If you installed caveman standalone (without the plugin), you can use `bash hooks/install.sh` to wire them into your settings.json manually.

## What's Included

### `caveman-activate.js` — SessionStart hook

- Runs once when Claude Code starts
- Writes `full` to `~/.claude/.caveman-active` (flag file)
- Emits caveman rules as hidden SessionStart context
- Detects missing statusline config and emits setup nudge (Claude will offer to help)

### `caveman-mode-tracker.js` — UserPromptSubmit hook

- Fires on every user prompt
- Detects `/caveman` commands plus natural-language activation/deactivation phrases like `talk like caveman`, `caveman mode`, `less tokens please`, `stop caveman`, and `normal mode`
- Writes the active mode to the flag file when caveman is activated or switched
- Emits structured `hookSpecificOutput` JSON with per-turn caveman reinforcement while standard caveman modes are active
- Supports: `full`, `lite`, `ultra`, `wenyan`, `wenyan-lite`, `wenyan-ultra`, `commit`, `review`, `compress`

### `caveman-statusline.sh` / `caveman-statusline.ps1` — Statusline badge script

- Reads `~/.claude/.caveman-active` and outputs a colored badge
- Shows `[CAVEMAN]`, `[CAVEMAN:ULTRA]`, `[CAVEMAN:WENYAN]`, etc.

## Statusline Badge

The statusline badge shows which caveman mode is active directly in your Claude Code status bar.

**Plugin users:** If you do not already have a `statusLine` configured, Claude will detect that on your first session after install and offer to set it up for you. Accept and you're done.

If you already have a custom statusline, caveman does not overwrite it and Claude stays quiet. Add the badge snippet to your existing script instead.

**Standalone users:** `install.sh` / `install.ps1` wires the statusline automatically if you do not already have a custom statusline. If you do, the installer leaves it alone and prints the merge note.

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

**Custom statusline:** Prefer calling the shipped `caveman-statusline.sh` / `caveman-statusline.ps1` from your existing statusline script so you inherit future fixes. If you must inline the logic, copy it from the current script in this repo or plugin install directory rather than using an older snippet.

Badge examples:
- `/caveman` → `[CAVEMAN]`
- `/caveman ultra` → `[CAVEMAN:ULTRA]`
- `/caveman wenyan` → `[CAVEMAN:WENYAN]`
- `/caveman-commit` → `[CAVEMAN:COMMIT]`
- `/caveman-review` → `[CAVEMAN:REVIEW]`

## How It Works

```
SessionStart hook ──writes "full"──▶ ~/.claude/.caveman-active ◀──writes mode── UserPromptSubmit hook
                                              │
                                           reads
                                              ▼
                                     Statusline script
                                    [CAVEMAN:ULTRA] │ ...
```

SessionStart stdout is injected as hidden system context — Claude sees it, users don't. The statusline runs as a separate process. The flag file is the bridge.

## Uninstall

If installed via plugin: disable the plugin — hooks deactivate automatically.

If installed via `install.sh`:
```bash
bash hooks/uninstall.sh
```

Or manually:
1. Remove `~/.claude/hooks/caveman-activate.js`, `~/.claude/hooks/caveman-mode-tracker.js`, and the matching statusline script (`caveman-statusline.sh` on macOS/Linux or `caveman-statusline.ps1` on Windows)
2. Remove the SessionStart, UserPromptSubmit, and statusLine entries from `~/.claude/settings.json`
3. Delete `~/.claude/.caveman-active`
