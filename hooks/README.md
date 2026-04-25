# Layman Hooks

These hooks are bundled with the Layman Claude Code plugin and activate automatically when the plugin is installed.

If you installed Layman standalone, run `bash hooks/install.sh` to wire them into `settings.json`.

## What's Included

### `layman-activate.js`

- Runs once when Claude Code starts.
- Writes `summary` to `~/.claude/.layman-active` by default.
- Emits the Layman rules as hidden SessionStart context.
- Detects missing statusline config and adds a setup nudge.

### `layman-mode-tracker.js`

- Watches user prompts for `/layman`, `/layman summary`, and `/layman explain`.
- Tracks independent modes for `/layman-commit`, `/layman-review`, and `/layman:compress`.
- Removes the flag when the user says `stop layman` or `normal mode`.

### `layman-statusline.sh` / `layman-statusline.ps1`

- Reads `~/.claude/.layman-active`.
- Shows `[LAYMAN]`, `[LAYMAN:EXPLAIN]`, `[LAYMAN:COMMIT]`, etc.

## Manual Statusline Setup

```json
{
  "statusLine": {
    "type": "command",
    "command": "bash /path/to/layman-statusline.sh"
  }
}
```

```json
{
  "statusLine": {
    "type": "command",
    "command": "powershell -ExecutionPolicy Bypass -File C:\\path\\to\\layman-statusline.ps1"
  }
}
```

Custom statusline snippet:

```bash
layman_text=""
layman_flag="$HOME/.claude/.layman-active"
if [ -f "$layman_flag" ]; then
  layman_mode=$(cat "$layman_flag" 2>/dev/null)
  if [ "$layman_mode" = "summary" ] || [ -z "$layman_mode" ]; then
    layman_text=$'\033[38;5;172m[LAYMAN]\033[0m'
  else
    layman_suffix=$(echo "$layman_mode" | tr '[:lower:]' '[:upper:]')
    layman_text=$'\033[38;5;172m[LAYMAN:'"${layman_suffix}"$']\033[0m'
  fi
fi
```

## How It Works

```txt
SessionStart hook --writes "summary"--> ~/.claude/.layman-active <--writes mode-- UserPromptSubmit hook
                                                    |
                                                  reads
                                                    v
                                             Statusline script
                                            [LAYMAN:EXPLAIN]
```

SessionStart stdout is injected as hidden system context. The statusline runs separately. The flag file is the bridge.

## Uninstall

If installed via plugin, disable the plugin.

If installed via `install.sh`:

```bash
bash hooks/uninstall.sh
```

Manual uninstall:

1. Remove `~/.claude/hooks/layman-activate.js`, `~/.claude/hooks/layman-mode-tracker.js`, and the matching statusline script.
2. Remove the SessionStart, UserPromptSubmit, and statusLine entries from `~/.claude/settings.json`.
3. Delete `~/.claude/.layman-active`.
