import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const require = createRequire(import.meta.url);
const DEDUPE = require('../../installer/lib/cursor-dedupe-hooks.js');
const CURSOR_NATIVE = require('../../installer/lib/cursor-native.js');
const HOST_HOOKS = require('../../installer/lib/host-hooks.js');

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');

function freshHome() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'caveman-cursor-home-'));
}

test('merge keeps unrelated hooks and adds caveman dedupe entries', () => {
  const root = path.join(os.tmpdir(), 'cursor-merge-doc');
  const doc = {
    version: 1,
    hooks: {
      beforeShellExecution: [{ command: './hooks/user.sh' }],
      preToolUse: [{ command: './hooks/other.js', matcher: 'Write' }],
    },
  };
  const merged = DEDUPE.mergeDedupeHooksDocument(doc, root, process.execPath);
  assert.equal(merged.hooks.beforeShellExecution.length, 2);
  assert.equal(merged.hooks.preToolUse.length, 4);
  const cavemanPre = merged.hooks.preToolUse.filter((e) => DEDUPE.isDedupeHookEntry(e, root));
  assert.equal(cavemanPre.length, 3);
  assert.ok(cavemanPre.some((e) => e.matcher === 'Read'));
  assert.ok(cavemanPre.some((e) => e.matcher === 'Grep'));
  assert.ok(cavemanPre.some((e) => e.matcher === 'Glob'));
  assert.ok(merged.hooks.beforeShellExecution.some((e) => e.command === './hooks/user.sh'));
});

test('merge replaces older caveman Read entry without duplicating', () => {
  const root = path.join(os.tmpdir(), 'cursor-merge-replace');
  const doc = {
    version: 1,
    hooks: {
      preToolUse: [{ command: 'node "./hooks/cursor-dedupe-tools.js" read', matcher: 'Read' }],
    },
  };
  const merged = DEDUPE.mergeDedupeHooksDocument(doc, root, process.execPath);
  const cavemanPre = merged.hooks.preToolUse.filter((e) => DEDUPE.isDedupeHookEntry(e, root));
  assert.equal(cavemanPre.length, 3);
});

test('strip removes only caveman dedupe hook entries', () => {
  const root = path.join(os.tmpdir(), 'cursor-strip');
  const merged = DEDUPE.mergeDedupeHooksDocument({
    version: 1,
    hooks: { beforeShellExecution: [{ command: './hooks/user.sh' }] },
  }, root, process.execPath);
  const { changed, doc } = DEDUPE.stripDedupeHooks(merged, root);
  assert.equal(changed, true);
  assert.equal(doc.hooks.preToolUse, undefined);
  assert.deepEqual(doc.hooks.beforeShellExecution, [{ command: './hooks/user.sh' }]);
});

test('installCursorNative copies dedupe script and merges hooks.json', () => {
  const home = freshHome();
  try {
    const notes = [];
    CURSOR_NATIVE.installCursorNative({
      repoRoot: REPO_ROOT,
      home,
      node: process.execPath,
      withHooks: true,
      force: true,
      note: (line) => notes.push(line),
    });
    const dedupePath = path.join(home, '.cursor', 'caveman', 'hooks', 'cursor-dedupe-tools.js');
    assert.ok(fs.existsSync(dedupePath));
    const manifest = JSON.parse(fs.readFileSync(CURSOR_NATIVE.hooksJsonPath(path.join(home, '.cursor')), 'utf8'));
    assert.ok(manifest.hooks.sessionStart?.length >= 1);
    assert.equal(manifest.hooks.preToolUse.filter((e) => DEDUPE.isDedupeHookEntry(e, path.join(home, '.cursor'))).length, 3);
    assert.equal(manifest.hooks.beforeShellExecution.filter((e) => DEDUPE.isDedupeHookEntry(e, path.join(home, '.cursor'))).length, 1);
    assert.ok(notes.some((line) => line.includes('dedupe')));
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

test('uninstallCursorNative removes dedupe hooks and payload', () => {
  const home = freshHome();
  const root = path.join(home, '.cursor');
  try {
    CURSOR_NATIVE.installCursorNative({
      repoRoot: REPO_ROOT,
      home,
      node: process.execPath,
      withHooks: true,
      force: true,
      note: () => {},
    });
    CURSOR_NATIVE.uninstallCursorNative({ home, note: () => {}, warn: () => {} });
    assert.equal(fs.existsSync(path.join(root, HOST_HOOKS.PAYLOAD_DIR)), false);
    assert.equal(fs.existsSync(CURSOR_NATIVE.hooksJsonPath(root)), false);
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});

test('install dry-run writes nothing', () => {
  const home = freshHome();
  try {
    const notes = [];
    CURSOR_NATIVE.installCursorNative({
      repoRoot: REPO_ROOT,
      home,
      dryRun: true,
      withHooks: true,
      note: (line) => notes.push(line),
    });
    assert.ok(notes.some((line) => line.includes('dedupe')));
    assert.equal(fs.existsSync(path.join(home, '.cursor')), false);
  } finally {
    fs.rmSync(home, { recursive: true, force: true });
  }
});
