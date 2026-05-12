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
FLAG="$GLOBAL_FLAG"

# Per-session flag: Claude Code pipes the hook event JSON on stdin. When
# session_id is available AND jq is present, prefer the per-session flag so
# concurrent sessions don't clobber each other's mode (issue #184). If
# anything is missing — no stdin, no jq, malformed JSON — fall back to the
# global flag, which the activate/tracker hooks also mirror on every write.
if [ -t 0 ]; then
  : # interactive shell — no JSON on stdin
else
  if command -v jq >/dev/null 2>&1; then
    SESSION_ID=$(jq -r '.session_id // empty' 2>/dev/null | tr -cd 'a-zA-Z0-9-' | head -c 64)
    if [ -n "$SESSION_ID" ]; then
      PER_SESSION="$CLAUDE_DIR/.caveman-active-$SESSION_ID"
      if [ -f "$PER_SESSION" ] && [ ! -L "$PER_SESSION" ]; then
        FLAG="$PER_SESSION"
      fi
    fi
  fi
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

if [ -z "$MODE" ] || [ "$MODE" = "full" ]; then
  printf '\033[38;5;172m[CAVEMAN]\033[0m'
else
  SUFFIX=$(printf '%s' "$MODE" | tr '[:lower:]' '[:upper:]')
  printf '\033[38;5;172m[CAVEMAN:%s]\033[0m' "$SUFFIX"
fi

# Savings suffix: on by default. Opt out via CAVEMAN_STATUSLINE_SAVINGS=0.
# Reads a pre-rendered string written by caveman-stats.js so we don't shell out
# to node on every keystroke. Refuses symlinks and strips control bytes —
# same hardening as the flag file (a local attacker could plant a file with
# ANSI escape codes otherwise). Until /caveman-stats has run at least once,
# the suffix file is absent and nothing is rendered — so the default is safe
# for fresh installs (no fake number, no crash).
if [ "${CAVEMAN_STATUSLINE_SAVINGS:-1}" != "0" ]; then
  SAVINGS_FILE="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/.caveman-statusline-suffix"
  if [ -f "$SAVINGS_FILE" ] && [ ! -L "$SAVINGS_FILE" ]; then
    SAVINGS=$(head -c 64 "$SAVINGS_FILE" 2>/dev/null | tr -d '\000-\037')
    [ -n "$SAVINGS" ] && printf ' \033[38;5;172m%s\033[0m' "$SAVINGS"
  fi
fi
