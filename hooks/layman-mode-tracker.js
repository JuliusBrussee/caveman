#!/usr/bin/env node
// layman — UserPromptSubmit hook to track which layman mode is active
// Inspects user input for /layman commands and writes mode to flag file

const fs = require('fs');
const path = require('path');
const os = require('os');
const { getDefaultMode, safeWriteFlag, readFlag } = require('./layman-config');

const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
const flagPath = path.join(claudeDir, '.layman-active');

let input = '';
process.stdin.on('data', chunk => { input += chunk; });
process.stdin.on('end', () => {
  try {
    const data = JSON.parse(input);
    const prompt = (data.prompt || '').trim().toLowerCase();

    // Natural language activation (e.g. "activate layman", "turn on layman mode",
    // "explain in plain English"). README tells users they can say these, but the hook
    // only matched /layman commands — flag file and statusline stayed out of sync.
    if (/\b(activate|enable|turn on|start|use)\b.*\blayman\b/i.test(prompt) ||
        /\blayman\b.*\b(mode|activate|enable|turn on|start)\b/i.test(prompt) ||
        /\b(plain english|non-technical summary|simple summary)\b/i.test(prompt)) {
      if (!/\b(stop|disable|turn off|deactivate)\b/i.test(prompt)) {
        const mode = getDefaultMode();
        if (mode !== 'off') {
          safeWriteFlag(flagPath, mode);
        }
      }
    }

    // Match /layman commands
    if (prompt.startsWith('/layman')) {
      const parts = prompt.split(/\s+/);
      const cmd = parts[0]; // /layman, /layman-commit, /layman-review, etc.
      const arg = parts[1] || '';

      let mode = null;

      if (cmd === '/layman-commit') {
        mode = 'commit';
      } else if (cmd === '/layman-review') {
        mode = 'review';
      } else if (cmd === '/layman-compress' || cmd === '/layman:compress' || cmd === '/layman:layman-compress') {
        mode = 'compress';
      } else if (cmd === '/layman' || cmd === '/layman:layman') {
        if (arg === 'explain') mode = 'explain';
        else if (arg === 'summary') mode = 'summary';
        else if (arg === 'brief') mode = 'brief';
        else if (arg === 'lite') mode = 'lite';
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
    if (/\b(stop|disable|deactivate|turn off)\b.*\blayman\b/i.test(prompt) ||
        /\blayman\b.*\b(stop|disable|deactivate|turn off)\b/i.test(prompt) ||
        /\bnormal mode\b/i.test(prompt)) {
      try { fs.unlinkSync(flagPath); } catch (e) {}
    }

    // Per-turn reinforcement: emit a structured reminder when layman is active.
    // The SessionStart hook injects the full ruleset once, but models lose it
    // when other plugins inject competing style instructions every turn.
    // This keeps layman visible in the model's attention on every user message.
    //
    // Skip independent modes (commit, review, compress) — they have their own
    // skill behavior and the base layman rules would conflict.
    // readFlag enforces symlink-safe read + size cap + VALID_MODES whitelist.
    // If the flag is missing, corrupted, oversized, or a symlink pointing at
    // something like ~/.ssh/id_rsa, readFlag returns null and we emit nothing
    // — never inject untrusted bytes into model context.
    const INDEPENDENT_MODES = new Set(['commit', 'review', 'compress']);
    const BRIEF_MODES = new Set([
      'brief', 'lite', 'full', 'ultra',
      'wenyan-lite', 'wenyan', 'wenyan-full', 'wenyan-ultra'
    ]);
    const activeMode = readFlag(flagPath);
    if (activeMode && !INDEPENDENT_MODES.has(activeMode)) {
      const additionalContext = BRIEF_MODES.has(activeMode)
        ? "LAYMAN BRIEF MODE ACTIVE (" + activeMode + "). " +
          "Respond with token-saving brevity. Cut filler, pleasantries, hedging, and repeated setup. " +
          "Keep technical terms exact. Code unchanged. Write normal clear prose for security warnings and irreversible actions."
        : "LAYMAN MODE ACTIVE (" + activeMode + "). " +
          "After completed coding work, use a plain-English handoff. " +
          "Summary mode uses: Done, Why it matters, What changed, Check this, Warning only if needed. " +
          "Keep it simple, accurate, calm, and not babyish.";
      process.stdout.write(JSON.stringify({
        hookSpecificOutput: {
          hookEventName: "UserPromptSubmit",
          additionalContext
        }
      }));
    }
  } catch (e) {
    // Silent fail
  }
});
