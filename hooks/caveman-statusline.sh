#!/bin/bash
# caveman — statusline badge script for Claude Code
# Reads the caveman mode flag file and outputs a colored badge.
#
# Usage in ~/.claude/settings.json:
#   "statusLine": { "type": "command", "command": "bash /path/to/caveman-statusline.sh" }
#
# Plugin users: Claude will offer to set this up on first session.
# Standalone users: install.sh wires this automatically.
#
# Badge examples:
#   full          → [CAVEMAN]
#   lite          → [CAVEMAN:LITE]
#   ultra         → [CAVEMAN:ULTRA]
#   full-es       → [CAVEMAN:ES]
#   lite-es       → [CAVEMAN:LITE·ES]
#   ultra-en      → [CAVEMAN:ULTRA·EN]
#   wenyan-full   → [CAVEMAN:WENYAN]

FLAG="$HOME/.claude/.caveman-active"
[ ! -f "$FLAG" ] && exit 0

MODE=$(cat "$FLAG" 2>/dev/null)
[ -z "$MODE" ] && exit 0

# Known language codes (2-letter suffix after last dash)
KNOWN_LANGS="en es fr de pt it ja zh"

# Extract lang suffix if present (e.g. "full-es" → lang="es", base="full")
LANG_CODE=""
BASE_MODE="$MODE"
LAST_PART="${MODE##*-}"
for l in $KNOWN_LANGS; do
  if [ "$LAST_PART" = "$l" ] && [ "$MODE" != "$l" ]; then
    LANG_CODE=$(echo "$l" | tr '[:lower:]' '[:upper:]')
    BASE_MODE="${MODE%-*}"
    break
  fi
done

# Build badge label from base mode
if [ "$BASE_MODE" = "full" ] || [ -z "$BASE_MODE" ]; then
  BADGE_PARTS=""
else
  BADGE_PARTS=$(echo "$BASE_MODE" | tr '[:lower:]' '[:upper:]')
fi

# Append lang if present
if [ -n "$LANG_CODE" ]; then
  if [ -n "$BADGE_PARTS" ]; then
    BADGE_PARTS="${BADGE_PARTS}·${LANG_CODE}"
  else
    BADGE_PARTS="$LANG_CODE"
  fi
fi

# Output colored badge
if [ -z "$BADGE_PARTS" ]; then
  printf '\033[38;5;172m[CAVEMAN]\033[0m'
else
  printf '\033[38;5;172m[CAVEMAN:%s]\033[0m' "$BADGE_PARTS"
fi
