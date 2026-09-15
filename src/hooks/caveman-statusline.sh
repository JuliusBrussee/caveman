#!/bin/bash
# caveman — statusline badge script for Claude Code
# Reads the caveman mode state and outputs a colored badge.
#
# Usage in ~/.claude/settings.json:
#   "statusLine": { "type": "command", "command": "bash /path/to/caveman-statusline.sh" }
#
# Plugin users: Claude will offer to set this up on first session.
# Standalone users: install.sh wires this automatically.

CFG="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"

# Claude Code sends session JSON on stdin, including session_id. We use it to
# read THIS window's mode instead of a machine-wide flag — otherwise every
# window renders whichever mode was set last.
#
# Reading stdin must never wedge the status bar, so two guards:
#   1. `[ -t 0 ]` — a real terminal (someone running this by hand) is never
#      read from at all.
#   2. `read -t 1` — a hard ceiling if stdin is an open pipe that never closes.
#      Integer only: macOS ships bash 3.2, which rejects fractional timeouts
#      ("invalid timeout specification"). In practice Claude Code writes and
#      closes, so read returns at EOF immediately and the ceiling never fires.
# `-d ''` reads to NUL, i.e. the whole payload even if it spans lines; it exits
# nonzero at EOF, which is the normal path and not an error.
SESSION_ID=""
if [ ! -t 0 ]; then
  IFS= read -r -d '' -t 1 CAVEMAN_STDIN
  SESSION_ID=$(printf '%s' "$CAVEMAN_STDIN" \
    | grep -o '"session_id"[[:space:]]*:[[:space:]]*"[^"]*"' \
    | head -1 \
    | sed -e 's/.*:[[:space:]]*"//' -e 's/"$//')
fi

# The id becomes part of a path, so whitelist its alphabet AND cap its length —
# same rule as validateSessionId in caveman-config.js and the regex in the .ps1
# port. Anything else, including empty, falls back to the legacy flag.
case "$SESSION_ID" in
  ''|*[!A-Za-z0-9_-]*) SESSION_ID="" ;;
esac
[ "${#SESSION_ID}" -gt 128 ] && SESSION_ID=""

FLAG="$CFG/.caveman-active"
if [ -n "$SESSION_ID" ] && [ -f "$CFG/.caveman-sessions/$SESSION_ID.mode" ]; then
  FLAG="$CFG/.caveman-sessions/$SESSION_ID.mode"
fi

# Refuse symlinks — a local attacker could point the flag at ~/.ssh/id_rsa and
# have the statusline render its bytes (including ANSI escape sequences) to
# the terminal every keystroke.
[ -L "$FLAG" ] && exit 0
[ ! -f "$FLAG" ] && exit 0

# Hard-cap the read at 64 bytes and strip anything outside [a-z0-9-] — blocks
# terminal-escape injection and OSC hyperlink spoofing via the flag contents.
MODE=$(head -c 64 "$FLAG" 2>/dev/null | tr -d '\n\r' | tr '[:upper:]' '[:lower:]')
MODE=$(printf '%s' "$MODE" | tr -cd 'a-z0-9-')

# Whitelist. Anything else → render nothing rather than echo attacker bytes.
case "$MODE" in
  off|lite|full|ultra|wenyan-lite|wenyan|wenyan-full|wenyan-ultra|commit|review|compress) ;;
  *) exit 0 ;;
esac

# Durable off: caveman is deactivated for this session. Render nothing at all,
# matching what an absent flag does — never "[CAVEMAN:OFF]", which would read
# as a caveman mode rather than the absence of one.
[ "$MODE" = "off" ] && exit 0

if [ -z "$MODE" ] || [ "$MODE" = "full" ]; then
  printf '\033[38;5;172m[CAVEMAN]\033[0m'
else
  SUFFIX=$(printf '%s' "$MODE" | tr '[:lower:]' '[:upper:]')
  printf '\033[38;5;172m[CAVEMAN:%s]\033[0m' "$SUFFIX"
fi

# Historical numeric savings suffixes are not measurements. Ignore them,
# including files written by older stats scripts before this update.
exit 0
