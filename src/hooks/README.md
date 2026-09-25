# Caveman Hooks

These hooks are **bundled with the caveman plugin** and activate automatically when the plugin is installed. No manual setup required.

If you installed caveman standalone (without the plugin), the unified Node installer at `bin/install.js` wires them into your `settings.json` for you — run `node bin/install.js --only claude` from a clone, or `npx -y github:JuliusBrussee/caveman -- --only claude` for the curl-pipe path.

## What's Included

### Where the mode lives

The mode is **per session**. Each Claude Code window stores its own mode in
`$CLAUDE_CONFIG_DIR/.caveman-sessions/<session_id>.mode` (default
`~/.claude/.caveman-sessions/`), keyed by the `session_id` that Claude Code puts
in every hook payload and in the statusline's stdin JSON.

`$CLAUDE_CONFIG_DIR/.caveman-active` still exists as a **last-write-wins
mirror** of whichever session wrote most recently. It is kept so `cat` still
answers "is caveman on", and so third-party statusline snippets keep working.

Two things worth knowing about the mirror:

- It **never** contains the literal string `off`. Deactivation deletes it, just
  as before. `off` is a valid mode name, so an older hook or statusline reading
  `off` from that path would treat it as an active mode and render
  `[CAVEMAN:OFF]` or inject "CAVEMAN MODE ACTIVE (off)".
- With two windows open it shows the other window's mode half the time. Read the
  per-session file if you need the truth for a specific window.

Readers accept both spellings of "off": a missing file (the old meaning) and a
literal `off` in a session file (the new, durable one).

### `caveman-activate.js` — SessionStart hook

- Runs on every SessionStart — `source` is `startup`, `resume`, `clear`, `compact` or `fork`
- Resolves this session's mode and persists it via the symlink-safe `safeWriteFlag` helper
- Emits caveman rules as hidden SessionStart context
- Sweeps session files older than 14 days (`CAVEMAN_SESSION_TTL_MS` overrides), on new sessions only
- Detects missing statusline config and emits setup nudge (Claude will offer to help)

**Why `source` matters.** The hook is registered with no matcher, so it fires
for every source — deliberately, because compaction is what prunes the ruleset
out of context and lets the model drift back to verbose prose, so the rules must
be re-injected afterwards. What it must *not* do on a continuation (`compact`,
`resume`, `fork`) is re-derive the configured default and overwrite the
session's mode. Doing that is how an explicit "stop caveman" used to get
silently undone by the next auto-compaction. `clear` counts as a fresh start,
since it is an explicit user reset.

### `caveman-mode-tracker.js` — UserPromptSubmit hook

- Fires on every user prompt, checks for `/caveman` commands and natural-language activation/deactivation phrases ("talk like caveman", "stop caveman", "normal mode")
- Writes the active mode for **this session** when a caveman command is detected; on deactivation it stores a durable `off` and clears the legacy mirror
- Emits a small per-turn reinforcement reminder when the session's mode is a non-independent one (`lite`/`full`/`ultra`/`wenyan*`)
- Remembers the displaced prose mode per session, so two windows each running `/caveman-commit` return to their own level
- Supports: `lite`, `full`, `ultra`, `wenyan`, `wenyan-lite`, `wenyan-full`, `wenyan-ultra`, `commit`, `review`, `compress`

### `caveman-statusline.sh` / `caveman-statusline.ps1` — Statusline badge script

- Reads the session JSON Claude Code sends on stdin, takes `session_id`, and renders **that window's** mode; falls back to the legacy mirror when there is no usable id
- Shows `[CAVEMAN]`, `[CAVEMAN:ULTRA]`, `[CAVEMAN:WENYAN]`, etc. A deactivated session renders nothing at all — never `[CAVEMAN:OFF]`
- Never blocks: an interactive terminal is not read from, and the stdin read has a 1s ceiling (integer, because macOS ships bash 3.2 and it rejects fractional `read -t`)
- Appends the lifetime savings suffix `⛏ 12.4k` from `$CLAUDE_CONFIG_DIR/.caveman-statusline-suffix` (written by `caveman-stats.js` on each `/caveman-stats` run; absent until the first run, so fresh installs render no fake number). Opt out with `CAVEMAN_STATUSLINE_SAVINGS=0`.

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

**Custom statusline:** If you already have a statusline script, add this snippet
to it. It reads the session id from the JSON Claude Code gives your script on
stdin, so the badge tracks the window it belongs to. If your script already
consumed stdin, pass what you read in as `$caveman_payload` instead of
re-reading it — stdin can only be drained once.

```bash
caveman_cfg="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
caveman_payload="${caveman_payload:-}"
if [ -z "$caveman_payload" ] && [ ! -t 0 ]; then
  IFS= read -r -d '' -t 1 caveman_payload
fi
caveman_sid=$(printf '%s' "$caveman_payload" \
  | grep -o '"session_id"[[:space:]]*:[[:space:]]*"[^"]*"' \
  | head -1 | sed -e 's/.*:[[:space:]]*"//' -e 's/"$//')
case "$caveman_sid" in ''|*[!A-Za-z0-9_-]*) caveman_sid="" ;; esac

caveman_flag="$caveman_cfg/.caveman-active"
if [ -n "$caveman_sid" ] && [ -f "$caveman_cfg/.caveman-sessions/$caveman_sid.mode" ]; then
  caveman_flag="$caveman_cfg/.caveman-sessions/$caveman_sid.mode"
fi

caveman_text=""
if [ -f "$caveman_flag" ]; then
  caveman_mode=$(cat "$caveman_flag" 2>/dev/null)
  if [ "$caveman_mode" = "off" ]; then
    caveman_text=""                       # deactivated — render nothing
  elif [ "$caveman_mode" = "full" ] || [ -z "$caveman_mode" ]; then
    caveman_text=$'\033[38;5;172m[CAVEMAN]\033[0m'
  else
    caveman_suffix=$(echo "$caveman_mode" | tr '[:lower:]' '[:upper:]')
    caveman_text=$'\033[38;5;172m[CAVEMAN:'"${caveman_suffix}"$']\033[0m'
  fi
fi
```

The older one-file version of this snippet still works — it just shows whichever
window wrote last, and it never sees a literal `off` because the legacy mirror
is deleted on deactivation rather than set to `off`.

Badge examples:
- `/caveman` → `[CAVEMAN]`
- `/caveman ultra` → `[CAVEMAN:ULTRA]`
- `/caveman wenyan` → `[CAVEMAN:WENYAN]`
- `/caveman-commit` → `[CAVEMAN:COMMIT]`
- `/caveman-review` → `[CAVEMAN:REVIEW]`

## How It Works

```
SessionStart hook ──┐                                        ┌── UserPromptSubmit hook
  (session_id,      │                                        │     (session_id, prompt)
   source)          ▼                                        ▼
        $CLAUDE_CONFIG_DIR/.caveman-sessions/<session_id>.mode
                             │             │
                          mirrors       reads
                             ▼             ▼
              .caveman-active      Statusline script  ◀── session JSON on stdin
           (last-write-wins,        [CAVEMAN:ULTRA]
            compat only)
```

SessionStart stdout is injected as hidden system context — Claude sees it, users
don't. The statusline runs as a separate process. All three surfaces get
`session_id` from Claude Code, which is what lets each window keep its own mode.

Every path here honors `CLAUDE_CONFIG_DIR`. All state writes go through
`safeWriteFlag()` (symlink-refusing, atomic, `0600`), and every read is
whitelist-validated — a session id is never interpolated into a path without
passing `^[A-Za-z0-9_-]{1,128}# Caveman Hooks

These hooks are **bundled with the caveman plugin** and activate automatically when the plugin is installed. No manual setup required.

If you installed caveman standalone (without the plugin), the unified Node installer at `bin/install.js` wires them into your `settings.json` for you — run `node bin/install.js --only claude` from a clone, or `npx -y github:JuliusBrussee/caveman -- --only claude` for the curl-pipe path.

## What's Included

### Where the mode lives

The mode is **per session**. Each Claude Code window stores its own mode in
`$CLAUDE_CONFIG_DIR/.caveman-sessions/<session_id>.mode` (default
`~/.claude/.caveman-sessions/`), keyed by the `session_id` that Claude Code puts
in every hook payload and in the statusline's stdin JSON.

`$CLAUDE_CONFIG_DIR/.caveman-active` still exists as a **last-write-wins
mirror** of whichever session wrote most recently. It is kept so `cat` still
answers "is caveman on", and so third-party statusline snippets keep working.

Two things worth knowing about the mirror:

- It **never** contains the literal string `off`. Deactivation deletes it, just
  as before. `off` is a valid mode name, so an older hook or statusline reading
  `off` from that path would treat it as an active mode and render
  `[CAVEMAN:OFF]` or inject "CAVEMAN MODE ACTIVE (off)".
- With two windows open it shows the other window's mode half the time. Read the
  per-session file if you need the truth for a specific window.

Readers accept both spellings of "off": a missing file (the old meaning) and a
literal `off` in a session file (the new, durable one).

### `caveman-activate.js` — SessionStart hook

- Runs on every SessionStart — `source` is `startup`, `resume`, `clear`, `compact` or `fork`
- Resolves this session's mode and persists it via the symlink-safe `safeWriteFlag` helper
- Emits caveman rules as hidden SessionStart context
- Sweeps session files older than 14 days (`CAVEMAN_SESSION_TTL_MS` overrides), on new sessions only
- Detects missing statusline config and emits setup nudge (Claude will offer to help)

**Why `source` matters.** The hook is registered with no matcher, so it fires
for every source — deliberately, because compaction is what prunes the ruleset
out of context and lets the model drift back to verbose prose, so the rules must
be re-injected afterwards. What it must *not* do on a continuation (`compact`,
`resume`, `fork`) is re-derive the configured default and overwrite the
session's mode. Doing that is how an explicit "stop caveman" used to get
silently undone by the next auto-compaction. `clear` counts as a fresh start,
since it is an explicit user reset.

### `caveman-mode-tracker.js` — UserPromptSubmit hook

- Fires on every user prompt, checks for `/caveman` commands and natural-language activation/deactivation phrases ("talk like caveman", "stop caveman", "normal mode")
- Writes the active mode for **this session** when a caveman command is detected; on deactivation it stores a durable `off` and clears the legacy mirror
- Emits a small per-turn reinforcement reminder when the session's mode is a non-independent one (`lite`/`full`/`ultra`/`wenyan*`)
- Remembers the displaced prose mode per session, so two windows each running `/caveman-commit` return to their own level
- Supports: `lite`, `full`, `ultra`, `wenyan`, `wenyan-lite`, `wenyan-full`, `wenyan-ultra`, `commit`, `review`, `compress`

### `caveman-statusline.sh` / `caveman-statusline.ps1` — Statusline badge script

- Reads the session JSON Claude Code sends on stdin, takes `session_id`, and renders **that window's** mode; falls back to the legacy mirror when there is no usable id
- Shows `[CAVEMAN]`, `[CAVEMAN:ULTRA]`, `[CAVEMAN:WENYAN]`, etc. A deactivated session renders nothing at all — never `[CAVEMAN:OFF]`
- Never blocks: an interactive terminal is not read from, and the stdin read has a 1s ceiling (integer, because macOS ships bash 3.2 and it rejects fractional `read -t`)
- Appends the lifetime savings suffix `⛏ 12.4k` from `$CLAUDE_CONFIG_DIR/.caveman-statusline-suffix` (written by `caveman-stats.js` on each `/caveman-stats` run; absent until the first run, so fresh installs render no fake number). Opt out with `CAVEMAN_STATUSLINE_SAVINGS=0`.

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

**Custom statusline:** If you already have a statusline script, add this snippet
to it. It reads the session id from the JSON Claude Code gives your script on
stdin, so the badge tracks the window it belongs to. If your script already
consumed stdin, pass what you read in as `$caveman_payload` instead of
re-reading it — stdin can only be drained once.

```bash
caveman_cfg="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
caveman_payload="${caveman_payload:-}"
if [ -z "$caveman_payload" ] && [ ! -t 0 ]; then
  IFS= read -r -d '' -t 1 caveman_payload
fi
caveman_sid=$(printf '%s' "$caveman_payload" \
  | grep -o '"session_id"[[:space:]]*:[[:space:]]*"[^"]*"' \
  | head -1 | sed -e 's/.*:[[:space:]]*"//' -e 's/"$//')
case "$caveman_sid" in ''|*[!A-Za-z0-9_-]*) caveman_sid="" ;; esac

caveman_flag="$caveman_cfg/.caveman-active"
if [ -n "$caveman_sid" ] && [ -f "$caveman_cfg/.caveman-sessions/$caveman_sid.mode" ]; then
  caveman_flag="$caveman_cfg/.caveman-sessions/$caveman_sid.mode"
fi

caveman_text=""
if [ -f "$caveman_flag" ]; then
  caveman_mode=$(cat "$caveman_flag" 2>/dev/null)
  if [ "$caveman_mode" = "off" ]; then
    caveman_text=""                       # deactivated — render nothing
  elif [ "$caveman_mode" = "full" ] || [ -z "$caveman_mode" ]; then
    caveman_text=$'\033[38;5;172m[CAVEMAN]\033[0m'
  else
    caveman_suffix=$(echo "$caveman_mode" | tr '[:lower:]' '[:upper:]')
    caveman_text=$'\033[38;5;172m[CAVEMAN:'"${caveman_suffix}"$']\033[0m'
  fi
fi
```

The older one-file version of this snippet still works — it just shows whichever
window wrote last, and it never sees a literal `off` because the legacy mirror
is deleted on deactivation rather than set to `off`.

Badge examples:
- `/caveman` → `[CAVEMAN]`
- `/caveman ultra` → `[CAVEMAN:ULTRA]`
- `/caveman wenyan` → `[CAVEMAN:WENYAN]`
- `/caveman-commit` → `[CAVEMAN:COMMIT]`
- `/caveman-review` → `[CAVEMAN:REVIEW]`

## How It Works

 first.

## Uninstall

If installed via plugin: disable the plugin — hooks deactivate automatically.

If installed via the standalone Node installer:
```bash
npx -y github:JuliusBrussee/caveman -- --uninstall
# or, from a clone:
node bin/install.js --uninstall
```

Or manually:
1. Remove the caveman hook files from `$CLAUDE_CONFIG_DIR/hooks/` (default `~/.claude/hooks/`): `caveman-activate.js`, `caveman-mode-tracker.js`, `caveman-parse.js`, `caveman-stats.js`, `caveman-config.js`, `cavecrew-model-overrides.js`, and `caveman-statusline.{sh,ps1}`.
2. Remove the SessionStart, UserPromptSubmit, and statusLine entries from `$CLAUDE_CONFIG_DIR/settings.json`.
3. Delete the mode state from `$CLAUDE_CONFIG_DIR`: the `.caveman-sessions/` directory, `.caveman-active`, `.caveman-active.prev`, `.caveman-mode-log.jsonl`, `.caveman-statusline-suffix`, and `.caveman-nudge-shown`.

The uninstaller does all of step 3 for you, but deliberately leaves
`.caveman-history.jsonl` alone — that is your accumulated lifetime savings
record, not caveman plumbing. Delete it by hand if you want it gone.
