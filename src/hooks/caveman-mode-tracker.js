#!/usr/bin/env node
// caveman — UserPromptSubmit hook to track which caveman mode is active
// Inspects user input for /caveman commands and writes mode to flag file

const fs = require('fs');
const path = require('path');
const os = require('os');
const { execFileSync } = require('child_process');
// caveman-config.js and caveman-parse.js are mandatory siblings, but an
// incomplete install leaves one absent. A bare top-level require turns that
// into an uncaught MODULE_NOT_FOUND on EVERY prompt, which the harness
// surfaces only as an opaque loader stack trace (#848). Resolve defensively.
//
// Deliberately inlined rather than extracted into a shared helper: a shared
// loader would itself be one more sibling that can go missing, which is the
// exact failure this guards against.
function requireSibling(name, isUsable) {
  let mod;
  try {
    mod = require('./' + name);
  } catch (primary) {
    // The opencode install layout renames siblings to `.cjs` (its plugin dir
    // is "type": "module"), same fallback caveman-parse.js already does. Gate
    // the retry on the error naming THIS module: a MODULE_NOT_FOUND thrown by
    // a require *inside* a sibling that loaded fine must not be re-reported as
    // "./<name>.cjs is missing", blaming a file never meant to exist.
    const message = String((primary && primary.message) || primary);
    if (primary && primary.code === 'MODULE_NOT_FOUND' && message.includes("'./" + name + "'")) {
      try { return require('./' + name + '.cjs'); } catch (e) { /* report primary */ }
    }
    const absent = !fs.existsSync(path.join(__dirname, name + '.js'))
                && !fs.existsSync(path.join(__dirname, name + '.cjs'));
    // Distinguish "the sibling is absent" from "the sibling loaded but its own
    // require failed" — naming the wrong cause is worse than no message. Only
    // the first line of error.message: Node appends a multi-line "Require
    // stack:" block, the very noise this guard exists to remove.
    process.stderr.write('caveman: ' + (absent
      ? name + '.js is missing from ' + __dirname + ' — the install is incomplete.'
      : name + ' could not load — ' + message.split('\n')[0]) + '\n'
      + 'Run `/plugin update caveman`, or rerun install.sh for standalone hooks. '
      + 'Continuing with reduced functionality.\n');
    return null;
  }
  // A module that LOADS but exports the wrong shape is the plugin-cache-drift
  // case #848 describes. Without this check the first use dereferences
  // undefined — the raw stack trace this guard exists to remove.
  if (!isUsable(mod)) {
    process.stderr.write('caveman: ' + name + ' loaded but is missing expected exports — the install is inconsistent.\n'
      + 'Run `/plugin update caveman`, or rerun install.sh for standalone hooks. '
      + 'Continuing with reduced functionality.\n');
    return null;
  }
  return mod;
}

// Degraded stubs make this hook a clean no-op when a sibling is unusable: no
// mode change is parsed, readFlag reports nothing active, so nothing is emitted
// and the process still exits 0 with stdin drained (never a broken pipe, #397).
// getDefaultMode's stub value is never consulted in that state — the only call
// site is gated behind an activeMode that readFlag can no longer produce.
const { getDefaultMode, safeWriteFlag, readFlag, recordModeChange, VALID_MODES } = requireSibling('caveman-config', (m) =>
  m && typeof m.getDefaultMode === 'function' && typeof m.safeWriteFlag === 'function'
    && typeof m.readFlag === 'function' && typeof m.recordModeChange === 'function'
    && Array.isArray(m.VALID_MODES)) || {
  getDefaultMode: () => 'full',
  safeWriteFlag: () => {},
  readFlag: () => null,
  recordModeChange: () => {},
  VALID_MODES: [],
};
const { parseModeChange, INDEPENDENT_MODES } = requireSibling('caveman-parse', (m) =>
  m && typeof m.parseModeChange === 'function' && m.INDEPENDENT_MODES instanceof Set) || {
  parseModeChange: () => null,
  INDEPENDENT_MODES: new Set(['commit', 'review', 'compress']),
};

const claudeDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), '.claude');
const flagPath = path.join(claudeDir, '.caveman-active');
// Remembers the prose mode active before a one-shot independent mode
// (/caveman-commit etc.) so the next ordinary prompt can restore it (#599).
const prevPath = path.join(claudeDir, '.caveman-active.prev');

function removeFlag(path) {
  try {
    fs.unlinkSync(path);
  } catch (error) {
    if (process.env.CAVEMAN_DEBUG === '1' && error.code !== 'ENOENT') {
      console.error(`caveman: failed to remove flag ${path}: ${error.message}`);
    }
  }
}

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

    // A /caveman argument that resolves to no mode used to leave the level
    // untouched and say nothing, so a typo or punctuation glued to the level
    // ("/caveman ultra;") looked like it worked. Build a notice instead — but
    // do NOT return here: an early exit would skip the #599 one-shot restore
    // below, stranding the user in /caveman-commit for an extra turn because
    // they made a typo, and would also drop that turn's reinforcement.
    let notice = null;
    if (change && change.action === 'unresolved') {
      if (change.independentMode) {
        // A real mode, just not reachable via /caveman <arg>. Denying it exists
        // would contradict the docs.
        notice = 'Tell the user ' + change.independentMode + ' mode is set with its own command, '
          + '/caveman-' + change.independentMode + ', not /caveman ' + change.independentMode
          + '. The level is unchanged.';
      } else {
        // Levels are derived from VALID_MODES so they cannot drift from the
        // parser, minus 'off', the independent modes, and 'wenyan' — that is
        // the storage alias for wenyan-full, and listing both would advertise
        // seven levels for a product documented as having six. The rejected
        // argument is never echoed: it is untrusted input headed for model
        // context.
        const levels = VALID_MODES.filter(m => m !== 'off' && m !== 'wenyan' && !INDEPENDENT_MODES.has(m));
        notice = 'Tell the user their /caveman level was not recognized and the level is '
          + 'unchanged. Valid levels: ' + levels.join(', ') + '. Use /caveman off to deactivate.';
      }
    }

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
      removeFlag(flagPath);
      removeFlag(prevPath);
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
      removeFlag(prevPath);
      if (prev && !INDEPENDENT_MODES.has(prev)) {
        recordModeChange(claudeDir, prev); // #601
        safeWriteFlag(flagPath, prev);
        activeMode = prev;
      } else {
        recordModeChange(claudeDir, null); // #601
        removeFlag(flagPath);
        activeMode = null;
      }
    }

    // #634: a repo-local .caveman.json / .caveman/config.json can set
    // defaultMode "off" to opt a project out of caveman entirely. Thread the
    // hook stdin's cwd through so that check resolves for the session's
    // directory, not this hook process's own cwd. This gates ONLY the
    // reinforcement output below — it never deletes or writes the flag file.
    const reinforce = activeMode && !INDEPENDENT_MODES.has(activeMode)
      && getDefaultMode(data.cwd) !== 'off'
      ? `CAVEMAN MODE ACTIVE (${activeMode}) — session ruleset applies.`
      : null;

    // One write, so an unresolved-level notice and the per-turn reinforcement
    // can both land on the same turn. Only one hookSpecificOutput per hook run
    // is read, so emitting them separately would drop whichever came second.
    const context = [notice, reinforce].filter(Boolean).join('\n\n');
    if (context) {
      process.stdout.write(JSON.stringify({
        hookSpecificOutput: {
          hookEventName: "UserPromptSubmit",
          additionalContext: context
        }
      }));
    }
  } catch (e) {
    // Silent fail
  }
});
