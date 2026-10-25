#!/usr/bin/env node
// caveman — sessionStart hook for hosts other than Claude Code.
//
//   node caveman-host-session-start.js <host>
//
// One script, one output key per host:
//   cursor  → {"additional_context": "..."}  Cursor plugin + ~/.cursor/hooks.json
//   copilot → {"additionalContext": "..."}   GitHub Copilot CLI ~/.copilot/hooks/
//
// Mode is the configured default (CAVEMAN_DEFAULT_MODE → repo .caveman.json →
// user config), resolved from the session's workspace, not this process's cwd.
// off, manual, a one-shot mode, an unknown host or any failure prints {}, which
// both hosts read as "no action". Unlike caveman-activate.js it writes no
// state: these hosts have no statusline or prompt hook that would read it.
//
// Stdin: returns on the first COMPLETE JSON object, never at EOF, with a
// watchdog — the same contour as caveman-activate.js (#729/#833).

'use strict';

const fs = require('fs');
const path = require('path');

const OUTPUT_KEYS = { cursor: 'additional_context', copilot: 'additionalContext' };
const RULESET_MODES = ['caveman', 'ultracave', 'megacave'];
const PAYLOAD_WATCHDOG_MS = 2000;

function sessionContext(payload) {
  const { getDefaultMode, loadRuleset, rulesetBanner } = require('./caveman-config');
  let data = {};
  try { data = JSON.parse(payload) || {}; } catch (e) { /* no/bad payload */ }
  // Copilot sends cwd; Cursor sends workspace_roots and exports CURSOR_PROJECT_DIR.
  const roots = Array.isArray(data.workspace_roots) ? data.workspace_roots : [];
  const cwd = [data.cwd, roots[0], process.env.CURSOR_PROJECT_DIR]
    .find((value) => typeof value === 'string' && value) || process.cwd();
  const mode = getDefaultMode(cwd);
  if (!RULESET_MODES.includes(mode)) return null;
  // The owned install (<host>/caveman/hooks/) ships its pinned skills one level
  // up; the shared resolver would try <host>/skills/ first, which is the
  // host's unpinned `npx skills add` copy. A plugin layout has no ../skills.
  let ruleset = null;
  try {
    ruleset = fs.readFileSync(path.join(__dirname, '..', 'skills', mode, 'SKILL.md'), 'utf8')
      .replace(/^---[\s\S]*?---\s*/, '');
  } catch (e) { ruleset = loadRuleset(mode, __dirname); }
  if (!ruleset) return null;
  // The banner already names the mode. A separate `Caveman mode: <mode>` line
  // got echoed at the top of ordinary answers in Copilot CLI live runs, so
  // that line stays reserved for an explicit `/caveman status` reply.
  return rulesetBanner(mode) + '\n\n' + ruleset.trimEnd();
}

function respond(payload) {
  let out = {};
  try {
    const key = OUTPUT_KEYS[process.argv[2]];
    const text = key && sessionContext(payload);
    if (text) out = { [key]: text };
  } catch (e) { /* fail open: never block a session */ }
  process.stdout.write(JSON.stringify(out));
}

if (process.stdin.isTTY) {
  respond('');
} else {
  let input = '';
  let done = false;
  const finish = () => {
    if (done) return;
    done = true;
    clearTimeout(watchdog);
    // pause() alone keeps the handle referenced; unref() lets us exit while
    // the host still holds the write end open.
    try { process.stdin.pause(); } catch (e) {}
    try { process.stdin.unref(); } catch (e) {}
    respond(input);
  };
  const watchdog = setTimeout(finish, PAYLOAD_WATCHDOG_MS);
  process.stdin.setEncoding('utf8');
  process.stdin.on('data', (chunk) => {
    input += chunk;
    try { JSON.parse(input); } catch (e) { return; }
    finish();
  });
  process.stdin.on('error', finish);
  process.stdin.on('end', finish);
}
