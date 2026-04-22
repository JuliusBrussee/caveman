#!/bin/bash
# caveman — uninstaller for the SessionStart + UserPromptSubmit hooks
# Removes: hook files in ~/.claude/hooks, settings.json entries, and the flag file
# Usage: bash hooks/uninstall.sh
#   or:  bash <(curl -s https://raw.githubusercontent.com/JuliusBrussee/caveman/main/hooks/uninstall.sh)
set -e

CLAUDE_DIR="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
HOOKS_DIR="$CLAUDE_DIR/hooks"
SETTINGS="$CLAUDE_DIR/settings.json"
FLAG_FILE="$CLAUDE_DIR/.caveman-active"

HOOK_FILES=("package.json" "caveman-config.js" "caveman-activate.js" "caveman-mode-tracker.js" "caveman-statusline.sh")

# Detect if caveman is installed as a plugin (check plugin cache)
PLUGIN_INSTALLED=0
if [ -d "$CLAUDE_DIR/plugins" ]; then
  if find "$CLAUDE_DIR/plugins" -path "*/caveman*" -name "plugin.json" -print -quit 2>/dev/null | grep -q .; then
    PLUGIN_INSTALLED=1
  fi
fi

if [ "$PLUGIN_INSTALLED" -eq 1 ]; then
  echo "Caveman appears to be installed as a Claude Code plugin."
  echo "To uninstall the plugin, run:"
  echo ""
  echo "  claude plugin disable caveman"
  echo ""
  echo "This script removes standalone hooks (installed via install.sh)."
  echo "Continuing with standalone hook removal..."
  echo ""
fi

echo "Uninstalling caveman hooks..."

# 1. Remove hook files
REMOVED_FILES=0
for hook in "${HOOK_FILES[@]}"; do
  if [ -f "$HOOKS_DIR/$hook" ]; then
    rm "$HOOKS_DIR/$hook"
    echo "  Removed: $HOOKS_DIR/$hook"
    REMOVED_FILES=$((REMOVED_FILES + 1))
  fi
done

if [ "$REMOVED_FILES" -eq 0 ]; then
  echo "  No hook files found in $HOOKS_DIR"
fi

# 2. Remove caveman entries from settings.json (idempotent)
if [ -f "$SETTINGS" ]; then
  # Require node for the same reason install.sh does — safe JSON editing
  if ! command -v node >/dev/null 2>&1; then
    echo "WARNING: 'node' not found — cannot safely edit settings.json."
    echo "         Remove the caveman SessionStart and UserPromptSubmit"
    echo "         entries from $SETTINGS manually."
  else
    # Back up before editing, same policy as install.sh
    cp "$SETTINGS" "$SETTINGS.bak"

    # Pass paths via env vars — avoids shell injection if $HOME contains single quotes
    CAVEMAN_SETTINGS="$SETTINGS" CAVEMAN_HOOKS_DIR="$HOOKS_DIR" node -e "
      const fs = require('fs');
      const settingsPath = process.env.CAVEMAN_SETTINGS;
      const hooksDir = process.env.CAVEMAN_HOOKS_DIR;
      const managedStatusLinePath = hooksDir + '/caveman-statusline.sh';
      const settings = JSON.parse(fs.readFileSync(settingsPath, 'utf8'));

      const isCavemanEntry = (entry) =>
        entry && entry.hooks && entry.hooks.some(h =>
          h.command && h.command.includes('caveman')
        );

      let removed = 0;
      if (settings.hooks) {
        for (const event of ['SessionStart', 'UserPromptSubmit']) {
          if (Array.isArray(settings.hooks[event])) {
            const before = settings.hooks[event].length;
            settings.hooks[event] = settings.hooks[event].filter(e => !isCavemanEntry(e));
            removed += before - settings.hooks[event].length;
            // Drop the event key if it's now empty (keeps settings.json tidy)
            if (settings.hooks[event].length === 0) {
              delete settings.hooks[event];
            }
          }
        }
        // Drop settings.hooks if it's now empty
        if (Object.keys(settings.hooks).length === 0) {
          delete settings.hooks;
        }
      }

      // Remove statusLine if it references caveman
      if (settings.statusLine) {
        const cmd = typeof settings.statusLine === 'string'
          ? settings.statusLine
          : (settings.statusLine.command || '');
        if (cmd.includes(managedStatusLinePath)) {
          delete settings.statusLine;
          console.log('  Removed caveman statusLine from settings.json');
        }
      }

      fs.writeFileSync(settingsPath, JSON.stringify(settings, null, 2) + '\n');
      console.log('  Removed ' + removed + ' caveman hook entries from settings.json');
    "
  fi
fi

# 3. Clean up backup file left by installer
if [ -f "$SETTINGS.bak" ]; then
  rm "$SETTINGS.bak"
  echo "  Removed: $SETTINGS.bak"
fi

# 4. Remove flag file
if [ -f "$FLAG_FILE" ]; then
  rm "$FLAG_FILE"
  echo "  Removed: $FLAG_FILE"
fi

echo ""
echo "Done! Restart Claude Code to complete the uninstall."

# Guidance for other agents
echo ""
echo "Other agents:"
echo "  npx skills remove caveman    # Cursor, Windsurf, Cline, Copilot, etc."
echo "  claude plugin disable caveman  # Claude Code plugin"
echo "  gemini extensions uninstall caveman  # Gemini CLI"
