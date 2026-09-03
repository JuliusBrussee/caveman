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
const cavemanConfig = requireSibling('caveman-config', (m) =>
  m && typeof m.getDefaultMode === 'function' && typeof m.safeWriteFlag === 'function'
    && typeof m.readFlag === 'function' && typeof m.recordModeChange === 'function'
    && Array.isArray(m.VALID_MODES));
const { getDefaultMode, safeWriteFlag, readFlag, recordModeChange, VALID_MODES } = cavemanConfig || {
  getDefaultMode: () => 'full',
  safeWriteFlag: () => {},
  readFlag: () => null,
  recordModeChange: () => {},
  VALID_MODES: [],
};

// Per-session helpers, resolved individually rather than folded into the shape
// check above: a caveman-config.js from before per-session state satisfies that
// check, and failing the whole module over the newer exports would turn "mode
// still tracked, machine-wide" into "hook is a no-op". Each stub reproduces the
// pre-per-session behavior against the legacy flag instead.
const cfg = cavemanConfig || {};
const validateSessionId = cfg.validateSessionId || (() => null);
const resolveActiveMode = cfg.resolveActiveMode || (() => {
  const m = readFlag(flagPath);
  return (!m || m === 'off') ? null : m;
});
const writeSessionMode = cfg.writeSessionMode || ((dir, sid, modeOrNull) => {
  if (!modeOrNull || modeOrNull === 'off') removeFlag(flagPath);
  else safeWriteFlag(flagPath, modeOrNull);
});
const writeSessionPrev = cfg.writeSessionPrev || ((dir, sid, mode) => safeWriteFlag(prevPath, mode));
const readSessionPrev = cfg.readSessionPrev || (() => readFlag(prevPath));
const clearSessionPrev = cfg.clearSessionPrev || (() => removeFlag(prevPath));
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

const REINFORCEMENT_RULES = {
  lite: 'No filler, hedging, or pleasantries. Keep articles and full sentences OK, but stay tight.',
  full: 'Drop articles (a/an/the), filler, pleasantries, and hedging. Prefer fragments over full natural-prose sentences. No preamble or recap.',
  ultra: 'Drop articles, filler, pleasantries, hedging, and excess conjunctions. Prefer fragments over full natural-prose sentences. State each fact once. No preamble or recap.',
  'wenyan-lite': 'Use wenyan-lite: semi-classical terse register. Drop filler and hedging. Keep meaning exact.',
  'wenyan-full': 'Use wenyan-full: maximum classical terseness. Drop filler and hedging. Keep meaning exact.',
  'wenyan-ultra': 'Use wenyan-ultra: extreme classical terseness. Drop filler and hedging. Keep meaning exact.',
};

function reinforcementForMode(mode) {
  const canonical = mode === 'wenyan' ? 'wenyan-full' : mode;
  const rules = REINFORCEMENT_RULES[canonical] || REINFORCEMENT_RULES.full;
  return 'CAVEMAN MODE ACTIVE (' + mode + '). Enforce this reply: ' + rules +
    ' Technical terms, code, commands, paths, and errors stay exact.';
}

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
let handled = false;

// Act on the first COMPLETE JSON payload rather than waiting for EOF. The host
// writes one object and closes, but under the Windows pipe implementation that
// close can lag arbitrarily (#729/#833) — and this hook is registered with a 5s
// budget, so a lagging EOF spends the whole budget and the host kills us before
// the flag is ever written. Parsing per chunk costs one JSON.parse of a payload
// we are about to parse anyway.
function handle(raw) {
  if (handled) return;
  handled = true;
  try {
    const data = JSON.parse(raw);

    // Scopes every read and write below to this session. null when absent or
    // malformed, in which case the helpers above fall back to the legacy
    // machine-wide flag — i.e. exactly the pre-per-session behavior.
    const sessionId = validateSessionId(data.session_id);

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
        // Lets stats drop mode-log rows belonging to other windows instead of
        // joining them onto this session's timeline.
        if (sessionId) argv.push('--session-id', sessionId);
        if (tailArgs.includes('--share')) argv.push('--share');
        if (tailArgs.includes('--all')) argv.push('--all');
        const sinceIdx = tailArgs.indexOf('--since');
        if (sinceIdx !== -1 && tailArgs[sinceIdx + 1]) {
          argv.push('--since', tailArgs[sinceIdx + 1]);
        }
        // 2.5s. Hook registration allows 30s for slow Windows process startup,
        // while this child watchdog still bounds optional context loading.
        // already spent its own Node startup; giving the child the host's
        // entire budget means the host kills the hook before the child's own
        // timeout can fire and produce the fallback message. Windows process
        // spawn is ~10x macOS before antivirus (#819), so the margin is real.
        block = execFileSync(process.execPath, argv, { encoding: 'utf8', timeout: 2500 }).trim();
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
        const current = resolveActiveMode(claudeDir, sessionId);
        if (current && !INDEPENDENT_MODES.has(current)) {
          writeSessionPrev(claudeDir, sessionId, current);
        }
        setIndependentThisTurn = true;
      }
      recordModeChange(claudeDir, mode, sessionId); // #601: timestamped transition log
      writeSessionMode(claudeDir, sessionId, mode);
    } else if (change && change.action === 'clear') {
      // Durable off: writeSessionMode stores the literal 'off' for this session
      // (and unlinks the legacy mirror), so the next SessionStart cannot mistake
      // deactivation for "never set" and re-arm caveman on the next compaction.
      recordModeChange(claudeDir, null, sessionId); // #601
      writeSessionMode(claudeDir, sessionId, null);
      clearSessionPrev(claudeDir, sessionId);
    }

    // Per-turn reinforcement: emit a short reminder when caveman is active.
    // The SessionStart hook injects the full ruleset once, but models lose it
    // when other plugins inject competing style instructions every turn.
    // This keeps caveman visible in the model's attention on every user message.
    //
    // Skip independent modes (commit, review, compress) — they have their own
    // skill behavior and the base caveman rules would conflict.
    // resolveActiveMode enforces symlink-safe read + size cap + VALID_MODES
    // whitelist, and treats both a missing file and a durable 'off' as "no
    // mode". If the state is missing, corrupted, oversized, or a symlink
    // pointing at something like ~/.ssh/id_rsa, it returns null and we emit
    // nothing — never inject untrusted bytes into model context.
    let activeMode = resolveActiveMode(claudeDir, sessionId);

    // One-shot restore (#599): an independent mode set on a PREVIOUS prompt
    // has served its turn — bring back the prose mode that was active before
    // it, or deactivate if caveman wasn't active then.
    if (activeMode && INDEPENDENT_MODES.has(activeMode) && !setIndependentThisTurn) {
      const prev = readSessionPrev(claudeDir, sessionId);
      clearSessionPrev(claudeDir, sessionId);
      // `prev !== 'off'` is not redundant: prev is stored literally, and
      // restoring a stored 'off' as a mode would inject "CAVEMAN MODE ACTIVE
      // (off)" for a session that had deliberately turned caveman off.
      if (prev && !INDEPENDENT_MODES.has(prev) && prev !== 'off') {
        recordModeChange(claudeDir, prev, sessionId); // #601
        writeSessionMode(claudeDir, sessionId, prev);
        activeMode = prev;
      } else {
        recordModeChange(claudeDir, null, sessionId); // #601
        writeSessionMode(claudeDir, sessionId, null);
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
      ? reinforcementForMode(activeMode)
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
}

// StringDecoder semantics: a multi-byte character split across two chunks is
// held until it is complete, instead of each half being coerced to a lone
// replacement char by `'' + buffer`. Matters more now that the payload is
// parsed per chunk rather than once at EOF.
process.stdin.setEncoding('utf8');
process.stdin.on('data', chunk => {
  input += chunk;
  // A partial payload throws here and we simply wait for more bytes.
  try { JSON.parse(input); } catch (e) { return; }
  handle(input);
  // pause() stops the flow but the 'data' listener has REFERENCED the stdin
  // handle, so the event loop stays alive until the host closes the write end.
  // On Windows that close lags arbitrarily (#729/#833), so the hook sat idle
  // with its work already done until the 5s budget expired and the host killed
  // it — the shape of #819 (timeouts on turns that measure ~56ms of real work).
  // unref() drops the handle from the loop without closing the fd, so we exit
  // as soon as stdout has flushed.
  process.stdin.pause();
  try { process.stdin.unref(); } catch (e) {}
});
// Abnormal stdin close (broken pipe, parent crash) emits 'error'; without a
// listener Node throws it as an uncaught exception and the hook exits
// non-zero — a spurious hook failure (#538). Hooks must always exit 0.
process.stdin.on('error', () => process.exit(0));
// Same failure, output side (#397): the harness can close its end of our
// stdout/stderr after this hook has already written a payload, and an
// unlistened 'error' there throws just as loudly.
for (const stream of [process.stdout, process.stderr]) {
  stream.on('error', () => process.exit(0));
}
process.stdin.on('end', () => handle(input));
