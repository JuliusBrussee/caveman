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

# Claude Code invokes statusLine commands with the same JSON-on-stdin
# contract as other hooks (e.g. session_id, cwd). No jq dependency — matches
# this script's existing zero-dependency posture. `cat` returns immediately
# on EOF (a closed/empty stdin, as in a manual/test invocation with no
# input, is not a hang).
STDIN_JSON=$(cat 2>/dev/null)
RAW_SESSION_ID=$(printf '%s' "$STDIN_JSON" | grep -o '"session_id"[[:space:]]*:[[:space:]]*"[^"]*"' | head -n 1 | sed -E 's/.*:[[:space:]]*"([^"]*)"/\1/')

# Whole-string anchored match, reject entirely on any non-match — never
# strip characters or truncate down to a valid-looking id (v1 Critical fix:
# a bare `case ... in [A-Za-z0-9_-]*)` glob only anchors the FIRST
# character, letting a path-traversal-style value through).
SESSION_ID=""
if [[ "$RAW_SESSION_ID" =~ ^[A-Za-z0-9_-]{1,128}$ ]]; then
  SESSION_ID="$RAW_SESSION_ID"
fi

# Resolve the flag path with the same ENOENT-vs-rejected fallback semantics
# as caveman-config.js's resolveFlag: no session id (or an invalid one) ->
# always the legacy path, unchanged from today. A valid session id with NO
# scoped file at all (true ENOENT) falls back to the legacy path. A valid
# session id whose scoped file EXISTS (even a dangling symlink, even
# invalid/oversized content) is fail-closed: never fall back to legacy,
# even if the scoped content turns out to be unreadable/invalid.
LEGACY_FLAG="$CLAUDE_DIR/.caveman-active"
FLAG="$LEGACY_FLAG"
SCOPED_IDENTITY=0
if [ -n "$SESSION_ID" ]; then
  SCOPED_FLAG="$CLAUDE_DIR/.caveman-active-$SESSION_ID"
  if [ -e "$SCOPED_FLAG" ] || [ -L "$SCOPED_FLAG" ]; then
    FLAG="$SCOPED_FLAG"
    SCOPED_IDENTITY=1
  fi
fi

# Refuse symlinks — a local attacker could point the flag at ~/.ssh/id_rsa and
# have the statusline render its bytes (including ANSI escape sequences) to
# the terminal every keystroke.
if [ -L "$FLAG" ] || [ ! -f "$FLAG" ]; then
  # Scoped identity + rejected/missing content -> fail closed, render
  # nothing at all, never fall back to the legacy sentinel (FLAG is
  # already the scoped path here, not the legacy one, when
  # SCOPED_IDENTITY=1).
  exit 0
fi

# Hard-cap the read at 64 bytes and strip anything outside [a-z0-9-] — blocks
# terminal-escape injection and OSC hyperlink spoofing via the flag contents.
MODE=$(head -c 64 "$FLAG" 2>/dev/null | tr -d '\n\r' | tr '[:upper:]' '[:lower:]')
MODE=$(printf '%s' "$MODE" | tr -cd 'a-z0-9-')

# Whitelist. Anything else → render nothing rather than echo attacker bytes
# (this is also the scoped-identity "rejected content" case: an invalid
# MODE here never falls back to the legacy sentinel, because FLAG is
# already the scoped path when SCOPED_IDENTITY=1).
case "$MODE" in
  off|lite|full|ultra|wenyan-lite|wenyan|wenyan-full|wenyan-ultra|commit|review|compress) ;;
  *) exit 0 ;;
esac

# A resolved mode of 'off' renders nothing at all, matching isActiveMode.
if [ "$MODE" = "off" ]; then
  exit 0
fi

if [ "$MODE" = "full" ]; then
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
  SAVINGS_FILE="$CLAUDE_DIR/.caveman-statusline-suffix"
  if [ -f "$SAVINGS_FILE" ] && [ ! -L "$SAVINGS_FILE" ]; then
    SAVINGS=$(head -c 64 "$SAVINGS_FILE" 2>/dev/null | tr -d '\000-\037')
    [ -n "$SAVINGS" ] && printf ' \033[38;5;172m%s\033[0m' "$SAVINGS"
  fi
fi

# An empty suffix file leaves the last [ -n ] test as the script's exit status
# (1), and Claude Code hides the whole status bar on non-zero exit (#711).
exit 0
