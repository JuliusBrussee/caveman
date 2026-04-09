#!/bin/bash
# caveman — uninstaller for the SessionStart + UserPromptSubmit hooks
# Removes: hook files in ~/.claude/hooks, settings.json entries, and the flag file
# Usage: bash hooks/uninstall.sh
#   or:  bash <(curl -s https://raw.githubusercontent.com/JuliusBrussee/caveman/main/hooks/uninstall.sh)
set -e

CLAUDE_DIR="$HOME/.claude"
HOOKS_DIR="$CLAUDE_DIR/hooks"
SETTINGS="$CLAUDE_DIR/settings.json"
FLAG_FILE="$CLAUDE_DIR/.caveman-active"

HOOK_FILES=("caveman-activate.js" "caveman-mode-tracker.js")

echo "Uninstalling caveman hooks..."

# 1. Remove hook files
for hook in "${HOOK_FILES[@]}"; do
  if [ -f "$HOOKS_DIR/$hook" ]; then
    rm "$HOOKS_DIR/$hook"
    echo "  Removed: $HOOKS_DIR/$hook"
  fi
done

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

    node -e "
      const fs = require('fs');
      const settings = JSON.parse(fs.readFileSync('$SETTINGS', 'utf8'));

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

      fs.writeFileSync('$SETTINGS', JSON.stringify(settings, null, 2) + '\n');
      console.log('  Removed ' + removed + ' caveman hook entries from settings.json');
    "
  fi
fi

# 3. Remove flag file
if [ -f "$FLAG_FILE" ]; then
  rm "$FLAG_FILE"
  echo "  Removed: $FLAG_FILE"
fi

echo ""
echo "Done! Restart Claude Code to complete the uninstall."
echo ""
echo "Note: If you installed caveman as a plugin, disabling the plugin is"
echo "      the recommended way to deactivate hooks — this script is only"
echo "      needed if you installed manually via install.sh."
