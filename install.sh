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
#   bash install.sh --node [flags]   # explicit default: Node + npx
#   bash install.sh --npm [flags]    # alias for --node
#   bash install.sh --bun [flags]    # curl-pipe path uses bunx
#   bash install.sh --pnpm [flags]   # curl-pipe path uses pnpx
#
# Why a Node installer? install.sh + install.ps1 used to be parallel sources
# of truth and constantly drifted (issue #249, etc.). One Node script works
# everywhere without bash/PowerShell quoting bugs.

set -euo pipefail

REPO="JuliusBrussee/caveman"
RUNNER="npx"
RUNNER_FLAG=""
PACKAGE_RUNNER="node"
INSTALLER_ARGS=()

for arg in "$@"; do
  case "$arg" in
    --node|--npm)
      if [ -n "$RUNNER_FLAG" ]; then
        echo "caveman: choose only one package manager flag (--node, --npm, --bun, --pnpm)." >&2
        exit 2
      fi
      RUNNER="npx"
      RUNNER_FLAG="$arg"
      PACKAGE_RUNNER="node"
      ;;
    --bun)
      if [ -n "$RUNNER_FLAG" ]; then
        echo "caveman: choose only one package manager flag (--node, --npm, --bun, --pnpm)." >&2
        exit 2
      fi
      RUNNER="bunx"
      RUNNER_FLAG="$arg"
      PACKAGE_RUNNER="bun"
      ;;
    --pnpm)
      if [ -n "$RUNNER_FLAG" ]; then
        echo "caveman: choose only one package manager flag (--node, --npm, --bun, --pnpm)." >&2
        exit 2
      fi
      RUNNER="pnpx"
      RUNNER_FLAG="$arg"
      PACKAGE_RUNNER="pnpm"
      ;;
    *)
      INSTALLER_ARGS+=("$arg")
      ;;
  esac
done

require_node() {
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
}

require_bun() {
  if ! command -v bun >/dev/null 2>&1; then
    echo "caveman: Bun required for --bun. Install: https://bun.sh" >&2
    exit 1
  fi
}

# If we're inside the repo clone, run the local installer directly — saves
# the npx round-trip and keeps offline installs working. BASH_SOURCE is unset
# when bash is invoked from stdin (curl | bash), and `set -u` would trip on a
# bare reference — default to empty so the curl-pipe path falls through cleanly.
here="$(cd "$(dirname "${BASH_SOURCE[0]:-}")" 2>/dev/null && pwd)" || here=""
if [ -n "$here" ] && [ -f "$here/bin/install.js" ]; then
  case "$RUNNER" in
    bunx)
      require_bun
      CAVEMAN_PACKAGE_RUNNER="$PACKAGE_RUNNER" exec bun "$here/bin/install.js" "${INSTALLER_ARGS[@]+"${INSTALLER_ARGS[@]}"}"
      ;;
    *)
      require_node
      CAVEMAN_PACKAGE_RUNNER="$PACKAGE_RUNNER" exec node "$here/bin/install.js" "${INSTALLER_ARGS[@]+"${INSTALLER_ARGS[@]}"}"
      ;;
  esac
fi

# Curl-pipe path: delegate to the selected package runner. We do NOT pass `--`
# here — npm 7+ npx already forwards trailing args to the package, and a
# literal `--` tripped bin/install.js's parseArgs as an unknown flag.
case "$RUNNER" in
  npx)
    require_node
    if ! command -v npx >/dev/null 2>&1; then
      echo "caveman: npx required (ships with Node ≥18). Reinstall Node.js." >&2
      exit 1
    fi
    CAVEMAN_PACKAGE_RUNNER="$PACKAGE_RUNNER" exec npx -y "github:$REPO" "${INSTALLER_ARGS[@]+"${INSTALLER_ARGS[@]}"}"
    ;;
  bunx)
    require_bun
    if ! command -v bunx >/dev/null 2>&1; then
      echo "caveman: bunx required for --bun. Install Bun: https://bun.sh" >&2
      exit 1
    fi
    CAVEMAN_PACKAGE_RUNNER="$PACKAGE_RUNNER" exec bunx --bun "github:$REPO" "${INSTALLER_ARGS[@]+"${INSTALLER_ARGS[@]}"}"
    ;;
  pnpx)
    require_node
    if ! command -v pnpx >/dev/null 2>&1; then
      echo "caveman: pnpx required for --pnpm. Install pnpm: https://pnpm.io" >&2
      exit 1
    fi
    CAVEMAN_PACKAGE_RUNNER="$PACKAGE_RUNNER" exec pnpx "github:$REPO" "${INSTALLER_ARGS[@]+"${INSTALLER_ARGS[@]}"}"
    ;;
esac
