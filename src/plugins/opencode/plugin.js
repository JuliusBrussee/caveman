// caveman — opencode plugin
//
// Uses opencode-native hook names (not Claude Code).
//   event                  → session.created / session.deleted
//   command.execute.before → /caveman /caveman-stats flag writes
//   experimental.chat.system.transform → reinforcement injection
//
// Layout once installed:
//   ~/.config/opencode/plugins/caveman/
//   ├── package.json
//   ├── plugin.js              ← this file
//   └── caveman-config.js      ← copied sibling of src/hooks/caveman-config.js
//
// Always-on caveman ruleset is provided separately via
// ~/.config/opencode/AGENTS.md (Tier-3 base) so this plugin only handles
// dynamic state — flag writes, slash-command interception, session tracking.

import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { existsSync, unlinkSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const require = createRequire(import.meta.url);
const here = dirname(fileURLToPath(import.meta.url));

function loadConfig() {
  try { return require(join(here, 'caveman-config.cjs')); }
  catch (_) { return require(join(here, '..', '..', 'hooks', 'caveman-config.js')); }
}
const config = loadConfig();

const { getDefaultMode, safeWriteFlag, readFlag, appendFlag, readHistory, VALID_MODES } = config;

const INDEPENDENT_MODES = new Set(['commit', 'review', 'compress']);

function opencodeConfigDir() {
  if (process.env.XDG_CONFIG_HOME) {
    return path.join(process.env.XDG_CONFIG_HOME, 'opencode');
  }
  if (process.platform === 'win32') {
    return path.join(
      process.env.APPDATA || path.join(os.homedir(), 'AppData', 'Roaming'),
      'opencode'
    );
  }
  return path.join(os.homedir(), '.config', 'opencode');
}

const flagPath = path.join(opencodeConfigDir(), '.caveman-active');
const historyPath = path.join(opencodeConfigDir(), '.caveman-history.jsonl');
let sessionStartTime = null;

function reinforcementLine(mode) {
  return 'CAVEMAN MODE ACTIVE (' + mode + '). ' +
    'Drop articles/filler/pleasantries/hedging. Fragments OK. ' +
    'Code/commits/security: write normal.';
}

// Parse /caveman <arg> for flag file writes.
// Called from command.execute.before to keep flag in sync with command files.
function parseCavemanCommand(args) {
  const arg = (args || '').trim().toLowerCase();
  if (!arg) return getDefaultMode();
  if (arg === 'off' || arg === 'stop' || arg === 'disable') return 'off';
  if (arg === 'wenyan-full') return 'wenyan';
  if (VALID_MODES.includes(arg) && !INDEPENDENT_MODES.has(arg)) return arg;
  return null;
}

function computeStatsSummary() {
  const history = readHistory(historyPath);
  if (!history || history.length === 0) {
    return 'No caveman sessions recorded yet.';
  }

  let totalDuration = 0;
  const modeCounts = {};
  let lastEntry = null;

  for (const line of history) {
    try {
      const entry = JSON.parse(line);
      totalDuration += entry.duration || 0;
      const m = entry.mode || 'off';
      modeCounts[m] = (modeCounts[m] || 0) + 1;
      lastEntry = entry;
    } catch {}
  }

  const totalMinutes = Math.round(totalDuration / 60);
  const estTokensSaved = Math.round(totalDuration * 0.75);
  const sorted = Object.entries(modeCounts).sort((a, b) => b[1] - a[1]);
  const mostUsed = sorted.length > 0 ? sorted[0] : null;

  const lines = [
    '=== CAVEMAN STATS ===',
    'Sessions: ' + history.length,
    'Total time: ' + totalMinutes + 'min',
    'Est. tokens saved: ~' + estTokensSaved.toLocaleString(),
  ];
  if (mostUsed) {
    lines.push('Most used: ' + mostUsed[0] + ' (' + mostUsed[1] + 'x)');
  }
  if (lastEntry) {
    const lastMin = Math.round((lastEntry.duration || 0) / 60);
    lines.push('Last session: ' + lastMin + 'min (' + (lastEntry.mode || 'off') + ')');
  }
  lines.push('====================');
  return lines.join('\n');
}

export const CavemanPlugin = async (_ctx) => ({
  // Session lifecycle: opencode exposes session.created and session.deleted
  // via the generic event hook with event.type discrimination.
  event: async ({ event }) => {
    if (event.type === 'session.created') {
      sessionStartTime = Date.now();
      const mode = getDefaultMode();
      if (mode === 'off') {
        try { if (existsSync(flagPath)) unlinkSync(flagPath); } catch (e) {}
        return;
      }
      safeWriteFlag(flagPath, mode);
      return;
    }

    if (event.type === 'session.deleted') {
      if (sessionStartTime) {
        const duration = Math.round((Date.now() - sessionStartTime) / 1000);
        const mode = readFlag(flagPath) || 'off';
        const entry = JSON.stringify({
          time: new Date().toISOString(),
          duration,
          mode,
        });
        try { appendFlag(historyPath, entry); } catch (e) {}
        sessionStartTime = null;
      }
      return;
    }
  },

  // Slash command interception: write/remove flag file so
  // experimental.chat.system.transform knows the current mode.
  // Command files (caveman.md, caveman-stats.md, etc.) handle the text output.
  'command.execute.before': async (input) => {
    const cmd = (input.command || '').trim().toLowerCase();
    const mode = cmd === '/caveman' ? parseCavemanCommand(input.arguments || '')
      : cmd === '/caveman-commit' ? 'commit'
      : cmd === '/caveman-review' ? 'review'
      : cmd === '/caveman-compress' ? 'compress'
      : null;
    if (mode === 'off') {
      try { if (existsSync(flagPath)) unlinkSync(flagPath); } catch (e) {}
    } else if (mode) {
      safeWriteFlag(flagPath, mode);
    }
  },

  // Reinforcement: inject caveman reminder into system prompt on each turn.
  // The command files handle the verbose instructions; this keeps the mode
  // reminder visible when the flag is active.
  'experimental.chat.system.transform': async (_input, output) => {
    const active = readFlag(flagPath);
    if (active && !INDEPENDENT_MODES.has(active)) {
      output.system = output.system || [];
      output.system.push(reinforcementLine(active));
    }
  },
});

export default CavemanPlugin;
