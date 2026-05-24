// caveman — opencode plugin
//
// Mirrors the Claude Code SessionStart + UserPromptSubmit hook pair using
// opencode's lifecycle hook system. Bun ESM module; loads the existing
// security-hardened helpers from caveman-config.js via createRequire so the
// symlink-safe flag-write code lives in one place.
//
// Layout once installed:
//   ~/.config/opencode/plugins/caveman/
//   ├── package.json
//   ├── plugin.js              ← this file
//   └── caveman-config.js      ← copied sibling of src/hooks/caveman-config.js
//
// Always-on caveman ruleset is provided separately via
// ~/.config/opencode/AGENTS.md (Tier-3 base) so this plugin only handles
// dynamic state — flag writes, slash-command parsing, natural-language
// activation, and per-prompt reinforcement. opencode's `session.created`
// payload doesn't expose a documented system-prompt-injection return, so we
// don't try to emit ruleset content here.

import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { existsSync, unlinkSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const require = createRequire(import.meta.url);
const here = dirname(fileURLToPath(import.meta.url));

// When installed: caveman-config.cjs sits next to plugin.js (copied by
// bin/install.js, renamed to .cjs because this directory's package.json
// declares "type": "module" — bare .js would be loaded as ESM and break
// require()). When loaded from the source tree (tests, dev): fall back
// to the canonical src/hooks/caveman-config.js, which lives in a directory
// whose own package.json pins "type": "commonjs". One source of truth
// either way.
function loadConfig() {
  try { return require(join(here, 'caveman-config.cjs')); }
  catch (_) { return require(join(here, '..', '..', 'hooks', 'caveman-config.js')); }
}
const config = loadConfig();

const { getDefaultMode, safeWriteFlag, readFlag, appendFlag, readHistory, VALID_MODES } = config;

// Modes handled by independent skills — not selectable via /caveman <arg>.
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

// Parse a prompt for slash-command activation or natural-language toggles.
// Returns the new mode to write, the literal string 'off' to deactivate, or
// null when the prompt doesn't change state. Mirrors caveman-mode-tracker.js.
function parseModeChange(promptRaw) {
  const prompt = (promptRaw || '').trim().toLowerCase();
  if (!prompt) return null;

  // Natural-language deactivation — checked before activation so "stop talking
  // like caveman" doesn't trip the activation regex.
  if (/\b(stop|disable|deactivate|turn off)\b.*\bcaveman\b/i.test(prompt) ||
      /\bcaveman\b.*\b(stop|disable|deactivate|turn off)\b/i.test(prompt) ||
      /\bnormal mode\b/i.test(prompt)) {
    return 'off';
  }

  // Natural-language activation
  if (/\b(activate|enable|turn on|start|talk like)\b.*\bcaveman\b/i.test(prompt) ||
      /\bcaveman\b.*\b(mode|activate|enable|turn on|start)\b/i.test(prompt)) {
    const mode = getDefaultMode();
    return mode === 'off' ? null : mode;
  }

  // Slash-command parsing — opencode also expands command files, but if the
  // user types the literal slash command we still want to flip the flag.
  if (prompt.startsWith('/caveman')) {
    const parts = prompt.split(/\s+/);
    const cmd = parts[0];
    const arg = parts[1] || '';

    if (cmd === '/caveman-commit')   return 'commit';
    if (cmd === '/caveman-review')   return 'review';
    if (cmd === '/caveman-compress') return 'compress';

    if (cmd === '/caveman') {
      if (!arg)                                     return getDefaultMode();
      if (arg === 'off' || arg === 'stop' || arg === 'disable') return 'off';
      if (arg === 'wenyan-full')                    return 'wenyan';
      if (VALID_MODES.includes(arg) && !INDEPENDENT_MODES.has(arg)) return arg;
      // Unknown arg — leave flag alone. No silent overwrite.
      return null;
    }
  }

  return null;
}

function applyModeChange(mode) {
  if (!mode) return;
  if (mode === 'off') {
    try { if (existsSync(flagPath)) unlinkSync(flagPath); } catch (e) {}
    return;
  }
  safeWriteFlag(flagPath, mode);
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
  'session.created': async () => {
    sessionStartTime = Date.now();
    const mode = getDefaultMode();
    if (mode === 'off') {
      try { if (existsSync(flagPath)) unlinkSync(flagPath); } catch (e) {}
      return;
    }
    safeWriteFlag(flagPath, mode);
  },

  'session.deleted': async () => {
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
  },

  // opencode's TUI prompt-append hook fires before the prompt is sent to the
  // model. We use it for three things: react to mode-changing prompts (slash
  // commands + natural language), intercept /caveman-stats to inject stats
  // into the prompt, and append a one-line reinforcement when caveman is
  // active so the model can't drift mid-session. Returning an object with
  // `append` adds text that the model sees immediately.
  'tui.prompt.append': async (input) => {
    const promptText = (input && (input.prompt || input.text)) || '';

    // Intercept /caveman-stats — inject computed stats into prompt context
    // so the model can render them inline.
    if (/^\/caveman-stats\b/.test(promptText.trim().toLowerCase())) {
      return { append: computeStatsSummary() };
    }

    const change = parseModeChange(promptText);
    if (change) applyModeChange(change);

    const active = readFlag(flagPath);
    if (active && !INDEPENDENT_MODES.has(active)) {
      return { append: reinforcementLine(active) };
    }
    return undefined;
  },
});

export default CavemanPlugin;
