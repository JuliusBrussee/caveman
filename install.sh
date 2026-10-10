#!/usr/bin/env bash
# caveman — installer shim.
#
# Thin wrapper around installer/install.js (the unified Node installer). Every flag
# you'd pass to installer/install.js can be passed here; we just forward them.
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
PINNED_REF="${CAVEMAN_REF:-v3.2.0}"
# The CLI release the first run comes from when caveman is not installed;
# kept equal to packages/cli/package.json (tests/installer/shim-security).
CLI_VERSION="2.1.0"

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
  launch=1
  for arg in "$@"; do
    case "$arg" in
      -h|--help|--list|-u|--uninstall|--dry-run) return 0 ;;
      # Never prompt: name the first run instead of starting it.
      --non-interactive) launch=0 ;;
    esac
  done
  # The skills above run on Node 18; the CLI (runtime, routing) needs 22.13.
  node_version=$(node -p "process.versions.node")
  node_minor=${node_version#*.}
  node_minor=${node_minor%%.*}
  case "$node_minor" in ''|*[!0-9]*) node_minor=0 ;; esac
  if [ "$NODE_MAJOR" -lt 22 ] || { [ "$NODE_MAJOR" -eq 22 ] && [ "$node_minor" -lt 13 ]; }; then
    echo
    echo "caveman: skills installed. The runtime (smaller inputs, Auto routing) needs Node 22.13+; this is v$node_version."
    echo "  Upgrade Node (https://nodejs.org), then run: npx -y @caveman-ai/cli@$CLI_VERSION"
    return 0
  fi
  # The caveman on PATH only when it is this release: an older CLI has an
  # older setup. npx runs this one, and its setup installs it for good.
  if command -v caveman >/dev/null 2>&1 &&
    caveman --version 2>/dev/null </dev/null | grep -Eq "\"version\": *\"$CLI_VERSION\""; then
    set -- caveman setup
  else
    set -- npx -y "@caveman-ai/cli@$CLI_VERSION" setup
  fi
  if [ "$launch" = 1 ] && [ -t 1 ] && { : </dev/tty; } 2>/dev/null; then
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
# $PWD/installer/install.js from the caller's checkout.
here=""
source_path="${BASH_SOURCE[0]:-}"
if [ -n "$source_path" ]; then
  here="$(cd "$(dirname "$source_path")" 2>/dev/null && pwd)" || here=""
fi
if [ -n "$here" ] && [ -f "$here/installer/install.js" ]; then
  node "$here/installer/install.js" "$@"
  first_run "$@"
  exit 0
fi

# Curl-pipe path: delegate to npx. We do NOT pass `--` here — npm 7+ npx
# already forwards trailing args to the package, and a literal `--` tripped
# installer/install.js's parseArgs as an unknown flag.
if ! command -v npx >/dev/null 2>&1; then
  echo "caveman: npx required (ships with Node ≥18). Reinstall Node.js." >&2
  exit 1
fi

# npm 12 disables git package fetches by default (EALLOWGIT). Allow only the
# root package requested here; keep the old invocation for npm versions that
# predate this config flag.
NPX_VERSION=""
if NPX_VERSION=$(npx --version 2>/dev/null); then :; fi
NPX_MAJOR=${NPX_VERSION%%.*}
case "$NPX_MAJOR" in
  ''|*[!0-9]*) NPX_MAJOR=0 ;;
esac

if [ "$NPX_MAJOR" -ge 12 ]; then
  npx --allow-git=root -y "github:$REPO#$PINNED_REF" "$@"
else
  npx -y "github:$REPO#$PINNED_REF" "$@"
fi
first_run "$@"
