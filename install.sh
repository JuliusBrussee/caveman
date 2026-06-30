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

# Resolve runtime: prefer Node ≥18, fall back to Bun.
runtime=""

if command -v node >/dev/null 2>&1; then
  NODE_MAJOR=$(node -p "process.versions.node.split('.')[0]")
  if [ "$NODE_MAJOR" -ge 18 ]; then
    runtime=node
  fi
  # If Node is too old, don't exit — fall through to the Bun probe below.
fi

if [ -z "$runtime" ] && command -v bun >/dev/null 2>&1; then
  runtime=bun
fi

if [ -z "$runtime" ]; then
  if command -v node >/dev/null 2>&1; then
    echo "caveman: Node $NODE_MAJOR too old (≥18 needed), and Bun not found. Install:" >&2
  else
    echo "caveman: Node.js (≥18) or Bun required. Install:" >&2
  fi
  echo "  macOS:  brew install node     (or brew install bun)" >&2
  echo "  Linux:  see https://nodejs.org (or nvm: https://github.com/nvm-sh/nvm)" >&2
  echo "         or https://bun.sh" >&2
  exit 1
fi

# If we're inside the repo clone, run the local installer directly — saves
# the npx round-trip and keeps offline installs working. BASH_SOURCE is unset
# when bash is invoked from stdin (curl | bash), and `set -u` would trip on a
# bare reference — default to empty so the curl-pipe path falls through cleanly.
here="$(cd "$(dirname "${BASH_SOURCE[0]:-}")" 2>/dev/null && pwd)" || here=""
if [ -n "$here" ] && [ -f "$here/bin/install.js" ]; then
  exec $runtime "$here/bin/install.js" "$@"
fi

# Curl-pipe path: delegate to npx (Node) or bunx (Bun).
if [ "$runtime" = node ]; then
  if ! command -v npx >/dev/null 2>&1; then
    echo "caveman: npx required (ships with Node ≥18). Reinstall Node.js." >&2
    exit 1
  fi
  # Do NOT pass `--` here — npm 7+ npx already forwards trailing args to the
  # package, and a literal `--` was tripping bin/install.js's parseArgs as an
  # unknown flag.
  exec npx -y "github:$REPO" "$@"
else
  # Bun runtime: bunx is Bun's equivalent of npx.
  exec bunx -y "github:$REPO" "$@"
fi
