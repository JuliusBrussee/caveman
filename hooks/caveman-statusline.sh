#!/bin/bash
# caveman — statusline badge script for Claude Code
# Reads the caveman mode flag file and outputs a colored badge.
#
# Usage in ~/.claude/settings.json:
#   "statusLine": { "type": "command", "command": "bash /path/to/caveman-statusline.sh" }
#
# Plugin users: Claude will offer to set this up on first session.
# Standalone users: install.sh wires this automatically.

FLAG="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/.caveman-active"

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

# Opt-in compact badge: CAVEMAN_BADGE_COMPACT=1 → [C] / [C:L] / [C:U] / etc.
# Useful for ccstatusline-style dense statuslines where [CAVEMAN] is too long.
# Default (env unset or any other value) → existing verbose output unchanged.
# Compact mode also skips the savings suffix to keep the badge tight.
if [ "${CAVEMAN_BADGE_COMPACT:-0}" = "1" ]; then
  # Whitelist above rejects empty MODE, so no empty arm needed here.
  case "$MODE" in
    full) printf '\033[38;5;172m[C]\033[0m' ;;
    lite) printf '\033[38;5;172m[C:L]\033[0m' ;;
    ultra) printf '\033[38;5;172m[C:U]\033[0m' ;;
    wenyan-lite) printf '\033[38;5;172m[C:WL]\033[0m' ;;
    wenyan|wenyan-full) printf '\033[38;5;172m[C:W]\033[0m' ;;
    wenyan-ultra) printf '\033[38;5;172m[C:WU]\033[0m' ;;
    commit) printf '\033[38;5;172m[C:CM]\033[0m' ;;
    review) printf '\033[38;5;172m[C:RV]\033[0m' ;;
    compress) printf '\033[38;5;172m[C:CP]\033[0m' ;;
  esac
  exit 0
fi

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
