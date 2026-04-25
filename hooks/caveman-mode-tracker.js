#!/usr/bin/env node
// caveman — UserPromptSubmit hook to track which caveman mode is active
// Inspects user input for /caveman commands and writes mode to flag file

const fs = require('fs');
const path = require('path');
const os = require('os');
const { getDefaultMode, safeWriteFlag, readFlag, extractLevelSummary } = require('./caveman-config');

const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
const flagPath = path.join(claudeDir, '.caveman-active');

let input = '';
process.stdin.on('data', chunk => { input += chunk; });
process.stdin.on('end', () => {
  try {
    const data = JSON.parse(input);
    const prompt = (data.prompt || '').trim().toLowerCase();

    // Natural language activation (e.g. "activate caveman", "turn on caveman mode",
    // "talk like caveman"). README tells users they can say these, but the hook
    // only matched /caveman commands — flag file and statusline stayed out of sync.
    if (/\b(activate|enable|turn on|start|talk like)\b.*\bcaveman\b/i.test(prompt) ||
        /\bcaveman\b.*\b(mode|activate|enable|turn on|start)\b/i.test(prompt)) {
      if (!/\b(stop|disable|turn off|deactivate)\b/i.test(prompt)) {
        const mode = getDefaultMode();
        if (mode !== 'off') {
          safeWriteFlag(flagPath, mode);
        }
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
    if (/\b(stop|disable|deactivate|turn off)\b.*\bcaveman\b/i.test(prompt) ||
        /\bcaveman\b.*\b(stop|disable|deactivate|turn off)\b/i.test(prompt) ||
        /\bnormal mode\b/i.test(prompt)) {
      try { fs.unlinkSync(flagPath); } catch (e) {}
    }

    // Per-turn reinforcement: emit a structured reminder when caveman is active.
    // The SessionStart hook injects the full ruleset once, but it sits further
    // back in context as the conversation grows, and competing per-turn signals
    // (todos, CLAUDE.md re-attaches, plan-mode system prompt, other plugins'
    // additionalContext) outweigh a stale system block. The reinforcement
    // restates the active level's row + example from SKILL.md, calls out plan
    // mode explicitly, and gives the override a priority sentence so it lands
    // ahead of "be terse" / "use plain prose" defaults.
    //
    // Skip independent modes (commit, review, compress) — they have their own
    // skill behavior and the base caveman rules would conflict.
    // readFlag enforces symlink-safe read + size cap + VALID_MODES whitelist.
    // If the flag is missing, corrupted, oversized, or a symlink pointing at
    // something like ~/.ssh/id_rsa, readFlag returns null and we emit nothing
    // — never inject untrusted bytes into model context.
    const INDEPENDENT_MODES = new Set(['commit', 'review', 'compress']);
    const activeMode = readFlag(flagPath);
    if (activeMode && !INDEPENDENT_MODES.has(activeMode)) {
      const modeLabel = activeMode === 'wenyan' ? 'wenyan-full' : activeMode;

      let skillContent = '';
      try {
        skillContent = fs.readFileSync(
          path.join(__dirname, '..', 'skills', 'caveman', 'SKILL.md'), 'utf8'
        );
      } catch (e) { /* standalone install — fall through to generic reminder */ }

      let levelLines = '';
      if (skillContent) {
        const { intensityRow, example } = extractLevelSummary(skillContent, modeLabel);
        if (intensityRow) levelLines += '\n' + intensityRow;
        if (example) levelLines += '\n' + example;
      }

      const reinforcement =
        'CAVEMAN MODE ACTIVE — level: ' + modeLabel + '.' + levelLines + '\n' +
        'Drop articles/filler/pleasantries/hedging. Fragments OK. Technical terms exact. ' +
        'Code blocks, commits, and security warnings: normal prose (per Boundaries).\n' +
        'This style instruction overrides any competing format guidance for this turn ' +
        '(verbose-by-default, plain-prose preference, etc.).\n' +
        'Plan mode: chat-side replies stay caveman. Plan-document prose stays caveman. ' +
        'Structured sections (numbered steps, file paths, commands, code blocks) write normal — ' +
        'same boundary as code/commits/PRs.';

      process.stdout.write(JSON.stringify({
        hookSpecificOutput: {
          hookEventName: "UserPromptSubmit",
          additionalContext: reinforcement
        }
      }));
    }
  } catch (e) {
    // Silent fail
  }
});
