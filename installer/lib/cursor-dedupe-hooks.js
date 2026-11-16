'use strict';

const path = require('path');
const HOST_HOOKS = require('./host-hooks');

const DEDUPE_SCRIPT = 'cursor-dedupe-tools.js';

function dedupeScriptPath(root) {
  return path.join(root, HOST_HOOKS.PAYLOAD_DIR, 'hooks', DEDUPE_SCRIPT);
}

function dedupeHookCommand(root, mode, node, platform = process.platform) {
  const script = dedupeScriptPath(root);
  if (platform === 'win32') return `node "${script}" ${mode}`;
  const sq = (value) => `'${String(value).replace(/'/g, `'\\''`)}'`;
  return `${sq(node)} ${sq(script)} ${mode}`;
}

function isDedupeHookEntry(entry, root) {
  const command = entry && entry.command;
  if (typeof command !== 'string') return false;
  return command.includes(DEDUPE_SCRIPT) || command.includes(dedupeScriptPath(root));
}

function cavemanDedupeHookEntries(root, node, platform = process.platform) {
  const hook = (mode) => dedupeHookCommand(root, mode, node, platform);
  return {
    preToolUse: [
      { command: hook('read'), matcher: 'Read' },
      { command: hook('grep'), matcher: 'Grep' },
      { command: hook('glob'), matcher: 'Glob' },
    ],
    beforeShellExecution: [{ command: hook('shell') }],
  };
}

function mergeDedupeHooksDocument(doc, root, node, platform = process.platform) {
  const next = doc && typeof doc === 'object' ? { ...doc } : {};
  if (next.version === undefined) next.version = 1;
  const hooks = next.hooks && typeof next.hooks === 'object' ? { ...next.hooks } : {};
  for (const [event, entries] of Object.entries(cavemanDedupeHookEntries(root, node, platform))) {
    const kept = Array.isArray(hooks[event]) ? hooks[event].filter((e) => !isDedupeHookEntry(e, root)) : [];
    hooks[event] = [...kept, ...entries];
  }
  next.hooks = hooks;
  return next;
}

function stripDedupeHooks(doc, root) {
  if (!doc || typeof doc !== 'object' || !doc.hooks || typeof doc.hooks !== 'object') {
    return { changed: false, doc: doc || { version: 1, hooks: {} } };
  }
  const hooks = { ...doc.hooks };
  let changed = false;
  for (const event of Object.keys(hooks)) {
    if (!Array.isArray(hooks[event])) continue;
    const filtered = hooks[event].filter((e) => !isDedupeHookEntry(e, root));
    if (filtered.length !== hooks[event].length) {
      changed = true;
      if (filtered.length === 0) delete hooks[event];
      else hooks[event] = filtered;
    }
  }
  return { changed, doc: { ...doc, hooks } };
}

module.exports = {
  DEDUPE_SCRIPT,
  dedupeScriptPath,
  dedupeHookCommand,
  isDedupeHookEntry,
  cavemanDedupeHookEntries,
  mergeDedupeHooksDocument,
  stripDedupeHooks,
};
