# Caveman Hooks

These hooks are **bundled with the caveman plugin** and activate automatically when the plugin is installed. No manual setup required.

If you installed caveman standalone (without the plugin), the unified Node installer at `bin/install.js` wires them into your `settings.json` for you — run `node bin/install.js --only claude` from a clone, or `npx -y github:JuliusBrussee/caveman -- --only claude` for the curl-pipe path.

## What's Included

### `caveman-activate.js` — SessionStart hook

- Runs once when Claude Code starts
- Writes `full` to `$CLAUDE_CONFIG_DIR/.caveman-active` (default `~/.claude/.caveman-active`) via the symlink-safe `safeWriteFlag` helper
- Emits caveman rules as hidden SessionStart context
- Detects missing statusline config and emits setup nudge (Claude will offer to help)

### `caveman-mode-tracker.js` — UserPromptSubmit hook

- Fires on every user prompt, checks for `/caveman` commands and natural-language activation/deactivation phrases ("talk like caveman", "stop caveman", "normal mode")
- Writes the active mode to the flag file when a caveman command is detected; deletes it on deactivation
- Emits a small per-turn reinforcement reminder when the flag is set to a non-independent mode (`lite`/`full`/`ultra`/`wenyan*`)
- Supports: `lite`, `full`, `ultra`, `wenyan`, `wenyan-lite`, `wenyan-full`, `wenyan-ultra`, `commit`, `review`, `compress`

### `caveman-statusline.sh` / `caveman-statusline.ps1` — Statusline badge script

- Reads `$CLAUDE_CONFIG_DIR/.caveman-active` (default `~/.claude/.caveman-active`) and outputs a colored badge
- Shows `[CAVEMAN]`, `[CAVEMAN:ULTRA]`, `[CAVEMAN:WENYAN]`, etc.
- Appends the lifetime savings suffix `⛏ 12.4k` from `$CLAUDE_CONFIG_DIR/.caveman-statusline-suffix` (written by `caveman-stats.js` on each `/caveman-stats` run; absent until the first run, so fresh installs render no fake number). Opt out with `CAVEMAN_STATUSLINE_SAVINGS=0`.

### `caveman-trim-tool-result.js` — PostToolUse hook (opt-in, OFF by default)

- Trims oversized built-in tool results (`Read`/`Bash`/`Grep`/`Glob`) **before they enter context**, where they would otherwise be re-sent every later turn (input tokens dominate the weekly limit in agentic use).
- Uses the public PostToolUse `updatedToolOutput` field to replace the result the model sees.
- Lossless first: strips ANSI/terminal noise, carriage-return progress redraws, trailing whitespace, blank-line runs. Only when still huge does it keep head + tail and drop the middle, with a marker telling the model to re-run the tool for the full output.
- Deterministic + fail-open: same input → same bytes (so the cache stays warm); any error passes the original result through unchanged. JSON-ish results are never touched.
- **Not wired by the plugin** (avoids a per-tool-call hook spawn for everyone). Opt in, then enable at runtime — see "Trim oversized tool results" below.

## Statusline Badge

The statusline badge shows which caveman mode is active directly in your Claude Code status bar.

**Plugin users:** If you do not already have a `statusLine` configured, Claude will detect that on your first session after install and offer to set it up for you. Accept and you're done.

If you already have a custom statusline, caveman does not overwrite it and Claude stays quiet. Add the badge snippet to your existing script instead.

**Standalone users:** the unified installer (`bin/install.js`, invoked by the `install.sh` / `install.ps1` shims at the repo root) wires the statusline automatically if you do not already have a custom statusline. If you do, the installer leaves it alone and prints the merge note.

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

**Custom statusline:** If you already have a statusline script, add this snippet to it:

```bash
caveman_text=""
caveman_flag="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/.caveman-active"
if [ -f "$caveman_flag" ]; then
  caveman_mode=$(cat "$caveman_flag" 2>/dev/null)
  if [ "$caveman_mode" = "full" ] || [ -z "$caveman_mode" ]; then
    caveman_text=$'\033[38;5;172m[CAVEMAN]\033[0m'
  else
    caveman_suffix=$(echo "$caveman_mode" | tr '[:lower:]' '[:upper:]')
    caveman_text=$'\033[38;5;172m[CAVEMAN:'"${caveman_suffix}"$']\033[0m'
  fi
fi
```

Badge examples:
- `/caveman` → `[CAVEMAN]`
- `/caveman ultra` → `[CAVEMAN:ULTRA]`
- `/caveman wenyan` → `[CAVEMAN:WENYAN]`
- `/caveman-commit` → `[CAVEMAN:COMMIT]`
- `/caveman-review` → `[CAVEMAN:REVIEW]`

## Trim oversized tool results (opt-in)

Two steps — wire the hook, then enable it at runtime. It is wired into
`settings.json` independently of the plugin (the plugin does not register
`PostToolUse`), so it never double-fires.

**Wire it** (standalone installer):

```bash
node bin/install.js --only claude --with-trim
# or curl-pipe: npx -y github:JuliusBrussee/caveman -- --only claude --with-trim
```

**Or wire it manually** in `~/.claude/settings.json`:

```json
{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Read|Bash|Grep|Glob",
        "hooks": [
          { "type": "command", "command": "node \"/path/to/hooks/caveman-trim-tool-result.js\"", "timeout": 10 }
        ]
      }
    ]
  }
}
```

**Enable at runtime** — the hook is inert until you set:

```bash
export CAVEMAN_TRIM_TOOL_RESULTS=1
# optional: raise/lower the size at which it kicks in (chars, default 8000)
export CAVEMAN_TRIM_THRESHOLD=8000
```

Leave the variable unset to keep it fully off (the hook exits immediately).

## How It Works

```
SessionStart hook ──writes "full"──▶ $CLAUDE_CONFIG_DIR/.caveman-active ◀──writes mode── UserPromptSubmit hook
                                              │
                                           reads
                                              ▼
                                     Statusline script
                                    [CAVEMAN:ULTRA] │ ...
```

SessionStart stdout is injected as hidden system context — Claude sees it, users don't. The statusline runs as a separate process. The flag file is the bridge.

## Uninstall

If installed via plugin: disable the plugin — hooks deactivate automatically.

If installed via the standalone Node installer:
```bash
npx -y github:JuliusBrussee/caveman -- --uninstall
# or, from a clone:
node bin/install.js --uninstall
```

Or manually:
1. Remove the caveman hook files from `$CLAUDE_CONFIG_DIR/hooks/` (default `~/.claude/hooks/`): `caveman-activate.js`, `caveman-mode-tracker.js`, `caveman-stats.js`, `caveman-config.js`, `caveman-trim-tool-result.js`, and `caveman-statusline.{sh,ps1}`.
2. Remove the SessionStart, UserPromptSubmit, PostToolUse, and statusLine entries from `$CLAUDE_CONFIG_DIR/settings.json`.
3. Delete `$CLAUDE_CONFIG_DIR/.caveman-active` (and `$CLAUDE_CONFIG_DIR/.caveman-statusline-suffix` if you ran `/caveman-stats`).
