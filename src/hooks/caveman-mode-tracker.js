#!/usr/bin/env node
// caveman — UserPromptSubmit hook to track which caveman mode is active
// Inspects user input for /caveman commands and writes mode to flag file

const fs = require('fs');
const path = require('path');
const os = require('os');
const { execFileSync } = require('child_process');
const { getDefaultMode, safeWriteFlag, readFlag, recordModeChange, VALID_MODES } = require('./caveman-config');

// Modes handled by their own slash commands (/caveman-commit, etc.) — not
// selectable via /caveman <arg>.
const INDEPENDENT_MODES = new Set(['commit', 'review', 'compress']);

const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
const flagPath = path.join(claudeDir, '.caveman-active');
// Remembers the prose mode active before a one-shot independent mode
// (/caveman-commit etc.) so the next ordinary prompt can restore it (#599).
const prevPath = path.join(claudeDir, '.caveman-active.prev');

let input = '';
process.stdin.on('data', chunk => { input += chunk; });
// Abnormal stdin close (broken pipe, parent crash) emits 'error'; without a
// listener Node throws it as an uncaught exception and the hook exits
// non-zero — a spurious hook failure (#538). Hooks must always exit 0.
process.stdin.on('error', () => process.exit(0));
process.stdin.on('end', () => {
  try {
    const data = JSON.parse(input);
    // Collapse whitespace so phrase triggers still match multiline prompts —
    // every regex below sees a single-line prompt (#598).
    const prompt = (data.prompt || '').trim().toLowerCase().replace(/\s+/g, ' ');

    // Deactivation intent — computed FIRST so "turn caveman mode off" never
    // falls through to the activation patterns (#598: the old contiguous
    // "turn off" phrasing missed the "turn X off" word order entirely, and
    // the activation regex then re-armed caveman at the default level).
    const wantsOff =
      /\b(stop|disable|deactivate|quit|exit|kill)\s+(the\s+)?caveman\b/.test(prompt) ||
      /\bcaveman(\s+mode)?\s+(off|stop|disabled?)\b/.test(prompt) ||
      /\bturn\s+off\s+(the\s+)?caveman\b/.test(prompt) ||
      // "normal mode" only as a command (prompt-initial, optionally led by a
      // switch-back verb) or with caveman context — never mid-sentence for
      // e.g. vim's normal mode ("how do I exit vim normal mode").
      /^(please\s+)?(go\s+|back\s+to\s+|switch\s+(back\s+)?to\s+|return\s+to\s+)?normal\s+mode\b/.test(prompt) ||
      (/\bnormal\s+mode\b/.test(prompt) && /\bcaveman\b/.test(prompt));

    // Questions about caveman are not activation commands
    // ("what is caveman mode?", "does caveman lite drop articles?").
    const isQuestion =
      /^(what|whats|what's|how|why|when|where|who|does|do|did|is|are|can|could|would|should|tell me|explain)\b/.test(prompt);

    // Natural language activation (e.g. "activate caveman", "turn on caveman
    // mode", "talk like caveman"). README tells users they can say these.
    // Also brevity requests ("less tokens", "be brief/terse", "fewer tokens",
    // "shorter answers") — but not when scoped to a single section
    // ("be brief in the summary"), which is a one-off instruction, not a
    // session-wide mode switch.
    if (!wantsOff && !isQuestion) {
      if (/\b(activate|enable|start|turn on|use|switch to|want|give me)\b[^.]{0,40}\bcaveman\b/.test(prompt) ||
          /\btalk like\b[^.]{0,40}\bcaveman\b/.test(prompt) ||
          /\bcaveman\s+mode\s+(on|please|now)\b/.test(prompt) ||
          /^caveman(\s+mode)?\s*[.!]*$/.test(prompt) ||
          /\b(less tokens|fewer tokens|be brief|be terse|shorter answers)\b(?!\s+(in|for|on|about|when|during|with)\b)/.test(prompt)) {
        const mode = getDefaultMode();
        if (mode !== 'off') {
          recordModeChange(claudeDir, mode); // #601: timestamped transition log
          safeWriteFlag(flagPath, mode);
        }
      }
    }

    // /caveman-stats [--share] — block the prompt and inject stats output as
    // the hook's reason. The script reads the active session log, so we pass
    // transcript_path through when Claude Code provides it.
    const statsMatch = /^\/caveman(?::caveman)?-stats(?:\s+(.*))?$/.exec(prompt);
    if (statsMatch) {
      const tailArgs = (statsMatch[1] || '').trim().split(/\s+/).filter(Boolean);
      try {
        const statsPath = path.join(__dirname, 'caveman-stats.js');
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
          reason: 'caveman-stats: could not run stats script.\nTry manually: node hooks/caveman-stats.js'
        }));
      }
      return;
    }

    // Match /caveman commands. Independent one-shot modes remember the prose
    // mode active before them so the next ordinary prompt restores it (#599)
    // — SKILL.md promises "Level persist until changed or session end", and a
    // one-shot skill invocation should not count as "changed" forever.
    let setIndependentThisTurn = false;
    if (prompt.startsWith('/caveman')) {
      const parts = prompt.split(/\s+/);
      const cmd = parts[0]; // /caveman, /caveman-commit, /caveman-review, etc.
      const arg = parts[1] || '';

      let mode = null;

      // Marketplace plugin installs surface commands namespaced as
      // /caveman:caveman-<name> — accept both forms for every skill (#599:
      // only compress and stats had the namespaced variant).
      if (cmd === '/caveman-commit' || cmd === '/caveman:caveman-commit') {
        mode = 'commit';
      } else if (cmd === '/caveman-review' || cmd === '/caveman:caveman-review') {
        mode = 'review';
      } else if (cmd === '/caveman-compress' || cmd === '/caveman:caveman-compress') {
        mode = 'compress';
      } else if (cmd === '/caveman' || cmd === '/caveman:caveman') {
        // Bare /caveman → activate at configured default
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
        if (INDEPENDENT_MODES.has(mode)) {
          // Save the prose mode being displaced — but never overwrite an
          // already-saved one with another independent mode (/caveman-commit
          // followed by /caveman-review must still restore the original).
          const current = readFlag(flagPath);
          if (current && !INDEPENDENT_MODES.has(current)) {
            safeWriteFlag(prevPath, current);
          }
          setIndependentThisTurn = true;
        }
        recordModeChange(claudeDir, mode); // #601
        safeWriteFlag(flagPath, mode);
      } else if (mode === 'off') {
        recordModeChange(claudeDir, null); // #601
        try { fs.unlinkSync(flagPath); } catch (e) {}
        try { fs.unlinkSync(prevPath); } catch (e) {}
      }
    }

    // Apply deactivation detected above
    if (wantsOff) {
      recordModeChange(claudeDir, null); // #601
      try { fs.unlinkSync(flagPath); } catch (e) {}
      try { fs.unlinkSync(prevPath); } catch (e) {}
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
    let activeMode = readFlag(flagPath);

    // One-shot restore (#599): an independent mode set on a PREVIOUS prompt
    // has served its turn — bring back the prose mode that was active before
    // it, or deactivate if caveman wasn't active then.
    if (activeMode && INDEPENDENT_MODES.has(activeMode) && !setIndependentThisTurn) {
      const prev = readFlag(prevPath);
      try { fs.unlinkSync(prevPath); } catch (e) {}
      if (prev && !INDEPENDENT_MODES.has(prev)) {
        recordModeChange(claudeDir, prev); // #601
        safeWriteFlag(flagPath, prev);
        activeMode = prev;
      } else {
        recordModeChange(claudeDir, null); // #601
        try { fs.unlinkSync(flagPath); } catch (e) {}
        activeMode = null;
      }
    }

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
