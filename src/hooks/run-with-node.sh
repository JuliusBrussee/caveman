#!/bin/sh
# run-with-node.sh <script.js> — resolve node from a robust fallback chain and
# exec the given hook script with it.
#
# Claude Code runs hook commands with a minimal, non-interactive PATH (no
# nvm/profile sourcing), so a bare `node "$script"` prints
# "/bin/sh: 1: node: not found" on every prompt even when node is installed
# and perfectly usable from an interactive shell (issue: SessionStart /
# UserPromptSubmit hook noise). Try PATH first, then the common absolute
# install locations, and if node genuinely isn't installed anywhere, skip the
# hook silently rather than printing non-blocking noise every turn.
set -eu

script="$1"

find_node() {
  command -v node 2>/dev/null && return 0
  command -v nodejs 2>/dev/null && return 0
  # nvm's installed versions (no active shell profile to resolve "default")
  # and the common OS package-manager / Homebrew install locations.
  # ${HOME:-} — HOME is occasionally unset in the minimal env hooks run
  # under; under `set -u` a bare "$HOME" would abort the script here
  # instead of falling through to the other candidates below.
  for candidate in "${HOME:-}"/.nvm/versions/node/*/bin/node \
                   /usr/local/bin/node /opt/homebrew/bin/node /usr/bin/node; do
    [ -x "$candidate" ] && printf '%s\n' "$candidate" && return 0
  done
  return 1
}

node_bin=$(find_node) || exit 0
exec "$node_bin" "$script"
