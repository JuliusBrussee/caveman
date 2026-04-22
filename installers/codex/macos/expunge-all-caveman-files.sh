#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "Expunge all Caveman installer-managed files..."

echo "Run skills-only uninstall..."
"${SCRIPT_DIR}/skills-only/uninstall.sh" --yes

echo "Run plugin uninstall..."
"${SCRIPT_DIR}/plugin-with-all-skills/uninstall.sh" --yes

echo
echo "Expunge complete."
echo "Restart Codex before fresh install."
