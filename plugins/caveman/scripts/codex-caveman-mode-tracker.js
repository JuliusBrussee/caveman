#!/usr/bin/env node
// caveman — Codex UserPromptSubmit hook to track active mode and reinforce style

const fs = require('fs');
const path = require('path');
const {
  getCodexHome,
  getDefaultMode,
  safeWriteFlag,
  readFlag,
} = require('./codex-caveman-config');

const flagPath = path.join(getCodexHome(), '.caveman-active');
const INDEPENDENT_MODES = new Set(['commit', 'review', 'compress']);

function removeFlag() {
  try { fs.unlinkSync(flagPath); } catch (e) {}
}

function maybeWriteMode(mode) {
  if (mode === 'off') {
    removeFlag();
    return;
  }
  if (mode) {
    safeWriteFlag(flagPath, mode);
  }
}

function parsePrompt(input) {
  if (!input.trim()) return '';

  try {
    const data = JSON.parse(input);
    const prompt = data.prompt || data.text || data.userPrompt || '';
    return String(prompt).trim();
  } catch (e) {
    return input.trim();
  }
}

function parseCommandMode(prompt) {
  const lower = prompt.toLowerCase();
  const parts = lower.split(/\s+/);
  const cmd = parts[0];
  const arg = parts[1] || '';

  if (cmd === '$caveman-commit' || cmd === '/caveman-commit') return 'commit';
  if (cmd === '$caveman-review' || cmd === '/caveman-review') return 'review';
  if (cmd === '$caveman-compress' || cmd === '/caveman-compress' || cmd === '/caveman:compress') return 'compress';
  if (cmd === '$caveman-help' || cmd === '/caveman-help') return 'help';
  if (cmd !== '$caveman' && cmd !== '/caveman' && cmd !== '/caveman:caveman') return null;

  if (arg === 'lite') return 'lite';
  if (arg === 'full') return 'full';
  if (arg === 'ultra') return 'ultra';
  if (arg === 'wenyan-lite') return 'wenyan-lite';
  if (arg === 'wenyan' || arg === 'wenyan-full') return 'wenyan';
  if (arg === 'wenyan-ultra') return 'wenyan-ultra';
  return getDefaultMode();
}

let input = '';
process.stdin.on('data', chunk => { input += chunk; });
process.stdin.on('end', () => {
  const prompt = parsePrompt(input);
  if (!prompt) return;

  const lower = prompt.toLowerCase();

  if (/\b(stop|disable|deactivate|turn off)\b.*\bcaveman\b/i.test(prompt) ||
      /\bcaveman\b.*\b(stop|disable|deactivate|turn off)\b/i.test(prompt) ||
      /\bnormal mode\b/i.test(prompt)) {
    removeFlag();
    return;
  }

  const commandMode = parseCommandMode(prompt);
  if (commandMode === 'help') {
    return;
  }
  if (commandMode) {
    maybeWriteMode(commandMode);
  } else if ((/\b(activate|enable|turn on|start|talk like)\b.*\bcaveman\b/i.test(prompt) ||
             /\bcaveman\b.*\b(mode|activate|enable|turn on|start)\b/i.test(prompt)) &&
             !/\b(stop|disable|turn off|deactivate)\b/i.test(prompt)) {
    const mode = getDefaultMode();
    if (mode !== 'off') {
      safeWriteFlag(flagPath, mode);
    }
  }

  const activeMode = readFlag(flagPath);
  if (activeMode && !INDEPENDENT_MODES.has(activeMode)) {
    process.stdout.write(
      'CAVEMAN MODE ACTIVE (' + activeMode + '). ' +
      'Drop articles/filler/pleasantries/hedging. ' +
      'Fragments OK. Code/commits/security: write normal.'
    );
  }
});
