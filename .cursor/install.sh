#!/usr/bin/env bash
# Cloud Agent install for the caveman monorepo (Go engine + pnpm workspace +
# Python suites). Idempotent: safe to re-run and to bake into a snapshot.
set -euo pipefail

cd "$(dirname "$0")/.."

# --- Node -------------------------------------------------------------------
# The pnpm workspace pins engines to Node >=22.19 (packages/agent). The base
# image ships nvm with a 22.x that satisfies this; prefer it over any older
# default node on PATH so builds and generated artifacts match CI.
export NVM_DIR="${NVM_DIR:-$HOME/.nvm}"
# shellcheck disable=SC1091
[ -s "$NVM_DIR/nvm.sh" ] && . "$NVM_DIR/nvm.sh"
nvm use 22 >/dev/null 2>&1 || true
corepack enable >/dev/null 2>&1 || true
node -v

# --- JS workspace (pnpm) ----------------------------------------------------
# Installs the root installer dep and all 18 workspace projects.
pnpm install --frozen-lockfile

# --- Go engine / proxy / CLI ------------------------------------------------
# Build every Go package (cgo tree-sitter needs gcc, present in the base image).
# Warms the module + build cache into the snapshot; also a fast compile check.
go build ./...

# --- Python test tooling ----------------------------------------------------
# pytest drives the mem/py and packages/sdk/python suites; the root tests use
# stdlib unittest. PEP 668 marks the system env externally managed, so this is
# an explicit opt-in for the ephemeral agent VM.
python3 -m pip install --break-system-packages --quiet pytest

echo "caveman dev environment ready"
