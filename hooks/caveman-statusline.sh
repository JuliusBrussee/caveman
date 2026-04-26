#!/bin/bash
# caveman — statusline badge script for Claude Code
# Reads the caveman mode flag file and outputs a colored badge.
#
# Usage in ~/.claude/settings.json:
#   "statusLine": { "type": "command", "command": "bash /path/to/caveman-statusline.sh" }
#
# Plugin users: Claude will offer to set this up on first session.
# Standalone users: install.sh wires this automatically.

CLAUDE_DIR="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
GLOBAL_FLAG="$CLAUDE_DIR/.caveman-active"

# Claude Code pipes session JSON to statusline scripts on stdin. Pull the
# session_id and prefer the per-session flag file so concurrent sessions in
# different caveman levels each see their own badge. Sanitize aggressively —
# the JSON is from Claude but anything we splice into a path needs to be safe.
SESSION_ID=""
if [ ! -t 0 ] && command -v jq >/dev/null 2>&1; then
  SESSION_ID=$(jq -r '.session_id // ""' 2>/dev/null \
    | tr -cd 'a-zA-Z0-9-' | head -c 128)
fi

FLAG=""
if [ -n "$SESSION_ID" ] && [ -f "$CLAUDE_DIR/.caveman-active-$SESSION_ID" ] \
    && [ ! -L "$CLAUDE_DIR/.caveman-active-$SESSION_ID" ]; then
  FLAG="$CLAUDE_DIR/.caveman-active-$SESSION_ID"
elif [ -f "$GLOBAL_FLAG" ] && [ ! -L "$GLOBAL_FLAG" ]; then
  FLAG="$GLOBAL_FLAG"
fi

# Refuse symlinks — a local attacker could point the flag at ~/.ssh/id_rsa and
# have the statusline render its bytes (including ANSI escape sequences) to
# the terminal every keystroke.
[ -z "$FLAG" ] && exit 0

# Hard-cap the read at 64 bytes and strip anything outside [a-z0-9-] — blocks
# terminal-escape injection and OSC hyperlink spoofing via the flag contents.
MODE=$(head -c 64 "$FLAG" 2>/dev/null | tr -d '\n\r' | tr '[:upper:]' '[:lower:]')
MODE=$(printf '%s' "$MODE" | tr -cd 'a-z0-9-')

# Whitelist. Anything else → render nothing rather than echo attacker bytes.
case "$MODE" in
  off|lite|full|ultra|wenyan-lite|wenyan|wenyan-full|wenyan-ultra|commit|review|compress) ;;
  *) exit 0 ;;
esac

if [ -z "$MODE" ] || [ "$MODE" = "full" ]; then
  printf '\033[38;5;172m[CAVEMAN]\033[0m'
else
  SUFFIX=$(printf '%s' "$MODE" | tr '[:lower:]' '[:upper:]')
  printf '\033[38;5;172m[CAVEMAN:%s]\033[0m' "$SUFFIX"
fi
