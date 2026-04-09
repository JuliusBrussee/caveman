#!/bin/zsh
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
INSTALL_SH="$SCRIPT_DIR/install.sh"

if [[ ! -f "$INSTALL_SH" ]]; then
  echo "Missing installer script: $INSTALL_SH"
  exit 1
fi

PROJECT_ROOT="${1:-}"

if [[ -z "$PROJECT_ROOT" ]]; then
  # Finder double-click flow: ask user to pick target project directory.
  PROJECT_ROOT="$(/usr/bin/osascript -e 'POSIX path of (choose folder with prompt "Select the project folder to install the Caveman Codex plugin into:")' 2>/dev/null || true)"
fi

if [[ -z "$PROJECT_ROOT" ]]; then
  echo "No project selected. Exiting."
  exit 1
fi

echo "Project: $PROJECT_ROOT"
echo ""

"$INSTALL_SH" --project "$PROJECT_ROOT"

echo ""
echo "Done."
echo "Next: restart Codex Desktop, then install \"caveman\" from the Plugins UI."

if [[ -t 0 ]]; then
  echo ""
  read -r -n 1 -p "Press any key to close…" || true
  echo ""
fi
