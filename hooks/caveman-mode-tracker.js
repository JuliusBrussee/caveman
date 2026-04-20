#!/usr/bin/env node
// caveman — UserPromptSubmit hook to track which caveman mode is active
// Inspects user input for /caveman commands and writes mode to flag file

const fs = require('fs');
const path = require('path');
const os = require('os');
const { getDefaultMode, safeWriteFlag, readFlag } = require('./caveman-config');

const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
const flagPath = path.join(claudeDir, '.caveman-active');
const INDEPENDENT_MODES = new Set(['commit', 'review', 'compress']);

const ACTIVATION_PATTERNS = [
  /\b(activate|enable|turn on|start|use|switch to|talk like)\b.*\bcaveman\b/i,
  /\bcaveman\b.*\b(mode|activate|enable|turn on|start|on)\b/i,
  /\b(less|fewer)\s+tokens\b/i,
  /\b(be|answer|reply)\s+(brief|terse|short)\b/i,
  /\bshorter\s+(answer|reply)\b/i,
];

const DEACTIVATION_PATTERNS = [
  /\b(stop|disable|deactivate|turn off)\b.*\bcaveman\b/i,
  /\bcaveman\b.*\b(stop|disable|deactivate|turn off|off)\b/i,
  /\bnormal mode\b/i,
  /\b(back to normal|talk normally|speak normally|use normal mode)\b/i,
];

const MODE_HINTS = {
  lite: 'Tight full sentences.',
  full: 'Drop articles. Fragments OK.',
  ultra: 'Abbrev OK. Arrows OK.',
  'wenyan-lite': 'Light classical register.',
  wenyan: 'Classical terseness.',
  'wenyan-full': 'Classical terseness.',
  'wenyan-ultra': 'Extreme classical compression.',
};

function matchesAny(patterns, value) {
  return patterns.some((pattern) => pattern.test(value));
}

function renderModeLabel(mode) {
  return mode === 'wenyan' ? 'wenyan-full' : mode;
}

let input = '';
process.stdin.on('data', chunk => { input += chunk; });
process.stdin.on('end', () => {
  try {
    const data = JSON.parse(input);
    const prompt = (data.prompt || '').trim().toLowerCase();
    const isDeactivationRequested = matchesAny(DEACTIVATION_PATTERNS, prompt);

    // Natural language activation (e.g. "activate caveman", "less tokens please",
    // "be brief"). README and SKILL copy promise these cues; keep flag/statusline
    // in sync with what the model is being asked to do.
    if (matchesAny(ACTIVATION_PATTERNS, prompt) && !isDeactivationRequested) {
      const mode = getDefaultMode();
      if (mode !== 'off') {
        safeWriteFlag(flagPath, mode);
      }
    }

    // Match /caveman commands
    if (prompt.startsWith('/caveman')) {
      const parts = prompt.split(/\s+/);
      const cmd = parts[0]; // /caveman, /caveman-commit, /caveman-review, etc.
      const arg = parts[1] || '';

      let mode = null;

      if (cmd === '/caveman-commit') {
        mode = 'commit';
      } else if (cmd === '/caveman-review') {
        mode = 'review';
      } else if (cmd === '/caveman-compress' || cmd === '/caveman:caveman-compress') {
        mode = 'compress';
      } else if (cmd === '/caveman' || cmd === '/caveman:caveman') {
        if (arg === 'lite') mode = 'lite';
        else if (arg === 'full') mode = 'full';
        else if (arg === 'ultra') mode = 'ultra';
        else if (arg === 'wenyan-lite') mode = 'wenyan-lite';
        else if (arg === 'wenyan' || arg === 'wenyan-full') mode = 'wenyan';
        else if (arg === 'wenyan-ultra') mode = 'wenyan-ultra';
        else mode = getDefaultMode();
      }

      if (mode && mode !== 'off') {
        safeWriteFlag(flagPath, mode);
      } else if (mode === 'off') {
        try { fs.unlinkSync(flagPath); } catch (e) {}
      }
    }

    // Detect deactivation — natural language and slash commands
    if (isDeactivationRequested) {
      try { fs.unlinkSync(flagPath); } catch (e) {}
    }

    // Per-turn reinforcement: emit a structured reminder when caveman is active.
    // The SessionStart hook injects the full ruleset once, but models lose it
    // when other plugins inject competing style instructions every turn.
    // This keeps caveman visible in the model's attention on every user message.
    //
    // Skip independent modes (commit, review, compress) — they have their own
    // skill behavior and the base caveman rules would conflict.
    // readFlag enforces symlink-safe read + size cap + VALID_MODES whitelist.
    // If the flag is missing, corrupted, oversized, or a symlink pointing at
    // something like ~/.ssh/id_rsa, readFlag returns null and we emit nothing
    // — never inject untrusted bytes into model context.
    const activeMode = readFlag(flagPath);
    if (activeMode && !INDEPENDENT_MODES.has(activeMode)) {
      const modeLabel = renderModeLabel(activeMode);
      const modeHint = MODE_HINTS[modeLabel] || MODE_HINTS[activeMode] || 'Keep terse.';
      process.stdout.write(JSON.stringify({
        hookSpecificOutput: {
          hookEventName: "UserPromptSubmit",
          additionalContext: "CAVEMAN MODE ACTIVE (" + modeLabel + "). " +
            modeHint + " " +
            "Answer first. Keep code/commands/file paths/flags/env vars/URLs/numbers/error text exact. " +
            "Use normal prose for security, irreversible, ordered multi-step, or confused-user moments. " +
            "Code/commits/security: write normal."
        }
      }));
    }
  } catch (e) {
    // Silent fail
  }
});
