#!/usr/bin/env node
// caveman — UserPromptSubmit hook to track which caveman mode is active
// Inspects user input for /caveman commands and writes mode to flag file

const fs = require('fs');
const path = require('path');
const os = require('os');
const { execFileSync } = require('child_process');
const { getDefaultMode, safeWriteFlag, readFlag, recordModeChange } = require('./caveman-config');
const { parseModeChange, INDEPENDENT_MODES } = require('./caveman-parse');

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
    let prompt = (data.prompt || '').trim().toLowerCase().replace(/\s+/g, ' ');

    // Unattended scheduled-task runs must never receive caveman styling —
    // the per-turn reinforcement would hijack the task prompt, and a
    // lightweight scheduled task would answer with a caveman greeting
    // instead of doing its job. Claude Code wraps these in a
    // <scheduled-task ...> marker; bail out completely when present: no flag
    // mutation, no reinforcement, no stats. Interactive sessions are
    // unaffected.
    if (/<scheduled-task\b/.test(prompt)) return;

    // Claude Code delivers slash commands to this hook as an envelope, not
    // the literal command (#537):
    //   <command-message>caveman</command-message>
    //   <command-name>/caveman</command-name>
    //   <command-args>ultra</command-args>
    // (one-line or newline-separated — the collapse above normalizes both
    // into single spaces; <command-args> may be empty or absent). Every
    // switch below matches against the literal command string, so this
    // envelope was a silent no-op for every slash command, including
    // '/caveman off'. Reconstruct '<name> <args>' for /caveman* envelopes so
    // the rest of this hook sees exactly what the user selected. A foreign
    // command's envelope is left untouched, and natural-language detection
    // is skipped for it so another command's own args can't misfire our
    // activation/deactivation triggers.
    let skipNaturalLanguage = false;
    const envName = /<command-name>\s*([^<\s]+)\s*<\/command-name>/.exec(prompt);
    if (envName) {
      if (envName[1].startsWith('/caveman')) {
        const envArgs = /<command-args>\s*([^<]*?)\s*<\/command-args>/.exec(prompt);
        const args = envArgs ? envArgs[1].trim() : '';
        prompt = args ? envName[1] + ' ' + args : envName[1];
      } else {
        skipNaturalLanguage = true;
      }
    }

    // /caveman-stats [--share] — run the stats script and inject its output
    // as additionalContext (#618), instructing the model to relay it
    // verbatim. The script reads the active session log, so we pass
    // transcript_path through when Claude Code provides it.
    const statsMatch = /^\/caveman(?::caveman)?-stats(?:\s+(.*))?$/.exec(prompt);
    if (statsMatch) {
      const tailArgs = (statsMatch[1] || '').trim().split(/\s+/).filter(Boolean);
      let block;
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
        block = execFileSync(process.execPath, argv, { encoding: 'utf8', timeout: 5000 }).trim();
      } catch (e) {
        block = 'caveman-stats: could not run stats script.\nTry manually: node hooks/caveman-stats.js';
      }
      process.stdout.write(JSON.stringify({
        hookSpecificOutput: {
          hookEventName: "UserPromptSubmit",
          additionalContext: 'Print this stats block verbatim inside a fenced code block. Say nothing else.\n\n' + block
        }
      }));
      return;
    }

    // Shared mode-change parser (#602) — single source of truth with the
    // opencode plugin for slash commands, namespaced /caveman:caveman-*,
    // natural-language activation/deactivation, and brevity triggers.
    const change = parseModeChange(prompt, { getDefaultMode, skipNaturalLanguage });

    // Independent one-shot modes remember the prose mode active before them
    // so the next ordinary prompt restores it (#599) — SKILL.md promises
    // "Level persist until changed or session end", and a one-shot skill
    // invocation should not count as "changed" forever.
    let setIndependentThisTurn = false;
    if (change && change.action === 'set') {
      const mode = change.mode;
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
      recordModeChange(claudeDir, mode); // #601: timestamped transition log
      safeWriteFlag(flagPath, mode);
    } else if (change && change.action === 'clear') {
      recordModeChange(claudeDir, null); // #601
      try { fs.unlinkSync(flagPath); } catch (e) {}
      try { fs.unlinkSync(prevPath); } catch (e) {}
    }

    // Per-turn reinforcement: emit a short reminder when caveman is active.
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

    // #634: a repo-local .caveman.json / .caveman/config.json can set
    // defaultMode "off" to opt a project out of caveman entirely. Thread the
    // hook stdin's cwd through so that check resolves for the session's
    // directory, not this hook process's own cwd. This gates ONLY the
    // reinforcement output below — it never deletes or writes the flag file.
    if (activeMode && !INDEPENDENT_MODES.has(activeMode) && getDefaultMode(data.cwd) !== 'off') {
      process.stdout.write(JSON.stringify({
        hookSpecificOutput: {
          hookEventName: "UserPromptSubmit",
          additionalContext: `CAVEMAN MODE ACTIVE (${activeMode}) — session ruleset applies.`
        }
      }));
    }
  } catch (e) {
    // Silent fail
  }
});
