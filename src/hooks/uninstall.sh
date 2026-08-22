#!/bin/bash
# caveman — uninstaller for the SessionStart + UserPromptSubmit hooks
# Removes: hook files in ~/.claude/hooks, settings.json entries, and the flag file
# Usage: bash src/hooks/uninstall.sh
#   or:  bash <(curl -s https://raw.githubusercontent.com/JuliusBrussee/caveman/main/src/hooks/uninstall.sh)
set -e

CLAUDE_DIR="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
HOOKS_DIR="$CLAUDE_DIR/hooks"
SETTINGS="$CLAUDE_DIR/settings.json"
FLAG_FILE="$CLAUDE_DIR/.caveman-active"

HOOK_FILES=("package.json" "caveman-config.js" "caveman-parse.js" "caveman-activate.js" "caveman-mode-tracker.js" "caveman-stats.js" "caveman-statusline.sh" "cavecrew-model-overrides.js")

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

# 1. Remove caveman entries from settings.json (idempotent)
#
# This runs BEFORE the hook files are deleted, on purpose. If the settings edit
# cannot complete, the install is still intact and the user can re-run or edit
# by hand. The other order left Claude Code pointing at deleted scripts, which
# is `Cannot find module …caveman-activate.js` on every session start (#471).
if [ -f "$SETTINGS" ]; then
  # Require node for the same reason install.sh does — safe JSON editing
  if ! command -v node >/dev/null 2>&1; then
    # Abort — do NOT fall through to deleting the hook files. settings.json
    # still points at them, and removing them is exactly #471 on every
    # session start. Same reason the node -e failure above exits non-zero.
    echo "ERROR: 'node' not found — cannot safely edit settings.json."
    echo "       Nothing was removed. Install node and re-run, or remove the"
    echo "       caveman SessionStart, UserPromptSubmit and statusLine entries"
    echo "       from $SETTINGS by hand first."
    exit 1
  else
    # Back up before editing, same policy as install.sh: never overwrite an
    # existing .bak, which is the only pre-caveman copy the user has.
    if [ ! -f "$SETTINGS.bak" ]; then
      cp "$SETTINGS" "$SETTINGS.bak"
    fi

    # Pass paths via env vars — avoids shell injection if $HOME contains single quotes
    CAVEMAN_SETTINGS="$SETTINGS" node -e "
      const fs = require('fs');
      const settingsPath = process.env.CAVEMAN_SETTINGS;
      // A settings.json with // comments is valid for Claude Code but not for
      // JSON.parse. Bail out before touching anything rather than half-
      // uninstalling: bin/install.js --uninstall handles JSONC properly.
      let settings;
      try {
        settings = JSON.parse(fs.readFileSync(settingsPath, 'utf8'));
      } catch (e) {
        console.error('  Cannot parse ' + settingsPath + ': ' + e.message);
        console.error('  Nothing was changed. If the file has // comments, run:');
        console.error('    npx -y github:JuliusBrussee/caveman -- --uninstall');
        process.exit(3);
      }

      const path = require('path');

      // Own ONLY handlers whose command targets one of our exact script
      // basenames. A bare 'caveman' substring also matches user-authored hooks
      // that merely mention the word in a path (#593). Mirrors
      // referencesManagedScript() in bin/lib/settings.js — keep the two in sync.
      const MANAGED = new Set([
        'caveman-activate.js', 'caveman-mode-tracker.js', 'caveman-stats.js',
        'caveman-statusline.sh', 'caveman-statusline.ps1',
      ]);
      const tokenize = (command) => {
        const out = [];
        const re = /\"([^\"]*)\"|'([^']*)'|(\S+)/g;
        let m;
        while ((m = re.exec(command)) !== null) out.push(m[1] ?? m[2] ?? m[3]);
        return out;
      };
      // win32.basename splits on both / and \\ so a settings.json written on
      // Windows still matches when this runs under bash.
      const isManaged = (command) => {
        if (typeof command !== 'string') return false;
        try {
          return tokenize(command).some(t => MANAGED.has(path.win32.basename(t)));
        } catch (e) { return false; }
      };

      let removed = 0;
      if (settings.hooks) {
        for (const event of Object.keys(settings.hooks)) {
          if (!Array.isArray(settings.hooks[event])) continue;
          let removedHere = 0;
          // Filter the inner handler list, not the entry: a matcher group
          // holding one of ours AND a foreign hook must keep the foreign one.
          settings.hooks[event] = settings.hooks[event].filter(entry => {
            if (!entry || !Array.isArray(entry.hooks)) return true;
            const before = entry.hooks.length;
            entry.hooks = entry.hooks.filter(h => !(h && isManaged(h.command)));
            const n = before - entry.hooks.length;
            removedHere += n;
            return n === 0 || entry.hooks.length > 0;
          });
          removed += removedHere;
          // Drop the event key if we emptied it (keeps settings.json tidy)
          if (removedHere > 0 && settings.hooks[event].length === 0) {
            delete settings.hooks[event];
          }
        }
        // Drop settings.hooks if it's now empty
        if (Object.keys(settings.hooks).length === 0) {
          delete settings.hooks;
        }
      }

      // Remove statusLine only when it points at our managed script. Matching
      // by basename rather than the full path also survives the slash-form
      // mismatch between a Windows-written settings.json and this env var.
      if (settings.statusLine) {
        const cmd = typeof settings.statusLine === 'string'
          ? settings.statusLine
          : (settings.statusLine.command || '');
        if (isManaged(cmd)) {
          delete settings.statusLine;
          console.log('  Removed caveman statusLine from settings.json');
        }
      }

      fs.writeFileSync(settingsPath, JSON.stringify(settings, null, 2) + '\n');
      console.log('  Removed ' + removed + ' caveman hook entries from settings.json');
    "
  fi
fi

# 2. Remove hook files (only now that settings.json no longer points at them)
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
