#!/usr/bin/env bash
# caveman-hermes installer — copies the caveman + cavecrew skills into a Hermes
# skills directory. Idempotent. Safe to re-run.
#
#   ./install.sh            # install both skills to ~/.hermes/skills/productivity/
#   ./install.sh --dry-run  # print what would happen, write nothing
#   HERMES_HOME=/path ./install.sh   # target a non-default profile
set -euo pipefail

DRY=0
[[ "${1:-}" == "--dry-run" ]] && DRY=1

SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/skills"
HERMES_HOME="${HERMES_HOME:-$HOME/.hermes}"
DEST_DIR="$HERMES_HOME/skills/productivity"

if [[ ! -d "$HERMES_HOME" ]]; then
  echo "error: Hermes home not found at $HERMES_HOME (set HERMES_HOME=...)" >&2
  exit 1
fi

echo "caveman-hermes installer"
echo "  source: $SRC_DIR"
echo "  dest:   $DEST_DIR"
[[ $DRY -eq 1 ]] && echo "  mode:   DRY RUN (no writes)"
echo

for skill in caveman cavecrew; do
  src="$SRC_DIR/$skill"
  dst="$DEST_DIR/$skill"
  if [[ ! -f "$src/SKILL.md" ]]; then
    echo "  skip $skill — no SKILL.md at $src" >&2
    continue
  fi
  if [[ $DRY -eq 1 ]]; then
    echo "  would install: $skill -> $dst"
  else
    mkdir -p "$dst"
    cp -f "$src/SKILL.md" "$dst/SKILL.md"
    echo "  installed: $skill -> $dst/SKILL.md"
  fi
done

echo
if [[ $DRY -eq 1 ]]; then
  echo "Dry run complete. Re-run without --dry-run to write."
else
  echo "Done. Verify with: hermes skills list | grep -E 'caveman|cavecrew'"
  echo "Use it: say \"caveman mode\" (levels: caveman lite|full|ultra). Off: \"normal mode\"."
fi
