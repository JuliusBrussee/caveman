# Caveman Hooks

These hooks are **bundled with the caveman plugin** and activate automatically when the plugin is installed. No manual setup required.

If you installed caveman standalone (without the plugin), the unified Node installer at `installer/install.js` wires them into your `settings.json` for you — run `node installer/install.js --only claude` from a clone, or `npx -y github:JuliusBrussee/caveman -- --only claude` for the curl-pipe path.

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
- Emits the active mode's skill (`skills/caveman`, `skills/ultracave` or `skills/megacave`) as hidden SessionStart context
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

**Headless sessions start off.** `claude -p` and Agent SDK sessions
(`CLAUDE_CODE_ENTRYPOINT=sdk-cli`, `sdk-ts`, `sdk-py`) are often tools that
probe Claude and parse the reply, so they start under the `manual` policy:
nothing injected until an explicit `/caveman`. Interactive surfaces (terminal,
VS Code, desktop) are unaffected. Set `CAVEMAN_DEFAULT_MODE=<mode>` in the
environment to opt a headless run back in.

### `caveman-activate.js --subagent` — SubagentStart hook

- SessionStart context reaches only the main conversation, so subagents never saw caveman. This hook hands each new subagent **this session's** active skill
- Injects nothing once the session has stored `off`, so "stop caveman" never leaks into subagents, even if another window turns caveman on
- A session with no stored mode at all (its SessionStart hook never ran or failed) falls back to the shared mirror, the same as the per-turn reminder its main conversation gets
- Skips the cavecrew agents (they already talk ultracave), one-shot modes (`commit`/`review`/`compress`), and projects whose repo config says `defaultMode: "off"`
- Read-only: it never writes mode state, logs, or marker files

### `caveman-mode-tracker.js` — UserPromptSubmit hook

- Fires on every user prompt, checks for `/caveman`, `/ultracave`, `/megacave` commands and natural-language activation/deactivation phrases ("talk like caveman", "stop caveman", "normal mode")
- Writes the active mode for **this session** when a caveman command is detected; on deactivation it stores a durable `off` and clears the legacy mirror
- Emits a small per-turn reinforcement reminder when the session's mode is a prose one (`caveman`/`ultracave`/`megacave`), built from that skill's first line
- Remembers the displaced prose mode per session, so two windows each running `/caveman-commit` return to their own mode
- Stores: `caveman`, `ultracave`, `megacave`, `commit`, `review`, `compress`, `off`. Older level names still read correctly: `lite`/`full` → `caveman`, `ultra` → `ultracave`, `wenyan*` → `megacave`

### `caveman-statusline.sh` / `caveman-statusline.ps1` — Statusline badge script

- Reads the session JSON Claude Code sends on stdin, takes `session_id`, and renders **that window's** mode; falls back to the legacy mirror when there is no usable id
- Shows `[CAVEMAN]`, `[ULTRACAVE]`, `[MEGACAVE]`, `[CAVEMAN:COMMIT]`, etc. A deactivated session renders nothing at all — never `[CAVEMAN:OFF]`
- Never blocks: an interactive terminal is not read from, and the stdin read has a 1s ceiling (integer, because macOS ships bash 3.2 and it rejects fractional `read -t`)
- Shows mode only. The old `⛏` savings suffix is gone: `.caveman-statusline-suffix` is ignored, because a transcript cannot show what caveman saved

### `caveman-stats.js --record` — SessionEnd hook

- Runs when Claude Code ends a session
- Reads `session_id` and `transcript_path` from the hook payload on stdin (first complete JSON object, 2s watchdog) and appends one snapshot to `$CLAUDE_CONFIG_DIR/.caveman-history.jsonl`: recorded output and cache-read tokens, turns, and per-mode attribution. No savings figures
- Writes no stdout and always exits 0, so it never interrupts shutdown
- Duplicate snapshots are safe: lifetime views (`--all`, `--since`) count only the newest row per `session_id`

## Statusline Badge

The statusline badge shows which caveman mode is active directly in your Claude Code status bar.

**Plugin users:** If you do not already have a `statusLine` configured, Claude will detect that on your first session after install and offer to set it up for you. Accept and you're done.

If you already have a custom statusline, caveman does not overwrite it and Claude stays quiet. Add the badge snippet to your existing script instead.

**Standalone users:** the unified installer (`installer/install.js`, invoked by the `install.sh` / `install.ps1` shims at the repo root) wires the statusline automatically if you do not already have a custom statusline. If you do, the installer leaves it alone and prints the merge note.

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
  case "$caveman_mode" in
    caveman|lite|full) caveman_badge="CAVEMAN" ;;
    ultracave|ultra) caveman_badge="ULTRACAVE" ;;
    megacave|wenyan*) caveman_badge="MEGACAVE" ;;
    commit|review|compress) caveman_badge="CAVEMAN:$(echo "$caveman_mode" | tr '[:lower:]' '[:upper:]')" ;;
    *) caveman_badge="" ;;                # off or unknown — render nothing
  esac
  [ -n "$caveman_badge" ] && caveman_text=$'\033[38;5;172m['"${caveman_badge}"$']\033[0m'
fi
```

The older one-file version of this snippet reads the legacy mirror, so it shows
whichever window wrote last and renders the new ids as `[CAVEMAN:CAVEMAN]` or
`[CAVEMAN:ULTRACAVE]`; re-run the installer to pick up the badge map above. It
never sees a literal `off` because the legacy mirror is deleted on deactivation.

Badge examples:
- `/caveman` → `[CAVEMAN]`
- `/ultracave` → `[ULTRACAVE]`
- `/megacave` → `[MEGACAVE]`
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
           (last-write-wins,        [CAVEMAN] / [ULTRACAVE] / [MEGACAVE]
            compat only)

SessionEnd hook ──(session_id, transcript_path)──▶ caveman-stats.js --record
                                                     ──appends──▶ .caveman-history.jsonl
```

SessionStart stdout is injected as hidden system context — Claude sees it, users
don't. The statusline runs as a separate process. All three surfaces get
`session_id` from Claude Code, which is what lets each window keep its own mode.

Every path here honors `CLAUDE_CONFIG_DIR`. All state writes go through
`safeWriteFlag()` (symlink-refusing, atomic, `0600`), and every read is
whitelist-validated — a session id is never interpolated into a path without
passing `^[A-Za-z0-9_-]{1,128}$` first.

## Uninstall

If installed via plugin: disable the plugin — hooks deactivate automatically.

If installed via the standalone Node installer:
```bash
npx -y github:JuliusBrussee/caveman -- --uninstall
# or, from a clone:
node installer/install.js --uninstall
```

Or manually:
1. Remove the caveman hook files from `$CLAUDE_CONFIG_DIR/hooks/` (default `~/.claude/hooks/`): `caveman-activate.js`, `caveman-mode-tracker.js`, `caveman-parse.js`, `caveman-stats.js`, `caveman-config.js`, `cavecrew-model-overrides.js`, and `caveman-statusline.{sh,ps1}`.
2. Remove the SessionStart, SubagentStart, UserPromptSubmit, SessionEnd, and statusLine entries from `$CLAUDE_CONFIG_DIR/settings.json`.
3. Delete the mode state from `$CLAUDE_CONFIG_DIR`: the `.caveman-sessions/` directory, `.caveman-active`, `.caveman-active.prev`, `.caveman-mode-log.jsonl`, `.caveman-statusline-suffix`, and `.caveman-nudge-shown`.

The uninstaller does all of step 3 for you, but deliberately leaves
`.caveman-history.jsonl` alone — that is your accumulated lifetime savings
record, not caveman plumbing. Delete it by hand if you want it gone.
