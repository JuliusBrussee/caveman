#!/usr/bin/env node
// missionctl — UserPromptSubmit hook to track which missionctl mode is active
// Inspects user input for /missionctl commands and writes mode to flag file

const fs = require('fs');
const path = require('path');
const os = require('os');
const { execFileSync } = require('child_process');
const { getDefaultMode, safeWriteFlag, readFlag, VALID_MODES } = require('./missionctl-config');

// Modes handled by their own slash commands (/missionctl-commit, etc.) — not
// selectable via /missionctl <arg>.
const INDEPENDENT_MODES = new Set(['commit', 'review', 'compress']);

const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
const flagPath = path.join(claudeDir, '.missionctl-active');

let input = '';
process.stdin.on('data', chunk => { input += chunk; });
process.stdin.on('end', () => {
  try {
    const data = JSON.parse(input);
    const prompt = (data.prompt || '').trim().toLowerCase();

    // Natural language activation (e.g. "activate missionctl", "turn on missionctl mode",
    // "operational brevity mode"). README tells users they can say these, but the hook
    // only matched /missionctl commands — flag file and statusline stayed out of sync.
    // Also recognize brevity requests ("less tokens", "be brief/terse", "fewer
    // tokens", "shorter answers") — README promises these trigger missionctl too.
    if (/\b(activate|enable|turn on|start|talk like)\b.*\bmissionctl\b/i.test(prompt) ||
        /\bmissionctl\b.*\b(mode|activate|enable|turn on|start)\b/i.test(prompt) ||
        /\b(less tokens|fewer tokens|be brief|be terse|shorter answers)\b/i.test(prompt)) {
      if (!/\b(stop|disable|turn off|deactivate)\b/i.test(prompt)) {
        const mode = getDefaultMode();
        if (mode !== 'off') {
          safeWriteFlag(flagPath, mode);
        }
      }
    }

    // /missionctl-stats [--share] — block the prompt and inject stats output as
    // the hook's reason. The script reads the active session log, so we pass
    // transcript_path through when Claude Code provides it.
    const statsMatch = /^\/missionctl(?::missionctl)?-stats(?:\s+(.*))?$/.exec(prompt);
    if (statsMatch) {
      const tailArgs = (statsMatch[1] || '').trim().split(/\s+/).filter(Boolean);
      try {
        const statsPath = path.join(__dirname, 'missionctl-stats.js');
        const argv = [statsPath];
        if (data.transcript_path) argv.push('--session-file', data.transcript_path);
        if (tailArgs.includes('--share')) argv.push('--share');
        if (tailArgs.includes('--all')) argv.push('--all');
        const sinceIdx = tailArgs.indexOf('--since');
        if (sinceIdx !== -1 && tailArgs[sinceIdx + 1]) {
          argv.push('--since', tailArgs[sinceIdx + 1]);
        }
        const out = execFileSync(process.execPath, argv, { encoding: 'utf8', timeout: 5000 });
        process.stdout.write(JSON.stringify({ decision: 'block', reason: out.trim() }));
      } catch (e) {
        process.stdout.write(JSON.stringify({
          decision: 'block',
          reason: 'missionctl-stats: could not run stats script.\nTry manually: node hooks/missionctl-stats.js'
        }));
      }
      return;
    }

    // Match /missionctl commands
    if (prompt.startsWith('/missionctl')) {
      const parts = prompt.split(/\s+/);
      const cmd = parts[0]; // /missionctl, /missionctl-commit, /missionctl-review, etc.
      const arg = parts[1] || '';

      let mode = null;

      if (cmd === '/missionctl-commit') {
        mode = 'commit';
      } else if (cmd === '/missionctl-review') {
        mode = 'review';
      } else if (cmd === '/missionctl-compress' || cmd === '/missionctl:missionctl-compress') {
        mode = 'compress';
      } else if (cmd === '/missionctl' || cmd === '/missionctl:missionctl') {
        // Bare /missionctl → activate at configured default
        if (!arg) {
          mode = getDefaultMode();
        } else if (arg === 'off' || arg === 'stop' || arg === 'disable') {
          mode = 'off';
        } else if (arg === 'wenyan-full') {
          // Canonical alias — config stores as 'wenyan'
          mode = 'wenyan';
        } else if (VALID_MODES.includes(arg) && !INDEPENDENT_MODES.has(arg)) {
          mode = arg;
        }
        // Unknown arg → mode stays null, flag untouched (no silent overwrite)
      }

      if (mode && mode !== 'off') {
        safeWriteFlag(flagPath, mode);
      } else if (mode === 'off') {
        try { fs.unlinkSync(flagPath); } catch (e) {}
      }
    }

    // Detect deactivation — natural language and slash commands
    if (/\b(stop|disable|deactivate|turn off)\b.*\bmissionctl\b/i.test(prompt) ||
        /\bmissionctl\b.*\b(stop|disable|deactivate|turn off)\b/i.test(prompt) ||
        /\bnormal mode\b/i.test(prompt)) {
      try { fs.unlinkSync(flagPath); } catch (e) {}
    }

    // Per-turn reinforcement: emit a structured reminder when missionctl is active.
    // The SessionStart hook injects the full ruleset once, but models lose it
    // when other plugins inject competing style instructions every turn.
    // This keeps missionctl visible in the model's attention on every user message.
    //
    // Skip independent modes (commit, review, compress) — they have their own
    // skill behavior and the base missionctl rules would conflict.
    // readFlag enforces symlink-safe read + size cap + VALID_MODES whitelist.
    // If the flag is missing, corrupted, oversized, or a symlink pointing at
    // something like ~/.ssh/id_rsa, readFlag returns null and we emit nothing
    // — never inject untrusted bytes into model context.
    const activeMode = readFlag(flagPath);
    if (activeMode && !INDEPENDENT_MODES.has(activeMode)) {
      process.stdout.write(JSON.stringify({
        hookSpecificOutput: {
          hookEventName: "UserPromptSubmit",
          additionalContext: "missionctl active (" + activeMode + "). " +
            "Drop articles/filler/pleasantries/hedging. Fragments OK. " +
            "Code/commits/security: write normal."
        }
      }));
    }
  } catch (e) {
    // Silent fail
  }
});
