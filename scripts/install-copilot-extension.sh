#!/usr/bin/env sh
set -e

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
node "$SCRIPT_DIR/install-copilot-extension.mjs" "$@"
