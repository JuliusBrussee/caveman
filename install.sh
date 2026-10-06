#!/usr/bin/env bash
# caveman — installer shim.
#
# Thin wrapper around bin/install.js (the unified Node installer). Every flag
# you'd pass to bin/install.js can be passed here; we just forward them.
#
# One-line install:
#   curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/v1.10.0/install.sh | bash
#   curl -fsSL https://raw.githubusercontent.com/JuliusBrussee/caveman/v1.10.0/install.sh | bash -s -- --all
#
# Local clone:
#   bash install.sh [flags]
#
# After the installer it hands over to the CLI's first run (`caveman setup`:
# modules, agents, one Continue) when a terminal is attached, and prints that
# one command otherwise.
#
# Why a Node installer? install.sh + install.ps1 used to be parallel sources
# of truth and constantly drifted (issue #249, etc.). One Node script works
# everywhere without bash/PowerShell quoting bugs.

set -euo pipefail

REPO="JuliusBrussee/caveman"
PINNED_REF="${CAVEMAN_REF:-v3.1.0}"
# The CLI release the first run comes from when caveman is not installed;
# kept equal to packages/cli/package.json (tests/installer/shim-security).
CLI_VERSION="2.0.1"

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

# first_run ends the install in the CLI's first run. The terminal comes from
# /dev/tty because under curl | bash stdin is the script itself.
first_run() {
  for arg in "$@"; do
    case "$arg" in -h|--help|--list|-u|--uninstall|--dry-run) return 0 ;; esac
  done
  if command -v caveman >/dev/null 2>&1; then
    set -- caveman setup
  else
    set -- npx -y "@caveman-ai/cli@$CLI_VERSION" setup
  fi
  if [ -t 1 ] && { : </dev/tty; } 2>/dev/null; then
    echo
    "$@" </dev/tty
  else
    echo "Next: $*"
  fi
}

# If we're inside the repo clone, run the local installer directly — saves
# the npx round-trip and keeps offline installs working. BASH_SOURCE is unset
# when bash is invoked from stdin (curl | bash). Do not feed an empty value to
# dirname: dirname "" resolves to "." and would execute an unrelated
# $PWD/bin/install.js from the caller's checkout.
here=""
source_path="${BASH_SOURCE[0]:-}"
if [ -n "$source_path" ]; then
  here="$(cd "$(dirname "$source_path")" 2>/dev/null && pwd)" || here=""
fi
if [ -n "$here" ] && [ -f "$here/bin/install.js" ]; then
  node "$here/bin/install.js" "$@"
  first_run "$@"
  exit 0
fi

# Curl-pipe path: delegate to npx. We do NOT pass `--` here — npm 7+ npx
# already forwards trailing args to the package, and a literal `--` tripped
# bin/install.js's parseArgs as an unknown flag.
if ! command -v npx >/dev/null 2>&1; then
  echo "caveman: npx required (ships with Node ≥18). Reinstall Node.js." >&2
  exit 1
fi

npx -y "github:$REPO#$PINNED_REF" "$@"
first_run "$@"
