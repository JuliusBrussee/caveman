#!/usr/bin/env node
// caveman — UserPromptSubmit hook to track which caveman mode is active
// Inspects user input for /caveman commands and writes mode to flag file

const fs = require('fs');
const path = require('path');
const os = require('os');
const { getDefaultMode, safeWriteFlag, readFlag } = require('./caveman-config');

const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
const flagPath = path.join(claudeDir, '.caveman-active');

let input = '';
process.stdin.on('data', chunk => { input += chunk; });
process.stdin.on('end', () => {
  try {
    const data = JSON.parse(input);
    const prompt = (data.prompt || '').trim().toLowerCase();

    // Deactivation patterns — reused below to unlink the flag and above as the
    // activation guard. Broad verbs (stop/disable/deactivate/turn off/switch off)
    // use sentence-spanning .* because they unambiguously indicate deactivation.
    // Bare off/no and the ambiguous verbs exit/end/cancel require tight proximity
    // to "caveman" so unrelated prose like "go caveman and end with a summary"
    // does not flip the flag.
    const isDeactivation =
        /\b(stop|disable|deactivate|turn off|switch off)\b.*\bcaveman\b/i.test(prompt) ||
        /\bcaveman\b.*\b(stop|disable|deactivate|turn off|switch off)\b/i.test(prompt) ||
        /\bcaveman\s+off\b/i.test(prompt) ||
        /\boff\s+caveman\b/i.test(prompt) ||
        /\bno\s+caveman\b/i.test(prompt) ||
        /\b(exit|end|cancel)\s+caveman\b/i.test(prompt) ||
        /\bcaveman\s+(exit|end|cancel)\b/i.test(prompt) ||
        /\b(normal mode|back to normal)\b/i.test(prompt);

    // Natural language activation (e.g. "activate caveman", "turn on caveman mode",
    // "talk like caveman", "caveman on", "go caveman"). Bare "on" only matches
    // in tight proximity to "caveman". Skipped when the same prompt also looks
    // like a deactivation so "stop caveman mode" cannot both activate and
    // deactivate simultaneously.
    const activationMatch =
        /\b(activate|enable|turn on|switch on|start|talk like|go|use|be)\b.*\bcaveman\b/i.test(prompt) ||
        /\bcaveman\b.*\b(mode|activate|enable|turn on|switch on|start)\b/i.test(prompt) ||
        /\bcaveman\s+on\b/i.test(prompt) ||
        /\bon\s+caveman\b/i.test(prompt);
    if (activationMatch && !isDeactivation) {
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

    // Unlink the flag if the prompt looks like a deactivation.
    if (isDeactivation) {
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
    const INDEPENDENT_MODES = new Set(['commit', 'review', 'compress']);
    const activeMode = readFlag(flagPath);
    if (activeMode && !INDEPENDENT_MODES.has(activeMode)) {
      process.stdout.write(JSON.stringify({
        hookSpecificOutput: {
          hookEventName: "UserPromptSubmit",
          additionalContext: "CAVEMAN MODE ACTIVE (" + activeMode + "). " +
            "Drop articles/filler/pleasantries/hedging. Fragments OK. " +
            "Code/commits/security: write normal."
        }
      }));
    }
  } catch (e) {
    // Silent fail
  }
});
