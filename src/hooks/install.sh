#!/bin/bash
# missionctl — one-command hook installer for Claude Code
# Installs: SessionStart hook (auto-load rules) + UserPromptSubmit hook (mode tracking)
# Usage: bash src/hooks/install.sh
#   or:  bash <(curl -s https://raw.githubusercontent.com/dnl-re/missionctl/main/src/hooks/install.sh)
#   or:  bash src/hooks/install.sh --force   (re-install over existing hooks)
set -e

FORCE=0
for arg in "$@"; do
  case "$arg" in
    --force|-f) FORCE=1 ;;
  esac
done

# Detect Windows (Git Bash / MSYS / MINGW) — not WSL (WSL reports "linux-gnu")
case "$OSTYPE" in
  msys*|cygwin*|mingw*)
    echo "WARNING: Running on Windows ($OSTYPE)."
    echo "         This script works in Git Bash/MSYS but symlinks may require"
    echo "         Developer Mode or admin privileges."
    echo "         If you installed via 'claude plugin install', you don't need this script."
    echo ""
    ;;
esac

# Require node — we use it to merge the hook config into settings.json
if ! command -v node >/dev/null 2>&1; then
  echo "ERROR: 'node' is required to install the missionctl hooks (used to merge"
  echo "       the hook config into ~/.claude/settings.json safely)."
  echo "       Install Node.js from https://nodejs.org and re-run this script."
  exit 1
fi

CLAUDE_DIR="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
HOOKS_DIR="$CLAUDE_DIR/hooks"
SETTINGS="$CLAUDE_DIR/settings.json"
REPO_URL="https://raw.githubusercontent.com/dnl-re/missionctl/main/hooks"

HOOK_FILES=("package.json" "missionctl-config.js" "missionctl-activate.js" "missionctl-mode-tracker.js" "missionctl-stats.js" "missionctl-statusline.sh")

# Resolve source — works from repo clone or curl pipe
SCRIPT_DIR=""
if [ -n "${BASH_SOURCE[0]:-}" ] && [ -f "${BASH_SOURCE[0]}" ]; then
  SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" 2>/dev/null && pwd)"
fi

# Check if already installed (unless --force). Older installs only had two hook
# files, so require the full current set plus the hook registrations before we
# short-circuit.
ALREADY_INSTALLED=0
if [ "$FORCE" -eq 0 ]; then
  ALL_FILES_PRESENT=1
  for hook in "${HOOK_FILES[@]}"; do
    if [ ! -f "$HOOKS_DIR/$hook" ]; then
      ALL_FILES_PRESENT=0
      break
    fi
  done

  HOOKS_WIRED=0
  HAS_STATUSLINE=0
  if [ "$ALL_FILES_PRESENT" -eq 1 ] && [ -f "$SETTINGS" ]; then
    if MISSIONCTL_SETTINGS="$SETTINGS" node -e "
      const fs = require('fs');
      const settings = JSON.parse(fs.readFileSync(process.env.MISSIONCTL_SETTINGS, 'utf8'));
      const hasmissionctlHook = (event) =>
        Array.isArray(settings.hooks?.[event]) &&
        settings.hooks[event].some(e =>
          e.hooks && e.hooks.some(h => h.command && h.command.includes('missionctl'))
        );
      process.exit(
        hasmissionctlHook('SessionStart') &&
        hasmissionctlHook('UserPromptSubmit') &&
        !!settings.statusLine
          ? 0
          : 1
      );
    " >/dev/null 2>&1; then
      HOOKS_WIRED=1
      HAS_STATUSLINE=1
    fi
  fi

  if [ "$ALL_FILES_PRESENT" -eq 1 ] && [ "$HOOKS_WIRED" -eq 1 ] && [ "$HAS_STATUSLINE" -eq 1 ]; then
    ALREADY_INSTALLED=1
    echo "missionctl hooks already installed in $HOOKS_DIR"
    echo "  Re-run with --force to overwrite: bash src/hooks/install.sh --force"
    echo ""
  fi
fi

if [ "$ALREADY_INSTALLED" -eq 1 ] && [ "$FORCE" -eq 0 ]; then
  echo "Nothing to do. Hooks are already in place."
  exit 0
fi

if [ "$FORCE" -eq 1 ] && [ -f "$HOOKS_DIR/missionctl-activate.js" ]; then
  echo "Reinstalling missionctl hooks (--force)..."
else
  echo "Installing missionctl hooks..."
fi

# 1. Ensure hooks dir exists
mkdir -p "$HOOKS_DIR"

# 2. Copy or download hook files
for hook in "${HOOK_FILES[@]}"; do
  if [ -n "$SCRIPT_DIR" ] && [ -f "$SCRIPT_DIR/$hook" ]; then
    cp "$SCRIPT_DIR/$hook" "$HOOKS_DIR/$hook"
  else
    curl -fsSL "$REPO_URL/$hook" -o "$HOOKS_DIR/$hook"
  fi
  echo "  Installed: $HOOKS_DIR/$hook"
done

# Make statusline script executable
chmod +x "$HOOKS_DIR/missionctl-statusline.sh"

# 3. Wire hooks + statusline into settings.json (idempotent)
if [ ! -f "$SETTINGS" ]; then
  echo '{}' > "$SETTINGS"
fi

# Back up existing settings.json before touching it
cp "$SETTINGS" "$SETTINGS.bak"

# Pass paths via env vars — avoids shell injection if $HOME contains single quotes
MISSIONCTL_SETTINGS="$SETTINGS" MISSIONCTL_HOOKS_DIR="$HOOKS_DIR" node -e "
  const fs = require('fs');
  const settingsPath = process.env.MISSIONCTL_SETTINGS;
  const hooksDir = process.env.MISSIONCTL_HOOKS_DIR;
  const managedStatusLinePath = hooksDir + '/missionctl-statusline.sh';
  const settings = JSON.parse(fs.readFileSync(settingsPath, 'utf8'));
  if (!settings.hooks) settings.hooks = {};

  // SessionStart — auto-load missionctl rules
  if (!settings.hooks.SessionStart) settings.hooks.SessionStart = [];
  const hasStart = settings.hooks.SessionStart.some(e =>
    e.hooks && e.hooks.some(h => h.command && h.command.includes('missionctl'))
  );
  if (!hasStart) {
    settings.hooks.SessionStart.push({
      hooks: [{
        type: 'command',
        command: 'node \"' + hooksDir + '/missionctl-activate.js\"',
        timeout: 5,
        statusMessage: 'Loading missionctl mode...'
      }]
    });
  }

  // UserPromptSubmit — track mode changes when user types /missionctl commands
  if (!settings.hooks.UserPromptSubmit) settings.hooks.UserPromptSubmit = [];
  const hasPrompt = settings.hooks.UserPromptSubmit.some(e =>
    e.hooks && e.hooks.some(h => h.command && h.command.includes('missionctl'))
  );
  if (!hasPrompt) {
    settings.hooks.UserPromptSubmit.push({
      hooks: [{
        type: 'command',
        command: 'node \"' + hooksDir + '/missionctl-mode-tracker.js\"',
        timeout: 5,
        statusMessage: 'Tracking missionctl mode...'
      }]
    });
  }

  // Statusline — wire missionctl badge (report if skipped)
  if (!settings.statusLine) {
    settings.statusLine = {
      type: 'command',
      command: 'bash \"' + managedStatusLinePath + '\"'
    };
    console.log('  Statusline badge configured.');
  } else {
    const cmd = typeof settings.statusLine === 'string'
      ? settings.statusLine
      : (settings.statusLine.command || '');
    if (cmd.includes(managedStatusLinePath)) {
      console.log('  Statusline badge already configured.');
    } else {
      console.log('  NOTE: Existing statusline detected — missionctl badge NOT added.');
      console.log('        See src/hooks/README.md to add the badge to your existing statusline.');
    }
  }

  fs.writeFileSync(settingsPath, JSON.stringify(settings, null, 2) + '\n');
  console.log('  Hooks wired in settings.json');
"

echo ""
echo "Done! Restart Claude Code to activate."
echo ""
echo "What's installed:"
echo "  - SessionStart hook: auto-loads missionctl rules every session"
echo "  - Mode tracker hook: updates statusline badge when you switch modes"
echo "    (/missionctl lite, /missionctl ultra, /missionctl-commit, etc.)"
echo "  - Statusline badge: shows [missionctl] or [missionctl:ULTRA] etc."
