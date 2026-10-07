'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');

// Filename only. The script itself lives in src/hooks and is copied at install
// time. This module must not require it: detached installs ship bin/ alone.
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
  const readCommand = `node "./hooks/${HOOK_SCRIPT_NAME}" read`;
  const shellCommand = `node "./hooks/${HOOK_SCRIPT_NAME}" shell`;
  return {
    preToolUse: [{ command: readCommand, matcher: 'Read' }],
    beforeShellExecution: [{ command: shellCommand }],
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

function installCursorHooks({ repoRoot, home = os.homedir(), dryRun = false, note = () => {} }) {
  if (!repoRoot) throw new Error('cursor hook install requires the caveman package root');
  const source = path.join(repoRoot, 'src', 'hooks', HOOK_SCRIPT_NAME);
  if (!fs.existsSync(source)) throw new Error(`missing hook script: ${source}`);

  const dest = hookScriptPath(home);
  const manifest = hooksJsonPath(home);

  if (dryRun) {
    note(`  would copy ${source} → ${dest}`);
    note(`  would merge caveman Cursor hook entries into ${manifest}`);
    return;
  }

  fs.mkdirSync(path.dirname(dest), { recursive: true });
  fs.copyFileSync(source, dest);
  try { fs.chmodSync(dest, 0o755); } catch (_) {}

  let doc = { version: 1, hooks: {} };
  if (fs.existsSync(manifest)) {
    try {
      doc = JSON.parse(fs.readFileSync(manifest, 'utf8'));
    } catch (error) {
      throw new Error(`invalid ${manifest}: ${error.message}`);
    }
  }
  const merged = mergeHooksDocument(doc);
  fs.writeFileSync(manifest, `${JSON.stringify(merged, null, 2)}\n`, { mode: 0o600 });
  note(`  installed Cursor dedupe hook (${dest})`);
}

function uninstallCursorHooks({ home = os.homedir(), dryRun = false, note = () => {} }) {
  const dest = hookScriptPath(home);
  const manifest = hooksJsonPath(home);
  let changed = false;

  if (fs.existsSync(dest)) {
    if (dryRun) note(`  would remove ${dest}`);
    else {
      try { fs.unlinkSync(dest); } catch (_) {}
      note(`  removed ${dest}`);
    }
    changed = true;
  }

  if (!fs.existsSync(manifest)) return { changed };
  let doc;
  try {
    doc = JSON.parse(fs.readFileSync(manifest, 'utf8'));
  } catch {
    return { changed };
  }
  const stripped = stripCavemanHooks(doc);
  if (stripped.changed) {
    if (!dryRun) fs.writeFileSync(manifest, `${JSON.stringify(stripped.doc, null, 2)}\n`, { mode: 0o600 });
    note(`  pruned caveman Cursor hook entries from ${manifest}`);
    changed = true;
  }
  return { changed };
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
