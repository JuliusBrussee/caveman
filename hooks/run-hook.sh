#!/usr/bin/env sh
# caveman — portable node runner for Claude Code plugin/standalone hooks
#
# Finds node even when it is not on the restricted PATH used by hook runners
# (e.g. Claude Code plugin hooks, desktop app launches, VS Code extension).
#
# Common case: node installed via nvm only adds itself to PATH in interactive
# shells (.bashrc/.zshrc). Non-interactive hook environments miss it entirely.
#
# Usage: sh run-hook.sh <script.js> [args...]

HOOK_SCRIPT="$1"
shift

# 1. Try node from the current PATH first (works for system/homebrew installs)
if command -v node >/dev/null 2>&1; then
    exec node "$HOOK_SCRIPT" "$@"
fi

# 2. Search common installation paths not always on the default PATH:
#    nvm (via current symlink), Volta, fnm, Homebrew (macOS), standard system
for _d in \
    "$HOME/.nvm/current/bin" \
    "${NVM_DIR:-$HOME/.nvm}/current/bin" \
    "$HOME/.volta/bin" \
    "$HOME/.fnm/current/bin" \
    /opt/homebrew/bin \
    /usr/local/bin \
    /usr/bin; do
    if [ -x "${_d}/node" ]; then
        exec "${_d}/node" "$HOOK_SCRIPT" "$@"
    fi
done

# 3. Not found — report and exit cleanly so a missing node does not block the
#    Claude Code session entirely (caveman mode simply won't activate).
printf 'caveman: node not found. Install Node.js: https://nodejs.org\n' >&2
exit 0
