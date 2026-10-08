'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');

// Pilot: install/uninstall only print the plan. No copy and no hooks.json write yet.
const HOOK_SCRIPT_NAME = 'cursor-dedupe-tools.js';

function cursorDir(home = os.homedir()) {
  return path.join(home, '.cursor');
}

function hooksJsonPath(home = os.homedir()) {
  return path.join(cursorDir(home), 'hooks.json');
}

function hookScriptPath(home = os.homedir()) {
  return path.join(cursorDir(home), 'hooks', HOOK_SCRIPT_NAME);
}

function isCavemanHookEntry(entry) {
  const command = entry && entry.command;
  return typeof command === 'string' && command.includes(HOOK_SCRIPT_NAME);
}

function cavemanHookEntries() {
  const hook = (mode) => `node "./hooks/${HOOK_SCRIPT_NAME}" ${mode}`;
  return {
    preToolUse: [
      { command: hook('read'), matcher: 'Read' },
      { command: hook('grep'), matcher: 'Grep' },
      { command: hook('glob'), matcher: 'Glob' },
    ],
    beforeShellExecution: [{ command: hook('shell') }],
  };
}

function mergeHooksDocument(doc) {
  const next = doc && typeof doc === 'object' ? { ...doc } : {};
  if (next.version === undefined) next.version = 1;
  const hooks = next.hooks && typeof next.hooks === 'object' ? { ...next.hooks } : {};
  for (const [event, entries] of Object.entries(cavemanHookEntries())) {
    const kept = Array.isArray(hooks[event]) ? hooks[event].filter((e) => !isCavemanHookEntry(e)) : [];
    hooks[event] = [...kept, ...entries];
  }
  next.hooks = hooks;
  return next;
}

function stripCavemanHooks(doc) {
  if (!doc || typeof doc !== 'object' || !doc.hooks || typeof doc.hooks !== 'object') {
    return { changed: false, doc: doc || { version: 1, hooks: {} } };
  }
  const hooks = { ...doc.hooks };
  let changed = false;
  for (const event of Object.keys(hooks)) {
    if (!Array.isArray(hooks[event])) continue;
    const filtered = hooks[event].filter((e) => !isCavemanHookEntry(e));
    if (filtered.length !== hooks[event].length) {
      changed = true;
      if (filtered.length === 0) delete hooks[event];
      else hooks[event] = filtered;
    }
  }
  return { changed, doc: { ...doc, hooks } };
}

function installCursorHooks({ home = os.homedir(), note = () => {}, dryRun = false }) {
  const dest = hookScriptPath(home);
  const manifest = hooksJsonPath(home);
  note(`  would copy src/hooks/${HOOK_SCRIPT_NAME} → ${dest}`);
  note(`  would merge caveman Cursor dedupe hook entries into ${manifest}`);
  // Pilot: dry-run and live install both stop at the plan. No copy, no write.
  void dryRun;
}

function uninstallCursorHooks({ home = os.homedir(), note = () => {}, dryRun = false }) {
  const dest = hookScriptPath(home);
  const manifest = hooksJsonPath(home);
  // A home that never got this hook must stay silent. Codex uninstall asserts
  // that an unparseable hooks.json caveman did not write is not mentioned.
  if (fs.existsSync(dest)) note(`  would remove ${dest}`);
  if (fs.existsSync(manifest)) note(`  would prune caveman Cursor dedupe hook entries from ${manifest}`);
  void dryRun;
  return { changed: false };
}

module.exports = {
  cavemanHookEntries,
  cursorDir,
  hookScriptPath,
  hooksJsonPath,
  installCursorHooks,
  isCavemanHookEntry,
  mergeHooksDocument,
  stripCavemanHooks,
  uninstallCursorHooks,
};
