#!/usr/bin/env bash
# caveman — installer shim.
#
# Thin wrapper around bin/install.js (the unified Node installer). Every flag
# you'd pass to bin/install.js can be passed here; we just forward them.
#
# One-line install:
#   curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.sh | bash
#   curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/main/install.sh | bash -s -- --all
#
# Local clone:
#   bash install.sh [flags]
#
# Why a Node installer? install.sh + install.ps1 used to be parallel sources
# of truth and constantly drifted (issue #249, etc.). One Node script works
# everywhere without bash/PowerShell quoting bugs.

set -euo pipefail

REPO="JuliusBrussee/caveman"

# Require Node ≥18. nvm is a common path; print a hint if missing.
if ! command -v node >/dev/null 2>&1; then
  echo "caveman: Node.js (≥18) required. Install:" >&2
  echo "  macOS:  brew install node" >&2
  echo "  Linux:  see https://nodejs.org or use nvm (https://github.com/nvm-sh/nvm)" >&2
  exit 1
fi

NODE_MAJOR=$(node -p "process.versions.node.split('.')[0]")
if [ "$NODE_MAJOR" -lt 18 ]; then
  echo "caveman: Node $NODE_MAJOR too old. Need Node ≥18." >&2
  echo "  Upgrade: https://nodejs.org" >&2
  exit 1
fi

# If we're inside the repo clone, run the local installer directly — saves
# the npx round-trip and keeps offline installs working.
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" 2>/dev/null && pwd)" || here=""
if [ -n "$here" ] && [ -f "$here/bin/install.js" ]; then
  exec node "$here/bin/install.js" "$@"
fi

# Curl-pipe path: delegate to npx. The `--` separates npx flags from our flags.
if ! command -v npx >/dev/null 2>&1; then
  echo "caveman: npx required (ships with Node ≥18). Reinstall Node.js." >&2
  exit 1
fi

exec npx -y "github:$REPO" -- "$@"
